# 0024. System verbs: one declaration table, an effect class each, and read-only where the owner is elsewhere

- **Status:** proposed
- **Date:** 2026-11-07

## Context

The standing goal names "userland utilities for networking/sound/keyboard/mouse/display". Before this record exactly one of the five was reachable as a verb: `system.audio@v1` and `system.audio.set@v1`, implemented in `controller/internal/service/service.go` over `controller/internal/sysaudio`. Display, network, keyboard and pointer existed as Settings **pages** in `compositor/src/shell/settings.rs`, which read `/sys` and the theme file directly. Four of the five utilities therefore sat outside the capability model that is the whole argument of the system: not grantable, not attestable, not callable by an agent, not recorded in provenance.

Two further things were missing even for audio, and they are the reason this is a record rather than a patch.

**A system verb had no declared effect anywhere.** `registry.verbs@v1` walks app manifests; a system verb has no manifest and no app, so `system.audio.set@v1`'s risk class existed only as a comment in a Go file. The gateway's tool policy and the approval prompt both key off `effect`, and neither could see one.

**The caller rule was nine `if`s waiting to happen.** `system.audio.set@v1` checked `mayDecide(req.From)` inline, at the top of its own `case` arm. With two verbs that reads fine. With nine it is a policy spread across nine places where the mistake is silent: an arm that forgets the check is a verb open to every domain, and nothing in the system says so.

## Decision

### The verbs, their effect class, and who may call them

Declared in one table, `systemVerbs` in `controller/internal/service/system.go`, which is both the thing enforced and the thing published by `system.verbs@v1`.

| Verb | Effect | Caller |
|---|---|---|
| `system.verbs@v1` | read | anyone |
| `system.audio@v1` | read | anyone |
| `system.audio.set@v1` | write | session socket |
| `system.audio.mute@v1` | write | session socket |
| `system.audio.sinks@v1` | read | session socket |
| `system.display@v1` | read | session socket |
| `system.pointer@v1` | read | session socket |
| `system.keyboard@v1` | read | session socket |
| `system.network@v1` | read | session socket |

The two mutating verbs are `write`, not `destructive`. Nothing is lost and the previous value can be put back, which is what separates a volume change from a delete. They are refused inside a domain by the same rule that refuses `registry.launch@v1`: an app the user gave a file to has not been given the speakers.

The rule is applied once, in `systemDispatch`, from the table, before any arm runs. `system.verbs@v1` serves the same rows, so what a caller is told about a verb and what is enforced for it come from one line. A table entry with no implementation answers `no_such_verb` naming itself, rather than `null`.

### What a confined app may read, and why that is not "everything" or "nothing"

**A confined app gets system state through the protocol that already serves it, not through a bus verb that reports the machine.** Its output size, scale and refresh arrive on `wl_output`. Its keymap and repeat rate arrive on `wl_keyboard`. Its pointer behaviour is the compositor's to apply and not its to read. Those channels give an app what concerns its own window. A `system.*` read gives it the machine: every connector including the dark ones, the monitor's make and model, every interface and every address.

So the only system verb a domain may call is `system.audio@v1` — volume and mute of the default sink, one user-visible figure that is not an identifier. It shipped open and stays open, because an in-window volume readout is a thing a person might reasonably want. `system.verbs@v1` is also open, because a catalogue of verb names carries no state about the machine.

`system.network@v1` is the strongest case and the one to notice. Decision 0015 is built so that a domain cannot see the network: no `resolv.conf`, no route, `lo` and nothing else in `/proc/net/dev`. Handing a domain the host's interfaces, link states and addresses would return that whole picture by another door, and would do it without appearing in the `net.` events 0015 added so that a person can see what is connected to what.

Two identifiers are handled deliberately. The **EDID serial number** is parsed past and never returned: it is a stable per-device identifier and nothing a settings page or an agent does with display state needs it. The **hardware address** of a network interface is likewise never returned. Neither omission is claimed as a privacy boundary — IPv6 link-local addresses *are* returned and under EUI-64 they encode the MAC, so the boundary is the caller rule above and the omissions are tidiness.

