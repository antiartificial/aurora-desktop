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

#include <BusClient.h>

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
#include <map>
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

std::string human(off_t n);
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
        sort_into(fresh);
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
        rebuild_view_locked();
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

    // Called from the WORKER thread when stat results land. Must be safe to call off the main
    // thread; Window::InvalidateThreadsafe is.
    void SetOnChange(std::function<void()> f) { on_change_ = std::move(f); }

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
                // Sorting by size or time means sorting on values the worker is still
                // learning, so the order has to be re-applied as they land. Sorting by name
                // does not, and re-sorting a large directory per batch for nothing would be
                // the expensive mistake.
                if (did) {
                    bool by_stat;
                    {
                        std::lock_guard<std::mutex> g(mu_);
                        by_stat = sort_col_ != 0;
                    }
                    if (by_stat) Resort();
                }
                // Tell the window something landed. Without this the sizes and dates arrived and
                // were never drawn: the listing looked like it had failed to load, and clicking
                // any toolbar button "fixed" it by forcing a frame.
                if (did && on_change_) on_change_();
                if (!did) std::this_thread::sleep_for(std::chrono::milliseconds(4));
            }
        });
    }
    void Stop() {
        stop_ = true;
        if (worker_.joinable()) worker_.join();
    }
    // ---- sorting ----
    //
    // Directories first, always, whichever column and whichever direction. Every file manager
    // does this and the reason is navigational: the folders are the map, and a reversed sort
    // that buried them under three thousand files would make going anywhere a search.
    //
    // Size and time sort on the NUMBER, not on "4.0 KB" and not on a formatted date. Sorting the
    // displayed text is how a listing ends up with 10 B after 9 KB.
    int SortColumn() const override { return sort_col_; }
    bool SortAscending() const override { return sort_asc_; }

    void Sort(int col, bool ascending) override {
        {
            std::lock_guard<std::mutex> g(mu_);
            sort_col_ = col;
            sort_asc_ = ascending;
        }
        Resort();
    }

    // Re-apply the current order to the rows we already have. Used by Sort and by the worker
    // when it has learned enough to change the answer.
    void Resort() {
        std::lock_guard<std::mutex> g(mu_);
        sort_locked(e_);
        rebuild_view_locked();
    }

    int stated() const { std::lock_guard<std::mutex> g(mu_); return stated_; }
    int Total() const { std::lock_guard<std::mutex> g(mu_); return (int)e_.size(); }

private:
    mutable std::mutex mu_;
    std::vector<std::unique_ptr<Entry>> e_;
    std::string path_;
    std::string filter_;
    std::vector<int> view_;        // row -> entry, when a filter is on
    std::function<void()> on_change_;
    int want_first_ = 0, want_last_ = 0, generation_ = 0, stated_ = 0;
    int sort_col_ = 0;
    bool sort_asc_ = true;

    // The filter is a VIEW over the entries, so it has to be rebuilt whenever their order
    // changes -- a sort that left view_ pointing at the old positions would show the right rows
    // in the wrong places, which looks like corruption rather than like a sort.
    void rebuild_view_locked() {
        view_.clear();
        if (filter_.empty()) return;
        for (size_t i = 0; i < e_.size(); i++)
            if (strcasestr(e_[i]->name.c_str(), filter_.c_str())) view_.push_back((int)i);
    }

    void sort_into(std::vector<std::unique_ptr<Entry>>& v) {
        std::lock_guard<std::mutex> g(mu_);
        sort_locked(v);
    }

    void sort_locked(std::vector<std::unique_ptr<Entry>>& v) {
        const int col = sort_col_;
        const bool asc = sort_asc_;
        std::sort(v.begin(), v.end(),
                  [col, asc](const std::unique_ptr<Entry>& a, const std::unique_ptr<Entry>& b) {
                      if (a->dir != b->dir) return a->dir > b->dir;
                      int c = 0;
                      if (col == 1) {
                          // A row whose lstat has not landed sorts as zero rather than jumping
                          // once it does. It will be re-sorted; see the worker.
                          const off_t x = a->stated.load(std::memory_order_acquire) ? a->size : 0;
                          const off_t y = b->stated.load(std::memory_order_acquire) ? b->size : 0;
                          c = x < y ? -1 : (x > y ? 1 : 0);
                      } else if (col == 2) {
                          const time_t x = a->stated.load(std::memory_order_acquire) ? a->mtime : 0;
                          const time_t y = b->stated.load(std::memory_order_acquire) ? b->mtime : 0;
                          c = x < y ? -1 : (x > y ? 1 : 0);
                      }
                      // Name is the column-0 order AND the tie-break for the others, so two
                      // files of the same size do not shuffle between frames.
                      if (c == 0) c = strcasecmp(a->name.c_str(), b->name.c_str());
                      return asc ? c < 0 : c > 0;
                  });
    }
    std::atomic<bool> stop_{false};
    std::thread worker_;
};

}  // namespace

