// 8.7, component 2: the command bar is 44 px of chrome with one flexible slot, and it says so.
// The checks are about the arrangement and the paint -- the two things the bar decides itself --
// and not about the buttons, which the toolkit's own test already covers.
#include "components/toolbar.h"

#include <cstdio>
#include <memory>
#include <string>
#include <vector>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

// A stand-in for the breadcrumb: a fixed natural size, so the arithmetic below is about the BAR's
// padding and gaps and not about a font. A Label measures through font metrics that do not exist
// without a display, and a test whose expected widths depend on a font is a test that fails on
// another machine for the wrong reason.
class Fixed : public Node {
public:
    Fixed(float w, float h) : w_(w), h_(h) {}
    Size Measure(const Env&, Constraints) override { return Size{w_, h_}; }
    void Draw(au_surface*, const Env&) override {}

private:
    float w_, h_;
};

static Node* find(Node* n, const std::string& name) {
    if (!n) return nullptr;
    if (n->name == name) return n;
    std::vector<Node*> kids;
    n->Children(&kids);
    for (Node* k : kids) if (Node* hit = find(k, name)) return hit;
    return nullptr;
}

static bool pixel_is(const au_surface* s, int x, int y, au_color c) {
    const uint32_t* p = au_surface_pixels(s);
    const int w = au_surface_px_w(s);
    if (!p || x < 0 || y < 0 || x >= w || y >= au_surface_px_h(s)) return false;
    const uint32_t v = p[y * w + x];
    return ((v >> 16) & 0xFF) == c.r && ((v >> 8) & 0xFF) == c.g && (v & 0xFF) == c.b;
}

