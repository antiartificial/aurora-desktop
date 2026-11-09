// 8.7, component: Expander is a disclosure -- one header, one revealed body, one shared cell.
//
// Headless: Measure, Place and HitTest are reachable without a compositor, and they are where the
// claims live. What is deliberately checked is that the state is the CALLER's cell, that a closed
// section keeps its content out of the tree and the focus order, and that the header is the only
// click target -- the three ways a disclosure widget usually goes wrong.
#include "components/expander.h"

#include <cstdio>
#include <string>
#include <vector>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

// Every node below `n`, so a test can ask what the tree actually contains rather than what the
// factory says it built.
static void collect(Node* n, std::vector<Node*>* out) {
    std::vector<Node*> kids;
    n->Children(&kids);
    for (Node* k : kids) {
        out->push_back(k);
        collect(k, out);
    }
}

static Node* by_name(Node* n, const std::string& name) {
    std::vector<Node*> all;
    collect(n, &all);
    for (Node* k : all) if (k->name == name) return k;
    return nullptr;
}

static bool has_role(Node* n, const char* role) {
    std::vector<Node*> all;
    collect(n, &all);
    for (Node* k : all) if (std::string(k->Role()) == role) return true;
    return false;
}

int main() {
    Env e;
    float sys = 0;
    if (!au_font_system("plain", &e.font, &sys)) { /* a headless host may have no face */ }
    if (!au_font_system("bold", &e.bold, &sys)) e.bold = e.font;
    e.italic = e.font;
    e.bold_italic = e.bold;

    State<bool> open(false);
    Desc content = VStack({Button("Apply", [] {}).name("apply")}).name("body");
    Desc ex = Expander("Advanced", open, sized(content, 200, 60)).name("advanced");

    check(std::string(ex->Role()) == "group", "the component declares its own role");
    check(std::string(ex->Role()) != "control", "a role better than control (8.7's check)");
    check(DuplicateNames(ex).empty(), "the tree has no duplicate names");

    check(by_name(ex.get(), "apply") == nullptr, "a closed section builds no reachable content");
    check(!has_role(ex.get(), "button"), "a closed section has no control in the tree");

    Size closed = ex->Measure(e, Constraints{400, 600});
    ex->Place(Rect{0, 0, closed.w, closed.h});
    check(closed.h > 0 && closed.h < 60, "closed, the group is just its header");
    check(closed.w >= 200, "the body's width is reserved while closed");

    check(ex->CanFocus(), "the header is a keyboard stop");
    check(ex->HitTest(5, 5) == ex.get(), "the header is the target");
    check(ex->HitTest(5, closed.h + 5) != ex.get(),
          "the header does not own the area below it");

    // A click on the header is the caller's cell changing, not an internal field.
    Node* head = ex->HitTest(5, 5);
    check(head != nullptr, "the header is hit-testable");
    if (head) head->OnRelease(true);
    check(open.get(), "clicking the header opens the section");

    // No window here to drive transitions: settle the reveal the way the window would, with
    // motion off, so the geometry is the end state rather than the start of an animation.
    ex->Retarget(false);
    Size opened = ex->Measure(e, Constraints{400, 600});
    ex->Place(Rect{0, 0, opened.w, opened.h});
    check(opened.h >= closed.h + 59.5f, "open, the body has its natural height");
    check(opened.h <= closed.h + 60.5f, "the body is not stretched past its size");
    check(opened.w == closed.w, "opening does not reflow the group's width");
    check(by_name(ex.get(), "apply") != nullptr, "an open section exposes its content");
    Node* inside = ex->HitTest(5, closed.h + 5);
    check(inside != nullptr && inside != ex.get() && std::string(inside->Role()) == "button",
          "a click below the header reaches the content, not the header");

    // Left and Right are the disclosure keys; the same cell is what they move.
    au_key k{};
    k.pressed = 1;
    k.raw_char = 0x1c;                                  // left
    check(ex->OnKey(k) && !open.get(), "Left collapses an open section");
    check(!ex->OnKey(k), "Left on a closed section declines, so focus can leave");
    k.raw_char = 0x1d;                                  // right
    check(ex->OnKey(k) && open.get(), "Right opens a closed section");
    check(!ex->OnKey(k), "Right on an open section declines");

    k.raw_char = 0;
    k.utf8[0] = ' ';
    check(ex->OnKey(k) && !open.get(), "Space toggles like a button");

    // Disabled: the section stays in the tree, dims, and answers neither pointer nor key.
    State<bool> off(false), locked(false);
    Desc dis = Expander("Settings", off, sized(Button("Inner", [] {}), 100, 30), locked);
    const Size dsz = dis->Measure(e, Constraints{400, 600});
    dis->Place(Rect{0, 0, dsz.w, dsz.h});
    Node* dhead = dis->HitTest(5, 5);
    check(dhead == dis.get(), "a disabled header is still hit-testable");
    if (dhead) dhead->OnRelease(true);
    check(!off.get(), "a disabled header does not toggle on a click");
    au_key rk{};
    rk.pressed = 1;
    rk.raw_char = 0x1d;
    check(!dis->OnKey(rk), "a disabled header declines the keyboard");

    // A constant binding is read-only: a click must not pretend to have changed anything. If the
    // header wrote its own copy, this would collapse to the header height.
    State<bool> cell(false);
    Desc fixed = Expander("Fixed", Binding<bool>(true), sized(Button("Inner", [] {}), 100, 30),
                          cell);
    const Size fsz = fixed->Measure(e, Constraints{400, 600});
    fixed->Place(Rect{0, 0, fsz.w, fsz.h});
    if (Node* fh = fixed->HitTest(5, 5)) fh->OnRelease(true);
    const Size after = fixed->Measure(e, Constraints{400, 600});
    check(after.h == fsz.h, "a constant section does not change height on a click");

    if (fails) std::printf("expander: %d of %d FAILED\n", fails, checks);
    else std::printf("expander: ok (%d checks)\n", checks);
    return fails ? 1 : 0;
}
