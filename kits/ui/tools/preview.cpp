// aurora-preview: P3's loop. Every registered state, rendered at every viewport, diffed against
// goldens. ui_specimen is four fixed sheets that assert nothing; this is the other thing -- a
// registry, a viewport loop and a differ, so a one-pixel regression fails a command instead of
// waiting for someone to notice a picture in a document.
//
//   aurora-preview --list
//   aurora-preview --states DIR     write DIR/<state>-<WxH>.{png,argb}
//   aurora-preview --check DIR      render and diff against DIR; exit 1 on any difference
//   aurora-preview --watch DIR      re-check whenever this binary is rebuilt
//
// Goldens are compared as pixels (the .argb sidecar), not as PNG bytes, so a failure can say where
// the first difference is. The PNG is for a person to look at.
#include "ui.h"
#include "components/all.h"

#include <zlib.h>

#include <chrono>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <string>
#include <thread>
#include <vector>

#include <sys/stat.h>

using namespace aurora::ui;

// ---- PNG, with no image library ---------------------------------------------------------------
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
        raw.push_back('\0');   // filter 0
        for (int x = 0; x < w; x++) {
            const uint32_t p = argb[(size_t)y * w + x];
            raw.push_back((char)((p >> 16) & 0xff));
            raw.push_back((char)((p >> 8) & 0xff));
            raw.push_back((char)(p & 0xff));
        }
    }
    uLongf bound = compressBound((uLong)raw.size());
    std::string z(bound, '\0');
    if (compress2((Bytef*)z.data(), &bound, (const Bytef*)raw.data(), (uLong)raw.size(), 6) != Z_OK)
        return false;
    z.resize(bound);

    std::string png("\x89PNG\r\n\x1a\n", 8);
    std::string ihdr;
    be32(ihdr, (uint32_t)w);
    be32(ihdr, (uint32_t)h);
    ihdr.push_back(8);    // bit depth
    ihdr.push_back(2);    // truecolour
    ihdr.push_back(0);
    ihdr.push_back(0);
    ihdr.push_back(0);
    chunk(png, "IHDR", ihdr);
    chunk(png, "IDAT", z);
    chunk(png, "IEND", std::string());

    FILE* f = fopen(path.c_str(), "wb");
    if (!f) return false;
    const bool ok = fwrite(png.data(), 1, png.size(), f) == png.size();
    fclose(f);
    return ok;
}

// ---- rendering, the way a window does it ------------------------------------------------------
static Env g_env;

static au_surface* render(const Desc& d, float w, float h) {
    au_surface* s = au_surface_new((int)ceilf(w), (int)ceilf(h), 1.0);
    au_clear(s, g_env.content);
    d->Measure(g_env, Constraints{w, h});
    d->Place(Rect{0, 0, w, h});
    d->Draw(s, g_env);
    return s;
}

// ---- the state registry: the cases nobody remembers to check -----------------------------------
static std::string repeat_join(const char* unit, int n) {
    std::string s;
    for (int i = 0; i < n; i++) { if (i) s += " "; s += unit; }
    return s;
}

static Desc st_empty() {
    return VStack({Bold("Nothing here"), Label("This folder is empty.")}, 0.5f).padding(1.5f);
}

static Desc st_permission_denied() {
    return VStack({Bold("Permission denied"),
                   Label("You do not have access to this folder. Ask its owner for read access."),
                   Button("Try again", [] {})},
                  0.5f).padding(1.5f);
}

static Desc st_long_name() {
    const std::string n = repeat_join("rechnung_2024_quartal_wirtschaftspruefung", 5);
    return VStack({Bold("220-character name"),
                   Label(n),
                   Label("The name wraps rather than clipping, and the row grows.")},
                  0.5f).padding(1.5f);
}

static Desc st_cjk() {
    return VStack({Bold("CJK"), Label("文件管理器 — 日本語のフォルダ名 — 한글 폴더"),
                   Label("Mixed: Report 2024 — 報告書.pdf")},
                  0.5f).padding(1.5f);
}

static Desc st_rtl() {
    return VStack({Bold("RTL"), Label("مرحبا بالعالم — مجلد المستندات"),
                   Label("Mixed: Documents — مستندات.pdf")},
                  0.5f).padding(1.5f);
}

