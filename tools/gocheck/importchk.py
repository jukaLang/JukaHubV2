#!/usr/bin/env python3
"""Check gofmt import sorting: within each blank-line-separated group of an
import block, specs must be sorted by path."""
import re, sys
bad = 0
for f in sys.argv[1:]:
    try:
        src = open(f, encoding='utf-8').read()
    except FileNotFoundError:
        continue
    m = re.search(r'^import \((.*?)^\)', src, re.S | re.M)
    if not m:
        continue
    groups = [[]]
    for line in m.group(1).split('\n'):
        t = line.strip()
        if t == '':
            if groups[-1]:
                groups.append([])
            continue
        if t.startswith('//'):
            continue
        pm = re.search(r'"([^"]+)"', t)
        if pm:
            groups[-1].append(pm.group(1))
    for g in groups:
        if g != sorted(g):
            print(f"{f}: import group not sorted: {g}")
            bad += 1
print(f"importchk: {bad} problem(s)")
