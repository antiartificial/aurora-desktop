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
        return Size{au_text_width(e.face(st_), e.sized(st_), str().c_str(), -1) + pad.l + pad.r,
                    text_h(e, st_) + pad.t + pad.b};
    }
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
    State<std::string>* s_ = nullptr;
    TextStyle st_;
};

// ---- Button -----------------------------------------------------------------------------------

class ButtonNode : public Node {
public:
    ButtonNode(std::string l, std::function<void()> f) : l_(std::move(l)), f_(std::move(f)) {}

    Size Measure(const Env& e, Constraints) override {
        const float w = au_text_width(e.face(st_), e.sized(st_), l_.c_str(), -1);
        return Size{w + e.unit * 3, text_h(e, st_) + e.unit * 1.75f};
    }
    void SetStyle(const TextStyle& st) override { st_ = st; }
    void Draw(au_surface* s, const Env& e) override {
        const au_color bg = down_ ? e.accent : (over_ ? e.surface_hi : e.surface);
        au_fill_round_rect(s, frame.box(), 7, 7, bg);
        // A visible focus ring, always. Hiding where focus is makes a keyboard user guess.
        if (focused_) au_stroke_rect(s, frame.box(), e.accent, 2);
        const float tw = au_text_width(e.face(st_), e.sized(st_), l_.c_str(), -1);
        au_text_draw(s, e.face(st_), e.sized(st_), l_.c_str(), -1, frame.x + (frame.w - tw) / 2,
                     baseline_in(e, frame, st_), down_ ? e.surface : e.ink(st_));
    }
    void OnEnter() override { over_ = true; }
    void OnLeave() override { over_ = false; down_ = false; }
    void OnPress() override { down_ = true; }
    void OnRelease(bool inside) override {
        down_ = false;
        if (inside && f_) f_();
    }
    bool CanFocus() const override { return true; }
    void OnFocus(bool g) override { focused_ = g; }
    bool OnKey(const au_key& k) override {
        // Space and Enter activate, which is what every toolkit since the 1980s does and what a
        // keyboard-only user reaches for first.
        if (!k.pressed) return false;
        if (k.utf8[0] == ' ' || k.utf8[0] == '\r' || k.utf8[0] == '\n') {
            if (f_) f_();
            return true;
        }
        return false;
    }

private:
    std::string l_;
    std::function<void()> f_;
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
            if (c_[i]->Wants(&ww)) w = ww > 0 ? ww : 1;
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
class ListNode : public Node {
public:
    ListNode(Rows* r, int* sel) : r_(r), sel_(sel) {}

    Size Measure(const Env& e, Constraints c) override {
        row_h_ = text_h(e) + e.unit * 0.75f;
        head_h_ = row_h_ + 2;
        return Size{c.max_w >= 0 ? c.max_w : 400, c.max_h >= 0 ? c.max_h : 300};
    }

    void Draw(au_surface* s, const Env& e) override {
        au_fill_rect(s, frame.box(), au_color{30, 32, 42, 255});

        // header
        float x = frame.x;
        for (int col = 0; col < r_->Columns(); col++) {
            const float cw = r_->ColumnWidth(col);
            au_text_draw(s, e.bold, e.size - 1, r_->ColumnTitle(col).c_str(), -1,
                         x + e.unit * 0.75f, frame.y + head_h_ * 0.72f, e.dim);
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
            if (sel_ && *sel_ == i)
                au_fill_rect(s, au_rect{frame.x, ry, frame.x + frame.w - 1, ry + row_h_ - 1},
                             e.accent);
            float cx = frame.x;
            for (int col = 0; col < r_->Columns(); col++) {
                const CellValue v = r_->Cell(i, col);
                const char* txt = v.state == CellState::Unknown ? "unknown" : v.text.c_str();
                if (v.state == CellState::Pending) txt = "";
                const au_color c = (sel_ && *sel_ == i)
                                       ? au_color{24, 26, 34, 255}
                                       : (v.state == CellState::Ready ? e.fg : e.dim);
                au_clip_set(s, au_rect{cx, ry, cx + r_->ColumnWidth(col) - 6, ry + row_h_ - 1});
                au_text_draw(s, e.font, e.size, txt, -1, cx + e.unit * 0.75f,
                             ry + row_h_ * 0.72f, c);
                au_clip_reset(s);
                cx += r_->ColumnWidth(col);
            }
        }
        rows_drawn_ = last >= first ? (last - first + 1) : 0;
    }

    Node* HitTest(float x, float y) override { return frame.has(x, y) ? this : nullptr; }
    void OnPress() override {}
    void OnRelease(bool inside) override {
        if (!inside || !sel_) return;
        const int i = top_ + (int)((click_y_ - frame.y - head_h_ - 1) / row_h_);
        if (i < 0 || i >= r_->Count()) return;
        const int before = *sel_;
        *sel_ = i;
        if (before != i) { rows_dirty_.push_back(before); rows_dirty_.push_back(i); dirty_ = true; }
        if (clicks_ >= 2 && on_act_) on_act_(i);      // double-click activates
    }
    void OnPressAt(float x, float y, int clicks) override {
        (void)x;
        click_y_ = y;
        clicks_ = clicks;
    }
    void SetClickY(float y) { click_y_ = y; }
    void SetActivateHandler(std::function<void(int)> f) { on_act_ = std::move(f); }

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

