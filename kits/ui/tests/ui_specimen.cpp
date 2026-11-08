// ui_specimen: the toolkit's own components, rasterised to the PNG sheets docs/ references.
//
// A design document that draws its own component gallery by hand drifts from the toolkit the
// first time the toolkit changes, and the reader has no way to tell. These sheets are the real
// nodes -- ListView, IconView, the icon family, the controls -- Measured, Placed and drawn into
// an offscreen surface by the same code paths a window uses, so the picture in the document is
// a rasterisation of the shipping toolkit rather than an idealisation of it.
//
// It is a generator and not a test: it asserts nothing. tests/test_ui_toolkit.cpp makes the
// claims; this one shows what they look like. It lives beside it because it needs exactly the
// same machinery (a headless surface, a Rows source, a laid-out Desc) and nothing else.
//
//   kits/build/ui_specimen docs/images        # writes specimen-*.png
//
// Every sheet is written twice, at scale 1.0 and at 1.45, because 1.45 is what a real display
// here asks for and because a component that only breaks at a fractional scale has shipped
// twice in this repo.
#include "ui.h"

#include <zlib.h>

#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <algorithm>
#include <map>
#include <string>
#include <vector>

using namespace aurora::ui;

// ---- PNG, with no image library ---------------------------------------------------------------
//
// 8-bit RGB, one zlib stream, filter 0 per row. zlib is already a dependency of everything that
// reads a theme; a PNG encoder is forty lines on top of it and avoids adding libpng to a target
// that exists to draw pictures for a document.
static void be32(std::string& out, uint32_t v) {
    const uint8_t b[4] = {(uint8_t)(v >> 24), (uint8_t)(v >> 16), (uint8_t)(v >> 8), (uint8_t)v};
    out.append((const char*)b, 4);
}

static void chunk(std::string& out, const char* tag, const std::string& data) {
    be32(out, (uint32_t)data.size());
    std::string body(tag);
    body += data;
    out += body;
    be32(out, (uint32_t)crc32(0, (const Bytef*)body.data(), (uInt)body.size()));
}

static bool write_png(const std::string& path, const uint32_t* argb, int w, int h) {
    std::string raw;
    raw.reserve((size_t)h * (1 + (size_t)w * 3));
    for (int y = 0; y < h; y++) {
        raw.push_back(0);                                  // filter: none
        for (int x = 0; x < w; x++) {
            const uint32_t p = argb[(size_t)y * w + x];
            raw.push_back((char)((p >> 16) & 0xff));
            raw.push_back((char)((p >> 8) & 0xff));
            raw.push_back((char)(p & 0xff));
        }
    }
    uLongf cap = compressBound((uLong)raw.size());
    std::string z((size_t)cap, '\0');
    if (compress2((Bytef*)&z[0], &cap, (const Bytef*)raw.data(), (uLong)raw.size(), 9) != Z_OK)
        return false;
    z.resize((size_t)cap);

    std::string png("\x89PNG\r\n\x1a\n", 8);
    std::string ihdr;
    be32(ihdr, (uint32_t)w);
    be32(ihdr, (uint32_t)h);
    const char tail[5] = {8, 2, 0, 0, 0};                  // 8-bit, truecolour, deflate, no interlace
    ihdr.append(tail, 5);
    chunk(png, "IHDR", ihdr);
    chunk(png, "IDAT", z);
    chunk(png, "IEND", std::string());

    FILE* f = fopen(path.c_str(), "wb");
    if (!f) return false;
    const bool ok = fwrite(png.data(), 1, png.size(), f) == png.size();
    fclose(f);
    return ok;
}

// ---- sheet plumbing ---------------------------------------------------------------------------

static Env g_env;
static double g_scale = 1.0;

static void lay(const Desc& d, Rect box) {
    d->Measure(g_env, Constraints{box.w, box.h});
    d->Place(box);
}

