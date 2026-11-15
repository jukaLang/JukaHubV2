#!/usr/bin/env python3
"""Make a JukaHub OS SD-card image for the TrimUI Smart Pro (TG5040).

plastron has no Smart Pro board, and TrimUI has not published the Smart Pro's
bootloader or kernel sources. So the image boots the device's OWN bootloader,
kernel, drivers and system libraries, saved from its internal storage by
dump-board.sh, and replaces only the startup and the menu with JukaHub:

  * the boot area (partition table, boot0, the boot package with U-Boot and
    the device tree) and every partition in front of the system are copied to
    the same sectors and partition numbers on the card;
  * the system is rebuilt from the saved files as a read-only image, with
    JukaHub's own process 1 (init) and launcher added;
  * U-Boot's environment on the card points the kernel at that image by
    PARTUUID, read-only, with init=/jukahub/init. It is only changed after
    checking that the stock environment really produced the stock command
    line, so the bootloader is known to take root= and init= from it;
  * a JUKAHUB exFAT (or FAT32) partition holds JukaHub, games and saves.

Nothing is written to the device's internal storage, at build time or at
boot: taking the card out boots the stock system.

Requires Linux, root, tar, squashfs-tools (or e2fsprogs), exfatprogs (or
dosfstools) and xz.
"""

from __future__ import annotations

import argparse
from dataclasses import dataclass, replace
import gzip
import hashlib
import json
import lzma
import os
from pathlib import Path
import re
import shlex
import shutil
import stat
import struct
import subprocess
import sys
import tempfile
import uuid
import zlib


HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
SECTOR = 512
STAMP = 0x5F0A6C39
BOOT0_OFFSET = 8 * 1024
PACKAGE_OFFSET = 16400 * 1024
TOC1_MAGIC = 0x89119800
DATA_NAME = "JUKAHUB"
DATA_TYPE = uuid.UUID("EBD0A0A2-B9E5-4433-87C0-68B6B72699C7")
INIT_PATH = "/jukahub/init"
CAPABILITY = "smartpro-stock-kernel-v1"
ALIGN = 2048  # 1 MiB in sectors
BOOTLOGO = REPO / "branding" / "bootlogo" / "jukahub-bootlogo-1280x720.bmp"
# The commands /jukahub/init and launch.sh run from the device's own system.
REQUIRED_COMMANDS = ("sh", "mount", "umount", "insmod", "cp", "mv", "grep", "readlink",
                     "sleep", "cat", "mkdir", "kill", "poweroff", "reboot", "sync", "ls", "rm")
# Daemons whose start belongs to the radios, after JukaHub is on screen.
RADIO_DAEMONS = {"wpa_supplicant", "udhcpc"}
RADIO_MODULE = re.compile(
    r"^(cfg80211|mac80211|rfkill.*|.*8189.*|.*8723.*|.*8821.*|.*8822.*|xr\d.*|xradio.*|"
    r"aic.*|bcmdhd|dhd|.*wlan.*|bt_.*|.*bluetooth.*|hci.*|btlinux)$")
# Modules the kernel needs to mount the root itself: with any of them as a
# module the stock system relies on an initramfs, which this image drops.
ROOT_CRITICAL_MODULES = {"sunxi_mmc", "mmc_block", "mmc_core", "squashfs", "ext4", "jbd2"}


class BuildError(Exception):
    pass


def need(condition: bool, message: str) -> None:
    if not condition:
        raise BuildError(message)


def u32(data: bytes, offset: int) -> int:
    return struct.unpack_from("<I", data, offset)[0]


# ------------------------------------------------------------ board folder

def read_kv(path: Path) -> dict[str, str]:
    values = {}
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        key, sep, value = line.partition("=")
        if sep and re.fullmatch(r"[A-Z0-9_]+", key):
            values[key] = value
    return values


def load_board(board: Path) -> dict[str, str]:
    need(board.is_dir(), f"board folder is missing: {board}")
    need((board / "COMPLETE").is_file(),
         f"{board} is incomplete: dump-board.sh did not finish (see FAILED.txt)")
    for name in ("facts.txt", "cmdline.txt", "modules.txt", "filesystems.txt",
                 "mountinfo.txt", "procs.txt", "partitions.txt", "lead.bin", "rootfs.tar"):
        need((board / name).is_file(), f"board folder has no {name}")
    facts = read_kv(board / "facts.txt")
    need(facts.get("FORMAT") == "jukahub-board-v1", "board folder is from another dump format")
    need(facts.get("DEVICE") == "trimui-smart-pro" and facts.get("PANEL") == "1280x720",
         "board folder is not from a TrimUI Smart Pro (TG5040)")
    need(facts.get("MACHINE") == "aarch64", "board folder is not from a 64-bit ARM system")
    need(bool(facts.get("KERNEL")), "board folder does not name its kernel")
    need(re.fullmatch(r"[A-Za-z0-9_.+-]+", facts.get("KERNEL", "")) is not None,
         "board folder names an odd kernel release")
    lead = int(facts.get("LEAD_SECTORS", "0"))
    need((board / "lead.bin").stat().st_size == lead * SECTOR,
         "lead.bin does not match the boot area size the device reported")
    sums = board / "SHA256SUMS"
    if sums.is_file():
        for line in sums.read_text(encoding="utf-8").splitlines():
            digest, _, name = line.partition("  ")
            name = name.strip()
            if not name:
                continue
            path = (board / name).resolve()
            need(board.resolve() in path.parents, f"SHA256SUMS names a file outside the board: {name}")
            need(path.is_file() and sha256(path) == digest.strip(),
                 f"{name} does not match its checksum: copy the board folder again")
    return facts


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while data := stream.read(1 << 20):
            digest.update(data)
    return digest.hexdigest()


# ------------------------------------------------------------- boot area

def word_sum(data: bytes, skip: int) -> int:
    """Allwinner's add-sum: every 32-bit word, the checksum word counted as STAMP."""
    total = 0
    for (word,) in struct.iter_unpack("<I", data):
        total = (total + word) & 0xFFFFFFFF
    stored = u32(data, skip)
    return (total - stored + STAMP) & 0xFFFFFFFF


def check_boot0(lead: bytes) -> int:
    head = lead[BOOT0_OFFSET:BOOT0_OFFSET + 32]
    need(len(head) == 32 and head[4:12] == b"eGON.BT0",
         "no boot0 at 8 KiB of the boot area")
    length = u32(head, 16)
    need(length % 4 == 0 and 32 <= length <= 1 << 20 and BOOT0_OFFSET + length <= len(lead),
         "boot0 has an impossible length")
    body = lead[BOOT0_OFFSET:BOOT0_OFFSET + length]
    need(word_sum(body, 12) == u32(body, 12), "boot0 checksum does not match: the copy is damaged")
    return length


