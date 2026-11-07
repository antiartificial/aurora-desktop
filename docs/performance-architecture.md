# Aurora compositor and toolkit: the performance architecture

The target design for a fluid desktop, and the route to it from the code that exists on
2026-11-07. Written to be refactored toward, so every section gives the target, the current
state, the gap, and the route, and ends in checks a reviewer can run. Costs are in milliseconds
at 1856x1160, scale 1.45 (the user's display), on this machine's RTX 4090 unless a section says
otherwise; the bare-metal section says what changes on a 50 GB/s integrated GPU.

Companion documents: `docs/visual-system.md` (what glass must look like; its section 11 is the
first draft of the blur cost analysis this document completes), decision 0010 (its sequence is
continued here, not restarted), decision 0023 (the toolkit's 8 ms rule), `docs/atlas-design.md`
section 7 (the motion budget, which section 9 below amends), and `docs/bare-metal.md`.

---

## 0. Where the time goes today

Facts first. Everything below is either a number taken from the code's own instrumentation or
a count taken from reading the code. The reasoning starts in section 1.

### 0.1 The compositor, on a quiet machine

`--frame-stats`, one Interface Kit window, nothing moving, the user's geometry:

```
total     p50=3.547  p95=9.610  p99=11.754  mean=4.834
effects   p50=3.510  p95=9.582
prepass   p50=0.492  p95=1.589
chain     p50=1.959  p95=6.325
composite p50=0.568  p95=1.522
```

Read against the code, that frame is **20 blur passes and about 8 composite passes, every
one a GPU round trip**, for a scene in which nothing had changed:

- One window makes **four glass surfaces**: the window's glass, its title bar (chrome
  material), the dock and the Deskbar. The stats row confirms `surfaces=4 prepasses=2`.
- Each surface runs a chain of `2 * blur_passes` = 4 passes (`[glass] blur_passes = 2`). The
  shared prepass is 2 more passes and runs twice: once for the window's glass, and again for
  the title bar, because the window has just been drawn into the scene and lies within the
  bar's blur reach (`Scratch::dirty` overlap). The dock and Deskbar are out of reach of both
  and share the second prepass. 4 x 4 + 2 x 2 = 20.
- Each pass is `bind` (an `eglMakeCurrent` and an FBO switch), `render`, one quad, `finish`
  (an EGL fence), `wait` (`eglWaitSync`, GPU-side in production). `compositor/README.md`
  measured that fixed cost at **~65 us per pass regardless of texture size**, at 1856x1160 and at
  320x200 alike. 20 x 65 us = 1.3 ms, which is most of `chain + prepass` = 2.45 ms.
- Every composite step (clear and background, each window's glass and surfaces, the bar, the
  dock, each chrome panel, the final blit) is its own `bind`/`render`/`finish`/`wait` cycle
  as well, and under `--frame-stats` each is also a CPU block on the fence.

The scene is rebuilt from the clear colour up on every frame. `fx::render_scene` does not
consult the `OutputDamageTracker` it is passed; the tracker is used only by the flat
`render_output` path, which runs when the effects pipeline is off or fails. **In the user's
measurement `fx=1` on every frame, so the buffer-age hypothesis under test in `render.rs`
(a literal `0` passed to `render_output`) cannot account for any of these numbers.** It matters
for the flat path and for winit's no-second-context fallback, and the design below makes it
right there too, but it is not where the 3.5 ms is.

A frame happens unconditionally on all three backends: winit calls `request_redraw()` after
every frame, headless renders on a 16 ms timer, DRM renders on every vblank. Nothing asks
whether anything changed.

Two more facts about the frame the stats do not show:

- The background is `[background] mode = "shader"`, `speed = 1.0`: a time-animated full-screen
  fragment shader. The user's "completely static scene" is legitimately dirty on every frame,
  and so is every glass surface above it (glass must see what is under it). This is a design
  choice with a cost; section 3.4 and the final section return to it.
- winit's two-context frame ends in `glFinish` before the window context samples B's texture.
  The CPU waits for the GPU every frame, so frame time is CPU plus GPU, not the larger of the
  two.

### 0.2 The compositor, on a loaded machine (this session's own measurements)

I reran the scene headless at the user's geometry to vary one thing at a time. The machine was
not quiet: load average 25 on 32 cores, a `rustc` at 866% CPU, and the GPU at 92-96%
utilisation with eleven other `aurora-compositor` processes on it from other agents' test runs.
The absolute numbers are therefore about ten times the user's and are **not frame times**; the
ratios are what survived, and they are the evidence for the design:

| variant (headless, 1856x1160 @ 1.45, `stats 240`) | total p50 | chain p50 | prepass p50 | composite p50 | surfaces / prepasses |
|---|---|---|---|---|---|
| A. glass on, shader background, one Interface Kit window | 41.1 / 38.9 (two runs) | 25.7 / 24.5 | 6.5 / 6.0 | 6.5 / 6.0 | 4 / 2 |
| B. as A, `--freeze-time` (shader drawn at t=0) | 50.7 | 31.7 | 7.4 | 8.3 | 4 / 2 |
| C. as A, background a static gradient texture | 50.7 | 32.0 | 7.4 | 7.8 | 4 / 2 |
| D. glass **off**, shader background, one window | 10.4 / 12.7 | 0 | 0 | 7.1 / 8.7 | 0 / 0 |
| E. glass on, **no client** (dock and Deskbar only) | 22.2 / 21.0 | 12.9 / 12.4 | 3.0 | 3.1 | 2 / 1 |
| F. as A, `blur_passes = 3` | 53.5 | 38.3 | 6.0 | 6.5 | 4 / 2 |

What the ratios say, and they say it independently of the contention:

1. **Cost per pass is constant and does not depend on texture size.** A: 20 passes in 24.5 ms
   = 1.2 ms per pass. E: 10 passes in 12.4 ms = 1.24 ms per pass. The chrome chain runs at a
   quarter of the output resolution and the window chain at half; the per-pass cost is the same.
   Under contention a GPU round trip costs a scheduling slot (about 1.2 ms here); on a quiet
   machine it costs 65 us. Either way the work is proportional to **round trips, not pixels**.
2. **Three passes instead of two cost exactly 1.5x** (38.3 / 25.5): 6 passes per chain against
   4. Linear in pass count, as 1 predicts.
3. **The animated background costs nothing measurable itself** (B and C are within noise of A,
   if anything slower in that run). Its cost is that it makes the whole scene dirty every
   frame, which matters only once the pipeline can skip clean work. Today nothing is skipped,
   so the shader is free; after sections 1 and 3 it is the single thing standing between an idle
   desktop and zero frames.
4. **One window costs as much as the empty desktop** (A minus E = 18 ms = two surfaces and a
   second prepass). The marginal cost of a window is about 45% of the empty desktop, and it is
   paid whether or not the window changed. This is what "cost grows with window count" means
   in round trips: +10 per window.
5. **With glass off, drawing a handful of quads still took 7-9 ms** (D), because the composite
   phase is itself five or six `finish`/`wait` cycles. Even the flat scene pays per round trip.

### 0.3 The client side

Measured in this session with a C micro-benchmark at 1856x1160 (8.6 MB per frame), on the
same loaded machine, so treat these as upper bounds too:

| operation | cost |
|---|---|
| one-shot present: `memfd_create` + `ftruncate` + `mmap` + full copy + `munmap` | **4.5-5.9 ms** (page faults on a fresh mapping dominate) |
| pooled present, full copy into a warm mapping | 0.19 ms (cache-warm; a cold copy at DRAM bandwidth is nearer 1 ms) |
| pooled present, 35 damaged rows of 1856 px | 0.004 ms |
| the app_server's second full copy into `last.argb` | 0.25 ms |
| a full-window CPU fill | 0.11 ms |

And from reading the code:

- The **Haiku app_server path presents one-shot**. `PresentWindow` calls `au_wl_win_present`,
  which makes a fresh memfd, copies the whole canvas and damages the whole surface, on every
  present; it also copies the whole canvas into `last.argb` first. It has a `dirty` region
  (`SWindow::dirty`) that it never passes to the presenter. That is the user's measured 9.5 ms
  resize: a reallocation, a fill, and a one-shot present.
- The **aurora::ui path presents pooled with damage** (`au_wl_win_present_damage`, two slots, an
  owed-damage backlog per slot), which is right, and was wrong until an hour before this
  document (section 2.4).
- **No client uses `wl_surface.frame`.** `present_wl.c` never requests a frame callback. The
  toolkit's loop sleeps on its own tick (16 ms while animating, otherwise the shortest
  `TickMs()`) and presents whenever `dirty_`; the compositor sends every window a frame callback
  after every frame it renders, and nobody listens. Client and compositor frame rates are
  uncorrelated.
