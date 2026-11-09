// The launcher: the whole app inventory, grouped by category, started from the compositor's
// System row and Super+Space. Decision 0025: "The launcher is an aurora::ui app, on the primitives
// Atlas proved. It calls registry.launch@v1 ... the launcher draws the inventory."
//
// v1 is deliberately small (0025): text only, no icons, no search/recents/favourites, no submenus.
// The model that turns a registry.apps@v1 reply into sections lives in launcher_model.h and is
// unit-tested headless; this file is the window, the bus call and the activation.
#include "ui.h"

#include <BusClient.h>
#include <BusJson.h>

#include "launcher_model.h"

#include <cstdio>
#include <functional>
#include <mutex>
#include <string>
#include <utility>
#include <vector>

using namespace aurora::ui;

namespace {

// Manifest strings reach a text surface: strip control characters and cap the length so a hostile
// or sloppy name cannot break a row or push a real note off it. (The Rust dock does the same; a
// shared typed client would own this for every C++ app -- flagged as its own task.)
std::string scrub(std::string s, std::size_t max = 120) {
    for (char& c : s) {
        const unsigned char u = static_cast<unsigned char>(c);
        if (u < 0x20 || u == 0x7f) c = ' ';
    }
    if (s.size() > max) {
        std::size_t cut = max;
        while (cut > 0 && (static_cast<unsigned char>(s[cut]) & 0xC0) == 0x80) --cut;
        s.resize(cut);
        s += "\xe2\x80\xa6";
    }
    return s;
}

// apps@v1 -> plain structs. Absent keys are absent: windowed/launch_problem are omitempty.
bool parse_inventory(const Json& payload, std::vector<launcher::App>& apps,
                     std::vector<launcher::Category>& cats) {
    const Json* ja = payload.get("apps");
    if (!ja || !ja->isArray()) return false;
    if (const Json* jc = payload.get("categories"); jc && jc->isArray()) {
        for (const Json& c : jc->items()) {
            launcher::Category k;
            k.id = c.getString("id");
            k.label = scrub(c.getString("label"), 60);
            cats.push_back(std::move(k));
        }
    }
    for (const Json& a : ja->items()) {
        if (apps.size() >= 256) break;
        launcher::App ap;
        ap.id = a.getString("id");
        ap.name = scrub(a.getString("name"));
        if (ap.name.empty()) ap.name = ap.id;
        ap.category = a.getString("category");
        if (const Json* v = a.get("windowed")) ap.windowed = v->asBool();
        if (const Json* v = a.get("launchable")) ap.launchable = v->asBool();
        ap.launch_problem = scrub(a.getString("launch_problem"), 200);
        apps.push_back(std::move(ap));
    }
    return true;
}

}  // namespace

