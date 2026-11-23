#!/usr/bin/env python3
"""Survey what each libretro buildbot core for linux/aarch64 needs to load.

The handheld cannot tell whether a buildbot nightly loads on it without
downloading it: the buildbot's index states a date and a CRC32 per core, nothing
about the libraries a build links or the symbol versions it needs (in November
2026: GLIBC up to 2.35 and GLIBCXX, the C++ library's versions, up to 3.4.30).
This script downloads every core the index lists, reads each binary's ELF
headers - DT_NEEDED and .gnu.version_r, the same two tables the handheld's own
load check reads (player/elfcompat.go) - and writes them to one JSON file, which
.github/workflows/retroarch-cores.yml publishes as buildbot-needs.json in the
cores-aarch64 release. JukaHub (player/corecompat.go) checks every entry against
the handheld's firmware libraries and its private glibc 2.39 runtime, and offers
only the cores that load there.

  survey-cores.py --out FILE [--index URL] [--base URL] [--jobs N] [--limit N]

The output, sorted for stable diffs:

  {"format": 1, "generated": "<UTC RFC 3339>", "index": "<url>",
   "cores": {"<core>": {"date": "...", "crc32": "...", "file": "<core>_libretro.so",
                        "arch": "aarch64", "needed": ["libc.so.6", ...],
                        "versions": {"libc.so.6": ["GLIBC_2.17", ...], ...}},
             ...},
   "failed": {"<core>": "<why>"}}

Weak version requirements are left out (the loader only warns about them), as
the handheld's check leaves them out. Only the standard library is used, so the
runner needs nothing installed. A core that cannot be read is listed under
"failed" and does not stop the survey; no core read at all fails it.
"""

import argparse
import concurrent.futures
import datetime
import io
import json
import os
import re
import struct
import sys
import time
import urllib.error
import urllib.request
import zipfile

INDEX_URL = "https://buildbot.libretro.com/nightly/linux/aarch64/latest/.index-extended"
BASE_URL = "https://buildbot.libretro.com/nightly/linux/aarch64/latest/"
MAX_ZIP = 192 << 20   # the handheld's own download limit is lower; a sanity bound
MAX_BINARY = 256 << 20
SHT_DYNAMIC = 6
SHT_GNU_VERNEED = 0x6FFFFFFE
DT_NEEDED = 1
VER_FLG_WEAK = 0x2
MACHINES = {183: "aarch64", 40: "arm", 62: "x86_64", 3: "x86"}
LINE_RE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
CORE_RE = re.compile(r"^([A-Za-z0-9_.+-]+)_libretro\.so\.zip$")


def fetch(url, limit, timeout=120, tries=3):
    """The body at url, at most limit bytes, with a few retries."""
    last = None
    for attempt in range(tries):
        if attempt:
            time.sleep(2 * attempt * attempt)
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "JukaHub-core-survey/1"})
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                data = resp.read(limit + 1)
            if len(data) > limit:
                raise ValueError("more than %d bytes" % limit)
            return data
        except (urllib.error.URLError, OSError, ValueError) as err:
            last = err
    raise RuntimeError("%s: %s" % (url, last))


def parse_index(text):
    """{core: (date, crc32, file)} from the buildbot's index, as JukaHub reads
    it (player/emulatorupdates.go parseCoreIndex): three columns, or four with a
    size; the last line for a core wins."""
    out = {}
    for raw in text.splitlines():
        fields = raw.split()
        if len(fields) not in (3, 4):
            continue
        date, crc, name = fields[0], fields[1].lower(), fields[2]
        if len(fields) == 4 and not name.endswith(".zip"):
            name = fields[3]
        m = CORE_RE.match(name)
        if not LINE_RE.match(date) or not re.fullmatch(r"[0-9a-f]{8}", crc) or not m:
            continue
        out[m.group(1)] = (date, crc, name)
    return out