static Desc st_broken_link() {
    return VStack({Bold("Broken link"), Label("shortcut.desktop → /media/usb/report.pdf"),
                   Label("The target was moved or removed."), Button("Locate…", [] {})},
                  0.5f).padding(1.5f);
}

static Desc st_shell() {
    Desc sidebar = VStack({Bold("Places"), Label("Home"), Label("Documents"), Label("Downloads"),
                           Label("Trash")},
                          0.25f).padding(1.0f).weight(0.3f);
    Desc list = VStack({Bold("Documents"), Label("Report.pdf"), Label("Notes.md"),
                        Label("photo.png"), Label("budget.xlsx")},
                       0.25f).padding(1.0f).weight(1.0f);
    return HStack({sidebar, list}, 0.0f);
}

static Desc st_controls() {
    return VStack({Bold("Controls"), Button("Open", [] {}), Button("Disabled", [] {}),
                   Label("A button row, a long label, and a second line that wraps when the "
                         "viewport is narrow so the 360-pixel run is not just empty space.")},
                  0.5f).padding(1.5f);
}

// ---- one state per component ------------------------------------------------------------------
//
// The eight above are the cases nobody remembers to check. These are the twelve components of
// docs/ui-components.md in a configuration a person would meet. A widget that only appears inside
// another state's context (the NumberBox in a toolbar) gets its own state as well, so a regression
// names the widget instead of pointing at a whole shell.
//
// The cells are file-scope because a Desc carries a Binding and not a value: a State local to the
// builder would die before render() measures the tree, and every binding would quietly read its
// constant fallback -- a golden that pins nothing.
static State<std::string> g_sidebar_scope("documents");
static State<std::string> g_crumb_path("/home/arti/Documents");
static State<int> g_list_cursor(1);
static State<std::string> g_tree_choice("Documents/archive/report.pdf");
static State<double> g_export_quality(100.0);   // the upper bound, where a spinbutton can go wrong
static State<bool> g_details_open(true);
static State<bool> g_tip_shown(true);

static MenuItem menu_item(std::string label, std::string note = "",
                          Effect effect = Effect::Read, bool enabled = true) {
    MenuItem m;
    m.label = std::move(label);
    m.note = std::move(note);
    m.effect = effect;
    m.enabled = enabled;
    return m;
}

// The overflow menu the toolbar and the menu state both offer. The destructive verb sits after a
// separator: the menu is where the effect class is shown before the user commits.
static std::vector<MenuItem> overflow_items() {
    return {menu_item("Open", "Return"), menu_item("Open With..."), menu_item("Copy Path"),
            MenuItem::sep(), menu_item("Move to Trash", "", Effect::Destructive)};
}

// The list and the tree only exist through a source. Fixed data, not a directory read: a golden
// must not change because the machine that generated it had different files.
class PreviewRows : public Rows {
public:
    int Count() const override { return 7; }
    int Columns() const override { return 2; }
    ColumnSpec Column(int c) const override {
        ColumnSpec sp;
        if (c == 0) {
            sp.title = "Name";
            sp.width = 320;
            sp.min = 150;
            sp.flex = 1;            // the name absorbs the slack; Modified stays whole or drops
        } else {
            sp.title = "Modified";
            sp.width = 132;
            sp.min = 110;
        }
        return sp;
    }
    float ColumnWidth(int c) const override { return Column(c).width; }
    std::string ColumnTitle(int c) const override { return Column(c).title; }
    CellValue Cell(int row, int col) const override {
        static const char* names[] = {"Report.pdf", "Notes.md",     "photo.png",    "budget.xlsx",
                                      "archive.tar.gz", "cv-2026.pdf", "README.txt"};
        static const char* dates[] = {"2026-11-02", "2026-10-28", "2026-10-19", "2026-10-11",
                                      "2026-09-30", "2026-09-14", "2026-08-21"};
        if (row < 0 || row >= Count()) return CellValue{};
        CellValue v;
        v.state = CellState::Ready;
        v.text = col == 0 ? names[row] : dates[row];
        return v;
    }
    void Want(int, int) override {}
};
static PreviewRows g_rows;

