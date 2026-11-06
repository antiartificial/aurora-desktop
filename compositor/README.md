# aurora-compositor

The Wayland compositor, Phase 4 first step. A small Smithay compositor whose
point is **frame capture that returns real pixels** over
`ext-image-copy-capture-v1`. It closes the gap that blocked recorded demo
evidence: no compositor on the dev host could be captured (see
`harness/capture/README.md`).

It is not Compositor v0. It is smallvil plus a working capture path.

## What exists

- Core: `wl_compositor`, `wl_subcompositor`, `wl_shm`, `xdg_shell` (move and
  resize grabs, popups), `wl_seat` with keyboard and pointer, `wl_output`,
  `zxdg_output_manager_v1`, `wl_data_device_manager`. Derived from Smithay's
  `smallvil` at the pinned revision.
- `ext_output_image_capture_source_manager_v1`: a client can pick the output
  as a capture source.
- `ext_image_copy_capture_manager_v1`, implemented (`src/capture.rs`):
  - the `Session` is kept alive; it is not dropped after the constraints;
  - constraints: the output's current mode size, shm `Xrgb8888` and `Argb8888`;
    they are updated on resize, and a frame whose session size no longer
    matches is failed with `buffer_constraints`;
  - `frame` queues the request; on the next rendered frame the framebuffer is
    read back (`ExportMem::copy_framebuffer`, GL read-back), written into the
    client's shm buffer (stride and offset honoured), and only then is
    `ready` sent. Any failure sends `failed`, never a blank success;
  - reported transform is `Normal`; damage is the full buffer.
- Two backends, both through the same render and capture path (`src/render.rs`):
  - winit (default): a window on the host session.
  - `--headless[=WxH]` (default 1280x800): no host window. Surfaceless EGL, an
    offscreen texture, a 16 ms timer for frame callbacks.
- Smithay is a git dependency pinned to rev
  `65aa86fa5236e41775c313408102a5d5c2d1e2ca` in `Cargo.toml`, recorded in
  `Cargo.lock`. That is the same commit `harness/nested-up` checks out for
  smallvil and anvil.

## Run

    cargo build --release                      # in compositor/
    harness/nested-up --compositor aurora      # winit window, plus a terminal if found
    harness/nested-up --compositor aurora --headless=1280x800
    compositor/target/release/aurora-compositor [--headless[=WxH]] [-c COMMAND]

The compositor prints `aurora-compositor: socket wayland-N`. Capture with:

    harness/capture/target/release/aurora-capture --display wayland-N -o shot.png

Run a terminal inside it with `WAYLAND_DISPLAY=wayland-N ghostty` (unset
`DISPLAY` so it uses Wayland).

## Glass and the switcher (Phase 1's look, ported)

Code: `src/fx/`. Parameters and shaders: `contracts/theme/` (`aurora-glass.toml`, `shaders/{down,up,glass,card}.frag`). Evidence: `demos/phase-1/`.

