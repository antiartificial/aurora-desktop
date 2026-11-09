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

// present.h, not a forward declaration. au_key is a typedef of an anonymous struct, so it
// cannot be forward-declared at all -- and present.h turns out to be safe to include anyway:
// it pulls in <stdint.h> and nothing else, declaring its Wayland types opaquely. The earlier
// forward declarations also had a subtler bug: written inside namespace aurora::ui they would
// have created aurora::ui::au_wl, a different type, and every call would have failed to convert.
#include "../render/present.h"

#include <atomic>
#include <chrono>
#include <functional>
#include <mutex>
#include <thread>
#include <type_traits>
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

// ---- text style -------------------------------------------------------------------------------

// Per-node text style. Size is a MULTIPLIER on the environment's size, not an absolute: a theme
// that changes the base size then scales the whole interface, which is what a person increasing
// their font size expects, rather than leaving every hand-set size behind.
// 8.6/P5: sizes are ratios. A design size is a multiplier on Env::size, so a bare 14 is a bug the
// type can refuse: Ratio has no implicit conversion from a number, and TextStyle{.size = 14} does
// not compile. Same rule as motion durations coming from the table rather than a call site.
struct Ratio {
    float v = 1.0f;
    constexpr Ratio() = default;
    explicit constexpr Ratio(float f) : v(f) {}
};

struct TextStyle {
    Ratio size{};                  // multiplier on Env::size
    bool bold = false;
    bool italic = false;
    bool has_color = false;
    au_color color{};              // only when has_color
};

// ---- motion ------------------------------------------------------------------------------------
//
// Durations and curves are VALUES carried in the environment, not constants at call sites. The
// reason is in docs/atlas-design.md section 7: motion is meant to be explored once the rest
// settles, and an exploration that means editing twelve call sites is a rewrite rather than a
// setting. So every duration in a window comes from one table, a curve is a function of a value,
// and turning motion down is one call -- MotionTable::resolve -- instead of a condition that
// every animating node repeats and one of them gets wrong.
//
// Two axes rather than one, because the axis is a property of the TRANSITION and not of the
// parent: a bar that slid down inside a row would be wrong, and a single Reveal used on the
// wrong axis is silently wrong rather than refused. I made exactly that mistake -- a height
// Reveal in an HStack reserved its full natural WIDTH while invisible, and the pane it was
// hiding swallowed the hit test.
enum class Motion : uint8_t { None, Reveal, RevealX, Fade };
enum class Curve : uint8_t { EaseOut, Linear, EaseInOut };

struct Transition {
    Motion motion = Motion::Reveal;
    uint32_t ms = 140;                 // matches the shell's chrome
    Curve curve = Curve::EaseOut;
};

// Eased progress of a normalised t, as a free function: a curve is then a value a caller passes,
// and a new curve is one case here rather than a new branch inside every animating node.
float curve_at(Curve c, float t);

// The named motions of the design and the two switches that turn them down. A node asks for the
// motion it MEANS (`env.motion.pane`), which is what makes a duration change one edit.
struct MotionTable {
    bool enabled = true;       // [motion] enabled = false snaps everything, the fade included
    bool reduced = false;      // [motion] reduced: sizes snap; opacity may still move

    Transition pane{Motion::RevealX, 160, Curve::EaseOut};   // sidebar and preview reveals
    Transition fade{Motion::Fade, 100, Curve::Linear};       // the type-ahead pill
    Transition tint{Motion::Fade, 200, Curve::EaseOut};      // a colour promotion (glass)

    // What a transition becomes here and now. `affordable` is the window's frame-budget verdict
    // (Window::motion_allowed), so degradation and preference resolve in ONE place and a node
    // cannot honour one and forget the other.
    Transition resolve(Transition t, bool affordable = true) const {
        if (!enabled || !affordable) { t.motion = Motion::None; return t; }
        if (reduced && t.motion != Motion::Fade) t.motion = Motion::None;
        return t;
    }
};

// ---- the environment: what flows down, with explicit override points -------------------------

struct Env {
    // The toolkit's base text size, and the one number a caller must NOT have to remember.
    //
    // Every size in a design is a multiplier on this (TextStyle::size), so the base decides the
    // whole interface. The plain system font reports 12 -- a size the shell abandoned for 14 --
    // and a window that adopted it put every size in docs/atlas-design.md one step low with
    // nothing on screen to say why. So the base is stated here, the system font supplies the
    // FACE and not the size, and a person who wants a larger interface sets AURORA_UI_SIZE.
    static constexpr float kBaseSize = 13;

    au_font font{};
    float size = kBaseSize;
    au_font bold{};
    au_font italic{};
    au_font bold_italic{};
    float unit = 8;                 // the spacing unit; padding(1) is one unit

    // ---- colour roles (docs/atlas-design.md section 3.6, the dark scheme) ----
    //
    // The first five keep their old names because every widget already draws with them:
    // fg = text, dim = text-2, surface = chrome, surface_hi = raised. The rest were hard-coded
    // inside the list, the grid and the tree -- three different near-blacks, none of them
    // nameable by a theme -- and are named here so a scheme is data and not a literal in a draw
    // routine. The theme reader that fills them from [theme.ui] is Phase 2.
    au_color fg{236, 239, 244, 255};            // text      #ECEFF4
    au_color dim{163, 170, 191, 255};           // text-2    #A3AABF
    au_color dim3{110, 117, 138, 255};          // text-3    #6E758A  disabled, placeholder, em dash
    au_color accent{122, 162, 247, 255};        // accent    #7AA2F7
    au_color accent_ink{24, 26, 34, 255};       // accent-ink #181A22 text on a focused selection
    au_color surface{38, 41, 54, 255};          // chrome    #262936
    au_color surface_hi{52, 56, 73, 255};       // raised    #343849
    au_color content{30, 32, 42, 255};          // content   #1E202A  the list and grid panes
    au_color content_alt{33, 35, 48, 255};      // content-alt #212330 zebra rows
    au_color sidebar{26, 28, 37, 255};          // sidebar   #1A1C25
    au_color field{22, 24, 32, 255};            // field     #161820
    au_color hover{39, 43, 56, 255};            // hover     #272B38  row and tile hover fill
    au_color hairline{62, 66, 86, 255};         // hairline  #3E4256
    au_color divider{44, 48, 64, 255};          // divider   #2C3040  under the column header
    // The selection fill when the pane does NOT have focus, and the focus ring. Both exist so
    // that selected-and-focused, selected-and-unfocused and cursor-only are three different
    // pictures rather than two: a state that renders like another state is a correctness bug in
    // this codebase, not a cosmetic one.
    au_color sel_unfocused{58, 65, 88, 255};    // selection-unfocused #3A4158
    au_color focus_ring{122, 162, 247, 255};    // accent, nameable apart from it
    au_color destructive{240, 138, 138, 255};   // destructive #F08A8A
    au_color warning{224, 175, 104, 255};       // warning   #E0AF68
    au_color success{158, 206, 106, 255};       // success   #9ECE6A

    // Every duration and curve in the window, in one place. A node animates with
    // `env.motion.resolve(env.motion.pane, allowed)` and never names a number.
    MotionTable motion;

    // The face and size a style asks for. One place resolves this, so every component that draws
    // text gets the same answer and a missing face falls back rather than drawing nothing.
    au_font face(const TextStyle& st) const {
        if (st.bold && st.italic) return bold_italic;
        if (st.bold) return bold;
        if (st.italic) return italic;
        return font;
    }
    float sized(const TextStyle& st) const { return size * st.size.v; }
    au_color ink(const TextStyle& st) const { return st.has_color ? st.color : fg; }
};

// The base size a Window starts at: Env::kBaseSize, or AURORA_UI_SIZE when a person asked for
// a larger interface. A FUNCTION, and public, so the rule can be checked without a display: the
// window's constructor needs a compositor, and a base size one step low is exactly the kind of
// mistake that only shows up in a running app and then only to someone who knows the design.
float default_base_size();

// ---- state: a cell, bound at build, that invalidates what it feeds ---------------------------

class Window;
class Node;      // State holds Node* to invalidate what reads it; the class comes below
class DerivedBase;                    // a derived value; defined after Node, used as a pointer before
template <class T> class Derived;

// Symmetric attachment. A state records the nodes that read it (so set() invalidates them), and a
// node records the states it bound to (so its destructor can detach itself). Without the second
// direction, SetRoot frees the old tree while cells still hold raw Node* and the next set() walks
// freed memory -- the heap-use-after-free ADR 0009 records.
// Something a Derived can be computed from: a State or another Derived. Dependents are named at
// construction, so the graph is fixed after the tree is built and needs no tracker or scheduler.
class SourceBase {
public:
    virtual ~SourceBase() = default;
    void addDependent(DerivedBase* d) { dependents_.push_back(d); }
protected:
    std::vector<DerivedBase*> dependents_;
};