// Copy a crop of one offscreen surface into another, in DEVICE pixels at the shared scale, so a
// strip can be lifted out of a widget's frame (a list without its header) without the widget
// knowing it is being cropped.
static void blit(au_surface* dst, float dx, float dy, au_surface* src, float sx, float sy,
                 float sw, float sh) {
    const double sc = au_surface_scale(dst);
    int dX = (int)lround(dx * sc), dY = (int)lround(dy * sc);
    int sX = (int)lround(sx * sc), sY = (int)lround(sy * sc);
    int w = (int)lround(sw * sc), h = (int)lround(sh * sc);
    if (sX + w > au_surface_px_w(src)) w = au_surface_px_w(src) - sX;
    if (sY + h > au_surface_px_h(src)) h = au_surface_px_h(src) - sY;
    if (dX + w > au_surface_px_w(dst)) w = au_surface_px_w(dst) - dX;
    if (dY + h > au_surface_px_h(dst)) h = au_surface_px_h(dst) - dY;
    if (w <= 0 || h <= 0) return;
    uint32_t* d = au_surface_pixels_mut(dst);
    const uint32_t* s = au_surface_pixels(src);
    for (int y = 0; y < h; y++)
        memcpy(d + (size_t)(dY + y) * au_surface_px_w(dst) + dX,
               s + (size_t)(sY + y) * au_surface_px_w(src) + sX, (size_t)w * 4);
}

// A node drawn into its own surface, so what lands on the sheet can be cropped.
static au_surface* render(const Desc& d, float w, float h, au_color bg) {
    au_surface* s = au_surface_new((int)ceilf(w), (int)ceilf(h), g_scale);
    au_clear(s, bg);
    lay(d, Rect{0, 0, w, h});
    d->Draw(s, g_env);
    return s;
}

static void text(au_surface* s, float x, float baseline, const std::string& t, au_color c,
                 bool bold = false, float size_delta = 0) {
    au_text_draw(s, bold ? g_env.bold : g_env.font, g_env.size + size_delta, t.c_str(), -1, x,
                 baseline, c);
}

// A section heading, and the hairline under it: the sheets carry several specimens each and the
// reader needs to know where one stops.
static float heading(au_surface* s, float x, float y, float w, const std::string& t) {
    text(s, x, y + 11, t, g_env.fg, true, 1);
    au_stroke_line(s, x, y + 18, x + w, y + 18, g_env.hairline, 1);
    return y + 30;
}

// ---- the sources ------------------------------------------------------------------------------

struct Entry {
    std::string name, kind, size;
    IconKind k = IconKind::Generic;
    IconMark mark = IconMark::None;
    IconBadge badge = IconBadge::None;
    bool dim = false;
};

// The listing of docs/atlas-design.md 4.1 and 4.3: the nine kinds, the eight special folders,
// the two badges and a hidden entry, which is every picture the procedural family can draw.
static std::vector<Entry> family() {
    return {
        {"Home", "Home", "\xe2\x80\x94", IconKind::Folder, IconMark::Home},
        {"Documents", "Documents", "\xe2\x80\x94", IconKind::Folder, IconMark::Documents},
        {"Downloads", "Downloads", "\xe2\x80\x94", IconKind::Folder, IconMark::Downloads},
        {"Pictures", "Pictures", "\xe2\x80\x94", IconKind::Folder, IconMark::Pictures},
        {"Music", "Music", "\xe2\x80\x94", IconKind::Folder, IconMark::Music},
        {"Videos", "Videos", "\xe2\x80\x94", IconKind::Folder, IconMark::Videos},
        {"Desktop", "Desktop", "\xe2\x80\x94", IconKind::Folder, IconMark::Desktop},
        {"Trash", "Trash", "\xe2\x80\x94", IconKind::Folder, IconMark::Trash},
        {"projects", "Folder", "\xe2\x80\x94", IconKind::Folder, IconMark::None},
        {"README.md", "Markdown", "4.0 KB", IconKind::Document},
        {"main.rs", "Rust source", "18.2 KB", IconKind::Code},
        {"photo.png", "PNG image", "2.4 MB", IconKind::Image},
        {"song.flac", "FLAC audio", "31.7 MB", IconKind::Audio},
        {"clip.mp4", "MP4 video", "104 MB", IconKind::Video},
        {"archive.zip", "ZIP archive", "9.1 MB", IconKind::Archive},
        {"libfoo.so", "Shared library", "812 KB", IconKind::Executable},
        {"notes", "File", "96 B", IconKind::Generic},
        {"latest", "Folder", "\xe2\x80\x94", IconKind::Folder, IconMark::None, IconBadge::Link},
        {"stale.conf", "CONF file", "broken link", IconKind::Generic, IconMark::None,
         IconBadge::Broken},
        {".config", "Folder", "\xe2\x80\x94", IconKind::Folder, IconMark::None, IconBadge::None,
         true},
    };
}

