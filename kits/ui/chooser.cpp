// aurora-chooser -- the graphical file picker behind the capability model's grants.
//
//   aurora-chooser --app A --verb V --mode ro|rw --kind file|dir|save [--start DIR] [--suggest NAME]
//
// One line on stdout, {"path":"/abs/path"}, on grant, and nothing else. Exit 0 grant, 3 cancel, 2 no display. The whole
// contract, and everything that can be decided without a window, is in chooser_model.h and is tested headless; this file
// is the window around it.
//
// A picker holds the user's authority for the length of one decision, so it has no verbs beyond choosing: no delete, no
// trash, no rename, no paste, no context menu, no drag. DirRows and PlacesTree come from the file manager's own model and
// the list is made single-select, so what is highlighted is exactly what Grant would bind.

#include "ui.h"
#include "filebrowse.h"
#include "chooser_model.h"

#include <unistd.h>

#include <cerrno>
#include <cstdio>
#include <cstdlib>
#include <optional>
#include <string>

using namespace aurora::ui;
using namespace aurora::ui::browse;
using namespace aurora::chooser;

namespace {

constexpr uint32_t kKeyEsc = 1, kKeyEnter = 28, kKeyKpEnter = 96;

bool write_all(int fd, const std::string& s) {
    size_t at = 0;
    while (at < s.size()) {
        const ssize_t n = write(fd, s.data() + at, s.size() - at);
        if (n < 0 && errno == EINTR) continue;
        if (n <= 0) return false;
        at += (size_t)n;
    }
    return true;
}

const char* title_for(Kind k) {
    switch (k) {
        case Kind::File: return "Choose a file";
        case Kind::Dir: return "Choose a folder";
        case Kind::Save: return "Save a file";
    }
    return "Choose";
}

}  // namespace

