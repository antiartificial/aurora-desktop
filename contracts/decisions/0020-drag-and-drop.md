# 0020. Drag and drop between the host and Aurora's clients, and what a dropped file means inside a domain

- **Status:** proposed (host to guest and the interim file policy are built; the grant and guest to host are not)
- **Date:** 2026-11-06

## Context

Aurora is a nested compositor and a winit client of the host, so the host sees one surface. The clipboard bridge (`compositor/src/clipboard/`) already moves MIME-typed selections both ways. Drag and drop had no bridge: `wl_data_device` worked guest to guest only.

Two facts shaped the design, both checked by running code rather than recalled.

1. **winit can do it; Smithay's winit backend hides it.** winit 0.31 implements Wayland drag and drop (`WindowEvent::DragEntered/DragPosition/DragDropped/DragLeft`, `ActiveEventLoop::{fetch_data_transfer, set_valid_dnd_actions, start_drag}`). Smithay's `WinitEventLoopApp` owns the `ApplicationHandler`, forwards resize, focus, keyboard and pointer, and drops the rest; the `ActiveEventLoop` is private to it. Nothing reaches Aurora.
2. **The clipboard's second connection cannot carry a drag.** A drag is delivered to the client that owns the surface under the host pointer. A second connection owns no surface, so no host compositor would ever send it a drag.

And one Aurora-specific problem: a dragged file is `text/uri-list`, lines of `file:///home/arti/photo.png`. A path means nothing inside a domain, and handing one over leaks a host path while granting nothing.

## Decision

**Transport.** Bind a second `wl_data_device` on **winit's own connection**, on a separate event queue (`Backend::from_foreign_display` on the `wl_display` raw-window-handle gives out). The host sends `enter/motion/leave/drop` to every data device of the client on the seat, so ours hears what winit's hears, with the real surface and serials. No window, seat or focus is invented. Only a Wayland host works; an X11 host logs that the bridge is off.

**Guest side.** A host drag becomes a Smithay `DnDGrab` whose `Source` proxies the host's `wl_data_offer`. A synthetic left press starts it (the host holds the real one) and the host's `drop` is a synthetic release; Smithay's own enter/motion/leave/drop/finish and accept/action negotiation apply unchanged. MIME types and source actions are carried verbatim. Data is lazy and zero-copy: the guest's pipe goes straight to `wl_data_offer.receive` on the host; Aurora never reads it. A leave without a drop cancels the grab, destroys the offer and releases the synthetic press. A drag that arrives during another pointer grab is declined.

**Winit's own device rejects every drag on enter** (`set_actions(none)`, `accept(None)`) and Aurora cannot reach winit's `set_valid_dnd_actions`. Our device therefore repeats its accept and action on every `motion` so ours is the later request. Whether a given host compositor resolves two offers of one client that way is **not verified against any real host**; it is verified against Aurora-as-host only (`compositor/tests/dnd.rs`).

**Files into a domain (interim, built).** A client that is not provably outside a domain (the clipboard's fail-closed test: `ClientState::may_read_clipboard` false) is not offered path-bearing types: `text/uri-list` and its relatives (`x-special/gnome-copied-files`, `text/x-moz-url`, `application/vnd.portal.file*`), and `text/plain` and its X11 aliases when a file type is present (a file drag's text is the path). Other types still arrive. If nothing is left, the app sees no drag at all. The data request is checked again, so asking for a withheld type anyway gets EOF. Every refusal is logged with the types withheld. This never leaks a path and never pretends a drop worked, but it is a refusal, not a feature.

## The grant (not built)

Dropping a file on a domain app should be a grant, by the same rule as the powerbox (0007, 0011): the user's gesture on exactly this file is the authority, for this app, mode and verb.

What it needs, in order:

1. **The drop is the gesture, and Aurora can attribute it.** The drop happens inside the compositor as a host `drop` event on a surface that holds the pointer. That is stronger evidence than a terminal picker's answer: the compositor, not a client, saw it.
2. **Read the list at drop, from the host, once.** On `drop`, Aurora (not the target) reads `text/uri-list` from the host offer into a bounded buffer (a few KiB; refuse beyond that), parses `file://` URIs, resolves symlinks and refuses anything that is not a regular file or directory the user can read. This is the one place Aurora reads drag bytes; it is a path list, not content.
3. **Ask the controller for a ticket.** Over the trusted socket the compositor sends `{app, domain, paths[], mode: ro}` (the target's identity from its cgroup, as `trusted::socket` already derives it). The controller mints a powerbox-class ticket per path (`source:"powerbox"`, single use, bound to app, mode and path) and mounts the file into the domain read-only at a path the domain can see, for example `/run/aurora/drops/<n>/photo.png`. This needs a controller verb that mounts into a running domain; none exists (grants today are decided at invoke).
4. **Rewrite the list.** The target is offered `text/uri-list` naming the in-domain paths, never the host ones. A `text/plain` twin is rewritten the same way or dropped.
5. **Show it.** The capability indicator (0016, on another branch at the time of writing) would show the grant while it is live; revocation is unmounting.
6. **Approval.** For `ro` of a regular file, none beyond the drop. A directory, or `rw`, goes through the approval prompt (0007 amendment), because one drop would otherwise grant a tree.

Open: whether the mount is per drop or per session (per drop is simpler and revocable; per session lets a re-drop reuse it); how an app that never asked for files is told a file arrived (it already handles `text/uri-list` as ordinary DnD, which is the point); and Haiku apps, whose drops arrive as `B_SIMPLE_DATA` messages with `refs` the shim would have to build from the in-domain path.

## Guest to host (not built)

Not impossible; not done, because the missing piece is a design and cannot be verified from here. `wl_data_device.start_drag` on our queue needs the origin `wl_surface` (winit's, from raw-window-handle) and the serial of the pointer press (a `wl_pointer` on our queue hears it with its serial). But the host takes the pointer from Aurora the instant `start_drag` is called, so either every guest drag, including guest-to-guest, would round-trip through the host, or the hand-off would have to happen at the window edge while the press is still held (valid: the implicit grab outlives the surface edge), with a source wrapper that can be detached from Smithay's `DnDGrab` without cancelling. Both change guest-to-guest behaviour for the sake of a case that, with a full-screen Aurora window, rarely arises. Decide that, then build it on this transport.

## Consequences

- Host to guest drag and drop works for text, HTML, images and, outside domains, files, with no copy of the data through Aurora.
- Inside a domain a file drop is refused loudly until the grant exists. Users will see a drag that "does not take" and a log line, which is the honest state.
- `--deny-cgroup PATH` now also makes clients at or below that cgroup count as confined for the clipboard and drag policy (it already did for the trusted socket). That only ever narrows access.
- The drag bridge opens one more object on winit's connection and reads that socket; winit's own reads can move our events into our queue, so the winit backend ticks the bridge once per frame.
