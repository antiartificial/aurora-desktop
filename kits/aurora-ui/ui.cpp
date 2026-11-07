// aurora::ui -- layout, widgets and the window. See ui.h for the model and docs/aurora-ui.md for
// the reasoning.

#include "ui.h"

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <strings.h>
#include <cctype>
#include <cmath>
#include <set>

namespace aurora {
namespace ui {

namespace {

float text_h(const Env& e) {
    au_font_metrics m;
    m.ascent = m.descent = m.leading = 0;
    au_font_metrics_get(e.font, e.size, &m);
    return m.ascent + m.descent + m.leading;
}
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
float baseline_in(const Env& e, Rect r) {
    au_font_metrics m;
    m.ascent = m.descent = m.leading = 0;
    au_font_metrics_get(e.font, e.size, &m);
    return r.y + (r.h + m.ascent - m.descent) / 2;
}

// ---- drag payloads ----------------------------------------------------------------------------

// Percent-encoding, as text/uri-list requires. Only the unreserved set survives literally; a
// space in a filename is the common case and a newline in one is the dangerous case, because
// text/uri-list is line-delimited and an unescaped newline would split one file into two URIs.
static std::string uri_escape(const std::string& p) {
    static const char* hex = "0123456789ABCDEF";
    std::string out;
    for (unsigned char c : p) {
        const bool plain = (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
                           (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' ||
                           c == '~' || c == '/';
        if (plain) {
            out += (char)c;
        } else {
            out += '%';
            out += hex[c >> 4];
            out += hex[c & 15];
        }
    }
    return out;
}

static std::string uri_unescape(const std::string& p) {
    std::string out;
    for (size_t i = 0; i < p.size(); i++) {
        if (p[i] == '%' && i + 2 < p.size() && isxdigit((unsigned char)p[i + 1]) &&
            isxdigit((unsigned char)p[i + 2])) {
            out += (char)strtol(p.substr(i + 1, 2).c_str(), nullptr, 16);
            i += 2;
        } else {
            out += p[i];
        }
    }
    return out;
}

// ---- the scrollbar ----------------------------------------------------------------------------
//
// A VALUE, not a widget. Four things scroll here -- the list, the icon grid, the text view and
// the tree -- and they differ only in what a unit is: a row, a grid row, a wrapped line. Each
// owns a Scroller, lays it out from (total, visible, top) and routes a press to it before its
// own hit handling. Making it a widget instead would mean a container around every scrollable
// thing, and the scrollable thing is the only one that knows what a unit is anyway.
//
// It overlays the right edge rather than taking width from the content. That is the modern
// convention and it is also the only one that works here: taking width would mean the content's
// measured size changed when a directory grew past the last visible row, which is a layout that
// depends on its own result.
//
// It draws only when there is something to scroll. A track with a full-length thumb is furniture
// that says nothing, and a list that fits is a list with nowhere to go.
struct Scroller {
    Rect track;
    float thumb_y = 0, thumb_h = 0;
    float grab = 0;
    bool shown = false, dragging = false, hot = false;

    static constexpr float kWidth = 9;
    static constexpr float kMinThumb = 24;

    // Lay out inside `box` for `n` units of which `vis` are visible, starting at `top`.
    void set(Rect box, int n, int vis, int top) {
        shown = n > vis && vis > 0;
        if (!shown) return;
        track = Rect{box.x + box.w - kWidth, box.y, kWidth, box.h};
        const float frac = (float)vis / (float)n;
        thumb_h = std::max(kMinThumb, track.h * frac);
        const float room = std::max(0.0f, track.h - thumb_h);
        const float at = n - vis > 0 ? (float)top / (float)(n - vis) : 0.0f;
        thumb_y = track.y + room * std::max(0.0f, std::min(1.0f, at));
    }

    void draw(au_surface* s, const Env& e) const {
        if (!shown) return;
        au_fill_rect(s, track.box(), au_color{34, 37, 48, 255});
        const au_color c = (hot || dragging) ? e.accent : e.surface_hi;
        au_fill_round_rect(s, au_rect{track.x + 2, thumb_y, track.x + track.w - 3,
                                      thumb_y + thumb_h - 1},
                           3, 3, c);
    }

    bool hit(float x, float y) const { return shown && track.has(x, y); }

    // A press. On the thumb it starts a drag from where it was grabbed; on the track it pages
    // toward the click, which is the behaviour people expect from a track and which a jump-to-
    // position would break -- a track click is "a bit further", not "go here".
    int press(float y, int n, int vis, int top) {
        if (!shown) return top;
        if (y >= thumb_y && y < thumb_y + thumb_h) {
            dragging = true;
            grab = y - thumb_y;
            return top;
        }
        dragging = false;
        const int page = std::max(1, vis - 1);
        return clamp(y < thumb_y ? top - page : top + page, n, vis);
    }

    int drag(float y, int n, int vis, int top) const {
        if (!dragging || !shown) return top;
        const float room = std::max(1.0f, track.h - thumb_h);
        const float at = (y - grab - track.y) / room;
        return clamp((int)std::lround(at * (n - vis)), n, vis);
    }

    static int clamp(int top, int n, int vis) {
        return std::max(0, std::min(top, std::max(0, n - vis)));
    }
};

// ---- Text -------------------------------------------------------------------------------------

class TextNode : public Node {
public:
    TextNode(std::string t, bool bold) : t_(std::move(t)) { st_.bold = bold; }
    TextNode(State<std::string>* s, bool bold) : s_(s) {
        st_.bold = bold;
        if (s_) s_->attach(this);     // the binding the design promises, made here
    }

    const std::string& str() const { return s_ ? s_->get() : t_; }

    Size Measure(const Env& e, Constraints) override {
        laid_ = str();
        return Size{au_text_width(e.face(st_), e.sized(st_), str().c_str(), -1) + pad.l + pad.r,
                    text_h(e, st_) + pad.t + pad.b};
    }

    // A bound label's WIDTH is its text, so new text is a new layout.
    //
    // Without this, setting the State invalidated the node's old, narrower frame, the window
    // clipped the frame to the damage it was given, and the longer string came out cut off:
    // "3 of 4 selected  9.3 KB" rendered as "3 of 4 s". It looks like the program is confused
    // rather than like a label short of room, which is the worst kind of wrong.
    //
    // The same mistake as Split, Either and Reveal: a state change that alters the LAYOUT and
    // only asks for a repaint.
    bool Animating() const override { return s_ && str() != laid_; }
    void Draw(au_surface* s, const Env& e) override {
        const Rect in = inner();
        au_text_draw(s, e.face(st_), e.sized(st_), str().c_str(), -1, in.x,
                     baseline_in(e, in, st_), e.ink(st_));
    }
    void SetStyle(const TextStyle& st) override {
        const bool keep_bold = st_.bold && !st.bold;   // Bold("x") must stay bold unless asked
        st_ = st;
        if (keep_bold) st_.bold = true;
    }
    Node* HitTest(float, float) override { return nullptr; }   // text is not a target

private:
    std::string t_;
    std::string laid_;            // the text the last layout measured
    State<std::string>* s_ = nullptr;
    TextStyle st_;
};

// ---- Button -----------------------------------------------------------------------------------

class ButtonNode : public Node {
public:
    ButtonNode(std::string l, std::function<void()> f, State<bool>* enabled = nullptr)
        : l_(std::move(l)), f_(std::move(f)), enabled_(enabled) {
        if (enabled_) enabled_->attach(this);
    }

    Size Measure(const Env& e, Constraints) override {
        const float w = au_text_width(e.face(st_), e.sized(st_), l_.c_str(), -1);
        return Size{w + e.unit * 3, text_h(e, st_) + e.unit * 1.75f};
    }
    void SetStyle(const TextStyle& st) override { st_ = st; }
    void Draw(au_surface* s, const Env& e) override {
        // Switched off: the resting fill and dim text, whatever the pointer is doing. It stays
        // visible, as a disabled menu item does, because a button that vanished would hide that
        // the action exists and what is missing before it can happen.
        const bool on = enabled();
        const au_color bg = !on ? e.surface : (down_ ? e.accent : (over_ ? e.surface_hi : e.surface));
        au_fill_round_rect(s, frame.box(), 7, 7, bg);
        // A visible focus ring, always. Hiding where focus is makes a keyboard user guess.
        if (focused_) au_stroke_rect(s, frame.box(), e.accent, 2);
        const float tw = au_text_width(e.face(st_), e.sized(st_), l_.c_str(), -1);
        au_text_draw(s, e.face(st_), e.sized(st_), l_.c_str(), -1, frame.x + (frame.w - tw) / 2,
                     baseline_in(e, frame, st_), !on ? e.dim : (down_ ? e.surface : e.ink(st_)));
    }
    void OnEnter() override { over_ = true; }
    void OnLeave() override { over_ = false; down_ = false; }
    void OnPress() override { down_ = true; }
    void OnRelease(bool inside) override {
        down_ = false;
        if (inside && enabled() && f_) f_();
    }
    bool CanFocus() const override { return true; }
    void OnFocus(bool g) override { focused_ = g; }
    bool OnKey(const au_key& k) override {
        // Space and Enter activate, which is what every toolkit since the 1980s does and what a
        // keyboard-only user reaches for first.
        if (!k.pressed || !enabled()) return false;
        if (k.utf8[0] == ' ' || k.utf8[0] == '\r' || k.utf8[0] == '\n') {
            if (f_) f_();
            return true;
        }
        return false;
    }

private:
    bool enabled() const { return !enabled_ || enabled_->get(); }

    std::string l_;
    std::function<void()> f_;
    State<bool>* enabled_ = nullptr;
    TextStyle st_;
    bool over_ = false, down_ = false, focused_ = false;

public:
    bool is_focused() const { return focused_; }
};

class SpacerNode : public Node {
public:
    Size Measure(const Env&, Constraints) override { return Size{0, 0}; }
    void Draw(au_surface*, const Env&) override {}
    Node* HitTest(float, float) override { return nullptr; }
    bool Wants(float* w) override { *w = 1; return true; }
};

// ---- Checkbox and Switch ----------------------------------------------------------------------

// One class for both, because they are the same control with two drawings and the difference is
// a visual convention, not a behaviour: a checkbox reads as "this option is on", a switch as
// "this thing is running". Splitting them into two classes would duplicate the focus handling,
// the keyboard and the state binding to express a choice of shape.
class ToggleNode : public Node {
public:
    ToggleNode(std::string label, State<bool>* on, bool as_switch, std::function<void(bool)> f)
        : l_(std::move(label)), on_(on), sw_(as_switch), f_(std::move(f)) {
        if (on_) on_->attach(this);
    }

    // The same, but the node OWNS the cell. RadioGroup needs one per option and has nowhere to
    // keep them; a raw `new` there would leak one cell per option per tree.
    ToggleNode(std::string label, std::unique_ptr<State<bool>> own, bool as_switch,
               std::function<void(bool)> f)
        : l_(std::move(label)), owned_(std::move(own)), sw_(as_switch), f_(std::move(f)) {
        on_ = owned_.get();
        if (on_) on_->attach(this);
    }

    Size Measure(const Env& e, Constraints) override {
        box_ = text_h(e, st_) + e.unit * 0.25f;
        const float w = l_.empty() ? 0 : au_text_width(e.face(st_), e.sized(st_), l_.c_str(), -1);
        const float lead = sw_ ? box_ * 1.9f : box_;
        return Size{lead + (l_.empty() ? 0 : e.unit * 0.75f + w), std::max(box_, text_h(e, st_)) + e.unit * 0.5f};
    }
    void SetStyle(const TextStyle& st) override { st_ = st; }

    void Draw(au_surface* s, const Env& e) override {
        const bool on = on_ && on_->get();
        const float cy = frame.y + frame.h * 0.5f;
        if (sw_) {
            // A track with a knob that slides. The knob is drawn at the end it is AT, with no
            // animation: a switch that animated while the window was not would be the one
            // moving thing on a still page.
            const float w = box_ * 1.9f, h = box_ * 0.9f;
            const au_rect track{frame.x, cy - h / 2, frame.x + w - 1, cy + h / 2 - 1};
            au_fill_round_rect(s, track, h / 2, h / 2, on ? e.accent : e.surface_hi);
            const float r = h * 0.40f;
            const float kx = on ? frame.x + w - h / 2 : frame.x + h / 2;
            au_fill_ellipse(s, au_rect{kx - r, cy - r, kx + r, cy + r}, on ? e.surface : e.fg);
        } else {
            const au_rect box{frame.x, cy - box_ / 2, frame.x + box_ - 1, cy + box_ / 2 - 1};
            au_fill_round_rect(s, box, 4, 4, on ? e.accent : e.surface);
            au_stroke_rect(s, box, on ? e.accent : e.surface_hi, 1);
            if (on) {
                // A drawn tick rather than a glyph: a check mark is a font's opinion and plenty
                // of faces do not have one.
                const float x0 = frame.x + box_ * 0.24f, x1 = frame.x + box_ * 0.44f,
                            x2 = frame.x + box_ * 0.76f;
                au_stroke_line(s, x0, cy, x1, cy + box_ * 0.20f, e.surface, 2);
                au_stroke_line(s, x1, cy + box_ * 0.20f, x2, cy - box_ * 0.22f, e.surface, 2);
            }
        }
        if (focused_) au_stroke_rect(s, frame.box(), e.accent, 1);
        if (!l_.empty()) {
            const float lead = sw_ ? box_ * 1.9f : box_;
            au_text_draw(s, e.face(st_), e.sized(st_), l_.c_str(), -1,
                         frame.x + lead + e.unit * 0.75f, baseline_in(e, frame, st_), e.ink(st_));
        }
    }

    Node* HitTest(float x, float y) override { return frame.has(x, y) ? this : nullptr; }
    bool CanFocus() const override { return true; }
    void OnFocus(bool g) override { focused_ = g; }
    void OnRelease(bool inside) override {
        if (inside) flip();
    }
    bool OnKey(const au_key& k) override {
        // Space only. Enter is "accept the dialog" in every toolkit, and a checkbox that
        // swallowed it would make a form impossible to submit from the keyboard.
        if (!k.pressed || k.utf8[0] != ' ') return false;
        flip();
        return true;
    }

private:
    void flip() {
        if (!on_) return;
        on_->set(!on_->get());
        if (f_) f_(on_->get());
    }

    std::string l_;
    std::unique_ptr<State<bool>> owned_;   // set only by the owning constructor
    State<bool>* on_ = nullptr;
    bool sw_;
    std::function<void(bool)> f_;
    TextStyle st_;
    float box_ = 14;
    bool focused_ = false;
};

// ---- Slider -----------------------------------------------------------------------------------

class SliderNode : public Node {
public:
    SliderNode(State<float>* v, float lo, float hi, std::function<void(float)> f)
        : v_(v), lo_(lo), hi_(std::max(hi, lo + 1e-6f)), f_(std::move(f)) {
        if (v_) v_->attach(this);
    }

    Size Measure(const Env& e, Constraints c) override {
        knob_ = text_h(e, st_) * 0.6f;
        // As wide as it is given. A slider's width IS its resolution: a narrow one cannot
        // express the values it claims to, so it takes the room rather than picking a number.
        return Size{c.max_w >= 0 ? c.max_w : 160, knob_ * 2 + e.unit * 0.5f};
    }
    void SetStyle(const TextStyle& st) override { st_ = st; }

    void Draw(au_surface* s, const Env& e) override {
        const float cy = frame.y + frame.h * 0.5f;
        const float x0 = frame.x + knob_, x1 = frame.x + frame.w - knob_;
        au_fill_round_rect(s, au_rect{x0, cy - 2, x1, cy + 1}, 2, 2, e.surface_hi);
        const float t = frac();
        const float kx = x0 + (x1 - x0) * t;
        au_fill_round_rect(s, au_rect{x0, cy - 2, kx, cy + 1}, 2, 2, e.accent);
        au_fill_ellipse(s, au_rect{kx - knob_, cy - knob_, kx + knob_, cy + knob_},
                        down_ || focused_ ? e.accent : e.fg);
        if (focused_) au_stroke_rect(s, frame.box(), e.accent, 1);
    }

    Node* HitTest(float x, float y) override { return frame.has(x, y) ? this : nullptr; }
    bool CanFocus() const override { return true; }
    void OnFocus(bool g) override { focused_ = g; }

    void OnPressAt(float x, float, int) override {
        down_ = true;
        set_from(x);
    }
    void OnDragTo(float x, float) override {
        if (down_) set_from(x);
    }
    void OnRelease(bool) override { down_ = false; }

    bool OnKey(const au_key& k) override {
        if (!k.pressed || !v_) return false;
        // A twentieth of the range per press, a fifth on Page. Arrow keys that moved by one
        // unit would make a 0-to-1 slider take a hundred presses to cross.
        const float step = (hi_ - lo_) / 20.0f;
        switch (k.raw_char) {
            case 0x1c: case 0x1f: put(v_->get() - step); return true;   // left, down
            case 0x1d: case 0x1e: put(v_->get() + step); return true;   // right, up
            case 0x0b: put(v_->get() + (hi_ - lo_) / 5.0f); return true;
            case 0x0c: put(v_->get() - (hi_ - lo_) / 5.0f); return true;
            case 0x01: put(lo_); return true;
            case 0x04: put(hi_); return true;
            default: return false;
        }
    }

private:
    float frac() const {
        if (!v_) return 0;
        return std::max(0.0f, std::min(1.0f, (v_->get() - lo_) / (hi_ - lo_)));
    }
    void set_from(float x) {
        const float x0 = frame.x + knob_, x1 = frame.x + frame.w - knob_;
        const float t = x1 > x0 ? (x - x0) / (x1 - x0) : 0.0f;
        put(lo_ + (hi_ - lo_) * std::max(0.0f, std::min(1.0f, t)));
    }
    void put(float nv) {
        if (!v_) return;
        nv = std::max(lo_, std::min(hi_, nv));
        if (nv == v_->get()) return;
        v_->set(nv);
        if (f_) f_(nv);
    }

    State<float>* v_;
    float lo_, hi_;
    std::function<void(float)> f_;
    TextStyle st_;
    float knob_ = 7;
    bool down_ = false, focused_ = false;
};

// ---- Progress ---------------------------------------------------------------------------------

// Deliberately not focusable and not clickable. A progress bar reports; it is not a control, and
// making it a Tab stop would put a dead stop in a keyboard user's path.
class ProgressNode : public Node {
public:
    explicit ProgressNode(State<float>* v) : v_(v) {
        if (v_) v_->attach(this);
    }

    Size Measure(const Env& e, Constraints c) override {
        return Size{c.max_w >= 0 ? c.max_w : 160, e.unit * 0.75f};
    }
    void Draw(au_surface* s, const Env& e) override {
        const float r = frame.h * 0.5f;
        au_fill_round_rect(s, frame.box(), r, r, e.surface_hi);
        const float t = v_ ? std::max(0.0f, std::min(1.0f, v_->get())) : 0.0f;
        if (t <= 0.0f) return;
        // At least as wide as it is tall, so a value just above zero is a dot rather than a
        // sliver too thin to see: "barely started" and "not started" must not look the same.
        const float w = std::max(frame.h, frame.w * t);
        au_fill_round_rect(s, au_rect{frame.x, frame.y, frame.x + w - 1, frame.y + frame.h - 1}, r, r, e.accent);
    }
    Node* HitTest(float, float) override { return nullptr; }

private:
    State<float>* v_;
};

// ---- Stacks -----------------------------------------------------------------------------------

class StackNode : public Node {
public:
    StackNode(std::vector<Desc> c, bool vertical, float gap)
        : c_(std::move(c)), vert_(vertical), gap_(gap) {}

    Size Measure(const Env& e, Constraints c) override {
        const float gap = gap_ * e.unit;
        const Constraints inner_c = shrink(c);
        float main = 0, cross = 0;
        wsum_ = 0;
        meas_.clear();
        meas_.reserve(c_.size());
        // Each child measured ONCE. Weighted children are measured too, for their cross size and
        // their minimum, but their main size is decided after the leftovers are known.
        for (size_t i = 0; i < c_.size(); i++) {
            const Size s = c_[i]->Measure(e, inner_c);
            meas_.push_back(s);
            float w = c_[i]->weight;
            float ww = 0;
            // An explicit weight() WINS over the node's own Wants. Place already decided it that
            // way, and for a while Measure decided the opposite: Wants overrode the weight here,
            // so weight(Spacer(), 3) was counted as one share while being placed at three. The
            // shares then did not sum to the available space and the stack's children overran
            // their box. Two places deciding the same thing differently is the whole bug, so
            // whichever rule wins, it has to be this one line and that one.
            if (w <= 0 && c_[i]->Wants(&ww)) w = ww > 0 ? ww : 1;
            wsum_ += w;
            main += vert_ ? s.h : s.w;
            cross = std::max(cross, vert_ ? s.w : s.h);
        }
        if (!c_.empty()) main += gap * (c_.size() - 1);
        return vert_ ? Size{cross + pad.l + pad.r, main + pad.t + pad.b}
                     : Size{main + pad.l + pad.r, cross + pad.t + pad.b};
    }

    void Place(Rect r) override {
        frame = r;
        const Rect in = inner();
        const float gap = gap_ * env_unit_;
        float fixed = 0;
        for (size_t i = 0; i < c_.size(); i++) {
            float ww = 0;
            const bool flex = c_[i]->weight > 0 || c_[i]->Wants(&ww);
            if (!flex) fixed += vert_ ? meas_[i].h : meas_[i].w;
        }
        if (!c_.empty()) fixed += gap * (c_.size() - 1);
        const float avail = (vert_ ? in.h : in.w) - fixed;
        const float per = wsum_ > 0 ? std::max(0.0f, avail) / wsum_ : 0;

        float at = vert_ ? in.y : in.x;
        for (size_t i = 0; i < c_.size(); i++) {
            float ww = 0;
            const bool wants = c_[i]->Wants(&ww);
            const float w = c_[i]->weight > 0 ? c_[i]->weight : (wants ? (ww > 0 ? ww : 1) : 0);
            const float m = w > 0 ? per * w : (vert_ ? meas_[i].h : meas_[i].w);
            c_[i]->parent = this;
            c_[i]->Place(vert_ ? Rect{in.x, at, in.w, m} : Rect{at, in.y, m, in.h});
            at += m + gap;
        }
    }

    void Draw(au_surface* s, const Env& e) override {
        env_unit_ = e.unit;
        for (auto& ch : c_) ch->Draw(s, e);
    }

    Node* HitTest(float x, float y) override {
        if (!frame.has(x, y)) return nullptr;
        for (auto& ch : c_) if (Node* n = ch->HitTest(x, y)) return n;
        return nullptr;
    }

    void Children(std::vector<Node*>* out) override {
        for (auto& ch : c_) out->push_back(ch.get());
    }

    void SetUnit(float u) { env_unit_ = u; }

private:
    std::vector<Desc> c_;
    std::vector<Size> meas_;
    bool vert_;
    float gap_;
    float wsum_ = 0;
    float env_unit_ = 8;
};

// ---- ListView ---------------------------------------------------------------------------------

// Rows are built for the visible band plus a margin and for nothing else, which is what makes a
// directory of 50,000 entries cost the same as one of 40.
// Defined further down; the list needs one and the field needs nothing from the list.
Desc make_field(State<std::string>* t, std::function<void(const std::string&)> ch,
                std::function<void(const std::string&)> co, std::function<void()> ca,
                FieldOpts o);

class ListNode : public Node {
public:
    ListNode(Rows* r, int* sel) : r_(r), sel_(sel), edit_text_("") {}

    Size Measure(const Env& e, Constraints c) override {
        row_h_ = text_h(e) + e.unit * 0.75f;
        head_h_ = row_h_ + 2;
        return Size{c.max_w >= 0 ? c.max_w : 400, c.max_h >= 0 ? c.max_h : 300};
    }

    void Draw(au_surface* s, const Env& e) override {
        // Nothing to draw, and more importantly nothing that MAY be drawn: the cells clip to
        // their column, not to the list, so a list placed at zero width went on painting its
        // header and rows across whatever was beside it.
        if (frame.w <= 0 || frame.h <= 0) return;
        au_fill_rect(s, frame.box(), au_color{30, 32, 42, 255});

        // Column widths come from the Rows source and have nothing to do with how much room the
        // list was given, so every column has to be clipped to the list's own right edge and a
        // column starting past it skipped. Without this the header and the cells carried on
        // across the neighbour: opening the preview pane left "Size" and "Mod" painted inside it.
        const float edge = frame.x + frame.w - 1;

        // header
        const int sort_col = r_->SortColumn();
        float x = frame.x;
        for (int col = 0; col < r_->Columns() && x <= edge; col++) {
            const float cw = r_->ColumnWidth(col);
            const bool sorted = col == sort_col;
            au_clip_set(s, au_rect{x, frame.y, std::min(x + cw - 6, edge), frame.y + head_h_});
            const float tx = x + e.unit * 0.75f;
            au_text_draw(s, e.bold, e.size - 1, r_->ColumnTitle(col).c_str(), -1, tx,
                         frame.y + head_h_ * 0.72f, sorted ? e.fg : e.dim);
            if (sorted) {
                // A chevron after the title, pointing the way the rows run. Drawn rather than
                // typed: an arrow glyph is a font's opinion and half of them have none.
                const float ax = tx + au_text_width(e.bold, e.size - 1,
                                                    r_->ColumnTitle(col).c_str(), -1) +
                                 e.unit * 0.5f;
                const float ay = frame.y + head_h_ * 0.5f;
                const float r = 3;
                const float dir = r_->SortAscending() ? -1.0f : 1.0f;
                au_stroke_line(s, ax, ay - r * dir * 0.5f, ax + r, ay + r * dir * 0.5f, e.fg, 1.5f);
                au_stroke_line(s, ax + r, ay + r * dir * 0.5f, ax + r * 2, ay - r * dir * 0.5f,
                               e.fg, 1.5f);
            }
            au_clip_reset(s);
            x += cw;
        }
        au_stroke_line(s, frame.x, frame.y + head_h_, frame.x + frame.w - 1,
                       frame.y + head_h_, au_color{60, 64, 82, 255}, 1);
        if (focused_) au_stroke_rect(s, frame.box(), e.accent, 1);
        if (!ahead_.empty()) {
            // What you typed, shown where you are looking. Type-ahead that gives no feedback
            // feels like the program ignoring you.
            const float w = au_text_width(e.bold, e.size, ahead_.c_str(), -1) + e.unit * 2;
            const au_rect box{frame.x + frame.w - w - 8, frame.y + head_h_ + 6,
                              frame.x + frame.w - 8, frame.y + head_h_ + 6 + row_h_ + 4};
            au_fill_round_rect(s, box, 6, 6, e.accent);
            au_text_draw(s, e.bold, e.size, ahead_.c_str(), -1, box.l + e.unit,
                         box.t + row_h_ * 0.78f + 2, au_color{24, 26, 34, 255});
        }

        const int n = r_->Count();
        const int vis = (int)((frame.h - head_h_) / row_h_);
        const int first = std::max(0, top_);
        const int last = std::min(n - 1, first + vis);
        if (n > 0) r_->Want(std::max(0, first - 8), std::min(n - 1, last + 16));

        for (int i = first; i <= last; i++) {
            const float ry = frame.y + head_h_ + 1 + (i - first) * row_h_;
            if (i == edit_row_ && edit_field_) {
                // The field replaces the row's first cell. Placed every frame because the row
                // moves when the list scrolls, and measured once here rather than in Measure:
                // the list's own size does not depend on it.
                const Rect fr{frame.x + 2, ry - 1, r_->ColumnWidth(0) - 4, row_h_ + 2};
                edit_field_->Measure(e, Constraints{fr.w, fr.h});
                edit_field_->Place(fr);
                edit_field_->Draw(s, e);
                continue;
            }
            const bool picked = is_selected(i);
            if (picked)
                au_fill_rect(s, au_rect{frame.x, ry, frame.x + frame.w - 1, ry + row_h_ - 1},
                             e.accent);
            else if (sel_ && *sel_ == i && focused_)
                // The cursor without the selection: it happens after a Ctrl-click that
                // DESELECTED a row, and losing sight of where the keyboard is would make the
                // next Shift-click a guess.
                au_stroke_rect(s, au_rect{frame.x, ry, frame.x + frame.w - 1, ry + row_h_ - 1},
                               e.surface_hi, 1);
            float cx = frame.x;
            for (int col = 0; col < r_->Columns() && cx <= edge; col++) {
                const CellValue v = r_->Cell(i, col);
                const char* txt = v.state == CellState::Unknown ? "unknown" : v.text.c_str();
                if (v.state == CellState::Pending) txt = "";
                const au_color c = picked ? au_color{24, 26, 34, 255}
                                          : (v.state == CellState::Ready ? e.fg : e.dim);
                au_clip_set(s, au_rect{cx, ry, std::min(cx + r_->ColumnWidth(col) - 6, edge),
                                       ry + row_h_ - 1});
                au_text_draw(s, e.font, e.size, txt, -1, cx + e.unit * 0.75f,
                             ry + row_h_ * 0.72f, c);
                au_clip_reset(s);
                cx += r_->ColumnWidth(col);
            }
        }
        rows_drawn_ = last >= first ? (last - first + 1) : 0;

        // Where the drop would land. A row gets an outline; "no row" outlines the whole list,
        // because dropping below the last row means into this directory and the user is owed
        // the difference between those two before they let go, not after.
        if (drop_active_) {
            if (drop_row_ >= first && drop_row_ <= last) {
                const float ry = frame.y + head_h_ + 1 + (drop_row_ - first) * row_h_;
                au_stroke_rect(s, au_rect{frame.x + 1, ry, frame.x + frame.w - 2, ry + row_h_ - 1},
                               e.accent, 2);
            } else if (drop_row_ < 0) {
                au_stroke_rect(s, frame.box(), e.accent, 2);
            }
        }
        // Below the header, so the thumb measures the rows and not the chrome.
        sb_.set(Rect{frame.x, frame.y + head_h_ + 1, frame.w, frame.h - head_h_ - 1}, n,
                visible(), top_);
        sb_.draw(s, e);
    }

    Node* HitTest(float x, float y) override { return frame.has(x, y) ? this : nullptr; }
    void OnPress() override {}
    void OnRelease(bool inside) override {
        if (header_press_) { header_press_ = false; return; }   // a sort, not a row click
        if (sb_.dragging) { sb_.dragging = false; return; }
        if (on_bar_) { on_bar_ = false; return; }   // a press on the bar is never a row click
        if (!inside || !sel_) return;
        const int i = row_at(click_y_);
        if (i < 0) {
            // Empty space below the rows clears the selection, the way every file manager does.
            // Not with a modifier held: that is someone part-way through building a selection.
            if (!(mods_now() & (AU_MOD_CTRL | AU_MOD_SHIFT))) set_only(-1);
            return;
        }
        const uint32_t m = mods_now();
        if (m & AU_MOD_CTRL) toggle_one(i);
        else if (m & AU_MOD_SHIFT) extend_to(i);
        else select(i);
        if (clicks_ >= 2 && on_act_ && !(m & (AU_MOD_CTRL | AU_MOD_SHIFT))) on_act_(i);
    }

    // A single-select list reads no modifiers, so Ctrl-click and Shift-click are plain clicks.
    uint32_t mods_now() const { return win_ && !single_ ? win_->mods() : 0u; }
    void SetSingleSelect() { single_ = true; }
    void OnPressAt(float x, float y, int clicks) override {
        click_y_ = y;
        clicks_ = clicks;
        // A click in the header sorts by that column, and clicking the sorted one reverses it.
        // Nothing else in the header is clickable, so this is checked before anything else.
        if (y < frame.y + head_h_ && r_->SortColumn() >= -1) {
            float cx = frame.x;
            for (int col = 0; col < r_->Columns(); col++) {
                const float cw = r_->ColumnWidth(col);
                if (x >= cx && x < cx + cw) {
                    const bool same = col == r_->SortColumn();
                    r_->Sort(col, same ? !r_->SortAscending() : true);
                    top_ = 0;              // the rows under you are different rows now
                    whole_ = true;
                    dirty_ = true;
                    header_press_ = true;
                    return;
                }
                cx += cw;
            }
        }
        header_press_ = false;
        on_bar_ = sb_.hit(x, y);
        if (!on_bar_) return;
        const int to = sb_.press(y, r_->Count(), visible(), top_);
        if (to != top_) { top_ = to; whole_ = true; dirty_ = true; }
    }
    void OnDragTo(float, float y) override {
        if (!sb_.dragging) return;
        const int to = sb_.drag(y, r_->Count(), visible(), top_);
        if (to != top_) { top_ = to; whole_ = true; dirty_ = true; }
    }
    void SetClickY(float y) { click_y_ = y; }
    void SetActivateHandler(std::function<void(int)> f) { on_act_ = std::move(f); }
    void SetDragPaths(std::function<std::vector<std::string>(int)> f) { drag_paths_ = std::move(f); }
    void SetDropHandler(std::function<bool(int, const DragData&)> f) { on_drop_ = std::move(f); }

    // The row under a y, or -1 for the header, past the last row, or outside. One place decides
    // it, because a press, a drag and a drop all have to agree about which row they mean.
    int row_at(float y) const {
        if (y < frame.y + head_h_ + 1) return -1;
        const int i = top_ + (int)((y - frame.y - head_h_ - 1) / row_h_);
        return (i >= 0 && i < r_->Count()) ? i : -1;
    }

    bool OnDragStart(float x, float y, DragData* out) override {
        // The app says what a row IS -- the list only knows cells of text, and a path is the
        // app's business. No mapping means this list is not a drag source, which is the right
        // default: a list of search results should not hand out file URIs by accident.
        if (!drag_paths_ || sb_.hit(x, y)) return false;
        const int row = row_at(y);
        if (row < 0) return false;
        // Dragging a row that is part of a selection drags the WHOLE selection; dragging one
        // outside it drags just that row. Anything else loses files on the way to a folder.
        std::vector<int> rows;
        if (is_selected(row) && multi_.size() > 1) rows = selection();
        else rows.push_back(row);
        std::vector<std::string> p;
        for (int r : rows) {
            const std::vector<std::string> one = drag_paths_(r);
            p.insert(p.end(), one.begin(), one.end());
        }
        if (p.empty()) return false;
        *out = DragData::uri_list(p);
        return true;
    }

    bool CanAcceptDrop(const DragData& d) const override {
        return on_drop_ && d.mime == "text/uri-list" && !d.paths.empty();
    }

    void OnDropHover(float x, float y, bool inside) override {
        (void)x;
        const int r = inside ? row_at(y) : -1;
        if (r == drop_row_ && inside == drop_active_) return;
        drop_row_ = r;
        drop_active_ = inside;
        whole_ = true;
        dirty_ = true;
    }

    bool OnDrop(float x, float y, const DragData& d) override {
        (void)x;
        // -1 means the list's own directory: dropping below the last row is dropping "in here",
        // which is what every file manager means by it.
        const int r = row_at(y);
        drop_row_ = -1;
        drop_active_ = false;
        whole_ = true;
        return on_drop_ ? on_drop_(r, d) : false;
    }

    int visible() const { return std::max(1, (int)((frame.h - head_h_) / row_h_)); }

    void ScrollBy(int rows) {
        const int before = top_;
        top_ = std::max(0, top_ + rows);
        top_ = std::min(top_, std::max(0, r_->Count() - visible()));
        if (top_ != before) { dirty_ = true; whole_ = true; }
    }
    // Keep the selection on screen after a keyboard move, scrolling the least that will do it.
    void Reveal(int row) {
        if (row < top_) { top_ = row; dirty_ = true; }
        else if (row >= top_ + visible()) { top_ = row - visible() + 1; dirty_ = true; }
    }

    bool OnWheel(float dy) override {
        ScrollBy(dy > 0 ? 3 : -3);
        return true;
    }

    bool CanFocus() const override { return true; }
    void OnFocus(bool g) override { focused_ = g; whole_ = true; dirty_ = true; }

    // ---- inline rename ----

    void BeginRename(int row) {
        if (row < 0 || row >= r_->Count() || !on_ren_) return;
        std::string name = r_->Cell(row, 0).text;
        if (!name.empty() && name.back() == '/') name.pop_back();   // edit the name, not the mark
        edit_row_ = row;
        edit_text_ = State<std::string>(name);
        edit_text_.bind(win_);
        FieldOpts o;
        o.select_all_on_focus = true;      // typing replaces, which is what a rename usually is
        edit_field_ = make_field(
            &edit_text_, nullptr,
            [this](const std::string& v) { CommitRename(v); },
            [this] { CancelRename(); }, o);
        Reveal(row);
        whole_ = true;
        dirty_ = true;
    }
    void CommitRename(const std::string& v) {
        if (edit_row_ < 0) return;
        if (v.empty()) { CancelRename(); return; }
        // A handler that refuses keeps the field open: discarding what someone typed because the
        // name collided is worse than making them change it.
        if (on_ren_ && !on_ren_(edit_row_, v)) return;
        edit_row_ = -1;
        edit_field_.reset();
        whole_ = true;
        dirty_ = true;
    }
    void CancelRename() {
        edit_row_ = -1;
        edit_field_.reset();
        whole_ = true;
        dirty_ = true;
    }
    bool renaming() const { return edit_row_ >= 0 && edit_field_ != nullptr; }
    void SetRenameHandler(std::function<bool(int, const std::string&)> f) { on_ren_ = std::move(f); }
    void SetSelectHandler(std::function<void(int)> f) { on_sel_ = std::move(f); }

    // Every path that moves the selection goes through here, so a listener cannot miss one.
    // ---- selection ----
    //
    // `*sel_` stays what it always was: the CURSOR, the row the keyboard is on and the one a
    // preview pane follows. The set is what is highlighted. They are usually the same single
    // row, and they come apart exactly when a file manager needs them to -- Ctrl-click adds a
    // row without moving the cursor's meaning, Shift-click sweeps a range from the anchor.
    //
    // Keeping the old int rather than replacing it with the set is deliberate: every existing
    // caller, every preview pane and every activate handler keeps working, and a widget that
    // never multi-selects pays nothing.
    void select(int row) {
        set_only(row);
        anchor_ = row;
    }

    void set_only(int row) {
        const bool same = sel_ && *sel_ == row && multi_.size() == 1 && multi_.count(row);
        if (same) return;
        mark_all_dirty();
        multi_.clear();
        if (row >= 0) multi_.insert(row);
        if (sel_) {
            const int before = *sel_;
            *sel_ = row;
            rows_dirty_.push_back(before);
        }
        rows_dirty_.push_back(row);
        dirty_ = true;
        if (on_sel_) on_sel_(row);
        if (on_multi_) on_multi_(selection());
    }

    // Ctrl-click: add or remove one row, leaving the rest alone.
    void toggle_one(int row) {
        if (row < 0) return;
        if (multi_.count(row)) multi_.erase(row);
        else multi_.insert(row);
        // The cursor follows the row you touched even when you have just DESELECTED it, because
        // the next Shift-click has to sweep from somewhere and that somewhere is here.
        if (sel_) *sel_ = row;
        anchor_ = row;
        rows_dirty_.push_back(row);
        dirty_ = true;
        if (on_sel_) on_sel_(row);
        if (on_multi_) on_multi_(selection());
    }

    // Shift-click: everything between the anchor and here, replacing what was selected.
    void extend_to(int row) {
        if (row < 0) return;
        if (anchor_ < 0) anchor_ = row;
        mark_all_dirty();
        multi_.clear();
        const int lo = std::min(anchor_, row), hi = std::max(anchor_, row);
        for (int i = lo; i <= hi && i < r_->Count(); i++)
            if (i >= 0) multi_.insert(i);
        if (sel_) *sel_ = row;
        dirty_ = true;
        if (on_sel_) on_sel_(row);
        if (on_multi_) on_multi_(selection());
    }

    void select_all() {
        mark_all_dirty();
        multi_.clear();
        for (int i = 0; i < r_->Count(); i++) multi_.insert(i);
        dirty_ = true;
        if (on_multi_) on_multi_(selection());
    }

    std::vector<int> selection() const { return std::vector<int>(multi_.begin(), multi_.end()); }
    bool is_selected(int row) const { return multi_.count(row) != 0; }
    void SetMultiHandler(std::function<void(const std::vector<int>&)> f) { on_multi_ = std::move(f); }

private:
    void mark_all_dirty() {
        // A change that can touch any highlighted row cannot name them one by one cheaply, and
        // the rows that LOSE their highlight are as dirty as the ones that gain it.
        whole_ = true;
    }

public:
    void Attached(Window* w) override { win_ = w; }

    bool OnKey(const au_key& k) override {
        if (!k.pressed) return false;
        // While renaming, the field has the keyboard. Everything below is the list's bindings and
        // must not fire: typing a name should not also jump the selection via type-ahead.
        if (renaming()) return edit_field_->OnKey(k);
        if (!sel_) return false;
        const int n = r_->Count();
        if (n == 0) return false;
        const int cur = *sel_ < 0 ? -1 : *sel_;
        // F2 by its evdev code, not by raw_char. raw_char carries what a key TYPES, and a
        // function key types nothing -- BeOS reports B_FUNCTION_KEY for all of them and
        // distinguishes by scancode, so a raw_char test for F2 is meaningless. KEY_F2 is 60.
        if (k.evdev == 60 && cur >= 0) { BeginRename(cur); return true; }
        // Enter activates the selection, the same thing a double-click means.
        if ((k.raw_char == 0x0a || k.raw_char == 0x0d) && cur >= 0) {
            if (on_act_) on_act_(cur);
            return true;
        }
        int next = cur;
        switch (k.raw_char) {
            case 0x1e: next = cur <= 0 ? 0 : cur - 1; break;                        // up
            case 0x1f: next = cur < 0 ? 0 : std::min(n - 1, cur + 1); break;        // down
            case 0x0b: next = std::max(0, cur - visible()); break;                  // page up
            case 0x0c: next = std::min(n - 1, (cur < 0 ? 0 : cur) + visible()); break;  // page down
            case 0x01: next = 0; break;                                             // home
            case 0x04: next = n - 1; break;                                         // end
            default: next = cur; break;
        }
        // Ctrl+A is the one shortcut a list owes a keyboard user, and it is checked before the
        // arrows because 'a' is also a type-ahead character.
        // raw_char, not utf8: utf8 carries the modifiers, so Ctrl+A types 0x01 and never
        // compares equal to 'a'. raw_char is the layout's level-0 symbol, which is the letter.
        if (!single_ && (k.mods & AU_MOD_CTRL) && (k.raw_char | 0x20) == 'a') {
            select_all();
            return true;
        }
        if (next != cur) {
            const int before_top = top_;
            // Shift+arrow sweeps from the anchor instead of moving a single selection, which is
            // how a keyboard user selects a run of files without touching the mouse.
            if (!single_ && (k.mods & AU_MOD_SHIFT)) extend_to(next);
            else select(next);
            Reveal(next);
            ahead_.clear();
            if (top_ != before_top) whole_ = true;        // it scrolled: every row moved
            dirty_ = true;
            return true;
        }
        // Type-ahead. It is never a dialog and never a search box: typing moves the selection,
        // which is Tracker's behaviour and the one the HIG position in the framework doc argues
        // for. A printable character extends the buffer; the buffer resets after a pause or on
        // any navigation key above.
        if (k.utf8[0] && (unsigned char)k.utf8[0] >= 0x20) {
            const std::chrono::steady_clock::time_point now = std::chrono::steady_clock::now();
            if (std::chrono::duration<double>(now - last_type_).count() > 1.0) ahead_.clear();
            last_type_ = now;
            ahead_ += k.utf8;
            for (int i = 0; i < n; i++) {
                const int row = (cur + 1 + i) % n;   // search forward from the selection, wrapping
                const CellValue v = r_->Cell(row, 0);
                if (v.text.size() >= ahead_.size() &&
                    strncasecmp(v.text.c_str(), ahead_.c_str(), ahead_.size()) == 0) {
                    const int before_top = top_;
                    select(row);
                    Reveal(row);
                    if (top_ != before_top) whole_ = true;
                    dirty_ = true;
                    return true;
                }
            }
            return true;    // no match: swallow it rather than letting it do something else
        }
        return false;
    }

    // The rows whose appearance changed, rather than the whole list. A selection moving one row
    // repaints two rows; a scroll or a focus change repaints the list, and says so by returning
    // false. Reported in layout units, clipped to the list's own frame.
    bool DamageHint(std::vector<Rect>* out) override {
        if (whole_) { whole_ = false; rows_dirty_.clear(); return false; }
        if (rows_dirty_.empty()) return false;
        for (int row : rows_dirty_) {
            if (row < top_ || row >= top_ + visible()) continue;
            const float ry = frame.y + head_h_ + 1 + (row - top_) * row_h_;
            out->push_back(Rect{frame.x, ry, frame.w, row_h_});
        }
        rows_dirty_.clear();
        // The type-ahead badge sits top-right of the list and changes with the buffer.
        if (!ahead_.empty())
            out->push_back(Rect{frame.x + frame.w * 0.5f, frame.y + head_h_,
                                frame.w * 0.5f, row_h_ + 12});
        return !out->empty();
    }

    // A right-click targets the row under the pointer WITHOUT changing the selection, unless
    // nothing is selected -- acting on a row you did not mean to select is how files get lost.
    bool OnContext(float x, float y) override {
        (void)x;
        const int row = top_ + (int)((y - frame.y - head_h_ - 1) / row_h_);
        if (row < 0 || row >= r_->Count()) {
            // Worth tracing: a right-click below the last row is a legitimate miss, and it looks
            // identical to a broken handler from the outside. It cost a run to tell apart.
            if (getenv("AURORA_UI_TRACE"))
                fprintf(stderr, "ui: context row=%d of %d -- past the end, no menu\n", row,
                        r_->Count());
            return false;
        }
        ctx_row_ = row;
        if (sel_ && *sel_ < 0) select(row);
        if (on_ctx_) on_ctx_(row, x, y);
        return true;
    }
    void SetContextHandler(std::function<void(int, float, float)> f) { on_ctx_ = std::move(f); }
    int context_row() const { return ctx_row_; }

    bool take_dirty() { const bool d = dirty_; dirty_ = false; return d; }
    const std::string& ahead() const { return ahead_; }
    int rows_drawn() const { return rows_drawn_; }

private:
    Rows* r_;
    int* sel_;
    int top_ = 0, rows_drawn_ = 0;
    Scroller sb_;
    bool on_bar_ = false;
    std::set<int> multi_;
    bool single_ = false;
    int anchor_ = -1;
    std::function<void(const std::vector<int>&)> on_multi_;
    std::function<std::vector<std::string>(int)> drag_paths_;
    std::function<bool(int, const DragData&)> on_drop_;
    int drop_row_ = -1;
    bool drop_active_ = false, header_press_ = false;
    float row_h_ = 18, head_h_ = 20, click_y_ = 0;
    bool focused_ = false, dirty_ = false, whole_ = false;
    std::vector<int> rows_dirty_;
    std::function<void(int, float, float)> on_ctx_;
    std::function<void(int)> on_act_;
    std::function<bool(int, const std::string&)> on_ren_;
    std::function<void(int)> on_sel_;
    int ctx_row_ = -1, clicks_ = 1, edit_row_ = -1;
    State<std::string> edit_text_;
    Desc edit_field_;
    Window* win_ = nullptr;
    std::string ahead_;
    std::chrono::steady_clock::time_point last_type_{};
};

// ---- procedural icons -------------------------------------------------------------------------

namespace {

enum class FileKind : uint8_t { Dir, Code, Image, Archive, Doc, Exec, Plain };

// Classified from the EXTENSION, which is all we have: MIME here would mean sniffing content,
// and content sniffing is a read of a file the list has not been asked to open. Decision 0019's
// rule applies -- the cheap honest signal, and the UI must not imply more than it knows.
FileKind classify(const std::string& name, bool dir) {
    if (dir) return FileKind::Dir;
    const size_t d = name.find_last_of('.');
    if (d == std::string::npos || d + 1 >= name.size()) return FileKind::Plain;
    std::string e = name.substr(d + 1);
    for (char& c : e) c = (char)tolower((unsigned char)c);
    if (e == "c" || e == "h" || e == "cpp" || e == "hpp" || e == "rs" || e == "go" || e == "py" ||
        e == "js" || e == "ts" || e == "sh" || e == "toml" || e == "json" || e == "yaml")
        return FileKind::Code;
    if (e == "png" || e == "jpg" || e == "jpeg" || e == "gif" || e == "svg" || e == "webp" ||
        e == "bmp" || e == "ppm")
        return FileKind::Image;
    if (e == "zip" || e == "gz" || e == "xz" || e == "bz2" || e == "tar" || e == "zst" ||
        e == "7z" || e == "rpm" || e == "deb")
        return FileKind::Archive;
    if (e == "md" || e == "txt" || e == "pdf" || e == "doc" || e == "odt" || e == "html")
        return FileKind::Doc;
    if (e == "so" || e == "a" || e == "o" || e == "ko") return FileKind::Exec;
    return FileKind::Plain;
}

au_color kind_color(FileKind k) {
    switch (k) {
        case FileKind::Dir: return au_color{122, 162, 247, 255};      // blue
        case FileKind::Code: return au_color{158, 206, 106, 255};     // green
        case FileKind::Image: return au_color{187, 154, 247, 255};    // violet
        case FileKind::Archive: return au_color{224, 175, 104, 255};  // amber
        case FileKind::Doc: return au_color{125, 207, 255, 255};      // cyan
        case FileKind::Exec: return au_color{247, 118, 142, 255};     // red
        default: return au_color{130, 138, 158, 255};                 // grey
    }
}

// Rasterise once per (kind, device size) and blit thereafter.
//
// Drawing the geometry per tile measured 62% of the frame: 8.418 ms with icons against 3.248 ms
// without, at about a hundred tiles. Round-rect fills compute per-pixel coverage for their
// corners, so a 36x40 icon is ~1,400 pixels of coverage maths and a grid is 140,000. Cached, each
// kind is rasterised once and every tile after that is a copy.
//
// This is also the shape HVIF wants when it lands: a vector icon is decoded and rasterised once
// at the size in use, then cached. The widget does not change.
void draw_icon_geometry(au_surface* s, Rect r, FileKind k);

struct IconCache {
    struct Entry {
        au_surface* sf = nullptr;
        int w = 0, h = 0;
    };
    // Seven kinds; one size in practice, but keyed by size so a zoom does not reuse the wrong
    // rasterisation.
    std::vector<std::pair<std::pair<int, int>, Entry>> e;

    const Entry* get(FileKind k, int w, int h, double scale) {
        const std::pair<int, int> key{(int)k, w * 1000 + h};
        for (auto& it : e)
            if (it.first == key) return &it.second;
        Entry fresh;
        fresh.sf = au_surface_new(w, h, scale);
        if (!fresh.sf) return nullptr;
        au_clear(fresh.sf, au_color{0, 0, 0, 0});
        draw_icon_geometry(fresh.sf, Rect{0, 0, (float)w, (float)h}, k);
        fresh.w = au_surface_px_w(fresh.sf);
        fresh.h = au_surface_px_h(fresh.sf);
        e.push_back({key, fresh});
        return &e.back().second;
    }
};

IconCache& icons_cache() {
    static IconCache* c = new IconCache();
    return *c;
}

void draw_icon(au_surface* s, Rect r, FileKind k, double scale) {
    const IconCache::Entry* en = icons_cache().get(k, (int)r.w, (int)r.h, scale);
    if (!en || !en->sf) return;
    au_draw_image(s, au_surface_pixels(en->sf), en->w, en->h,
                  au_rect{r.x, r.y, r.x + r.w - 1, r.y + r.h - 1});
}

// A folder, or a page with a folded corner. Everything is a canvas primitive: no bitmap, no
// decoder, and it scales because it is geometry rather than pixels.
void draw_icon_geometry(au_surface* s, Rect r, FileKind k) {
    const au_color c = kind_color(k);
    const au_color dim{(uint8_t)(c.r * 2 / 3), (uint8_t)(c.g * 2 / 3), (uint8_t)(c.b * 2 / 3), 255};
    if (k == FileKind::Dir) {
        // tab, then body: drawn tab-first so the body's corner radius covers the seam
        au_fill_round_rect(s, au_rect{r.x, r.y + r.h * 0.12f, r.x + r.w * 0.45f, r.y + r.h * 0.35f},
                           3, 3, dim);
        au_fill_round_rect(s, au_rect{r.x, r.y + r.h * 0.26f, r.x + r.w - 1, r.y + r.h - 1}, 4, 4, c);
        return;
    }
    const float fold = r.w * 0.34f;
    au_fill_round_rect(s, au_rect{r.x + r.w * 0.12f, r.y, r.x + r.w * 0.88f, r.y + r.h - 1}, 4, 4,
                       c);
    // The folded corner, as a stack of shortening lines: a triangle without a polygon fill.
    for (int i = 0; i < (int)fold; i++) {
        const float y = r.y + 1 + i;
        au_stroke_line(s, r.x + r.w * 0.88f - fold + i, y, r.x + r.w * 0.88f - 1, y, dim, 1);
    }
}

}  // namespace

// ---- TextView ---------------------------------------------------------------------------------

class TextViewNode : public Node {
public:
    TextViewNode(State<std::string>* t, bool wrap) : t_(t), wrap_(wrap) {
        if (t_) t_->attach(this);
    }

    Size Measure(const Env& e, Constraints c) override {
        line_h_ = text_h(e) + 2;
        return Size{c.max_w >= 0 ? c.max_w : 300, c.max_h >= 0 ? c.max_h : 200};
    }

    void Draw(au_surface* s, const Env& e) override {
        au_fill_rect(s, frame.box(), au_color{22, 24, 32, 255});
        if (focused_) au_stroke_rect(s, frame.box(), e.accent, 1);
        rewrap(e);

        au_clip_set(s, frame.box());
        const int vis = std::max(1, (int)(frame.h / line_h_));
        const int first = std::min(top_, std::max(0, (int)lines_.size() - 1));
        const int last = std::min((int)lines_.size() - 1, first + vis);
        for (int i = first; i <= last; i++) {
            const float y = frame.y + 4 + (i - first) * line_h_ + line_h_ * 0.75f;
            au_text_draw(s, e.font, e.size, lines_[i].c_str(), -1, frame.x + e.unit * 0.75f, y,
                         e.fg);
        }
        // The same bar the list and the tree use, rather than the hint this drew before: a hint
        // tells you there is more, and a bar lets you go there.
        sb_.set(frame, (int)lines_.size(), vis, first);
        sb_.draw(s, e);
        au_clip_reset(s);
    }

    void OnPressAt(float x, float y, int clicks) override {
        (void)clicks;
        if (!sb_.hit(x, y)) return;
        top_ = sb_.press(y, (int)lines_.size(), visible(), top_);
    }
    void OnDragTo(float, float y) override {
        if (sb_.dragging) top_ = sb_.drag(y, (int)lines_.size(), visible(), top_);
    }
    void OnRelease(bool) override { sb_.dragging = false; }

    Node* HitTest(float x, float y) override { return frame.has(x, y) ? this : nullptr; }
    bool CanFocus() const override { return true; }
    void OnFocus(bool g) override { focused_ = g; }

    bool OnWheel(float dy) override {
        scroll(dy > 0 ? 3 : -3);
        return true;
    }
    bool OnKey(const au_key& k) override {
        if (!k.pressed) return false;
        const int vis = std::max(1, (int)(frame.h / line_h_));
        switch (k.raw_char) {
            case 0x1e: scroll(-1); return true;
            case 0x1f: scroll(1); return true;
            case 0x0b: scroll(-vis); return true;
            case 0x0c: scroll(vis); return true;
            case 0x01: top_ = 0; return true;
            case 0x04: top_ = std::max(0, (int)lines_.size() - vis); return true;
            default: return false;
        }
    }

    int line_count() const { return (int)lines_.size(); }

private:
    int visible() const { return std::max(1, (int)(frame.h / line_h_)); }

    void scroll(int by) {
        top_ = Scroller::clamp(top_ + by, (int)lines_.size(), visible());
    }

    // Split and wrap once per (text, width). A scroll must not re-measure the document, which is
    // what doing this in Draw unconditionally would cost: wrapping calls au_text_width per word.
    void rewrap(const Env& e) {
        const std::string& v = t_->get();
        if (v == cached_ && frame.w == cached_w_ && lines_valid_) return;
        cached_ = v;
        cached_w_ = frame.w;
        lines_valid_ = true;
        lines_.clear();
        top_ = 0;
        const float maxw = frame.w - e.unit * 2;
        size_t i = 0;
        while (i <= v.size()) {
            const size_t nl = v.find('\n', i);
            std::string line = v.substr(i, nl == std::string::npos ? std::string::npos : nl - i);
            if (!line.empty() && line.back() == '\r') line.pop_back();
            // Tabs as four spaces: a tab drawn as a glyph is a box, and as zero width it hides
            // the indentation that is most of what makes a text preview readable.
            std::string flat;
            for (char c : line) {
                if (c == '\t') flat += "    ";
                else if ((unsigned char)c < 0x20) flat += ' ';   // other control bytes
                else flat += c;
            }
            if (!wrap_ || au_text_width(e.font, e.size, flat.c_str(), -1) <= maxw) {
                lines_.push_back(flat);
            } else {
                // Wrap at spaces when there is one, mid-word when there is not: a path or a long
                // token must still be readable rather than vanishing off the edge.
                size_t start = 0;
                while (start < flat.size()) {
                    size_t lo = 1, hi = flat.size() - start, best = 1;
                    while (lo <= hi) {
                        const size_t mid = (lo + hi) / 2;
                        if (au_text_width(e.font, e.size, flat.substr(start, mid).c_str(),
                                          (int)mid) <= maxw) {
                            best = mid;
                            lo = mid + 1;
                        } else {
                            if (mid == 0) break;
                            hi = mid - 1;
                        }
                    }
                    size_t take = best;
                    if (start + take < flat.size()) {
                        const size_t sp = flat.rfind(' ', start + take);
                        if (sp != std::string::npos && sp > start) take = sp - start + 1;
                    }
                    lines_.push_back(flat.substr(start, take));
                    start += take;
                    if (lines_.size() > 20000) break;   // a cap, so a pathological line cannot hang
                }
            }
            if (nl == std::string::npos) break;
            i = nl + 1;
        }
    }

    State<std::string>* t_;
    bool wrap_;
    std::vector<std::string> lines_;
    std::string cached_;
    float cached_w_ = -1, line_h_ = 16;
    Scroller sb_;
    int top_ = 0;
    bool focused_ = false, lines_valid_ = false;
};

// ---- Breadcrumb -------------------------------------------------------------------------------

class CrumbNode : public Node {
public:
    CrumbNode(State<std::string>* p, std::function<void(const std::string&)> pick,
              std::function<void()> edit)
        : p_(p), pick_(std::move(pick)), edit_(std::move(edit)) {
        if (p_) p_->attach(this);
    }

    Size Measure(const Env& e, Constraints c) override {
        env_ = e;
        return Size{c.max_w >= 0 ? c.max_w : 400, text_h(e) + e.unit * 1.5f};
    }

    void Draw(au_surface* s, const Env& e) override {
        env_ = e;
        segs_.clear();
        split();
        au_clip_set(s, frame.box());
        float x = frame.x + e.unit * 0.5f;
        const float base = frame.y + frame.h * 0.72f;
        for (size_t i = 0; i < segs_.size(); i++) {
            const Seg& g = segs_[i];
            const float w = au_text_width(e.font, e.size, g.label.c_str(), -1);
            const bool last = i + 1 == segs_.size();
            if ((int)i == hover_)
                au_fill_round_rect(s, au_rect{x - 3, frame.y + 3, x + w + 2, frame.y + frame.h - 4},
                                   5, 5, e.surface_hi);
            // The last segment is where you are: bold, and not a link to itself.
            au_text_draw(s, last ? e.bold : e.font, e.size, g.label.c_str(), -1, x, base,
                         last ? e.fg : e.dim);
            segs_[i].x0 = x;
            segs_[i].x1 = x + w;
            x += w + e.unit * 0.5f;
            if (!last) {
                au_text_draw(s, e.font, e.size, "\xe2\x80\xba", -1, x, base, e.dim);  // >
                x += au_text_width(e.font, e.size, "\xe2\x80\xba", -1) + e.unit * 0.5f;
            }
        }
        end_x_ = x;
        au_clip_reset(s);
    }

    Node* HitTest(float x, float y) override { return frame.has(x, y) ? this : nullptr; }

    void OnPressAt(float x, float y, int clicks) override {
        (void)y;
        (void)clicks;
        const int i = seg_at(x);
        if (i < 0) {
            // Past the last segment: that empty strip is the affordance for editing the path.
            if (edit_) edit_();
            return;
        }
        if (i + 1 == (int)segs_.size()) return;      // already here
        if (pick_) pick_(segs_[i].path);
    }

    void OnEnter() override {}
    void OnLeave() override { hover_ = -1; }

    bool DamageHint(std::vector<Rect>* out) override {
        (void)out;
        return false;                                 // the whole strip; it is one line tall
    }

private:
    struct Seg {
        std::string label, path;
        float x0 = 0, x1 = 0;
    };

    void split() {
        const std::string& p = p_->get();
        segs_.push_back(Seg{"/", "/"});
        size_t i = 0;
        std::string acc = "";
        while (i < p.size()) {
            while (i < p.size() && p[i] == '/') i++;
            const size_t j = p.find('/', i);
            const std::string part = p.substr(i, j == std::string::npos ? std::string::npos : j - i);
            if (!part.empty()) {
                acc += "/" + part;
                segs_.push_back(Seg{part, acc});
            }
            if (j == std::string::npos) break;
            i = j;
        }
    }
    int seg_at(float x) const {
        for (size_t i = 0; i < segs_.size(); i++)
            if (x >= segs_[i].x0 - 3 && x <= segs_[i].x1 + 2) return (int)i;
        return -1;
    }

    State<std::string>* p_;
    std::function<void(const std::string&)> pick_;
    std::function<void()> edit_;
    std::vector<Seg> segs_;
    Env env_;
    float end_x_ = 0;
    int hover_ = -1;
};

// ---- IconView ---------------------------------------------------------------------------------

class IconsNode : public Node {
public:
    IconsNode(Rows* r, int* sel) : r_(r), sel_(sel) {}

    Size Measure(const Env& e, Constraints c) override {
        env_ = e;
        tile_w_ = 96;
        tile_h_ = 84;
        return Size{c.max_w >= 0 ? c.max_w : 400, c.max_h >= 0 ? c.max_h : 300};
    }

    int cols() const { return std::max(1, (int)(frame.w / tile_w_)); }
    int visible_rows() const { return std::max(1, (int)(frame.h / tile_h_)); }

    void Draw(au_surface* s, const Env& e) override {
        env_ = e;
        au_fill_rect(s, frame.box(), au_color{30, 32, 42, 255});
        if (focused_) au_stroke_rect(s, frame.box(), e.accent, 1);

        const int n = r_->Count();
        const int c = cols();
        const int first = top_row_ * c;
        const int last = std::min(n - 1, first + visible_rows() * c + c - 1);
        if (n > 0) r_->Want(std::max(0, first - c), std::min(n - 1, last + c * 2));

        au_clip_set(s, frame.box());
        for (int i = first; i <= last; i++) {
            const int col = (i - first) % c;
            const int row = (i - first) / c;
            const Rect t{frame.x + col * tile_w_, frame.y + row * tile_h_, tile_w_, tile_h_};
            if (t.y > frame.y + frame.h) break;
            const bool is_sel = is_selected(i);
            if (is_sel)
                au_fill_round_rect(s, au_rect{t.x + 3, t.y + 3, t.x + t.w - 4, t.y + t.h - 4}, 8, 8,
                                   e.accent);

            const CellValue v = r_->Cell(i, 0);
            std::string name = v.text;
            const bool dir = !name.empty() && name.back() == '/';
            if (dir) name.pop_back();
            // Attribution knobs. Two wrong guesses cost two runs each; measuring which half of
            // the tile is expensive costs one run.
            static const bool no_icons = getenv("AURORA_UI_NO_ICONS") != nullptr;
            static const bool no_labels = getenv("AURORA_UI_NO_LABELS") != nullptr;
            if (!no_icons)
                draw_icon(s, Rect{t.x + t.w / 2 - 18, t.y + 10, 36, 40}, classify(name, dir),
                          au_surface_scale(s));
            if (no_labels) continue;

            // One line, middle-elided: a tile is narrower than most names, and truncating the
            // tail hides the extension, which is the part that tells you what the thing is.
            const float maxw = t.w - 10;
            // Eliding by BINARY SEARCH over the kept length, not by shrinking one character at
            // a time. The first version did the latter and cost up to twenty au_text_width calls
            // per tile; at a hundred tiles that is two thousand shaping calls a frame, and it
            // measured p50 8.665 ms with four of five frames over the 8 ms budget. Binary search
            // is at most seven, and the result is identical.
            std::string shown = name;
            if (au_text_width(e.font, e.size - 1, name.c_str(), -1) > maxw && name.size() > 6) {
                size_t lo = 6, hi = name.size();
                std::string best = name.substr(0, 3) + "\xe2\x80\xa6";
                while (lo <= hi) {
                    const size_t keep = (lo + hi) / 2;
                    const size_t half = keep / 2;
                    const std::string cand = name.substr(0, half) + "\xe2\x80\xa6" +
                                             name.substr(name.size() - (keep - half));
                    if (au_text_width(e.font, e.size - 1, cand.c_str(), -1) <= maxw) {
                        best = cand;
                        lo = keep + 1;
                    } else {
                        if (keep == 0) break;
                        hi = keep - 1;
                    }
                }
                shown = best;
            }
            const float tw = au_text_width(e.font, e.size - 1, shown.c_str(), -1);
            au_text_draw(s, e.font, e.size - 1, shown.c_str(), -1, t.x + (t.w - tw) / 2,
                         t.y + t.h - 14, is_sel ? au_color{24, 26, 34, 255} : e.fg);
        }
        if (band_) {
            const au_rect b{std::min(bx0_, bx1_), std::min(by0_, by1_),
                            std::max(bx0_, bx1_), std::max(by0_, by1_)};
            au_stroke_rect(s, b, e.accent, 1);
        }
        // A unit here is a tile ROW, not an item: the thumb has to measure what a scroll moves.
        sb_.set(frame, grid_rows(), visible_rows(), top_row_);
        sb_.draw(s, e);
        au_clip_reset(s);
    }

    int grid_rows() const { return (r_->Count() + cols() - 1) / std::max(1, cols()); }

    Node* HitTest(float x, float y) override { return frame.has(x, y) ? this : nullptr; }
    bool CanFocus() const override { return true; }
    void OnFocus(bool g) override { focused_ = g; }

    int index_at(float x, float y) const {
        const int c = cols();
        const int col = (int)((x - frame.x) / tile_w_);
        const int row = (int)((y - frame.y) / tile_h_);
        if (col < 0 || col >= c || row < 0) return -1;
        const int i = (top_row_ + row) * c + col;
        return (i >= 0 && i < r_->Count()) ? i : -1;
    }

    void OnPressAt(float x, float y, int clicks) override {
        if (sb_.hit(x, y)) {
            top_row_ = sb_.press(y, grid_rows(), visible_rows(), top_row_);
            return;
        }
        const int i = index_at(x, y);
        const uint32_t m = mods_now();
        if (i < 0) {
            // Empty space clears, unless a modifier is held: that is someone mid-selection.
            // It also ARMS a rubber band, which is the gesture people reach for in a grid
            // before they reach for Ctrl-click.
            if (!(m & (AU_MOD_CTRL | AU_MOD_SHIFT))) set_only(-1);
            band_ = true;
            band_add_ = (m & AU_MOD_CTRL) != 0;
            band_base_ = band_add_ ? selection() : std::vector<int>();
            bx0_ = bx1_ = x;
            by0_ = by1_ = y;
            return;
        }
        if (m & AU_MOD_CTRL) {
            toggle_one(i);
            return;
        }
        if (m & AU_MOD_SHIFT) {
            extend_to(i);
            return;
        }
        select(i);
        if (clicks >= 2 && on_act_) on_act_(i);
    }
    bool OnContext(float x, float y) override {
        const int i = index_at(x, y);
        if (i < 0) return false;
        if (sel_ && *sel_ < 0) select(i);
        if (on_ctx_) on_ctx_(i, x, y);
        return true;
    }
    bool OnWheel(float dy) override {
        top_row_ = Scroller::clamp(top_row_ + (dy > 0 ? 2 : -2), grid_rows(), visible_rows());
        return true;
    }

    void OnDragTo(float x, float y) override {
        if (sb_.dragging) {
            top_row_ = sb_.drag(y, grid_rows(), visible_rows(), top_row_);
            return;
        }
        if (!band_) return;
        bx1_ = x;
        by1_ = y;
        // Rebuilt from the BASE each time rather than accumulated, so shrinking the band
        // deselects again. A band that only ever added would make an overshoot unfixable
        // without starting over.
        multi_.clear();
        for (int k : band_base_) multi_.insert(k);
        const float lo_x = std::min(bx0_, bx1_), hi_x = std::max(bx0_, bx1_);
        const float lo_y = std::min(by0_, by1_), hi_y = std::max(by0_, by1_);
        const int c = cols();
        const int n = r_->Count();
        for (int k = 0; k < n; k++) {
            const int row = k / c - top_row_, col = k % c;
            if (row < 0) continue;
            const float tx = frame.x + col * tile_w_, ty = frame.y + row * tile_h_;
            // Touched, not enclosed: a band has to select what it grazes, or picking up a row
            // of icons means carefully boxing every one.
            if (tx < hi_x && tx + tile_w_ > lo_x && ty < hi_y && ty + tile_h_ > lo_y) {
                multi_.insert(k);
            }
        }
        if (on_multi_) on_multi_(selection());
    }

    void OnRelease(bool) override {
        sb_.dragging = false;
        band_ = false;
    }

    bool band_active() const { return band_; }
    bool OnKey(const au_key& k) override {
        if (!k.pressed || !sel_) return false;
        const int n = r_->Count();
        if (n == 0) return false;
        // Before the switch: the switch's default returns, so anything checked after it is
        // never reached. Ctrl+A is a letter, and a letter is exactly what that default catches.
        // raw_char, not utf8: utf8 carries the modifiers, so Ctrl+A types 0x01 and never
        // compares equal to 'a'. raw_char is the layout's level-0 symbol, which is the letter.
        if ((k.mods & AU_MOD_CTRL) && (k.raw_char | 0x20) == 'a') {
            select_all();
            return true;
        }
        const int c = cols();
        const int cur = *sel_ < 0 ? -1 : *sel_;
        int next = cur;
        switch (k.raw_char) {
            case 0x1c: next = std::max(0, cur - 1); break;          // left
            case 0x1d: next = std::min(n - 1, cur + 1); break;      // right
            case 0x1e: next = cur < c ? cur : cur - c; break;       // up
            case 0x1f: next = std::min(n - 1, cur < 0 ? 0 : cur + c); break;  // down
            case 0x01: next = 0; break;
            case 0x04: next = n - 1; break;
            case 0x0a:
            case 0x0d:
                if (cur >= 0 && on_act_) { on_act_(cur); return true; }
                return false;
            default: return false;
        }
        if (next == cur) return true;
        if (k.mods & AU_MOD_SHIFT) extend_to(next);
        else select(next);
        // Keep it on screen, scrolling by whole tile rows.
        const int r = next / c;
        if (r < top_row_) top_row_ = r;
        else if (r >= top_row_ + visible_rows()) top_row_ = r - visible_rows() + 1;
        return true;
    }

    void SetActivateHandler(std::function<void(int)> f) { on_act_ = std::move(f); }
    void SetContextHandler(std::function<void(int, float, float)> f) { on_ctx_ = std::move(f); }
    void SetSelectHandler(std::function<void(int)> f) { on_sel_ = std::move(f); }
    void SetMultiHandler(std::function<void(const std::vector<int>&)> f) { on_multi_ = std::move(f); }
    void SetDragPaths(std::function<std::vector<std::string>(int)> f) { drag_paths_ = std::move(f); }

    // The same model the list uses, for the same reason: a grid you cannot rubber-band or
    // Ctrl-click is a grid you have to leave to do anything with more than one file. `*sel_`
    // stays the cursor; the set is what is highlighted.
    bool is_selected(int i) const { return multi_.count(i) != 0; }
    std::vector<int> selection() const { return std::vector<int>(multi_.begin(), multi_.end()); }

    void select(int i) {
        set_only(i);
        anchor_ = i;
    }

    void set_only(int i) {
        if (sel_ && *sel_ == i && multi_.size() == 1 && multi_.count(i)) return;
        multi_.clear();
        if (i >= 0) multi_.insert(i);
        if (sel_) *sel_ = i;
        if (on_sel_) on_sel_(i);
        if (on_multi_) on_multi_(selection());
    }

    void toggle_one(int i) {
        if (i < 0) return;
        if (multi_.count(i)) multi_.erase(i);
        else multi_.insert(i);
        if (sel_) *sel_ = i;
        anchor_ = i;
        if (on_sel_) on_sel_(i);
        if (on_multi_) on_multi_(selection());
    }

    void extend_to(int i) {
        if (i < 0) return;
        if (anchor_ < 0) anchor_ = i;
        multi_.clear();
        // A grid's range is by INDEX, not by rectangle: Shift-clicking the tile below picks up
        // the whole row between, which is what a reading order means and what every icon view
        // does. A rectangular block would be a different gesture (a rubber band).
        for (int k = std::min(anchor_, i); k <= std::max(anchor_, i) && k < r_->Count(); k++)
            if (k >= 0) multi_.insert(k);
        if (sel_) *sel_ = i;
        if (on_sel_) on_sel_(i);
        if (on_multi_) on_multi_(selection());
    }

    void select_all() {
        multi_.clear();
        for (int k = 0; k < r_->Count(); k++) multi_.insert(k);
        if (on_multi_) on_multi_(selection());
    }

    uint32_t mods_now() const { return win_ ? win_->mods() : 0u; }
    void Attached(Window* w) override { win_ = w; }

    bool OnDragStart(float x, float y, DragData* out) override {
        if (!drag_paths_) return false;
        const int i = index_at(x, y);
        if (i < 0) return false;
        std::vector<int> rows;
        if (is_selected(i) && multi_.size() > 1) rows = selection();
        else rows.push_back(i);
        std::vector<std::string> p;
        for (int r : rows) {
            const std::vector<std::string> one = drag_paths_(r);
            p.insert(p.end(), one.begin(), one.end());
        }
        if (p.empty()) return false;
        *out = DragData::uri_list(p);
        return true;
    }

private:
    Rows* r_;
    int* sel_;
    Env env_;
    std::function<void(int)> on_sel_;
    Scroller sb_;
    std::set<int> multi_;
    std::vector<int> band_base_;        // what was selected when the band started
    float bx0_ = 0, by0_ = 0, bx1_ = 0, by1_ = 0;
    bool band_ = false, band_add_ = false;
    Window* win_ = nullptr;
    int anchor_ = -1;
    std::function<void(const std::vector<int>&)> on_multi_;
    std::function<std::vector<std::string>(int)> drag_paths_;
    float tile_w_ = 96, tile_h_ = 84;
    int top_row_ = 0;
    bool focused_ = false;
    std::function<void(int)> on_act_;
    std::function<void(int, float, float)> on_ctx_;
};

// ---- TextField --------------------------------------------------------------------------------

// Caret and selection are BYTE offsets into a UTF-8 string, moved a codepoint at a time. Byte
// offsets keep insertion and deletion trivial; the two helpers below are what keep them from
// ever landing inside a multi-byte sequence, which would corrupt the text and draw mojibake.
size_t utf8_prev(const std::string& s, size_t i) {
    if (i == 0) return 0;
    do { i--; } while (i > 0 && ((unsigned char)s[i] & 0xC0) == 0x80);
    return i;
}
size_t utf8_next(const std::string& s, size_t i) {
    if (i >= s.size()) return s.size();
    do { i++; } while (i < s.size() && ((unsigned char)s[i] & 0xC0) == 0x80);
    return i;
}

class FieldNode : public Node {
public:
    FieldNode(State<std::string>* t, std::function<void(const std::string&)> ch,
              std::function<void(const std::string&)> co, std::function<void()> ca,
              FieldOpts o)
        : t_(t), ch_(std::move(ch)), co_(std::move(co)), ca_(std::move(ca)), o_(std::move(o)) {
        if (t_) t_->attach(this);     // the binding the design promises, made here
    }

    Size Measure(const Env& e, Constraints c) override {
        h_ = text_h(e) + e.unit * 1.5f;
        env_ = e;                      // kept for hit testing, which needs font metrics
        return Size{c.max_w >= 0 ? std::min(c.max_w, 260.0f) : 260, h_};
    }

    void Draw(au_surface* s, const Env& e) override {
        env_ = e;
        const std::string& v = t_->get();
        const float pad = e.unit;
        const float avail = frame.w - pad * 2;

        au_fill_round_rect(s, frame.box(), 6, 6,
                           o_.read_only ? au_color{28, 30, 39, 255} : au_color{22, 24, 32, 255});
        au_stroke_rect(s, frame.box(), focused_ ? e.accent : au_color{60, 64, 82, 255},
                       focused_ ? 2 : 1);

        // Keep the caret in view: scroll horizontally only as far as needed, and never past the
        // end of the text, so a field you have scrolled right does not show empty space when you
        // delete back. This is the overflow behaviour a single-line field has to have.
        const float caret_x = au_text_width(e.font, e.size, v.c_str(), (int)caret_);
        const float full_w = au_text_width(e.font, e.size, v.c_str(), -1);
        if (caret_x - scroll_ > avail - 2) scroll_ = caret_x - avail + 2;
        if (caret_x - scroll_ < 0) scroll_ = caret_x;
        if (full_w - scroll_ < avail && scroll_ > 0) scroll_ = std::max(0.0f, full_w - avail);
        if (full_w <= avail) scroll_ = 0;

        au_clip_set(s, au_rect{frame.x + 2, frame.y, frame.x + frame.w - 3, frame.y + frame.h - 1});
        const float tx = frame.x + pad - scroll_;
        const float base = baseline_in(e, frame);

        if (v.empty() && !focused_ && !o_.placeholder.empty()) {
            au_text_draw(s, e.font, e.size, o_.placeholder.c_str(), -1, frame.x + pad, base, e.dim);
        } else {
            if (sel_ != caret_) {
                const size_t a = std::min(sel_, caret_), b = std::max(sel_, caret_);
                const float x0 = tx + au_text_width(e.font, e.size, v.c_str(), (int)a);
                const float x1 = tx + au_text_width(e.font, e.size, v.c_str(), (int)b);
                // Selection keeps its own contrast: the accent at low alpha over a dark field
                // would leave the text unreadable, so it is a solid surface tone instead.
                au_fill_rect(s, au_rect{x0, frame.y + 3, x1 - 1, frame.y + frame.h - 4},
                             e.surface_hi);
            }
            au_text_draw(s, e.font, e.size, v.c_str(), -1, tx, base, e.fg);
            if (focused_ && caret_on_ && !o_.read_only) {
                const float cx = tx + caret_x;
                au_fill_rect(s, au_rect{cx, frame.y + 3, cx + 1, frame.y + frame.h - 4}, e.accent);
            }
        }
        // Overflow is shown, not hidden: a fade at whichever edge has more text off-screen.
        if (scroll_ > 0)
            au_fill_rect_gradient(s, au_rect{frame.x + 2, frame.y + 2, frame.x + 14,
                                             frame.y + frame.h - 3},
                                  au_color{22, 24, 32, 255}, au_color{22, 24, 32, 0});
        if (full_w - scroll_ > avail)
            au_fill_rect_gradient(s, au_rect{frame.x + frame.w - 15, frame.y + 2,
                                             frame.x + frame.w - 3, frame.y + frame.h - 3},
                                  au_color{22, 24, 32, 0}, au_color{22, 24, 32, 255});
        au_clip_reset(s);
    }

    void Attached(Window* w) override { win_ = w; }

    bool CanFocus() const override { return !o_.read_only; }
    void OnFocus(bool g) override {
        focused_ = g;
        caret_on_ = true;
        if (g) {
            if (o_.select_all_on_focus) { sel_ = 0; caret_ = t_->get().size(); }
            else { caret_ = t_->get().size(); sel_ = caret_; }
        }
    }

    // A caret that does not blink reads as a frozen program. 530 ms is the long-standing
    // platform interval; the window only wakes for it while this field has focus.
    int TickMs() const override { return focused_ && !o_.read_only ? 530 : 0; }
    void OnTick() override { caret_on_ = !caret_on_; }

    // ---- pointer: click to place, drag to select, double-click a word, triple-click all ----

    void OnPressAt(float x, float y, int clicks) override {
        (void)y;
        const std::string& v = t_->get();
        const size_t at = offset_at(x);
        if (clicks >= 3) { sel_ = 0; caret_ = v.size(); }
        else if (clicks == 2) { word_at(at, &sel_, &caret_); }
        else { caret_ = at; sel_ = at; }
        caret_on_ = true;
    }
    void OnDragTo(float x, float y) override {
        (void)y;
        caret_ = offset_at(x);     // sel_ stays: dragging extends from where the press landed
        caret_on_ = true;
    }

    bool OnKey(const au_key& k) override {
        if (!k.pressed) return false;
        std::string v = t_->get();
        const bool shift = (k.mods & AU_MOD_SHIFT) != 0;
        const bool ctrl = (k.mods & AU_MOD_CTRL) != 0;
        const uint32_t rc = k.raw_char;
        caret_on_ = true;

        if (rc == 0x1b) { if (ca_) ca_(); return true; }                 // Escape
        if (rc == 0x0a || rc == 0x0d) { if (co_) co_(v); return true; }  // Enter

        // Ctrl+A, C, X and V.
        //
        // The letter comes from EVDEV, not from utf8: with ctrl held many layouts deliver a
        // control character or nothing at all in utf8, so testing that would make the shortcuts
        // work on some keyboards and not others.
        if (ctrl) {
            switch (k.evdev) {
                case 30:                          // KEY_A: select all
                    sel_ = 0;
                    caret_ = v.size();
                    return true;
                case 46:                          // KEY_C: copy
                case 45: {                        // KEY_X: cut
                    if (sel_ == caret_) return true;
                    const size_t a = std::min(sel_, caret_), b = std::max(sel_, caret_);
                    if (win_) win_->RequestCopy(v.substr(a, b - a));
                    if (k.evdev == 45 && !o_.read_only) {
                        v.erase(a, b - a);
                        caret_ = a;
                        commit(v);
                    }
                    return true;
                }
                case 47:                          // KEY_V: paste
                    if (!o_.read_only && win_) win_->RequestPaste(this);
                    return true;
                default: break;
            }
        }

        if (o_.read_only) return rc == 0x1c || rc == 0x1d;   // navigable, not editable

        if (rc == 0x08) {                                                 // Backspace
            if (!erase_selection(&v)) {
                if (caret_ == 0) return true;
                const size_t p = ctrl ? word_start(v, caret_) : utf8_prev(v, caret_);
                v.erase(p, caret_ - p);
                caret_ = p;
            }
            commit(v);
            return true;
        }
        if (rc == 0x7f) {                                                 // Delete
            if (!erase_selection(&v)) {
                if (caret_ >= v.size()) return true;
                const size_t e2 = ctrl ? word_end(v, caret_) : utf8_next(v, caret_);
                v.erase(caret_, e2 - caret_);
            }
            commit(v);
            return true;
        }
        if (rc == 0x1c) { move(ctrl ? word_start(v, caret_) : utf8_prev(v, caret_), shift); return true; }
        if (rc == 0x1d) { move(ctrl ? word_end(v, caret_) : utf8_next(v, caret_), shift); return true; }
        if (rc == 0x01) { move(0, shift); return true; }                  // Home
        if (rc == 0x04) { move(v.size(), shift); return true; }           // End

        if (k.utf8[0] && (unsigned char)k.utf8[0] >= 0x20 && !ctrl) {
            if (o_.max_len && v.size() >= o_.max_len && sel_ == caret_) return true;
            erase_selection(&v);
            v.insert(caret_, k.utf8);
            caret_ += strlen(k.utf8);
            commit(v);
            return true;
        }
        return false;
    }

    void InsertText(const std::string& raw) override {
        if (o_.read_only) return;
        std::string in = raw;
        // A single-line field takes one line. A pasted newline would otherwise be an invisible
        // character in a filename, so everything after the first break is dropped rather than
        // flattened silently into the name.
        const size_t nl = in.find_first_of("\r\n");
        if (nl != std::string::npos) in.resize(nl);
        if (in.empty()) return;
        std::string v = t_->get();
        erase_selection(&v);
        if (o_.max_len && v.size() + in.size() > o_.max_len)
            in.resize(o_.max_len - std::min(o_.max_len, v.size()));
        if (caret_ > v.size()) caret_ = v.size();
        v.insert(caret_, in);
        caret_ += in.size();
        commit(v);
    }

private:
    void commit(const std::string& v) {
        sel_ = caret_;
        t_->set(v);
        if (ch_) ch_(v);
    }
    void move(size_t to, bool extend) {
        caret_ = to;
        if (!extend) sel_ = caret_;
    }
    bool erase_selection(std::string* v) {
        if (sel_ == caret_) return false;
        const size_t a = std::min(sel_, caret_), b = std::max(sel_, caret_);
        v->erase(a, b - a);
        caret_ = a;
        sel_ = a;
        return true;
    }

    // The byte offset whose glyph boundary is nearest x. Nearest, not "the one containing x", so
    // clicking the right half of a character puts the caret after it, as every editor does.
    size_t offset_at(float x) const {
        const std::string& v = t_->get();
        const float tx = frame.x + env_.unit - scroll_;
        size_t best = 0;
        float bestd = 1e9f;
        for (size_t i = 0; i <= v.size(); i = (i < v.size() ? utf8_next(v, i) : v.size() + 1)) {
            const float gx = tx + au_text_width(env_.font, env_.size, v.c_str(), (int)i);
            const float d = std::fabs(gx - x);
            if (d < bestd) { bestd = d; best = i; }
            if (i >= v.size()) break;
        }
        return best;
    }
    static bool wordch(unsigned char c) {
        return isalnum(c) || c == '_' || c == '.' || c == '-' || c >= 0x80;
    }
    static size_t word_start(const std::string& v, size_t i) {
        while (i > 0 && !wordch((unsigned char)v[i - 1])) i--;
        while (i > 0 && wordch((unsigned char)v[i - 1])) i--;
        return i;
    }
    static size_t word_end(const std::string& v, size_t i) {
        while (i < v.size() && !wordch((unsigned char)v[i])) i++;
        while (i < v.size() && wordch((unsigned char)v[i])) i++;
        return i;
    }
    void word_at(size_t at, size_t* a, size_t* b) const {
        const std::string& v = t_->get();
        size_t s2 = at, e2 = at;
        while (s2 > 0 && wordch((unsigned char)v[s2 - 1])) s2--;
        while (e2 < v.size() && wordch((unsigned char)v[e2])) e2++;
        *a = s2;
        *b = e2;
    }

    State<std::string>* t_;
    std::function<void(const std::string&)> ch_, co_;
    std::function<void()> ca_;
    FieldOpts o_;
    Env env_;
    size_t caret_ = 0, sel_ = 0;
    float h_ = 24, scroll_ = 0;
    bool focused_ = false, caret_on_ = true;
    Window* win_ = nullptr;
};

// ---- Sized ------------------------------------------------------------------------------------

// A fixed size on one axis or both.
//
// Its absence was a real bug and not a missing convenience. A child that is not weighted gets
// its NATURAL size from a stack, and several widgets answer "as much as you will give me" --
// TextView returns the constraint it was handed, because a wrapped document has no width of its
// own. Put one of those in a row beside a weighted sibling and it takes everything, and the
// weighted sibling is placed at zero: the preview pane opened over the file list rather than
// beside it, and the list was still drawn only because it did not clip to its own frame.
//
// So a pane that should be 260 units wide has to be able to SAY 260. Every toolkit in this
// family has this (SwiftUI's .frame(width:), Flutter's SizedBox); we had left it out.
class SizedNode : public Node {
public:
    SizedNode(Desc c, float w, float h) : c_(std::move(c)), w_(w), h_(h) {}

    Size Measure(const Env& e, Constraints c) override {
        Constraints cc = c;
        // The child is measured against the fixed size, not the parent's offer, so a wrapping
        // child wraps to the width it will be drawn at.
        if (w_ >= 0) cc.max_w = w_;
        if (h_ >= 0) cc.max_h = h_;
        const Size s = c_->Measure(e, cc);
        return Size{w_ >= 0 ? w_ : s.w, h_ >= 0 ? h_ : s.h};
    }
    void Place(Rect r) override {
        frame = r;
        c_->parent = this;
        c_->Place(r);
    }
    void Draw(au_surface* s, const Env& e) override { c_->Draw(s, e); }
    Node* HitTest(float x, float y) override { return c_->HitTest(x, y); }
    void Children(std::vector<Node*>* out) override { out->push_back(c_.get()); }

private:
    Desc c_;
    float w_, h_;
};

// ---- Either -----------------------------------------------------------------------------------

class EitherNode : public Node {
public:
    EitherNode(State<bool>* f, Desc a, Desc b) : f_(f), a_(std::move(a)), b_(std::move(b)) {
        if (f_) f_->attach(this);
    }
    Node* live() const { return (f_ && f_->get()) ? a_.get() : b_.get(); }

    Size Measure(const Env& e, Constraints c) override {
        applied_ = pick();
        return live()->Measure(e, c);
    }
    void Place(Rect r) override {
        frame = r;
        live()->parent = this;
        live()->Place(r);
    }
    void Draw(au_surface* s, const Env& e) override { live()->Draw(s, e); }
    Node* HitTest(float x, float y) override { return live()->HitTest(x, y); }
    void Children(std::vector<Node*>* out) override { out->push_back(live()); }

    // A switch needs a LAYOUT, not just a repaint, and saying so is this node's whole job beyond
    // picking. The child that was not live has never been measured or placed -- that is the
    // point of Either, and it is stated in the header -- so on the switch it still holds the
    // frame it was born with, which is nothing. It then drew nothing: an icon view that was
    // reached by pressing the button, rather than by starting in it, was a blank pane.
    //
    // Animating() is the existing way to tell the window "my layout is stale", the same way the
    // splitter uses it. `applied_` starts at -1 so the first layout settles it.
    bool Animating() const override { return (int)pick() != applied_; }

private:
    bool pick() const { return f_ && f_->get(); }

    State<bool>* f_;
    Desc a_, b_;
    int applied_ = -1;
};

// ---- Reveal -----------------------------------------------------------------------------------

class RevealNode : public Node {
public:
    RevealNode(State<bool>* shown, Desc child, Transition t)
        : shown_(shown), c_(std::move(child)), t_(t) {
        if (shown_) shown_->attach(this);
        e_.target(shown_ && shown_->get() ? 1.0f : 0.0f, 0);   // start at the end state
        e_.snap();
    }

    Size Measure(const Env& env, Constraints c) override {
        nat_ = c_->Measure(env, c);          // the child's NATURAL size, measured once
        want_ = (shown_ && shown_->get()) ? 1.0f : 0.0f;
        if (t_.motion == Motion::None) e_.snap();
        const float f = e_.value();
        applied_ = f;
        return t_.motion == Motion::RevealX ? Size{nat_.w * f, nat_.h} : Size{nat_.w, nat_.h * f};
    }

    void Place(Rect r) override {
        frame = r;
        c_->parent = this;
        // The child keeps its natural size on the revealed axis and is clipped, so revealing
        // never reflows its contents. A child re-laid-out every frame of the animation would be
        // the expensive, janky version of this.
        c_->Place(t_.motion == Motion::RevealX ? Rect{r.x, r.y, nat_.w, r.h}
                                               : Rect{r.x, r.y, r.w, nat_.h});
    }

    void Draw(au_surface* s, const Env& env) override {
        if (e_.value() <= 0.001f) return;
        au_clip_set(s, frame.box());
        c_->Draw(s, env);
        au_clip_reset(s);
    }

    // Hit testing follows the STATE, not the animation: the target state is live immediately.
    Node* HitTest(float x, float y) override {
        if (!shown_ || !shown_->get()) return nullptr;
        return c_->HitTest(x, y);
    }
    void Children(std::vector<Node*>* out) override {
        if (shown_ && shown_->get()) out->push_back(c_.get());
    }

    // While moving, ask for a frame at display rate. When settled, stop asking: an idle window
    // must go back to sleeping.
    int TickMs() const override { return e_.running() ? 16 : 0; }
    void OnTick() override { e_.tick(); }

    void Retarget(bool allowed) override {
        want_ = (shown_ && shown_->get()) ? 1.0f : 0.0f;
        if (!allowed || t_.motion == Motion::None) { e_.target(want_, 0); e_.snap(); return; }
        e_.target(want_, t_.ms);
    }
    // Running is not the only way to be stale. When motion is NOT allowed -- Motion::None, or
    // the budget gate degrading under frame pressure -- Retarget SNAPS and nothing is running,
    // so a check of e_.running() alone said "settled" while the layout still held the old
    // fraction. The pane then never appeared at all.
    //
    // That is the worst possible shape for a degradation path: it failed precisely when the
    // system was already struggling, and it failed by disappearing rather than by being abrupt.
    // Comparing against the fraction the last layout actually used covers both cases, and it is
    // the same thing Split and Either answer.
    bool Animating() const override { return e_.running() || e_.value() != applied_; }

private:
    State<bool>* shown_;
    Desc c_;
    Transition t_;
    Ease e_;
    float applied_ = -1;
    Size nat_{};
    float want_ = 0;
};

}  // namespace

// ---- Tree -------------------------------------------------------------------------------------

class TreeNode : public Node {
public:
    TreeNode(TreeSource* src, State<std::string>* sel) : src_(src), sel_(sel) {
        if (sel_) sel_->attach(this);
    }

    Size Measure(const Env& e, Constraints c) override {
        row_h_ = text_h(e) + e.unit * 0.5f;
        unit_ = e.unit;
        return Size{c.max_w >= 0 ? c.max_w : 200, c.max_h >= 0 ? c.max_h : 300};
    }

    void Draw(au_surface* s, const Env& e) override {
        unit_ = e.unit;
        au_fill_rect(s, frame.box(), au_color{26, 28, 37, 255});
        if (focused_) au_stroke_rect(s, frame.box(), e.accent, 1);
        // Rebuilt here and not in Measure: the flattening asks the source for children, and the
        // tree's own size does not depend on the answer. Rebuilding in Measure would ask on
        // every layout pass instead of only when something actually opened or closed.
        ensure();

        au_clip_set(s, frame.box());
        const int vis = visible();
        const int first = std::max(0, std::min(top_, std::max(0, (int)flat_.size() - 1)));
        const int last = std::min((int)flat_.size() - 1, first + vis);
        for (int i = first; i <= last; i++) {
            const Row& r = flat_[i];
            const float ry = frame.y + (i - first) * row_h_;
            const bool is_sel = sel_ && sel_->get() == r.id;
            if (is_sel)
                au_fill_rect(s, au_rect{frame.x, ry, frame.x + frame.w - 1, ry + row_h_ - 1},
                             e.accent);
            else if (i == cur_ && focused_)
                // The keyboard cursor, distinct from the selection. They are usually the same
                // row; they come apart when the app sets the selection from elsewhere, and a
                // tree that showed only one of them would lose the user's place.
                au_stroke_rect(s, au_rect{frame.x + 1, ry, frame.x + frame.w - 2, ry + row_h_ - 1},
                               e.surface_hi, 1);

            const float ind = frame.x + e.unit * 0.5f + r.depth * e.unit * 1.5f;
            const au_color ink = is_sel ? au_color{24, 26, 34, 255} : e.fg;
            if (r.expandable) chevron(s, ind + e.unit * 0.5f, ry + row_h_ * 0.5f,
                                     open_.count(r.id) != 0, is_sel ? ink : e.dim);
            const float tx = ind + e.unit * 1.5f;
            au_clip_set(s, au_rect{tx, ry, frame.x + frame.w - 2, ry + row_h_ - 1});
            au_text_draw(s, is_sel ? e.bold : e.font, e.size, r.label.c_str(), -1, tx,
                         ry + row_h_ * 0.74f, ink);
            au_clip_reset(s);
            // A node whose children are not here yet says so, rather than looking like a leaf.
            if (r.expandable && open_.count(r.id) && pending_.count(r.id))
                au_text_draw(s, e.font, e.size - 2, "...", -1, frame.x + frame.w - e.unit * 2,
                             ry + row_h_ * 0.74f, e.dim);
        }
        if (drop_row_ >= first && drop_row_ <= last) {
            const float ry = frame.y + (drop_row_ - first) * row_h_;
            au_stroke_rect(s, au_rect{frame.x + 1, ry, frame.x + frame.w - 2, ry + row_h_ - 1},
                           e.accent, 2);
        }
        sb_.set(frame, (int)flat_.size(), vis, first);
        sb_.draw(s, e);
        au_clip_reset(s);
    }

    Node* HitTest(float x, float y) override { return frame.has(x, y) ? this : nullptr; }
    bool CanFocus() const override { return true; }
    void OnFocus(bool g) override { focused_ = g; }

    bool OnWheel(float dy) override {
        const int before = top_;
        top_ = Scroller::clamp(top_ + (dy > 0 ? 3 : -3), (int)flat_.size(), visible());
        if (top_ != before) whole_ = true;
        return top_ != before;
    }

    void OnDragTo(float, float y) override {
        if (!sb_.dragging) return;
        const int to = sb_.drag(y, (int)flat_.size(), visible(), top_);
        if (to != top_) { top_ = to; whole_ = true; }
    }
    void OnRelease(bool) override { sb_.dragging = false; }

    void OnPressAt(float x, float y, int clicks) override {
        ensure();
        if (sb_.hit(x, y)) {
            const int to = sb_.press(y, (int)flat_.size(), visible(), top_);
            if (to != top_) { top_ = to; whole_ = true; }
            return;
        }
        const int i = top_ + (int)((y - frame.y) / row_h_);
        if (i < 0 || i >= (int)flat_.size()) return;
        cur_ = i;
        const Row& r = flat_[i];
        // Hitting the twisty toggles without selecting. Clicking the row selects. These are
        // different intents -- "show me what is in there" is not "go there" -- and a tree that
        // conflates them navigates every time someone peeks.
        const float tw = frame.x + unit_ * 0.5f + r.depth * unit_ * 1.5f + unit_ * 1.5f;
        if (r.expandable && x < tw) {
            toggle(i);
            return;
        }
        select(i);
        if (clicks >= 2 && on_act_) on_act_(r.id);
    }

    bool OnKey(const au_key& k) override {
        if (!k.pressed) return false;
        ensure();
        const int n = (int)flat_.size();
        if (n == 0) return false;
        // Nothing selected yet is -1, and each key decides what that means for itself rather
        // than being clamped to 0 up front: clamping first made the FIRST Down land on row one,
        // because it then incremented a cursor that had already been moved to row zero.
        const int cur = cur_;
        const int from = cur < 0 ? 0 : cur;
        switch (k.raw_char) {
            case 0x1e: move(cur <= 0 ? 0 : cur - 1); return true;                  // up
            case 0x1f: move(cur < 0 ? 0 : std::min(n - 1, cur + 1)); return true;  // down
            case 0x0b: move(std::max(0, from - visible())); return true;           // page up
            case 0x0c: move(std::min(n - 1, from + visible())); return true;       // page down
            case 0x01: move(0); return true;                                       // home
            case 0x04: move(n - 1); return true;                                   // end
            case 0x1d: {                                                           // right
                if (cur < 0) { move(0); return true; }
                const Row& r = flat_[cur];
                if (r.expandable && !open_.count(r.id)) { toggle(cur); return true; }
                if (cur + 1 < n && flat_[cur + 1].depth > r.depth) { move(cur + 1); return true; }
                // Nothing to open and nowhere further in: DECLINE, so the window can move focus
                // out of the sidebar and into the pane beside it. Swallowing it here is what
                // makes a tree a place you can get into and not out of.
                return false;
            }
            case 0x1c: {                                                           // left
                if (cur < 0) { move(0); return true; }
                const Row& r = flat_[cur];
                if (r.expandable && open_.count(r.id)) { toggle(cur); return true; }
                // Climbing to the parent is the part people use. Walking BACK through the flat
                // rows finds it without asking the source, which is right: the flattening
                // already knows the shape, and TreeSource::Parent is only for a reveal from
                // outside, where there is no row to walk back from.
                for (int i = cur - 1; i >= 0; i--)
                    if (flat_[i].depth < r.depth) { move(i); return true; }
                return false;        // at the root with nothing to collapse: let focus leave
            }
            case 0x0a:
            case 0x0d:
                if (cur >= 0 && on_act_) on_act_(flat_[cur].id);
                return true;
            default: break;
        }
        return false;
    }

    bool DamageHint(std::vector<Rect>* out) override {
        if (whole_) { whole_ = false; return false; }
        if (dirty_rows_.empty()) return false;
        for (int row : dirty_rows_) {
            if (row < top_ || row >= top_ + visible()) continue;
            out->push_back(Rect{frame.x, frame.y + (row - top_) * row_h_, frame.w, row_h_});
        }
        dirty_rows_.clear();
        return true;
    }

    void SetSelectHandler(std::function<void(const std::string&)> f) { on_sel_ = std::move(f); }
    void SetActivateHandler(std::function<void(const std::string&)> f) { on_act_ = std::move(f); }
    void SetDropHandler(std::function<bool(const std::string&, const DragData&)> f) {
        on_drop_ = std::move(f);
    }

    // The sidebar is the other half of a file manager's drag: dragging into a folder you can
    // see but are not inside is most of what dragging is FOR.
    bool CanAcceptDrop(const DragData& d) const override {
        return on_drop_ && d.mime == "text/uri-list" && !d.paths.empty();
    }

    void OnDropHover(float x, float y, bool inside) override {
        (void)x;
        const int r = inside ? row_at(y) : -1;
        if (r == drop_row_) return;
        drop_row_ = r;
        whole_ = true;
    }

    bool OnDrop(float x, float y, const DragData& d) override {
        (void)x;
        const int r = row_at(y);
        drop_row_ = -1;
        whole_ = true;
        if (r < 0 || r >= (int)flat_.size() || !on_drop_) return false;
        return on_drop_(flat_[r].id, d);
    }

    int row_at(float y) const {
        const int i = top_ + (int)((y - frame.y) / row_h_);
        return (i >= 0 && i < (int)flat_.size()) ? i : -1;
    }

    // Expand down to `id` and select it. The chain is collected from the source's Parent and
    // opened from the top, because opening a node is what makes its children askable at all.
    void RevealId(const std::string& id) {
        if (id.empty()) return;
        std::vector<std::string> chain;
        for (std::string p = src_->Parent(id); !p.empty() && chain.size() < 64;
             p = src_->Parent(p))
            chain.push_back(p);
        for (size_t i = chain.size(); i-- > 0;) open_.insert(chain[i]);
        flat_valid_ = false;
        whole_ = true;
        rebuild();
        if (sel_) sel_->set(id);
        for (size_t i = 0; i < flat_.size(); i++)
            if (flat_[i].id == id) { cur_ = (int)i; show(cur_); break; }
    }

    // These flatten first if the flattening is stale. Reading "what is showing" must not depend
    // on a Draw having happened: an accessibility consumer asking before the first frame, or
    // between an expand and the next frame, would otherwise get the previous answer.
    int row_count() {
        ensure();
        return (int)flat_.size();
    }
    int cursor() const { return cur_; }
    std::string row_id(int i) {
        ensure();
        return i >= 0 && i < (int)flat_.size() ? flat_[i].id : std::string();
    }
    int row_depth(int i) {
        ensure();
        return i >= 0 && i < (int)flat_.size() ? flat_[i].depth : -1;
    }

private:
    struct Row {
        std::string id, label;
        int depth = 0;
        bool expandable = false;
    };

    int visible() const { return std::max(1, (int)(frame.h / row_h_)); }

    void ensure() {
        if (!flat_valid_) rebuild();
    }

    void move(int to) {
        if (to == cur_ || to < 0 || to >= (int)flat_.size()) return;
        dirty_rows_.push_back(cur_);
        dirty_rows_.push_back(to);
        cur_ = to;
        show(to);
        select(to);
    }

    void select(int i) {
        dirty_rows_.push_back(i);
        cur_ = i;
        const std::string& id = flat_[i].id;
        if (sel_ && sel_->get() == id) return;
        if (sel_) sel_->set(id);
        if (on_sel_) on_sel_(id);
    }

    void show(int i) {
        const int before = top_;
        if (i < top_) top_ = i;
        else if (i >= top_ + visible()) top_ = i - visible() + 1;
        if (top_ != before) whole_ = true;      // it scrolled: every row moved
    }

    void toggle(int i) {
        const std::string id = flat_[i].id;
        if (open_.count(id)) open_.erase(id);
        else open_.insert(id);
        flat_valid_ = false;
        whole_ = true;                          // rows below it all moved
        rebuild();
        // Keep the cursor on the row the user acted on, which after a collapse may have moved.
        for (size_t j = 0; j < flat_.size(); j++)
            if (flat_[j].id == id) { cur_ = (int)j; break; }
    }

    void chevron(au_surface* s, float cx, float cy, bool open, au_color c) {
        const float r = std::max(3.0f, unit_ * 0.33f);
        if (open) {   // pointing down
            au_stroke_line(s, cx - r, cy - r * 0.5f, cx, cy + r * 0.5f, c, 1.5f);
            au_stroke_line(s, cx, cy + r * 0.5f, cx + r, cy - r * 0.5f, c, 1.5f);
        } else {      // pointing right
            au_stroke_line(s, cx - r * 0.5f, cy - r, cx + r * 0.5f, cy, c, 1.5f);
            au_stroke_line(s, cx + r * 0.5f, cy, cx - r * 0.5f, cy + r, c, 1.5f);
        }
    }

    void rebuild() {
        flat_.clear();
        pending_.clear();
        flat_valid_ = true;
        walk(std::string(), 0);
        if (cur_ >= (int)flat_.size()) cur_ = (int)flat_.size() - 1;
    }

    void walk(const std::string& id, int depth) {
        // Two caps, and both are reachable by accident rather than by malice: a symlink loop
        // makes a path tree infinitely deep, and a directory of directories makes it wide. A
        // widget that hangs on either is worse than one that stops drawing.
        if (depth > 32 || flat_.size() > 20000) return;
        std::vector<TreeItem> kids;
        if (!src_->Children(id, &kids)) {
            if (!id.empty()) pending_.insert(id);
            return;
        }
        for (const TreeItem& k : kids) {
            flat_.push_back(Row{k.id, k.label, depth, k.expandable});
            if (k.expandable && open_.count(k.id)) walk(k.id, depth + 1);
        }
    }

    TreeSource* src_;
    State<std::string>* sel_;
    std::vector<Row> flat_;
    std::set<std::string> open_, pending_;
    std::vector<int> dirty_rows_;
    std::function<void(const std::string&)> on_sel_, on_act_;
    Scroller sb_;
    std::function<bool(const std::string&, const DragData&)> on_drop_;
    float row_h_ = 16, unit_ = 8;
    int top_ = 0, cur_ = -1, drop_row_ = -1;
    bool focused_ = false, flat_valid_ = false, whole_ = true;
};

// ---- Split ------------------------------------------------------------------------------------

// Two panes and a divider the user drags.
//
// Relayout is driven through Animating(), which is not a borrowed mechanism but exactly the one
// this needs: Animating means "my size is part of the layout and it changed, lay out again", and
// a dragged divider is that statement. The window already lays out before drawing when anything
// answers true, so the new ratio shows in the SAME frame as the drag, and setting the ratio from
// outside the widget works for free -- which is what makes `ratio` being the app's State honest
// rather than decorative.
class SplitNode : public Node {
public:
    SplitNode(Desc a, Desc b, State<float>* r, bool vert, SplitOpts o)
        : a_(std::move(a)), b_(std::move(b)), r_(r), vert_(vert), o_(o) {
        if (r_) r_->attach(this);
    }

    Size Measure(const Env& e, Constraints c) override {
        unit_ = e.unit;
        div_ = std::max(2.0f, o_.thickness * e.unit);
        const Constraints in = shrink(c);
        // A split divides the space it is GIVEN. With no constraint on the main axis there is
        // nothing to take a fraction of, so it falls back to a size rather than asking its
        // children and inventing a ratio from their natural sizes -- which would make the
        // divider jump the first time a pane's content changed.
        const float main = vert_ ? (in.max_h >= 0 ? in.max_h : 300) : (in.max_w >= 0 ? in.max_w : 400);
        const float cross = vert_ ? in.max_w : in.max_h;
        geom(main);
        // Each child measured once, at the main size it will actually be placed at: a pane that
        // wraps text must wrap to its own width, not to the split's.
        a_->Measure(e, vert_ ? Constraints{cross, ma_} : Constraints{ma_, cross});
        b_->Measure(e, vert_ ? Constraints{cross, mb_} : Constraints{mb_, cross});
        applied_ = raw();
        return vert_ ? Size{(cross >= 0 ? cross : 400) + pad.l + pad.r, main + pad.t + pad.b}
                     : Size{main + pad.l + pad.r, (cross >= 0 ? cross : 300) + pad.t + pad.b};
    }

    void Place(Rect r) override {
        frame = r;
        const Rect in = inner();
        geom(vert_ ? in.h : in.w);
        a_->parent = this;
        b_->parent = this;
        if (vert_) {
            a_->Place(Rect{in.x, in.y, in.w, ma_});
            dv_ = Rect{in.x, in.y + ma_, in.w, div_};
            b_->Place(Rect{in.x, in.y + ma_ + div_, in.w, mb_});
        } else {
            a_->Place(Rect{in.x, in.y, ma_, in.h});
            dv_ = Rect{in.x + ma_, in.y, div_, in.h};
            b_->Place(Rect{in.x + ma_ + div_, in.y, mb_, in.h});
        }
    }

    void Draw(au_surface* s, const Env& e) override {
        unit_ = e.unit;
        a_->Draw(s, e);
        b_->Draw(s, e);
        au_fill_rect(s, dv_.box(), (hot_ || drag_) ? e.accent : e.surface_hi);
        // The grip: three marks at the middle of the divider. There is no cursor change to
        // discover the gesture with, so the divider has to say what it is.
        const float cx = dv_.x + dv_.w * 0.5f, cy = dv_.y + dv_.h * 0.5f;
        const float step = std::max(3.0f, unit_ * 0.4f);
        for (int i = -1; i <= 1; i++) {
            const float gx = vert_ ? cx + i * step * 2 : cx;
            const float gy = vert_ ? cy : cy + i * step * 2;
            au_fill_round_rect(s, au_rect{gx - 1, gy - 1, gx + 1, gy + 1}, 1, 1,
                               (hot_ || drag_) ? e.fg : e.dim);
        }
    }

    Node* HitTest(float x, float y) override {
        if (!frame.has(x, y)) return nullptr;
        // The divider wins inside its grab zone, which is wider than it is drawn: a 4 px target
        // is a target people miss, and missing it hits a pane instead, which moves a selection.
        if (grab().has(x, y)) return this;
        if (Node* n = a_->HitTest(x, y)) return n;
        if (Node* n = b_->HitTest(x, y)) return n;
        return nullptr;
    }

    void Children(std::vector<Node*>* out) override {
        out->push_back(a_.get());
        out->push_back(b_.get());
    }

    void OnEnter() override { hot_ = true; }
    void OnLeave() override { hot_ = false; }

    void OnPressAt(float x, float y, int clicks) override {
        if (clicks >= 2) {
            // Collapse and restore. Remembering the old ratio is what makes this a toggle: a
            // collapse with no way back would be the one gesture on this widget that destroys
            // something, and it would be the easiest one to trigger by accident.
            drag_ = false;
            if (collapsed_) {
                set(restore_);
                collapsed_ = false;
            } else {
                restore_ = raw();
                set(0.0f);
                collapsed_ = true;
            }
            return;
        }
        drag_ = true;
        grab_at_ = vert_ ? y - dv_.y : x - dv_.x;
    }

    void OnDragTo(float x, float y) override {
        if (!drag_) return;
        const Rect in = inner();
        const float avail = std::max(1.0f, (vert_ ? in.h : in.w) - div_);
        const float at = (vert_ ? y - in.y : x - in.x) - grab_at_;
        set(std::max(0.0f, std::min(1.0f, at / avail)));
        collapsed_ = false;
    }

    void OnRelease(bool) override { drag_ = false; }

    // The ratio changed since the last layout, so the layout is stale. `applied_` holds the RAW
    // value seen at layout time, not the clamped one: storing the clamp would leave the two
    // permanently unequal whenever a min kicked in, and the window would lay out every frame
    // forever.
    bool Animating() const override { return raw() != applied_; }

    float divider_pos() const { return vert_ ? dv_.y : dv_.x; }   // for tests

private:
    float raw() const { return r_ ? r_->get() : 0.5f; }
    void set(float v) {
        if (r_) r_->set(v);
    }

    // The two main-axis sizes for a given total. One place decides this, so Measure and Place
    // cannot disagree about where the divider is -- which would put a pane's hit test and its
    // pixels in different places.
    void geom(float main) {
        const float avail = std::max(0.0f, main - div_);
        const float lo = std::min(o_.min_a * unit_, avail);
        const float hi = std::max(lo, avail - o_.min_b * unit_);
        ma_ = std::max(lo, std::min(raw() * avail, hi));
        mb_ = avail - ma_;
    }

    Rect grab() const {
        const float s = o_.grab_slop;
        return vert_ ? Rect{dv_.x, dv_.y - s, dv_.w, dv_.h + 2 * s}
                     : Rect{dv_.x - s, dv_.y, dv_.w + 2 * s, dv_.h};
    }

    Desc a_, b_;
    State<float>* r_;
    bool vert_;
    SplitOpts o_;
    Rect dv_;
    float unit_ = 8, div_ = 4, ma_ = 0, mb_ = 0, grab_at_ = 0;
    float applied_ = -1, restore_ = 0.5f;
    bool hot_ = false, drag_ = false, collapsed_ = false;
};

// ---- Ease -------------------------------------------------------------------------------------

void Ease::target(float t, uint32_t ms) {
    if (t == want_ && running_) return;      // already heading there
    from_ = cur_;                            // resume from where it got to, not from the start
    want_ = t;
    ms_ = ms;
    t0_ = std::chrono::steady_clock::now();
    running_ = ms > 0 && cur_ != want_;
    if (!running_) cur_ = want_;
}

bool Ease::tick() {
    if (!running_) return false;
    const double el =
        std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0_).count();
    float p = ms_ > 0 ? (float)(el / ms_) : 1.0f;
    if (p >= 1.0f) { cur_ = want_; running_ = false; return false; }
    // Cubic ease-out. No ease-in anywhere in this toolkit: for anything the user initiated it
    // reads as the program hesitating.
    const float k = 1.0f - (1.0f - p) * (1.0f - p) * (1.0f - p);
    cur_ = from_ + (want_ - from_) * k;
    return true;
}

// ---- damage -----------------------------------------------------------------------------------

void Damage::add(Rect r) {
    if (all_) return;
    if (r.w <= 0 || r.h <= 0) return;
    // Merge into an existing rect when one already covers it: a hover that invalidates the same
    // button twice in a frame should not add two entries.
    for (const Rect& e : r_)
        if (r.x >= e.x && r.y >= e.y && r.x + r.w <= e.x + e.w && r.y + r.h <= e.y + e.h) return;
    if (r_.size() >= kMax) { all(); return; }
    r_.push_back(r);
}

Rect Damage::bounds() const {
    if (r_.empty()) return Rect{};
    float x0 = r_[0].x, y0 = r_[0].y, x1 = r_[0].x + r_[0].w, y1 = r_[0].y + r_[0].h;
    for (const Rect& e : r_) {
        x0 = std::min(x0, e.x);
        y0 = std::min(y0, e.y);
        x1 = std::max(x1, e.x + e.w);
        y1 = std::max(y1, e.y + e.h);
    }
    return Rect{x0, y0, x1 - x0, y1 - y0};
}

// ---- drag payloads ----------------------------------------------------------------------------

DragData DragData::uri_list(const std::vector<std::string>& files) {
    DragData d;
    d.mime = "text/uri-list";
    d.paths = files;
    // CRLF, which RFC 2483 asks for and which every other toolkit sends.
    for (const std::string& f : files) d.bytes += "file://" + uri_escape(f) + "\r\n";
    return d;
}

DragData DragData::parse_uri_list(const std::string& bytes) {
    DragData d;
    d.mime = "text/uri-list";
    d.bytes = bytes;
    size_t i = 0;
    while (i < bytes.size()) {
        size_t j = bytes.find('\n', i);
        std::string line = bytes.substr(i, j == std::string::npos ? std::string::npos : j - i);
        i = j == std::string::npos ? bytes.size() : j + 1;
        if (!line.empty() && line.back() == '\r') line.pop_back();
        // "#" is a comment in text/uri-list, and a blank line is nothing. Both appear in the
        // wild and a file manager that treated them as filenames would be the one with the bug.
        if (line.empty() || line[0] == '#') continue;
        if (line.compare(0, 7, "file://") == 0) d.paths.push_back(uri_unescape(line.substr(7)));
    }
    return d;
}

// ---- modifiers --------------------------------------------------------------------------------

Desc padding(Desc d, float units) { d->pad = Insets(units * 8); return d; }
Desc padding(Desc d, Insets i) { d->pad = i; return d; }
Desc weight(Desc d, float w) { d->weight = w; return d; }
Desc named(Desc d, std::string n) { d->name = std::move(n); return d; }
Desc styled(Desc d, TextStyle st) { d->SetStyle(st); return d; }

// ---- primitives -------------------------------------------------------------------------------

Desc Label(std::string t) { return std::make_shared<TextNode>(std::move(t), false); }
Desc Label(State<std::string>* s) { return std::make_shared<TextNode>(s, false); }
Desc Bold(std::string t) { return std::make_shared<TextNode>(std::move(t), true); }
Desc Button(std::string l, std::function<void()> f) {
    return std::make_shared<ButtonNode>(std::move(l), std::move(f));
}
Desc Button(std::string l, std::function<void()> f, State<bool>* enabled) {
    return std::make_shared<ButtonNode>(std::move(l), std::move(f), enabled);
}
Desc Spacer() { return std::make_shared<SpacerNode>(); }

Desc Checkbox(std::string label, State<bool>* on, std::function<void(bool)> f) {
    return std::make_shared<ToggleNode>(std::move(label), on, false, std::move(f));
}

Desc Switch(std::string label, State<bool>* on, std::function<void(bool)> f) {
    return std::make_shared<ToggleNode>(std::move(label), on, true, std::move(f));
}

Desc Slider(State<float>* value, float lo, float hi, std::function<void(float)> f) {
    return std::make_shared<SliderNode>(value, lo, hi, std::move(f));
}

Desc Progress(State<float>* fraction) { return std::make_shared<ProgressNode>(fraction); }

Desc RadioGroup(std::vector<std::string> labels, State<int>* chosen,
                std::function<void(int)> f, bool horizontal) {
    // A radio group is a STACK of toggles that agree to be exclusive, not a new widget. The
    // exclusivity is the only thing it adds, and it is one line per option.
    std::vector<Desc> items;
    for (size_t i = 0; i < labels.size(); i++) {
        auto cell = std::make_unique<State<bool>>(chosen && chosen->get() == (int)i);
        items.push_back(std::make_shared<ToggleNode>(labels[i], std::move(cell), false,
            [chosen, f, i](bool on) {
                if (!on || !chosen) return;
                chosen->set((int)i);
                if (f) f((int)i);
            }));
    }
    return horizontal ? HStack(std::move(items), 1.0f) : VStack(std::move(items), 0.35f);
}
Desc VStack(std::vector<Desc> c, float gap) {
    return std::make_shared<StackNode>(std::move(c), true, gap);
}
Desc HStack(std::vector<Desc> c, float gap) {
    return std::make_shared<StackNode>(std::move(c), false, gap);
}
Desc ListView(Rows* r, int* sel) { return std::make_shared<ListNode>(r, sel); }
void OnListContext(const Desc& list, std::function<void(int, float, float)> f) {
    if (auto* l = dynamic_cast<ListNode*>(list.get())) l->SetContextHandler(std::move(f));
}
void OnListDragPaths(const Desc& list, std::function<std::vector<std::string>(int)> f) {
    if (ListNode* l = dynamic_cast<ListNode*>(list.get())) l->SetDragPaths(std::move(f));
}

void OnListDrop(const Desc& list, std::function<bool(int, const DragData&)> f) {
    if (ListNode* l = dynamic_cast<ListNode*>(list.get())) l->SetDropHandler(std::move(f));
}

void OnListActivate(const Desc& list, std::function<void(int)> f) {
    if (auto* l = dynamic_cast<ListNode*>(list.get())) l->SetActivateHandler(std::move(f));
}
void OnListSelect(const Desc& list, std::function<void(int)> f) {
    if (auto* l = dynamic_cast<ListNode*>(list.get())) l->SetSelectHandler(std::move(f));
}
void OnIconsSelectionChanged(const Desc& d, std::function<void(const std::vector<int>&)> f) {
    if (IconsNode* i = dynamic_cast<IconsNode*>(d.get())) i->SetMultiHandler(std::move(f));
}

std::vector<int> IconsSelection(const Desc& d) {
    IconsNode* i = dynamic_cast<IconsNode*>(d.get());
    return i ? i->selection() : std::vector<int>();
}

void OnIconsDragPaths(const Desc& d, std::function<std::vector<std::string>(int)> f) {
    if (IconsNode* i = dynamic_cast<IconsNode*>(d.get())) i->SetDragPaths(std::move(f));
}

void OnIconsSelect(const Desc& d, std::function<void(int)> f) {
    if (auto* n = dynamic_cast<IconsNode*>(d.get())) n->SetSelectHandler(std::move(f));
}

void OnListSelectionChanged(const Desc& list, std::function<void(const std::vector<int>&)> f) {
    if (ListNode* l = dynamic_cast<ListNode*>(list.get())) l->SetMultiHandler(std::move(f));
}

void ListSingleSelect(const Desc& list) {
    if (ListNode* l = dynamic_cast<ListNode*>(list.get())) l->SetSingleSelect();
}

std::vector<int> ListSelection(const Desc& list) {
    ListNode* l = dynamic_cast<ListNode*>(list.get());
    return l ? l->selection() : std::vector<int>();
}

void OnListRename(const Desc& list, std::function<bool(int, const std::string&)> f) {
    if (auto* l = dynamic_cast<ListNode*>(list.get())) l->SetRenameHandler(std::move(f));
}
void BeginListRename(const Desc& list, int row) {
    if (auto* l = dynamic_cast<ListNode*>(list.get())) l->BeginRename(row);
}
Desc sized(Desc child, float w, float h) {
    return std::make_shared<SizedNode>(std::move(child), w, h);
}

Desc Either(State<bool>* first, Desc a, Desc b) {
    return std::make_shared<EitherNode>(first, std::move(a), std::move(b));
}

Desc TreeView(TreeSource* src, State<std::string>* selected) {
    return std::make_shared<TreeNode>(src, selected);
}

void OnTreeSelect(const Desc& tree, std::function<void(const std::string&)> f) {
    if (TreeNode* t = dynamic_cast<TreeNode*>(tree.get())) t->SetSelectHandler(std::move(f));
}

void OnTreeActivate(const Desc& tree, std::function<void(const std::string&)> f) {
    if (TreeNode* t = dynamic_cast<TreeNode*>(tree.get())) t->SetActivateHandler(std::move(f));
}

void OnTreeDrop(const Desc& tree, std::function<bool(const std::string&, const DragData&)> f) {
    if (TreeNode* t = dynamic_cast<TreeNode*>(tree.get())) t->SetDropHandler(std::move(f));
}

void RevealInTree(const Desc& tree, const std::string& id) {
    if (TreeNode* t = dynamic_cast<TreeNode*>(tree.get())) t->RevealId(id);
}

int TreeRowCount(const Desc& tree) {
    TreeNode* t = dynamic_cast<TreeNode*>(tree.get());
    return t ? t->row_count() : 0;
}

std::string TreeRowId(const Desc& tree, int row) {
    TreeNode* t = dynamic_cast<TreeNode*>(tree.get());
    return t ? t->row_id(row) : std::string();
}

int TreeRowDepth(const Desc& tree, int row) {
    TreeNode* t = dynamic_cast<TreeNode*>(tree.get());
    return t ? t->row_depth(row) : -1;
}

Desc HSplit(Desc a, Desc b, State<float>* ratio, SplitOpts o) {
    return std::make_shared<SplitNode>(std::move(a), std::move(b), ratio, false, o);
}

Desc VSplit(Desc a, Desc b, State<float>* ratio, SplitOpts o) {
    return std::make_shared<SplitNode>(std::move(a), std::move(b), ratio, true, o);
}

Desc Reveal(State<bool>* shown, Desc child, Transition t) {
    return std::make_shared<RevealNode>(shown, std::move(child), t);
}

Desc TextView(State<std::string>* t, bool wrap) {
    return std::make_shared<TextViewNode>(t, wrap);
}

Desc IconView(Rows* r, int* sel) { return std::make_shared<IconsNode>(r, sel); }
void OnIconsActivate(const Desc& d, std::function<void(int)> f) {
    if (auto* n = dynamic_cast<IconsNode*>(d.get())) n->SetActivateHandler(std::move(f));
}
void OnIconsContext(const Desc& d, std::function<void(int, float, float)> f) {
    if (auto* n = dynamic_cast<IconsNode*>(d.get())) n->SetContextHandler(std::move(f));
}

Desc Breadcrumb(State<std::string>* p, std::function<void(const std::string&)> pick,
                std::function<void()> edit) {
    return std::make_shared<CrumbNode>(p, std::move(pick), std::move(edit));
}

Desc TextField(State<std::string>* t, std::function<void(const std::string&)> ch,
               std::function<void(const std::string&)> co, std::function<void()> ca,
               FieldOpts o) {
    return std::make_shared<FieldNode>(t, std::move(ch), std::move(co), std::move(ca),
                                       std::move(o));
}

namespace {
Desc make_field(State<std::string>* t, std::function<void(const std::string&)> ch,
                std::function<void(const std::string&)> co, std::function<void()> ca,
                FieldOpts o) {
    auto f = std::make_shared<FieldNode>(t, std::move(ch), std::move(co), std::move(ca),
                                         std::move(o));
    f->OnFocus(true);     // it appears already focused: nobody clicks a field they just summoned
    return f;
}
}  // namespace

// ---- Menu -------------------------------------------------------------------------------------

namespace {

// A glyph per effect class. Deliberately not colour alone: colour is the second signal, and a
// person who cannot distinguish red from grey still has to be able to tell a delete from an open.
const char* effect_mark(Effect e) {
    switch (e) {
        case Effect::Destructive: return "\xe2\x9c\x95";   // multiplication x
        case Effect::External: return "\xe2\x86\x97";      // north-east arrow: leaves the machine
        case Effect::Write: return "\xe2\x97\x8f";         // filled circle
        default: return "";
    }
}

// Default first, then by effect so reads precede writes, with destructive last behind a
// separator. The plumber already ranks candidates; this preserves that order and only forces
// the two rules the UI owes the user.
std::vector<MenuItem> arrange(std::vector<MenuItem> in) {
    std::vector<MenuItem> safe, destructive;
    for (MenuItem& m : in) {
        if (m.separator) continue;                       // callers do not place separators
        (m.effect == Effect::Destructive ? destructive : safe).push_back(std::move(m));
    }
    std::stable_sort(safe.begin(), safe.end(),
                     [](const MenuItem& a, const MenuItem& b) { return a.is_default > b.is_default; });
    std::vector<MenuItem> out = std::move(safe);
    if (!destructive.empty()) {
        out.push_back(MenuItem::sep());
        for (MenuItem& m : destructive) out.push_back(std::move(m));
    }
    return out;
}

}  // namespace

Menu::Menu(Window* parent, float x, float y, std::vector<MenuItem> items)
    : parent_(parent), items_(arrange(std::move(items))) {
    if (!parent_ || !parent_->native()) return;
    const Env& e = parent_->env_const();
    scale_ = parent_->scale();
    row_ = text_h(e) + e.unit * 1.25f;

    float widest = 0;
    for (const MenuItem& m : items_) {
        if (m.separator) continue;
        float wm = au_text_width(e.font, e.size, m.label.c_str(), -1) + e.unit * 4;
        if (!m.note.empty()) wm += au_text_width(e.font, e.size - 1, m.note.c_str(), -1) + e.unit * 2;
        widest = std::max(widest, wm);
    }
    w_ = std::max(140.0f, widest);
    h_ = e.unit;
    for (const MenuItem& m : items_) h_ += m.separator ? e.unit : row_;
    h_ += e.unit;

    sf_ = au_surface_new((int)w_, (int)h_, scale_);
    win_ = au_wl_popup_create(parent_->display(), parent_->native(), (int)x, (int)y, (int)w_,
                              (int)h_, &Menu::Event, this);
    if (win_) au_wl_win_set_key_fn(win_, &Menu::Key);
}

Menu::~Menu() {
    if (win_) au_wl_win_destroy(win_);
    if (sf_) au_surface_free(sf_);
}

int Menu::at(float y) const {
    const Env& e = parent_->env_const();
    float cy = e.unit;
    for (size_t i = 0; i < items_.size(); i++) {
        const float hh = items_[i].separator ? e.unit : row_;
        if (y >= cy && y < cy + hh) return items_[i].separator ? -1 : (int)i;
        cy += hh;
    }
    return -1;
}

void Menu::Draw() {
    if (!sf_) return;
    const Env& e = parent_->env_const();
    au_clear(sf_, au_color{32, 34, 45, 255});
    au_fill_round_rect(sf_, au_rect{0, 0, w_ - 1, h_ - 1}, 8, 8, au_color{38, 41, 54, 255});
    au_stroke_rect(sf_, au_rect{0, 0, w_ - 1, h_ - 1}, au_color{62, 66, 86, 255}, 1);

    float cy = e.unit;
    for (size_t i = 0; i < items_.size(); i++) {
        const MenuItem& m = items_[i];
        if (m.separator) {
            au_stroke_line(sf_, e.unit, cy + e.unit / 2, w_ - e.unit - 1, cy + e.unit / 2,
                           au_color{62, 66, 86, 255}, 1);
            cy += e.unit;
            continue;
        }
        if ((int)i == hover_ && m.enabled)
            au_fill_round_rect(sf_, au_rect{4, cy, w_ - 5, cy + row_ - 1}, 6, 6, e.surface_hi);

        // Disabled items are dimmed, never removed, and their note says why.
        const au_color fg = !m.enabled ? e.dim
                            : m.effect == Effect::Destructive ? au_color{240, 138, 138, 255}
                                                              : e.fg;
        const float base = cy + row_ * 0.72f;
        const char* mark = effect_mark(m.effect);
        float tx = e.unit;
        if (*mark) {
            au_text_draw(sf_, e.font, e.size - 1, mark, -1, tx, base, fg);
            tx += e.unit * 1.75f;
        } else {
            tx += e.unit * 1.75f;
        }
        au_text_draw(sf_, m.is_default ? e.bold : e.font, e.size, m.label.c_str(), -1, tx, base, fg);
        if (!m.note.empty()) {
            const float nw = au_text_width(e.font, e.size - 1, m.note.c_str(), -1);
            au_text_draw(sf_, e.font, e.size - 1, m.note.c_str(), -1, w_ - e.unit - nw, base, e.dim);
        }
        cy += row_;
    }
    au_wl_win_present_damage(win_, au_surface_pixels(sf_), au_surface_px_w(sf_),
                             au_surface_px_h(sf_), (int)scale_, nullptr, 0);
    dirty_ = false;
}

void Menu::Event(void* user, int kind, int a, int b) {
    Menu* m = static_cast<Menu*>(user);
    if (!m) return;
    if (kind == AU_EV_CLOSE) { m->done_ = true; return; }
    if (kind == AU_EV_POINTER_MOVE) {
        const int h = m->at(b / 256.0f);
        if (h != m->hover_) { m->hover_ = h; m->dirty_ = true; }
        return;
    }
    if (kind == AU_EV_POINTER_LEAVE) {
        if (m->hover_ != -1) { m->hover_ = -1; m->dirty_ = true; }
        return;
    }
    if (kind == AU_EV_POINTER_BUTTON && a == 1 && !b) {
        // Picked on RELEASE, not press: a press-and-drag off the item must be able to change its
        // mind, which is how menus have always worked and how a mis-aimed click is survivable.
        if (m->hover_ >= 0 && m->items_[m->hover_].enabled) m->picked_ = m->hover_;
        m->done_ = true;
    }
}

void Menu::Key(void* user, const au_key* k) {
    Menu* m = static_cast<Menu*>(user);
    if (!m || !k || !k->pressed) return;
    const int n = (int)m->items_.size();
    if (k->raw_char == 0x1b) { m->done_ = true; return; }                  // Escape
    if (k->raw_char == 0x0a || k->raw_char == 0x0d) {                       // Enter
        if (m->hover_ >= 0 && m->items_[m->hover_].enabled) m->picked_ = m->hover_;
        m->done_ = true;
        return;
    }
    const int step = k->raw_char == 0x1e ? -1 : (k->raw_char == 0x1f ? 1 : 0);
    if (!step) return;
    // Skip separators and disabled items: arrowing onto something you cannot pick is a dead end.
    int i = m->hover_;
    for (int tries = 0; tries < n; tries++) {
        i = i < 0 ? (step > 0 ? 0 : n - 1) : ((i + step) % n + n) % n;
        if (!m->items_[i].separator && m->items_[i].enabled) { m->hover_ = i; m->dirty_ = true; return; }
    }
}

int Menu::Run(int max_ms) {
    if (!win_) return -1;
    const std::chrono::steady_clock::time_point start = std::chrono::steady_clock::now();
    Draw();
    while (!done_) {
        if (max_ms > 0 &&
            std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - start)
                    .count() > max_ms)
            break;
        if (dirty_) { Draw(); continue; }
        au_wl_wait(parent_->display(), 50);
    }
    if (picked_ >= 0 && picked_ < (int)items_.size() && items_[picked_].run) items_[picked_].run();
    return picked_;
}

void Window::Defer(std::function<void()> f) {
    {
        std::lock_guard<std::mutex> g(deferred_mu_);
        deferred_.push_back(std::move(f));
    }
    dirty_ = true;      // wake the loop so it drains promptly
}

void Window::RequestCopy(const std::string& text) {
    Defer([this, text] { au_clipboard_set_text(wl_, text.c_str()); });
}

void Window::RequestPaste(Node* target) {
    Defer([this, target] {
        char buf[4096];
        const int n = au_clipboard_get_text(wl_, buf, sizeof buf);
        if (n <= 0) return;
        // Still in the tree? A rename field is gone once its edit commits.
        if (std::find(all_.begin(), all_.end(), target) == all_.end()) return;
        target->InsertText(std::string(buf, (size_t)n));
        Invalidate(target->frame);
    });
}

int Window::ShowMenu(float x, float y, std::vector<MenuItem> items, int max_ms) {
    Menu m(this, x, y, std::move(items));
    if (getenv("AURORA_UI_TRACE"))
        fprintf(stderr, "ui: menu at %.0f,%.0f -> %s\n", x, y, m.ok() ? "created" : "FAILED");
    if (!m.ok()) return -1;
    const int picked = m.Run(max_ms);
    Invalidate();          // the menu covered part of the window; it owes a full frame back
    return picked;
}

// ---- window -----------------------------------------------------------------------------------

Window::Window(const char* title, int lw, int lh) : lw_(lw), lh_(lh) {
    wl_ = au_wl_connect();
    if (!wl_) return;
    scale_ = au_wl_output_scale(wl_);
    if (scale_ < 1) scale_ = 1;
    float sz = 13;
    if (au_font_system("plain", &env_.font, &sz)) env_.size = sz;
    if (!au_font_system("bold", &env_.bold, &sz)) env_.bold = env_.font;
    // Italic is not a system font: Haiku's SystemFont only knows plain, bold and fixed. So find
    // the plain face's family and ask for its italic sibling by name, falling back to upright
    // rather than drawing nothing. "Oblique" is tried too, which is what many families call it.
    env_.italic = env_.font;
    env_.bold_italic = env_.bold;
    char fam[128];
    if (au_font_family_of(env_.font, fam, sizeof fam)) {
        au_font f{};
        if (au_font_lookup(fam, "Italic", &f) || au_font_lookup(fam, "Oblique", &f))
            env_.italic = f;
        if (au_font_lookup(fam, "Bold Italic", &f) || au_font_lookup(fam, "Bold Oblique", &f))
            env_.bold_italic = f;
    }
    sf_ = au_surface_new(lw_, lh_, scale_);
    win_ = au_wl_win_create(wl_, title, lw_, lh_, &Window::Event, this);
    if (win_) {
        au_wl_win_set_key_fn(win_, &Window::Key);
        au_wl_win_extra_buttons(win_, 1);   // this toolkit distinguishes the secondary button
        au_wl_popups_grab(wl_, 1);          // menus need a grab to receive keys at all
    }
}

Window::~Window() {
    if (win_) au_wl_win_destroy(win_);
    if (sf_) au_surface_free(sf_);
}

void Window::Layout() {
    if (!root_) return;
    root_->Measure(env_, Constraints{(float)lw_, (float)lh_});
    root_->Place(Rect{0, 0, (float)lw_, (float)lh_});
    laid_ = true;
}

void Window::Draw() {
    if (!sf_) return;
    // Apply a resize the event thread asked for. Here, because this is the main thread and the
    // surface belongs to it; before the damage check, because a resize is always a full frame.
    {
        int nw = 0, nh = 0;
        {
            std::lock_guard<std::mutex> g(size_mu_);
            nw = want_w_;
            nh = want_h_;
            want_w_ = want_h_ = 0;
        }
        if (nw > 0 && nh > 0 && (nw != lw_ || nh != lh_)) {
            lw_ = nw;
            lh_ = nh;
            au_surface_resize(sf_, nw, nh, scale_);   // keeps the pixels; no white flash
            laid_ = false;
            damage_.all();
            dirty_ = true;
        }
    }
    if (damage_.empty()) { dirty_ = false; return; }   // nothing asked to be redrawn
    const std::chrono::steady_clock::time_point t0 = std::chrono::steady_clock::now();

    if (!laid_) { Layout(); CollectFocusables(); damage_.all(); }   // new layout: all of it

    // Drive transitions. Retarget first, so a state change that happened since the last frame
    // starts moving; then, if anything IS moving, lay out again -- an animating node's size is
    // part of the layout, so the frame would otherwise show the old geometry.
    const bool allow = motion_allowed();
    bool moving = false;
    for (Node* n : all_) {
        n->Retarget(allow);
        if (n->Animating()) moving = true;
    }
    if (moving) {
        Layout();
        // Re-collect, because what answered Animating may have changed WHICH children exist:
        // an Either hands over a different subtree, and the focus order and the tick scan are
        // both built from that walk. Without this, Tab still led to the view you had left.
        // CollectFocusables already drops a focus that is no longer in the tree.
        CollectFocusables();
        damage_.all();        // a reveal moves everything below it; its own rect is not enough
    }

    const bool full = damage_.is_all();
    const Rect clip = full ? Rect{0, 0, (float)lw_, (float)lh_} : damage_.bounds();

    // Clip to the damaged union and redraw the tree through it. The nodes are not asked which
    // of them intersect: the clip rejects their pixels cheaply, and an intersection test per
    // node would cost more than it saves at this tree size. That stops being true for a tree
    // with thousands of nodes, which is what the list's virtualisation already prevents.
    au_clip_set(sf_, clip.box());
    au_fill_rect(sf_, clip.box(), au_color{24, 26, 34, 255});
    if (root_) root_->Draw(sf_, env_);
    au_clip_reset(sf_);

    // AURORA_UI_SLOW_MS burns that many milliseconds inside the draw. It exists to prove the
    // budget gate fires: a safety mechanism nobody has watched trip is a mechanism that does not
    // work. Nothing reads it in a normal run.
    if (const char* sl = getenv("AURORA_UI_SLOW_MS")) {
        const double want = atof(sl);
        const std::chrono::steady_clock::time_point s0 = std::chrono::steady_clock::now();
        while (std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - s0)
                   .count() < want) {}
    }
    draw_ms_.push_back(
        std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - t0).count());

