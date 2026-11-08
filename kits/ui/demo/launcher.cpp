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
#include <string>
#include <utility>
#include <vector>

using namespace aurora::ui;

namespace {

// The inventory as a flat tree: a non-selectable section heading, then its app rows. The tree
// already skips headings on the arrow keys and refuses to select them; TreeItem::enabled makes a
// verb-only or blocked row unactionable with its reason drawn right-aligned.
class InventorySource : public TreeSource {
public:
    std::vector<launcher::Section> sections;

    bool Children(const std::string& id, std::vector<TreeItem>* out) override {
        if (!id.empty()) return true;   // every row is flat: nothing is ever expanded
        for (const launcher::Section& s : sections) {
            TreeItem h;
            h.id = "section:" + s.label;
            h.label = s.label;
            h.section = true;
            h.flat = true;
            out->push_back(std::move(h));
            for (const launcher::Row& r : s.rows) {
                TreeItem it;
                it.id = r.id;
                it.label = r.label;
                it.flat = true;
                it.enabled = r.enabled;
                it.note = r.note;
                out->push_back(std::move(it));
            }
        }
        return true;
    }
};

// apps@v1 -> plain structs. Absent keys are absent: `windowed`/`launch_problem` are omitempty.
bool parse_inventory(const Json& payload, std::vector<launcher::App>& apps,
                     std::vector<launcher::Category>& cats) {
    const Json* ja = payload.get("apps");
    if (!ja || !ja->isArray()) return false;
    if (const Json* jc = payload.get("categories"); jc && jc->isArray()) {
        for (const Json& c : jc->items()) {
            launcher::Category k;
            k.id = c.getString("id");
            k.label = c.getString("label");
            cats.push_back(std::move(k));
        }
    }
    for (const Json& a : ja->items()) {
        launcher::App ap;
        ap.id = a.getString("id");
        ap.name = a.getString("name");
        ap.category = a.getString("category");
        if (const Json* v = a.get("windowed")) ap.windowed = v->asBool();
        if (const Json* v = a.get("launchable")) ap.launchable = v->asBool();
        ap.launch_problem = a.getString("launch_problem");
        apps.push_back(std::move(ap));
    }
    return true;
}

}  // namespace

int main() {
    Window w("Launcher", 520, 640);
    if (!w.ok()) {
        std::fprintf(stderr, "launcher: no Wayland compositor reachable\n");
        return 2;
    }

    BusClient bus;
    const bool bus_up = bus.Connect() == B_OK;

    State<std::string> sel("");
    State<std::string> status("");
    sel.bind(&w);
    status.bind(&w);

    InventorySource src;

    auto name_of = [&](const std::string& id) -> std::string {
        for (const launcher::Section& s : src.sections)
            for (const launcher::Row& r : s.rows)
                if (r.id == id) return r.label;
        return id;
    };

    std::function<void()> refresh;
    std::function<void(const std::string&)> launch;

    // A refusal is a structured error envelope (kind:"error", payload.message); say what it said
    // rather than swallowing it, which is how Atlas's "rc != B_OK -> empty" would hide the reason.
    launch = [&](const std::string& id) {
        BusEnvelope req;
        req.kind = "request";
        req.to = "registry";
        req.verb = "registry.launch@v1";
        Json p = Json::MakeObject();
        p.set("app", Json::MakeString(id));
        req.payload = p;
        BusEnvelope rep;
        const status_t rc = bus.Call(req, &rep, 3000000);
        if (rc != B_OK) {
            const std::string msg = rep.payload.getString("message", rep.payload.getString("detail", ""));
            status.set(msg.empty() ? ("cannot launch " + name_of(id))
                                   : ("cannot launch " + name_of(id) + ": " + msg));
            return;
        }
        status.set("launched " + name_of(id));
        refresh();   // a launch that failed within the grace window shows as a launch_problem
    };

    std::function<Desc()> make_root = [&]() -> Desc {
        Desc tree = TreeView(&src, sel);
        OnTreeActivate(tree, [&](const std::string& id) {
            for (const launcher::Section& s : src.sections)
                for (const launcher::Row& r : s.rows)
                    if (r.id == id) {
                        if (r.enabled) w.Defer([&, id] { launch(id); });   // not during a node event
                        return;
                    }
        });
        return VStack({weight(tree, 1.0f), sized(Label(status), -1, 30)}, 0.0f);
    };

    refresh = [&] {
        src.sections.clear();
        if (!bus_up) {
            status.set("no bus: start the desktop with aurora-up");
        } else {
            BusEnvelope req;
            req.kind = "request";
            req.to = "registry";
            req.verb = "registry.apps@v1";
            req.payload = Json::MakeObject();
            BusEnvelope rep;
            const status_t rc = bus.Call(req, &rep, 2000000);
            std::vector<launcher::App> apps;
            std::vector<launcher::Category> cats;
            if (rc != B_OK) {
                status.set("the registry did not answer");
            } else if (!parse_inventory(rep.payload, apps, cats)) {
                status.set("unexpected reply from the registry");
            } else {
                src.sections = launcher::build_sections(apps, cats);
                status.set(apps.empty() ? "no apps installed" : "");
            }
        }
        w.SetRoot(make_root());
    };

    w.SetKeyHook([&](const au_key& k) -> bool {
        if (!k.pressed) return false;
        if (k.raw_char == 0x1b) { w.Quit(); return true; }                         // Escape closes
        if (k.raw_char == 'r' || k.raw_char == 'R') { w.Defer([&] { refresh(); }); return true; }
        return false;
    });

    refresh();
    w.Run(0, false, 0);
    return 0;
}
