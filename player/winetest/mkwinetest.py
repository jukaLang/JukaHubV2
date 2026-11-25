#!/usr/bin/env python3
"""mkwinetest.py: build JukaHub's built-in Wine test (winetest.c) into a
64-bit Windows program, without a Windows toolchain.

Windows Apps installs the result on a handheld (player/windowsapps_winetest.go,
which embeds winetest.exe.gz), so a person can see at once that Wine runs:
one window that says so, with the Wine version, the host, the processor and
a moving ball, and a chime through Windows' sound.

How it is made, step by step:

 1. chime.h: a short two-note WAV, drawn here with a soft attack and a decay.
 2. The imports: every IMPORT(dll, ret, name, args) line of winetest.c becomes
    a slot in an import address table, laid out the way Windows' loader reads
    it (descriptors, lookup tables, the address table, hint/name entries, DLL
    names); a linker script points each __imp_<name> at its slot.
 3. gcc (any x86-64 Linux gcc) compiles winetest.c in the Microsoft x64
    calling convention (-mabi=ms), position independent (-fpie, hidden
    symbols), with no C runtime; ld links it as a static ELF at the program's
    Windows addresses (image base 0x140000000). The object is checked first:
    only PC-relative relocations, so nothing in the image depends on where it
    is loaded.
 4. The PE file is written from the linked sections: DOS stub, PE32+ headers
    (GUI subsystem, NX, dynamic base), .text / .rdata / .data / .idata and a
    .reloc section holding one empty block (the code needs no fixups, and an
    image with relocation data may be loaded at any address), plus the
    standard checksum.
 5. With --check, pe_smoke.c maps the program the way a loader does — at a
    different base than its own, imports resolved by name to stand-ins for
    user32, gdi32 and the rest — and runs it through a scripted session
    (paint, timer ticks, clicks, keys, Escape), counting GDI objects.

Run from anywhere (Python 3.8+, gcc and binutils):

    python3 player/winetest/mkwinetest.py [--check] [--out DIR]

It writes winetest.exe.gz beside this file (and winetest.exe with --keep).
Bytes are reproducible: no time stamps, a fixed gzip header.
"""

import argparse
import gzip
import io
import math
import os
import re
import shutil
import struct
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.path.join(HERE, "winetest.c")
SMOKE = os.path.join(HERE, "pe_smoke.c")

IMAGE_BASE = 0x140000000
SECT_ALIGN = 0x1000
FILE_ALIGN = 0x200
# A fixed stamp keeps the bytes reproducible (2026-11-25 00:00 UTC).
TIMESTAMP = 1795564800

CFLAGS = [
    "-std=c11", "-O2", "-mabi=ms", "-ffreestanding", "-fno-builtin", "-nostdlib",
    "-fno-stack-protector", "-fno-asynchronous-unwind-tables", "-fno-unwind-tables",
    "-fno-exceptions", "-fpie", "-mno-red-zone", "-fcf-protection=none",
    "-fvisibility=hidden", "-fno-jump-tables", "-fno-ident", "-fshort-wchar",
    "-Wall", "-Wextra", "-Wno-unused-parameter", "-Werror", "-Wframe-larger-than=3000",
]

# Relocation types a PC-relative image may carry (x86-64 ELF numbers).
PC_RELATIVE = {2: "R_X86_64_PC32", 4: "R_X86_64_PLT32", 9: "R_X86_64_GOTPCREL",
               41: "R_X86_64_GOTPCRELX", 42: "R_X86_64_REX_GOTPCRELX"}


def die(msg):
    sys.stderr.write("mkwinetest: " + msg + "\n")
    sys.exit(1)


# ── 1. The chime ─────────────────────────────────────────────────────────

