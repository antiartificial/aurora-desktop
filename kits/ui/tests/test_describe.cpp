// 8.8/P6: the toolkit half of ui:describe -- named action nodes become the surface's action list.
#include "ui.h"

#include <cstdio>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

int main() {
    Desc tree = VStack({Label("Open").name("act:open"),
                        HStack({Label("Rename").name("act:rename=Rename…"),
                                Label("not an action").name("files")})});

    const SurfaceDescriptor d = Describe(tree, "org.example.atlas", "Atlas");
    check(d.app == "org.example.atlas" && d.title == "Atlas", "the surface names its app and title");
    check(d.actions.size() == 2, "two action nodes became two actions");
    check(d.actions.size() == 2 && d.actions[0].id == "open", "the id is the verb, in tree order");
    check(d.actions.size() == 2 && d.actions[1].id == "rename", "the second action is parsed");
    check(d.actions.size() == 2 && d.actions[1].label == "Rename…", "act:verb=label sets the label");
    check(d.actions.size() == 2 && d.actions[0].label == "open", "a bare act:verb labels with the verb");

    const SurfaceDescriptor empty = Describe(Desc(), "x", "y");
    check(empty.actions.empty(), "an empty tree offers no actions");

    std::printf(fails ? "describe: %d of %d FAILED\n" : "describe: ok (%d checks)\n",
                fails ? fails : checks);
    return fails ? 1 : 0;
}