class StateBase : public SourceBase {
public:
    virtual ~StateBase() = default;
    virtual void detach(Node* n) = 0;
};

// The commit protocol (markDirty/readers) is internal to the window: a widget never calls it, and a
// derived never registers itself with a reader -- Binding::attach does that.
class DerivedBase : public StateBase {
public:
    void addReader(Node* n) { readers_.push_back(n); }
    void removeReader(Node* n);
    void detach(Node* n) override { removeReader(n); }   // ~Node undoes a derived reader too (ADR 0009)
    // A source changed: this is stale, its readers repaint, and deriveds that read it follow.
    void markDirty(Window* w);
protected:
    std::vector<Node*> readers_;
    mutable bool dirty_ = true;
};

// Set() invalidates exactly the nodes that read this cell: no diffing and no tree walk, because
// the binding was made when the tree was built. A node constructed from a State attaches itself,
// which is what makes "bound at build time" true rather than aspirational -- without it set()
// can only invalidate the whole window, which it did at first and which measured every frame as
// a full repaint.
//
// Lifetime: a State must outlive the nodes built from it, OR the node must detach before the
// State dies (StateBase::detach from ~Node). Both directions are recorded here.
template <class T>
class State : public StateBase {
public:
    explicit State(T v) : v_(std::move(v)) {}
    const T& get() const { return v_; }
    void set(T n);
    void bind(Window* w) { win_ = w; }
    Window* window() const { return win_; }
    ~State() override;               // removes this from each bound node's cell list
    void attach(Node* n);            // defined below, after Node is complete
    void detach(Node* n) override;   // erase n from nodes_
    size_t bound_count() const { return nodes_.size(); }
    // A liveness flag a Binding shares. ~State flips it, so a Binding that outlives its cell
    // reads its own constant instead of dereferencing freed memory (ADR 0009's open question).
    std::shared_ptr<std::atomic<bool>> aliveToken() const { return alive_; }

private:
    T v_;
    Window* win_ = nullptr;
    std::vector<Node*> nodes_;
    std::shared_ptr<std::atomic<bool>> alive_{std::make_shared<std::atomic<bool>>(true)};
    void invalidate_bound();         // invalidate the nodes that read this cell (loop thread only)
    void notify_dependents();        // mark derived values computed from this cell dirty
};

// Binding<T> — the one way a widget takes state: no overload set to get wrong. Reads through a
// live State<T> (writable) or a constant literal (read-only). It converts implicitly from
// State<T>& and from a non-string literal; a State<T>* is NOT a State<T>&, so migrating a pointer
// call site stays a deliberate source edit. A string literal will NOT reach a Binding (const char*
// -> std::string -> Binding is two user-defined conversions), which is intended: static text keeps
// the plain Label(std::string) overload, live text takes the Binding.
template <class T>
class Binding {
public:
    Binding() : lit_() {}                                      // a default constant (false/empty)
    Binding(State<T>& s) : s_(&s), alive_(s.aliveToken()) {}   // live, writable
    Binding(Derived<T>& d) : d_(&d) {}                         // a derived: read-only
    explicit Binding(T v) : lit_(std::move(v)) {}             // a read-only constant (explicit: no pointer/literal slips in)

    // The one place that decides what an unbound/dead binding reads as, so a widget cannot forget
    // the guard: a derived or a live cell -> its value, otherwise the caller's fallback.
    T get_or(T fallback) const { return d_ ? d_->get() : (cell() ? s_->get() : std::move(fallback)); }
    const T& get() const { return d_ ? d_->get() : (cell() ? s_->get() : lit_); }
    void set(T v) const {
        if (d_) return;                                        // a derived is read-only
        if (cell()) s_->set(std::move(v));
        else lit_ = std::move(v);                              // a constant writes its own copy
    }
    bool live() const { return d_ != nullptr || cell(); }      // a derived, or a live cell
    bool writable() const { return !d_ && cell(); }
    bool alive() const { return d_ != nullptr || !s_ || alive_->load(std::memory_order_acquire); }
    // A node built from a live binding attaches to the cell so set() invalidates it; a derived gets
    // it as a reader (so a source change repaints through the derived); a constant has neither.
    void attach(Node* n) const { if (d_) { d_->addReader(n); n->addCell(d_); } else if (cell()) s_->attach(n); }

private:
    bool cell() const { return s_ && alive_->load(std::memory_order_acquire); }
    State<T>* s_ = nullptr;
    Derived<T>* d_ = nullptr;
    std::shared_ptr<std::atomic<bool>> alive_;
    mutable T lit_{};
};

// ---- derived values ---------------------------------------------------------------------------
//
// A cell computed from cells declared before it (docs 04-runtime §1). Declaration order is
// topological order, so one pass in construction order is enough: no graph sort, no cycle
// detection, no scheduler, and no ambient observer -- which would have to be correct across the
// set()-from-any-thread boundary 8.2b opened. Read-only, and it reads as a Binding<T>, so it can
// feed Either, Reveal, a split ratio or a label without the widget knowing the difference.
template <class T>
class Derived : public DerivedBase {
public:
    // Sources must be declared before this (they are captured by reference and outlived by it).
    template <class F, class... S>
    Derived(F&& f, S&... sources)
        : f_([fn = std::forward<F>(f), &sources...]() { return fn(sources.get()...); }) {
        (sources.addDependent(this), ...);   // each source marks this dirty when it changes
    }
    const T& get() const {                    // window thread; recomputes once if stale
        if (dirty_) { value_ = f_(); dirty_ = false; }
        return value_;
    }
    Binding<T> as_binding() const { return Binding<T>(const_cast<Derived<T>&>(*this)); }

private:
    std::function<T()> f_;
    mutable T value_{};
};

// ---- drag and drop ----------------------------------------------------------------------------

// What a drag carries. One MIME type and its bytes, which is all a file manager needs: a drag of
// files is `text/uri-list`, lines of file:// URIs, and that is the same thing the host hands the
// compositor (decision 0020). Richer payloads are the compositor's story, not a widget's.
//
// `paths` is a convenience over the same data -- the URIs already decoded -- so a drop target
// does not re-parse what the source just encoded. It is advisory: `mime` and `bytes` are what
// would cross a process boundary, and what a target must be willing to read.
struct DragData {
    std::string mime;
    std::string bytes;
    std::vector<std::string> paths;

    static DragData uri_list(const std::vector<std::string>& files);   // builds mime/bytes/paths
    static DragData parse_uri_list(const std::string& bytes);          // the other direction
};

// ---- nodes -----------------------------------------------------------------------------------

class Node;
class Desc;   // a handle to a node; defined after Node so its methods can see the fields

class Node {
public:
    virtual ~Node();   // detaches from every cell it bound to, so a swapped-out tree cannot dangle

    // A cell records this node (State::attach); the node records the cell here so the destructor
    // can undo it. addCell is the reverse pointer that makes attachment symmetric (ADR 0009).
    void addCell(StateBase* c) { cells_.push_back(c); }
    // The reverse of addCell: a State being destroyed forgets itself in every node it bound to, so
    // a later ~Node has no dangling cell to call. Symmetric in BOTH directions, so neither order
    // ("node outlives state" or "state outlives node") can dereference a dead object.
    void removeCell(StateBase* c) {
        for (auto it = cells_.begin(); it != cells_.end();) {
            if (*it == c) it = cells_.erase(it);
            else ++it;
        }
    }

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

    // The pointer MOVED inside this node, which for a list or a grid is a different row under
    // it and a different two rectangles to repaint. Return true when something about the way
    // this node draws has changed; the window then takes DamageHint, so a hover down a list
    // costs two rows and not the pane. Returning false means "nothing changed", and the window
    // damages NOTHING -- which is why the default is false: a node that does not track the
    // pointer must not make every pointer move a repaint of itself.
    virtual bool OnHoverAt(float x, float y) { (void)x; (void)y; return false; }
    virtual void OnPress() {}
    virtual void OnRelease(bool inside) { (void)inside; }

    // Pointer with coordinates, and the click count for double/triple. A node that only cares
    // that it was pressed overrides OnPress; one that cares WHERE overrides these. Drag arrives
    // between a press and its release, which is what selection, rubber-banding and a column
    // resize all need.
    virtual void OnPressAt(float x, float y, int clicks) { (void)x; (void)y; (void)clicks; OnPress(); }
    // The secondary button. Returning true means handled; the window does not synthesise a
    // primary press from it, because a right-click must not move a selection it is acting on.
    virtual bool OnContext(float x, float y) { (void)x; (void)y; return false; }
    virtual void OnDragTo(float x, float y) { (void)x; (void)y; }

    // A node that animates asks for a tick. 0 means "never wake me". The window uses the
    // smallest interval any node asks for, so an idle window still sleeps.
    virtual int TickMs() const { return 0; }
    virtual void OnTick() {}

