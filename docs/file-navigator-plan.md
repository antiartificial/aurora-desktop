# A file navigator for Aurora: the plan

Written 2026-11-06. A plan, not a decision record: it says what to build and in
what order. Each stage that lands gets its own record if it decides a contract.

## What exists today, measured

- **`demos/browser/browser.cpp`, 465 lines**, built as `iface_browser`, shipped as
  the app `org.aurora.haiku.browser` named "Files". A `BListView` in
  `B_SINGLE_SELECTION_LIST` mode, a right-hand `Pane`, `readdir` + `lstat` from
  `$HOME` or `BROWSER_DIR`. Enter or double-click enters a directory or hands a
  file to `aurora open FILE`. Selecting a file runs `aurora query show FILE --json`
  as a child process and prints the provenance into the pane.
- **Saved queries already exist**, and this is the part worth building on.
  `index/query.go` defines a one-key-per-line `<name>.query` file with predicates
  `mime`, `ext`, `app`, `verb`, `since`, `under` and `derived-from`. Keys AND,
  repeated keys OR. It is parsed strictly: an unknown key or a query with no
  predicate is an error rather than a query that quietly matches everything.
  `Query.Match` and `Service.next(who, q, after)` give matching and paging.
- **`BColumnListView` landed** this cycle: sortable columns, column dragging,
  nested rows. A real grid is available now; the navigator does not need one built.
- **`registry.plumb@v1`** answers "I have this selected, what can the system do
  with it?" as a ranked candidate list, each carrying an `effect`
  (`read | write | destructive | external`) and a `default` flag.

## What "best of all worlds" should mean here

**From Haiku's Tracker — the model, not the chrome.** A query is a first-class
object you keep, not a search box you retype. Type-ahead jumps to a name instead
of opening a dialog. Drag and drop is the primary verb rather than a shortcut.
Per-folder view state is remembered, so a folder looks how you left it.

**From NeXT's Workspace and Finder — the column browser.** Navigating a deep tree
without losing the path you took is the one view the current list cannot do, and
it is the view that makes a keyboard-driven navigator fast.

**From Explorer — the breadcrumb that is also an editable path field.** Cheap,
and the fastest way to get somewhere you can already name.

