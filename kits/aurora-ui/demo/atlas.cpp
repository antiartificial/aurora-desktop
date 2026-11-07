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
#include "../filebrowse.h"

#include <BusClient.h>

#include <dirent.h>
#include <fcntl.h>
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
using namespace aurora::ui::browse;

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
    std::vector<std::string> history, forward;
    State<std::string> status(std::to_string(rows.Count()) + " items");
    State<std::string> filter("");
    State<bool> filter_open(false);
    // The location bar: Ctrl+L, or a click past the last breadcrumb segment. Separate from the
    // filter because they mean opposite things -- one narrows what you are looking at, the other
    // leaves for somewhere else -- and a single box that did both would have to guess which.
    State<std::string> path_text("");
    State<bool> path_edit(false);
    // Dotfiles. Off by default, as every file manager has it, and remembered only for the
    // session: a hidden-files toggle that persisted would surprise someone who turned it on to
    // look at one thing.
    State<bool> show_hidden(false);
    path_text.bind(&w);
    path_edit.bind(&w);
    show_hidden.bind(&w);
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
        if (record && to != path.get()) {
            history.push_back(path.get());
            // A new destination ends the forward trail. Keeping it would offer to go "forward"
            // to a place that is no longer anywhere ahead of you, which is the browser bug
            // everyone has met.
            forward.clear();
        }
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
    // Re-apply the dotfile rule and tell everything that counts rows. Selection is dropped
    // rather than remapped: the row a person had selected may not be in the list any more, and
    // keeping an index that now names a different file is how a Delete goes to the wrong place.
    auto apply_hidden = [&] {
        rows.SetShowHidden(show_hidden.get());
        selected = -1;
        status.set(std::to_string(rows.Count()) + " items");
        w.Invalidate();
    };

    // Back and forward are a pair of stacks and nothing cleverer: going back pushes where you
    // were onto forward, going forward pushes it onto back. Neither records a new destination,
    // which is what stops walking back and forth from filling the history with itself.
    auto go_back = [&] {
        if (history.empty()) return;
        const std::string to = history.back();
        history.pop_back();
        forward.push_back(path.get());
        go(to, false);
    };
    auto go_forward = [&] {
        if (forward.empty()) return;
        const std::string to = forward.back();
        forward.pop_back();
        history.push_back(path.get());
        go(to, false);
    };

    // Opening it fills it with where you ARE and selects the lot, so typing replaces and
    // Backspace edits -- which is what makes it useful both for jumping somewhere unrelated and
    // for trimming a path by a segment.
    auto open_path_bar = [&] {
        path_text.set(path.get());
        path_edit.set(true);
        w.FocusNamed("pathbar");
    };

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
    ListIcons(list);   // a folder should look like a folder, not like a name with a slash
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
    // One place decides what the status bar says about a selection, so the two views cannot
    // disagree about it.
    auto report_selection = [&](const std::vector<int>& v) {
        const int n = rows.Count();
        if (v.size() > 1) {
            off_t total = 0;
            for (int r : v) total += rows.RowSize(r);
            status.set(std::to_string(v.size()) + " of " + std::to_string(n) + " selected  " +
                       human(total));
        } else {
            status.set(std::to_string(n) + " items");
        }
    };
    OnIconsSelect(icons, [&](int row) { load_preview(row); });

    // The grid is a drag source and reports its selection, exactly like the list. A view that
    // could only do half of what the other does would make switching views a decision rather
    // than a preference.
    OnIconsDragPaths(icons, [&](int row) -> std::vector<std::string> {
        std::string name = rows.Cell(row, 0).text;
        if (name.empty()) return {};
        if (name.back() == '/') name.pop_back();
        const std::string base = path.get() == "/" ? "" : path.get();
        return {base + "/" + name};
    });
    OnIconsSelectionChanged(icons, [&](const std::vector<int>& v) { report_selection(v); });

    // The status line says what is selected, because a multi-selection you cannot count is one
    // you cannot trust before pressing Delete.
    OnListSelectionChanged(list, [&](const std::vector<int>& v) { report_selection(v); });

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

    // ---- deleting, which means the trash ---------------------------------------------------------
    //
    // Delete moves to the XDG trash (~/.local/share/Trash), it does not unlink. A file manager
    // that destroys on one keystroke is one people learn to be frightened of, and "are you
    // sure?" is a worse answer than "it is in the trash": a confirmation interrupts every
    // deletion to guard against the rare wrong one, while the trash costs nothing and undoes
    // all of them.
    //
    // The .trashinfo file is what makes it RECOVERABLE rather than merely hidden -- it records
    // where the file came from, and without it a restore has nowhere to go. It is written
    // BEFORE the move, so a crash in between leaves an orphaned info file (harmless) rather
    // than a trashed file nobody can put back.
    auto trash_dir = [&]() -> std::string {
        const char* data = getenv("XDG_DATA_HOME");
        const char* home = getenv("HOME");
        if (data && *data) return std::string(data) + "/Trash";
        return std::string(home ? home : "/tmp") + "/.local/share/Trash";
    };

    auto iso_now = [] {
        const time_t t = time(nullptr);
        struct tm tm {};
        localtime_r(&t, &tm);
        char buf[32];
        strftime(buf, sizeof buf, "%Y-%m-%dT%H:%M:%S", &tm);
        return std::string(buf);
    };

    auto to_trash = [&](const std::vector<std::string>& paths) {
        const std::string base = trash_dir();
        const std::string files = base + "/files", info = base + "/info";
        (void)base;
        int done = 0;
        std::string err;
        for (const std::string& from : paths) {
            const size_t slash = from.find_last_of('/');
            const std::string leaf = slash == std::string::npos ? from : from.substr(slash + 1);
            if (leaf.empty() || leaf == "." || leaf == "..") continue;
            // A real mkdir -p. The first version created only Trash/files/info and assumed
            // their parents existed; with XDG_DATA_HOME pointing somewhere new, creating
            // Trash failed with ENOENT and every delete silently did nothing.
            for (const std::string& d : {files, info}) {
                for (size_t i = 1; i <= d.size(); i++) {
                    if (i < d.size() && d[i] != '/') continue;
                    const std::string part = d.substr(0, i);
                    if (!part.empty() && part != "/") mkdir(part.c_str(), 0700);
                }
            }
            // A name already in the trash does not overwrite what is there. Two files called
            // notes.txt deleted on two days must both be recoverable.
            std::string name = leaf;
            struct stat st;
            for (int n = 1; lstat((files + "/" + name).c_str(), &st) == 0 && n < 10000; n++)
                name = leaf + "." + std::to_string(n);
            const std::string info_path = info + "/" + name + ".trashinfo";
            FILE* f = fopen(info_path.c_str(), "we");
            if (!f) {
                err = std::string("cannot write to the trash: ") + strerror(errno);
                continue;
            }
            fprintf(f, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", from.c_str(), iso_now().c_str());
            fclose(f);
            if (rename(from.c_str(), (files + "/" + name).c_str()) != 0) {
                unlink(info_path.c_str());       // no orphan for a move that did not happen
                err = errno == EXDEV
                          ? std::string("the trash is on another filesystem; that needs a copy, which is a verb")
                          : std::string(strerror(errno));
                continue;
            }
            done++;
        }
        if (done) {
            rows.Open(path.get());
            rows.SetShowHidden(show_hidden.get());
            rows.SetFilter(filter.get());
            status.set(std::to_string(rows.Count()) + " items");
            selected = -1;
        }
        std::string msg;
        if (done) msg = "moved " + std::to_string(done) + (done == 1 ? " item" : " items") + " to the trash";
        if (!err.empty()) msg += (msg.empty() ? "" : "; ") + ("refused: " + err);
        if (!msg.empty()) last_action.set(msg);
        w.Invalidate();
    };

    // What the current selection is, as paths. One place, because Delete, copy and the context
    // menu must all mean the same thing by "what is selected".
    auto selected_paths = [&]() -> std::vector<std::string> {
        std::vector<std::string> out;
        const std::string base = path.get() == "/" ? "" : path.get();
        std::vector<int> rowsel = ListSelection(list);
        if (rowsel.empty() && !list_view.get()) rowsel = IconsSelection(icons);
        if (rowsel.empty() && selected >= 0) rowsel.push_back(selected);
        for (int r : rowsel) {
            std::string n = rows.Cell(r, 0).text;
            if (n.empty()) continue;
            if (n.back() == '/') n.pop_back();
            out.push_back(base + "/" + n);
        }
        return out;
    };

    // A new folder, named by typing rather than by a dialog: it is created as "New Folder" and
    // the list opens its rename field on it, so the keystrokes go straight into the name. A
    // modal asking for a name before the thing exists is a step people have to read.
    auto new_folder = [&] {
        const std::string base = path.get() == "/" ? "" : path.get();
        std::string name = "New Folder";
        struct stat st;
        for (int n = 2; lstat((base + "/" + name).c_str(), &st) == 0 && n < 1000; n++)
            name = "New Folder " + std::to_string(n);
        if (mkdir((base + "/" + name).c_str(), 0755) != 0) {
            last_action.set(std::string("could not create a folder: ") + strerror(errno));
            return;
        }
        rows.Open(path.get());
        rows.SetShowHidden(show_hidden.get());
        rows.SetFilter(filter.get());
        status.set(std::to_string(rows.Count()) + " items");
        // Find it in the new listing and open its name for editing.
        for (int i = 0; i < rows.Count(); i++) {
            std::string n = rows.Cell(i, 0).text;
            if (!n.empty() && n.back() == '/') n.pop_back();
            if (n == name) {
                selected = i;
                w.Defer([&, i] { BeginListRename(list, i); });
                break;
            }
        }
        last_action.set("created " + name);
        w.Invalidate();
    };

    // ---- copy, cut and paste ---------------------------------------------------------------------
    //
    // The clipboard holds PATHS, not bytes: that is what text/uri-list is and what every other
    // file manager puts there, so a copy here can be pasted elsewhere and vice versa.
    //
    // A paste copies on a worker thread. Bytes take time proportional to the file, and a file
    // manager that freezes while copying a video is one people kill. The thread reports through
    // InvalidateThreadsafe, the same way the directory lister already does.
    //
    // Directories are refused rather than copied. A recursive copy needs progress, cancellation
    // and a decision about what to do on a failure half way down a tree -- that is `copy@v1` in
    // a domain, not a keystroke in a widget. Moving one is fine, because a move is a rename.
    struct Pending {
        std::vector<std::string> paths;
        bool cut = false;
    };
    auto clip = std::make_shared<Pending>();

    auto copy_file = [](const std::string& from, const std::string& to) -> std::string {
        const int in = open(from.c_str(), O_RDONLY | O_CLOEXEC);
        if (in < 0) return strerror(errno);
        // O_EXCL: a paste never overwrites. The caller has already picked a free name; if
        // something appeared there since, that is a reason to stop, not to clobber.
        const int out = open(to.c_str(), O_WRONLY | O_CREAT | O_EXCL | O_CLOEXEC, 0644);
        if (out < 0) {
            const std::string e = strerror(errno);
            close(in);
            return e;
        }
        char buf[64 * 1024];
        std::string err;
        for (;;) {
            const ssize_t n = read(in, buf, sizeof buf);
            if (n == 0) break;
            if (n < 0) { err = strerror(errno); break; }
            for (ssize_t off = 0; off < n;) {
                const ssize_t w = write(out, buf + off, (size_t)(n - off));
                if (w <= 0) { err = strerror(errno); break; }
                off += w;
            }
            if (!err.empty()) break;
        }
        close(in);
        close(out);
        if (!err.empty()) unlink(to.c_str());   // no half-written file left behind
        return err;
    };

    auto paste_into = [&, clip, copy_file](const std::string& dest_dir) {
        if (clip->paths.empty()) {
            last_action.set("nothing to paste");
            return;
        }
        const std::vector<std::string> src = clip->paths;
        const bool cut = clip->cut;
        if (cut) clip->paths.clear();           // a cut pastes once
        std::thread([&w, &rows, &path, &status, &last_action, &show_hidden, &filter, src, cut, dest_dir, copy_file] {
            int done = 0;
            std::string err;
            for (const std::string& from : src) {
                const size_t slash = from.find_last_of('/');
                const std::string leaf = slash == std::string::npos ? from : from.substr(slash + 1);
                if (leaf.empty()) continue;
                // A CUT pasted into the folder it came from is nothing at all. Checked before
                // the collision rename below, or the file collides with ITSELF and gets moved
                // to "source copy 2.txt" -- which is what the first version did: cut, paste in
                // place, and the file quietly acquired a new name.
                //
                // A COPY in the same folder is different and is meant to duplicate.
                if (cut && from == dest_dir + "/" + leaf) continue;
                // Never overwrite. "notes.txt" pasted beside itself becomes "notes copy.txt",
                // which is what a person means by pasting into the folder they copied from.
                std::string name = leaf;
                struct stat st;
                if (lstat((dest_dir + "/" + name).c_str(), &st) == 0) {
                    const size_t dot = leaf.find_last_of('.');
                    const std::string stem = dot == std::string::npos || dot == 0 ? leaf : leaf.substr(0, dot);
                    const std::string ext = dot == std::string::npos || dot == 0 ? "" : leaf.substr(dot);
                    name = stem + " copy" + ext;
                    for (int n = 2; lstat((dest_dir + "/" + name).c_str(), &st) == 0 && n < 1000; n++)
                        name = stem + " copy " + std::to_string(n) + ext;
                }
                const std::string to = dest_dir + "/" + name;
                if (from == to) continue;
                if (cut) {
                    if (rename(from.c_str(), to.c_str()) != 0) {
                        err = errno == EXDEV ? "that is a different filesystem; a move across one is a verb"
                                             : strerror(errno);
                        continue;
                    }
                    done++;
                    continue;
                }
                struct stat sf;
                if (lstat(from.c_str(), &sf) == 0 && S_ISDIR(sf.st_mode)) {
                    err = "copying a folder is copy@v1, not a keystroke";
                    continue;
                }
                const std::string e = copy_file(from, to);
                if (!e.empty()) { err = e; continue; }
                done++;
            }
            std::string msg;
            if (done) msg = (cut ? "moved " : "copied ") + std::to_string(done) + (done == 1 ? " item" : " items");
            if (!err.empty()) msg += (msg.empty() ? "" : "; ") + ("refused: " + err);
            last_action.set(msg.empty() ? "nothing to paste" : msg);
            if (done) {
                rows.Open(path.get());
                rows.SetShowHidden(show_hidden.get());
                rows.SetFilter(filter.get());
                status.set(std::to_string(rows.Count()) + " items");
            }
            w.InvalidateThreadsafe();
        }).detach();
    };

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
        items.push_back(MenuItem{"New folder", "Ctrl+Shift+N", Effect::Write, true, false, false,
                                 [&] { w.Defer([&] { new_folder(); }); }});
        // Destructive, so it goes after a separator -- decision 0009's ordering, and the reason
        // the menu has one. It says "the trash" rather than "Delete" because that is what it
        // does, and a menu that overstates what it will do is one people hesitate over.
        items.push_back(MenuItem::sep());
        items.push_back(MenuItem{"Move to trash", "Delete", Effect::Destructive, true, false, false,
                                 [&, full] { w.Defer([&, full] { to_trash({full}); }); }});

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
                    Spacer(),
                    Button("Back", [&] { go_back(); }),
                    Button("Forward", [&] { go_forward(); }),
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
                    // The toolkit's own checkbox rather than another button: a toggle that
                    // looks like a push button cannot show which way it is set.
                    padding(Checkbox("Hidden", &show_hidden, [&](bool) { apply_hidden(); }), 0.4f),
                }),
                0.75f),
        // NO weight: this is inside a VStack, so a weight would make the breadcrumb take a share
        // of the leftover HEIGHT. It did, at 189px, and swallowed the area the list should have
        // had -- a right-click meant for a row hit the breadcrumb instead. Weight is per-axis and
        // the axis is the stack's, which is the kind of thing a layout API should make harder to
        // get wrong than this one currently does.
        padding(named(Breadcrumb(&path, [&](const std::string& to) { go(to, true); },
                                 [&] { open_path_bar(); }),
                      "crumbs"),
                0.25f),
        // The location bar, above the filter. Enter goes there; Escape leaves it alone.
        named(Reveal(&path_edit,
                     padding(named(TextField(&path_text, nullptr,
                                             [&](const std::string& to) {
                                                 path_edit.set(false);
                                                 go(to, true);
                                                 w.FocusNamed("files");
                                             },
                                             [&] {
                                                 path_edit.set(false);
                                                 w.FocusNamed("files");
                                             },
                                             FieldOpts{"go to\u2026", false, 0, true}),
                                   "pathbar"),
                             0.5f),
                     Transition{Motion::Reveal, 160, Curve::EaseOut}),
              "pathbarreveal"),
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
        // A status bar along the bottom, which is where Dolphin and Finder put it and where it
        // has room. It used to sit beside the title, between a 1.35x heading and four buttons,
        // and "3 of 4 selected 9.3 KB" came out as "3 of 4 s" -- a count you cannot read is
        // worse than no count, because it looks like the program is confused rather than the
        // label being short of room.
        padding(HStack({
                    styled(Label(&status), TextStyle{0.9f, false, true}),
                    Spacer(),
                    styled(Label(&last_action), TextStyle{0.85f, false, true}),
                }),
                Insets(0.75f, 0.35f)),
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
    // The keys a file manager is expected to have, wherever the focus happens to be. These are
    // the ones Dolphin, Finder and every browser agree on; an explorer that needed the mouse for
    // "go up" would be one people stop using without being able to say why.
    w.SetKeyHook([&](const au_key& k) -> bool {
        if (!k.pressed) return false;
        const bool alt = (k.mods & AU_MOD_ALT) != 0;
        const bool ctrl = (k.mods & AU_MOD_CTRL) != 0;
        if (alt && k.raw_char == 0x1c) { go_back(); return true; }        // Alt+Left
        if (alt && k.raw_char == 0x1d) { go_forward(); return true; }     // Alt+Right
        if (alt && k.raw_char == 0x1e) { go(parent_of(path.get()), true); return true; }  // Alt+Up
        // Backspace, and only when Ctrl is NOT held: Ctrl+H types 0x08 on every layout, and a
        // "go up" that fired on the hidden-files shortcut would be baffling.
        if (!ctrl && k.raw_char == 0x08) { go(parent_of(path.get()), true); return true; }
        if (k.evdev == 63) {                                             // F5
            go(path.get(), false);
            last_action.set("refreshed");
            return true;
        }
        // A Ctrl shortcut is matched on raw_char, NOT on utf8. utf8 is what the key TYPES with
        // the modifiers applied, so Ctrl+H is 0x08 and Ctrl+A is 0x01; raw_char is the symbol
        // at level 0 of the layout, which is the letter. Matching utf8 means none of these ever
        // fire, which is exactly what happened.
        const uint32_t rc = k.raw_char | 0x20;                           // fold case
        if (ctrl && rc == 'l') {                                         // Ctrl+L: the location
            open_path_bar();
            return true;
        }
        if (ctrl && rc == 'f') {                                         // Ctrl+F: the filter
            filter_open.set(true);
            w.FocusNamed("filter");
            return true;
        }
        if (ctrl && rc == 'h') {                                         // Ctrl+H: dotfiles
            show_hidden.set(!show_hidden.get());
            apply_hidden();
            return true;
        }
        if (ctrl && (k.mods & AU_MOD_SHIFT) && rc == 'n') {               // Ctrl+Shift+N
            new_folder();
            return true;
        }
        if (ctrl && (rc == 'c' || rc == 'x')) {                           // Ctrl+C, Ctrl+X
            const std::vector<std::string> sel = selected_paths();
            if (sel.empty()) return true;
            clip->paths = sel;
            clip->cut = rc == 'x';
            // Also on the system clipboard, so the selection can be pasted into another app.
            w.RequestCopy(DragData::uri_list(sel).bytes);
            last_action.set((clip->cut ? "cut " : "copied ") + std::to_string(sel.size()) +
                            (sel.size() == 1 ? " item" : " items"));
            return true;
        }
        if (ctrl && rc == 'v') {                                          // Ctrl+V
            paste_into(path.get());
            return true;
        }
        // Delete, and Shift+Delete is NOT a shortcut for destroying things here. Everything
        // goes to the trash; a modifier that silently made it permanent would be a trap.
        if (k.evdev == 111) {                                             // KEY_DELETE
            const std::vector<std::string> sel = selected_paths();
            if (getenv("AURORA_UI_TRACE"))
                fprintf(stderr, "atlas: Delete, %zu selected\n", sel.size());
            if (!sel.empty()) to_trash(sel);
            return true;
        }
        return false;
    });

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
