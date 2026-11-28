import re, sys
m = open('player/MODERNIZATION.md', encoding='utf-8').read()
c = open('CHANGELOG.md', encoding='utf-8').read()
hdr = re.search(r'Open tasks: \*\*(\d+)\*\*', m)
ids = re.findall(r'^### ([A-Z]+-\d+)', m, re.M)
print('header', hdr.group(1) if hdr else 'MISSING', 'blocks', len(ids), 'sections', len(re.findall(r'^## ', m, re.M)))
print('first line', m.splitlines()[0])
print('--- tasks ---')
for line in m.splitlines():
    if line.startswith('### ') or line.startswith('## '):
        print(line.encode('ascii', 'replace').decode())
print('--- changelog hits ---')
for tid in ids:
    pat = re.compile(r'\b' + re.escape(tid) + r'\b')
    hits = [ln for ln in c.splitlines() if pat.search(ln)]
    if hits:
        print(tid, '|', hits[0][:120].encode('ascii', 'replace').decode())
