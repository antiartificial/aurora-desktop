// Atlas -- Aurora's file explorer. First light, on aurora::ui. Stage 1 of docs/aurora-ui.md's build order:
// the window, the chrome and the NAMES appear in the first frame; everything else arrives.
//
// The measurement this is built around: readdir over 3,000-5,000 entries costs 0.9-1.8 ms, while
// lstat on every entry costs 13-43 ms -- about thirty times as much. Today's demos/browser does
// both synchronously before showing anything, so a large directory spends up to three frames on
// data that is not needed to draw the first screen. So: readdir on the UI thread, lstat on a
// worker, and only for the rows the list is actually looking at.
//
// ATLAS_DIR=path     where to start (default $HOME)
// ATLAS_FRAMES=n     stop after n frames, for scripted runs
// ATLAS_REPORT=1     print the first-frame timing and what the list drew

#include "../ui.h"

#include <dirent.h>
#include <sys/stat.h>
#include <unistd.h>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cerrno>
#include <cstring>
#define _GNU_SOURCE 1
#include <strings.h>
#include <memory>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

using namespace aurora::ui;

namespace {

struct Entry {
    std::string name;
    bool dir = false;
    // Filled by the worker. `stated` is the only cross-thread flag and is the reason Cell() can
    // read without a lock: it is written after the fields it guards.
    std::atomic<bool> stated{false};
    off_t size = 0;
    time_t mtime = 0;
};

std::string human(off_t n) {
    const char* u[] = {"B", "KB", "MB", "GB", "TB"};
    double v = (double)n;
    int i = 0;
    while (v >= 1024 && i < 4) { v /= 1024; i++; }
    char b[32];
    snprintf(b, sizeof b, i == 0 ? "%.0f %s" : "%.1f %s", v, u[i]);
    return b;
}

std::string when(time_t t) {
    char b[32];
    struct tm tm;
    localtime_r(&t, &tm);
    strftime(b, sizeof b, "%Y-%m-%d %H:%M", &tm);
    return b;
}

class DirRows : public Rows {
public:
    // readdir only. No lstat: that is the worker's job, and it is why the first frame is cheap.
    double Open(const std::string& path) {
        const std::chrono::steady_clock::time_point t0 = std::chrono::steady_clock::now();
        {
            std::lock_guard<std::mutex> g(mu_);
            generation_++;
            e_.clear();
        }
        DIR* d = opendir(path.c_str());
        if (!d) return 0;
        std::vector<std::unique_ptr<Entry>> fresh;
        while (struct dirent* de = readdir(d)) {
            // Hide "." and "..". The Up button and the breadcrumb are the way out of a
            // directory; a ".." row that produces a path containing ".." is the worse of the two
            // affordances and it is what the first run actually did (path=/usr/bin/..).
            if (!strcmp(de->d_name, ".") || !strcmp(de->d_name, "..")) continue;
            auto en = std::make_unique<Entry>();
            en->name = de->d_name;
            // d_type is free when the filesystem gives it; it is NOT a stat.
            en->dir = de->d_type == DT_DIR;
            fresh.push_back(std::move(en));
        }
        closedir(d);
        std::sort(fresh.begin(), fresh.end(),
                  [](const std::unique_ptr<Entry>& a, const std::unique_ptr<Entry>& b) {
                      if (a->dir != b->dir) return a->dir > b->dir;
                      return strcasecmp(a->name.c_str(), b->name.c_str()) < 0;
                  });
        {
            std::lock_guard<std::mutex> g(mu_);
            e_ = std::move(fresh);
            path_ = path;
        }
        return std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0)
            .count();
    }

    // The filter is a view over what readdir already gave us, not a re-read. The names are in
    // memory, so narrowing is a scan; re-running readdir per keystroke would be slower and also
    // wrong, because the directory can change between keystrokes and the list would jump.
    void SetFilter(const std::string& f) {
        std::lock_guard<std::mutex> g(mu_);
        filter_ = f;
        view_.clear();
        if (f.empty()) return;
        for (size_t i = 0; i < e_.size(); i++)
            if (strcasestr(e_[i]->name.c_str(), f.c_str())) view_.push_back((int)i);
    }

