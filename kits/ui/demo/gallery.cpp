// Photo Library -- the component gallery, as an application rather than a specimen sheet.
//
// Why a photo library: every one of the twelve components has a job here that a person would
// recognise. Destinations on the left, a command bar with one verb that has alternatives, a
// location, a listing, a disclosure for detail, a first-run note, a status strip when a view is
// empty, and a confirmation before a copy. Nothing is on screen to demonstrate a widget; the
// widgets are the screen.
//
// The model (Library and Session) is deliberately window-free. A State cell does not need a
// Window to hold a value, so the same code that drives the running app can be exercised by
// tests/test_gallery.cpp without a compositor. main() is compiled out there with
// GALLERY_MODEL_ONLY, which is why this file includes its own main guard.
//
// GALLERY_FRAMES=n    stop after n frames
// GALLERY_MAX_MS=n    wall-clock ceiling for a scripted run
// GALLERY_REPORT=1    print what the model and the window think
// GALLERY_SCOPE=id    start on a destination (all, recent, fav, album:holiday, ...)
// GALLERY_DETAILS=1   start with the Details disclosure open
// GALLERY_DIALOG=1    start with the copy-path confirmation open

#include "../ui.h"
#include "components/all.h"

#include <algorithm>
#include <cctype>
#include <cstdio>
#include <cstdlib>
#include <string>
#include <utility>
#include <vector>

using namespace aurora::ui;

namespace gallery {

// ---- the data ---------------------------------------------------------------------------------
//
// A library is immutable data in this build: the app selects and filters it, and does not edit
// it. That is a scope decision, not a shortcut -- a gallery that offered editing would need an
// undo model before it needed another widget.

struct Photo {
    std::string name;
    std::string album;          // empty means unsorted
    long long bytes = 0;
    bool recent = false;
    bool favourite = false;
};

std::string lower(std::string s) {
    for (char& c : s) c = (char)std::tolower((unsigned char)c);
    return s;
}

std::string extension_of(const std::string& name) {
    const size_t dot = name.find_last_of('.');
    if (dot == std::string::npos || dot + 1 >= name.size()) return std::string();
    return lower(name.substr(dot + 1));
}

// The formats a camera and a screenshot tool actually produce. The kind is a sentence a details
// pane can show, not a MIME type: "JPEG image" is what a person reads, and the extension is
// already in the name.
std::string kind_for(const std::string& name) {
    const std::string ext = extension_of(name);
    if (ext == "jpg" || ext == "jpeg") return "JPEG image";
    if (ext == "png") return "PNG image";
    if (ext == "heic") return "HEIC image";
    if (ext == "webp") return "WebP image";
    return "Image";
}

std::string human_size(long long bytes) {
    if (bytes < 1024) return std::to_string(bytes) + " B";
    const double kb = (double)bytes / 1024.0;
    char b[32];
    if (kb < 1024.0) std::snprintf(b, sizeof b, "%.1f KB", kb);
    else std::snprintf(b, sizeof b, "%.1f MB", kb / 1024.0);
    return std::string(b);
}

class Library {
public:
    Library() {
        add("Sunrise over the bay.jpg", "Holiday", 4'214'784, true, true);
        add("Harbour at dusk.jpg", "Holiday", 3'842'112, true, false);
        add("Trail to the lighthouse.jpg", "Holiday", 5'132'288, false, true);
        add("Picnic on the cliff.jpg", "Holiday", 3'204'096, false, false);
        add("Ferry crossing.jpg", "Holiday", 2'818'048, false, false);
        add("Birthday cake.jpg", "Family", 3'606'528, true, false);
        add("Garden lunch.jpg", "Family", 4'090'880, false, true);
        add("Cousins on the porch.jpg", "Family", 2'998'272, false, false);
        add("Snow day.png", "Family", 5'505'024, true, false);
        add("City lights.heic", "", 6'291'456, false, false);
        add("Studio wall.png", "", 2'097'152, false, false);
    }

    const std::vector<Photo>& photos() const { return photos_; }