    // Transitions, as virtuals so the window never needs to know a concrete animating type.
    // Retarget is called each frame with whether motion is affordable; a node that animates
    // its own geometry answers true from Animating while it moves, which tells the window to
    // lay out again, because the node's size is part of the layout.
    // Text style, for any node that draws text. The default ignores it, so a container does not
    // have to care; a node that draws text overrides it.
    virtual void SetStyle(const TextStyle& st) { (void)st; }

    // Called once per layout with the owning window, for a node that needs to bind a State of
    // its own (the list's rename field does). Generic so the window never special-cases a type.
    virtual void Attached(Window* w) { (void)w; }

    // Insert text at the caret, for a paste. Separate from OnKey because a paste cannot happen
    // inside a key handler: see Window::RequestPaste.
    virtual void InsertText(const std::string& t) { (void)t; }

    virtual void Retarget(bool motion_allowed) { (void)motion_allowed; }
    virtual bool Animating() const { return false; }
    virtual bool Wants(float* out_weight) { (void)out_weight; return false; }

    // ---- drag and drop ----
    //
    // A drag is a press that MOVED. The window asks the pressed node for a payload once the
    // pointer passes a threshold, and a node that has nothing to drag says so by returning
    // false, which leaves the press meaning whatever it meant before -- a selection, a scroll.
    // That ordering is the whole reason a threshold exists: without one, every click on a file
    // name would begin a drag and nothing could be selected.
    virtual bool OnDragStart(float x, float y, DragData* out) {
        (void)x; (void)y; (void)out;
        return false;
    }

    // A target is asked whether it would take this payload BEFORE the drop, so it can say so by
    // drawing. `OnDropHover(.., false)` means the drag left; a target that highlighted itself
    // clears it there, and the window guarantees that call.
    virtual bool CanAcceptDrop(const DragData& d) const {
        (void)d;
        return false;
    }
    virtual void OnDropHover(float x, float y, bool inside) { (void)x; (void)y; (void)inside; }
    virtual bool OnDrop(float x, float y, const DragData& d) {
        (void)x; (void)y; (void)d;
        return false;
    }

    // Wheel and keys return true when handled. Unhandled events bubble to `parent`, which is
    // what replaced routing them to a global pointer to the one list a window happened to have.
    virtual bool OnWheel(float dy) { (void)dy; return false; }
    virtual bool OnKey(const au_key& k) { (void)k; return false; }

    // Focus. A node that takes keys says so; the window collects these in tree order for Tab.
    virtual bool CanFocus() const { return false; }
    virtual void OnFocus(bool gained) { (void)gained; }

    // How the window walks the tree, for focus order and for nothing else. A container appends
    // its children in the order they are laid out.
    virtual void Children(std::vector<Node*>* out) { (void)out; }

    // After handling an event, a node may say that less than its whole frame changed. Without
    // this the window damages the handler's frame, which for a list that fills the window is a
    // full repaint for a selection moving one row -- measured at 0.906 of the window before this
    // existed, against 0.03 for a hover. Return false to mean "my whole frame".
    virtual bool DamageHint(std::vector<Rect>* out) { (void)out; return false; }

    Node* parent = nullptr;

    Rect frame;
    Insets pad;
    float weight = 0;              // > 0 takes a share of the leftover main-axis space
    std::string name;              // what the accessibility tree and tests find it by
    std::vector<StateBase*> cells_; // states this node bound to; detached in ~Node

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

// One node, cheap to copy: a handle, not the node. A modifier mutates the node and returns the SAME
// handle (Decision 4 -- shared_ptr semantics), so a chain sets fields on one node rather than
// rebuilding it, which is why "modifiers return a new value" is not the contract.
class Desc {
public:
    Desc() = default;
    Desc(std::nullptr_t) {}
    template <class T, class = std::enable_if_t<std::is_base_of_v<Node, T>>>
    Desc(std::shared_ptr<T> n) : n_(std::move(n)) {}

    Node* get() const { return n_.get(); }
    Node* operator->() const { return n_.get(); }
    explicit operator bool() const { return n_ != nullptr; }
    bool operator==(std::nullptr_t) const { return n_ == nullptr; }
    bool operator!=(std::nullptr_t) const { return n_ != nullptr; }
    bool operator==(const Desc& o) const { return n_ == o.n_; }
    bool operator!=(const Desc& o) const { return n_ != o.n_; }
    void reset() { n_.reset(); }

    Desc padding(float units) const;
    Desc padding(Insets i) const;
    Desc weight(float w) const;
    Desc name(std::string n) const;
    Desc style(TextStyle st) const;

private:
    std::shared_ptr<Node> n_;
};

// 8.6: the naming rule, checked. A name is how the accessibility tree, the preview and every test
// find a node, so two nodes sharing one make every lookup ambiguous. Walks the tree in layout order
// and returns each name that occurs more than once (empty for a clean tree).
std::vector<std::string> DuplicateNames(Desc root);

// ---- modifiers: each takes a Desc and returns it, so they chain -------------------------------

Desc padding(Desc d, float units);
Desc padding(Desc d, Insets i);
Desc weight(Desc d, float w);
Desc named(Desc d, std::string n);

// Text style, on any node that draws text. Chains like the others:
//   styled(Label("Atlas"), TextStyle{Ratio(1.4f), true})          -- 40% larger and bold
//   styled(Label(&note), TextStyle{Ratio(0.85f), false, true})    -- smaller and italic
Desc styled(Desc d, TextStyle st);

// ---- primitives -------------------------------------------------------------------------------

Desc Label(std::string text);
Desc Label(Binding<std::string> bound);
Desc Bold(std::string text);
Desc Button(std::string label, std::function<void()> on_click);
// A button that can be switched off by a State. Off, it draws dim, ignores a click and the
// activation keys, and stays in the tree: a disabled action is shown with its absence, not hidden.
Desc Button(std::string label, std::function<void()> on_click, Binding<bool> enabled);
Desc Spacer();                                       // takes all the leftover space

// ---- glyph buttons ------------------------------------------------------------------------------
//
// A Button draws a STRING. A toolbar of words is the thing the design rejects first, and a glyph
// is a drawing, so it cannot be a label: these are the glyphs the toolbar of docs/atlas-design.md
// 6.9 asks for, drawn from canvas primitives at the size they are placed at.
enum class Glyph : uint8_t {
    Back, Forward, Up, Sidebar, Preview, View, Filter, Search, Plus, Minus, More, Refresh, Close
};

// 28x28 by default (the toolbar's button), hover and press and focus like a Button, and the
// same `enabled` cell -- a disabled button STAYS VISIBLE with its glyph in the tertiary ink,
// because hiding what is unavailable teaches the wrong model.
Desc IconButton(Glyph g, std::function<void()> on_click, Binding<bool> enabled = Binding<bool>(true));

// MenuButton is declared with the menus, below: it needs MenuItem.

// ---- the small controls -------------------------------------------------------------------------

// A checkbox and a switch are ONE widget with two drawings. The difference is a visual
// convention and not a behaviour: a checkbox reads as "this option is on", a switch as "this
// thing is running". Both toggle on Space -- not Enter, which every toolkit reserves for
// "accept the dialog", and a checkbox that swallowed it would make a form unsubmittable from
// the keyboard.
Desc Checkbox(std::string label, Binding<bool> on, std::function<void(bool)> changed = nullptr);
Desc Switch(std::string label, Binding<bool> on, std::function<void(bool)> changed = nullptr);

// A slider over [lo, hi]. It takes the width it is given, because a slider's width IS its
// resolution: a narrow one cannot express the values it claims to. Arrows move a twentieth of
// the range and Page a fifth -- stepping by one unit would make a 0-to-1 slider take a hundred
// presses to cross.
Desc Slider(Binding<float> value, float lo, float hi, std::function<void(float)> changed = nullptr);

// A progress bar, 0 to 1. Not focusable and not clickable: it reports, it is not a control, and
// a Tab stop that does nothing is a dead stop in a keyboard user's path.
Desc Progress(Binding<float> fraction);

// Exclusive choice. A stack of checkboxes that agree to be exclusive rather than a new widget --
// exclusivity is the only thing it adds.
Desc RadioGroup(std::vector<std::string> labels, Binding<int> chosen,
                std::function<void(int)> changed = nullptr, bool horizontal = false);
Desc VStack(std::vector<Desc> children, float gap_units = 0.5f);
Desc HStack(std::vector<Desc> children, float gap_units = 0.5f);

// ---- the list: rows are demanded, never pushed -----------------------------------------------

class Rows;

// Where a cell's text sits in its column. Numbers are right-aligned because the text stack has
// no tabular figures: a right edge is what lets a column of sizes be read down.
enum class Align : uint8_t { Left, Right, Center };

// One column, as the source declares it. Widths are layout units.
//
// `flex` > 0 takes a share of the leftover width; `min` is a floor that the slack-sharing and
// the shrinking both respect; `visible_from` is the list width at which the column appears at
// all, which is how a narrow window shows Name and Size and a wide one adds Kind without
// anything being clipped at either size.
struct ColumnSpec {
    std::string title;
    float width = 120;             // the natural width, and the width when nothing flexes
    float min = 0;                 // a floor, not a hint
    float flex = 0;                // > 0 absorbs the slack, shared in proportion
    Align align = Align::Left;
    float visible_from = 0;        // show this column only when the list is at least this wide
};

// The resolved width of every column for a list this wide, in column order; 0 means the column
// is NOT shown. This is the rule itself, not a convenience: a reviewer and a test can ask what
// the layout will be without a window, and the invariant worth asserting is that the shown
// columns sum to at most `avail` and that no shown column is narrower than its min.
std::vector<float> SolveColumns(const Rows* rows, float avail);

// ---- icons -------------------------------------------------------------------------------------
//
// A kind, a mark and a badge, which is the information a listing actually carries: what sort of
// thing this is, which particular one of that sort it is, and what is true about this instance
// (a link, a broken link). The three are separate because they are cached separately: every
// icon is rasterised once per (kind, mark, badge, variant, size, scale) and blitted thereafter,
// so a badge is part of the cached image and a dimmed icon is a second cached variant rather
// than a per-frame blend.
enum class IconKind : uint8_t {
    Folder, Document, Code, Image, Audio, Video, Archive, Executable, Generic
};
// A mark drawn inside the kind's body. The special folders of docs/atlas-design.md section 4.3.
enum class IconMark : uint8_t { None, Home, Desktop, Documents, Downloads, Pictures, Music, Videos, Trash };
enum class IconBadge : uint8_t { None, Link, Broken };

// Where the picture comes from.
//
// Procedural is the family drawn from canvas primitives, and it is the default for the reason
// docs/aurora-ui.md gives: an explorer must not link an image decoder, because decoding an
// untrusted file is a verb that runs in a domain. Vector and Bitmap exist because an icon theme
// and an app's own icon are real things a desktop has, and because a thumbnail from preview@v1
// arrives as pixels.
//
// Vector reads a SUBSET of SVG: `path` (M, L, H, V, C, S, Q, T, Z, absolute and relative),
// `rect`, `circle`, `ellipse`, `polygon`, `polyline`, a `fill` of #rgb / #rrggbb / `none` /
// `currentColor`, `fill-opacity`, and `viewBox`. No transforms, no strokes, no gradients, no
// text, no CSS. Anything outside that subset makes the whole file unreadable rather than
// half-drawn: IconVectorOk() says so, and an icon that cannot be read falls back to its kind's
// procedural glyph, which is a wrong icon rather than a broken window.
//
// Bitmap takes PIXELS, not an encoded file: straight-alpha ARGB (0xAARRGGBB) that the caller
// owns, or a PPM/PAM file, which needs no decoder. There is deliberately no PNG path here; a
// PNG is decoded by whatever produced it (preview@v1, an icon service) and handed over as
// pixels, which keeps the decoder out of this process.
enum class IconFormat : uint8_t { Procedural, Vector, Bitmap };

struct IconSpec {
    IconKind kind = IconKind::Generic;
    IconMark mark = IconMark::None;
    IconBadge badge = IconBadge::None;
    bool dimmed = false;                 // a hidden entry: a cached variant, never a blend
    IconFormat format = IconFormat::Procedural;

