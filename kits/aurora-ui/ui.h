/*
 * aurora::ui -- the toolkit. See docs/aurora-ui.md for the language and the reasoning.
 *
 * Shape: values composed into a tree, modifiers that return values, an environment that flows
 * down, and state cells bound at build time. Taken from the Flutter / SwiftUI / Compose family;
 * none of them invented it and we are not copying any one of them.
 *
 * What it is NOT: there is no reconciler and no re-evaluated body. A tree is built once and then
 * mutated through State<T>, which invalidates exactly what it feeds. That is the decision that
 * makes a C++ API nearly as pleasant as a Swift one -- result builders exist to make a
 * re-evaluated body ergonomic, and we do not have one.
 *
 * Layout follows Flutter's rule because it is the one with a cost guarantee: constraints go down,
 * sizes come back up, the parent sets position, and a child is visited at most twice per pass.
 */
#ifndef AURORA_UI_H
#define AURORA_UI_H

#include "au_draw.h"

// Forward-declared at GLOBAL scope on purpose: declaring these inside namespace aurora::ui
// would create aurora::ui::au_wl, a different type from present.h's, and every call would fail
// to convert. Including present.h here instead would put Wayland in every consumer's headers.
struct au_wl;
struct au_wl_win;

#include <functional>
#include <memory>
#include <string>
#include <vector>

namespace aurora {
namespace ui {

// ---- geometry (layout units, BeOS-inclusive rects as au_draw uses them) ------------------------

struct Size { float w = 0, h = 0; };
struct Rect {
    float x = 0, y = 0, w = 0, h = 0;
    au_rect box() const { return au_rect{x, y, x + w - 1, y + h - 1}; }
    bool has(float px, float py) const { return px >= x && py >= y && px < x + w && py < y + h; }
};
struct Insets {
    float l = 0, t = 0, r = 0, b = 0;
    Insets() {}
    explicit Insets(float all) : l(all), t(all), r(all), b(all) {}
    Insets(float h, float v) : l(h), t(v), r(h), b(v) {}
};

// What a parent offers a child. -1 on an axis means "as much as you like, tell me what you want".
struct Constraints { float max_w = -1, max_h = -1; };

// ---- the environment: what flows down, with explicit override points -------------------------

struct Env {
    au_font font{};
    float size = 13;
    au_font bold{};
    float unit = 8;                 // the spacing unit; padding(1) is one unit
    au_color fg{236, 239, 244, 255};
    au_color dim{150, 157, 178, 255};
    au_color accent{122, 162, 247, 255};
    au_color surface{38, 41, 54, 255};
    au_color surface_hi{52, 56, 73, 255};
};

// ---- state: a cell, bound at build, that invalidates what it feeds ---------------------------

class Window;

// Set() marks the window dirty and nothing else: no diffing, no tree walk. The binding was made
// when the tree was built, so the cell already knows what to invalidate.
template <class T>
class State {
public:
    explicit State(T v) : v_(std::move(v)) {}
    const T& get() const { return v_; }
    void set(T n);
    void bind(Window* w) { win_ = w; }
    Window* window() const { return win_; }

private:
    T v_;
    Window* win_ = nullptr;
};

// ---- nodes -----------------------------------------------------------------------------------

class Node;
using Desc = std::shared_ptr<Node>;

class Node {
public:
    virtual ~Node() {}

    // Flutter's two halves. Measure must not depend on where it will be placed, and must not
    // visit a child more than once -- that is what keeps a pass linear.
    virtual Size Measure(const Env&, Constraints) = 0;
    virtual void Place(Rect r) { frame = r; }
    virtual void Draw(au_surface*, const Env&) = 0;

    // Returns the node that took the event, or nullptr. Hit testing walks the placed tree, so it
    // costs nothing until something is actually clicked.
    virtual Node* HitTest(float x, float y) { return frame.has(x, y) ? this : nullptr; }
    virtual void OnEnter() {}
    virtual void OnLeave() {}
    virtual void OnPress() {}
    virtual void OnRelease(bool inside) { (void)inside; }
    virtual bool Wants(float* out_weight) { (void)out_weight; return false; }

