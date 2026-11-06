# 0003. Compositor protocol

- **Status:** proposed
- **Date:** 2026-11-06

## Context

This record covers how pixels and layers reach the screen. It is contract 4 in the plan. The compositor is built in Phase 4. The trusted UI path is wired in Phase 4 and used in Phase 10.

## Decision

**Now (Phase 4).** Standard Wayland, plus:

- XWayland, running inside the app's own domain.
- layer-shell for shell pieces. The dock, launcher and notification area are separate layer-shell clients (Phase 8).
- Portals for files and screen sharing.

**Client identity.** Every Wayland client is bound to its domain through socket peer credentials.

**Trusted UI path (reserved now).** A surface class that only the compositor can draw. It is used for approvals, permission sheets and secure attention. It is never themable and never reachable by clients.

**Dependency on Phase 2.** The trusted path is only worth drawing if the compositor can say truthfully which domain asked. That identity is Phase 2's attested domain id, read from socket peer credentials. For this reason the compositor cannot be built before domains exist.

**Later.** A native layer-tree protocol. Native apps send layers and animations, and the compositor renders them at full frame rate, as in Core Animation. This record sets the direction only. It belongs to the Interface Kit and layer protocol phase.

**Window state (decided).**

- The compositor owns live window state. The bus log owns settled state.
- A drag publishes nothing until it ends. Then it publishes one durable `window.moved`.
- Crash recovery restores the last settled layout and loses an in-flight drag.
- Events published: `window.opened`, `window.focused`, `window.closed`, `window.moved`.
- Requests served: `focus`, `tile`, `close`, `layout.snapshot`, `layout.restore`.
- Frame times go to an ephemeral topic.

## Open questions

1. **Renderer.** Smithay's GLES renderer, with Vulkan only at a real limit, is the plan's direction. Phase 1's notes on the host compositor feed this choice. Phase 4.
2. **Trusted surface mechanics.** Partly answered, see "Amendment: the trusted path as built" below. Open: secure attention, a user-held secret, pointer interaction, permission sheets, and everything under "What it does not guarantee".
3. **Display-proxy split.** Whether and how the split that holds client connections changes the connection model. Phase 8.
4. **Native layer protocol.** Its wire format and how it coexists with Wayland. Interface Kit phase.

## Consequences

- Clients cannot draw trusted surfaces. Themes cannot reach the trusted path (Phase 8).
- The compositor is blocked on Phase 2's domain identity.
- Any change to the trusted path needs a written threat note.
- Because the log holds only settled state, crash recovery returns windows to their last settled positions. An in-flight drag is lost by design.

## Amendment: the trusted path as built (Phase 4, approvals only)

The reservation above is now implemented for one use, approval prompts. The compositor is `compositor/`; details and tests are in its README, "Trusted path".

**Mechanics.** The overlay is not a surface class and not a protocol object: no client can create, target or name it. It is compositor state (`Trusted::queue`) rasterised by the compositor's own code and drawn after every client surface. A client "learns the answer" only in the sense that nothing arrives: the answer goes to a separate privileged Unix socket (not the Wayland socket) and only to the connection that asked. Requests are refused by `SO_PEERCRED` uid allowlist plus a cgroup check that excludes domain cgroups.

**Threat note.**
- *Capture as a read channel:* closed for `ext-image-copy-capture-v1` by ordering: captures are served from the framebuffer before the overlay is drawn. Tested by pixels: the compositor's own frame has the overlay, a capture taken meanwhile is byte-identical to one taken before it. Any future capture or screencast path must preserve that ordering.
- *Input leakage:* while an overlay is up the focused client is unfocused (it gets `leave`), keys go to the overlay only, pointer events are dropped, new windows are not focused, releases of keys pressed during the overlay are swallowed. Tested with a real client. Not hidden: the client can tell a prompt exists.
- *Request channel:* open only to allowlisted peers. It does not distinguish the controller from another same-uid process outside a domain, so such a process can raise a misleading prompt or spam the queue; it cannot answer one. The controller now checks the compositor's uid and that it is outside a domain cgroup (see "Amendment: domain verification"); it still cannot tell the real compositor from a same-uid host process that bound the socket first.
- *Not met:* see "Amendment: domain verification" for what remains of the domain requirement. No secure attention key or user-held secret, so a look-alike client window is not distinguishable by anything the user can verify. Key events are trusted as the backend reports them. Only the headless backend was exercised; the winit path was not run.
- *Affordances:* `--test-input` (key injection) exists only with `--headless`, which opens no input device; it must never be combined with a real-input backend. `--dump-overlay` writes the overlay to a file and is a deliberate read channel for whoever started the compositor with it.

