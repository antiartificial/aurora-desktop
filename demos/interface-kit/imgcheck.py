#!/usr/bin/python3
"""Decode the compositor's capture (PNG) and the app server's own frame (PPM) and assert real pixels.

usage: python3 -I imgcheck.py CAPTURE.png FRAME.ppm METRICS.txt
       python3 -I imgcheck.py --bbox CAPTURE.png R G B     (bounding box of one colour)
Prints one `ok:` or `FAIL:` line per assertion; the exit status is the number of failures.

Nothing here trusts the app: the capture comes from the compositor over ext-image-copy-capture, the window is
FOUND in it by pixel matching, and every expectation is a colour, a position or a measured ink box.
Standard library only (zlib, struct)."""
import re, struct, sys, zlib

def read_png(path):
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
    if bpp == 4:   # drop alpha so both images are packed RGB
        rows = [bytes(b for k, b in enumerate(r) if k % 4 != 3) for r in rows]
    return w, h, rows

def read_ppm(path):
    d = open(path, 'rb').read(); m = re.match(rb'P6\n(\d+) (\d+)\n255\n', d)
    w, h = int(m.group(1)), int(m.group(2)); px = d[m.end():]
    return w, h, [px[y * w * 3:(y + 1) * w * 3] for y in range(h)]

