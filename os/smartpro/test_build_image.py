import contextlib
import importlib.util
import io
import os
from pathlib import Path
import shutil
import struct
import subprocess
import sys
import tempfile
import unittest
import uuid
import zlib


HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("smartpro_build_image", HERE / "build_image.py")
builder = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = builder  # dataclasses look their module up there
spec.loader.exec_module(builder)
SECTOR = 512
STAMP = 0x5F0A6C39


def write(path: Path, data: str | bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data.encode() if isinstance(data, str) else data)


def arm64_elf() -> bytes:
    data = bytearray(20)
    data[:6] = b"\x7fELF\x02\x01"
    struct.pack_into("<H", data, 18, 183)
    return bytes(data)


def add_sum(data: bytearray, at: int) -> None:
    struct.pack_into("<I", data, at, STAMP)
    total = sum(w for (w,) in struct.iter_unpack("<I", bytes(data))) & 0xFFFFFFFF
    struct.pack_into("<I", data, at, total)


def fdt(lcd_x: int, lcd_y: int) -> bytes:
    strings = b"lcd_x\0lcd_y\0"
    body = bytearray()

    def be(*values):
        body.extend(struct.pack(">" + "I" * len(values), *values))

    be(1); body.extend(b"\0\0\0\0")                       # root node ""
    be(1); body.extend(b"lcd0@1c0c000\0".ljust(16, b"\0"))  # lcd0
    be(3, 4, 0, lcd_x)
    be(3, 4, 6, lcd_y)
    be(2, 2, 9)
    struct_off = 40
    strings_off = struct_off + len(body)
    total = strings_off + len(strings)
    head = struct.pack(">10I", 0xD00DFEED, total, struct_off, strings_off, 40, 17, 16, 0,
                       len(strings), len(body))
    return head + bytes(body) + strings


def boot0() -> bytes:
    data = bytearray(64 * 1024)
    data[4:12] = b"eGON.BT0"
    struct.pack_into("<I", data, 16, len(data))
    data[100:110] = b"dram stuff"
    add_sum(data, 12)
    return bytes(data)


def package(panel=(1280, 720)) -> bytes:
    data = bytearray(256 * 1024)
    data[:13] = b"sunxi-package"
    struct.pack_into("<I", data, 16, 0x89119800)
    struct.pack_into("<I", data, 36, len(data))
    blob = fdt(*panel)
    data[4096:4096 + len(blob)] = blob
    add_sum(data, 20)
    return bytes(data)


# A small eMMC: GPT with 16 entries at LBA 2, boot0 at 8 KiB, boot package at
# 16400 KiB, partitions from 36 MiB (sector 73728) like Allwinner's layout.
FIRST = 73728
PARTS = [("boot-resource", 2048), ("env", 2048), ("boot", 4096), ("rootfs", 8192),
         ("private", 2048), ("UDISK", 20480)]


def emmc_lead(panel=(1280, 720)) -> tuple[bytes, list]:
    lead = bytearray(FIRST * SECTOR)
    lead[builder.BOOT0_OFFSET:builder.BOOT0_OFFSET + 64 * 1024] = boot0()
    pkg = package(panel)
    lead[builder.PACKAGE_OFFSET:builder.PACKAGE_OFFSET + len(pkg)] = pkg
    count, size = 16, 128
    table = bytearray(count * size)
    lba, layout = FIRST, []
    linux = uuid.UUID("0FC63DAF-8483-4772-8E79-3D69D8477DE4").bytes_le
    for i, (name, sectors) in enumerate(PARTS):
        struct.pack_into("<16s16sQQQ", table, i * size, linux, uuid.uuid4().bytes_le,
                         lba, lba + sectors - 1, 0)
        encoded = name.encode("utf-16le")
        table[i * size + 56:i * size + 56 + len(encoded)] = encoded
        layout.append((i + 1, name, lba, sectors))
        lba += sectors
    lead[2 * SECTOR:2 * SECTOR + len(table)] = table
    head = bytearray(92)
    struct.pack_into("<8sIIIIQQQQ16sQIII", head, 0, b"EFI PART", 0x10000, 92, 0, 0, 1,
                     lba + 100, 34, lba + 60, uuid.uuid4().bytes_le, 2, count, size,
                     zlib.crc32(table))
    struct.pack_into("<I", head, 16, zlib.crc32(head))
    lead[SECTOR:SECTOR + 92] = head
    lead[446 + 4] = 0xEE
    lead[510:512] = b"\x55\xaa"
    return bytes(lead), layout


