// 8.2d: Desc is a class. Modifiers mutate the node and return the SAME handle (Decision 4:
// shared_ptr semantics), so a chain sets fields on one node rather than rebuilding it.
#include "ui.h"

#include <cstdio>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

int main() {
    Desc d = Label("x");
    check(d != nullptr, "a factory returns a live Desc");

    check(d.name("hello").get() == d.get(), "name() returns the same handle, not a copy of the node");
    check(d->name == "hello", "name() set the field on the node");

    check(d.weight(1.5f).get() == d.get() && d->weight == 1.5f, "weight() mutates and returns the handle");
    check(d.padding(0.5f).get() == d.get(), "padding() returns the handle");

    // A chain is one node: every modifier lands on the same Desc.
    Desc chained = Label("y").name("n").weight(2.0f).padding(0.25f);
    check(chained != nullptr && chained->name == "n" && chained->weight == 2.0f,
          "a modifier chain lands on one node");

    // The free spellings were removed once every call site was a method: one way to say it now.
    check(Desc(Label("z")).name("f") != nullptr, "factory handles are Descs");

    std::printf(fails ? "desc: %d of %d FAILED\n" : "desc: ok (%d checks)\n", fails ? fails : checks);
    return fails ? 1 : 0;
}