### Display is read-only, and the controller reads `/sys` rather than asking the compositor

The compositor knows more: the mode it set, the scale it applied, which output the dock is on. It is not asked, for three reasons.

1. **There is no channel.** The compositor serves no bus verb; it is a bus *client*. Its only server socket carries signed approval prompts. Adding a query is a compositor protocol change.
2. **The compositor need not be running.** A machine's displays exist before the desktop starts and after it crashes. A verb that answers only while the thing it describes is up is a verb a diagnostic cannot use.
3. **`/sys` plus the EDID is the authority the compositor itself defers to.** `compositor/src/drm.rs` picks the connector's PREFERRED mode and derives scale from the EDID's millimetre size. Reading the same two facts gives the same numbers from the same source rather than a copy of compositor state that can be stale.

What that costs is stated in the reply rather than papered over. `sysfs` carries the connector's mode *list*, not the mode that is set, so `current_mode`, `current_refresh`, `scale` and `primary` are reported in an `unavailable` map with the reason. (`primary` has no meaning in DRM or in Wayland at all; which display is primary is a compositor choice.) `native` is the EDID's preferred timing with the refresh computed from the pixel clock and the blanking, and `suggested_scale` is what `drm.rs auto_scale` would decide from it — labelled as what the compositor *would* choose, because `--scale` and the theme's `[output] scale` both override it and neither is readable here.

A **write verb for scale is not built**, and read-only is the accepted stage for display. The compositor owns scale, hot-reloading it is known to wedge a headless session, and a write path would need the channel that reason (1) says does not exist.

`AutoScale` is a second copy of a rule that already exists in Rust, which is what `demos/theme-drift/run.sh` exists to warn about. It is copied anyway, because the alternative is reporting nothing for the figure a settings page most wants; the copy is eight lines with no state, and `TestAutoScaleMatchesTheCompositor` holds it to `drm.rs`'s own test cases.

### Network is read-only, and no write path is proposed

Not a stage: a decision. 0015 settled what a network grant means, and nothing in it is a knob for changing the machine's own configuration. Bringing an interface up, changing an address or joining a network is NetworkManager's or systemd-networkd's job, needs privilege this process does not have and should not acquire, and deserves its own record. The filter (hardware-backed only, no loopback) is the same `device`-symlink test `compositor/src/deskbar/sysinfo.rs` uses, so the deskbar and the verb never disagree about which interfaces exist; what was filtered out is published in `virtual` rather than dropped silently.

### Pointer and keyboard are read-only, and the write path belongs to the compositor

This is the one place this record declines what the work asked for, so the argument is set out in full.

The theme file has exactly **one writer**: the compositor, through `Fx::apply_edits`, which rewrites the keys it is given while preserving every comment and line it was not asked to touch, then picks up its own change through the normal hot reload. The settings panel holds no second copy of any setting; it is rebuilt from the file after each change. That single-writer property is what makes the panel correct.

A controller write verb breaks it twice:

- It needs a second comment-preserving TOML rewriter, in Go, for a file whose format is defined by a Rust implementation. That is precisely the shape of `demos/theme-drift/run.sh`: one file, two programs' understanding of it. A second *writer* is worse than a second reader.
- Two processes rewriting the same file have no ordering. A controller write between the panel's read and its write loses silently, with no error for anyone to see.

So the write path for pointer and keyboard belongs to the component that owns the file. What it needs is a verb the **compositor** serves, reaching the same `Fx::apply_edits` the panel already calls — which means a request channel into the compositor that does not exist yet. Until then these are first-class reads and nothing claims otherwise.

The reads are honest about a second limit. They report the **configured** values, not device state. `pointer_speed`, `natural_scroll`, `tap_to_click` and `left_handed` reach the devices through libinput, which exists only on the DRM backend, and `--drm` has never run on a display; while the compositor is a nested winit client the host owns the devices and nothing in the table is in force. The controller cannot tell which backend the compositor is running, so `reaches_devices` is reported unavailable with that reason rather than guessed.

