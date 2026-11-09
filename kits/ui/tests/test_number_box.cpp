// 8.7, component 2: NumberBox clamps, steps and refuses what is not a number.
//
// Headless: the range rule is a free function and the widget's keys and steppers are driven
// directly, so the claims are numbers rather than a description of a screenshot.
#include "components/number_box.h"

#include <cmath>
#include <cstdio>
#include <string>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

static bool near(double a, double b) { return std::fabs(a - b) < 1e-9; }

static void press(Node* n, unsigned raw) {
    au_key k{};
    k.pressed = 1;
    k.raw_char = raw;
    n->OnKey(k);
}

static void type(Node* n, char c) {
    au_key k{};
    k.pressed = 1;
    k.utf8[0] = c;
    k.utf8[1] = '\0';
    n->OnKey(k);
}

int main() {
    // ---- the range rule, numerically ----
    check(near(ClampNumber(15, 0, 10), 10), "above the range clamps to max");
    check(near(ClampNumber(-3, 0, 10), 0), "below the range clamps to min");
    check(near(ClampNumber(5, 0, 10), 5), "inside the range is untouched");
    check(near(ClampNumber(5, 10, 0), 5), "reversed bounds still read as a range");
    check(near(ClampNumber(std::nan(""), 0, 10), 0), "NaN is refused, resolving to min");
    check(near(ClampNumber(INFINITY, 2, 10), 2), "Inf is refused, resolving to min");

    check(near(StepNumber(10, 1, 0, 10, +1), 10), "stepping up from max stays at max");
    check(near(StepNumber(0, 1, 0, 10, -1), 0), "stepping down from min stays at min");
    check(near(StepNumber(2.5, 0.5, 0, 10, +1), 3.0), "a step is exactly the step size");
    check(near(StepNumber(2.5, 0.5, 0, 10, -1), 2.0), "a downward step subtracts");
    check(near(StepNumber(1, 0, 0, 10, +1), 1), "a zero step does not move");
    check(near(StepNumber(1, -2, 0, 10, +1), 1), "a negative step does not move");
    check(near(StepNumber(1, 1, 0, 10, 0), 1), "a zero direction does not move");
    check(near(StepNumber(std::nan(""), 1, 0, 10, +1), 0), "NaN cannot be stepped from");
    check(near(StepNumber(9, 4, 0, 10, +1), 10), "a step that overshoots lands on max");
    check(near(StepNumber(9, 2, 0, 10, -3), 3), "the direction carries a count of steps");

    // ---- the widget ----
    Env e;
    float sys = 0;
    if (!au_font_system("plain", &e.font, &sys)) e.font = au_font{};
    e.size = 13;

    State<double> v(5.0);
    int calls = 0;
    double last = 0;
    Desc nb = NumberBox(v, 0.0, 10.0, 2.0, [&](double n) { ++calls; last = n; }).name("count");

    check(std::string(nb->Role()) == "spinbutton", "the component declares its own role");
    check(std::string(nb->Role()) != "control", "a role better than control (8.7's check)");

    const Size natural = nb->Measure(e, Constraints{});
    check(natural.w > 0 && natural.h > 0, "a natural size is reported");
    const Size capped = nb->Measure(e, Constraints{60, 40});
    check(capped.w <= 60, "a narrow constraint is respected");
    nb->Place(Rect{0, 0, 140, std::max(24.0f, natural.h)});

    press(nb.get(), 0x1e);                       // Up
    check(near(v.get(), 7), "Up steps by the step");
    check(calls == 1 && near(last, 7), "a changed value fires the callback once");
    press(nb.get(), 0x1e);
    press(nb.get(), 0x1e);
    check(near(v.get(), 10), "stepping stops at max");
    press(nb.get(), 0x1e);
    check(near(v.get(), 10) && calls == 3, "a step at max changes nothing and does not fire");

    press(nb.get(), 0x1f);                       // Down
    check(near(v.get(), 8), "Down steps back down");
    press(nb.get(), 0x0c);                       // PageDown
    check(near(v.get(), 0), "PageDown moves ten steps and clamps");
    press(nb.get(), 0x01);                       // Home
    check(near(v.get(), 0), "Home goes to min");
    press(nb.get(), 0x04);                       // End
    check(near(v.get(), 10), "End goes to max");

    // ---- the steppers, by hit region ----
    nb->OnPressAt(5, 10, 1);
    nb->OnRelease(true);
    check(near(v.get(), 8), "the left stepper subtracts a step");
    nb->OnPressAt(135, 10, 1);
    nb->OnRelease(true);
    check(near(v.get(), 10), "the right stepper adds a step");

    // ---- typing ----
    type(nb.get(), '4');
    press(nb.get(), 0x0a);                       // Enter commits
    check(near(v.get(), 4), "a typed number commits on Enter");

    type(nb.get(), '9');
    type(nb.get(), '9');
    press(nb.get(), 0x0a);
    check(near(v.get(), 10), "typed text is clamped like any other input");

    type(nb.get(), '-');
    press(nb.get(), 0x0a);
    check(near(v.get(), 10), "a bare sign is refused and the value is kept");

    type(nb.get(), '2');
    press(nb.get(), 0x1b);                       // Escape
    check(near(v.get(), 10), "Escape discards what was typed");

    if (fails) std::printf("number_box: %d of %d FAILED\n", fails, checks);
    else std::printf("number_box: ok (%d checks)\n", checks);
    return fails ? 1 : 0;
}