def cstr(table, off):
    """The NUL-terminated string at off in a string table, or None."""
    if off >= len(table):
        return None
    end = table.find(b"\0", off)
    if end < 0:
        return None
    return table[off:end].decode("utf-8", "replace")


def elf_needs(data):
    """(arch, needed, versions) of an ELF shared object: its DT_NEEDED names in
    order, and per library the non-weak symbol versions .gnu.version_r asks of
    it. Raises ValueError for anything that is not a readable ELF file."""
    if len(data) < 64 or data[:4] != b"\x7fELF":
        raise ValueError("not an ELF file")
    bits = {1: 32, 2: 64}.get(data[4])
    order = {1: "<", 2: ">"}.get(data[5])
    if bits is None or order is None:
        raise ValueError("unknown ELF class or byte order")
    (machine,) = struct.unpack_from(order + "H", data, 18)
    if bits == 64:
        (shoff,) = struct.unpack_from(order + "Q", data, 0x28)
        shentsize, shnum, _ = struct.unpack_from(order + "HHH", data, 0x3A)
        sh_fmt, dyn_fmt = order + "IIQQQQIIQQ", order + "qQ"
    else:
        (shoff,) = struct.unpack_from(order + "I", data, 0x20)
        shentsize, shnum, _ = struct.unpack_from(order + "HHH", data, 0x2E)
        sh_fmt, dyn_fmt = order + "IIIIIIIIII", order + "iI"
    if shoff == 0 or shnum == 0 or shoff + shnum * shentsize > len(data):
        raise ValueError("no section headers")
    sections = []
    for i in range(shnum):
        f = struct.unpack_from(sh_fmt, data, shoff + i * shentsize)
        # name, type, flags, addr, offset, size, link, info, addralign, entsize
        sections.append({"type": f[1], "offset": f[4], "size": f[5], "link": f[6], "info": f[7]})

    def body(sec):
        start, size = sec["offset"], sec["size"]
        if start + size > len(data):
            raise ValueError("a section runs past the end of the file")
        return data[start:start + size]

    def strtab_of(sec):
        if sec["link"] >= len(sections):
            raise ValueError("a section links no string table")
        return body(sections[sec["link"]])

    needed = []
    for sec in sections:
        if sec["type"] != SHT_DYNAMIC:
            continue
        table, raw = strtab_of(sec), body(sec)
        step = struct.calcsize(dyn_fmt)
        for off in range(0, len(raw) - step + 1, step):
            tag, val = struct.unpack_from(dyn_fmt, raw, off)
            if tag == 0:
                break
            if tag == DT_NEEDED:
                name = cstr(table, val)
                if name and name not in needed:
                    needed.append(name)
    versions = {}
    for sec in sections:
        if sec["type"] != SHT_GNU_VERNEED:
            continue
        table, raw = strtab_of(sec), body(sec)
        off, count = 0, sec["info"]
        for i in range(4096):
            if off + 16 > len(raw):
                break
            _, cnt, vn_file, vn_aux, vn_next = struct.unpack_from(order + "HHIII", raw, off)
            lib = cstr(table, vn_file)
            a = off + vn_aux
            for _ in range(min(cnt, 4096)):
                if not vn_aux or a + 16 > len(raw):
                    break
                _, flags, _, vna_name, vna_next = struct.unpack_from(order + "IHHII", raw, a)
                name = cstr(table, vna_name)
                if lib and name and not flags & VER_FLG_WEAK:
                    versions.setdefault(lib, set()).add(name)
                if not vna_next:
                    break
                a += vna_next
            if not vn_next or (count and i + 1 >= count):
                break
            off += vn_next
    arch = MACHINES.get(machine, "machine-%d" % machine)
    return arch, needed, {lib: sorted(v) for lib, v in sorted(versions.items())}


