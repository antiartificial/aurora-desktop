// 8.8/P6: the surface encoder, checked against the contract's rules rather than against itself.
//
// The round trip is a hand-written reader for exactly the shape the contract defines -- objects,
// arrays and strings, nothing else -- because the toolkit links no JSON library and a parser that
// could read anything would be a dependency hidden in a test. Decoding gives two claims a string
// comparison alone cannot: that the bytes are well-formed, and that re-encoding the decoded value
// produces the same bytes (the canonicity the contract asks for).
#include "surface.h"

#include <cstdint>
#include <cstdio>
#include <string>
#include <utility>
#include <vector>

using namespace aurora::ui;

static int fails = 0, checks = 0;
static void check(bool ok, const char* what) {
    ++checks;
    if (!ok) {
        std::printf("FAIL %s\n", what);
        ++fails;
    }
}

namespace {

// ---- a reader for the contract's shape only ------------------------------------------------

struct J {
    enum Kind { Str, Arr, Obj, Bool } kind = Str;
    std::string s;
    bool b = false;
    std::vector<J> arr;
    std::vector<std::pair<std::string, J>> obj;

    const J* field(const char* k) const {
        for (const auto& kv : obj)
            if (kv.first == k) return &kv.second;
        return nullptr;
    }
};

struct Parser {
    const std::string& t;
    std::size_t i = 0;
    bool ok = true;

    explicit Parser(const std::string& text) : t(text) {}

    void ws() {
        while (i < t.size() && (t[i] == ' ' || t[i] == '\n' || t[i] == '\t' || t[i] == '\r')) ++i;
    }
    bool want(char c) {
        ws();
        if (i < t.size() && t[i] == c) { ++i; return true; }
        ok = false;
        return false;
    }
    static int hex(char c) {
        if (c >= '0' && c <= '9') return c - '0';
        if (c >= 'a' && c <= 'f') return c - 'a' + 10;
        if (c >= 'A' && c <= 'F') return c - 'A' + 10;
        return -1;
    }
    static void put_utf8(std::string* o, uint32_t cp) {
        if (cp < 0x80) {
            o->push_back(static_cast<char>(cp));
        } else if (cp < 0x800) {
            o->push_back(static_cast<char>(0xc0 | (cp >> 6)));
            o->push_back(static_cast<char>(0x80 | (cp & 0x3f)));
        } else {
            o->push_back(static_cast<char>(0xe0 | (cp >> 12)));
            o->push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3f)));
            o->push_back(static_cast<char>(0x80 | (cp & 0x3f)));
        }
    }

    std::string str() {
        std::string o;
        if (!want('"')) return o;
        while (i < t.size()) {
            const char c = t[i++];
            if (c == '"') return o;
            if (c != '\\') { o.push_back(c); continue; }
            if (i >= t.size()) { ok = false; return o; }
            const char e = t[i++];
            switch (e) {
                case '"': o.push_back('"'); break;
                case '\\': o.push_back('\\'); break;
                case '/': o.push_back('/'); break;
                case 'b': o.push_back('\b'); break;
                case 'f': o.push_back('\f'); break;
                case 'n': o.push_back('\n'); break;
                case 'r': o.push_back('\r'); break;
                case 't': o.push_back('\t'); break;
                case 'u': {
                    uint32_t cp = 0;
                    for (int k = 0; k < 4; ++k) {
                        const int v = i < t.size() ? hex(t[i]) : -1;
                        if (v < 0) { ok = false; return o; }
                        cp = (cp << 4) | static_cast<uint32_t>(v);
                        ++i;
                    }
                    put_utf8(&o, cp);
                    break;
                }
                default: ok = false; return o;
            }
        }
        ok = false;
        return o;
    }

    J value() {
        ws();
        J v;
        if (i >= t.size()) { ok = false; return v; }
        if (t[i] == '"') {
            v.kind = J::Str;
            v.s = str();
            return v;
        }
        if (t[i] == '[') {
            v.kind = J::Arr;
            ++i;
            ws();
            if (i < t.size() && t[i] == ']') { ++i; return v; }
            while (ok) {
                v.arr.push_back(value());
                if (!ok) return v;
                ws();
                if (i < t.size() && t[i] == ',') { ++i; continue; }
                if (!want(']')) return v;
                return v;
            }
            return v;
        }
        // true / false: the only two literals the contract's shape has, and only as the value of
        // shell_chrome. Numbers and null stay outside the shape.
        if (t.compare(i, 4, "true") == 0) { v.kind = J::Bool; v.b = true; i += 4; return v; }
        if (t.compare(i, 5, "false") == 0) { v.kind = J::Bool; v.b = false; i += 5; return v; }
        if (t[i] == '{') {
            v.kind = J::Obj;
            ++i;
            ws();
            if (i < t.size() && t[i] == '}') { ++i; return v; }
            while (ok) {
                std::string k = str();
                if (!ok) return v;
                if (!want(':')) return v;
                v.obj.emplace_back(std::move(k), value());
                if (!ok) return v;
                ws();
                if (i < t.size() && t[i] == ',') { ++i; continue; }
                if (!want('}')) return v;
                return v;
            }
        }
        ok = false;   // numbers, null, true and false are not part of this shape, by design
        return v;
    }
};