- **The compositor sends an xdg configure on every pointer motion** of a resize grab
  (`resize_grab.rs`: `send_pending_configure()` in `motion`). A 60-motion drag was 43 window
  reallocations in the app_server until it learned to apply only the last size in a drained
  queue (now 2). The toolkit coalesces too (`want_w_/want_h_`). Every future client would have
  to rediscover this; the fix belongs in the compositor (section 6).
- The compositor's shm import uploads **only the damaged rectangles** (`glTexSubImage2D` per
  damage rect in Smithay's `import_shm_buffer`), so client damage already saves upload
  bandwidth. The full upload happens only when the texture is created, which is every resize.
- Text is **shaped on every draw**: `au_text_draw` and `au_text_width` call HarfBuzz
  (`fonts.cpp: Shape`) per string per frame. Glyph bitmaps are cached (hash-keyed, bounded at
  8192); icons are cached; shaped runs are not.
- The toolkit's `Window::Draw` walks the entire node tree and relies on the canvas clip to
  reject pixels outside the damage. Draw cost scales with tree size, not damage size; the
  Atlas spec notes this stops being fine at thousands of nodes and relies on list
  virtualisation to prevent it.

---

## 1. The frame

### 1.1 Target: render on demand, present on the output's clock

A frame is rendered when, and only when, there is a reason, and it is presented at the next
opportunity the output offers. The compositor keeps one **frame scheduler** per output, shared
by the three backends, with two inputs:

- **Demand.** Any of: a client commit carrying damage or a frame-callback request on a mapped
  surface; a change to a shell texture (dock, Deskbar, decoration, menu, clock); a running
  compositor animation (arrival, departure, switcher, menu, deco ease); the pointer moving on a
  backend without a hardware cursor plane; an output change; the animated background, while it
  is allowed to animate (section 3.4); an explicit request (`--test-input frame N`, a capture).
- **Opportunity.** winit: the host's redraw request (the host is the clock). DRM: the vblank
  event, offset by a deadline estimate (section 7). Headless: the 16 ms timer, which stays a
  timer but renders only on demand so idle-frame checks can run headless.

A frame with no demand costs **0 ms** and produces no frame callbacks (clients that asked for
one are waiting for their own commit to be shown, and will be, as soon as there is a frame).
The first frame after an idle period is a full repaint (buffer age 0 on DRM, scene cache reset
otherwise) and is allowed to cost the worst case once.

### 1.2 The pipeline, client damage to scanout

```
client:  input -> node damage -> Draw (clip to damage) -> copy damage to pooled shm -> damage_buffer + frame + commit
         (toolkit draws only after the previous frame's callback, section 6)
comp:    commit -> import damaged rects -> surface damage -> output damage (+ blur reach)
         -> recompose dirty region of the persistent scene (section 3)
         -> present (winit: one textured quad + swap; DRM: page flip of the scanout buffer)
output:  vblank -> frame callbacks to clients whose buffers were shown -> clients draw the next frame
```

### 1.3 The budget, split by phase

Two columns because the toolkit, the compositor and the shell must all still fit at 120 Hz,
and decision 0023's "8 ms" was written for 60 Hz. Compositor numbers are for four glass
surfaces at 1856x1160; "typical" is a frame in which one window's content changed and the
backdrop under the chrome did not; "worst" is everything dirty (a window move, the first frame
after idle, the animated background on).

| phase | 60 Hz (16.67 ms) | 120 Hz (8.33 ms) | notes |
|---|---|---|---|
| client: input dispatch + layout + draw | <= 8.0 | <= 4.0 | decision 0023's rule, restated as half the period; typical frames are far below (a hover <= 0.2) |
| client: copy damage into the pool + commit | <= 0.3 | <= 0.3 | measured 0.004 ms for 35 rows; a full window about 1 ms cold, which only a resize or first frame should pay |
| compositor CPU: prologue, element collection, damage, submission | <= 1.0 | <= 0.5 | today the prologue formats cache keys and polls the theme files every 250 ms; fine, but it is in the budget |
| compositor GPU: upload of damaged client pixels | <= 0.3 typical | <= 0.3 | full 8.6 MB upload about 0.7 ms over PCIe; damage-proportional otherwise |
| compositor GPU: scene (clear + background) | 0 typical, <= 0.2 worst | same | 0 when the scene texture persists and the background is static |
| compositor GPU: prepass | 0 typical, <= 0.15 worst | same | 2 passes, when something within a surface's reach changed |
| compositor GPU: blur chains | <= 0.1 per surface recomputed, 0 cached | same | 4 passes per surface, submitted as one unit of GPU work (section 3.1) |
| compositor GPU: composite | <= 0.3 | <= 0.3 | all glass and surfaces in one `Frame` |
| compositor GPU: present | <= 0.2 (winit blit + swap) / 0 (DRM flip) | same | |
| **compositor GPU total** | **<= 1.0 typical, <= 2.5 worst, <= 6.0 p99** | **<= 1.0 typical, <= 2.0 worst, <= 3.0 p99** | the p99 at 60 Hz is C7.1, unchanged |
| compositor render-time standard deviation | <= 1.0 | <= 0.5 | C7.5; variance is latency (section 7.2) |
| slack (vblank jitter, scheduling, the host on winit) | >= 3.0 | >= 1.5 | |

Idle desktop: 0 frames per second, 0 ms. Pointer motion alone, on DRM: 0 composites (hardware
cursor plane); on winit: 0 (the host draws the cursor).

