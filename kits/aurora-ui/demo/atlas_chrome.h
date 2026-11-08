// Atlas's own chrome: the three things docs/atlas-design.md section 2.1 asks for that are
// regions rather than widgets -- a band of chrome colour with a hairline on one edge, and a
// status line whose two halves know each other's width.
//
// Why these are here and not in kits/aurora-ui/ui.h: neither is a control. A Band is "this strip
// of the window is the toolbar", which is an application's layout and not a toolkit widget, and
// a StatusLine is one app's sentence drawn two ways. Both are built from Node, which is public
// API, so this is the toolkit being used rather than extended.
//
// The display-integrity rule is the whole reason StatusLine is a node at all. An
// HStack{Label, Spacer, Label} measures each label on its own and places them where they fall,
// so at 480 wide the two halves of one line overlap and the window shows a sentence that is
// neither of them. A line whose halves are drawn without each knowing the other's width is the
// same defect class this codebase has shipped four times.
#pragma once

#include "../ui.h"

#include <algorithm>
#include <cmath>
#include <string>
#include <vector>

namespace atlas {

using namespace aurora::ui;

// The longest prefix+suffix of `t` that fits in `room`, joined by a horizontal ellipsis. The
// middle goes because the two ends are what identify a path or a sentence; an end-elided status
// line loses the thing that just happened.
inline std::string middle_elide(au_font f, float size, const std::string& t, float room) {
    if (room <= 0) return std::string();
    if (au_text_width(f, size, t.c_str(), -1) <= room) return t;
    static const char* kEll = "\xe2\x80\xa6";
    const float ew = au_text_width(f, size, kEll, -1);
    if (ew > room) return std::string();
    // Byte positions on UTF-8 boundaries only, so a cut never lands inside a character.
    std::vector<size_t> cut;
    for (size_t i = 0; i <= t.size(); i++)
        if (i == t.size() || ((unsigned char)t[i] & 0xC0) != 0x80) cut.push_back(i);
    // Grow the two ends one character at a time, head first, while they still fit.
    size_t head = 0, tail = cut.size() - 1;
    std::string best = kEll;
    for (bool from_head = true;; from_head = !from_head) {
        const size_t nh = from_head ? head + 1 : head;
        const size_t nt = from_head ? tail : tail - 1;
        if (nh >= nt) break;
        const std::string cand =
            t.substr(0, cut[nh]) + kEll + t.substr(cut[nt], t.size() - cut[nt]);
        if (au_text_width(f, size, cand.c_str(), -1) > room) break;
        best = cand;
        head = nh;
        tail = nt;
    }
    return best;
}

// Which colour role the band is filled with, and which edge carries its hairline. Roles, not
// colours: the band is drawn from the Env it is handed, so a theme change is a theme change.
enum class BandFill { Chrome, Content };
enum class BandEdge { None, Top, Bottom, Left };

// A strip of the window: a size it states on either axis (-1 means "whatever the child is"),
// one fill, one hairline, and one child inset by `pad`.
//
// The child is placed in the INSET box, which is how the toolbar's 28 px buttons end up centred
// in a 44 px bar: a stack stretches its children to its cross size, so the only way to give a
// button 28 of 44 is to hand the stack 28.
//
// Stating the WIDTH matters for the preview pane and is the kind of thing that is wrong before
// it is seen: a band that answered "all of it" beside a weighted sibling starved the sibling to
// zero and the preview opened over the whole listing instead of beside it. The toolkit's own
// `sized` carries the same warning for the same reason.
class BandNode : public Node {
public:
    BandNode(Desc c, float w, float h, BandFill fill, BandEdge edge, Insets p)
        : c_(std::move(c)), w_(w), h_(h), fill_(fill), edge_(edge) {
        pad = p;
    }

