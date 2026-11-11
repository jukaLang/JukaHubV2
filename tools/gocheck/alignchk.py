#!/usr/bin/env python3
"""alignchk.py — gofmt alignment check for const/var groups and struct fields.

gocheck.py verifies key/value alignment in composite literals; this covers the
other tabwriter-aligned constructs gofmt rewrites: the columns of a
parenthesized const/var group (names | type | = values | // comment) and of a
struct type (names | type | // comment). It emulates text/tabwriter's column
blocks (padding 1, DiscardEmptyColumns) over sections of consecutive one-line
entries; blank lines, comment lines and multi-line entries end a section.
Sections it cannot model exactly (struct tags) are skipped, never guessed.

Usage: alignchk.py FILE.go [FILE.go ...]   (exit 1 when a problem is found)
"""
import re
import sys

IDENT_LIST = re.compile(r'^([A-Za-z_]\w*(?:, [A-Za-z_]\w*)*)(?= |$)')


def split_comment(s):
    """Split s into (code, comment) at a // that is outside strings/runes."""
    i, n = 0, len(s)
    while i < n:
        c = s[i]
        if c == '"':
            i += 1
            while i < n and s[i] != '"':
                if s[i] == '\\':
                    i += 1
                i += 1
            i += 1
            continue
        if c == '`':
            j = s.find('`', i + 1)
            i = j + 1 if j >= 0 else n
            continue
        if c == "'":
            i += 1
            while i < n and s[i] != "'":
                if s[i] == '\\':
                    i += 1
                i += 1
            i += 1
            continue
        if s.startswith('//', i):
            return s[:i], s[i:]
        if s.startswith('/*', i):
            j = s.find('*/', i + 2)
            if j < 0:
                return s, ''
            i = j + 2
            continue
        i += 1
    return s, ''


def depth_delta(code):
    """Net bracket depth change of a code fragment (strings skipped)."""
    d = 0
    i, n = 0, len(code)
    while i < n:
        c = code[i]
        if c in '"\'':
            q = c
            i += 1
            while i < n and code[i] != q:
                if code[i] == '\\':
                    i += 1
                i += 1
        elif c == '`':
            j = code.find('`', i + 1)
            if j < 0:
                return d + 1000  # raw string continues on the next line
            i = j
        elif c in '([{':
            d += 1
        elif c in ')]}':
            d -= 1
        i += 1
    return d


def find_assign(code):
    """Index of the ' = ' that separates a spec's values, outside brackets."""
    depth = 0
    i, n = 0, len(code)
    while i < n:
        c = code[i]
        if c in '"\'':
            q = c
            i += 1
            while i < n and code[i] != q:
                if code[i] == '\\':
                    i += 1
                i += 1
        elif c == '`':
            j = code.find('`', i + 1)
            i = j if j >= 0 else n
        elif c in '([{':
            depth += 1
        elif c in ')]}':
            depth -= 1
        elif c == '=' and depth == 0 and i > 0 and code[i - 1] == ' ' and i + 1 < n and code[i + 1] == ' ':
            return i
        i += 1
    if code.startswith('= '):
        return 0
    return -1


def tw_starts(lines):
    """text/tabwriter column starts (padding 1, DiscardEmptyColumns)."""
    n = len(lines)
    widths = [dict() for _ in range(n)]

    def fmt(line0, line1, column):
        pos = line0
        while pos < line1:
            if column >= len(lines[pos]) - 1:
                pos += 1
                continue
            start, width, empty = pos, 0, True
            while pos < line1 and column < len(lines[pos]) - 1:
                cw = len(lines[pos][column])
                if cw > 0:
                    empty = False
                width = max(width, cw + 1)
                pos += 1
            if empty:
                width = 0
            for i in range(start, pos):
                widths[i][column] = width
            fmt(start, pos, column + 1)

    fmt(0, n, 0)
    out = []
    for i, cells in enumerate(lines):
        starts, acc = [0], 0
        for col in range(len(cells) - 1):
            acc += widths[i][col]
            starts.append(acc)
        out.append(starts)
    return out


def render(cells, starts):
    s = ''
    for cell, st in zip(cells, starts):
        if len(s) < st:
            s += ' ' * (st - len(s))
        s += cell
    return s.rstrip()


def parse_spec(body):
    """A const/var spec line → (names, type, values, comment) or None."""
    code, comment = split_comment(body)
    code = code.rstrip()
    m = IDENT_LIST.match(code)
    if not m:
        return None
    names = m.group(1)
    rest = code[len(names):].lstrip(' ')
    typ, vals = '', ''
    if rest:
        k = find_assign(' ' + rest)
        if k >= 0:
            k -= 1  # index into rest (a space was prepended)
            typ = rest[:k].strip()
            vals = rest[k:].strip()  # '= ...'
            if not vals.startswith('='):
                return None
        else:
            typ = rest.strip()
    return names, typ, vals, comment.strip()


