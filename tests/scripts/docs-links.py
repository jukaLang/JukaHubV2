#!/usr/bin/env python3
"""Fail on a broken relative link in any Markdown file.

The Go test that checked doc links went with the test cut, and links to deleted
files then pile up (TEST-7 in MODERNIZATION.md). This replaces it: standard
library only, no build step, so `make check` can run it wherever Python 3 is.

Rules:
  - every ``*.md`` outside node_modules/, vendor/, .git/, tmp/ and .sdl2/
  - links inside fenced code blocks and inline code are ignored, so an example
    path in a code sample is not a link
  - ``http(s):``, ``mailto:``, ``data:``, ``tel:`` and bare ``#anchor`` links are
    not relative; ``path#fragment`` is checked by its path
  - the target may be a file or a directory

Exit status: 0 when every relative link resolves, 1 with a list otherwise.
"""

from __future__ import annotations

import os
import re
import sys

SKIP_DIRS = {
    ".git",
    ".sdl2",
    "__pycache__",
    "dist",
    "node_modules",
    "tmp",
    "vendor",
}

FENCE = re.compile(r"^(```|~~~)")
INLINE_CODE = re.compile(r"`[^`]*`")
LINK = re.compile(r"!?\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
ABSOLUTE = ("#", "http://", "https://", "mailto:", "data:", "tel:")


def markdown_files(root: str):
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS]
        for name in filenames:
            if name.lower().endswith(".md"):
                yield os.path.join(dirpath, name)


def without_code(text: str) -> str:
    """Drop fenced blocks and inline code so their examples are not links."""
    kept = []
    in_fence = False
    for line in text.splitlines():
        if FENCE.match(line.strip()):
            in_fence = not in_fence
            continue
        kept.append("" if in_fence else INLINE_CODE.sub("", line))
    return "\n".join(kept)


def broken_links(root: str):
    bad = []
    for path in markdown_files(root):
        try:
            with open(path, encoding="utf-8", errors="replace") as fh:
                text = without_code(fh.read())
        except OSError as err:
            bad.append((path, f"<unreadable: {err}>"))
            continue
        for match in LINK.finditer(text):
            link = match.group(1).strip()
            if not link or link.startswith(ABSOLUTE):
                continue
            target = link.split("#", 1)[0].strip()
            if not target:
                continue
            resolved = os.path.normpath(os.path.join(os.path.dirname(path), target))
            if not os.path.exists(resolved):
                bad.append((path, link))
    return bad


def main() -> int:
    root = sys.argv[1] if len(sys.argv) > 1 else "."
    bad = broken_links(root)
    if not bad:
        print("docs-links: every relative link resolves")
        return 0
    print(f"docs-links: {len(bad)} broken relative link(s)")
    for path, link in bad:
        print(f"  {os.path.relpath(path, root)} -> {link}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