def check_package(lead: bytes) -> dict:
    head = lead[PACKAGE_OFFSET:PACKAGE_OFFSET + 64]
    need(len(head) == 64 and head[:13] == b"sunxi-package" and u32(head, 16) == TOC1_MAGIC,
         "no Allwinner boot package (U-Boot) at 16400 KiB of the boot area")
    valid = u32(head, 36)
    need(valid % 4 == 0 and 64 <= valid <= 32 << 20 and PACKAGE_OFFSET + valid <= len(lead),
         "boot package has an impossible length")
    body = lead[PACKAGE_OFFSET:PACKAGE_OFFSET + valid]
    return {"length": valid, "checksum_ok": word_sum(body, 20) == u32(body, 20),
            "panel": fdt_panel(body)}


def fdt_panel(blob: bytes) -> tuple[int, int] | None:
    """The lcd0 node's lcd_x/lcd_y from the first device tree in blob."""
    start = 0
    while (at := blob.find(b"\xd0\x0d\xfe\xed", start)) >= 0:
        start = at + 4
        if at + 40 > len(blob):
            break
        total, struct_off, strings_off = struct.unpack_from(">III", blob, at + 4)
        if not (64 <= total <= len(blob) - at and struct_off < total and strings_off < total):
            continue
        fdt = blob[at:at + total]
        found = _fdt_lcd(fdt, struct_off, strings_off)
        if found:
            return found
    return None


def _fdt_lcd(fdt: bytes, struct_off: int, strings_off: int) -> tuple[int, int] | None:
    pos, path, props = struct_off, [], {}
    while pos + 4 <= len(fdt):
        token = struct.unpack_from(">I", fdt, pos)[0]
        pos += 4
        if token == 1:
            end = fdt.index(b"\0", pos)
            path.append(fdt[pos:end].decode("ascii", "replace"))
            pos = (end + 4) & ~3
        elif token == 2:
            if path and path[-1].split("@")[0] == "lcd0" and "lcd_x" in props and "lcd_y" in props:
                return props["lcd_x"], props["lcd_y"]
            if path:
                path.pop()
            props = {}
        elif token == 3:
            length, name_off = struct.unpack_from(">II", fdt, pos)
            pos += 8
            name_end = fdt.index(b"\0", strings_off + name_off)
            name = fdt[strings_off + name_off:name_end].decode("ascii", "replace")
            if path and path[-1].split("@")[0] == "lcd0" and length == 4 and name in ("lcd_x", "lcd_y"):
                props[name] = struct.unpack_from(">I", fdt, pos)[0]
            pos = (pos + length + 3) & ~3
        elif token == 4:
            continue
        else:
            break
    return None


# ------------------------------------------------------------------- GPT

@dataclass
class Part:
    index: int
    type_guid: bytes
    unique: bytes
    first: int
    last: int
    attrs: int
    name: str

    @property
    def sectors(self) -> int:
        return self.last - self.first + 1

    @property
    def partuuid(self) -> str:
        return str(uuid.UUID(bytes_le=self.unique))


@dataclass
class Gpt:
    first_usable: int
    entry_lba: int
    count: int
    entry_size: int
    parts: list[Part]


def parse_gpt(disk: bytes) -> Gpt:
    head = disk[SECTOR:2 * SECTOR]
    need(head[:8] == b"EFI PART", "the eMMC has no GPT")
    size = u32(head, 12)
    need(92 <= size <= SECTOR, "GPT header has an odd size")
    check = bytearray(head[:size])
    check[16:20] = b"\0\0\0\0"
    need(zlib.crc32(check) == u32(head, 16), "GPT header checksum does not match")
    first_usable, = struct.unpack_from("<Q", head, 40)
    entry_lba, count, entry_size, entries_crc = struct.unpack_from("<QIII", head, 72)
    need(0 < count <= 256 and 128 <= entry_size <= 1024 and entry_size % 8 == 0,
         "GPT has an odd entry table")
    table = disk[entry_lba * SECTOR:entry_lba * SECTOR + count * entry_size]
    need(len(table) == count * entry_size, "GPT entry table is outside the boot area")
    need(zlib.crc32(table) == entries_crc, "GPT entry table checksum does not match")
    parts = []
    for i in range(count):
        entry = table[i * entry_size:(i + 1) * entry_size]
        if entry[:16] == bytes(16):
            continue
        first, last, attrs = struct.unpack_from("<QQQ", entry, 32)
        name = entry[56:128].decode("utf-16le", errors="replace").split("\0", 1)[0]
        need(0 < first <= last, f"GPT partition {i + 1} has an impossible extent")
        parts.append(Part(i, entry[:16], entry[16:32], first, last, attrs, name))
    return Gpt(first_usable, entry_lba, count, entry_size, parts)


def gpt_entries(gpt: Gpt, parts: list[Part]) -> bytes:
    table = bytearray(gpt.count * gpt.entry_size)
    for part in parts:
        need(0 <= part.index < gpt.count, "partition index outside the GPT")
        name = part.name.encode("utf-16le")
        need(len(name) <= 72, f"partition name too long: {part.name}")
        at = part.index * gpt.entry_size
        struct.pack_into("<16s16sQQQ", table, at, part.type_guid, part.unique,
                         part.first, part.last, part.attrs)
        table[at + 56:at + 56 + len(name)] = name
    return bytes(table)