bool is_str_array(const J* v) {
    if (!v || v->kind != J::Arr) return false;
    for (const J& e : v->arr)
        if (e.kind != J::Str) return false;
    return true;
}

std::vector<std::string> strs(const J* v) {
    std::vector<std::string> out;
    if (v) for (const J& e : v->arr) out.push_back(e.s);
    return out;
}

// Decode the contract's shape and reject anything it does not describe -- an unknown key, a
// non-string where a string belongs, or trailing bytes. A reader this strict is what makes the
// round-trip evidence about the encoder rather than about forgiveness.
bool decode(const std::string& bytes, SurfaceDescriptor* d, SurfaceOpts* opts, std::string* why) {
    Parser p(bytes);
    J j = p.value();
    if (!p.ok || p.i != bytes.size()) { if (why) *why = "not one well-formed JSON value"; return false; }
    if (j.kind != J::Obj) { if (why) *why = "top level is not an object"; return false; }

    for (const auto& kv : j.obj) {
        const std::string& k = kv.first;
        if (k != "app" && k != "title" && k != "actions" && k != "grants" &&
            k != "shell_chrome") {
            if (why) *why = "unknown key \"" + k + "\"";
            return false;
        }
    }
    const J* app = j.field("app");
    const J* title = j.field("title");
    const J* actions = j.field("actions");
    if (!app || app->kind != J::Str) { if (why) *why = "app is not a string"; return false; }
    if (!title || title->kind != J::Str) { if (why) *why = "title is not a string"; return false; }
    if (!actions || actions->kind != J::Arr) { if (why) *why = "actions is not an array"; return false; }

    SurfaceDescriptor o;
    o.app = app->s;
    o.title = title->s;
    for (const J& e : actions->arr) {
        if (e.kind != J::Obj) { if (why) *why = "an action is not an object"; return false; }
        for (const auto& kv : e.obj)
            if (kv.first != "id" && kv.first != "label" && kv.first != "effect" && kv.first != "grants") {
                if (why) *why = "unknown action key \"" + kv.first + "\"";
                return false;
            }
        const J* id = e.field("id");
        const J* label = e.field("label");
        const J* effect = e.field("effect");
        const J* grants = e.field("grants");
        if (!id || id->kind != J::Str) { if (why) *why = "an action has no id"; return false; }
        if (!label || label->kind != J::Str) { if (why) *why = "an action has no label"; return false; }
        if (!effect || effect->kind != J::Str) { if (why) *why = "an action has no effect"; return false; }
        Action a;
        a.id = id->s;
        a.label = label->s;
        a.effect = effect->s;
        if (grants) {
            if (!is_str_array(grants)) { if (why) *why = "action grants is not an array of strings"; return false; }
            a.grants = strs(grants);
        }
        o.actions.push_back(std::move(a));
    }
    const J* grants = j.field("grants");
    if (grants) {
        if (!is_str_array(grants)) { if (why) *why = "grants is not an array of strings"; return false; }
        o.grants = strs(grants);
    }
    // Absent and false are one claim (omitempty), so the decoder may only raise the flag, never
    // read a false as some third state.
    if (const J* sc = j.field("shell_chrome")) {
        if (sc->kind != J::Bool) { if (why) *why = "shell_chrome is not a boolean"; return false; }
        if (opts) opts->shell_chrome = sc->b;
    }
    *d = std::move(o);
    return true;
}

