// Atlas stage 1, "the frame": the parts of docs/atlas-design.md sections 2, 3 and 5 that are a
// number rather than a picture, checked without a compositor.
//
// What is here and what is not. The checks that are geometry or text -- the column rule over
// Atlas's OWN ColumnSpecs, the em dash, the date formats, the band heights, the status line's
// two halves -- are reachable headless and are here. The ones that are a pixel on a running
// window (the zebra, the hover fill, the halo after an arrow key, the hover damage fraction)
// are driven against the real binary by harness/atlas-shots, because a headless list has no
// window and the halo follows the window's keyboard_driven flag.
//
// Every drawing claim runs at scale 1.0 AND 1.45. Three bugs in this repo in one day appeared
// only at a fractional scale, one of which displaced 140 of 145 rows.
#include "filebrowse.h"
#include "demo/atlas_chrome.h"

#include <sys/stat.h>
#include <unistd.h>

#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <ctime>
#include <string>
#include <vector>

using namespace aurora::ui;
using namespace aurora::ui::browse;

static int fails = 0, checks = 0;
#define CHECK(c, ...) do { checks++; if (!(c)) { fails++; printf("  FAIL %s:%d: ", __FILE__, __LINE__); printf(__VA_ARGS__); printf("\n"); } } while (0)

static float sum(const std::vector<float>& v) {
    float t = 0;
    for (float f : v) t += f;
    return t;
}

static uint32_t at(au_surface* s, float x, float y) {
    const double sc = au_surface_scale(s);
    const int px = (int)(x * sc), py = (int)(y * sc);
    if (px < 0 || py < 0 || px >= au_surface_px_w(s) || py >= au_surface_px_h(s)) return 0;
    return au_surface_pixels(s)[(size_t)py * au_surface_px_w(s) + px] & 0xFFFFFFu;
}
static uint32_t rgb(au_color c) { return ((uint32_t)c.r << 16) | ((uint32_t)c.g << 8) | c.b; }

static void lay(const Desc& d, const Env& e, Rect box) {
    d->Measure(e, Constraints{box.w, box.h});
    d->Place(box);
}

// A directory with one folder and one file, which is all the cell rules need.
static std::string make_dir() {
    char tmpl[] = "/tmp/atlasframe.XXXXXX";
    const char* d = mkdtemp(tmpl);
    if (!d) return std::string();
    const std::string dir = d;
    mkdir((dir + "/Pictures").c_str(), 0755);
    FILE* f = fopen((dir + "/note.txt").c_str(), "wb");
    if (f) { fputs("hello", f); fclose(f); }
    return dir;
}
static void rm_dir(const std::string& d) {
    unlink((d + "/note.txt").c_str());
    rmdir((d + "/Pictures").c_str());
    rmdir(d.c_str());
}

