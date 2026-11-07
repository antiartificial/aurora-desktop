# `aurora::ui` — the UI language

The reference for Aurora's toolkit. `docs/aurora-ui-framework.md` holds the
critique, the research and the build order and is the *why*; this is the *what*.

The spine — build once, `State<T>` cells, `Env`, `Node::Materialise`, the `Rows`
source — is from that document and is not restated here except where this one
extends it. Everything below covers what it left open: transitions, previews,
views, selection, sorting, search, commands, and the shelf.

## 0. Where this sits, and why not on top of `BView`

**`aurora::ui` is a Wayland-native toolkit on our own substrate. The BeAPI is a
legacy path beside it, not underneath it.**

The framework document argued the opposite: that the substrate two kits would
share *is* `BView`, so a second kit sharing it is the Interface Kit with a new
API on top. That was answering a different question — it costed replacing
Haiku's widgets with our own view class *while keeping the app_server*, which the
kits README had already measured at about thirteen thousand lines. Skipping the
app_server is a different and much smaller proposition, because of what we
already own:

| | lines | `BView` refs | `AS_` refs |
|---|---|---|---|
| `canvas.cpp` — fills, strokes, round rects, ellipses, gradients, clip, blit, `CopyBits` | 297 | 0 | 0 |
| `fonts.cpp` + `text_backend.h` — FreeType + HarfBuzz: shaping, metrics, glyph bitmaps | 355 | 0 | 0 |
| `present_wl.c` — `wl_shm` presentation | 458 | 0 | 0 |
| `server.cpp` — ports, `AS_*` dispatch, the global lock, the resize wipe | 1,939 | 3 | 132 |

The first three rows are a complete 2D rasteriser, a real text stack and a
Wayland presenter. They are ours, they are about a thousand lines, and **none of
them knows what a `BView` is.** The fourth row exists only to serve the BeAPI.

So `aurora::ui` sits directly on those three, in one process, with no ports and
no opcodes. Haiku's Interface Kit keeps `server.cpp` and keeps working
unchanged; it becomes the compatibility path for the apps that need it.

### What that buys, measured

Three defects are removed by construction rather than fixed, because all three
live in the layer we are not using:

1. **The global lock.** `WindowThread` holds one process-wide mutex across the
   whole of `WindowDispatch`, including all drawing. Measured: eight windows take
   about **12.8x** one window's time, worse than plain serialisation, with
   per-repaint cost rising from 1.23 ms to about 2.0 ms — a convoy on top of the
   serialisation. It is a property of opcode dispatch, and there is no opcode
   dispatch here.
2. **The resize flicker.** `Canvas::Init` does `fPx.assign(w*h, 0xFFFFFFFFu)` —
   it fills the window **opaque white** — and then tells the client to redraw
   asynchronously. At scale 1.45 that is **19 MB reallocated and filled per
   resize event, about 5.4 ms**, a third of the frame budget spent erasing. The
   white gap before the client repaints is the flicker you can see. A toolkit
   that owns its own resize keeps the old pixels and repaints over them.
3. **No frame control.** The 8 ms handler rule is enforceable when the toolkit
   owns its frame loop, and aspirational when a port and an opcode dispatcher sit
   in between.

### What it costs, honestly

This is **more** new code than layering over `BView`, and the failure mode is a
half-built toolkit, which is worse than a finished one. Four things we would
otherwise have inherited:

- **Widgets, all of them.** `BColumnListView` is lost, and it is the grid that
  just landed. The list is now ours to write — though the framework document's
  stage 2a was already testing whether `BColumnListView` was adequate, so this
  may be a smaller loss than it looks.
- **Input.** Wayland input to events is `server.cpp`'s job today. `present_wl.c`
  has the Wayland plumbing; the event layer above it is new.
- **Theming through `BControlLook`.** `AuroraControlLook` is an implementation of
  Haiku's skinning seam and does not apply. We theme from the same tokens in
  `contracts/theme/aurora-glass.toml` directly, which is more direct but is code
  that does not exist.
- **Layout.** `BLayout`, `BGridLayout`'s column weights (which is where XAML's
  star sizing already lives) and `B_FOLLOW_*` anchoring are all BeAPI. Ours to
  build, to the Flutter rule the framework document adopted: a child visited at
  most twice per layout pass.

