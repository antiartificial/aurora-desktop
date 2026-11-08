// What aurora-chooser decides, with no window in sight: the arguments it accepts, what a selection is worth for each
// kind of grant, the words that tell the user what they are granting, and the one line it prints on stdout.
//
// A Go process starts the chooser and parses that line, so every function here that touches the contract is pinned by
// tests/test_chooser_model.cpp, which runs without a display, fonts or a compositor.
//
//   argv:   --app A --verb V --mode ro|rw --kind file|dir|save [--start DIR] [--suggest NAME]
//   stdout: one line, {"path":"/abs/path"}, on grant. NOTHING else on stdout, ever.
//   exit:   0 grant, 3 cancel, 2 no Wayland display reachable. 64 is added here: the arguments were not understood.
#pragma once

#include <sys/stat.h>

#include <cstddef>
#include <cstdint>
#include <cstdio>
#include <optional>
#include <string>
#include <vector>

namespace aurora {
namespace chooser {

// ---- the two closed sets ------------------------------------------------------------------------------------------

enum class Kind { File, Dir, Save };
enum class Mode { ReadOnly, ReadWrite };

// What the app will be able to do with the grant. Mode alone does not say it: a save writes whatever --mode claims, so
// the access shown to the user comes from both, in one place, and `heavy` follows from it.
enum class Access { Read, ReadWrite, Write };

constexpr Access access_of(Mode m, Kind k) {
    return k == Kind::Save ? Access::Write : (m == Mode::ReadWrite ? Access::ReadWrite : Access::Read);
}
constexpr bool heavy(Access a) { return a != Access::Read; }
constexpr bool takes_name(Kind k) { return k == Kind::Save; }

// Process exit codes. 0, 2 and 3 are the contract with the caller; 64 (EX_USAGE) is ours, and 1 is for a grant that
// could not be delivered. Exit 0 is only ever accompanied by a line on stdout.
constexpr int kExitGrant = 0;
constexpr int kExitIo = 1;
constexpr int kExitNoDisplay = 2;
constexpr int kExitCancel = 3;
constexpr int kExitUsage = 64;

// ---- argv ---------------------------------------------------------------------------------------------------------

struct Args {
    std::string app, verb;
    Mode mode = Mode::ReadOnly;
    Kind kind = Kind::File;
    std::string start, suggest;
};

enum class ParseStatus { Ok, Help, Bad };
struct ParsedArgs {
    ParseStatus status = ParseStatus::Bad;
    Args args;               // meaningful only when status == Ok
    std::string error;       // what was wrong, when status == Bad
};

inline const char* usage() {
    return "usage: aurora-chooser --app NAME --verb VERB --mode ro|rw --kind file|dir|save [--start DIR] [--suggest NAME]\n"
           "  stdout carries one line, {\"path\":\"/abs/path\"}, on grant and nothing else.\n"
           "  exit 0 grant, 3 cancel, 2 no Wayland display, 64 bad arguments.\n";
}

// argv[0] is skipped. Strict: an unknown flag, a repeated flag, a flag with no value and a stray positional are all
// refused, because the only caller is a program and a typo there should fail loudly rather than grant the wrong thing.
inline ParsedArgs parse_args(int argc, const char* const* argv) {
    ParsedArgs r;
    std::optional<std::string> app, verb, mode, kind, start, suggest;
    auto fail = [&](std::string why) {
        r.status = ParseStatus::Bad;
        r.error = std::move(why);
        return r;
    };
    for (int i = 1; i < argc; i++) {
        const std::string a = argv[i];
        if (a == "--help" || a == "-h") {
            r.status = ParseStatus::Help;
            return r;
        }
        std::optional<std::string>* slot = a == "--app" ? &app : a == "--verb" ? &verb : a == "--mode" ? &mode
                                         : a == "--kind" ? &kind : a == "--start" ? &start
                                         : a == "--suggest" ? &suggest : nullptr;
        if (!slot) return fail(a.rfind("--", 0) == 0 ? "unknown option " + a : "unexpected argument " + a);
        if (slot->has_value()) return fail(a + " given twice");
        if (i + 1 >= argc) return fail(a + " needs a value");
        *slot = argv[++i];
    }
    if (!app) return fail("--app is required");
    if (!verb) return fail("--verb is required");
    if (!mode) return fail("--mode is required");
    if (!kind) return fail("--kind is required");
    if (app->empty()) return fail("--app must not be empty");
    if (verb->empty()) return fail("--verb must not be empty");
    if (*mode == "ro") r.args.mode = Mode::ReadOnly;
    else if (*mode == "rw") r.args.mode = Mode::ReadWrite;
    else return fail("--mode must be ro or rw, not \"" + *mode + "\"");
    if (*kind == "file") r.args.kind = Kind::File;
    else if (*kind == "dir") r.args.kind = Kind::Dir;
    else if (*kind == "save") r.args.kind = Kind::Save;
    else return fail("--kind must be file, dir or save, not \"" + *kind + "\"");
    r.args.app = *app;
    r.args.verb = *verb;
    r.args.start = start.value_or("");
    r.args.suggest = suggest.value_or("");
    r.status = ParseStatus::Ok;
    return r;
}

// ---- paths --------------------------------------------------------------------------------------------------------

// Resolve "." and "..", collapse repeated slashes. Lexical, not realpath: a symlink stays the name it was reached by.
inline std::string normalise_path(const std::string& p) {
    std::vector<std::string> out;
    size_t i = 0;
    while (i < p.size()) {
        while (i < p.size() && p[i] == '/') i++;
        const size_t j = p.find('/', i);
        const std::string part = p.substr(i, j == std::string::npos ? std::string::npos : j - i);
        if (part == "..") { if (!out.empty()) out.pop_back(); }
        else if (!part.empty() && part != ".") out.push_back(part);
        if (j == std::string::npos) break;
        i = j;
    }
    std::string r;
    for (const std::string& seg : out) r += "/" + seg;
    return r.empty() ? std::string("/") : r;
}

inline std::string join_path(const std::string& dir, const std::string& name) {
    return dir == "/" ? "/" + name : dir + "/" + name;
}

inline std::string parent_path(const std::string& p) {
    const size_t i = p.find_last_of('/');
    return (p == "/" || i == std::string::npos || i == 0) ? std::string("/") : p.substr(0, i);
}

// ---- what is there ------------------------------------------------------------------------------------------------

enum class Presence { Missing, File, Dir, Other };

// stat, not lstat: a symlink to a directory is a directory to whoever opens it, and the verdict is about what a grant
// would let the app reach, not about what the row's icon says.
inline Presence presence_of(const std::string& path) {
    struct stat st;
    if (path.empty() || stat(path.c_str(), &st) != 0) return Presence::Missing;
    if (S_ISREG(st.st_mode)) return Presence::File;
    if (S_ISDIR(st.st_mode)) return Presence::Dir;
    return Presence::Other;
}

struct Look {
    std::string path;
    Presence presence = Presence::Missing;
};

// Everything the verdict is allowed to know, as values. `row` has an empty path when nothing is highlighted. `named` is
// folder/name as it is on disk now, and only matters for a save.
struct Pick {
    Look folder;
    Look row;
    std::string name;
    Look named;
};

inline Look look_at(const std::string& path) { return Look{path, presence_of(path)}; }

// Reads the disk. The one place a Pick is built from the world, so the UI and the grant-time re-check build it the same
// way.
inline Pick make_pick(const std::string& folder, const std::string& row_path, const std::string& name) {
    Pick p;
    p.folder = look_at(folder);
    if (!row_path.empty()) p.row = look_at(row_path);
    p.name = name;
    if (!name.empty() && name.find('/') == std::string::npos) p.named = look_at(join_path(folder, name));
    return p;
}

// ---- the wire line ------------------------------------------------------------------------------------------------

// Strict UTF-8: no overlong forms, no surrogates, nothing above U+10FFFF. JSON text is UTF-8, and a decoder that meets
// anything else may substitute U+FFFD, which would bind a ticket to a path nobody chose.
inline bool valid_utf8(const std::string& s) {
    size_t i = 0;
    const size_t n = s.size();
    while (i < n) {
        const unsigned char c = (unsigned char)s[i];
        size_t len;
        uint32_t cp;
        if (c < 0x80) { i++; continue; }
        if (c >= 0xC2 && c <= 0xDF) { len = 2; cp = c & 0x1F; }
        else if (c >= 0xE0 && c <= 0xEF) { len = 3; cp = c & 0x0F; }
        else if (c >= 0xF0 && c <= 0xF4) { len = 4; cp = c & 0x07; }
        else return false;
        if (i + len > n) return false;
        for (size_t k = 1; k < len; k++) {
            const unsigned char cc = (unsigned char)s[i + k];
            if ((cc & 0xC0) != 0x80) return false;
            cp = (cp << 6) | (cc & 0x3F);
        }
        if ((len == 3 && cp < 0x800) || (len == 4 && cp < 0x10000)) return false;
        if (cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF)) return false;
        i += len;
    }
    return true;
}

// The line that means "granted", newline included, or nothing when the path cannot be said faithfully: not absolute,
// holds a NUL, or is not UTF-8 (a Linux file name may be any bytes; a JSON string may not).
inline std::optional<std::string> grant_line(const std::string& path) {
    if (path.empty() || path[0] != '/' || path.find('\0') != std::string::npos || !valid_utf8(path))
        return std::nullopt;
    std::string out = "{\"path\":\"";
    for (const char ch : path) {
        const unsigned char c = (unsigned char)ch;
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            case '\b': out += "\\b"; break;
            case '\f': out += "\\f"; break;
            default:
                if (c < 0x20) {
                    char b[8];
                    snprintf(b, sizeof b, "\\u%04x", c);
                    out += b;
                } else {
                    out += ch;
                }
        }
    }
    out += "\"}\n";
    return out;
}