The theme directory is **not guessed**. `controllerd --theme-dir` (or `AURORA_THEME_DIR`) names it; empty means those two verbs report unavailable and name the flag. The compositor's own default is the repo's `contracts/theme`, and a controller that assumed it would answer about a tree it may not be running from.

### Do not invent data

Every reply distinguishes "the figure is X" from "there is no figure". `available: false` with a `reason` where nothing could be reached; a per-field `unavailable` map where the verb could answer some things and not others; `null` rather than `0` for a link speed the driver has no answer for; `[]` rather than `null` for an empty list. A `-1` from `/sys/class/net/*/speed` is not a speed, a speed read from a link that is down is the driver's last answer and not this link's, and a zero pixel clock in an EDID is not a 0 Hz mode.

`system.audio.mute@v1` takes `{"muted": true|false}` and **requires** it. A plain `bool` would decode a missing field to `false`, so an empty payload would silently unmute the machine — a change nobody asked for from a request that looks like a query. It is a state and not a toggle: two callers toggling race each other, and a mute key pressed twice is indistinguishable from one press that was lost.

## Open questions

- **How a system verb reaches the gateway, and so an agent.** The gateway's policy (`gateway/policy.go`) is keyed on app id and filesystem roots, and refuses a policy with an empty `apps` or `roots`. A system verb has neither, so offering one as an MCP tool needs a policy grammar change — a `system` pseudo-app, or a separate list — plus a decision about which of these reads an agent should have at all. Not built. Until then the `system.*` family is callable by any same-uid process on the bus and invisible to the gateway.
- **Does a system verb belong in provenance?** `registry.invoke@v1` writes a provenance record; these verbs write nothing. `system.audio.set@v1` has changed the machine for every application since it shipped and left no record. The mutating verbs probably want a `system.` event on the bus, in the shape 0015 used for `net.`.
- **Should the Settings pages read through these verbs?** The Display and Network pages read `/sys` in Rust and the Pointer and Keyboard pages read the theme in-process; the verbs now read the same facts in Go. Nothing was changed in `settings.rs`, so there are two readers of each. The pages are the ones with a window to draw and the lower latency; the duplication is real and is the thing to resolve when the compositor serves verbs of its own.
- **`drm.rs` disagrees with itself about scale.** `add_output` derives it from the connector's width only; `probe` takes `max(width, height)`. The two differ on a display whose EDID millimetre size is anisotropic. `AutoScale` here follows `add_output`, because that is the one that runs. Worth fixing in one place.
- **Should `system.audio@v1` stay open to a domain?** It shipped open and is left open. The argument for narrowing it is the same as for the others; the argument against is that an in-window volume readout is legitimate and the figure is not an identifier.

## Consequences

- Four of the five utilities named in the goal are now first-class verbs **for reading**: display, network, pointer, keyboard. Sound is first-class for reading and writing. No utility has a verb-mediated write path except sound.
- Every `system.*` verb now declares an effect class and a caller rule, in one inspectable place, and `system.verbs@v1` publishes both. A new verb that forgets its arm, or whose declaration disagrees with what is enforced, fails a test rather than shipping.
- `registry.verbs@v1` is unchanged. System verbs are listed by `system.verbs@v1` instead, because that verb's contract is "the verbs app manifests declare", every row has an `app`, and the shell and the gateway both read it that way. Folding appless rows in would change an answer two components already depend on, to save a verb name.
- `controllerd` gains `--theme-dir` / `AURORA_THEME_DIR`. Without it, two of the nine verbs report unavailable; the rest read `/sys` and need no configuration.
- The audio verbs inherit the controller's environment, and that is now load-bearing in a way nothing documented before: a `controllerd` started with a scrubbed `XDG_RUNTIME_DIR` finds no PipeWire socket and `system.audio@v1` correctly answers `available: false`. The session's `XDG_RUNTIME_DIR`, or `PIPEWIRE_RUNTIME_DIR`/`PULSE_RUNTIME_PATH`, has to reach the process.
- `controller/go.mod` gains `github.com/BurntSushi/toml`, which the workspace already carried for `contracts/manifest`.