### 1.4 Current state and gap

| | today | target |
|---|---|---|
| frames rendered while idle, 60 Hz output | 60/s, 3.5 ms each (210 ms of GPU per second doing nothing) | 0/s |
| GPU round trips per frame, 1 window | about 28 | <= 4 (upload, prepass+chains as one, composite, present) |
| chain + prepass, 1 window, static | 2.45 ms p50 | <= 0.2 typical, <= 0.6 worst |
| composite | 0.57 ms p50 | <= 0.3 |
| p95 / p50 ratio | 2.7 | <= 1.5 (C7.5 implies it) |
| CPU and GPU | serialised (`glFinish` on winit, `wait` per phase under stats) | overlapped: CPU submits frame N+1's prologue while the GPU finishes N |

### 1.5 Route

1. **Counters before timers.** Add to `stats.rs` per frame: round trips (`finish` calls), blur
   passes, surfaces recomputed vs cached, bytes uploaded, frames skipped for no demand. Counting
   is deterministic and survives a loaded machine; the timing checks in section 8 are not the
   only checks.
2. **A `FrameScheduler`** (`compositor/src/frame.rs`): `demand(reason)`, `opportunity()`, and
   `take()` which returns whether to render. Backends call `take()` where they render today.
   `after_frame` sends frame callbacks only when a frame was rendered (it already runs only
   then). The commit handler calls `demand(Commit)`; the shell's prepare functions call
   `demand(Chrome)` when a cache key changes instead of being polled during render; animations
   call `demand(Animation)` from their tick.
3. **GPU timer queries** (`GL_EXT_disjoint_timer_query`, present on this driver) so
   `--frame-stats` can stay on in normal use without blocking the CPU. The CPU-blocking mode
   stays as `--frame-stats-sync` for comparison.
4. The winit `glFinish` becomes a fence: B's `finish()` returns a `SyncPoint`; A `wait`s on it
   GPU-side before sampling. The CPU is free to start the next prologue.

**Checks.** P1.1 Headless, `--freeze-time`, gradient background, one static client: after the
first frame, frames rendered over 3 s is 0 (counter from `stats`, via `--test-input`). It must
fail today (it renders about 180). P1.2 The same scene with one `pointer` motion per 100 ms:
frames rendered <= motions + 1. P1.3 Round trips per frame with 4 glass surfaces <= 4 (counter);
today about 28. P1.4 A 300-frame window of continuous client commits at 60 Hz shows frame
interval p99 <= 20 ms (C7.2) and no frame callbacks sent in a frame that was not rendered.
P1.5 `--frame-stats` on, no `--frame-stats-sync`: total p50 within 5% of a run with stats off
(measured by frame interval), proving the instrumentation no longer blocks.

---

## 2. Damage, properly

### 2.1 The one invariant

The present-pool bug, the title-bar strip bug and the resize flicker were one mistake in three
places: a partial update was applied to a buffer that did not hold the state the update was
relative to. The rule that makes a damage model verifiable rather than hopeful:

> **Every cached pixel buffer carries the identity of the state it holds. A partial update is
> legal only against a known identity; against an unknown one, the update is the whole buffer.**

Buffer age is this rule for scanout buffers (age 0 means "unknown, repaint everything");
`pbuf.owed` is this rule for the client pool; `Scratch::dirty` is this rule, approximately,
for the prepass. The design gives every level the same shape so the same check applies to all.

### 2.2 The four levels

| level | where | what the damage is relative to | identity |
|---|---|---|---|
| node | `ui::Damage` (toolkit) | the surface as last drawn | implicit: one surface, one drawer |
| buffer | `wl_surface.damage_buffer` from `present_wl` | the buffer the compositor last displayed for this surface | per pool slot: `owed` + `owes_all` |
| output | Smithay's per-element damage and `OutputDamageTracker` | the output buffer being rendered into | **buffer age** (DRM swapchain) or scene-texture generation (winit, headless) |
| effects | the persistent scene texture and per-surface blur caches | the scene as it was when the cache was filled | a generation counter per cache entry plus the dirty rects since |

Today levels one and two exist and are correct; level three exists but is fed `0` and is
bypassed when effects are on; level four exists only within a frame (`Scratch::dirty` is reset
every frame because the scene is rebuilt every frame).

### 2.3 Target: output and effects damage across frames

- The composed scene (`Scratch::scene`) **persists across frames** and carries a generation.
  Each frame computes the output damage: client surface damage (from Smithay's element
  damage), shell texture changes, moved or resized elements (old and new rectangles),
  animation rectangles, plus the background if it animated. Nothing outside that region is
  redrawn into the scene.
- Glass expands damage: a surface whose glass lies within `support(radius, passes, kind)` of
  a dirty rect is recomputed (section 3.2), and its own rectangle joins the damage. This is
  the one place where damage grows, and it is bounded by the reach table in
  `docs/visual-system.md` 3.2 (33 px for window glass at the shipped radius, 62 px for
  chrome).
- **Present damage.** winit: the blit of the scene into the window is clipped to the damage
  (`present_texture` takes a damage slice today and is given `full`). DRM: the damage is
  handed to `queue_buffer` so KMS can do a partial update where the hardware supports it.
  Headless: the damage is recorded in stats so the check below can read it.
- The flat path gets the real buffer age (`GbmBufferedSurface::next_buffer()` returns it;
  DRM discards it as `_age` today) and the scene-generation age on the other backends.
  Captures that need a full frame read the scene texture, which is always complete, rather
  than forcing a full repaint.

### 2.4 What makes it verifiable

Three checks, each of which must be shown to fail when a known damage bug is reintroduced:

- **Toolkit:** `AURORA_UI_DAMAGE_CHECK=1` makes `Window::Draw` also draw the full tree into a
  second surface with no clip and compare the two byte for byte after each frame; a mismatch is
  printed with the first differing rectangle and counted. The headless `tk_layout` run and the
  demos run with it on. Reverting the `TextNode` growing-label fix (the fifth layout-staleness
  bug) must trip it.
- **Client pool:** `harness/present-pool` already proves the two failure modes
  (`demos/buffer-pool/run.sh`). It stays, and gains a third case: a one-shot fallback mid-sequence
  (both slots busy) followed by a pooled present.
- **Compositor:** `--damage-check` renders every frame twice, incrementally and from scratch,
  into two textures and compares them; the first mismatch is dumped (`trusted::dump`) and the
  frame counter in stats records it. `compositor/tests` runs a scripted session (open, type,
  move, resize, close, menu) with it on. Reintroducing the title-bar strip bug (the damage
  rectangle relative to `dst`) must trip it.

**Checks.** P2.1 The three checks above exist, and for each a one-line revert that makes it
fail is named in the test's comment and was run once. P2.2 Headless, one client typing one
character per frame in a 600x400 window: output damage per frame <= the window rectangle plus
33 px on each side (counter); the compositor never redraws the dock. P2.3 Moving a window with
`move N X Y` across the dock produces damage covering old and new rectangles plus 62 px and
nothing else. P2.4 On DRM the age passed to `render_output` is the swapchain's and is never 0
after the first three frames while the output is steady (log line).

