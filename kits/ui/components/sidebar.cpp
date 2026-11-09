#include "sidebar.h"

#include <memory>
#include <utility>

namespace aurora::ui {
namespace {

// Why this component is a node at all: the source a Sidebar builds must live exactly as long as
// the Desc, and TreeView takes a raw pointer because it is documented not to own its source. The
// node holds the shared_ptr, delegates every layout call to the one body, and answers the role.
//
// It forwards Measure/Place/Draw/HitTest/Children and nothing else on purpose. A wrapper that
// re-answered a virtual the body already answers would be a second authority, which is how
// EitherNode once lost a layout; the tree below is the only thing that scrolls, tracks the
// pointer or takes keys.
class SidebarNode : public Node {
public:
    SidebarNode(std::shared_ptr<TreeSource> src, Desc tree, Desc body)
        : src_(std::move(src)), tree_(std::move(tree)), body_(std::move(body)) {}

    const char* Role() const override { return "navigation"; }

    Size Measure(const Env& e, Constraints c) override { return body_->Measure(e, c); }
    void Place(Rect r) override {
        frame = r;
        body_->parent = this;
        body_->Place(r);
    }
    void Draw(au_surface* s, const Env& e) override { body_->Draw(s, e); }
    Node* HitTest(float x, float y) override {
        return frame.has(x, y) ? body_->HitTest(x, y) : nullptr;
    }
    void Children(std::vector<Node*>* out) override { out->push_back(body_.get()); }

    const Desc& tree() const { return tree_; }

private:
    std::shared_ptr<TreeSource> src_;   // keeps the tree's rows askable for as long as the Desc
    Desc tree_;
    Desc body_;
};

}   // namespace

Desc Sidebar(std::vector<SidebarSection> sections, Binding<std::string> selected,
             std::function<void(const std::string&)> navigated) {
    auto src = std::make_shared<SectionListSource>();
    src->sections.reserve(sections.size());
    for (size_t i = 0; i < sections.size(); i++) {
        SidebarSection& s = sections[i];
        // An empty section is not shown: a heading over no destinations claims a group the app
        // does not have. Skipped here rather than in the app, so every sidebar agrees.
        if (s.items.empty()) continue;

        TreeSection sec;
        // A heading id is what the tree remembers and steps over by, so two unnamed sections
        // sharing "section:" would shadow each other. An empty key falls back to POSITION --
        // wrong only if the app reorders sections it never named, which it gave us no way to
        // recognise.
        sec.key = s.key.empty() ? "s" + std::to_string(i) : std::move(s.key);
        sec.label = std::move(s.title);
        sec.rows.reserve(s.items.size());
        for (SidebarItem& it : s.items) {
            TreeItem row;
            row.id = std::move(it.id);
            row.label = std::move(it.label);
            row.enabled = it.enabled;
            row.note = std::move(it.note);
            row.flat = true;   // a destination: no chevron, no children, nothing expands
            sec.rows.push_back(std::move(row));
        }
        src->sections.push_back(std::move(sec));
    }

    Desc tree = TreeView(src.get(), std::move(selected)).name("sidebar");
    if (navigated) OnTreeActivate(tree, std::move(navigated));
    // The tree takes the pane's height rather than the 300 units it asks for on its own: a
    // sidebar beside a list is as tall as the window, and a pane that stopped short would leave
    // the content colour showing under it.
    Desc body = VStack({tree.weight(1.0f)}, 0);
    return std::make_shared<SidebarNode>(std::move(src), std::move(tree), std::move(body));
}

Desc SidebarTree(const Desc& sidebar) {
    if (auto* s = dynamic_cast<SidebarNode*>(sidebar.get())) return s->tree();
    return Desc();
}

void OnSidebarNavigate(const Desc& sidebar, std::function<void(const std::string&)> f) {
    if (auto* s = dynamic_cast<SidebarNode*>(sidebar.get())) OnTreeActivate(s->tree(), std::move(f));
}

}   // namespace aurora::ui
