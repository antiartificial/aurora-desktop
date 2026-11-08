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
	// Like Init, but KEEPS the pixels that still fit instead of filling white.
	//
	// Init exists for a window being created, where white is as good as anything. A window being
	// RESIZED is different: the repaint that follows is asynchronous -- the app_server posts
	// B_WINDOW_RESIZED and the client answers later -- so every frame presented in between shows
	// whatever Init left behind, which is an opaque white rectangle the size of the window. That
	// is the flash on resize. Keeping the overlap means the stale frame is the previous CONTENT,
	// which is a frame late rather than a different colour.
	//
	// The copy is done in DEVICE pixels, by row. Going through layout units would resample, and
	// at a fractional scale (1.45 is what a real display here asks for) the rounding duplicates
	// and drops rows, which is a mangled window rather than a stale one.
	void Resize(int layoutW, int layoutH, double scale);
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
	// BView::CopyBits: the pixels of src go to dst (same size, layout units), overlap-safe, only inside the clip.
	void CopyBits(FRect src, FRect dst);
	void InvertRect(FRect r);	// B_OP_INVERT: each pixel's RGB becomes 255 - RGB
	void StrokeRect(FRect r, Color c, float pen);
	void StrokeLine(float x0, float y0, float x1, float y1, Color c, float pen);
	void FillRoundRect(FRect r, float rx, float ry, Color c);
	void FillRoundRect(FRect r, float rx, float ry, const Paint& p);
	void FillEllipse(FRect r, Color c);
	void FillEllipse(FRect r, const Paint& p);
	void StrokeEllipse(FRect r, Color c, float pen);
	void StrokeRoundRect(FRect r, float rx, float ry, Color c, float pen);
	void FillPolygon(const float* xy, int n, Color c);	// layout units, pixel centres at +0.5
	// Several closed loops in ONE fill, even-odd between them, so a shape with a hole is one call.
	// Vector icons need it: a counter is a hole, and filling the loops one at a time paints the
	// hole over the shape instead of out of it. `loop_points` gives the point count of each loop
	// and `xy` holds them end to end.
	void FillPolygons(const float* xy, const int* loop_points, int loops, Color c);
	// glyph: pen is the baseline origin in layout units
	void DrawGlyph(const text::GlyphBitmap& g, float penX, float penY, Color c);
	void Blend(int x, int y, Color c, int coverage255);

private:
	void FillDevicePoly(const std::vector<float>& xy, const Paint& p);
	void FillDevicePolys(const std::vector<std::vector<float> >& loops, const Paint& p);
	Color PaintAt(const Paint& p, int x, int y) const;
	int fW, fH;
	double fScale;
	DRect fClip;
	std::vector<uint32_t> fPx;
};

}	// namespace aurora

#endif