---

## 3. Glass and effects

Dual-Kawase stays; it is the right kernel (visual system 3.2) and the look is the system's
identity. What changes is how often it runs and how it is submitted.

### 3.1 Submission: one unit of GPU work, not 20 round trips

Target: the prepass and every chain that needs recomputing this frame are submitted inside
**one** Smithay `Frame` through `GlesFrame::with_context`, as raw GL: one FBO per chain level
kept alive in `Scratch` (not re-created by `bind`), `glBindFramebuffer` and `glViewport` per
pass, one draw per pass, no `eglMakeCurrent` and no fence between passes. GL guarantees
ordering within a context: a pass that samples the previous pass's target sees its writes.
One `finish()` and one `SyncPoint` for the lot, waited on GPU-side by the composite that
samples the results.

What it buys: at 65 us per round trip and 20 passes, about **1.3 ms of the 2.45 ms chain +
prepass goes away** on the quiet machine, leaving the actual blur work (bandwidth-bound; the
visual system's model says 0.11 ms for the whole chain, so expect 0.3-0.5 ms with driver
overhead). Confidence: high that round trips are the dominant term (section 0.2, points 1 and
2); medium on the exact remainder, because it depends on this driver's cost for
`glBindFramebuffer` versus `eglMakeCurrent`. Decision 0010 item 8 named this; the measurement
now justifies it.

Risk, and the mitigation: it is the kind of change that works on NVIDIA and fails elsewhere.
It ships behind `[glass] submit = "batched" | "per-pass"` with the per-pass path kept, and the
`--damage-check` comparison of section 2.4 is the correctness test: both submission modes must
produce byte-identical scenes.

### 3.2 When glass must recompute, precisely

A glass surface's blurred backdrop is a pure function of:

1. the scene pixels inside `rect ⊕ support(radius_dev, passes, kind)`, where `support` is the
   function in `fx/blur.rs` (tested against the measured one-sided reach, +7%),
2. the surface's rectangle size (the result texture's size) and its position modulo the chain's
   downsampling (a move by one device pixel changes which pixels fall in which half-res
   texel; a move is a recompute),
3. the theme's glass parameters, the output scale, the chain format, and `linear`.

"Behind the glass changed" means: the output damage of this frame, including other surfaces'
glass that was recomputed, **intersects `rect ⊕ support`**. Anything else, the cached result
is valid. Each cache entry records the scene generation it was computed at; the scheduler keeps
the dirty rects since any live generation (a short ring), so the test is a rectangle
intersection, never a hash of pixels.

The painter's order is kept: surfaces are processed back to front, and a recomputed surface
adds its own rectangle to the dirty set before the next surface is tested, which is what
`Scratch::mark_dirty` does within a frame today; the change is that the dirty set and the
cache survive the frame.

Cost model for a typing frame in one window with the dock and Deskbar elsewhere: the window's
own glass is under the client's surface and is recomputed only if the damage reaches past the
client's edges (it does not; client damage is inside the client); the title bar's reach
(62 px) does not touch a caret in the middle of a window; the dock and Deskbar are untouched.
**Zero blur passes.** A window move: every surface the window passes within reach of, which is
the worst case and bounded by the frame budget's "worst" column.

### 3.3 Resolution and reach

Already right: window glass chains from the half-resolution prepass, chrome from the quarter
(decision 0010 item 5). Two additions, both cheap after 3.1:

- **Scissor the chain to `rect ⊕ support`** at each level (decision 0010 item 9). Pixels matter
  less than passes on this GPU, but at 3840x2160 the full-screen prepass alone moves 50 MB per
  frame, and on an integrated GPU (section 7) the bandwidth is the budget. Buys 2-4x of the
  remaining blur work for typical window sizes.
- **Above 2560 px wide, start the window chain from the quarter** (item 6): the sigma stays in
  logical units and the work stays constant per logical pixel.

### 3.4 The fallback ladder

Decision 0023 states the principle: a window opens opaque and is **promoted** to glass when the
budget allows; glass is a level of detail. The compositor has no such gate today. Target: one
gate, the same rule as the toolkit's `motion_allowed` and the Atlas spec's 7.2, so that a
reviewer learns one rule (section 9 reconciles the two existing wordings):

> **Degrade when 3 of the last 8 rendered frames exceeded 75% of the frame period on the
> GPU timer. Promote one level after 60 consecutive frames under 50%.**

The levels, each a strict subset of the work of the one above, each a theme-expressible state
so it can be pinned for a screenshot and so the look at each level is designed rather than
accidental:

| level | what runs | what it costs, worst-case frame (quiet machine, after 3.1) |
|---|---|---|
| 0 | window glass at half, chrome at quarter, passes 2, shadows and bevel when they land | <= 2.5 ms |
| 1 | windows opaque with the tinted flat material; chrome glass only | <= 1.0 ms |
| 2 | chrome chains from the quarter with passes 1; the background shader frozen | <= 0.6 ms |
| 3 | no blur; chrome drawn with its tint over a dimmed backdrop; the flat path with buffer age | <= 0.3 ms |

The gate records every demotion and promotion in stats and logs it once per change, so a build
that lives at level 1 shows up in the report, which is the toolkit's `motion_degraded`
principle: a safety mechanism nobody has watched trip is one that does not work. A reviewer
can force a level (`[glass] level_max`) and a test can set `AURORA_FX_SLOW_MS` to burn time in
the composite and watch the ladder descend and climb back.

### 3.5 The animated background

Section 0.2 showed the shader costs nothing to draw and everything to allow: while it animates,
every glass surface is dirty every frame, and sections 1 and 3.2 buy nothing while the desktop
is idle. This is the trade the user has to make (final section). The design supports all three
answers, as theme state:

- `[background] animate = "always"`: today's behaviour; the idle desktop renders every frame at
  the worst-case cost (<= 2.5 ms after 3.1, which fits the budget but is 150 ms of GPU per
  second doing nothing a person is looking at).
- `animate = "while-active"` with `idle_after_ms`: animates while there has been input or a
  client commit within the last N ms, then freezes; the first frame after it freezes is a full
  recompute, then zero.
- `animate = "never"`: the shader is evaluated once at `t = 0` into a texture (the gradient path
  already exists for the static case).

Whatever the answer, level 2 of the ladder freezes it, so a machine that cannot afford it does
not pay for it.

**Checks.** P3.1 Headless, gradient background, one client typing in the middle of a 600x400
window: blur passes per frame = 0 after the first frame (counter). Today 20. P3.2 The same
with a window move across the dock: passes <= 4 per surface within reach and 0 for others; the
recomputed set is logged by surface. P3.3 Batched and per-pass submission produce
byte-identical scene textures over the scripted session of P2.1. P3.4 With four glass
surfaces, effects p99 <= 6.0 ms and sd <= 1.0 ms over 300 frames on a quiet machine (C7.1,
C7.5, unchanged). P3.5 `AURORA_FX_SLOW_MS=8` descends the ladder to level 3 within 8 frames
and climbs back within 70 after it is unset; the transitions are in the log and the stats
file. P3.6 Each ladder level has a reference capture in `compositor/tests` and a theme
knob that pins it.

