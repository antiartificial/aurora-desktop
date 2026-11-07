# 0023. The Aurora toolkit: declarative in code, glass from the compositor

Status: proposed. Follows `0010` (visual system) and `0013` (kits are services).
Written for the file navigator (`docs/file-navigator-plan.md`), which is its
first real client, but the toolkit is not navigator-specific.

## The ask

A world-class file navigator for 2026: stylish, performant, presenting itself
immediately and filling in afterwards. Styled after modern component toolkits
(XAML, SwiftUI). Explicitly balanced against bloat.

## What exists, measured

- **Glass exists, and apps cannot have it.** `compositor/src/fx/blur.rs` has a
  working dual-Kawase path and `backdrop(..., region, radius_dev, ...)` already
  takes a region. It is used for compositor chrome only: the dock, the settings
  panel, the powerbox, the switcher. The compositor advertises seventeen protocol
  states and **not one lets a client ask for a blurred backdrop.**
- **Theming exists.** `contracts/theme/aurora-glass.toml` holds the tokens;
  `AuroraControlLook` already skins Haiku's controls through
  `BControlLook`, the kit's own skinning seam.
- **A real grid exists.** `BColumnListView`: sortable, reorderable, nested rows.
- **The Interface Kit is Haiku's own**, compiled in place, imperative and
  BeOS-shaped. It is not going away and should not.

## Decision

The toolkit is **three pieces**, not a new UI framework. The distinction matters:
a new framework would compete with the Interface Kit we already compile, and
that is where this kind of project dies.

### 1. `aurora_blur_v1` — the piece that cannot be faked

A client marks a region of its surface as glass; the compositor blurs what is
behind it using the existing path. Without this, "glass" in an app window is a
painted gradient over nothing, which reads as fake precisely because the backdrop
does not move when the window does.

Three properties this must have, and the reasons are not cosmetic:

- **The client never receives backdrop pixels.** It declares a region and gets a
  visual result it cannot read. A protocol that handed back the blurred content
  would be a screen-capture channel with no grant behind it, which is the whole
  thing decisions 0014 and 0016 exist to prevent.
- **The compositor owns the radius and the passes**, from the theme. A client
  asking for its own radius can make an arbitrarily expensive frame, and there is
  **no frame watchdog yet** — the plan calls for one and it does not exist. Until
  it does, the only safe arrangement is that the expensive parameter is not the
  client's to set.
- **Bounded per surface and per frame.** A client that re-declares a large region
  every frame must not be able to spend the whole budget. Measured budget today
  at 1920x1200: total p50 0.915ms, p95 2.682, p99 4.920, against 16.67ms.

#### Glass is a quality level, not a constant

A window **opens opaque and is promoted to glass once the frame budget shows
room.** This was not the original design and is better than it.

The first frame is the worst possible moment to add a blur pass: it is already
paying for window creation, surface mapping, decoration, and the directory read.
Opening opaque makes the cheapest frame the first one, which is exactly where
"present immediately" needs the room.

It also buys three things that were otherwise unsolved:

- **A graceful degradation path.** Under budget pressure a surface stays opaque
  instead of dropping frames. Glass becomes a level of detail that degrades, which
  is how games spend a frame budget and is what this record's performance contract
  is modelled on.
- **A partial stand-in for the frame watchdog**, which does not exist. A design
  where the expensive effect is only ever *promoted into*, never assumed, cannot
  blow the budget on arrival -- it declines to arrive. That is weaker than a
  watchdog and does not remove the need for one, but it fails safe where the
  alternative fails visibly.
- **A deliberate arrival.** The window settling from opaque into glass, eased out,
  is a moment rather than a pop. Ease-out, not ease-in, per the house preference.

The constraint this adds: **text must stay legible in both states, and the
promotion must not move anything.** Opaque panel and blurred backdrop are
different luminances, so contrast has to be guaranteed twice, not once. The theme
already carries `luminance_max` for the glass case and the reference values are
asserted on pixels (text L 0.97 on panel L 0.22, about 15:1). Promotion changes
colour and nothing else: no reflow, no size change, no shift of a row under the
pointer.

### 2. Declarative in code, not in markup

**SwiftUI's shape, not XAML's.** The reasoning, since this is the part most
likely to be re-litigated:

- XAML's real value is its tooling — a visual designer, hot reload, a binding
  engine. We have none of them, and building them is a larger project than the
  navigator they would serve. Adopting the file format without the tooling takes
  the cost and leaves the benefit.
