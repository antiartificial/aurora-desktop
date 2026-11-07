# 0021. The Deskbar: a glass box of applications and their windows, drawn by the compositor, showing only numbers it can source

- **Status:** proposed
- **Date:** 2026-11-06

## Context

The request was "the BeOS style box that shows open windows and applications, optionally CPU, network etc.", as "a 2026 version, glass". BeOS's Deskbar was a box in a screen corner listing every running application and, opened (the expando), its windows, with a tray of small monitors beside it.

Three things in this tree decide the shape of it.

- The compositor already owns the one list of every window (`Space`), already draws the dock, the settings panel and the capability indicator itself (0010, 0011, 0016), and already has chrome glass: the dual-Kawase chain and the `[glass.chrome]` values the dock sits on.
- Per-application CPU, memory and network are only attributable to something that has a cgroup and a network proxy of its own, and that is a domain. A host process shares the session's cgroup with everything else.
- Decision 0022 landed while this was being built: a dock click now launches a windowed app inside a domain, and `compositor/src/peer.rs` records at connect which domain the kernel put each Wayland client in. The brief for this record described that work as still in flight. It is on `main`, and the Deskbar reuses it rather than reading the same cgroup file a second way.

## Decision

### The Deskbar is compositor-drawn, not a client app

It has to list **every** window, including those of sandboxed domain apps. A client could only do that if something handed it a "see every window" capability, and an always-running app holding that is strictly more dangerous than the Deskbar itself: it would be a process that can read the title of every window the user has open (titles are often the document, the URL, the person on the other end of a chat) and that the user must keep running. The compositor already has the list, already draws the other chrome that needs it, and is already the most trusted process in the session. So the Deskbar adds no new holder of the capability and no new protocol object.

It is chrome in the scene, **like the dock, not like the indicator**: it has chrome glass behind it, so it is drawn by the effects pipeline before captures are served, and a client capture shows it. This is a choice. The indicator is left out of captures because it is evidence a recording must not be able to forge or hide; the Deskbar is a window list, and a capture that omitted it would show a desktop that is not the one on the screen. What it shows (window titles) is already in the pixels of those windows.

It takes no keyboard focus and restyles nothing a client can see. The pointer over it belongs to the compositor: no client sees the motion, the press or the release.

### Grouped by application, expanding to its windows

One row per application; a click opens it to list that application's windows (the expando); a click on a window raises and focuses it (`Aurora::focus_and_activate`, the same call a click on the window itself makes). Order is the order things first appeared, never stacking order, so raising a window does not shuffle the list under the pointer.

An application is **(what the client calls itself, where it runs)**, not the name alone. An `app_id` is a claim any client can make; a confined window that sets `app_id = "org.aurora.terminal"` gets a row of its own with its domain on it, instead of being folded into the real one. Window titles and app ids are drawn with control characters and Unicode bidi/format characters replaced, so a title cannot reorder itself or hide the text beside it.

Keyboard access is not built. Alt+Tab (the switcher) remains the keyboard way to a window. Taking focus would have made the Deskbar a thing that changes what a capture shows and where typing goes; that is a decision to make on its own.

### Glass, using what exists

The box is a `ChromeDraw` like the settings panel's: `blur::backdrop(Kind::Chrome)` then `draw_glass` with `chrome_glass(&glass)`, the dock's own path. No second blur. A new `[deskbar]` table sets placement, width and four text colours; the material is `[glass.chrome]`. The text colours are asserted against the chrome-glass luminance bound (`deskbar_text_clears_the_chrome_glass_contrast_floor`, in `fx/theme.rs`): text 6.0:1, dim 4.8:1, warn 5.2:1 over the worst backdrop, so a re-tint cannot silently make the refused host unreadable. Without glass (`[glass] enabled = false`) it draws the dock's flat panel, as the dock does.

### Motion is ease-out