bool ascending(const std::vector<std::string>& v) {
    for (std::size_t i = 1; i < v.size(); ++i)
        if (!(v[i - 1] < v[i])) return false;
    return true;
}

// Decode, check the three canonical properties, and re-encode: same value, same bytes.
void round_trip(const std::string& bytes, SurfaceDescriptor* d, const SurfaceOpts& opts,
                const char* what) {
    std::string why;
    SurfaceOpts got;
    const bool decoded = decode(bytes, d, &got, &why);
    check(decoded, what);
    if (!decoded) {
        std::printf("      why: %s\n      bytes: %s\n", why.c_str(), bytes.c_str());
        return;
    }
    check(got.shell_chrome == opts.shell_chrome,
          "shell_chrome decodes to what was encoded (absent and false are one claim)");
    std::vector<std::string> ids;
    for (const Action& a : d->actions) ids.push_back(a.id);
    check(ascending(ids), "actions arrive sorted by id");
    check(ascending(d->grants), "grants arrive sorted");
    for (const Action& a : d->actions) check(ascending(a.grants), "action grants arrive sorted");

    std::string again;
    check(SurfaceJson(*d, opts, &again), "the decoded descriptor re-encodes");
    check(again == bytes, "re-encoding the decoded value is byte-identical (canonical)");
}

}   // namespace