def chime_wav(rate=11025, seconds=0.55):
    """Two bell-like notes (E6 then B6), each a sine with a quiet octave, a
    5 ms attack and an exponential decay; 16-bit mono PCM."""
    n = int(rate * seconds)
    notes = [(1318.51, 0.0, 0.22), (1975.53, 0.11, 0.30)]  # Hz, start s, decay s
    out = bytearray()
    for i in range(n):
        t = i / rate
        v = 0.0
        for freq, start, decay in notes:
            if t < start:
                continue
            u = t - start
            env = min(1.0, u / 0.005) * math.exp(-u / decay)
            v += env * (math.sin(2 * math.pi * freq * u) + 0.25 * math.sin(4 * math.pi * freq * u))
        s = max(-32767, min(32767, int(v * 0.42 * 32767)))
        out += struct.pack("<h", s)
    data = bytes(out)
    fmt = struct.pack("<HHIIHH", 1, 1, rate, rate * 2, 2, 16)
    body = b"WAVE" + b"fmt " + struct.pack("<I", len(fmt)) + fmt + b"data" + struct.pack("<I", len(data)) + data
    return b"RIFF" + struct.pack("<I", len(body)) + body


def chime_header(wav):
    lines = ["/* chime.h: made by mkwinetest.py; do not edit. */",
             "static const unsigned char chime_wav[%d] __attribute__((aligned(4))) = {" % len(wav)]
    for i in range(0, len(wav), 20):
        lines.append("\t" + ", ".join("%d" % b for b in wav[i:i + 20]) + ",")
    lines.append("};")
    return "\n".join(lines) + "\n"


# ── 2. The imports ───────────────────────────────────────────────────────

def read_imports(src):
    """[(dll, [names])] in the order the IMPORT lines first name each DLL."""
    order, funcs = [], {}
    for m in re.finditer(r"^IMPORT\((\w+),\s*[^,]+,\s*(\w+),", src, re.M):
        dll, name = m.group(1).lower() + ".dll", m.group(2)
        if dll not in funcs:
            order.append(dll)
            funcs[dll] = []
        if name in funcs[dll]:
            die("%s is imported twice" % name)
        funcs[dll].append(name)
    if not order:
        die("winetest.c has no IMPORT lines")
    return [(d, funcs[d]) for d in order]


def align(v, a):
    return (v + a - 1) // a * a


def idata_layout(imports):
    """Offsets inside .idata, which do not depend on where it is loaded:
    (size, {name: IAT slot offset}, the parts' offsets)."""
    ndll = len(imports)
    off = 20 * (ndll + 1)  # import descriptors and their terminator
    ilt = {}
    for dll, names in imports:
        ilt[dll] = off
        off += 8 * (len(names) + 1)
    iat_start = off
    iat, slots = {}, {}
    for dll, names in imports:
        iat[dll] = off
        for i, name in enumerate(names):
            slots[name] = off + 8 * i
        off += 8 * (len(names) + 1)
    iat_size = off - iat_start
    hints = {}
    for dll, names in imports:
        for name in names:
            hints[name] = off
            off = align(off + 2 + len(name) + 1, 2)
    dllname = {}
    for dll, _ in imports:
        dllname[dll] = off
        off = align(off + len(dll) + 1, 2)
    size = align(off, 8)
    return size, slots, {"ilt": ilt, "iat": iat, "iat_start": iat_start, "iat_size": iat_size,
                         "hints": hints, "dllname": dllname}


def idata_bytes(imports, rva, size, parts):
    buf = bytearray(size)
    for i, (dll, names) in enumerate(imports):
        struct.pack_into("<IIIII", buf, 20 * i, rva + parts["ilt"][dll], 0, 0,
                         rva + parts["dllname"][dll], rva + parts["iat"][dll])
        for j, name in enumerate(names):
            entry = rva + parts["hints"][name]
            struct.pack_into("<Q", buf, parts["ilt"][dll] + 8 * j, entry)
            struct.pack_into("<Q", buf, parts["iat"][dll] + 8 * j, entry)
            h = parts["hints"][name]
            struct.pack_into("<H", buf, h, 0)
            buf[h + 2:h + 2 + len(name)] = name.encode("ascii")
        d = parts["dllname"][dll]
        buf[d:d + len(dll)] = dll.encode("ascii")
    return bytes(buf)