    // Report the damage in BUFFER pixels, which is layout times scale.
    const double sc = scale_;
    total_px_ += (double)lw_ * lh_;
    if (full) {
        full_frames_++;
        damaged_px_ += (double)lw_ * lh_;
        au_wl_win_present_damage(win_, au_surface_pixels(sf_), au_surface_px_w(sf_),
                                 au_surface_px_h(sf_), (int)sc, nullptr, 0);
    } else {
        partial_frames_++;
        std::vector<int> r;
        r.reserve(damage_.rects().size() * 4);
        for (const Rect& e : damage_.rects()) {
            r.push_back((int)(e.x * sc));
            r.push_back((int)(e.y * sc));
            r.push_back((int)(e.w * sc) + 1);
            r.push_back((int)(e.h * sc) + 1);
            damaged_px_ += (double)e.w * e.h;
        }
        au_wl_win_present_damage(win_, au_surface_pixels(sf_), au_surface_px_w(sf_),
                                 au_surface_px_h(sf_), (int)sc, r.data(),
                                 (int)(r.size() / 4));
    }
    damage_.clear();
    dirty_ = false;
}

bool Window::motion_allowed() const {
    // The last eight frames decide. Eight is enough to notice sustained pressure and few enough
    // that one slow frame does not switch motion off for a whole interaction.
    const size_t n = draw_ms_.size();
    if (n < 8) return true;
    size_t over = 0;
    for (size_t i = n - 8; i < n; i++)
        if (draw_ms_[i] > 8.0) over++;
    if (over >= 3) { motion_degraded_++; return false; }
    return true;
}