    int Count() const override {
        std::lock_guard<std::mutex> g(mu_);
        return filter_.empty() ? (int)e_.size() : (int)view_.size();
    }
    int Columns() const override { return 3; }
    float ColumnWidth(int c) const override { return c == 0 ? 320 : (c == 1 ? 110 : 160); }
    std::string ColumnTitle(int c) const override {
        return c == 0 ? "Name" : (c == 1 ? "Size" : "Modified");
    }

    CellValue Cell(int row, int col) const override {
        std::lock_guard<std::mutex> g(mu_);
        const int i = map(row);
        if (i < 0) return CellValue{};
        const Entry& en = *e_[i];
        if (col == 0) {
            CellValue v;
            v.state = CellState::Ready;        // the name came from readdir: always known
            v.text = en.dir ? en.name + "/" : en.name;
            return v;
        }
        if (!en.stated.load(std::memory_order_acquire)) return CellValue{};   // Pending
        CellValue v;
        v.state = CellState::Ready;
        v.text = col == 1 ? (en.dir ? std::string("--") : human(en.size)) : when(en.mtime);
        return v;
    }

    // The list tells us what it is looking at. A worker that finds the range has moved on simply
    // stops: that is how navigating away cancels, with no cancellation token to forget to check.
    void Want(int first, int last) override {
        std::lock_guard<std::mutex> g(mu_);
        want_first_ = first;
        want_last_ = last;
    }

    // Row index to entry index. Called with the lock held.
    int map(int row) const {
        if (filter_.empty()) return (row >= 0 && row < (int)e_.size()) ? row : -1;
        if (row < 0 || row >= (int)view_.size()) return -1;
        return view_[row];
    }

    void StartWorker() {
        worker_ = std::thread([this] {
            while (!stop_.load()) {
                int f, l, gen;
                std::string path;
                {
                    std::lock_guard<std::mutex> g(mu_);
                    f = want_first_; l = want_last_; gen = generation_; path = path_;
                }
                bool did = false;
                for (int row = f; row <= l; row++) {
                    std::string name;
                    int i = -1;
                    {
                        std::lock_guard<std::mutex> g(mu_);
                        if (gen != generation_) break;          // navigated away: drop the work
                        i = map(row);                           // the worker follows the FILTER
                        if (i < 0) continue;
                        if (e_[i]->stated.load(std::memory_order_relaxed)) continue;
                        name = e_[i]->name;
                    }
                    struct stat st;
                    const std::string full = path + "/" + name;
                    const bool okst = lstat(full.c_str(), &st) == 0;
                    {
                        std::lock_guard<std::mutex> g(mu_);
                        if (gen != generation_ || i >= (int)e_.size()) break;
                        if (okst) { e_[i]->size = st.st_size; e_[i]->mtime = st.st_mtime; }
                        e_[i]->stated.store(true, std::memory_order_release);
                        stated_++;
                    }
                    did = true;
                }
                if (!did) std::this_thread::sleep_for(std::chrono::milliseconds(4));
            }
        });
    }
    void Stop() {
        stop_ = true;
        if (worker_.joinable()) worker_.join();
    }
    int stated() const { std::lock_guard<std::mutex> g(mu_); return stated_; }
    int Total() const { std::lock_guard<std::mutex> g(mu_); return (int)e_.size(); }

private:
    mutable std::mutex mu_;
    std::vector<std::unique_ptr<Entry>> e_;
    std::string path_;
    std::string filter_;
    std::vector<int> view_;        // row -> entry, when a filter is on
    int want_first_ = 0, want_last_ = 0, generation_ = 0, stated_ = 0;
    std::atomic<bool> stop_{false};
    std::thread worker_;
};

}  // namespace

