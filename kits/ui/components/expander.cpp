#include "expander.h"

#include <algorithm>
#include <cmath>
#include <utility>
#include <vector>

namespace aurora::ui {
namespace {

float text_height(const Env& e, const TextStyle& st) {
    au_font_metrics m{};
    au_font_metrics_get(e.face(st), e.sized(st), &m);
    return m.ascent + m.descent + m.leading;
}

float baseline_in(const Env& e, const Rect& r, const TextStyle& st) {
    au_font_metrics m{};
    au_font_metrics_get(e.face(st), e.sized(st), &m);
    return r.y + (r.h + m.ascent - m.descent) * 0.5f;
}

// The body is wrapped in Reveal so opening and closing use the toolkit's one height transition --
// its duration comes from the environment's motion table and a theme that turns motion down is
// honoured there. A second animation implementation here would be a second place to get the
// degradation path wrong.
Desc wrap_body(Binding<bool>& open, Desc content) {
    // A null child would crash the first Measure, and an empty label is a truthful empty section
    // rather than a widget that pretends it was given something.
    if (!content) content = Label(std::string());
    return Reveal(open, std::move(content));
}

class ExpanderNode : public Node {
public:
    const char* Role() const override { return "group"; }

    ExpanderNode(std::string title, Binding<bool> open, Desc content, Binding<bool> enabled)
        : title_(std::move(title)),
          open_(std::move(open)),
          enabled_(std::move(enabled)),
          body_(wrap_body(open_, std::move(content))) {
        open_.attach(this);
        enabled_.attach(this);
    }

    Size Measure(const Env& e, Constraints c) override {
        unit_ = e.unit;
        // A header is a pointer target first and a line of text second, so it has a floor as
        // well as a text-driven height: a missing font must not shrink the control to a sliver.
        header_h_ = std::max(std::round(text_height(e, st_) + e.unit), std::round(e.unit * 3.0f));

        Constraints inner = shrink(c);
        if (inner.max_w >= 0) inner.max_w = std::max(0.0f, inner.max_w);
        if (inner.max_h >= 0) inner.max_h = std::max(0.0f, inner.max_h);
        Constraints body_c = inner;
        if (body_c.max_h >= 0) body_c.max_h = std::max(0.0f, body_c.max_h - header_h_);

        // The body is measured even when closed. Its WIDTH is part of the group's width, and a
        // group that narrowed while closed would shift everything beside it on every toggle --
        // and would make the next open a reflow of the header too.
        const Size body = body_->Measure(e, body_c);
        const float title_w = au_text_width(e.face(st_), e.sized(st_), title_.c_str(), -1);
        const float head_w = unit_ * 1.8f + title_w;
        return Size{std::max(head_w, body.w) + pad.l + pad.r,
                    header_h_ + body.h + pad.t + pad.b};
    }

    void Place(Rect r) override {
        frame = r;
        const Rect in = inner();
        body_->parent = this;
        body_->Place(Rect{in.x, in.y + header_h_, in.w,
                          std::max(0.0f, in.h - header_h_)});
    }

