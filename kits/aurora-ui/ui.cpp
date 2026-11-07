// aurora::ui -- layout, widgets and the window. See ui.h for the model and docs/aurora-ui.md for
// the reasoning.

#include "ui.h"
#include "../interface/appserver/present.h"

#include <algorithm>
#include <chrono>
#include <cstdio>
#include <cstring>

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

private:
    std::string l_;
    std::function<void()> f_;
    bool over_ = false, down_ = false;
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
    void Scroll(float dy) {
        top_ = std::max(0, top_ + (dy > 0 ? 3 : -3));
        const int vis = (int)((frame.h - head_h_) / row_h_);
        top_ = std::min(top_, std::max(0, r_->Count() - vis));
    }
    int rows_drawn() const { return rows_drawn_; }

private:
    Rows* r_;
    int* sel_;
    int top_ = 0, rows_drawn_ = 0;
    float row_h_ = 18, head_h_ = 20, click_y_ = 0;
};

ListNode* g_list = nullptr;   // the one list a window has, for wheel routing in the spike stage

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
Desc ListView(Rows* r, int* sel) {
    auto n = std::make_shared<ListNode>(r, sel);
    g_list = n.get();
    return n;
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
    sf_ = au_surface_new(lw_, lh_, scale_);
    win_ = au_wl_win_create(wl_, title, lw_, lh_, &Window::Event, this);
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

    if (!laid_) { Layout(); damage_.all(); }           // a new layout invalidates everything

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
                if (g_list && n == g_list) g_list->SetClickY(w->py_);
                if (n) { n->OnPress(); w->Invalidate(n->frame); }
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
        case AU_EV_WHEEL:
            // A scroll changes every row, so the list's own rect is the damage -- which is still
            // less than the window, and is why ListView reports its frame rather than calling
            // the no-argument Invalidate.
            if (g_list) { g_list->Scroll(b / 256.0f); w->Invalidate(g_list->frame); }
            break;
        default:
            break;
    }
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
