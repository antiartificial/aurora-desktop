// 8.6: the naming rule. A name is how the a11y tree, the preview and tests find a node, so a
// duplicate makes every lookup ambiguous. DuplicateNames walks the tree in layout order.
#include "ui.h"

#include <cstdio>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

int main() {
    Desc clean = VStack({Label("a").name("one"),
                         HStack({Label("b").name("two"), Label("c").name("three")})});
    check(DuplicateNames(clean).empty(), "unique names are clean");

    Desc dup = VStack({Label("a").name("one"),
                       HStack({Label("b").name("two"), Label("c").name("two")})});
    const std::vector<std::string> d = DuplicateNames(dup);
    check(d.size() == 1 && d[0] == "two", "a duplicate is reported once, by name");

    Desc thrice = VStack({Label("x").name("n"), Label("y").name("n"), Label("z").name("n")});
    check(DuplicateNames(thrice).size() == 1, "a name used three times is still one report");

    check(DuplicateNames(Desc()).empty(), "an empty tree has no duplicates");

    // An unnamed node is not a name, so many of them at once is fine.
    Desc anon = VStack({Label("a"), Label("b"), Label("c")});
    check(DuplicateNames(anon).empty(), "unnamed nodes do not collide");

    std::printf(fails ? "names: %d of %d FAILED\n" : "names: ok (%d checks)\n", fails ? fails : checks);
    return fails ? 1 : 0;
}
