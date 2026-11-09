#include "toolbar.h"

#include <memory>

namespace aurora::ui {
namespace {

// The chrome plate, and the only reason this component is a node rather than a stack with a role:
// nothing in the toolkit paints a CONTAINER. A bar that did not paint would be a row of buttons
// floating on the content, and the hairline under it is what tells the eye where the content
// starts (6.9), so both belong to the bar and not to whatever is stacked under it.
class ToolbarBand : public Node {
public:
    ToolbarBand(Desc row, float height, bool hairline)
        : row_(std::move(row)), height_(height), hairline_(hairline) {}

    Size Measure(const Env& e, Constraints c) override {
        // The row is measured once, with the space the bar's ends leave. Its height is asked for
        // and then thrown away: 6.9 fixes the bar at 44, so a taller control inside it -- a
        // path field, a 32 px button -- cannot make the bar grow and shift the content below it.
        const Size nat = row_->Measure(e, shrink(c));
        // A bar spans what it is offered rather than hugging its buttons: 6.9 puts the toolbar
        // across the whole window, and a bar that sized to its contents would leave the content
        // colour showing at its right end. Only an unbounded measure (a test, a nested HStack)
        // falls back to the contents.
        const float w = c.max_w >= 0 ? c.max_w : nat.w + pad.l + pad.r;
        return Size{w, height_};
    }

    void Place(Rect r) override {
        frame = r;
        Rect in = inner();
        // A caller that hands the bar less height than 44 (a squeezed window, a test placing it in
        // a small box) still gets a bar rather than a negative child box.
        if (in.h < 0) in.h = 0;
        if (in.w < 0) in.w = 0;
        row_->parent = this;
        row_->Place(in);
    }

    void Draw(au_surface* s, const Env& e) override {
        au_fill_rect(s, frame.box(), e.surface);
        // Filled, not stroked: a 1 px stroke on a fractional edge is two half-lit rows, and the
        // rule under a toolbar has to be one row of `hairline` or the bar looks blurred.
        if (hairline_ && frame.h >= 1) {
            const float y = frame.y + frame.h - 1;
            au_fill_rect(s, au_rect{frame.x, y, frame.x + frame.w - 1, y}, e.hairline);
        }
        row_->Draw(s, e);
    }

    Node* HitTest(float x, float y) override {
        if (!frame.has(x, y)) return nullptr;
        // Not `this`: the band has no behaviour of its own, so a click on the bar's empty space
        // must reach nothing rather than a container that swallows it.
        return row_->HitTest(x, y);
    }

    void Children(std::vector<Node*>* out) override { out->push_back(row_.get()); }

private:
    Desc row_;
    float height_;
    bool hairline_;
};

}   // namespace

Desc Toolbar(std::vector<ToolbarGroup> leading, Desc location,
             std::vector<ToolbarGroup> trailing, ToolbarOpts opts) {
    // One flat row of groups rather than one stack per side: the groups are the same thing on
    // both sides of the slot, and the gaps between them and around the slot are the same 12 px.
    std::vector<Desc> row;
    for (ToolbarGroup& g : leading) {
        if (g.items.empty()) continue;
        row.push_back(HStack(std::move(g.items), opts.item_gap));
    }
    // The slot takes every pixel the clusters do not. A Spacer holds it when there is no location,
    // so a command-only bar keeps its trailing cluster at the right edge instead of sliding it up
    // against the leading one.
    if (location) row.push_back(location.weight(1.0f));
    else row.push_back(Spacer());
    for (ToolbarGroup& g : trailing) {
        if (g.items.empty()) continue;
        row.push_back(HStack(std::move(g.items), opts.item_gap));
    }

    Desc bar = std::make_shared<ToolbarBand>(HStack(std::move(row), opts.group_gap),
                                             opts.height, opts.hairline);
    bar.padding(opts.pad);
    return WithRole(bar, "toolbar");
}

Desc ToolbarButton(Glyph g, std::string name, std::function<void()> run, Binding<bool> enabled) {
    Desc b = IconButton(g, std::move(run), std::move(enabled));
    // An unnamed button is left unnamed on purpose: Desc::name("") would claim a name no lookup
    // can find, and DuplicateNames would then report every unnamed button as a collision.
    if (!name.empty()) b.name(std::move(name));
    return b;
}

}   // namespace aurora::ui