Window::DamageStats Window::damage_stats() const {
    DamageStats s;
    s.full = full_frames_;
    s.partial = partial_frames_;
    s.mean_fraction = total_px_ > 0 ? damaged_px_ / total_px_ : 1.0;
    return s;
}

// The present_wl callbacks. These run on ITS thread and do exactly one thing: record.
void Window::Event(void* user, int kind, int a, int b) {
    Window* w = static_cast<Window*>(user);
    if (!w) return;
    std::lock_guard<std::mutex> g(w->q_mu_);
    // Pointer motion COALESCES. A pointer moving at 1000 Hz into a window drawing at 60 would
    // otherwise build a queue of positions that are all stale but the last, and the cursor would
    // lag behind the hand by however far behind the queue had fallen.
    if (kind == AU_EV_POINTER_MOVE && !w->queue_.empty() &&
        w->queue_.back().kind == AU_EV_POINTER_MOVE) {
        w->queue_.back().a = a;
        w->queue_.back().b = b;
        return;
    }
    // A bound, so a wedged main loop cannot be made to eat memory by waggling the mouse. Dropping
    // the OLDEST keeps the newest state, which is the one worth having.
    if (w->queue_.size() > 4096) w->queue_.erase(w->queue_.begin());
    InputEvent ev;
    ev.kind = kind;
    ev.a = a;
    ev.b = b;
    w->queue_.push_back(ev);
}