int main() {
    // ---- the naming rule: act:<verb>, =label, #effect, and the nominal default --------------
    Desc tree = VStack({
        HStack({Label("Open").name("act:open"),
                Label("Peek").name("act:peek#read")}),
        Label("Save").name("act:save=Save all"),
        Label("files").name("files"),                      // not an action, must not appear
        VStack({Label("Delete").name("act:delete=Delete...#write")}),
    });

    SurfaceDescriptor d;
    std::string why;
    check(SurfaceOf(tree, "org.example.atlas", "Atlas", {"ui:write", "ui:read"}, &d, &why),
          "a well-named tree describes");
    check(d.app == "org.example.atlas" && d.title == "Atlas", "app and title come from the caller");
    check(d.actions.size() == 4, "four act: nodes became four actions");
    check(d.grants.size() == 2 && d.grants[0] == "ui:write", "grants are the caller's, in the caller's order");
    bool saw_delete = false, saw_peek = false, saw_save = false, saw_open = false;
    for (const Action& a : d.actions) {
        if (a.id == "delete") saw_delete = a.effect == "write" && a.label == "Delete...";
        if (a.id == "peek") saw_peek = a.effect == "read" && a.label == "peek";
        if (a.id == "save") saw_save = a.effect == "nominal" && a.label == "Save all";
        if (a.id == "open") saw_open = a.effect == "nominal" && a.label == "open";
    }
    check(saw_delete, "act:verb=label#write sets both label and effect");
    check(saw_peek, "act:verb#read sets the effect and labels with the verb");
    check(saw_save, "an action with no #effect is nominal");
    check(saw_open, "a bare act:verb labels with the verb and is nominal");

    // The encoder owns the per-action grants (the caller states them); sort order is the
    // encoder's job, so these go in backwards on purpose.
    for (Action& a : d.actions)
        if (a.id == "save") a.grants = {"files:write", "files:read"};

    std::string bytes;
    check(SurfaceJson(d, &bytes, &why), "a valid descriptor encodes");
    const std::string expected =
        R"({"app":"org.example.atlas","title":"Atlas","actions":[)"
        R"({"id":"delete","label":"Delete...","effect":"write"},)"
        R"({"id":"open","label":"open","effect":"nominal"},)"
        R"({"id":"peek","label":"peek","effect":"read"},)"
        R"({"id":"save","label":"Save all","effect":"nominal","grants":["files:read","files:write"]})"
        R"(],"grants":["ui:read","ui:write"]})";
    check(bytes == expected, "the canonical bytes are the contract's shape");
    if (bytes != expected) std::printf("      got: %s\n     want: %s\n", bytes.c_str(), expected.c_str());

    SurfaceDescriptor back;
    round_trip(bytes, &back, SurfaceOpts{}, "the canonical bytes decode");
    check(back.actions.size() == 4 && back.actions[0].id == "delete", "decoding restores the actions");
    check(back.actions.size() == 4 && back.app == "org.example.atlas", "decoding restores app");
    check(back.actions.size() == 4 && back.actions[3].grants.size() == 2,
          "decoding restores per-action grants");

    // ---- both orders of the two suffixes ------------------------------------------------
    Desc orders = VStack({Label("One").name("act:one#write=One"),
                          Label("Two").name("act:two=Two#read")});
    check(SurfaceJson(orders, "a", "t", {}, &bytes, &why), "effect-before-label also parses");
    check(bytes.find(R"({"id":"one","label":"One","effect":"write"})") != std::string::npos,
          "act:one#write=One is write/One");
    check(bytes.find(R"({"id":"two","label":"Two","effect":"read"})") != std::string::npos,
          "act:two=Two#read is read/Two");

    // ---- tree order does not reach the bytes ---------------------------------------------
    Desc unordered = VStack({Label("z").name("act:zulu"), Label("a").name("act:alpha"),
                             Label("m").name("act:mike")});
    check(SurfaceJson(unordered, "a", "t", {"g2", "g1"}, &bytes, &why), "an unordered tree encodes");
    check(bytes == R"({"app":"a","title":"t","actions":[)"
                    R"({"id":"alpha","label":"alpha","effect":"nominal"},)"
                    R"({"id":"mike","label":"mike","effect":"nominal"},)"
                    R"({"id":"zulu","label":"zulu","effect":"nominal"})"
                    R"(],"grants":["g1","g2"]})",
          "actions and grants sort, so build order never reaches the wire");
    round_trip(bytes, &back, SurfaceOpts{}, "the sorted bytes decode");

    // ---- escaping, including the HTML escapes Go's encoder emits -------------------------
    // q"b\s, a newline, a tab, < > &, SOH, and U+2028 (the line separator Go escapes for JS).
    const std::string nasty = std::string("q\"b\\s\n\t<&>") + '\x01' + "\xe2\x80\xa8";
    Desc esc = VStack({Label("esc").name("act:esc=" + nasty)});
    check(SurfaceJson(esc, "a", "t", {}, &bytes, &why), "a label full of metacharacters encodes");
    check(bytes == R"({"app":"a","title":"t","actions":[)"
                    R"({"id":"esc","label":"q\"b\\s\n\t\u003c\u0026\u003e\u0001\u2028","effect":"nominal"})"
                    R"(]})",
          "strings escape as encoding/json does, < > & included");
    if (bytes.find("\\u003c") == std::string::npos) std::printf("      escaped: %s\n", bytes.c_str());
    check(decode(bytes, &back, nullptr, &why) && back.actions.size() == 1 && back.actions[0].label == nasty,
          "the escaped label round-trips to the original bytes");

    // ---- an empty tree: no actions, and no grants key at all (omitempty) ----------------
    check(SurfaceJson(Desc(), "x", "y", {}, &bytes, &why), "an empty tree encodes");
    check(bytes == R"({"app":"x","title":"y","actions":[]})",
          "an empty surface states an empty actions array and omits grants");
    round_trip(bytes, &back, SurfaceOpts{}, "the empty surface decodes");

    // ---- shell_chrome: absent is false, and true survives the round trip --------------------
    // The default is what keeps every app that never heard of this option on the same bytes it
    // published before, so the absence is asserted, not just the false.
    check(SurfaceJson(VStack({Label("a").name("act:open")}), "a", "t", {}, &bytes, &why),
          "a surface with no options encodes");
    check(bytes.find("shell_chrome") == std::string::npos,
          "an app that does not opt in emits no shell_chrome key at all");

    SurfaceOpts shell;
    shell.shell_chrome = true;
    check(SurfaceJson(tree, "org.example.atlas", "Atlas", {"ui:read"}, shell, &bytes, &why),
          "a surface asking the shell to draw its chrome encodes");
    check(bytes == R"({"app":"org.example.atlas","title":"Atlas","actions":[)"
                    R"({"id":"delete","label":"Delete...","effect":"write"},)"
                    R"({"id":"open","label":"open","effect":"nominal"},)"
                    R"({"id":"peek","label":"peek","effect":"read"},)"
                    R"({"id":"save","label":"Save all","effect":"nominal"}],)"
                    R"("grants":["ui:read"],"shell_chrome":true})",
          "shell_chrome is emitted last, where Go puts a field declared after Grants");
    if (bytes.find("shell_chrome") == std::string::npos) std::printf("      got: %s\n", bytes.c_str());
    round_trip(bytes, &back, shell, "the shell-chrome descriptor decodes with its claim intact");
    check(back.app == "org.example.atlas" && back.actions.size() == 4,
          "the flagged descriptor still decodes to the same surface");

    // The same surface with the option off is byte-identical to the unflagged test above: the flag
    // adds a key or nothing, never a different document.
    SurfaceOpts off;
    check(SurfaceJson(tree, "org.example.atlas", "Atlas", {"ui:read"}, off, &bytes, &why) &&
              bytes.find("shell_chrome") == std::string::npos,
          "shell_chrome=false encodes exactly as the default does");

    // ---- the refusals -------------------------------------------------------------------
    std::string bad;
    check(!SurfaceJson(VStack({Label("a").name("act:open"), Label("b").name("act:open=Open")}),
                       "a", "t", {}, &bad, &why),
          "two actions claiming one id are refused");
    check(why.find("duplicate") != std::string::npos, "the refusal names the duplicate id");

    check(!SurfaceJson(Label("x").name("act:#write"), "a", "t", {}, &bad, &why),
          "an act: node with no verb is refused");

    check(!SurfaceJson(Label("x").name("act:x#destroy"), "a", "t", {}, &bad, &why),
          "an effect outside read/write/nominal is refused, not emitted");
    check(why.find("destroy") != std::string::npos, "the refusal names the effect");

    check(!SurfaceJson(Desc(), "", "t", {}, &bad, &why), "an empty app is refused");
    check(!SurfaceJson(Desc(), "a", "t", {}, nullptr, &why), "a missing destination is refused");

    SurfaceDescriptor broken;
    broken.app = "a";
    broken.actions.push_back(Action{"x", "X", "read", {}});
    broken.actions.push_back(Action{"x", "X again", "read", {}});
    check(!SurfaceJson(broken, &bad, &why), "SurfaceJson validates what it is handed");
    check(!SurfaceOk(broken, &why), "SurfaceOk says why a descriptor cannot be drawn");

    // A refused encode must not leave half a document in the caller's string.
    std::string untouched = "keep";
    check(!SurfaceJson(broken, &untouched, &why) && untouched == "keep",
          "a refused encode leaves the destination untouched");

    if (fails) std::printf("surface: %d of %d FAILED\n", fails, checks);
    else std::printf("surface: ok (%d checks)\n", checks);
    return fails ? 1 : 0;
}
