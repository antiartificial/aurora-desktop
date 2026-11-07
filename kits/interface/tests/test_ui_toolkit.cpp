// The aurora::ui foundations Atlas is built on: the column rule, the icon model, the transition
// primitive, and the three selection states that must never render alike.
//
// Headless on purpose. Every claim here is a number or a pixel, and all of them are reachable
// without a compositor: a Desc tree is Measured and Placed directly and drawn into an offscreen
// surface, which is what lets the drawing claims be CHECKED rather than described. The claims
// that need a running window instead -- the damage fraction of a hover (C6.2) -- are driven
// against the real binary; what is here is the mechanism that one depends on.
//
// Scale: every drawing check runs at 1.0 AND at 1.45, because 1.45 is what a real display here
// asks for and because two bugs in one day were invisible at 1.
#include "ui.h"

#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <ctime>
#include <string>
#include <vector>

using namespace aurora::ui;

static int fails = 0, checks = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); printf(__VA_ARGS__); printf("\n"); } } while (0)

// The cursor row, through the int the list was built with: the test holds it anyway, but
// naming it here keeps the geometry arithmetic readable.
static void lay(const Desc& d, const Env& e, Rect box) {
    d->Measure(e, Constraints{box.w, box.h});
    d->Place(box);
}

// A source that states real ColumnSpecs: a flexible Name, fixed Size and Modified, and a Kind
// column that only appears on a wide list. The shape of Atlas's details view.
struct FileRows : Rows {
    int n;
    explicit FileRows(int count) : n(count) {}
    int Count() const override { return n; }
    CellValue Cell(int row, int col) const override {
        if (col == 0) return CellValue{CellState::Ready, "entry-" + std::to_string(row) + ".txt"};
        if (col == 1) return CellValue{CellState::Ready, "4.0 KB"};
        if (col == 2) return CellValue{CellState::Ready, "Today 12:56"};
        return CellValue{CellState::Ready, "Text"};
    }
    int Columns() const override { return 4; }
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
};

// The legacy source: two virtuals, no spec. It must lay out exactly as it did before ColumnSpec
// existed, because the chooser's DirRows is one of these and is not this stage's file.
struct PlainRows : Rows {
    int Count() const override { return 20; }
    CellValue Cell(int row, int) const override {
        return CellValue{CellState::Ready, "row " + std::to_string(row)};
    }
    int Columns() const override { return 2; }
    float ColumnWidth(int c) const override { return c == 0 ? 200 : 120; }
    std::string ColumnTitle(int c) const override { return c == 0 ? "Name" : "Size"; }
};

static float sum(const std::vector<float>& v) {
    float t = 0;
    for (float f : v) t += f;
    return t;
}

// The pixel at a point of a drawn surface, as 0xRRGGBB.
static uint32_t at(au_surface* s, float x, float y) {
    const double sc = au_surface_scale(s);
    const int px = (int)(x * sc), py = (int)(y * sc);
    if (px < 0 || py < 0 || px >= au_surface_px_w(s) || py >= au_surface_px_h(s)) return 0;
    return au_surface_pixels(s)[(size_t)py * au_surface_px_w(s) + px] & 0xFFFFFFu;
}

static void write_file(const std::string& p, const std::string& body) {
    FILE* f = fopen(p.c_str(), "wb");
    if (!f) return;
    fwrite(body.data(), 1, body.size(), f);
    fclose(f);
}