    Rect frame;
    Insets pad;
    float weight = 0;              // > 0 takes a share of the leftover main-axis space
    std::string name;              // what the accessibility tree and tests find it by

protected:
    // The box a child gets after padding, and the constraints to offer it.
    Rect inner() const {
        return Rect{frame.x + pad.l, frame.y + pad.t,
                    frame.w - pad.l - pad.r, frame.h - pad.t - pad.b};
    }
    Constraints shrink(Constraints c) const {
        if (c.max_w >= 0) c.max_w -= pad.l + pad.r;
        if (c.max_h >= 0) c.max_h -= pad.t + pad.b;
        return c;
    }
};

// ---- modifiers: each takes a Desc and returns it, so they chain -------------------------------

Desc padding(Desc d, float units);
Desc padding(Desc d, Insets i);
Desc weight(Desc d, float w);
Desc named(Desc d, std::string n);

// ---- primitives -------------------------------------------------------------------------------

Desc Label(std::string text);
Desc Label(State<std::string>* bound);
Desc Bold(std::string text);
Desc Button(std::string label, std::function<void()> on_click);
Desc Spacer();                                       // takes all the leftover space
Desc VStack(std::vector<Desc> children, float gap_units = 0.5f);
Desc HStack(std::vector<Desc> children, float gap_units = 0.5f);

// ---- the list: rows are demanded, never pushed -----------------------------------------------

enum class CellState { Ready, Pending, Unknown };
struct CellValue {
    CellState state = CellState::Pending;
    std::string text;
};

// The demand-driven source from docs/aurora-ui.md. Cell() must never block: a value that is not
// there yet is Pending, and one that cannot exist is Unknown, which renders as the word rather
// than as blank -- blank reads as "nothing made this", which is a different claim.
class Rows {
public:
    virtual ~Rows() {}
    virtual int Count() const = 0;
    virtual CellValue Cell(int row, int col) const = 0;
    // The widget says what it is looking at, plus a margin in the direction of travel. The source
    // fetches that and only that; this is also how navigate-away cancels, because a worker checks
    // the range before each unit of work and a range it has left is no longer wanted.
    virtual void Want(int first, int last) { (void)first; (void)last; }
    virtual int Columns() const = 0;
    virtual float ColumnWidth(int col) const = 0;
    virtual std::string ColumnTitle(int col) const = 0;
};

Desc ListView(Rows* rows, int* selected);

// ---- the window -------------------------------------------------------------------------------

class Window {
public:
    Window(const char* title, int lw, int lh);
    ~Window();

    bool ok() const { return win_ != nullptr; }
    void SetRoot(Desc root) { root_ = std::move(root); dirty_ = true; }
    Env& env() { return env_; }
    void Invalidate() { dirty_ = true; }
    void Quit() { quit_ = true; }

    // Runs until the window closes. frames > 0 stops after that many, for scripted runs.
    int Run(int frames = 0);

    // What the frame-timing instrument saw. The 8 ms rule is only a rule if it is measured.
    struct Timing { size_t frames = 0; double p50 = 0, p95 = 0, max = 0; size_t over_8ms = 0; };
    Timing timing() const;

    au_surface* surface() const { return sf_; }

private:
    void Layout();
    void Draw();
    static void Event(void* user, int kind, int a, int b);

    ::au_wl* wl_ = nullptr;
    ::au_wl_win* win_ = nullptr;
    au_surface* sf_ = nullptr;
    Desc root_;
    Env env_;
    int lw_ = 0, lh_ = 0;
    double scale_ = 1.0;
    bool dirty_ = true, quit_ = false, laid_ = false;
    float px_ = -1, py_ = -1;
    Node* hover_ = nullptr;
    Node* pressed_ = nullptr;
    std::vector<double> draw_ms_;
};

template <class T>
void State<T>::set(T n) {
    v_ = std::move(n);
    if (win_) win_->Invalidate();
}

}  // namespace ui
}  // namespace aurora

#endif