// Name, Kind, Size over a list of entries. Name flexes, so the picture shows the column rule
// doing its job as well as the icons it was built for.
struct Listing : Rows {
    std::vector<Entry> v;
    bool with_kind = true;
    explicit Listing(std::vector<Entry> rows) : v(std::move(rows)) {}
    int Count() const override { return (int)v.size(); }
    int Columns() const override { return with_kind ? 3 : 2; }
    CellValue Cell(int r, int c) const override {
        const Entry& e = v[(size_t)r];
        if (c == 0) return CellValue{CellState::Ready, e.name};
        if (c == 1) return CellValue{CellState::Ready, e.size};
        return CellValue{CellState::Ready, e.kind};
    }
    float ColumnWidth(int c) const override { return c == 0 ? 300 : 110; }
    std::string ColumnTitle(int c) const override {
        return c == 0 ? "Name" : (c == 1 ? "Size" : "Kind");
    }
    ColumnSpec Column(int c) const override {
        ColumnSpec s;
        s.title = ColumnTitle(c);
        if (c == 0) { s.width = 300; s.min = 160; s.flex = 1; }
        else if (c == 1) { s.width = 84; s.min = 72; s.align = Align::Right; }
        else { s.width = 120; s.min = 96; }
        return s;
    }
    IconSpec Icon(int r) const override {
        const Entry& e = v[(size_t)r];
        IconSpec sp;
        sp.kind = e.k;
        sp.mark = e.mark;
        sp.badge = e.badge;
        sp.dimmed = e.dim;
        return sp;
    }
};

// The four columns of docs/atlas-design.md 5.1, with their real defaults, floors and
// visible_from thresholds. The one source the width sheet needs.
struct Details : Rows {
    int n;
    explicit Details(int rows) : n(rows) {}
    int Count() const override { return n; }
    int Columns() const override { return 4; }
    CellValue Cell(int r, int c) const override {
        static const char* names[] = {"quarterly-report-final-v3.pdf", "aurora-compositor.rs",
                                      "holiday-photos", "budget.ods", "notes.md",
                                      "libaurora-ui.so"};
        static const char* sizes[] = {"1.2 MB", "84.0 KB", "\xe2\x80\x94", "42.1 KB", "3.3 KB",
                                      "812 KB"};
        static const char* mods[] = {"Today 12:56", "Yesterday 09:14", "3 Nov 14:02",
                                     "4 Mar 2025", "Today 08:02", "28 Oct 11:40"};
        static const char* kinds[] = {"PDF document", "Rust source", "Folder",
                                      "OpenDocument sheet", "Markdown", "Shared library"};
        const int i = r % 6;
        if (c == 0) return CellValue{CellState::Ready, names[i]};
        if (c == 1) return CellValue{CellState::Ready, sizes[i]};
        if (c == 2) return CellValue{CellState::Ready, mods[i]};
        return CellValue{CellState::Ready, kinds[i]};
    }
    float ColumnWidth(int c) const override { return c == 0 ? 300 : 120; }
    std::string ColumnTitle(int c) const override {
        const char* t[] = {"Name", "Size", "Modified", "Kind"};
        return t[c];
    }
    ColumnSpec Column(int c) const override {
        ColumnSpec s;
        s.title = ColumnTitle(c);
        switch (c) {
            case 0: s.width = 300; s.min = 160; s.flex = 1; break;
            case 1: s.width = 84; s.min = 72; s.align = Align::Right; break;
            case 2: s.width = 132; s.min = 96; s.align = Align::Right; s.visible_from = 600; break;
            default: s.width = 120; s.min = 96; s.visible_from = 900; break;
        }
        return s;
    }
    IconSpec Icon(int r) const override {
        static const IconKind k[] = {IconKind::Document, IconKind::Code, IconKind::Folder,
                                     IconKind::Document, IconKind::Document,
                                     IconKind::Executable};
        IconSpec s;
        s.kind = k[r % 6];
        return s;
    }
};

// ---- sheet 1: the icon family -----------------------------------------------------------------