**One thing gets better, not worse.** Decision 0017 derives the accessibility
tree from `AS_*` draw opcodes, and its own recorded weakest point is that it
only contains what was *drawn* — a forty-item list reports the fourteen rows it
painted. A toolkit that knows its own widget tree **emits** structure instead of
inferring it from draw calls, so an agent sees forty rows, and roles stop being
guesses from `B_NAVIGABLE` flags. The tree gets more honest by being authored
rather than reconstructed.

### The rule that keeps this from sprawling

**A component enters the language when a real client needs it.** Not to fill a
catalogue, and not because another file manager has it. The explorer is the only
client until the language is real, and the Haiku apps keep running on the legacy
path throughout — so nothing regresses while this is built.

## 1. Transitions

The user asked for transitions; the research says the perceptible threshold for
indirect input is **55 ms for dragging** (CHI 2014, n=16, via the framework
document) and that **nothing may move under the pointer**. Those are not in
tension if transitions are declared as *properties of a state change* rather than
written as animations.

```cpp
enum class Motion : uint8_t { None, Fade, Expand, Slide };
enum class Curve  : uint8_t { EaseOut, Linear };          // no ease-in. House rule.

struct Transition {
    Motion motion = Motion::Fade;
    uint32 ms     = 140;                                  // matches the shell's chrome
    Curve  curve  = Curve::EaseOut;
};

d.transition(Transition{Motion::Expand, 140})             // a modifier like any other
```

Four rules, and they are the whole of it:

1. **Only two things may animate: a thing appearing or disappearing, and a
   container changing size.** Content position never animates. A row that slides
   while you are reaching for it is worse than a row that was simply there.
2. **The target state is live immediately; only pixels catch up.** A click on a
   row that is still fading in hits that row. A transition that gates input is a
   latency bug wearing a costume.
3. **Transitions degrade, they do not drop frames.** The toolkit owns its frame
   loop, so it knows the recent frame times directly rather than inferring them
   from message dispatch; when they are over budget, `Motion` resolves to `None`
   for new transitions and running ones snap to their end state. This is the same
   "promoted into, never assumed" rule the compositor uses for glass, applied to
   motion — and it is only trustworthy because there is no port and no opcode
   dispatcher between the decision and the frame.
4. **Interruptible, from wherever it got to.** A reversal eases out from the
   current value, never from the start, or fast repeated toggles stutter.

What this deliberately cannot express: spring physics, staggered cascades,
cross-fades between two live views, anything per-item in a list. A list of forty
rows each on its own curve is forty invalidations a frame, and the measured
budget has 8 ms for the whole handler.

## 2. Previews — and where the decoding runs

**This is the most security-relevant decision in the toolkit, and the answer is
that the app never decodes a previewed file.**

A preview means parsing a file the user did not write with a library that has a
CVE history. Aurora already has the mechanism for exactly this: a verb, with an
effect class, run inside a domain (decisions 0009 and 0014). So:

> **Preview is a verb.** `preview@v1`, effect `read`, provided by an app running
> in a domain that holds the file read-only and `device:wayland` for nothing at
> all. It returns a bitmap and a description over the bus. The explorer links no
> image, video, PDF or archive decoder, ever.

```cpp
// The pane. It asks; it does not decode.
Desc PreviewPane(State<Selection>& sel);

// What comes back, and what the pane shows for each case. There is no fourth
// state where it shows nothing and lets the user wonder.
struct PreviewResult {
    enum class Kind { Bitmap, Text, Unavailable } kind;
    BBitmap*    bitmap = nullptr;    // owned by the pane
    std::string text;                // capped: see below
    std::string why;                 // Unavailable always says why, in words
};
```

Three consequences worth stating, because each is a cost:

- **A round trip, not a function call.** Budget it like one: the pane shows the
  file's name and type immediately and the preview arrives later, under the same
  progressive rule as the list. A preview is never on the path to a first frame.
- **One long-lived preview domain, not one per file.** A domain per preview would
  pay a spawn per selection change, and a selection change happens on every
  arrow-key press. The domain is started once, holds no file, and is handed each
  file as a grant for the duration of one request — which is exactly a tier-1
  lease (decision 0019), and the first real use for one.
- **Caps are policy, in the verb, not in the pane.** Porpoise caps text preview
  at 2,000 lines or 256 KiB; adopt that shape. The cap belongs where the
  untrusted parse happens, so the pane cannot be talked out of it.

The full-window preview (porpoise's Space, Finder's Quick Look) is the same verb
with a bigger target rect. It is not a second mechanism.

## 3. Views