// ---- the verdict --------------------------------------------------------------------------------------------------

enum class Verdict {
    Ok,
    NothingSelected,   // file: no row is highlighted
    Gone,              // the thing highlighted no longer exists
    NotAFile,          // file: a folder or a special file is highlighted
    NotAFolder,        // dir: a file or a special file is highlighted
    FolderGone,        // dir, save: the folder on show is not a directory any more
    NameEmpty,         // save
    NameInvalid,       // save: contains '/', or is "." or ".."
    NameTaken,         // save: a folder or special file already has that name
    Unrepresentable,   // the path cannot be said on the wire
};

// A verdict and the path a grant would bind. `path` is empty unless the verdict is Ok, so the line that is printed is
// exactly what was judged.
struct Judgement {
    Verdict verdict = Verdict::NothingSelected;
    std::string path;
    bool ok() const { return verdict == Verdict::Ok; }
};

inline bool valid_leaf(const std::string& n) {
    return !n.empty() && n != "." && n != ".." && n.find('/') == std::string::npos &&
           n.find('\0') == std::string::npos;
}

// THE function: what a selection is worth for this kind of grant. The Grant button's state and the sentence under the
// list both come from here, and so does the path that is granted.
inline Judgement judge(Kind kind, const Pick& p) {
    auto verdict = [](Verdict v) { return Judgement{v, std::string()}; };
    auto finish = [&](const std::string& path) {
        if (!grant_line(path)) return verdict(Verdict::Unrepresentable);
        return Judgement{Verdict::Ok, path};
    };
    switch (kind) {
        case Kind::File:
            if (p.row.path.empty()) return verdict(Verdict::NothingSelected);
            if (p.row.presence == Presence::Missing) return verdict(Verdict::Gone);
            if (p.row.presence != Presence::File) return verdict(Verdict::NotAFile);
            return finish(p.row.path);
        case Kind::Dir: {
            // A highlighted row wins; with none, the folder on show is what is on offer, so a folder can be granted
            // without having to find it inside its own parent.
            const Look& l = p.row.path.empty() ? p.folder : p.row;
            if (l.presence == Presence::Missing) return verdict(p.row.path.empty() ? Verdict::FolderGone : Verdict::Gone);
            if (l.presence != Presence::Dir) return verdict(Verdict::NotAFolder);
            return finish(l.path);
        }
        case Kind::Save:
            if (p.folder.presence != Presence::Dir) return verdict(Verdict::FolderGone);
            if (p.name.empty()) return verdict(Verdict::NameEmpty);
            if (!valid_leaf(p.name)) return verdict(Verdict::NameInvalid);
            if (p.named.presence == Presence::Dir || p.named.presence == Presence::Other)
                return verdict(Verdict::NameTaken);
            return finish(join_path(p.folder.path, p.name));
    }
    return verdict(Verdict::NothingSelected);
}

