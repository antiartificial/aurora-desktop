// 8.7: the sidebar is sections of destinations with one selected, and it says so.
//
// The checks are about what this component decides -- its role, the structure it hands back, the
// order the app and the accessibility tree read, and what the keyboard does -- not about the row
// drawing, which the toolkit's own tree test already covers. No font is needed: the row list is
// the tree's observable state and it is flattened without a frame.
#include "components/sidebar.h"

#include <cstdio>
#include <string>
#include <vector>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

static au_key key(uint32_t raw) {
    au_key k{};
    k.pressed = 1;
    k.raw_char = raw;
    return k;
}

static const uint32_t kDown = 0x1f, kEnter = 0x0d;

static bool pixel_is(const au_surface* s, int x, int y, au_color c) {
    const uint32_t* p = au_surface_pixels(s);
    const int w = au_surface_px_w(s);
    if (!p || x < 0 || y < 0 || x >= w || y >= au_surface_px_h(s)) return false;
    const uint32_t v = p[y * w + x];
    return ((v >> 16) & 0xFF) == c.r && ((v >> 8) & 0xFF) == c.g && (v & 0xFF) == c.b;
}

int main() {
    const Env e = Env();

    // Two sections in the order the app declares them, one destination carrying a note and one
    // section with no key at all -- the two shapes a caller gets wrong first.
    std::vector<SidebarSection> sections = {
        {"places", "Places", {{"home", "Home"}, {"documents", "Documents"}}},
        {"", "", {{"root", "Filesystem", true, "7"}}},
        {"empty", "Recent", {}},   // no destinations: draws nothing
    };

    State<std::string> selected("");
    int navigated = 0;
    std::string last;
    Desc side = Sidebar(std::move(sections), selected,
                        [&](const std::string& id) { ++navigated; last = id; });

    check(std::string(side->Role()) == "navigation", "the component declares its own role");
    check(std::string(side->Role()) != "control", "a role better than control (8.7's check)");

    // The role node wraps one body (the stack), and the stack holds the pane. A caller gets one
    // node either way, which is what keeps the pane's 200-unit natural width from leaking out.
    std::vector<Node*> body;
    side->Children(&body);
    check(body.size() == 1, "the sidebar wraps one body");
    std::vector<Node*> kids;
    if (!body.empty()) body[0]->Children(&kids);
    check(kids.size() == 1, "the body holds the pane");
    if (!kids.empty()) {
        check(std::string(kids[0]->Role()) == "tree", "the pane is the toolkit's tree");
        check(kids[0]->name == "sidebar", "the pane is addressable by name");
    }

    Desc tree = SidebarTree(side);
    check(tree && tree.get() == (kids.empty() ? nullptr : kids[0]),
          "the tree verb reaches the pane the component built");
    check(!SidebarTree(Desc()), "a Desc that is not a Sidebar has no pane");

    // ---- what is showing, in order ----------------------------------------------------------
    // Heading, its destinations, heading, destination. The unnamed section takes a position-based
    // id so its heading cannot collide with another unnamed one, and the empty section is gone.
    check(TreeRowCount(tree) == 5, "three sections flatten to five rows, not seven");
    check(TreeRowId(tree, 0) == "section:places" && TreeRowId(tree, 1) == "home" &&
              TreeRowId(tree, 2) == "documents",
          "the first section keeps its declared order");
    check(TreeRowId(tree, 3) == "section:s1" && TreeRowId(tree, 4) == "root",
          "the unnamed section is identified by position and keeps its row");
    check(TreeRowDepth(tree, 1) == 0 && TreeRowDepth(tree, 4) == 0,
          "a destination sits at the root, not under its heading");
    check(DuplicateNames(side).empty(), "no two nodes share a name");

    // ---- the keyboard drives the app's cell ------------------------------------------------
    // Down steps over the heading, selects the first destination and WRITES the cell: the app
    // reads the selection as a value, it is not asked for it.
    tree->OnKey(key(kDown));
    check(selected.get() == "home", "Down from nothing selects the first destination");
    check(navigated == 0, "moving the cursor is not a navigation");
    tree->OnKey(key(kEnter));
    check(navigated == 1 && last == "home", "Enter navigates to the selected destination");

    // ---- a disabled destination stays visible and stays unselectable -----------------------
    std::vector<SidebarSection> with_off = {
        {"places", "Places", {{"home", "Home"}, {"trash", "Trash", false}}},
    };
    State<std::string> sel2("");
    int nav2 = 0;
    Desc off = Sidebar(std::move(with_off), sel2, [&](const std::string&) { ++nav2; });
    Desc off_tree = SidebarTree(off);
    check(TreeRowCount(off_tree) == 3, "a disabled destination still takes its row");
    off_tree->OnKey(key(kDown));
    off_tree->OnKey(key(kDown));
    check(TreeCursor(off_tree) == 2, "the keyboard reaches the disabled row");
    check(sel2.get() == "home", "the disabled row is not selected");
    off_tree->OnKey(key(kEnter));
    check(nav2 == 0, "Enter on a disabled destination does not navigate");

    // A callback can be replaced after the tree is built, the way OnTreeActivate rebinds a tree.
    int nav3 = 0;
    OnSidebarNavigate(side, [&](const std::string&) { ++nav3; });
    tree->OnKey(key(kEnter));
    check(nav3 == 1, "a rebound activation handler is the one that runs");

    // ---- the paint --------------------------------------------------------------------------
    // The pane is the tree's, so it paints the sidebar role colour and nothing outside its frame.
    // No text lands (there is no font backend here), which is why every background pixel is the
    // pane's -- and that is the claim: this component contributes structure, not a second plate.
    au_surface* s = au_surface_new(200, 620, 1.0);
    check(s != nullptr, "an offscreen surface for the paint claims");
    if (s) {
        const au_color sentinel{10, 10, 10, 255};
        side->Measure(e, Constraints{200, 600});
        side->Place(Rect{0, 0, 200, 600});
        au_clear(s, sentinel);
        side->Draw(s, e);
        check(pixel_is(s, 2, 2, e.sidebar), "the pane paints the sidebar colour");
        check(pixel_is(s, 100, 599, e.sidebar), "the pane runs to its own last row");
        check(pixel_is(s, 100, 610, sentinel), "the pane paints nothing below its frame");
        au_surface_free(s);
    }

    if (fails) std::printf("sidebar: %d of %d FAILED\n", fails, checks);
    else std::printf("sidebar: ok (%d checks)\n", checks);
    return fails ? 1 : 0;
}
