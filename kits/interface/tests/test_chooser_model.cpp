// aurora-chooser, headless: no display, no fonts, no compositor.
//
// What is held here is what the caller of the chooser can observe. The Go process that starts it reads exit codes and
// one stdout line; the user reads a Grant button that is on or off and a sentence about what is being granted. So the
// tests drive argv in, a selection in, and read the verdict, the words and the exact bytes out.
#include "../chooser_model.h"

#include <BusJson.h>

#include <dirent.h>
#include <unistd.h>

#include <cstdio>
#include <cstdlib>
#include <string>

using namespace aurora::chooser;

static int fails = 0;
static void eq(const char* what, bool cond) {
    if (!cond) {
        fprintf(stderr, "FAIL %s\n", what);
        fails++;
    }
}
static void eq_str(const char* what, const std::string& got, const std::string& want) {
    if (got != want) {
        fprintf(stderr, "FAIL %s\n  got:  [%s]\n  want: [%s]\n", what, got.c_str(), want.c_str());
        fails++;
    }
}

static ParsedArgs parse(std::initializer_list<const char*> words) {
    std::vector<const char*> v{"aurora-chooser"};
    v.insert(v.end(), words.begin(), words.end());
    return parse_args((int)v.size(), v.data());
}

static bool bad_naming(std::initializer_list<const char*> words, const char* needle) {
    const ParsedArgs r = parse(words);
    return r.status == ParseStatus::Bad && r.error.find(needle) != std::string::npos;
}

static Look at(const char* path, Presence p) { return Look{path, p}; }

