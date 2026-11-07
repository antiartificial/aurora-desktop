/*
 * au_draw: a flat C surface over Aurora's drawing substrate.
 *
 * Why this exists, rather than calling Canvas and text::Backend directly from C++.
 *
 * The substrate is good and we own it: canvas.cpp rasterises (fills, strokes, round rects,
 * ellipses, gradients, clip, glyph blend), fonts.cpp and text_backend give FreeType + HarfBuzz,
 * present_wl.c puts a frame on a Wayland surface. None of it knows what a BView is, and together
 * it is about a thousand lines.
 *
 * But it is C++ with std::vector in its value types, which makes it awkward to call from anything
 * else. A flat C boundary is worth the ~200 lines because:
 *
 *   1. It is the fast, boring FFI that every language already speaks. Swift's C interop is mature
 *      where its C++ interop is young; Rust's is trivial. Adopting another language for the
 *      toolkit later becomes a binding exercise against this header, not a port.
 *   2. It names exactly what a toolkit may do to a surface. Everything above it is layout, state
 *      and events -- the part worth rewriting -- and everything below is pixels, which is not.
 *   3. present_wl.c is already C, so half the boundary existed anyway.
 *
 * Units: LAYOUT units throughout, as Canvas uses them, and rects are BeOS-inclusive -- {0,0,9,9}
 * is ten units wide. The surface multiplies by its scale and rasterises in device pixels, so a
 * caller never does scale arithmetic and text stays crisp at fractional scales.
 */
#ifndef AURORA_AU_DRAW_H
#define AURORA_AU_DRAW_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct au_surface au_surface;

typedef struct { float l, t, r, b; } au_rect;      /* BeOS-inclusive: width = r - l + 1 */
typedef struct { uint8_t r, g, b, a; } au_color;

/* ---- the surface: a canvas sized in layout units, rasterising at `scale` ---- */

au_surface* au_surface_new(int layout_w, int layout_h, double scale);
void        au_surface_free(au_surface*);
/* Resize WITHOUT clearing. This is the whole point of the call: the app_server's
 * ApplyWindowResize calls Canvas::Init, which fills the buffer opaque white (19 MB and about
 * 5.4 ms at scale 1.45 on a 1920x1200 window) and only then asks the client to repaint -- so a
 * resize shows a white flash. Keeping the old pixels and painting over them removes the flash
 * and the memset. Returns 1 when the buffer was reallocated. */
int         au_surface_resize(au_surface*, int layout_w, int layout_h, double scale);
int         au_surface_px_w(const au_surface*);
int         au_surface_px_h(const au_surface*);
double      au_surface_scale(const au_surface*);
const uint32_t* au_surface_pixels(const au_surface*);   /* 0xAARRGGBB, px_w * px_h */
/* The same pixels, writable. For a surface a caller owns and is using as an OFFSCREEN -- the
 * icon cache rasterises into one and then makes its dimmed variant by taking the alpha down
 * once, which is a transform of a cached image rather than a per-frame blend. Not for the
 * window's own surface: everything there goes through the drawing calls. */
uint32_t* au_surface_pixels_mut(au_surface*);

/* ---- drawing ---- */

void au_clear(au_surface*, au_color);
void au_clip_set(au_surface*, au_rect);      /* layout units; intersected with the surface */
void au_clip_reset(au_surface*);
void au_fill_rect(au_surface*, au_rect, au_color);
void au_fill_round_rect(au_surface*, au_rect, float rx, float ry, au_color);
void au_stroke_rect(au_surface*, au_rect, au_color, float pen);
void au_stroke_line(au_surface*, float x0, float y0, float x1, float y1, au_color, float pen);
void au_fill_ellipse(au_surface*, au_rect, au_color);
/* A rounded-rect OUTLINE. The selection ring of a focused row is a rounded stroke, and stroking
 * a square rect inside a rounded fill shows the fill's corners outside the ring. */
void au_stroke_round_rect(au_surface*, au_rect, float rx, float ry, au_color, float pen);
/* Several closed loops in one even-odd fill, in layout units: `counts` gives each loop's point
 * count and `xy` holds x,y pairs end to end. This is what a vector icon rasterises through --
 * a path with a hole is one fill, not two. */
void au_fill_polys(au_surface*, const float* xy, const int* counts, int loops, au_color);
/* A vertical linear gradient between two colours, over the rect, in layout units. */
void au_fill_rect_gradient(au_surface*, au_rect, au_color top, au_color bottom);

/* Blit a straight-alpha ARGB image. Used for anything rasterised once and reused -- icons, and
 * later thumbnails. dst is in layout units; at 1:1 it is a copy. */
void au_draw_image(au_surface*, const uint32_t* argb, int sw, int sh, au_rect dst);

/* ---- text ---- */

typedef struct { uint16_t family, style; } au_font;        /* opaque pair, from au_font_system */
typedef struct { float ascent, descent, leading; } au_font_metrics;

/* which: "plain" | "bold" | "fixed". Returns 0 when the backend has no such font. */
int   au_font_system(const char* which, au_font* out, float* size);
/* A named face of a named family, e.g. ("DejaVu Sans", "Italic"). Returns 0 when there is no
 * such face, so a caller can fall back rather than draw nothing. The system fonts above do not
 * cover italic: Haiku's SystemFont only knows plain, bold and fixed. */
int   au_font_lookup(const char* family, const char* style, au_font* out);
/* The family name of a face, for finding its italic sibling. */
int   au_font_family_of(au_font, char* buf, int cap);
int   au_font_metrics_get(au_font, float size, au_font_metrics* out);
/* Advance width of a UTF-8 run, in layout units. len < 0 means NUL-terminated. */
float au_text_width(au_font, float size, const char* utf8, int len);
/* Draw a UTF-8 run with its BASELINE at (x, y), in layout units. Returns the advance. */
float au_text_draw(au_surface*, au_font, float size, const char* utf8, int len,
                   float x, float y, au_color);

#ifdef __cplusplus
}
#endif
#endif