    // The visible photos, in library order. Scope first, then the filter, because a filter that
    // also changed scope would make "no matches" ambiguous: the user needs to know WHICH thing
    // left the pane empty, and the two info strips below say exactly that.
    std::vector<const Photo*> view(const std::string& scope, const std::string& query) const {
        std::vector<const Photo*> out;
        const std::string q = lower(query);
        const bool by_album = scope.rfind("album:", 0) == 0;
        const std::string album = by_album ? scope.substr(6) : std::string();
        for (const Photo& p : photos_) {
            bool in_scope = false;
            if (scope == "all") in_scope = true;
            else if (scope == "recent") in_scope = p.recent;
            else if (scope == "fav") in_scope = p.favourite;
            else if (by_album) in_scope = lower(p.album) == album;
            if (!in_scope) continue;
            if (!q.empty() && lower(p.name).find(q) == std::string::npos) continue;
            out.push_back(&p);
        }
        return out;
    }

    int count_scope(const std::string& scope) const { return (int)view(scope, "").size(); }

    int album_total(const std::string& album) const {
        int n = 0;
        for (const Photo& p : photos_)
            if (lower(p.album) == album) n++;
        return n;
    }

    // Scope ids are the sidebar's ids, so the sidebar, the breadcrumb and the listing all name
    // a destination the same way. An index would have been shorter and would have pointed at a
    // different album the first time the sections were reordered.
    static bool is_album_scope(const std::string& id) { return id.rfind("album:", 0) == 0; }
    static std::string album_of_scope(const std::string& id) { return id.substr(6); }

    static std::string label_for_scope(const std::string& id) {
        if (id == "all") return "All Photos";
        if (id == "recent") return "Recent";
        if (id == "fav") return "Favourites";
        if (is_album_scope(id)) {
            std::string a = album_of_scope(id);
            if (!a.empty()) a[0] = (char)std::toupper((unsigned char)a[0]);
            return a;
        }
        return "All Photos";
    }

    static std::string path_for_scope(const std::string& id) {
        return "/Photo Library/" + label_for_scope(id);
    }

    // The breadcrumb hands back the prefix that was clicked, including "/" for the root, so the
    // reverse mapping is here and not at the call site. An unknown segment falls back to the
    // whole library rather than to a blank pane.
    static std::string scope_for_path(const std::string& path) {
        std::vector<std::string> parts;
        size_t i = 0;
        while (i < path.size()) {
            while (i < path.size() && path[i] == '/') i++;
            const size_t j = path.find('/', i);
            const std::string part =
                path.substr(i, j == std::string::npos ? std::string::npos : j - i);
            if (!part.empty()) parts.push_back(part);
            if (j == std::string::npos) break;
            i = j;
        }
        if (parts.empty()) return "all";
        const std::string& last = parts.back();
        if (last == "Recent") return "recent";
        if (last == "Favourites") return "fav";
        if (last == "Holiday") return "album:holiday";
        if (last == "Family") return "album:family";
        if (last == "Screenshots") return "album:screenshots";
        return "all";
    }

private:
    void add(const std::string& name, const std::string& album, long long bytes,
             bool recent, bool favourite) {
        photos_.push_back(Photo{name, album, bytes, recent, favourite});
    }

    std::vector<Photo> photos_;
};

// The disclosure starts collapsed: the listing is what the window is for, and a details pane
// that opened itself would take width from it before the user had asked for anything.
constexpr bool kDetailsOpenByDefault = false;

// The two empty states are separate sentences, not one generic one. "No matches" is a filter
// the user can clear; "this album is empty" is a fact about the album. A single message would
// leave them guessing which of the two they are looking at.
constexpr const char* kNoMatchesMessage = "No photos match this filter.";
constexpr const char* kEmptyAlbumMessage = "This album is empty. Photos added here will appear in this list.";

// ---- the session ------------------------------------------------------------------------------
//
// Every value the interface shows lives in a cell here. The widgets are given Bindings, so a
// change is one set() and the toolkit invalidates exactly the nodes that read it. The library
// data is not a cell because it never changes; the scope that selects a part of it is.

struct Session {
    Library lib;

