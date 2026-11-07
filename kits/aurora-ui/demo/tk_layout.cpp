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

    if (fails == 0) printf("tk_layout: ok\n");
    return fails ? 1 : 0;
}