void Window::Key(void* user, const au_key* k) {
    Window* w = static_cast<Window*>(user);
    if (!w || !k) return;
    std::lock_guard<std::mutex> g(w->q_mu_);
    if (w->queue_.size() > 4096) w->queue_.erase(w->queue_.begin());
    InputEvent ev;
    ev.kind = InputEvent::kKey;
    ev.key = *k;
    w->queue_.push_back(ev);
}

// Main thread. Everything below here touches nodes, focus, damage and the surface, and is only
// ever reached from the loop.
void Window::DrainInput() {
    std::vector<InputEvent> todo;
    {
        std::lock_guard<std::mutex> g(q_mu_);
        todo.swap(queue_);
    }
    for (const InputEvent& ev : todo) {
        if (ev.kind == InputEvent::kKey) HandleKey(ev.key);
        else HandleEvent(ev.kind, ev.a, ev.b);
    }
}

void Window::HandleEvent(int kind, int a, int b) {
    Window* w = this;
    switch (kind) {
        case AU_EV_CLOSE:
            w->quit_ = true;
            break;
        case AU_EV_MODS:
            // Which modifiers are held, kept so a PRESS can know. OnPressAt carries where and
            // how many clicks, not what was held, and a file manager needs Ctrl-click and
            // Shift-click to mean different things from a plain one.
            w->mods_ = (uint32_t)a;
            break;
        case AU_EV_RESIZE:
            // RECORD it; the loop applies it. This handler runs on present_wl's event thread,
            // and it used to call au_surface_resize here -- reallocating the pixel buffer while
            // the main thread was drawing into it. At scale 1 the sizes were benign enough to
            // get away with; at the 1.45 a real display asks for, Atlas aborted inside
            // Canvas::FillRect on the first fill of Window::Draw, indexing the vector that had
            // just been replaced underneath it.
            //
            // This is the same rule the menu and the clipboard already follow and it is the
            // third time it has been learned: handlers decide, the loop acts.
            if (a > 0 && b > 0) {
                std::lock_guard<std::mutex> g(w->size_mu_);
                w->want_w_ = a;
                w->want_h_ = b;
                w->dirty_ = true;
            }
            break;
        case AU_EV_POINTER_MOVE: {
            w->px_ = a / 256.0f;
            w->py_ = b / 256.0f;
            // A drag goes to the node the press landed on, not to whatever is under the
            // pointer now: dragging a selection past the end of a field must keep selecting in
            // that field rather than hand the motion to a neighbour.
            if (w->pressed_) {
                w->drag_motion(w->px_, w->py_);
                // While a payload is in flight the press no longer means "scrub inside this
                // widget": a scrollbar thumb must not follow the pointer that is carrying a file.
                if (!w->drag_live_) {
                    w->pressed_->OnDragTo(w->px_, w->py_);
                    w->Invalidate(w->pressed_->frame);
                }
                break;
            }
            Node* n = w->root_ ? w->root_->HitTest(w->px_, w->py_) : nullptr;
            if (n != w->hover_) {
                // The payoff: a hover repaints the two nodes whose appearance changed, not the
                // window. Before damage tracking this was a full repaint of every frame a
                // pointer crossed a button.
                if (w->hover_) { w->hover_->OnLeave(); w->Invalidate(w->hover_->frame); }
                w->hover_ = n;
                if (n) { n->OnEnter(); w->Invalidate(n->frame); }
            }
            break;
        }
        case AU_EV_POINTER_LEAVE:
            if (w->hover_) {
                w->hover_->OnLeave();
                w->Invalidate(w->hover_->frame);
                w->hover_ = nullptr;
            }
            break;
        case AU_EV_POINTER_BUTTON: {
            if (getenv("AURORA_UI_TRACE"))
                fprintf(stderr, "ui: button a=%d pressed=%d at %.0f,%.0f\n", a, b, w->px_, w->py_);
            if (a == 2 || a == 3) {        // secondary button: BTN_RIGHT arrives as 2 or 3
                if (!b) break;             // act on press, as a context menu does
                Node* hit = w->root_ ? w->root_->HitTest(w->px_, w->py_) : nullptr;
                if (getenv("AURORA_UI_TRACE"))
                    fprintf(stderr, "ui: context hit=%s frame=%.0f,%.0f %.0fx%.0f\n",
                            hit ? (hit->name.empty() ? "(unnamed)" : hit->name.c_str()) : "NONE",
                            hit ? hit->frame.x : 0, hit ? hit->frame.y : 0,
                            hit ? hit->frame.w : 0, hit ? hit->frame.h : 0);
                for (Node* n = hit; n; n = n->parent)
                    if (n->OnContext(w->px_, w->py_)) { w->Invalidate(); break; }
                break;
            }
            if (a != 1) break;
            Node* n = w->root_ ? w->root_->HitTest(w->px_, w->py_) : nullptr;
            if (b) {
                // Click counting: the same node, within 400 ms and 4 layout units, is a repeat.
                // Both bounds matter -- time alone makes two deliberate clicks in different
                // places a double-click, which is how a selection gets destroyed by accident.
                const std::chrono::steady_clock::time_point now = std::chrono::steady_clock::now();
                const double since =
                    std::chrono::duration<double, std::milli>(now - w->last_click_).count();
                const bool near = std::fabs(w->px_ - w->last_x_) < 4 &&
                                  std::fabs(w->py_ - w->last_y_) < 4;
                w->clicks_ = (n == w->last_node_ && since < 400 && near) ? w->clicks_ + 1 : 1;
                w->last_click_ = now;
                w->last_node_ = n;
                w->last_x_ = w->px_;
                w->last_y_ = w->py_;

                w->pressed_ = n;
                // Arm, do not start: whether this press is a drag is not known until it moves.
                w->drag_armed_ = n != nullptr;
                w->drag_x0_ = w->px_;
                w->drag_y0_ = w->py_;
                if (n) {
                    // A press moves focus to what was pressed, if it takes focus. Clicking a
                    // thing and then typing should talk to that thing.
                    if (n->CanFocus()) w->SetFocus(n);
                    n->OnPressAt(w->px_, w->py_, w->clicks_);
                    w->Invalidate(n->frame);
                }
            } else {
                // A drag that was in flight ends here and the release is ITS ending, not a
                // click on whatever happens to be under the pointer: releasing over a folder
                // after dragging a file there must not also select that folder.
                const bool was_drag = w->drag_live_;
                w->drag_finish(w->px_, w->py_);
                if (w->pressed_ && !was_drag) {
                    w->pressed_->OnRelease(w->pressed_ == n);
                    w->Invalidate(w->pressed_->frame);
                }
                w->pressed_ = nullptr;
                // A click handler can change anything, so after a release the safe answer is a
                // full frame. A node that knows better can call Invalidate(rect) itself.
                w->Invalidate();
            }
            break;
        }
        case AU_EV_WHEEL: {
            // Route to the node under the pointer and bubble up until something handles it.
            // This replaced a global pointer to the one list a window happened to have, which
            // worked only because there was exactly one.
            Node* n = w->root_ ? w->root_->HitTest(w->px_, w->py_) : nullptr;
            for (; n; n = n->parent) {
                if (n->OnWheel(b / 256.0f)) {
                    // A scroll changes every row, so the handler's own rect is the damage --
                    // still less than the window.
                    w->Invalidate(n->frame);
                    break;
                }
            }
            break;
        }
        default:
            break;
    }
}

