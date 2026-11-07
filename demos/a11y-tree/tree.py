#!/usr/bin/python3
"""Decoded queries over `aurora ui tree --json` output, for demos/a11y-tree/run.sh. Standard library only.
Every command prints plain words or numbers; none of them trusts the application beyond what the reply says.

  tree.py schema FILE                      the reply's schema string and the viewer kind
  tree.py token FILE NAME [PARENT]         token of the first view named NAME (under a view named PARENT), or none
  tree.py texts FILE NAME                  the text runs of every view named NAME, joined by '|'
  tree.py deep FILE NAME                   the text runs of the first view named NAME and its descendants, '|'-joined
  tree.py abs FILE NAME                    "l,t,r,b" of the first view named NAME, window coordinates
  tree.py role FILE NAME                   "role/confidence" of the first view named NAME
  tree.py tbox FILE NAME [INDEX]           "l,t,r,b" of text run INDEX (default 0) of the first view named NAME
  tree.py state FILE NAME                  "hidden clipped navigable enabled focused" of the first view named NAME
  tree.py count FILE                       "views texts roles" : number of views, text runs, and distinct roles
  tree.py roles FILE                       every distinct role/confidence with its count
  tree.py find FILE SUBSTRING              names of the views that drew text containing SUBSTRING
  tree.py check FILE                       exit 1 unless every view has the documented fields
"""
import json, sys


def load(p):
    return json.load(open(p))


def walk(d):
    for w in d.get('windows', []):
        stack = [(w['root'], None)]
        while stack:
            n, par = stack.pop(0)
            yield w, n, par
            for c in n.get('children', []):
                stack.append((c, n))


def first(d, name, parent=None):
    for _, n, par in walk(d):
        if n['name'] == name and (parent is None or (par and par['name'] == parent)):
            return n
    return None


def box(b):
    return '%g,%g,%g,%g' % (b['l'], b['t'], b['r'], b['b'])


def descend(n):
    yield n
    for c in n.get('children', []):
        yield from descend(c)


def main(a):
    cmd, d = a[0], load(a[1])
    if cmd == 'schema':
        print(d.get('schema'), d.get('viewer', {}).get('kind'))
    elif cmd == 'token':
        n = first(d, a[2], a[3] if len(a) > 3 else None)
        print(n['token'] if n else 'none')
    elif cmd == 'texts':
        print('|'.join(t['text'] for _, n, _ in walk(d) if n['name'] == a[2] for t in n['text']))
    elif cmd == 'deep':
        n = first(d, a[2])
        print('|'.join(t['text'] for x in descend(n) for t in x['text']) if n else 'none')
    elif cmd == 'abs':
        n = first(d, a[2]); print(box(n['abs']) if n else 'none')
    elif cmd == 'role':
        n = first(d, a[2]); print('%s/%s' % (n['role'], n['role_confidence']) if n else 'none')
    elif cmd == 'tbox':
        n = first(d, a[2]); i = int(a[3]) if len(a) > 3 else 0
        print(box(n['text'][i]['box']) if n and len(n['text']) > i else 'none')
    elif cmd == 'state':
        n = first(d, a[2]); s = n['state']
        print(*(('null' if s[k] is None else int(s[k])) for k in ('hidden', 'clipped', 'navigable', 'enabled', 'focused')))
    elif cmd == 'count':
        ns = [n for _, n, _ in walk(d)]
        print(len(ns), sum(len(n['text']) for n in ns), len({n['role'] for n in ns}))
    elif cmd == 'roles':
        c = {}
        for _, n, _ in walk(d):
            k = '%s/%s' % (n['role'], n['role_confidence']); c[k] = c.get(k, 0) + 1
        for k in sorted(c): print(k, c[k])
    elif cmd == 'find':
        print('|'.join(sorted({n['name'] for _, n, _ in walk(d) if any(a[2] in t['text'] for t in n['text'])})))
    elif cmd == 'check':
        need = ('token', 'parent', 'name', 'role', 'role_confidence', 'role_basis', 'frame', 'abs', 'visible', 'state', 'drew', 'text', 'children')
        st = ('hidden', 'effectively_hidden', 'clipped', 'navigable', 'enabled', 'focused')
        bad = [n['name'] for _, n, _ in walk(d) if any(k not in n for k in need) or any(k not in n['state'] for k in st)]
        print('bad:', bad) if bad else print('ok')
        sys.exit(1 if bad else 0)
    else:
        sys.exit('unknown command ' + cmd)


main(sys.argv[1:])
