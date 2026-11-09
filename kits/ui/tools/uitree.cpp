// aurora-uitree: 8.7's check, made runnable. Every component is built once and the tree is dumped
// as "role name" lines, so "appears with a role better than control" is a command rather than a
// claim in a commit message.
//
// A layout box answers "none" -- ARIA's role for a wrapper that contributes no semantics -- and
// the one-child decorators (sized, Reveal, Either) answer their child's role, so they read as what
// they frame. "control" is the base class's "I have not said what I am": it is not a layout
// answer, and it is the thing a new component must stop saying.
//
//   aurora-uitree            # dump
//   aurora-uitree --check    # exit 1 if a component root answers no role of its own
#include "components/all.h"

#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

using namespace aurora::ui;

// Roles that describe a BOX rather than a thing. A component ROOT must not be one: the dump is
// only worth anything if a root names a real component, and a container that starts answering a
// plausible non-control word must not be able to satisfy the check that way. "presentation" is
// ARIA's long form of "none" and is listed so either spelling still fails.
static bool IsLayoutRole(const char* r) {
    return std::strcmp(r, "control") == 0 || std::strcmp(r, "none") == 0 ||
           std::strcmp(r, "presentation") == 0;
}

struct Entry {
    const char* name;
    Desc root;
};

static void walk(const Desc& d, int depth, std::string* out) {
    if (!d) return;
    out->append((size_t)depth * 2, ' ');
    out->append(d->Role());
    if (!d->name.empty()) {
        out->push_back(' ');
        out->append(d->name);
    }
    out->push_back('\n');
    std::vector<Node*> kids;
    d->Children(&kids);
    for (Node* k : kids) {
        // Children() hands back raw pointers; wrap them without taking ownership.
        Desc child(std::shared_ptr<Node>(k, [](Node*) {}));
        walk(child, depth + 1, out);
    }
}

int main(int argc, char** argv) {
    const bool check = argc > 1 && std::strcmp(argv[1], "--check") == 0;

    Binding<std::string> no_selection{std::string()};
    Binding<bool> closed(false);
    Binding<bool> open(true);

    const Entry entries[] = {
        {"splitbutton", SplitButton("Resize", [] {}, {})},
        {"sidebar", Sidebar({}, no_selection)},
        {"toolbar", Toolbar({}, Label("location"), {})},
        {"info_bar", InfoBar(InfoSeverity::Info, "nothing to report")},
        {"expander", Expander("Details", closed, Label("body"))},
        {"number_box", NumberBox(Binding<double>(1.0), 0.0, 10.0)},
        {"content_dialog", ContentDialog("Title", "Body", Affirm::Accept, [] {})},
        {"teaching_tip", TeachingTip("Title", "Body", open, [] {})},
    };

    int bad = 0;
    std::string dump;
    for (const Entry& e : entries) {
        std::string sub;
        walk(e.root, 0, &sub);
        dump += sub;
        const std::string role = e.root->Role();
        if (IsLayoutRole(role.c_str())) {
            std::printf("FAIL %s answers \"%s\", not a role of its own\n", e.name, role.c_str());
            ++bad;
        }
    }

    if (check) {
        if (bad) {
            std::printf("uitree: %d component(s) with no role\n", bad);
            return 1;
        }
        std::printf("uitree: ok (%zu components, every root has a role)\n",
                    sizeof entries / sizeof *entries);
        return 0;
    }
    std::fputs(dump.c_str(), stdout);
    return 0;
}