class PreviewTree : public TreeSource {
public:
    bool Children(const std::string& id, std::vector<TreeItem>* out) override {
        auto leaf = [out](const char* id, const char* label) {
            TreeItem t;
            t.id = id;
            t.label = label;
            t.flat = true;          // a destination, not a branch: no chevron, no expansion
            out->push_back(std::move(t));
        };
        auto branch = [out](const char* id, const char* label) {
            TreeItem t;
            t.id = id;
            t.label = label;
            t.expandable = true;
            out->push_back(std::move(t));
        };
        if (id.empty()) {
            branch("Documents", "Documents");
            branch("Pictures", "Pictures");
            leaf("Trash", "Trash");
        } else if (id == "Documents") {
            leaf("Documents/notes.md", "notes.md");
            leaf("Documents/budget.xlsx", "budget.xlsx");
            branch("Documents/archive", "archive");
        } else if (id == "Documents/archive") {
            leaf("Documents/archive/report.pdf", "report.pdf");
            leaf("Documents/archive/old", "old-2025");
        } else if (id == "Pictures") {
            leaf("Pictures/2026-10", "2026-10");
            leaf("Pictures/2026-09", "2026-09");
        }
        return true;
    }
    // Parent is what RevealInTree walks to open the chain down to the selected file.
    std::string Parent(const std::string& id) const override {
        const size_t slash = id.rfind('/');
        return slash == std::string::npos ? std::string() : id.substr(0, slash);
    }
};
static PreviewTree g_tree;

static Desc st_ui_sidebar() {
    std::vector<SidebarSection> sections;
    sections.push_back(SidebarSection{
        "places", "Places",
        {SidebarItem{"home", "Home", true, ""},
         SidebarItem{"documents", "Documents", true, "12"},
         SidebarItem{"downloads", "Downloads", true, "3"},
         SidebarItem{"trash", "Trash", false, "read only"}}});
    sections.push_back(SidebarSection{
        "filesystem", "File System",
        {SidebarItem{"root", "Root", true, "/"}, SidebarItem{"media", "Removable", true, ""}}});
    Desc side = Sidebar(std::move(sections), g_sidebar_scope, [](const std::string&) {});
    // A sidebar is a pane: it keeps its width beside content rather than filling the page.
    return HStack({sized(side, 260),
                   VStack({Bold("Documents"), Label("Report.pdf"), Label("Notes.md"),
                           Label("photo.png"), Label("budget.xlsx")},
                          0.5f).padding(1.5f).weight(1.0f)},
                 0.0f);
}

static Desc st_ui_toolbar() {
    Desc crumb = Breadcrumb(g_crumb_path, [](const std::string&) {}, [] {});
    Desc bar = Toolbar({{ToolbarButton(Glyph::Back, "back", [] {}),
                         ToolbarButton(Glyph::Forward, "forward", [] {})},
                        {ToolbarButton(Glyph::Search, "search", [] {})}},
                       crumb,
                       {{MenuButton(Glyph::More, [] { return overflow_items(); })}});
    return VStack({bar,
                   VStack({Bold("Reports"), Label("4 folders, 17 files, 2.4 GB")}, 0.5f)
                       .padding(1.5f).weight(1.0f)},
                  0.0f);
}

static Desc st_ui_split_button() {
    // The control keeps its 150 px width inside a spacer, because an unweighted VStack child is
    // stretched across the cross axis and a SplitButton the width of the window is not the control.
    Desc sb = SplitButton("Export", [] {},
                          {menu_item("Export as PNG", "Cmd+E"), menu_item("Export as JPEG"),
                           menu_item("Export as WebP")});
    return VStack({Bold("Export"), HStack({sized(sb, 150), Spacer()}, 0.0f),
                   Label("One verb, plus its alternatives.")},
                  0.5f).padding(1.5f);
}