def gpt_header(gpt: Gpt, disk_guid: bytes, total: int, entries: bytes, backup: bool) -> bytes:
    entry_sectors = -(-len(entries) // SECTOR)
    my_lba, alt_lba = (total - 1, 1) if backup else (1, total - 1)
    entry_lba = total - 1 - entry_sectors if backup else gpt.entry_lba
    head = bytearray(92)
    struct.pack_into("<8sIIIIQQQQ16sQIII", head, 0, b"EFI PART", 0x00010000, 92, 0, 0,
                     my_lba, alt_lba, gpt.first_usable, total - 2 - entry_sectors,
                     disk_guid, entry_lba, gpt.count, gpt.entry_size, zlib.crc32(entries))
    struct.pack_into("<I", head, 16, zlib.crc32(head))
    return bytes(head) + bytes(SECTOR - 92)


def protective_mbr(old: bytes, total: int) -> bytes:
    mbr = bytearray(old[:SECTOR])
    if not (mbr[510:512] == b"\x55\xaa" and mbr[446 + 4] == 0xEE):
        mbr = bytearray(SECTOR)
        mbr[446:446 + 16] = struct.pack("<B3sB3sII", 0, b"\x00\x02\x00", 0xEE, b"\xff\xff\xff",
                                        1, 0)
        mbr[510:512] = b"\x55\xaa"
    struct.pack_into("<I", mbr, 446 + 12, min(total - 1, 0xFFFFFFFF))
    return bytes(mbr)


# ------------------------------------------------------------ U-Boot env

@dataclass
class Env:
    data_offset: int
    size: int
    pad: int
    names: list[str]
    values: dict[str, str]


ENV_SIZES = (0x20000, 0x10000, 0x8000, 0x4000, 0x2000, 0x40000, 0x80000, 0x100000)


def parse_env(raw: bytes) -> Env:
    for size in ENV_SIZES:
        if size > len(raw):
            continue
        for offset in (4, 5):
            if zlib.crc32(raw[offset:size]) != u32(raw, 0):
                continue
            body = raw[offset:size]
            end = body.find(b"\0\0")
            need(end >= 0, "U-Boot environment is not terminated")
            names, values = [], {}
            for item in body[:end].split(b"\0"):
                key, sep, value = item.decode("latin-1").partition("=")
                need(bool(sep) and key, "U-Boot environment has a malformed entry")
                if key not in values:
                    names.append(key)
                values[key] = value
            pad = body[end + 2] if end + 2 < len(body) else 0
            return Env(offset, size, pad, names, values)
    raise BuildError("env partition holds no U-Boot environment with a valid checksum")


def env_bytes(env: Env, raw: bytes) -> bytes:
    body = b"".join(f"{k}={env.values[k]}".encode("latin-1") + b"\0" for k in env.names) + b"\0"
    room = env.size - env.data_offset
    need(len(body) <= room, "the changed U-Boot environment does not fit")
    body += bytes([env.pad]) * (room - len(body))
    out = bytearray(raw)
    out[env.data_offset:env.size] = body
    struct.pack_into("<I", out, 0, zlib.crc32(body))
    return bytes(out)


def expand(text: str, values: dict[str, str], depth: int = 0) -> str:
    need(depth < 8, "U-Boot environment variables refer to each other in a loop")
    out = re.sub(r"\$\{([A-Za-z0-9_]+)\}|\$([A-Za-z0-9_]+)",
                 lambda m: values.get(m.group(1) or m.group(2), ""), text)
    return out if "$" not in out or out == text else expand(out, values, depth + 1)


def bootargs_commands(env: Env) -> list[tuple[str, int, str]]:
    """(variable, segment number, arguments) of every 'setenv bootargs ...'."""
    found = []
    for name in env.names:
        for n, segment in enumerate(env.values[name].split(";")):
            words = segment.strip().split(None, 2)
            if len(words) >= 2 and words[0] == "setenv" and words[1] == "bootargs":
                found.append((name, n, words[2] if len(words) > 2 else ""))
    return found


def bootargs_templates(env: Env) -> list[str]:
    """Every kernel command line the environment can build. Allwinner's U-Boot
    may choose setargs_mmc or setargs_nand itself, so all of them count."""
    found = [args for _, _, args in bootargs_commands(env)]
    if "bootargs" in env.values:
        found.append(env.values["bootargs"])
    return found


def args_dict(text: str) -> dict[str, str]:
    """Like the kernel: the last value of a repeated argument wins."""
    out: dict[str, str] = {}
    for word in text.split():
        key, sep, value = word.partition("=")
        out[key] = value if sep else ""
    return out


def arg_count(text: str, key: str) -> int:
    return sum(1 for word in text.split() if word.partition("=")[0] == key)


def check_env_controls_root(env: Env, cmdline: str) -> None:
    """Refuse unless the stock root= and init= came from this environment."""
    stock = args_dict(cmdline)
    need("root" in stock, "the stock kernel command line names no root")
    need(arg_count(cmdline, "root") == 1 and arg_count(cmdline, "init") <= 1,
         "the stock kernel command line names root= or init= twice: the bootloader adds its own")
    templates = bootargs_templates(env)
    need(bool(templates), "U-Boot's environment builds no kernel command line")
    built = [args_dict(expand(t, env.values)) for t in templates]
    need(any(b.get("root") == stock["root"] and b.get("init") == stock.get("init") for b in built),
         f"no command line in U-Boot's environment gives root={stock['root']} "
         f"init={stock.get('init')}: this bootloader picks the system itself, so the "
         "image could not point it at the card")


BOOTIMG_CMDLINE = slice(64, 64 + 512)


def bootimg_cmdline(raw: bytes) -> str:
    return raw[BOOTIMG_CMDLINE].split(b"\0", 1)[0].decode("latin-1")


def rewrite_bootimg_cmdline(raw: bytes, partuuid: str, fstype: str) -> bytes:
    """U-Boot appends the boot image's own command line; if that names the
    system, it would win over the environment, so it is pointed at the card too."""
    text = bootimg_cmdline(raw)
    if not any(arg_count(text, k) for k in ("root", "init", "rdinit", "rootfstype", "rw")):
        return raw
    new = rewrite_args(text, partuuid, fstype).encode("latin-1")
    need(len(new) < 512, "the boot image's command line has no room for the change")
    out = bytearray(raw)
    out[BOOTIMG_CMDLINE] = new.ljust(512, b"\0")
    return bytes(out)


def rewrite_args(args: str, partuuid: str, fstype: str) -> str:
    words, seen = [], set()
    for word in args.split():
        key = word.partition("=")[0]
        if key in ("root", "rootfstype", "init", "rdinit", "rw", "ro", "rootwait"):
            if key in seen or key == "rdinit":
                continue
            seen.add(key)
            word = {"root": f"root=PARTUUID={partuuid}", "rootfstype": f"rootfstype={fstype}",
                    "init": f"init={INIT_PATH}", "rw": "ro", "ro": "ro",
                    "rootwait": "rootwait"}[key]
            if key in ("rw", "ro"):
                seen.update(("rw", "ro"))
        words.append(word)
    for key, word in (("root", f"root=PARTUUID={partuuid}"), ("rootfstype", f"rootfstype={fstype}"),
                      ("init", f"init={INIT_PATH}"), ("ro", "ro"), ("rootwait", "rootwait")):
        if key not in seen and not (key == "ro" and "rw" in seen):
            words.append(word)
    return " ".join(words)


def rewrite_env(env: Env, partuuid: str, fstype: str) -> Env:
    values = dict(env.values)
    for name in env.names:
        segments = values[name].split(";")
        changed = False
        for n, segment in enumerate(segments):
            words = segment.strip().split(None, 2)
            if len(words) >= 2 and words[0] == "setenv" and words[1] == "bootargs":
                lead = segment[:len(segment) - len(segment.lstrip())]
                args = words[2] if len(words) > 2 else ""
                segments[n] = f"{lead}setenv bootargs {rewrite_args(args, partuuid, fstype)}"
                changed = True
        if changed:
            values[name] = ";".join(segments)
    if "bootargs" in values:
        values["bootargs"] = rewrite_args(values["bootargs"], partuuid, fstype)
    # Variables the templates may read, set as well, in case the bootloader
    # rebuilds the line from them.
    for key, value in (("mmc_root", f"PARTUUID={partuuid}"), ("init", INIT_PATH),
                       ("rootfstype", fstype)):
        if key in values:
            values[key] = value
    return replace(env, values=values)


# -------------------------------------------------------------- boot.img

def check_bootimg(raw: bytes, release: str) -> dict:
    need(raw[:8] == b"ANDROID!", "the boot partition is not an Android boot image")
    kernel_size, _, ramdisk_size, _, second_size, _, _, page = struct.unpack_from("<8I", raw, 8)
    header_version = u32(raw, 40)
    need(page in (2048, 4096, 8192, 16384), "boot image has an odd page size")
    need(0 < kernel_size <= len(raw), "boot image has an impossible kernel size")
    kernel = raw[page:page + kernel_size]
    marker = f"Linux version {release} ".encode()
    found = marker in kernel
    if not found and kernel[:2] == b"\x1f\x8b":
        try:
            found = marker in gzip.decompress(kernel)
        except (OSError, EOFError):
            found = False
    return {"kernel_size": kernel_size, "ramdisk_size": ramdisk_size, "second_size": second_size,
            "page": page, "header_version": header_version, "kernel_matches": found}


def strip_ramdisk(raw: bytes, info: dict) -> bytes:
    """The same boot image without its ramdisk: the kernel then mounts root= itself."""
    if info["ramdisk_size"] == 0:
        return raw
    need(info["second_size"] == 0 and info["header_version"] == 0,
         "boot image carries more than a kernel and ramdisk; cannot drop its ramdisk safely")
    page = info["page"]
    out = bytearray(raw)
    struct.pack_into("<I", out, 16, 0)
    # The id field is a digest of the parts; U-Boot does not check it, but keep
    # it honest: SHA-1 over kernel, size, ramdisk (empty), size, second, size.
    digest = hashlib.sha1()
    kernel = raw[page:page + info["kernel_size"]]
    for blob in (kernel, b"", b""):
        digest.update(blob)
        digest.update(struct.pack("<I", len(blob)))
    out[576:576 + 32] = digest.digest().ljust(32, b"\0")
    kernel_end = page + -(-info["kernel_size"] // page) * page
    out[kernel_end:] = bytes(len(out) - kernel_end)
    return bytes(out)


# ---------------------------------------------- modules, daemons and GPIO

def norm_module(name: str) -> str:
    return name.replace("-", "_")


def loaded_modules(text: str) -> list[str]:
    """Load order: /proc/modules lists the newest first."""
    names = [line.split()[0] for line in text.splitlines() if line.strip()]
    return [norm_module(n) for n in reversed(names)]


def module_users(text: str) -> dict[str, set[str]]:
    """name -> the modules that use it (the fourth column of /proc/modules)."""
    users = {}
    for line in text.splitlines():
        fields = line.split()
        if len(fields) >= 4:
            users[norm_module(fields[0])] = {norm_module(u) for u in fields[3].split(",")
                                             if u and u != "-"}
    return users


def module_plan(text: str, root: Path, release: str) -> tuple[list[str], list[str], list[str]]:
    index: dict[str, str] = {}
    preferred = root / "lib" / "modules" / release
    for base in (preferred, root):
        if not base.is_dir():
            continue
        for path in base.rglob("*.ko"):
            rel = "/" + path.relative_to(root).as_posix()
            if rel.startswith(("/proc/", "/sys/", "/dev/", "/mnt/")):
                continue
            index.setdefault(norm_module(path.stem), rel)
    # A radio module that an early module needs loads early too.
    users = module_users(text)
    late = {n for n in users if RADIO_MODULE.match(n)}
    changed = True
    while changed:
        changed = False
        for name in sorted(late):
            if users.get(name, set()) - late:
                late.discard(name)
                changed = True
    early, radio, missing = [], [], []
    for name in loaded_modules(text):
        path = index.get(name)
        if path is None:
            missing.append(name)
        elif name in late:
            radio.append(path)
        else:
            early.append(path)
    return early, radio, missing


def daemon_plan(text: str, root: Path, start: set[str], skip: set[str]) -> tuple[list[str], list[str]]:
    lines, ignored, seen = [], [], set()
    rows = []
    for line in text.splitlines():
        fields = line.split("\t")
        if len(fields) != 5 or not fields[0].isdigit():
            continue
        rows.append((int(fields[0]), fields[1], fields[2], fields[3], fields[4]))
    for pid, exe, cwd, ld, cmd in sorted(rows):
        argv = [a for a in cmd.split("\x1f") if a != ""]
        if not argv:
            continue
        if exe.endswith(" (deleted)"):
            exe = exe[:-len(" (deleted)")]
        name = Path(exe or argv[0]).name
        applet = name in ("busybox", "toybox")
        if applet:
            name = Path(argv[0]).name  # udhcpc and the like are busybox applets
        if name in skip:
            ignored.append(name)
            continue
        # Only what is known to be needed starts by default: the stick daemon
        # and the Wi-Fi pair. Anything else of the stock system's is listed in
        # the build report and starts only when asked for (--start-daemon).
        if re.fullmatch(r"trimui_inputd.*", name) or name in start:
            group = "hardware"
        elif name in RADIO_DAEMONS:
            group = "radio"
        else:
            ignored.append(name)
            continue
        if exe.startswith("/mnt/") or not exe or not (root / exe.lstrip("/")).exists():
            # CrossMix runs its own stick daemon from the card; the system's
            # own copy does the same job.
            stock = root / "usr" / "trimui" / "bin" / "trimui_inputd"
            if group == "hardware" and name.startswith("trimui_inputd") and stock.is_file():
                exe, cwd, argv = "/usr/trimui/bin/trimui_inputd", "/usr/trimui/bin", \
                    ["/usr/trimui/bin/trimui_inputd"] + argv[1:]
            else:
                ignored.append(name)
                continue
        if any(c in cwd + ld for c in "|\n") or any("\n" in a for a in argv):
            ignored.append(name)
            continue
        if "/" not in argv[0] and not applet:
            argv = [exe] + argv[1:]  # process 1's PATH may not find a bare name
        command = " ".join(shlex.quote(a) for a in argv)
        if command in seen:
            continue
        seen.add(command)
        lines.append(f"{group}|{cwd or '/'}|{ld}|{command}")
    stock = root / "usr" / "trimui" / "bin" / "trimui_inputd"
    if stock.is_file() and "trimui_inputd" not in skip and \
            not any("trimui_inputd" in line.rsplit("|", 1)[1] for line in lines):
        # The sticks need it even if it was not running when the dump was made.
        lines.insert(0, "hardware|/usr/trimui/bin|/usr/trimui/lib|/usr/trimui/bin/trimui_inputd")
    return lines, sorted(set(ignored))


def gpio_plan(text: str) -> list[str]:
    out = []
    for line in text.splitlines():
        parts = line.split()
        if len(parts) == 3 and parts[0].isdigit() and parts[1] in ("in", "out") and parts[2] in ("0", "1"):
            out.append(" ".join(parts))
    return out


# ------------------------------------------------------------ file systems

def filesystems(text: str) -> set[str]:
    return {line.split()[-1] for line in text.splitlines() if line.strip()}


def builtin_fs(text: str, modules: list[str], name: str) -> bool:
    return name in filesystems(text) and name not in modules


SQUASHFS_COMPRESSORS = {1: "gzip", 2: "lzma", 3: "lzo", 4: "xz", 5: "lz4", 6: "zstd"}
CONFIG_COMPRESSORS = (("CONFIG_SQUASHFS_XZ=y", "xz"), ("CONFIG_SQUASHFS_ZLIB=y", "gzip"),
                      ("CONFIG_SQUASHFS_LZ4=y", "lz4"), ("CONFIG_SQUASHFS_LZO=y", "lzo"),
                      ("CONFIG_SQUASHFS_ZSTD=y", "zstd"))


def squashfs_compressor(root_head: bytes | None, config: str | None) -> str | None:
    """The compression the stock kernel is known to read: its own squashfs
    root's, else one its configuration enables."""
    if root_head and root_head[:4] == b"hsqs" and len(root_head) >= 22:
        found = SQUASHFS_COMPRESSORS.get(struct.unpack_from("<H", root_head, 20)[0])
        if found:
            return found
    for line, name in CONFIG_COMPRESSORS:
        if config and line in config.splitlines():
            return name
    return None


def choose_root_fs(fs_text: str, modules: list[str], stock_fstype: str,
                   compressor: str | None) -> str:
    """squashfs when the kernel's own squashfs compression is known, else ext4."""
    squashfs = builtin_fs(fs_text, modules, "squashfs") and compressor is not None
    ext4 = builtin_fs(fs_text, modules, "ext4")
    if squashfs and (stock_fstype == "squashfs" or not ext4):
        return "squashfs"
    if ext4:
        return "ext4"
    raise BuildError("the stock kernel has neither ext4 nor a squashfs whose compression is known built in")


def choose_data_fs(fs_text: str, have_exfat: bool) -> str:
    known = filesystems(fs_text)
    if "exfat" in known and have_exfat:
        return "exfat"
    need("vfat" in known, "the stock kernel reads neither exFAT nor FAT32 cards")
    return "vfat"


def parse_size(text: str) -> int:
    match = re.fullmatch(r"\s*(\d+(?:\.\d+)?)\s*([KMGT]?)B?\s*", text.upper())
    need(match is not None, f"unreadable size: {text!r}")
    scale = {"": 1, "K": 10**3, "M": 10**6, "G": 10**9, "T": 10**12}[match.group(2)]
    return int(float(match.group(1)) * scale)


def align_up(value: int, step: int = ALIGN) -> int:
    return -(-value // step) * step


def layout(gpt: Gpt, root_name: str, root_bytes: int, card_bytes: int) -> tuple[list[Part], int]:
    """The card's partitions: the stock ones in front of the system, at the same
    sectors and numbers; the system at its stock place (grown if needed); the
    JUKAHUB partition after it, to the end of the card."""
    roots = [p for p in gpt.parts if p.name == root_name]
    need(len(roots) == 1, f"the eMMC has no single {root_name!r} partition")
    stock_root = roots[0]
    kept = [replace(p, unique=uuid.uuid4().bytes_le) for p in gpt.parts if p.first < stock_root.first]
    sectors = max(stock_root.sectors, align_up(-(-root_bytes // SECTOR)))
    root = replace(stock_root, unique=uuid.uuid4().bytes_le, last=stock_root.first + sectors - 1)
    used = {p.index for p in kept} | {root.index}
    free = [i for i in range(gpt.count) if i not in used]
    need(bool(free), "the GPT has no free slot for the JUKAHUB partition")
    entry_sectors = -(-gpt.count * gpt.entry_size // SECTOR)
    total = (int(card_bytes * 0.96) // SECTOR) // ALIGN * ALIGN
    data_first = align_up(root.last + 1)
    data_last = (total - 2 - entry_sectors) // ALIGN * ALIGN - 1
    need(data_last - data_first + 1 >= 512 * 2048,
         "the card is too small: JukaHub OS needs at least 512 MiB after the system")
    data = Part(free[0], DATA_TYPE.bytes_le, uuid.uuid4().bytes_le, data_first, data_last, 0, DATA_NAME)
    return sorted(kept + [root, data], key=lambda p: p.index), total


# ------------------------------------------------------------- root files

def elf_aarch64(path: Path) -> None:
    need(path.is_file(), f"missing executable: {path}")
    with path.open("rb") as stream:
        head = stream.read(20)
    need(len(head) >= 20 and head[:4] == b"\x7fELF", f"not an ELF binary: {path}")
    need(head[4] == 2 and head[5] == 1 and struct.unpack_from("<H", head, 18)[0] == 183,
         f"not a little-endian AArch64 binary: {path}")


def has_command(root: Path, name: str) -> bool:
    return any(os.path.lexists(root / d / name) for d in ("bin", "sbin", "usr/bin", "usr/sbin"))


def transform_root(root: Path, board: Path, facts: dict[str, str], data_fs: str,
                   start: set[str], skip: set[str]) -> dict:
    missing = [c for c in REQUIRED_COMMANDS if not has_command(root, c)]
    need(not missing, f"the stock system lacks {', '.join(missing)}")
    need((root / "etc").is_dir() and not (root / "etc").is_symlink(), "the stock system has no /etc")
    # Mount points: the root is read-only on the card, so they exist up front.
    for name in ("proc", "sys", "dev", "tmp", "mnt/SDCARD", "mnt/UDISK"):
        path = root / name
        need(not path.is_symlink() and (path.is_dir() or not path.exists()),
             f"the stock /{name} is not a folder")
        path.mkdir(parents=True, exist_ok=True)
    release = facts["KERNEL"]
    early, radio, absent = module_plan((board / "modules.txt").read_text(encoding="utf-8"), root, release)
    daemons, ignored = daemon_plan((board / "procs.txt").read_text(encoding="utf-8", errors="replace"),
                                   root, start, skip)
    gpio = gpio_plan((board / "gpio.txt").read_text(encoding="utf-8")) if (board / "gpio.txt").is_file() else []
    wifi = any(line.startswith("radio|") and "wpa_supplicant" in line for line in daemons)

    for script in ("init", "launch.sh"):
        need(b"\r" not in (HERE / script).read_bytes(),
             f"{script} has Windows line endings; check it out with LF (see .gitattributes)")
    # The kernel opens /dev/console before devtmpfs is mounted over /dev.
    for node, minor_pair in (("console", (5, 1)), ("null", (1, 3))):
        path = root / "dev" / node
        if not os.path.lexists(path) and os.geteuid() == 0:
            os.mknod(path, 0o600 | stat.S_IFCHR, os.makedev(*minor_pair))
    j = root / "jukahub"
    need(not j.exists(), "the stock system already has a /jukahub folder")
    j.mkdir()
    shutil.copy2(HERE / "init", j / "init")
    shutil.copy2(HERE / "launch.sh", j / "launch.sh")
    (j / "init").chmod(0o755)
    (j / "launch.sh").chmod(0o755)
    (j / "board.env").write_text(
        "# Written by os/smartpro/build_image.py from this device's board folder.\n"
        f"DATA_NAME={DATA_NAME}\n"
        f"DATA_FSTYPE={data_fs}\n"
        f"MODULES={shlex.quote(' '.join(early))}\n"
        f"RADIO_MODULES={shlex.quote(' '.join(radio))}\n"
        f"WIFI={1 if wifi else 0}\n",
        encoding="utf-8")
    (j / "daemons.list").write_text("".join(line + "\n" for line in daemons), encoding="utf-8")
    (j / "gpio.list").write_text("".join(line + "\n" for line in gpio), encoding="utf-8")
    if (board / "asound.state").is_file():
        shutil.copy2(board / "asound.state", j / "asound.state")
    (root / "etc" / "jukahub-os-release").write_text(
        "JUKAHUB_OS_NATIVE=1\nBASE=smartpro-stock-kernel\nDEVICE=trimui-smart-pro\n"
        f"KERNEL={release}\n", encoding="utf-8")
    return {"modules": early, "radio_modules": radio, "modules_not_found": absent,
            "daemons": daemons, "processes_not_started": ignored, "gpio": gpio, "wifi": wifi}


# ---------------------------------------------------------- card contents

def copy_card_tree(source: Path, target: Path, skipped: list[str]) -> None:
    """Copy for FAT/exFAT: regular files and folders only."""
    for dirpath, dirnames, filenames in os.walk(source):
        rel = Path(dirpath).relative_to(source)
        (target / rel).mkdir(parents=True, exist_ok=True)
        for name in list(dirnames):
            if (Path(dirpath) / name).is_symlink():
                skipped.append(str(rel / name))
                dirnames.remove(name)
        for name in filenames:
            path = Path(dirpath) / name
            if path.is_symlink() or not path.is_file():
                skipped.append(str(rel / name))
                continue
            shutil.copyfile(path, target / rel / name)


def stage_card(card: Path, app: Path, board: Path, stock_card: Path | None) -> dict:
    elf_aarch64(app / "JukaHub")
    capability = app / "native-os-capability.txt"
    need(capability.is_file() and CAPABILITY in capability.read_text(encoding="utf-8").split(),
         "JukaHub package predates Smart Pro OS support")
    skipped: list[str] = []
    copy_card_tree(app, card / "Apps" / "JukaHub", skipped)
    need(not skipped, f"the JukaHub package holds links or special files: {', '.join(skipped[:5])}")
    udisk = card / ".userdata" / "smartpro" / "udisk"
    udisk.mkdir(parents=True)
    for config in (board / "udisk").glob("joypad*.config") if (board / "udisk").is_dir() else []:
        shutil.copyfile(config, udisk / config.name)
    (card / ".userdata" / "jukahub").mkdir(parents=True)
    copied = []
    if stock_card is not None:
        need(stock_card.is_dir(), f"stock card folder is missing: {stock_card}")
        for name in ("RetroArch", "Emus", "Bios"):
            if (stock_card / name).is_dir():
                copy_card_tree(stock_card / name, card / name, skipped)
                copied.append(name)
    for name in ("Roms", "Bios", "Saves", "Screenshots"):
        (card / name).mkdir(exist_ok=True)
    licenses = card / "Licenses"
    licenses.mkdir()
    shutil.copyfile(REPO / "LICENSE", licenses / "JukaHub-AGPL-3.0.txt")
    (card / "JukaHub-OS-README.txt").write_text(
        "JukaHub OS for the TrimUI Smart Pro\r\n\r\n"
        "This card boots the Smart Pro's own bootloader and kernel, copied from\r\n"
        "this device, with JukaHub as its menu. Take the card out to boot the\r\n"
        "stock system; nothing on the device itself was changed.\r\n\r\n"
        "Put games in Roms/<system>, BIOS files in Bios, saves go to Saves.\r\n"
        "Wi-Fi: add WIFI=0 to .userdata/jukahub/boot.env to keep it off.\r\n"
        "Logs: .userdata/jukahub/os.log and boot.log.\r\n\r\n"
        "The system partition holds TrimUI's software from this device: do not\r\n"
        "share this card image. JukaHub source: https://github.com/jukaLang/JukaHubV2\r\n",
        encoding="utf-8")
    return {"stock_card_folders": copied, "not_copied": skipped}


# ------------------------------------------------------------- boot logo

def bmp_info(data: bytes) -> tuple[int, int, int, int] | None:
    if len(data) < 54 or data[:2] != b"BM":
        return None
    offset = u32(data, 10)
    width, height = struct.unpack_from("<ii", data, 18)
    bpp = struct.unpack_from("<H", data, 28)[0]
    compression = u32(data, 30)
    if compression not in (0, 3):
        return None
    return width, height, bpp, offset


def bmp_like(stock: bytes, ours: bytes) -> bytes | None:
    """Our 24-bit logo in the stock file's size and depth, or None."""
    s, o = bmp_info(stock), bmp_info(ours)
    if s is None or o is None or o[2] != 24 or (s[0], abs(s[1])) != (o[0], abs(o[1])):
        return None
    width, height, bpp = s[0], abs(s[1]), s[2]
    if bpp not in (24, 32) or o[1] < 0:
        return None
    src_stride = (width * 3 + 3) & ~3
    rows = [ours[o[3] + y * src_stride:o[3] + y * src_stride + width * 3] for y in range(height)]
    if s[1] < 0:
        rows.reverse()  # the stock file is top-down
    if bpp == 24:
        stride = src_stride
        pixels = b"".join(r + bytes(stride - len(r)) for r in rows)
    else:
        pixels = b"".join(b"".join(r[i:i + 3] + b"\xff" for i in range(0, len(r), 3)) for r in rows)
    head = bytearray(stock[:s[3]])
    struct.pack_into("<I", head, 2, len(head) + len(pixels))
    struct.pack_into("<I", head, 34, len(pixels))
    return bytes(head) + pixels


# ----------------------------------------------------------------- build

def run(*args: str) -> None:
    subprocess.run(list(args), check=True)


def require_tool(name: str) -> None:
    need(shutil.which(name) is not None, f"{name} is required")


def make_root_image(root: Path, fstype: str, out: Path, compressor: str | None = None) -> None:
    if fstype == "squashfs":
        require_tool("mksquashfs")
        run("mksquashfs", str(root), str(out), "-comp", compressor or "gzip", "-b", "131072",
            "-noappend", "-no-xattrs", "-quiet")
        return
    require_tool("mkfs.ext4")
    size = sum(p.lstat().st_size for p in root.rglob("*")) * 13 // 10 + (64 << 20)
    size = align_up(size, 1 << 20)
    with out.open("wb") as stream:
        stream.truncate(size)
    base = ["mkfs.ext4", "-F", "-q", "-L", "jukahub-root", "-d", str(root)]
    for features in ("^metadata_csum,^64bit,^metadata_csum_seed,^orphan_file", "^metadata_csum,^64bit"):
        if subprocess.run(base + ["-O", features, str(out)]).returncode == 0:
            return
    raise BuildError("mkfs.ext4 could not make a root the 4.9 kernel reads")


def make_data_image(card: Path, fstype: str, out: Path, sectors: int) -> None:
    with out.open("wb") as stream:
        stream.truncate(sectors * SECTOR)
    if fstype == "exfat":
        version = subprocess.run(["mkfs.exfat", "-V"], capture_output=True, text=True)
        flag = "-L" if "exfatprogs" in (version.stdout + version.stderr) else "-n"
        run("mkfs.exfat", flag, DATA_NAME, str(out))
    else:
        run("mkfs.vfat", "-F", "32", "-n", DATA_NAME, str(out))
    mount = Path(tempfile.mkdtemp(prefix="jukahub-card-"))
    run("mount", "-o", "loop", str(out), str(mount))
    try:
        skipped: list[str] = []
        copy_card_tree(card, mount, skipped)
        need(not skipped, "card payload changed while copying")
    finally:
        run("sync")
        run("umount", str(mount))
        mount.rmdir()


def replace_logo(image: Path) -> str:
    mount = Path(tempfile.mkdtemp(prefix="jukahub-logo-"))
    try:
        if subprocess.run(["mount", "-o", "loop", str(image), str(mount)]).returncode != 0:
            return "kept the stock boot logo (its partition is not FAT)"
        try:
            logo = mount / "bootlogo.bmp"
            if not logo.is_file():
                return "kept the stock boot logo (no bootlogo.bmp)"
            old = logo.read_bytes()
            new = bmp_like(old, BOOTLOGO.read_bytes())
            if new is None:
                return "kept the stock boot logo (different size or depth)"
            space = os.statvfs(mount)
            if len(new) > len(old) + space.f_bavail * space.f_frsize - (64 << 10):
                return "kept the stock boot logo (no room for the JukaHub one)"
            logo.write_bytes(new)
            return "JukaHub boot logo"
        finally:
            run("sync")
            run("umount", str(mount))
    finally:
        mount.rmdir()


def write_at(image, offset: int, source: Path) -> None:
    image.seek(offset)
    zero = bytes(1 << 20)
    with source.open("rb") as stream:
        while data := stream.read(1 << 20):
            if data == zero[:len(data)]:
                image.seek(len(data), 1)
            else:
                image.write(data)


def build_image(args: argparse.Namespace) -> None:
    need(sys.platform.startswith("linux"), "image packing requires Linux")
    need(os.geteuid() == 0, "run image packing as root (system file owners, loop mounts)")
    for tool in ("tar", "xz", "mount", "umount"):
        require_tool(tool)
    board = args.board.resolve()
    output = args.output.resolve()
    need(not output.exists(), f"output already exists: {output}")
    facts = load_board(board)
    lead_path = board / "lead.bin"
    lead = lead_path.read_bytes()
    boot0_len = check_boot0(lead)
    package = check_package(lead)
    if package["panel"] is not None:
        need(package["panel"] == (1280, 720),
             f"the device tree describes a {package['panel'][0]}x{package['panel'][1]} panel, not the Smart Pro's")
    gpt = parse_gpt(lead)
    need(gpt.entry_lba * SECTOR + gpt.count * gpt.entry_size <= BOOT0_OFFSET,
         "the GPT entry table overlaps boot0")
    reported = {}
    for line in (board / "partitions.txt").read_text(encoding="utf-8").split("\n"):
        fields = line.split()
        if len(fields) == 4:
            reported[int(fields[0])] = (fields[1], int(fields[2]), int(fields[3]))
    for part in gpt.parts:
        need(reported.get(part.index + 1) == (part.name, part.first, part.sectors),
             f"GPT partition {part.index + 1} ({part.name}) differs from what the kernel reported")
    root_name = facts["ROOT_NAME"]
    by_name = {p.name: p for p in gpt.parts}
    need(root_name in by_name, f"no {root_name!r} partition in the GPT")
    front = [p for p in gpt.parts if p.first < by_name[root_name].first]
    for part in front:
        need(part.first >= len(lead) // SECTOR, f"{part.name} overlaps the boot area")
        image = board / "parts" / f"{part.name}.img"
        need(image.is_file() and image.stat().st_size == part.sectors * SECTOR,
             f"board folder has no complete copy of partition {part.name}")
    need("boot" in by_name and by_name["boot"] in front, "no boot partition in front of the system")
    env_parts = [p for p in front if p.name in ("env", "env-redund")]
    need(bool(env_parts), "no env partition in front of the system")

    modules_text = (board / "modules.txt").read_text(encoding="utf-8")
    modules = loaded_modules(modules_text)
    critical = sorted(ROOT_CRITICAL_MODULES & set(modules))
    need(not critical, f"the stock kernel loads {', '.join(critical)} as modules: it needs its "
                       "initramfs to mount a system, which this image cannot use")
    fs_text = (board / "filesystems.txt").read_text(encoding="utf-8")
    need("devtmpfs" in filesystems(fs_text), "the stock kernel has no devtmpfs")
    head_path = board / "root-head.bin"
    config_path = board / "config.gz"
    config = gzip.decompress(config_path.read_bytes()).decode("latin-1") if config_path.is_file() else None
    compressor = squashfs_compressor(head_path.read_bytes() if head_path.is_file() else None, config)
    root_fs = choose_root_fs(fs_text, modules, facts.get("ROOT_FSTYPE", ""), compressor)
    data_fs = choose_data_fs(fs_text, shutil.which("mkfs.exfat") is not None)
    require_tool("mkfs.exfat" if data_fs == "exfat" else "mkfs.vfat")
    cmdline = (board / "cmdline.txt").read_text(encoding="utf-8").strip()

    with tempfile.TemporaryDirectory(prefix="jukahub-smartpro-") as tmp:
        work = Path(tmp)
        boot_raw = (board / "parts" / "boot.img").read_bytes()
        boot_info = check_bootimg(boot_raw, facts["KERNEL"])
        if not boot_info["kernel_matches"]:
            print(f"note: no 'Linux version {facts['KERNEL']}' banner found in the boot "
                  "partition's kernel (it may be compressed); it is used as it is")
        boot_stripped = strip_ramdisk(boot_raw, boot_info)

        root = work / "root"
        root.mkdir()
        run("tar", "--numeric-owner", "-xpf", str(board / "rootfs.tar"), "-C", str(root))
        plan = transform_root(root, board, facts, data_fs, set(args.start_daemon or []),
                              set(args.skip_daemon or []))
        root_img = work / "root.img"
        make_root_image(root, root_fs, root_img, compressor)
        shutil.rmtree(root)

        parts, total = layout(gpt, root_name, root_img.stat().st_size, parse_size(args.card_size))
        new_root = next(p for p in parts if p.name == root_name)
        data = next(p for p in parts if p.name == DATA_NAME)

        envs = {}
        for part in env_parts:
            raw = (board / "parts" / f"{part.name}.img").read_bytes()
            try:
                env = parse_env(raw)
            except BuildError:
                if part.name == "env-redund":
                    continue  # never written: U-Boot ignores a copy that fails its CRC
                raise
            check_env_controls_root(env, cmdline)
            envs[part.name] = env_bytes(rewrite_env(env, new_root.partuuid, root_fs), raw)
        need(bool(envs), "no U-Boot environment to point at the card")
        boot_new = rewrite_bootimg_cmdline(boot_stripped, new_root.partuuid, root_fs)

        logo = "kept the stock boot logo"
        images = {}
        for part in parts:
            if part is new_root or part is data:
                continue
            path = work / f"{part.name}.img"
            if part.name in envs:
                path.write_bytes(envs[part.name])
            elif part.name == "boot":
                path.write_bytes(boot_new)
            else:
                shutil.copyfile(board / "parts" / f"{part.name}.img", path)
                if part.name in ("boot-resource", "bootloader") and not args.keep_stock_logo:
                    logo = replace_logo(path)
            images[part.name] = path

        card = work / "card"
        card.mkdir()
        staged = stage_card(card, args.app.resolve(), board,
                            args.stock_card.resolve() if args.stock_card else None)
        data_img = work / "data.img"
        make_data_image(card, data_fs, data_img, data.sectors)
        shutil.rmtree(card)

        raw = work / "card-image.img"
        images[root_name] = root_img
        images[DATA_NAME] = data_img
        assemble_image(raw, lead, gpt, parts, images, total)
        verify_image(raw, lead, boot0_len, package["length"], parts)
        output.parent.mkdir(parents=True, exist_ok=True)
        staged_output = work / output.name
        if output.suffix == ".xz":
            with staged_output.open("wb") as target:
                subprocess.run(["xz", "-T0", "-6", "-c", str(raw)], stdout=target, check=True)
        else:
            shutil.move(raw, staged_output)
        shutil.move(staged_output, output)

    manifest = {
        "device": "trimui-smart-pro",
        "base": "smartpro-stock-kernel",
        "kernel": facts["KERNEL"],
        "root_filesystem": root_fs,
        "squashfs_compression": compressor if root_fs == "squashfs" else None,
        "root_partuuid": new_root.partuuid,
        "card_filesystem": data_fs,
        "boot_package_checksum_ok": package["checksum_ok"],
        "boot_image_ramdisk_dropped": boot_info["ramdisk_size"] > 0,
        "boot_logo": logo,
        "partitions": [{"number": p.index + 1, "name": p.name, "first_lba": p.first,
                        "sectors": p.sectors} for p in parts],
        "jukahub_binary_sha256": sha256(args.app / "JukaHub"),
        "output_sha256": sha256(output),
        **plan, **staged,
    }
    output.with_name(output.name + ".build.json").write_text(
        json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(f"JukaHub OS image for the Smart Pro: {output}")
    print(f"SHA-256: {manifest['output_sha256']}")
    if not package["checksum_ok"]:
        print("note: the boot package's checksum did not verify; it is copied unchanged")
    if plan["modules_not_found"]:
        print("note: modules not found in the system files: " + ", ".join(plan["modules_not_found"]))
    print("daemons started at boot: " + (", ".join(line.rsplit("|", 1)[1] for line in plan["daemons"]) or "none"))
    if plan["processes_not_started"]:
        print("stock processes NOT started (add with --start-daemon NAME if the hardware needs one): "
              + ", ".join(plan["processes_not_started"]))
    if not plan["wifi"]:
        print("note: Wi-Fi was off when the board was saved, so it will not start; "
              "save the board again with Wi-Fi on")
    print("This image holds TrimUI's software from your device: do not share it.")


def assemble_image(raw: Path, lead: bytes, gpt: Gpt, parts: list[Part],
                   images: dict[str, Path], total: int) -> None:
    """The boot area as the device has it, a new GPT, each partition's contents."""
    entries = gpt_entries(gpt, parts)
    disk_guid = uuid.uuid4().bytes_le
    entry_sectors = -(-len(entries) // SECTOR)
    with raw.open("w+b") as image:
        image.truncate(total * SECTOR)
        image.write(lead)
        image.seek(0)
        image.write(protective_mbr(lead, total))
        image.write(gpt_header(gpt, disk_guid, total, entries, backup=False))
        image.seek(gpt.entry_lba * SECTOR)
        image.write(entries)
        for part in parts:
            source = images[part.name]
            need(source.stat().st_size <= part.sectors * SECTOR, f"{part.name} does not fit its partition")
            write_at(image, part.first * SECTOR, source)
        image.seek((total - 1 - entry_sectors) * SECTOR)
        image.write(entries)
        image.write(gpt_header(gpt, disk_guid, total, entries, backup=True))


def verify_image(raw: Path, lead: bytes, boot0_len: int, package_len: int, parts: list[Part]) -> None:
    """Read the finished image back: boot code where the boot ROM looks, a valid
    GPT naming the same partitions."""
    with raw.open("rb") as image:
        head = image.read(len(lead))
        image.seek(-SECTOR, 2)
        backup = image.read(SECTOR)
        total = image.tell() // SECTOR
    check = bytearray(backup[:92])
    check[16:20] = bytes(4)
    need(backup[:8] == b"EFI PART" and zlib.crc32(check) == u32(backup, 16) and
         struct.unpack_from("<Q", backup, 24)[0] == total - 1, "the backup GPT is not valid")
    need(head[BOOT0_OFFSET:BOOT0_OFFSET + boot0_len] == lead[BOOT0_OFFSET:BOOT0_OFFSET + boot0_len],
         "boot0 was not written intact")
    need(head[PACKAGE_OFFSET:PACKAGE_OFFSET + package_len] ==
         lead[PACKAGE_OFFSET:PACKAGE_OFFSET + package_len], "the boot package was not written intact")
    check_boot0(head)
    gpt = parse_gpt(head)
    need([(p.index, p.name, p.first, p.last) for p in gpt.parts] ==
         [(p.index, p.name, p.first, p.last) for p in parts], "the written GPT differs from the plan")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--board", type=Path, required=True,
                        help="JukaHub-SmartPro-board folder written by dump-board.sh")
    parser.add_argument("--app", type=Path, required=True,
                        help="built JukaHub-Trimui directory")
    parser.add_argument("--stock-card", type=Path,
                        help="a copy of your current Smart Pro card: its RetroArch, Emus and Bios are reused")
    parser.add_argument("--card-size", default="8G",
                        help="size of the microSD card the image is for (e.g. 32G, 128G); default 8G")
    parser.add_argument("--keep-stock-logo", action="store_true",
                        help="leave the stock boot logo on the card's copy of the boot partition")
    parser.add_argument("--start-daemon", action="append", metavar="NAME",
                        help="also start this stock process at boot (repeatable)")
    parser.add_argument("--skip-daemon", action="append", metavar="NAME",
                        help="do not start this stock process at boot (repeatable)")
    parser.add_argument("--output", type=Path, required=True,
                        help="new .img or .img.xz; must not exist")
    args = parser.parse_args()
    try:
        build_image(args)
    except (BuildError, OSError, subprocess.CalledProcessError, lzma.LZMAError, ValueError) as error:
        print(f"build_image: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
