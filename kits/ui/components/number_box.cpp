#include "number_box.h"

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <string>
#include <utility>

namespace aurora::ui {

namespace {

float text_height(const Env& e, const TextStyle& st) {
    au_font_metrics m{};
    au_font_metrics_get(e.face(st), e.sized(st), &m);
    return m.ascent + m.descent + m.leading;
}

float text_baseline(const Env& e, Rect r, const TextStyle& st) {
    au_font_metrics m{};
    au_font_metrics_get(e.face(st), e.sized(st), &m);
    return r.y + (r.h + m.ascent - m.descent) / 2;
}

// %.10g because a spinbutton must not show 0.30000000000000004 after three presses of 0.1:
// the binding keeps full precision, the field shows the shortest form that round-trips visibly.
std::string format_number(double v) {
    char b[40];
    std::snprintf(b, sizeof b, "%.10g", v);
    return std::string(b);
}

// A plus or minus mark, drawn at the stroke weight of the other glyph buttons so the steppers
// belong to the same family rather than looking like two spare lines.
void draw_sign(au_surface* s, Rect r, bool plus, au_color c) {
    const float cx = r.x + r.w * 0.5f;
    const float cy = r.y + r.h * 0.5f;
    const float arm = std::max(3.0f, std::min(r.w, r.h) * 0.22f);
    au_stroke_line(s, cx - arm, cy, cx + arm + 1, cy, c, 1.5f);
    if (plus) au_stroke_line(s, cx, cy - arm, cx, cy + arm + 1, c, 1.5f);
}

class NumberNode : public Node {
public:
    const char* Role() const override { return "spinbutton"; }

    NumberNode(Binding<double> v, double lo, double hi, double step,
               std::function<void(double)> f)
        : v_(std::move(v)), lo_(lo), hi_(hi), step_(step), f_(std::move(f)) {
        if (lo_ > hi_) std::swap(lo_, hi_);
        v_.attach(this);
    }

    Size Measure(const Env& e, Constraints c) override {
        const float h = text_height(e, st_) + e.unit * 1.5f;
        side_ = std::round(h);                  // a square stepper: the glyph gets its room
        const float tw = std::max(au_text_width(e.face(st_), e.sized(st_), format_number(lo_).c_str(), -1),
                                  au_text_width(e.face(st_), e.sized(st_), format_number(hi_).c_str(), -1));
        const float natural = tw + side_ * 2 + e.unit * 2;
        return Size{c.max_w >= 0 ? std::min(c.max_w, natural) : natural, h};
    }
    void SetStyle(const TextStyle& st) override { st_ = st; }

    void Draw(au_surface* s, const Env& e) override {
        // One skin resolution for the whole control (8.5): the field, its ink and the focus ring
        // all come from the same state, so hover and focus cannot disagree about what is lit.
        const VState vs = focused_ ? VState::Focused : (over_ ? VState::Hover : VState::Normal);
        const ControlSkin skin = ResolveSkin(e, vs);
        const au_color fill = skin.fill.a ? skin.fill : e.field;
        au_fill_round_rect(s, frame.box(), 4, 4, fill);

        // A narrow field gives up stepper width rather than letting the two zones meet in the
        // middle and leave the number no box at all.
        const float side = stepper_w();
        const Rect minus{frame.x, frame.y, side, frame.h};
        const Rect plus{frame.x + frame.w - side, frame.y, side, frame.h};
        // The pressed stepper shows which half took the press; without it a step is a number
        // that changed with nothing to say the click landed on the control and not the field.
        if (down_ == -1) au_fill_rect(s, au_rect{minus.x + 1, minus.y + 1,
                                                 minus.x + minus.w - 2, minus.y + minus.h - 2},
                                      e.surface_hi);
        if (down_ == 1) au_fill_rect(s, au_rect{plus.x + 1, plus.y + 1,
                                                plus.x + plus.w - 2, plus.y + plus.h - 2},
                                     e.surface_hi);
        draw_sign(s, minus, false, skin.ink);
        draw_sign(s, plus, true, skin.ink);
        au_stroke_line(s, minus.x + minus.w, frame.y + 2, minus.x + minus.w, frame.y + frame.h - 3,
                       e.hairline, 1);
        au_stroke_line(s, plus.x, frame.y + 2, plus.x, frame.y + frame.h - 3, e.hairline, 1);

        const Rect mid{minus.x + minus.w, frame.y, frame.w - side * 2, frame.h};
        au_clip_set(s, au_rect{mid.x + 2, mid.y, mid.x + mid.w - 3, mid.y + mid.h - 1});
        const std::string shown = editing_ ? buffer_ : format_number(v_.get_or(lo_));
        const float tw = au_text_width(e.face(st_), e.sized(st_), shown.c_str(), -1);
        float tx = mid.x + (mid.w - tw) * 0.5f;
        if (tx < mid.x + 2) tx = mid.x + 2;
        au_text_draw(s, e.face(st_), e.sized(st_), shown.c_str(), -1, tx,
                     text_baseline(e, mid, st_), skin.ink);
        if (focused_ && editing_ && caret_on_)
            au_fill_rect(s, au_rect{tx + tw + 1, frame.y + 3, tx + tw + 2, frame.y + frame.h - 4},
                         e.accent);
        au_clip_reset(s);

        if (skin.ring_visible) au_stroke_round_rect(s, frame.box(), 4, 4, skin.ring, 1.5f);
    }

