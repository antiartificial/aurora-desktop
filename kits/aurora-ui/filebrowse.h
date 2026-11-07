// The file-browsing model Atlas and the capability chooser share: DirRows (a directory as Rows, readdir on the
// caller's thread and lstat on a worker) and PlacesTree (the sidebar's TreeSource). Moved out of
// demo/atlas.cpp unchanged; the comments on both classes record measurements and bugs and travel with them.
// Header-only because the two binaries that use it are the only callers.
#pragma once

#include "ui.h"

#include <dirent.h>
#include <sys/stat.h>
#include <unistd.h>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cstdio>
#include <cstring>
#include <ctime>
#include <functional>
#include <map>
#include <memory>
#include <mutex>
#include <string>
#include <strings.h>
#include <thread>
#include <vector>

namespace aurora {
namespace ui {
namespace browse {

struct Entry {
    std::string name;
    bool dir = false;
    bool hidden = false;
    // Filled by the worker. `stated` is the only cross-thread flag and is the reason Cell() can
    // read without a lock: it is written after the fields it guards.
    std::atomic<bool> stated{false};
    off_t size = 0;
    time_t mtime = 0;
};

inline std::string human(off_t n);
inline std::string human(off_t n) {
    const char* u[] = {"B", "KB", "MB", "GB", "TB"};
    double v = (double)n;
    int i = 0;
    while (v >= 1024 && i < 4) { v /= 1024; i++; }
    char b[32];
    snprintf(b, sizeof b, i == 0 ? "%.0f %s" : "%.1f %s", v, u[i]);
    return b;
}

inline std::string when(time_t t) {
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
            en->hidden = de->d_name[0] == '.';
            // Read ALWAYS, filtered on display. Hiding at readdir would mean re-reading the
            // directory every time the toggle flips, and re-reading is the expensive part.
            fresh.push_back(std::move(en));
        }
        closedir(d);
        sort_into(fresh);
        {
            std::lock_guard<std::mutex> g(mu_);
            e_ = std::move(fresh);
            path_ = path;
            // The view indexes into e_, so a new directory means a new view. Before the hidden
            // rule existed, an empty filter meant the view was unused and this did not matter;
            // now the view is in use whenever dotfiles are off, which is the default, and
            // leaving it stale made every directory open EMPTY.
            rebuild_view_locked();
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

    /// Show dotfiles, or not. Ctrl+H, as in every file manager.
    void SetShowHidden(bool on) {
        std::lock_guard<std::mutex> g(mu_);
        show_hidden_ = on;
        rebuild_view_locked();
    }
    bool ShowHidden() const {
        std::lock_guard<std::mutex> g(mu_);
        return show_hidden_;
    }

    int Count() const override {
        std::lock_guard<std::mutex> g(mu_);
        return (filter_.empty() && show_hidden_) ? (int)e_.size() : (int)view_.size();
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
        if (filter_.empty() && show_hidden_) return (row >= 0 && row < (int)e_.size()) ? row : -1;
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

    // The size of one row, for a selection total. Zero when the lstat has not landed, which is
    // honest: a running total that counted unknown rows as something would be a wrong number
    // shown confidently.
    off_t RowSize(int row) const {
        std::lock_guard<std::mutex> g(mu_);
        const int i = map(row);
        if (i < 0) return 0;
        return e_[i]->stated.load(std::memory_order_acquire) && !e_[i]->dir ? e_[i]->size : 0;
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
    bool show_hidden_ = false;

    // The filter is a VIEW over the entries, so it has to be rebuilt whenever their order
    // changes -- a sort that left view_ pointing at the old positions would show the right rows
    // in the wrong places, which looks like corruption rather than like a sort.
    void rebuild_view_locked() {
        view_.clear();
        // The view is on whenever EITHER a text filter or the hidden rule excludes something,
        // so the two cannot disagree about which rows exist.
        if (filter_.empty() && show_hidden_) return;
        for (size_t i = 0; i < e_.size(); i++) {
            if (!show_hidden_ && e_[i]->hidden) continue;
            if (!filter_.empty() && !strcasestr(e_[i]->name.c_str(), filter_.c_str())) continue;
            view_.push_back((int)i);
        }
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


}  // namespace browse
}  // namespace ui
}  // namespace aurora
