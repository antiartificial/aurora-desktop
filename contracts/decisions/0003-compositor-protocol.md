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
- *Request channel:* open only to allowlisted peers. It does not distinguish the controller from another same-uid process outside a domain, so such a process can raise a misleading prompt or spam the queue; it cannot answer one. The controller does not authenticate the compositor.
- *Not met:* the compositor cannot say truthfully which domain asked (the Phase 2 dependency above): it displays the allowlisted caller's text. No secure attention key or user-held secret, so a look-alike client window is not distinguishable by anything the user can verify. Key events are trusted as the backend reports them. Only the headless backend was exercised; the winit path was not run.
- *Affordances:* `--test-input` (key injection) exists only with `--headless`, which opens no input device; it must never be combined with a real-input backend. `--dump-overlay` writes the overlay to a file and is a deliberate read channel for whoever started the compositor with it.