int main() {
    const Env e = Env();

    int back_runs = 0;
    Desc back = ToolbarButton(Glyph::Back, "back", [&] { ++back_runs; });
    check(std::string(back->Role()) == "button", "a command is a button");
    check(back->name == "back", "a command can be addressed by name");
    check(back->Measure(e, Constraints{}).w == 28 && back->Measure(e, Constraints{}).h == 28,
          "a command is the toolbar's 28x28 button");

    // Enabled is a cell, and it is the button's own: off, the press does nothing and the button
    // is still there. A bar that DROPPED a disabled command would be a bar whose shape changed
    // with the selection, which is the wrong model for a command that is merely unavailable.
    State<bool> can_go(true);
    int nav_runs = 0;
    Desc fwd = ToolbarButton(Glyph::Forward, "forward", [&] { ++nav_runs; }, can_go);
    fwd->OnPress();
    fwd->OnRelease(true);
    check(nav_runs == 1, "an enabled command runs on release inside");
    can_go.set(false);
    fwd->OnPress();
    fwd->OnRelease(true);
    check(nav_runs == 1, "a disabled command does not run");

    // The full bar of 6.9's shape: sidebar + navigation | location | filter + view + preview.
    auto loc = std::make_shared<Fixed>(20, 28);
    Desc location(loc);
    location.name("location");

    std::vector<ToolbarGroup> leading = {
        {ToolbarButton(Glyph::Sidebar, "sidebar", [&] {}),
         ToolbarButton(Glyph::Back, "b", [&] {})},
        {ToolbarButton(Glyph::Forward, "f", [&] {}),
         ToolbarButton(Glyph::Up, "up", [&] {})},
    };
    std::vector<ToolbarGroup> trailing = {
        {ToolbarButton(Glyph::Filter, "filter", [&] {}),
         MenuButton(Glyph::View, [] { return std::vector<MenuItem>{}; })},
        {ToolbarButton(Glyph::Preview, "preview", [&] {})},
    };

    Desc bar = Toolbar(leading, location, trailing);

    check(std::string(bar->Role()) == "toolbar", "the component declares its own role");
    check(std::string(bar->Role()) != "control", "a role better than control (8.7's check)");

    const Size nat = bar->Measure(e, Constraints{400, 600});
    check(nat.h == 44, "the bar is 44 tall whatever it is offered");
    check(nat.w == 400, "the bar is as wide as the offer");

    bar->Place(Rect{0, 0, 400, 44});

    Node* lb = find(bar.get(), "sidebar");
    Node* lo = find(bar.get(), "location");
    Node* pv = find(bar.get(), "preview");
    check(lb && lo && pv, "every named control is reachable from the bar");
    if (lb && lo && pv) {
        // 8 px end, two groups of two 28 px buttons at 4 px, 12 px between groups.
        check(lb->frame.x == 8, "the bar's left end is one unit of padding");
        check(lb->frame.w == 28 && lb->frame.h == 28, "a command keeps its own 28x28 box");
        // 384 inside - 60 - 60 - 60 - 28 for the four groups (the view button is 38: 28 plus its
        // 10 px chevron, 6.9) - four 12 px gaps = 118 for the slot, far more than the 20 it
        // measured, so weight and not width is what sizes it.
        check(lo->frame.w == 118, "the slot takes the width the clusters leave");
        check(lo->frame.x == 152, "the slot starts after the leading clusters");
        // 8 + 384 = 392: the trailing cluster ends at the bar's right padding, and the slot is
        // what makes that true rather than a hand-tuned width.
        check(pv->frame.x + pv->frame.w == 392, "the trailing cluster ends at the right padding");
        // 44 tall, 28 of content and 8 of padding: the bar's buttons are vertically centred by
        // the same unit the ends are padded with, which is why 28 + 8 + 8 is exactly 44.
        check(pv->frame.y == 8 && pv->frame.h == 28, "a command sits inside the bar's height");
    }

    check(DuplicateNames(bar).empty(), "no two commands share a name");

    // No location: the trailing cluster still ends at the right padding, because a Spacer holds
    // the slot. Without one the commands would bunch up at the left and the bar would lose its
    // right-hand cluster to the breadcrumb's absence.
    Desc bare = Toolbar({{ToolbarButton(Glyph::Back, "only", [&] {})}}, Desc(),
                        {{ToolbarButton(Glyph::Close, "close", [&] {})}});
    bare->Measure(e, Constraints{400, 44});
    bare->Place(Rect{0, 0, 400, 44});
    Node* cl = find(bare.get(), "close");
    check(cl && cl->frame.x + cl->frame.w == 392, "a command-only bar keeps its right cluster right");

    // An empty group is not a gap. A caller that omits the preview cluster must not leave a
    // stranded 12 px break where it would have been, so a bar with a dropped group measures
    // exactly as the bar without it.
    Desc dense = Toolbar({{ToolbarButton(Glyph::Back, "d1", [&] {})}},
                         Desc(std::make_shared<Fixed>(20, 28)), {});
    Desc sparse = Toolbar({{}, {ToolbarButton(Glyph::Back, "s1", [&] {})}},
                          Desc(std::make_shared<Fixed>(20, 28)), {});
    check(sparse->Measure(e, Constraints{}).w == dense->Measure(e, Constraints{}).w,
          "an empty group contributes no width and no gap");

    // A tall control inside the bar cannot make the bar grow: the height is the bar's, not the
    // content's, or the whole layout below it would shift when one control changed.
    Desc tall = Toolbar({{ToolbarButton(Glyph::Back, "t1", [&] {})}}, Desc(),
                        {{std::make_shared<Fixed>(20, 80)}});
    check(tall->Measure(e, Constraints{300, 600}).h == 44, "a tall child does not grow the bar");

    // ---- the paint -------------------------------------------------------------------------
    au_surface* s = au_surface_new(400, 60, 1.0);
    check(s != nullptr, "an offscreen surface for the drawing claims");
    if (s) {
        const au_color sentinel{10, 10, 10, 255};
        au_clear(s, sentinel);
        bar->Draw(s, e);

        check(pixel_is(s, 2, 2, e.surface), "the bar paints the chrome colour");
        check(pixel_is(s, 200, 2, e.surface), "the chrome runs the full width");
        check(pixel_is(s, 2, 42, e.surface), "the row above the rule is chrome");
        check(pixel_is(s, 2, 43, e.hairline), "the 1 px rule under the bar is the hairline colour");
        check(pixel_is(s, 2, 44, sentinel), "the bar paints nothing below its own frame");

        ToolbarOpts no_rule;
        no_rule.hairline = false;
        Desc plain = Toolbar({{ToolbarButton(Glyph::Back, "p1", [&] {})}}, Desc(), {}, no_rule);
        plain->Measure(e, Constraints{400, 44});
        plain->Place(Rect{0, 0, 400, 44});
        au_clear(s, sentinel);
        plain->Draw(s, e);
        check(pixel_is(s, 2, 43, e.surface), "a bar with no rule is chrome to its last row");
        au_surface_free(s);
    }

    if (fails) std::printf("toolbar: %d of %d FAILED\n", fails, checks);
    else std::printf("toolbar: ok (%d checks)\n", checks);
    return fails ? 1 : 0;
}
