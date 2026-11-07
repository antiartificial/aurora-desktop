#!/usr/bin/python3
"""Pixel queries over compositor captures (PNG) and server frame dumps (PPM) for demos/kitchen-sink/run.sh.
Standard library only. Every command prints plain numbers/words for the shell to test; none of them trusts the app.

  pix.py find CAP.png FRAME.ppm [X Y]            "x y" where FRAME appears verbatim in CAP (optionally only there), or "none"
  pix.py cmp CAP.png FRAME.ppm OX OY [X0 Y0 X1 Y1]   number of pixels where CAP (shifted by OX,OY) differs from FRAME, in a FRAME rect
  pix.py cmpbox CAP.png FRAME.ppm OX OY [X0 Y0 X1 Y1]   like cmp, but prints "n x0 y0 x1 y1" (count and bounding box of the differing pixels)
  pix.py diff A.png B.png [X0 Y0 X1 Y1]          "n x0 y0 x1 y1": count and bounding box of pixels that differ between two captures
  pix.py ink CAP.png X0 Y0 X1 Y1                 number of pixels in the rect that are not the rect's most common colour
  pix.py px CAP.png X Y                          "r g b"
  pix.py count CAP.png X0 Y0 X1 Y1 R G B TOL     pixels in the rect within TOL (max channel distance) of the colour
  pix.py xspan CAP.png X0 Y0 X1 Y1 R G B TOL     "xmin xmax" of those pixels, or "none"
  pix.py dark CAP.png X0 Y0 X1 Y1 LIMIT          "n xmin xmax ymin ymax" of pixels whose brightest channel is below LIMIT
  pix.py darkx CAP.png Y X0 X1 LIMIT             x of every pixel on row Y whose brightest channel is below LIMIT
  pix.py size CAP.png                            "w h"
  pix.py distinct CAP.png                        number of distinct colours

PIX_WINDOW="x,y,w,h" (set once the window has been located): every PNG is cropped to that rectangle when read, so
all coordinates below are WINDOW coordinates, as they were when the compositor put the window at the origin. It is
there because the compositor now draws a title bar above a window and places it (cascade, centred on a parent):
the window is no longer at 0,0. `find` and `size` always see the whole capture.
"""
import os, re, struct, sys, zlib


def read_png(path, crop=True):
    d = open(path, 'rb').read()
    assert d[:8] == b'\x89PNG\r\n\x1a\n', 'not a PNG'
    pos, idat, w, h, bd, ct = 8, b'', 0, 0, 0, 0
    while pos < len(d):
        n, t = struct.unpack('>I4s', d[pos:pos + 8]); body = d[pos + 8:pos + 8 + n]; pos += 12 + n
        if t == b'IHDR': w, h, bd, ct = struct.unpack('>IIBB', body[:10])
        elif t == b'IDAT': idat += body
    assert bd == 8 and ct in (2, 6), 'only 8-bit RGB/RGBA handled'
    bpp = 3 if ct == 2 else 4
    raw = zlib.decompress(idat); stride = w * bpp; rows = []; prev = bytearray(stride); i = 0
    for y in range(h):
        f = raw[i]; line = bytearray(raw[i + 1:i + 1 + stride]); i += 1 + stride
        if f == 1:
            for x in range(bpp, stride): line[x] = (line[x] + line[x - bpp]) & 255
        elif f == 2:
            for x in range(stride): line[x] = (line[x] + prev[x]) & 255
        elif f == 3:
            for x in range(stride): line[x] = (line[x] + (((line[x - bpp] if x >= bpp else 0) + prev[x]) >> 1)) & 255
        elif f == 4:
            for x in range(stride):
                a = line[x - bpp] if x >= bpp else 0; b = prev[x]; c = prev[x - bpp] if x >= bpp else 0
                p = a + b - c; pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                line[x] = (line[x] + (a if pa <= pb and pa <= pc else b if pb <= pc else c)) & 255
        rows.append(bytes(line)); prev = line
    if bpp == 4:
        rows = [bytes(b for k, b in enumerate(r) if k % 4 != 3) for r in rows]
    win = os.environ.get('PIX_WINDOW') if crop else None
    if win:
        x, y, cw, ch = [int(v) for v in win.split(',')]
        rows = [r[3 * x:3 * (x + cw)] for r in rows[y:y + ch]]
        w, h = cw, ch
    return w, h, rows


def read_ppm(path):
    d = open(path, 'rb').read(); m = re.match(rb'P6\n(\d+) (\d+)\n255\n', d)
    w, h = int(m.group(1)), int(m.group(2)); px = d[m.end():]
    return w, h, [px[y * w * 3:(y + 1) * w * 3] for y in range(h)]