// ---- the sidebar's source ---------------------------------------------------------------------
//
// Directories only, read on demand and cached by path. Three things about it are deliberate:
//
// Expandable is true for EVERY directory, because answering it honestly means reading the
// directory to see whether it holds another one -- which is the work that expanding does. So a
// chevron may open onto nothing. That is the trade every file manager makes, and the alternative
// (reading one level ahead of what the user asked for) costs a readdir per visible row.
//
// Directory-ness comes from the dirent's d_type, not from a stat. On a filesystem that answers
// DT_UNKNOWN we fall back to a stat for that entry alone; the point is that the common case does
// not pay for it. This is the same lesson DirRows measured: readdir on 3-5k entries is 0.9-1.8 ms
// and the lstats are 13-43 ms.
//
// It reads SYNCHRONOUSLY, once per expand, which is a 1-2 ms stall on a local directory and an
// unbounded one on a dead network mount. The TreeSource contract already has the answer -- return
// false for "not yet" and invalidate when the worker lands, which the widget draws as pending --
// so making this async is a change to this class and to nothing else. It is not done because
// nothing here mounts anything, and a thread per sidebar expand to fix a problem we do not have
// would be the kind of weight this whole project is arguing against.
class PlacesTree : public aurora::ui::TreeSource {
public:
    explicit PlacesTree(const std::string& home) : home_(home) {}

    bool Children(const std::string& id, std::vector<aurora::ui::TreeItem>* out) override {
        if (id.empty()) {
            // The roots are PLACES, not the filesystem: a sidebar that starts at / makes the
            // user walk down to where they live every time.
            out->push_back(aurora::ui::TreeItem{home_, "Home", true});
            out->push_back(aurora::ui::TreeItem{"/tmp", "Temporary", true});
            out->push_back(aurora::ui::TreeItem{"/", "Filesystem", true});
            return true;
        }
        std::map<std::string, std::vector<aurora::ui::TreeItem>>::iterator it = cache_.find(id);
        if (it == cache_.end()) it = cache_.emplace(id, read(id)).first;
        *out = it->second;
        return true;
    }

    std::string Parent(const std::string& id) const override {
        // A root place has no parent IN THIS TREE even though its path has one: revealing
        // ~/Documents must open Home, not walk up to / and open the whole chain under
        // Filesystem, which would be a second, duplicate route to the same directory.
        if (id == home_ || id == "/tmp" || id == "/" || id.empty()) return std::string();
        if (id == home_ + "/") return std::string();
        const size_t slash = id.find_last_of('/');
        if (slash == std::string::npos) return std::string();
        return slash == 0 ? std::string("/") : id.substr(0, slash);
    }

    // A directory whose contents changed on disk, or one we want re-read after a rename.
    void Forget(const std::string& id) { cache_.erase(id); }

private:
    std::vector<aurora::ui::TreeItem> read(const std::string& dir) const {
        std::vector<aurora::ui::TreeItem> kids;
        DIR* d = opendir(dir.c_str());
        if (!d) return kids;
        const std::string base = dir == "/" ? std::string() : dir;
        while (const struct dirent* ent = readdir(d)) {
            if (!strcmp(ent->d_name, ".") || !strcmp(ent->d_name, "..")) continue;
            if (ent->d_name[0] == '.') continue;          // dotfiles stay out of the sidebar
            bool is_dir = ent->d_type == DT_DIR;
            if (ent->d_type == DT_UNKNOWN) {
                struct stat st;
                is_dir = lstat((base + "/" + ent->d_name).c_str(), &st) == 0 && S_ISDIR(st.st_mode);
            }
            if (!is_dir) continue;
            kids.push_back(
                aurora::ui::TreeItem{base + "/" + ent->d_name, ent->d_name, true});
            if (kids.size() >= 2000) break;    // a sidebar is not a place to render 50,000 rows
        }
        closedir(d);
        std::sort(kids.begin(), kids.end(),
                  [](const aurora::ui::TreeItem& a, const aurora::ui::TreeItem& b) {
                      return strcasecmp(a.label.c_str(), b.label.c_str()) < 0;
                  });
        return kids;
    }