- XAML's cost lands exactly where this project cannot pay it: a markup parse at
  startup, on the path whose whole goal is the first frame. A declarative tree
  written in code costs nothing to parse because there is nothing to parse.
- SwiftUI's shape is just functions returning values, which maps onto a C++
  builder API over `BView` with no new language, no new file type, and no second
  place to look when something renders wrong.
- **What XAML gets right and we take:** styles and control templates, separated
  from structure. That already exists here as theme tokens plus
  `AuroraControlLook`, so we take the idea and keep the mechanism.
- **What SwiftUI gets right and we refuse:** full retained-mode diffing of a view
  tree. That is where the complexity and the surprise live, and for a file list it
  buys almost nothing over invalidating the rows whose data changed. Refusing it
  is the anti-bloat decision in this record.

So: a builder API producing real `BView`s, themed by the existing tokens,
with invalidation driven by data arriving rather than by diffing a tree.

### 3. `AuroraListView` — virtualised and async-backed

The flagship widget, and the one that makes the navigator feel instant. Rows are
built only for the visible range plus a margin. Cells whose data has not arrived
render a reserved placeholder. It is a toolkit widget rather than navigator code
because every list over slow data wants it.

## The performance contract

This is the part that answers "games solved this a while ago", and it is a
contract because a target nobody measures is a wish.

1. **The window thread never blocks for more than 8ms.** Half a frame, leaving
   the other half for the compositor. Anything slower goes to a worker.
2. **First paint shows chrome and names.** Measured: `readdir` on 3,000-5,000
   entries costs **0.9-1.8ms**, while `lstat` on every entry costs **13-43ms** --
   roughly thirty times as much. Today's browser does both synchronously before
   showing anything. So names come from `readdir` and appear immediately; size,
   date, type and provenance arrive per row, later.
3. **Nothing reflows when data arrives.** Row height and column widths are fixed
   from the first frame, and a late value fills a space already reserved for it.
   This is the line between delight and annoyance: progressive loading that moves
   a row out from under the pointer is worse than having waited.
4. **Work is cancelled on navigate-away.** A directory the user has left must not
   keep spending the budget. Without this, fast navigation gets slower the more
   you do it, which is the opposite of the intended feel.
5. **Prefetch along the direction of travel**, and only there. Speculative work in
   every direction is how a responsive design becomes a hot laptop.
6. **No per-row child processes.** Measured: spawning the `aurora` CLI costs
   **1.7ms** at the floor, before it connects to anything. Today's browser spawns
   one per selection to fetch provenance. One per visible row would be seconds
   per screen. Provenance must be one batched bus call for the visible range.
7. **Effects are promoted into, never assumed.** A surface opens opaque and
   becomes glass when there is room (above). The same rule should hold for every
   later effect: the expensive version is the one the system opts into once it
   knows it can afford it, not the one it starts with and retreats from.

## What this is not

Not a replacement for Haiku's Interface Kit. Not a retained-mode framework. Not
a new markup language. Each of those was considered and each would cost more than
the navigator it serves, which is the test this record applies.

## Open

- Whether `aurora_blur_v1` should be a private Aurora protocol or an extension of
  something existing. Private first, because inventing a public protocol before
  one client has used it in anger is how bad protocols ship.
- The frame watchdog is still needed. Opening opaque and promoting into glass
  fails safe, but it only governs arrival: it says nothing about a surface that is
  already glass when the budget later collapses. Demotion -- glass falling back to
  opaque under load -- is the other half and is not decided here.
- Rounded corners and shadows for client surfaces are the obvious next request
  after glass, and are not decided here.

## Appendix: what to lift, from where, and what to refuse

This record earlier said "SwiftUI's shape, not XAML's". That was too blunt. The
four toolkits worth raiding each solve a *different* problem well, and the useful
answer is which mechanism to take from which, not which framework to imitate.

A second kit beside Haiku's Interface Kit is the proposal here: a BeOS-flavoured
kit and a modern one, sharing a substrate. **The rule that makes two kits safe:
they share drawing primitives, layout, theme tokens and the app_server, and they
share no widgets.** If they shared widgets the modern kit would be a skin; if
they shared nothing we would maintain two of everything. Decision `0013` already
treats kits as plural, so this is an extension of it, not a new idea.

### The spine: composition and modifiers, from SwiftUI

