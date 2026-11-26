#!/usr/bin/env python3
"""Build JukaHub's small Wine Virtual Keyboard PE without a Windows SDK.

Uses the existing Wine Test PE writer without changing its source or output.
Run with Linux x86-64 Python 3, gcc, and binutils::

    python3 player/winekeyboard/mkkeyboard.py
    python3 player/winekeyboard/mkkeyboard.py --proof --keep --out /tmp/vk-proof

The default writes keyboard.exe.gz next to this script. ``--proof`` makes a
temporary test build which opens the Smart Pro's Notepad++ if
JUKAHUB_VK_TARGET is unset; production builds require that environment value.
``--keep`` also saves the uncompressed PE.
"""

import argparse
import contextlib
import gzip
import io
import pathlib
import sys
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
sys.dont_write_bytecode = True
sys.path.insert(0, str(HERE.parent / "winetest"))
import mkwinetest as pe  # noqa: E402


def write_if_changed(path, data):
    if not path.exists() or path.read_bytes() != data:
        path.write_bytes(data)


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--proof", action="store_true", help="enable the local Notepad++ proof target")
    parser.add_argument("--keep", action="store_true", help="keep an uncompressed keyboard.exe beside the gzip")
    parser.add_argument("--out", type=pathlib.Path, default=HERE, help="output folder")
    args = parser.parse_args()

    # The PE writer reads SRC at build time and names its temporary artifact
    # winetest.exe. Keep all its output private, then publish keyboard.exe.gz.
    pe.SRC = str(HERE / "keyboard.c")
    if args.proof:
        pe.CFLAGS = pe.CFLAGS + ["-DVK_PROOF"]
    with tempfile.TemporaryDirectory(prefix="wine-keyboard-") as work:
        with contextlib.redirect_stdout(io.StringIO()):
            pe.build(work, False, False, work)
        workdir = pathlib.Path(work)
        args.out.mkdir(parents=True, exist_ok=True)
        gz = (workdir / "winetest.exe.gz").read_bytes()
        write_if_changed(args.out / "keyboard.exe.gz", gz)
        if args.keep:
            write_if_changed(args.out / "keyboard.exe", (workdir / "winetest.exe").read_bytes())
        print("keyboard.exe.gz: %d bytes, PE image %d bytes%s" %
              (len(gz), len(gzip.decompress(gz)), " (proof)" if args.proof else ""))


if __name__ == "__main__":
    main()