    std::string home_;
    mutable std::map<std::string, std::vector<aurora::ui::TreeItem>> cache_;
};

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
    // The sidebar. `place` is the tree's selected id and the split's ratio is a State for the
    // reason the widget takes one: it is a setting, and it belongs to whatever the app persists.
    PlacesTree places(getenv("HOME") ? getenv("HOME") : "/");
    State<std::string> place(dir);
    State<float> sidebar(0.28f);
    Desc tree;                      // built below; `go` reveals into it, so it is named first
    bool revealing = false;         // the tree selecting a folder calls go, which reveals: once
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
        // Keep the sidebar on the directory the main pane is showing, whichever way we got here
        // -- a crumb, Up, a double-click, the tree itself. The guard is because the tree's own
        // selection calls go, and go reveals: without it that is a loop through the widget.
        if (tree && !revealing) {
            revealing = true;
            RevealInTree(tree, to);
            revealing = false;
        }
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
    // reason. The verbs come from registry.plumb@v1, so what the menu offers is what an
    // installed app's manifest actually claims -- Atlas does not know what a PNG can have done
    // to it, and should not.
    // ---- the preview pane ----
    //
    // docs/aurora-ui.md section 2: the explorer links NO decoder for image, video, PDF or
    // archive, ever. Decoding an untrusted file is preview@v1 -- effect read, run in a domain
    // holding the file read-only -- and this pane asks for it rather than doing it.
    //
    // Plain text is the exception and it is a real one, not a loophole: rendering bytes with the
    // text stack we already use for filenames involves no parser and no library with a CVE
    // history. It is capped at 256 KiB and 2,000 lines, the caps porpoise uses, and the cap is
    // here because here is where the read happens.
    //
    // Everything else reports Unavailable WITH A REASON, which names the verb as the thing that
    // would supply it. A blank pane would imply the file has nothing to show.
    State<std::string> preview_text("");
    State<std::string> preview_head("");
    preview_text.bind(&w);
    preview_head.bind(&w);
    int preview_row = -1;

    auto looks_like_text = [](const char* buf, size_t n) {
        if (n == 0) return true;
        size_t odd = 0;
        for (size_t i = 0; i < n; i++) {
            const unsigned char c = (unsigned char)buf[i];
            if (c == 0) return false;                       // a NUL settles it
            if (c < 0x09 || (c > 0x0d && c < 0x20)) odd++;  // control bytes that are not whitespace
        }
        return odd * 100 < n * 5;    // under 5% odd bytes: a heuristic, and labelled as one
    };

    auto load_preview = [&](int row) {
        preview_row = row;
        if (row < 0) {
            preview_head.set("");
            preview_text.set("");
            return;
        }
        std::string name = rows.Cell(row, 0).text;
        const bool dir = !name.empty() && name.back() == '/';
        if (dir) name.pop_back();
        const std::string base = path.get() == "/" ? "" : path.get();
        const std::string full = base + "/" + name;

        if (dir) {
            preview_head.set(name + "  (directory)");
            preview_text.set("A directory. Enter to open it.");
            return;
        }
        struct stat st;
        const bool ok = lstat(full.c_str(), &st) == 0;
        char head[512];
        snprintf(head, sizeof head, "%s  (%s)", name.c_str(),
                 ok ? human(st.st_size).c_str() : "unreadable");
        preview_head.set(head);

        FILE* f = fopen(full.c_str(), "rb");
        if (!f) {
            preview_text.set(std::string("Unavailable: cannot open (") + strerror(errno) + ")");
            return;
        }
        static const size_t kCap = 256 * 1024;
        std::string buf;
        buf.resize(kCap);
        const size_t n = fread(&buf[0], 1, kCap, f);
        fclose(f);
        buf.resize(n);
        if (!looks_like_text(buf.data(), std::min<size_t>(n, 1024))) {
            preview_text.set(
                "Unavailable: not text.\n\n"
                "Previewing this type needs preview@v1 -- a verb that runs in a domain holding "
                "the file read-only. Atlas links no image, video, PDF or archive decoder, so it "
                "cannot and will not decode this itself.");
            return;
        }
        // 2,000 lines, so a one-line 256 KiB file does not become 20,000 wrapped lines.
        size_t lines = 0, cut = buf.size();
        for (size_t i = 0; i < buf.size(); i++)
            if (buf[i] == '\n' && ++lines >= 2000) { cut = i; break; }
        if (cut < buf.size()) buf.resize(cut);
        if (n == kCap) buf += "\n\n[truncated at 256 KiB]";
        preview_text.set(buf);
    };

    place.bind(&w);
    sidebar.bind(&w);
    tree = named(TreeView(&places, &place), "places");
    // Selecting in the sidebar navigates. That is single-click-to-navigate, which is what a
    // sidebar is for and what every explorer does there -- unlike the main pane, where a single
    // click selects and only a double-click opens. The two panes mean different things by a
    // click because the user means different things by them.
    OnTreeSelect(tree, [&](const std::string& id) {
        if (!revealing) go(id, true);
    });
    OnTreeActivate(tree, [&](const std::string& id) {
        if (!revealing) go(id, true);
    });

    State<bool> list_view(true);
    State<bool> preview_open(false);
    list_view.bind(&w);
    preview_open.bind(&w);
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
    OnListSelect(list, [&](int row) {
        load_preview(row);
        if (getenv("ATLAS_REPORT"))
            printf("ATLAS preview open=%s head=\"%s\" bytes=%zu first=\"%.40s\"\n",
                   preview_open.get() ? "yes" : "no", preview_head.get().c_str(),
                   preview_text.get().size(), preview_text.get().c_str()),
                fflush(stdout);
    });
    OnIconsSelect(icons, [&](int row) { load_preview(row); });

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

    // ---- dragging files ------------------------------------------------------------------------
    //
    // A drag inside Atlas MOVES, by rename(2). It is deliberately not a copy and deliberately
    // not a cross-filesystem move: rename within one filesystem is atomic and instant, and the
    // moment a move has to read and rewrite bytes it is a long-running operation that wants
    // progress, cancellation and a verb to run it in -- which is `move@v1` in a domain, not a
    // drop handler in a widget. EXDEV therefore says so instead of pretending.
    //
    // Nothing here overwrites. rename(2) is happy to clobber the destination, and a file manager
    // that silently destroyed a file because two folders had one with the same name would be the
    // worst bug in this program.
    auto move_into = [&](const std::string& dest_dir, const std::vector<std::string>& paths) {
        int moved = 0;
        std::string err;
        for (const std::string& from : paths) {
            const size_t slash = from.find_last_of('/');
            const std::string leaf = slash == std::string::npos ? from : from.substr(slash + 1);
            if (leaf.empty() || leaf == "." || leaf == "..") continue;
            const std::string to = (dest_dir == "/" ? "" : dest_dir) + "/" + leaf;
            if (to == from) continue;                    // dropped where it already is
            // Into itself, or into its own subtree: rename(2) returns EINVAL for this, but the
            // message it produces is not one anybody can act on.
            if (dest_dir == from || dest_dir.compare(0, from.size() + 1, from + "/") == 0) {
                err = "a folder cannot be moved into itself";
                continue;
            }
            struct stat st;
            if (lstat(to.c_str(), &st) == 0) {
                err = "\"" + leaf + "\" already exists there";
                continue;
            }
            if (rename(from.c_str(), to.c_str()) != 0) {
                err = errno == EXDEV
                          ? std::string("that is a different filesystem; a copy is move@v1, not a drag")
                          : std::string(strerror(errno));
                continue;
            }
            moved++;
        }
        if (moved) {
            rows.Open(path.get());        // the listing changed under us either way
            rows.SetFilter(filter.get());
            status.set(std::to_string(rows.Count()) + " items");
            selected = -1;
        }
        // Both halves are reported. "3 moved, 1 refused" is the honest answer to a multi-file
        // drop, and silence about the refusal is how a user loses track of a file.
        std::string msg;
        if (moved) msg = "moved " + std::to_string(moved) + (moved == 1 ? " item" : " items");
        if (!err.empty()) msg += (msg.empty() ? "" : "; ") + ("refused: " + err);
        if (!msg.empty()) last_action.set(msg);
        w.Invalidate();
        return moved > 0;
    };

    // A row is a path. Without this the list is not a drag source at all.
    OnListDragPaths(list, [&](int row) -> std::vector<std::string> {
        std::string name = rows.Cell(row, 0).text;
        if (name.empty()) return {};
        if (name.back() == '/') name.pop_back();
        const std::string base = path.get() == "/" ? "" : path.get();
        return {base + "/" + name};
    });

    // A drop on a FOLDER row moves into it; anywhere else in the list means this directory,
    // which for a drag that started here is a no-op and for one from the sidebar is a move out.
    OnListDrop(list, [&](int row, const DragData& d) {
        std::string dest = path.get();
        if (row >= 0) {
            const std::string name = rows.Cell(row, 0).text;
            if (!name.empty() && name.back() == '/') {
                const std::string base = path.get() == "/" ? "" : path.get();
                dest = base + "/" + name.substr(0, name.size() - 1);
            }
        }
        return move_into(dest, d.paths);
    });

    // The sidebar takes drops too: dragging into a folder you can see but are not inside is
    // most of what dragging is for.
    OnTreeDrop(tree, [&](const std::string& id, const DragData& d) {
        const bool ok = move_into(id, d.paths);
        if (ok) places.Forget(id);        // its children changed
        return ok;
    });

    // ---- the real plumber ----
    //
    // The menu's verbs come from registry.plumb@v1: the controller ranks what can be done with a
    // selection and marks each with its effect class (decision 0009). Atlas does not decide what
    // a file can have done to it, and that is the point -- the verbs are whatever apps have
    // declared, the ranking is the plumber's, and the effect is the controller's word.
    //
    // When the bus is not reachable the menu still opens with the operations Atlas can do by
    // itself, and says why the rest are missing. A menu that vanishes because a daemon is down
    // is worse than one that is honest about being short.
    BusClient bus;
    const bool bus_up = bus.Connect() == B_OK;
    if (!bus_up) fprintf(stderr, "atlas: no bus; the menu offers local actions only\n");

    auto plumb = [&](const std::string& full) -> std::vector<MenuItem> {
        std::vector<MenuItem> out;
        if (!bus_up) return out;
        BusEnvelope req;
        req.kind = "request";
        req.to = "registry";        // what controllerd registers as; not its app id
        req.verb = "registry.plumb@v1";
        Json sel = Json::MakeObject();
        sel.set("kind", Json::MakeString("path"));
        sel.set("value", Json::MakeString(full));
        Json p = Json::MakeObject();
        p.set("selection", sel);
        req.payload = p;
        BusEnvelope rep;
        // 2 s: a menu must not wait. This runs on the event thread, so a long call stalls
        // input -- which is survivable for a context menu and is why the budget is short.
        const status_t rc = bus.Call(req, &rep, 2000000);
        if (getenv("AURORA_UI_TRACE"))
            fprintf(stderr, "atlas: plumb(%s) rc=%d payload=%s\n", full.c_str(), (int)rc,
                    rep.payload.Dump().substr(0, 200).c_str());
        if (rc != B_OK) return out;
        // Why there are no app verbs, when there are none. The plumber always says; a menu that
        // just stops after the local actions leaves the user guessing whether it is broken.
        if ((!rep.payload.get("candidates") || rep.payload.get("candidates")->items().empty())) {
            const std::string why = rep.payload.getString("reason", "");
            const std::string mime =
                rep.payload.get("selection")
                    ? rep.payload.get("selection")->getString("mime", "")
                    : std::string();
            MenuItem m;
            m.label = mime.empty() ? "No other actions" : ("No actions for " + mime);
            m.note = why;
            m.enabled = false;
            out.push_back(std::move(m));
            return out;
        }
        const Json* cands = rep.payload.get("candidates");
        if (!cands || !cands->isArray()) return out;
        for (const Json& c : cands->items()) {
            const std::string verb = c.getString("verb", "");
            if (verb.empty()) continue;
            const std::string eff = c.getString("effect", "read");
            MenuItem m;
            m.label = c.getString("summary", verb);
            m.note = verb;
            m.effect = eff == "destructive" ? Effect::Destructive
                       : eff == "external"  ? Effect::External
                       : eff == "write"     ? Effect::Write
                                            : Effect::Read;
            const Json* def = c.get("default");
            m.is_default = def && def->asBool();
            m.enabled = true;
            const std::string app = c.getString("app", "");
            m.run = [&, app, verb, full] {
                last_action.set(verb + " on " + full + " (" + app + ")");
            };
            out.push_back(m);
        }
        return out;
    };

    OnListContext(list, [&](int row, float x, float y) {
        const std::string name = rows.Cell(row, 0).text;
        const bool is_dir = !name.empty() && name.back() == '/';
        std::string leaf = name;
        if (is_dir && !leaf.empty()) leaf.pop_back();
        const std::string base = path.get() == "/" ? "" : path.get();
        const std::string full = base + "/" + leaf;

        // What Atlas can do itself, without asking anyone.
        std::vector<MenuItem> items;
        items.push_back(MenuItem{is_dir ? "Open" : "Open", "Enter", Effect::Read, true, true,
                                 false, [&, row] { w.Defer([&, row] { 
                                     const std::string n2 = rows.Cell(row, 0).text;
                                     if (!n2.empty() && n2.back() == '/') {
                                         const std::string b2 = path.get() == "/" ? "" : path.get();
                                         go(b2 + "/" + n2.substr(0, n2.size() - 1), true);
                                     } else {
                                         last_action.set("open " + n2);
                                     }
                                 }); }});
        items.push_back(MenuItem{"Copy path", "", Effect::Read, true, false, false,
                                 [&, full] { w.RequestCopy(full); last_action.set("copied path"); }});
        items.push_back(MenuItem{"Rename", "F2", Effect::Write, true, false, false,
                                 [&, row] { w.Defer([&, row] { BeginListRename(list, row); }); }});

        // And what the plumber offers. Appended, so a daemon being down shortens the menu
        // rather than emptying it.
        for (MenuItem& m : plumb(full)) items.push_back(std::move(m));
        if (!bus_up) {
            MenuItem m;
            m.label = "More actions";
            m.note = "no bus: controller unreachable";
            m.enabled = false;
            items.push_back(std::move(m));
        }
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
                    Button("Preview", [&] {
                        preview_open.set(!preview_open.get());
                        if (preview_open.get()) load_preview(selected);
                        w.Invalidate();
                    }),
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
        // The sidebar and the rest, with a divider the user drags. The preview pane stays a
        // RevealX rather than becoming a second split, and the difference is real: appearing is
        // not resizing. A pane you toggle wants a transition between absent and present; a pane
        // you size wants a handle. Making the preview a split would have meant animating a ratio
        // to fake the first, and making the sidebar a Reveal would have meant no handle at all.
        weight(HSplit(tree,
                      HStack({
                   weight(Either(&list_view, list, icons), 2),
                   named(Reveal(&preview_open,
                                // 260 units wide, stated. Without it the TextView inside
                                // answers with the whole offer and the file list is placed at
                                // zero -- the pane opened OVER the list.
                                sized(padding(VStack({
                                            styled(Label(&preview_head),
                                                   TextStyle{0.95f, true}),
                                            weight(named(TextView(&preview_text), "preview"), 1),
                                        }, 0.25f),
                                        0.5f), 260),
                                // RevealX: a side pane reveals by WIDTH. A height reveal here
                                // kept its full width while invisible and swallowed the list's
                                // hit test, which is how the axis mistake announced itself.
                                Transition{Motion::RevealX, 160, Curve::EaseOut}),
                         "previewpane"),
                      }, 0),
                      &sidebar, SplitOpts{9, 16, 0.5f, 3}),
               1),
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
    if (getenv("ATLAS_PREVIEW")) {
        preview_open.set(true);
        load_preview(selected);
    }
    if (getenv("ATLAS_OPEN_FILTER")) {
        filter_open.set(true);
        w.FocusNamed("filter");
    }
    rows.SetOnChange([&w] { w.InvalidateThreadsafe(); });
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
        printf("ATLAS preview open=%s head=\"%s\" bytes=%zu first=\"%.48s\"\n",
               preview_open.get() ? "yes" : "no", preview_head.get().c_str(),
               preview_text.get().size(), preview_text.get().c_str());
        printf("ATLAS selected=%d name=%s\n", selected,
               selected >= 0 ? rows.Cell(selected, 0).text.c_str() : "(none)");
        fflush(stdout);
    }
    return 0;
}
