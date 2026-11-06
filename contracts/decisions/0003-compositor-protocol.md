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
2. **Trusted surface mechanics.** How the compositor draws a trusted surface and how a client learns the answer. The plan does not specify this. Phase 4, with the threat note the plan requires for trusted-path changes.
3. **Display-proxy split.** Whether and how the split that holds client connections changes the connection model. Phase 8.
4. **Native layer protocol.** Its wire format and how it coexists with Wayland. Interface Kit phase.

## Consequences

- Clients cannot draw trusted surfaces. Themes cannot reach the trusted path (Phase 8).
- The compositor is blocked on Phase 2's domain identity.
- Any change to the trusted path needs a written threat note.
- Because the log holds only settled state, crash recovery returns windows to their last settled positions. An in-flight drag is lost by design.