An application opening or closing is an `Ease` from `shell/motion.rs` (cubic ease-out, `1 - (1 - t)^3`, `[deskbar] ease_ms = 140`), reversing from wherever it is. `a_row_opens_on_an_ease_out_so_it_is_ahead_of_linear_on_its_first_frames` samples the box's height during a 1000 ms open and checks it follows the cubic and is ahead of linear. Nothing else animates: no appear fade (the box is there or it is not), no idle animation, hover is an instant wash. That is deliberate for the capture assertions below.

### Where it sits, and when it is not drawn

Top-right of the usable area, below the clock (which owns the corner), `margin` logical pixels in; `edge = "left"` puts it top-left. It floats over windows, as a dock does not, so it is **not drawn** while any window is fullscreen (a box over a video is worse than a missing box; the dock avoids this by reserving its own strip, which this does not), under `--legacy-scene` (the pre-shell look the byte-comparing tests rely on), or when `[deskbar] enabled = false`. The desktop menu has "Hide Deskbar" / "Show Deskbar" because a floating box needs a way out one right-click away.

**Amended:** emptiness was originally a reason to hide ("an empty box is clutter") and no longer
is. With nothing running the box stays up and says "No windows open", because a box that
vanishes cannot be told from a box that is broken, and "0 apps - 0 windows" reads like a failed
lookup rather than an answer. `deskbar/draw.rs` carries the reasoning and
`tests/deskbar.rs::it_is_hidden_behind_a_fullscreen_window_and_under_legacy_scene_but_not_for_being_empty`
asserts it. This paragraph and `demos/deskbar/run.sh` were not updated at the time, and the demo
failed at its first step from then until 2026-11-07.

### What numbers it shows, and where each comes from

**The rule: never show a number that cannot be sourced.** An application that is not in a domain says "not in a domain"; a figure that cannot be read is not drawn. `model.rs` returns `Option` for every figure and has no path from `None` to text; `draw` tests assert that a row outside a domain contains no digit.

| Shown | Source | Live today |
|---|---|---|
| Applications, windows, titles, focus | the compositor's `Space` | yes |
| Which domain a window is in | `ClientState.domain` (`peer.rs`: `SO_PEERCRED` pid, then `/proc/<pid>/cgroup`, at connect). The same fact as the title-bar badge. | yes, for windows launched into a domain (0022) |
| "not in a domain" | the same read, finding no `aurora-domains` component | yes |
| "domain unknown (X11 window)" | an X11 window's client is Xwayland; its cgroup says nothing about the app | yes |
| Domain CPU, `cpu N%` | `cpu.stat` `usage_usec` of the domain's cgroup, differenced over >= 0.4 s, sampled once a second; percent of ONE core, so a busy multi-threaded app passes 100. First reading gives nothing, not 0%. | yes, for domain apps |
| Domain process count | `pids.current` of the domain's cgroup | yes, for domain apps |
| Domain memory, `mem N MB` | `memory.current` of the domain's cgroup | **no, on this machine**: the user manager delegates only the `pids` controller, so the file does not exist and nothing is drawn. It appears by itself where the memory controller is delegated. |
| Network: hosts, refusals, per-host verdict and count | `net.conn` and `net.summary` on the bus (0015), subscribed over the session socket by one thread, attributed by the event's `domain` (busd stamps it) | yes for a domain with a net grant; counts arrive with the proxy's 30 s summary, a host with no count yet shows no number |
| Machine CPU and memory in the header, labelled `cpu` / `mem` | `/proc/stat`, `/proc/meminfo` (`MemAvailable`) | yes; the machine's, never an application's. `[deskbar] system = false` removes them. |

What the network shows is the point of the feature as asked, and the refused host is first: `net 2 hosts · 1 refused` under a domain app, and opened, `refused.example  refused` ahead of `allowed.test  allowed 700`. "Refused" is always the word as well as the colour.

The bus thread is `indicator/bus.rs`'s pattern (a thread, a session-socket connection, subscribe-then-replay, a whole-state snapshot over a calloop channel on every change), with the topics changed. It replays 15 minutes of log. `net.summary` counts are deltas and are added; because subscribe-then-replay can deliver an event twice, the model drops an envelope id it has seen, which the indicator's idempotent set does not need.

