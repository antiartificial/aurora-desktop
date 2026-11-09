#include "info_bar.h"

#include <algorithm>
#include <vector>

namespace aurora::ui {

const char* InfoSeverityName(InfoSeverity s) {
    switch (s) {
        case InfoSeverity::Info: return "info";
        case InfoSeverity::Warning: return "warning";
        case InfoSeverity::Error: return "error";
    }
    return "info";
}

au_color InfoSeverityInk(const Env& e, InfoSeverity s) {
    switch (s) {
        case InfoSeverity::Info: return e.dim;              // text-2: informative, not urgent
        case InfoSeverity::Warning: return e.warning;
        case InfoSeverity::Error: return e.destructive;     // red is the error role, not `warning-1`
    }
    return e.dim;
}

namespace {

// The strip's own geometry, derived from the environment's unit so a theme that grows spacing
// grows the bar with it. Kept as functions because Measure and Draw must agree about them or the
// text lands outside the mark it belongs to.
float stripe_w(const Env& e) { return std::max(2.0f, e.unit * 0.375f); }
float mark_size(const Env& e) { return std::max(6.0f, e.unit * 0.75f); }
float gap(const Env& e) { return e.unit * 0.625f; }
float pad_h(const Env& e) { return e.unit * 0.75f; }
float pad_v(const Env& e) { return e.unit * 0.5f; }

// Everything left of the message: the stripe, the mark, and the space between them.
float lead(const Env& e) { return stripe_w(e) + gap(e) + mark_size(e) + gap(e); }

float text_h(const Env& e, const TextStyle& st) {
    au_font_metrics m;
    m.ascent = m.descent = m.leading = 0;
    au_font_metrics_get(e.face(st), e.sized(st), &m);
    return m.ascent + m.descent + m.leading;
}

float baseline_in(const Env& e, Rect r, const TextStyle& st) {
    au_font_metrics m;
    m.ascent = m.descent = m.leading = 0;
    au_font_metrics_get(e.face(st), e.sized(st), &m);
    return r.y + (r.h + m.ascent - m.descent) / 2;
}

// End-elision by BINARY SEARCH over the kept length, the same rule the list uses for cells: a
// message that does not fit must say so with an ellipsis rather than be cut mid-word by the clip
// rectangle, which reads as a rendering fault instead of as "there is more". Binary search and
// not one character at a time because each step is a shaping call.
std::string elide_text(au_font f, float size, const std::string& t, float maxw) {
    if (au_text_width(f, size, t.c_str(), -1) <= maxw) return t;
    const char* dots = "...";
    if (au_text_width(f, size, dots, -1) > maxw) return std::string();
    size_t lo = 0, hi = t.size();
    while (lo < hi) {
        const size_t mid = (lo + hi + 1) / 2;
        const std::string cand = t.substr(0, mid) + dots;
        if (au_text_width(f, size, cand.c_str(), -1) <= maxw) lo = mid;
        else hi = mid - 1;
    }
    // Never split a UTF-8 sequence: a byte cut leaves a replacement glyph at the end.
    while (lo > 0 && (static_cast<unsigned char>(t[lo]) & 0xC0) == 0x80) --lo;
    return t.substr(0, lo) + dots;
}

// The mark is a SHAPE as well as a colour. Three severities that differ only in hue are three
// states that render alike for anyone who cannot separate red from amber, which this codebase
// treats as a correctness bug rather than an accessibility nicety.
void draw_mark(au_surface* s, Rect box, InfoSeverity sev, au_color tone) {
    const float cx = box.x + box.w / 2, cy = box.y + box.h / 2;
    const float r = box.w / 2;
    switch (sev) {
        case InfoSeverity::Info:
            au_fill_ellipse(s, au_rect{cx - r, cy - r, cx + r - 1, cy + r - 1}, tone);
            break;
        case InfoSeverity::Warning: {
            // A triangle: its point is up, which is the picture every warning sign already uses.
            const float xy[] = {cx, cy - r, cx + r, cy + r, cx - r, cy + r};
            const int counts[] = {3};
            au_fill_polys(s, xy, counts, 1, tone);
            break;
        }
        case InfoSeverity::Error: {
            // A diamond, so an error reads as an error in silhouette too.
            const float xy[] = {cx, cy - r, cx + r, cy, cx, cy + r, cx - r, cy};
            const int counts[] = {4};
            au_fill_polys(s, xy, counts, 1, tone);
            break;
        }
    }
}

class InfoBarNode : public Node {
public:
    InfoBarNode(InfoSeverity sev, std::string msg, Desc action)
        : sev_(sev), msg_(std::move(msg)), action_(std::move(action)) {}