static Desc st_ui_number_box() {
    // At the upper bound: this is where a step, a typed value or a formatter must stay at 100
    // instead of rolling over or showing 100.00000000000001.
    Desc nb = NumberBox(g_export_quality, 1.0, 100.0, 1.0, [](double) {});
    return VStack({Bold("Quality"), HStack({Label("JPEG quality"), sized(nb, 140), Spacer()},
                                           1.0f),
                   Label("1 to 100, one per press; held at the maximum."),
                   Spacer()},
                  0.5f).padding(1.5f);
}

static Desc st_ui_info_bar() {
    // Warning with its one action: the message elides, the button keeps its natural width.
    return VStack({InfoBar(InfoSeverity::Warning, "3 files could not be moved: the destination "
                                                  "folder is read only.",
                           "Retry", [] {}),
                   Label("In the flow of the pane, not a dialog."),
                   Spacer()},
                  1.0f).padding(1.5f);
}

static Desc st_ui_expander() {
    Desc body = VStack({HStack({sized(Label("Name"), 62), Label("Report.pdf")}, 0.5f),
                        HStack({sized(Label("Kind"), 62), Label("PDF document")}, 0.5f),
                        HStack({sized(Label("Size"), 62), Label("2.4 MB")}, 0.5f)},
                       0.5f).padding(0.5f);
    return VStack({Expander("Details", g_details_open, body), Spacer()}, 0.5f).padding(1.5f);
}

static Desc st_ui_content_dialog() {
    // A long body in a narrow window is capped and clipped above the buttons, so the primary is
    // always in view. That is the state this pins: an answerable confirmation, not a cut-off one.
    return ContentDialog(
        "Copy this photo's path?",
        "The path /home/arti/Pictures/2024/Q4/beach.jpg is copied to the clipboard. The photo "
        "stays where it is: nothing is moved, renamed, or changed. You can paste the path into a "
        "terminal, a message or another application, and you can copy it again at any time.",
        Affirm::Continue, [] {}, [] {});
}

static Desc st_ui_teaching_tip() {
    return VStack({TeachingTip("Details follow your selection",
                               "Pick a photo and its name, size and kind appear here. Open "
                               "Details to copy its path.",
                               g_tip_shown, [] {}, "Got it"),
                   Label("The caller owns whether the note is shown."),
                   Spacer()},
                  1.0f).padding(1.5f);
}

static Desc st_ui_breadcrumb() {
    Desc crumb = Breadcrumb(g_crumb_path, [](const std::string&) {}, [] {});
    return VStack({Bold("Location"), crumb,
                   Label("Each segment links to that prefix."),
                   Spacer()},
                  0.5f).padding(1.5f);
}

static Desc st_ui_list() {
    Desc list = ListView(&g_rows, g_list_cursor);
    ListIcons(list);        // the list is over files here, so the icon column is the point
    return VStack({Bold("Documents"), list.weight(1.0f)}, 0.5f).padding(1.5f);
}

static Desc st_ui_menu() {
    // Menu itself is a popup on its own Wayland surface and cannot be drawn into this offscreen
    // surface; MenuButton is the in-tree half a client builds, so that is what the state pins.
    Desc more = MenuButton(Glyph::More, [] { return overflow_items(); });
    return VStack({Bold("Report.pdf"), Label("2.4 MB - modified 2026-11-02"),
                   Label("Anchors the menu for the selected row."),
                   HStack({Spacer(), more}, 0.0f), Spacer()},
                  0.5f).padding(1.5f);
}

static Desc st_ui_tree() {
    Desc t = TreeView(&g_tree, g_tree_choice);
    // RevealId scrolls the selected row into view. With no frame yet it would scroll the whole
    // tree to that row, so give it a nominal layout first; render() measures and places the real
    // one afterwards, leaving only the open set and the cursor behind.
    t->Measure(g_env, Constraints{320, 600});
    t->Place(Rect{0, 0, 320, 600});
    RevealInTree(t, g_tree_choice.get());
    return VStack({Label("Documents"), t.weight(1.0f)}, 0.5f).padding(1.5f);
}

