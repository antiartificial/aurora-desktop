// aurora-uitree: 8.7's check, made runnable. Every component is built once and the tree is dumped
// as "role name" lines, so "appears with a role better than control" is a command rather than a
// claim in a commit message.
//
//   aurora-uitree            # dump
//   aurora-uitree --check    # exit 1 if a component root still answers "control"
#include "components/all.h"

#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

using namespace aurora::ui;

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
        if (role == "control") {
            std::printf("FAIL %s answers only \"control\"\n", e.name);
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