    bool OnKey(const au_key& k) override {
        if (!k.pressed || !sel_) return false;
        const int n = r_->Count();
        if (n == 0) return false;
        const int cur = *sel_ < 0 ? -1 : *sel_;
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
        if (next != cur) {
            const int before_top = top_;
            *sel_ = next;
            Reveal(next);
            ahead_.clear();
            if (top_ != before_top) whole_ = true;        // it scrolled: every row moved
            else { rows_dirty_.push_back(cur); rows_dirty_.push_back(next); }
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
                    const int before_sel = cur;
                    *sel_ = row;
                    Reveal(row);
                    if (top_ != before_top) whole_ = true;
                    else { rows_dirty_.push_back(before_sel); rows_dirty_.push_back(row); }
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
        if (row < 0 || row >= r_->Count()) return false;
        ctx_row_ = row;
        if (sel_ && *sel_ < 0) { *sel_ = row; rows_dirty_.push_back(row); dirty_ = true; }
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
    float row_h_ = 18, head_h_ = 20, click_y_ = 0;
    bool focused_ = false, dirty_ = false, whole_ = false;
    std::vector<int> rows_dirty_;
    std::function<void(int, float, float)> on_ctx_;
    std::function<void(int)> on_act_;
    int ctx_row_ = -1, clicks_ = 1;
    std::string ahead_;
    std::chrono::steady_clock::time_point last_type_{};
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

        // Ctrl+A selects all. Every modern field does, and a user who cannot select all with the
        // keyboard has to reach for the mouse to replace a value.
        if (ctrl && (k.utf8[0] == 'a' || k.utf8[0] == 'A')) {
            sel_ = 0;
            caret_ = v.size();
            return true;
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
        return Size{nat_.w, nat_.h * e_.value()};
    }

    void Place(Rect r) override {
        frame = r;
        c_->parent = this;
        // The child keeps its natural height and is clipped, so revealing never reflows its
        // contents. A child that re-laid-out every frame of the animation would be the
        // expensive, janky version of this.
        c_->Place(Rect{r.x, r.y, r.w, nat_.h});
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
    bool Animating() const override { return e_.running(); }

private:
    State<bool>* shown_;
    Desc c_;
    Transition t_;
    Ease e_;
    Size nat_{};
    float want_ = 0;
};

}  // namespace

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
Desc Spacer() { return std::make_shared<SpacerNode>(); }
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
void OnListActivate(const Desc& list, std::function<void(int)> f) {
    if (auto* l = dynamic_cast<ListNode*>(list.get())) l->SetActivateHandler(std::move(f));
}
Desc Reveal(State<bool>* shown, Desc child, Transition t) {
    return std::make_shared<RevealNode>(shown, std::move(child), t);
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

void Window::Event(void* user, int kind, int a, int b) {
    Window* w = static_cast<Window*>(user);
    if (!w) return;
    switch (kind) {
        case AU_EV_CLOSE:
            w->quit_ = true;
            break;
        case AU_EV_RESIZE:
            if (a > 0 && b > 0) {
                w->lw_ = a;
                w->lh_ = b;
                au_surface_resize(w->sf_, a, b, w->scale_);   // keeps the pixels; no white flash
                w->laid_ = false;
                w->Invalidate();                              // new geometry: the whole frame
            }
            break;
        case AU_EV_POINTER_MOVE: {
            w->px_ = a / 256.0f;
            w->py_ = b / 256.0f;
            // A drag goes to the node the press landed on, not to whatever is under the
            // pointer now: dragging a selection past the end of a field must keep selecting in
            // that field rather than hand the motion to a neighbour.
            if (w->pressed_) {
                w->pressed_->OnDragTo(w->px_, w->py_);
                w->Invalidate(w->pressed_->frame);
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
                if (n) {
                    // A press moves focus to what was pressed, if it takes focus. Clicking a
                    // thing and then typing should talk to that thing.
                    if (n->CanFocus()) w->SetFocus(n);
                    n->OnPressAt(w->px_, w->py_, w->clicks_);
                    w->Invalidate(n->frame);
                }
            } else {
                if (w->pressed_) {
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

void Window::Key(void* user, const au_key* k) {
    Window* w = static_cast<Window*>(user);
    if (!w || !k) return;
    w->keys_seen_++;
    // Tab moves focus. It is handled before the focused node sees the key, because a widget that
    // swallowed Tab would trap a keyboard user inside it.
    if (k->pressed && k->raw_char == '\t') {
        w->MoveFocus((k->mods & AU_MOD_SHIFT) ? -1 : 1);
        return;
    }
    if (w->focus_ && w->focus_->OnKey(*k)) {
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
    for (Node* n : order)
        if (n->CanFocus()) focusables_.push_back(n);
    if (focus_ && std::find(focusables_.begin(), focusables_.end(), focus_) == focusables_.end())
        focus_ = nullptr;        // it went away with the last layout
}

void Window::SetFocus(Node* n) {
    if (n == focus_) return;
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

void Window::FocusFirst() {
    if (focusables_.empty()) CollectFocusables();
    if (!focusables_.empty()) SetFocus(focusables_.front());
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
        // Drain deferred work BEFORE drawing. An event handler cannot create a surface -- it
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
        if (!au_wl_wait(wl_, wait) && tick > 0) {
            for (Node* n : all_)
                if (n->TickMs() > 0) { n->OnTick(); Invalidate(n->frame); }
            if (focus_ && focus_->TickMs() > 0) { focus_->OnTick(); Invalidate(focus_->frame); }
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
