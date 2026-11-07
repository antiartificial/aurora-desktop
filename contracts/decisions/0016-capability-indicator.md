# 0016. The capability-in-use indicator: a trusted dot, driven by the grant lifecycle

- **Status:** proposed
- **Date:** 2026-11-06

## Context

The request was "a privacy alert similar to macOS, when the mic or camera is in use". There is no audio or camera code in this tree. What exists since decision 0014 is `device:<class>` grants: `device:gpu` (a render node) and `device:wayland` (the compositor socket), minted by the controller as bus handles and carried in provenance.

`device:wayland` is the more sensitive of the two, and more than a microphone in one respect: a domain that holds it can show windows that look like any app and receive the input of its own surfaces, and 0014 records that nothing yet stops it drawing a look-alike approval dialog. It is the capability a person would most want a light for. So this record builds the general mechanism against the classes that exist, with a microphone and a camera slotting in later as new `device:` classes and one line of theme.

The compositor already has a trusted layer: the approval overlay is drawn after every capture was served, by compositor code, with no protocol object (`compositor/src/trusted/`, proved in `demos/full-stack/run.sh` step 5). The indicator is drawn there.

## Decision

### What is shown

While a **domain holds** a capability whose class is on the theme's list, a pill sits at the top-right of the output: an orange-red dot and `Display: <domain>` (`GPU: ...` and so on for other classes, `+N` when several are held). Hovering it, or clicking it to pin it, opens a small panel: for each held grant, what it is (`Display access`), the domain, whether busd attests the domain (`attested by busd`, or `NOT ATTESTED` in a warning colour), how long it has been held, and the grant's cap id. That is the capability inspector in miniature and nothing more; there is no revoke button.

The dot appears on an ease-out (`shell/motion.rs::Appear`, cubic, 140 ms) and is gone the moment the grant is revoked.

### Which classes count

`[indicator] classes` in `contracts/theme/aurora-glass.toml`: exact capability names as the bus carries them, hot-reloaded, and `enabled = false` turns the whole thing off. The default is **`["device:wayland"]`**.

- `device:wayland` is on. It is the ability to see and inject into the display's protocol surface, which is what 0014 says it is.
- `device:gpu` is off. A render node can compute; it cannot see the screen or any other app's windows. GPU kits are expected to run often (every Lumen scale, every future compute kit), and a dot that lights for each one trains the eye to ignore the dot that matters. The list is the control: put `"device:gpu"` in it and a GPU grant shows as `GPU: <domain>` (proved in `tests/indicator.rs`). This is a judgement and it is easy to disagree with; the point of a list is that disagreeing costs one line.
- `device:camera`, `device:mic` and the like are **not named**. Nothing grants them, and a guessed name would silently match nothing. The panel already has labels for `device:camera`, `device:mic` and `device:microphone` for when the adapter composes them.
- Path and network grants are not on the list and are not announced (below), so they cannot be added by editing it.

### Grant-driven, not device-driven

The indicator shows **possession**: from the moment the controller has composed the grant into the domain until it revokes the handle. macOS's dot shows **use**: it is driven by the device layer noticing a process open the camera.

Reasons for grant-driven, in the order they mattered:

1. **There is no device layer here to hook.** A Wayland socket is not opened and closed the way a microphone is; a client connects once and holds the connection. The compositor cannot see "using the display" separately from "has the display". For the GPU, the open happens in a kernel driver Aurora does not mediate.
2. **The grant is the thing Aurora actually controls and records.** Whether a capability is held is a fact the controller, the adapter and the bus all agree on and write down (provenance, cap ids, `controller provenance --verify`). Whether it is *being used right now* is a fact only the domain knows.
3. **It cannot be dodged by being quiet.** A device-driven light is off while the app is idle and on when it acts, so it tells you after the fact. A held-grant light is on for the whole time the domain *could* act.

The cost is real and is stated rather than hidden: the light means "this domain is able to", not "this domain is". A domain holding the display for eight seconds while idle shows the same dot as one that is drawing. For the microphone this is the wrong default for a recording indicator and is probably right for a standing "this app can hear you" one; which of the two a microphone class wants is a decision for when it exists, and it may want a second, use-driven source in addition to this one.

### How the compositor learns about grants

From the bus, on the **session socket**, as the user's own process, the same connection class `trusted/verify.rs` already uses. A thread (`indicator/bus.rs`, never the render loop) subscribes to three topics, replays the log once so a compositor that starts mid-grant still shows it, and while anything is held asks `bus.inspect@v1` every two seconds for the handles busd still has outstanding and the domains it has registered and cgroup-attested.

Topic rules respected, not routed around (`bus/server/server.go`): `provenance.`, `approval.`, `bus.`, `agent.`, `index.`, `supervisor.` (and `net.` where it has landed) are never delivered to a domain connection and a domain may not subscribe to them. The compositor is not a domain connection, so it may; no domain can hear any of this, which matters because the panel names other domains' grants.