if len(sys.argv) == 6 and sys.argv[1] == '--bbox':    # bounding box of one colour in a capture
    w, h, rows = read_png(sys.argv[2]); col = bytes(int(v) for v in sys.argv[3:6]); xs = []; ys = []
    for y in range(h):
        i = rows[y].find(col)
        while i >= 0:
            if i % 3 == 0: xs.append(i // 3); ys.append(y)
            i = rows[y].find(col, i + 1)
    print(min(xs), min(ys), max(xs), max(ys)) if xs else print('none'); sys.exit(0)

fails = 0
def ok(c, msg):
    global fails
    print(('   ok: ' if c else '   FAIL: ') + msg)
    if not c: fails += 1

cw, ch, crow = read_png(sys.argv[1]); fw, fh, frow = read_ppm(sys.argv[2])
metrics = {}
for line in open(sys.argv[3]):
    m = re.match(r'METRIC (\w+) x=(\S+) baseline=(\S+) width=(\S+) ascent=(\S+) descent=(\S+)', line)
    if m: metrics[m.group(1)] = dict(x=float(m.group(2)), base=float(m.group(3)), w=float(m.group(4)), a=float(m.group(5)), d=float(m.group(6)))

def cpx(x, y): return tuple(crow[y][3 * x:3 * x + 3])
def fpx(x, y): return tuple(frow[y][3 * x:3 * x + 3])

# ---- find the window in the capture by matching the app server's own first row, then verify the whole rectangle
distinct = len({crow[y][i:i + 3] for y in range(0, ch, 7) for i in range(0, cw * 3, 21)})
ok(distinct > 50, f'capture {cw}x{ch} is not blank: {distinct} distinct colours sampled')
ox = oy = None
for y in range(ch - fh + 1):
    x = crow[y].find(frow[0])
    while x >= 0 and x % 3: x = crow[y].find(frow[0], x + 1)
    if x >= 0 and all(crow[y + k][x:x + len(frow[k])] == frow[k] for k in range(fh)):
        ox, oy = x // 3, y; break
ok(ox is not None, f'the {fw}x{fh} frame the app server finished appears verbatim in the compositor capture' + (f' at ({ox},{oy})' if ox is not None else ''))
if ox is None:
    # report what is there instead, so the failure is diagnosable
    print('   (no exact match; first row of the frame not found in any capture row)'); sys.exit(fails or 1)
total = fw * fh; same = sum(1 for y in range(fh) for x in range(0, fw * 3, 3) if crow[oy + y][3 * ox + x:3 * ox + x + 3] == frow[y][x:x + 3])
ok(same == total, f'window pixels identical: {same}/{total}')

W, H = fw, fh   # scale 1: layout px == device px
BLACK, BG = (0, 0, 0), (240, 240, 230)
ok(cpx(ox, oy) == BLACK and cpx(ox + W - 1, oy) == BLACK and cpx(ox, oy + H - 1) == BLACK and cpx(ox + W - 1, oy + H - 1) == BLACK,
   f'the 1px black frame is on all four corners of the window rectangle ({ox},{oy})..({ox + W - 1},{oy + H - 1})')
ok(ox + W >= cw or cpx(ox + W, oy + 100) != BLACK, 'the window ends at its right edge (next column is not window)')
ok(cpx(ox + 5, oy + 5) == BG, 'background pixel is the view colour (240,240,230)')
ok(cpx(ox + 60, oy + 40) == (200, 30, 30) and cpx(ox + 20, oy + 20) == (200, 30, 30) and cpx(ox + 99, oy + 59) == (200, 30, 30), 'red block fills (20,20)-(99,59) to its corners')
ok(cpx(ox + 100, oy + 40) == BG and cpx(ox + 19, oy + 40) == BG and cpx(ox + 60, oy + 60) == BG, 'red block does not leak past (99,59)')
ok(cpx(ox + 150, oy + 40) == (30, 160, 60) and cpx(ox + 240, oy + 40) == (40, 70, 210), 'green and blue blocks are there')
ok(all(cpx(ox + x, oy + 70) == BLACK for x in (20, 100, 200, 279)) and cpx(ox + 19, oy + 70) == BG and cpx(ox + 280, oy + 70) == BG, 'the 1px rule spans x=20..279 at y=70')

def ink(x0, y0, x1, y1):
    """pixels that differ from the background by more than 40 (L1), as (x,y,luma)"""
    out = []
    for y in range(max(y0, 0), min(y1, H - 1) + 1):
        for x in range(max(x0, 0), min(x1, W - 1) + 1):
            p = cpx(ox + x, oy + y)
            if abs(p[0] - BG[0]) + abs(p[1] - BG[1]) + abs(p[2] - BG[2]) > 40: out.append((x, y, sum(p) / 3))
    return out

def label(tag, text):
    m = metrics.get(tag)
    ok(m is not None, f'{tag}: the app reported BFont metrics for "{text}"')
    if not m: return None
    pts = ink(int(m['x']) - 4, int(m['base'] - m['a']) - 3, int(m['x'] + m['w']) + 12, int(m['base'] + m['d']) + 3)
    xs = [p[0] for p in pts]; ys = [p[1] for p in pts]
    ok(len(pts) > 40, f'{tag}: {len(pts)} glyph pixels on screen where the label should be')
    ok(sum(1 for p in pts if p[2] < 90) > 15, f'{tag}: solid dark glyph pixels exist ({sum(1 for p in pts if p[2] < 90)})')
    rows = len(set(ys)); ok(rows >= 6, f'{tag}: ink covers {rows} distinct rows, so the label row is not uniform')
    ok(min(xs) >= m['x'] - 2, f'{tag}: ink starts at x={min(xs)}, at or right of the pen x={m["x"]:.0f}')
    ok(m['x'] + m['w'] - 4 <= max(xs) <= m['x'] + m['w'] + 1.5, f'{tag}: ink ends at x={max(xs)}; pen + BFont::StringWidth = {m["x"] + m["w"]:.2f}')
    ok(min(ys) >= m['base'] - m['a'] - 1.5 and max(ys) <= m['base'] + m['d'] + 1.5, f'{tag}: ink rows {min(ys)}..{max(ys)} fit baseline {m["base"]:.0f} - ascent {m["a"]:.2f} .. + descent {m["d"]:.2f}')
    return (min(xs), max(xs), min(ys), max(ys))

b1 = label('plain', 'Hello, Aurora'); b2 = label('bold24', 'BeOS toolkit'); b3 = label('fixed', '0123456789 {fixed}')
if b1 and b2:
    ok((b2[3] - b2[2]) >= 1.4 * (b1[3] - b1[2]), f'24pt bold ink is {b2[3] - b2[2] + 1} rows tall vs {b1[3] - b1[2] + 1} for 12pt: size is honoured')
sys.exit(fails)