def linker_script(slots):
    lines = [
        "ENTRY(winetest_entry)",
        "SECTIONS",
        "{",
        "  . = 0x%x;" % (IMAGE_BASE + SECT_ALIGN),
        "  .text : { *(.text.unlikely .text.unlikely.*) *(.text.startup .text.startup.*) *(.text .text.*) }",
        "  . = ALIGN(0x1000);",
        "  .rdata : { *(.rodata .rodata.*) }",
        "  . = ALIGN(0x1000);",
        "  .data : { *(.data .data.*) *(.bss .bss.*) *(COMMON) }",
        "  . = ALIGN(0x1000);",
        "  __idata_start = .;",
        "  /DISCARD/ : { *(.comment) *(.note .note.*) *(.eh_frame .eh_frame_hdr) *(.gnu.property) }",
        "}",
    ]
    for name, off in sorted(slots.items(), key=lambda kv: kv[1]):
        lines.append("__imp_%s = __idata_start + 0x%x;" % (name, off))
    return "\n".join(lines) + "\n"


# ── 3. Compile and link ──────────────────────────────────────────────────

def run(cmd, cwd):
    p = subprocess.run(cmd, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, universal_newlines=True)
    if p.returncode != 0:
        die("%s failed:\n%s" % (" ".join(cmd), p.stdout))
    return p.stdout