    State<std::string> scope{"all"};
    State<std::string> query{""};
    State<int> cursor{0};
    State<bool> details_open{kDetailsOpenByDefault};
    State<bool> tip_open{true};
    State<bool> dialog_open{false};
    State<bool> no_matches{false};
    State<bool> empty_album{false};
    State<double> quality{90.0};
    State<std::string> detail_name{""};
    State<std::string> detail_album{""};
    State<std::string> detail_kind{""};
    State<std::string> detail_size{""};
    State<std::string> detail_path{""};        // what Copy path puts on the clipboard
    State<std::string> detail_short_path{""};  // what the pane draws, kept short on purpose
    State<std::string> last_action{"All photos"};
    State<float> split_ratio{0.21f};       // sidebar share: ~210 px of the 1000 px window
    State<float> rail_ratio{0.62f};        // listing share of what remains: ~300 px of rail

    // The location is derived, not assigned at every navigation: a breadcrumb that could fall
    // out of step with the sidebar is the classic two-sources-of-truth bug, and Derived makes
    // the second copy impossible to forget.
    Derived<std::string> location;
    Derived<std::string> count_text;
    Derived<bool> has_selection;
    Derived<bool> has_query;

    Session()
        : location([](const std::string& s) { return Library::path_for_scope(s); }, scope),
          count_text([this](const std::string& sc, const std::string& q) {
                         const int n = (int)lib.view(sc, q).size();
                         return std::to_string(n) + (n == 1 ? " photo" : " photos");
                     },
                     scope, query),
          has_selection([this](const std::string& sc, const std::string& q, int cur) {
                            return cur >= 0 && cur < (int)lib.view(sc, q).size();
                        },
                        scope, query, cursor),
          has_query([](const std::string& q) { return !q.empty(); }, query) {
        refresh_info();
        refresh_detail();
    }

    // A Window is needed for threadsafe set(), damage and focus, but not for the values
    // themselves. Binding the cells once, before the tree is built, is all State::set needs to
    // invalidate the right nodes.
    void bind(Window& w) {
        scope.bind(&w);
        query.bind(&w);
        cursor.bind(&w);
        details_open.bind(&w);
        tip_open.bind(&w);
        dialog_open.bind(&w);
        no_matches.bind(&w);
        empty_album.bind(&w);
        quality.bind(&w);
        detail_name.bind(&w);
        detail_album.bind(&w);
        detail_kind.bind(&w);
        detail_size.bind(&w);
        detail_path.bind(&w);
        detail_short_path.bind(&w);
        last_action.bind(&w);
        split_ratio.bind(&w);
        rail_ratio.bind(&w);
    }

    std::vector<const Photo*> visible() const { return lib.view(scope.get(), query.get()); }

    // The sidebar's two sections. The counts are computed once at build: in this build the
    // library is read-only, so they cannot go stale, and a note is exactly what SidebarItem's
    // right-aligned slot is for.
    std::vector<SidebarSection> sidebar_sections() const {
        SidebarSection library;
        library.key = "library";
        library.title = "Library";
        library.items = {
            SidebarItem{"all", "All Photos", true, std::to_string(lib.count_scope("all"))},
            SidebarItem{"recent", "Recent", true, std::to_string(lib.count_scope("recent"))},
            SidebarItem{"fav", "Favourites", true, std::to_string(lib.count_scope("fav"))},
        };
        SidebarSection albums;
        albums.key = "albums";
        albums.title = "Albums";
        albums.items = {
            SidebarItem{"album:holiday", "Holiday", true,
                        std::to_string(lib.album_total("holiday"))},
            SidebarItem{"album:family", "Family", true,
                        std::to_string(lib.album_total("family"))},
            SidebarItem{"album:screenshots", "Screenshots", true,
                        std::to_string(lib.album_total("screenshots"))},
        };
        return {library, albums};
    }

    void select_scope(const std::string& id) {
        if (id.empty()) return;
        // No "already here" early return. The sidebar's selected cell IS `scope`, so the tree
        // writes this value BEFORE its select handler runs; returning early would leave the
        // listing showing the previous destination. Setting the same value again is one damage
        // rectangle, and the refresh below is the navigation.
        scope.set(id);
        // The filter belongs to a view, not to the window: carrying it across destinations is
        // how a person opens an album and finds it apparently empty.
        if (!query.get().empty()) query.set("");
        const auto v = visible();
        cursor.set(v.empty() ? -1 : 0);
        refresh_detail();
        refresh_info();
        last_action.set("Showing " + Library::label_for_scope(id));
    }

