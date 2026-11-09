// 8.7, component 2: a TeachingTip is a note that explains once and then leaves at the caller's
// command -- it owns no "seen" flag, so the test checks both sides of that bargain.
#include "components/teaching_tip.h"

#include <cstdio>
#include <string>
#include <vector>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

int main() {
    Env env;
    int dismissals = 0;
    State<bool> show(true);
    Desc tip = TeachingTip("Search as you type",
                           "Start typing and the list narrows. Escape clears the filter.",
                           show, [&] { ++dismissals; show.set(false); });

    check(std::string(tip->Role()) == "note", "the component declares its own role");
    check(std::string(tip->Role()) != "control", "a role better than control (8.7's check)");

    std::vector<Node*> kids;
    tip->Children(&kids);
    check(kids.size() == 1, "shown, the note offers exactly one action");
    if (kids.size() == 1) check(std::string(kids[0]->Role()) == "button", "that action is a button");

    const Size wide = tip->Measure(env, Constraints{400, -1});
    check(wide.w > 0 && wide.h > 0, "a shown note has a size");
    check(wide.w <= 400, "it respects the width it is offered");
    check(wide.w < 400, "it keeps a reading measure instead of filling the offer");

    const Size narrow = tip->Measure(env, Constraints{120, -1});
    check(narrow.w <= 120, "a narrow offer narrows the note");
    check(narrow.w < wide.w, "and it is genuinely narrower than the preferred measure");

    // The state change is a LAYOUT change: hiding must not just repaint an unchanged box.
    show.set(false);
    check(tip->Animating(), "hiding asks for a layout, not only a repaint");
    const Size gone = tip->Measure(env, Constraints{400, -1});
    check(gone.w == 0 && gone.h == 0, "hidden, the note takes no room");
    kids.clear();
    tip->Children(&kids);
    check(kids.empty(), "hidden, its button is not in the focus order");
    check(tip->HitTest(0, 0) == nullptr, "hidden, it takes no clicks");

    au_key esc{};
    esc.pressed = true;
    esc.raw_char = 0x1b;
    check(!tip->OnKey(esc), "hidden, Escape is not its business");

    // Show it again; the caller's cell is the only thing deciding that.
    show.set(true);
    tip->Measure(env, Constraints{400, -1});
    check(!tip->Animating(), "the first layout after showing settles the node");
    kids.clear();
    tip->Children(&kids);
    const bool has_btn = kids.size() == 1;
    check(has_btn, "shown again, the action is back");
    if (has_btn) {
        kids[0]->OnPress();
        kids[0]->OnRelease(true);
        check(dismissals == 1, "the button reports the one dismissal");
        check(!show.get(), "and the caller's cell, not the widget, closes the note");
    }

    // Escape is the keyboard's route to the same action, and it bubbles from the button.
    show.set(true);
    check(tip->OnKey(esc), "Escape is handled while the note is shown");
    check(dismissals == 2, "Escape is the same dismissal, not a separate one");
    au_key letter{};
    letter.pressed = true;
    letter.raw_char = 'a';
    check(!tip->OnKey(letter), "other keys are left to the focused widget");

    // A missing callback is tolerated: the note stays, which is the caller's statement that it
    // has nowhere to go yet.
    State<bool> always(true);
    Desc quiet = TeachingTip("Preview", "Select a file to preview it.", always, nullptr);
    quiet->Measure(env, Constraints{400, -1});
    std::vector<Node*> qkids;
    quiet->Children(&qkids);
    const bool quiet_btn = qkids.size() == 1;
    check(quiet_btn, "a note without a callback still shows its action");
    if (quiet_btn) {
        qkids[0]->OnPress();
        qkids[0]->OnRelease(true);
        check(always.get(), "and pressing it changes nothing by itself");
    }

    if (fails) std::printf("teaching_tip: %d of %d FAILED\n", fails, checks);
    else std::printf("teaching_tip: ok (%d checks)\n", checks);
    return fails ? 1 : 0;
}