    std::string path;                    // Vector: an .svg file; Bitmap: a .ppm / .pam file
    // An in-memory bitmap. The caller owns the pixels and must keep them alive until the first
    // draw; after that the rasterisation is cached and the source is not read again. `id` is
    // what identifies it in the cache -- two different bitmaps with the same id are one icon --
    // so it has to be stable and unique per image (a pointer value will not do: a freed buffer
    // and a new one can share an address).
    const uint32_t* argb = nullptr;
    int argb_w = 0, argb_h = 0;
    uint64_t id = 0;
};

// The default classification, from the name and directory-ness: the table of
// docs/atlas-design.md section 4.1. It is a free function so an app can call it, adjust the
// result and hand back something closer to the truth than a name can be.
IconSpec IconFromName(const std::string& name, bool dir);

// Whether a vector icon can be read at all, without drawing it. For a test, and for an app that
// would rather know than find out one frame at a time.
bool IconVectorOk(const std::string& svg_path);

// How many icons have been rasterised since the process started. The cost rule of section 4.5
// is that a scroll rasterises nothing: this is the number that proves it, rather than a trace
// line a reviewer has to read by eye.
size_t IconsRasterised();

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
    // Still here, and still the only two a simple source has to write. They are the INPUT to
    // Column()'s default below rather than what the widget draws from: a source that states a
    // width and a title keeps the layout it had, and a source that wants the rest overrides
    // Column(). Migrating the other way -- deleting these now -- would mean editing the
    // chooser's shared DirRows, which is a different stage's file.
    virtual float ColumnWidth(int col) const = 0;
    virtual std::string ColumnTitle(int col) const = 0;

    // What the widget actually lays out from. The important field is `flex`: the Name column
    // absorbs the slack, which is what makes "the Modified column cut off mid-value" -- the
    // loudest defect in Atlas today -- impossible by construction. The rule the list applies
    // (SolveColumns, below) is that a column either fits WHOLE or is dropped; a fixed column
    // never gives way to the flexible one, and the flexible one never shrinks past `min`.
    //
    // The default derives a fixed column from the two legacy virtuals, which is the layout
    // every existing source already has.
    virtual ColumnSpec Column(int col) const {
        ColumnSpec s;
        s.title = ColumnTitle(col);
        s.width = ColumnWidth(col);
        s.min = s.width;             // a stated width and no floor is a fixed column
        return s;
    }

    // What a row IS, for the icon at the head of its first cell: a kind, a mark inside it, a
    // badge over it, and a dimmed variant for a hidden entry. The default classifies by name,
    // with the trailing '/' that meant "directory" before this existed kept as its input, so no
    // existing Rows has to grow a field to go on working.
    //
    // A source may also hand over a VECTOR or a BITMAP icon instead of the procedural family
    // (IconSpec::format): an icon theme, an app's own icon, a thumbnail from a verb. Either is
    // rasterised once per (source, size, scale, variant) and blitted after that, which is the
    // same bargain the procedural icons already keep -- see IconSpec.
    virtual IconSpec Icon(int row) const;

    // ---- sorting ----
    //
    // The source sorts, not the widget. The widget has cells of text and no idea that one column
    // is a size in bytes displayed as "4.0 KB" and another is a time; sorting those as the
    // strings it was handed is how a file manager ends up putting 10 B after 9 KB.
    //
    // SortColumn() is -1 when nothing is sorted. The widget draws the indicator from these, so
    // a source that ignores Sort() simply never shows one, and the header stops looking
    // clickable -- which is correct, because it is not.
    virtual int SortColumn() const { return -1; }
    virtual bool SortAscending() const { return true; }
    virtual void Sort(int col, bool ascending) { (void)col; (void)ascending; }
};

Desc ListView(Rows* rows, Binding<int> selected);

// ---- icon view --------------------------------------------------------------------------------

// A grid of tiles over the SAME Rows source as the list, so switching view fetches nothing it
// already has and the demand-driven lstat keeps working unchanged.
//
// Icons are drawn procedurally from canvas primitives -- a folder shape, a page with a folded
// corner, tinted by file class. That is deliberate, not a placeholder for want of an icon
// system: an explorer must not link an image decoder (docs/aurora-ui.md section 2 -- decoding an
// untrusted file is a verb that runs in a domain), and a grid of identical generic glyphs would
// be worse than a list. A procedural icon per class carries real information at zero risk.
// HVIF, when it lands, replaces the drawing and not the widget.
Desc IconView(Rows* rows, Binding<int> selected);
void OnIconsActivate(const Desc& icons, std::function<void(int)> f);
void OnIconsContext(const Desc& icons, std::function<void(int, float, float)> f);
// A right-click on a row calls this with (row, x, y) in window coordinates, so the app can open
// a menu there. Set it on the Desc a ListView returned.
void OnListContext(const Desc& list, std::function<void(int, float, float)> f);
// Activation: Enter, or a double-click on a row. One callback for both, because they mean the
// same thing and a user who learns one expects the other.
void OnListActivate(const Desc& list, std::function<void(int)> f);

// Inline rename. F2 (or a call to BeginListRename) puts an editable field over the row's first
// cell; Enter commits and calls `f`, Escape cancels. Returning false from `f` keeps the field
// open, which is what a name collision should do -- silently discarding what someone typed is
// worse than making them fix it.
//
// The list owns the field rather than the app placing one, because the list is the only thing
// that knows where a row is and the tree is built once: a field that appears over a row is
// structural change, and structural change is a widget's job.
// Selection changed, by any means: click, arrow, type-ahead. A preview pane wants this and a
// status line wants this, and neither should have to poll the int it was handed.
void OnListSelect(const Desc& list, std::function<void(int)> f);
void OnIconsSelect(const Desc& icons, std::function<void(int)> f);