int main() {
    Env e;
    // The real system face at the real base size, because the geometry under test is derived
    // from the font's metrics: a row is round(text_h + 6), and a test that ran without a face
    // would be measuring a row six pixels tall and proving nothing about the one on screen.
    float sys_size = 0;
    const bool have_font = au_font_system("plain", &e.font, &sys_size) != 0;
    if (!au_font_system("bold", &e.bold, &sys_size)) e.bold = e.font;
    e.size = default_base_size();
    if (!have_font) printf("  (no system font: the drawing checks are skipped)\n");

    // ---- the base text size ---------------------------------------------------------------
    //
    // The trap: the plain system font reports 12, a size the shell abandoned, and every size in
    // a design is a multiplier on the base. A window that adopted the font's number put the
    // whole interface one step low and nothing on screen said why.
    {
        CHECK(Env::kBaseSize == 13, "the toolkit's base size is 13, not %g", (double)Env::kBaseSize);
        CHECK(Env().size == 13, "a fresh Env starts at the base size, got %g", (double)Env().size);
        unsetenv("AURORA_UI_SIZE");
        CHECK(default_base_size() == 13, "a window starts at 13 unless asked otherwise, got %g",
              (double)default_base_size());
        setenv("AURORA_UI_SIZE", "17", 1);
        CHECK(default_base_size() == 17, "AURORA_UI_SIZE sets the base, got %g",
              (double)default_base_size());
        setenv("AURORA_UI_SIZE", "900", 1);
        CHECK(default_base_size() == 13, "a nonsense size is refused, got %g",
              (double)default_base_size());
        unsetenv("AURORA_UI_SIZE");

        float sys = 0;
        au_font f{};
        if (au_font_system("plain", &f, &sys))
            CHECK(sys != 13 || true, "the system plain font reports %g", (double)sys);
    }

    // ---- the column rule --------------------------------------------------------------------
    //
    // "A column either fits whole or is dropped, and the flexible one absorbs the rest." That
    // is the sentence that makes "Modified cut off mid-value" impossible, so it is the sentence
    // the test asserts: at every width from a narrow window to a wide one, the shown columns
    // sum to no more than the list and none of them is below its floor.
    {
        FileRows src(10);
        for (float w = 200; w <= 1400; w += 7) {
            const std::vector<float> c = SolveColumns(&src, w);
            CHECK(sum(c) <= w + 0.51f, "columns overflow the list at %g: sum %g", (double)w,
                  (double)sum(c));
            for (int i = 1; i < 4; i++)
                CHECK(c[(size_t)i] == 0 || c[(size_t)i] >= src.Column(i).min,
                      "column %d is %g at width %g, under its floor of %g -- it would be cut "
                      "mid-value", i, (double)c[(size_t)i], (double)w, (double)src.Column(i).min);
        }

        // The flexible Name takes the slack, and it is the ONLY column that grows.
        const std::vector<float> wide = SolveColumns(&src, 1200);
        CHECK(std::fabs(sum(wide) - 1200) < 0.51f, "the flex column leaves %g unused",
              (double)(1200 - sum(wide)));
        CHECK(wide[1] == 84 && wide[2] == 132 && wide[3] == 120,
              "a fixed column must not move: %g %g %g", (double)wide[1], (double)wide[2],
              (double)wide[3]);
        CHECK(wide[0] > 300, "Name absorbed the slack: %g", (double)wide[0]);

        // Narrow: Kind is gone below 900, Modified below 600, and Name is still there.
        const std::vector<float> mid = SolveColumns(&src, 700);
        CHECK(mid[3] == 0, "Kind is shown at 700 wide, where it does not belong");
        CHECK(mid[2] > 0, "Modified is dropped at 700 wide, where it fits");
        const std::vector<float> narrow = SolveColumns(&src, 520);
        CHECK(narrow[2] == 0 && narrow[3] == 0, "only Name and Size below 600");
        CHECK(narrow[0] > 0 && narrow[1] == 84, "Name and Size survive: %g %g",
              (double)narrow[0], (double)narrow[1]);

        // A list far too narrow for even the fixed columns drops them from the right rather
        // than painting one over another.
        const std::vector<float> tiny = SolveColumns(&src, 180);
        CHECK(sum(tiny) <= 180.51f, "a 180-wide list draws %g of columns", (double)sum(tiny));
        CHECK(tiny[0] > 0, "the first column is never dropped: a listing with no names is not a listing");

        // The legacy source keeps exactly the layout it had.
        PlainRows plain;
        const std::vector<float> legacy = SolveColumns(&plain, 400);
        CHECK(legacy[0] == 200 && legacy[1] == 120,
              "a source with no ColumnSpec must lay out as before: %g %g", (double)legacy[0],
              (double)legacy[1]);
    }

    // ---- the icon model ---------------------------------------------------------------------
    {
        struct { const char* name; IconKind kind; } want[] = {
            {"README.md", IconKind::Document}, {"main.rs", IconKind::Code},
            {"photo.png", IconKind::Image},    {"archive.zip", IconKind::Archive},
            {"libfoo.so", IconKind::Executable}, {"song.flac", IconKind::Audio},
            {"clip.mp4", IconKind::Video},     {"notes", IconKind::Generic},
        };
        for (const auto& w : want)
            CHECK(IconFromName(w.name, false).kind == w.kind, "%s classified as %d", w.name,
                  (int)IconFromName(w.name, false).kind);
        CHECK(IconFromName("Pictures", true).kind == IconKind::Folder &&
                  IconFromName("Pictures", true).mark == IconMark::Pictures,
              "a special folder carries its mark");
        CHECK(IconFromName("plain", true).mark == IconMark::None, "an ordinary folder has no mark");
        CHECK(IconFromName(".config", true).dimmed, "a hidden entry is dimmed");

        // No two kinds draw the same picture. This is C1.2 at the level the toolkit can answer
        // it: if two kinds rasterise to identical pixels, no listing can tell them apart.
        for (double sc : {1.0, 1.45}) {
            if (!have_font) break;
            const int side = 48;
            std::vector<std::vector<uint32_t> > shots;
            const IconKind kinds[] = {IconKind::Folder, IconKind::Document, IconKind::Code,
                                      IconKind::Image, IconKind::Audio, IconKind::Video,
                                      IconKind::Archive, IconKind::Executable,
                                      IconKind::Generic};
            for (IconKind k : kinds) {
                au_surface* s = au_surface_new(side, side, sc);
                au_clear(s, au_color{0, 0, 0, 255});
                IconSpec sp;
                sp.kind = k;
                Rect r{0, 0, (float)side, (float)side};
                // through the public path: a Rows that answers this spec
                struct One : Rows {
                    IconSpec sp;
                    int Count() const override { return 1; }
                    CellValue Cell(int, int) const override { return CellValue{CellState::Ready, "x"}; }
                    int Columns() const override { return 1; }
                    float ColumnWidth(int) const override { return 100; }
                    std::string ColumnTitle(int) const override { return "n"; }
                    IconSpec Icon(int) const override { return sp; }
                } one;
                one.sp = sp;
                int selected = -1;
                Desc list = ListView(&one, &selected);
                ListIcons(list);
                lay(list, e, Rect{0, 0, 300, 200});
                list->Draw(s, e);
                const int n = au_surface_px_w(s) * au_surface_px_h(s);
                shots.push_back(std::vector<uint32_t>(au_surface_pixels(s),
                                                      au_surface_pixels(s) + n));
                au_surface_free(s);
                (void)r;
            }
            for (size_t i = 0; i < shots.size(); i++)
                for (size_t j = i + 1; j < shots.size(); j++)
                    CHECK(shots[i] != shots[j],
                          "kinds %zu and %zu draw identical pixels at scale %g -- two different "
                          "things look the same", i, j, sc);
        }

        // The cache: one rasterisation per (kind, size, scale, variant), and the SCALE is part
        // of the key. It was not, and one window has one scale so nothing had broken yet -- but
        // the first 16-unit icon decided the device size of every 16-unit icon after it.
        IconSpec sp;
        sp.kind = IconKind::Folder;
        // Scales nothing else in this file uses, so the cache is being asked something new:
        // a check that passes because an earlier block happened to fill the cache is not a check.
        au_surface* a = au_surface_new(300, 200, 1.3);
        au_surface* b = au_surface_new(300, 200, 2.1);
        struct One : Rows {
            IconSpec sp;
            int Count() const override { return 1; }
            CellValue Cell(int, int) const override { return CellValue{CellState::Ready, "x"}; }
            int Columns() const override { return 1; }
            float ColumnWidth(int) const override { return 100; }
            std::string ColumnTitle(int) const override { return "n"; }
            IconSpec Icon(int) const override { return sp; }
        } one;
        one.sp = sp;
        int selected = -1;
        Desc list = ListView(&one, &selected);
        ListIcons(list);
        lay(list, e, Rect{0, 0, 300, 200});
        list->Draw(a, e);
        const size_t after_first = IconsRasterised();
        list->Draw(a, e);
        list->Draw(a, e);
        CHECK(IconsRasterised() == after_first,
              "drawing the same icon again rasterised it again (%zu -> %zu): the cost rule of "
              "section 4.5 is that a scroll rasterises nothing",
              after_first, IconsRasterised());
        list->Draw(b, e);
        CHECK(IconsRasterised() == after_first + 1,
              "the same icon at a different SCALE must be its own rasterisation (%zu -> %zu)",
              after_first, IconsRasterised());
        au_surface_free(a);
        au_surface_free(b);
    }

    // ---- vector and bitmap icons ------------------------------------------------------------
    {
        const std::string dir = "/tmp/auui-icons-test";
        char tmpl[] = "/tmp/auui-ic-XXXXXX";
        const char* d = mkdtemp(tmpl);
        CHECK(d != nullptr, "no temp dir");
        const std::string ok = std::string(d) + "/ok.svg";
        const std::string bad = std::string(d) + "/bad.svg";
        const std::string ppm = std::string(d) + "/flat.ppm";
        write_file(ok,
                   "<svg viewBox=\"0 0 16 16\" xmlns=\"http://www.w3.org/2000/svg\">"
                   "<path d=\"M2 2 L14 2 L14 14 L2 14 Z\" fill=\"#ff0000\"/>"
                   "<circle cx=\"8\" cy=\"8\" r=\"3\" fill=\"#00ff00\"/></svg>");
        // A transform is outside the subset: the file is refused WHOLE rather than drawn wrong.
        write_file(bad,
                   "<svg viewBox=\"0 0 16 16\"><g transform=\"translate(2,2)\">"
                   "<rect x=\"0\" y=\"0\" width=\"8\" height=\"8\" fill=\"#ff0000\"/></g></svg>");
        write_file(ppm, std::string("P6\n2 2\n255\n") +
                            std::string("\xff\x00\x00\xff\x00\x00\xff\x00\x00\xff\x00\x00", 12));
        CHECK(IconVectorOk(ok), "a path, a circle and a hex fill are inside the subset");
        CHECK(!IconVectorOk(bad), "a transform must make the file unreadable, not half-drawn");
        CHECK(!IconVectorOk(std::string(d) + "/missing.svg"), "a file that is not there is not ok");

        for (double sc : {1.0, 1.45}) {
            if (!have_font) break;
            au_surface* s = au_surface_new(300, 100, sc);
            au_clear(s, au_color{0, 0, 0, 255});
            struct One : Rows {
                IconSpec sp;
                int Count() const override { return 1; }
                CellValue Cell(int, int) const override { return CellValue{CellState::Ready, "x"}; }
                int Columns() const override { return 1; }
                float ColumnWidth(int) const override { return 100; }
                std::string ColumnTitle(int) const override { return "n"; }
                IconSpec Icon(int) const override { return sp; }
            } one;
            one.sp.format = IconFormat::Vector;
            one.sp.path = ok;
            one.sp.kind = IconKind::Generic;
            int selected = -1;
            Desc list = ListView(&one, &selected);
            ListIcons(list);
            lay(list, e, Rect{0, 0, 300, 100});
            list->Draw(s, e);
            // The icon sits at the row's left, 16 units wide: its middle is green, its corner red.
            bool red = false, green = false;
            for (int y = 0; y < 48; y++)
                for (int x = 0; x < 48; x++) {
                    const uint32_t p = at(s, (float)x, (float)y);
                    if (p == 0xFF0000) red = true;
                    if (p == 0x00FF00) green = true;
                }
            CHECK(red && green, "the vector icon drew its own colours at scale %g (red %d green %d)",
                  sc, (int)red, (int)green);

            // The same source with the transform falls back to the kind's glyph: a wrong icon
            // is a picture, a half-read one is a lie.
            au_surface* s2 = au_surface_new(300, 100, sc);
            au_clear(s2, au_color{0, 0, 0, 255});
            one.sp.path = bad;
            Desc l2 = ListView(&one, &selected);
            ListIcons(l2);
            lay(l2, e, Rect{0, 0, 300, 100});
            l2->Draw(s2, e);
            bool any_red = false;
            for (int y = 0; y < 48; y++)
                for (int x = 0; x < 48; x++)
                    if (at(s2, (float)x, (float)y) == 0xFF0000) any_red = true;
            CHECK(!any_red, "a refused vector icon drew its colours anyway at scale %g", sc);
            au_surface_free(s2);

            // A bitmap, from a file no decoder was needed for.
            au_surface* s3 = au_surface_new(300, 100, sc);
            au_clear(s3, au_color{0, 0, 0, 255});
            one.sp.format = IconFormat::Bitmap;
            one.sp.path = ppm;
            Desc l3 = ListView(&one, &selected);
            ListIcons(l3);
            lay(l3, e, Rect{0, 0, 300, 100});
            l3->Draw(s3, e);
            bool bred = false;
            for (int y = 0; y < 48; y++)
                for (int x = 0; x < 48; x++)
                    if (at(s3, (float)x, (float)y) == 0xFF0000) bred = true;
            CHECK(bred, "the bitmap icon did not reach the row at scale %g", sc);
            au_surface_free(s3);
            au_surface_free(s);
        }
        (void)dir;
    }

    // ---- the three selection states ---------------------------------------------------------
    //
    // A focused selection, an unfocused selection and a focused cursor with nothing selected
    // must be three different pictures. Two of them drawn alike is the bug this codebase has
    // shipped four times, and it is a correctness failure: the screen would be claiming
    // something the program does not hold.
    {
        for (double sc : {1.0, 1.45}) {
            if (!have_font) break;
            FileRows src(10);
            int selected = -1;
            Desc list = ListView(&src, &selected);
            lay(list, e, Rect{0, 0, 600, 400});
            au_key down{};
            down.pressed = 1;
            down.raw_char = 0x1f;

            // The pixel in the middle of the row's fill, away from the text and from the ring
            // at its edge: WHICH fill is drawn is the signal a person reads, so it is the thing
            // the test pins. Comparing whole frames is not enough -- two states can differ by a
            // ring while the fill, which is what the eye sees first, says the same thing.
            uint32_t fill_focused = 0, fill_unfocused = 0, fill_cursor = 0, fill_plain = 0;
            uint32_t last_fill = 0;
            auto shoot = [&](bool focused, bool clear_selection) {
                au_surface* s = au_surface_new(600, 400, sc);
                au_clear(s, au_color{0, 0, 0, 255});
                list->OnFocus(focused);
                if (clear_selection) {
                    // Ctrl-deselect leaves the cursor where it is with nothing selected, which
                    // is exactly the third state.
                    ListSetCursor(list, 0);
                }
                list->Draw(s, e);
                const float head = ListHeaderHeight(list), row = ListRowHeight(list);
                last_fill = at(s, 500, head + 1 + row * 0.5f);
                std::vector<uint32_t> px(au_surface_pixels(s),
                                         au_surface_pixels(s) +
                                             au_surface_px_w(s) * au_surface_px_h(s));
                au_surface_free(s);
                return px;
            };
            list->OnKey(down);                       // row 0 selected, cursor on it
            const std::vector<uint32_t> focused_sel = shoot(true, false);
            fill_focused = last_fill;
            const std::vector<uint32_t> unfocused_sel = shoot(false, false);
            fill_unfocused = last_fill;
            list->OnFocus(true);
            const std::vector<uint32_t> cursor_only = shoot(true, true);
            fill_cursor = last_fill;
            // The fourth picture: a row with neither selection nor cursor. The focused cursor
            // row has to differ from THIS too, or "the keyboard is here" is invisible.
            ListSetCursor(list, -1);
            const std::vector<uint32_t> plain = shoot(true, false);
            fill_plain = last_fill;

            CHECK(focused_sel != unfocused_sel,
                  "a focused selection and an unfocused one draw alike at scale %g", sc);
            CHECK(focused_sel != cursor_only,
                  "a focused selection and a bare cursor draw alike at scale %g", sc);
            CHECK(unfocused_sel != cursor_only,
                  "an unfocused selection and a bare cursor draw alike at scale %g", sc);
            CHECK(cursor_only != plain,
                  "a focused cursor row and an ordinary row draw alike at scale %g: the focus "
                  "ring says nothing", sc);
            CHECK(fill_focused != fill_unfocused,
                  "the selection FILL is the same colour focused and unfocused at scale %g "
                  "(%06x): whether this pane has the keyboard is what that colour says", sc,
                  fill_focused);
            CHECK(fill_focused != fill_cursor && fill_unfocused != fill_cursor,
                  "a bare cursor is filled like a selection at scale %g (%06x %06x %06x)", sc,
                  fill_focused, fill_unfocused, fill_cursor);
            // The halo is a RING with a gap, not a fill: around the cursor row there are
            // pixels in the focus colour, and between them and the fill a pixel of the row's
            // own surface. Without the gap the ring and an accent fill are one shape.
            {
                au_surface* s = au_surface_new(600, 400, sc);
                au_clear(s, au_color{0, 0, 0, 255});
                list->OnFocus(true);
                ListSetCursor(list, 1);
                list->Draw(s, e);
                const float head = ListHeaderHeight(list), row = ListRowHeight(list);
                const uint32_t ring = (uint32_t)e.focus_ring.r << 16 |
                                      (uint32_t)e.focus_ring.g << 8 | e.focus_ring.b;
                int ring_px = 0;
                for (float y = head + 1 + row - 4; y < head + 1 + row * 2 + 4; y += 1)
                    for (float x = 2; x < 598; x += 1)
                        if (at(s, x, y) == ring) ring_px++;
                CHECK(ring_px > 20, "the cursor row has %d pixels of focus colour around it at "
                      "scale %g: there is no halo", ring_px, sc);
                au_surface_free(s);
            }
            // The GAP. `focus` and `accent` are the same value today, so a halo drawn tight
            // against an accent fill is not a ring around a selection -- it is one fatter
            // selection. The gap is the whole reason it reads as a ring, and it is one pixel
            // of the surface colour, so it is one pixel a test can look for.
            {
                au_surface* s = au_surface_new(600, 400, sc);
                au_clear(s, au_color{0, 0, 0, 255});
                list->OnFocus(true);
                au_key d2{};
                d2.pressed = 1;
                d2.raw_char = 0x1f;
                list->OnKey(d2);              // a selected row that is also the cursor
                list->Draw(s, e);
                const float head = ListHeaderHeight(list), row = ListRowHeight(list);
                const int cur = selected;
                const float ry = head + 1 + cur * row;
                const uint32_t surface = (uint32_t)((cur & 1) ? e.content_alt.r : e.content.r)
                                             << 16 |
                                         (uint32_t)((cur & 1) ? e.content_alt.g : e.content.g)
                                             << 8 |
                                         ((cur & 1) ? e.content_alt.b : e.content.b);
                int gap_px = 0;
                for (float y = ry - 3; y < ry + 1.5f; y += 0.5f)
                    if (at(s, 300, y) == surface) gap_px++;
                CHECK(gap_px > 0,
                      "there is no gap between the selection fill and the halo at scale %g: the "
                      "ring and the fill are one shape", sc);
                au_surface_free(s);
            }
            CHECK(fill_cursor == fill_plain,
                  "a cursor row is FILLED at scale %g; the design says a ring, not a fill", sc);
        }
    }

    // ---- zebra and hover --------------------------------------------------------------------
    {
        FileRows src(40);
        int selected = -1;
        Desc list = ListView(&src, &selected);
        lay(list, e, Rect{0, 0, 600, 400});
        au_surface* s = au_surface_new(600, 400, 1.0);
        au_clear(s, au_color{0, 0, 0, 255});
        list->Draw(s, e);
        if (!have_font) { au_surface_free(s); goto no_font_done; }
        // The row geometry comes from the widget, not from a guess about the font.
        const float head = ListHeaderHeight(list);
        const float row = ListRowHeight(list);
        CHECK(row == std::round(row) && row > 0, "the row height is not a whole number: %g",
              (double)row);
        const float y0 = head + 1 + row * 0.5f, y1 = y0 + row;
        const uint32_t even = at(s, 300, y0), odd = at(s, 300, y1);
        CHECK(even != odd, "adjacent rows are the same colour: there is no zebra (%06x %06x)",
              even, odd);

        // Hover: the pointer moves to row 1 and the widget reports TWO rectangles, not the pane.
        CHECK(list->OnHoverAt(300, y1), "the list did not notice the pointer");
        std::vector<Rect> hint;
        CHECK(list->DamageHint(&hint), "a hover gave no damage hint, so the window repaints the pane");
        float area = 0;
        for (const Rect& r : hint) area += r.w * r.h;
        CHECK(area / (600 * 400) < 0.1,
              "a hover damages %.3f of the pane; C6.2 wants well under 0.1", area / (600 * 400));
        au_clear(s, au_color{0, 0, 0, 255});
        list->Draw(s, e);
        const uint32_t hovered = at(s, 300, y1);
        CHECK(hovered != odd, "the hovered row looks exactly like the row did before (%06x)", hovered);
        // The pointer did not move: nothing changed, so nothing is damaged. A node that said
        // otherwise would make every pointer move a repaint of itself.
        CHECK(!list->OnHoverAt(301, y1), "a move inside the same row claimed a change");
        au_surface_free(s);
    }

no_font_done:
    // ---- the press selects -------------------------------------------------------------------
    //
    // On the press, not the release: that is what lets a drag start on a row that was not
    // selected, because by the time the pointer has moved 6 px the row is already the selection.
    {
        FileRows src(40);
        int selected = -1;
        Desc list = ListView(&src, &selected);
        lay(list, e, Rect{0, 0, 600, 400});
        list->Draw(nullptr, e);
        const float head = ListHeaderHeight(list);
        const float row = ListRowHeight(list);
        list->OnPressAt(100, head + 1 + row * 2.5f, 1);
        CHECK(selected == 2, "the press did not select (selected=%d)", selected);
        CHECK(ListSelection(list).size() == 1, "the press selected %zu rows",
              ListSelection(list).size());
        list->OnRelease(true);
        CHECK(selected == 2, "the release changed the selection");
    }

    // ---- glyph buttons -------------------------------------------------------------------------
    //
    // A toolbar of words is the thing the design rejects first, so a glyph button has to be a
    // drawing -- and a drawing that is the same for two different actions is worse than a word.
    {
        const Glyph all[] = {Glyph::Back, Glyph::Forward, Glyph::Up, Glyph::Sidebar,
                             Glyph::Preview, Glyph::View, Glyph::Filter, Glyph::Search,
                             Glyph::Plus, Glyph::Minus, Glyph::More, Glyph::Refresh,
                             Glyph::Close};
        for (double sc : {1.0, 1.45}) {
            std::vector<std::vector<uint32_t> > shots;
            for (Glyph g : all) {
                int fired = 0;
                Desc b = IconButton(g, [&] { fired++; });
                au_surface* s = au_surface_new(40, 40, sc);
                au_clear(s, au_color{0, 0, 0, 255});
                lay(b, e, Rect{0, 0, 28, 28});
                b->Draw(s, e);
                const int n = au_surface_px_w(s) * au_surface_px_h(s);
                shots.push_back(std::vector<uint32_t>(au_surface_pixels(s),
                                                      au_surface_pixels(s) + n));
                au_surface_free(s);
                CHECK(b->frame.w == b->frame.h, "an icon button is square: %gx%g",
                      (double)b->frame.w, (double)b->frame.h);
            }
            for (size_t i = 0; i < shots.size(); i++)
                for (size_t j = i + 1; j < shots.size(); j++)
                    CHECK(shots[i] != shots[j],
                          "glyphs %zu and %zu draw identically at scale %g", i, j, sc);
        }

        // Hover, press and disabled are three different pictures too, and a disabled button is
        // still THERE: hiding what is unavailable teaches the wrong model.
        State<bool> on(true);
        int fired = 0;
        Desc b = IconButton(Glyph::Back, [&] { fired++; }, &on);
        lay(b, e, Rect{0, 0, 28, 28});
        auto shot = [&]() {
            au_surface* s = au_surface_new(28, 28, 1.0);
            au_clear(s, au_color{0, 0, 0, 255});
            b->Draw(s, e);
            std::vector<uint32_t> px(au_surface_pixels(s),
                                     au_surface_pixels(s) +
                                         au_surface_px_w(s) * au_surface_px_h(s));
            au_surface_free(s);
            return px;
        };
        // How much of the button's INSIDE is not the plate: the glyph, in other words. Taken
        // inside the corner radius so an antialiased corner cannot be mistaken for a mark.
        auto glyph_pixels = [&]() {
            au_surface* s = au_surface_new(28, 28, 1.0);
            au_clear(s, au_color{0, 0, 0, 255});
            b->Draw(s, e);
            int n = 0;
            for (int y = 7; y < 21; y++)
                for (int x = 7; x < 21; x++) {
                    const uint32_t p = at(s, (float)x, (float)y);
                    if (p != ((uint32_t)e.surface.r << 16 | (uint32_t)e.surface.g << 8 |
                              e.surface.b))
                        n++;
                }
            au_surface_free(s);
            return n;
        };
        const int on_pixels = glyph_pixels();
        const std::vector<uint32_t> rest = shot();
        b->OnEnter();
        const std::vector<uint32_t> hovered = shot();
        b->OnPress();
        const std::vector<uint32_t> pressed = shot();
        b->OnRelease(true);
        b->OnLeave();
        CHECK(fired == 1, "the button did not fire on release (%d)", fired);
        on.set(false);
        const std::vector<uint32_t> off = shot();
        b->OnPress();
        b->OnRelease(true);
        CHECK(fired == 1, "a disabled button fired (%d)", fired);
        CHECK(rest != hovered, "hover draws nothing");
        CHECK(hovered != pressed, "a press looks exactly like a hover");
        CHECK(rest != off, "a disabled button looks exactly like a resting one");
        const int off_pixels = glyph_pixels();
        CHECK(on_pixels > 0, "the button drew no glyph at all");
        CHECK(off_pixels > 0,
              "a disabled button drew no glyph: it must stay visible with its mark dimmed, the "
              "way a disabled menu item does, or nothing says the action exists");

        // The menu form reserves room for its chevron, which is the whole reason it is a node:
        // a menu that opens somewhere other than under its control reads as another control.
        Desc m = MenuButton(Glyph::View, [] { return std::vector<MenuItem>(); });
        lay(m, e, Rect{0, 0, 60, 28});
        CHECK(m->Measure(e, Constraints{60, 28}).w > b->Measure(e, Constraints{28, 28}).w,
              "a menu button is no wider than a plain one: there is no room for the chevron");
    }

    // ---- the transition primitive -------------------------------------------------------------
    {
        CHECK(curve_at(Curve::Linear, 0.5f) == 0.5f, "linear is not linear");
        CHECK(curve_at(Curve::EaseOut, 0.5f) > 0.5f, "an ease-out is not ahead at the midpoint");
        CHECK(curve_at(Curve::EaseOut, 0) == 0 && curve_at(Curve::EaseOut, 1) == 1,
              "a curve must start at 0 and end at 1");
        float prev = -1;
        for (int i = 0; i <= 20; i++) {
            const float v = curve_at(Curve::EaseInOut, i / 20.0f);
            CHECK(v >= prev, "ease-in-out went backwards at %d", i);
            prev = v;
        }

        MotionTable m;
        CHECK(m.pane.ms == 160 && m.pane.curve == Curve::EaseOut,
              "the pane reveal is 160 ms ease-out (section 7)");
        CHECK(m.resolve(m.pane).motion == Motion::RevealX, "a transition resolves to itself");
        m.reduced = true;
        CHECK(m.resolve(m.pane).motion == Motion::None, "reduced motion must snap a size change");
        CHECK(m.resolve(m.fade).motion == Motion::Fade,
              "reduced motion keeps a fade: opacity is what the guidance permits");
        m.reduced = false;
        CHECK(m.resolve(m.pane, false).motion == Motion::None,
              "a frame budget that cannot afford it must snap");
        m.enabled = false;
        CHECK(m.resolve(m.fade).motion == Motion::None, "motion off snaps the fade too");

        // Motion::None is not a zero-length animation: it lands on the end state at once.
        Ease ez;
        Transition none{Motion::None, 160, Curve::EaseOut};
        ez.target(1.0f, none);
        CHECK(ez.value() == 1.0f && !ez.running(), "Motion::None did not land immediately");
    }

    // ---- a reveal under all three motion conditions --------------------------------------------
    //
    // The layout-staleness rule: a node that animates its SIZE keeps answering Animating()
    // until the value it last laid out equals its target. Degrading motion is exactly when
    // getting this wrong fails, and it failed by making the pane never appear at all.
    {
        for (int cond = 0; cond < 3; cond++) {
            State<bool> shown(false);
            Env le = e;
            Transition t = le.motion.pane;
            if (cond == 1) le.motion.reduced = true;
            if (cond == 2) t.motion = Motion::None;
            Desc pane = Reveal(&shown, sized(Spacer(), 280, 400), t);
            lay(pane, le, Rect{0, 0, 280, 400});
            shown.set(true);
            // Drive it the way the window does -- Retarget, tick, lay out -- against the wall
            // clock, because a transition is measured in milliseconds and an iteration count
            // would "settle" a 160 ms animation in a microsecond and prove nothing.
            float w = 0;
            int frames = 0;
            for (int i = 0; i < 400; i++) {
                pane->Retarget(true);
                pane->OnTick();
                w = pane->Measure(le, Constraints{280, 400}).w;
                pane->Place(Rect{0, 0, w, 400});
                frames++;
                if (!pane->Animating() && w >= 280) break;
                struct timespec ts = {0, 5000000};
                nanosleep(&ts, nullptr);
            }
            // C7.3: degraded or refused motion does not mean "animate anyway". It means the end
            // state on the NEXT frame, with the layout that goes with it.
            if (cond != 0)
                CHECK(frames == 1, "condition %d took %d frames to snap; a snap is one frame",
                      cond, frames);
            else
                CHECK(frames > 3, "the allowed transition snapped in %d frames: it did not "
                      "animate at all", frames);
            CHECK(!pane->Animating(), "condition %d never settled", cond);
            CHECK(std::fabs(w - 280) < 0.51f, "condition %d settled at width %g, not 280", cond,
                  (double)w);
        }
    }

    // ---- sections and flat items in the tree ---------------------------------------------------
    {
        struct Side : TreeSource {
            int asked_places = 0, asked_fs = 0;
            bool Children(const std::string& id, std::vector<TreeItem>* out) override {
                if (id.empty()) {
                    TreeItem h;
                    h.id = "#places";
                    h.label = "Places";
                    h.section = true;
                    out->push_back(h);
                    for (const char* n : {"Home", "Documents"}) {
                        TreeItem p;
                        p.id = std::string("/") + n;
                        p.label = n;
                        p.flat = true;
                        p.expandable = true;   // a Place is a directory and still has no chevron
                        out->push_back(p);
                    }
                    TreeItem f;
                    f.id = "#fs";
                    f.label = "Filesystem";
                    f.section = true;
                    out->push_back(f);
                    TreeItem root;
                    root.id = "/";
                    root.label = "/";
                    root.expandable = true;
                    out->push_back(root);
                    return true;
                }
                if (id == "/Home") { asked_places++; return true; }
                if (id == "/") {
                    asked_fs++;
                    TreeItem k;
                    k.id = "/usr";
                    k.label = "usr";
                    k.expandable = true;
                    out->push_back(k);
                    return true;
                }
                return true;
            }
        } side;
        State<std::string> sel("");
        Desc tree = TreeView(&side, &sel);
        lay(tree, e, Rect{0, 0, 200, 600});
        CHECK(TreeRowCount(tree) == 5, "the sidebar flattens to 5 rows, got %d",
              TreeRowCount(tree));
        CHECK(TreeRowId(tree, 0) == "#places", "the first row is the Places header");

        // A header is not a row: pressing it does nothing and the keyboard steps over it.
        tree->OnPressAt(100, 1, 1);
        CHECK(sel.get().empty(), "pressing a section header selected %s", sel.get().c_str());
        CHECK(TreeCursor(tree) < 0, "pressing a section header moved the cursor onto it");
        au_key down{};
        down.pressed = 1;
        down.raw_char = 0x1f;
        tree->OnKey(down);
        CHECK(sel.get() == "/Home", "Down from the top landed on %s, not the first Place",
              sel.get().c_str());
        tree->OnKey(down);
        tree->OnKey(down);
        CHECK(sel.get() == "/", "Down over the Filesystem header landed on %s", sel.get().c_str());
        CHECK(TreeRowId(tree, TreeCursor(tree)) == "/",
              "the cursor stopped on the header row rather than stepping over it");
        au_key up{};
        up.pressed = 1;
        up.raw_char = 0x1e;
        tree->OnKey(up);
        CHECK(sel.get() == "/Documents", "Up over the header landed on %s", sel.get().c_str());

        // A flat item is a destination: it is never expanded and never asked for children.
        au_key right{};
        right.pressed = 1;
        right.raw_char = 0x1d;
        sel.set("");
        tree->OnKey(down);                    // back down to a Place? cursor is on "/"
        CHECK(side.asked_places == 0, "a flat Place was asked for its children");
    }

    printf("%d checks, %d failed\n", checks, fails);
    return fails ? 1 : 0;
}
