#!/usr/bin/env python3
"""List player functions that only tests call, and fail on undocumented ones.

ARCH-8 marked the player's test-only functions and ARCH-9 removed the dead ones
(2026-11-17). This keeps the list honest without a Go toolchain: standard
library only, so it runs wherever Python 3 is.

Rules:
  - the top-level ``func``/method declarations of every non-test ``*.go`` file
    directly in the player directory (one ``package main``; vendor/ and tool
    sub-packages are not read)
  - comments, strings and runes are stripped before identifiers are counted
  - build tags are ignored on purpose: a reference from the other GOOS's file
    counts, so a function one platform uses is never listed
  - a function counts as used by any token of its name in production code
    (a call, a function value, a map of handlers); a method only through a
    ``.Name`` selector, and well-known interface method names are skipped
  - ``//export`` and ``//go:linkname`` names are skipped
  - a candidate is any of those with no production reference but at least one
    in a ``*_test.go`` file

A candidate is a *documented seam* when its name ends in ``ForTest`` or its doc
comment says what the tests need it for (the comment mentions a test), or when
it implements an interface of a vendored library (``VENDOR_INTERFACE``).

Exit status: 0 when every candidate is a documented seam, 1 with the list of
undocumented ones otherwise. ``-v`` also lists the documented seams.
"""

from __future__ import annotations

import collections
import os
import re
import sys

TOKEN = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
DECL = re.compile(r"^func\s+(\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*[\[(]", re.M)
EXPORT = re.compile(r"//\s*(?:export|go:linkname)\s+([A-Za-z_][A-Za-z0-9_]*)")
MENTIONS_TEST = re.compile(r"(?i)\btest(s|ing)?\b|test seam")

# Method names a standard or common interface calls through a value of the
# interface type, never through a selector the scan can see.
INTERFACE_METHODS = {
    "String", "Error", "Len", "Less", "Swap", "Read", "Write", "Close", "ServeHTTP",
    "MarshalJSON", "UnmarshalJSON", "Is", "As", "Unwrap", "Seek", "ReadAt", "WriteTo",
    "ReadFrom", "Format", "GoString", "Name", "Size", "Mode", "ModTime", "IsDir", "Sys",
    "Type", "Info", "Stat", "Open", "ReadDir", "Lock", "Unlock", "Run", "Next", "Value",
    "Scan", "Timeout", "Temporary", "Dial", "RoundTrip", "Push", "Pop", "Get", "Set",
    "Init", "Draw", "Update", "Handle", "Load", "Store", "Header", "WriteHeader", "Flush",
    "Hijack", "Accept", "Addr",
}

# Methods that implement a vendored library's handler interface: pkg/sftp's
# FileReader/FileWriter/FileLister/FileCmder/RealPathFileLister (sftp_root.go).
VENDOR_INTERFACE = {"Fileread", "Filewrite", "Filelist", "Filecmd", "RealPath"}


def strip(src: str) -> str:
    """Blank out comments, strings and runes, keeping line breaks."""
    out = []
    i, n = 0, len(src)
    while i < n:
        c = src[i]
        if src.startswith("//", i):
            j = src.find("\n", i)
            j = n if j < 0 else j
            out.append(" " * (j - i))
            i = j
            continue
        if src.startswith("/*", i):
            j = src.find("*/", i + 2)
            j = n if j < 0 else j + 2
            out.append(re.sub(r"[^\n]", " ", src[i:j]))
            i = j
            continue
        if c == "`":
            j = src.find("`", i + 1)
            j = n if j < 0 else j + 1
            out.append(re.sub(r"[^\n]", " ", src[i:j]))
            i = j
            continue
        if c in "\"'":
            j = i + 1
            while j < n and src[j] != c and src[j] != "\n":
                if src[j] == "\\":
                    j += 1
                j += 1
            j = min(n, j + 1)
            out.append(" " * (j - i))
            i = j
            continue
        out.append(c)
        i += 1
    return "".join(out)


def doc_comment(lines: list[str], line: int) -> str:
    """The // comment block directly above a 1-based declaration line."""
    block = []
    k = line - 2
    while k >= 0 and lines[k].lstrip().startswith("//"):
        block.append(lines[k])
        k -= 1
    return "\n".join(reversed(block))


def scan(root: str):
    files = sorted(f for f in os.listdir(root) if f.endswith(".go"))
    decls = collections.defaultdict(list)  # name -> [(file, is_method, line, doc)]
    prod, prod_sel, tests = collections.Counter(), collections.Counter(), collections.Counter()
    exports = set()
    for f in files:
        with open(os.path.join(root, f), encoding="utf-8", errors="replace") as fh:
            raw = fh.read()
        exports.update(EXPORT.findall(raw))
        s = strip(raw)
        is_test = f.endswith("_test.go")
        if not is_test:
            raw_lines = raw.split("\n")
            for m in DECL.finditer(s):
                line = s.count("\n", 0, m.start()) + 1
                decls[m.group(2)].append((f, bool(m.group(1)), line, doc_comment(raw_lines, line)))
        counter = tests if is_test else prod
        for m in TOKEN.finditer(s):
            counter[m.group(0)] += 1
            if not is_test and m.start() > 0 and s[m.start() - 1] == ".":
                prod_sel[m.group(0)] += 1
    found = []
    for name, ds in decls.items():
        if name in ("main", "init", "_") or name in exports:
            continue
        is_method = all(d[1] for d in ds)
        if is_method:
            if name in INTERFACE_METHODS:
                continue
            refs = prod_sel[name]
        else:
            refs = prod[name] - len(ds)
        if refs > 0 or tests[name] == 0:
            continue
        f, _, line, doc = ds[0]
        if is_method and name in VENDOR_INTERFACE:
            why = "vendored interface"
        elif name.endswith("ForTest") or name.endswith("ForTests") or MENTIONS_TEST.search(doc):
            why = "documented seam"
        else:
            why = ""
        found.append((f, line, name, "method" if is_method else "func", tests[name], why))
    return sorted(found)


def main(argv: list[str]) -> int:
    verbose = "-v" in argv
    args = [a for a in argv if a != "-v"]
    root = args[0] if args else os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "player")
    found = scan(root)
    bad = [r for r in found if not r[5]]
    if verbose:
        for f, line, name, kind, n, why in found:
            if why:
                print("%s:%d %s (%s, %d test refs): %s" % (f, line, name, kind, n, why))
    for f, line, name, kind, n, _ in bad:
        print("%s:%d %s (%s, %d test refs): only tests call it and its comment does not say why"
              % (f, line, name, kind, n))
    print("%d test-only functions, %d undocumented" % (len(found), len(bad)), file=sys.stderr)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
