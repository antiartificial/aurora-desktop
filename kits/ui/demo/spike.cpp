// The architecture spike for aurora::ui: a window that opens on Wayland, draws through our own
// canvas, and presents through our own wl_shm path -- with NO app_server. No ports, no AS_*
// opcodes, no BView, no BLooper.
//
// It exists to prove or kill the decision in docs/aurora-ui.md before a toolkit is written on
// top of it, and it reports the three things that decision rests on:
//
//   1. a frame reaches the compositor at all, through present_wl.c, drawn by canvas.cpp and
//      shaped by FreeType/HarfBuzz
//   2. a resize does NOT wipe the window white. The app_server's ApplyWindowResize calls
//      Canvas::Init, which fills the buffer opaque white and only then asks for a repaint, so a
//      resize flashes -- 19 MB and about 5.4 ms at scale 1.45. au_surface_resize keeps the
//      pixels, and this prints the proof as a before/after sample of one pixel.
//   3. what a frame costs, so the 8 ms handler rule is a measurement rather than a wish
//
// SPIKE_FRAMES=n quits after n frames. SPIKE_RESIZE=WxH resizes once, at frame 3.

#include "../au_draw.h"
#include "../../render/present.h"

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <vector>

namespace {

struct App {
    au_wl* wl = nullptr;
    au_wl_win* win = nullptr;
    au_surface* sf = nullptr;
    int lw = 520, lh = 300;
    double scale = 1.0;
    au_font plain{}, bold{};
    float plain_sz = 13, bold_sz = 13;

    int frames = 0, want_frames = 0;
    bool quit = false;
    int hover = -1, pressed = -1, clicks = 0;
    float px = -1, py = -1;
    std::vector<double> draw_ms;

    bool resized = false;
    uint32_t before_px = 0, after_px = 0;
    int resize_w = 0, resize_h = 0;
};

App g;

const au_color kBg{24, 26, 34, 255};
const au_color kBgLo{16, 17, 24, 255};
const au_color kCard{38, 41, 54, 255};
const au_color kCardHi{52, 56, 73, 255};
const au_color kAccent{122, 162, 247, 255};
const au_color kText{236, 239, 244, 255};
const au_color kDim{150, 157, 178, 255};

struct Btn { au_rect r; const char* label; };

// Two buttons, placed by arithmetic rather than a layout engine on purpose: the layout engine is
// the next stage, and this spike must not depend on the thing it is meant to justify.
std::vector<Btn> buttons() {
    const float w = 150, h = 34, y = (float)g.lh - 60;
    return {
        Btn{au_rect{24, y, 24 + w - 1, y + h - 1}, "Click me"},
        Btn{au_rect{24 + w + 12, y, 24 + w + 12 + w - 1, y + h - 1}, "Quit"},
    };
}

int hit(float x, float y) {
    const std::vector<Btn> bs = buttons();
    for (size_t i = 0; i < bs.size(); i++) {
        const au_rect& r = bs[i].r;
        if (x >= r.l && x <= r.r && y >= r.t && y <= r.b) return (int)i;
    }
    return -1;
}

void draw() {
    const std::chrono::steady_clock::time_point t0 = std::chrono::steady_clock::now();

    // SPIKE_FLAT=1 swaps the full-window gradient for a flat fill, to attribute the frame cost.
    if (getenv("SPIKE_FLAT"))
        au_fill_rect(g.sf, au_rect{0, 0, (float)g.lw - 1, (float)g.lh - 1}, kBg);
    else
        au_fill_rect_gradient(g.sf, au_rect{0, 0, (float)g.lw - 1, (float)g.lh - 1}, kBg, kBgLo);
    au_fill_round_rect(g.sf, au_rect{16, 16, (float)g.lw - 17, 120}, 10, 10, kCard);
    au_fill_rect(g.sf, au_rect{16, 16, (float)g.lw - 17, 18}, kAccent);

    au_text_draw(g.sf, g.bold, g.bold_sz + 4, "aurora::ui", -1, 32, 56, kText);
    au_text_draw(g.sf, g.plain, g.plain_sz, "no app_server, no ports, no BView", -1, 32, 78, kDim);

    char buf[160];
    snprintf(buf, sizeof buf, "%dx%d at scale %.2f  frame %d  %d click%s",
             g.lw, g.lh, g.scale, g.frames, g.clicks, g.clicks == 1 ? "" : "s");
    au_text_draw(g.sf, g.plain, g.plain_sz, buf, -1, 32, 100, kDim);

    // Shaping, not a bitmap font: ligatures and diacritics in one run.
    au_text_draw(g.sf, g.plain, g.plain_sz, "HarfBuzz: fi ffl \xc3\x84\xc3\xb6\xc3\xbc 123", -1,
                 32, 148, kText);

    const std::vector<Btn> bs = buttons();
    for (size_t i = 0; i < bs.size(); i++) {
        const bool down = g.pressed == (int)i;
        const bool over = g.hover == (int)i;
        au_fill_round_rect(g.sf, bs[i].r, 7, 7, down ? kAccent : (over ? kCardHi : kCard));
        const float tw = au_text_width(g.plain, g.plain_sz, bs[i].label, -1);
        const float cx = bs[i].r.l + ((bs[i].r.r - bs[i].r.l + 1) - tw) / 2;
        au_font_metrics m;
        m.ascent = m.descent = m.leading = 0;
        au_font_metrics_get(g.plain, g.plain_sz, &m);
        const float cy = bs[i].r.t + ((bs[i].r.b - bs[i].r.t + 1) + m.ascent - m.descent) / 2;
        au_text_draw(g.sf, g.plain, g.plain_sz, bs[i].label, -1, cx, cy, down ? kBg : kText);
    }

    const double ms = std::chrono::duration<double, std::milli>(
                          std::chrono::steady_clock::now() - t0).count();
    g.draw_ms.push_back(ms);

    au_wl_win_present(g.win, au_surface_pixels(g.sf), au_surface_px_w(g.sf),
                      au_surface_px_h(g.sf), (int)g.scale);
    g.frames++;
}

uint32_t sample(int dx, int dy) {
    const uint32_t* p = au_surface_pixels(g.sf);
    if (!p) return 0;
    const int w = au_surface_px_w(g.sf), h = au_surface_px_h(g.sf);
    if (dx < 0 || dy < 0 || dx >= w || dy >= h) return 0;
    return p[(size_t)dy * w + dx];
}

void on_event(void*, int kind, int a, int b) {
    if (kind == AU_EV_CLOSE) { g.quit = true; return; }
    if (kind == AU_EV_RESIZE) {
        if (a <= 0 || b <= 0) return;
        g.lw = a; g.lh = b;
        au_surface_resize(g.sf, g.lw, g.lh, g.scale);
        draw();
        return;
    }
    if (kind == AU_EV_POINTER_MOVE) {
        g.px = a / 256.0f; g.py = b / 256.0f;
        const int h = hit(g.px, g.py);
        if (h != g.hover) { g.hover = h; draw(); }
        return;
    }
    if (kind == AU_EV_POINTER_LEAVE) {
        if (g.hover != -1) { g.hover = -1; draw(); }
        return;
    }
    if (kind == AU_EV_POINTER_BUTTON && a == 1) {
        if (b) { g.pressed = hit(g.px, g.py); draw(); }
        else {
            const int up = hit(g.px, g.py);
            if (up >= 0 && up == g.pressed) { if (up == 1) g.quit = true; else g.clicks++; }
            g.pressed = -1;
            draw();
        }
    }
}

}  // namespace