static void sheet_icons(const std::string& dir, const std::string& suffix) {
    const float W = 896;
    Listing grid_src(family()), list_src(family());
    State<int> sel_a(-1), sel_b(-1);

    Desc grid = IconView(&grid_src, sel_a);
    au_surface* gs = render(grid, W - 32, 330, g_env.content);

    Desc list = ListView(&list_src, sel_b);
    ListIcons(list);
    const float list_h = 27 + 20 * 24 + 4;
    au_surface* ls = render(list, W - 32, list_h, g_env.content);

    const float H = 46 + 330 + 40 + list_h + 24;
    au_surface* s = au_surface_new((int)W, (int)ceilf(H), g_scale);
    au_clear(s, g_env.surface);

    float y = heading(s, 16, 12, W - 32, "Icons view, 48 px in a 112 x 104 tile");
    blit(s, 16, y, gs, 0, 0, W - 32, 330);
    y += 330 + 16;
    y = heading(s, 16, y, W - 32, "Details rows, 16 px at the head of the Name column");
    blit(s, 16, y, ls, 0, 0, W - 32, list_h);

    au_surface_free(gs);
    au_surface_free(ls);
    write_png(dir + "/specimen-icons" + suffix + ".png", au_surface_pixels(s),
              au_surface_px_w(s), au_surface_px_h(s));
    au_surface_free(s);
}

// ---- sheet 2: the row states ------------------------------------------------------------------
//
// Section 6.3's table, drawn. The rule it serves is that no two of these may look alike, and a
// table of words cannot be checked against that rule by eye.
static void sheet_states(const std::string& dir, const std::string& suffix) {
    struct Strip { const char* label; const char* note; };
    const Strip strips[] = {
        {"at rest", "zebra, no state"},
        {"hovered", "hover fill, instant"},
        {"selected, pane focused", "accent fill, accent-ink text"},
        {"selected, pane unfocused", "a different fill, ordinary text"},
        {"cursor only, focused", "halo, no fill"},
        {"cursor only, unfocused", "nothing is drawn"},
        {"hovered and selected", "the selection wins"},
        {"three selected, Shift+Down", "the cursor carries the halo"},
    };
    const int n_strips = (int)(sizeof(strips) / sizeof(strips[0]));
    // The label column is MEASURED, not guessed. A sheet whose point is that no value is ever
    // cut mid-word must not cut its own captions, and a hand-set width is exactly how that
    // happens the next time a caption changes or the base size does.
    float label_w = 0;
    for (const Strip& st : strips) {
        label_w = std::max(label_w, au_text_width(g_env.font, g_env.size, st.label, -1));
        label_w = std::max(label_w, au_text_width(g_env.font, g_env.size - 2, st.note, -1));
    }
    label_w += 20;
    const float strip_w = 430, pad = 16;
    const float W = pad + label_w + strip_w + pad;

    std::vector<Entry> rows;
    for (const Entry& e : family())
        if (e.name == "README.md" || e.name == "main.rs" || e.name == "photo.png" ||
            e.name == "archive.zip")
            rows.push_back(e);

    // One list per strip: these are nine different pieces of state, and sharing a node would
    // mean the last one drawn is the only one that is true.
    std::vector<Listing*> srcs;
    std::vector<Desc> lists;
    std::vector<State<int>> sels; sels.reserve((size_t)n_strips);
    for (size_t k = 0; k < (size_t)n_strips; k++) sels.emplace_back(-1);
    std::vector<au_surface*> shots;
    float row_h = 0, head_h = 0;
    for (int i = 0; i < n_strips; i++) {
        Listing* src = new Listing(rows);
        src->with_kind = false;
        srcs.push_back(src);
        Desc l = ListView(src, sels[(size_t)i]);
        ListIcons(l);
        lists.push_back(l);
        lay(l, Rect{0, 0, strip_w, 200});
        row_h = ListRowHeight(l);
        head_h = ListHeaderHeight(l);

        // The state, applied to row 1 -- the middle of the three shown -- so the halo's 3 px
        // overhang into its neighbours is visible rather than clipped by the crop.
        //
        // Headless, a list has no Window, and the halo follows Window::keyboard_driven -- so
        // every selection here draws the KEYBOARD-driven picture. The pointer-driven one is
        // this picture without the halo, and it is the one state of the table that this sheet
        // cannot show; the caption says so rather than drawing two strips that coincide.
        au_key down{};
        down.pressed = 1;
        down.raw_char = 0x1f;                        // Down
        const float mid = head_h + 1 + row_h * 1.5f;
        switch (i) {
            case 0: l->OnFocus(false); break;
            case 1: l->OnFocus(false); l->OnHoverAt(200, mid); break;
            case 2: l->OnFocus(true); l->OnPressAt(200, mid, 1); break;
            case 3: l->OnFocus(true); l->OnPressAt(200, mid, 1); l->OnFocus(false); break;
            case 4: l->OnFocus(true); ListSetCursor(l, 1); break;
            case 5: ListSetCursor(l, 1); l->OnFocus(false); break;
            case 6:
                l->OnFocus(true);
                l->OnPressAt(200, mid, 1);
                l->OnHoverAt(200, mid);
                break;
            case 7: {
                l->OnFocus(true);
                l->OnPressAt(200, head_h + 1 + row_h * 0.5f, 1);
                au_key ext = down;
                ext.mods = AU_MOD_SHIFT;
                l->OnKey(ext);
                l->OnKey(ext);
                break;
            }
            default: break;
        }
        au_surface* shot = au_surface_new((int)strip_w, (int)(head_h + 1 + row_h * 4 + 8), g_scale);
        au_clear(shot, g_env.content);
        l->Draw(shot, g_env);
        shots.push_back(shot);
    }

    const float strip_h = row_h * 3 + 2;
    const float W_h = 42 + n_strips * (strip_h + 12) + 10;
    au_surface* s = au_surface_new((int)W, (int)ceilf(W_h), g_scale);
    au_clear(s, g_env.surface);
    float y = heading(s, pad, 12, W - pad * 2,
                      "Six row states that must never coincide, and two compounds of them");
    for (int i = 0; i < n_strips; i++) {
        text(s, pad, y + strip_h * 0.5f - 2, strips[i].label, g_env.fg);
        text(s, pad, y + strip_h * 0.5f + 11, strips[i].note, g_env.dim, false, -2);
        blit(s, pad + label_w, y, shots[i], 0, head_h + 1, strip_w, strip_h);
        y += strip_h + 12;
    }
    for (au_surface* sh : shots) au_surface_free(sh);
    for (Listing* p : srcs) delete p;
    write_png(dir + "/specimen-row-states" + suffix + ".png", au_surface_pixels(s),
              au_surface_px_w(s), au_surface_px_h(s));
    au_surface_free(s);
}

