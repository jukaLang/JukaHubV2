#!/usr/bin/env python3
"""Check the open-task count, IDs and local links in MODERNIZATION.md."""

from __future__ import annotations

import re
import sys
from pathlib import Path


TASK = re.compile(r"^### ([A-Z][A-Z0-9]*(?:-[A-Z][A-Z0-9]*)*-\d+) — ", re.M)
REFERENCE = re.compile(r"\b[A-Z][A-Z0-9]*(?:-[A-Z][A-Z0-9]*)*-\d+\b")
COUNT = re.compile(r"^Open tasks: \*\*(\d+)\*\*", re.M)
HEADING = re.compile(r"^#{1,6} (.+)$", re.M)
LOCAL_LINK = re.compile(r"\]\(#([^\s)]+)\)")
NON_TASK_IDS = {"SHA-1", "SHA-256"}


def without_fences(source: str) -> str:
    """Hide examples while retaining their line numbers for diagnostics."""
    lines = []
    fence = ""
    for line in source.splitlines():
        marker = line.lstrip()[:3]
        if marker in ("```", "~~~"):
            fence = "" if fence else marker
            lines.append("")
        else:
            lines.append("" if fence else line)
    return "\n".join(lines)


def slug(heading: str) -> str:
    # GitHub heading fragments: punctuation goes, spaces become hyphens.
    heading = re.sub(r"<[^>]+>", "", heading)
    heading = heading.replace("`", "").lower()
    return "".join(c for c in heading if c.isalnum() or c in " _-").replace(" ", "-")


def line_number(source: str, offset: int) -> int:
    return source.count("\n", 0, offset) + 1


def check(source: str) -> list[str]:
    source = without_fences(source)
    problems: list[str] = []
    task_matches = list(TASK.finditer(source))
    task_ids = [m.group(1) for m in task_matches]
    known = set(task_ids)

    for task_id in sorted(known):
        locations = [line_number(source, m.start()) for m in task_matches if m.group(1) == task_id]
        if len(locations) > 1:
            problems.append(f"duplicate {task_id} at lines {locations}")

    counts = list(COUNT.finditer(source))
    if len(counts) != 1:
        problems.append("expected one 'Open tasks: **N**' line")
    elif int(counts[0].group(1)) != len(task_ids):
        problems.append(f"header says {counts[0].group(1)} tasks; found {len(task_ids)}")

    for ref in REFERENCE.finditer(source):
        if ref.group() not in known and ref.group() not in NON_TASK_IDS:
            problems.append(f"line {line_number(source, ref.start())}: missing task {ref.group()}")

    anchors = {slug(m.group(1)) for m in HEADING.finditer(source)}
    for link in LOCAL_LINK.finditer(source):
        anchor = link.group(1)
        if anchor not in anchors:
            problems.append(f"line {line_number(source, link.start())}: missing anchor #{anchor}")
    return problems


def main() -> int:
    path = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("player/MODERNIZATION.md")
    problems = check(path.read_text(encoding="utf-8"))
    if problems:
        print(f"backlog-check: {len(problems)} issue(s)")
        for problem in problems:
            print("  " + problem)
        return 1
    print("backlog-check: task count, IDs and anchors agree")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