def pixels(rows, x0, y0, x1, y1):
    for y in range(y0, y1 + 1):
        r = rows[y]
        for x in range(x0, x1 + 1):
            yield x, y, r[3 * x:3 * x + 3]


def rect(args, w, h):
    if len(args) >= 4: return [int(a) for a in args[:4]]
    return [0, 0, w - 1, h - 1]


def main(a):
    cmd = a[0]
    if cmd == 'find':
        _, _, crow = read_png(a[1], crop=False); _, _, frow = read_ppm(a[2]); fh = len(frow)
        only = (int(a[3]), int(a[4])) if len(a) == 5 else None
        for y in ([only[1]] if only else range(len(crow) - fh + 1)):
            x = crow[y].find(frow[0])
            while x >= 0:
                if x % 3 == 0 and (not only or x // 3 == only[0]) and all(crow[y + k][x:x + len(frow[k])] == frow[k] for k in range(fh)):
                    print(x // 3, y); return
                x = crow[y].find(frow[0], x + 1)
        print('none')
    elif cmd == 'cmp':
        _, _, crow = read_png(a[1]); fw, fh, frow = read_ppm(a[2]); ox, oy = int(a[3]), int(a[4])
        x0, y0, x1, y1 = rect(a[5:], fw, fh)
        n = sum(1 for x, y, p in pixels(frow, x0, y0, x1, y1) if crow[y + oy][3 * (x + ox):3 * (x + ox) + 3] != p)
        print(n)
    elif cmd == 'cmpbox':
        _, _, crow = read_png(a[1]); fw, fh, frow = read_ppm(a[2]); ox, oy = int(a[3]), int(a[4]); x0, y0, x1, y1 = rect(a[5:], fw, fh)
        pts = [(x, y) for x, y, p in pixels(frow, x0, y0, x1, y1) if crow[y + oy][3 * (x + ox):3 * (x + ox) + 3] != p]
        print(len(pts), *((min(p[0] for p in pts), min(p[1] for p in pts), max(p[0] for p in pts), max(p[1] for p in pts)) if pts else (-1, -1, -1, -1)))
    elif cmd == 'diff':
        w, h, A = read_png(a[1]); _, _, B = read_png(a[2]); x0, y0, x1, y1 = rect(a[3:], w, h)
        xs, ys = [], []
        for x, y, p in pixels(A, x0, y0, x1, y1):
            if B[y][3 * x:3 * x + 3] != p: xs.append(x); ys.append(y)
        print(len(xs), *((min(xs), min(ys), max(xs), max(ys)) if xs else (-1, -1, -1, -1)))
    elif cmd == 'ink':
        w, h, R = read_png(a[1]); x0, y0, x1, y1 = [int(v) for v in a[2:6]]
        cnt = {}
        for _, _, p in pixels(R, x0, y0, x1, y1): cnt[p] = cnt.get(p, 0) + 1
        top = max(cnt.values()); print(sum(cnt.values()) - top)
    elif cmd == 'px':
        _, _, R = read_png(a[1]); x, y = int(a[2]), int(a[3]); print(*R[y][3 * x:3 * x + 3])
    elif cmd in ('count', 'xspan'):
        w, h, R = read_png(a[1]); x0, y0, x1, y1 = [int(v) for v in a[2:6]]; col = [int(v) for v in a[6:9]]; tol = int(a[9])
        xs = [x for x, y, p in pixels(R, x0, y0, x1, y1) if max(abs(p[i] - col[i]) for i in range(3)) <= tol]
        if cmd == 'count': print(len(xs))
        else: print(min(xs), max(xs)) if xs else print('none')
    elif cmd == 'dark':
        w, h, R = read_png(a[1]); x0, y0, x1, y1 = [int(v) for v in a[2:6]]; lim = int(a[6])
        pts = [(x, y) for x, y, p in pixels(R, x0, y0, x1, y1) if max(p) < lim]
        print(len(pts), *((min(p[0] for p in pts), max(p[0] for p in pts), min(p[1] for p in pts), max(p[1] for p in pts)) if pts else (-1, -1, -1, -1)))
    elif cmd == 'darkx':
        w, h, R = read_png(a[1]); y = int(a[2]); x0, x1, lim = int(a[3]), int(a[4]), int(a[5])
        print(*[x for x in range(x0, x1 + 1) if max(R[y][3 * x:3 * x + 3]) < lim])
    elif cmd == 'size':
        w, h, _ = read_png(a[1], crop=False); print(w, h)
    elif cmd == 'distinct':
        w, h, R = read_png(a[1]); print(len({R[y][3 * x:3 * x + 3] for y in range(h) for x in range(w)}))
    else:
        sys.exit(__doc__)


if __name__ == '__main__':
    if len(sys.argv) < 2: sys.exit(__doc__)
    main(sys.argv[1:])
