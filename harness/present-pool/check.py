#!/usr/bin/env python3
"""Decide the pool test from the captured frame.

The client painted a 400x300 window: red everywhere, a green square at 40,40 and a blue square
at 240,40, all in window-local coordinates. The window is placed by the compositor, so find it
by looking for the red field rather than assuming a position.
"""
import struct, sys, zlib


def readpng(path):
    d = open(path, "rb").read()
    assert d[:8] == b"\x89PNG\r\n\x1a\n", "not a png"
    i, w, h, ct, idat = 8, 0, 0, 0, b""
    while i < len(d):
        ln = struct.unpack(">I", d[i : i + 4])[0]
        typ = d[i + 4 : i + 8]
        body = d[i + 8 : i + 8 + ln]
        if typ == b"IHDR":
            w, h, _bd, ct = struct.unpack(">IIBB", body[:10])
        elif typ == b"IDAT":
            idat += body
        i += 8 + ln + 4
    raw = zlib.decompress(idat)
    ch = {0: 1, 2: 3, 4: 2, 6: 4}[ct]
    stride, out, prev, pos = w * ch, bytearray(), bytearray(w * ch), 0
    for _ in range(h):
        f = raw[pos]; pos += 1
        line = bytearray(raw[pos : pos + stride]); pos += stride
        if f == 1:
            for x in range(ch, stride):
                line[x] = (line[x] + line[x - ch]) & 255
        elif f == 2:
            for x in range(stride):
                line[x] = (line[x] + prev[x]) & 255
        elif f == 3:
            for x in range(stride):
                a = line[x - ch] if x >= ch else 0
                line[x] = (line[x] + (a + prev[x]) // 2) & 255
        elif f == 4:
            for x in range(stride):
                a = line[x - ch] if x >= ch else 0
                b = prev[x]
                c = prev[x - ch] if x >= ch else 0
                p = a + b - c
                pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                line[x] = (line[x] + (a if pa <= pb and pa <= pc else b if pb <= pc else c)) & 255
        out += line
        prev = line
    return w, h, ch, bytes(out)


def main(path, steps):
    w, h, ch, px = readpng(path)

    def at(x, y):
        i = (y * w + x) * ch
        return px[i], px[i + 1], px[i + 2]

    def near(p, r, g, b, tol=40):
        return abs(p[0] - r) <= tol and abs(p[1] - g) <= tol and abs(p[2] - b) <= tol

    # The window's top-left: the first pixel of a run of red that is at least 20 wide.
    origin = None
    for y in range(h):
        for x in range(w - 20):
            if all(near(at(x + k, y), 255, 0, 0) for k in range(20)):
                origin = (x, y)
                break
        if origin:
            break
    if not origin:
        # No red at all is itself the bug: the last buffer attached was the fresh one.
        counts = {
            "black": sum(1 for y in range(0, h, 3) for x in range(0, w, 3) if max(at(x, y)) < 16),
            "green": sum(1 for y in range(0, h, 3) for x in range(0, w, 3) if near(at(x, y), 0, 255, 0)),
            "blue": sum(1 for y in range(0, h, 3) for x in range(0, w, 3) if near(at(x, y), 0, 0, 255)),
        }
        print(f"FAIL: no red field found. sampled counts: {counts}")
        print("      the window is not showing frame 1's background, which is the pool defect")
        return 1
    ox, oy = origin
    print(f"window origin {ox},{oy}")

    # Sample the centre of each square and of the background between them.
    g = at(ox + 40 + 40, oy + 40 + 40)
    b = at(ox + 240 + 40, oy + 40 + 40)
    bg = at(ox + 170, oy + 200)
    print(f"  green square centre {g}   expected ~(0,255,0)")
    print(f"  blue  square centre {b}   expected ~(0,0,255)")
    print(f"  background          {bg}  expected ~(255,0,0)")

    bad = []
    if steps < 3 and not near(b, 0, 0, 255):
        pass            # frame 3 was not presented in this run
    if not near(g, 0, 255, 0):
        bad.append("the GREEN square from frame 2 is not on screen: the buffer frame 3 went into never received frame 2's damage")
    if steps >= 3 and not near(b, 0, 0, 255):
        bad.append("the BLUE square from frame 3 is not on screen")
    if not near(bg, 255, 0, 0):
        bad.append("the RED background from frame 1 is not on screen: a freshly allocated pool buffer got only the damage rects, so the rest is the zeroed memfd")
    if bad:
        for x in bad:
            print(f"FAIL: {x}")
        return 1
    print("PASS: every frame's content survives into the buffer actually on screen")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1], int(sys.argv[2]) if len(sys.argv) > 2 else 3))
