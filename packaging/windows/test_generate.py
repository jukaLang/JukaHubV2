#!/usr/bin/env python3
"""Local checks for the Windows packaging generator (DESK-4)."""

import json
import tempfile
import unittest
from pathlib import Path

from generate import ASSET, build, read_hash, version_parts


class GenerateTest(unittest.TestCase):
    def test_version(self):
        self.assertEqual(version_parts("v0.4.0"), ("0", "4", "0", "0.4.0"))
        self.assertEqual(version_parts("1.2.3"), ("1", "2", "3", "1.2.3"))
        with self.assertRaises(ValueError):
            version_parts("latest")

    def test_hash(self):
        with tempfile.TemporaryDirectory() as tmp:
            sums = Path(tmp) / "SHA256SUMS"
            sums.write_text(f"abc  {ASSET}\n" + "a" * 64 + f"  {ASSET}\n", encoding="utf-8")
            self.assertEqual(read_hash(sums), "a" * 64)
            self.assertEqual(read_hash(None), "")

    def test_build_outputs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            cfg = root / "jukaconfig.json"
            cfg.write_text('{"Version":"v0.4.0"}', encoding="utf-8")
            sums = root / "SHA256SUMS"
            sums.write_text("b" * 64 + f"  {ASSET}\n", encoding="utf-8")
            written = build(cfg, root / "generated", sums, r"C:\staging")
            names = {p.name for p in written}
            self.assertEqual(names, {
                "versioninfo.rc", "JukaHub.iss",
                "JukaHub.JukaHub.0.4.0.installer.yaml",
                "JukaHub.JukaHub.0.4.0.locale.en-US.yaml",
                "JukaHub.JukaHub.0.4.0.version.yaml", "JukaHub.json",
            })
            rc = (root / "generated" / "versioninfo.rc").read_text(encoding="utf-8")
            self.assertIn("FILEVERSION 0,4,0,0", rc)
            self.assertIn('"FileVersion", "0.4.0"', rc)
            iss = (root / "generated" / "JukaHub.iss").read_text(encoding="utf-8")
            self.assertNotIn("$version", iss)
            self.assertNotIn("$$", iss)
            scoop = json.loads((root / "generated" / "JukaHub.json").read_text(encoding="utf-8"))
            self.assertEqual(scoop["hash"], "b" * 64)
            self.assertEqual(scoop["version"], "0.4.0")
            for name in names:
                if name.endswith(".yaml"):
                    text = (root / "generated" / name).read_text(encoding="utf-8")
                    self.assertNotIn("$version", text)
                    self.assertIn("$schema=https://aka.ms/", text)


if __name__ == "__main__":
    unittest.main()