int main() {
    if (const char* s = getenv("SPIKE_FRAMES")) g.want_frames = atoi(s);
    if (const char* s = getenv("SPIKE_RESIZE")) {
        if (sscanf(s, "%dx%d", &g.resize_w, &g.resize_h) != 2) { g.resize_w = 0; g.resize_h = 0; }
    }

    g.wl = au_wl_connect();
    if (!g.wl) { fprintf(stderr, "spike: no Wayland compositor reachable\n"); return 2; }
    g.scale = au_wl_output_scale(g.wl);
    if (g.scale < 1) g.scale = 1;

    if (!au_font_system("plain", &g.plain, &g.plain_sz)) {
        fprintf(stderr, "spike: no plain system font\n");
        return 3;
    }
    if (!au_font_system("bold", &g.bold, &g.bold_sz)) g.bold = g.plain;

    g.sf = au_surface_new(g.lw, g.lh, g.scale);
    g.win = au_wl_win_create(g.wl, "aurora-ui spike", g.lw, g.lh, on_event, NULL);
    if (!g.win) { fprintf(stderr, "spike: could not create a window\n"); return 4; }

    printf("SPIKE up: %dx%d scale %.2f, surface %dx%d px, no app_server\n",
           g.lw, g.lh, g.scale, au_surface_px_w(g.sf), au_surface_px_h(g.sf));
    fflush(stdout);

    draw();
    while (!g.quit) {
        if (g.want_frames && g.frames >= g.want_frames) break;

        // Sample a pixel, resize, sample the SAME pixel before any repaint. If the resize
        // cleared the buffer the second read is white; if it kept the pixels it is what was drawn.
        if (g.resize_w && !g.resized && g.frames >= 3) {
            g.resized = true;
            g.before_px = sample(40, 40);
            au_surface_resize(g.sf, g.resize_w, g.resize_h, g.scale);
            g.after_px = sample(40, 40);
            g.lw = g.resize_w; g.lh = g.resize_h;
            printf("SPIKE resize %dx%d: pixel(40,40) before=%08x after=%08x %s\n",
                   g.resize_w, g.resize_h, g.before_px, g.after_px,
                   g.after_px == g.before_px ? "KEPT" : "LOST");
            fflush(stdout);
        }
        draw();
    }

    if (!g.draw_ms.empty()) {
        std::vector<double> v = g.draw_ms;
        std::sort(v.begin(), v.end());
        printf("SPIKE frames=%zu draw_ms p50=%.3f p95=%.3f max=%.3f over_8ms=%zu\n",
               v.size(), v[v.size() / 2], v[(size_t)(v.size() * 0.95)], v.back(),
               (size_t)std::count_if(v.begin(), v.end(), [](double d) { return d > 8.0; }));
    }
    printf("SPIKE clicks=%d\n", g.clicks);
    fflush(stdout);

    au_wl_win_destroy(g.win);
    au_surface_free(g.sf);
    return 0;
}
