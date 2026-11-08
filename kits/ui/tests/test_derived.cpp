// 8.2e: Derived<T> — declared sources, declaration order is topological order, lazy recompute,
// and it reads as a read-only Binding so it can feed Either/Reveal/a label.
#include "ui.h"

#include <cmath>
#include <cstdio>
#include <cstdlib>

using namespace aurora::ui;

static int fails = 0;
static void check(bool ok, const char* what) {
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

int main() {
    setenv("WAYLAND_DISPLAY", "aurora-derived-headless", 1);

    State<int> items(0);
    State<float> tax(0.0825f);

    // subtotal is declared before total, and total names subtotal (a Derived) and tax. Declaration
    // order is topological order: one read of total resolves subtotal first, then total.
    Derived<float> subtotal{[](int n) { return n * 9.99f; }, items};
    Derived<float> total{[](float s, float t) { return s * (1 + t); }, subtotal, tax};

    check(total.get() == 0.0f, "empty cart totals zero");

    items.set(3);
    const float once = total.get();
    check(std::fabs(once - 3 * 9.99f * 1.0825f) < 1e-3f, "derived-of-derived resolves in one read");

    tax.set(0.0f);
    check(std::fabs(total.get() - 3 * 9.99f) < 1e-3f, "a second source dirties the total");

    // reads as a Binding: a bool derived can feed a structure condition
    Derived<bool> many{[](int n) { return n > 2; }, items};
    Binding<bool> b = many;
    check(b.get() == true, "a derived feeds a Binding");
    check(!b.writable(), "a derived Binding is read-only");
    b.set(false);                                     // no-op: the graph owns a derived
    check(many.get() == true, "set() on a derived Binding does nothing");

    // lazy: two reads without a change recompute once
    {
        int computes = 0;
        State<int> s(1);
        Derived<int> d{[&](int v) { ++computes; return v * 2; }, s};
        check(d.get() == 2, "value");
        check(d.get() == 2, "value again");
        check(computes == 1, "recompute is lazy: one compute for two reads");
        s.set(5);
        check(d.get() == 10, "stale after set");
        check(computes == 2, "exactly one more compute after a change");
    }

    std::printf(fails ? "derived: %d FAILED\n" : "derived: ok (%d checks)\n", fails ? fails : 8);
    return fails ? 1 : 0;
}