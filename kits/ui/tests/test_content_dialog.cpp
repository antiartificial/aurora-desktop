// 8.7: ContentDialog -- a modal confirmation that cannot ask for a destructive verb.
//
// Headless: Measure/Place are called directly and the frames are read back, and one Draw goes to
// an offscreen surface so the scrim and the plate are checked as pixels rather than described.
#include "components/content_dialog.h"

#include <cstdint>
#include <cstdio>
#include <string>
#include <vector>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) { std::printf("FAIL %s\n", what); ++fails; }
}

static Node* named(Node* n, const char* name) {
    if (!n) return nullptr;
    if (n->name == name) return n;
    std::vector<Node*> kids;
    n->Children(&kids);
    for (Node* k : kids)
        if (Node* f = named(k, name)) return f;
    return nullptr;
}

// Case-insensitive, because the failure this guards against is a label someone wrote in whatever
// case felt right at the call site.
static bool names_a_destructive_verb(const std::string& label) {
    std::string low;
    for (char c : label) low += (char)(c >= 'A' && c <= 'Z' ? c - 'A' + 'a' : c);
    static const char* kForbidden[] = {"delete", "trash", "remove", "replace", "erase", "destroy"};
    for (const char* w : kForbidden)
        if (low.find(w) != std::string::npos) return true;
    return false;
}