int main() {
    // Declared before the window on purpose: ~Window joins the worker tasks, and a worker uses the
    // bus, so the bus and its mutex must outlive the window -- not the other way round.
    BusClient bus;
    const bool bus_up = bus.Connect() == B_OK;
    std::mutex bus_mu;

    Window w("Launcher", 520, 640);
    if (!w.ok()) {
        std::fprintf(stderr, "launcher: no Wayland compositor reachable\n");
        return 2;
    }

    State<std::string> sel("");
    State<std::string> status("");
    sel.bind(&w);
    status.bind(&w);

    SectionListSource src;

    auto find_row = [&](const std::string& id) -> const TreeItem* {
        for (const TreeSection& s : src.sections)
            for (const TreeItem& r : s.rows)
                if (r.id == id) return &r;
        return nullptr;
    };

    auto name_of = [&](const std::string& id) -> std::string {
        const TreeItem* r = find_row(id);
        return r ? r->label : id;
    };

    std::function<void()> refresh;
    std::function<void(const std::string&)> launch;

    // A refusal is a structured error envelope (kind:"error", payload.message); say what it said
    // rather than swallowing it, which is how Atlas's "rc != B_OK -> empty" would hide the reason.
    launch = [&](const std::string& id) {
        status.set("launching " + name_of(id));
        Task(w, [&, id]() -> std::pair<bool, std::string> {
            std::lock_guard<std::mutex> g(bus_mu);
            Json p = Json::MakeObject();
            p.set("app", Json::MakeString(id));
            BusEnvelope req;
            req.kind = "request";
            req.to = "registry";
            req.verb = "registry.launch@v1";
            req.payload = std::move(p);
            BusEnvelope rep;
            const status_t rc = bus.Call(req, &rep, 3000000);
            return {rc == B_OK, rc == B_OK ? std::string() : rep.payload.Dump()};
        }, [&, id](std::pair<bool, std::string> res) {
            if (res.first) {
                // Refresh first (a grace-window failure becomes a launch_problem), then say it: the
                // refresh clears the status, so the confirmation is set after it.
                refresh();
                status.set("launched " + name_of(id));
                return;
            }
            std::string msg;
            Json j;
            std::string err;
            if (Json::Parse(res.second, &j, &err)) msg = j.getString("message", j.getString("detail", ""));
            status.set(msg.empty() ? ("cannot launch " + name_of(id))
                                   : ("cannot launch " + name_of(id) + ": " + msg));
        });
    };

    // Built once: a refresh asks the source again and repaints in place, rather than swapping the
    // tree, so the scroll position, the cursor and keyboard focus all survive.
    Desc tree = TreeView(&src, sel);
    OnTreeActivate(tree, [&](const std::string& id) {
        // The widget only activates enabled rows, so a disabled row never reaches here.
        w.Defer([&, id] { launch(id); });   // not during a node event
    });
    // TextView wraps: a controller refusal can be a whole sentence and the strip must show it.
    Desc root = VStack({weight(tree, 1.0f), sized(TextView(status, true), -1, 46)}, 0.0f);

    // The apps@v1 call runs on a Task worker (a slow registry must not freeze the window); the reply
    // is parsed and applied on the loop. A failure keeps the inventory already on screen.
    refresh = [&] {
        if (!bus_up) {
            status.set("no bus: start the desktop with aurora-up");
            TreeInvalidate(w, tree);
            return;
        }
        status.set("loading\u2026");
        Task(w, [&]() -> std::string {
            std::lock_guard<std::mutex> g(bus_mu);
            BusEnvelope req;
            req.kind = "request";
            req.to = "registry";
            req.verb = "registry.apps@v1";
            req.payload = Json::MakeObject();
            BusEnvelope rep;
            const status_t rc = bus.Call(req, &rep, 2000000);
            return rc == B_OK ? rep.payload.Dump() : std::string();   // "" means the call failed
        }, [&](std::string payload) {
            if (payload.empty()) {
                status.set("the registry did not answer");
                return;
            }
            Json j;
            std::string err;
            std::vector<launcher::App> apps;
            std::vector<launcher::Category> cats;
            if (!Json::Parse(payload, &j, &err) || !parse_inventory(j, apps, cats)) {
                status.set("unexpected reply from the registry");
                return;
            }
            std::vector<TreeSection> sections;
            for (const launcher::Section& sec : launcher::build_sections(apps, cats)) {
                TreeSection ts;
                ts.key = sec.id;
                ts.label = sec.label;
                for (const launcher::Row& row : sec.rows) {
                    TreeItem it;
                    it.id = row.id;
                    it.label = row.label;
                    it.flat = true;
                    it.enabled = row.enabled;
                    it.note = row.note;
                    ts.rows.push_back(std::move(it));
                }
                sections.push_back(std::move(ts));
            }
            src.sections = std::move(sections);   // swap only on success
            TreeInvalidate(w, tree);              // rebuild the rows; scroll/cursor/selection stay
            status.set(apps.empty() ? "no apps installed" : "");
        });
    };

    w.SetKeyHook([&](const au_key& k) -> bool {
        if (!k.pressed) return false;
        if (k.raw_char == 0x1b) { w.Quit(); return true; }                         // Escape closes
        if (k.raw_char == 'r' || k.raw_char == 'R') { w.Defer([&] { refresh(); }); return true; }
        return false;
    });

    refresh();
    w.SetRoot(root);
    w.Run(0, false, 0);
    return 0;
}
