/*
 * Headless layout tests for the toolkit.
 *
 * No window, no compositor, no fonts: a Desc tree is Measured and Placed directly and the frames
 * are read back. That is the whole point -- layout is the part of a widget most likely to be
 * silently wrong, and the two worst layout bugs this toolkit has had (a weight applied on the
 * wrong axis, and a Reveal reserving width while invisible) were both found by eye, late, after
 * they had hidden other bugs behind them. A frame is a number; numbers belong in a test.
 *
 * Panes are Spacer() so nothing here needs a font face.
 */
#include "../ui.h"

#include <cstdio>
#include <cmath>
#include <map>

using namespace aurora::ui;

static int fails = 0;

static void near(const char* what, float got, float want, float tol = 0.51f) {
    if (std::fabs(got - want) > tol) {
        fprintf(stderr, "FAIL %s: got %.2f want %.2f\n", what, got, want);
        fails++;
    }
}

static void eq(const char* what, bool cond) {
    if (!cond) {
        fprintf(stderr, "FAIL %s\n", what);
        fails++;
    }
}

// Measure then Place, the way Window::Layout does.
static void lay(const Desc& root, const Env& e, Rect box) {
    root->Measure(e, Constraints{box.w, box.h});
    root->Place(box);
}

// A tree three levels deep that notes what it was asked for. The note is the point: the cost
// claim for a tree is that it asks only about what is expanded, and a claim about what a widget
// did NOT do can only be tested by recording what it did.
struct Fake : TreeSource {
    std::map<std::string, int> asked;

    bool Children(const std::string& id, std::vector<TreeItem>* out) override {
        asked[id]++;
        if (id.empty()) {
            out->push_back(TreeItem{"/a", "a", true});
            out->push_back(TreeItem{"/b", "b", true});
            return true;
        }
        if (id == "/a") {
            out->push_back(TreeItem{"/a/x", "x", true});
            out->push_back(TreeItem{"/a/y", "y", false});
            return true;
        }
        if (id == "/a/x") {
            out->push_back(TreeItem{"/a/x/deep", "deep", false});
            return true;
        }
        if (id == "/b") return false;          // a source that does not know its children yet
        return true;
    }

    std::string Parent(const std::string& id) const override {
        const size_t slash = id.rfind('/');
        return slash == 0 || slash == std::string::npos ? std::string() : id.substr(0, slash);
    }
};

static au_key pressing(uint32_t raw) {
    au_key k{};
    k.pressed = 1;
    k.raw_char = raw;
    return k;
}

// Enough rows to overflow any frame here, so the scrollbar has a reason to exist.
struct ManyRows : Rows {
    int n;
    explicit ManyRows(int count) : n(count) {}
    int Count() const override { return n; }
    CellValue Cell(int row, int) const override {
        return CellValue{CellState::Ready, "row " + std::to_string(row)};
    }
    int Columns() const override { return 2; }
    float ColumnWidth(int c) const override { return c == 0 ? 200 : 120; }
    std::string ColumnTitle(int c) const override { return c == 0 ? "Name" : "Size"; }

    // What the header asked for, so a test can hold the widget to it.
    int col = -1;
    bool asc = true;
    int sorts = 0;
    int SortColumn() const override { return col; }
    bool SortAscending() const override { return asc; }
    void Sort(int c, bool a) override {
        col = c;
        asc = a;
        sorts++;
    }
};

// A child with a natural size and no font behind it. Spacer measures zero, and everything that
// measures text needs a face, so a layout test of anything that reveals or divides needs this.
struct Box : Node {
    Size want;
    explicit Box(Size w) : want(w) {}
    Size Measure(const Env&, Constraints) override { return want; }
    void Draw(au_surface*, const Env&) override {}
};

// A child that answers with whatever it is offered, which is what TextView does: a wrapped
// document has no width of its own. Harmless alone, and the cause of the preview-pane bug the
// moment it sits unweighted beside a weighted sibling.
struct Greedy : Node {
    Size Measure(const Env&, Constraints c) override {
        return Size{c.max_w >= 0 ? c.max_w : 100, 50};
    }
    void Draw(au_surface*, const Env&) override {}
};