```cpp
Desc ListView(Rows& rows);        // columns, sortable. The default.
Desc ColumnView(Rows& rows);      // NeXT/Finder browser: one column per level
Desc IconView(Rows& rows);        // a Collection over the same Rows
```

All three take the *same* `Rows` source, so switching views re-materialises the
widget and fetches nothing it already has. View state (which view, column widths,
sort) is remembered per folder — Tracker's spatial memory without Tracker's
window explosion.

**Icon view does not ship first, and the reason is honest.** There is no icon
system: HVIF with a bitmap fallback is approved and unbuilt. An icon view before
icons is a grid of identical placeholder glyphs, which is strictly worse than a
list — it costs more space and conveys less. `IconView` is specified here so the
`Collection` it needs is designed once, and it ships with the icons.

## 4. Selection is a value

Everything downstream consumes one thing, so there is one place to get it right.

```cpp
struct Selection {
    std::vector<entry_ref> items;
    int32                  anchor = -1;    // for shift-extend
    bool Empty() const { return items.empty(); }
};
```

The plumber takes it (`registry.plumb@v1` ranks verbs over a selection), the
preview pane takes it, the shelf takes it, and a drag carries it. Porpoise's
`Ctrl+Space` toggle-current-item is worth adopting: it makes a disjoint selection
reachable from the keyboard, which rubber-banding never does.

## 5. Sorting, and the trap

Values arrive late — `lstat` is 13–43 ms for a large directory and runs on a
worker. So a sort can be asked for before the values it sorts on exist. The
wrong answer is to re-sort as they land, because **that moves rows under the
pointer**, which rule 1 of transitions forbids and which is maddening in use.

> **A sort is a snapshot.** It orders the values known at the moment it was
> asked for. Late values fill in place and never re-order. The header says how
> many cells were still unknown, and clicking it again re-sorts with what is now
> known.

```cpp
struct Rows {   // extending the framework document's source
    virtual void  SortBy(int32 col, bool ascending) = 0;
    virtual int32 PendingAtSort() const = 0;   // what the header reports
};
```

This is a product decision dressed as a technical one, and it is the right way
round: the user asked to sort *now*, by what is *on screen*. A list that quietly
rearranges itself for the next ten seconds is not answering that.

## 6. Search is three separate things

Conflating them is why search in most file managers feels like a mode you get
stuck in. The framework document's HIG position says type-ahead is never a
dialog; this is that, made concrete.

| | gesture | scope | result |
|---|---|---|---|
| **Type-ahead** | just type | the visible listing | moves the selection. No UI appears. |
| **Filter** | `/` | the current folder's listing | narrows the rows in place; `Esc` restores |
| **Query** | `Cmd+F` | wherever the query says | opens a **query window** |

The third is the one that is ours. A query is not a search box — it is a saved
object in the existing `.query` format (`index/query.go`: `mime`, `ext`, `app`,
`verb`, `since`, `under`, `derived-from`). So `Cmd+F` builds a query, and a
query window's contents *are* its results. Provenance predicates get first-class
UI because no other file manager can offer them:

```
verb: compile@v1
derived-from: /home/arti/src/aurora/thing.h
since: this-week
```

**What the query window must not imply.** Our queries are re-run, not live —
Tracker's update as the filesystem changes and ours do not. The window says when
it was run. Claiming freshness we do not have is worse than showing a timestamp.

Worth stealing from Sigma: **typo tolerance** — wrong case, wrong word order,
missing extension. It applies to the filter and the palette, not to query
predicates, where an unmatched key must stay an error (that strictness is why a
typo cannot silently match everything).

## 7. Commands — one registry, four surfaces, and the agent story

```cpp
struct Command {
    const char* id;        // "navigate.up" -- stable, and what an agent names
    const char* title;     // "Go Up"
    Effect      effect;    // read | write | destructive | external (decision 0009)
    Shortcut    key;
    std::function<bool(const Selection&)>        enabled;  // and WHY, when false
    std::function<void(const Selection&)>        run;
};
```

**Every surface enumerates the same registry**: the command palette, the menus,
the keyboard map, and the accessibility tree. There is no command reachable one
way and not another, and no hidden verb.

Two things fall out of that, both of which were asked for separately:

- **A disabled command stays visible, with its reason.** Apple's 1992 guidance —
  "not eliminated from a display but merely dimmed" — and porpoise does this in
  its palette. Hiding what is unavailable teaches the user the wrong model of
  the program.
