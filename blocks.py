import re, sys
m = open('player/MODERNIZATION.md', encoding='utf-8').read()
want = sys.argv[1:]
for tid in want:
    pat = re.compile(r'^### ' + re.escape(tid) + r' — .*?(?=^### |^## |\Z)', re.M | re.S)
    hit = pat.search(m)
    print('=' * 20, tid, '=' * 20)
    print(hit.group(0).strip() if hit else 'MISSING')