// The grid selects the same way the list does: Ctrl-click, Shift-click, Shift+arrows, Ctrl+A,
// click-empty-to-clear, and a drag of a selected tile carries the whole selection. A grid you
// cannot multi-select in is one you have to leave to do anything with more than one file.
//
// Shift in a GRID takes a range by index, not a rectangle: shift-clicking the tile below picks
// up the row between, which is what reading order means and what every icon view does.
void OnIconsSelectionChanged(const Desc& icons, std::function<void(const std::vector<int>&)> f);
std::vector<int> IconsSelection(const Desc& icons);
void OnIconsDragPaths(const Desc& icons, std::function<std::vector<std::string>(int)> f);

// ---- selecting more than one row ---------------------------------------------------------------

// Ctrl-click adds or removes a row; Shift-click sweeps a range from the anchor; Shift+arrows do
// the same from the keyboard; Ctrl+A takes everything; a click on empty space below the rows
// clears it, the way every file manager does.
//
// `selected` -- the int a ListView was built with -- keeps its old meaning: it is the CURSOR,
// the row the keyboard is on and the one a preview pane follows. The set is what is highlighted.
// They are the same single row almost always, and they come apart exactly where a file manager
// needs them to. Keeping both means every existing caller still works and a list that never
// multi-selects pays nothing.
void OnListSelectionChanged(const Desc& list, std::function<void(const std::vector<int>&)> f);
std::vector<int> ListSelection(const Desc& list);
// Make a list refuse multi-selection: Ctrl-click, Shift-click, Shift+arrows and Ctrl+A all behave
// as the plain gesture, so highlighted rows and the cursor are always the same single row. For a
// picker, where what is highlighted is exactly what will be granted.
// A file-type icon at the head of each row's first column, classified from the name's
// extension exactly as the icon grid does, with a trailing '/' meaning a directory. Opt-in,
// because `Rows` is generic and a list of things that are not files should not get file icons.
// With it on, the first column drops the trailing slash: the icon already says "folder", and
// saying it twice is how a list starts to look like a terminal.
void ListIcons(const Desc& list, bool on = true);

void ListSingleSelect(const Desc& list);

// Put the cursor on a row WITHOUT selecting it, which is a state the design asks for twice:
// after a navigation ("the cursor is the first row and nothing is selected") and after a
// delete. The cursor is drawn as a focus ring rather than a fill, so it cannot be mistaken for
// a selection -- which is the whole reason the two are separate here.
void ListSetCursor(const Desc& list, int row);

// The list's row and header heights, as it resolved them from the environment. An app placing
// anything over a row needs these (a rename field, a drag ghost, a tooltip), and so does a test
// that wants to point at the third row rather than at a y it guessed; they are the list's
// observable geometry, the same way TreeRowCount is the tree's observable state.
float ListRowHeight(const Desc& list);
float ListHeaderHeight(const Desc& list);

void OnListRename(const Desc& list, std::function<bool(int, const std::string&)> f);

// ---- dragging out of, and into, a list --------------------------------------------------------

// What a row IS, as paths. Set this and the list becomes a drag source; leave it unset and it is
// not one. That is the right default: a list of search results or of mail should not start
// handing out file URIs because it happens to be a list.
void OnListDragPaths(const Desc& list, std::function<std::vector<std::string>(int)> f);

// A drop landed. `row` is the row it landed on, or -1 for the list's own container -- dropping
// below the last row means "in here", which is what every file manager means by it. Return true
// if the drop was taken.
void OnListDrop(const Desc& list, std::function<bool(int, const DragData&)> f);

// The same for the sidebar, keyed by tree id. Dragging into a folder you can see but are not
// inside is most of what dragging is for.
void OnTreeDrop(const Desc& tree, std::function<bool(const std::string&, const DragData&)> f);
void BeginListRename(const Desc& list, int row);

// ---- multiline text ---------------------------------------------------------------------------

// Read-only multiline text: wraps, scrolls, and draws only the visible lines.
//
// This is the display half of a text area, and it exists first because a preview pane needs
// exactly this and nothing more. Editing multiline text -- a caret as line and column, selection
// across lines, up and down moving by VISUAL line rather than logical -- is the other half and
// is genuinely harder; building the display first means the preview is not waiting on it.
//
// Lines are split and wrapped once per (text, width) and cached, because wrapping measures text
// and a scroll must not re-measure the document.
Desc TextView(Binding<std::string> text, bool wrap = true);

// ---- breadcrumb -------------------------------------------------------------------------------

// Path segments, clickable, each navigating to that prefix.
//
// It is a WIDGET that draws segments, not a stack of Buttons, and that is the architecture
// showing its shape rather than a shortcut: the tree is built once and there is no reconciler,
// so a container whose child count changes with the path could not be expressed as children.
// Structural change is a widget's job -- the same reason the list owns its own rows.
//
// Clicking past the last segment calls on_edit, which is where a path field belongs (Cmd+L).
Desc Breadcrumb(Binding<std::string> path,
                std::function<void(const std::string&)> on_pick,
                std::function<void()> on_edit = nullptr);

// ---- text input -------------------------------------------------------------------------------

// An editable single-line field: caret, selection, and the editing keys a person expects.
//
// `on_change` fires on every edit, which is what a filter bar wants (narrow as you type);
// `on_commit` fires on Enter and `on_cancel` on Escape. The State is the text, so the app reads
// it without asking the widget anything.
//
// Ctrl+C, Ctrl+X and Ctrl+V work on plain UTF-8 text.
//
// What this does NOT do, stated rather than discovered later: there is no input-method editor.
// Composed text -- the preedit a CJK or dead-key input method shows before committing -- needs
// text_input_v3, which the compositor implements for its own clients but present_wl does not
// speak. So this handles what a keyboard commits, including anything the layout produces with
// shift, caps and AltGr, and cannot yet handle composition. Pasting is also absent: the
// clipboard carries only plain text here; a rich flavour is the compositor's story
// (ext_data_control, and decision 0019's mediated access), not a text field's.
struct FieldOpts {
    std::string placeholder;
    bool read_only = false;
    size_t max_len = 0;              // 0 = unlimited
    bool select_all_on_focus = false;  // what a path field wants, so typing replaces
};

Desc TextField(Binding<std::string> text,
               std::function<void(const std::string&)> on_change = nullptr,
               std::function<void(const std::string&)> on_commit = nullptr,
               std::function<void()> on_cancel = nullptr,
               FieldOpts opts = FieldOpts{});

// ---- transitions ------------------------------------------------------------------------------

// A transition is a property of a state change, not an animation someone writes. See
// docs/aurora-ui.md section 1 for the four rules; the two this type enforces are that the curve
// is ease-out (there is no ease-in: it reads as sluggish for anything the user initiated) and
// that motion is something the system opts INTO when it can afford it.
// Reveal: height 0 <-> natural, for a bar. RevealX: width, for a side pane.
//
// Two of them rather than one axis-agnostic Reveal because the axis is a property of the
// TRANSITION, not of the parent: a bar that slides down inside a row would be wrong, and a
// single Reveal used on the wrong axis is silently wrong rather than refused. I made exactly
// that mistake -- a height Reveal in an HStack reserved its full natural WIDTH while invisible,
// and the pane it was hiding swallowed the hit test.
// Motion, Curve, Transition and MotionTable are declared above Env, because Env carries the
// table: see "---- motion ----" there for why durations are values.

// Eased progress, and the one rule that makes a reversal not stutter: it resumes from where it
// got to rather than from the start.
class Ease {
public:
    void target(float t, uint32_t ms);
    // The same, taking the transition it is running: duration and curve together, so a node
    // that was handed a Transition never has to take it apart.
    void target(float t, Transition tr);
    float value() const { return cur_; }
    bool running() const { return running_; }
    // Advance to now. Returns true while still moving.
    bool tick();
    void snap() { cur_ = want_; running_ = false; }

private:
    float cur_ = 0, from_ = 0, want_ = 0;
    uint32_t ms_ = 140;
    Curve curve_ = Curve::EaseOut;
    bool running_ = false;
    std::chrono::steady_clock::time_point t0_{};
};

// A container that reveals its child: height animates between zero and the child's natural
// height. The child is NOT measured differently -- it keeps its own layout -- so revealing does
// not reflow the child's contents, which is what makes a reveal cheap and stable.
//
// The target state is live immediately: `shown` flips, hit testing follows it the same frame,
// and only the pixels catch up. A transition that gated input would be a latency bug in costume.
Desc Reveal(Binding<bool> shown, Desc child, Transition t = Transition{});

// Exactly one of two children, chosen by a State. Both are built once and kept; the one not
// shown is not measured, not drawn, not hit-tested and not in the focus order.
//
// This is how a view switch works when there is no reconciler: you do not rebuild the tree, you
// hold both and pick. It costs the memory of two widgets and buys instant switching with each
// view keeping its own scroll position and selection -- which is what a person expects when they
// toggle back.
Desc Either(Binding<bool> first, Desc a, Desc b);

