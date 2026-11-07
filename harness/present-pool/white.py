#!/usr/bin/env python3
"""Count pure white in the captured frame. The app's own body is 0xEDEDED, so any large run of
exactly 255,255,255 is the reallocated buffer showing through, not content."""
import struct, sys, zlib

def readpng(path):
    d = open(path, "rb").read()
    i, w, h, ct, idat = 8, 0, 0, 0, b""
    while i < len(d):
        ln = struct.unpack(">I", d[i:i+4])[0]; typ = d[i+4:i+8]; body = d[i+8:i+8+ln]
        if typ == b"IHDR": w, h, _bd, ct = struct.unpack(">IIBB", body[:10])
        elif typ == b"IDAT": idat += body
        i += 8 + ln + 4
    raw = zlib.decompress(idat); ch = {0:1,2:3,4:2,6:4}[ct]; stride = w*ch
    out, prev, pos = bytearray(), bytearray(stride), 0
    for _ in range(h):
        f = raw[pos]; pos += 1
        line = bytearray(raw[pos:pos+stride]); pos += stride
        if f == 1:
            for x in range(ch, stride): line[x] = (line[x]+line[x-ch]) & 255
        elif f == 2:
            for x in range(stride): line[x] = (line[x]+prev[x]) & 255
        elif f == 3:
            for x in range(stride):
                a = line[x-ch] if x >= ch else 0
                line[x] = (line[x]+(a+prev[x])//2) & 255
        elif f == 4:
            for x in range(stride):
                a = line[x-ch] if x >= ch else 0; b = prev[x]; c = prev[x-ch] if x >= ch else 0
                p = a+b-c; pa, pb, pc = abs(p-a), abs(p-b), abs(p-c)
                line[x] = (line[x]+(a if pa <= pb and pa <= pc else b if pb <= pc else c)) & 255
        out += line; prev = line
    return w, h, ch, bytes(out)

w, h, ch, px = readpng(sys.argv[1])
white = grey = 0
for y in range(0, h):
    for x in range(0, w):
        i = (y*w+x)*ch
        r, g, b = px[i], px[i+1], px[i+2]
        if r == 255 and g == 255 and b == 255: white += 1
        elif 230 <= r <= 245 and abs(r-g) < 4 and abs(r-b) < 4: grey += 1
print(f"  pure white pixels: {white}")
print(f"  app body (grey ~0xEDEDED): {grey}")
if white > 2000:
    print(f"FAIL: the window flashed WHITE on resize ({white} pure-white px in the frame right")
    print( "      after the configure, before the client repainted)")
    sys.exit(1)
print("PASS: no white flash; the frame after the configure still shows content")