    void set_query(const std::string& q) {
        query.set(q);
        const auto v = visible();
        cursor.set(v.empty() ? -1 : 0);
        refresh_detail();
        refresh_info();
        if (!q.empty()) last_action.set("Filter: " + q);
    }

    void clear_filter() {
        if (query.get().empty()) return;
        set_query("");
        last_action.set("Filter cleared");
    }

    void select_row(int row) {
        cursor.set(row);
        refresh_detail();
    }

    void dismiss_tip() { tip_open.set(false); }

    void show_tip() { tip_open.set(true); }

    // The info strip's two conditions. An empty album outranks a filter: the album has no
    // photos to match, so blaming the filter would send the user to clear a field that was
    // never the reason.
    void refresh_info() {
        const auto v = visible();
        const bool album = Library::is_album_scope(scope.get());
        const bool album_empty =
            album && lib.album_total(Library::album_of_scope(scope.get())) == 0;
        empty_album.set(album_empty);
        no_matches.set(!album_empty && v.empty() && !query.get().empty());
    }

    void refresh_detail() {
        const auto v = visible();
        const int at = cursor.get();
        if (at < 0 || at >= (int)v.size()) {
            detail_name.set("No photo selected");
            detail_album.set("-");
            detail_kind.set("-");
            detail_size.set("-");
            detail_path.set("");
            detail_short_path.set("Select a photo to see its details.");
            return;
        }
        const Photo& p = *v[(size_t)at];
        detail_name.set(p.name);
        detail_album.set(p.album.empty() ? "Unsorted" : p.album);
        detail_kind.set(kind_for(p.name));
        detail_size.set(human_size(p.bytes));
        detail_path.set(path_of(p));
        // The pane is 300 px wide and a Label does not wrap, so the path is shown as the two
        // segments that identify it. The full path is what Copy path copies.
        detail_short_path.set(p.album.empty() ? p.name : p.album + " / " + p.name);
        last_action.set("Selected " + p.name);
    }

    static std::string path_of(const Photo& p) {
        return "/home/arti/Pictures/" + (p.album.empty() ? std::string("Unsorted") : p.album) +
               "/" + p.name;
    }

    // ---- verbs ------------------------------------------------------------------------------
    //
    // Export is the one command with ranked alternatives, which is what SplitButton is for: the
    // default is a real button (JPEG at the chosen quality) and the adjacent menu holds the
    // three things that are not the default.
    std::vector<MenuItem> export_alternatives() {
        std::vector<MenuItem> out;
        out.push_back(MenuItem{"Export as PNG", "", Effect::Write, true, false, false,
                               [this] { request_export("Export as PNG"); }});
        out.push_back(MenuItem{"Export as JPEG", "", Effect::Write, true, false, false,
                               [this] { request_export("Export as JPEG"); }});
        out.push_back(MenuItem{"Copy path", "", Effect::Read, true, false, false,
                               [this] { request_export("Copy path"); }});
        return out;
    }

    bool export_default() {
        if (!has_selection.get()) {
            last_action.set("Select a photo to export");
            return false;
        }
        last_action.set("Exported " + detail_name.get() + " as JPEG at quality " +
                        std::to_string((int)quality.get()));
        return true;
    }

    // Only Copy path opens the dialog. The confirmation exists because copying puts a path on
    // a clipboard that other programs read; PNG and JPEG export are already named by the verb
    // the user picked and need no second question.
    bool request_export(const std::string& alternative) {
        if (!has_selection.get()) {
            last_action.set("Select a photo first");
            return false;
        }
        if (alternative == "Copy path") {
            dialog_open.set(true);
            last_action.set("Confirm copying the path");
            return true;
        }
        if (alternative == "Export as PNG")
            last_action.set("Exported " + detail_name.get() + " as PNG");
        else
            last_action.set("Exported " + detail_name.get() + " as JPEG at quality " +
                            std::to_string((int)quality.get()));
        return false;
    }

    std::string confirm_copy() {
        const std::string p = detail_path.get();
        dialog_open.set(false);
        if (p.empty()) {
            last_action.set("Nothing to copy");
            return std::string();
        }
        last_action.set("Copied the path to the clipboard");
        return p;
    }