void Window::HandleKey(const au_key& kk) {
    Window* w = this;
    const au_key* k = &kk;
    w->keys_seen_++;
    // The app's accelerators first: they must work wherever the focus is.
    if (w->key_hook_ && w->key_hook_(*k)) return;
    // Tab moves focus. It is handled before the focused node sees the key, because a widget that
    // swallowed Tab would trap a keyboard user inside it.
    if (k->pressed && k->raw_char == '\t') {
        w->MoveFocus((k->mods & AU_MOD_SHIFT) ? -1 : 1);
        return;
    }
    // OnKey is asked ONCE and the answer kept. Asking twice -- once to see whether it declined,
    // then again to let it act -- delivers every keystroke to the widget twice, which for a
    // list's type-ahead means every letter typed lands two deep.
    const bool took = w->focus_ && w->focus_->OnKey(*k);

    // An arrow the focused widget DECLINED moves focus instead. A list keeps its own up and
    // down, and a tree keeps expand and collapse; what reaches here is an arrow that meant
    // nothing where it was, which is exactly when a person expects to leave.
    if (!took && k->pressed && !(k->mods & AU_MOD_ALT) && w->focus_) {
        int dx = 0, dy = 0;
        switch (k->raw_char) {
            case 0x1c: dx = -1; break;
            case 0x1d: dx = 1; break;
            case 0x1e: dy = -1; break;
            case 0x1f: dy = 1; break;
            default: break;
        }
        if ((dx || dy) && w->MoveFocusSpatial(dx, dy)) return;
    }
    if (took) {
        std::vector<Rect> hint;
        if (w->focus_->DamageHint(&hint))
            for (const Rect& r : hint) w->Invalidate(r);
        else
            w->Invalidate(w->focus_->frame);
        return;
    }
    // Unhandled: bubble to the ancestors, so a container can act for a child.
    for (Node* n = w->focus_ ? w->focus_->parent : nullptr; n; n = n->parent)
        if (n->OnKey(*k)) { w->Invalidate(n->frame); return; }
}

