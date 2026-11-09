// 8.7, component 1: SplitButton is a verb plus its ranked alternatives, and it says so.
#include "components/split_button.h"

#include <cstdio>
#include <string>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

int main() {
    int primary_runs = 0;
    std::vector<MenuItem> alts;
    MenuItem m;
    m.label = "Fit to window";
    m.run = [] {};
    alts.push_back(m);

    Desc sb = SplitButton("Resize", [&] { ++primary_runs; }, alts);

    check(std::string(sb->Role()) == "splitbutton", "the component declares its own role");
    check(std::string(sb->Role()) != "control", "a role better than control (8.7's check)");

    // The role decorator wraps the pair, so the two halves are its child's children.
    std::vector<Node*> top;
    sb->Children(&top);
    check(top.size() == 1, "WithRole wraps one node");
    std::vector<Node*> kids;
    if (!top.empty()) top[0]->Children(&kids);
    check(kids.size() == 2, "a default action and its alternatives are two nodes");
    if (kids.size() == 2) {
        check(std::string(kids[0]->Role()) == "button", "the default action is a button");
        check(std::string(kids[1]->Role()) == "button", "the alternatives open from a button");
    }

    if (fails) std::printf("splitbutton: %d of %d FAILED\n", fails, checks);
    else std::printf("splitbutton: ok (%d checks)\n", checks);
    return fails ? 1 : 0;
}