int main() {
    // A fixed zone, because "Today" is computed against LOCAL midnight: a date test that ran in
    // the machine's zone would pass or fail depending on where the machine is.
    setenv("TZ", "UTC", 1);
    tzset();

    Env e;
    float sys_size = 0;
    const bool have_font = au_font_system("plain", &e.font, &sys_size) != 0;
    if (!au_font_system("bold", &e.bold, &sys_size)) e.bold = e.font;
    if (!au_font_system("italic", &e.italic, &sys_size)) e.italic = e.font;
    e.size = default_base_size();
    if (!have_font) printf("  (no system font: the drawing checks are skipped)\n");

    const std::string dir = make_dir();
    if (dir.empty()) { printf("FAIL: no temp directory\n"); return 1; }

    // ---- the columns Atlas declares (C5.1, C5.2) -------------------------------------------
    //
    // The numbers are the design's table, not the ones that happened to be in the file: Name
    // flexes from a floor of 160, Size is 84 and right-aligned, Modified is 132, right-aligned
    // and only shown from 600.
    {
        DirRows rows;
        rows.Open(dir);
        const ColumnSpec name = rows.Column(0), size = rows.Column(1), mod = rows.Column(2);
        CHECK(rows.Columns() == 3, "the details view has three columns in stage 1, got %d",
              rows.Columns());
        CHECK(name.flex > 0 && name.min == 160, "Name is the flexible column with a 160 floor: flex %g min %g",
              (double)name.flex, (double)name.min);
        CHECK(size.flex == 0 && size.width == 84 && size.min == 72 && size.align == Align::Right,
              "Size is 84/72, fixed and right-aligned: %g/%g flex %g align %d", (double)size.width,
              (double)size.min, (double)size.flex, (int)size.align);
        CHECK(mod.flex == 0 && mod.width == 132 && mod.min == 96 && mod.align == Align::Right &&
                  mod.visible_from == 600,
              "Modified is 132/96, right-aligned, from 600: %g/%g align %d from %g",
              (double)mod.width, (double)mod.min, (int)mod.align, (double)mod.visible_from);

        // The 960x640 reference window: 200 sidebar, 4 divider, so the list is 756 wide. Every
        // pixel is accounted for, which is exactly what puts the Modified column's right edge
        // 8 px inside the pane (the widget's cell padding) instead of under it.
        const std::vector<float> ref = SolveColumns(&rows, 756);
        CHECK(std::fabs(sum(ref) - 756) < 0.51f,
              "the columns leave %g px unaccounted at the reference width", (double)(756 - sum(ref)));
        CHECK(ref[1] == 84 && ref[2] == 132, "a fixed column must not move: %g %g",
              (double)ref[1], (double)ref[2]);
        CHECK(ref[0] == 756 - 84 - 132, "Name absorbs the slack: %g", (double)ref[0]);

        // Across every width a window can have: nothing overflows, nothing is shown below its
        // floor, and Modified is either absent or whole. "Cut off mid-value" is the defect this
        // sentence makes impossible, so it is the sentence that is asserted.
        for (float w = 200; w <= 1600; w += 3) {
            const std::vector<float> c = SolveColumns(&rows, w);
            CHECK(sum(c) <= w + 0.51f, "columns overflow a %g-wide list by %g", (double)w,
                  (double)(sum(c) - w));
            CHECK(c[1] == 0 || c[1] == 84, "Size is %g at width %g -- part of a column is not a column",
                  (double)c[1], (double)w);
            CHECK(c[2] == 0 || c[2] == 132, "Modified is %g at width %g", (double)c[2], (double)w);
            CHECK(w >= 600 || c[2] == 0, "Modified is drawn at %g, under its 600 threshold", (double)w);
        }
        CHECK(SolveColumns(&rows, 600)[2] == 132, "Modified appears whole at exactly 600");
        CHECK(SolveColumns(&rows, 599)[2] == 0, "Modified is gone at 599");
    }

    // ---- the cells (C5.2, C1.3) --------------------------------------------------------------
    {
        DirRows rows;
        rows.Open(dir);
        // No worker: this is the FIRST frame, before any lstat has landed. What is known then
        // is the name and that it is a directory, and a directory's size is known with it.
        int folder = -1, file = -1;
        for (int i = 0; i < rows.Count(); i++)
            (rows.Cell(i, 0).text == "Pictures/" ? folder : file) = i;
        CHECK(folder >= 0 && file >= 0, "the fixture has a folder and a file: %d %d", folder, file);
        if (folder >= 0) {
            const CellValue v = rows.Cell(folder, 1);
            CHECK(v.state == CellState::Ready,
                  "a folder's size is known from readdir; it was drawn blank waiting for a stat");
            CHECK(v.text == "\xe2\x80\x94", "a folder's size is an em dash, got \"%s\"",
                  v.text.c_str());
        }
        if (file >= 0)
            CHECK(rows.Cell(file, 1).state == CellState::Pending,
                  "a file's size is not known before its stat lands");

        // And once the worker has run, the file's size and date are there and neither is blank.
        rows.Want(0, rows.Count() - 1);
        rows.StartWorker();
        for (int i = 0; i < 200 && rows.stated() < rows.Count(); i++) usleep(5000);
        rows.Stop();
        if (file >= 0) {
            CHECK(rows.Cell(file, 1).state == CellState::Ready && rows.Cell(file, 1).text == "5 B",
                  "the file's size after the stat: \"%s\"", rows.Cell(file, 1).text.c_str());
            const CellValue m = rows.Cell(file, 2);
            CHECK(m.state == CellState::Ready && m.text.compare(0, 5, "Today") == 0,
                  "a file written now reads \"Today HH:MM\", got \"%s\"", m.text.c_str());
        }
    }

    // ---- the Modified format (C5.3) ----------------------------------------------------------
    //
    // A fixed `now` and a fixed zone, so every branch is reachable and none of them depends on
    // when the test runs.
    {
        struct tm t {};
        t.tm_year = 126; t.tm_mon = 10; t.tm_mday = 7; t.tm_hour = 15; t.tm_min = 30;
        t.tm_isdst = 0;
        const time_t now = timegm(&t);                       // 2026-11-07 15:30 UTC
        auto mk = [](int y, int mo, int d, int h, int mi) {
            struct tm x {};
            x.tm_year = y - 1900; x.tm_mon = mo - 1; x.tm_mday = d; x.tm_hour = h; x.tm_min = mi;
            return timegm(&x);
        };
        struct { time_t when; float width; const char* want; } cases[] = {
            {mk(2026, 11, 7, 12, 56), 132, "Today 12:56"},
            {mk(2026, 11, 7, 0, 0),   132, "Today 00:00"},
            {mk(2026, 11, 6, 9, 14),  132, "Yesterday 09:14"},
            // 23:50 yesterday is 15 hours ago -- under a day, and still not "Today". This is the
            // case a "less than 86,400 seconds" rule gets wrong, and the reason midnight is
            // computed rather than subtracted.
            {mk(2026, 11, 6, 23, 50), 132, "Yesterday 23:50"},
            {mk(2026, 11, 3, 14, 2),  132, "3 Nov 14:02"},
            {mk(2025, 3, 4, 9, 30),   132, "4 Mar 2025"},
            // Between 96 and 119 the time is dropped and the date survives.
            {mk(2026, 11, 7, 12, 56), 96,  "Today"},
            {mk(2026, 11, 6, 9, 14),  96,  "Yesterday"},
            {mk(2026, 11, 3, 14, 2),  96,  "3 Nov"},
            {mk(2025, 3, 4, 9, 30),   96,  "4 Mar 2025"},
        };
        for (const auto& c : cases) {
            const std::string got = when_relative(c.when, c.width, now);
            CHECK(got == c.want, "a %g-wide Modified column wants \"%s\", got \"%s\"",
                  (double)c.width, c.want, got.c_str());
        }
        // The day is not space-padded: " 3 Nov" would put the 1st to the 9th of a month half a
        // character out of line in a right-aligned column.
        CHECK(when_relative(mk(2026, 11, 3, 14, 2), 132, now)[0] == '3',
              "the day of the month is not padded");
    }

    // ---- the bands (C2.1) ---------------------------------------------------------------------
    //
    // Toolbar 44, status bar 26, and the toolbar's 28 px buttons centred inside it. The button
    // height is the one worth asserting: a stack stretches its children to its cross size, so a
    // 28 px button in a 44 px bar is only 28 if something hands the stack 28.
    {
        // Laid out as the window lays it out -- a VStack of toolbar, body, status bar at
        // 960x640 -- and not by placing each band in a box of the size the test wants. A band
        // placed in a 44-tall rect is 44 tall whatever it measured; the question is whether the
        // STACK gives it 44, which is the question the window asks.
        Desc btn = IconButton(Glyph::Back, [] {});
        Desc bar = atlas::Band(HStack({btn, Spacer()}, 0.5f), 44, atlas::BandFill::Chrome,
                               atlas::BandEdge::Bottom, Insets(8, 8));
        State<std::string> sl("13 items"), sr("");
        Desc foot = atlas::Band(atlas::StatusLine(&sl, &sr), 26, atlas::BandFill::Chrome,
                                atlas::BandEdge::Top, Insets(8, 4));
        Desc body = (Spacer()).weight(1);
        Desc root = VStack({bar, body, foot}, 0);
        lay(root, e, Rect{0, 0, 960, 640});
        CHECK(bar->frame.h == 44 && bar->frame.y == 0, "the toolbar is 44 tall at the top, got %g at %g",
              (double)bar->frame.h, (double)bar->frame.y);
        CHECK(foot->frame.h == 26 && foot->frame.y == 614,
              "the status bar is 26 tall at the bottom, got %g at %g", (double)foot->frame.h,
              (double)foot->frame.y);
        CHECK(body->frame.h == 570, "the body takes the remaining 570, got %g",
              (double)body->frame.h);
        CHECK(std::fabs(btn->frame.h - 28) < 0.51f, "a toolbar button is 28 tall, got %g",
              (double)btn->frame.h);
        CHECK(std::fabs(btn->frame.y - 8) < 0.51f, "a 28 px button sits 8 below the bar's top, got %g",
              (double)btn->frame.y);
        CHECK(std::fabs(btn->frame.x - 8) < 0.51f, "the toolbar's end padding is 8, got %g",
              (double)btn->frame.x);

        // The preview pane beside the listing. A side band states its WIDTH and the weighted
        // sibling takes the rest; a band that answered "all of it" starved the list to zero and
        // the preview opened over the whole listing, which is how this check came to exist.
        Desc listing = (Spacer()).weight(2);
        Desc pane = atlas::SideBand(Label("preview"), 280, atlas::BandFill::Content,
                                    atlas::BandEdge::Left, Insets(16, 16));
        Desc beside = HStack({listing, pane}, 0);
        lay(beside, e, Rect{0, 44, 960, 500});
        CHECK(pane->frame.w == 280, "the preview pane is 280 wide, got %g", (double)pane->frame.w);
        CHECK(std::fabs(listing->frame.w - 680) < 0.51f,
              "the listing keeps the other 680, got %g", (double)listing->frame.w);

        // And the list's own two: a 24 px row and a 26 px header at the base size of 13.
        DirRows rows;
        rows.Open(dir);
        State<int> sel(-1);
        Desc list = ListView(&rows, sel);
        lay(list, e, Rect{0, 44, 756, 570});
        CHECK(ListRowHeight(list) == 24, "a details row is 24 at base 13, got %g",
              (double)ListRowHeight(list));
        CHECK(ListHeaderHeight(list) == 26, "the column header is 26, got %g",
              (double)ListHeaderHeight(list));
    }

    // ---- the band draws its fill and its hairline, at both scales ------------------------------
    if (have_font) {
        for (double sc : {1.0, 1.45}) {
            au_surface* s = au_surface_new(200, 60, sc);
            au_clear(s, au_color{0, 0, 0, 255});
            Desc bar = atlas::Band(HStack({Spacer()}, 0.5f), 44, atlas::BandFill::Chrome,
                                   atlas::BandEdge::Bottom, Insets(8, 8));
            lay(bar, e, Rect{0, 0, 200, 44});
            bar->Draw(s, e);
            CHECK(at(s, 100, 20) == rgb(e.surface),
                  "at scale %g the toolbar is not filled with `chrome`: %06X", sc, at(s, 100, 20));
            // The hairline is one LOGICAL pixel, so at 1.45 it lands across two device rows and
            // the sample is a blend of it and the chrome under it. The claim is that the band's
            // last row is the separator and not the fill, which is what near() asks.
            const uint32_t h = at(s, 100, 43);
            auto near = [](uint32_t a, au_color b) {
                return std::abs((int)((a >> 16) & 0xFF) - b.r) < 16 &&
                       std::abs((int)((a >> 8) & 0xFF) - b.g) < 16 &&
                       std::abs((int)(a & 0xFF) - b.b) < 16;
            };
            CHECK(near(h, e.hairline) && !near(h, e.surface),
                  "at scale %g the toolbar's last row is not the hairline: %06X", sc, h);
            CHECK(at(s, 100, 50) != rgb(e.surface),
                  "at scale %g the band painted past its own height", sc);
            au_surface_free(s);
        }
    }

    // ---- the status line's two halves (C2.4) ----------------------------------------------------
    //
    // The rule is not "it looks fine at 960": it is that at EVERY width down to the window's
    // 480 minimum the right half is elided into what the left leaves, and the two never touch.
    // A left text and a right text measured apart is the display-integrity bug this codebase
    // has shipped four times.
    if (have_font) {
        State<std::string> left("12 of 1 204 selected · 9.3 KB");
        State<std::string> right(
            "open a-very-long-file-name-that-will-not-fit-in-the-name-column-at-all.tar.gz");
        for (double sc : {1.0, 1.45}) {
            au_surface* s = au_surface_new(1300, 30, sc);
            Desc line = atlas::StatusLine(&left, &right);
            atlas::StatusLineNode* n = static_cast<atlas::StatusLineNode*>(line.get());
            for (float w = 1280; w >= 460; w -= 1) {
                au_clear(s, au_color{0, 0, 0, 255});
                lay(line, e, Rect{8, 0, w - 16, 26});
                line->Draw(s, e);
                CHECK(n->right_x() >= line->frame.x + n->left_width() + e.unit - 0.51f,
                      "at %g wide and scale %g the status halves overlap: the left ends at %g, "
                      "the right starts at %g",
                      (double)w, sc, (double)(line->frame.x + n->left_width()),
                      (double)n->right_x());
                CHECK(n->right_x() + 0.51f >= line->frame.x,
                      "at %g wide the right half starts outside the bar", (double)w);
                // The RIGHT half is the one that gives. A line that shortened the left to make
                // room would pass every arithmetic check above and still be wrong: the count is
                // the half that is read, and it is the shorter one.
                CHECK(n->left_shown() == left.get(),
                      "at %g wide and scale %g the LEFT half was shortened to \"%s\"", (double)w,
                      sc, n->left_shown().c_str());
                CHECK(!n->right_shown().empty(),
                      "at %g wide the right half was dropped altogether", (double)w);
                // Whole when it fits, elided when it does not -- and never simply dropped.
                const float cap = e.size * 0.85f;
                const float full = au_text_width(e.font, cap, left.get().c_str(), -1) + e.unit +
                                   au_text_width(e.italic, cap, right.get().c_str(), -1);
                if (full <= line->frame.w)
                    CHECK(n->right_shown() == right.get(),
                          "at %g wide the right half fits and was elided anyway", (double)w);
                else
                    // Not fewer BYTES: dropping one character for a three-byte ellipsis makes
                    // the elided string longer. What must shrink is the width on screen.
                    CHECK(n->right_shown() != right.get(),
                          "at %g wide the two halves cannot both fit and the right was not elided",
                          (double)w);
            }
            au_surface_free(s);
        }
    }

    // ---- two states that must not render alike --------------------------------------------------
    //
    // "Failed: ..." is an error and is drawn in `destructive`; the same sentence without the
    // word is not. If those two produced the same pixels the colour would be carrying no
    // information, which is the bug class this repo treats as correctness.
    if (have_font) {
        State<std::string> left("13 items");
        State<std::string> ok("renamed a -> b"), bad("Failed: Permission denied");
        au_surface* a = au_surface_new(400, 26, 1.45);
        au_surface* b = au_surface_new(400, 26, 1.45);
        au_clear(a, au_color{0, 0, 0, 255});
        au_clear(b, au_color{0, 0, 0, 255});
        Desc la = atlas::StatusLine(&left, &ok), lb = atlas::StatusLine(&left, &bad);
        lay(la, e, Rect{0, 0, 400, 26});
        lay(lb, e, Rect{0, 0, 400, 26});
        la->Draw(a, e);
        lb->Draw(b, e);
        bool same = true, bad_is_red = false;
        for (int y = 0; y < au_surface_px_h(a) && same; y++)
            for (int x = 0; x < au_surface_px_w(a); x++)
                if (au_surface_pixels(a)[(size_t)y * au_surface_px_w(a) + x] !=
                    au_surface_pixels(b)[(size_t)y * au_surface_px_w(b) + x]) { same = false; break; }
        for (int y = 0; y < au_surface_px_h(b); y++)
            for (int x = 0; x < au_surface_px_w(b); x++) {
                const uint32_t p = au_surface_pixels(b)[(size_t)y * au_surface_px_w(b) + x] & 0xFFFFFFu;
                if (((p >> 16) & 0xFF) > ((p >> 8) & 0xFF) + 20) bad_is_red = true;
            }
        CHECK(!same, "an error and a plain last action draw the same pixels");
        CHECK(bad_is_red, "an error is not drawn in `destructive`");
        au_surface_free(a);
        au_surface_free(b);
    }

    rm_dir(dir);
    printf("test_atlas_frame: %d checks, %d failures\n", checks, fails);
    return fails ? 1 : 0;
}