// A fixed size on one axis, or both; -1 keeps the child's natural size on that axis.
//
//   sized(previewPane, 260)        -- 260 units wide, natural height
//
// Needed, not merely convenient. A stack gives an unweighted child its NATURAL size, and a
// widget whose width depends on what it is given -- TextView, because a wrapped document has no
// width of its own -- answers with the whole offer. Beside a weighted sibling that starves the
// sibling to zero. A pane with an intended width has to be able to state it.
Desc sized(Desc child, float w, float h = -1);

// ---- tree -------------------------------------------------------------------------------------

// One node of a tree. `id` is whatever the source knows it by -- for a file tree, the path -- and
// the widget treats it as opaque. Expansion is remembered BY ID and not by position, so a source
// that re-reads a directory and returns its children in a different order does not shuffle what
// the user had open, and a row that disappears takes only its own expansion with it.
struct TreeItem {
    std::string id;
    std::string label;
    bool expandable = false;

    // A non-selectable heading. It takes a row, draws in the caption size and the secondary
    // ink, and the keyboard steps over it: Up and Down skip it, a click on it does nothing,
    // and it can never be the selection. One widget then draws a sidebar that has both
    // "Places" and "Filesystem" in it, which is what the design asks for and what a second
    // widget stacked above the tree could not do (it would scroll separately).
    bool section = false;

    // A leaf that is a destination, not a branch: no chevron, no children asked for, no
    // expansion remembered. Places are flat; the Filesystem tree is not.
    bool flat = false;

    // A row that is shown but cannot be actioned (an app with no window, or one whose launch is
    // blocked). It draws dim, takes no hover fill, and neither click nor Return activates it;
    // `note` says why, right-aligned at the small size. An empty note means no note.
    bool enabled = true;
    std::string note;
};

// Demand-driven, the same bargain as Rows: the widget asks only for the children of what is
// actually expanded, and asking MUST NOT BLOCK. Returning false means "not yet" -- the row draws
// as pending and the source invalidates the window when it knows. A tree that called readdir
// from Draw would stall a frame on every expand, and on a slow mount it would stall every frame.
class TreeSource {
public:
    virtual ~TreeSource() {}
    // Children of `id`, or of the root when `id` is empty.
    virtual bool Children(const std::string& id, std::vector<TreeItem>* out) = 0;
    // The parent of `id`, or empty at the root. Only RevealInTree needs it: the widget cannot
    // know that a path's parent is a prefix of it, and a tree of something else would have a
    // different answer. A source that does not implement it simply cannot be revealed into.
    virtual std::string Parent(const std::string& id) const { (void)id; return std::string(); }
};

// A TreeSource for a flat, sectioned list: a non-selectable heading then its rows, all at the root
// (nothing is expandable). The launcher's inventory is exactly this. The heading id comes from the
// caller's key, not the label, so renaming a section never changes its identity.
struct TreeSection {
    std::string key;                 // stable identity for the heading (e.g. a category token)
    std::string label;               // what the heading draws
    std::vector<TreeItem> rows;
};

class SectionListSource : public TreeSource {
public:
    std::vector<TreeSection> sections;
    bool Children(const std::string& id, std::vector<TreeItem>* out) override {
        if (!id.empty()) return true;     // flat: nothing is ever expanded
        for (const TreeSection& s : sections) {
            TreeItem h;
            h.id = "section:" + s.key;
            h.label = s.label;
            h.section = true;
            h.flat = true;
            out->push_back(std::move(h));
            for (const TreeItem& r : s.rows) out->push_back(r);
        }
        return true;
    }
};

// A sidebar tree: expand, collapse, select, and navigate from the keyboard.
//
// Rows are FLATTENED to what is visible, which is what keeps the cost proportional to what is on
// screen rather than to the hierarchy: a collapsed home directory costs one row no matter what is
// under it, and only expanded nodes are ever asked for children.
//
// The keys are the convention every tree shares, and they are worth naming because half-doing
// them makes a tree that needs a mouse: Up and Down move, Right expands or steps into, Left
// collapses or steps out to the parent, Enter activates. Left on a collapsed node going to the
// parent rather than doing nothing is the detail people actually use to climb a tree.
//
// `selected` is a State holding the selected id, so the app reads the selection as a value and a
// breadcrumb or a path field can be bound to the same cell.
Desc TreeView(TreeSource* src, Binding<std::string> selected);
void OnTreeSelect(const Desc& tree, std::function<void(const std::string&)> f);
void OnTreeActivate(const Desc& tree, std::function<void(const std::string&)> f);
// Re-ask a tree's source on the next frame: its rows changed (a refresh). Keeps the scroll
// position and the selection. The window repaints the tree's frame.
void TreeInvalidate(Window& w, const Desc& tree);

// Expand the chain down to `id` and select it. This is how navigating in the main pane keeps the
// sidebar in step without the app having to know how the tree is laid out -- it names a
// destination, not a sequence of expansions.
void RevealInTree(const Desc& tree, const std::string& id);

// What the tree is currently showing, flattened. This is not only a test seam: the flattened row
// list IS the tree's observable state -- it is the thing expanding and collapsing change -- so an
// accessibility tree wants exactly this, and a test that could not read it would have to assert
// on pixels, which tells you a row moved without telling you which row it now is.
int TreeRowCount(const Desc& tree);
std::string TreeRowId(const Desc& tree, int row);
int TreeRowDepth(const Desc& tree, int row);
// Which row the keyboard is on, which is not always the selection: they come apart when the app
// sets the selection from elsewhere, and a section header must never be either.
int TreeCursor(const Desc& tree);

// ---- split panes ------------------------------------------------------------------------------

// Two children with a divider the user drags. `ratio` is the fraction of the main axis the FIRST
// child gets, and it is a State rather than an internal field so the APP owns it: a pane ratio is
// something a person sets once and expects to find again, so it belongs in whatever the app
// persists, not inside a widget that is rebuilt on the next launch.
//
// Two functions rather than one axis flag, for the same reason Motion has both Reveal and
// RevealX: the axis is a property of the split, and a bool at the call site reads as noise where
// a name reads as the layout. HSplit divides left | right, so its divider is vertical; VSplit
// divides top / bottom.
//
// min_a and min_b are in layout units and are a floor, not a hint. A pane dragged to nothing is
// a pane the user cannot get back, because there is no longer a divider wide enough to grab.
// Collapsing is therefore a separate gesture -- a double-click on the divider -- and it
// remembers where the divider was, so it is a toggle and not a destructive snap.
//
// There is no cursor change over the divider, because present_wl does not set a cursor shape.
// That is why the divider draws a grip: without one the gesture is undiscoverable, and a
// resizable pane nobody knows is resizable is a fixed pane that cost more to build.
struct SplitOpts {
    float min_a = 8;           // layout units, a floor on the first pane
    float min_b = 8;
    float thickness = 0.5f;    // units; the divider as drawn
    float grab_slop = 3;       // px added each side of it for hit testing only
};

Desc HSplit(Desc a, Desc b, Binding<float> ratio, SplitOpts o = SplitOpts{});
Desc VSplit(Desc a, Desc b, Binding<float> ratio, SplitOpts o = SplitOpts{});

// ---- damage -----------------------------------------------------------------------------------

// A bounded list of dirty rectangles in LAYOUT units.
//
// Deliberately not a region algebra. Overlapping rects cost a little duplicate copying and no
// correctness; a proper region costs code and a per-frame allocation. Past `kMax` it collapses to
// one bounding rect, so the worst case is a full repaint rather than an unbounded list -- which is
// also what a scroll or a resize wants anyway.
class Damage {
public:
    static const size_t kMax = 16;

    void add(Rect r);
    void all() { all_ = true; r_.clear(); }
    void clear() { all_ = false; r_.clear(); }
    bool empty() const { return !all_ && r_.empty(); }
    bool is_all() const { return all_; }
    const std::vector<Rect>& rects() const { return r_; }
    // The union, for the clip: drawing is clipped to this and then each rect is reported
    // separately to the compositor.
    Rect bounds() const;

private:
    std::vector<Rect> r_;
    bool all_ = true;        // a fresh window owes a full frame
};

// ---- menus ------------------------------------------------------------------------------------

// The effect class a menu item carries, from decision 0009. It is the only authority signal in
// the system and the menu's job is to show it BEFORE the user commits, not after.
enum class Effect : uint8_t { Read, Write, Destructive, External };

struct MenuItem {
    std::string label;
    std::string note;                  // a shortcut, or why it is disabled
    Effect effect = Effect::Read;
    bool enabled = true;
    bool is_default = false;           // drawn bold and placed first
    bool separator = false;
    std::function<void()> run;

    static MenuItem sep() {
        MenuItem m;
        m.separator = true;
        m.enabled = false;
        return m;
    }
};