void Window::CollectFocusables() {
    focusables_.clear();
    all_.clear();
    if (!root_) return;
    // Depth-first in layout order, which is the order a reader's eye takes and therefore the
    // order Tab should follow.
    std::vector<Node*> stack{root_.get()};
    std::vector<Node*> order;
    while (!stack.empty()) {
        Node* n = stack.back();
        stack.pop_back();
        order.push_back(n);
        std::vector<Node*> kids;
        n->Children(&kids);
        for (size_t i = kids.size(); i > 0; i--) stack.push_back(kids[i - 1]);
    }
    all_ = order;
    for (Node* n : order) {
        n->Attached(this);
        if (n->CanFocus()) focusables_.push_back(n);
    }
    if (focus_ && std::find(focusables_.begin(), focusables_.end(), focus_) == focusables_.end())
        focus_ = nullptr;        // it went away with the last layout
}

void Window::SetFocus(Node* n) {
    if (n == focus_) return;
    if (getenv("AURORA_UI_TRACE"))
        fprintf(stderr, "ui: focus %s -> %s\n",
                focus_ ? (focus_->name.empty() ? "(unnamed)" : focus_->name.c_str()) : "NONE",
                n ? (n->name.empty() ? "(unnamed)" : n->name.c_str()) : "NONE");
    if (focus_) { focus_->OnFocus(false); Invalidate(focus_->frame); }
    focus_ = n;
    if (focus_) { focus_->OnFocus(true); Invalidate(focus_->frame); }
}