int main() {
    Env e;
    float sys_size = 0;
    const bool have_font = au_font_system("plain", &e.font, &sys_size) != 0;
    if (!au_font_system("bold", &e.bold, &sys_size)) e.bold = e.font;
    e.size = Env::kBaseSize;
    e.unit = 8;

    // ---- the primary action cannot name a destructive verb -------------------------------------
    bool all_labelled = true, any_destructive = false;
    for (int i = 0; i < kAffirmCount; i++) {
        const char* l = AffirmLabel(static_cast<Affirm>(i));
        if (!l || !*l) all_labelled = false;
        else if (names_a_destructive_verb(l)) any_destructive = true;
    }
    check(all_labelled, "every Affirm has a label, so no primary draws blank");
    check(!any_destructive, "no label in the table names a destructive verb");
    check(AffirmLabel(static_cast<Affirm>(kAffirmCount)) == nullptr,
          "a value outside the closed set has no label");

    // ---- the tree ------------------------------------------------------------------------------
    int confirms = 0, cancels = 0;
    Desc d = ContentDialog("Apply changes",
                           "These settings will be applied to the open window.",
                           Affirm::Apply, [&] { ++confirms; }, [&] { ++cancels; });

    check(std::string(d->Role()) == "dialog", "the component declares its own role");
    check(std::string(d->Role()) != "control", "a role better than control (8.7's check)");
    check(DuplicateNames(d).empty(), "the two buttons' names are unique");

    std::vector<Node*> parts;
    d->Children(&parts);
    check(parts.size() == 3, "a title, a body and the button row");
    if (parts.size() == 3) {
        check(std::string(parts[0]->Role()) == "label", "the title is a label");
        check(std::string(parts[1]->Role()) == "text", "the body is text, not a label that cannot wrap");
    }

    Node* primary = named(d.get(), kDialogPrimaryName);
    Node* cancel = named(d.get(), kDialogCancelName);
    check(primary != nullptr, "the primary button is named for FocusNamed");
    check(cancel != nullptr, "the cancel button is named too");
    check(primary && std::string(primary->Role()) == "button", "the primary is a button");
    check(cancel && std::string(cancel->Role()) == "button", "cancel is a button");

    // ---- layout: it fills the window, the plate is centred, the pair is bottom-right -----------
    const Size offered{800, 600};
    const Size got = d->Measure(e, Constraints{offered.w, offered.h});
    check(got.w == offered.w && got.h == offered.h, "the dialog fills the space it is given");
    d->Place(Rect{0, 0, offered.w, offered.h});

    if (parts.size() == 3 && primary && cancel) {
        check(parts[0]->frame.y + parts[0]->frame.h <= parts[1]->frame.y,
              "the title sits above the body");
        check(parts[1]->frame.y + parts[1]->frame.h <= primary->frame.y,
              "the body sits above the buttons");
        check(primary->frame.x > cancel->frame.x, "cancel is left of the primary");
        check(primary->frame.x > 400, "the answer pair is on the right of the plate");
        check(primary->frame.x + primary->frame.w <= 800 && primary->frame.y + primary->frame.h <= 600,
              "the primary is inside the window");
        check(primary->frame.y + primary->frame.h <= 600 - 16, "nothing is flush against the edge");
    }

    // ---- modality: the pointer --------------------------------------------------------------
    check(d->HitTest(2, 2) == d.get(), "a click on the scrim is consumed by the dialog");
    if (primary) {
        Node* hit = d->HitTest(primary->frame.x + 1, primary->frame.y + 1);
        check(hit == primary, "a click on the primary reaches the button");
    }
    check(d->HitTest(801, 601) == nullptr, "outside its own frame it does not claim the pointer");
    check(d->OnWheel(3), "the wheel is swallowed, so a list under it cannot scroll");

    // ---- the three ways to answer -------------------------------------------------------------
    if (primary) primary->OnRelease(true);
    check(confirms == 1, "a click on the primary confirms");
    if (cancel) cancel->OnRelease(true);
    check(cancels == 1, "a click on cancel cancels");

    au_key esc{};
    esc.pressed = 1;
    esc.raw_char = 0x1b;
    check(d->OnKey(esc) && cancels == 2, "Escape cancels");

    au_key ret{};
    ret.pressed = 1;
    ret.raw_char = 0x0d;
    ret.utf8[0] = '\r';
    check(d->OnKey(ret) && confirms == 2, "Return confirms when no button holds focus");

    au_key release{};
    check(!d->OnKey(release), "a key release is not handled");

    // ---- wrapping, and a short window gives way in the body and not in the buttons -------------
    if (have_font) {
        Desc short_body = ContentDialog("Apply changes", "Applied now.", Affirm::Apply, nullptr);
        std::string long_body;
        for (int i = 0; i < 40; i++) long_body += "the settings will be applied to the open window ";
        Desc tall = ContentDialog("Apply changes", long_body, Affirm::Apply, nullptr);

        short_body->Measure(e, Constraints{800, 600});
        short_body->Place(Rect{0, 0, 800, 600});
        tall->Measure(e, Constraints{800, 600});
        tall->Place(Rect{0, 0, 800, 600});

        std::vector<Node*> sp, tp;
        short_body->Children(&sp);
        tall->Children(&tp);
        check(sp.size() == 3 && tp.size() == 3, "both dialogs have three parts");
        if (sp.size() == 3 && tp.size() == 3)
            check(tp[1]->frame.h > sp[1]->frame.h * 2,
                  "a long body wraps to more lines than a short one");

        // 140 px leaves room for the title, the buttons, nothing else.
        tall->Measure(e, Constraints{800, 140});
        tall->Place(Rect{0, 0, 800, 140});
        Node* pr = named(tall.get(), kDialogPrimaryName);
        check(pr != nullptr, "the primary survives a short window");
        if (pr)
            check(pr->frame.y + pr->frame.h <= 140,
                  "a short window clips the body, never pushes the buttons off the plate");
        if (tp.size() == 3) check(tp[1]->frame.h < 40, "the body took the height the window did not have");
    }

    // ---- drawing: scrim over the window, plate in the raised surface colour, centred -----------
    au_surface* s = au_surface_new(400, 300, 1);
    check(s != nullptr, "an offscreen surface for the drawing claims");
    if (s) {
        au_clear(s, au_color{255, 255, 255, 255});
        Desc g = ContentDialog("Open in a new window?",
                               "The current view stays where it is.", Affirm::Open, nullptr);
        g->Measure(e, Constraints{400, 300});
        g->Place(Rect{0, 0, 400, 300});
        g->Draw(s, e);

        const uint32_t* px = au_surface_pixels(s);
        const int pw = au_surface_px_w(s);
        const int ph = au_surface_px_h(s);
        auto at = [&](int x, int y) { return px[(size_t)y * (size_t)pw + (size_t)x]; };
        check(at(1, 1) != 0xFFFFFFFFu, "the scrim covers the window");

        const uint32_t plate = 0xFF343849u;      // Env::surface_hi, the dark scheme
        long plate_px = 0, minx = pw, maxx = -1;
        for (int y = 0; y < ph; y++)
            for (int x = 0; x < pw; x++) {
                if (at(x, y) != plate) continue;
                ++plate_px;
                if (x < minx) minx = x;
                if (x > maxx) maxx = x;
            }
        check(plate_px > 2000, "the plate is drawn in the raised surface colour");
        check(maxx > minx && (minx + maxx) / 2 >= pw / 2 - 2 && (minx + maxx) / 2 <= pw / 2 + 2,
              "the plate is centred in the window");
        au_surface_free(s);
    }

    if (fails) std::printf("content_dialog: %d of %d FAILED\n", fails, checks);
    else std::printf("content_dialog: ok (%d checks)\n", checks);
    return fails ? 1 : 0;
}
