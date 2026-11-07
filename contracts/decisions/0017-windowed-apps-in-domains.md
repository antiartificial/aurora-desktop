# 0017. Windowed apps launch in domains, and the compositor knows which one

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Decision 0012 recorded that a windowed app, which is every Haiku app in the dock, launched as a plain host process outside the domain machinery that is this project's security argument. Decision 0014 made `device:wayland` a real, enforced capability and proved a Wayland client can run in a domain and map a window, and said so plainly: nothing launched one that way. Decision 0016's weakest point was the consequence: the capability indicator is driven by grants, a host process holds none, so nothing lit for any app in the dock.

This record closes that. The hard part (the one socket, bound alone, and the proof that `approval.key` and `powerbox.key` are not exposed) was 0014's. What was missing was a launch path that creates the domain, and a compositor that can say which domain a window belongs to.

## Decision

### Where launching lives: a controller verb, `registry.launch@v1`

A dock click sends `registry.launch@v1 {"app": ID}` to the controller's registry service. The compositor no longer starts the process. `registry.stop@v1 {"app"|"domain"}` ends one. Both are refused unless the caller is the session user (the rule `approval.decide@v1` uses): a domain's app cannot ask the controller to start another app in another domain with a display grant.

Why the controller, not the compositor:

- The controller already owns everything a domain launch needs: creating and destroying the domain, composing grants, minting the bus handle for each, announcing which domain holds it (what the indicator reads), registering the domain with busd, and revoking it all afterwards. The same code, in the same process, that launches a verb. The compositor would have needed the `domain` binary, cgroup write access and a bus capability client: a bigger and more privileged compositor, which is the process that draws the approval prompt and must stay small.
- The compositor already talks to the registry over the bus for `registry.apps@v1`; one more verb on that wire is the smallest change.
- The failure reason lives where the filesystem and the domain tooling are (`apps.App.Launchable`), and `launch_problem` is already the registry's field.

It is deliberately **not** a verb invocation. 0012's reason stands: `run --verb NAME`, JSON in, JSON out, a deadline the controller enforces, a result recorded in provenance. A window has no result, no input at launch and no deadline. `registry.launch@v1` returns once the app has stayed up for 1.5 s (`DefaultLaunchGrace`), carrying the domain name, its bus identity and the grants; it is a separate code path (`controller/internal/invoke/launch.go`) that shares the grant, handle and announce helpers and nothing of `Invoke`.

### What the domain gets

- **`device:wayland`, always.** The answer to 0014's open question "should `windowed = true` imply `device:wayland`": yes, at launch. A windowed app is by definition a Wayland client, and the alternative (a manifest line every GUI app must repeat) is a ceremony that cannot be forgotten into safety. It does not make `windowed` a lower-privilege flag: the manifest diff already treats a change to `windowed` or `exec` as needing fresh consent.
- **What the manifest declares, and nothing more.** `[capabilities] devices` (the adapter refuses a class it does not compose, and the launch fails with that reason) and `[capabilities] paths` (ambient, `read` as `ro`, `write` as `rw`; a declared path that does not exist is skipped with a log line). `[capabilities] network` is **not** granted: nothing in the controller grants a verb a host either, so a windowed app has no network.
- Everything else a domain is: bubblewrap, its own mount namespace, a subordinate uid, a cgroup under `aurora-domains`, an empty network namespace, `HOME` its own `/domain/data`, the host's runtime directory (and so both keys) not a path in it.

`demos/windowed-domain/run.sh` step 8 measures this from inside a fixture app with one declared read path and one declared write path: the grants are exactly `device:wayland`, the read path, the write path; the read path is readable and not writable; a sibling never declared is neither readable nor listable; `/run/aurora-display` holds `wayland-0` and nothing else; both keys read as 0 bytes; no 32-byte file in the domain has either key's contents (with a control that the same `cat` reads `/etc/passwd`); the only network interface is `lo`.

The real Haiku app launched from the dock holds exactly `["device:wayland"]`, because its manifest declares nothing.

### Lifetime, failure, and the way out