int main(int argc, char** argv) {
    // Stdout belongs to the grant line. Keep a private copy of the real one and point fd 1 at stderr, so that nothing a
    // library prints on a path nobody thought of can ever reach the process that parses it.
    const int out_fd = dup(STDOUT_FILENO);
    if (out_fd >= 0) dup2(STDERR_FILENO, STDOUT_FILENO);

    const ParsedArgs parsed = parse_args(argc, argv);
    if (parsed.status != ParseStatus::Ok) {
        if (parsed.status == ParseStatus::Bad) fprintf(stderr, "aurora-chooser: %s\n", parsed.error.c_str());
        fputs(usage(), stderr);
        return kExitUsage;
    }
    const Args& args = parsed.args;
    const Kind kind = args.kind;

    Window w((std::string(title_for(kind)) + " for " + display_text(args.app, 32)).c_str(), 760, 520);
    if (!w.ok()) {
        fprintf(stderr, "aurora-chooser: no Wayland compositor reachable\n");
        return kExitNoDisplay;
    }

    const char* home_env = getenv("HOME");
    const std::string home = home_env && *home_env ? normalise_path(home_env) : std::string("/");
    std::string start = presence_of(home) == Presence::Dir ? home : std::string("/");
    if (!args.start.empty()) {
        if (args.start[0] == '/' && presence_of(args.start) == Presence::Dir) start = normalise_path(args.start);
        else fprintf(stderr, "aurora-chooser: --start %s is not an existing absolute directory\n", args.start.c_str());
    }

    DirRows rows;
    rows.Open(start);

    int selected = -1;
    State<std::string> path(start), place(start), name(args.suggest), message("");
    State<float> sidebar(0.27f);
    State<bool> can_grant(false), show_hidden(false);
    for (auto* s : {&path, &place, &name, &message}) s->bind(&w);
    sidebar.bind(&w);
    can_grant.bind(&w);
    show_hidden.bind(&w);

    PlacesTree places(home);
    Desc tree;
    bool revealing = false;
    std::optional<std::string> granted;

    // The leaf of a row, or empty. A directory's cell carries a trailing slash and no file name can.
    auto row_leaf = [&](int row) {
        std::string t = row < 0 ? std::string() : rows.Cell(row, 0).text;
        if (!t.empty() && t.back() == '/') t.pop_back();
        return t;
    };
    auto row_path = [&](int row) {
        const std::string leaf = row_leaf(row);
        return leaf.empty() ? std::string() : join_path(path.get(), leaf);
    };
    auto pick = [&] { return make_pick(path.get(), row_path(selected), name.get()); };

    // The one place the screen learns what the selection is worth. The button and the sentence both come from here.
    auto refresh = [&] {
        const Judgement j = judge(kind, pick());
        can_grant.set(j.ok());
        message.set(j.ok() ? "Selected: " + display_text(j.path, 200) : describe(j.verdict));
    };

    auto go = [&](const std::string& raw) {
        const std::string to = normalise_path(raw);
        if (presence_of(to) != Presence::Dir) return;
        rows.Open(to);
        path.set(to);
        selected = -1;
        if (tree && !revealing) {
            revealing = true;
            RevealInTree(tree, to);
            revealing = false;
        }
        refresh();
        w.Invalidate();
    };

    auto cancel = [&] { w.Quit(); };

    // Judged again from the disk at the moment of the grant, not trusted from when the button last lit up.
    auto try_grant = [&] {
        const Judgement j = judge(kind, pick());
        if (!j.ok()) {
            refresh();
            return;
        }
        granted = j.path;
        w.Quit();
    };

    tree = named(TreeView(&places, place), "places");
    OnTreeSelect(tree, [&](const std::string& id) { if (!revealing) go(id); });
    OnTreeActivate(tree, [&](const std::string& id) { if (!revealing) go(id); });

    Desc list = named(ListView(&rows, &selected), "files");
    ListIcons(list);   // a folder should look like a folder, not like a name with a slash
    ListSingleSelect(list);
    OnListSelect(list, [&](int row) {
        // Clicking a file in a save dialog proposes its name, which is how replacing a file is done.
        if (takes_name(kind) && presence_of(row_path(row)) == Presence::File) name.set(row_leaf(row));
        refresh();
    });
    OnListActivate(list, [&](int row) {
        const std::string p = row_path(row);
        if (p.empty()) return;
        if (presence_of(p) == Presence::Dir) go(p);
        else try_grant();
    });

    const Access access = access_of(args.mode, kind);
    const au_color amber{255, 196, 92, 255};
    const TextStyle weighty{1.0f, true, false, true, amber};
    const TextStyle grant_style = heavy(access) ? weighty : TextStyle{1.0f, true};

    Desc headline_text = heavy(access)
        ? styled(Bold(headline(args.app, args.mode, kind)), TextStyle{1.3f, true, false, true, amber})
        : styled(Bold(headline(args.app, args.mode, kind)), TextStyle{1.3f, true});
    Desc consequence_text = heavy(access) ? styled(Label(consequence(access)), weighty)
                                          : styled(Label(consequence(access)), TextStyle{0.95f});

    std::vector<Desc> column;
    column.push_back(padding(VStack({headline_text, consequence_text,
                                     styled(Label(action_line(args.verb)), TextStyle{0.85f, false, true})}, 0.25f),
                             Insets(1, 0.75f)));
    column.push_back(padding(HStack({Button("Up", [&] { go(parent_path(path.get())); }),
                                     padding(Checkbox("Hidden files", &show_hidden, [&](bool on) {
                                                 rows.SetShowHidden(on);
                                                 selected = -1;
                                                 refresh();
                                                 w.Invalidate();
                                             }), 0.4f),
                                     Spacer()}),
                             Insets(1, 0.25f)));
    column.push_back(padding(named(Breadcrumb(path, [&](const std::string& to) { go(to); }), "crumbs"), 0.25f));
    column.push_back(weight(HSplit(tree, list, sidebar, SplitOpts{9, 16, 0.5f, 3}), 1));
    if (takes_name(kind))
        column.push_back(padding(HStack({Label("Name"),
                                         weight(named(TextField(name, [&](const std::string&) { refresh(); },
                                                                [&](const std::string&) { try_grant(); }, cancel,
                                                                FieldOpts{"file name", false, 0, true}),
                                                      "name"), 1)}, 0.75f),
                                 Insets(1, 0.35f)));
    column.push_back(padding(styled(Label(message), TextStyle{0.9f, false, true}), Insets(1, 0.35f)));
    column.push_back(padding(HStack({styled(Label("Enter opens a folder or grants a file   Ctrl+Enter grants   Esc cancels"),
                                            TextStyle{0.8f}),
                                     Spacer(),
                                     named(Button("Cancel", cancel), "cancel"),
                                     named(styled(Button("Grant", try_grant, &can_grant), grant_style), "grant")}),
                             Insets(1, 0.5f)));
    w.SetRoot(VStack(column, 0));

    revealing = true;
    RevealInTree(tree, start);
    revealing = false;
    refresh();
    w.FocusNamed(takes_name(kind) ? "name" : "files");

    w.SetKeyHook([&](const au_key& k) -> bool {
        if (!k.pressed) return false;
        if (k.evdev == kKeyEsc) { cancel(); return true; }
        if ((k.evdev == kKeyEnter || k.evdev == kKeyKpEnter) && (k.mods & AU_MOD_CTRL)) {
            try_grant();
            return true;
        }
        // With the name field focused the arrows would otherwise do nothing useful, and the list is where the
        // selection is.
        if ((k.raw_char == 0x1e || k.raw_char == 0x1f) && w.focus() && w.focus() == w.FindNamed("name")) {
            list->OnKey(k);
            // What Window::HandleKey does after a focused widget takes a key: the list asks for less than its frame.
            std::vector<Rect> hint;
            if (list->DamageHint(&hint)) for (const Rect& r : hint) w.Invalidate(r);
            else w.Invalidate(list->frame);
            return true;
        }
        return false;
    });

    rows.SetOnChange([&w] { w.InvalidateThreadsafe(); });
    rows.StartWorker();
    w.Run();
    rows.Stop();

    if (!granted) return kExitCancel;
    const std::optional<std::string> line = grant_line(*granted);
    if (!line || out_fd < 0 || !write_all(out_fd, *line)) {
        fprintf(stderr, "aurora-chooser: could not write the grant to stdout\n");
        return kExitIo;
    }
    close(out_fd);
    return kExitGrant;
}