## Amendment: domain verification and compositor authentication

0003 requires an approval that says truthfully **which domain** asked. That has two halves, and only one is met.

**Met: the named domain is real.** The compositor no longer displays a domain name on the caller's say-so alone. It takes the domain's bus identity from the request, asks busd itself (`bus.inspect@v1` on the session socket, `compositor/src/trusted/verify.rs`) and draws the result: `VERIFIED` when busd has that identity registered with a cgroup it attests, otherwise (busd does not vouch, or cannot be asked) a banner `UNVERIFIED DOMAIN` with the name shown as a claim. An unverified claim is displayed, never refused, since refusing would let an attacker suppress approvals. The result goes back in the reply (`domain_verified: true|false|unknown`) and into provenance. Inventing a domain name or identity busd has never registered is now visible to the user.

**Not met: the named domain is the one that asked, or that this invoke runs in.** Even `VERIFIED` proves only that a domain with that identity exists and is attested. The association "this request, this invoke, this domain" is made by the controller and passed to the compositor as text. The compositor cannot check it, and any same-uid process allowed on the trusted socket can name a real attested domain and be shown as verified. So 0003's requirement, read as "the compositor says truthfully which domain asked", is **not fully met**; what is met is "the compositor does not display a domain that does not exist as if it did". Closing the rest needs the compositor to learn the requester's domain from the connection itself (a request made from inside a domain, over a socket whose peer cgroup the compositor reads, which today the compositor refuses on purpose) or a controller credential the compositor can verify. Neither was built. Also: a domain with no connection open still verifies (registration plus cgroup is what busd attests against), and busd's own caveats apply (cgroup-only attestation unless namespaces are registered, which the controller does not request).

**Controller authenticates the compositor, partly.** After connecting, before sending a byte, the controller reads `SO_PEERCRED` and `/proc/<pid>/cgroup` of the peer and refuses it unless its uid is the expected one (`--compositor-uid`, default the controller's own) and it is outside every `aurora-domains` cgroup. Refusal logs loudly and falls back to the bus route. This stops another user's process and a domain-confined one from answering `allow`. It does not stop a same-uid host process that bound the socket path first. Exe and comm are logged, not trusted.

**What had to change outside the compositor.** The controller registered domains with busd without a cgroup, so busd could not attest any of them and none could verify. It now passes `domain cgroup <name>`. `bus/` was not touched; there is no narrower read verb, so the compositor reads the whole inspect listing.

Tests: `compositor/tests/verify.rs`, `compositor/src/trusted/verify.rs`, `controller/internal/invoke/compositor_peer_test.go`. Demonstration with a real busd, controllerd, headless compositor and bwrap domains: `controller/demo-verified.sh`.

## Amendment: the theme parameter file (Phase 1, glass and switcher)

The parameter set Phase 8's theme engine will consume is checked in at `contracts/theme/`: `aurora-glass.toml` plus four GLSL ES 1.00 fragment shaders in `shaders/` (`down`, `up`, `glass`, `card`). The compositor (`compositor/src/fx/`) reads it, polls it every 250 ms and hot-reloads it; a file that does not parse or a shader that does not compile is rejected and the last good one stays.

**Shape (version 1).** `[theme] name, version`; `[glass] enabled, blur_radius` (dual-Kawase tap-offset multiplier), `blur_passes` (1 to 6), `corner_radius` (px); `[glass.tint] oklab = [L, a, b], strength` (0 to 1); `[glass.edge] oklab, width` (px), `strength`; `[switcher] layout = "grid" | "flip", backdrop_dim, flip_angle_deg, accent` (Oklab). Colours are Oklab tokens, converted to gamma-encoded sRGB when loaded; blending itself is not done in Oklab or in linear light. Unknown keys are ignored and missing keys take the defaults written in the file, so a later version can add keys without breaking v1 readers. Values are clamped on load.

**Not in the file yet:** spacing and font tokens, window frames, cursors, sounds, the manifest and the topic allowlist (Phase 8), per-window glass opt-out, switcher animation timing. Shader uniform names are part of the contract between a shader and the compositor (listed in `fx/mod.rs`, `uniform_names`); renaming one is a breaking change.