    void cancel_copy() {
        dialog_open.set(false);
        last_action.set("Copy cancelled");
    }
};

// ---- the rows source --------------------------------------------------------------------------
//
// A real Rows implementation, not a VStack of labels: the list is the component under review,
// and its virtualisation, columns, header and keyboard only exist for a source. The view is
// cached per (scope, query) because Count and Cell are called once per drawn row; rebuilding
// the vector in each call would be a filter pass per column per row.

class PhotoRows : public Rows {
public:
    explicit PhotoRows(Session& s) : s_(&s) {}

    int Count() const override { return (int)view().size(); }
    int Columns() const override { return 3; }

    // Name absorbs the slack and is the column that identifies a row; Album and Size are fixed
    // and are either drawn whole or dropped by SolveColumns, never clipped mid-value.
    ColumnSpec Column(int c) const override {
        ColumnSpec sp;
        switch (c) {
            case 0: sp.title = "Name"; sp.width = 320; sp.min = 150; sp.flex = 1; break;
            case 1: sp.title = "Album"; sp.width = 118; sp.min = 90; break;
            default: sp.title = "Size"; sp.width = 84; sp.min = 72; sp.align = Align::Right; break;
        }
        return sp;
    }
    float ColumnWidth(int c) const override { return Column(c).width; }
    std::string ColumnTitle(int c) const override { return Column(c).title; }

    CellValue Cell(int row, int col) const override {
        const auto& v = view();
        if (row < 0 || row >= (int)v.size()) return CellValue{};
        const Photo& p = *v[(size_t)row];
        CellValue out;
        out.state = CellState::Ready;   // all of this data is in memory: nothing is pending
        if (col == 0) out.text = p.name;
        else if (col == 1) out.text = p.album.empty() ? "Unsorted" : p.album;
        else out.text = human_size(p.bytes);
        return out;
    }

    // The source owns everything it needs, so a scroll is a read and not a fetch. Stated here
    // rather than left empty so a reviewer can see that the no-op is a decision.
    void Want(int, int) override {}

private:
    const std::vector<const Photo*>& view() const {
        const std::string& sc = s_->scope.get();
        const std::string& q = s_->query.get();
        if (sc != cached_scope_ || q != cached_query_) {
            cached_scope_ = sc;
            cached_query_ = q;
            view_ = s_->lib.view(sc, q);
        }
        return view_;
    }

    Session* s_;
    mutable std::string cached_scope_;
    mutable std::string cached_query_;
    mutable std::vector<const Photo*> view_;
};

}   // namespace gallery

#ifndef GALLERY_MODEL_ONLY

using namespace gallery;