int main() {
    const char* d = getenv("ATLAS_DIR");
    std::string dir = d && *d ? d : (getenv("HOME") ? getenv("HOME") : "/");

    DirRows rows;
    const double readdir_ms = rows.Open(dir);

    Window w("Atlas", 640, 420);
    if (!w.ok()) {
        fprintf(stderr, "atlas: no Wayland compositor reachable\n");
        return 2;
    }

    int selected = -1;
    State<std::string> path(dir);
    std::vector<std::string> history;
    State<std::string> status(std::to_string(rows.Count()) + " items");
    State<std::string> filter("");
    State<bool> filter_open(false);
    status.bind(&w);
    filter.bind(&w);
    filter_open.bind(&w);
    path.bind(&w);

    // The filter narrows as you type. It is not a search box and it does not open a mode: it is
    // one of the three distinct things docs/aurora-ui.md separates -- type-ahead moves the
    // selection, the filter narrows the listing, and a query opens its own window.
    auto refilter = [&](const std::string& f) {
        rows.SetFilter(f);
        selected = -1;
        status.set(f.empty() ? std::to_string(rows.Count()) + " items"
                             : std::to_string(rows.Count()) + " of " +
                                   std::to_string(rows.Total()) + " match \"" + f + "\"");
        if (Node* n = w.FindNamed("files")) w.Invalidate(n->frame);
    };

    // `/` opens the filter and focuses it; Escape clears it, closes it, and gives the keyboard
    // back to the list. The bar is revealed rather than always present, which is what the
    // transition is for and also what keeps the chrome out of the way when it is not in use.
    auto close_filter = [&] {
        filter.set("");
        refilter("");
        filter_open.set(false);
        w.FocusNamed("files");
    };

    // Navigation. A directory's name comes back from readdir with a trailing slash (the list
    // appends it), so entering one is: strip the slash, join, re-open. The row source bumps its
    // generation on Open, which is what cancels the previous directory's outstanding lstats --
    // no token to forget to check.
    // Resolve "." and ".." and collapse repeated slashes, so a path typed or assembled by hand
    // is the same string as one navigated to. Without this /usr/bin/.. and /usr are different
    // places as far as the breadcrumb and the history are concerned.
    auto normalise = [](std::string p) {
        if (p.empty()) return std::string("/");
        std::vector<std::string> out;
        size_t i = 0;
        while (i < p.size()) {
            while (i < p.size() && p[i] == '/') i++;
            const size_t j = p.find('/', i);
            const std::string part =
                p.substr(i, j == std::string::npos ? std::string::npos : j - i);
            if (part == "..") { if (!out.empty()) out.pop_back(); }
            else if (!part.empty() && part != ".") out.push_back(part);
            if (j == std::string::npos) break;
            i = j;
        }
        std::string r;
        for (const std::string& seg : out) r += "/" + seg;
        return r.empty() ? std::string("/") : r;
    };

    auto go = [&](const std::string& raw, bool record) {
        const std::string to = normalise(raw);
        if (to.empty()) return;
        if (record && to != path.get()) history.push_back(path.get());
        const double ms = rows.Open(to);
        (void)ms;
        path.set(to);
        selected = -1;
        rows.SetFilter(filter.get());
        status.set(std::to_string(rows.Count()) + " items");
        w.Invalidate();
    };
    auto parent_of = [](const std::string& p) {
        if (p == "/" || p.empty()) return std::string("/");
        const size_t i = p.find_last_of('/');
        if (i == std::string::npos) return std::string("/");
        return i == 0 ? std::string("/") : p.substr(0, i);
    };

    // The context menu. Its shape is the plumber's (decision 0009): the default first and bold,
    // effects marked, destructive behind a separator, and a disabled item left visible with its
    // reason. The verbs are stubs here -- wiring registry.plumb@v1 is the next piece -- but the
    // ORDER and the MARKING are the part the user is owed, and they are real.
    State<bool> list_view(true);
    list_view.bind(&w);
    auto list = named(ListView(&rows, &selected), "files");
    auto icons = named(IconView(&rows, &selected), "icons");
    State<std::string> last_action("");
    last_action.bind(&w);

    // Enter or a double-click enters a directory. A file is handed to the plumber, which is the
    // next piece; for now it is recorded so the demo can show the intent.
    OnListActivate(list, [&](int row) {
        const std::string name = rows.Cell(row, 0).text;
        if (name.empty()) return;
        if (name.back() == '/') {
            const std::string leaf = name.substr(0, name.size() - 1);
            const std::string base = path.get() == "/" ? "" : path.get();
            go(base + "/" + leaf, true);
        } else {
            last_action.set("open " + name);
        }
    });

    OnIconsActivate(icons, [&](int row) {
        const std::string name = rows.Cell(row, 0).text;
        if (name.empty()) return;
        if (name.back() == '/') {
            const std::string leaf = name.substr(0, name.size() - 1);
            const std::string base = path.get() == "/" ? "" : path.get();
            go(base + "/" + leaf, true);
        } else {
            last_action.set("open " + name);
        }
    });

    // Rename, for real: rename(2) in the current directory. Returning false keeps the field
    // open, which is what a collision or a refusal from the filesystem should do.
    OnListRename(list, [&](int row, const std::string& to) -> bool {
        std::string from = rows.Cell(row, 0).text;
        if (!from.empty() && from.back() == '/') from.pop_back();
        if (to == from) return true;
        if (to.find('/') != std::string::npos) {
            last_action.set("rename refused: a name cannot contain '/'");
            return false;
        }
        const std::string base = path.get() == "/" ? "" : path.get();
        const std::string a = base + "/" + from, b = base + "/" + to;
        // Refuse rather than clobber: renaming onto an existing name would destroy it silently,
        // and rename(2) is happy to do that.
        struct stat st;
        if (lstat(b.c_str(), &st) == 0) {
            last_action.set("rename refused: \"" + to + "\" already exists");
            return false;
        }
        if (rename(a.c_str(), b.c_str()) != 0) {
            last_action.set(std::string("rename failed: ") + strerror(errno));
            return false;
        }
        last_action.set("renamed " + from + " -> " + to);
        rows.Open(path.get());            // re-read: the listing and its order have changed
        rows.SetFilter(filter.get());
        status.set(std::to_string(rows.Count()) + " items");
        w.Invalidate();
        return true;
    });

    OnListContext(list, [&](int row, float x, float y) {
        const std::string name = rows.Cell(row, 0).text;
        const bool is_dir = !name.empty() && name.back() == '/';
        std::vector<MenuItem> items;
        items.push_back(MenuItem{is_dir ? "Open" : "Open with...", "Enter", Effect::Read, true,
                                 true, false, [&, name] { last_action.set("open " + name); }});
        items.push_back(MenuItem{"Copy path", "", Effect::Read, true, false, false,
                                 [&, name] { last_action.set("copy " + name); }});
        items.push_back(MenuItem{"Show provenance", "", Effect::Read, true, false, false,
                                 [&, name] { last_action.set("prov " + name); }});
        items.push_back(MenuItem{"Rename", "F2", Effect::Write, true, false, false,
                                 [&, row] { w.Defer([&, row] { BeginListRename(list, row); }); }});
        items.push_back(MenuItem{"Upload", "", Effect::External, false, false, false, nullptr});
        items.back().note = "no network grant";
        items.push_back(MenuItem{"Move to Trash", "Del", Effect::Destructive, true, false, false,
                                 [&, name] { last_action.set("trash " + name); }});
        // Deferred, not called here: this runs on the event thread, which holds the display
        // mutex, and creating the popup's surface would deadlock against it.
        w.Defer([&w, x, y, items = std::move(items)]() mutable {
            w.ShowMenu(x, y, std::move(items), 4000);
        });
    });

    w.SetRoot(VStack({
        padding(HStack({
                    styled(Bold("Atlas"), TextStyle{1.35f, true}),
                    styled(Label(&status), TextStyle{0.9f, false, true}),
                    Spacer(),
                    Button("Back", [&] {
                        if (history.empty()) return;
                        const std::string to = history.back();
                        history.pop_back();
                        go(to, false);
                    }),
                    Button("Up", [&] { go(parent_of(path.get()), true); }),
                    Button("View", [&] {
                        list_view.set(!list_view.get());
                        w.Invalidate();
                        w.FocusNamed(list_view.get() ? "files" : "icons");
                    }),
                }),
                0.75f),
        // NO weight: this is inside a VStack, so a weight would make the breadcrumb take a share
        // of the leftover HEIGHT. It did, at 189px, and swallowed the area the list should have
        // had -- a right-click meant for a row hit the breadcrumb instead. Weight is per-axis and
        // the axis is the stack's, which is the kind of thing a layout API should make harder to
        // get wrong than this one currently does.
        padding(named(Breadcrumb(&path, [&](const std::string& to) { go(to, true); },
                                 [&] { filter_open.set(true); w.FocusNamed("filter"); }),
                      "crumbs"),
                0.25f),
        // Reveal: height animates 0 <-> natural on a cubic ease-out, 160 ms. The child keeps its
        // own layout and is clipped, so revealing never reflows the field's contents.
        named(Reveal(&filter_open,
                     padding(named(TextField(&filter, refilter, nullptr, close_filter,
                                             FieldOpts{"filter files", false, 0, false}),
                                   "filter"),
                             0.5f),
                     Transition{Motion::Reveal, 160, Curve::EaseOut}),
              "filterbar"),
        weight(Either(&list_view, list, icons), 1),
    }, 0));

    // Focus the LIST on open, by name. FocusFirst would focus the toolbar's button, because
    // tree order is reading order and the toolbar comes first -- correct for Tab, wrong for
    // "what should the arrow keys talk to". The keyboard should work without a click.
    if (!w.FocusNamed("files")) fprintf(stderr, "atlas: could not focus the file list\n");
    // ATLAS_FOCUS=filter starts in the filter field, so a scripted run can type into it.
    if (const char* f = getenv("ATLAS_FOCUS")) w.FocusNamed(f);

    // ATLAS_OPEN_FILTER=1 opens the bar at startup, so a scripted run can measure the reveal
    // without a key injection race.
    // ATLAS_VIEW=icons starts in the grid, so a scripted run does not depend on hitting a
    // toolbar button by guessed coordinates -- which cost two runs before this existed.
    if (const char* v = getenv("ATLAS_VIEW")) {
        if (!strcmp(v, "icons")) {
            list_view.set(false);
            w.FocusNamed("icons");
        }
    }
    if (getenv("ATLAS_OPEN_FILTER")) {
        filter_open.set(true);
        w.FocusNamed("filter");
    }
    rows.StartWorker();

    const char* fr = getenv("ATLAS_FRAMES");
    const bool force = getenv("ATLAS_FORCE") != nullptr;
    const char* ms = getenv("ATLAS_MAX_MS");
    w.Run(fr ? atoi(fr) : 0, force, ms ? atoi(ms) : 0);
    rows.Stop();

    if (getenv("ATLAS_REPORT")) {
        const Window::Timing t = w.timing();
        printf("ATLAS dir=%s entries=%d readdir_ms=%.2f\n", dir.c_str(), rows.Total(), readdir_ms);
        printf("ATLAS frames=%zu draw_ms p50=%.3f p95=%.3f max=%.3f over_8ms=%zu\n",
               t.frames, t.p50, t.p95, t.max, t.over_8ms);
        printf("ATLAS stated=%d of %d (lstat is demand-driven: only what the list looked at)\n",
               rows.stated(), rows.Total());
        const Window::DamageStats ds = w.damage_stats();
        printf("ATLAS damage full=%zu partial=%zu mean_fraction=%.3f\n",
               ds.full, ds.partial, ds.mean_fraction);
        printf("ATLAS focusables=%zu focused=%s keys_seen=%zu\n", w.focusable_count(),
               w.focus() ? "yes" : "no", w.keys_seen());
        printf("ATLAS filter=\"%s\" matched=%d of %d open=%s\n", filter.get().c_str(),
               rows.Count(), rows.Total(), filter_open.get() ? "yes" : "no");
        printf("ATLAS motion_degraded=%zu\n", w.motion_degraded());
        printf("ATLAS last_action=\"%s\"\n", last_action.get().c_str());
        printf("ATLAS path=%s history=%zu view=%s\n", path.get().c_str(), history.size(),
               list_view.get() ? "list" : "icons");
        printf("ATLAS selected=%d name=%s\n", selected,
               selected >= 0 ? rows.Cell(selected, 0).text.c_str() : "(none)");
        fflush(stdout);
    }
    return 0;
}