// A glyph button that opens a menu under itself. Composable from Button + Defer + ShowMenu
// today, which is why it is a node: the ANCHOR is the thing a caller gets wrong, and a menu
// that opens somewhere other than under the control it belongs to reads as a different control.
//
// The items are asked for when the button is pressed, not when the tree is built: a view menu
// whose ticks were decided at build time would show the state the app started in.
Desc MenuButton(Glyph g, std::function<std::vector<MenuItem>()> items);

// A popup menu on its own Wayland surface, so it is not clipped by its parent window and the
// compositor can place it. Ordering and marking follow docs/aurora-ui.md section 7 and the
// plumber's ranking: the default first and bold, destructive items after a separator, and a
// disabled item STAYS VISIBLE with its reason -- Apple's 1992 rule, "not eliminated from a
// display but merely dimmed", because hiding what is unavailable teaches the wrong model.
class Menu {
public:
    Menu(Window* parent, float x, float y, std::vector<MenuItem> items);
    ~Menu();
    bool ok() const { return win_ != nullptr; }
    // Runs until something is picked or it is dismissed. Returns the index picked, or -1.
    int Run(int max_ms = 0);

private:
    void Draw();
    static void Event(void* user, int kind, int a, int b);
    static void Key(void* user, const au_key* k);
    int at(float y) const;

    Window* parent_;
    std::vector<MenuItem> items_;
    au_wl_win* win_ = nullptr;
    au_surface* sf_ = nullptr;
    float w_ = 0, h_ = 0, row_ = 0;
    double scale_ = 1;
    int hover_ = -1, picked_ = -1;
    bool done_ = false, dirty_ = true;
};

// ---- the window -------------------------------------------------------------------------------

class Window {
public:
    Window(const char* title, int lw, int lh);
    ~Window();

    bool ok() const { return win_ != nullptr; }
    // True on the thread that constructed (and runs) this window. A cell uses it to decide whether
    // a set() can write its value directly or must post the apply to this window's loop.
    bool on_loop_thread() const { return std::this_thread::get_id() == loop_tid_; }
    // A tree swap. The old tree is dropped here; attachment is symmetric (ADR 0009) so the CELLS
    // are safe, but the window's own node lists still named the freed nodes, so clear them and force
    // a fresh layout. Before this, a swap mid-run (preview, hot reload) let Run's tick scan and
    // event routing walk freed Node*; SetRoot was only safe before the first Run.
    void SetRoot(Desc root) {
        root_ = std::move(root);
        all_.clear();
        focusables_.clear();
        hover_ = pressed_ = focus_ = last_node_ = nullptr;
        drag_src_ = drag_tgt_ = nullptr;
        laid_ = false;
        damage_.all();
        dirty_ = true;
    }
    Env& env() { return env_; }
    void Invalidate() { dirty_ = true; damage_.all(); }
    // Mark one rectangle dirty. This is what a State<T> set and a hover should call: a hover that
    // restyles one button must not repaint the window.
    void Invalidate(Rect r) { dirty_ = true; damage_.add(r); }
    void Quit() { quit_ = true; }

    // Mark the window dirty from ANOTHER THREAD.
    //
    // A worker that has produced something worth showing calls this -- Atlas's directory lister
    // fills in sizes and dates behind the listing, and before this existed those values simply
    // never appeared: the data landed, nothing repainted, and the columns stayed blank until
    // some unrelated event happened to force a frame. The user's report was that a directory
    // would not load until they clicked a toolbar button.
    //
    // It touches ONE ATOMIC and nothing else. Invalidate() appends to the damage list, which is
    // not safe to touch off the main thread; the loop turns this flag into real damage.
    void InvalidateThreadsafe() { ext_dirty_.store(true, std::memory_order_release); }

    // ---- the drag ghost ----
    //
    // What the pointer carries during a drag, drawn by the WINDOW rather than by the source:
    // the ghost has to be able to leave the widget it came from, and a node can only draw
    // inside its own frame. Built once at the start of the drag and damaged per move, which is
    // two small rectangles and not the window (docs/atlas-design.md 6.6).
    struct DragGhost {
        std::string label;        // middle-elided to `max_label` when it does not fit
        IconSpec icon;
        int count = 1;            // > 1 draws the count badge
        float max_label = 160;
        bool has_icon = true;
    };
    void SetDragGhost(const DragGhost& g);
    void ClearDragGhost();
    bool drag_ghost_shown() const { return ghost_on_; }
    Rect drag_ghost_rect() const;      // where it is now, for a test and for the damage

    // What is being dragged right now, or nullptr. A node that wants to draw differently while
    // a drag is in flight -- a list dimming the rows it cannot accept -- reads this.
    const DragData* dragging() const { return drag_live_ ? &drag_ : nullptr; }

private:
    void damage_for(Node* n);
    void damage_hinted(Node* n);     // what the node says changed, else its whole frame
    // The drag state machine: see Window::Event's POINTER_MOVE case for why a drag is a press
    // that moved rather than a mode the app enters.
    void drag_motion(float x, float y);
    void drag_finish(float x, float y);

    // A resize the event thread has asked for and the loop has not applied yet. The size is
    // the only piece of window state a handler must not act on directly: everything else a
    // handler touches is a scalar or a damage rectangle, but a resize REPLACES the pixel buffer
    // the main thread is drawing into.
    // ---- input arrives on another thread -------------------------------------------------------
    //
    // present_wl dispatches on its own thread, so every one of these callbacks used to run there
    // and mutate window state -- hover, focus, the pressed node, the damage list, and for a while
    // the pixel buffer itself -- while the main thread was reading it. The buffer was the one
    // that crashed; the rest had simply not been caught yet.
    //
    // So the callbacks now only RECORD. One small struct per event goes on a queue under a
    // mutex, and the loop drains it and dispatches on the main thread, where every node already
    // lives. It is the same rule the menu, the clipboard and the resize each arrived at
    // separately: handlers decide, the loop acts. This makes it the toolkit's shape rather than
    // three special cases.
    struct InputEvent {
        int kind = 0;            // AU_EV_*, or kKey
        int a = 0, b = 0;
        au_key key{};
        static const int kKey = -1;
    };
    std::mutex q_mu_;
    std::vector<InputEvent> queue_;

    void DrainInput();                       // main thread: dispatch what arrived
    void HandleEvent(int kind, int a, int b);
    void HandleKey(const au_key& k);

    uint32_t mods_ = 0;
    bool keyboard_driven_ = false;
    std::function<bool(const au_key&)> key_hook_;
    std::atomic<bool> ext_dirty_{false};
    std::mutex size_mu_;
    int want_w_ = 0, want_h_ = 0;

    DragGhost ghost_;
    bool ghost_on_ = false;
    void draw_ghost();

    DragData drag_;
    Node* drag_src_ = nullptr;      // where it started, so a drop on the source is a no-op
    Node* drag_tgt_ = nullptr;      // what is currently highlighted
    float drag_x0_ = 0, drag_y0_ = 0;
    bool drag_live_ = false, drag_armed_ = false;

public:

    // Runs until the window closes.
    //
    // frames > 0 stops after that many. An IDLE APP NOW DRAWS NOTHING -- the loop sleeps until
    // an event arrives -- so a frame count alone can wait forever, which is correct behaviour
    // and a trap for a scripted run. Hence two guards:
    //   force_redraw: invalidate the whole window each iteration. Measurement only, and the only
    //                 way to get an unattended full-repaint number.
    //   max_ms:       wall-clock ceiling, so a scripted run cannot hang.
    int Run(int frames = 0, bool force_redraw = false, int max_ms = 0);

    // What the frame-timing instrument saw. The 8 ms rule is only a rule if it is measured.
    struct Timing { size_t frames = 0; double p50 = 0, p95 = 0, max = 0; size_t over_8ms = 0; };
    Timing timing() const;

    au_surface* surface() const { return sf_; }
    au_wl* display() const { return wl_; }
    au_wl_win* native() const { return win_; }
    const Env& env_const() const { return env_; }
    double scale() const { return scale_; }

    // Show a menu at a point in this window's coordinates. Returns the index picked, or -1.
    //
    // MUST NOT be called from an event handler. Handlers run on present_wl's event thread while
    // it holds the display mutex, and creating a surface takes that same mutex: the result is a
    // deadlock, which is exactly what happened the first time. Use Defer.
    int ShowMenu(float x, float y, std::vector<MenuItem> items, int max_ms = 0);

    // Run `f` on the main loop, after the current dispatch returns. This is how an event handler
    // asks for anything that touches the display -- a menu, a dialog, a new window. The rule is
    // simple and absolute: handlers decide, the loop acts.
    void Defer(std::function<void()> f);
    // Task<F,G> bookkeeping: the token flips at teardown; the thread is joined then too.
    std::shared_ptr<std::atomic<bool>> lifetimeToken() const { return lifetime_; }
    void adoptTask(std::thread t);

