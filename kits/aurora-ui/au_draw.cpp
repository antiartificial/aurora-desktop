// au_draw: the C boundary over Canvas and the text backend. See au_draw.h for why it exists.
//
// Everything here is a thin adapter. The one piece of real behaviour is au_surface_resize,
// which keeps the old pixels instead of clearing -- the defect this toolkit exists partly to
// avoid, and the reason the surface owns its buffer rather than calling Canvas::Init.

#include "au_draw.h"

#include "../interface/appserver/canvas.h"
#include "../interface/appserver/text_backend.h"

#include <cmath>
#include <cstdio>
#include <cstring>
#include <string>
#include <unordered_map>
#include <vector>

using aurora::Canvas;
using aurora::Color;
using aurora::DRect;
using aurora::FRect;
using aurora::Paint;
using aurora::Stop;

namespace {

inline Color C(au_color c) { return Color{c.r, c.g, c.b, c.a}; }
inline FRect R(au_rect r) { return FRect{r.l, r.t, r.r, r.b}; }

aurora::text::Backend* TB() { return aurora::text::DefaultBackend(); }

// One glyph cache entry. Keyed by (font, size, scale, glyph, subpixel bucket): rasterising is
// the expensive half of drawing text and a UI redraws the same strings constantly.
struct GKey {
    int family, style;
    float size, scale;
    uint32_t glyph;
    int subpx;                       // quarter-pixel bucket, 0..3
    bool operator==(const GKey& o) const {
        return family == o.family && style == o.style && size == o.size && scale == o.scale
            && glyph == o.glyph && subpx == o.subpx;
    }
};
struct GHash {
    size_t operator()(const GKey& k) const {
        size_t h = 1469598103934665603ull;
        auto mix = [&](size_t v) { h ^= v; h *= 1099511628211ull; };
        mix((size_t)k.family); mix((size_t)k.style); mix((size_t)(k.size * 64));
        mix((size_t)(k.scale * 64)); mix(k.glyph); mix((size_t)k.subpx);
        return h;
    }
};

}  // namespace

struct au_surface {
    Canvas canvas;
    int layout_w = 0, layout_h = 0;
    double scale = 1.0;
};

// ---- surface ----------------------------------------------------------------------------------

au_surface* au_surface_new(int layout_w, int layout_h, double scale) {
    auto* s = new au_surface();
    s->layout_w = layout_w;
    s->layout_h = layout_h;
    s->scale = scale;
    s->canvas.Init(layout_w, layout_h, scale);
    return s;
}

void au_surface_free(au_surface* s) { delete s; }

int au_surface_resize(au_surface* s, int layout_w, int layout_h, double scale) {
    if (!s) return 0;
    const bool same = (layout_w == s->layout_w && layout_h == s->layout_h && scale == s->scale);
    if (same) return 0;

    // Keep what is on screen rather than flashing white, and let the caller paint over it.
    // Canvas::Resize is the whole of it now. This used to copy the overlap back row by row
    // through DrawImage, whose destination is in LAYOUT units -- so each device row was placed
    // at y / scale and resampled. At scale 1, harmless. At the 1.45 a real display asks for,
    // rows were duplicated and dropped, and the kept frame came back sheared. Canvas::Resize
    // copies in device pixels, where a pixel keeps its position.
    s->layout_w = layout_w;
    s->layout_h = layout_h;
    s->scale = scale;
    s->canvas.Resize(layout_w, layout_h, scale);
    return 1;
}

int au_surface_px_w(const au_surface* s) { return s ? s->canvas.Width() : 0; }
int au_surface_px_h(const au_surface* s) { return s ? s->canvas.Height() : 0; }
double au_surface_scale(const au_surface* s) { return s ? s->canvas.Scale() : 1.0; }
const uint32_t* au_surface_pixels(const au_surface* s) { return s ? s->canvas.Pixels() : nullptr; }

uint32_t* au_surface_pixels_mut(au_surface* s) {
    // Canvas::Pixels() is const and the buffer is not. One const_cast, named and explained here,
    // is better than a second accessor on Canvas that every other caller could reach for.
    return s ? const_cast<uint32_t*>(s->canvas.Pixels()) : nullptr;
}

// ---- drawing ----------------------------------------------------------------------------------

void au_clear(au_surface* s, au_color c) { if (s) s->canvas.Clear(C(c)); }

void au_clip_set(au_surface* s, au_rect r) {
    if (!s) return;
    s->canvas.SetClip(s->canvas.ToDevice(R(r)));
}
void au_clip_reset(au_surface* s) {
    if (s) s->canvas.SetClip(DRect{0, 0, s->canvas.Width(), s->canvas.Height()});
}

void au_fill_rect(au_surface* s, au_rect r, au_color c) { if (s) s->canvas.FillRect(R(r), C(c)); }
void au_fill_round_rect(au_surface* s, au_rect r, float rx, float ry, au_color c) {
    if (s) s->canvas.FillRoundRect(R(r), rx, ry, C(c));
}
void au_stroke_rect(au_surface* s, au_rect r, au_color c, float pen) {
    if (s) s->canvas.StrokeRect(R(r), C(c), pen);
}
void au_stroke_line(au_surface* s, float x0, float y0, float x1, float y1, au_color c, float pen) {
    if (s) s->canvas.StrokeLine(x0, y0, x1, y1, C(c), pen);
}
void au_fill_ellipse(au_surface* s, au_rect r, au_color c) {
    if (s) s->canvas.FillEllipse(R(r), C(c));
}
void au_stroke_round_rect(au_surface* s, au_rect r, float rx, float ry, au_color c, float pen) {
    if (s) s->canvas.StrokeRoundRect(R(r), rx, ry, C(c), pen);
}
void au_fill_polys(au_surface* s, const float* xy, const int* counts, int loops, au_color c) {
    if (s && xy && counts && loops > 0) s->canvas.FillPolygons(xy, counts, loops, C(c));
}