When a domain has no host to list, the note says what is known about the bus: "network: no bus configured" (no `--bus-socket`), "network: bus unreachable" (last known, in the warning colour), "no connections reported". It does not say "no network activity" when it cannot know that.

## What is not shown, and why

- **CPU, memory and network for a host process.** Not attributable: it shares the session's cgroup, and the proxy only exists for a domain. The row says so.
- **Memory on a stock systemd user manager.** See the table. The honest figure needs `Delegate=memory` (or `cpu memory pids`) on the user manager, which is the user's system configuration and not a thing the compositor should write.
- **A network line for an app with no grant and no events.** Silence is not "0 hosts": nothing is drawn.
- **Per-window figures.** A cgroup is a domain's, not a window's. Two windows of one domain share its numbers, and the row is the domain's. 0022 gives each launch its own domain, so for a dock-launched app this is one app.
- **Network counts in real time.** They trail by up to the proxy's 30 s flush.
- **Attempts that never reach the proxy** (0015, weakest point 5). The Deskbar shows what the proxy decided.

## Weakest points

1. **The domain label is the kernel's placement, not busd's attestation.** `peer.rs` says it plainly and the Deskbar inherits it: a same-user process can move itself into a domain cgroup and be shown as that domain. It looks more confined than it is, never less. The indicator verifies against busd (`verify::judge`); the Deskbar does not.
2. **`peer.rs` reads "cgroup unreadable" as "no domain".** A process that exits between accept and the read gets no attribution and the Deskbar says "not in a domain" for it. It fails towards the row without figures, and the window is gone a moment later. The Deskbar's own X11 case is the one place it can say "unknown" instead.
3. **Network attribution is by the domain's name.** A window's domain name (from its cgroup) is matched to the `domain` field of the bus event. Both are the controller's names for the domain, but a same-user process squatting a real domain's cgroup name would be shown that domain's network rows on the user's own screen. No privilege is gained, and 0016's trust-zone note applies.
4. **The box floats over windows.** It will cover the corner of a window. That is the BeOS trade and the hide entry and the fullscreen rule are the mitigation.
5. **A cost that is not free** (below).

## Verification