// ---- sheet 3: the column rule -----------------------------------------------------------------
//
// The same source at three widths. A column is never clipped mid-value: it fits whole or it is
// not there, and the Name column absorbs every pixel in between.
static void sheet_columns(const std::string& dir, const std::string& suffix) {
    const float widths[] = {520, 760, 1000};
    const float pad = 16, W = 1000 + pad * 2;
    Details src(5);
    std::vector<State<int>> sels; sels.reserve(3);
    for (int k = 0; k < 3; k++) sels.emplace_back(-1);
    std::vector<au_surface*> shots;
    float total = 0, head = 0, row = 0;
    for (int i = 0; i < 3; i++) {
        Desc l = ListView(&src, sels[(size_t)i]);
        ListIcons(l);
        lay(l, Rect{0, 0, widths[i], 300});
        head = ListHeaderHeight(l);
        row = ListRowHeight(l);
        const float h = head + 1 + row * 5;
        au_surface* shot = au_surface_new((int)widths[i], (int)(h + 4), g_scale);
        au_clear(shot, g_env.content);
        l->Draw(shot, g_env);
        shots.push_back(shot);
        total += h;
    }
    const float H = 42 + total + 3 * 34 + 10;
    au_surface* s = au_surface_new((int)W, (int)ceilf(H), g_scale);
    au_clear(s, g_env.surface);
    float y = heading(s, pad, 12, W - pad * 2,
                      "One listing at three widths: a column fits whole or is dropped");
    const char* notes[] = {"520: Name and Size. Modified needs 600, Kind 900.",
                           "760: Modified is in, at its 132 default.",
                           "1000: Kind is in, and Name keeps the slack."};
    for (int i = 0; i < 3; i++) {
        text(s, pad, y + 10, notes[i], g_env.dim, false, -1);
        y += 16;
        blit(s, pad, y, shots[i], 0, 0, widths[i], head + 1 + row * 5);
        y += head + 1 + row * 5 + 18;
    }
    for (au_surface* sh : shots) au_surface_free(sh);
    write_png(dir + "/specimen-columns" + suffix + ".png", au_surface_pixels(s),
              au_surface_px_w(s), au_surface_px_h(s));
    au_surface_free(s);
}

// ---- sheet 4: the controls --------------------------------------------------------------------

