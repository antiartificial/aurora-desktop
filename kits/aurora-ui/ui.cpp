// aurora::ui -- layout, widgets and the window. See ui.h for the model and docs/aurora-ui.md for
// the reasoning.

#include "ui.h"

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <cstring>
#include <strings.h>

namespace aurora {
namespace ui {

namespace {

float text_h(const Env& e) {
    au_font_metrics m;
    m.ascent = m.descent = m.leading = 0;
    au_font_metrics_get(e.font, e.size, &m);
    return m.ascent + m.descent + m.leading;
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
    TextNode(std::string t, bool bold) : t_(std::move(t)), bold_(bold) {}
    TextNode(State<std::string>* s, bool bold) : s_(s), bold_(bold) {}

    const std::string& str() const { return s_ ? s_->get() : t_; }

    Size Measure(const Env& e, Constraints) override {
        const au_font f = bold_ ? e.bold : e.font;
        return Size{au_text_width(f, e.size, str().c_str(), -1) + pad.l + pad.r,
                    text_h(e) + pad.t + pad.b};
    }
    void Draw(au_surface* s, const Env& e) override {
        const Rect in = inner();
        au_text_draw(s, bold_ ? e.bold : e.font, e.size, str().c_str(), -1,
                     in.x, baseline_in(e, in), e.fg);
    }
    Node* HitTest(float, float) override { return nullptr; }   // text is not a target

private:
    std::string t_;
    State<std::string>* s_ = nullptr;
    bool bold_ = false;
};

// ---- Button -----------------------------------------------------------------------------------

class ButtonNode : public Node {
public:
    ButtonNode(std::string l, std::function<void()> f) : l_(std::move(l)), f_(std::move(f)) {}

    Size Measure(const Env& e, Constraints) override {
        const float w = au_text_width(e.font, e.size, l_.c_str(), -1);
        return Size{w + e.unit * 3, text_h(e) + e.unit * 1.75f};
    }
    void Draw(au_surface* s, const Env& e) override {
        const au_color bg = down_ ? e.accent : (over_ ? e.surface_hi : e.surface);
        au_fill_round_rect(s, frame.box(), 7, 7, bg);
        // A visible focus ring, always. Hiding where focus is makes a keyboard user guess.
        if (focused_) au_stroke_rect(s, frame.box(), e.accent, 2);
        const float tw = au_text_width(e.font, e.size, l_.c_str(), -1);
        au_text_draw(s, e.font, e.size, l_.c_str(), -1, frame.x + (frame.w - tw) / 2,
                     baseline_in(e, frame), down_ ? e.surface : e.fg);
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
        if (i >= 0 && i < r_->Count()) *sel_ = i;
    }
    void SetClickY(float y) { click_y_ = y; }

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
    std::string ahead_;
    std::chrono::steady_clock::time_point last_type_{};
};

}  // namespace

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

// ---- window -----------------------------------------------------------------------------------

Window::Window(const char* title, int lw, int lh) : lw_(lw), lh_(lh) {
    wl_ = au_wl_connect();
    if (!wl_) return;
    scale_ = au_wl_output_scale(wl_);
    if (scale_ < 1) scale_ = 1;
    float sz = 13;
    if (au_font_system("plain", &env_.font, &sz)) env_.size = sz;
    if (!au_font_system("bold", &env_.bold, &sz)) env_.bold = env_.font;
    sf_ = au_surface_new(lw_, lh_, scale_);
    win_ = au_wl_win_create(wl_, title, lw_, lh_, &Window::Event, this);
    if (win_) au_wl_win_set_key_fn(win_, &Window::Key);
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
            if (a != 1) break;
            Node* n = w->root_ ? w->root_->HitTest(w->px_, w->py_) : nullptr;
            if (b) {
                w->pressed_ = n;
                if (n) {
                    // A press moves focus to what was pressed, if it takes focus. Clicking a
                    // thing and then typing should talk to that thing.
                    if (n->CanFocus()) w->SetFocus(n);
                    n->OnPress();
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
        au_wl_wait(wl_, max_ms > 0 ? 20 : 100);
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