// ---- the words ----------------------------------------------------------------------------------------------------

// Why Grant is disabled, in the words of someone who has never heard of a ticket. Empty for Ok.
inline std::string describe(Verdict v) {
    switch (v) {
        case Verdict::Ok: return "";
        case Verdict::NothingSelected: return "Select a file to continue.";
        case Verdict::Gone: return "That is no longer there.";
        case Verdict::NotAFile: return "That is not a file. Press Enter to open a folder, or select a file.";
        case Verdict::NotAFolder: return "That is not a folder.";
        case Verdict::FolderGone: return "This folder is no longer there.";
        case Verdict::NameEmpty: return "Type a name for the file.";
        case Verdict::NameInvalid: return "A file name cannot contain / and cannot be . or ..";
        case Verdict::NameTaken: return "A folder or special file already has that name.";
        case Verdict::Unrepresentable: return "That name cannot be passed on safely.";
    }
    return "";
}

// Text from the caller that is about to be shown to the user as a claim. Control characters become spaces so it cannot
// break a line or hide behind one, and a long one is cut at a character boundary with an ellipsis, so it cannot push the
// real sentence off the window.
inline std::string display_text(const std::string& s, size_t max_bytes = 48) {
    std::string t;
    for (const char ch : s) t += ((unsigned char)ch < 0x20 || ch == 0x7f) ? ' ' : ch;
    if (t.size() <= max_bytes) return t;
    size_t cut = max_bytes;
    while (cut > 0 && ((unsigned char)t[cut] & 0xC0) == 0x80) cut--;
    return t.substr(0, cut) + "\xE2\x80\xA6";
}

inline const char* access_verb(Access a) {
    switch (a) {
        case Access::Read: return "read";
        case Access::ReadWrite: return "read and change";
        case Access::Write: return "save";
    }
    return "";
}

inline const char* kind_object(Kind k) {
    switch (k) {
        case Kind::File: return "one file";
        case Kind::Dir: return "one folder and everything in it";
        case Kind::Save: return "one file";
    }
    return "";
}

// "Notes wants to read one file". The verb the app sent is not in it: it is the app's own word for what it is doing, and
// the sentence has to make sense to someone who does not know what a verb is. It is shown beside this, as `action_line`.
inline std::string headline(const std::string& app, Mode m, Kind k) {
    return display_text(app) + " wants to " + access_verb(access_of(m, k)) + " " + kind_object(k);
}

inline std::string action_line(const std::string& verb) { return "Requested action: " + display_text(verb); }

// One sentence on what the grant allows, heavier for the ones that can change something.
inline std::string consequence(Access a) {
    switch (a) {
        case Access::Read: return "It can look at this. It cannot change it.";
        case Access::ReadWrite: return "It will be able to change what you choose here.";
        case Access::Write: return "It will create or replace the file you name here.";
    }
    return "";
}

}  // namespace chooser
}  // namespace aurora