struct PreviewState { const char* name; Desc (*build)(); };
static const PreviewState kStates[] = {
    {"empty", st_empty},         {"permission-denied", st_permission_denied},
    {"long-name", st_long_name}, {"cjk", st_cjk},
    {"rtl", st_rtl},             {"broken-link", st_broken_link},
    {"shell", st_shell},         {"controls", st_controls},
    // one per component in docs/ui-components.md
    {"sidebar", st_ui_sidebar},         {"toolbar", st_ui_toolbar},
    {"split-button", st_ui_split_button}, {"number-box", st_ui_number_box},
    {"info-bar", st_ui_info_bar},       {"expander", st_ui_expander},
    {"content-dialog", st_ui_content_dialog}, {"teaching-tip", st_ui_teaching_tip},
    {"breadcrumb", st_ui_breadcrumb},   {"list", st_ui_list},
    {"menu", st_ui_menu},               {"tree", st_ui_tree},
};


struct Viewport { int w, h; };
static const Viewport kViewports[] = {{360, 640}, {768, 1024}, {1440, 900}};

// ---- goldens ----------------------------------------------------------------------------------
static std::string stem(const char* state, const Viewport& v) {
    char buf[160];
    snprintf(buf, sizeof buf, "%s-%dx%d", state, v.w, v.h);
    return buf;
}

static bool save_golden(const std::string& dir, const std::string& base, au_surface* s) {
    return write_png(dir + "/" + base + ".png", au_surface_pixels(s), au_surface_px_w(s),
                     au_surface_px_h(s));
}

// Our own encoder writes filter 0 only, so the decoder needs exactly that: inflate, then strip the
// one filter byte per row. A golden edited by some other tool may use another filter; we refuse it
// loudly rather than decode it wrong.
struct Golden { int w = 0, h = 0; std::vector<uint32_t> px; };

static uint32_t rd32(const unsigned char* p) {
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) | ((uint32_t)p[2] << 8) | p[3];
}

static bool load_golden(const std::string& path, Golden* g) {
    FILE* f = fopen(path.c_str(), "rb");
    if (!f) return false;
    std::vector<unsigned char> buf;
    unsigned char tmp[65536];
    size_t n;
    while ((n = fread(tmp, 1, sizeof tmp, f)) > 0) buf.insert(buf.end(), tmp, tmp + n);
    fclose(f);
    if (buf.size() < 8 || memcmp(buf.data(), "\x89PNG\r\n\x1a\n", 8) != 0) return false;

    std::string idat;
    int bitdepth = 0, colortype = -1;
    for (size_t off = 8; off + 8 <= buf.size();) {
        const uint32_t len = rd32(&buf[off]);
        const std::string tag((const char*)&buf[off + 4], 4);
        if (off + 12 + len > buf.size()) return false;
        const unsigned char* body = &buf[off + 8];
        if (tag == "IHDR" && len >= 13) {
            g->w = (int)rd32(body);
            g->h = (int)rd32(body + 4);
            bitdepth = body[8];
            colortype = body[9];
        } else if (tag == "IDAT") {
            idat.append((const char*)body, len);
        } else if (tag == "IEND") {
            break;
        }
        off += 12 + len;
    }
    if (bitdepth != 8 || colortype != 2 || g->w <= 0 || g->h <= 0) return false;

    const size_t stride = (size_t)g->w * 3;
    std::vector<unsigned char> raw((stride + 1) * (size_t)g->h);
    uLongf out = (uLongf)raw.size();
    if (uncompress((Bytef*)raw.data(), &out, (const Bytef*)idat.data(), (uLong)idat.size()) != Z_OK)
        return false;
    if (out != raw.size()) return false;

    g->px.resize((size_t)g->w * (size_t)g->h);
    for (int y = 0; y < g->h; y++) {
        const unsigned char* row = &raw[(stride + 1) * (size_t)y];
        if (row[0] != 0) return false;   // a filter we do not decode
        for (int x = 0; x < g->w; x++) {
            const unsigned char* p = row + 1 + (size_t)x * 3;
            g->px[(size_t)y * (size_t)g->w + x] =
                0xff000000u | ((uint32_t)p[0] << 16) | ((uint32_t)p[1] << 8) | p[2];
        }
    }
    return true;
}