    void Draw(au_surface* s, const Env& e) override {
        unit_ = e.unit;
        const Rect head{frame.x, frame.y, frame.w, std::min(header_h_, frame.h)};
        const VState state = !enabled() ? VState::Disabled
                                         : (focused_ ? VState::Focused
                                                     : (over_ ? VState::Hover : VState::Normal));
        const ControlSkin skin = ResolveSkin(e, state);

        if (skin.fill.a != 0) au_fill_round_rect(s, head.box(), 6, 6, skin.fill);
        // Inside the header rather than around it: a ring drawn outside would overlap the row
        // above, and this component is normally the first of a group.
        if (skin.ring_visible) au_stroke_round_rect(s, head.box(), 6, 6, skin.ring, 1);

        // The chevron is Glyph::Forward's mark rotated by the open state: along the reading
        // direction when closed, down into the content when open. It is drawn here rather than
        // through IconButton because the whole header is the one control, and because an
        // IconButton cannot turn its glyph with a Binding.
        const float cx = head.x + e.unit * 0.9f;
        const float cy = head.y + head.h * 0.5f;
        const float a = std::max(2.0f, e.unit * 0.3f);
        if (open()) {
            au_stroke_line(s, cx - a, cy - a * 0.55f, cx, cy + a * 0.45f, skin.ink, 1.5f);
            au_stroke_line(s, cx, cy + a * 0.45f, cx + a, cy - a * 0.55f, skin.ink, 1.5f);
        } else {
            au_stroke_line(s, cx - a * 0.55f, cy - a, cx + a * 0.45f, cy, skin.ink, 1.5f);
            au_stroke_line(s, cx + a * 0.45f, cy, cx - a * 0.55f, cy + a, skin.ink, 1.5f);
        }

        // The title is clipped to the header so a long one is cut at the edge instead of painting
        // over a neighbouring pane. There is no middle-elision here: the title is a section name,
        // and the part that identifies it is at the start.
        const float tx = head.x + e.unit * 1.8f;
        const float ty = head.y + e.unit * 0.25f;
        au_clip_set(s, au_rect{tx, ty, head.x + head.w - 2, head.y + head.h - 1});
        au_text_draw(s, e.face(st_), e.sized(st_), title_.c_str(), -1, tx,
                     baseline_in(e, head, st_), skin.ink);
        au_clip_reset(s);

        body_->Draw(s, e);
    }

    void SetStyle(const TextStyle& st) override { st_ = st; }

    // The header is the target; the content is only reachable through its own nodes, which is
    // what keeps a click on a control inside an open section from collapsing the section.
    Node* HitTest(float x, float y) override {
        if (Rect{frame.x, frame.y, frame.w, std::min(header_h_, frame.h)}.has(x, y)) return this;
        return body_->HitTest(x, y);
    }

    void OnEnter() override { over_ = true; }
    void OnLeave() override { over_ = false; }
    void OnRelease(bool inside) override {
        if (inside) flip();
    }

    bool CanFocus() const override { return true; }
    void OnFocus(bool gained) override { focused_ = gained; }

    bool OnKey(const au_key& k) override {
        if (!k.pressed) return false;
        // Space and Enter both toggle, as a Button does. Left and Right are the disclosure
        // convention: Right opens, Left closes, and a direction that changes nothing DECLINES so
        // the window can move focus instead of trapping it.
        if (k.utf8[0] == ' ' || k.raw_char == 0x0a || k.raw_char == 0x0d) return flip();
        if (k.raw_char == 0x1d) return set_open(true);    // right
        if (k.raw_char == 0x1c) return set_open(false);   // left
        return false;
    }

    // The reveal's motion is the layout's too: its height is the group's height. Delegating is
    // what tells the window to lay out again while it moves. Retarget is forwarded as well, so
    // the group can be driven through the handle it was handed -- which is also how a headless
    // test settles it without a Window to walk the tree.
    bool Animating() const override { return body_->Animating(); }
    void Retarget(bool motion_allowed) override { body_->Retarget(motion_allowed); }

    void Children(std::vector<Node*>* out) override { out->push_back(body_.get()); }

private:
    bool open() const { return open_.get_or(false); }
    bool enabled() const { return enabled_.get_or(true); }

    bool flip() { return set_open(!open()); }

    bool set_open(bool want) {
        // A constant or derived Binding has no cell to write; pretending it toggled would leave
        // the header visibly unchanged after a click. A disabled section answers neither.
        if (!enabled() || !open_.writable() || open() == want) return false;
        open_.set(want);
        return true;
    }

    std::string title_;
    Binding<bool> open_;
    Binding<bool> enabled_;
    Desc body_;
    TextStyle st_;
    float unit_ = 8;
    float header_h_ = 24;
    bool over_ = false, focused_ = false;
};

}   // namespace

Desc Expander(std::string title, Binding<bool> open, Desc content, Binding<bool> enabled) {
    return std::make_shared<ExpanderNode>(std::move(title), std::move(open), std::move(content),
                                          std::move(enabled));
}

}   // namespace aurora::ui