def parse_field(body):
    """A struct field line → (names, type, comment, tagged) or None."""
    code, comment = split_comment(body)
    code = code.rstrip()
    if not code:
        return None
    tagged = code.endswith('`') or code.endswith('"')
    m = IDENT_LIST.match(code)
    if m and len(code) > len(m.group(1)) and code[len(m.group(1))] == ' ':
        names = m.group(1)
        typ = code[len(names):].strip()
        return names, typ, comment.strip(), tagged
    # embedded field: one token (possibly *pkg.Type)
    if re.match(r'^\*?[\w.]+(\[.*\])?$', code):
        return '', code, comment.strip(), tagged
    return None


def check_file(path):
    problems = []
    try:
        src = open(path, encoding='utf-8').read().split('\n')
    except OSError as e:
        return [f'{path}: {e}']
    i, n = 0, len(src)
    while i < n:
        line = src[i]
        stripped = line.strip()
        kind = None
        if re.match(r'^\s*(const|var) \($', line):
            kind = 'spec'
        elif re.search(r'\bstruct \{$', split_comment(line)[0].rstrip()):
            kind = 'field'
        if not kind:
            i += 1
            continue
        indent = len(line) - len(line.lstrip('\t'))
        closer = ')' if kind == 'spec' else '}'
        entries = []  # (lineno, body, multiline_first)
        j = i + 1
        blocks = [[]]
        while j < n:
            l = src[j]
            ind = len(l) - len(l.lstrip('\t'))
            if l.strip().startswith(closer) and ind == indent:
                break
            if not l.strip() or l.strip().startswith('//') or l.strip().startswith('/*'):
                blocks.append([])
                j += 1
                continue
            body = l[ind:]
            if ind != indent + 1:
                blocks.append([])
                j += 1
                continue
            code, _ = split_comment(body)
            d = depth_delta(code)
            blocks[-1].append((j + 1, body))
            if d != 0:
                # first line of a multi-line entry ends the section; skip to
                # where its brackets close
                blocks.append([])
                depth = d
                j += 1
                while j < n and depth > 0:
                    c2, _ = split_comment(src[j])
                    depth += depth_delta(c2)
                    j += 1
                continue
            j += 1
        # keepType runs for specs (over the whole group, in order)
        if kind == 'spec':
            allspecs = []
            for b in blocks:
                for (ln, body) in b:
                    p = parse_spec(body)
                    allspecs.append((ln, p))
            keep = {}
            run = []

            def close_run():
                k = any(p and p[1] for (_, p) in run)
                for (ln, _) in run:
                    keep[ln] = k
            for (ln, p) in allspecs:
                if p and p[2]:
                    run.append((ln, p))
                else:
                    close_run()
                    run = []
                    keep[ln] = False
            close_run()
        for b in blocks:
            if len(b) < 2:
                continue
            cells_list, bodies, ok = [], [], True
            for (ln, body) in b:
                if kind == 'spec':
                    p = parse_spec(body)
                    if p is None:
                        ok = False
                        break
                    names, typ, vals, com = p
                    cells = [names]
                    if typ or keep.get(ln):
                        cells.append(typ)
                    if vals:
                        cells.append(vals)
                    if com:
                        while len(cells) < 3:
                            cells.append('')
                        cells.append(com)
                else:
                    p = parse_field(body)
                    if p is None or p[3]:
                        ok = False
                        break
                    names, typ, com, _ = p
                    if names:
                        cells = [names, typ]
                        if com:
                            cells.append(com)
                    else:
                        cells = [typ]
                        if com:
                            cells += ['', com]
                cells_list.append(cells)
                bodies.append((ln, body.rstrip()))
            if not ok:
                continue
            if kind == 'field' and sum(1 for _ in cells_list) < 2:
                continue
            starts = tw_starts(cells_list)
            for (ln, body), cells, st in zip(bodies, cells_list, starts):
                want = render(cells, st)
                if want != body:
                    problems.append(f'{path}:{ln}: gofmt alignment, want: {want!r}')
        i = j + 1
    return problems


def main():
    probs = []
    for p in sys.argv[1:]:
        probs += check_file(p)
    for p in probs:
        print(p)
    print(f'alignchk: {len(probs)} problem(s)')
    return 1 if probs else 0


if __name__ == '__main__':
    sys.exit(main())