int main(int argc, char** argv) {
    std::string mode, dir;
    for (int i = 1; i < argc; i++) {
        const std::string a = argv[i];
        if (a == "--list") mode = "list";
        else if (a == "--states" && i + 1 < argc) { mode = "states"; dir = argv[++i]; }
        else if (a == "--check" && i + 1 < argc) { mode = "check"; dir = argv[++i]; }
        else if (a == "--watch" && i + 1 < argc) { mode = "watch"; dir = argv[++i]; }
    }
    if (mode.empty()) {
        fprintf(stderr,
                "usage: aurora-preview --list | --states DIR | --check DIR | --watch DIR\n");
        return 2;
    }

    float sys = 0;
    if (!au_font_system("plain", &g_env.font, &sys)) {
        fprintf(stderr, "aurora-preview: no system font; nothing drawn\n");
        return 1;
    }
    if (!au_font_system("bold", &g_env.bold, &sys)) g_env.bold = g_env.font;
    g_env.size = Env::kBaseSize;

    if (mode == "list") {
        for (const PreviewState& st : kStates) printf("%-18s", st.name);
        printf("\nviewports:");
        for (const Viewport& v : kViewports) printf(" %dx%d", v.w, v.h);
        printf("\n");
        return 0;
    }

    const int total = (int)(sizeof kStates / sizeof *kStates) *
                      (int)(sizeof kViewports / sizeof *kViewports);
    auto run_check = [&]() -> int {
        int files = 0, bad = 0;
        for (const PreviewState& st : kStates) {
            for (const Viewport& v : kViewports) {
                au_surface* s = render(st.build(), (float)v.w, (float)v.h);
                Golden g;
                const std::string base = stem(st.name, v);
                if (!load_golden(dir + "/" + base + ".png", &g)) {
                    printf("MISSING %s\n", base.c_str());
                    ++bad;
                } else {
                    ++files;
                    const int w = au_surface_px_w(s), h = au_surface_px_h(s);
                    const uint32_t* px = au_surface_pixels(s);
                    if (w != g.w || h != g.h) {
                        printf("FAIL %s: size %dx%d, golden %dx%d\n", base.c_str(), w, h, g.w, g.h);
                        ++bad;
                    } else {
                        size_t n = 0;
                        int fx = -1, fy = -1;
                        for (int y = 0; y < h; y++)
                            for (int x = 0; x < w; x++)
                                if (px[(size_t)y * w + x] != g.px[(size_t)y * w + x]) {
                                    if (fx < 0) { fx = x; fy = y; }
                                    ++n;
                                }
                        if (n) {
                            printf("FAIL %s: %zu px differ, first at (%d,%d)\n", base.c_str(), n, fx,
                                   fy);
                            ++bad;
                        }
                    }
                }
                au_surface_free(s);
            }
        }
        if (bad) printf("preview: %d of %d images differ\n", bad, total);
        else printf("preview: %d images, all match\n", files);
        return bad ? 1 : 0;
    };

    if (mode == "states") {
        int n = 0;
        for (const PreviewState& st : kStates)
            for (const Viewport& v : kViewports) {
                au_surface* s = render(st.build(), (float)v.w, (float)v.h);
                if (!save_golden(dir, stem(st.name, v), s)) {
                    fprintf(stderr, "aurora-preview: cannot write %s/%s\n", dir.c_str(),
                            stem(st.name, v).c_str());
                    au_surface_free(s);
                    return 1;
                }
                au_surface_free(s);
                ++n;
            }
        printf("preview: wrote %d images to %s\n", n, dir.c_str());
        return 0;
    }

    if (mode == "check") return run_check();

    // watch: re-check whenever the binary is rebuilt (an external build is the edit signal, so the
    // loop never guesses which sources matter).
    auto mtime = [argv] {
        struct stat st{};
        return stat(argv[0], &st) == 0 ? (long long)st.st_mtime : -1LL;
    };
    long long last = mtime();
    int rc = run_check();
    printf("preview: watching (rebuild to re-check)\n");
    for (;;) {
        std::this_thread::sleep_for(std::chrono::milliseconds(400));
        const long long now = mtime();
        if (now != last) {
            last = now;
            printf("--- rebuild detected ---\n");
            rc = run_check();
        }
        (void)rc;
    }
}