**Refused, with reasons.** A ribbon: the verb list is already ranked and typed by
the plumber, so a second, untyped surface for the same thing would compete with
it. Enforced spatial windows (Tracker's purist mode, one window per folder): the
remembered-state half is the good part and survives without the window
explosion. An icon view as the default: honest thumbnails need an icon system
that does not exist yet (HVIF with a bitmap fallback is approved and unbuilt),
and a grid of identical placeholder glyphs is worse than a list.

## The part only Aurora can do

Every file manager shows name, size and date. Aurora records, per file, which
app produced it, under which verb, inside which domain, holding which grant,
hash-chained in the bus log — and `index/query.go` already has the predicates for
it. So **provenance is a column and a query axis**, which is a thing Tracker
could not do because BeFS attributes describe a file, not its origin.

Concretely, these are expressible today and should be reachable from the UI:

```
# Everything the compiler made from this header, this week
verb: compile@v1
derived-from: /home/arti/src/aurora/thing.h
since: this-week
```

That is the feature worth being known for, and it costs little because the
engine is written.

**Authority belongs in the same surface.** Right-click is the plumber's ranked
list with effect classes marked, so a destructive or external verb says so before
you pick it, not after. Dragging a file out of a domain app is the grant gesture
(decision 0020), not a copy. A navigator running inside a domain shows what the
domain holds and nothing else — which it already does, and which looked like a
bug ("Files shows an empty home") when it was the sandbox working.

## Honest gaps, because the plan is worth nothing if it plans around them

1. **indexd only knows files Aurora itself made.** `browser.cpp`'s own header says
   so. For most files on disk the provenance column has no answer, and it must
   render as *unknown* — never blank, which reads as "nothing made this", and
   never a guess. This is the single biggest honesty constraint on the design.
2. **MIME comes from the file extension** (`MIMEOf`), not from content. A renamed
   file is mistyped, and the verb list that follows is wrong. Content sniffing is
   its own job; until then the UI should not present a type as authoritative.
3. **Queries are not live.** Tracker's queries update as the filesystem changes,
   and that is a large part of why they feel like objects rather than searches.
   Ours are re-run. Live queries need a watch (inotify, plus indexd pushing
   changes), and that is a real piece of work, not a flag.
4. **No icon system.** Blocks icon view, thumbnails, and the animated dock icons
   already asked for.
5. **Haiku apps sometimes hang on close** — measured at 4 of 10 for
   `iface_controls`, main thread in a futex wait, and present without domains. A
   navigator is the app a person opens and closes most, so this bug hits it
   hardest. It should be fixed before polish, not after.

## What it must feel like, and the measurements that decide it

The goal is a window that is **there**, immediately, and fills in. Not a
progress bar, not a spinner over an empty pane. The toolkit record `0023` holds
the performance contract; these are the measurements that shaped it.

| | 3,000-5,000 entries |
| --- | --- |
| `readdir`, names only | **0.9 - 1.8 ms** |
| `+ lstat` on every entry | **13 - 43 ms** |
| spawning the `aurora` CLI (floor, before it connects) | **1.7 ms** |

Three conclusions, and they are the design:

1. **`lstat` is about thirty times `readdir`.** Today's browser does both
   synchronously before showing anything, so a large directory spends up to 43ms
   -- nearly three frames -- on data that is not needed to draw the first screen.
   Names are enough for the first frame and cost under 2ms.
2. **Stat the visible rows, not the directory.** A screen holds tens of rows, not
   thousands. The cost of a screenful of `lstat` is negligible; the cost of the
   directory is not.
3. **No per-row child process, ever.** At a 1.7ms floor, a provenance column
   filled one process at a time would take seconds per screen. Today's browser
   spawns one per *selection*, which is survivable; one per *row* is not.
   Provenance for the visible range must be a single batched bus call.

And the rule that separates progressive loading from a bad experience:
**nothing reflows when data arrives.** Row heights and column widths are fixed
from the first frame; a late value fills a space already reserved. A row that
moves out from under the pointer is worse than a row that was never there.

The window itself follows the same idea: it **opens opaque and settles into
glass** once there is budget for the blur (`0023`). The cheapest frame is the
first one, and the effect is something the system opts into once it knows it can
afford it.

## Order, by time-to-first-pixel

Staged by what the person sees first, not by feature size. Each stage is
shippable alone.

**Stage 1 — the instant window.** The window, chrome and names appear in the
first frame, from `readdir` alone, on an `AuroraListView` that only builds
visible rows. `lstat` moves to a worker and fills size, date and type per row
into reserved space. Navigate-away cancels outstanding work. This stage is
almost entirely about *removing* synchronous work from the window thread, and it
is the one that decides whether the thing feels 2026 or not.

**Stage 2 — the grid and the path.** `BColumnListView` behind the virtualised
view: sortable, reorderable columns, including a provenance column reading
*unknown* where indexd has nothing. The editable breadcrumb path field. Both
pieces exist; this is assembly.

**Stage 3 — glass and the arrival.** `aurora_blur_v1` so the window has a real
backdrop rather than a painted gradient, and the opaque-to-glass promotion,
eased out. Needs the compositor protocol from `0023`.

**Stage 4 — the verb surface.** Right-click and Enter both go through
`registry.plumb@v1`: ranked, default marked, destructive and external flagged.
Delete stops being a special case and becomes the destructive verb it already
is. Provenance for the visible range becomes one batched call.

**Stage 5 — queries as windows.** A saved query opens as a navigator window
whose contents are its results, built and edited from the UI over the existing
`.query` format. Provenance predicates (`verb`, `app`, `derived-from`) get
first-class UI, since they are what makes this ours. Still re-run rather than
live, and the window says so rather than implying freshness it does not have.

**Stage 6 — the column browser and type-ahead.** The two things that make it
fast to drive from the keyboard.

**Stage 7 — live queries.** indexd watches and pushes; query windows update.
The stage that makes a query feel like an object, and the one with the most new
machinery, which is why it is last.

**Not scheduled:** icon view and thumbnails, which wait on the icon system.

## Dependencies

- Stage 1 needs `AuroraListView` from `0023` and nothing else. It is the stage
  with the largest effect on how the thing feels and the smallest dependency.
- Stage 3 needs `aurora_blur_v1`, a new compositor protocol (`0023`).
- Stage 4 wants the file-drop-as-grant path from `0020`, which is designed and
  unbuilt, and is itself blocked on the controller having no verb that mounts
  into a running domain.
- Stage 7 needs a change to indexd's service surface and probably its own record.
- **The hang-on-close bug should be fixed first regardless of stage order**
  (4 of 10 for `iface_controls`, present without domains). A navigator is the app
  a person opens and closes most often, so it is where that bug does the most
  damage to exactly the impression this plan is trying to make.
- The app_server's global lock is being split; until it is, two windows drawing
  at once take turns (measured: 8 windows take ~12.8x one window's time). A
  navigator with a preview pane is a two-window workload.