def env_image(values: dict[str, str], size=0x20000, redundant=False) -> bytes:
    offset = 5 if redundant else 4
    body = b"".join(f"{k}={v}".encode() + b"\0" for k, v in values.items()) + b"\0"
    body = body.ljust(size - offset, b"\0")
    raw = bytearray(1 << 20)
    raw[offset:size] = body
    struct.pack_into("<I", raw, 0, zlib.crc32(body))
    if redundant:
        raw[4] = 1
    return bytes(raw)


STOCK_ENV = {
    "bootdelay": "0",
    "bootcmd": "run setargs_nand boot_normal",
    "setargs_nand": "setenv bootargs console=${console} root=${nand_root} init=${init} loglevel=${loglevel}",
    "setargs_mmc": "setenv bootargs console=${console} root=${mmc_root} rootwait init=${init} loglevel=${loglevel} rw",
    "boot_normal": "sunxi_flash read 45000000 boot;bootm 45000000",
    "console": "ttyS0,115200",
    "nand_root": "/dev/nandd",
    "mmc_root": "/dev/mmcblk0p4",
    "init": "/init",
    "loglevel": "8",
}
STOCK_CMDLINE = ("console=ttyS0,115200 root=/dev/mmcblk0p4 rootwait init=/init loglevel=8 rw "
                 "partitions=boot-resource@mmcblk0p1:env@mmcblk0p2:boot@mmcblk0p3:rootfs@mmcblk0p4")


def bootimg(ramdisk=b"", cmdline=b"") -> bytes:
    page = 2048
    kernel = b"\0" * 100 + b"Linux version 4.9.191 (x@y) #1 SMP" + b"\0" * 100
    head = bytearray(page)
    head[:8] = b"ANDROID!"
    struct.pack_into("<8I", head, 8, len(kernel), 0x40080000, len(ramdisk), 0x41000000, 0, 0,
                     0x40000100, page)
    head[64:64 + len(cmdline)] = cmdline
    pad = lambda b: b + bytes(-len(b) % page)
    return bytes(head) + pad(kernel) + pad(ramdisk)


class BootAreaTests(unittest.TestCase):
    def test_boot0_and_package_checks(self):
        lead, _ = emmc_lead()
        self.assertEqual(builder.check_boot0(lead), 64 * 1024)
        info = builder.check_package(lead)
        self.assertTrue(info["checksum_ok"])
        self.assertEqual(info["panel"], (1280, 720))
        broken = bytearray(lead)
        broken[builder.BOOT0_OFFSET + 200] ^= 1
        with self.assertRaisesRegex(builder.BuildError, "boot0 checksum"):
            builder.check_boot0(bytes(broken))

    def test_brick_device_tree_is_seen(self):
        lead, _ = emmc_lead(panel=(1024, 768))
        self.assertEqual(builder.check_package(lead)["panel"], (1024, 768))

    def test_gpt_layout_keeps_stock_numbers_and_sectors(self):
        lead, layout = emmc_lead()
        gpt = builder.parse_gpt(lead)
        self.assertEqual([(p.index + 1, p.name, p.first, p.sectors) for p in gpt.parts], layout)
        parts, total = builder.layout(gpt, "rootfs", 20 << 20, 8 * 10**9)
        names = [(p.index + 1, p.name) for p in parts]
        self.assertEqual(names, [(1, "boot-resource"), (2, "env"), (3, "boot"), (4, "rootfs"),
                                 (5, "JUKAHUB")])
        root = parts[3]
        self.assertEqual(root.first, gpt.parts[3].first)
        self.assertGreaterEqual(root.sectors * SECTOR, 20 << 20)
        self.assertEqual(parts[4].first % 2048, 0)
        self.assertLess(total * SECTOR, 8 * 10**9)
        for kept, stock in zip(parts[:3], gpt.parts[:3]):
            self.assertEqual((kept.first, kept.last), (stock.first, stock.last))
            self.assertNotEqual(kept.unique, stock.unique)

    def test_assembled_image_reads_back(self):
        lead, _ = emmc_lead()
        gpt = builder.parse_gpt(lead)
        with tempfile.TemporaryDirectory() as temp:
            tmp = Path(temp)
            parts, total = builder.layout(gpt, "rootfs", 4 << 20, 2 * 10**9)
            images = {}
            for part in parts:
                images[part.name] = tmp / f"{part.name}.bin"
                write(images[part.name], part.name.encode() * 10)
            raw = tmp / "card.img"
            builder.assemble_image(raw, lead, gpt, parts, images, total)
            builder.verify_image(raw, lead, 64 * 1024, 256 * 1024, parts)
            with raw.open("rb") as image:
                image.seek(parts[3].first * SECTOR)
                self.assertEqual(image.read(6), b"rootfs")
                image.seek(parts[4].first * SECTOR)
                self.assertEqual(image.read(7), b"JUKAHUB")
            self.assertEqual(raw.stat().st_size, total * SECTOR)
            # The Linux kernel's own GPT reader agrees, if this host has sfdisk.
            if shutil.which("sfdisk"):
                out = subprocess.run(["sfdisk", "--json", str(raw)], capture_output=True, text=True)
                self.assertEqual(out.returncode, 0, out.stderr)
                self.assertIn('"name":"JUKAHUB"', out.stdout.replace(" ", ""))
                self.assertIn(parts[3].partuuid.upper(), out.stdout.upper())


