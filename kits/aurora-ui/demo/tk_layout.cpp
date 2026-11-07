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
    int Columns() const override { return 1; }
    float ColumnWidth(int) const override { return 380; }
    std::string ColumnTitle(int) const override { return "Name"; }
};

int main() {
    Env e;              // unit 8, no fonts needed by anything here
    const Rect box{0, 0, 1000, 600};

    // ---- HSplit divides the width it is given, at the ratio -----------------------------------
    {
        State<float> r(0.5f);
        Desc a = Spacer(), b = Spacer();
        Desc s = HSplit(a, b, &r);
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
        Desc s = HSplit(a, b, &r, SplitOpts{10, 10, 0.5f, 3});   // 80 px each side
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
        Desc s = HSplit(a, b, &r, SplitOpts{10, 10, 0.5f, 3});
        lay(s, e, Rect{0, 0, 60, 600});
        eq("a.w not negative when cramped", a->frame.w >= 0);
        eq("b.w not negative when cramped", b->frame.w >= 0);
    }

    // ---- VSplit is the same on the other axis -------------------------------------------------
    {
        State<float> r(0.5f);
        Desc a = Spacer(), b = Spacer();
        Desc s = VSplit(a, b, &r);
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
        Desc s = HSplit(a, b, &r, SplitOpts{10, 10, 0.5f, 3});
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
        Desc s = HSplit(a, b, &r);
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
        Desc v = VStack({weight(a, 1), weight(b, 3)}, 0);
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
        Desc t = TreeView(&src, &sel);
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
        Desc t = TreeView(&src, &sel);
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
        Desc t = TreeView(&src, &sel);
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
        Desc t = TreeView(&src, &sel);
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
        int selected = -1;
        Desc list = ListView(&src, &selected);
        lay(list, e, Rect{0, 0, 400, 600});
        list->Draw(nullptr, e);              // the bar is laid out during Draw

        // x = 395 is inside the bar (9 px wide, at the right edge of a 400 px frame).
        list->OnPressAt(395, 300, 1);
        list->OnRelease(true);
        eq("pressing the scrollbar selects nothing", selected == -1);

        // The same press away from the bar does select.
        list->OnPressAt(100, 300, 1);
        list->OnRelease(true);
        eq("pressing a row does select", selected >= 0);
    }

    // ---- the bar appears only when there is somewhere to go -----------------------------------
    // A track with a full-length thumb is furniture that says nothing.
    {
        ManyRows few(3);
        int selected = -1;
        Desc list = ListView(&few, &selected);
        lay(list, e, Rect{0, 0, 400, 600});
        list->Draw(nullptr, e);
        // Near the top, because with three rows y = 300 is past the last one -- which would
        // make this pass for the wrong reason.
        list->OnPressAt(395, 12, 1);
        list->OnRelease(true);
        eq("with nothing to scroll the press reaches the rows", selected >= 0);
    }

    if (fails == 0) printf("tk_layout: ok\n");
    return fails ? 1 : 0;
}