int main() {
    // ---- argv -----------------------------------------------------------------------------------------------------
    {
        const ParsedArgs r = parse({"--app", "Notes", "--verb", "open", "--mode", "ro", "--kind", "file"});
        eq("a minimal argv parses", r.status == ParseStatus::Ok);
        eq("app and verb come through", r.args.app == "Notes" && r.args.verb == "open");
        eq("ro and file", r.args.mode == Mode::ReadOnly && r.args.kind == Kind::File);
        eq("start and suggest default to empty", r.args.start.empty() && r.args.suggest.empty());
    }
    {
        const ParsedArgs r = parse({"--kind", "save", "--mode", "rw", "--verb", "export", "--app", "Pix",
                                    "--suggest", "out.png", "--start", "/home/arti/Pictures"});
        eq("flag order does not matter, and every option parses", r.status == ParseStatus::Ok);
        eq("rw and save", r.args.mode == Mode::ReadWrite && r.args.kind == Kind::Save);
        eq("start and suggest are kept", r.args.start == "/home/arti/Pictures" && r.args.suggest == "out.png");
    }
    eq("kind dir parses", parse({"--app", "A", "--verb", "v", "--mode", "ro", "--kind", "dir"}).args.kind == Kind::Dir);

    eq("a missing --app is refused and named", bad_naming({"--verb", "v", "--mode", "ro", "--kind", "file"}, "--app"));
    eq("a missing --verb is refused and named", bad_naming({"--app", "A", "--mode", "ro", "--kind", "file"}, "--verb"));
    eq("a missing --mode is refused and named", bad_naming({"--app", "A", "--verb", "v", "--kind", "file"}, "--mode"));
    eq("a missing --kind is refused and named", bad_naming({"--app", "A", "--verb", "v", "--mode", "ro"}, "--kind"));
    eq("no arguments at all is refused", parse({}).status == ParseStatus::Bad);
    eq("a bad --mode is refused and named",
       bad_naming({"--app", "A", "--verb", "v", "--mode", "readwrite", "--kind", "file"}, "--mode"));
    eq("--mode is case-sensitive: RW is not rw",
       parse({"--app", "A", "--verb", "v", "--mode", "RW", "--kind", "file"}).status == ParseStatus::Bad);
    eq("a bad --kind is refused and named",
       bad_naming({"--app", "A", "--verb", "v", "--mode", "ro", "--kind", "folder"}, "--kind"));
    eq("an unknown option is refused",
       bad_naming({"--app", "A", "--verb", "v", "--mode", "ro", "--kind", "file", "--multi", "1"}, "--multi"));
    eq("a flag with no value is refused",
       bad_naming({"--app", "A", "--verb", "v", "--mode", "ro", "--kind"}, "--kind"));
    eq("a repeated flag is refused, not last-wins",
       bad_naming({"--app", "A", "--app", "B", "--verb", "v", "--mode", "ro", "--kind", "file"}, "twice"));
    eq("a stray positional is refused",
       parse({"--app", "A", "--verb", "v", "--mode", "ro", "--kind", "file", "extra"}).status == ParseStatus::Bad);
    eq("an empty --app is refused",
       parse({"--app", "", "--verb", "v", "--mode", "ro", "--kind", "file"}).status == ParseStatus::Bad);
    eq("--help is its own status, not an error", parse({"--help"}).status == ParseStatus::Help);

    // ---- the exit codes are a contract ----------------------------------------------------------------------------
    eq("exit codes: 0 grant, 3 cancel, 2 no display",
       kExitGrant == 0 && kExitCancel == 3 && kExitNoDisplay == 2);
    eq("no other code collides with those three",
       kExitUsage != kExitGrant && kExitUsage != kExitCancel && kExitUsage != kExitNoDisplay &&
       kExitIo != kExitGrant && kExitIo != kExitCancel && kExitIo != kExitNoDisplay);

    // ---- the verdict: every kind, from values ---------------------------------------------------------------------
    {
        Pick p;
        p.folder = at("/home/arti", Presence::Dir);

        // file
        eq("file: nothing highlighted is not grantable", judge(Kind::File, p).verdict == Verdict::NothingSelected);
        p.row = at("/home/arti/notes.txt", Presence::File);
        Judgement j = judge(Kind::File, p);
        eq("file: a regular file is grantable", j.ok() && j.path == "/home/arti/notes.txt");
        p.row = at("/home/arti/Documents", Presence::Dir);
        eq("file: a folder is not a file", judge(Kind::File, p).verdict == Verdict::NotAFile);
        eq("file: and grants no path", judge(Kind::File, p).path.empty());
        p.row = at("/home/arti/fifo", Presence::Other);
        eq("file: a special file is not a file", judge(Kind::File, p).verdict == Verdict::NotAFile);
        p.row = at("/home/arti/gone.txt", Presence::Missing);
        eq("file: a row that vanished is not grantable", judge(Kind::File, p).verdict == Verdict::Gone);

        // dir
        p.row = Look{};
        j = judge(Kind::Dir, p);
        eq("dir: with nothing highlighted the folder on show is on offer", j.ok() && j.path == "/home/arti");
        p.row = at("/home/arti/Documents", Presence::Dir);
        j = judge(Kind::Dir, p);
        eq("dir: a highlighted folder wins over the folder on show", j.ok() && j.path == "/home/arti/Documents");
        p.row = at("/home/arti/notes.txt", Presence::File);
        eq("dir: a highlighted file is not a folder", judge(Kind::Dir, p).verdict == Verdict::NotAFolder);
        p.row = at("/home/arti/gone", Presence::Missing);
        eq("dir: a highlighted row that vanished is Gone", judge(Kind::Dir, p).verdict == Verdict::Gone);
        p.row = Look{};
        p.folder = at("/home/arti", Presence::Missing);
        eq("dir: a folder on show that is gone is not grantable", judge(Kind::Dir, p).verdict == Verdict::FolderGone);
        p.folder = at("/", Presence::Dir);
        eq("dir: the root is grantable and spelled /", judge(Kind::Dir, p).path == "/");
    }
    {
        Pick p;
        p.folder = at("/home/arti", Presence::Dir);
        p.name = "report.txt";
        p.named = at("/home/arti/report.txt", Presence::Missing);
        Judgement j = judge(Kind::Save, p);
        eq("save: a new name in an existing folder is grantable", j.ok() && j.path == "/home/arti/report.txt");

        p.named = at("/home/arti/report.txt", Presence::File);
        j = judge(Kind::Save, p);
        eq("save: replacing an existing file is grantable (the confirm step is the caller's)",
           j.ok() && j.path == "/home/arti/report.txt");

        p.named = at("/home/arti/report.txt", Presence::Dir);
        eq("save: a folder of that name blocks it", judge(Kind::Save, p).verdict == Verdict::NameTaken);
        p.named = at("/home/arti/report.txt", Presence::Other);
        eq("save: so does a special file", judge(Kind::Save, p).verdict == Verdict::NameTaken);
        p.named = at("/home/arti/report.txt", Presence::Missing);

        p.name = "";
        eq("save: an empty name is not grantable", judge(Kind::Save, p).verdict == Verdict::NameEmpty);
        for (const char* bad : {"a/b", "/etc/passwd", ".", "..", "dir/"}) {
            p.name = bad;
            eq((std::string("save: the name \"") + bad + "\" is refused").c_str(),
               judge(Kind::Save, p).verdict == Verdict::NameInvalid);
        }
        p.name = std::string("a\0b", 3);
        eq("save: a NUL in the name is refused", judge(Kind::Save, p).verdict == Verdict::NameInvalid);

        p.name = ".hidden";
        p.named = at("/home/arti/.hidden", Presence::Missing);
        eq("save: a dotfile name is fine", judge(Kind::Save, p).ok());
        p.name = "..x";
        eq("save: a name that merely starts with dots is fine", judge(Kind::Save, p).ok());

        p.name = "report.txt";
        p.folder = at("/home/arti", Presence::Missing);
        eq("save: a folder that is gone is not grantable", judge(Kind::Save, p).verdict == Verdict::FolderGone);
        p.folder = at("/home/arti/notes.txt", Presence::File);
        eq("save: a file where the folder should be is not grantable", judge(Kind::Save, p).verdict == Verdict::FolderGone);

        p.folder = at("/", Presence::Dir);
        p.named = at("/report.txt", Presence::Missing);
        eq("save: in the root there is no double slash", judge(Kind::Save, p).path == "/report.txt");

        p.folder = at("/home/arti", Presence::Dir);
        p.row = at("/home/arti/Documents", Presence::Dir);
        p.named = at("/home/arti/report.txt", Presence::Missing);
        eq("save: a highlighted row does not change what is saved", judge(Kind::Save, p).path == "/home/arti/report.txt");
    }
    {
        Pick p;
        p.folder = at("/home/arti", Presence::Dir);
        p.row = at("/home/arti/caf\xE9", Presence::File);        // Latin-1 e-acute: not UTF-8
        eq("a file name that is not UTF-8 is not grantable, rather than granted wrongly",
           judge(Kind::File, p).verdict == Verdict::Unrepresentable);
        p.row = at("/home/arti/caf\xC3\xA9", Presence::File);
        eq("the same name in UTF-8 is", judge(Kind::File, p).ok());
        p.row = at("relative/notes.txt", Presence::File);
        eq("a relative path is never granted", judge(Kind::File, p).verdict == Verdict::Unrepresentable);
    }
    {
        bool every = true;
        for (Verdict v : {Verdict::NothingSelected, Verdict::Gone, Verdict::NotAFile, Verdict::NotAFolder,
                          Verdict::FolderGone, Verdict::NameEmpty, Verdict::NameInvalid, Verdict::NameTaken,
                          Verdict::Unrepresentable})
            every = every && !describe(v).empty();
        eq("every reason Grant can be off has words", every);
        eq("Ok has none", describe(Verdict::Ok).empty());
    }

    // ---- the verdict against the real disk ------------------------------------------------------------------------
    {
        char tmpl[] = "/tmp/chooser-model-XXXXXX";
        const std::string root = mkdtemp(tmpl) ? tmpl : "";
        eq("a scratch directory exists", !root.empty());
        const std::string file = root + "/a.txt", dir = root + "/sub", link_dir = root + "/to-sub",
                          link_file = root + "/to-a", fifo = root + "/pipe", dangling = root + "/dangling";
        FILE* f = fopen(file.c_str(), "w");
        if (f) { fputs("x", f); fclose(f); }
        mkdir(dir.c_str(), 0700);
        (void)!symlink("sub", link_dir.c_str());
        (void)!symlink("a.txt", link_file.c_str());
        (void)!symlink("nowhere", dangling.c_str());
        mkfifo(fifo.c_str(), 0600);

        eq("a regular file is a file", presence_of(file) == Presence::File);
        eq("a directory is a directory", presence_of(dir) == Presence::Dir);
        eq("a symlink to a directory is a directory", presence_of(link_dir) == Presence::Dir);
        eq("a symlink to a file is a file", presence_of(link_file) == Presence::File);
        eq("a dangling symlink is missing", presence_of(dangling) == Presence::Missing);
        eq("a fifo is neither", presence_of(fifo) == Presence::Other);
        eq("nothing is missing", presence_of(root + "/nope") == Presence::Missing);

        eq("end to end: a file row grants kind file", judge(Kind::File, make_pick(root, file, "")).path == file);
        eq("end to end: a folder row does not grant kind file",
           !judge(Kind::File, make_pick(root, dir, "")).ok());
        eq("end to end: a symlinked folder grants kind dir", judge(Kind::Dir, make_pick(root, link_dir, "")).ok());
        eq("end to end: a fifo grants nothing", !judge(Kind::File, make_pick(root, fifo, "")).ok() &&
                                                 !judge(Kind::Dir, make_pick(root, fifo, "")).ok());
        eq("end to end: save as a new name", judge(Kind::Save, make_pick(root, "", "new.txt")).path == root + "/new.txt");
        eq("end to end: save over a file", judge(Kind::Save, make_pick(root, "", "a.txt")).path == file);
        eq("end to end: save over a folder is refused", !judge(Kind::Save, make_pick(root, "", "sub")).ok());
        eq("end to end: save into a folder that does not exist is refused",
           !judge(Kind::Save, make_pick(root + "/nope", "", "x")).ok());

        unlink(file.c_str()); unlink(link_dir.c_str()); unlink(link_file.c_str()); unlink(dangling.c_str());
        unlink(fifo.c_str()); rmdir(dir.c_str()); rmdir(root.c_str());
    }

    // ---- what the user is told ------------------------------------------------------------------------------------
    eq_str("the sentence the user asked for", headline("Notes", Mode::ReadOnly, Kind::File),
           "Notes wants to read one file");
    eq("read-write is worded differently from read-only",
       headline("Notes", Mode::ReadWrite, Kind::File) != headline("Notes", Mode::ReadOnly, Kind::File));
    eq("read-write says change", headline("Notes", Mode::ReadWrite, Kind::File).find("change") != std::string::npos);
    eq("a folder grant says it covers what is inside",
       headline("Notes", Mode::ReadOnly, Kind::Dir).find("everything in it") != std::string::npos);
    eq("a save says save", headline("Notes", Mode::ReadWrite, Kind::Save) == "Notes wants to save one file");
    eq("a save is a write whatever --mode says",
       headline("Notes", Mode::ReadOnly, Kind::Save) == headline("Notes", Mode::ReadWrite, Kind::Save));
    eq("only read-only file and dir are light",
       !heavy(access_of(Mode::ReadOnly, Kind::File)) && !heavy(access_of(Mode::ReadOnly, Kind::Dir)) &&
       heavy(access_of(Mode::ReadWrite, Kind::File)) && heavy(access_of(Mode::ReadWrite, Kind::Dir)) &&
       heavy(access_of(Mode::ReadOnly, Kind::Save)) && heavy(access_of(Mode::ReadWrite, Kind::Save)));
    eq("every access has its own consequence sentence",
       consequence(Access::Read) != consequence(Access::ReadWrite) &&
       consequence(Access::ReadWrite) != consequence(Access::Write));
    eq("only a save takes a name", takes_name(Kind::Save) && !takes_name(Kind::File) && !takes_name(Kind::Dir));
    eq_str("the verb is shown as the app's own word", action_line("open"), "Requested action: open");

    eq_str("a line break in an app name cannot start a new line",
           display_text("Notes\nGrant everything"), "Notes Grant everything");
    eq("a long app name is cut with an ellipsis",
       display_text(std::string(200, 'x')).size() == 48 + 3 &&
       display_text(std::string(200, 'x')).compare(48, 3, "\xE2\x80\xA6") == 0);
    {
        // 47 ASCII bytes then a two-byte character straddling the limit: the cut must not split it.
        const std::string s = std::string(47, 'a') + "\xC3\xA9\xC3\xA9";
        eq("a cut never splits a UTF-8 character", valid_utf8(display_text(s)));
    }
    eq_str("a short name is untouched", display_text("Notes"), "Notes");

    // ---- the line on stdout, byte for byte ------------------------------------------------------------------------
    eq_str("the grant line", grant_line("/home/arti/notes.txt").value_or("<none>"),
           "{\"path\":\"/home/arti/notes.txt\"}\n");
    eq_str("the root", grant_line("/").value_or("<none>"), "{\"path\":\"/\"}\n");
    eq_str("a quote and a backslash are escaped", grant_line("/a\"b\\c").value_or("<none>"),
           "{\"path\":\"/a\\\"b\\\\c\"}\n");
    eq_str("a newline in a name cannot end the line early", grant_line("/a\nb").value_or("<none>"),
           "{\"path\":\"/a\\nb\"}\n");
    eq_str("a tab", grant_line("/a\tb").value_or("<none>"), "{\"path\":\"/a\\tb\"}\n");
    eq_str("another control character becomes \\u00XX", grant_line("/a\x01z").value_or("<none>"),
           "{\"path\":\"/a\\u0001z\"}\n");
    eq_str("UTF-8 passes through as UTF-8", grant_line("/caf\xC3\xA9/\xE6\x97\xA5").value_or("<none>"),
           "{\"path\":\"/caf\xC3\xA9/\xE6\x97\xA5\"}\n");
    eq_str("a slash is not escaped", grant_line("/a/b").value_or("<none>"), "{\"path\":\"/a/b\"}\n");
    eq("a path that is not UTF-8 produces no line", !grant_line("/caf\xE9").has_value());
    eq("an overlong encoding produces no line", !grant_line("/\xC0\xAF").has_value());
    eq("a UTF-16 surrogate produces no line", !grant_line("/\xED\xA0\x80").has_value());
    eq("a truncated sequence produces no line", !grant_line("/\xE6\x97").has_value());
    eq("a relative path produces no line", !grant_line("notes.txt").has_value());
    eq("an empty path produces no line", !grant_line("").has_value());
    eq("a NUL produces no line", !grant_line(std::string("/a\0b", 4)).has_value());
    {
        // One line, ending in exactly one newline, and a JSON reader gives the path back.
        const char* paths[] = {"/home/arti/notes.txt", "/a\"b\\c", "/a\nb\tc", "/a\x01z", "/caf\xC3\xA9/\xE6\x97\xA5",
                               "/emoji/\xF0\x9F\x8C\x85", "/", "/a b/c  d", "/{\"path\":\"/etc/shadow\"}"};
        for (const char* path : paths) {
            const std::string line = grant_line(path).value_or("");
            eq("exactly one line", !line.empty() && line.back() == '\n' &&
                                       line.find('\n') == line.size() - 1);
            Json j;
            std::string err;
            const bool parsed = Json::Parse(line, &j, &err);
            eq("a JSON reader accepts it", parsed);
            eq("with exactly one member, path", parsed && j.isObject() && j.members().size() == 1 && j.get("path"));
            eq("and reads back the path unchanged", parsed && j.getString("path") == path);
        }
    }

    // The verdict and the wire agree: whatever judge calls Ok has a line, and the line carries the judged path.
    {
        Pick p;
        p.folder = at("/tmp", Presence::Dir);
        p.name = "x \"quoted\".txt";
        p.named = at("/tmp/x \"quoted\".txt", Presence::Missing);
        const Judgement j = judge(Kind::Save, p);
        const std::optional<std::string> line = grant_line(j.path);
        eq("a granted save has a line", j.ok() && line.has_value());
        Json parsed;
        eq("that carries the judged path", line && Json::Parse(*line, &parsed) &&
                                               parsed.getString("path") == "/tmp/x \"quoted\".txt");
    }

    // ---- paths ----------------------------------------------------------------------------------------------------
    eq_str("normalise collapses slashes and dots", normalise_path("//usr/./bin//"), "/usr/bin");
    eq_str("normalise resolves ..", normalise_path("/usr/bin/.."), "/usr");
    eq_str("normalise cannot climb above the root", normalise_path("/../.."), "/");
    eq_str("parent of a top-level name is the root", parent_path("/home"), "/");
    eq_str("parent of the root is the root", parent_path("/"), "/");
    eq_str("parent of a nested path", parent_path("/home/arti"), "/home");

    if (fails == 0) printf("test_chooser_model: ok\n");
    return fails ? 1 : 0;
}
