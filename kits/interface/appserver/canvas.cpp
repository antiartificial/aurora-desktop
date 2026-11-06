#include "canvas.h"

#include <math.h>
#include <string.h>
#include <algorithm>

namespace aurora {

void Canvas::Init(int lw, int lh, double scale)
{
	fScale = scale;
	fW = (int)ceil(lw * scale);
	fH = (int)ceil(lh * scale);
	fPx.assign((size_t)fW * fH, 0xFFFFFFFFu);
	fClip = DRect{0, 0, fW, fH};
}

void Canvas::Clear(Color c)
{
	uint32_t v = 0xFF000000u | (c.r << 16) | (c.g << 8) | c.b;
	std::fill(fPx.begin(), fPx.end(), v);
}

DRect Canvas::Intersect(DRect a, DRect b)
{
	DRect r{std::max(a.x0, b.x0), std::max(a.y0, b.y0), std::min(a.x1, b.x1), std::min(a.y1, b.y1)};
	if (r.x1 < r.x0) r.x1 = r.x0;
	if (r.y1 < r.y0) r.y1 = r.y0;
	return r;
}

DRect Canvas::ToDevice(FRect r) const
{
	// BeOS rect is inclusive of right/bottom: the covered span is [l, r+1) layout units.
	return DRect{(int)lround(r.l * fScale), (int)lround(r.t * fScale),
		(int)lround((r.r + 1) * fScale), (int)lround((r.b + 1) * fScale)};
}

void Canvas::Blend(int x, int y, Color c, int cov)
{
	if (x < fClip.x0 || y < fClip.y0 || x >= fClip.x1 || y >= fClip.y1 || cov <= 0) return;
	int a = (cov * c.a + 127) / 255;
	uint32_t& d = fPx[(size_t)y * fW + x];
	if (a >= 255) { d = 0xFF000000u | (c.r << 16) | (c.g << 8) | c.b; return; }
	int dr = (d >> 16) & 255, dg = (d >> 8) & 255, db = d & 255;
	dr += ((c.r - dr) * a + 127) / 255;
	dg += ((c.g - dg) * a + 127) / 255;
	db += ((c.b - db) * a + 127) / 255;
	d = 0xFF000000u | (dr << 16) | (dg << 8) | db;
}

void Canvas::FillRect(FRect r, Color c)
{
	DRect d = Intersect(ToDevice(r), fClip);
	uint32_t v = 0xFF000000u | (c.r << 16) | (c.g << 8) | c.b;
	if (c.a == 255) {
		for (int y = d.y0; y < d.y1; y++)
			for (int x = d.x0; x < d.x1; x++) fPx[(size_t)y * fW + x] = v;
	} else {
		for (int y = d.y0; y < d.y1; y++)
			for (int x = d.x0; x < d.x1; x++) Blend(x, y, c, 255);
	}
}

void Canvas::StrokeRect(FRect r, Color c, float pen)
{
	StrokeLine(r.l, r.t, r.r, r.t, c, pen);
	StrokeLine(r.l, r.b, r.r, r.b, c, pen);
	StrokeLine(r.l, r.t, r.l, r.b, c, pen);
	StrokeLine(r.r, r.t, r.r, r.b, c, pen);
}

// Even-odd polygon fill with 4x4 supersampling, in device space.
void Canvas::FillDevicePoly(const std::vector<float>& xy, const Paint& paint)
{
	int n = (int)xy.size() / 2;
	if (n < 3) return;
	float minx = xy[0], maxx = xy[0], miny = xy[1], maxy = xy[1];
	for (int i = 1; i < n; i++) {
		minx = std::min(minx, xy[2 * i]); maxx = std::max(maxx, xy[2 * i]);
		miny = std::min(miny, xy[2 * i + 1]); maxy = std::max(maxy, xy[2 * i + 1]);
	}
	int x0 = std::max((int)floorf(minx), fClip.x0), x1 = std::min((int)ceilf(maxx), fClip.x1);
	int y0 = std::max((int)floorf(miny), fClip.y0), y1 = std::min((int)ceilf(maxy), fClip.y1);
	if (x1 <= x0 || y1 <= y0) return;
	const int S = 4;
	std::vector<uint8_t> cov((size_t)(x1 - x0), 0);
	std::vector<float> xs;
	for (int y = y0; y < y1; y++) {
		std::fill(cov.begin(), cov.end(), 0);
		for (int sy = 0; sy < S; sy++) {
			float py = y + (sy + 0.5f) / S;
			xs.clear();
			for (int i = 0; i < n; i++) {
				float ax = xy[2 * i], ay = xy[2 * i + 1];
				float bx = xy[2 * ((i + 1) % n)], by = xy[2 * ((i + 1) % n) + 1];
				if ((ay <= py && by > py) || (by <= py && ay > py))
					xs.push_back(ax + (py - ay) * (bx - ax) / (by - ay));
			}
			std::sort(xs.begin(), xs.end());
			for (size_t k = 0; k + 1 < xs.size(); k += 2) {
				float a = xs[k], b = xs[k + 1];
				int xa = std::max((int)floorf(a), x0), xb = std::min((int)ceilf(b), x1);
				for (int x = xa; x < xb; x++) {
					float l = std::max(a, (float)x), r = std::min(b, (float)x + 1);
					if (r > l) cov[x - x0] = (uint8_t)std::min(255, cov[x - x0] + (int)((r - l) * 255.0f / S + 0.5f));
				}
			}
		}
		for (int x = x0; x < x1; x++) if (cov[x - x0]) Blend(x, y, PaintAt(paint, x, y), cov[x - x0]);
	}
}

void Canvas::FillPolygon(const float* xy, int n, Color c)
{
	std::vector<float> d(2 * n);
	for (int i = 0; i < 2 * n; i++) d[i] = xy[i] * (float)fScale;
	FillDevicePoly(d, Paint(c));
}

void Canvas::StrokeLine(float x0, float y0, float x1, float y1, Color c, float pen)
{
	// BeOS: the pen is centred on pixel centres, so (x,y) means the middle of pixel (x,y).
	float ax = (x0 + 0.5f) * fScale, ay = (y0 + 0.5f) * fScale, bx = (x1 + 0.5f) * fScale, by = (y1 + 0.5f) * fScale;
	float w = std::max(pen, 0.0f) * (float)fScale * 0.5f;
	float dx = bx - ax, dy = by - ay, len = sqrtf(dx * dx + dy * dy);
	if (len < 1e-6f) {	// a point: a pen-sized square
		std::vector<float> p = {ax - w, ay - w, ax + w, ay - w, ax + w, ay + w, ax - w, ay + w};
		FillDevicePoly(p, Paint(c)); return;
	}
	float nx = -dy / len * w, ny = dx / len * w, ex = dx / len * w, ey = dy / len * w;
	// butt caps extended by half a pen so axis-aligned 1px lines cover exactly their end pixels
	std::vector<float> p = {ax - nx - ex, ay - ny - ey, bx - nx + ex, by - ny + ey,
		bx + nx + ex, by + ny + ey, ax + nx - ex, ay + ny - ey};
	FillDevicePoly(p, Paint(c));
}

static void RoundRectPath(std::vector<float>& p, FRect r, float rx, float ry, double s)
{
	float l = r.l * s, t = r.t * s, rr = (r.r + 1) * s, bb = (r.b + 1) * s;
	rx = std::min(rx * (float)s, (rr - l) / 2); ry = std::min(ry * (float)s, (bb - t) / 2);
	const int N = 16;
	float cx[4] = {rr - rx, l + rx, l + rx, rr - rx}, cy[4] = {t + ry, t + ry, bb - ry, bb - ry};
	for (int q = 0; q < 4; q++)
		for (int i = 0; i <= N; i++) {
			float a = (float)M_PI * 0.5f * (q + (float)i / N) - (float)M_PI * 0.5f;
			p.push_back(cx[q] + cosf(a) * rx); p.push_back(cy[q] + sinf(a) * ry);
		}
}

void Canvas::FillRoundRect(FRect r, float rx, float ry, Color c) { FillRoundRect(r, rx, ry, Paint(c)); }
void Canvas::FillRoundRect(FRect r, float rx, float ry, const Paint& pt)
{
	std::vector<float> p; RoundRectPath(p, r, rx, ry, fScale); FillDevicePoly(p, pt);
}

Color Canvas::PaintAt(const Paint& p, int x, int y) const
{
	if (!p.gradient || p.stops.empty()) return p.solid;
	// project the pixel centre (layout units) on the gradient axis
	float px = (x + 0.5f) / (float)fScale, py = (y + 0.5f) / (float)fScale;
	float dx = p.x1 - p.x0, dy = p.y1 - p.y0, len2 = dx * dx + dy * dy;
	float t = len2 > 0 ? ((px - p.x0) * dx + (py - p.y0) * dy) / len2 : 0;
	if (t <= p.stops.front().offset) return p.stops.front().c;
	if (t >= p.stops.back().offset) return p.stops.back().c;
	for (size_t i = 1; i < p.stops.size(); i++) {
		if (t <= p.stops[i].offset) {
			const Stop& a = p.stops[i - 1]; const Stop& b = p.stops[i];
			float u = b.offset > a.offset ? (t - a.offset) / (b.offset - a.offset) : 0;
			return Color{(uint8_t)(a.c.r + (b.c.r - a.c.r) * u + 0.5f), (uint8_t)(a.c.g + (b.c.g - a.c.g) * u + 0.5f),
				(uint8_t)(a.c.b + (b.c.b - a.c.b) * u + 0.5f), (uint8_t)(a.c.a + (b.c.a - a.c.a) * u + 0.5f)};
		}
	}
	return p.stops.back().c;
}

void Canvas::FillRect(FRect r, const Paint& p)
{
	DRect d = Intersect(ToDevice(r), fClip);
	for (int y = d.y0; y < d.y1; y++)
		for (int x = d.x0; x < d.x1; x++) Blend(x, y, PaintAt(p, x, y), 255);
}

void Canvas::FillEllipse(FRect r, Color c)
{
	std::vector<float> p;
	float cx = (r.l + r.r + 1) * 0.5f * fScale, cy = (r.t + r.b + 1) * 0.5f * fScale;
	float rx = (r.r - r.l + 1) * 0.5f * fScale, ry = (r.b - r.t + 1) * 0.5f * fScale;
	for (int i = 0; i < 48; i++) { float a = 2 * (float)M_PI * i / 48; p.push_back(cx + cosf(a) * rx); p.push_back(cy + sinf(a) * ry); }
	FillDevicePoly(p, c);
}

void Canvas::DrawGlyph(const text::GlyphBitmap& g, float penX, float penY, Color c)
{
	int ox = (int)floorf(penX * fScale), oy = (int)lroundf(penY * fScale);
	for (int y = 0; y < g.height; y++) {
		int dy = oy - g.top + y;
		for (int x = 0; x < g.width; x++) {
			int a = g.alpha[(size_t)y * g.pitch + x];
			if (a) Blend(ox + g.left + x, dy, c, a);
		}
	}
}

}	// namespace aurora