`cd compositor && cargo test --release --no-fail-fast`: 277 passed, 0 failed, 2 ignored (`frame_budget_report`, which was already ignored, and the Deskbar's own measurement, `deskbar_frame_cost_report`). The base without this change, after merging 0022, is 277 less the 36 tests added here (24 `deskbar::` unit tests, 2 theme tests, 10 in `tests/deskbar.rs`): 241.

`tests/deskbar.rs` runs real headless compositors and real Wayland clients at scale 1 and 1.45, a fake busd speaking the `net.conn` / `net.summary` shapes of `bus/server/netreport.go`, and one test that moves a real client into a real cgroup under the user's delegated `aurora-domains` tree (it skips, loudly, where that is not possible): the box appears at the corner as glass over a window that reaches under it, and only its own rectangle changes when it goes; windows group by application and the header counts them; a row opens to its windows and closes again; a click on a window raises it and focuses it and the client sees no press; a host window says "not in a domain" and carries no digit while the bus knows a busy domain; the header carries only labelled machine figures; it hides for no windows, fullscreen and `--legacy-scene`; hover changes pixels only inside the box and a click on it moves no window and no focus; the open is ease-out; and a client in a real domain cgroup gets `cpu`, a process count and the domain's network line (and a memory figure exactly when `memory.current` exists), while the host app beside it still has none.

`demos/deskbar/run.sh` does the same against the real stack: busd, controllerd, the compositor, a domain the controller made and registered, a real Wayland client run in it with `domain run`, and real `net.conn` events from the domain's own proxy (a refused host and a granted-but-unresolvable one). 28 passed, 0 failed. It prints what it does not prove.

`demos/full-stack/run.sh` (the capture-while-overlay-up assertion unchanged at <= 4000 pixels; measured 2624, a title bar restyling on focus), `demos/theme-drift/run.sh` (22 passed) and `demos/capability-indicator/run.sh` (35 checks) are green.

### Frame cost, measured

`deskbar_frame_cost_report`, 1920x1200 at scale 1, `--frame-stats` (which makes the effects pass wait on the GPU at each phase, so it is slower than a normal run), the default animated wallpaper and dock, two runs each, on a GPU the live desktop shares, so the noise is about as large as the effect:

| Scene | Deskbar | total p50 (ms) | p95 | p99 |
|---|---|---|---|---|
| 1 window | off | 1.81 / 1.82 | 4.82 / 5.15 | 10.3 / 11.2 |
| 1 window | on | 2.24 / 2.21 | 5.41 / 5.37 | 9.9 / 8.9 |
| 1 window | on, one app open | 2.26 / 2.14 | 6.45 / 5.13 | 10.1 / 8.4 |
| 6 windows (12 glass surfaces) | off | 8.67 / 8.77 | 13.2 / 13.6 | 18.4 / 18.0 |
| 6 windows | on | 9.39 / 8.98 | 14.1 / 13.4 | 18.3 / 15.8 |
| 6 windows | on, two apps open | 9.71 / 10.23 | 13.9 / 15.1 | 16.2 / 16.4 |

The cost is one more chrome-glass surface: a quarter-resolution backdrop chain and a glass draw, and a prepass redo when it lies within the blur's reach of a window drawn before it. About 0.4 ms at the median for one window, and about 0.3 to 1.5 ms at the median with six (the six-window desktop is already near the 16.67 ms budget at p99 without it). The CPU side is a rasterisation only when what it says changes (a once-a-second header figure, a window opening, a hover), a key comparison otherwise, and one texture upload per change; the sampling is two small file reads per domain and two for the machine, once a second, on the main thread, and only while the box is shown. The brief's reference scene (p50 0.915 ms at 1920x1200) is not one this measurement reproduces; the table is an A/B of the same scene.

There is no frame watchdog, so a Deskbar that got slower would not be caught by one. The sampling reads are bounded (32 domains, small files) and the raster is bounded (a box cut to the output's height with "+N more applications").

## Open questions

- Memory per domain needs the memory controller delegated to the user manager. Should `aurora-up` check for it and say so, or should the adapter ask systemd for it? Not the compositor's call.
- Should the Deskbar verify a domain against busd (`bus.inspect@v1`), as the indicator does, and mark an unattested one? It would close weakest point 1 and needs the inspect call on the bus thread.
- Keyboard access (focus the box, arrow through rows, Enter to raise). It changes where typing goes, so it is its own decision.
- A tray: the BeOS Deskbar's monitors sat beside the list. The header carries the two machine figures; a per-domain sparkline would need a history the compositor does not keep.

## Consequences

- A new `[deskbar]` table in `contracts/theme/aurora-glass.toml`, spelled out at the parser's defaults, hot-reloaded. The test harness strips it from its copy of the reference theme (as it does `[indicator]`), and `the_reference_deskbar_table_is_the_default_and_its_numbers_are_kept_sane` keeps the file and the parser from drifting.
- Tests that measure whole frames or exact client pixels now switch the Deskbar off in their theme, because a default-on glass box that appears with the first window is a new surface in a scene they measure: `tests/decoration.rs` (BASE), `tests/shell.rs` (FLAT) and `tests/visual.rs` (`prepass_scene_with`). Five tests failed before this was done, each for that reason (a glass-surface count, a pixel diff outside a window, an exact client area); none was loosened. `demos/full-stack/run.sh` needed no change.
- `--test-input state` gains `deskbar`: the box's rectangle in device pixels, or null while it is not drawn.
- The desktop menu gains one entry.
- The Deskbar is another reader of `ClientState.domain`; if 0022's attribution gains an "unreadable" state, the Deskbar should show it.
