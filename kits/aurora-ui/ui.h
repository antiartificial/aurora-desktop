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
#include "../interface/appserver/present.h"

#include <chrono>
#include <functional>
#include <mutex>
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
struct TextStyle {
    float size = 1.0f;             // multiplier on Env::size
    bool bold = false;
    bool italic = false;
    bool has_color = false;
    au_color color{};              // only when has_color
};

// ---- the environment: what flows down, with explicit override points -------------------------

struct Env {
    au_font font{};
    float size = 13;
    au_font bold{};
    au_font italic{};
    au_font bold_italic{};
    float unit = 8;                 // the spacing unit; padding(1) is one unit
    au_color fg{236, 239, 244, 255};
    au_color dim{150, 157, 178, 255};
    au_color accent{122, 162, 247, 255};
    au_color surface{38, 41, 54, 255};
    au_color surface_hi{52, 56, 73, 255};

    // The face and size a style asks for. One place resolves this, so every component that draws
    // text gets the same answer and a missing face falls back rather than drawing nothing.
    au_font face(const TextStyle& st) const {
        if (st.bold && st.italic) return bold_italic;
        if (st.bold) return bold;
        if (st.italic) return italic;
        return font;
    }
    float sized(const TextStyle& st) const { return size * st.size; }
    au_color ink(const TextStyle& st) const { return st.has_color ? st.color : fg; }
};

// ---- state: a cell, bound at build, that invalidates what it feeds ---------------------------

class Window;
class Node;      // State holds Node* to invalidate what reads it; the class comes below

// Set() invalidates exactly the nodes that read this cell: no diffing and no tree walk, because
// the binding was made when the tree was built. A node constructed from a State attaches itself,
// which is what makes "bound at build time" true rather than aspirational -- without it set()
// can only invalidate the whole window, which it did at first and which measured every frame as
// a full repaint.
//
// Lifetime: a State must outlive the nodes built from it. In practice both are owned by the
// window's setup scope, and a node is never destroyed before the tree it is in.
template <class T>
class State {
public:
    explicit State(T v) : v_(std::move(v)) {}
    const T& get() const { return v_; }
    void set(T n);
    void bind(Window* w) { win_ = w; }
    Window* window() const { return win_; }
    void attach(Node* n) { nodes_.push_back(n); }
    size_t bound_count() const { return nodes_.size(); }

private:
    T v_;
    Window* win_ = nullptr;
    std::vector<Node*> nodes_;
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

// Text style, on any node that draws text. Chains like the others:
//   styled(Label("Atlas"), TextStyle{1.4f, true})          -- 40% larger and bold
//   styled(Label(&note), TextStyle{0.85f, false, true})    -- smaller and italic
Desc styled(Desc d, TextStyle st);

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
Desc IconView(Rows* rows, int* selected);
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

void OnListRename(const Desc& list, std::function<bool(int, const std::string&)> f);
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
Desc TextView(State<std::string>* text, bool wrap = true);

// ---- breadcrumb -------------------------------------------------------------------------------

// Path segments, clickable, each navigating to that prefix.
//
// It is a WIDGET that draws segments, not a stack of Buttons, and that is the architecture
// showing its shape rather than a shortcut: the tree is built once and there is no reconciler,
// so a container whose child count changes with the path could not be expressed as children.
// Structural change is a widget's job -- the same reason the list owns its own rows.
//
// Clicking past the last segment calls on_edit, which is where a path field belongs (Cmd+L).
Desc Breadcrumb(State<std::string>* path,
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

Desc TextField(State<std::string>* text,
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
enum class Motion : uint8_t { None, Reveal, RevealX };
enum class Curve : uint8_t { EaseOut, Linear };

struct Transition {
    Motion motion = Motion::Reveal;
    uint32_t ms = 140;                 // matches the shell's chrome
    Curve curve = Curve::EaseOut;
};

// Eased progress, and the one rule that makes a reversal not stutter: it resumes from where it
// got to rather than from the start.
class Ease {
public:
    void target(float t, uint32_t ms);
    float value() const { return cur_; }
    bool running() const { return running_; }
    // Advance to now. Returns true while still moving.
    bool tick();
    void snap() { cur_ = want_; running_ = false; }

private:
    float cur_ = 0, from_ = 0, want_ = 0;
    uint32_t ms_ = 140;
    bool running_ = false;
    std::chrono::steady_clock::time_point t0_{};
};

// A container that reveals its child: height animates between zero and the child's natural
// height. The child is NOT measured differently -- it keeps its own layout -- so revealing does
// not reflow the child's contents, which is what makes a reveal cheap and stable.
//
// The target state is live immediately: `shown` flips, hit testing follows it the same frame,
// and only the pixels catch up. A transition that gated input would be a latency bug in costume.
Desc Reveal(State<bool>* shown, Desc child, Transition t = Transition{});

// Exactly one of two children, chosen by a State. Both are built once and kept; the one not
// shown is not measured, not drawn, not hit-tested and not in the focus order.
//
// This is how a view switch works when there is no reconciler: you do not rebuild the tree, you
// hold both and pick. It costs the memory of two widgets and buys instant switching with each
// view keeping its own scroll position and selection -- which is what a person expects when they
// toggle back.
Desc Either(State<bool>* first, Desc a, Desc b);

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
    void SetRoot(Desc root) { root_ = std::move(root); dirty_ = true; }
    Env& env() { return env_; }
    void Invalidate() { dirty_ = true; damage_.all(); }
    // Mark one rectangle dirty. This is what a State<T> set and a hover should call: a hover that
    // restyles one button must not repaint the window.
    void Invalidate(Rect r) { dirty_ = true; damage_.add(r); }
    void Quit() { quit_ = true; }

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

    au_wl* wl_ = nullptr;
    au_wl_win* win_ = nullptr;
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

template <class T>
void State<T>::set(T n) {
    v_ = std::move(n);
    if (!win_) return;
    // Nothing attached means nothing in the tree reads this cell through a node, so the safe
    // answer is the whole frame. One or more attached means we know exactly what changed.
    if (nodes_.empty()) {
        win_->Invalidate();
        return;
    }
    for (Node* n : nodes_) win_->Invalidate(n->frame);
}

}  // namespace ui
}  // namespace aurora

#endif
