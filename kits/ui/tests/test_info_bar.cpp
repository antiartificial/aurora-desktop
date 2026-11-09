// 8.7, component: the info bar. What is asserted is what the component PROMISES -- its role, that
// severity is a type mapped to the theme's tokens, and that an action is one button and nothing
// else. Nothing here needs a compositor: a strip that could only be checked by looking at it
// would be a strip whose severity mapping drifts the first time a token is renamed.
#include "components/info_bar.h"

#include <cstdio>
#include <set>
#include <string>
#include <vector>

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

    // ---- what the component IS ------------------------------------------------------------
    Desc plain = InfoBar(InfoSeverity::Info, "3 items were skipped");
    check(std::string(plain->Role()) == "status", "the component declares its own role");
    check(std::string(plain->Role()) != "control", "a role better than control (8.7's check)");

    std::vector<Node*> none;
    plain->Children(&none);
    check(none.empty(), "a message with no action has no child control");

    // ---- severity is a type ---------------------------------------------------------------
    std::set<std::string> names;
    for (InfoSeverity s : {InfoSeverity::Info, InfoSeverity::Warning, InfoSeverity::Error}) {
        names.insert(InfoSeverityName(s));
    }
    check(names.size() == 3, "three distinct severity names");

    const au_color info = InfoSeverityInk(e, InfoSeverity::Info);
    const au_color warn = InfoSeverityInk(e, InfoSeverity::Warning);
    const au_color err = InfoSeverityInk(e, InfoSeverity::Error);
    check(same(warn, e.warning), "warning draws with the warning token");
    check(same(err, e.destructive), "error draws with the destructive token, not a second red");
    check(same(info, e.dim), "info draws with the secondary ink, and is not success");
    check(!same(info, warn) && !same(warn, err) && !same(info, err),
          "the three severities are three different pictures");

    // ---- the optional action --------------------------------------------------------------
    int runs = 0;
    Desc with = InfoBar(InfoSeverity::Error, "Could not move 2 files", "Retry", [&] { ++runs; });
    std::vector<Node*> kids;
    with->Children(&kids);
    check(kids.size() == 1, "one action, because a strip with two is a form");
    if (kids.size() == 1) {
        check(std::string(kids[0]->Role()) == "button", "the action is a real button");
        // Press and release is what the window's event routing would deliver; calling it here
        // proves the callback is wired rather than that the tree merely has a child.
        kids[0]->OnPress();
        kids[0]->OnRelease(true);
        check(runs == 1, "the action runs when it is activated");
    }

    if (fails) std::printf("info_bar: %d of %d FAILED\n", fails, checks);
    else std::printf("info_bar: ok (%d checks)\n", checks);
    return fails ? 1 : 0;
}