A grant is **held** only when two statements about one cap id agree (`indicator/model.rs`, pure, unit-tested):

1. `bus.cap.minted` from `bus`, and no `bus.cap.revoked`. The `bus.` namespace is the daemon's own voice (a client's publish to it is refused), so existence, resource name, minter and time are busd's word. The minter must **not** be a `domain:` identity. This is not hypothetical: a domain connection may call `bus.mint@v1` for any resource name, busd stamps the domain truthfully as minter, and `demos/capability-indicator/run.sh` step 3 does it from inside a real domain and shows the forged `bus.cap.minted` for `device:wayland` in busd's log.
2. `provenance.grant.held`, published by **the same connection that minted the handle** (the event's `from`, stamped by busd, equals the handle's `minter`), naming the resource and the domain identity `domain:<name>:<id>`. The controller publishes it after `GrantDevice` succeeds and before the payload runs (`controller/internal/invoke/caps.go: announceHeld`). A domain can publish the same topic on its own socket, so the topic alone proves nothing: it is stamped `domain:...`, is not the minter, and is ignored. Attributions are kept whoever sent them and the minter's is the one that counts, so a forged attribution sent first cannot shadow the real one and hide the dot.

The bus does not carry the domain in `bus.cap.minted` (the handle is held by the controller, not the domain, and the domain is not known when it is minted), which is why the second statement exists. The domain name shown is parsed from the identity string and nothing else in a payload is ever drawn. busd's `bus.inspect@v1` then confirms the domain is registered with a cgroup it attests; that is the `attested by busd` line. A domain busd does not vouch for is still shown, in a warning colour: an unverified grant is not a reason to hide it.

### Unoccludable, unforgeable, uncapturable

- **Above everything.** `indicator::draw::draw_indicator` runs last in `render_and_capture` and `render_direct`, after the approval overlay (its scrim must not dim the evidence). A fullscreen client cannot cover it: `compositor/src/handlers/xdg_shell.rs` now honours `xdg_toplevel.set_fullscreen` (it ignored it before), and `tests/indicator.rs::a_fullscreen_client_cannot_cover_it` first proves a fullscreen client covers the output (control), then that the dot is lit over it, at scale 1 and 1.45.
- **Not in captures.** Captures are served from the framebuffer before the indicator is drawn, the property the approval overlay already has. Proved with pixels at both scales: with the indicator and its panel up, a client capture has zero pixels of the dot colour and is byte-identical to a capture taken before the grant; the compositor's own frame (`--test-input dump PATH`, a test affordance) has the dot. A mutation that drew the indicator before `serve_pending` made the test fail with "the capture contains the dot".
- **Not a protocol object, not a surface.** No global mentions it (`the_wayland_protocol_has_no_object_for_it`). The pointer over it belongs to the compositor like the dock's: a client sees `pointer_leave` and no further motion, and a click on it opens or pins the panel and reaches nobody.
- **What a client cannot do:** make the compositor draw it (see the forgeries below); cover it; remove it while the grant is held; capture it; hear the events that drive it; click through it. **What it can do:** paint pixels. A fullscreen client can fill the screen with the dot's colour. That is a picture, not the indicator: nothing is laid out, hovering opens no panel, and the pointer events reach the client (tested). The effect is a false alarm, never a false all-clear.

### What a client cannot forge, run

`tests/indicator.rs::a_client_cannot_cause_a_false_indicator` feeds the compositor, through a fake busd that speaks the real shapes: a mint by a domain; an attribution by a domain for the controller's real handle; an envelope claiming to be a mint but not stamped `bus`; an attribution naming a different resource than was minted; an attribution for a handle never minted; a Wayland client whose title and app id say `device:wayland held by domain:evil:1234`. The compositor lays out nothing and its frame is unchanged; a real controller grant after them lights it, naming the real domain. `demos/capability-indicator/run.sh` step 3 does the domain-side forgeries against the **real** busd from inside a **real** domain, with the same result.

## What this does not cover

**Grants only exist for domains, so a process that is not in one is invisible.** *(Amended by 0022. When this record was written the weakest point was that every windowed app in the dock was such a process. Windowed apps now launch in a domain with `device:wayland` from the dock, and the indicator lights for them: `demos/windowed-domain/run.sh` step 4 reads it for a real dock-launched Haiku app, with the dot in the compositor's frame, none in a capture, and the handle in busd's own table. What follows is what is still true.)* A host process that connects to the compositor and holds the display is invisible to this indicator, because there is no grant to observe. macOS's dot is driven from the device layer and covers every process on the machine; this one covers only processes the controller put in a domain and gave a capability. An unsandboxed app with a microphone would show nothing. **That is still the weakest point of the design, narrower than it was:** anything not launched by the controller. That is the desktop menu's "New Terminal", anything the user starts from a shell, and every app when the controller runs with `--unsandboxed-windows` (`windowed-domain` step 12 shows the indicator dark for one). `demos/capability-indicator/run.sh` step 7 still runs it with a host-process client, and still passes: a host process maps a fullscreen window over the whole output and the compositor lays out no indicator.