    void OnEnter() override { over_ = true; }
    void OnLeave() override { over_ = false; down_ = 0; }
    void OnPressAt(float x, float, int) override {
        const float side = stepper_w();
        if (x < frame.x + side) { down_ = -1; step(-1); return; }
        if (x >= frame.x + frame.w - side) { down_ = 1; step(1); return; }
        // A press in the field means "type over this", so the next digit starts a fresh buffer
        // rather than appending to the formatted value.
        down_ = 0;
        editing_ = false;
        buffer_.clear();
    }
    void OnRelease(bool) override { down_ = 0; }

    bool CanFocus() const override { return true; }
    void OnFocus(bool g) override {
        focused_ = g;
        caret_on_ = true;
        if (g) { editing_ = false; buffer_.clear(); }
        else if (editing_) commit();     // clicking away keeps what was typed, as a field does
    }
    int TickMs() const override { return focused_ && editing_ ? 530 : 0; }
    void OnTick() override { caret_on_ = !caret_on_; }

    bool OnKey(const au_key& k) override {
        if (!k.pressed) return false;
        switch (k.raw_char) {
            case 0x1c: case 0x1f: step(-1); return true;          // left, down
            case 0x1d: case 0x1e: step(1); return true;           // right, up
            case 0x0c: step(-10); return true;                    // PageDown
            case 0x0b: step(10); return true;                     // PageUp
            case 0x01: put(lo_); return true;                     // Home
            case 0x04: put(hi_); return true;                     // End
            case 0x0a: case 0x0d:                                       // Enter commits
                if (!editing_) return false;    // an unedited field leaves Enter for the dialog
                commit();
                return true;
            case 0x1b:                                            // Escape discards
                editing_ = false;
                buffer_.clear();
                return true;
            case 0x08: case 0x7f:                                 // Backspace, Delete
                if (!editing_) { buffer_ = format_number(v_.get_or(lo_)); editing_ = true; }
                if (!buffer_.empty()) buffer_.pop_back();
                caret_on_ = true;
                return true;
            default: break;
        }
        const char c = k.utf8[0];
        const bool numeric = (c >= '0' && c <= '9') || c == '.' || c == '-';
        if (!numeric || k.utf8[1] != 0) return false;
        if (c == '-' && !buffer_.empty()) return true;            // the sign belongs at the front
        if (!editing_) { buffer_.clear(); editing_ = true; }
        if (c == '-') buffer_.insert(buffer_.begin(), '-');
        else buffer_.push_back(c);
        caret_on_ = true;
        return true;
    }

private:
    float stepper_w() const { return std::min(side_, frame.w * 0.4f); }

    void step(int dir) {
        put(StepNumber(v_.get_or(lo_), step_, lo_, hi_, dir));
    }
    void put(double nv) {
        nv = ClampNumber(nv, lo_, hi_);
        const double cur = v_.get_or(lo_);
        if (nv == cur) return;
        v_.set(nv);
        if (f_) f_(nv);
    }
    void commit() {
        if (buffer_.empty()) { editing_ = false; return; }
        char* end = nullptr;
        const double parsed = std::strtod(buffer_.c_str(), &end);
        // Strict on purpose: "12abc" is not a number, and silently accepting the 12 would make
        // the field agree with something nobody typed.
        if (end == buffer_.c_str() || *end != '\0' || !std::isfinite(parsed)) {
            editing_ = false;
            buffer_.clear();
            return;
        }
        editing_ = false;
        buffer_.clear();
        put(parsed);
    }

    Binding<double> v_;
    double lo_, hi_, step_;
    std::function<void(double)> f_;
    TextStyle st_;
    float side_ = 24;
    std::string buffer_;
    int down_ = 0;
    bool over_ = false, focused_ = false, editing_ = false, caret_on_ = true;
};

}   // namespace

double ClampNumber(double v, double lo, double hi) {
    if (lo > hi) std::swap(lo, hi);
    if (!std::isfinite(v)) return lo;     // refused: a NaN must not reach the binding
    return std::max(lo, std::min(hi, v));
}

double StepNumber(double v, double step, double lo, double hi, int dir) {
    if (!std::isfinite(v)) return ClampNumber(v, lo, hi);
    if (dir == 0 || !std::isfinite(step) || step <= 0) return ClampNumber(v, lo, hi);
    const double next = v + step * static_cast<double>(dir);
    return ClampNumber(next, lo, hi);
}

Desc NumberBox(Binding<double> value, double min, double max, double step,
               std::function<void(double)> changed) {
    return std::make_shared<NumberNode>(std::move(value), min, max, step, std::move(changed));
}

}   // namespace aurora::ui