- **Agents are first-class without a second API.** An agent reads the registry
  through the accessibility tree (decision 0017) and invokes by `id`. It sees the
  effect class, so it knows which commands are destructive before calling one.
  That is the whole of "agents as first-class citizens": not an automation
  surface bolted on, but the same list the menus are built from.

The palette's scope follows porpoise, which is broader than most: menu actions
*plus* places, recent locations, open tabs, and saved queries. One place to type
the name of anything.

## 8. The shelf

Porpoise's best idea, and in Aurora it is more than a convenience.

```cpp
Desc Shelf(State<Selection>& staged);
```

A staging area holding references gathered across folders before an operation —
NeXT's shelf, which Finder dropped and nobody replaced. Two Aurora-specific
reasons it earns its place:

1. **A drop from the shelf into a sandboxed app is the grant gesture** (decision
   0020). The shelf is where the user assembles what they are about to hand over,
   which makes the grant deliberate instead of incidental.
2. **It carries provenance.** A staged item knows what produced it, so the shelf
   can say "these three came from the same build" — a thing a path list cannot.

## 9. What this language refuses

- **A binding engine.** String paths that fail silently at runtime, when
  late-arriving data is this toolkit's central case.
- **Markup files.** A startup parse on the path whose goal is the first frame.
- **Retained-mode tree diffing.** `State<T>` binds at build time; structural
  change is a widget's job, and the list owns its own.
- **Per-item animation**, spring physics, and cross-fades. See §1.
- **An in-process decoder**, for anything. See §2.
- **A visual designer.** It is what makes XAML worth its ceremony and it would
  cost more than every client we have.

## 10. Open, and where this is most likely wrong

- **`Collection` is unspecified** beyond its name, because its only client is an
  icon view that cannot ship yet. Specifying it now would be designing against
  nothing.
- **The preview domain's lifetime.** One long-lived domain is the plan; whether
  a lease per request is fast enough for arrow-key selection changes is
  unmeasured, and the fallback (a short-lived cache of the last N previews,
  keyed by path and mtime) has its own staleness question.
- **Typo tolerance has no implementation here**, only a borrowed claim from
  Sigma's README. Whether it is worth its complexity in the filter is untested.
- **The watchdog governs transitions but nothing governs the watchdog.** If
  handler timing is itself noisy under load, `Motion::None` could flap. The
  compositor's own flaky ease-out test is a live example of exactly that class of
  problem.
- **`Shortcut` and `Effect` are named but not defined here**; they come from the
  kit and decision 0009 respectively, and the mapping between a Haiku modifier
  mask and a declared shortcut is unwritten.

## 11. What is actually built, as of 2026-11-07

This document is the design, written before the code. The sections above still
describe the intent; this one records what `kits/aurora-ui/ui.h` currently
declares, so the two can be told apart without reading the header.

**Built.** `Label` / `Bold` / `styled` (per-node `TextStyle`: size multiplier,
bold, italic, colour) · `Button` · `Spacer` · `VStack` / `HStack` with per-axis
weight · `padding` · `named` · `ListView` over `Rows`, with columns, selection,
type-ahead, inline rename on F2, and a context menu · `IconView` over the same
`Rows`, with procedurally drawn icons · `Breadcrumb` · `TextField` (caret,
selection, overflow scrolling, clipboard) · `TextView` (read-only multiline,
wrapped and virtualised) · `Reveal` / `RevealX` / `Either` and the `Transition`
vocabulary · `Menu` on its own surface, with the plumber's ordering · `HSplit` /
`VSplit` with a draggable divider and double-click collapse · `TreeView` over a
demand-driven `TreeSource`, flattened to what is visible · one `Scroller` shared
by the list, the grid, the text view and the tree.

`Atlas` (`demo/atlas.cpp`) is built from exactly these and nothing else.

**Specified here and not built.** `ColumnView` (section 3). The `Selection`
value as a struct — selection is still a single index per view (section 4).
Sort-as-snapshot, including clicking a column header to sort (section 5). The
three kinds of search (section 6). The command registry and its four surfaces
(section 7). The shelf (section 8).

**Known gaps that are not in this document.** Multiline *editing* — `TextView`
is the display half; a caret as line and column, selection across lines, and
up/down by visual rather than logical line are the other half. No input-method
editor: composed text needs `text_input_v3`, which `present_wl` does not speak.
No drag and drop (decision 0020 specifies it). No HVIF; icons are drawn from
canvas primitives. `PlacesTree` reads directories synchronously, which the
`TreeSource` contract is already shaped to fix without touching the widget.
