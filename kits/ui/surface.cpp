// 8.8/P6: Desc tree -> the frozen surface contract's bytes. See surface.h for the rules and the
// reasoning; this file is the walk, the name rule, and the encoder.
#include "surface.h"

#include <algorithm>
#include <cstdint>
#include <set>

namespace aurora::ui {
namespace {

// The shape of an action name, after "act:" has been taken off.
struct Parsed {
    std::string verb;
    std::string label;
    std::string effect = "nominal";
};

bool is_effect(const std::string& s) {
    return s == "read" || s == "write" || s == "nominal";
}

// "act:<verb>[=<label>][#<effect>]" and "act:<verb>[#<effect>][=<label>]".
//
// Both orders are accepted rather than one: "#write" reads best straight after the verb
// (act:delete#write) and "=label" reads best straight after it too (act:delete=Delete...), so a
// caller will write either order, and refusing one would turn a reasonable name into a runtime
// failure with no visible cause. Anything the rule does not describe -- an empty verb, a second
// '#', an effect the contract does not list -- is an error, not a best effort: a surface whose
// action cannot be policed is worse than one that failed to describe itself.
bool parse_action(const std::string& name, Parsed* p, std::string* why) {
    const std::size_t eq = name.find('=');
    const std::size_t hash = name.find('#');

    std::size_t head_end = name.size();
    if (eq != std::string::npos) head_end = std::min(head_end, eq);
    if (hash != std::string::npos) head_end = std::min(head_end, hash);

    p->verb = name.substr(0, head_end);
    p->label = p->verb;
    p->effect = "nominal";

    // Everything after the head is one "=<label>" and one "#<effect>", in whichever order.
    std::size_t i = head_end;
    while (i < name.size()) {
        if (name[i] == '=') {
            const std::size_t end = name.find('#', i + 1);
            const std::string label = name.substr(i + 1, end == std::string::npos ? std::string::npos
                                                                                  : end - i - 1);
            if (label.empty()) {
                if (why) *why = "act:" + p->verb + " has an empty label";
                return false;
            }
            p->label = label;
            i = end;
        } else if (name[i] == '#') {
            const std::size_t end = name.find('=', i + 1);
            const std::string effect = name.substr(i + 1, end == std::string::npos ? std::string::npos
                                                                                   : end - i - 1);
            if (!is_effect(effect)) {
                if (why) *why = "act:" + p->verb + " has effect \"" + effect +
                                "\", which is not read/write/nominal";
                return false;
            }
            p->effect = effect;
            i = end;
        } else {
            // Unreachable given head_end, but a silent loop forever is the wrong failure.
            if (why) *why = "act:" + p->verb + " has trailing junk";
            return false;
        }
    }

    if (p->verb.empty()) {
        if (why) *why = "an act: node has no verb";
        return false;
    }
    return true;
}

// Depth cap and the same traversal Describe uses (layout order through Children()): a malformed
// tree cannot hang the encoder, and the order here is irrelevant to the bytes because the encoder
// sorts -- but walking in layout order keeps this rule identical to Describe's when a reviewer
// compares them.
void walk_actions(Node* n, std::vector<Action>* out, std::set<std::string>* ids,
                  std::string* why, bool* ok, int depth) {
    if (!n || depth > 512 || !*ok) return;
    const std::string& nm = n->name;
    if (nm.rfind("act:", 0) == 0) {
        Parsed p;
        if (!parse_action(nm.substr(4), &p, why)) {
            *ok = false;
            return;
        }
        if (!ids->insert(p.verb).second) {
            if (why) *why = "duplicate action id \"" + p.verb + "\"";
            *ok = false;
            return;
        }
        Action a;
        a.id = p.verb;
        a.label = p.label;
        a.effect = p.effect;
        out->push_back(std::move(a));
    }
    std::vector<Node*> kids;
    n->Children(&kids);
    for (Node* k : kids) walk_actions(k, out, ids, why, ok, depth + 1);
}

const char* kHex = "0123456789abcdef";

void put_u_escape(std::string* o, uint32_t cp) {
    o->append("\\u");
    o->push_back(kHex[(cp >> 12) & 0xf]);
    o->push_back(kHex[(cp >> 8) & 0xf]);
    o->push_back(kHex[(cp >> 4) & 0xf]);
    o->push_back(kHex[cp & 0xf]);
}

// One JSON string, escaped exactly as Go's encoding/json escapes it, HTML escaping included.
//
// The HTML escaping is not decoration: encoding/json turns < > & into \u003c \u003e \u0026 by
// default, so a C++ side that passes them through produces different bytes for the same
// descriptor and every cross-language golden diff fails on a label like "Open & Save". U+2028 and
// U+2029 are escaped for the same reason (Go does, because they are line terminators in
// JavaScript). Bytes above 0x7f are otherwise passed through: a label is UTF-8 by convention, and
// coercing invalid sequences to U+FFFD the way Go does would hide a caller's bug rather than
// report it.
void put_string(std::string* o, const std::string& s) {
    o->push_back('"');
    for (std::size_t i = 0; i < s.size(); ++i) {
        const unsigned char c = static_cast<unsigned char>(s[i]);
        switch (c) {
            case '"': o->append("\\\""); break;
            case '\\': o->append("\\\\"); break;
            case '\b': o->append("\\b"); break;
            case '\f': o->append("\\f"); break;
            case '\n': o->append("\\n"); break;
            case '\r': o->append("\\r"); break;
            case '\t': o->append("\\t"); break;
            case '<': o->append("\\u003c"); break;
            case '>': o->append("\\u003e"); break;
            case '&': o->append("\\u0026"); break;
            default:
                if (c < 0x20) {
                    put_u_escape(o, c);
                } else if (c == 0xe2 && i + 2 < s.size() &&
                           static_cast<unsigned char>(s[i + 1]) == 0x80 &&
                           (static_cast<unsigned char>(s[i + 2]) == 0xa8 ||
                            static_cast<unsigned char>(s[i + 2]) == 0xa9)) {
                    put_u_escape(o, static_cast<unsigned char>(s[i + 2]) == 0xa8 ? 0x2028 : 0x2029);
                    i += 2;
                } else {
                    o->push_back(static_cast<char>(c));
                }
        }
    }
    o->push_back('"');
}

void put_strings(std::string* o, const std::vector<std::string>& v) {
    o->push_back('[');
    for (std::size_t i = 0; i < v.size(); ++i) {
        if (i) o->push_back(',');
        put_string(o, v[i]);
    }
    o->push_back(']');
}

}   // namespace

bool SurfaceOk(const SurfaceDescriptor& d, std::string* why) {
    if (d.app.empty()) {
        if (why) *why = "surface: app is empty";
        return false;
    }
    std::set<std::string> seen;
    for (const Action& a : d.actions) {
        if (a.id.empty()) {
            if (why) *why = "surface: an action has no id";
            return false;
        }
        if (!seen.insert(a.id).second) {
            if (why) *why = "surface: duplicate action id \"" + a.id + "\"";
            return false;
        }
        if (!is_effect(a.effect)) {
            if (why) *why = "surface: action \"" + a.id + "\" has effect \"" + a.effect +
                            "\", which is not read/write/nominal";
            return false;
        }
    }
    return true;
}

bool SurfaceOf(const Desc& root, std::string app, std::string title,
               std::vector<std::string> grants, SurfaceDescriptor* out, std::string* why) {
    if (!out) {
        if (why) *why = "surface: no destination";
        return false;
    }
    SurfaceDescriptor d;
    d.app = std::move(app);
    d.title = std::move(title);
    d.grants = std::move(grants);   // the caller's claim, copied and never derived from the tree

    bool ok = !d.app.empty();
    if (!ok && why) *why = "surface: app is empty";
    if (ok && root) {
        std::set<std::string> ids;
        walk_actions(root.get(), &d.actions, &ids, why, &ok, 0);
    }
    if (!ok) return false;
    *out = std::move(d);
    return true;
}

bool SurfaceJson(const SurfaceDescriptor& d, const SurfaceOpts& opts, std::string* out,
                 std::string* why) {
    std::string reason;
    if (!SurfaceOk(d, &reason)) {
        if (why) *why = reason;
        return false;
    }
    if (!out) {
        if (why) *why = "surface: no destination";
        return false;
    }

    // Canonical means sorted, so the sort happens on a copy: a caller's descriptor is not
    // reordered under it, and two trees that differ only in build order encode identically.
    std::vector<Action> actions = d.actions;
    for (Action& a : actions) std::sort(a.grants.begin(), a.grants.end());
    std::sort(actions.begin(), actions.end(),
              [](const Action& a, const Action& b) { return a.id < b.id; });

    std::vector<std::string> grants = d.grants;
    std::sort(grants.begin(), grants.end());

    std::string o;
    o.reserve(96 + actions.size() * 64);
    o.append("{\"app\":");
    put_string(&o, d.app);
    o.append(",\"title\":");
    put_string(&o, d.title);

    // Always an array, even when empty. Go marshals a nil slice as null, but null is not a list
    // and every consumer would need a second case for it; [] decodes to the same empty action
    // list in Go and is unambiguous everywhere else.
    o.append(",\"actions\":[");
    for (std::size_t i = 0; i < actions.size(); ++i) {
        const Action& a = actions[i];
        if (i) o.push_back(',');
        o.append("{\"id\":");
        put_string(&o, a.id);
        o.append(",\"label\":");
        put_string(&o, a.label);
        o.append(",\"effect\":");
        put_string(&o, a.effect);
        if (!a.grants.empty()) {
            o.append(",\"grants\":");
            put_strings(&o, a.grants);
        }
        o.push_back('}');
    }
    o.push_back(']');

    // omitempty, as the contract's tags say: an absent grants key and an empty one mean the same
    // thing to a reader, and the contract's canonical bytes omit it.
    if (!grants.empty()) {
        o.append(",\"grants\":");
        put_strings(&o, grants);
    }

    // Last, and only when true. The position is Go's: encoding/json marshals struct fields in
    // declaration order, so once Descriptor gains `ShellChrome` after `Grants` the field lands
    // exactly here. Omitted when false for the same reason grants are omitted when empty --
    // omitempty on the Go side means absent and false are one claim, and emitting the false would
    // make an app that never opted in produce new bytes for the same meaning.
    if (opts.shell_chrome) o.append(",\"shell_chrome\":true");
    o.push_back('}');

    *out = std::move(o);
    return true;
}

bool SurfaceJson(const SurfaceDescriptor& d, std::string* out, std::string* why) {
    return SurfaceJson(d, SurfaceOpts{}, out, why);
}

bool SurfaceJson(const Desc& root, std::string app, std::string title,
                 std::vector<std::string> grants, const SurfaceOpts& opts, std::string* out,
                 std::string* why) {
    SurfaceDescriptor d;
    if (!SurfaceOf(root, std::move(app), std::move(title), std::move(grants), &d, why)) return false;
    return SurfaceJson(d, opts, out, why);
}

bool SurfaceJson(const Desc& root, std::string app, std::string title,
                 std::vector<std::string> grants, std::string* out, std::string* why) {
    return SurfaceJson(root, std::move(app), std::move(title), std::move(grants), SurfaceOpts{},
                       out, why);
}


// Describe is the same rule as SurfaceOf, with no grants and no failure channel: it is the old entry
// point, and it now cannot drift from the authority it delegates to.
SurfaceDescriptor Describe(const Desc& root, std::string app, std::string title) {
    SurfaceDescriptor d;
    std::string why;
    if (!SurfaceOf(root, std::move(app), std::move(title), {}, &d, &why)) return SurfaceDescriptor{};
    return d;
}

}   // namespace aurora::ui