int main() {
    Session s;

    Window w("Photo Library", 1000, 700);
    // Its own identity, not the toolkit's: the shell and the deskbar name an app by this, and a
    // gallery that called itself org.aurora.interface-kit would be unreadable in the dock.
    w.SetAppId("org.example.gallery");
    if (!w.ok()) {
        std::fprintf(stderr, "gallery: no Wayland compositor reachable\n");
        return 2;
    }
    s.bind(w);

    // Scripted starts exist so a screenshot run does not have to guess a coordinate. They set
    // the same cells a person would by clicking.
    if (const char* sc = std::getenv("GALLERY_SCOPE")) s.select_scope(sc);
    if (std::getenv("GALLERY_DETAILS")) s.details_open.set(true);
    if (std::getenv("GALLERY_DIALOG")) s.request_export("Copy path");

    PhotoRows rows(s);

    // ---- the sidebar ------------------------------------------------------------------------
    Desc sidebar = Sidebar(s.sidebar_sections(), s.scope, [&s](const std::string& id) {
        s.select_scope(id);
    });
    Desc side_tree = SidebarTree(sidebar);
    // A sidebar navigates on a single click, unlike the main pane, where a click selects and
    // only Enter/double-click opens. The two panes mean different things by a click because
    // the user does.
    OnTreeSelect(side_tree, [&s](const std::string& id) { s.select_scope(id); });

    // ---- the listing ------------------------------------------------------------------------
    Desc list = ListView(&rows, s.cursor).name("photos");
    ListIcons(list);
    OnListSelect(list, [&s](int row) { s.select_row(row); });
    OnListActivate(list, [&s](int row) { s.select_row(row); });

    // ---- the details disclosure -------------------------------------------------------------
    //
    // Labels take Bindings, so the details follow the cursor without rebuilding the pane. The
    // path is the short form; the button copies the full one.
    Desc details_body = VStack({
        HStack({sized(Label("Name"), 62), Label(s.detail_name)}),
        HStack({sized(Label("Album"), 62), Label(s.detail_album)}),
        HStack({sized(Label("Kind"), 62), Label(s.detail_kind)}),
        HStack({sized(Label("Size"), 62), Label(s.detail_size)}),
        Label(s.detail_short_path).style(TextStyle{Ratio(0.92f)}),
        Button("Copy path", [&s] { s.request_export("Copy path"); }, s.has_selection)
            .name("copy-path"),
    }, 0.5f).padding(0.5f);

    Desc rail = VStack({
        TeachingTip("Details follow your selection",
                    "Pick a photo and its name, size and kind appear here. Open Details to copy "
                    "its path.",
                    s.tip_open, [&s] { s.dismiss_tip(); }, "Got it").name("tip"),
        Expander("Details", s.details_open, details_body).name("details"),
        Spacer(),
    }, 1.0f).padding(1.0f);

    // ---- the toolbar ------------------------------------------------------------------------
    //
    // Two leading clusters: navigation (back to the whole library, and focus the filter) and the
    // one compound verb. The breadcrumb takes the flexible slot; the trailing cluster holds the
    // export quality and the overflow menu.
    Desc crumb = Breadcrumb(s.location.as_binding(),
                            [&s, &w](const std::string& p) {
                                s.select_scope(Library::scope_for_path(p));
                                w.FocusNamed("photos");
                            },
                            [&w] { w.FocusNamed("filter"); })
                     .name("location");

    auto overflow_items = [&s]() -> std::vector<MenuItem> {
        const char* tick = "\xe2\x9c\x93";   // a check mark, drawn only when it is true now
        std::vector<MenuItem> it;
        it.push_back(MenuItem{"Show welcome tip", s.tip_open.get() ? tick : "", Effect::Read,
                              true, false, false, [&s] { s.show_tip(); }});
        it.push_back(MenuItem{"Clear filter", "", Effect::Read, s.has_query.get(), false, false,
                              [&s] { s.clear_filter(); }});
        it.push_back(MenuItem::sep());
        it.push_back(MenuItem{"About Photo Library", "", Effect::Read, true, false, false, [&s] {
                                  s.last_action.set("Photo Library 1.0 - built on aurora::ui");
                              }});
        return it;
    };

    Desc bar = Toolbar(
        {
            {ToolbarButton(Glyph::Back, "all-photos",
                           [&s, &w] {
                               s.select_scope("all");
                               w.FocusNamed("photos");
                           }),
             ToolbarButton(Glyph::Search, "find", [&w] { w.FocusNamed("filter"); })},
            {sized(SplitButton("Export", [&s] { s.export_default(); }, s.export_alternatives(),
                               s.has_selection),
                   150)},
        },
        crumb,
        {{Label("Quality"), NumberBox(s.quality, 1, 100, 1,
                                       [&s](double v) {
                                           s.last_action.set("Quality " +
                                                             std::to_string((int)v));
                                       })
                                  .name("quality"),
          MenuButton(Glyph::More, overflow_items).name("overflow")}},
        ToolbarOpts{});

    // ---- the filter row and the two empty states -------------------------------------------
    Desc filter = TextField(s.query, [&s](const std::string& q) { s.set_query(q); },
                            [&w](const std::string&) { w.FocusNamed("photos"); },
                            [&s, &w] {
                                s.clear_filter();
                                w.FocusNamed("photos");
                            },
                            FieldOpts{"Filter photos", false, 0, false})
                       .name("filter");

    Desc hidden_a = sized(Spacer(), 0, 0);
    Desc hidden_b = sized(Spacer(), 0, 0);
    Desc strips = VStack({
        Either(s.no_matches, InfoBar(InfoSeverity::Warning, kNoMatchesMessage), hidden_a),
        Either(s.empty_album, InfoBar(InfoSeverity::Info, kEmptyAlbumMessage), hidden_b),
    }, 0.5f);

    Desc filter_row = HStack({
        sized(filter, 240),
        Label(s.count_text),
        Spacer(),
        Button("Clear", [&s] { s.clear_filter(); }, s.has_query).name("clear-filter"),
    }, 1.0f);

    Desc status_row = HStack({
        Label(s.last_action).name("status"),
        Spacer(),
        Label("Photo Library").style(TextStyle{Ratio(0.92f)}),
    }, 1.0f);

    Desc listing = VStack({
        strips,
        filter_row,
        list.weight(1.0f),
        status_row,
    }, 0.75f).padding(1.0f);

    // Two splits rather than one: the sidebar is a pane and the Details rail is a pane, and the
    // listing is what is left between them. Nesting keeps each divider's ratio independent, so
    // widening the details rail does not move the sidebar.
    Desc main_pane = HSplit(listing, rail, s.rail_ratio, SplitOpts{20, 40, 0.5f, 3});
    Desc body = HSplit(sidebar, main_pane, s.split_ratio, SplitOpts{20, 40, 0.5f, 3});

    Desc page = VStack({
        bar,
        body.weight(1.0f),
    }, 0);

    // ---- the confirmation -------------------------------------------------------------------
    //
    // The dialog is a sibling of the page in an Either, built once. This is the tree's shape
    // rather than a special modal layer: a closed dialog is not measured, drawn, hit-tested or
    // in the focus order, and the pointer cannot reach the page while the scrim is live.
    //
    // Its primary is Affirm::Continue and the label comes from the component's table, so no
    // call site here can ask the user to approve a destructive verb.
    Desc dialog = ContentDialog(
        "Copy this photo's path?",
        "The path is copied to the clipboard. The photo stays where it is; nothing is moved, "
        "renamed or changed.",
        Affirm::Continue,
        [&s, &w] {
            const std::string path = s.confirm_copy();
            // Both the clipboard call and the focus change belong to the next loop pass: the
            // dialog branch is still the laid-out one while this handler runs, so a focus
            // lookup for the list would fail until the Either has switched back.
            w.Defer([&w, path] {
                if (!path.empty()) w.RequestCopy(path);
                w.FocusNamed("photos");
            });
        },
        [&s, &w] {
            s.cancel_copy();
            w.Defer([&w] { w.FocusNamed("photos"); });
        })
        .name("copy-dialog");

    w.SetRoot(Either(s.dialog_open, dialog, page));

    // The list, by name, not FocusFirst: reading order starts at the toolbar, which is right
    // for Tab and wrong for what the arrow keys should talk to on open. A scripted start with
    // the dialog already open has no list in the tree; the dialog asks for its own primary.
    if (!s.dialog_open.get() && !w.FocusNamed("photos"))
        std::fprintf(stderr, "gallery: could not focus the photo list\n");

    const char* fr = std::getenv("GALLERY_FRAMES");
    const char* ms = std::getenv("GALLERY_MAX_MS");
    w.Run(fr ? std::atoi(fr) : 0, false, ms ? std::atoi(ms) : 0);

    if (std::getenv("GALLERY_REPORT")) {
        const Window::Timing t = w.timing();
        std::printf("GALLERY frames=%zu p50=%.3f p95=%.3f max=%.3f over_8ms=%zu\n", t.frames,
                    t.p50, t.p95, t.max, t.over_8ms);
        std::printf("GALLERY scope=%s query=\"%s\" photos=%d selected=%d name=\"%s\"\n",
                    s.scope.get().c_str(), s.query.get().c_str(), rows.Count(), s.cursor.get(),
                    s.detail_name.get().c_str());
        std::printf("GALLERY details=%s tips=%s dialog=%s quality=%d\n",
                    s.details_open.get() ? "open" : "closed", s.tip_open.get() ? "shown" : "dismissed",
                    s.dialog_open.get() ? "open" : "closed", (int)s.quality.get());
        std::printf("GALLERY focusables=%zu focused=\"%s\" keys=%zu\n", w.focusable_count(),
                    w.focus() ? w.focus()->name.c_str() : "", w.keys_seen());
        std::printf("GALLERY last=\"%s\"\n", s.last_action.get().c_str());
        std::fflush(stdout);
    }
    return 0;
}

#endif  // GALLERY_MODEL_ONLY