---

## 4. Client buffers

### 4.1 Target: shm with damage for software clients, dmabuf import for everyone else

Every Aurora client today is a software rasteriser: the Interface Kit's `Canvas`, aurora::ui's
`au_surface` (the same `Canvas`), the chooser, Atlas. For a CPU-drawn, text-heavy, small-damage
UI, **shm with damaged-rectangle copy and damaged-rectangle upload is the right path**, not a
compromise: the copy is microseconds (0.004 ms for 35 rows), the upload is proportional to the
damage, and the alternative (rasterising straight into a GPU-visible linear buffer) is slower on
a discrete GPU because the mapping is uncached and the rasteriser reads back what it blends.

dmabuf import is wanted for three things that are not the toolkit: GPU clients that will exist
(Xwayland with GL, a browser, anything using EGL), **zero-copy capture** (export the scene
texture rather than `glReadPixels`), and **direct scanout** of a fullscreen client on DRM. It
costs: a `DmabufState` global and `ImportDma` handling in the compositor (Smithay provides
both; about a day), format and modifier negotiation per backend, and a test that a dmabuf
client's frame shows up headless (an `EGLDevice` client can run headless on this machine).
Buys: for a 1856x1160 GPU client at 60 Hz, 0.7 ms of upload per frame goes to zero, and
capture stops stalling the pipeline on a read-back.

### 4.2 What must keep working on shm, and what changes

| path | today | target |
|---|---|---|
| aurora::ui | pooled, damage, 2 slots | unchanged in shape; 3 slots when frame callbacks are not yet honoured by the compositor (never block a frame on a busy slot) |
| Haiku app_server | **one-shot**: memfd + full copy + full damage per present, plus `last.argb` | `PresentWindow` calls `au_wl_win_present_damage` with `SWindow::dirty` (it has the region; it clears it in `EndUpdate`); `last.argb` is copied only when a dump or `last_frame` reader asks (lazy snapshot, or a copy of the dirty rects into it). **Saves 4.5-5.9 ms per present, and a present per draw.** Confidence high; it is the measured difference between the two paths. |
| screen capture | `glReadPixels` of the framebuffer after the scene | reads the persistent scene texture; a capture is no longer a reason for a full repaint (removes the `buffer age 0` comment's justification) |
| headless tests | shm clients, offscreen target | unchanged; gains the counters |
| Xwayland | shm through Smithay | unchanged; dmabuf when the X client uses GL |
| software client into dmabuf | not done | an experiment on the DRM path on an integrated GPU only (section 7.4), where the copy is the cost |

### 4.3 Resize is the expensive case, by design

A resize recreates the client's shm buffer (both slots), the compositor's texture (full upload,
0.7 ms), the toolkit's canvas (`Canvas::Resize` keeps the overlap: 2.7 ms measured, down from
9.5), and relayout. That is why section 6 limits configures to one per frame: the cost is
legitimate once per frame and absurd 43 times between two frames. Within one frame, a resize of
an 8.6 MB window should cost the client <= 4 ms (canvas 2.7 + one pooled full copy <= 1) and
the compositor <= 1 ms (upload 0.7 + relayout of chrome).

**Checks.** P4.1 The app_server presents one-shot zero times after the first present of a
window (counter in `present_wl`, printed by `AURORA_APPSERVER_TRACE`); `harness/present-pool/
chunky.sh` reads it. P4.2 Bytes copied per present by the app_server during a caret blink <=
the caret rectangle's rows (trace line). P4.3 Bytes uploaded per frame by the compositor for a
typing client <= 2x the client's damage (counter; Smithay uploads per rect so the bound is tight).
P4.4 A dmabuf client (an EGL example in `compositor/examples`) maps and shows headless, with
upload bytes 0 for its frames. P4.5 Capture during a static scene causes 0 extra frames (P1.1
with a capture in the middle).

---

## 5. The toolkit's draw path

### 5.1 The CPU rasteriser is the right answer, with conditions

`Canvas` stays. The reasons are not nostalgia: a GPU-rasterised toolkit means an EGL context,
a swapchain and 30-60 MB of VRAM per client, a shader path for every primitive the canvas has,
anti-aliasing and text that differ by driver, and the loss of the headless pixel-identical
test idiom the whole suite depends on (`tk_layout`, the demos, the decision-record captures).
For a UI that draws text, lines, fills and icons into rectangles that change a few percent of
the window per frame, the CPU path is cheaper in every dimension once it draws only what
changed and never re-rasterises what it already has.

The condition under which this changes, written down so it is a decision and not drift: a
client whose **typical** frame (not resize, not first frame) needs more than half the frame
period of raster at the target refresh (4 ms at 120 Hz) with the caches of 5.2 warm. Today's
nearest candidate is a full-window 4K icon grid; the Atlas spec already virtualises it. A video
player or a canvas application is such a client, and the answer for it is a dmabuf surface of
its own (section 4.1), not a GPU toolkit.

### 5.2 What must never be re-rasterised, and what each cache is keyed on

| what | today | target key | why |
|---|---|---|---|
| glyph bitmaps | cached, hash, bounded 8192, key (font, style, size, scale, glyph, subpixel bucket) | unchanged | the fix that took an icon grid from 8.25 ms to under budget |
| shaped runs (HarfBuzz output) | **not cached**: `Shape` per draw and per `au_text_width` | (font key, size, string) -> glyph ids and advances, LRU of 4096 runs | a 25-row list shapes 75 strings a frame; shaping is the most expensive per-string step left |
| icons | cached per (source, w, h, scale, on_accent, pane) | unchanged; the `IconSource` seam of the Atlas spec | |
| rounded-rect and ellipse coverage | computed per fill | (w, h, rx, ry, scale) -> 8-bit coverage mask, small LRU | every button, every row halo; the maths is the same each frame |
| gradient strips | computed per fill | (stops, length) -> strip | the sidebar and toolbar fills |
| laid-out text lines (wrapping) | `TextNode` caches lines per (text, width) | unchanged | |
| the previous frame's pixels | the surface itself | unchanged: damage makes this the retained layer | no per-node bitmaps; one layer is enough when damage is right |

### 5.3 Draw proportional to damage

- Container nodes (`Split`, `Rows`, `Either`, lists) skip children whose `frame` does not
  intersect the clip. One rectangle test per child; it turns the walk from O(tree) to
  O(visible ∩ damaged).
- `FillRect` fills rows with a 32-bit fill (`std::fill_n` per row) rather than a per-pixel
  loop with a bounds computation per pixel; `DrawGlyph` keeps its branch per pixel (coverage
  is sparse).
- `Damage::kMax` stays 16 and the collapse to a full frame stays; the Atlas spec's rule that
  row position never animates is what keeps the list short.

### 5.4 Targets and current state