def survey_one(base, core, date, crc, name, timeout):
    """One core's entry, or raises with why it could not be read."""
    data = fetch(base + name, MAX_ZIP, timeout)
    with zipfile.ZipFile(io.BytesIO(data)) as zf:
        want = core + "_libretro.so"
        members = [i for i in zf.infolist() if not i.is_dir() and os.path.basename(i.filename) == want]
        if not members:
            members = [i for i in zf.infolist() if not i.is_dir() and i.filename.endswith(".so")]
        if not members:
            raise ValueError("the zip holds no .so")
        info = members[0]
        if info.file_size > MAX_BINARY:
            raise ValueError("the binary is %d bytes" % info.file_size)
        binary = zf.read(info)
    arch, needed, versions = elf_needs(binary)
    return {"date": date, "crc32": crc, "file": os.path.basename(info.filename), "arch": arch,
            "needed": needed, "versions": versions}


def newest(versions, family):
    """The newest version of family any library is asked for ("" for none)."""
    best = None
    for names in versions.values():
        for n in names:
            fam, _, num = n.rpartition("_")
            if fam != family or not re.fullmatch(r"\d+(\.\d+)*", num):
                continue
            key = tuple(int(p) for p in num.split("."))
            if best is None or key > best[0]:
                best = (key, n)
    return best[1] if best else ""


def main(argv):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--out", required=True)
    ap.add_argument("--index", default=INDEX_URL)
    ap.add_argument("--base", default=BASE_URL)
    ap.add_argument("--jobs", type=int, default=6)
    ap.add_argument("--limit", type=int, default=0, help="survey only the first N cores (a test run)")
    ap.add_argument("--timeout", type=int, default=120)
    args = ap.parse_args(argv)

    index = parse_index(fetch(args.index, 8 << 20, args.timeout).decode("utf-8", "replace"))
    if not index:
        sys.exit("survey-cores: the index lists no core")
    names = sorted(index)
    if args.limit > 0:
        names = names[: args.limit]
    cores, failed = {}, {}
    with concurrent.futures.ThreadPoolExecutor(max_workers=max(1, args.jobs)) as pool:
        jobs = {pool.submit(survey_one, args.base, c, *index[c], args.timeout): c for c in names}
        for job in concurrent.futures.as_completed(jobs):
            core = jobs[job]
            try:
                cores[core] = job.result()
            except Exception as err:  # one core's failure is noted, not fatal
                failed[core] = str(err)[:300]
    if not cores:
        sys.exit("survey-cores: no core could be read (%d failed)" % len(failed))
    doc = {
        "format": 1,
        "generated": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "index": args.index,
        "cores": {c: cores[c] for c in sorted(cores)},
        "failed": {c: failed[c] for c in sorted(failed)},
    }
    tmp = args.out + ".tmp"
    with open(tmp, "w", encoding="utf-8", newline="\n") as out:
        json.dump(doc, out, indent=1, sort_keys=True)
        out.write("\n")
    os.replace(tmp, args.out)

    # What the handheld will make of it, for the job's log and summary.
    lines = ["surveyed %d of %d cores (%d could not be read)" % (len(cores), len(names), len(failed))]
    for family in ("GLIBC", "GLIBCXX", "CXXABI", "GCC"):
        counts = {}
        for entry in cores.values():
            v = newest(entry["versions"], family)
            if v:
                counts[v] = counts.get(v, 0) + 1
        if counts:
            top = sorted(counts.items(), key=lambda kv: tuple(int(p) for p in kv[0].rpartition("_")[2].split(".")))
            lines.append("%s: %s" % (family, ", ".join("%s x%d" % kv for kv in top)))
    gl = sorted(c for c, e in cores.items() if any(n.startswith(("libGL.so", "libOpenGL.so", "libGLX.so")) for n in e["needed"]))
    if gl:
        lines.append("desktop OpenGL (%d): %s" % (len(gl), " ".join(gl)))
    other = sorted(c for c, e in cores.items() if e["arch"] != "aarch64")
    if other:
        lines.append("not aarch64: " + " ".join(other))
    print("\n".join(lines))
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as out:
            out.write("### Buildbot linux/aarch64 survey\n```\n" + "\n".join(lines) + "\n```\n")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