class EnvTests(unittest.TestCase):
    def test_env_rewrite_points_root_at_card(self):
        raw = env_image(STOCK_ENV)
        env = builder.parse_env(raw)
        builder.check_env_controls_root(env, STOCK_CMDLINE)
        new = builder.env_bytes(builder.rewrite_env(env, "1234-uuid", "squashfs"), raw)
        reread = builder.parse_env(new)
        args = builder.expand(reread.values["setargs_mmc"].split("bootargs ", 1)[1], reread.values)
        words = args.split()
        self.assertIn("root=PARTUUID=1234-uuid", words)
        self.assertIn("init=/jukahub/init", words)
        self.assertIn("rootfstype=squashfs", words)
        self.assertIn("ro", words)
        self.assertNotIn("rw", words)
        self.assertEqual(words.count("rootwait"), 1)
        nand = reread.values["setargs_nand"]
        self.assertIn("root=PARTUUID=1234-uuid", nand)
        self.assertEqual(reread.values["boot_normal"], STOCK_ENV["boot_normal"])
        self.assertEqual(len(new), len(raw))

    def test_redundant_env(self):
        raw = env_image(STOCK_ENV, size=0x10000, redundant=True)
        env = builder.parse_env(raw)
        self.assertEqual((env.data_offset, env.size), (5, 0x10000))
        new = builder.env_bytes(builder.rewrite_env(env, "u", "ext4"), raw)
        self.assertEqual(new[4], 1)
        self.assertIn("rootfstype=ext4", builder.parse_env(new).values["setargs_mmc"])

    def test_refuses_bootloader_that_picks_root_itself(self):
        env = builder.parse_env(env_image(STOCK_ENV))
        with self.assertRaisesRegex(builder.BuildError, "picks the system itself"):
            builder.check_env_controls_root(env, STOCK_CMDLINE.replace("mmcblk0p4 rootwait", "mmcblk0p5 rootwait"))
        with self.assertRaisesRegex(builder.BuildError, "twice"):
            builder.check_env_controls_root(env, STOCK_CMDLINE + " root=/dev/mmcblk0p5")

    def test_bad_env_checksum(self):
        raw = bytearray(env_image(STOCK_ENV))
        raw[40] ^= 1
        with self.assertRaisesRegex(builder.BuildError, "valid checksum"):
            builder.parse_env(bytes(raw))


class BootImageTests(unittest.TestCase):
    def test_ramdisk_is_dropped(self):
        raw = bootimg(ramdisk=b"R" * 5000)
        info = builder.check_bootimg(raw, "4.9.191")
        self.assertTrue(info["kernel_matches"])
        self.assertEqual(info["ramdisk_size"], 5000)
        out = builder.strip_ramdisk(raw, info)
        self.assertEqual(len(out), len(raw))
        self.assertEqual(builder.check_bootimg(out, "4.9.191")["ramdisk_size"], 0)
        self.assertNotIn(b"RRRR", out)

    def test_boot_image_cmdline_follows(self):
        raw = bootimg(cmdline=b"init=/pseudo_init rw")
        out = builder.rewrite_bootimg_cmdline(raw, "abc", "squashfs")
        self.assertIn("init=/jukahub/init", builder.bootimg_cmdline(out))
        self.assertNotIn("rw", builder.bootimg_cmdline(out).split())
        plain = bootimg(cmdline=b"quiet")
        self.assertEqual(builder.rewrite_bootimg_cmdline(plain, "abc", "squashfs"), plain)