    Size Measure(const Env& e, Constraints c) override {
        Constraints cc = shrink(c);
        if (w_ >= 0) cc.max_w = w_ - pad.l - pad.r;
        if (h_ >= 0) cc.max_h = h_ - pad.t - pad.b;
        const Size s = c_->Measure(e, cc);
        return Size{w_ >= 0 ? w_ : s.w + pad.l + pad.r,
                    h_ >= 0 ? h_ : s.h + pad.t + pad.b};
    }
    void Place(Rect r) override {
        frame = r;
        c_->parent = this;
        c_->Place(inner());
    }
    void Draw(au_surface* s, const Env& e) override {
        if (frame.w <= 0 || frame.h <= 0) return;
        au_fill_rect(s, frame.box(), fill_ == BandFill::Chrome ? e.surface : e.content);
        c_->Draw(s, e);
        // One pixel, in `hairline`, drawn last so nothing the child painted sits on it. Regions
        // are separated by hairlines and never by shadows or boxes (section 3.4).
        const float r = frame.x + frame.w - 1, b = frame.y + frame.h - 1;
        switch (edge_) {
            case BandEdge::Top:    au_stroke_line(s, frame.x, frame.y, r, frame.y, e.hairline, 1); break;
            case BandEdge::Bottom: au_stroke_line(s, frame.x, b, r, b, e.hairline, 1); break;
            case BandEdge::Left:   au_stroke_line(s, frame.x, frame.y, frame.x, b, e.hairline, 1); break;
            case BandEdge::None:   break;
        }
    }
    Node* HitTest(float x, float y) override {
        if (!frame.has(x, y)) return nullptr;
        if (Node* n = c_->HitTest(x, y)) return n;
        return this;    // the band swallows a click on its own padding; see 6.10
    }
    void Children(std::vector<Node*>* out) override { out->push_back(c_.get()); }

private:
    Desc c_;
    float w_, h_;
    BandFill fill_;
    BandEdge edge_;
};

// A horizontal band: the width it is given, the height it states.
inline Desc Band(Desc child, float h, BandFill fill, BandEdge edge, Insets p = Insets()) {
    return std::make_shared<BandNode>(std::move(child), -1, h, fill, edge, p);
}
// A vertical one: the width it states, the height it is given.
inline Desc SideBand(Desc child, float w, BandFill fill, BandEdge edge, Insets p = Insets()) {
    return std::make_shared<BandNode>(std::move(child), w, -1, fill, edge, p);
}

// The status bar's one line: a count on the left, the last action on the right, and the
// guarantee that they never touch.
//
// The right half is the one that gives, because the left is the shorter and the more often
// read; it middle-elides into whatever the left leaves. If the LEFT alone would fill the line it
// is end-elided too, so the worst case is a short sentence and never two sentences on top of
// one another.
class StatusLineNode : public Node {
public:
    StatusLineNode(State<std::string>* left, State<std::string>* right) : l_(left), r_(right) {
        if (l_) l_->attach(this);
        if (r_) r_->attach(this);
    }

    Size Measure(const Env& e, Constraints c) override {
        return Size{c.max_w >= 0 ? c.max_w : 200, std::round(e.size * 0.85f * 1.35f)};
    }
    void Draw(au_surface* s, const Env& e) override {
        if (frame.w <= 0) return;
        const float cap = e.size * 0.85f;                 // `caption`, 11 at the base of 13
        const float base = frame.y + frame.h * 0.72f;
        const std::string left = l_ ? l_->get() : std::string();
        const std::string right = r_ ? r_->get() : std::string();
        // An error says so in `destructive`, and the design names the two words it starts with
        // so that the colour is never the only carrier of the meaning.
        const bool bad = right.compare(0, 8, "Refused:") == 0 || right.compare(0, 7, "Failed:") == 0;
        const float gap = e.unit;                          // 8 between the halves, at minimum
        // The one italic in the whole window (section 3.1), with a fallback: au_font is a pair
        // of ids and an unset one draws nothing at all, which would hide the half rather than
        // de-emphasise it.
        const au_font em = (e.italic.family || e.italic.style) ? e.italic : e.font;
        const float lw = au_text_width(e.font, cap, left.c_str(), -1);
        // What the right half may have is what the left does not want, less the gap. This is
        // the whole point of the node: the number comes from the other half, not from a guess.
        const float room = std::max(0.0f, frame.w - std::min(lw, frame.w) - gap);
        const std::string rs = middle_elide(em, cap, right, room);
        const float rw = au_text_width(em, cap, rs.c_str(), -1);
        const std::string ls = lw <= frame.w - (rs.empty() ? 0 : rw + gap)
                                   ? left
                                   : middle_elide(e.font, cap, left,
                                                  frame.w - (rs.empty() ? 0 : rw + gap));
        au_text_draw(s, e.font, cap, ls.c_str(), -1, frame.x, base, e.dim);
        if (!rs.empty())
            au_text_draw(s, em, cap, rs.c_str(), -1,
                         frame.x + frame.w - rw, base, bad ? e.destructive : e.dim);
        last_left_w_ = au_text_width(e.font, cap, ls.c_str(), -1);
        last_right_x_ = rs.empty() ? frame.x + frame.w : frame.x + frame.w - rw;
        last_left_ = ls;
        last_right_ = rs;
    }

    // What was actually drawn, for the check that the two halves never overlap (C2.4). The
    // STRINGS as well as the geometry, because the geometry alone cannot fail: a node that
    // shortened the left half until the two fitted would satisfy every arithmetic check while
    // breaking the rule, which is that the right half is the one that gives.
    float left_width() const { return last_left_w_; }
    float right_x() const { return last_right_x_; }
    const std::string& left_shown() const { return last_left_; }
    const std::string& right_shown() const { return last_right_; }

private:
    State<std::string>* l_;
    State<std::string>* r_;
    float last_left_w_ = 0, last_right_x_ = 0;
    std::string last_left_, last_right_;
};

inline Desc StatusLine(State<std::string>* left, State<std::string>* right) {
    return std::make_shared<StatusLineNode>(left, right);
}

}  // namespace atlas
