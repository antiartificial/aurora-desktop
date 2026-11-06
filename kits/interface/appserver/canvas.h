/*
 * Software canvas for the app_server analogue. Written fresh.
 * Coordinates handed to the drawing calls are LAYOUT units (what BView code uses); the canvas multiplies by
 * its scale and rasterises in device pixels. Rects follow BeOS: BRect(0,0,9,9) covers 10x10 layout pixels,
 * i.e. [left, right+1). Pixels are 0xAARRGGBB, alpha always 0xFF (opaque windows).
 */
#ifndef AURORA_CANVAS_H
#define AURORA_CANVAS_H

#include <stdint.h>
#include <vector>
#include "text_backend.h"

namespace aurora {

struct Color { uint8_t r, g, b, a; };
struct Stop { float offset; Color c; };
// A paint is a solid colour or a linear gradient (device-space endpoints are derived from layout units).
struct Paint {
	Color solid{0, 0, 0, 255};
	bool gradient = false;
	float x0 = 0, y0 = 0, x1 = 0, y1 = 0;	// layout units, window space
	std::vector<Stop> stops;
	Paint() {}
	Paint(Color c) : solid(c) {}
};
struct FRect { float l, t, r, b; };	// BeOS-inclusive: width = r - l + 1 in layout units
struct DRect { int x0, y0, x1, y1; };	// device pixels, half-open

class Canvas {
public:
	Canvas() : fW(0), fH(0), fScale(1.0) {}
	void Init(int layoutW, int layoutH, double scale);	// device size = ceil(layout * scale)
	int Width() const { return fW; }		// device px
	int Height() const { return fH; }
	double Scale() const { return fScale; }
	const uint32_t* Pixels() const { return fPx.data(); }
	void Clear(Color c);

	void SetClip(DRect r) { fClip = Intersect(r, DRect{0, 0, fW, fH}); }
	DRect Clip() const { return fClip; }
	DRect ToDevice(FRect r) const;
	static DRect Intersect(DRect a, DRect b);

	void FillRect(FRect r, Color c);
	void FillRect(FRect r, const Paint& p);
	// Straight-alpha ARGB image (sw x sh), source rect in image pixels, destination in layout units; nearest neighbour.
	void DrawImage(const uint32_t* argb, int sw, int sh, FRect src, FRect dst);
	void InvertRect(FRect r);	// B_OP_INVERT: each pixel's RGB becomes 255 - RGB
	void StrokeRect(FRect r, Color c, float pen);
	void StrokeLine(float x0, float y0, float x1, float y1, Color c, float pen);
	void FillRoundRect(FRect r, float rx, float ry, Color c);
	void FillRoundRect(FRect r, float rx, float ry, const Paint& p);
	void FillEllipse(FRect r, Color c);
	void FillPolygon(const float* xy, int n, Color c);	// layout units, pixel centres at +0.5
	// glyph: pen is the baseline origin in layout units
	void DrawGlyph(const text::GlyphBitmap& g, float penX, float penY, Color c);
	void Blend(int x, int y, Color c, int coverage255);

private:
	void FillDevicePoly(const std::vector<float>& xy, const Paint& p);
	Color PaintAt(const Paint& p, int x, int y) const;
	int fW, fH;
	double fScale;
	DRect fClip;
	std::vector<uint32_t> fPx;
};

}	// namespace aurora

#endif