int main() {
    Env e;              // unit 8, no fonts needed by anything here
    const Rect box{0, 0, 1000, 600};

    // ---- HSplit divides the width it is given, at the ratio -----------------------------------
    {
        State<float> r(0.5f);
        Desc a = Spacer(), b = Spacer();
        Desc s = HSplit(a, b, r);
        lay(s, e, box);
        const float div = 4;                     // thickness 0.5 * unit 8
        near("hsplit.a.w at 0.5", a->frame.w, (1000 - div) / 2);
        near("hsplit.b.w at 0.5", b->frame.w, (1000 - div) / 2);
        near("hsplit.a.h fills cross", a->frame.h, 600);
        // The panes and the divider tile the box exactly: a gap would show the window's
        // background through the middle of a pane, which is how an off-by-one here looks.
        near("hsplit tiles", a->frame.w + div + b->frame.w, 1000);
        near("hsplit.b.x after divider", b->frame.x, a->frame.w + div);

        r.set(0.25f);
        lay(s, e, box);
        near("hsplit.a.w at 0.25", a->frame.w, (1000 - div) * 0.25f);
        near("hsplit.b.w at 0.25", b->frame.w, (1000 - div) * 0.75f);
    }

    // ---- the minimums are a floor, not a hint -------------------------------------------------
    {
        State<float> r(0.0f);
        Desc a = Spacer(), b = Spacer();
        Desc s = HSplit(a, b, r, SplitOpts{10, 10, 0.5f, 3});   // 80 px each side
        lay(s, e, box);
        near("min_a holds at ratio 0", a->frame.w, 80);
        r.set(1.0f);
        lay(s, e, box);
        near("min_b holds at ratio 1", b->frame.w, 80);
        near("min_b leaves the rest to a", a->frame.w, 1000 - 4 - 80);
    }

    // A box too small for both minimums must still produce non-negative panes rather than a
    // negative width, which would make Rect::has true for nothing and the pane unclickable.
    {
        State<float> r(0.5f);
        Desc a = Spacer(), b = Spacer();
        Desc s = HSplit(a, b, r, SplitOpts{10, 10, 0.5f, 3});
        lay(s, e, Rect{0, 0, 60, 600});
        eq("a.w not negative when cramped", a->frame.w >= 0);
        eq("b.w not negative when cramped", b->frame.w >= 0);
    }

    // ---- VSplit is the same on the other axis -------------------------------------------------
    {
        State<float> r(0.5f);
        Desc a = Spacer(), b = Spacer();
        Desc s = VSplit(a, b, r);
        lay(s, e, box);
        near("vsplit.a.h at 0.5", a->frame.h, (600 - 4) / 2);
        near("vsplit.a.w fills cross", a->frame.w, 1000);
        near("vsplit.b.y after divider", b->frame.y, a->frame.h + 4);
    }

    // ---- the relayout contract ----------------------------------------------------------------
    // Animating() is how the split tells the window its layout is stale. It must be false right
    // after a layout and true after the ratio moves, or a drag either never shows or lays the
    // window out forever. The second case is the one a clamp can cause, so it is checked at a
    // ratio the clamp rejects.
    {
        State<float> r(0.5f);
        Desc a = Spacer(), b = Spacer();
        Desc s = HSplit(a, b, r, SplitOpts{10, 10, 0.5f, 3});
        lay(s, e, box);
        eq("settled after layout", !s->Animating());
        r.set(0.3f);
        eq("stale after the ratio moves", s->Animating());
        lay(s, e, box);
        eq("settled again", !s->Animating());
        r.set(0.0f);                      // the clamp will refuse this one
        lay(s, e, box);
        eq("settled even when clamped", !s->Animating());
    }

    // ---- dragging the divider sets the ratio --------------------------------------------------
    {
        State<float> r(0.5f);
        Desc a = Spacer(), b = Spacer();
        Desc s = HSplit(a, b, r);
        lay(s, e, box);
        // Grab the divider at its left edge and drag to x = 300.
        Node* hit = s->HitTest(a->frame.w + 1, 300);
        eq("the divider takes the hit", hit == s.get());
        // Away from the divider the split must NOT take the hit. (A Spacer is deliberately
        // not a target at all, so the assertion is about the divider not swallowing the click,
        // which is the part that would move a selection the user was acting on.)
        eq("the divider does not swallow a click away from it", s->HitTest(100, 300) != s.get());
        s->OnPressAt(a->frame.w, 300, 1);
        s->OnDragTo(300, 300);
        lay(s, e, box);
        near("drag moved the divider", a->frame.w, 300);
        near("ratio followed", r.get(), 300.0f / (1000 - 4));

        // Double-click collapses, and again restores: a collapse with no way back would be the
        // only destructive gesture on this widget.
        s->OnPressAt(a->frame.w, 300, 2);
        lay(s, e, box);
        near("collapsed to min_a", a->frame.w, 8 * 8);
        s->OnPressAt(a->frame.w, 300, 2);
        lay(s, e, box);
        near("restored to where it was", a->frame.w, 300);
    }

    // ---- weight is per-axis, which is the bug this line exists for ----------------------------
    // A weighted child in a VStack takes a share of the HEIGHT and its natural width. Getting
    // this wrong cost three runs: weight(Breadcrumb(...), 1) in a VStack took 189 px of height
    // and swallowed the list below it.
    {
        Desc a = Spacer(), b = Spacer();
        Desc v = VStack({(a).weight(1), (b).weight(3)}, 0);
        lay(v, e, box);
        near("vstack weight 1 of 4", a->frame.h, 150);
        near("vstack weight 3 of 4", b->frame.h, 450);
        near("vstack child spans the width", a->frame.w, 1000);
    }

    // ---- the tree flattens to what is visible -------------------------------------------------
    // The whole cost argument for a tree is that a collapsed node is one row no matter what is
    // under it, and that only expanded nodes are ever asked for children. Both are checked by
    // counting what the source was asked, because the second is a claim about what did not happen.
    {
        Fake src;
        State<std::string> sel("");
        Desc t = TreeView(&src, sel);
        lay(t, e, Rect{0, 0, 200, 600});
        eq("root rows only while collapsed", TreeRowCount(t) == 2);
        eq("a collapsed node is not asked for children", src.asked.count("/a") == 0);

        // Row 0 is /a, 16-ish px tall. Clicking inside the indent hits the twisty.
        t->OnPressAt(4, 1, 1);
        eq("expanding adds the children", TreeRowCount(t) == 4);
        eq("the child is at depth 1", TreeRowDepth(t, 1) == 1);
        eq("the first child is /a/x", TreeRowId(t, 1) == "/a/x");
        eq("the twisty did not select", sel.get().empty());
        eq("the expanded node was asked once", src.asked["/a"] == 1);
        eq("its sibling still was not", src.asked.count("/b") == 0);

        // Past the indent, a click selects rather than toggling: "show me what is in there" is
        // not "go there", and a tree that conflates them navigates every time someone peeks.
        t->OnPressAt(120, 1, 1);
        eq("clicking the row selects it", sel.get() == "/a");
        eq("selecting did not collapse", TreeRowCount(t) == 4);
    }

    // ---- the keys that make a tree usable without a mouse -------------------------------------
    {
        Fake src;
        State<std::string> sel("");
        Desc t = TreeView(&src, sel);
        lay(t, e, Rect{0, 0, 200, 600});

        au_key down = pressing(0x1f), right = pressing(0x1d), left = pressing(0x1c);
        eq("down is handled", t->OnKey(down));
        eq("down selects the first row", sel.get() == "/a");

        t->OnKey(right);
        eq("right expands a collapsed node", TreeRowCount(t) == 4);
        t->OnKey(right);
        eq("right again steps into the first child", sel.get() == "/a/x");

        t->OnKey(left);
        eq("left on a closed node climbs to the parent", sel.get() == "/a");
        t->OnKey(left);
        eq("left on an open node collapses it", TreeRowCount(t) == 2);

        std::string fired;
        OnTreeActivate(t, [&](const std::string& id) { fired = id; });
        t->OnKey(pressing(0x0a));
        eq("Enter activates the cursor row", fired == "/a");
    }

    // ---- a source that does not know yet is not a leaf ----------------------------------------
    // /b answers false, which means "not yet". Expanding it must leave it expandable and add no
    // rows, rather than caching an empty answer -- a slow mount would otherwise read as empty.
    {
        Fake src;
        State<std::string> sel("");
        Desc t = TreeView(&src, sel);
        lay(t, e, Rect{0, 0, 200, 600});
        t->OnKey(pressing(0x1f));
        t->OnKey(pressing(0x1f));
        eq("cursor on /b", sel.get() == "/b");
        t->OnKey(pressing(0x1d));
        eq("a pending expand adds no rows", TreeRowCount(t) == 2);
        eq("and it was asked", src.asked.count("/b") == 1);
        t->OnKey(pressing(0x1c));
        eq("collapsing it again is harmless", TreeRowCount(t) == 2);
    }

    // ---- RevealInTree opens the chain ---------------------------------------------------------
    // The app names a destination; the widget works out which nodes have to open. Opening is what
    // makes a node's children askable at all, so the chain has to open from the top down.
    {
        Fake src;
        State<std::string> sel("");
        Desc t = TreeView(&src, sel);
        lay(t, e, Rect{0, 0, 200, 600});
        RevealInTree(t, "/a/x/deep");
        eq("the revealed id is selected", sel.get() == "/a/x/deep");
        eq("the chain opened", TreeRowCount(t) == 5);
        eq("the deep row is where it should be", TreeRowId(t, 2) == "/a/x/deep");
        eq("and at depth 2", TreeRowDepth(t, 2) == 2);
    }

    // ---- a press on the scrollbar is not a click on a row -------------------------------------
    // The list routes presses to its scrollbar before its own hit handling, and the hazard that
    // creates is the reason this test exists: a bar drawn OVER the rows means every press on it
    // lands on a row too, so dragging the thumb would also select whatever was behind it.
    {
        ManyRows src(500);
        State<int> selected(-1);
        Desc list = ListView(&src, selected);
        lay(list, e, Rect{0, 0, 400, 600});
        list->Draw(nullptr, e);              // the bar is laid out during Draw

        // x = 395 is inside the bar (9 px wide, at the right edge of a 400 px frame).
        list->OnPressAt(395, 300, 1);
        list->OnRelease(true);
        eq("pressing the scrollbar selects nothing", selected.get() == -1);

        // The same press away from the bar does select.
        list->OnPressAt(100, 300, 1);
        list->OnRelease(true);
        eq("pressing a row does select", selected.get() >= 0);
    }

    // ---- the bar appears only when there is somewhere to go -----------------------------------
    // A track with a full-length thumb is furniture that says nothing.
    {
        ManyRows few(3);
        State<int> selected(-1);
        Desc list = ListView(&few, selected);
        lay(list, e, Rect{0, 0, 400, 600});
        list->Draw(nullptr, e);
        // Just inside the first row. Not y = 12: that is the column header with a real font
        // loaded, and it only ever "passed" because the negative row index truncated to zero.
        list->OnPressAt(395, list->frame.y + 40, 1);
        list->OnRelease(true);
        eq("with nothing to scroll the press reaches the rows", selected.get() >= 0);
    }

    // ---- switching views is a layout, not a repaint -------------------------------------------
    // Either deliberately never measures the child that is not showing, so that child holds no
    // frame at all. Switching to it therefore has to force a layout, and when it did not, the
    // icon view came up BLANK -- but only when reached by pressing the button, because starting
    // in it laid it out like anything else. That is why this is a test and not a glance.
    {
        State<bool> first(true);
        Desc a = Spacer(), b = Spacer();
        Desc ei = Either(first, a, b);
        lay(ei, e, box);
        eq("settled after layout", !ei->Animating());
        near("the live child was placed", a->frame.w, 1000);
        eq("the hidden child was not placed at all", b->frame.w == 0);

        first.set(false);
        eq("a view switch leaves the layout stale", ei->Animating());
        lay(ei, e, box);
        near("the newly live child is placed", b->frame.w, 1000);
        near("and gets the full height", b->frame.h, 600);
        eq("settled again", !ei->Animating());
    }

    // ---- a reveal that cannot animate must still appear ---------------------------------------
    // Motion::None is the same path the budget gate takes when it degrades under frame pressure:
    // Retarget snaps, and nothing is left running. Reporting "settled" there meant no layout was
    // ever requested and the pane kept its old zero fraction -- so under load the preview pane
    // did not appear ABRUPTLY, it did not appear at all. A degradation path that fails exactly
    // when the system is already struggling is worth its own test.
    {
        State<bool> shown(false);
        Desc child = std::make_shared<Box>(Size{200, 50});
        Desc rv = Reveal(shown, child, Transition{Motion::RevealX, 160, Curve::EaseOut});
        lay(rv, e, box);
        near("hidden measures no width", rv->Measure(e, Constraints{1000, 600}).w, 0);
        eq("settled while hidden", !rv->Animating());

        shown.set(true);
        rv->Retarget(false);                      // motion not allowed: snap
        eq("a snapped reveal still needs a layout", rv->Animating());
        near("revealed measures its natural width", rv->Measure(e, Constraints{1000, 600}).w, 200);
        eq("settled once the layout used it", !rv->Animating());
    }

    // The same with motion allowed: it should be running, and still settle.
    {
        State<bool> shown(false);
        Desc child = std::make_shared<Box>(Size{200, 50});
        Desc rv = Reveal(shown, child, Transition{Motion::RevealX, 160, Curve::EaseOut});
        lay(rv, e, box);
        shown.set(true);
        rv->Retarget(true);
        eq("an allowed reveal animates", rv->Animating());
    }

    // ---- sized(), and the starvation it exists to prevent --------------------------------------
    {
        Desc list = std::make_shared<Box>(Size{0, 0});
        Desc pane = std::make_shared<Greedy>();
        Desc row = HStack({(list).weight(1), pane}, 0);
        lay(row, e, box);
        // Not a bug in the stack -- an unweighted child gets its natural size, and this child's
        // natural size really is "everything". It is a bug in asking for it. Recorded so the
        // stack's rule is not quietly changed to paper over the call site.
        near("a greedy unweighted child starves the weighted one", list->frame.w, 0);

        Desc list2 = std::make_shared<Box>(Size{0, 0});
        Desc pane2 = sized(std::make_shared<Greedy>(), 260);
        Desc row2 = HStack({(list2).weight(1), pane2}, 0);
        lay(row2, e, box);
        near("sized() leaves the rest to the weighted child", list2->frame.w, 740);
        near("and the sized pane is the width it asked for", pane2->frame.w, 260);
        near("the sized pane keeps its natural height", pane2->frame.h, 600);
    }

    // ---- drag payloads ------------------------------------------------------------------------
    // text/uri-list is line-delimited, so the dangerous character in a filename is a NEWLINE: an
    // unescaped one splits one file into two URIs, and the second is a path nobody chose. The
    // round trip is where that gets caught.
    {
        std::vector<std::string> files;
        files.push_back("/tmp/plain.txt");
        files.push_back("/tmp/with space.png");
        files.push_back("/tmp/a#hash.md");
        files.push_back("/tmp/new\nline.txt");
        const DragData d = DragData::uri_list(files);
        eq("the mime is the one the host speaks", d.mime == "text/uri-list");
        eq("a space is escaped", d.bytes.find("with%20space") != std::string::npos);
        eq("a hash is escaped", d.bytes.find("a%23hash") != std::string::npos);
        eq("a newline is escaped", d.bytes.find("new%0Aline") != std::string::npos);
        eq("lines end CRLF", d.bytes.find("\r\n") != std::string::npos);

        const DragData back = DragData::parse_uri_list(d.bytes);
        eq("every path survives the round trip", back.paths.size() == files.size());
        for (size_t i = 0; i < files.size() && i < back.paths.size(); i++)
            eq("path round trips", back.paths[i] == files[i]);
    }

    // Comments and blank lines are part of text/uri-list and appear in the wild. A file manager
    // that treated "# comment" as a filename would be the one with the bug.
    {
        const DragData d = DragData::parse_uri_list(
            "# a comment\r\n\r\nfile:///tmp/real.txt\r\nhttp://example.com/no.txt\r\n");
        eq("comments, blanks and non-file URIs are dropped", d.paths.size() == 1);
        eq("and the file URI survives", !d.paths.empty() && d.paths[0] == "/tmp/real.txt");
    }

    // ---- a list drags out and takes drops -------------------------------------------------------
    // Row heights come from the loaded font, so nothing here names a pixel row. The test asks
    // which row the widget thinks it was given and holds it to its own answer -- an earlier
    // version hardcoded y = 20 for "row 0", which with a real font is the HEADER, and another
    // passed only because integer truncation turned a negative row index into zero.
    {
        ManyRows src(20);
        State<int> selected(-1);
        Desc list = ListView(&src, selected);
        lay(list, e, Rect{0, 0, 400, 600});
        list->Draw(nullptr, e);

        DragData out;
        // No mapping set: a list is NOT a drag source by default, because a list of search
        // results should not hand out file URIs because it happens to be a list.
        eq("no mapping means no drag", !list->OnDragStart(100, 100, &out));

        int asked = -99;
        OnListDragPaths(list, [&](int row) {
            asked = row;
            return std::vector<std::string>{"/tmp/row" + std::to_string(row)};
        });
        eq("a row drags once the app says what a row is", list->OnDragStart(100, 100, &out));
        eq("it asked about a real row", asked >= 0);
        eq("and carries that row's path",
           out.paths.size() == 1 && out.paths[0] == "/tmp/row" + std::to_string(asked));
        // y at the very top is the column header, which is not a row and must not drag.
        eq("the header is not a row", !list->OnDragStart(100, list->frame.y, &out));

        eq("it does not accept a drop until it can", !list->CanAcceptDrop(out));
        int dropped_on = -2;
        OnListDrop(list, [&](int row, const DragData& d) {
            dropped_on = row;
            return !d.paths.empty();
        });
        eq("now it accepts", list->CanAcceptDrop(out));
        eq("an empty payload is still refused", !list->CanAcceptDrop(DragData{}));

        eq("a drop on a row is taken", list->OnDrop(100, 100, out));
        eq("and names the same row the drag came from", dropped_on == asked);
        // Below the last row is "in here", which is what every file manager means by it.
        eq("a drop past the end is taken", list->OnDrop(100, 595, out));
        eq("and reports no row", dropped_on == -1);
    }

    // ---- the sidebar takes drops ----------------------------------------------------------------
    {
        Fake src;
        State<std::string> sel("");
        Desc t = TreeView(&src, sel);
        lay(t, e, Rect{0, 0, 200, 600});
        TreeRowCount(t);                       // flatten

        const DragData d = DragData::uri_list({"/tmp/a.txt"});
        eq("a tree refuses drops until the app takes them", !t->CanAcceptDrop(d));
        std::string onto;
        OnTreeDrop(t, [&](const std::string& id, const DragData&) {
            onto = id;
            return true;
        });
        eq("now it accepts", t->CanAcceptDrop(d));
        eq("a drop on row 0 is taken", t->OnDrop(100, 1, d));
        eq("and names that row's id", onto == "/a");
    }

    // ---- clicking a column header sorts, and clicking it again reverses ------------------------
    // The widget does not sort; it asks the SOURCE to, because the widget has cells of text and
    // no idea that one column is a byte count shown as "4.0 KB". Sorting the displayed text is
    // how a listing ends up with 10 B after 9 KB. So what is tested here is the ASK.
    {
        ManyRows src(30);
        State<int> selected(-1);
        Desc list = ListView(&src, selected);
        lay(list, e, Rect{0, 0, 400, 600});
        list->Draw(nullptr, e);

        // frame.y + 1 is inside the header whatever the font makes the header height.
        list->OnPressAt(50, list->frame.y + 1, 1);
        eq("a header click sorts by that column", src.sorts == 1 && src.col == 0);
        eq("and ascending first", src.asc);
        list->OnRelease(true);
        eq("a header click does not select a row", selected.get() == -1);

        list->OnPressAt(50, list->frame.y + 1, 1);
        list->OnRelease(true);
        eq("clicking the sorted column again reverses it", src.sorts == 2 && !src.asc);

        // The second column is a different column, and it starts ascending again rather than
        // inheriting the direction of the one before it.
        list->OnPressAt(260, list->frame.y + 1, 1);
        list->OnRelease(true);
        eq("another column sorts by that one", src.col == 1);
        eq("and starts ascending", src.asc);

        // Past the last column is not a column.
        const int before = src.sorts;
        list->OnPressAt(390, list->frame.y + 1, 1);
        list->OnRelease(true);
        eq("past the last column sorts nothing", src.sorts == before);
    }

    // ---- selecting more than one row -----------------------------------------------------------
    // Without a Window there are no live modifiers, so the mouse paths that need Ctrl and Shift
    // are driven through the keyboard, which carries its own mods. What is checked here is the
    // model: what ends up selected, and where the cursor is left.
    {
        ManyRows src(10);
        State<int> selected(-1);
        Desc list = ListView(&src, selected);
        lay(list, e, Rect{0, 0, 400, 600});
        list->Draw(nullptr, e);

        std::vector<int> last;
        int calls = 0;
        OnListSelectionChanged(list, [&](const std::vector<int>& v) { last = v; calls++; });

        au_key down{}; down.pressed = 1; down.raw_char = 0x1f;
        list->OnKey(down);                       // row 0
        eq("one row selected", ListSelection(list).size() == 1);
        eq("and it is the cursor", selected.get() == 0 && ListSelection(list)[0] == 0);
        eq("the app heard about it", calls > 0 && last.size() == 1);

        // Shift+Down sweeps from the anchor rather than moving a single selection.
        au_key sdown = down; sdown.mods = AU_MOD_SHIFT;
        list->OnKey(sdown);
        list->OnKey(sdown);
        const std::vector<int> three = ListSelection(list);
        eq("shift+arrow extends the range", three.size() == 3);
        eq("the range runs from the anchor", !three.empty() && three.front() == 0 && three.back() == 2);
        eq("and the cursor is at the far end", selected.get() == 2);

        // A plain arrow collapses back to one, which is what makes shift feel like a mode you
        // leave rather than one you are stuck in.
        list->OnKey(down);
        eq("a plain arrow collapses the selection", ListSelection(list).size() == 1);
        eq("to where the cursor now is", selected.get() == 3 && ListSelection(list)[0] == 3);

        // Ctrl+A takes everything.
        // A REAL Ctrl+A as present_wl delivers it: raw_char is the layout's letter, utf8 is
        // what the key types with Ctrl applied, which is 0x01. The first version of this test
        // set utf8 to 'a' -- the same wrong assumption the code had -- so it passed while the
        // shortcut did nothing in the running app.
        au_key ctrl_a{}; ctrl_a.pressed = 1; ctrl_a.mods = AU_MOD_CTRL; ctrl_a.raw_char = 'a'; ctrl_a.utf8[0] = 0x01;
        list->OnKey(ctrl_a);
        eq("ctrl+A selects every row", (int)ListSelection(list).size() == src.Count());
    }

    // A multi-row drag carries every selected row, not the one under the pointer. Anything else
    // loses files on the way to a folder.
    {
        ManyRows src(10);
        State<int> selected(-1);
        Desc list = ListView(&src, selected);
        lay(list, e, Rect{0, 0, 400, 600});
        list->Draw(nullptr, e);
        OnListDragPaths(list, [](int row) {
            return std::vector<std::string>{"/tmp/row" + std::to_string(row)};
        });

        au_key down{}; down.pressed = 1; down.raw_char = 0x1f;
        au_key sdown = down; sdown.mods = AU_MOD_SHIFT;
        list->OnKey(down);                       // row 0
        list->OnKey(sdown);                      // rows 0..1
        eq("two rows are selected", ListSelection(list).size() == 2);

        // Start the drag ON a selected row.
        const float y = list->frame.y + 40;
        DragData out;
        eq("the drag starts", list->OnDragStart(100, y, &out));
        eq("and carries both rows", out.paths.size() == 2);
    }

    // ---- a bound label's width is its text -----------------------------------------------------
    // Setting the State invalidated the node's OLD, narrower frame; the window clips to the
    // damage it is given; and the longer string came out cut off. "3 of 4 selected  9.3 KB"
    // rendered as "3 of 4 s", which reads as the program being confused rather than the label
    // being short of room.
    {
        State<std::string> text("short");
        Desc label = Label(text);
        lay(label, e, box);
        eq("settled after layout", !label->Animating());
        const float narrow = label->Measure(e, Constraints{1000, 600}).w;

        text.set("a considerably longer run of text than before");
        eq("new text leaves the layout stale", label->Animating());
        lay(label, e, box);
        eq("settled again", !label->Animating());
        const float wide = label->Measure(e, Constraints{1000, 600}).w;
        eq("and it measured wider", wide > narrow);

        // A label that is not bound to a State never changes under the window's feet, so it must
        // not claim to: answering true forever would lay out every frame.
        Desc fixed = Label(std::string("constant"));
        lay(fixed, e, box);
        eq("a plain label never asks for a relayout", !fixed->Animating());
    }

    // ---- the grid selects like the list ---------------------------------------------------------
    {
        ManyRows src(12);
        State<int> selected(-1);
        Desc grid = IconView(&src, selected);
        lay(grid, e, Rect{0, 0, 400, 600});
        grid->Draw(nullptr, e);

        std::vector<int> last;
        OnIconsSelectionChanged(grid, [&](const std::vector<int>& v) { last = v; });
        OnIconsDragPaths(grid, [](int row) {
            return std::vector<std::string>{"/tmp/tile" + std::to_string(row)};
        });

        au_key right{}; right.pressed = 1; right.raw_char = 0x1d;
        grid->OnKey(right);
        eq("one tile selected", IconsSelection(grid).size() == 1);

        au_key sright = right; sright.mods = AU_MOD_SHIFT;
        grid->OnKey(sright);
        grid->OnKey(sright);
        const std::vector<int> three = IconsSelection(grid);
        eq("shift+arrow extends across the grid", three.size() == 3);
        eq("as a run of indices, not a rectangle",
           three.size() == 3 && three[1] == three[0] + 1 && three[2] == three[1] + 1);
        eq("and the app heard", last.size() == 3);

        // A REAL Ctrl+A as present_wl delivers it: raw_char is the layout's letter, utf8 is
        // what the key types with Ctrl applied, which is 0x01. The first version of this test
        // set utf8 to 'a' -- the same wrong assumption the code had -- so it passed while the
        // shortcut did nothing in the running app.
        au_key ctrl_a{}; ctrl_a.pressed = 1; ctrl_a.mods = AU_MOD_CTRL; ctrl_a.raw_char = 'a'; ctrl_a.utf8[0] = 0x01;
        grid->OnKey(ctrl_a);
        eq("ctrl+A takes the whole grid", (int)IconsSelection(grid).size() == src.Count());

        // A drag from inside the selection carries all of it. The press puts the cursor on the
        // tile the drag will start from, which is the point -- dragging a tile OUTSIDE the
        // selection must carry only that one.
        grid->OnPressAt(10, 10, 1);               // tile 0, no modifier: just it
        eq("the press selected one tile", IconsSelection(grid).size() == 1);
        grid->OnKey(sright);                      // tiles 0 and 1
        eq("two tiles selected", IconsSelection(grid).size() == 2);
        DragData out;
        eq("the grid drags", grid->OnDragStart(10, 10, &out));
        eq("and carries both", out.paths.size() == 2);
    }

    // ---- the small controls ---------------------------------------------------------------------
    {
        State<bool> on(false);
        int changes = 0;
        Desc cb = Checkbox("Show hidden files", on, [&](bool) { changes++; });
        lay(cb, e, Rect{0, 0, 300, 40});

        // A click toggles. Press-then-release, because a press alone must not commit: dragging
        // off a checkbox you did not mean to hit is how people cancel.
        cb->OnPressAt(10, 20, 1);
        cb->OnRelease(true);
        eq("a click turns it on", on.get() && changes == 1);
        cb->OnPressAt(10, 20, 1);
        cb->OnRelease(false);
        eq("releasing outside does not toggle", on.get() && changes == 1);

        // Space toggles; Enter does NOT, because Enter means "accept the dialog" and a checkbox
        // that ate it would make a form unsubmittable from the keyboard.
        au_key space{}; space.pressed = 1; space.utf8[0] = ' ';
        eq("space is handled", cb->OnKey(space));
        eq("and toggles", !on.get());
        au_key enter{}; enter.pressed = 1; enter.raw_char = 0x0a;
        eq("enter is left alone", !cb->OnKey(enter));
        eq("and changed nothing", !on.get());
        eq("a checkbox takes focus", cb->CanFocus());
    }

    {
        State<float> v(0.5f);
        int changes = 0;
        Desc sl = Slider(v, 0.0f, 1.0f, [&](float) { changes++; });
        lay(sl, e, Rect{0, 0, 200, 40});
        sl->Draw(nullptr, e);

        // Dragging past either end clamps rather than running off.
        sl->OnPressAt(-500, 20, 1);
        near("dragging left of the track clamps to the low end", v.get(), 0.0f);
        sl->OnDragTo(5000, 20);
        near("and right of it to the high end", v.get(), 1.0f);

        // Arrows move a twentieth of the range: a 0-to-1 slider crossed in 20 presses, not 100.
        au_key left{}; left.pressed = 1; left.raw_char = 0x1c;
        sl->OnKey(left);
        near("left steps down a twentieth", v.get(), 0.95f);
        au_key home{}; home.pressed = 1; home.raw_char = 0x01;
        sl->OnKey(home);
        near("home goes to the low end", v.get(), 0.0f);
        sl->OnKey(left);
        near("and stops there", v.get(), 0.0f);
        eq("the app heard every real change", changes > 0);
    }

    {
        // A progress bar reports; it is not a control. A Tab stop that does nothing is a dead
        // stop in a keyboard user's path, and a click on it should fall through.
        State<float> p(0.3f);
        Desc pr = Progress(p);
        lay(pr, e, Rect{0, 0, 200, 20});
        eq("progress takes no focus", !pr->CanFocus());
        eq("and no clicks", pr->HitTest(10, 10) == nullptr);
    }

    {
        // A radio group is exclusive: choosing one is choosing it, not adding it.
        State<int> pick(0);
        int last = -1;
        Desc rg = RadioGroup({"Name", "Size", "Date"}, pick, [&](int i) { last = i; });
        lay(rg, e, Rect{0, 0, 300, 200});
        std::vector<Node*> kids;
        rg->Children(&kids);
        eq("one option per label", kids.size() == 3);

        kids[2]->OnPressAt(5, kids[2]->frame.y + 2, 1);
        kids[2]->OnRelease(true);
        eq("choosing the third selects it", pick.get() == 2 && last == 2);
    }

    // ---- rubber band ------------------------------------------------------------------------
    // The gesture people reach for in a grid before they reach for Ctrl-click.
    {
        ManyRows src(12);
        State<int> selected(-1);
        Desc grid = IconView(&src, selected);
        lay(grid, e, Rect{0, 0, 400, 600});
        grid->Draw(nullptr, e);
        std::vector<int> last;
        OnIconsSelectionChanged(grid, [&](const std::vector<int>& v) { last = v; });

        // Start on empty space well below the tiles, so no tile is pressed.
        grid->OnPressAt(10, 560, 1);
        eq("a press on empty space selects nothing", IconsSelection(grid).empty());

        // Drag up and across the whole grid: everything it grazes comes with it.
        grid->OnDragTo(390, 10);
        const size_t many = IconsSelection(grid).size();
        eq("a band over the grid selects tiles", many > 1);
        eq("and the app heard", last.size() == many);

        // Shrinking it deselects again. Rebuilt from the base each time rather than
        // accumulated, or an overshoot would be unfixable without starting over.
        grid->OnDragTo(10, 555);
        eq("shrinking the band gives them back", IconsSelection(grid).size() < many);

        grid->OnRelease(true);
        grid->OnDragTo(390, 10);
        eq("and the band is over once released", IconsSelection(grid).size() < many);
    }

    // ---- a button that can be switched off ------------------------------------------------------
    // A picker's Grant is the one control whose being disabled is the whole safety property.
    {
        int fired = 0;
        State<bool> on(false);
        Desc b = Button("Grant", [&] { fired++; }, on);

        b->OnPress();
        b->OnRelease(true);
        eq("a disabled button ignores a click", fired == 0);
        au_key space = pressing(' ');
        space.utf8[0] = ' ';
        eq("a disabled button declines Space", !b->OnKey(space) && fired == 0);
        au_key enter = pressing('\r');
        enter.utf8[0] = '\r';
        eq("a disabled button declines Enter", !b->OnKey(enter) && fired == 0);

        on.set(true);
        b->OnPress();
        b->OnRelease(true);
        eq("the same button fires once enabled", fired == 1);
        eq("and takes Space once enabled", b->OnKey(space) && fired == 2);

        // The two-argument form is unchanged: no State means always on.
        int plain = 0;
        Desc p = Button("Go", [&] { plain++; });
        p->OnPress();
        p->OnRelease(true);
        eq("a button with no enabled State is always on", plain == 1);
    }

    // ---- a single-select list stays single ------------------------------------------------------
    // The plain list multi-selects from the keyboard; the same keys on a single-select one must
    // leave exactly one row, or what is highlighted would not be what a picker grants.
    {
        ManyRows src(10);
        State<int> multi_sel(-1), single_sel(-1);
        Desc multi = ListView(&src, multi_sel);
        Desc single = ListView(&src, single_sel);
        // Row icons are opt-in and are drawn, not laid out, so what a headless test can check
        // is that turning them on changes no geometry and that the draw path survives a null
        // surface the way every other node here does. tk_layout calls Draw(nullptr), so a
        // widget that dereferenced the surface would take the whole suite down.
        {
            State<int> icon_sel(-1);
            Desc icony = ListView(&src, icon_sel);
            const Size before = icony->Measure(e, Constraints{400, 600});
            ListIcons(icony);
            const Size after = icony->Measure(e, Constraints{400, 600});
            eq("ListIcons changes no geometry", before.w == after.w && before.h == after.h);
            lay(icony, e, Rect{0, 0, 400, 600});
            icony->Draw(nullptr, e);          // must not dereference the surface
            ListIcons(icony, false);
            lay(icony, e, Rect{0, 0, 400, 600});
            icony->Draw(nullptr, e);
        }

        ListSingleSelect(single);
        for (Desc l : {multi, single}) {
            lay(l, e, Rect{0, 0, 400, 600});
            l->Draw(nullptr, e);
        }

        au_key shift_down = pressing(0x1f);
        shift_down.mods = AU_MOD_SHIFT;
        au_key ctrl_a = pressing('a');
        ctrl_a.mods = AU_MOD_CTRL;

        // A plain Down first: it puts the cursor and the anchor on row 0, the way a click would.
        multi->OnKey(pressing(0x1f));
        single->OnKey(pressing(0x1f));
        multi->OnKey(shift_down);
        multi->OnKey(shift_down);
        eq("a plain list extends with Shift+Down", ListSelection(multi).size() == 3);
        multi->OnKey(ctrl_a);
        eq("a plain list takes everything on Ctrl+A", ListSelection(multi).size() == 10);

        single->OnKey(shift_down);
        single->OnKey(shift_down);
        eq("a single-select list moves on Shift+Down", single_sel.get() == 2);
        eq("and keeps one row selected", ListSelection(single) == std::vector<int>{2});
        single->OnKey(ctrl_a);
        eq("Ctrl+A selects nothing more", ListSelection(single) == std::vector<int>{2});
    }

    // ---- the app clears the selection by writing -1 ---------------------------------------------
    // Atlas does it on every directory it opens, and the picker does it on every folder. The rows that were
    // highlighted must not stay highlighted in a listing they were never chosen from.
    {
        ManyRows src(10);
        State<int> selected(-1);
        Desc list = ListView(&src, selected);
        lay(list, e, Rect{0, 0, 400, 600});
        list->Draw(nullptr, e);
        list->OnKey(pressing(0x1f));
        list->OnKey(pressing(0x1f));
        eq("a row is selected", selected.get() == 1 && ListSelection(list) == std::vector<int>{1});

        selected.set(-1);                       // the app opens another directory
        list->Draw(nullptr, e);
        eq("the highlight goes with it", ListSelection(list).empty());
        list->OnKey(pressing(0x1f));
        eq("and the next selection starts clean", selected.get() == 0 && ListSelection(list) == std::vector<int>{0});
    }

    if (fails == 0) printf("tk_layout: ok\n");
    return fails ? 1 : 0;
}
