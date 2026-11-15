#!/usr/bin/env python3
"""Make a standalone Brick image from plastron's Brick build output.

The input is plastron's brick-sdcard.img (or a TortOS release image built by
plastron). TortOS is removed from the root; its libretro cores can be reused.
Requires Linux squashfs-tools and xz.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import lzma
import os
from pathlib import Path
import re
import shutil
import struct
import subprocess
import sys
import tempfile


HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
SECTOR = 512
SYSTEMS = HERE / "systems.tsv"


class BuildError(Exception):
    pass


def need(condition: bool, message: str) -> None:
    if not condition:
        raise BuildError(message)


def replace_one(text: str, old: str, new: str, label: str) -> str:
    need(text.count(old) == 1, f"{label}: expected one {old!r}, found {text.count(old)}")
    return text.replace(old, new)


def elf_aarch64(path: Path) -> None:
    need(path.is_file(), f"missing executable: {path}")
    with path.open("rb") as stream:
        head = stream.read(20)
    need(len(head) >= 20 and head[:4] == b"\x7fELF", f"not an ELF binary: {path}")
    need(head[4] == 2 and head[5] == 1 and struct.unpack_from("<H", head, 18)[0] == 183,
         f"not a little-endian AArch64 binary: {path}")


def copy_tree(source: Path, target: Path) -> None:
    need(source.is_dir(), f"missing directory: {source}")
    for path in source.rglob("*"):
        need(not path.is_symlink(), f"card payload cannot contain a symlink: {path}")
    shutil.copytree(source, target, dirs_exist_ok=True)


def system_rows() -> list[tuple[str, str]]:
    rows = []
    for line in SYSTEMS.read_text(encoding="utf-8").splitlines():
        if not line.strip() or line.startswith("#"):
            continue
        folder, core = (part.strip() for part in line.split("|", 1))
        need(folder not in ("", ".", "..") and "/" not in folder and "\\" not in folder,
             f"invalid ROM folder in {SYSTEMS}: {folder!r}")
        need(re.fullmatch(r"[a-z0-9_]+", core) is not None,
             f"invalid core in {SYSTEMS}: {core!r}")
        rows.append((folder, core))
    need(bool(rows), f"no systems in {SYSTEMS}")
    return rows


def configure_retroarch(card: Path, old_card: Path, extra_cores: Path | None) -> list[str]:
    ra = card / "RetroArch"
    cores = ra / ".retroarch" / "cores"
    cores.mkdir(parents=True, exist_ok=True)
    bundled = old_card / "TortOS" / "cores"
    if bundled.is_dir():
        for core in bundled.glob("*_libretro.so"):
            if not (cores / core.name).exists():
                shutil.copy2(core, cores / core.name)
    if extra_cores is not None:
        need(extra_cores.is_dir(), f"cores directory is missing: {extra_cores}")
        for core in extra_cores.glob("*_libretro.so"):
            shutil.copy2(core, cores / core.name)
    ra_cfg = ra / "retroarch.cfg"
    ra_cfg.write_text(
        '# JukaHub OS: one card layout for RetroArch and the library.\n'
        'video_driver = "sdl2"\n'
        'audio_driver = "alsa"\n'
        'input_driver = "sdl2"\n'
        'input_joypad_driver = "sdl2"\n'
        'menu_driver = "rgui"\n'
        'video_fullscreen = "true"\n'
        'libretro_directory = "/mnt/SDCARD/RetroArch/.retroarch/cores"\n'
        'system_directory = "/mnt/SDCARD/Bios"\n'
        'savefile_directory = "/mnt/SDCARD/Saves/RetroArch"\n'
        'savestate_directory = "/mnt/SDCARD/Saves/RetroArch/states"\n'
        'screenshot_directory = "/mnt/SDCARD/Screenshots"\n'
        'config_save_on_exit = "false"\n',
        encoding="utf-8",
    )
    available = []
    for folder, core in system_rows():
        if not (cores / f"{core}_libretro.so").is_file():
            continue
        launcher = card / "Emus" / folder / "launch.sh"
        launcher.parent.mkdir(parents=True, exist_ok=True)
        launcher.write_text(
            '#!/bin/sh\n'
            'RA=/mnt/SDCARD/RetroArch\n'
            'export HOME="$RA"\n'
            'exec "$RA/retroarch" --config "$RA/retroarch.cfg" '
            f'-L "$RA/.retroarch/cores/{core}_libretro.so" "$@"\n',
            encoding="utf-8",
        )
        launcher.chmod(0o755)
        (card / "Roms" / folder).mkdir(parents=True, exist_ok=True)
        available.append(folder)
    need(bool(available), "no usable libretro cores; pass --cores with AArch64 cores")
    (card / "Bios").mkdir(exist_ok=True)
    (card / "Saves" / "RetroArch" / "states").mkdir(parents=True, exist_ok=True)
    return available


def transform_root(root: Path, app: Path, retroarch: Path,
                   extra_cores: Path | None = None) -> list[str]:
    old_card = root / "usr" / "share" / "tortos" / "card"
    need((old_card / "TortOS" / "cores").is_dir(),
         "base is not plastron's Brick image with its libretro cores")
    elf_aarch64(app / "JukaHub")
    capability = app / "native-os-capability.txt"
    need(capability.is_file() and "plastron-brick-v1" in
         capability.read_text(encoding="utf-8").split(), "JukaHub package predates native OS support")
    elf_aarch64(retroarch / "retroarch")
    need((root / "usr" / "lib" / "brick").is_dir(), "base has no plastron Brick hardware support")
    need((root / "usr" / "share" / "splash" / "frames.bin").is_file(),
         "base has no Brick splash player data")

    card = root / "usr" / "share" / "jukahub" / "card"
    (card / "Apps").mkdir(parents=True)
    copy_tree(app, card / "Apps" / "JukaHub")
    copy_tree(retroarch, card / "RetroArch")
    elf_aarch64(card / "Apps" / "JukaHub" / "JukaHub")
    elf_aarch64(card / "RetroArch" / "retroarch")
    systems = configure_retroarch(card, old_card, extra_cores)

    notices = card / "Licenses"
    notices.mkdir(exist_ok=True)
    for name in ("plastron.md", "plastron-packages.md"):
        source = old_card / "Licenses" / name
        if source.is_file():
            shutil.copy2(source, notices / name)
    core_notice = old_card / "Licenses" / "TortOS.md"
    if core_notice.is_file():
        shutil.copy2(core_notice, notices / "Libretro-cores.md")
    shutil.copy2(REPO / "LICENSE", notices / "JukaHub-AGPL-3.0.txt")
    (card / "JukaHub-OS-SOURCES.txt").write_text(
        "JukaHub OS source: https://github.com/jukaLang/JukaHubV2\n"
        "plastron source: https://github.com/ericreinsmidt/plastron\n"
        "Libretro core sources and licenses: Licenses/Libretro-cores.md\n"
        "RetroArch source: https://github.com/libretro/RetroArch\n",
        encoding="utf-8",
    )

    inittab = root / "etc" / "inittab"
    text = inittab.read_text(encoding="utf-8")
    text = replace_one(text, "::sysinit:/etc/init.d/tortos start",
                       "::sysinit:/etc/init.d/jukahub start", "inittab")
    text = replace_one(text, "::shutdown:/etc/init.d/tortos stop",
                       "::shutdown:/etc/init.d/jukahub stop", "inittab")
    text = replace_one(text, "::restart:/usr/sbin/tortos-update\n", "", "inittab")
    inittab.write_text(text, encoding="utf-8")

    datapart = root / "etc" / "init.d" / "datapart"
    text = datapart.read_text(encoding="utf-8")
    text = replace_one(text, "name=tortos", "name=jukahub", "datapart")
    text = replace_one(text, "-L TORTOS", "-L JUKAHUB", "datapart")
    text = replace_one(text, "games=$(part tortos)",
                       'games=$(part jukahub)\n\t[ -n "$games" ] || games=$(part tortos)',
                       "datapart")
    datapart.write_text(text, encoding="utf-8")

    bt = root / "etc" / "init.d" / "bluetooth"
    if bt.is_file():
        text = bt.read_text(encoding="utf-8")
        text = replace_one(text, "BTPLAYER=/mnt/SDCARD/TortOS/btplayer\n", "", "bluetooth")
        text = replace_one(text, "\t[ -x $BTPLAYER ] && daemon btplayer $BTPLAYER\n", "", "bluetooth")
        text = replace_one(text, "\tstopd btplayer\n", "", "bluetooth")
        bt.write_text(text, encoding="utf-8")

    shutil.copy2(HERE / "init.sh", root / "etc" / "init.d" / "jukahub")
    shutil.copy2(HERE / "launch.sh", card.parent / "launch.sh")
    (root / "etc" / "init.d" / "jukahub").chmod(0o755)
    (card.parent / "launch.sh").chmod(0o755)
    shutil.copy2(HERE / "frames-brick.bin", root / "usr" / "share" / "splash" / "frames.bin")
    (root / "etc" / "jukahub-os-release").write_text(
        "JUKAHUB_OS_NATIVE=1\nBASE=plastron\nDEVICE=trimui-brick\n",
        encoding="utf-8",
    )
    (root / "etc" / "os-release").write_text(
        'NAME="JukaHub OS"\nID=jukahubos\nID_LIKE=buildroot\nPRETTY_NAME="JukaHub OS for TrimUI Brick"\n',
        encoding="utf-8",
    )
    old_share = root / "usr" / "share" / "tortos"
    shutil.rmtree(old_share)
    (root / "etc" / "init.d" / "tortos").unlink()
    (root / "usr" / "sbin" / "tortos-update").unlink(missing_ok=True)
    return systems


def root_partition(raw: Path) -> tuple[int, int]:
    with raw.open("rb") as image:
        image.seek(SECTOR)
        head = image.read(SECTOR)
        need(head[:8] == b"EFI PART", "image has no GPT header")
        entry_lba, count, entry_size = struct.unpack_from("<QII", head, 72)
        need(0 < count <= 4096 and 128 <= entry_size <= 4096,
             "image has invalid GPT entries")
        image.seek(entry_lba * SECTOR)
        for _ in range(count):
            entry = image.read(entry_size)
            need(len(entry) == entry_size, "truncated GPT entries")
            name = entry[56:128].decode("utf-16le", errors="replace").rstrip("\x00")
            if name == "plastron-root":
                first, last = struct.unpack_from("<QQ", entry, 32)
                need(first > 0 and last >= first, "invalid plastron-root partition")
                offset, size = first * SECTOR, (last - first + 1) * SECTOR
                need(offset + size <= raw.stat().st_size, "root partition exceeds image")
                return offset, size
    raise BuildError("image has no plastron-root partition")


def copy_bytes(source, target, count: int) -> None:
    while count:
        data = source.read(min(count, 1 << 20))
        need(bool(data), "image ended while reading root partition")
        target.write(data)
        count -= len(data)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while data := stream.read(1 << 20):
            digest.update(data)
    return digest.hexdigest()


def require_tool(name: str) -> None:
    need(shutil.which(name) is not None, f"{name} is required (Linux squashfs-tools and xz)")


def validate_linkage(root: Path) -> None:
    """Reject binaries that cannot find their direct shared libraries in plastron."""
    system_libs = set()
    for libroot in (root / "lib", root / "lib64", root / "usr" / "lib",
                    root / "usr" / "lib64"):
        if libroot.is_dir():
            system_libs.update(path.name for path in libroot.rglob("*")
                               if path.is_file() or path.is_symlink())
    card = root / "usr" / "share" / "jukahub" / "card"
    app = card / "Apps" / "JukaHub"
    app_libs = set(system_libs)
    for libroot in (app, app / "libs"):
        if libroot.is_dir():
            app_libs.update(path.name for path in libroot.iterdir()
                            if path.is_file() or path.is_symlink())
    binaries = [app / "JukaHub", card / "RetroArch" / "retroarch"]
    binaries.extend((card / "RetroArch" / ".retroarch" / "cores").glob("*_libretro.so"))
    for binary in binaries:
        dynamic = subprocess.run(["readelf", "-d", str(binary)], check=True,
                                 text=True, capture_output=True).stdout
        needed = re.findall(r"\(NEEDED\).*\[([^]]+)\]", dynamic)
        available = app_libs if binary == app / "JukaHub" else system_libs
        missing = sorted(set(needed) - available)
        need(not missing,
             f"{binary.name} needs {', '.join(missing)} outside plastron's libraries; "
             "build it against plastron's Brick sysroot")
        program = subprocess.run(["readelf", "-l", str(binary)], check=True,
                                 text=True, capture_output=True).stdout
        interpreter = re.search(r"Requesting program interpreter: ([^]]+)", program)
        if interpreter:
            path = interpreter.group(1)
            need(path.startswith("/") and (root / path.lstrip("/")).exists(),
                 f"{binary.name} requests missing interpreter {path}")


def build_image(args: argparse.Namespace) -> None:
    need(sys.platform.startswith("linux"), "image packing requires Linux")
    need(os.geteuid() == 0, "run image packing as root to preserve root filesystem ownership")
    for tool in ("unsquashfs", "mksquashfs", "xz", "readelf"):
        require_tool(tool)
    source = args.base.resolve()
    output = args.output.resolve()
    need(source.is_file(), f"base image is missing: {source}")
    need(not output.exists(), f"output already exists: {output}")
    need(source != output, "output must differ from the base image")
    if args.base_sha256:
        need(sha256(source).lower() == args.base_sha256.lower(), "base image SHA-256 does not match")
    output.parent.mkdir(parents=True, exist_ok=True)

    with tempfile.TemporaryDirectory(prefix="jukahub-plastron-") as tmp:
        work = Path(tmp)
        raw = work / "card.img"
        if source.suffix == ".xz":
            with lzma.open(source, "rb") as compressed, raw.open("wb") as target:
                shutil.copyfileobj(compressed, target, 1 << 20)
        else:
            shutil.copy2(source, raw)
        offset, size = root_partition(raw)
        root_sqfs = work / "root.squashfs"
        with raw.open("rb") as image, root_sqfs.open("wb") as target:
            image.seek(offset)
            copy_bytes(image, target, size)
        root = work / "root"
        subprocess.run(["unsquashfs", "-q", "-d", str(root), str(root_sqfs)], check=True)
        systems = transform_root(root, args.app.resolve(), args.retroarch.resolve(),
                                 args.cores.resolve() if args.cores else None)
        validate_linkage(root)
        repacked = work / "new.squashfs"
        subprocess.run(["mksquashfs", str(root), str(repacked), "-comp", "lz4", "-Xhc",
                        "-b", "131072", "-noappend", "-no-xattrs", "-all-root", "-quiet"],
                       check=True)
        need(repacked.stat().st_size <= size,
             f"root is {repacked.stat().st_size} bytes; plastron-root holds {size}")
        with raw.open("r+b") as image, repacked.open("rb") as replacement:
            image.seek(offset)
            zero = bytes(1 << 20)
            remaining = size
            while remaining:
                part = min(remaining, len(zero))
                image.write(zero[:part])
                remaining -= part
            image.seek(offset)
            shutil.copyfileobj(replacement, image, 1 << 20)
        staged_output = work / output.name
        if output.suffix == ".xz":
            with staged_output.open("wb") as target:
                subprocess.run(["xz", "-T0", "-6", "-c", str(raw)], stdout=target, check=True)
        else:
            shutil.copy2(raw, staged_output)
        shutil.move(staged_output, output)

    manifest = {
        "device": "trimui-brick",
        "base": "plastron",
        "base_image_sha256": sha256(source),
        "jukahub_binary_sha256": sha256(args.app / "JukaHub"),
        "retroarch_binary_sha256": sha256(args.retroarch / "retroarch"),
        "output_sha256": sha256(output),
        "systems": systems,
    }
    output.with_name(output.name + ".build.json").write_text(
        json.dumps(manifest, indent=2) + "\n", encoding="utf-8"
    )
    print(f"JukaHub OS image: {output}")
    print(f"SHA-256: {manifest['output_sha256']}")
    print(f"RetroArch systems: {', '.join(systems)}")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", type=Path, required=True,
                        help="plastron Brick sdcard.img or .img.xz")
    parser.add_argument("--app", type=Path, required=True,
                        help="built JukaHub-Trimui directory")
    parser.add_argument("--retroarch", type=Path, required=True,
                        help="AArch64 RetroArch directory containing retroarch")
    parser.add_argument("--cores", type=Path,
                        help="extra AArch64 libretro cores to add")
    parser.add_argument("--base-sha256", help="expected SHA-256 of the base image")
    parser.add_argument("--output", type=Path, required=True,
                        help="new JukaHub OS .img or .img.xz; must not exist")
    args = parser.parse_args()
    try:
        build_image(args)
    except (BuildError, OSError, subprocess.CalledProcessError, lzma.LZMAError) as error:
        print(f"build_image: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