class Elf:
    """Just enough of an ELF64 little-endian reader."""

    def __init__(self, data):
        if data[:4] != b"\x7fELF" or data[4] != 2 or data[5] != 1:
            die("not an ELF64 little-endian file")
        self.data = data
        (self.entry,) = struct.unpack_from("<Q", data, 0x18)
        (shoff,) = struct.unpack_from("<Q", data, 0x28)
        shentsize, shnum, shstrndx = struct.unpack_from("<HHH", data, 0x3A)
        hdrs = [struct.unpack_from("<IIQQQQIIQQ", data, shoff + i * shentsize) for i in range(shnum)]
        strtab = hdrs[shstrndx]
        names = data[strtab[4]:strtab[4] + strtab[5]]
        self.sections = []
        for h in hdrs:
            name = names[h[0]:names.index(b"\0", h[0])].decode()
            self.sections.append({"name": name, "type": h[1], "flags": h[2], "addr": h[3], "offset": h[4],
                                  "size": h[5], "link": h[6], "info": h[7], "entsize": h[9]})

    def section(self, name):
        for s in self.sections:
            if s["name"] == name:
                return s
        return None

    def body(self, s):
        if s["type"] == 8:  # SHT_NOBITS
            return b"\0" * s["size"]
        return self.data[s["offset"]:s["offset"] + s["size"]]

    def symbols(self):
        out = {}
        for s in self.sections:
            if s["type"] != 2:  # SHT_SYMTAB
                continue
            strs = self.sections[s["link"]]
            for i in range(s["size"] // 24):
                st_name, _, _, _, st_value, _ = struct.unpack_from("<IBBHQQ", self.data, s["offset"] + 24 * i)
                if st_name:
                    end = self.data.index(b"\0", strs["offset"] + st_name)
                    out[self.data[strs["offset"] + st_name:end].decode()] = st_value
        return out

    def relocations(self):
        """[(section, type)] of every RELA entry."""
        out = []
        for s in self.sections:
            if s["type"] != 4:  # SHT_RELA
                continue
            for i in range(s["size"] // 24):
                _, info, _ = struct.unpack_from("<QQq", self.data, s["offset"] + 24 * i)
                out.append((s["name"], info & 0xFFFFFFFF))
        return out


# ── 4. The PE file ───────────────────────────────────────────────────────

DOS_STUB = (bytes.fromhex("0e1fba0e00b409cd21b8014ccd21") + b"This program cannot be run in DOS mode.\r\r\n$")


def pe_checksum(data, checksum_at):
    total = 0
    n = len(data)
    padded = data + (b"\0" if n % 2 else b"")
    for i in range(0, len(padded), 2):
        if checksum_at <= i < checksum_at + 4:
            continue
        total += padded[i] | (padded[i + 1] << 8)
        total = (total & 0xFFFF) + (total >> 16)
    total = (total & 0xFFFF) + (total >> 16)
    return (total + n) & 0xFFFFFFFF


def write_pe(sections, entry_rva, dirs):
    """sections: [(name, rva, data, vsize, characteristics)] in address order;
    dirs: {index: (rva, size)} of the data directories."""
    nsec = len(sections)
    pe_at = 0x80
    opt_at = pe_at + 4 + 20
    sec_at = opt_at + 240
    size_headers = align(sec_at + 40 * nsec, FILE_ALIGN)
    raw_at, file_off = [], size_headers
    for _, _, data, _, _ in sections:
        raw_at.append(file_off)
        file_off += align(len(data), FILE_ALIGN)
    last = sections[-1]
    size_image = align(last[1] + max(len(last[2]), last[3]), SECT_ALIGN)
    code = sum(align(len(d), FILE_ALIGN) for _, _, d, _, c in sections if c & 0x20)
    idata = sum(align(len(d), FILE_ALIGN) for _, _, d, _, c in sections if c & 0x40)

    out = bytearray(file_off)
    # DOS header and stub.
    struct.pack_into("<2sHHHHHHHHHHHHH", out, 0, b"MZ", 0x90, 3, 0, 4, 0, 0xFFFF, 0, 0xB8, 0, 0, 0, 0x40, 0)
    struct.pack_into("<I", out, 0x3C, pe_at)
    out[0x40:0x40 + len(DOS_STUB)] = DOS_STUB
    # PE signature and COFF header: x64, an executable, large-address aware.
    out[pe_at:pe_at + 4] = b"PE\0\0"
    struct.pack_into("<HHIIIHH", out, pe_at + 4, 0x8664, nsec, TIMESTAMP, 0, 0, 240, 0x0022)
    # The PE32+ optional header.
    o = opt_at
    struct.pack_into("<HBBIIIII", out, o, 0x20B, 2, 38, code, idata, 0, entry_rva, sections[0][1])
    struct.pack_into("<QII", out, o + 24, IMAGE_BASE, SECT_ALIGN, FILE_ALIGN)
    struct.pack_into("<HHHHHH", out, o + 40, 6, 0, 1, 0, 6, 0)
    struct.pack_into("<IIII", out, o + 52, 0, size_image, size_headers, 0)
    # GUI; high-entropy VA, dynamic base, NX, terminal-server aware.
    struct.pack_into("<HH", out, o + 68, 2, 0x0020 | 0x0040 | 0x0100 | 0x8000)
    struct.pack_into("<QQQQII", out, o + 72, 0x100000, 0x1000, 0x100000, 0x1000, 0, 16)
    for idx, (rva, size) in dirs.items():
        struct.pack_into("<II", out, o + 112 + 8 * idx, rva, size)
    # Section headers and bodies.
    for i, (name, rva, data, vsize, chars) in enumerate(sections):
        struct.pack_into("<8sIIIIIIHHI", out, sec_at + 40 * i, name.encode().ljust(8, b"\0"),
                         max(len(data), vsize), rva, align(len(data), FILE_ALIGN), raw_at[i], 0, 0, 0, 0, chars)
        out[raw_at[i]:raw_at[i] + len(data)] = data
    struct.pack_into("<I", out, o + 64, pe_checksum(bytes(out), o + 64))
    return bytes(out)


def check_pe(exe, imports):
    """Read the file back the way a loader would and compare."""
    (pe_at,) = struct.unpack_from("<I", exe, 0x3C)
    assert exe[:2] == b"MZ" and exe[pe_at:pe_at + 4] == b"PE\0\0"
    machine, nsec, _, _, _, opt_size, chars = struct.unpack_from("<HHIIIHH", exe, pe_at + 4)
    assert machine == 0x8664 and opt_size == 240 and chars & 2
    o = pe_at + 24
    assert struct.unpack_from("<H", exe, o)[0] == 0x20B
    entry = struct.unpack_from("<I", exe, o + 16)[0]
    size_image, size_headers, checksum = struct.unpack_from("<III", exe, o + 56)
    assert checksum == pe_checksum(exe, o + 64), "checksum"
    secs = []
    for i in range(nsec):
        name, vsize, rva, rawsize, rawptr = struct.unpack_from("<8sIIII", exe, o + 240 + 40 * i)
        secs.append((name.rstrip(b"\0").decode(), vsize, rva, rawsize, rawptr))
    prev_end = SECT_ALIGN
    for name, vsize, rva, rawsize, rawptr in secs:
        assert rva == prev_end, "section %s is not right after the one before it" % name
        assert rawptr % FILE_ALIGN == 0 and rawsize % FILE_ALIGN == 0
        prev_end = align(rva + vsize, SECT_ALIGN)
    assert size_image == prev_end and size_headers == secs[0][4]

    def at(rva):
        for _, vsize, srva, rawsize, rawptr in secs:
            if srva <= rva < srva + max(vsize, rawsize):
                return rawptr + rva - srva
        raise AssertionError("RVA %#x is in no section" % rva)

    text = [s for s in secs if s[0] == ".text"][0]
    assert text[2] <= entry < text[2] + text[1], "entry point outside .text"
    imp_rva, imp_size = struct.unpack_from("<II", exe, o + 112 + 8)
    found = []
    p = at(imp_rva)
    while True:
        ilt, _, _, name_rva, iat = struct.unpack_from("<IIIII", exe, p)
        if not (ilt or name_rva or iat):
            break
        n = at(name_rva)
        dll = exe[n:exe.index(b"\0", n)].decode()
        names, q, r = [], at(ilt), at(iat)
        while True:
            (e,) = struct.unpack_from("<Q", exe, q)
            (e2,) = struct.unpack_from("<Q", exe, r)
            assert e == e2, "lookup and address tables differ"
            if not e:
                break
            assert not e >> 63, "import by ordinal"
            h = at(e)
            names.append(exe[h + 2:exe.index(b"\0", h + 2)].decode())
            q, r = q + 8, r + 8
        found.append((dll, names))
        p += 20
    assert imp_size == 20 * (len(found) + 1)
    assert found == imports, "import table does not read back"
    reloc_rva, reloc_size = struct.unpack_from("<II", exe, o + 112 + 8 * 5)
    page, block = struct.unpack_from("<II", exe, at(reloc_rva))
    assert reloc_size == block == 12 and page % SECT_ALIGN == 0
    assert struct.unpack_from("<HH", exe, at(reloc_rva) + 8) == (0, 0), "relocation entries are not empty"
    return secs


def build(workdir, check, keep, outdir):
    with open(SRC, encoding="utf-8") as f:
        src = f.read()
    imports = read_imports(src)
    size, slots, parts = idata_layout(imports)
    with open(os.path.join(workdir, "chime.h"), "w", encoding="utf-8") as f:
        f.write(chime_header(chime_wav()))
    with open(os.path.join(workdir, "link.ld"), "w", encoding="utf-8") as f:
        f.write(linker_script(slots))
    shutil.copy(SRC, os.path.join(workdir, "winetest.c"))

    run(["gcc"] + CFLAGS + ["-c", "winetest.c", "-o", "winetest.o"], workdir)
    obj = Elf(open(os.path.join(workdir, "winetest.o"), "rb").read())
    bad = sorted({"%s type %d" % r for r in obj.relocations() if r[1] not in PC_RELATIVE})
    if bad:
        die("the object needs absolute relocations, so the image would not be position independent: " + ", ".join(bad))
    run(["ld", "-static", "-nostdlib", "--build-id=none", "-z", "norelro", "-z", "noexecstack",
         "--no-dynamic-linker", "-T", "link.ld", "winetest.o", "-o", "winetest.elf"], workdir)
    elf = Elf(open(os.path.join(workdir, "winetest.elf"), "rb").read())
    for s in elf.sections:
        if s["flags"] & 2 and s["size"] and s["name"] not in (".text", ".rdata", ".data"):
            die("the link made a section the PE writer does not carry: " + s["name"])
    syms = elf.symbols()
    idata_va = syms.get("__idata_start")
    if not idata_va:
        die("no __idata_start in the link")

    pe_secs = []
    chars = {".text": 0x60000020, ".rdata": 0x40000040, ".data": 0xC0000040}
    for name in (".text", ".rdata", ".data"):
        s = elf.section(name)
        if not s or not s["size"]:
            continue
        rva = s["addr"] - IMAGE_BASE
        if pe_secs:
            prev = pe_secs[-1]
            expect = align(prev[1] + max(len(prev[2]), prev[3]), SECT_ALIGN)
            if rva != expect:
                die("%s starts at %#x, expected %#x" % (name, rva, expect))
        pe_secs.append((name, rva, elf.body(s), s["size"], chars[name]))
    idata_rva = idata_va - IMAGE_BASE
    prev = pe_secs[-1]
    if idata_rva != align(prev[1] + max(len(prev[2]), prev[3]), SECT_ALIGN):
        die(".idata is not right after %s" % prev[0])
    pe_secs.append((".idata", idata_rva, idata_bytes(imports, idata_rva, size, parts), size, 0xC0000040))
    reloc_rva = align(idata_rva + size, SECT_ALIGN)
    # One block for the first page with two padding entries (type 0, ABSOLUTE).
    pe_secs.append((".reloc", reloc_rva, struct.pack("<IIHH", SECT_ALIGN, 12, 0, 0), 12, 0x42000040))
    dirs = {
        1: (idata_rva, 20 * (len(imports) + 1)),
        5: (reloc_rva, 12),
        12: (idata_rva + parts["iat_start"], parts["iat_size"]),
    }
    exe = write_pe(pe_secs, elf.entry - IMAGE_BASE, dirs)
    secs = check_pe(exe, imports)

    exe_path = os.path.join(workdir, "winetest.exe")
    with open(exe_path, "wb") as f:
        f.write(exe)
    if check:
        smoke(workdir, exe_path)
    buf = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=buf, compresslevel=9, mtime=0) as g:
        g.write(exe)
    gz = buf.getvalue()
    os.makedirs(outdir, exist_ok=True)
    target = os.path.join(outdir, "winetest.exe.gz")
    old = open(target, "rb").read() if os.path.exists(target) else None
    if old != gz:
        with open(target, "wb") as f:
            f.write(gz)
    if keep:
        shutil.copy(exe_path, os.path.join(outdir, "winetest.exe"))
    print("winetest.exe: %d bytes (%s), %d imports from %s; gzip %d bytes%s" % (
        len(exe), ", ".join("%s %d" % (n, v) for n, v, _, _, _ in secs),
        sum(len(n) for _, n in imports), ", ".join(d for d, _ in imports), len(gz),
        "" if old != gz else " (unchanged)"))


def smoke(workdir, exe_path):
    shutil.copy(SMOKE, os.path.join(workdir, "pe_smoke.c"))
    run(["gcc", "-std=gnu11", "-O1", "-g", "-Wall", "-Werror", "pe_smoke.c", "-o", "pe_smoke"], workdir)
    out = run([os.path.join(workdir, "pe_smoke"), exe_path], workdir)
    sys.stdout.write(out)


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--check", action="store_true", help="run the program under pe_smoke.c after building")
    ap.add_argument("--keep", action="store_true", help="also write winetest.exe beside the .gz")
    ap.add_argument("--out", default=HERE, help="where winetest.exe.gz goes (default: beside this script)")
    ap.add_argument("--work", help="build folder to keep (default: a temporary one)")
    args = ap.parse_args()
    if args.work:
        os.makedirs(args.work, exist_ok=True)
        build(args.work, args.check, args.keep, args.out)
        return
    with tempfile.TemporaryDirectory(prefix="winetest-") as work:
        build(work, args.check, args.keep, args.out)


if __name__ == "__main__":
    main()