| frame kind | target at 1856x1160 | today |
|---|---|---|
| hover: two rows restyled | <= 0.2 ms draw, damage < 0.1 of the window (C6.2) | 0.03 of the window (Atlas spec 7.2) |
| typing: one row and the caret | <= 0.3 ms | not measured at this geometry |
| a reveal tick: 60% of a 960x640 window | <= 2 ms | 1.5-2 ms (Atlas spec 7.2) |
| full window, 25-row list with sidebar | <= 4 ms | 1.23 ms per repaint at the size `docs/aurora-ui.md` measured; not measured at 1856x1160 |
| full window, 48 px icon grid | <= 6 ms | was 8.25 ms before the glyph-cache fix; after it, not recorded |
| resize: relayout + keep-overlap + full draw | <= 8 ms | 2.7 ms canvas resize + draw |

The "not measured" rows are honest: this session's machine was loaded and a toolkit timing
taken on it would mislead. `Window::timing()` and `damage_stats()` exist; `ATLAS_REPORT=1`
prints them; the perf harness of section 8 records them per scenario.

**Checks.** P5.1 With `AURORA_UI_DAMAGE_CHECK=1` the demos and `tk_layout` report 0
mismatches. P5.2 HarfBuzz is called 0 times during a 2 s hover sweep over an unchanging list
(counter in `fonts.cpp`, printed by `ATLAS_REPORT`). P5.3 A 25-row list draws <= 30 nodes per
hover frame (node-visit counter), not the whole tree. P5.4 The full-window timings above,
recorded by the harness on a quiet machine, with the icon grid under 6 ms. P5.5 `Canvas::
FillRect` of a full 1856x1160 window <= 0.15 ms (micro-benchmark; 0.11 measured for a raw fill).

---

## 6. Input to photon

### 6.1 The pacing contract

Three clocks exist today and none is coupled to another: the output's refresh, the compositor's
render loop, and each client's sleep-based loop. The target has one clock, the output's, and two
rules:

1. **The compositor coalesces configure to one per frame.** The resize and move grabs record the
   wanted geometry on motion; the scheduler sends at most one `xdg_toplevel.configure` per
   rendered frame per toplevel, carrying the latest size (Resizing state set), and one final
   configure on release. Clients that already coalesce lose nothing; clients that do not
   (every future one) are protected.
2. **A client draws for a frame when the compositor tells it to.** `present_wl` requests a
   `wl_surface.frame` callback on every commit; the toolkit's loop treats the callback as its
   tick when one is outstanding (falling back to the sleep after 50 ms so a hidden or occluded
   window still gets its state applied), so it draws once per displayed frame and never twice.
   Animations tick on the callback: at 120 Hz they tick at 120 Hz without a code change, which is
   why the Atlas spec's durations are in milliseconds and not in frames.

The compositor sends frame callbacks right after a frame is presented (today: after render; on
DRM: on the vblank event), so a client has the whole next period to draw. On winit the host's
redraw request plays the vblank.

### 6.2 Four interactions, each with its path and its latency target