- **Glass.** With `glass.enabled`, the scene is composed in an offscreen texture, window by window from the back. Before each window is drawn, everything below it is blurred and drawn under the window's rectangle: a **dual-Kawase blur** (Bjorge's kernel: 5-tap downsample, centre x4 plus four diagonals, /8; 8-tap upsample, edge midpoints x1 plus diagonals x2, /12), `blur_passes` downsamples and as many upsamples, `blur_radius` scaling the tap offset. Then the glass shader tints (Oklab token converted to sRGB on load), clips to a rounded rectangle and adds a highlight along the inside of the edge (full at the top of the window, 45% at the bottom). The window's own pixels go on top, so a translucent client shows the blurred desktop through itself. The finished scene is blitted into the output framebuffer, **before** captures are served and the trusted overlay is drawn: ordering is unchanged and a test re-checks overlay exclusion on a glass scene.
- **What it approximates.** Blending and blur run on sRGB-encoded values (not linear light; Oklab is used only for the theme's colour tokens). The whole scene below a window is blurred, not just the part under it (cost grows with window count: 2 x passes full-chain draws per window). Every window gets glass under its geometry; there is no per-window opt-out, and glass is only visible under a translucent client. Textures are 8-bit, so the lowest levels band slightly. Each glass window re-blurs the scene, so a glass window above another glass window blurs the lower one's glass (correct, but not shared work).
- **Theme file and hot reload.** `--theme DIR` (default: `../contracts/theme` of this source tree, else built-in copies). The files are polled every 250 ms; on a change the theme is re-parsed and the shaders recompiled. A theme that does not parse, or a shader that fails to compile, is logged (`theme: ... keeping last good`) and the previous one stays in force; a shader that has never compiled falls back to the built-in copy. Not done: a frame-time watchdog (a shader that compiles but is too slow is not caught), and a shader that compiles but fails at draw time only drops that frame to the flat renderer.
- **Switcher.** Alt+Tab opens it (Tab / Shift+Tab / arrows move the selection, Return or releasing Alt commits, Escape cancels). Keys are filtered in `keyboard_input` (`src/fx/switcher.rs`): while open, no key reaches the client; Alt's own press and release are forwarded (so the client never sees a stuck Alt) and the release commits. Modifiers do work headless: `--test-input` takes `down alt`, `key Tab`, `up alt`. Each card is the window's live texture, re-rendered every frame into its own texture, drawn by `card.frag`, which inverts a perspective projection per pixel, so one shader draws both layouts: `grid` (flat cards, scaled to fit) and `flip` (a receding, rotated stack, `flip_angle_deg`). The layout is a theme key, so it hot-reloads while the switcher is open. The backdrop is the blurred, dimmed desktop. The selected card has an accent border.
- **Switcher limits.** A card is the window's surfaces only: a translucent window's card shows no glass behind it. No titles or icons on cards (no font path for client text, deliberately). No animation between layouts. Clicking cards does nothing (pointer events are not routed to the switcher). Popups are not on cards. A raised overlay closes the switcher.
- **Test affordance added:** `--test-input` also takes `move N X Y` (place the Nth window in stacking order), to build overlapping scenes.
- **Tests** (`tests/glass.rs`, run with the others): glass on/off frames differ; the blur turns a black/white step into a gradient with 27 intermediate pixels in an 80-pixel row, largest neighbour jump 8 (the glass-off control is a one-pixel step); a hot reload changes the frame, also for the pass count; a deliberately broken shader and a broken theme file are rejected, the frame is identical to the last good one and the compositor keeps serving captures; a valid edit afterwards is picked up; the switcher shows N cards for N windows (2, 3, 4) and the flip layout is perspective-slanted; the trusted overlay is still absent from captures of a glass scene. `tests/evidence.rs` regenerates `demos/phase-1/` when `AURORA_EVIDENCE_DIR` is set.

## Trusted path

Decision 0003 reserved "a surface class only the compositor can draw". This is its first implementation: approval prompts for destructive and external verbs, requested by the controller (`controller/README.md`, "Approval"). Code: `src/trusted/`.

**Design.**
- The overlay is not a surface. There is no Wayland object for it: `ext_image_copy_capture` and every other global are unchanged, and `tests/trusted.rs` lists the advertised globals and checks none concerns it. It is a record in `Trusted::queue`, rasterised by the compositor itself (`draw.rs`: a built-in 8x16 bitmap font generated by `tools/gen_font.py` from a Linux console font, a CPU canvas, one texture and a dimming scrim), and drawn **after** every client surface in `render_and_capture`.
- **Capture exclusion is by order, not by filter.** `render_and_capture` renders the space, serves queued capture frames from the framebuffer (`serve_pending`), and only then draws the overlay. A capture therefore reads a framebuffer that never contained it. Any new capture or screencast path must read at that same point; one that reads after `draw_overlay` would leak it.
- **Input.** While any overlay is queued: keyboard focus is taken from the client (it receives `wl_keyboard.leave`, so it sees no further keys and no stuck keys), every key goes to `Aurora::keyboard_input` and is intercepted for the overlay (`a` allows; `d` or Escape denies; anything else is swallowed), pointer events are dropped, and a window that maps meanwhile is not given focus. Releases of keys pressed during the overlay are swallowed after it goes away too. When the last overlay is decided or withdrawn, the previous focus is restored if that surface is alive.
- Several requests queue; the first is shown, the count is displayed. An undecided request is denied at its deadline (`timeout_ms`, default 60 s, clamped 1 s to 10 min). If the requester hangs up, its overlay is withdrawn.
- Displayed text is untrusted (app names, paths and args come from apps). It is reduced to printable ASCII (everything else becomes `?`), each field is limited to 2048 characters, grants beyond four are replaced by a visible "+N more" line, paths keep their tail, args wrap to three lines. Text cannot add layout, but it can still say misleading words inside its own field.

**The request channel.** A Unix socket, not the Wayland socket: `--trusted-socket PATH` (default `$XDG_RUNTIME_DIR/aurora-trusted.sock`), mode 0600. One JSON line in, one out:

    {"v":1,"op":"approve","request_id":"..","app":"..","domain":"<name>","domain_identity":"domain:<name>:<hex>","verb":"..","effect":"destructive",
     "grants":[{"path":"..","mode":"rw","source":"gesture"}],"args":{...},"timeout_ms":60000}
    {"v":1,"request_id":"..","decision":"allow"|"deny","key":"a","reason":"key"|"timeout",
     "domain_verified":"true"|"false"|"unknown","domain_verify_detail":"..","domain_source":"bus"|"claim","domain_identity":"..",
     "domain_mismatch":"true"|"false","sig":"<hex HMAC-SHA256>"}   or   {"v":1,"error":".."}

The decision comes only from the keyboard, never from the socket: nothing on the socket can answer a prompt, including the requester. The reply goes only to the connection that asked.

**What the socket authenticates.** On accept, before reading any byte, `SO_PEERCRED` gives the peer's uid and pid, and `/proc/<pid>/cgroup` its cgroup. A peer is refused unless its euid is allowlisted (default: the compositor's own; `--trusted-uid N`, repeatable, replaces it) **and** no component of its cgroup path is `aurora-domains` (the parent of every domain cgroup the adapter creates; the same `0::` cgroup-v2 reading the bus uses for attestation) and it is not at or below a `--deny-cgroup PATH`. Unreadable or non-v2 cgroup information refuses (fail closed). Tests: another uid is refused; a peer inside a denied cgroup (this test process's own, passed as `--deny-cgroup`) is refused; neither produces an overlay.

**Signed replies** (`src/trusted/sign.rs`). Every decision reply carries `sig`, an HMAC-SHA256 over the request id, decision, key pressed, reason, `domain_verified`, `domain_verify_detail`, `domain_source`, `domain_identity` and `domain_mismatch` (each field length-prefixed, after the label `aurora-approval-reply-v1`). The key is a 32-byte file shared with the controller: `$AURORA_APPROVAL_KEY`, else `$XDG_RUNTIME_DIR/aurora/approval.key`, created atomically with mode 0600 by whichever of the compositor and the controller starts first, and refused on load unless it is a regular file owned by this user with mode 0600 and 32 bytes (the same scheme as the powerbox ticket, `controller/ticket/key.go`, but a different file). If the key cannot be loaded the compositor says so on start and replies unsigned, which the controller refuses. The controller verifies `sig` and that the signed request id is the one it sent (`controller/README.md`, "Signed replies"). `{"error":..}` replies are not signed: they never allow. SHA-256 and HMAC are implemented in `sign.rs` because the lockfile has no hash crate and nothing could be installed; they are checked against the published SHA-256 and RFC 4231 HMAC vectors.
- **What this buys:** a reply the controller accepts was produced by something that held the key file. A process that binds the socket path first and never read the key cannot produce one; neither can a replay of an earlier reply for another request, nor an edit of any signed field.
- **What it does not buy:** any process that can READ the key file (every same-uid process, by default) can sign. This is key possession, as with the powerbox, not kernel authentication of the compositor. The signature does not cover the request text (app, verb, grants, args), only the decision and the domain shown.
- Tests (`tests/signed.rs`): the reply is signed and the MAC recomputed independently from the key file matches; a timeout deny is signed too; changing any one signed field changes the MAC; a loose-mode key file is refused at start and the reply is then unsigned.

**Domain verification** (`src/trusted/verify.rs`). The request carries the domain's display name and the bus identity the controller claims for it. Before drawing, the compositor asks busd itself: it connects to the bus session socket (`--bus-socket PATH`, default `$AURORA_BUS_SOCK`), sends `bus.inspect@v1` and looks the identity up in `domains`. The check runs on a thread with a 1.5 s deadline, so a slow or hung busd never stalls the compositor loop, and the prompt is drawn when it returns, never refused.
- `domain_verified:"true"` (drawn as a cyan `VERIFIED` line): busd has a domain registered under exactly that identity, the identity's name part equals the displayed name, and busd holds a cgroup for it (the thing it attests connections against; without one the domain is registered but unattestable, which is `false`). The live connection count is shown for information and is **not** required: a domain with no connection open at that moment still verifies.
- `"false"` (yellow banner `UNVERIFIED DOMAIN`, the name drawn as `CLAIMED "<name>" (NOT VERIFIED)`, busd's reason below it): busd answered and does not vouch for the identity (unknown, no cgroup, name and identity disagree, no identity sent).
- `"unknown"` (same banner, "busd could not be asked"): no bus socket configured, busd unreachable, a bad reply, or the deadline passed. Never `true`.
- **The ceiling.** `true` proves exactly this: a domain with that identity exists in busd and busd attests it. It does **not** prove the request came from that domain, nor that this invoke will run in it. Only the controller knows which domain an invoke runs in, and the compositor takes the controller's word for that association; the controller is just an allowlisted same-uid peer (below). A same-uid process can name a real, attested domain and get `VERIFIED`. What the check does close: inventing a domain, or a name and identity busd has never registered, now shows as an unverified claim.
- Workaround: busd has no narrower read verb, so the compositor fetches the whole `bus.inspect@v1` listing (clients, services, handle ids, never tokens) and reads only `domains` and `clients[].domain`. Any session-socket peer can do the same, so this widens nothing.
**Bus-attested domain (`src/trusted/attest.rs`, decision 0003).** The controller runs `aurora-preflight` inside the app's domain before the prompt. The helper publishes `approval.preflight` (request id, app, verb, effect, grants) on the domain's own bus socket, so busd stamps `from` with the identity it derives from the accepting socket and the connector's cgroup. After the request arrives the compositor reads that event from the bus log (`bus.log@v1`, topic `approval.preflight`, last 2 min) by request id and displays the **bus's** domain, not the one in the request.
- An event counts only if: `from` is `domain:<name>:<id>` (session-socket senders are ignored), request id, app, verb, effect and grants equal the prompt request's (a recorded event cannot be replayed under another request), its timestamp is within 60 s, busd still has the domain registered with a cgroup, and the request id has not attested a prompt before (remembered, 4096 ids). Events for one id from two different domains are a conflict and attest nothing.
- Attested: the domain line is `BUS-ATTESTED` (green). If the controller claimed a different domain, the overlay gets a red `DOMAIN MISMATCH` banner, shows the bus's domain, and lists the controller's claim as `REJECTED`. The reply adds `domain_source:"bus"`, `domain_identity` (what was shown) and `domain_mismatch`.
- No usable event (helper missing, failed, slow, or a forger who never ran code in a domain): the old display, now with a purple `CONTROLLER'S CLAIM` line, `domain_source:"claim"`, and the reason in `domain_verify_detail`. The prompt is always shown; absence of an event never suppresses it and never upgrades it.
- Tests: `tests/attest.rs` (fake busd with `bus.inspect@v1` and `bus.log@v1`; reads the compositor's own frame): attested identity; bus overrules a controller naming another domain and flags it; same-uid claim with no event; one-time use and conflicting senders. Unit tests for event selection in `attest.rs`. Real busd and a real in-domain helper: `controller/demo-attested.sh`.
- **Ceiling.** The bus attests *where the helper ran*. A same-uid host process can run the helper inside any domain it can `domain run` (demo step 3 does exactly that) and so get any domain attested, with a request id and content it chooses; the setns attack in `bus/README.md` (8g) does the same. The verb and grants text is still the controller's, and the controller still chooses which domain the helper and the payload run in. What changed: a process that cannot run code in the domain it names can no longer get it displayed as bus-attested.
- The reply carries `domain_verified` and `domain_verify_detail` whether the human allows or denies; the controller records them in provenance.
- Tests (`tests/verify.rs`, a fake busd speaking the real `bus.inspect@v1` wire shape; `src/trusted/verify.rs` unit tests for the decision): verified, unknown-to-busd, name/identity mismatch, unreachable bus, hung bus. Each reads the compositor's own dumped frame (banner pixels present or absent) and the reply. The real busd is exercised by `controller/demo-verified.sh`.

**What it does not authenticate.**
- Any same-uid process outside a domain cgroup passes. It cannot approve anything, but it can put up a prompt with any text it likes (a misleading verb or app name), spam the queue so real prompts wait or time out, and learn nothing from the reply to someone else's request. The controller is not distinguished from other same-uid processes: no executable or unit check. A distinct uid for the controller plus `--trusted-uid` would close that; not done.
- The compositor does not know what the verb really is, and it can tell which domain asked only when the request came with an `approval.preflight` event busd stamped from inside that domain (see "Bus-attested domain"); otherwise the domain is the allowlisted peer's claim. Verb, grants and args are always the peer's text.
- The controller checks the compositor's uid, that it is outside a domain cgroup, and now that the reply is signed with the approval key (`controller/README.md`, "Approval"). A same-uid host process that binds the socket path first still passes the uid and cgroup checks and still receives the request text, but cannot answer `allow` unless it can read the key file; if it can (any same-uid process can, by default), it can. The signature covers the decision and the domain shown, not the verb, grants or args that were displayed.
- The cgroup check relies on a domain's processes being unable to leave its cgroup, and on domains not holding the socket path. Neither was tested here; an app confined only by a mount namespace never sees `$XDG_RUNTIME_DIR`, which is the first line of defence.
- A cooperating client learns a prompt exists: it receives `wl_keyboard.leave` when the overlay rises and `enter` when it falls. Not hiding that is the price of not leaving stuck keys.
- Look-alikes are not prevented. A client can draw its own window that looks like this prompt. The real one differs only in that it is above everything, swallows keys and the client's focus is lost; the user has no secret or secure-attention key to tell them apart, and nothing in the compositor stops a client mimicking the layout.
- Keys are matched by keysym of the active layout (`a`, `d`, Escape). Nothing shows that a human pressed it: key events come from the backend. Clients have no virtual-keyboard or input-method protocol here (not advertised), but on a real session host-level injection (uinput, the host compositor under winit) is outside what this compositor sees.
- The winit backend draws the overlay through the same function with the output's transform (`Flipped180`), but that was **not run**: only `--headless` was verified. Under winit the host compositor can screenshot the window, overlay included.
- No dmabuf, cursor capture or toplevel capture exist, so there is nothing else to read the overlay today; that is a fact about the current feature set, not a guarantee.

**Test and demo affordances (loud on purpose).**
- `--test-input PATH` injects key events (`key a`, `down s`, `up s`) over a 0600 same-uid socket into the same `keyboard_input` function real keyboards use. It exists only with `--headless`; `main` refuses it otherwise. That is the whole protection: the headless backend opens no input device, so there is no real keyboard to impersonate. It must not be combined with a backend that has real input: any same-uid process could then press `a` for the user. The compositor prints a WARNING on start when it is on.
- `--dump-overlay PATH` makes the compositor write its own final frame, overlay included, to PATH as a PNG the first time each prompt is drawn. It is a deliberate read channel for the overlay, for anyone who can read PATH. It is how the tests show the overlay really was drawn. Do not use it for real. WARNING on start.
- `tests/trusted.rs` runs real headless compositors with a real Wayland client (`examples/testclient.rs`) and `harness/capture`'s `aurora-capture`:

        (cd harness/capture && cargo build --release)
        cd compositor && cargo build --release --examples && cargo test --release -- --nocapture

  It checks, on decoded pixels: a control capture contains the client window; the compositor's own frame shows 14k+ overlay-border pixels while a capture taken at the same time shows none and is byte-identical to a capture taken before the overlay; keys typed at an overlay never reach the focused client (its log shows `leave`, nothing, then `enter`), and a key typed after it does; `a`, `d`, Escape, timeout and withdrawal; refused peers (uid and cgroup); no overlay-related global; `--test-input` refused without `--headless`. `src/trusted/socket.rs` has unit tests for the allowlist and text cleaning.

## Known limits of the backends

- **winit blocks when the host does not present its window.** With the host
  screen locked, the second `eglSwapBuffers` never returned, KWin marked the
  window "Not Responding", and every client stalled. That is why `--headless`
  exists. The winit path was therefore **not verified to deliver a capture**.
  Its row order (the winit surface is rendered with `Flipped180`) is the
  untested part.
- Without a window, nothing draws. An empty compositor captures as one flat
  colour (the clear colour, 25,25,25). That is correct, not a failure.

## What from Phase 4 does NOT exist

- Glass and the switcher exist (see "Glass and the switcher"), but no window management beyond smallvil's move and resize grabs, and no animation: the switcher appears and changes between frames.
- No multi-monitor: one output.
- No HiDPI: scale is fixed at 1.
- No XWayland.
- No client-domain binding: clients are not tied to domains or capabilities.
- No secure attention key, no per-user secret on the prompt, no pointer interaction with it, attribution of a request to a domain only through the bus event (a same-uid process that can run code in any domain can still get that domain attested; otherwise it can only verify that the named domain exists and is attested; see "Trusted path" for what exists and what it does not guarantee).
- No dmabuf: no `linux-dmabuf`, so GL clients fall back to shm. Capture is
  shm only.
- No cursor capture (`paint_cursors`), no toplevel capture source, no
  rotated output.
- Capture is served on the next redraw, so a request waits up to one frame.
- Tests: the glass/switcher tests above and the trusted-path tests below; no test of the plain capture path beyond the manual runs in `demos/phase-0` and the control capture in `tests/trusted.rs`. The winit backend has no test at all.