Making windowed apps launch inside domains with `device:wayland` (0012, 0014, 0022) moved the dock's apps under the indicator. A device-driven source on the compositor side could show the rest: the compositor now reads, at connect, which client is in a domain and which is not (`SO_PEERCRED` and the cgroup, `compositor/src/peer.rs`, 0022), so it could show a client with no domain as a distinct, honest "unsandboxed app, no restrictions" indicator. That is not built and would be a different claim from this one.

Other limits, each found or run, not assumed:

- **Only controller-minted grants light it.** `domain grant NAME --device wayland` by hand mints no bus handle, so the indicator never hears of it: the domain holds the compositor's socket and nothing shows (`run.sh` step 8). The adapter's recorded grants are the fuller truth; the bus is where this reads from. A future version could also watch the adapter's grant records.
- **"Held" is the handle's lifetime, not the adapter's.** The handle has a TTL (the invoke timeout plus ten seconds) and busd drops it from `bus.inspect@v1` when it expires, so a controller that dies mid-run lets the dot clear within about two seconds of expiry while the adapter's grant, which the controller was to revoke, may still be composed. The TTL behaviour is read from `bus/server/caps.go`, not run. A window's handle (0022) has no TTL, because a window has no deadline: it is revoked when the app ends, and a controller killed mid-window is a case this reasoning does not cover and that was not run.
- **Possession, not use** (above).
- **Small windows.** The attribution is published after the grant is composed and before the payload runs, but the compositor learns of it a few milliseconds later and draws a frame after that, so a payload that acts at once could act before the dot is drawn. At the other end, the device grant is removed from the domain slightly before the handle is revoked, so the dot can outlive the grant by a frame or two. Neither window was measured.
- **The domain is the controller's statement, checked against busd.** The compositor does not derive the domain from a socket the way it does for the clipboard: it takes the controller's word for which domain a handle was composed into, and checks only that busd has that domain registered and cgroup-attested and that the statement came from the handle's own minter. A compromised controller could name the wrong domain. *(0022: the compositor can now attribute a window to a domain by itself, from the kernel, and draws a badge for it. The indicator row is still built from the bus statement and is not cross-checked against the windows.)*
- **Bus down.** If busd goes away the indicator keeps showing the last thing it knew and says so in the panel (`Bus lost`); it cannot learn of a revoke it never heard. With no `--bus-socket` it never lights at all, and the compositor says so at start.
- **Not run on a real display.** All pixel evidence is from the headless backend. The winit and DRM backends call the same function; they were not run with this change, and the winit backend cannot read frames back, so capture exclusion is vacuous there.
- **Not proved:** the ease-out. The pill and panel use the shell's `Appear`, whose curve has its own unit tests; no test measures this element's first frames.
- **Not measured:** the per-frame cost (two small texture uploads while visible).
- **Layout:** the pill sits where the desktop's hostname label is and covers part of it. The indicator wins by design.
- **Side finding:** the dock is still drawn over a fullscreen client. Not changed here.

## Contrast

The panel is opaque, so legibility does not depend on what is behind it. Text and dim text are asserted on decoded pixels of the panel at both scales, over the worst backdrop (a white fullscreen client): 4.5:1 floor, with the unattested-domain warning colour included. A control test with a light panel measures below the floor, so the assertion can fail. Numbers are in the test output.

## Open questions

- **Use-driven sources** for the microphone and camera when they exist, in addition to possession: probably from the adapter or the device-owning service, not the compositor.
- **Should unsandboxed display clients get their own indicator?** Distinct from this one, from the compositor's own knowledge of who is connected (0022 added that knowledge). It would close the weakest point's visibility, not its cause.
- **Watching the adapter's grant records** as a second source, to cover hand-made grants and a controller that died.
- **Domain from the socket.** *(Half done by 0022: the compositor derives a window's domain from `SO_PEERCRED` plus the cgroup at connect. The indicator still takes the controller's word and the two are not compared.)*
- **A revoke from the panel.** Deliberately absent: a button in the trusted layer that can act is a new thing to attack.
- **Persistence across a compositor restart** needs nothing: the log replay brings back what is held.

## Consequences

- A person watching the top-right corner learns when a domain can reach the display, by which attested domain, since when, and by which cap id, which is the id in the provenance record of the run (`run.sh` step 4 checks the two are the same string).
- The controller now publishes `provenance.grant.held` for device grants. It is a system-namespace topic nobody else consumes.
- The compositor honours fullscreen requests. It did not before.
- Adding a camera or microphone class to the adapter and listing it in the theme is all the indicator needs to cover it, provided the controller mints it as a handle, and provided it is understood that this shows possession.
- Nothing here changes what a domain may do. It changes whether a person can see that it can.
