// The gallery's model, exercised without a compositor.
//
// gallery.cpp is compiled in model-only mode, so this test drives the SAME Session, Library and
// Rows the running application uses rather than a second copy of the rules. What is asserted
// here is what a person can actually do: choose a destination, filter the list, open Details,
// dismiss the first-run note, and confirm a copy -- plus the two empty states and the fact that
// only one of the export alternatives opens a dialog.
#include <cstdio>
#include <string>
#include <vector>

#define GALLERY_MODEL_ONLY 1
#include "../demo/gallery.cpp"

using namespace aurora::ui;
using gallery::Library;
using gallery::Session;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) {
        std::printf("FAIL %s\n", what);
        ++fails;
    }
}

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
    for (Node* k : all)
        if (k->name == name) return k;
    return nullptr;
}

static bool contains(const std::string& hay, const std::string& needle) {
    return hay.find(needle) != std::string::npos;
}

static bool same_color(const au_color& a, const au_color& b) {
    return a.r == b.r && a.g == b.g && a.b == b.b && a.a == b.a;
}

static au_key key(uint32_t raw) {
    au_key k{};
    k.pressed = 1;
    k.raw_char = raw;
    return k;
}

int main() {
    Env e;
    float sys = 0;
    if (!au_font_system("plain", &e.font, &sys)) { /* a headless host may have no face */ }
    if (!au_font_system("bold", &e.bold, &sys)) e.bold = e.font;
    e.italic = e.font;
    e.bold_italic = e.bold;

    Session s;

    // ---- sidebar: two sections, the destinations in declared order ---------------------------
    const std::vector<SidebarSection> secs = s.sidebar_sections();
    check(secs.size() == 2, "the sidebar has two sections");
    if (secs.size() == 2) {
        check(secs[0].key == "library" && secs[0].title == "Library",
              "the first section is Library, keyed stably");
        check(secs[1].key == "albums" && secs[1].title == "Albums",
              "the second section is Albums");
        check(secs[0].items.size() == 3, "Library holds three destinations");
        check(secs[1].items.size() == 3, "Albums holds three destinations");
        if (secs[0].items.size() == 3) {
            check(secs[0].items[0].id == "all" && secs[0].items[0].label == "All Photos",
                  "All Photos is the first destination");
            check(secs[0].items[1].id == "recent" && secs[0].items[2].id == "fav",
                  "Recent and Favourites follow, by id");
        }
        if (secs[1].items.size() == 3) {
            check(secs[1].items[0].id == "album:holiday", "Holiday is an album id");
            check(secs[1].items[1].id == "album:family", "Family is an album id");
            check(secs[1].items[2].id == "album:screenshots", "Screenshots is an album id");
            check(secs[1].items[2].note == "0", "the empty album says it has no photos");
        }
    }

    // ---- the listing follows the selected destination --------------------------------------
    check(s.scope.get() == "all" && s.cursor.get() == 0, "the app opens on All Photos");
    check(s.visible().size() == 11, "All Photos shows the whole library");
    check(s.detail_name.get() == "Sunrise over the bay.jpg",
          "the first photo's details are ready on open");
    check(s.has_selection.get(), "an initial row means Export is available");

    check(s.lib.count_scope("recent") == 4, "Recent is the flagged recent photos");
    check(s.lib.count_scope("fav") == 3, "Favourites is the flagged favourites");
    check(s.lib.count_scope("album:holiday") == 5, "Holiday holds five photos");
    check(s.lib.album_total("screenshots") == 0, "Screenshots starts empty");

    // The sidebar's selected cell IS the scope, so it is written before the select handler runs.
    // This is the shape a sidebar click actually has, and the listing must still change.
    s.scope.set("recent");
    s.select_scope("recent");
    check(s.visible().size() == 4, "a sidebar click after the cell is written still lists Recent");
    check(s.cursor.get() == 0, "and puts the cursor on its first photo");

    s.select_scope("album:holiday");
    check(s.scope.get() == "album:holiday", "choosing a destination writes the shared cell");
    check(s.visible().size() == 5, "the listing shows only the album");
    check(s.cursor.get() == 0, "a new destination puts the cursor on its first photo");
    check(s.detail_album.get() == "Holiday", "details follow the selection");

    s.select_row(2);
    check(s.cursor.get() == 2, "a row click moves the cursor");
    check(s.detail_name.get() == "Trail to the lighthouse.jpg", "details follow the row");
    check(s.detail_kind.get() == "JPEG image", "the kind comes from the extension");
    check(contains(s.detail_size.get(), "KB") || contains(s.detail_size.get(), "MB"),
          "the size is humanised rather than bytes");

    // ---- the filter, and the two different empty states ------------------------------------
    s.set_query("sun");
    check(s.visible().size() == 1, "the filter narrows to the matching name");
    check(s.detail_name.get() == "Sunrise over the bay.jpg", "the cursor follows the narrowed view");
    check(!s.no_matches.get() && !s.empty_album.get(), "a match shows no strip");

    s.set_query("zzz");
    check(s.visible().empty(), "a filter can match nothing");
    check(s.no_matches.get(), "no matches raises the filter strip");
    check(!s.empty_album.get(), "and not the empty-album strip");
    check(s.cursor.get() == -1 && !s.has_selection.get(),
          "nothing is selected when the view is empty");

    s.clear_filter();
    check(s.query.get().empty() && s.visible().size() == 5, "clearing restores the album");

    s.select_scope("album:screenshots");
    check(s.visible().empty(), "the empty album is empty");
    check(s.empty_album.get(), "the empty album raises its own strip");
    check(!s.no_matches.get(), "the empty album does not blame the filter");
    check(s.detail_name.get() == "No photo selected", "details say nothing is selected");
    check(!s.export_default(), "Export declines with no selection");
    check(!s.request_export("Copy path"), "Copy path declines with no selection");
    check(!s.dialog_open.get(), "and no dialog opens");

    // ---- the export alternatives: exactly one is a confirmation -----------------------------
    s.select_scope("album:holiday");
    const std::vector<MenuItem> alts = s.export_alternatives();
    check(alts.size() == 3, "Export has three alternatives");
    if (alts.size() == 3) {
        check(alts[0].label == "Export as PNG", "the first alternative is PNG");
        check(alts[1].label == "Export as JPEG", "the second is JPEG");
        check(alts[2].label == "Copy path", "the third is Copy path");
    }

    check(s.export_default(), "the default export runs");
    check(!s.dialog_open.get(), "the default export is not a confirmation");
    check(s.request_export("Export as PNG") == false, "PNG export reports no dialog");
    check(!s.dialog_open.get(), "PNG export opens no dialog");
    check(s.request_export("Export as JPEG") == false, "JPEG export reports no dialog");
    check(!s.dialog_open.get(), "JPEG export opens no dialog");

    check(s.request_export("Copy path") == true, "Copy path opens the confirmation");
    check(s.dialog_open.get(), "the dialog cell is set");
    const std::string copied = s.confirm_copy();
    check(!s.dialog_open.get(), "confirming closes the dialog");
    check(contains(copied, "/Pictures/Holiday/Sunrise over the bay.jpg"),
          "confirming returns the real path to copy");
    check(contains(s.last_action.get(), "Copied"), "the status line reports the copy");

    s.request_export("Copy path");
    check(s.dialog_open.get(), "the dialog can be opened again");
    s.cancel_copy();
    check(!s.dialog_open.get(), "cancelling closes it");
    check(contains(s.last_action.get(), "cancel"), "cancelling is reported as cancellation");

    // ---- the list's keyboard moves the cursor, and the app follows it -----------------------
    // The real ListNode, over the real Rows: Down is the list's own gesture, and the handler the
    // app installs is what turns the new cursor into the details pane's values.
    s.select_scope("all");
    check(s.cursor.get() == 0, "All Photos starts on the first row");
    gallery::PhotoRows rows(s);
    Desc list = ListView(&rows, s.cursor);
    OnListSelect(list, [&s](int row) { s.select_row(row); });
    list->Measure(e, Constraints{620, 400});
    list->Place(Rect{0, 0, 620, 400});
    check(list->OnKey(key(0x1f)), "the list consumes Down");
    check(s.cursor.get() == 1, "Down moves the list cursor");
    check(s.detail_name.get() == "Harbour at dusk.jpg",
          "the details pane follows the keyboard, not only the pointer");
    check(s.detail_album.get() == "Holiday", "and its album is the row's album");

    // ---- Details is collapsed by default ----------------------------------------------------
    check(!gallery::kDetailsOpenByDefault, "the declared default is collapsed");
    check(!s.details_open.get(), "the session starts with Details collapsed");

    Desc det = Expander("Details", s.details_open,
                        VStack({Label(s.detail_name).name("details-value")}, 0.5f))
                   .name("details-test");
    const Size closed = det->Measure(e, Constraints{300, 600});
    det->Place(Rect{0, 0, closed.w, closed.h});
    check(closed.h < 60, "collapsed, the Expander is only its header");
    check(by_name(det.get(), "details-value") == nullptr,
          "collapsed, the details are not in the tree");
    Node* head = det->HitTest(5, 5);
    check(head == det.get(), "the header is the disclosure target");
    if (head) head->OnRelease(true);
    check(s.details_open.get(), "clicking the header opens the shared cell");
    det->Retarget(false);   // settle the reveal without a window to tick it
    const Size opened = det->Measure(e, Constraints{300, 600});
    det->Place(Rect{0, 0, opened.w, opened.h});
    check(opened.h > closed.h, "open, the section takes its content's height");
    check(by_name(det.get(), "details-value") != nullptr,
          "open, the details are reachable");

    // ---- the first-run note ----------------------------------------------------------------
    check(s.tip_open.get(), "the teaching tip is shown on first run");
    Desc tip = TeachingTip("Details follow your selection", "Pick a photo.",
                           s.tip_open, [&s] { s.dismiss_tip(); });
    tip->Measure(e, Constraints{300, -1});
    std::vector<Node*> tip_kids;
    tip->Children(&tip_kids);
    check(tip_kids.size() == 1 && std::string(tip_kids[0]->Role()) == "button",
          "the tip offers one action");
    if (tip_kids.size() == 1) {
        tip_kids[0]->OnPress();
        tip_kids[0]->OnRelease(true);
    }
    check(!s.tip_open.get(), "its button dismisses the note through the app's cell");
    check(tip->Animating(), "hiding the note asks for a layout");
    const Size gone = tip->Measure(e, Constraints{300, -1});
    check(gone.w == 0 && gone.h == 0, "dismissed, the note takes no room");

    // ---- the breadcrumb and the scope ids agree --------------------------------------------
    check(Library::path_for_scope("album:holiday") == "/Photo Library/Holiday",
          "a scope has one breadcrumb path");
    check(Library::scope_for_path("/Photo Library/Holiday") == "album:holiday",
          "clicking the album crumb returns to that scope");
    check(Library::scope_for_path("/Photo Library/Favourites") == "fav",
          "the Favourites crumb is a scope");
    check(Library::scope_for_path("/Photo Library") == "all",
          "the library crumb is the whole library");
    check(Library::scope_for_path("/") == "all", "the root is the whole library");
    check(s.location.get() == "/Photo Library/All Photos", "the location derives from the scope");

    // ---- severity is a type, not a colour chosen at the call site --------------------------
    check(std::string(InfoSeverityName(InfoSeverity::Warning)) == "warning",
          "warning has a stable name");
    check(same_color(InfoSeverityInk(e, InfoSeverity::Warning), e.warning),
          "the warning strip uses the warning token");
    check(same_color(InfoSeverityInk(e, InfoSeverity::Info), e.dim),
          "the info strip uses the secondary ink");
    check(gallery::kNoMatchesMessage[0] != '\0' && gallery::kEmptyAlbumMessage[0] != '\0',
          "both empty states have a sentence");

    // ---- the quality control's range rule ---------------------------------------------------
    check(s.quality.get() == 90.0, "quality starts at 90");
    check(ClampNumber(140, 1, 100) == 100, "the range clamps above");
    check(ClampNumber(-4, 1, 100) == 1, "and below");
    check(StepNumber(100, 1, 1, 100, 1) == 100, "a step from the maximum stays there");

    if (fails) std::printf("gallery: %d of %d FAILED\n", fails, checks);
    else std::printf("gallery: ok (%d checks)\n", checks);
    return fails ? 1 : 0;
}
