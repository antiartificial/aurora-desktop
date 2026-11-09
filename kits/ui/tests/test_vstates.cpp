// 8.5: six states, one table. A state that renders like another is a correctness bug here, so the
// test checks the distinctions the design names rather than that the function returns something.
#include "ui.h"

#include <cstdio>
#include <set>
#include <string>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}
static bool same(const au_color& a, const au_color& b) {
    return a.r == b.r && a.g == b.g && a.b == b.b && a.a == b.a;
}

int main() {
    const Env e = Env();

    std::set<std::string> names;
    for (int i = 0; i < kVStateCount; i++) names.insert(VStateName((VState)i));
    check(names.size() == 6, "six distinct state names");
    check(std::string(VStateName(VState::SelectedFocused)) == "selected+focused",
          "the focused selection has its own name");

    const ControlSkin normal = ResolveSkin(e, VState::Normal);
    const ControlSkin hover = ResolveSkin(e, VState::Hover);
    const ControlSkin focus = ResolveSkin(e, VState::Focused);
    const ControlSkin sel = ResolveSkin(e, VState::Selected);
    const ControlSkin self = ResolveSkin(e, VState::SelectedFocused);
    const ControlSkin dis = ResolveSkin(e, VState::Disabled);

    check(!same(normal.fill, hover.fill), "hover has a fill normal does not");
    check(!normal.ring_visible && !hover.ring_visible, "no ring when not focused");

    check(focus.ring_visible && !focus.selected, "focused alone is a ring, not a selection");
    check(sel.selected && !sel.ring_visible, "selected and unfocused has no ring");
    check(self.selected && self.ring_visible, "selected and focused has both");

    check(!same(sel.fill, self.fill) && !same(sel.ink, self.ink),
          "selected+unfocused and selected+focused are different pictures");
    check(!same(sel.fill, focus.fill), "cursor-only differs from selected");
    check(same(self.fill, e.accent) && same(self.ink, e.accent_ink),
          "the focused selection is the accent pair");

    check(same(dis.ink, e.dim3), "disabled ink is dim3");
    check(!dis.ring_visible && !dis.selected, "disabled is neither ringed nor selected");

    std::printf(fails ? "vstates: %d of %d FAILED\n" : "vstates: ok (%d checks)\n",
                fails ? fails : checks);
    return fails ? 1 : 0;
}