    // Whether the last input was a KEY rather than a pointer press.
    //
    // This is what :focus-visible is in a browser and what Full Keyboard Access is on a Mac:
    // the focus halo follows keyboard use, appears on the first keyboard navigation and goes
    // away on the next pointer press. Without it the halo is either always on -- a ring around
    // a row somebody just clicked, which reads as a second kind of selection -- or never on,
    // which is the state the design is fixing. The window is the only place that sees both
    // kinds of input, so it is the one that answers.
    bool keyboard_driven() const { return keyboard_driven_; }

    // Which modifier keys are held right now (AU_MOD_*). A press carries where and how many
    // clicks; what was HELD comes from here, which is what lets a list tell a plain click from
    // a Ctrl-click without every node growing a new handler signature.
    uint32_t mods() const { return mods_; }

    // An app-level accelerator, offered every key BEFORE the focused widget sees it. Return true
    // to consume. This is where Back, Forward, Up and refresh live: they have to work wherever
    // the focus happens to be, and routing them through whichever widget is focused would mean
    // every widget knowing about navigation.
    //
    // It runs before Tab, so an app can override even that, and it is one hook rather than a
    // registry because a window has one set of accelerators and a list of them with priorities
    // would be machinery for a problem nobody has here yet.
    void SetKeyHook(std::function<bool(const au_key&)> f) { key_hook_ = std::move(f); }

    // Clipboard, from a key handler.
    //
    // Both of these MUST be deferred and the reason is the same deadlock the menu hit: a handler
    // runs on present_wl's event thread while it holds the display mutex, and both clipboard
    // calls take that mutex. Paste is worse than a lock inversion -- reading the selection needs
    // the event thread to dispatch the owner's `send`, and when the owner is this same process
    // that thread is the one blocked in the handler, so it waits for itself.
    //
    // RequestPaste checks the target is still in the tree before inserting: a rename field is
    // destroyed when the edit commits, and a paste that arrived after that would write to freed
    // memory.
    void RequestCopy(const std::string& text);
    void RequestPaste(Node* target);
    Node* focus() const { return focus_; }
    size_t focusable_count() const { return focusables_.size(); }
    size_t keys_seen() const { return keys_seen_; }
    // Focus the first focusable node in tree order. Note what that means: tree order is reading
    // order, so in a window whose toolbar comes before its content the FIRST focusable is a
    // toolbar button, not the content. That is correct for Tab and wrong for "what should the
    // keyboard talk to when this opens", which is why FocusNamed exists and is what callers want.
    void FocusFirst();

    // Focus the node with this `name`. The name modifier is not decoration: it is how a test, an
    // agent reading the accessibility tree, and the app itself all address a widget. This is its
    // first real use, and it is the primitive that makes the other two possible.
    bool FocusNamed(const char* name);

    // Move focus in a DIRECTION, for the arrow keys. Tab is reading order; arrows are where
    // things are on screen, which is what a person means when the sidebar is to the left of the
    // list. Only reached when the focused widget declined the key, so a list still gets its own
    // up and down and a tree still gets expand and collapse.
    bool MoveFocusSpatial(int dx, int dy);
    Node* FindNamed(const char* name);

private:
    void Layout();
    void Draw();
    static void Event(void* user, int kind, int a, int b);
    static void Key(void* user, const au_key* k);
    void CollectFocusables();
    void MoveFocus(int delta);
    void SetFocus(Node* n);

    std::vector<Node*> focusables_;
    std::vector<Node*> all_;        // every node, from one walk; focusables_ is a subset
    Node* focus_ = nullptr;
    size_t keys_seen_ = 0;
    // Click counting, for double and triple clicks.
    std::chrono::steady_clock::time_point last_click_{};
    Node* last_node_ = nullptr;
    float last_x_ = 0, last_y_ = 0;
    int clicks_ = 0;
    mutable size_t motion_degraded_ = 0;
    std::vector<std::function<void()>> deferred_;
    std::mutex deferred_mu_;        // the event thread writes it, the loop drains it
    std::shared_ptr<std::atomic<bool>> lifetime_{std::make_shared<std::atomic<bool>>(true)};
    std::vector<std::thread> tasks_;
    std::mutex tasks_mu_;            // guards tasks_ (Task may be called from the loop; teardown joins)

    au_wl* wl_ = nullptr;
    au_wl_win* win_ = nullptr;
    au_surface* sf_ = nullptr;
    Desc root_;
    Env env_;
    int lw_ = 0, lh_ = 0;
    double scale_ = 1.0;
    bool dirty_ = true, quit_ = false, laid_ = false;
    std::thread::id loop_tid_;       // the thread that constructed/run this window
    float px_ = -1, py_ = -1;
    Node* hover_ = nullptr;
    Node* pressed_ = nullptr;
    std::vector<double> draw_ms_;
    Damage damage_;
    size_t full_frames_ = 0, partial_frames_ = 0;
    double damaged_px_ = 0, total_px_ = 0;

public:
    // What damage tracking actually saved, so the claim is measured rather than asserted.
    struct DamageStats { size_t full = 0, partial = 0; double mean_fraction = 1.0; };
    DamageStats damage_stats() const;

    // Motion degrades rather than dropping frames: when the recent frames have run over
    // budget, a new transition resolves to None and a running one snaps. Same rule the
    // compositor uses for glass -- an effect is promoted into, never assumed.
    bool motion_allowed() const;
    size_t motion_degraded() const { return motion_degraded_; }
};

// Async: the only way to leave the window thread. T is deduced from `work`; `done` runs on the loop
// thread with the result (or nothing for a void work). If the window closes first, `done` does not
// run, and the worker's thread is joined at teardown -- so it cannot post to a destroyed queue.
template <class F, class G>
void Task(Window& w, F work, G done) {
    auto life = w.lifetimeToken();
    std::thread th([&w, life, work = std::move(work), done = std::move(done)]() mutable {
        using R = std::invoke_result_t<F>;
        if constexpr (std::is_void_v<R>) {
            work();
            if (!life->load(std::memory_order_acquire)) return;
            w.Defer([done = std::move(done), life]() mutable {
                if (life->load(std::memory_order_acquire)) done();
            });
        } else {
            R r = work();
            if (!life->load(std::memory_order_acquire)) return;
            w.Defer([done = std::move(done), life, r = std::move(r)]() mutable {
                if (life->load(std::memory_order_acquire)) done(std::move(r));
            });
        }
    });
    w.adoptTask(std::move(th));
}

template <class T>
void State<T>::invalidate_bound() {
    // The damage list and dirty_ are the loop thread's; this is only ever called on it (directly
    // from a loop-thread set(), or from the deferred lambda the loop drains).
    if (!win_) return;
    if (nodes_.empty()) { win_->Invalidate(); return; }
    for (Node* n : nodes_) win_->Invalidate(n->frame);
}

template <class T>
void State<T>::notify_dependents() {
    // Declared sources are named at construction, so this is a fixed list, not a walk. markDirty
    // cascades to derived-of-derived; win_ may be null (an unbound cell), which markDirty tolerates.
    for (DerivedBase* d : dependents_) d->markDirty(win_);
}

template <class T>
void State<T>::set(T n) {
    // v_ is the loop thread's to write. On the loop thread (~ or with no window at all) apply now;
    // off it, post the whole apply to the deferred queue, which is thread-safe and drained on the
    // loop. So a loop-thread get() never races a worker's set(), and the damage list is only ever
    // touched on the loop thread. The liveness token guards a cell that dies before the drain.
    if (!win_ || win_->on_loop_thread()) {
        v_ = std::move(n);
        invalidate_bound();
        notify_dependents();
        return;
    }
    auto tok = alive_;
    win_->Defer([this, tok, n = std::move(n)]() mutable {
        if (!*tok) return;             // the cell died before the loop drained this
        v_ = std::move(n);
        invalidate_bound();
        notify_dependents();
    });
}

// Symmetric attach/detach (ADR 0009). attach() records the node in the state so set() invalidates
// it, and the state in the node so the node can detach itself when destroyed. detach() erases the
// node from this state's list. ~Node calls detach on every cell it bound to, which is what makes
// SetRoot safe: dropping the old tree frees the nodes, and their destructors remove them from the
// cells before a later set() can touch them.
template <class T>
void State<T>::attach(Node* n) {
    nodes_.push_back(n);
    n->addCell(this);
}

template <class T>
void State<T>::detach(Node* n) {
    for (auto it = nodes_.begin(); it != nodes_.end();) {
        if (*it == n) it = nodes_.erase(it);
        else ++it;
    }
}

inline Node::~Node() {
    for (StateBase* c : cells_) c->detach(this);
}

// The other direction: a State is destroyed before the nodes that read it (main's teardown can
// destroy a tree after its cells, e.g. a Desc held longer than the state it bound). Forget itself
// in each node so a later ~Node has nothing dangling to call.
template <class T>
State<T>::~State() {
    alive_->store(false, std::memory_order_release);   // a Binding that outlives this cell stops reading through it
    for (Node* n : nodes_) n->removeCell(this);
}

}  // namespace ui
}  // namespace aurora

#endif