- **The domain's lifetime is the app's.** When the process ends, for any reason, the controller revokes the handles, revokes the grants, unregisters the domain from busd and destroys it. Handles are minted with no expiry (a window has no deadline) and revoked explicitly. Each launch is its own domain (`win-<app>-<hash>-<random>`, at most 28 characters), so two windows of one app are two domains, and none shares a domain with the app's verbs.
- **A launch that fails says why, on `launch_problem`.** If the app dies within the grace period, or the sandbox could not be fully enforced (the domain tooling printed `running WITHOUT its own uid` or `WITHOUT a domain cgroup`; the controller stops the app rather than offer it as sandboxed), the launch returns an error carrying the app's own last stderr line, nothing is left behind, and for 30 s `registry.apps@v1` reports that app as `launchable=false` with `launch_problem` `last launch failed: ...`. The dock already dims such an app and its menu row says why. The 30 s expiry exists so a fixed app can be tried again, since the dock does not offer a launch for an app reported as not launchable.
- **No fallback to a host process.** If domain tooling is unavailable the launch fails; it does not run the app on the host with a warning, as a verb does.
- **The escape hatch is a controller flag, `controllerd --unsandboxed-windows` (`aurora-up --unsandboxed-windows`).** It is process-wide, not a per-request field, so no client of the bus can opt itself out. It is off by default, logs a `WARNING` at startup and at every launch, and the reply says `isolated: false` with the warning. It exists for `HAIKU_SRC` builds and debugging. What it costs is shown by `demos/windowed-domain/run.sh` step 12: the app runs as our uid in no domain cgroup, and the indicator shows nothing for it.

### The compositor attributes the window

At connect, `compositor/src/peer.rs` reads the client's `SO_PEERCRED` pid and `/proc/<pid>/cgroup`, takes the path component after `aurora-domains`, accepts it only if it is a name the adapter could have created (`[a-z0-9_-]{1,32}`), and stores it on the client's `ClientState` with the pid and uid. This is the read the clipboard refusal already does, at the same place and for the same reason (asking for credentials from inside a global filter deadlocks every connecting client).

