import importlib.util
from pathlib import Path
import struct
import tempfile
import unittest
import zlib


HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("build_image", HERE / "build_image.py")
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


def write(path: Path, data: str | bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(data.encode() if isinstance(data, str) else data)


def arm64_elf() -> bytes:
    data = bytearray(20)
    data[:6] = b"\x7fELF\x02\x01"
    struct.pack_into("<H", data, 18, 183)
    return bytes(data)


class PlastronImageTests(unittest.TestCase):
    def fixture(self, tmp: Path) -> tuple[Path, Path, Path]:
        root, app, ra = tmp / "root", tmp / "app", tmp / "retroarch"
        write(root / "usr/share/tortos/card/TortOS/cores/mgba_libretro.so", arm64_elf())
        write(root / "usr/share/tortos/card/Licenses/plastron.md", "plastron notice")
        write(root / "usr/share/tortos/card/Licenses/plastron-packages.md", "packages")
        write(root / "usr/share/tortos/card/Licenses/TortOS.md", "core notices")
        write(root / "usr/lib/brick/backlight", "stub")
        write(root / "usr/share/splash/frames.bin", b"old splash")
        write(root / "etc/inittab",
              "::sysinit:/etc/init.d/tortos start\n"
              "::restart:/usr/sbin/tortos-update\n"
              "::shutdown:/etc/init.d/tortos stop\n")
        write(root / "etc/init.d/datapart",
              "name=tortos\nmkfs.exfat -c 32K -L TORTOS\ngames=$(part tortos)\n")
        write(root / "etc/init.d/bluetooth",
              "BTPLAYER=/mnt/SDCARD/TortOS/btplayer\n"
              "\t[ -x $BTPLAYER ] && daemon btplayer $BTPLAYER\n"
              "\tstopd btplayer\n")
        write(root / "etc/init.d/tortos", "old service")
        write(root / "usr/sbin/tortos-update", "old updater")
        write(app / "JukaHub", arm64_elf())
        write(app / "native-os-capability.txt", "plastron-brick-v1\n")
        write(app / "launch.sh", "old app launcher")
        write(ra / "retroarch", arm64_elf())
        return root, app, ra

    def test_replaces_tortos_service_and_wires_retroarch(self):
        with tempfile.TemporaryDirectory() as temp:
            root, app, ra = self.fixture(Path(temp))
            systems = builder.transform_root(root, app, ra)
            self.assertIn("Game Boy", systems)
            self.assertNotIn("PlayStation", systems)
            self.assertFalse((root / "usr/share/tortos").exists())
            self.assertFalse((root / "etc/init.d/tortos").exists())
            self.assertFalse((root / "usr/sbin/tortos-update").exists())
            self.assertIn("jukahub start", (root / "etc/inittab").read_text())
            self.assertNotIn("tortos-update", (root / "etc/inittab").read_text())
            self.assertIn("-L JUKAHUB", (root / "etc/init.d/datapart").read_text())
            self.assertIn("games=$(part tortos)", (root / "etc/init.d/datapart").read_text())
            self.assertIn("JUKAHUB_OS_NATIVE=1", (root / "etc/jukahub-os-release").read_text())
            card = root / "usr/share/jukahub/card"
            cfg = (card / "RetroArch/retroarch.cfg").read_text()
            self.assertIn('system_directory = "/mnt/SDCARD/Bios"', cfg)
            self.assertIn('savefile_directory = "/mnt/SDCARD/Saves/RetroArch"', cfg)
            self.assertTrue((card / "RetroArch/.retroarch/cores/mgba_libretro.so").is_file())
            self.assertIn("--config", (card / "Emus/Game Boy/launch.sh").read_text())
            self.assertTrue((card / "Licenses/Libretro-cores.md").is_file())

    def test_rejects_older_jukahub_package(self):
        with tempfile.TemporaryDirectory() as temp:
            root, app, ra = self.fixture(Path(temp))
            (app / "native-os-capability.txt").unlink()
            with self.assertRaisesRegex(builder.BuildError, "predates native OS"):
                builder.transform_root(root, app, ra)

    def test_finds_root_partition_by_gpt_name(self):
        with tempfile.TemporaryDirectory() as temp:
            image = Path(temp) / "card.img"
            data = bytearray(64 * builder.SECTOR)
            data[512:520] = b"EFI PART"
            struct.pack_into("<QII", data, 512 + 72, 2, 2, 128)
            entry = 2 * builder.SECTOR + 128
            struct.pack_into("<QQ", data, entry + 32, 8, 23)
            name = "plastron-root".encode("utf-16le")
            data[entry + 56:entry + 56 + len(name)] = name
            image.write_bytes(data)
            self.assertEqual(builder.root_partition(image), (8 * 512, 16 * 512))

    def test_splash_is_plastron_brick_format(self):
        data = (HERE / "frames-brick.bin").read_bytes()
        magic, width, height, frames, fps, offset = struct.unpack_from("<4sHHHHI", data)
        self.assertEqual((magic, width, height, frames, fps, offset),
                         (b"TSPZ", 1024, 768, 1, 60, 16))
        first, rows, length = struct.unpack_from("<HHI", data, offset)
        self.assertEqual((first, rows), (0, 768))
        self.assertEqual(len(zlib.decompress(data[offset + 8:offset + 8 + length])),
                         1024 * 768 * 4)


if __name__ == "__main__":
    unittest.main()
