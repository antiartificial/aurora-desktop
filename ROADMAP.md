# Roadmap

What is built, what is next, and what is deliberately not being built. Each item says how you
would check it yourself, because a roadmap that cannot be audited is a wish list.

Dates are absent on purpose. This is one person and a set of agents working evenings; the order
is real, the schedule is not.

---

## Built and checkable today

Each of these has a demo under `demos/` that starts a real stack, asserts on decoded output,
and exits non-zero on failure. Run `make demos` to check the lot, or a single `run.sh` to check
one claim.

| | what you get | check it |
|---|---|---|
| **Typed verbs and domains** | Every action is a verb from a manifest. Running one builds a bubblewrap sandbox with its own subordinate uid, granted exactly one input and one output file. | `demos/full-stack/run.sh` |
| **Provenance you can verify** | Every output file records which app, which verb, which grants and which capability handles made it, as a file attribute and as an event in a hash-chained log. | `controller provenance FILE --verify` |
| **A trusted path** | Approval prompts are drawn by the compositor. A client has no protocol object for one and the capture path runs before the overlay, so a client can neither read nor forge a confirm. | `demos/capability-indicator/run.sh` |
| **Haiku's real Interface Kit** | Haiku's unmodified Interface Kit, compiled in place, running as Wayland apps: buttons, checkboxes, radio groups, sliders, tabs, lists, menus, the colour control. | `demos/kitchen-sink/run.sh` |
| **A compositor with real glass** | Dual-Kawase blur, Flip 3D switcher, fractional-scale HiDPI, XWayland, IME, window decorations with edge resize. | `demos/theme-drift/run.sh` |
| **Drag and drop both ways** | Files and rich text in and out of Aurora, with every representation the source offered; a sandboxed app's paths are withheld from the host rather than leaked. | `demos/drag-drop/run.sh` |
| **Live queries** | An index built from provenance events; a saved query streams new files as they are made, and a domain sees only its own. | `demos/live-query/run.sh` |
| **Agents as first-class callers** | MCP tools generated from the same manifests apps use, with destructive calls stopping at a person. | `demos/agent-gateway/run.sh` |
| **Crash survival** | The compositor is a supervised service: kill it and it returns with its scale, background, dock and font, and reports the crash on the bus. | `demos/crash-survival/run.sh` |

---

## Next

### Atlas, the file manager, in reviewable stages

The design is specified before it is built, by a different hand than builds it, and reviewed
against the specification afterwards. See [`docs/atlas-design.md`](docs/atlas-design.md); the
stage table is section 10. Each stage ends where you can open Atlas, see one difference and
judge it.

| stage | what you get |
|---|---|
| ~~0. Foundations~~ | **Done.** Columns that drop rather than clip, an icon model, focus rings, motion as a table of values rather than constants at call sites. |
| 1. The frame | A toolbar of icons with the location in it, columns that fit, right-aligned sizes, relative dates, zebra rows, selection and focus that read correctly. |
| 2. Every row has a face | Nine icon kinds, marks for the special folders, link and broken-link badges. The end of "every row is the same blue folder". |
| 3. The sidebar is a map | Places flat with icons, Filesystem as a tree, and three width bands that collapse the right things in the right order. |
| 4. The keyboard and the pointer | The full key map, type-ahead, rename in place with collisions answered in the row, natural sort, a path field that swaps in. |
| 5. Drag, drop and the edges | The drag ghost and drop outline, and every ugly case looking deliberate: empty, permission denied, 220-character names, CJK, right-to-left overrides, broken links. |
| 6. Motion spent | The reveals, fades and sweeps on their tokens. A working session rather than a review: this is where durations get tuned by eye. |
| 7. Icon files | A real icon theme in SVG and PNG. Needs a rasterising verb, and a wider SVG subset than the toolkit accepts today. |
| 8. Looks like the system | Light scheme, the window title following the folder, tooltips, cursor shapes. |
| 9. Daily driver | Image previews, per-folder view memory, pixel scrolling, the glass sidebar, Trash and Devices, the directory read off the window thread. Seven independent reviews. |

### Performance

Measured, not guessed. See [`docs/performance-architecture.md`](docs/performance-architecture.md).

- **Done:** a frame was waiting on the GPU 28 times and needed to wait once. 24 ms to 3.6 ms at
  1856x1160, no visual change. A drag cost 43 full window reallocations and now costs 2.
- **Next:** render on demand, so an idle desktop produces no frames at all. Blocked on a taste
  decision about the animated background, which dirties every glass surface every frame.
- **After:** the Haiku app_server onto the pooled damage path, where one present measured
  4.5-5.9 ms against 0.19 ms warm.

### Userland utilities

- **Sound** works end to end against real PipeWire for volume. `system.audio.mute@v1` and
  `system.audio.sinks@v1` do not exist yet; only get and volume-set do.
- **Display** reports real refresh and resolution; pointer settings are written but have never
  executed, because libinput only exists on the DRM backend.
- **Networking** and **keyboard** have settings surfaces; neither is complete.

---

## The long goal: bare metal

`--drm` is written and has never run on a display. `--probe-drm` reads connectors without
taking a seat and *has* run on real hardware here, but the modeset, scanout, cursor and
libinput are all unexecuted. Running it from inside a desktop session fails immediately,
because taking DRM master needs a seat and the session compositor holds it.

`harness/bare-metal` pre-flights everything checkable, refuses to launch while anything is
wrong, and leaves one log worth pasting back. It already found that this machine's user is not
in the `video` group.

This is the item that most changes what Aurora is, and it is gated on one session at a text VT.

---

## Not being built, and why

Saying no is part of a roadmap.

- **A display proxy to hold client connections across a compositor crash.** The other half of
  crash survival. Window layout comes back as a placement memory instead; the `wl_display`
  socket dies with the compositor, so clients exit. Deliberate: the proxy is a large piece of
  machinery for a case a supervised restart already handles adequately.
- **Security against a same-uid attacker.** Domains, bus, controller and compositor all run as
  you. The isolation story holds against *confined apps*, not against the person running them.
  A user namespace even grants its owner `CAP_SYS_PTRACE` inside it, so a sandboxed process is
  *easier* to inject into than an ordinary one. Reproducible: `demos/ptrace-ancestry/run.sh`.
- **A tamper-proof log.** It is tamper-*evident*: the hash chain is unkeyed, so whoever can
  write the directory can rewrite history and its anchor together.
- **Mapping a sandboxed app's paths back to host paths on drag-out.** Possible in principle,
  since the controller knows both. Not done, because it would quietly undo the boundary the
  rest of the system maintains. A confined app's file drag out fails loudly instead.
- **An app store, and generations.** Not in scope.