It is carried on the window: the title bar the compositor draws gets a **badge** at its right end, a filled pill with the domain's name, drawn by the compositor into the bar. A client can set its title to anything, including a domain's exact name, and that is only text beside the badge: `demos/windowed-domain/run.sh` step 5 runs a host client titled with the real domain's name; its window list entry has `domain: null`, its bar has no accent-coloured pixels at the badge's place (the domain window's has 2600), and its layout line has no `domain=` field. The compositor's `--test-input state` reports `domain` per window, and the `deco: layout` log line gains ` domain=NAME` only for a domain window, so a window without one logs exactly what it always did.

**A window with no domain is normal and unchanged**: a host process, XWayland, a test client. They are shown as nothing.

What this does and does not establish:

- A client the kernel places in a domain cgroup is attributed to it, and a client cannot lie its way out of the attribution: leaving the cgroup needs write access to the host's cgroup tree, which a domain's uid lacks.
- A host process of the same user can move itself *into* a domain cgroup (the delegated subtree is the user's) and would be labelled as that domain. That makes it look more confined than it is, never less. The label is evidence of where the kernel says the process is. It is not a proof of the domain's sandbox.
- It is decided once, at connect, for the pid that connected. A client that hands its socket to another process keeps the first process's attribution.
- A client that draws its own decorations (client-side) has no compositor bar, so no badge. The Haiku apps use the compositor's bar.

### The capability indicator

The launch mints a handle for `device:wayland`, composes the grant, and announces it on `provenance.grant.held` from the connection that minted it, exactly as a verb's device grant does (0016), so nothing in the indicator changed. `demos/windowed-domain/run.sh` step 4 reads it for a real dock-launched Haiku app: the row `Display access | win-controls-... | attested`, the dot in the compositor's own frame (60 pixels of the dot colour) and not in a client capture, and the handle in busd's own table with a minter that is not a domain. When the app ends the handle is gone from busd, and the compositor's frame is pixel-identical to the baseline.

## Findings

- **The kits' shared memory survives a domain, in `/tmp`, not `/dev/shm`.** `kits/src/shm.c` uses `/dev/shm` if `access("/dev/shm", W_OK)` succeeds, else `/tmp`. In a domain `/dev/shm` is a root-owned 0755 tmpfs; `access(2)` says no to the subordinate uid, although a write there succeeds, because the domain's payload keeps `CAP_DAC_OVERRIDE` and `CAP_FOWNER` in its own user namespace (`CapEff 0xa`; measured, and not something this record introduced). So the segment is `/tmp/aurora-kits-<subuid>-default`, 33,611,776 bytes, created by an app that ran and mapped a window, and not visible on the host. Consequence: **two Haiku apps in two domains do not share kit ports.** Anything in the kits that relies on two processes sharing one `aurora-kits-*` segment (the BMessage and port machinery between separate apps) does not cross a domain boundary. Each app is its own world, which is what a domain is; it does mean inter-app messaging between windowed Haiku apps is not available in a domain, and that has not been tried beyond this check.
- **Fonts, `/usr` paths and the dynamic libraries were not a problem.** The binary's libraries (`libwayland-client`, `libfreetype`, `libharfbuzz`, `libicu`, `libxkbcommon`, `libstdc++`) come from the host `/usr` that every domain binds read-only. Text rendered; Haiku Controls draws 228 distinct colours in its client area in the capture. No sandbox setting was loosened to make it work.
- **Files (`iface_browser`) is useless in a domain, and that is the sandbox working.** Its `$HOME` is the domain's empty data directory and the `aurora` binary it shells out to is not in the domain. Its manifest declares no path, because declaring `$HOME` would hand it the whole home directory. The way to show it a folder is a path grant chosen through the powerbox; windowed launches do not do that yet.
- **`iface_controls` and `iface_layout` sometimes close their window and keep running.** The window is gone from the compositor and the process stays. It happens with `--unsandboxed-windows` too (4 of 10 launches in a loop here), so it is the kits' shutdown and not the domain; `iface_text` ended on its close button 12 of 12 times. The demo says so when it happens and ends the app with `registry.stop@v1`, and ends `iface_text` by its own close button to prove an app that quits by itself tears the domain down. The owner of `kits/` should look at it.
- **The uid inside is not the uid outside.** The payload reports one uid and the host sees it as another (`46308` inside, `146307` outside in one run). Neither is ours or root.

## What this does not do

- **`device:wayland` is everything the Wayland protocol allows a client** (0014 states it and nothing here narrows it): any window of any size and content, including an imitation of another app, an approval dialog look-alike in its own area, the compositor's parser as an attack surface. Attribution lets the compositor *say* which domain a window is; it adds no per-domain policy. Global filtering (screencopy, virtual input, input-method) for domain clients is still open.
- **Not attributed to the title:** the title and app id are still what the client says. The badge is the new, compositor-drawn statement beside them. A user has to know to read the badge; nothing stops a window from being titled to look official, and a client controls everything inside its own area.
- **The indicator still takes the controller's word for the domain.** The compositor can now derive a window's domain from the kernel, but the indicator row is built from the bus statement, and the two are not cross-checked. Doing so would let a compositor notice a held grant whose domain has no window, or a domain window with no grant.
- **A host process is still invisible to the indicator.** `--unsandboxed-windows`, the desktop menu's "New Terminal", anything the user starts. 0016's weakest point is narrowed to those, not removed.
- **Controller death while a window is open is not tested.** The domain and the app are children of the `domain run` process, which is a child of `controllerd`; whether a SIGKILLed controller leaves a window with its grants held and its handle outstanding was not run.
- **GPU, network and per-domain resource attribution for a window**, and a `Files` that can see a folder, are not done.
- **The dock clips**: with fifteen apps the Haiku Text, List and Menu entries are past the dock's right edge at 1280 px, so the demo clicks Haiku Controls. Not changed here.

## Open questions

- Should a windowed launch be able to carry a path grant chosen by the user (the powerbox), so Files has something to show?
- Should the compositor cross-check the indicator's domain against the window's, and refuse to show an attested row for a domain with no window?
- Should a window carry a restart policy, and should the dock say a launched app is already running instead of starting a second domain?
- Should the compositor refuse `zwlr_screencopy`, input-method and virtual-input globals to domain clients (0014 open question)?
- Should the badge show the app's name (the controller knows it) rather than the domain's?

## Consequences

- A dock click on a Haiku app creates a domain with exactly one capability, and the indicator lights for it. 0012's statement that windowed apps run unsandboxed is false by default and true only under an explicit flag.
- `compositor` no longer spawns windowed apps and no longer needs to know where they are.
- A windowed manifest's `[capabilities]` is now enforced, so its absence is least privilege and not an unenforced silence (the manifests' comments were rewritten to say so).
- The controller holds more state: running windows, with their teardown. `controllerd`'s shutdown (`Invoker.Close`) ends them all and finishes their teardown first (a unit test; `aurora-up` also sweeps domains when it stops, so a cgroup check after it would not show this).