void au_draw_image(au_surface* s, const uint32_t* argb, int sw, int sh, au_rect dst) {
    if (!s || !argb || sw <= 0 || sh <= 0) return;
    s->canvas.DrawImage(argb, sw, sh, FRect{0, 0, (float)(sw - 1), (float)(sh - 1)}, R(dst));
}

void au_fill_rect_gradient(au_surface* s, au_rect r, au_color top, au_color bottom) {
    if (!s) return;
    Paint p;
    p.gradient = true;
    p.x0 = r.l; p.y0 = r.t; p.x1 = r.l; p.y1 = r.b;
    p.stops.push_back(Stop{0.0f, C(top)});
    p.stops.push_back(Stop{1.0f, C(bottom)});
    s->canvas.FillRect(R(r), p);
}

// ---- text -------------------------------------------------------------------------------------

int au_font_system(const char* which, au_font* out, float* size) {
    if (!which || !out) return 0;
    aurora::text::FontKey k{};
    float sz = 0;
    if (!TB()->SystemFont(which, &k, &sz)) return 0;
    out->family = (uint16_t)k.family;
    out->style = (uint16_t)k.style;
    if (size) *size = sz;
    return 1;
}

int au_font_lookup(const char* family, const char* style, au_font* out) {
    if (!family || !style || !out) return 0;
    aurora::text::FontKey k{};
    uint16_t face = 0;
    if (!TB()->Lookup(family, style, &k, &face)) return 0;
    out->family = (uint16_t)k.family;
    out->style = (uint16_t)k.style;
    return 1;
}

int au_font_family_of(au_font f, char* buf, int cap) {
    if (!buf || cap <= 0) return 0;
    std::string name;
    if (!TB()->FamilyName(f.family, &name)) return 0;
    snprintf(buf, (size_t)cap, "%s", name.c_str());
    return 1;
}

int au_font_metrics_get(au_font f, float size, au_font_metrics* out) {
    if (!out) return 0;
    aurora::text::Metrics m{};
    if (!TB()->GetMetrics(aurora::text::FontKey{f.family, f.style}, size, &m)) return 0;
    out->ascent = m.ascent;
    out->descent = m.descent;
    out->leading = m.leading;
    return 1;
}

float au_text_width(au_font f, float size, const char* utf8, int len) {
    if (!utf8) return 0;
    if (len < 0) len = (int)strlen(utf8);
    std::vector<aurora::text::Glyph> gl;
    if (!TB()->Shape(aurora::text::FontKey{f.family, f.style}, size, utf8, len, &gl)) return 0;
    float w = 0;
    for (const auto& g : gl) w += g.advance;
    return w;
}

float au_text_draw(au_surface* s, au_font f, float size, const char* utf8, int len,
                   float x, float y, au_color c) {
    if (!s || !utf8) return 0;
    if (len < 0) len = (int)strlen(utf8);
    const aurora::text::FontKey key{f.family, f.style};
    std::vector<aurora::text::Glyph> gl;
    if (!TB()->Shape(key, size, utf8, len, &gl)) return 0;

    const double sc = s->canvas.Scale();
    // Advance in DEVICE pixels and convert back per glyph, as the app_server does: accumulating
    // in layout units lets rounding drift visibly across a long run.
    double penDev = (double)x * sc;
    for (const auto& g : gl) {
        const double whole = std::floor(penDev);
        const float frac = (float)(penDev - whole);
        const int bucket = (int)(frac * 4.0f) & 3;

        // A HASH lookup, not a linear scan. The first version scanned a vector that grows to
        // thousands of entries, once per glyph: an icon grid of a hundred tiles draws about a
        // thousand glyphs a frame, so the scan was quadratic in the cache size and measured
        // p50 8.25 ms with every frame over the 8 ms budget. I had written down that this was
        // the thing to fix if text-heavy frames got slow; they did.
        static std::unordered_map<GKey, aurora::text::GlyphBitmap, GHash>* cache =
            new std::unordered_map<GKey, aurora::text::GlyphBitmap, GHash>();
        const GKey gk{(int)f.family, (int)f.style, size, (float)sc, g.id, bucket};
        const aurora::text::GlyphBitmap* bm = nullptr;
        auto it = cache->find(gk);
        if (it != cache->end()) {
            bm = &it->second;
        } else {
            aurora::text::GlyphBitmap fresh;
            if (TB()->Rasterize(key, size, (float)sc, g.id, bucket / 4.0f, &fresh)) {
                if (cache->size() > 8192) cache->clear();   // bounded; a UI reuses few glyphs
                bm = &cache->emplace(gk, std::move(fresh)).first->second;
            }
        }
        if (bm && bm->width > 0)
            s->canvas.DrawGlyph(*bm, (float)(whole / sc), y + g.y_offset, C(c));
        penDev += (double)g.advance * sc;
    }
    return (float)(penDev / sc) - x;
}
