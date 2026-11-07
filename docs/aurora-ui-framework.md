# The Aurora UI framework: a critique of 0023, the research, and what to build

Written 2026-11-06. A design document, not a decision record: it argues with
`contracts/decisions/0023-aurora-toolkit.md`, settles what it can, and ends in a
build order the author will build from. Where it changes a decision in 0023, it
says so in section 1 and the change should be folded back into 0023 before
building. Where a claim is not verified, it says "unverified".

Contents: [0 Summary](#0-summary) · [1 The lift list](#1-the-lift-list-what-0023-gets-wrong-and-right) ·
[2 Research](#2-research) · [3 HIG across eras](#3-human-interface-guidelines-across-eras) ·
[4 Vocabulary](#4-the-vocabulary) · [5 Build order](#5-build-order) ·
[6 Cost and failure](#6-cost-failure-modes-and-where-this-is-most-likely-wrong) · [7 References](#7-references)

---

## 0. Summary

Five disagreements with 0023, in order of weight.

1. **There is no second kit to build, and trying to build one is the 20%-done
   failure 0023 is afraid of.** The substrate 0023 wants the two kits to share
   ("drawing primitives, layout, theme tokens and the app_server") *is*
   `BView`: every drawing call is a `BView` method that becomes an `AS_*`
   opcode, every layout is a `BLayout` over `BLayoutItem`s, and the
   accessibility tree (0017) is read from the `BView` tree the server is sent.
   A kit that shares those and "no widgets" has `BView` leaves and `BLayout`
   containers, which is the Interface Kit with a different API on top. The
   alternative, a kit with its own view class, is the 13k-line rewrite of
   `View/Window/Font.cpp` that `kits/interface/README.md` section 1.4(b)
   already costed and rejected. So: **one kit, two APIs.** The declarative
   layer builds real Haiku views and updates them in place. It may wrap any
   Haiku widget and it writes its own only where Haiku's behaviour is wrong
   for the job. The rule "share no widgets" is replaced by "share the widget
   substrate; own the widgets whose behaviour we need to change". That makes
   the layer *additive with no cliff*: a navigator built on it is a Haiku
   application on the day the layer stalls.

2. **"Composition and modifiers from SwiftUI" plus "refuse retained-mode tree
   diffing" is a contradiction.** A view-as-value API that returns a fresh
   description on every change needs *something* to reconcile that description
   with the `BView`s already on screen; that something is diffing. Without it
   you rebuild (an `AS_VIEW_CREATE` round trip per view, per change) or you
   mutate through handles. The resolution is to mutate through handles:
   **build once, update through typed state cells, diff nothing.** That is
   fine-grained reactivity (Solid, Svelte, Xilem's `Adapt` nodes), not
   SwiftUI, and the document should say so rather than claim a shape it
   refuses the mechanism of. Section 1.2 and 4.1.

3. **`aurora_blur_v1` should not exist.** `docs/visual-system.md` section 3.9
   already chose `ext-background-effect-v1` (wayland-protocols staging, merged
   2025-05) for per-surface blur regions; 0023 proposes a private protocol
   without mentioning it. The staging protocol has exactly the three properties
   0023 demands: the client never receives pixels, the compositor owns radius
   and passes, and the compositor may decline to apply the effect at all,
   which is where "opaque, promoted to glass when there is budget" belongs: it
   is **compositor policy, not client code**. `.glass(.promoted)` as a
   per-view modifier is therefore the wrong scope and the wrong owner.
   Section 1.4.

4. **The flagship widget may not be needed for the first client, and nobody
   has measured.** `BColumnListView` (5,485 lines, compiled, tested in
   `demos/data-grid`) draws only visible rows already; what it does not do is
   virtualise *row objects* or accept late fields. Whether 5,000 `BRow`s with
   empty fields cost 1 ms or 30 ms to add, and whether `BRow::SetField` plus
   `UpdateRow` reflows, is a one-day experiment that decides whether
   `AuroraListView` is stage 2 or stage 7. Section 5 puts that experiment
   first. The honest cost estimate ranges from "fewer pieces than 0023" to
   "the same three", and the experiment picks.

5. **The lift list's attributions are wrong in ways that will make someone
   write code that exists.** Star sizing is `BGridLayout::SetColumnWeight`.
   Anchoring is `B_FOLLOW_*`, which 0023 says and then lists as a lift.
   Named visual states are the `flags` word every `BControlLook::Draw*` takes
   (`B_HOVER`, `B_ACTIVATED`, `B_FOCUSED`, `B_DISABLED`, `B_DEFAULT_BUTTON`),
   and the bitfield is better than a state machine for the orthogonal states
   a control actually has. XAML's value-precedence chain is a runtime
   property system we must not build. Section 1.3.

What 0023 gets right, and this document keeps: the performance contract (with
its numbers, now with a reason for the 8 ms figure, section 2.1); glass as a
quality level; no markup; no binding engine with string paths; a virtualised,
async-backed list *as the data model* even if not as a new widget; "six, not
sixty"; and the instinct that the toolkit is not a framework.

The HIG position (section 3): the 1992 Macintosh and 1995 Windows guidelines
are right about visibility, stability and directness, and the measured
regressions of the flat and touch-first era (hidden navigation, weak
signifiers, deeper menus, vanishing scrollbars, low-contrast glass) are
regressions against *those* principles, each with a number attached. Modern
guidance is better on type, contrast as a gate, motion as feedback, and
accessibility. Aurora takes the 1992 principles, the 2020s craft, and adds the
thing neither era had: measurement.

Cost (section 6): about three weeks for the layer the navigator needs (watchdog,
list or grid experiment, builder, breadcrumb), plus one to two weeks of
compositor work for backdrop blur that is blocked on `compositor/` being free.
The paragraph on where this is most likely wrong is section 6.3.

---

## 1. The lift list: what 0023 gets wrong, and right

### 1.1 The two-kit split is the wrong cut

0023's appendix: "they share drawing primitives, layout, theme tokens and the
app_server, and they share no widgets. If they shared widgets the modern kit
would be a skin; if they shared nothing we would maintain two of everything."

Take the shared list literally against the tree as it stands:

| "shared" item | what it is in this tree | consequence for a "second kit" |
|---|---|---|
| drawing primitives | `BView::FillRect/StrokeLine/DrawString/...`, each an `AS_*` message on the window's link (`View.cpp`, 121 opcodes) | every leaf of the new kit is a `BView`, or the new kit re-implements the link |
| layout | `BLayout`, `BLayoutItem`, `BGroupLayout`, `BGridLayout`, `BSplitLayout`, `BCardLayout`, `layouter/*` (all compiled in; `kits/interface/CMakeLists.txt`) | layout items are `BView`s or `BLayoutItem`s |
| theme tokens | `ui_color()` from `[theme.colors]`, `AuroraControlLook` | fine, shared by construction |
| app_server | the per-window server thread, the dirty region, `BeginUpdate/EndUpdate`, the a11y tree built from `AS_VIEW_CREATE` and `AS_DRAW_STRING` (0017) | a view not sent to the server is invisible to accessibility |

So a kit that shares the top two rows has nothing left to be separate *with*
except its widget classes, and the widgets it would need before any app can be
written in it alone are the expensive ones: a text field (`BTextView` is
`textview_support/*` plus ~6k lines), menus (`Menu*.cpp`, popups as
`xdg_popup`, verified), scroll bars, buttons with the control look. 0023's
"six, not sixty" counts a toolbar and a sidebar as components; it does not
count the text field the breadcrumb needs or the popup the context menu needs,
because those come from Haiku, which is the admission that the kits share
widgets.

The failure mode 0023 names ("a skin") is a real one, but its cure is not
"share no widgets". A skin is what you get when the new layer *only* restyles.
The cure is that the new layer owns *behaviour* where Haiku's is wrong:
the list (Haiku's `BListView` holds a `BListItem` per row and `BColumnListView`
a `BRow`; neither accepts a value that arrives later without an item object
existing first), the breadcrumb (no Haiku equivalent), and the window shell
(backdrop request, message timing, cancellation on navigate-away). Everything
else is a Haiku widget reached through the new API.

**Decision this document makes:** the declarative layer is `aurora::ui`, a
library beside `kits/interface/` that produces and updates real Interface Kit
views. "Kit" keeps the meaning 0013 gives it; this is not a third one.

### 1.2 Composition without diffing: the contradiction, and the resolution

0023 takes "a view is a value; appearance is a chain of modifiers" and refuses
"full retained-mode diffing of a view tree", with invalidation "driven by data
arriving rather than by diffing a tree". For the list's rows that is right and
is just virtualisation. For everything else it leaves the question open: when
the breadcrumb's path changes on navigation, when a toolbar button becomes
disabled, when the sidebar's selection moves, what runs?

The three honest answers:

1. **Rebuild the description and diff it** against the live views (SwiftUI,
   Flutter's element reconciliation, Xilem). Levien's Xilem keeps a retained
   widget tree precisely because "immediate mode" was "oversimplified in a
   number of ways", made "sophisticated layout" difficult, and had "numerous
   papercuts related to sometimes rendering stale state"; the view tree is
   diffed into it [LEVIEN-2022]. Flutter's reconciliation is not a tree diff
   either: "the framework decides whether to reuse elements by examining the
   child list for each element independently using an O(N) algorithm"
   [FLUTTER-INSIDE]. Cost here: a description allocation per change, a key or
   identity scheme, and a reconciler, which is the complexity 0023 refuses.
2. **Rebuild the views** (tear down, create). An `AS_VIEW_CREATE` plus
   `AS_VIEW_SET_STATE` plus `AS_VIEW_SET_FONT_STATE` per view, through the
   port, under the server's lock. Unacceptable per change.
3. **Build once, mutate through handles.** The description is evaluated once
   to produce `BView`s; the places that can change are declared as typed state
   cells at build time, and a cell knows which view property it feeds. Setting
   a cell posts to the window's looper and the handler sets the property and
   invalidates that view. No reconciliation, no keys, no per-change
   allocation, no identity problem.

This document picks 3. It is the model of Solid and Svelte on the web and of
Xilem's `Adapt`/memoize nodes (described in [LEVIEN-2022] as prototype work),
and its cost is one small template (`State<T>`, section 4.1). Its limit is
that *structure* cannot change through a cell: a view that appears or
disappears is a `visible` cell on a view built in advance, and a list whose
length changes is the list widget's job, not the layer's. For a navigator that
is the whole story; for an app whose structure is data-driven it is not, and
that app writes a `BView` by hand or waits for a reconciler that is not
planned.

So the honest precedent line is: **retained `BView` tree (Haiku's), fine-grained
state cells for updates (Solid/Xilem), demand-driven cells for data (Adapton,
section 2.4), and nothing from SwiftUI but the surface syntax.**

### 1.3 Attribution errors that will make someone write code that exists

| 0023 lifts | from | it is already | so |
|---|---|---|---|
| star sizing (`*`, `Auto`, fixed) | XAML | `BGridLayout::SetColumnWeight/SetRowWeight`, `BGroupLayout` weights, `BSize` min/max/preferred with `B_SIZE_UNLIMITED` | expose in the builder; write nothing |
| anchoring and docking | WinForms | `B_FOLLOW_LEFT_RIGHT` and friends, applied by the server on resize (`ApplyWindowResize`, `server.cpp:944`) | 0023 says this itself, then lists it as a lift |
| named visual states | XAML VisualStateManager | the `flags` word of every `BControlLook::Draw*` (`B_HOVER`, `B_ACTIVATED`, `B_FOCUSED`, `B_DISABLED`, `B_DEFAULT_BUTTON`, `B_CLICKED`...) | a bitfield of orthogonal states is the better model: hover and focused and default are not exclusive; a VSM makes them exclusive and then adds "state groups" to undo that |
| control template vs data template | XAML | `BControlLook` (control template, proven by `AuroraControlLook`); a row renderer is the data template | right idea; the mechanism exists |
| environment propagation | SwiftUI | nothing in Haiku propagates font or scale; `be_plain_font` and `ui_color()` are globals | keep, but as a *build-time* parameter (section 4.1), since we build once |
| `ItemsRepeater`'s "virtualisation is first class" | WinUI | `BColumnListView` draws visible rows only; `BListView` too | the missing piece is virtualised *data*, not drawing (section 4.5) |
| compiled, typed bindings (`x:Bind`) | WinUI | nothing | keep: `State<T>` is this |

### 1.4 Lifts to refuse that 0023 takes

**The value-precedence chain** (animation > local > template > style >
inherited > default). This is WPF's `DependencyProperty` system: a runtime
property store with a resolution walk on every read, which is why WPF's
property access is slow and why "which value won" is a debugging genre. Aurora
has three sources: the theme token, the control look, and the explicit
modifier. Explicit wins, then the look, then the token. There is no animation
layer in the kit because the animations that matter (glass promotion, window
open) are the compositor's, and a value that eases in an app is a cell being
set per frame by a timer, which is already expressible.

**`.glass(.promoted)` as a view modifier.** Blur is a property of a Wayland
surface, requested as a region, applied by the compositor. The right owner of
"when" is the compositor: it has the frame stats and the budget, the client
has neither. `ext-background-effect-v1` lets the client set a blur region and
lets the compositor apply or not; `docs/visual-system.md` 3.9 already chose it
and lists the ACKs. So the kit's surface is `Window(...).backdrop(Backdrop::Blur)`
plus `.opaque()` on content regions (the list rows are a document surface and
must not sit on glass, per 0010's two-materials rule), and the promotion curve
and budget rule live in `compositor/`. 0023's private `aurora_blur_v1` is
refused. Unverified: whether the pinned Smithay revision carries the staging
protocol's server bindings; if not, it is a protocol XML and a dispatch
handler, which is still smaller than inventing one.

**The appendix's list of six components.** "Toolbar" is `BGroupView` plus flat
`BButton`s (or Haiku's own `BToolBar`, `src/kits/shared/ToolBar.cpp`, present
in the Vitruvian fork and not yet in this build's file list; compilable in
place, unverified). "Sidebar" is the list widget with a
different row template. "Split view" is `BSplitView`, verified in the kitchen
sink. "Context menu" is `BPopUpMenu`, popups verified as `xdg_popup`. The
honest list of *new* components for the navigator is two: the list (if the
experiment in section 5 says so) and the breadcrumb. The rest is wiring.

### 1.5 What 0023 is missing

- **Keyboard and focus.** Stage 6 of the navigator plan is type-ahead and the
  column browser; Fitts and Hick-Hyman (section 3.2) say the expert path is
  the keyboard. Haiku has `B_NAVIGABLE`, `BWindow::AddShortcut`, and the
  tab-order rules of its HIG. The layer must expose `.shortcut()` and
  `.focusable()` or the navigator's fastest path goes through raw `BView`.
- **The accessibility constraint on custom drawing.** 0017's tree is built
  from `AS_DRAW_STRING` runs, bounded at 512 runs and 64 KiB per view. A list
  that draws cells with `DrawString` per cell is visible to it (40 rows by 5
  columns is 200 runs); a list that caches rows into a bitmap and blits is
  blind to it. This is a hard rule for the list widget and it is not in 0023.
- **The data model for late values.** 0023 says "cells whose data has not
  arrived render a reserved placeholder" and nothing about how a cell learns,
  how the visible range is told to the source, or how work is cancelled.
  That is the whole design of the list (section 4.5).
- **Text input.** The breadcrumb is also an editable path field, which is
  `BTextControl`. Shared widget, again.
- **Drag and drop.** Stage 4 wants file-drop-as-grant. `BView::DragMessage` is
  not wired to Wayland (`kits/interface/README.md` 7). Not the layer's job,
  but a dependency the plan should list.
- **Theme hot reload.** Apps read the theme once. If the layer resolves
  colours at draw time through `ui_color()` rather than caching bytes, a later
  `B_COLORS_UPDATED` works for free. State it as a rule now.
- **Testability.** Every widget must be drivable by a `demos/*/run.sh` with
  decoded-pixel assertions and by `aurora ui tree`. The list's row text must
  appear in the a11y tree; that is the test that the drawing rule above holds.
- **The watchdog is buildable in the kit today.** 0023 says "no frame watchdog"
  and treats it as a compositor job. A kit-side one is a timer around
  `BWindow::DispatchMessage` that logs any handler over a budget. It gives the
  performance contract its instrument (section 5, stage 1).

### 1.6 Scorecard

| 0023 item | verdict |
|---|---|
| three pieces, not a framework | keep the instinct; the pieces change (section 5) |
| `aurora_blur_v1`, private | **refuse**; `ext-background-effect-v1` |
| client never receives backdrop pixels; compositor owns radius; bounded | keep; all three are properties of the staging protocol |
| glass is a quality level, promoted into | keep; move ownership to the compositor |
| declarative in code, no markup | keep |
| SwiftUI's shape | **replace** with build-once, typed state cells |
| refuse tree diffing | keep, and say what replaces it |
| `AuroraListView` as flagship | **measure first** (section 5, stage 2) |
| performance contract 1-7 | keep; item 1 gets a reason and an instrument |
| two kits, no shared widgets | **refuse**; one kit, two APIs |
| composition and modifiers | keep as syntax |
| environment propagation | keep as build-time `Env` |
| control/data template split | keep; it is `BControlLook` plus a row renderer |
| value precedence chain | **refuse** |
| star sizing, anchoring | keep; already here |
| visual state manager | **refuse**; use `BControlLook` flags |
| WinUI inventory as catalogue | keep as a reading list |
| six components | **two new**, four reused |
| refuse binding engine, markup, deep inheritance, designer | keep |

---

## 2. Research

### 2.1 Latency: the number "present immediately" needs

The project's rule is that a target nobody measures is a wish. Here is what has
been measured about perceptible latency, and what it implies for a desktop
with a mouse.

| input | task | threshold | source | verified |
|---|---|---|---|---|
| stylus, direct | dragging a small box | median **2 ms** JND (range 2-7), n=16 | Ng, Annett, Dietz, Gupta, Bischof, CHI 2014 [NG-2014] | yes, from the PDF |
| stylus, direct | dragging a large box | median 6 ms (range 2-16) | same | yes |
| stylus, direct | scribbling (inking) | median 40 ms (range 10-70) | same | yes |
| stylus, direct | inking, realistic tasks | ~50 ms | Annett, Ng, Dietz, Bischof, Gupta, GI 2014 [ANNETT-2014] | abstract only |
| touch, direct | dragging | ~5-10 ms (as summarised by later papers); Ng 2014 says its results were "similar to results found prior by Ng et al." | Ng, Lepinski, Wigdor, Sanders, Dietz, UIST 2012 [NG-2012] | **primary not reached**; cite through [NG-2014] |
| touch, direct | land-on (tapping) | JND 20-100 ms, mean 64 ms (sd 24) | Jota, Ng, Dietz, Wigdor, CHI 2013 [JOTA-2013] | as cited in `docs/visual-system.md` |
| touch, direct | dragging / tapping / combined | 11 / 69 / 40 ms | Deber, Jota, Forlines, Wigdor, CHI 2015 [DEBER-2015] | as cited in `docs/visual-system.md`; PDF unreachable this session |
| **indirect (mouse-like)** | **dragging / tapping / combined** | **55 / 96 / 75 ms** | same | same |
| touch | visual feedback to a press felt simultaneous | guideline 30-85 ms (tactile 5-50, audio 20-70) | Kaaresoja, Brewster, Lantz, ACM TAP 2014 [KAARESOJA-2014] | abstract |
| mouse | Fitts pointing under lag | at 225 ms lag, movement time +64%, errors +214%; lag multiplies the index of difficulty | MacKenzie and Ware, CHI 1993 [MACKENZIE-1993] | abstract |
| any | 0.1 / 1 / 10 s | design heuristics, not thresholds | Miller 1968 via Nielsen 1993 [NIELSEN-1993] | bib only; Nielsen's own page says they restate Miller |
| any | "400 ms Doherty threshold" | **folklore**; the 1982 IBM paper argues productivity rises more than proportionally as response time falls, with no cliff; readers of the paper report no 400 ms figure in it | [DOHERTY-1982] | primary not reached; multiple secondary sources disagree, so unverified either way |
| review | guidelines before 2017 "seldom addressed delays under 100 ms" though they are perceptible and hurt performance | Franke, Attig, Rauh, Krems, HCII 2017 [FRANKE-2017] | abstract |

What this means for Aurora, which is indirect input (a mouse or trackpad on a
desktop) with occasional stylus:

1. **Dragging is the sensitive case, and the indirect JND is about 55 ms.**
   A column-edge drag, a split divider, a scroll-thumb drag, a window move
   must redraw within that. At 60 Hz, two consecutive dropped frames (33 ms)
   plus the compositor's own latency is still under 55 ms for the median
   participant and above it for the sensitive tail. That is the quantitative
   reason for 0023's rule 1 (no handler over 8 ms): half a frame for the app,
   half for the compositor, so that a drag never drops even one frame. The
   rule is right; it was unexplained.
2. **For a press (tapping), the JND is about 96 ms indirect,** so a hover or
   press highlight that lands on the next frame (16-33 ms) is perceived as
   simultaneous. This is why "ease-out on arrival" matters (0010, section 5):
   an ease-in curve that covers 1.2% in the first frame spends 30-60 ms of
   that budget on nothing.
3. **The number for a new window:** nothing in this literature measures
   "window appears after launch". The nearest is Nielsen's 100 ms heuristic
   for "feels instantaneous", which is a restatement of Miller 1968 and not a
   measurement. So the target is stated as **first frame with chrome and names
   within 100 ms of `exec`, and names within one frame (16.7 ms) of a
   navigation click**, the second being a measurement-backed target because
   `readdir` is 0.9-1.8 ms and the JND for the click's feedback is ~96 ms.
   The first is a heuristic and the document says so.
4. **If Aurora ever runs direct touch or a stylus on a tablet, the thresholds
   collapse to 2-10 ms,** below a frame, and only the compositor can meet
   them (a low-latency overlay layer of the kind Ng et al. built). Out of
   scope, recorded so that nobody later claims the desktop numbers for it.

### 2.2 Perceived performance: skeletons are folklore at this scale

The claim "skeleton screens feel faster than spinners" traces to a 2013
anecdote about the Polar app [WROBLEWSKI-2013]. The one controlled test this
research found, Viget's 2017 study (136 participants, mobile, identical load
duration), found the **opposite**: perceived wait 2.82 s for the skeleton,
2.41 s for the spinner, 2.29 s for a blank screen, and the skeleton group
completed the task more slowly; the authors say skeletons "aren't a silver
bullet" and more testing is needed [VIGET-2017] (numbers as quoted by several
secondary sources; the primary page rate-limited this session). A UX
Collective write-up of a smaller test found skeletons slightly shorter "but
not by much" and that "the sample sizes ... are too small to conclude
anything" [UXC-SKELETON]. One academic abstract reports higher perceived speed
with skeletons; its venue and method could not be confirmed. **Verdict: no
reliable evidence either way at the seconds scale; the best controlled study
points against.**

What *is* evidenced about perceived duration: Harrison, Amento, Kuznetsov and
Bell (UIST 2007) and Harrison, Yeo and Hudson (CHI 2010) showed that a
progress bar's pacing and animation change perceived duration, with pauses
early tolerated better than pauses late [HARRISON-2007, HARRISON-2010]. That
is about multi-second operations with a bar.

None of this applies to the navigator's waits. `lstat` for a screenful is
tens of microseconds; the whole directory is 13-43 ms; a batched provenance
call is a few ms plus the bus. These are below any perceived-duration
effect in the literature. The question is not skeleton-versus-spinner, it is
**whether anything moves**. That is answered by the latency JNDs above and by
the 1992 principle of perceived stability (section 3.1), not by loading-state
folklore. Hence:

- No shimmer, no pulse, no placeholder animation. A late cell is empty space
  of the reserved width, and provenance that indexd does not know is the word
  *unknown* (the navigator plan's honesty rule), never blank.
- No spinner for anything under a second. For a query that re-runs for
  seconds (stage 5), a text line in the status area with elapsed time, not a
  bar, because the total is unknown and Harrison's result is that a bar that
  stalls late reads worse than no bar.

### 2.3 Layout algorithms: cost and predictability

| model | mechanism | cost | predictability | where it lives here |
|---|---|---|---|---|
| Cassowary | incremental dual simplex over linear equalities and inequalities with strengths; Badros, Borning, Stuckey, TOCHI 8(4) 2001 [CASSOWARY] | incremental edits are cheap in practice; worst case is the simplex; Apple's Auto Layout is on it and reports linear scaling only since iOS 12 (WWDC 2018 session 220, secondary summaries [WWDC18-220], unverified) | low: an over-constrained system resolves by strengths and the answer is non-local; an infeasible one fails ("BALMLayout failed to solve your layout!" in Haiku's own ALM) | **half in the tree**: upstream Haiku has `src/libs/alm` (Lutteroth, Kim 2007-08, Zeidler 2010) with `BALMLayout` over X/Y tabs [HAIKU-ALM]; the Vitruvian fork this project compiles from carries only its solver, `src/libs/linprog` (`LinearSpec`), and not `alm` itself (checked by `find`). Available to an app that brings `alm` across; not a toolkit container |
| proposal/response (SwiftUI `Layout`) | parent proposes a size, child answers with `sizeThatFits`, parent places with `placeSubviews`; a cache is passed between calls [WWDC22-10056] | one pass per proposal, but a parent may propose several times (ideal, min, max) | medium: local, but the number of proposals a container makes is its own business | not here |
| measure/arrange (WPF) | `Measure` computes `DesiredSize`, `Arrange` assigns a slot; a panel overrides both [WPF-LAYOUT] | two passes; `Grid` with `Auto` and `*` columns re-measures children (known) | medium | `BLayout` is the same shape: `BaseMinSize/BaseMaxSize/BasePreferredSize` then `DoLayout` |
| constraints down, sizes up (Flutter) | "the *only* information that flows from parent to child are the constraints and the *only* information that flows from child to parent is the geometry"; "each render object is visited *at most twice* during layout"; relayout boundaries cut the walk; intrinsics are the exception that risks "quadratic performance in the worst case" [FLUTTER-INSIDE] | linear initial, sublinear update | high, by construction | not here, but the rule generalises |
| anchoring (`B_FOLLOW_*`) | parent resize moves and resizes children by edge rules; no pass | O(views) arithmetic | highest | here, applied by the server |

Position: **the navigator's layout is anchoring plus one grid**, and the list
must never be a layout item per row. Concretely:

- The window is a `BGridLayout` of two rows (toolbar `Auto`, content weight
  1) and the content row a `BSplitView` (sidebar 240 logical, content weight
  1). That is one `BLayout` pass with a handful of items, measured by the
  kitchen sink as needing nothing from the server but the size-limits reply.
- Inside the list, row `i` is at `i * rowHeight`; column `j` is at the
  prefix sum of column widths. Arithmetic, no layout items, no intrinsic
  queries. Fixed row height is the same rule that makes Flutter's layout
  sublinear (a child that cannot change size does not propagate) and it is
  0023's "nothing reflows" rule from the other side.
- Cassowary is refused *for the toolkit's own containers*, because its answer
  to over-constraint is non-local and a first frame must not depend on a
  solver's iteration count. An application that wants ALM links it; nothing
  in the layer prevents that.
- Flutter's rule is adopted as the *test* for any new container: visited at
  most twice, no intrinsic query into a child whose size is fixed.

### 2.4 Retained, immediate, incremental

Muratori's 2005 immediate-mode argument is about where state lives: the caller
supplies it each frame, so there is no widget tree to set up and keep in sync
[MURATORI-2005]. The Dear ImGui maintainers' own caveat is the relevant one
here: "in games, most UI is known at compile or load time, so most
immediate-mode advantages do not hold outside dynamic data structures like
lists" [IMGUI-WIKI]. Levien's retrospective on Xilem gives the other half:
immediate mode makes layout hard and leaks stale state, so the widget tree is
retained and the view description is diffed into it [LEVIEN-2022]. Adapton
(Hammer, Khoo, Hicks, Foster, PLDI 2014) names the principle the list needs:
demand-driven incremental computation, where "programs only recompute
computations as demanded by observers" [ADAPTON].

Put together, for this tree:

- **The widget tree is retained and is Haiku's.** It is what the server
  renders, what 0017 reads, and what hit-testing and focus already work on.
- **Updates are fine-grained** (state cells), not diffed (section 1.2).
- **Row data is demand-driven**: the list observes a range, and only that
  range is computed, fetched, or stat'ed. Scrolling changes the demanded set;
  work outside it is cancelled by not being demanded. This is Adapton's
  dirtying/propagation split applied to a directory: `readdir` dirties every
  row cheaply; `lstat` propagates only to observed rows.
- **Rows draw immediate-mode inside one view.** A row template is a function
  that draws a row into the list's `BView` with `DrawString` per cell. This
  is what `BColumnListView` already does, it keeps the a11y tree fed, and it
  is where immediate mode is strongest (dynamic data, no per-row state).

---

## 3. Human interface guidelines across eras

The person asking invited the view that modern HIGs regress from an earlier
era. Here is the case, made from the primary texts, and then the limits of
that case.

### 3.1 What the 1992 and 1995 texts say

*Macintosh Human Interface Guidelines*, Apple, Addison-Wesley 1992, chapter 1
[APPLE-HIG-1992], read from the scanned PDF:

- **See-and-point**: "two basic assumptions: that users can see on the screen
  what they're doing and that users can point at what they see."
- **Direct manipulation**: "an object on the screen remains visible while a
  user performs physical actions on the object. When the user performs
  operations on the object, the impact of those operations on the object is
  immediately visible."
- "Users want to see what actions are available at any given moment. If grave
  consequences might follow from any of those actions, they want to know about
  those consequences — before any damage is done and while they can still
  change their minds."
- **Feedback and dialog**: "Provide feedback as they do tasks and make that
  feedback as immediate as possible."
- **Forgiveness**: "frequent alert boxes are a good indication that something
  is wrong with the program design."
- **Perceived stability**: "Even when particular actions are unavailable, they
  are not eliminated from a display but are merely dimmed." And: "it is the
  perception of stability that you want to preserve."
- **User control**: "Allow the user, not the computer, to initiate and control
  actions."

*The Windows Interface Guidelines for Software Design*, Microsoft, February
1995 pre-release, chapter 1 [MS-WIG-1995], read from the PDF:

- **Directness**: "Visibility of information and choices also reduce the
  user's mental workload. Users can recognize a command easier than they can
  recall its syntax."
- **User in control**: "Avoid modes whenever possible ... make certain the
  mode is obvious, visible, the result of an explicit user choice, and easy to
  cancel."
- **Feedback**: "Nothing is more disconcerting than a 'dead' screen that is
  unresponsive to input."
- **Simplicity**, on progressive disclosure: it "does not imply using
  unconventional techniques for revealing information, such as requiring a
  modifier key as the only way to access basic functions or forcing the user
  down a longer sequence of hierarchical interaction. This can make an
  interface more complex and cumbersome."

*Haiku Human Interface Guidelines* (the BeOS lineage; the Be Book itself is an
API reference and Be published no separate HIG this research could find)
[HAIKU-HIG]:

- "Fitts's Law ... Screen corners and edges are easy targets ... teeny controls
  slow users down."
- "There is not nor will there ever be a busy cursor in BeOS-based operating
  systems." (Responsiveness is a thread-per-window property, not a cursor.)
- "Submenus should be avoided when other options exist."
- "Controls should never overlap." "Never ask 'Are you sure you want to
  quit?'"

The through-line is one sentence: **what you can do must be visible, what you
do must respond at once, and the screen must not move under you.**

### 3.2 Fitts's law and Hick-Hyman, used properly

**Fitts.** Movement time to a target is `MT = a + b·log2(A/W + 1)` in the
Shannon form MacKenzie established for HCI, where `A` is distance and `W` is
target width along the axis of approach [MACKENZIE-1992, SOUKOREFF-2004].
Three consequences that are design rules, not decoration:

- A target on a screen edge has unbounded `W` along the approach axis; a
  corner has it on two axes. Tognazzini, February 1999: "since the menu bar
  lies on a screen edge, it has an infinite height" and the easiest pixels
  are "the four corners of the screen" plus "the current location of the
  mouse pointer" [TOG-1999]. Hence: pop-up menus at the pointer (`A ≈ 0` for
  the first item) and a dock on an edge, which Aurora already has.
- `W` is the target's extent *where the pointer arrives*. A 24-logical-pixel
  row versus an 18-pixel row at `A = 300`: `log2(300/18+1) = 4.14` bits
  against `log2(300/24+1) = 3.75`, about 10% of the movement term. Larger
  rows cost density (25% fewer rows per screen), and density costs scrolling,
  which is another Fitts task. The trade is real and is settled in 3.5.
- Lag multiplies the index of difficulty [MACKENZIE-1993]. Latency is a Fitts
  cost, which is why section 2.1 and this section are the same argument.

**Hick-Hyman.** Choice reaction time among `n` equally likely alternatives is
`RT = a + b·log2(n + 1)` [HICK-1952, HYMAN-1953]. Seow's 2005 review explains
why it gained little traction in HCI compared with Fitts: the information
measure is hard to compute for real menus, and it only models the *decision*
of a practised user [SEOW-2005]. Cockburn, Gutwin and Greenberg's CHI 2007
menu model is the usable form: novice time is *visual search*, roughly linear
in item count; expert time is Hick-Hyman decision plus Fitts pointing; and
the model predicted measured performance across menu designs [COCKBURN-2007].
Landauer and Nachbar (CHI 1985) found selection time per level `k + c·log b`,
which favours **breadth over depth** [LANDAUER-1985].

Consequences:

- Splitting a menu into two levels adds a decision, a pointing movement, and
  a disclosure delay, for both novices and experts. Windows 11's "Show more
  options" is this exact regression and Microsoft is now reversing it
  (section 3.3).
- A long *visible* menu is cheap for experts (log) and linear for novices;
  grouping with separators reduces the novice's effective `n` because search
  is over groups first. So: flat, grouped, visible.
- The expert path should bypass pointing: type-ahead in a list collapses
  Hick-Hyman's `n` to the alternatives matching the typed prefix and Fitts's
  `A` to zero. This is stage 6 of the navigator plan and it is the
  highest-leverage speed feature there is.

### 3.3 Where modern guidance regressed, with numbers

| regression | principle broken | measurement | source |
|---|---|---|---|
| hidden navigation (hamburger) on desktop | see-and-point; recognition over recall | discoverability drop "more than 20%"; desktop users at least **39% slower** with hidden navigation; mobile 15% slower; navigation use on desktop 27% hidden vs 48% visible; n=179, 6 sites | Pernice and Budiu, NN/g, June 2016 [NNG-HAMBURGER] |
| flat, weak signifiers | see-and-point ("users want to see what actions are available") | **22% more time on task, 25% more fixations** on weak-signifier pages; 71 participants, 9 live pages, eye tracking, p < 0.05; the authors note full-task slowdowns would be smaller | Moran, NN/g, September 2017 [NNG-FLAT-2017] |
| touch-first on the desktop (Windows 8) | user in control; density; no hidden modes | "weak on tablets, terrible for PCs"; hidden commands, low information density, error-prone gestures; 12 experienced users | Nielsen, NN/g, November 2012 (press summaries; primary not fetched) [NNG-WIN8] |
| two-level context menu (Windows 11) | breadth over depth; progressive disclosure must not add a hierarchy level | an extra click Windows 10 "never required"; Microsoft announced nested submenus and customisation after sustained complaints, 2025 | press coverage [WIN11-MENU]; Hick-Hyman and [LANDAUER-1985] for why |
| vanishing scrollbars (OS X Lion, 2011) | perceived stability; see-and-point | position-in-document cue lost; "a smaller, harder-to-hit target" even when shown; Apple added an always-show preference | contemporary reviews [LION-SCROLL] (opinion, not a study) |
| translucent chrome under text (Liquid Glass, 2025) | legibility | Apple reduced transparency in iOS 26 beta 3 after readability complaints; the HIG now advises a dimming layer and thicker materials for text | press coverage of the beta cycle; Apple HIG "Materials" [APPLE-MATERIALS] |
| designers' retreat from their own principles | all of the above | Norman and Tognazzini, 2015: discoverability, feedback, recovery, undo named as abandoned | [NORMAN-TOG-2015] (essay, not a study) |

Two honest limits on the regression case. First, the NN/g flat-design result
is criticised as comparing *bad* flat design against better non-flat design,
i.e. it measures signifier strength, not flatness as such; Moran says the
same. The lesson is therefore "keep signifiers", not "add bevels". Second,
the 1992 Macintosh's strong signifiers came partly from a 1-bit palette that
forced them; the principle survives the palette, the look does not.

### 3.4 Where modern guidance is better

- **Contrast as a gate.** WCAG 2's ratios are a measurable floor the 1992
  text had no equivalent for; this tree already asserts them on pixels
  (`kits/interface/tests/test_theme.cpp`, `tests/indicator.rs`).
- **Type and spacing.** Modern HIGs' type scales and generous spacing are
  readability improvements; the 1992 Chicago-at-12-point era was constrained
  by the screen, not chosen.
- **Motion as feedback, with reduced-motion as a right.** 0010 section 5 has
  the curves and the honest statement of where the research runs out.
- **Accessibility as structure, not afterthought.** The 1992 text has a short
  accessibility section; modern platforms expose a tree. Aurora's 0017 is in
  the modern tradition.
- **HiDPI and layout units.** Nothing in 1992 scaled.
- **Keyboard focus rings as a first-class, themed element.** Aurora's
  two-pixel accent ring is modern.

### 3.5 Aurora's position: the rules for the navigator

Principles of 1992, craft of the 2020s, and measurement. Concretely:

1. **Everything that can be done is visible or one keystroke away.** No
   hamburger, no hidden toolbar, no gesture-only command. The sidebar is
   always visible; the toolbar is always visible.
2. **Nothing is removed, only dimmed.** A verb the plumber ranks as
   unavailable is shown dimmed with its effect class; a destructive verb is
   visible, marked, and separated by distance from the default (forgiveness
   through Fitts: distance is the safety).
3. **Context menu at the pointer, flat, grouped, ranked default first.**
   `A ≈ 0` for the default; no second level for "more". All verbs from
   `registry.plumb@v1`, effects marked inline.
4. **Scrollbars always visible**, Haiku's `B_V_SCROLL_BAR_WIDTH` (14 logical
   px), the pill thumb at 3:1 against the panel (already asserted).
5. **Row height is the font's line height plus 6 logical px**, which is
   about 22-23 at 12-point Noto Sans (ascent 12.8 + descent 3.5 + 6), not a
   44-px touch target. The 10% Fitts cost against an 18-px row buys a
   readable row; the 25% density cost of a 30-px row buys nothing on a
   desktop. Fixed from the first frame.
6. **Type-ahead selects; it never opens a dialog.** The expert path has no
   pointing in it.
7. **Feedback within one frame for anything under the pointer.** Hover,
   press, drag, selection: the handler budget is 8 ms and it is measured.
8. **Nothing moves when data arrives.** Reserved widths, fixed heights, no
   shimmer; *unknown* for provenance the index lacks.
9. **Text sits on an opaque surface.** List rows and the breadcrumb field are
   `document`; glass is for chrome (toolbar, sidebar background) and only
   when the compositor grants it. This is 0010's two-materials rule.
10. **Strong signifiers on a quiet surface.** Buttons have a border at 3:1;
    the focus ring is two pixels; links are coloured. Quiet is not flat.
11. **No alerts for the recoverable.** Delete is the destructive verb,
    marked, undoable where the plumber says it is; "Are you sure you want to
    quit?" never.
12. **Every rule above is a test** (section 5).

---

## 4. The vocabulary

Sketches are C++17 in the style of 0023's appendix. Namespace `aurora::ui`.
They are the author's intent and will change when they meet the compiler;
section 6.3 says where.

### 4.1 The model: build once, state cells, environment

```cpp
namespace aurora::ui {

struct Env {                 // build-time, flows down; override per subtree
    const BFont* font = be_plain_font;
    float        scale = 1.0f;   // layout units per logical px (from the window)
    float        unit  = 8.0f;   // spacing unit, logical
    color_which  text  = B_PANEL_TEXT_COLOR;
};

// A description. Materialise() is called once per build and returns a real
// BView that the parent takes ownership of (Haiku's rule). Nothing is kept
// that could dangle: updates go through State<T> below, which addresses the
// view by looper message, never by raw pointer from another thread.
struct Node {
    virtual ~Node() = default;
    virtual BView* Materialise(const Env&) = 0;
};
using Desc = std::shared_ptr<Node>;   // cheap to pass; built into a tree once

// A typed cell. Set() from any thread posts to the owning window's looper;
// the bound updater runs there, under the window lock, and invalidates
// exactly the view it feeds. No diffing: the binding was made at build time.
template <class T>
class State {
public:
    explicit State(T initial);
    const T& Get() const;              // window thread only
    void     Set(T v);                 // any thread; coalesced per cell
    // Called by widgets at Materialise(); runs on the window thread.
    void     Bind(BMessenger window, std::function<void(const T&)> apply);
};

}   // namespace aurora::ui
```

What this refuses and why: no `Body()` re-evaluation, no view identity, no
keys. The price is that structural change is the widget's job. The navigator
has one structurally dynamic thing, the list, and it owns that.

### 4.2 Primitives and modifiers

Primitives are the Haiku widgets, reached by name; modifiers return a `Desc`
and apply at `Materialise()`.

```cpp
Desc Label(std::string);                    // BStringView
Desc Label(State<std::string>&);            // bound: Set() updates the text
Desc Button(std::string, std::function<void()>);   // BButton; flat variant via .flat()
Desc TextField(State<std::string>&, std::function<void(const std::string&)> onSubmit);  // BTextControl
Desc Spacer();                              // BSpaceLayoutItem glue
Desc Separator();                           // BSeparatorView

Desc HStack(std::initializer_list<Desc>);   // BGroupView horizontal
Desc VStack(std::initializer_list<Desc>);   // BGroupView vertical
Desc Grid(Rows, Cols);                      // BGridLayout; weights are star sizing
Desc Split(Desc left, float leftWidth, Desc right);   // BSplitView; the divider drags

// Modifiers (each is a value returning a value; applied in order at build)
d.padding(float)            d.padding(Insets)
d.weight(float)             // BGroupLayout/BGridLayout weight
d.minSize(BSize) d.maxSize(BSize) d.explicitSize(BSize)
d.anchor(Anchor)            // B_FOLLOW_* for views outside a BLayout
d.font(const BFont&)        d.color(color_which)       // ui_color() at draw time, not bytes
d.visible(State<bool>&)     d.enabled(State<bool>&)
d.focusable()               d.shortcut(char key, uint32 modifiers)
d.tooltip(std::string)
d.name(const char*)         // the BView name, which is what 0017's tree shows
```

The `.name()` modifier is not cosmetic: `aurora ui tree` and every `run.sh`
find views by name. A view without a name is untestable.

### 4.3 Components for the navigator

| component | what | new code? |
|---|---|---|
| window shell | `AuroraWindow : BWindow`: message timing watchdog, backdrop request, cancellation token for workers, `B_QUIT_REQUESTED` that cancels everything first | yes, small |
| toolbar | `HStack` of flat `Button`s (or Haiku's `BToolBar`, `src/kits/shared/ToolBar.cpp`, compiled in place; in the fork, not yet in this build; unverified) | no |
| sidebar | the list widget with a one-column template over a `Places` source | no (template) |
| content | the list or grid (section 4.5) | **experiment decides** |
| breadcrumb | segments as flat buttons; click on empty space or `Cmd+L` swaps in a `BTextControl` with the path; Escape swaps back | yes |
| status line | `Label(State<std::string>&)` | no |
| context menu | `BPopUpMenu` built from `registry.plumb@v1`'s ranked list, effects marked by a glyph and a dim colour, default item bold, destructive items after a separator | no (a builder function) |
| split | `BSplitView` | no |

Two new components, as section 1.4 says, plus the shell.

### 4.4 Layout model

Anchoring plus `BLayout`, as section 2.3 argues. The navigator's shell:

```cpp
auto shell = Grid(Rows{Auto, Star(1)}, Cols{Star(1)})
    .cell(0, 0, HStack({ Button("Back", back).flat(), Button("Up", up).flat(),
                         Breadcrumb(path, navigate).weight(1) }).padding(6))
    .cell(1, 0, Split(Sidebar(places).name("places"), 240,
                      FileList(rows).name("files")));
```

Rules a new container must meet, taken from Flutter's guarantee and made a
test: a child is visited at most twice per layout; no container queries a
child's intrinsic size if that child declared a fixed size; the list reports
`B_SIZE_UNLIMITED` max and a fixed min, and never a preferred size that
depends on its row count.

### 4.5 The list: the data model is the design

Whether the drawing widget is `AuroraListView` or `BColumnListView` with late
fields, the data model is this, and it is the part 0023 did not specify.

```cpp
namespace aurora::ui {

enum class Cell : uint8_t { Ready, Pending, Unknown };   // Unknown renders the word

struct CellValue { Cell state; std::string text; /* or a typed variant later */ };

struct Rows {                       // the demand-driven source
    virtual ~Rows() = default;
    virtual int32     Count() const = 0;                      // known after readdir
    virtual CellValue Cell(int32 row, int32 col) const = 0;   // never blocks
    // The widget tells the source what it is looking at, including a margin
    // in the direction of travel. The source fetches that and only that;
    // rows outside the range are not fetched, and a worker checks the range
    // before each unit of work, which is how navigate-away cancels.
    virtual void      Want(int32 first, int32 last) = 0;
    // The source tells the widget what changed; the widget invalidates only
    // those rows that are visible. Called on any thread; delivered on the
    // window thread.
    std::function<void(int32 first, int32 last)> changed;
};

struct Column { std::string title; float width; alignment align; };

class ListView : public BView {     // if the experiment says we need it
public:
    ListView(const char* name, std::vector<Column>, Rows&);
    void    Draw(BRect update) override;        // rows first..last of `update`, DrawString per cell
    void    MouseDown(BPoint) override;         // select; double-click activates
    void    KeyDown(const char*, int32) override;   // arrows, type-ahead buffer with timeout
    BSize   MinSize() override; BSize MaxSize() override;   // never depends on Count()
    float   RowHeight() const;                  // fixed at attach from the font
    // The visible range is derived from Bounds() and RowHeight(); on scroll
    // it calls rows.Want(first - margin, last + margin) with the margin on
    // the side the scroll moved toward.
};

}
```

Contract, each line a test:

- `Cell()` never blocks and never does I/O. `Want()` is the only hint. A
  source that needs `lstat` keeps a worker that drains the wanted range
  nearest-first and re-reads the range between files.
- `changed()` arriving for rows outside the visible range costs one
  comparison and no invalidation.
- Row height is fixed at `AttachedToWindow()` from the font and `Env`; a
  column width is fixed at creation or by the user dragging; neither changes
  when a value arrives.
- Every visible cell is drawn with `DrawString`, so `aurora ui text` on the
  view returns the visible rows in reading order (0017's bound of 512 runs
  per view is 100 rows of 5 columns; a 4K display at scale 1 with 24-px rows
  shows ~90 rows, so the margin is thin and a two-column sidebar must be its
  own view).
- Selection is a `State<std::vector<int32>>`; the context menu and the status
  line bind to it.
- `Want()` is called at most once per scroll event, with hysteresis of a few
  rows, so a jittering pointer does not thrash the worker.

Provenance is a second `Rows`-like source layered on the first, with
`Want()` batching the visible range into **one** `registry` call (0023 rule
6), never a process per row.

### 4.6 The window and the backdrop

```cpp
class AuroraWindow : public BWindow {
public:
    AuroraWindow(BRect, const char* title, Desc content, Backdrop b = Backdrop::Opaque);
    void DispatchMessage(BMessage*, BHandler*) override;   // timed; see stage 1
    CancelToken NewWork();        // a token workers check; QuitRequested() cancels all
};
enum class Backdrop { Opaque, Blur };   // Blur = a region request via ext-background-effect-v1;
                                        // the compositor decides whether and when
```

The `Blur` request is a region: the window's frame minus every view marked
`.opaque()`, which the shell marks for the list and any text field. The
presenter (`kits/interface/appserver/present_wl.c`) binds the manager and sets
the region on commit; the compositor applies it when its budget allows and
eases the tint in over 200 ms with `enter-emphasised` (0010 section 5.3's
window-open duration). The client cannot tell whether it was granted, which is
the point: text contrast is asserted for both states.

### 4.7 File layout

```
kits/aurora-ui/                 new; does not touch kits/interface/
  CMakeLists.txt                target aurora_ui, links haiku_iface_real + aurora_appserver
  ui.h                          umbrella
  env.h                         Env
  node.h  node.cpp              Node, Desc, Build(), the modifiers' application
  state.h                       State<T>, the looper-message delivery
  widgets.h  widgets.cpp        Label/Button/TextField/Stacks/Grid/Split wrappers
  list_view.h  list_view.cpp    Rows, Column, ListView (stage 2b only if needed)
  breadcrumb.h  breadcrumb.cpp
  window.h  window.cpp          AuroraWindow, watchdog, CancelToken, backdrop request
  menu.h  menu.cpp              PopupFromPlumb(): BPopUpMenu from registry.plumb@v1
  tests/                        ctest, headless (AURORA_APPSERVER_PRESENT=0), pixel + tree
demos/navigator/                the client; run.sh with decoded-pixel and ui-tree checks
  navigator.cpp
  sources/dir_rows.cpp          readdir + lstat worker implementing Rows
  sources/provenance_rows.cpp   batched registry call implementing Rows
```

---

## 5. Build order

Each stage ends in something a reviewer runs. Each is shippable alone. The
order is by what removes the most uncertainty per day, not by feature size.

**Stage 0: prerequisites not in this leg.** The hang-on-close bug (4 of 10 for
`iface_controls`, main thread in a futex wait): the navigator is opened and
closed more than any app, so it must be fixed first regardless. The global
server lock split, in flight. Both block measurement, not design.

**Stage 1: the instrument (about one day).** `AuroraWindow::DispatchMessage`
times every handler; `AURORA_UI_TRACE=1` prints per-`what` p50/p95/max and
the count over 8 ms, in the compositor's `--frame-stats` CSV style. A ctest
runs the kitchen sink under it and asserts the count over 8 ms is zero except
for a named allowlist (`BColumnListView` column drag's `CopyBits` is a
candidate). *Verifiable by:* the CSV, and a deliberately slow handler in a
test that trips it. *Ships alone:* every Haiku app on Aurora gets it by
subclassing. This is the frame watchdog's app-side half, and it exists before
any widget is written, because a target nobody measures is a wish.

**Stage 2a: the grid experiment (one day).** `demos/data-grid` extended: add
5,000 and 50,000 `BRow`s with empty fields; time `AddRow` loop, first draw,
and a `SetField` + `UpdateRow` sweep over the visible rows; capture before and
after and assert zero pixel movement outside the updated cells. Numbers to
beat: `AddRow` loop under 8 ms for 5,000 rows, update sweep under 2 ms, no
reflow. *Decides:* whether stage 2b happens now or after stage 7.

**Stage 2b: `ListView` (two to three days, only if 2a fails).** The widget in
4.5 over a synthetic `Rows` of 100,000 entries whose cells arrive on a timer.
*Verifiable by:* headless test that a scroll of N rows repaints only the band
(server trace of `AS_VIEW_INVALIDATE_RECT`), `aurora ui text` returns exactly
the visible rows, p95 of `Draw` under 4 ms at 40 visible rows, `Want()`
called once per scroll event, and a navigate-away test where a worker with a
200 ms-per-row delay is observed to stop within one row.

**Stage 3: navigator stage 1 (two days).** `demos/browser` rebuilt as
`demos/navigator` on the shell: `readdir` on the window thread (it is under
2 ms), `lstat` worker driven by `Want()`, provenance batched. *Verifiable by:*
time from `exec` to the first compositor capture containing the directory's
names, measured by `run.sh` with the compositor's capture timestamps, target
under 100 ms; time from a navigation click (`--test-input`) to names of the
new directory on screen, target one frame; the watchdog CSV with zero
over-budget handlers; the a11y tree listing visible rows only.

**Stage 4: the builder (three to four days).** `Node`, `State<T>`, `Env`,
the wrappers in 4.2 and 4.4, only as far as the navigator's shell uses them.
*Verifiable by:* a kitchen-sink page written twice, once with
`BLayoutBuilder` and once with the builder, **pixel-identical** in the
compositor's capture, since both produce the same `BView` tree; a `State<T>`
set from a worker thread updates a label within one message dispatch and
never touches the view off the window thread (assert via a lock-held check
in debug builds).

**Stage 5: the breadcrumb (two days).** Segments and the editable field.
*Verifiable by:* a click on a segment navigates (capture shows the parent's
names); `Cmd+L`, typed path, Enter navigates; Escape restores; the a11y tree
shows segments as `text` runs with the path's components.

**Stage 6: the context menu from the plumber (one to two days).**
`PopupFromPlumb()`: ranked, default bold and first, effects marked, destructive
after a separator. *Verifiable by:* a popup capture against a headless
reference; the destructive item's distance from the default asserted in
layout units.

**Stage 7: backdrop (one day kit-side; three to five days compositor-side,
blocked on `compositor/` being free).** `ext-background-effect-v1` in the
presenter and the compositor; the promotion policy (opaque on map, blur when
the last 60 frames' p95 is under a threshold from the theme, eased in over
200 ms; demotion is a later record). *Verifiable by:* a capture showing blur
under the toolbar region and none under the list; text contrast asserted in
both states; a frame-stats run showing the window's first frames carry no
chain.

**Later, by the navigator plan's stages:** type-ahead and the column browser
(these are `ListView`/grid features plus a second list), queries as windows,
live queries.

Rough total for stages 1-6: **two to three weeks** of the author's time, with
2b the swing item. Stage 7 adds one to two weeks and is mostly compositor.

---

## 6. Cost, failure modes, and where this is most likely wrong

### 6.1 Is the second kit a mistake?

As 0023 words it, yes: a kit that shares the `BView` substrate and no widgets
is either the Interface Kit with a builder on top (in which case call it that)
or a second `BView` (in which case it is the 13k-line rewrite already
refused). The thing worth building is a layer, and it is safe because it has
no cliff: every piece is a real Haiku view, so a navigator half-built on it
is a working Haiku application, not a half-built kit. The failure mode 0023
fears, 20% done and worse than nothing, cannot happen to a layer whose
absence is "write the `BView` by hand".

### 6.2 Failure modes that remain

- **The builder grows a reconciler.** The moment someone wants a data-driven
  structure outside the list, the pressure to add "rebuild the description
  and diff it" arrives, and with it keys and identity. The guard: structural
  change is a widget's job; a second structurally dynamic widget is written
  as a `BView` with a `Rows`-like source, not by generalising the builder.
- **Text shaping on the scroll path.** Every `DrawString` shapes through
  HarfBuzz in the server (`fonts.cpp`); a glyph cache exists
  (`GlyphKey`, `server.cpp:615`) but a *shaped-run* cache is unverified. Two
  hundred cells per repaint at tens of microseconds each is a few
  milliseconds, which is most of the 8 ms budget at 1.45 scale. If stage 2b's
  p95 fails, the fix is a run cache keyed by (font, size, string) in the
  server, which is in `kits/interface/`, held by another agent today.
- **The a11y bound.** 512 runs per view is one tall screen of a five-column
  grid. The sidebar is its own view and the grid's columns are capped at what
  fits; a 6-column grid on a 4K display at scale 1 would truncate the tree.
  Record it; raise the bound when it bites.
- **`BColumnListView` is half a fit.** If stage 2a passes, we take its sort,
  column drag and title row and give up rounded rows and a flat header (its
  drawing is its own, "a fork, not a subclass", README section 12). If it
  fails, we write the list and lose column drag until written. Either way the
  honest statement is that the first navigator does not look exactly like the
  mock, and that is the right trade.
- **Hot reload stays off.** Apps read the theme once. The layer's draw-time
  `ui_color()` rule makes `B_COLORS_UPDATED` possible later; it does not make
  it exist.

### 6.3 Where this design is most likely wrong once it meets the C++

This is the paragraph to come back to.

**Ownership and lifetime.** `BView`s are owned by their parent and deleted by
`BWindow`; a `State<T>` bound at build time must not hold a `BView*` that a
worker thread's `Set()` could chase after the window is gone. The sketch
routes through `BMessenger` and the looper, which is correct, but the
*binding closure* captures something, and if it captures the view pointer
the dangling is just one hop away. Expect this to be wrong on the first
attempt, and expect the fix to be a view token resolved under the window lock
in the handler, with a "gone" path. Also: `State<T>::Set()` before the
window exists, and during `Quit()`.

**The looper path's latency is unmeasured.** A worker posts `changed()`;
the window's looper wakes; the handler invalidates; the server thread paints
at `EndUpdate`. Three hops through ports under a lock that is being split.
If that path is hundreds of microseconds it is fine; if it serialises behind
a long draw it is the thing that makes "names appear, then sizes fill in"
visibly late. Stage 3 measures it; nothing here assumes the answer.

**`Env` as build-time only.** The first time the user changes scale with a
window open, a build-time environment is wrong and every size cached from it
is stale. The presenter reads the output scale once at bind
(`present_wl.c`, `out_scale`) and the port carries it "end to end" for the
first frame; nothing handles a change in flight, and Haiku has no event for
one. The design accepts this today and should not pretend otherwise.

**The grid experiment could pass for the wrong reason.** 5,000 `BRow`s might
add in 3 ms and then sorting on a late field could re-sort the whole list
under the pointer, which is exactly the reflow the contract forbids. The
experiment must include "a late value arrives in the sort column" and assert
nothing moves until the user asks to sort.

**The watchdog might be the wrong boundary.** Timing `DispatchMessage` misses
the server thread, where drawing actually happens, and the server is in
`kits/interface/`. The kit-side timer is what can be built now; the real
number is the one from the server's `EndUpdate` to the presenter's commit,
and it should be added when that directory is free.

---

## 7. References

Tagged as cited above. "verified" means the primary text was read this
session; "abstract" means only the abstract or a bibliographic record was;
"secondary" means press or a summary; "in-house" means the citation is
carried from `docs/visual-system.md`, whose author verified it.

**Latency and perception**

- [NG-2014] Albert Ng, Michelle Annett, Paul Dietz, Anoop Gupta, Walter F.
  Bischof, "In the blink of an eye: investigating latency perception during
  stylus interaction", CHI 2014, pp. 1103-1112, DOI 10.1145/2556288.2557037.
  https://sites.ualberta.ca/~wfb/publications/C-2014-SIGCHI-Latency.pdf
  — verified from the PDF: 16 participants; dragging JND median 2 ms (small
  box) and 6 ms (large box), scribbling median 40 ms; 1 ms baseline.
- [NG-2012] Albert Ng, G. Julian Lepinski, Daniel Wigdor, Steven Sanders,
  Paul Dietz, "Designing for low-latency direct-touch input", UIST 2012,
  pp. 453-464, DOI 10.1145/2380116.2380174 — **primary not reached**; the
  5-10 ms touch-dragging figure is as summarised by later papers; [NG-2014]
  says its own results were "similar to results found prior by Ng et al."
- [ANNETT-2014] Michelle Annett, Albert Ng, Paul Dietz, Walter F. Bischof,
  Anoop Gupta, "How low should we go? Understanding the perception of latency
  while inking", Graphics Interface 2014, pp. 167-174.
  https://graphicsinterface.org/proceedings/gi2014/gi2014-22/ — abstract:
  inking ~50 ms versus ~2-7 ms for non-inking.
- [JOTA-2013] Ricardo Jota, Albert Ng, Paul Dietz, Daniel Wigdor, "How fast
  is fast enough? A study of the effects of latency in direct-touch pointing
  tasks", CHI 2013, pp. 2291-2300, DOI 10.1145/2470654.2481317 — in-house.
- [DEBER-2015] Jonathan Deber, Ricardo Jota, Clifton Forlines, Daniel Wigdor,
  "How Much Faster is Fast Enough? User Perception of Latency & Latency
  Improvements in Direct and Indirect Touch", CHI 2015, pp. 1827-1836, DOI
  10.1145/2702123.2702300 — in-house (indirect 55/96/75 ms; direct
  11/69/40 ms); the lab's PDF mirror refused connections this session.
- [KAARESOJA-2014] Topi Kaaresoja, Stephen Brewster, Vuokko Lantz, "Towards
  the temporally perfect virtual button", ACM TAP 11(2), 2014, DOI
  10.1145/2611387. https://eprints.gla.ac.uk/104653/ — abstract: visual
  feedback guideline 30-85 ms.
- [MACKENZIE-1993] I. Scott MacKenzie, Colin Ware, "Lag as a determinant of
  human performance in interactive systems", INTERCHI/CHI 1993, pp. 488-493,
  DOI 10.1145/169059.169431. https://www.yorku.ca/mack/CHI93.html — abstract:
  225 ms lag, +64% movement time, +214% errors, multiplicative model.
- [NIELSEN-1993] Jakob Nielsen, "Response Times: The 3 Important Limits",
  1993, restating R. B. Miller, "Response time in man-computer conversational
  transactions", AFIPS FJCC 1968, pp. 267-277, DOI 10.1145/1476589.1476628.
  Heuristics, not measurements.
- [DOHERTY-1982] Walter J. Doherty, Ahrvind J. Thadani, "The economic value
  of rapid response time", IBM, 1982 — primary not reached; the "400 ms
  threshold" attribution is disputed by readers of the paper; treated as
  folklore here.
- [FRANKE-2017] Thomas Franke, Christiane Attig, Nadine Rauh, Josef F. Krems,
  "System Latency Guidelines Then and Now — Is Zero Latency Really Considered
  Necessary?", HCII 2017, LNCS 10276 — abstract.

**Perceived performance**

- [VIGET-2017] Viget, "A Bone to Pick with Skeleton Screens", 2017.
  https://www.viget.com/articles/a-bone-to-pick-with-skeleton-screens —
  numbers (2.82 / 2.41 / 2.29 s; n=136) as quoted by several secondary
  sources; the page returned HTTP 429 this session.
- [UXC-SKELETON] Bill Chung, "Everything you need to know about skeleton
  screens", UX Collective. https://uxdesign.cc/what-you-should-know-about-skeleton-screens-a820c45a571a
  — secondary; the author's own caveat on sample size.
- [WROBLEWSKI-2013] Luke Wroblewski, "Mobile Design Details: Avoid The
  Spinner", 2013 — the origin anecdote; unverified this session.
- [HARRISON-2007] Chris Harrison, Brian Amento, Stacey Kuznetsov, Robert
  Bell, "Rethinking the progress bar", UIST 2007, pp. 115-118 — abstract.
- [HARRISON-2010] Chris Harrison, Zhiquan Yeo, Scott E. Hudson, "Faster
  progress bars", CHI 2010, DOI 10.1145/1753326.1753556 — in-house.

**Layout and architecture**

- [CASSOWARY] Greg J. Badros, Alan Borning, Peter J. Stuckey, "The Cassowary
  linear arithmetic constraint solving algorithm", ACM TOCHI 8(4), 2001,
  pp. 267-306, DOI 10.1145/504704.504705 — abstract.
- [HAIKU-ALM] Haiku source, `src/libs/alm/ALMLayout.cpp` and
  `src/tests/libs/alm/*` (Lutteroth, Kim 2007-08; Zeidler 2010).
  https://git.haiku-os.org/haiku.git/tree/src/libs/alm — verified by file
  listing upstream; absent from the Vitruvian fork, which has only
  `src/libs/linprog`; no paper reached.
- [WWDC18-220] Apple, "High Performance Auto Layout", WWDC 2018 session 220
  — the "scales linearly" claim is from secondary summaries; unverified.
- [WWDC22-10056] Apple, "Compose custom layouts with SwiftUI", WWDC 2022
  session 10056. https://developer.apple.com/videos/play/wwdc2022/10056/ —
  session page plus secondary guides.
- [WPF-LAYOUT] Microsoft, "The Layout System" (WPF).
  https://learn.microsoft.com/en-us/dotnet/desktop/wpf/advanced/layout — verified.
- [FLUTTER-INSIDE] Flutter, "Inside Flutter".
  https://docs.flutter.dev/resources/inside-flutter — verified; quotations
  in section 2.3.
- [LEVIEN-2022] Raph Levien, "Xilem: an architecture for UI in Rust", May
  2022. https://raphlinus.github.io/rust/gui/2022/05/07/ui-architecture.html
  — verified.
- [MURATORI-2005] Casey Muratori, "Immediate-Mode Graphical User Interfaces",
  2005. https://caseymuratori.com/blog_0001 — verified (blog summary).
- [IMGUI-WIKI] Dear ImGui wiki, "About the IMGUI paradigm".
  https://github.com/ocornut/imgui/wiki/About-the-IMGUI-paradigm — verified;
  contributor commentary, treated as practitioner opinion.
- [ADAPTON] Matthew A. Hammer, Yit Phang Khoo, Michael Hicks, Jeffrey S.
  Foster, "Adapton: Composable, Demand-Driven Incremental Computation", PLDI
  2014, pp. 156-166, DOI 10.1145/2594291.2594324 — abstract.
- [WINUI-VIRT] Microsoft, "Attached layouts" (WinUI): "UI virtualization
  means delaying the creation of a UI object until when it's needed"; the
  3x-viewport realisation window.
  https://learn.microsoft.com/windows/uwp/design/layout/attached-layouts

**Human interface guidelines and the laws**

- [APPLE-HIG-1992] Apple Computer, *Macintosh Human Interface Guidelines*,
  Addison-Wesley, 1992, chapter 1. Scanned PDF:
  https://archive.org/download/inside-macintosh-1992-1994/1992-human_interface_guidelines.pdf
  — verified; quotations in 3.1 extracted from the PDF text.
- [MS-WIG-1995] Microsoft, *The Windows Interface Guidelines for Software
  Design*, pre-release of 13 February 1995, chapter 1.
  https://ics.uci.edu/~kobsa/courses/ICS104/course-notes/Microsoft_WindowsGuidelines.pdf
  — verified; the published book is Microsoft Press 1995.
- [HAIKU-HIG] Haiku, *Human Interface Guidelines*.
  https://www.haiku-os.org/docs/HIG/index.xml — verified.
- [TOG-1999] Bruce Tognazzini, "A Quiz Designed to Give You Fitts", Ask Tog,
  February 1999. https://asktog.com/columns/022DesignedToGiveFitts.html —
  verified.
- [FITTS-1954] Paul M. Fitts, "The information capacity of the human motor
  system in controlling the amplitude of movement", J. Exp. Psychol. 47(6),
  1954 — standard citation, not re-fetched.
- [MACKENZIE-1992] I. Scott MacKenzie, "Fitts' law as a research and design
  tool in human-computer interaction", Human-Computer Interaction 7, 1992,
  pp. 91-139 — standard citation, not re-fetched; the Shannon form.
- [SOUKOREFF-2004] R. William Soukoreff, I. Scott MacKenzie, "Towards a
  standard for pointing device evaluation", IJHCS 61, 2004, pp. 751-789, DOI
  10.1016/j.ijhcs.2004.09.001 — abstract.
- [HICK-1952] W. E. Hick, "On the rate of gain of information", QJEP 4, 1952;
  [HYMAN-1953] Ray Hyman, "Stimulus information as a determinant of reaction
  time", J. Exp. Psychol. 45, 1953 — standard citations, not re-fetched.
- [SEOW-2005] Steven C. Seow, "Information Theoretic Models of HCI: A
  Comparison of the Hick-Hyman Law and Fitts' Law", Human-Computer
  Interaction 20(3), 2005, pp. 315-352, DOI 10.1207/s15327051hci2003_3 —
  abstract.
- [COCKBURN-2007] Andy Cockburn, Carl Gutwin, Saul Greenberg, "A predictive
  model of menu performance", CHI 2007, pp. 627-636, DOI
  10.1145/1240624.1240723 — abstract.
- [LANDAUER-1985] Thomas K. Landauer, Daniel W. Nachbar, "Selection from
  alphabetic and numeric menu trees using a touch screen: breadth, depth, and
  width", CHI 1985, pp. 73-78 — bibliographic; the `k + c·log b` form is from
  secondary summaries.
- [NNG-HAMBURGER] Kara Pernice, Raluca Budiu, "Hamburger Menus and Hidden
  Navigation Hurt UX Metrics", NN/g, 26 June 2016.
  https://www.nngroup.com/articles/hamburger-menus/ — verified.
- [NNG-FLAT-2017] Kate Moran, "Flat UI Elements Attract Less Attention and
  Cause Uncertainty", NN/g, 3 September 2017.
  https://www.nngroup.com/articles/flat-ui-less-attention-cause-uncertainty/
  — verified.
- [NNG-FLAT-2015] "Flat Design: Its Origins, Its Problems, and Why Flat 2.0
  Is Better for Users", NN/g, 27 September 2015 — byline (Kate Meyer or
  Moran) not confirmed; secondary.
- [NNG-WIN8] Jakob Nielsen, "Windows 8 — Disappointing Usability for Both
  Novice and Expert Users", NN/g, November 2012 — primary not fetched;
  press summaries (PCWorld, Computerworld, The Register, 2012).
- [NORMAN-TOG-2015] Don Norman, Bruce Tognazzini, "How Apple Is Giving Design
  a Bad Name", Fast Company, November 2015.
  https://www.fastcompany.com/3053406/how-apple-is-giving-design-a-bad-name —
  press summaries; essay, not a study.
- [WIN11-MENU] Windows Central, Tom's Hardware, Pureinfotech, 2021-2025
  coverage of the Windows 11 "Show more options" context menu and Microsoft's
  announced changes — secondary.
- [LION-SCROLL] TidBITS, "Subtle Irritations in Lion", August 2011, and
  contemporaries — opinion.
- [APPLE-MATERIALS] Apple, Human Interface Guidelines, "Materials", 2025.
  https://developer.apple.com/design/human-interface-guidelines/materials —
  partial excerpt; the iOS 26 beta 3 transparency reduction is from press
  coverage (July 2025).
- [EXT-BG-EFFECT] wayland-protocols staging `ext-background-effect-v1`,
  merged 2025-05-26 — as cited in `docs/visual-system.md` 3.9.

**In this tree**

- `contracts/decisions/0023-aurora-toolkit.md`, `0010-visual-system.md`,
  `0013-kits-are-services.md`, `0017-accessibility-tree.md`,
  `0020-drag-and-drop.md`; `docs/file-navigator-plan.md`;
  `docs/visual-system.md` sections 3.9, 5, 11, 12; `kits/interface/README.md`
  sections 1.3, 1.4, 7, 9, 10, 12, 13; `kits/interface/appserver/server.cpp`
  (`gMu`, `ApplyWindowResize`, `GlyphKey`); `contracts/theme/aurora-glass.toml`.