void Window::MoveFocus(int delta) {
    if (focusables_.empty()) return;
    int at = 0;
    for (size_t i = 0; i < focusables_.size(); i++)
        if (focusables_[i] == focus_) { at = (int)i + delta; break; }
    const int n = (int)focusables_.size();
    at = ((at % n) + n) % n;     // wrap both ways
    SetFocus(focusables_[at]);
}

Node* Window::FindNamed(const char* name) {
    if (!root_ || !name) return nullptr;
    std::vector<Node*> stack{root_.get()};
    while (!stack.empty()) {
        Node* n = stack.back();
        stack.pop_back();
        if (n->name == name) return n;
        std::vector<Node*> kids;
        n->Children(&kids);
        for (size_t i = kids.size(); i > 0; i--) stack.push_back(kids[i - 1]);
    }
    return nullptr;
}

bool Window::FocusNamed(const char* name) {
    if (focusables_.empty()) CollectFocusables();
    Node* n = FindNamed(name);
    if (!n || !n->CanFocus()) return false;
    SetFocus(n);
    return true;
}

bool Window::MoveFocusSpatial(int dx, int dy) {
    if (focusables_.empty()) CollectFocusables();
    if (!focus_ || focusables_.size() < 2) return false;
    const Rect f = focus_->frame;
    const float cx = f.x + f.w * 0.5f, cy = f.y + f.h * 0.5f;
    Node* best = nullptr;
    float best_d = 0;
    for (Node* n : focusables_) {
        if (n == focus_ || n->frame.w <= 0 || n->frame.h <= 0) continue;
        const float ddx = n->frame.x + n->frame.w * 0.5f - cx;
        const float ddy = n->frame.y + n->frame.h * 0.5f - cy;
        // Strictly in the direction asked for. Without this, Left from a left-hand pane picks
        // the thing to its RIGHT because it happens to be nearest.
        if (dx != 0 && ddx * dx <= 0) continue;
        if (dy != 0 && ddy * dy <= 0) continue;
        const float along = dx != 0 ? std::fabs(ddx) : std::fabs(ddy);
        const float across = dx != 0 ? std::fabs(ddy) : std::fabs(ddx);
        // Drift across the axis costs double, so Left from the list prefers the sidebar beside
        // it over the toolbar button that is also technically to the left, but far above.
        const float d = along + across * 2;
        if (!best || d < best_d) {
            best = n;
            best_d = d;
        }
    }
    if (!best) return false;
    SetFocus(best);
    return true;
}