A view is a value; you build by nesting rather than subclassing; appearance is a
chain of small orthogonal modifiers. This is the anti-bloat mechanism in the
whole record: one `padding` modifier replaces a `padding` property on every
widget, and `n` modifiers compose into far more than `n` behaviours. It is also
the exact opposite of WinForms' and BeOS's deep inheritance, which is why a
modern kit cannot be grafted onto the Interface Kit and has to sit beside it.

Also from SwiftUI: **environment propagation** — font, scale, colour scheme and
locale flow down the tree implicitly with explicit override points, so a control
does not take six constructor arguments to look right.

### The structural idea: the template split, from XAML

**`ControlTemplate` separate from `DataTemplate`.** A control's *appearance* is
replaceable independently of its *behaviour*, and how a data item is rendered is
separate again. This is the most valuable structural idea for a themeable
toolkit, and `BControlLook` (which `AuroraControlLook` already implements) is a
primitive version of exactly it — so the seam is proven here, just narrow.

**Value precedence**, also from XAML: a property's effective value resolves
through an explicit chain (animation, then local, then template, then style, then
inherited, then default). The explicit ordering is what lets themes compose
without becoming CSS's cascade.

### The layout: star sizing from XAML, anchoring from WinForms

**Star sizing** (`*`, `Auto`, fixed) is the best idea in XAML layout and predates
CSS Grid's `fr` by over a decade. Proportional allocation that a person can
predict by reading it.

**Anchoring and docking**, from WinForms — and we already have it: BeOS's
`B_FOLLOW_ALL`, `B_FOLLOW_LEFT_RIGHT` and friends are the same mechanism. It
handles the easy eighty percent with no layout pass worth the name, which matters
for a first frame. Keep both: anchoring where it suffices, star-sized grids where
it does not.

### Named visual states, from XAML's VisualStateManager

Hover, pressed, disabled, focused declared per control rather than coded per
control. Cheap to build and it buys consistency across every widget for free,
including the ones written later. Without it, each control reinvents its own
hover and they drift.

### The vocabulary: the component inventory, from WinUI

This is WinUI's real contribution, and it is what "a rich vocabulary" means in
practice: named solutions to recurring problems. `NavigationView`, `CommandBar`,
`BreadcrumbBar`, `InfoBar`, `Expander`, `SplitButton`, `NumberBox`,
`ContentDialog`, `TeachingTip`, `ItemsRepeater`.

Take the *list* as a catalogue to work from, with the visual treatment from
macOS: vibrancy, restrained type, generous spacing, subtle depth. Fluent's
inventory, Apple's manners.

**Filtered hard, though.** A component enters the kit when a real client needs
it, never to fill the catalogue. For the navigator that is roughly: the
virtualised list, a breadcrumb, a split view, a toolbar, a sidebar and a context
menu. Six, not sixty.

Also from WinUI: **`ItemsRepeater`'s premise that virtualisation is a
first-class concern**, not an optimisation bolted on later. And **`x:Bind`'s
lesson** — compiled, typed bindings — which here means: no string paths at all.

### What to refuse, and why

- **The binding engine.** XAML's stringly-typed paths fail silently and at
  runtime. Data arriving late is this toolkit's central case, so the one thing it
  cannot have is a data mechanism that fails quietly.
- **Markup files.** A startup parse on the path whose goal is the first frame,
  and a second place to look when something renders wrong.
- **Retained-mode tree diffing.** SwiftUI's complexity and its surprises live
  here, and for a list over slow data it buys little over invalidating the rows
  whose data changed.
- **Deep inheritance hierarchies**, from WinForms and BeOS. Fine for the
  Interface Kit, which is what it is; wrong for the kit being added.
- **A visual designer.** It is what makes XAML worth its ceremony, and building
  one costs more than every client we have.

### The shape, concretely

Declarative in code, no new language:

```
Grid(rows: ["Auto", "*"], cols: ["240", "*"])
  .child(Toolbar(...), row: 0, col: 0, colSpan: 2)
  .child(Sidebar(places).anchor(.vertical), row: 1, col: 0)
  .child(FileList(model)                      // virtualised, async-backed
           .rowTemplate(FileRow)              // DataTemplate
           .onActivate(plumb)                 // ranked verbs, 0009 effects
           .padding(8)
           .glass(.promoted),                 // opaque until there is budget
         row: 1, col: 1)
```

`Grid`/`anchor` is the layout, `.rowTemplate` is the DataTemplate split,
`.glass(.promoted)` is this record's quality-level rule, and every modifier is a
value returning a value. Nothing here needs a parser, a designer or a binding
engine.