static void sheet_controls(const std::string& dir, const std::string& suffix) {
    const float pad = 16, W = 640;
    static State<bool> on_yes(true), on_no(false), sw(true), enabled_off(false);
    static State<float> slider(0.42f), prog(0.62f);
    static State<int> radio(1);
    static State<std::string> field("quarterly-report.pdf"), empty("");

    auto nop = [] {};
    Desc buttons = HStack({Button("Open", nop), Button("Rename", nop), Button("Move\xe2\x80\xa6", nop),
                           Button("Delete", nop, enabled_off), Spacer()}, 0.5f);
    Desc glyphs = HStack({IconButton(Glyph::Back, nop), IconButton(Glyph::Forward, nop),
                          IconButton(Glyph::Up, nop), IconButton(Glyph::Sidebar, nop),
                          IconButton(Glyph::Preview, nop), IconButton(Glyph::View, nop),
                          IconButton(Glyph::Filter, nop), IconButton(Glyph::Refresh, nop),
                          Spacer()}, 0.5f);
    Desc toggles = HStack({Checkbox("Show hidden files", on_yes),
                           Checkbox("Group folders first", on_no), Spacer()}, 1.0f);
    Desc switches = HStack({Switch("Preview pane", sw), Spacer()}, 1.0f);
    Desc radios = RadioGroup({"Name", "Size", "Modified"}, radio, nullptr, true);
    Desc slid = sized(Slider(slider, 0, 1), 220);
    Desc bar = sized(Progress(prog), 220);
    FieldOpts fo;
    fo.placeholder = "Filter\xe2\x80\xa6";
    Desc f1 = sized(TextField(field, nullptr, nullptr, nullptr, fo), 260);
    Desc f2 = sized(TextField(empty, nullptr, nullptr, nullptr, fo), 160);

    struct Row { const char* label; Desc node; float h; };
    std::vector<Row> rows = {
        {"Buttons: at rest, hovered, pressed, disabled", buttons, 30},
        {"Glyph buttons, 28 x 28", glyphs, 32},
        {"Checkbox: on, off", toggles, 26},
        {"Switch", switches, 26},
        {"Radio group, exclusive", radios, 26},
        {"Slider over [0, 1]", slid, 26},
        {"Progress, 0.62", bar, 20},
        {"Text field: with a value, and a placeholder", HStack({f1, f2, Spacer()}, 1.0f), 30},
    };

    float H = 42;
    for (const Row& r : rows) H += r.h + 30;
    au_surface* s = au_surface_new((int)W, (int)ceilf(H + 10), g_scale);
    au_clear(s, g_env.surface);
    float y = heading(s, pad, 12, W - pad * 2, "aurora::ui controls, drawn by the toolkit");
    for (const Row& r : rows) {
        text(s, pad, y + 9, r.label, g_env.dim, false, -1);
        y += 16;
        lay(r.node, Rect{pad, y, W - pad * 2, r.h});
        // Hover and press are node state, not a colour a sheet can fake: the second button is
        // entered and the third pressed, through the same calls the window makes.
        if (r.node == buttons) {
            if (Node* hov = r.node->HitTest(pad + 70, y + r.h * 0.5f)) hov->OnEnter();
            if (Node* prs = r.node->HitTest(pad + 150, y + r.h * 0.5f)) { prs->OnEnter(); prs->OnPress(); }
        }
        r.node->Draw(s, g_env);
        y += r.h + 14;
    }
    write_png(dir + "/specimen-controls" + suffix + ".png", au_surface_pixels(s),
              au_surface_px_w(s), au_surface_px_h(s));
    au_surface_free(s);
}

int main(int argc, char** argv) {
    const std::string dir = argc > 1 ? argv[1] : "docs/images";
    float sys = 0;
    if (!au_font_system("plain", &g_env.font, &sys)) {
        fprintf(stderr, "ui_specimen: no system font; nothing drawn\n");
        return 1;
    }
    if (!au_font_system("bold", &g_env.bold, &sys)) g_env.bold = g_env.font;
    g_env.size = Env::kBaseSize;

    const struct { double scale; const char* suffix; } runs[] = {{1.0, ""}, {1.45, "-1.45x"}};
    for (const auto& r : runs) {
        g_scale = r.scale;
        sheet_icons(dir, r.suffix);
        sheet_states(dir, r.suffix);
        sheet_columns(dir, r.suffix);
        sheet_controls(dir, r.suffix);
        printf("ui_specimen: wrote %s/specimen-{icons,row-states,columns,controls}%s.png\n",
               dir.c_str(), r.suffix);
    }
    return 0;
}