| interaction | who does the work | per-frame cost | target input-to-photon |
|---|---|---|---|
| **drag (move)** | compositor only: `space.map_element`, damage old + new rectangles, recompose; client untouched | glass under the window's new place recomputed (one chain, <= 0.1 ms); chrome in reach recomputed | pointer sample to flip: <= 1 frame period |
| **resize** | compositor sends one configure per frame; client relayouts, resizes canvas (2.7 ms), draws, commits; compositor draws the client's last buffer anchored at the grab's fixed edge until the new one arrives (never stretched) | client <= 8 / 4 ms; compositor <= 1 ms (texture recreate + upload) | pointer sample to a frame at the new size: <= 2 periods (one for the client, one to composite) |
| **scroll** | client: `Rows` damage the viewport, draw, commit | <= 2 ms at 25 rows (the Atlas spec's number); the compositor recomputes no glass (damage is inside the client) | wheel event to photon: <= 2 periods; with `scroll` token at 0 it is one client frame per notch |
| **typing** | client: the field's `DamageHint` (two rects), draw, commit | <= 0.3 ms client, <= 0.5 ms compositor | key to photon: <= 2 periods |

The resize storm is the first row's defect applied to the second: 60 motions in one frame
produced 60 configures (and 43 reallocations). With rule 1 they produce one.

### 6.3 Current state and route

| | today | target |
|---|---|---|
| configures per resize drag of 60 motions | 60 (+1 on release) | <= frames rendered during the drag (+1) |
| client frames per compositor frame while animating | uncorrelated (toolkit sleeps 16 ms; compositor runs on the host clock) | 1 |
| frame callback requests by clients | 0 | 1 per commit |
| animation tick source | `au_wl_wait(16)` | the frame callback, 50 ms fallback |

Route, in order: (1) the grab throttle in the compositor, with `chunky.sh` as the check (it
already counts); (2) `present_wl` requests the callback and exposes `au_wl_frame_pending()`;
(3) the toolkit loop waits on "callback arrived or 50 ms"; (4) `wp_presentation` feedback so
`ATLAS_REPORT` can report real present times and the toolkit can measure its own latency rather
than its draw time.

**Checks.** P6.1 `demos/resize-storm`'s bound tightens from `<= 8` reallocations to `<= frames
rendered during the drag + 1`, read from the compositor's counter; the 60-motion drag lands at
the dragged size. P6.2 Headless, a toolkit client running a 160 ms reveal: client commits during
the reveal = frames rendered during it (+-1), at both the 60 Hz headless timer and a `--refresh
120` headless mode (the timer at 8.33 ms). P6.3 Key-to-commit latency reported by the toolkit
for a typing script is <= 1 period at p95. P6.4 A `move N X Y` drag of a window produces 0
client commits (the client is not involved in a move) and one frame per motion at most.

---

## 7. Bare metal

`--drm` has never run on a display (`docs/bare-metal.md`); nothing below is to be built before
the first TTY session confirms the modeset, and nothing in sections 1-6 depends on it. What the
DRM path changes is that four things the nested backends approximate become real.

### 7.1 Buffer age and page flips are real

`GbmBufferedSurface::next_buffer()` returns the buffer's age; `drm.rs` discards it (`_age`).
With the scheduler of section 1 and the damage of section 2 the age feeds `render_output` on the
flat path and the scene-generation check on the effects path, and `queue_buffer` receives the
frame's damage. A flip that is not needed is not queued: an idle output simply has no frame in
flight, and the vblank handler, which today "carries on" rendering the next frame
unconditionally, asks the scheduler first.

### 7.2 Vsync is real, so a deadline is real, so variance is latency

On winit a slow frame is absorbed by the host; on DRM a frame that misses the flip deadline
shows the previous buffer for a whole period: visible, counted, and the thing the user will
notice first. The scheduler therefore renders **as late as it safely can**: at
`vblank + period - estimate - margin`, where `estimate` is the p95 of the last 30 frames' GPU time
from the timer queries and `margin` is 1.5 ms. A low-variance pipeline composites late and
delivers fresh input; a bursty one is forced early and adds latency to every frame, which is why
C7.5's standard deviation bound is a budget and not a diagnostic. Missed flips are a counter
in stats and a check below.

The `glFinish` before `queue_buffer` ("no fence: finish the GPU work before KMS reads the
buffer") becomes a fence: `queue_buffer(Some(sync), ...)` hands KMS an in-fence and the CPU is
free. On a driver without in-fences the finish stays, behind the same flag as 3.1.

### 7.3 The cursor is a plane

The hardware cursor exists in `drm.rs` (legacy ioctl, 64x64). Pointer motion then costs no
composite, which is the single largest saving of the DRM path over winit for an idle desktop
with a moving pointer, and it is why section 1's demand list says "on a backend without a
cursor plane". A client-set cursor image (the toolkit's `au_wl_win_set_cursor`, Atlas spec
section 11) is a cursor-plane update, not a frame.

### 7.4 A different GPU is a different budget

The development machine is a 1008 GB/s discrete GPU; a laptop that boots Aurora will have a
50-100 GB/s integrated one, and a CPU that shares that bandwidth with the toolkit's rasteriser.
Three consequences, each already in the design:

- Bandwidth becomes the cost, so 3.3's scissoring and quarter-resolution start are not
  optional there; the visual system's `fp16` precision option costs 27% of such a GPU's
  bandwidth and stays off.
- A software client rasterising into a **linear dmabuf** (gbm map, write-combined) removes the
  compositor's upload entirely, and on an integrated GPU the memory is the same DRAM, so the
  experiment in 4.2 is for this path: measure canvas draw into a mapped dmabuf against malloc
  plus shm plus upload, and keep whichever is faster per frame kind.
- Direct scanout of a fullscreen dmabuf client (a video player, a game) skips composition
  entirely; Smithay's `DrmCompositor` does the plane assignment. This is the one place the
  design would adopt `DrmCompositor` over `GbmBufferedSurface`, and it is after the first
  modeset works, not before.

### 7.5 Missing a deadline

The ladder of 3.4 is the response to sustained misses; the response to a single miss is nothing
(the frame shows a period late). What must not happen is the nested backends' habit of
rendering regardless: a late frame on DRM must not start the next frame late as well, so the
scheduler's opportunity is always the next vblank, never "as soon as the last frame finished".

**Checks.** P7.1 After the first TTY session: `harness/bare-metal` records frames rendered,
flips queued, missed flips and the age distribution for 25 s of an idle desktop; idle frames
after the first = 0 and missed flips = 0. P7.2 A pointer sweep for 5 s: 0 frames rendered
(cursor plane), log line per cursor-plane move disabled. P7.3 A typing client at 60 Hz: missed
flips = 0 over 600 frames; GPU p95 <= 2.5 ms. P7.4 The same measurements on an integrated GPU,
recorded in `docs/bare-metal.md` under a dated heading, with the ladder level the gate chose.

---

## 8. How performance is kept

### 8.1 What is measured

| where | what | by |
|---|---|---|
| compositor, per frame | GPU time per phase (timer queries), CPU prologue/submit/wait, round trips, blur passes run vs cached, surfaces recomputed, bytes uploaded, output damage area, frames skipped for no demand, configures sent, frame callbacks sent, ladder level, missed flips (DRM) | `--frame-stats PATH` (CSV, one row per frame, unbuffered), `stats [N]` over test-input |
| compositor, per run | the distributions: p50, p95, p99, max, mean, sd per column | `stats.rs::Dist`, unchanged |
| present_wl, per window | presents (one-shot vs pooled), bytes copied, damage rects, frame callbacks waited for, callback-to-commit time | counters read by `AURORA_APPSERVER_TRACE` and by the toolkit's report |
| toolkit, per window | draw time distribution and `over_budget` count, damage fraction, full vs partial frames, nodes visited per frame, HarfBuzz calls, cache hits and misses per cache, `motion_degraded` | `Window::timing()`, `damage_stats()`, `ATLAS_REPORT=1` |
| host | load average and GPU utilisation at the start and end of a timing run | the harness, so a contaminated run is labelled as one |

### 8.2 Where the numbers live

- `harness/perf`: one script, scenarios by name (`idle`, `typing`, `hover-sweep`, `move`,
  `resize-drag`, `reveal`, `four-windows`), each a headless stack at 1856x1160 @ 1.45 with a
  scripted `--test-input` session, writing `harness/perf-out/<scenario>.csv` and a one-line
  summary. It refuses to record a **timing** result when the host's load average exceeds the
  core count / 4 or GPU utilisation exceeds 20% at the start, and says so; counting results are
  recorded regardless.
- `compositor/tests/perf-baseline.toml`: committed, per scenario, the counting bounds (exact)
  and the timing bounds (p95 and sd ceilings) with the date and the machine they were taken on.
  A change to a bound is a reviewed diff with the reason in the commit.

### 8.3 What fails a build

`make test` runs the **counting** checks; they are deterministic and run on a loaded machine:
P1.1, P1.3, P2.2, P2.3, P3.1, P3.2, P4.1, P4.3, P5.2, P5.3, P6.1, P6.4. `make perf` runs the
**timing** checks and fails on a p95 more than 20% over the baseline or an sd over its bound:
P1.4, P3.4, P5.4, P6.3, P7.3 when hardware exists. `make demos` keeps the behaviour checks it
has (`buffer-pool`, `resize-storm`) with the tightened bounds.

### 8.4 What a regression looks like, and what a check that cannot fail looks like

A regression is any counting check that fails, or a timing p95 more than 20% over its baseline
on the machine class the baseline names, or a ladder level above 0 in a scenario whose baseline
is 0. The report names the counter or column, the scenario, the baseline and the observed
value, and the commit range.

Every check in this document is introduced with the one-line change that makes it fail, run
once and named in the check's comment (`fixtures-that-cannot-fail`): P1.1 fails when the
scheduler is bypassed; P1.3 when `[glass] submit = "per-pass"`; P3.1 when the blur cache's
generation check is commented out; P4.1 when `PresentWindow` calls `au_wl_win_present`; P5.2 when
the run cache is sized 0; P6.1 when the grab throttle is disabled. A check added without its
failing counterpart is not merged.

**Checks.** P8.1 `make test` on this loaded machine passes the counting checks and takes less
than 60 s more than today. P8.2 `make perf` on this machine refuses to record timings and says
why, with the load figures. P8.3 Each perf check's comment names its revert; a reviewer picks
two at random and runs them. P8.4 The baseline file's bounds are within 20% of the measured
values on a quiet machine, not loose.

---

## 9. What this changes in the Atlas spec's motion budget

`docs/atlas-design.md` section 7 stays correct in its primitive (7.1) and its tokens (7.3).
Three statements in 7.2 are amended by this document:

1. **The gate.** 7.2 says `motion_allowed()` is false "when the p95 of the last 30 frames' draw
   time exceeds 6 ms"; `ui.cpp` implements "3 of the last 8 frames over 8 ms". Neither is
   written for 120 Hz. This document's rule (3.4), for the toolkit and the compositor alike:
   **degrade when 3 of the last 8 frames exceeded 75% of the frame period; promote after 60
   under 50%.** At 60 Hz that is 12.5 ms, looser than either existing wording for a window
   thread whose own budget is half the period; so the toolkit's gate uses its own budget as the
   period: **3 of 8 over 75% of half the period** (6.25 ms at 60 Hz, 3.1 ms at 120 Hz), which is
   the spec's 6 ms to within rounding and a correct number at 120 Hz. The spec's text and the
   code converge on this one wording.
2. **The per-tick cost rule.** "One tick must stay under 2 ms of draw" becomes "under 25% of the
   toolkit's budget" (2 ms at 60 Hz, 1 ms at 120 Hz). The reveal's 1.5-2 ms tick is at the
   line at 120 Hz; the icon-grid caching of 5.2 is what keeps it under.
3. **The tick source.** 7.1's animations tick on the toolkit's 16 ms sleep; under 6.1 they tick
   on the frame callback. Durations are in milliseconds and `Ease::tick` reads the clock, so
   nothing in the token table changes; the frame counts in 7.2 ("10 frames", "6 frames") are
   60 Hz figures and double at 120 Hz at the same cost per tick. C7.1's "about 10 frames at
   60 Hz" is correct as written and should say "at 60 Hz".

Nothing in 7.3's day-one spend is invalidated. The hover-fade argument (a pointer sweep starts
40 fades a second and the window never sleeps) is strengthened by 6.1: with frame callbacks a
window that is animating is one that draws every displayed frame, so a hover fade keeps the
compositor rendering as well.

---

## 10. The sequence

Continuing decision 0010's numbering, so the two lists read as one. Costs are engineering
estimates; gains are at the user's geometry on the quiet machine, from section 0's numbers.

| # | change | buys | confidence | cost |
|---|---|---|---|---|
| 17 | counters in stats and present_wl; `harness/perf`; the baseline file | nothing yet; every later row's check | high | 2 days |
| 18 | batched chain submission (3.1) | **about 1.3 ms of the 3.5 ms frame**, and the per-window marginal cost falls from ~0.65 ms to ~0.1 ms | high that it is the dominant term; medium on the remainder | 2-3 days, with the per-pass path kept |
| 19 | the frame scheduler (1.1) and demand from commits, chrome and animations | **all 3.5 ms of every idle frame**, conditional on the background answer; at 60 Hz, 210 ms of GPU per second | high | 3 days |
| 20 | configure throttle in the grabs (6.1 rule 1) | 60 configures per drag -> about 10; the app_server's remaining 2 reallocations per burst -> 1 per frame; every future client protected | high | half a day |
| 21 | app_server pooled present with its dirty region (4.2) | **4.5-5.9 ms per present** on the Interface Kit path, and presents proportional to damage | high (measured) | 1 day |
| 22 | persistent scene + per-surface blur cache + dirty-region recomposition (2.3, 3.2) | a typing frame's blur from 2.45 ms to 0; a move's from 2.45 to the surfaces in reach; needs 18 first or the cached frames still pay the round trips | medium: the geometry of reach is tested, the cache invalidation is new | 1 week |
| 23 | frame callbacks in present_wl and the toolkit loop (6.1 rule 2) | one client frame per displayed frame; animations correct at 120 Hz | high | 2 days |
| 24 | GPU timer queries; winit and DRM fences instead of `glFinish` (1.5, 7.2) | stats usable in production; CPU no longer serialised with GPU, up to the GPU time per frame of CPU headroom | medium (driver-dependent) | 2 days |
| 25 | the ladder (3.4) | the worst case bounded on any GPU; decision 0023's promotion made real | high | 2 days |
| 26 | shaped-run and coverage caches, container culling, row fills (5.2, 5.3) | HarfBuzz off the hot path; draw O(damage) | high | 3 days |
| 27 | scissored chains and quarter start above 2560 px (3.3) | 2-4x of remaining blur work at 4K; the integrated-GPU budget | medium | 2 days |
| 28 | dmabuf import; capture from the scene texture (4.1) | zero-copy GPU clients and capture | medium | 2 days |
| 29 | the background's `animate` policy (3.5) | makes 19's saving real on an idle desktop | n/a: the user's decision | half a day |
| 30 | DRM: age, damage to `queue_buffer`, deadline scheduling, in-fences, cursor demand (7) | the real thing | unknown until the first TTY session | after that session |

Items 17-21 are the first week and deliver most of the milliseconds; 22 is the structural one;
23-26 make the client side match; 27-30 are the long goal.

---

## 11. Where the architecture is in the way, not merely slow

For the reviewer who wants the list without the argument:

1. **The effects pipeline is immediate-mode from the clear colour up.** It never reads the damage
   tracker it is handed, resets its dirty set every frame, and keeps no result past the frame.
   No amount of tuning makes a frame cheaper than "everything"; only a persistent scene with
   generations does (section 2.3, item 22).
2. **The unit of GPU work is `bind`/`render`/`finish`/`wait`.** Smithay's `Frame` targets one
   framebuffer, so a 20-pass chain is 20 frames. The chain has to step outside that API
   (`with_context`) to become one submission (3.1, item 18).
3. **Frame production has no scheduler.** Three backends each render on their own clock
   unconditionally; demand does not exist as a concept (1.1, item 19).
4. **Client pacing is not a protocol.** No frame callbacks requested, no configure throttling,
   a sleep-based toolkit loop, and an app_server whose present is one-shot by construction with a
   region it never passes on (sections 4 and 6, items 20, 21, 23).
5. **The instrumentation blocks the CPU to read the GPU.** It cannot stay on, so the numbers that
   justify every row above are taken in a mode the production path never runs in (item 24).
6. **The animated background makes idle impossible by design** (3.5, item 29).

The buffer-age `0` in `render.rs` is not on this list: it is a one-line defect in a path the
effects pipeline does not use, worth fixing (2.3) and not where the time goes (0.1).

---

## Appendix A. Measurement conditions for section 0.2

Headless, `--headless=1856x1160 --scale 1.45 --frame-stats --test-input`, a private theme
directory copied from `contracts/theme` with one `sed` per variant, one client started with
`WAYLAND_DISPLAY` pointing at the run's own socket in a `mktemp -d /tmp/aufp-XXXXXX` runtime dir,
7 s settle, then `stats 240` over the test-input socket; compositor and client killed by
recorded pid. Device: `GL Renderer: NVIDIA GeForce RTX 4090/PCIe/SSE2`, driver 580.196.07,
`PLATFORM_DEVICE_EXT` on `/dev/dri/renderD128`. Host at the time: load average 24.9 / 23.4 /
22.2 on 32 cores; `rustc` at 866% CPU; `nvidia-smi` GPU utilisation 92-96%, with
`kwin_wayland`, two browsers and eleven other `aurora-compositor` processes resident. The stats
flag blocks the CPU at each phase end, so each phase figure includes a scheduling wait under
this load, which is why per-pass cost came out at 1.2 ms against the 65 us `compositor/README.md`
measured quiet. The ratios in 0.2 are the result; the absolute values are not to be quoted.

The client micro-benchmark (0.3) is `cc -O2` C on the same host, 40 iterations per row, results
consumed through a volatile sink so no copy is elided; the one-shot row's cost is page-fault
bound and therefore also inflated by the load, by less than the GPU figures.