class PlanTests(unittest.TestCase):
    def test_modules_daemons_and_gpio(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            write(root / "lib/modules/4.9.191/kernel/pvrsrvkm.ko", b"")
            write(root / "lib/modules/4.9.191/kernel/aic8800-fdrv.ko", b"")
            write(root / "lib/modules/4.9.191/kernel/exfat.ko", b"")
            write(root / "usr/trimui/bin/trimui_inputd", b"")
            write(root / "usr/trimui/bin/keymon", b"")
            write(root / "usr/trimui/bin/MainUI", b"")
            write(root / "usr/sbin/wpa_supplicant", b"")
            modules = ("aic8800_fdrv 100 0 - Live 0x0\nexfat 100 1 - Live 0x0\n"
                       "pvrsrvkm 100 2 - Live 0x0\nmissing_mod 1 0 - Live 0x0\n")
            # cfg80211 is used by an early module, so it cannot wait for the radios.
            dep = "early_user 1 0 - Live 0x0\ncfg80211 1 1 early_user, Live 0x0\n"
            write(root / "lib/modules/4.9.191/cfg80211.ko", b"")
            write(root / "lib/modules/4.9.191/early_user.ko", b"")
            e2, r2, _ = builder.module_plan(dep, root, "4.9.191")
            self.assertEqual(e2, ["/lib/modules/4.9.191/cfg80211.ko", "/lib/modules/4.9.191/early_user.ko"])
            self.assertEqual(r2, [])
            early, radio, missing = builder.module_plan(modules, root, "4.9.191")
            self.assertEqual(early, ["/lib/modules/4.9.191/kernel/pvrsrvkm.ko",
                                     "/lib/modules/4.9.191/kernel/exfat.ko"])
            self.assertEqual(radio, ["/lib/modules/4.9.191/kernel/aic8800-fdrv.ko"])
            self.assertEqual(missing, ["missing_mod"])
            us = "\x1f"
            procs = "\n".join([
                f"1\t/sbin/procd\t/\t\t/sbin/procd",
                f"300\t/mnt/SDCARD/System/bin/trimui_inputd\t/mnt/SDCARD\t/usr/trimui/lib\t./trimui_inputd{us}-d",
                f"310\t/usr/trimui/bin/keymon\t/usr/trimui/bin\t/usr/trimui/lib\tkeymon",
                f"320\t/usr/trimui/bin/MainUI\t/usr/trimui/bin\t\t./MainUI",
                f"330\t/usr/sbin/wpa_supplicant\t/\t\twpa_supplicant{us}-B{us}-c{us}/etc/wifi/wpa supplicant.conf",
                f"340\t/bin/busybox\t/\t\tsh{us}/usr/trimui/bin/runtrimui.sh",
                f"350\t/bin/busybox\t/\t\tudhcpc{us}-i{us}wlan0",
                f"360\t/usr/trimui/bin/updater\t/\t\tupdater",
            ])
            write(root / "bin/busybox", b"")
            lines, ignored = builder.daemon_plan(procs, root, set(), {"keymon"})
            self.assertEqual(lines[0], "hardware|/usr/trimui/bin|/usr/trimui/lib|/usr/trimui/bin/trimui_inputd -d")
            self.assertEqual(lines[1], "radio|/||/usr/sbin/wpa_supplicant -B -c '/etc/wifi/wpa supplicant.conf'")
            self.assertEqual(lines[2], "radio|/||udhcpc -i wlan0")
            self.assertEqual(len(lines), 3)
            self.assertIn("updater", ignored)  # not on the allowlist: never started unasked
            self.assertIn("MainUI", ignored)
            self.assertIn("keymon", ignored)
            self.assertIn("procd", ignored)
            # Not running at dump time: the stick daemon is still started.
            lines, _ = builder.daemon_plan("", root, set(), set())
            self.assertEqual(lines, ["hardware|/usr/trimui/bin|/usr/trimui/lib|/usr/trimui/bin/trimui_inputd"])
        self.assertEqual(builder.gpio_plan("110 out 1\n12 in 0\nbad line\n243 out x\n"),
                         ["110 out 1", "12 in 0"])

    def test_filesystem_choices(self):
        fs = "nodev\tsysfs\nnodev\tdevtmpfs\n\text4\n\tsquashfs\n\tvfat\n\texfat\n"
        self.assertEqual(builder.choose_root_fs(fs, [], "squashfs", "xz"), "squashfs")
        self.assertEqual(builder.choose_root_fs(fs, [], "ext4", "xz"), "ext4")
        self.assertEqual(builder.choose_root_fs(fs, [], "squashfs", None), "ext4")
        self.assertEqual(builder.choose_root_fs(fs, ["squashfs"], "squashfs", "xz"), "ext4")
        head = b"hsqs" + bytes(16) + struct.pack("<H", 4)
        self.assertEqual(builder.squashfs_compressor(head, None), "xz")
        self.assertEqual(builder.squashfs_compressor(None, "CONFIG_SQUASHFS=y\nCONFIG_SQUASHFS_ZLIB=y\n"), "gzip")
        self.assertIsNone(builder.squashfs_compressor(b"\0" * 64, None))
        self.assertEqual(builder.choose_data_fs(fs, True), "exfat")
        self.assertEqual(builder.choose_data_fs(fs, False), "vfat")
        with self.assertRaises(builder.BuildError):
            builder.choose_root_fs("nodev\tproc\n", [], "squashfs", "xz")
        self.assertEqual(builder.parse_size("64G"), 64 * 10**9)

    def test_logo_matches_stock_depth(self):
        width, height = 4, 2
        ours = bytearray(54)
        ours[:2] = b"BM"
        struct.pack_into("<IIiiHHI", ours, 10, 54, 40, width, height, 1, 24, 0)
        pixels = bytes(range(width * 3)) * height
        ours += pixels
        stock = bytearray(ours[:54])
        struct.pack_into("<H", stock, 28, 32)
        stock += bytes(width * height * 4)
        out = builder.bmp_like(bytes(stock), bytes(ours))
        self.assertEqual(len(out), 54 + width * height * 4)
        self.assertEqual(out[54:58], bytes([0, 1, 2, 255]))
        self.assertIsNone(builder.bmp_like(bytes(stock[:18]) + struct.pack("<ii", 8, 8) + bytes(stock[26:]),
                                           bytes(ours)))
        real = builder.BOOTLOGO.read_bytes()
        self.assertEqual(builder.bmp_info(real)[:3], (1280, 720, 24))


class RootAndCardTests(unittest.TestCase):
    def fake_board(self, tmp: Path) -> Path:
        board = tmp / "board"
        write(board / "modules.txt", "pvrsrvkm 1 0 - Live 0x0\n")
        write(board / "procs.txt", "")
        write(board / "gpio.txt", "110 out 1\n")
        write(board / "udisk/joypad.config", "x=1\n")
        return board

    def test_root_gets_init_and_marker(self):
        with tempfile.TemporaryDirectory() as temp:
            tmp = Path(temp)
            root = tmp / "root"
            for cmd in builder.REQUIRED_COMMANDS:
                write(root / "bin" / cmd, b"")
            write(root / "lib/modules/4.9.191/pvrsrvkm.ko", b"")
            write(root / "etc/hostname", "TrimUI\n")
            plan = builder.transform_root(root, self.fake_board(tmp), {"KERNEL": "4.9.191"},
                                          "exfat", set(), set())
            self.assertTrue(os.access(root / "jukahub/init", os.X_OK))
            env = (root / "jukahub/board.env").read_text()
            self.assertIn("DATA_FSTYPE=exfat", env)
            self.assertIn("MODULES=/lib/modules/4.9.191/pvrsrvkm.ko", env)
            self.assertEqual((root / "jukahub/gpio.list").read_text(), "110 out 1\n")
            marker = (root / "etc/jukahub-os-release").read_text()
            self.assertIn("JUKAHUB_OS_NATIVE=1\n", marker)
            self.assertIn("BASE=smartpro-stock-kernel", marker)
            self.assertTrue((root / "mnt/SDCARD").is_dir())
            self.assertFalse(plan["wifi"])

    def test_board_from_another_model_is_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            board = Path(temp) / "board"
            for name in ("COMPLETE", "cmdline.txt", "modules.txt", "filesystems.txt",
                         "mountinfo.txt", "procs.txt", "partitions.txt", "lead.bin", "rootfs.tar"):
                write(board / name, b"")
            facts = "FORMAT=jukahub-board-v1\nPANEL=1280x720\nMACHINE=aarch64\nLEAD_SECTORS=0\n"
            write(board / "facts.txt", facts + "DEVICE=trimui-smart-pro\nMODEL=TG5040\nKERNEL=4.9.191\n")
            self.assertEqual(builder.load_board(board)["MODEL"], "TG5040")
            write(board / "facts.txt", facts + "DEVICE=trimui-smart-pro-s\nMODEL=TG5050\nKERNEL=5.15.147\n")
            with self.assertRaises(builder.BuildError):
                builder.load_board(board)
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(builder.load_board(board, True)["MODEL"], "TG5050")

    def test_root_without_commands_is_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            tmp = Path(temp)
            with self.assertRaisesRegex(builder.BuildError, "lacks"):
                builder.transform_root(tmp / "root", self.fake_board(tmp), {"KERNEL": "4.9.191"},
                                       "vfat", set(), set())

    def test_card_payload(self):
        with tempfile.TemporaryDirectory() as temp:
            tmp = Path(temp)
            app, card, stock = tmp / "app", tmp / "card", tmp / "stock"
            write(app / "JukaHub", arm64_elf())
            write(app / "native-os-capability.txt", "plastron-brick-v1\nsmartpro-stock-kernel-v1\n")
            write(stock / "RetroArch/ra64.trimui", arm64_elf())
            write(stock / "Emus/GBA/launch.sh", "#!/bin/sh\n")
            os.symlink("ra64.trimui", stock / "RetroArch/link")
            card.mkdir()
            info = builder.stage_card(card, app, self.fake_board(tmp), stock)
            self.assertTrue((card / "Apps/JukaHub/JukaHub").is_file())
            self.assertTrue((card / "RetroArch/ra64.trimui").is_file())
            self.assertTrue((card / ".userdata/smartpro/udisk/joypad.config").is_file())
            self.assertIn("RetroArch", info["stock_card_folders"])
            self.assertIn("link", info["not_copied"])
            (app / "native-os-capability.txt").write_text("plastron-brick-v1\n")
            with self.assertRaisesRegex(builder.BuildError, "predates"):
                builder.stage_card(tmp / "card2", app, self.fake_board(tmp), None)


class ScriptTests(unittest.TestCase):
    def test_scripts_parse(self):
        for name in ("init", "launch.sh", "dump-board.sh"):
            subprocess.run(["sh", "-n", str(HERE / name)], check=True)

    def test_init_finds_card_partition_and_starts_daemons(self):
        with tempfile.TemporaryDirectory() as temp:
            tmp = Path(temp)
            sys_ = tmp / "sys"
            devices = tmp / "devices"
            for disk, kind, parts in (("mmcblk0", "MMC", {"mmcblk0p5": "JUKAHUB"}),
                                      ("mmcblk1", "SD", {"mmcblk1p4": "rootfs", "mmcblk1p5": "JUKAHUB"})):
                write(devices / disk / "device/type", kind + "\n")
                for part, name in parts.items():
                    write(devices / disk / part / "uevent", f"MAJOR=179\nPARTNAME={name}\n")
                    (sys_ / "class/block").mkdir(parents=True, exist_ok=True)
                    os.symlink(devices / disk / part, sys_ / "class/block" / part)
            j = tmp / "j"
            out = tmp / "out.txt"
            write(j / "daemons.list",
                  f"hardware|/|/opt/lib|sh -c 'echo \"$LD_LIBRARY_PATH $0\" > {out}' 'a|b'\n"
                  "radio|/||false\n")
            script = (f". {HERE / 'init'}; card_part JUKAHUB; start_daemons hardware; wait")
            env = dict(os.environ, JUKAHUB_INIT_TEST="1", JUKAHUB_SYS=str(sys_), JUKAHUB_ROOT=str(j))
            result = subprocess.run(["sh", "-c", script], capture_output=True, text=True, env=env)
            self.assertEqual(result.stdout.strip(), "/dev/mmcblk1p5", result.stderr)
            self.assertEqual(out.read_text().strip(), "/opt/lib a|b")


if __name__ == "__main__":
    unittest.main()