void Window::FocusFirst() {
    if (focusables_.empty()) CollectFocusables();
    if (!focusables_.empty()) SetFocus(focusables_.front());
}

// ---- the drag state machine ---------------------------------------------------------------------
//
// A drag is a PRESS THAT MOVED, not a mode. The pressed node is asked for a payload only once
// the pointer has travelled past a threshold, and a node with nothing to drag declines, leaving
// the press meaning what it meant before. Without the threshold every click on a filename would
// begin a drag and nothing could be selected; without the decline, a list could not both select
// and drag.
void Window::drag_motion(float x, float y) {
    if (getenv("AURORA_UI_TRACE"))
        fprintf(stderr, "ui: drag_motion armed=%d live=%d pressed=%s d=%.1f,%.1f\n",
                (int)drag_armed_, (int)drag_live_,
                pressed_ ? (pressed_->name.empty() ? "(unnamed)" : pressed_->name.c_str()) : "NONE",
                x - drag_x0_, y - drag_y0_);
    if (drag_armed_ && !drag_live_) {
        // 6 layout units. Below about four, a drag starts on the hand tremor of an ordinary
        // click; far above it, dragging feels like it is resisting.
        if (std::fabs(x - drag_x0_) < 6 && std::fabs(y - drag_y0_) < 6) return;
        drag_armed_ = false;
        DragData d;
        if (!pressed_ || !pressed_->OnDragStart(drag_x0_, drag_y0_, &d) || d.mime.empty()) return;
        drag_ = std::move(d);
        drag_src_ = pressed_;
        drag_live_ = true;
        if (getenv("AURORA_UI_TRACE"))
            fprintf(stderr, "ui: drag START %zu path(s) first=%s\n", drag_.paths.size(),
                    drag_.paths.empty() ? "-" : drag_.paths[0].c_str());
        Invalidate();
    }
    if (!drag_live_) return;

    // The target is whatever is under the pointer and willing, walking UP from the hit so a row
    // inside a list can decline and let the list answer.
    //
    // The SOURCE is not excluded, and that is the important part. Excluding it to stop a drop on
    // itself looked like care and broke the main gesture outright: dragging a file onto a folder
    // in the same list is a drop on the source. Whether a particular drop is a no-op is a
    // question only the node can answer -- the list compares destination against origin per path
    // -- so the node answers it.
    Node* hit = root_ ? root_->HitTest(x, y) : nullptr;
    Node* want = nullptr;
    for (Node* n = hit; n; n = n->parent)
        if (n->CanAcceptDrop(drag_)) { want = n; break; }

    if (want != drag_tgt_) {
        // The leave is guaranteed: a target that highlighted itself gets told, even when the
        // pointer left the window entirely or the drag was cancelled.
        if (drag_tgt_) { drag_tgt_->OnDropHover(x, y, false); Invalidate(drag_tgt_->frame); }
        drag_tgt_ = want;
        if (drag_tgt_) Invalidate(drag_tgt_->frame);
    }
    if (drag_tgt_) { drag_tgt_->OnDropHover(x, y, true); Invalidate(drag_tgt_->frame); }
}

void Window::drag_finish(float x, float y) {
    drag_armed_ = false;
    if (!drag_live_) return;
    Node* tgt = drag_tgt_;
    // Cleared BEFORE the handler runs: a drop handler may rebuild the tree (dropping a file into
    // a folder re-reads the directory), and leaving a pointer to a node it might free is how a
    // use-after-free gets written.
    drag_live_ = false;
    drag_tgt_ = nullptr;
    drag_src_ = nullptr;
    if (getenv("AURORA_UI_TRACE"))
        fprintf(stderr, "ui: drag FINISH tgt=%s\n",
                tgt ? (tgt->name.empty() ? "(unnamed)" : tgt->name.c_str()) : "NONE");
    if (tgt) {
        tgt->OnDropHover(x, y, false);
        tgt->OnDrop(x, y, drag_);
    }
    drag_ = DragData{};
    Invalidate();
}

// Damage for a node that has just ticked.
//
// A node animating its own GEOMETRY cannot damage itself. While a reveal opens from zero its
// frame is zero-WIDE, so Invalidate(frame) adds a degenerate rectangle, the damage list stays
// empty, and Draw returns at its first line -- before the layout that would have given the node
// a frame to damage. The animation then advanced correctly and invisibly, forever: the preview
// pane reported open and running at fraction 0.000 every frame.
//
// A node whose size is part of the layout owes the window a full frame anyway; Draw's own
// `moving` branch already says so with damage_.all(). This is that same rule, one step earlier.
void Window::damage_for(Node* n) {
    if (n->Animating()) Invalidate();
    else Invalidate(n->frame);
}

int Window::Run(int frames, bool force_redraw, int max_ms) {
    if (!win_) return 1;
    const std::chrono::steady_clock::time_point start = std::chrono::steady_clock::now();
    Draw();
    while (!quit_) {
        if (frames > 0 && (int)draw_ms_.size() >= frames) break;
        if (max_ms > 0 &&
            std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - start)
                    .count() > max_ms)
            break;
        // Input first: dispatch what the event thread recorded, on THIS thread, where the
        // nodes live. Before the deferred queue, because a handler may defer something.
        DrainInput();
        // Then deferred work, BEFORE drawing. An event handler cannot create a surface -- it
        // runs on present_wl's event thread, which holds the display mutex, so au_wl_popup_create
        // would deadlock against it. Handlers decide; the loop acts.
        {
            std::vector<std::function<void()>> todo;
            {
                std::lock_guard<std::mutex> g(deferred_mu_);
                todo.swap(deferred_);
            }
            for (auto& f : todo) f();
        }
        // A worker asked for a frame. Turned into damage HERE, on the main thread, because the
        // damage list is the main thread's.
        if (ext_dirty_.exchange(false, std::memory_order_acquire)) Invalidate();
        if (force_redraw) Invalidate();
        if (dirty_) {
            Draw();
            continue;             // drain the work that is already pending before sleeping
        }
        // Sleep until the event thread dispatched something. Events arrive on present_wl's own
        // thread and set dirty_ through the handlers, so this is the whole loop: wake, redraw
        // what changed, sleep. The old version called Draw() unconditionally, which spun a core
        // and made every frame measurement a measurement of a busy loop.
        //
        // The timeout is a floor on responsiveness, not a tick: nothing animates yet. When the
        // animation driver lands it shortens this while a transition is running.
        // Sleep no longer than the shortest tick anything asks for: a caret blinks at 530 ms, a
        // running transition wants 16. An idle window with nothing animating sleeps the full
        // interval, which is what keeps a desktop of open windows off the CPU.
        int tick = 0;
        for (Node* n : all_) {
            const int t = n->TickMs();
            if (t > 0 && (tick == 0 || t < tick)) tick = t;
        }
        if (focus_) {
            const int t = focus_->TickMs();
            if (t > 0 && (tick == 0 || t < tick)) tick = t;
        }
        const int wait = max_ms > 0 ? std::min(20, tick > 0 ? tick : 20)
                                    : (tick > 0 ? tick : 100);
        // Tick after the wait WHATEVER the wait returned. This used to tick only on a timeout,
        // which meant an animation advanced only while nothing else was happening: under a
        // compositor sending frame callbacks the wait is woken every time, so the ease never
        // got a tick and every transition in the toolkit sat frozen at its starting value. The
        // preview pane reported itself open, running and at fraction 0.000, forever.
        //
        // Ticking extra times is free and correct because Ease::tick reads the clock rather
        // than counting calls; ticking too rarely was the bug.
        au_wl_wait(wl_, wait);
        if (tick > 0) {
            for (Node* n : all_)
                if (n->TickMs() > 0) { n->OnTick(); damage_for(n); }
            if (focus_ && focus_->TickMs() > 0) { focus_->OnTick(); damage_for(focus_); }
        }
    }
    return 0;
}

Window::Timing Window::timing() const {
    Timing t;
    if (draw_ms_.empty()) return t;
    std::vector<double> v = draw_ms_;
    std::sort(v.begin(), v.end());
    t.frames = v.size();
    t.p50 = v[v.size() / 2];
    t.p95 = v[(size_t)(v.size() * 0.95)];
    t.max = v.back();
    t.over_8ms = (size_t)std::count_if(v.begin(), v.end(), [](double d) { return d > 8.0; });
    return t;
}

}  // namespace ui
}  // namespace aurora
