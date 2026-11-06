#!/usr/bin/env python3
"""Tiny PNG pixel inspector for run.sh (stdlib only; 8-bit RGB/RGBA, all filters).
  info F                -> "W H"
  diff A B              -> number of differing pixels (-1 if sizes differ)
  count F R G B         -> pixels exactly that colour
  profile F Y X0 X1     -> "mids=N maxjump=M" for the red channel along row Y, X0..X1-1
                           (mids: values strictly between the row's min and max)
"""
import struct, sys, zlib

def load(path):
    d = open(path, "rb").read()
    assert d[:8] == b"\x89PNG\r\n\x1a\n", "not a PNG"
    p, idat = 8, b""
    while p < len(d):
        n, t = struct.unpack(">I4s", d[p:p + 8]); c = d[p + 8:p + 8 + n]; p += 12 + n
        if t == b"IHDR": w, h, bd, ct = struct.unpack(">IIBB", c[:10])
        if t == b"IDAT": idat += c
    assert bd == 8 and ct in (2, 6), "need 8-bit RGB or RGBA"
    bpp = 3 if ct == 2 else 4; st = w * bpp
    raw = zlib.decompress(idat); prev = bytearray(st); rows = []; i = 0
    for _ in range(h):
        f = raw[i]; row = bytearray(raw[i + 1:i + 1 + st]); i += 1 + st
        for x in range(st):
            a = row[x - bpp] if x >= bpp else 0; b = prev[x]; c = prev[x - bpp] if x >= bpp else 0
            if f == 1: row[x] = (row[x] + a) & 255
            elif f == 2: row[x] = (row[x] + b) & 255
            elif f == 3: row[x] = (row[x] + (a + b) // 2) & 255
            elif f == 4:
                pa, pb, pc = abs(b - c), abs(a - c), abs(a + b - 2 * c)
                row[x] = (row[x] + (a if pa <= pb and pa <= pc else b if pb <= pc else c)) & 255
        rows.append(row); prev = row
    return w, h, bpp, rows

def main(a):
    cmd = a[0]
    if cmd == "info":
        w, h, _, _ = load(a[1]); print(w, h)
    elif cmd == "diff":
        w, h, bpp, r1 = load(a[1]); w2, h2, bpp2, r2 = load(a[2])
        if (w, h) != (w2, h2): print(-1); return
        print(sum(1 for y in range(h) for x in range(w)
                  if r1[y][x * bpp:x * bpp + 3] != r2[y][x * bpp2:x * bpp2 + 3]))
    elif cmd == "count":
        w, h, bpp, rows = load(a[1]); want = bytes(int(v) for v in a[2:5])
        print(sum(1 for r in rows for x in range(w) if bytes(r[x * bpp:x * bpp + 3]) == want))
    elif cmd == "profile":
        w, h, bpp, rows = load(a[1]); y, x0, x1 = int(a[2]), int(a[3]), int(a[4])
        v = [rows[y][x * bpp] for x in range(x0, x1)]; lo, hi = min(v), max(v)
        print(f"mids={sum(1 for q in v if lo < q < hi)} maxjump={max(abs(v[i + 1] - v[i]) for i in range(len(v) - 1))}")
    else:
        sys.exit("unknown command")

main(sys.argv[1:])