    // "status" and not "control": a bar is a statement about the pane, and an accessibility tree
    // that hears "control" cannot tell it from a button.
    const char* Role() const override { return "status"; }

    void SetStyle(const TextStyle& st) override { st_ = st; }

    Size Measure(const Env& e, Constraints c) override {
        act_ = Size{0, 0};
        // The padding resolved HERE and reused by Place and Draw: a unit that changes between
        // passes would otherwise leave the action beside the text it was laid out against.
        pad_h_ = pad_h(e);
        gap_ = gap(e);
        // The node's own pad is honoured, because a caller that asks for more room around a strip
        // means it, and a component that silently dropped it would be the one place padding lies.
        const Constraints in = shrink(c);
        if (action_) {
            const Constraints sub{in.max_w >= 0 ? in.max_w : -1, -1};
            act_ = action_->Measure(e, sub);
        }
        const float fixed = lead(e) + pad_h_ * 2 + (action_ ? act_.w + gap_ : 0.0f);
        const float tw = au_text_width(e.face(st_), e.sized(st_), msg_.c_str(), -1);
        float w = tw + fixed;
        // Never answer wider than the offer: the message elides instead, and the action keeps its
        // natural width because a button squeezed to nothing is a button nobody can press.
        if (in.max_w >= 0) w = std::min(w, std::max(fixed, in.max_w));
        float h = std::max(text_h(e, st_), act_.h) + pad_v(e) * 2;
        if (in.max_h >= 0) h = std::min(h, std::max(0.0f, in.max_h));
        return Size{w + pad.l + pad.r, h + pad.t + pad.b};
    }

    void Place(Rect r) override {
        frame = r;
        if (!action_) return;
        // Measured size, not a new measurement: Measure may run once per pass and Place must be
        // free to run after it without asking the child a second time.
        const Rect in = inner();
        const float x = in.x + in.w - pad_h_ - act_.w;
        const float y = in.y + (in.h - act_.h) / 2;
        action_->parent = this;
        action_->Place(Rect{x, y, act_.w, act_.h});
    }

    void Draw(au_surface* s, const Env& e) override {
        const Rect band = inner();
        const au_color tone = InfoSeverityInk(e, sev_);
        au_fill_round_rect(s, band.box(), 5, 5, e.surface_hi);
        // The stripe runs the full height so the severity is still legible when the bar is wide
        // and the mark is far from where the eye starts.
        au_fill_round_rect(s, au_rect{band.x, band.y, band.x + stripe_w(e) - 1,
                                      band.y + band.h - 1},
                           2, 2, tone);
        const Rect mark{band.x + stripe_w(e) + gap(e), band.y + (band.h - mark_size(e)) / 2,
                        mark_size(e), mark_size(e)};
        draw_mark(s, mark, sev_, tone);

        const float tx = mark.x + mark.w + gap(e);
        const float trail = action_ ? act_.w + gap(e) : 0.0f;
        const float avail = std::max(0.0f, band.x + band.w - pad_h(e) - trail - tx);
        const std::string shown = elide_text(e.face(st_), e.sized(st_), msg_, avail);
        au_text_draw(s, e.face(st_), e.sized(st_), shown.c_str(), -1, tx,
                     baseline_in(e, band, st_), e.ink(st_));

        if (action_) action_->Draw(s, e);
    }

    // Only the action is a target. The bar itself says something; making the whole strip a hit
    // area would swallow clicks meant for the pane it sits in.
    Node* HitTest(float x, float y) override {
        if (!frame.has(x, y)) return nullptr;
        return action_ ? action_->HitTest(x, y) : nullptr;
    }

    void Children(std::vector<Node*>* out) override {
        if (action_) out->push_back(action_.get());
    }

private:
    InfoSeverity sev_;
    std::string msg_;
    Desc action_;
    TextStyle st_;
    Size act_{0, 0};
    float pad_h_ = 6;      // resolved in Measure, reused by Place so the two cannot disagree
    float gap_ = 5;
};

}   // namespace

Desc InfoBar(InfoSeverity severity, std::string message) {
    return std::make_shared<InfoBarNode>(severity, std::move(message), Desc{});
}

Desc InfoBar(InfoSeverity severity, std::string message, std::string action_label,
             std::function<void()> on_action) {
    Desc action = Button(std::move(action_label), std::move(on_action));
    return std::make_shared<InfoBarNode>(severity, std::move(message), std::move(action));
}

}   // namespace aurora::ui
