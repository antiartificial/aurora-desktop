# Toy Desktop OS — Design & Implementation Plan

Nov 6, 2026 · @Aaron

## Abstract

This project builds a toy desktop operating system to explore what a desktop could be in 2026, starting from BeOS and Haiku's ideas: a responsive, integrated system where one design runs from the kernel to the pixels.

The thesis is that most of what makes a desktop good lives in userspace, as with macOS (Quartz, Cocoa, Spotlight, launchd on top of XNU) and the PS5 (Sony's userland on a FreeBSD-derived kernel). So the work focuses on the layer people actually touch: a message bus that every app, the shell and agents share; apps described by manifests of typed verbs; domains that isolate each app by construction; capability-based security driven by user gestures; a compositor that owns animation, glass effects and trusted UI; and a filesystem layer with attributes, live queries and history.

Everything is built and run first on openSUSE Tumbleweed, using the 4090 natively, behind a small kernel-adapter interface. In parallel, and never blocking the UX work, the same userland is brought up on Ginux, an independent Rust kernel with a Linux personality and domains as a kernel primitive, first in QEMU with virtio-gpu and Venus for GPU acceleration. Linux supplies the drivers and the app catalog; this project supplies the experience.

## Goals and non-goals

The goal is a working, delightful prototype of new desktop ideas, not a production OS.

**Goals**

1. **Fast UX iteration.** Change a shader, theme or layout and see it in under a second, without logging out.
2. **One integration layer.** Apps, the shell, scripts and agents all act through the same bus and verbs.
3. **Isolation by construction.** Every app runs in a domain that starts with nothing and is granted capabilities.
4. **Moddable like early-2000s Windows.** Aero-style glass, Flip 3D-style Alt-Tab, full theming, all supported by the OS rather than hacked in.
5. **Agents as first-class, safe participants.** Typed verbs, effect classes, snapshot transactions and approvals drawn by the system.
6. **Durable under failure.** Driver, compositor and app crashes are restarts, not lost sessions.
7. **Kernel-portable.** The same userland runs on Linux now and on Ginux later.

**Non-goals (for now)**

- A server OS, or a daily driver.
- Writing drivers from scratch. Linux's drivers are used, directly on Tumbleweed and through a LinuxKPI shim on Ginux later.
- A new filesystem. Btrfs on Linux, ZFS on Ginux, with the new ideas layered on top.
- A new terminal emulator. libghostty-vt provides emulation.
- Bare-metal Ginux, or ARM64, before the VM path works end to end.
- Supporting other people's hardware.

## Principles and method

Every phase ends in a demo that takes 30 seconds to show; if a phase can't, it is scoped wrong.

**Design principles**

- **Reuse the plumbing, own the experience.** Linux drivers, Mesa, PipeWire, Wayland, Smithay, OCI images and Flathub are dependencies. The bus, manifests, domains, compositor, theme engine and query layer are ours.
- **Default-deny.** A domain starts empty. User gestures (opening, dropping, picking) are how capabilities are granted.
- **The bus is the source of truth for state worth keeping.** Window layout, events and history live in its log, so any server can crash and rebuild from it.
- **Control on the bus, data off it.** Messages carry commands, events and handles; bulk data moves through shared memory and dma-buf.
- **Every crashable part is a restartable server,** BeOS-style, under a supervisor.
- **Contracts are versioned from day one.** Bus messages, manifests and on-disk formats follow explicit evolution rules (the BeOS C++ ABI lesson).
- **Imperative UX, declarative record.** Users act normally; the system records generations they can diff, export and roll back.

**Method**

1. Write a one-page decision record before building each contract: three at Phase 0, and the kernel adapter's once Phase 2 has built it.
2. Build the thinnest vertical slice that makes the phase demo work.
3. Record the demo (a short screen capture) and keep it in the repo as the phase's exit evidence.
4. Only then generalize. Refactors happen at phase boundaries, not mid-phase.
5. Ginux work happens in its own track, whenever kernel work sounds fun, and never gates a UX phase.

## Architecture overview

Apps, the shell and agents talk only through the bus; services sit beside them, and nothing above the kernel adapter knows which kernel it runs on.

&#91;embedded content: system architecture · apps, bus, services, kernel adapter, backends\]

**Languages:** Rust for the compositor, Interface Kit and anything on the frame path; Go for the bus, controller, indexer and agent gateway; Zig only through libghostty. **Contracts** live in `contracts/` and generate code for both languages.

## The four contracts

Four interfaces are expensive to change later, so each gets a decision record before its first line of code. Everything else is free to churn.

### 1. Bus messages and capabilities

**Purpose:** the one way apps, the shell, the compositor and agents talk to each other.

- **Schema:** an IDL (Cap'n Proto is the leading candidate) for every message type, generated into Go and Rust.
- **Envelope:** message id, sender domain, target, verb or topic, QoS class (user-interactive, user-initiated, utility, background), hybrid logical clock timestamp, optional capability handles, optional shared-memory handles.
- **Capabilities:** unforgeable handles passed in messages, never ambient names. Cap'n Proto RPC's object capabilities and promise pipelining are the model.
- **Log:** append-only, segmented, replayable; every message durable unless marked ephemeral.
- **Evolution rules:** fields are only added, never renumbered or retyped; unknown fields are preserved; a verb's major version is part of its name.

**What a handle is, concretely, is not decided here.** Phase 2 builds domains and answers it in code; this record treats handles as opaque, unforgeable and transferable, and nothing more. The log stores a handle's identity, never its value.

### 2. App manifest

**Purpose:** one file that drives permissions, the launcher, agent tools, settings UI, widgets and updates.

- **Sections:** app identity and image hash; pinned runtimes; requested capabilities; types opened and created; verbs with input/output schemas and an effect class (read, write, destructive, external); emitted events; typed settings schema; replicants; config migrations.
- **Format:** TOML on disk, validated against a schema published in the repo, compiled to the bus IDL at install time.
- **Rule:** an update that changes requested capabilities or effect classes requires fresh consent.

### 3. Kernel adapter

**Purpose:** the line nothing above may cross, so the userland runs on Linux now and Ginux later.

| Operation | Linux backend (now) | Ginux backend (later) |
| --- | --- | --- |
| Create / destroy domain | bubblewrap + namespaces + cgroups | native domain |
| Restrict filesystem and syscalls | Landlock + seccomp | domain capabilities |
| Grant capability (file, folder, net host, device) | bind mount, fd passing, network namespace rules | capability handle |
| Snapshot / clone / roll back | Btrfs subvolume snapshots | ZFS snapshots and clones |
| Shared memory | memfd with seals, dma-buf | native shared memory, dma-buf |
| Suspend / resume | cgroup freezer | native domain suspend |
| Resource budget | cgroups v2 CPU, memory, IO | native scheduler and budgets |

**This record is written in Phase 2, not Phase 0.** The Linux column is built first and the record describes what was built; the Ginux column stays a sketch until G4. Writing it before either backend exists is the "contracts designed too early" risk in its purest form.

### 4. Compositor protocol

**Purpose:** how pixels and layers reach the screen.

- **Now:** standard Wayland (plus XWayland in the app's domain, layer-shell for shell pieces, portals for files and screen sharing).
- **Later:** a native layer-tree protocol in which native apps send layers and animations, and the compositor renders them at full frame rate (the Core Animation model).
- **Reserved now:** a trusted UI path the compositor alone can draw (approvals, permission sheets, secure attention), never themable, never reachable by clients.

**The trusted path needs an identity to name.** An approval sheet is only worth drawing if the compositor can say truthfully which domain asked. That comes from Phase 2's attested domain id, read from socket peer credentials, which is why the compositor cannot be built before domains exist.

## Phase 0: Foundations

**Goal:** one command each to run the nested compositor on Tumbleweed and to boot Ginux in QEMU, plus the records that decide what comes after. **Estimate:** 2–3 weeks.

**Deliverables**

- Monorepo with top-level folders: `contracts/` (IDL, manifest schema, decision records), `bus/` (Go), `controller/` (Go), `compositor/` (Rust), `shell/`, `kits/`, `apps/`, `adapter/linux/`, `adapter/ginux/`, `ginux/`, `harness/`, `demos/`.
- Toolchains: Go, stable Rust, Cap'n Proto compiler, Zig (for libghostty later), QEMU 10+ with KVM, virglrenderer. Versions recorded in the README.
- Nested compositor script: Smithay's `smallvil` example built and launched as a window on the current desktop, running `foot` inside.
- Ginux harness: `harness/ginux-up` builds GINUX.EFI, boots it under QEMU/KVM with OVMF, starts an Alpine domain and opens a serial console.
- **Three** decision records, status "proposed": bus envelope, app manifest, compositor protocol.
- Nvidia check: confirm the nested compositor runs on the 4090 with the current driver, and note the driver version.
- Vitruvian decision record: what the existing VOS tree contributes — the working GBM display backend, the per-window compositing work — and whether it is harvested, ported behind the kernel adapter, or retired. One page, written before Phase 1, so the answer is on the record instead of implied by silence.

**Why three records, not four:** the kernel adapter's hardest question is what a capability *is* — a passed fd, a bind mount, a netns rule. Phase 2 answers it by building it. Writing that record first, with nothing to constrain it, is the plan's own "contracts designed too early" risk in its purest form.

**Exit demo:** a terminal typing inside the nested compositor, then `harness/ginux-up` dropping into an Alpine shell on Ginux.

**Tasks**

- [ ] Create the repo and folder layout
- [ ] Install toolchains and record versions in the README
- [ ] Build and run `smallvil` nested; launch `foot`
- [ ] Build GINUX.EFI and write `harness/ginux-up`
- [ ] Draft the three decision records
- [ ] Write the Vitruvian reuse decision

## Phase 1: Visual proof, borrowed compositor

**Goal:** the project's look exists, is tuned and is recorded before a line of compositor code. **Estimate:** 1 week.

**Why this is now a phase:** glass and a Flip 3D-style Alt-Tab are the visual thesis, and both can be prototyped in an existing compositor's plugin API in days. Writing the compositor first puts the motivating demo months out and bets the look on shader work nobody has tried on this hardware. The old risk table already named this as the *mitigation* for the Smithay curve; promoting it to a phase costs a week and de-risks the most expensive phase in the plan.

**Deliverables**

- Dual-Kawase blur with tint and a highlight edge, as a Hyprland plugin (or a KWin effect), tuned until it looks right on the 4090.
- A Flip 3D-style window switcher in the same plugin, with live thumbnails.
- The shader and its parameter set (radius, passes, tint, Oklab tokens) checked into `contracts/`, in the shape Phase 8's theme engine will consume.
- A written note on what the host compositor made easy and what it fought — direct input into Phase 4's renderer choice.

**Exit demo:** glass and 3D Alt-Tab, recorded on the 4090 — the project's look, before the project has a compositor.

**Not a dependency:** nothing later imports this plugin. What carries forward is the shader, the parameters, and the knowledge that the effect holds frame rate.

## Phase 2: Domains v0 — the kernel adapter

**Goal:** a domain is a real, attested, empty thing that holds capabilities, before anything is built on top of it. **Estimate:** 4–6 weeks.

**Why this moved ahead of the bus and the compositor:** contract 1's centrepiece is unforgeable capability handles, and contract 4's trusted UI path has to bind an approval to the domain that asked for it. Both need an answer to two adapter questions — what is a capability, concretely, on Linux, and how does a server know which domain it is talking to. Answering them in code here means the bus log format and the compositor's client bookkeeping get designed once instead of twice.

**Deliverables**

- Kernel adapter, Linux backend, the identity-and-handle half of contract 3: create and destroy a domain (bubblewrap, namespaces, cgroups v2), restrict it (Landlock, seccomp), grant a capability (fd passing, bind mount, network namespace rule), snapshot / clone / roll back (Btrfs subvolumes), suspend and resume (cgroup freezer), shared memory (memfd with seals, dma-buf).
- Domain identity: every domain carries an id the kernel can attest. A server reads it from socket peer credentials and never from a message.
- `domain` CLI: `domain run`, `domain grant <path|host|device>`, `domain snapshot`, `domain rollback`, `domain freeze`.
- Kernel adapter decision record, now written against working code, status "proposed".
- Capability representation decision record: what a handle is on the wire, how it transfers between domains, how it is revoked, and what the log stores in its place.

**Deferred to Phase 6:** the app store, the powerbox picker, the activity page and generations. Those need manifests, the bus and a compositor; only the primitives live here.

**Exit demo:** `domain run` starts a shell that can see no files and reach no hosts; `domain grant ./photo.png` and it can read exactly that file and nothing else; snapshot it, delete everything inside, roll back.

## Phase 3: The bus

**Goal:** a durable, typed, replayable message bus whose capability handles are backed by the primitives Phase 2 built. **Estimate:** 8–12 weeks, in three sub-demos.

**On the estimate:** the earlier figure was 2–3 weeks for a segmented indexed log, four QoS classes, fd passing, capability handles, two bridges and a replay harness. That is a quarter of work. It ships as three demos so the cadence survives: (a) typed request/response between two domains, (b) the log, with queries, (c) replay.

**Design**

- Daemon (`busd`, Go): one Unix socket per session, one connection per domain. The sender's domain comes from Phase 2's attested identity, never from the message.
- Patterns: request/response for verbs, publish/subscribe for topics, streams for long-running work.
- Log: append-only segment files indexed by time, topic and sender; retention per topic; ephemeral messages (pointer motion, frame timing) skip the log entirely.
- QoS: four classes in the envelope. User-interactive is delivered first and never queues behind background traffic.
- Bulk data: messages carry memfd or dma-buf handles passed as file descriptors (SCM\_RIGHTS), not payload bytes.
- Capability handles: Phase 2's representation, carried in the envelope and logged *by identity rather than by value*, so the log never holds a usable handle.
- Bridges (stubs this phase): D-Bus and portals, enough to prove the shape.

**`bus` CLI**

- `bus send <target> <verb> [args]` — call a verb and print the typed reply
- `bus sub <topic>` — stream events
- `bus log --since 10m --topic window.*` — query history
- `bus replay <session> --speed 4x` — re-deliver a recorded session to a fresh daemon
- `bus inspect <domain>` — list a domain's verbs, topics and capabilities

**Replay is checked on invariants, not bytes.** The earlier exit demo asked for byte-for-byte equality, which a log carrying hybrid logical clocks, file descriptors and GPU-adjacent timing will not produce twice. Replay asserts on observable state instead: the same messages in the same causal order, the same final state per subscriber, the same set of capability grants. Byte diffing stays a debugging tool, never a CI gate — a flaky gate gets disabled, and then the real check is gone too.

**Exit demo:** two domains exchange typed messages while `bus sub` streams them; `bus replay` re-runs the session against a fresh daemon and the invariant check passes.

**Open decisions:** Cap'n Proto versus a custom IDL — the record has to settle two specifics, that promise pipelining exists so intermediate results never materialise while the log wants everything materialised, and that three-party capability handoff (RPC level 3) is what the later federation item needs and no implementation provides. Also: single broker, or peer-to-peer with a broker only for discovery and logging.

## Phase 4: Compositor v0

**Goal:** a nested Wayland compositor that knows which domain each client belongs to, handles real input on real monitors, and renders Phase 1's glass. **Estimate:** 8–12 weeks. The riskiest phase in the plan, and the reason Phase 1 exists.

**Scope**

- Base: grow from `smallvil` toward `anvil`'s features — xdg-shell windows, focus, move, resize.
- Renderer: Smithay's GLES renderer, with Phase 1's shaders ported across. Revisit Vulkan only at a real limit.
- **Input stack:** libinput, xkb keymaps, key repeat and modifiers, touchpad gestures, and a compositor-level binding for Alt-Tab. Unlisted in the old plan, and not free.
- **Output handling:** multi-monitor, hotplug and fractional / HiDPI scaling from the start. Also unlisted, also not free, and on a 4090 desktop it arrives on day one rather than at the end.
- Client identity: every Wayland client is bound to its Phase 2 domain through socket peer credentials, so approvals and the activity page later have something trustworthy to name.
- Trusted UI path: a surface class only the compositor can draw — never themable, never reachable by clients. Wired here, used in Phase 10.
- Bus integration: publishes `window.opened`, `window.focused`, `window.closed`, `window.moved`; exposes `focus`, `tile`, `close`, `layout.snapshot`, `layout.restore`.
- Frame timing: frame times to an ephemeral topic, with an overlay when a frame misses its deadline.

**Window state, decided — this is no longer an open question.** The compositor owns live window state; the bus log owns settled state. A drag publishes nothing until it ends, then one durable `window.moved`. Crash recovery restores the last settled layout and loses an in-flight drag, which is the right trade. Left open, this would have put the log on the interaction path at pointer rate, and the fix would have been a log format change after Phase 8 depended on it.

**Exit demo:** three terminals behind glass, Alt-Tab with live thumbnails across two monitors at different scale factors, then `bus send compositor focus <id>` from outside, with the events streaming in `bus sub window.*`.

**Not yet:** the display-proxy split for crash survival (Phase 8), the native layer protocol (Later).

## Phase 5: Manifests, verbs, launcher and provenance v0

**Goal:** apps describe themselves, the launcher composes their verbs, and every verb that creates a file records where it came from. **Estimate:** 4–6 weeks.

**Deliverables**

- Manifest schema v0 and validator in `contracts/`, with the sections from contract 2.
- Registry (in `controller`): loads manifests, registers verbs and types on the bus, and resolves "which verbs accept `image/png`?".
- Toy native app ("Lumen"): opens an image, exposes `resize`, `export` and `histogram`, emits `export.finished`.
- Wrapped CLI tools: manifests for ffmpeg and ImageMagick, each running in a Phase 2 domain whose grants are exactly its declared inputs and outputs. The declared effect class is *enforced* rather than asserted: a wrapped tool that reaches past its manifest fails instead of succeeding quietly.
- Launcher v0 (Quicksilver / Raycast style): pick an object, pick a verb filtered by type, fill typed arguments, run; chains of two verbs.
- Translators v0: a registry of type converters, so dropping a CSV on something that wants a table just works.
- **Provenance v0:** every verb that writes a file records app, verb, inputs and time, as a bus event and an extended attribute. Moved here from the old Phase 6 — the old Phase 3 exit demo already claimed provenance it could not produce, and at the moment a verb runs the information is free. Phase 9 adds the index, the queries and the graph; it no longer has to invent the data.

**Why manifests come after domains now:** for a native app a manifest is a contract. For `ffmpeg` it is a hope — its real effect class is "write wherever the arguments point", and nothing in the registry can verify otherwise. With the adapter already in place, the enforcement is real, and the launcher never offers a verb whose declared effects are not the effects it can have.

**Exit demo:** select a PNG, open the launcher, choose "resize → 800px" then "export as AVIF". The result appears, and `bus log` shows the chain with its provenance — the demo the old Phase 3 promised.

**Questions to settle:** how arguments are collected for verbs with many parameters without the launcher becoming a form; whether chains are saved as reusable recipes in this phase or later.

## Phase 6: App store, powerbox, activity and generations

**Goal:** every app is an immutable image plus a domain it owns, and permissions come from user gestures the compositor draws. **Estimate:** 6–8 weeks.

This is the second half of the old Phase 4, the half that needed manifests, the bus and a compositor before it could exist.

**Deliverables**

- App store: content-addressed images (OCI layers to start), mounted read-only, deduplicated by hash.
- Per-app domain: three subvolumes per app and person — `config/` (versioned), `data/`, `cache/` (always safe to delete).
- Install and uninstall: signature check, a permission sheet generated from the manifest and drawn on the trusted UI path, image added, domain created, verbs registered, generation recorded. Uninstall offers "keep my data" or "remove everything".
- Powerbox picker: a file picker drawn by the compositor. Picking a file or folder grants exactly that, by bind-mounting it into the app's domain.
- Activity page: per app, what it opened and which hosts it reached, read from the bus log, with a revoke button per grant.
- Generations v0: the set of app image hashes and grants, recorded on every change. Booting a previous generation can wait; the record starts here.
- The consent rule enforced: an update that changes requested capabilities or effect classes requires fresh consent.

**Exit demo:** install Lumen, open a photo through the powerbox (Lumen sees only that file), read the activity page, uninstall with "remove everything", and show nothing remains outside the deleted domain.

## Phase 7: Real Linux apps

**Goal:** the desktop runs software people actually use, so every phase after this is tested against real apps instead of toys. **Estimate:** 3–4 weeks.

**Why this is a phase at all:** the abstract claims Linux supplies the app catalog, and the old sequence never installed a single Linux app — Flathub appeared as a dependency and then nowhere, with Wine, web apps and Waydroid pushed into "Later". Until a browser and an editor run inside domains, every later phase is validated only against apps written to make it easy.

**Deliverables**

- Flatpak apps as domains: a translator from Flatpak metadata to manifest v0, with requested permissions mapped to adapter grants.
- XWayland running inside the app's own domain, for apps that need it.
- Portals bridged to the bus: file chooser → powerbox, screenshot and screen share → compositor-drawn consent, notifications → the shell.
- Labeled developer domain: an explicit, visibly marked broad-access domain for apps that expect the run of the machine, so the defaults stay strict for everyone else.
- A dogfood list that has to keep working from here on: browser, editor, image viewer, media player.

**Exit demo:** Firefox and an editor running in their own domains, each reaching files only through the powerbox, with their grants listed on the activity page and revocable from it.

## Phase 8: Theme engine, shell pieces and crash survival

**Goal:** the desktop is fully themable live, no single crash costs the session, and a glass regression fails the build. **Estimate:** 5–7 weeks.

**Theme engine**

- Theme package: design tokens in Oklab (colors, radii, spacing, fonts), window frames, cursors, sounds, boot splash, and effect shaders — Phase 1's, by now proven on real hardware — plus a manifest.
- Live swap: themes hot-reload with no logout. A shader that fails to compile or trips the frame watchdog falls back to the last good version automatically.
- Effects on events: a theme may subscribe to a **named allowlist of topics declared in its manifest** — build status, an approval waiting, time of day. Subscribing is a capability, so a theme is capability-*minimal*, not capability-free; the earlier wording claimed both at once.
- Nothing beyond that list, and never the trusted UI path.

**Shell pieces**

- Dock, launcher and notification area as separate layer-shell clients, each restartable on its own.
- Replicants v0: desktop widgets that subscribe to bus topics (CPU, now playing, build status).

**Crash survival**

- Display-proxy split: a small process holds client connections and buffers; the effects compositor connects to it and can crash or be hot-swapped.
- State from the bus: on restart, the compositor replays the last *settled* window layout from the log, per Phase 4's decision.
- Supervisor: the controller restarts shell pieces and the compositor with back-off, and publishes crash reports to the bus.

**Visual regression harness.** A screenshot harness renders a fixed window set under each theme and diffs against committed references. Without it, the one thing this project is selling is the one thing nothing tests — bus replay checks messages, not pixels.

**Exit demo:** switch between two themes live (glass to flat and back), `kill -9` the compositor, and watch it return with every window where it was.

## Phase 9: Storage — attributes, queries and history

**Goal:** BeOS-style attributes and live queries on top of Btrfs, built on the provenance Phase 5 has been writing since the launcher's first run. **Estimate:** 6–8 weeks.

**Deliverables**

- Attributes: typed attributes stored as extended attributes, with a schema registry — types and attribute names declared by manifests.
- Index daemon (`indexd`, Go): watches changes with fanotify and keeps an index of attributes, relationships and provenance. contextdb is the candidate backing store for its graph, bi-temporal history and vectors.
- Live queries: `/queries/` as saved queries; apps and the shell subscribe over the bus and get updates as files change.
- Relationships: "exported from", "used in", "derived from" edges, built from the events Phase 5 already emits rather than inferred after the fact.
- History: "this file as of Tuesday", from Btrfs snapshots plus the bus log of what changed it.
- Results honor capabilities: a query from an app or agent returns only files that domain can see.

**Exit demo:** a saved query "Images exported this week" in the dock; run a Lumen export and watch the new file appear in it within a second, then open its provenance and history — which now reaches back to Phase 5's first exports.

**Later in this area:** the on-device semantic index (Intelligence Kit) and sync between machines (Sync Kit).

## Phase 10: Agents

**Goal:** agents use the same verbs as everything else, inside their own domains, and every risky effect goes through the user. **Estimate:** 6–8 weeks.

**Deliverables**

- Agent gateway (Go): generates MCP tools from manifests (verbs, schemas, effect classes) and from the query layer. An agent sees only the verbs its capabilities allow.
- Agent domains: each session gets its own domain with nothing inherited, plus the specific handles granted — this folder, these verbs, this host.
- Effect-class policy: reads free; writes inside the agent's snapshot; destructive and external effects require approval.
- Trusted approvals: the compositor draws the approval with the real verb and arguments, on the Phase 4 trusted UI path, bound to the agent's attested domain.
- Transactions: the agent works in a copy-on-write snapshot; the user reviews a diff — files, settings, messages it wants to send — and commits or discards.
- **Taint labels v0, with saturation decided up front.** Labels propagate as the union of the inputs' labels, which for opaque verbs (every wrapped CLI tool) is the only sound rule, and which will label most of a working set within a day. The answer is an explicit declassification gesture — the user marks a value clean and the act is recorded — not a cleverer propagation rule. CaMeL's stronger guarantee comes from its structure, a privileged model that emits the program and a quarantined model that never holds capabilities; that is not available to a general-purpose bus, and the plan should not claim it is.
- Audit: every agent action recorded with agent, capability, verb and inputs, viewable on the activity page.

**Exit demo:** ask an agent to "resize the photos in this folder, put them in a new album, and draft a share link". It does the first two inside a snapshot, shows the diff, and the share step stops at a system-drawn approval.

**Read first:** Debenedetti et al., "Defeating Prompt Injections by Design" (CaMeL), Google DeepMind, 2025 — for the taint-tracking design and, as importantly, for what the design buys only when the structure comes with it.

## Revised sequence

&#91;embedded content: revised phase sequence · 11 phases, the three cross-dependencies that set the order\]

Phase 1 sits off the spine on purpose: it hands Phase 4 a shader that has already run at frame rate, and gates nothing. The three side edges are the ordering fixes — attested domain identity into the compositor, provenance into the storage phase, and the visual proof into the renderer choice.

**On the totals.** The phases sum to roughly 53–75 full-time weeks: a year to eighteen months at full pace, three to five years at evenings and weekends. The number matters less than what it does to the cadence. At that length a phase is a quarter, and a quarter with one demo at the end of it is exactly where the motivation risk bites. So every phase above either carries sub-demos or is short enough not to need them, and no stretch of work should run more than two weeks without producing something recordable.

## Ginux track (parallel)

**Goal:** prove the kernel adapter seam by running the same userland on a second kernel. Retargeted by decision 0005: upstream Ginux retired its bootable UEFI kernel and is now a userspace kernel that runs as an ordinary Linux process, so there is no QEMU and no GINUX.EFI in this track. Pace: whenever kernel work sounds fun; never blocks a UX phase.

**G1. The bus inside a Ginux domain**

- Build Ginux with its own `make build`. The target is `x86_64-unknown-linux-gnu` and the binary is `ginux`; there is no EFI image on the current branch.
- Run `busd` and a client inside a Ginux managed domain. Log every unimplemented or partial syscall they hit, and fix the gaps.
- Exit: `bus send` and `bus sub` working between two Ginux domains, with no QEMU anywhere in the loop.

**G2. Ginux as a kernel adapter backend**

- Implement contract 3 against Ginux's own domain contract: create and destroy, restrict, grant a capability, snapshot and roll back, suspend and resume, resource budget.
- Compare it operation by operation against the Linux backend. The operations with no clean equivalent are the real finding, and they are what decision 0005's constraint is about: contract 3 must stay expressible by a kernel that owns the hardware, because bare metal is still a goal.
- Exit: the Phase 6 and Phase 7 demos run unchanged with the Ginux backend selected.

**G3. Syscall-trace diffing**

- The same workload under the Linux adapter and the Ginux adapter, traced on both sides and diffed; the first differing line is the bug. Cheaper than originally planned, because both sides now run on the same host.

**Deferred to the bare-metal route**

These were G2, G3, G5 and the "After that" list. Every one of them needs a kernel that owns the hardware, which a userspace kernel does not: pixels on screen via virtio-gpu and DRM/KMS; Venus GPU acceleration and its `vulkaninfo` to `vkcube` to Zink to glass chain; drivers as restartable server domains with a fence watchdog; the LinuxKPI shim; NVIDIA's open kernel module on bare metal; ARM64 and Apple Silicon via Asahi.

None is abandoned. The frozen UEFI kernel, preserved upstream at tag `bootable-kernel-final`, is the starting point when that work resumes. Decision 0005 records what preserving the route back requires.

## Later phases

These follow Phase 10 in roughly this order; each gets its own goal and exit demo when it starts.

| Area | What it adds | Exit demo idea |
| --- | --- | --- |
| Interface Kit and layer protocol | A native toolkit on Vello-style GPU vector rendering; apps send layer trees, the compositor animates them; declarative UI with hot reload; accessibility tree from the start | A native app whose animations stay smooth while its main thread is blocked |
| Terminal | libghostty-vt plus command blocks (OSC 133), typed output, `bus` from the shell, per-command snapshots and undo | Undo an accidental `rm -rf` with one key |
| Capability shell | Commands run in sub-domains with only their arguments (Shill/Capsicum ideas), effect preview before run, transactions | "This will delete 340 files" shown before Enter |
| Media Kit | PipeWire graph exposed on the bus, the desktop patch bay, Vulkan Video with zero-copy dma-buf | Route a camera through a filter app into a recorder by dragging wires |
| Scheduling | QoS-driven sched\_ext scheduler fed by the compositor's focus signal | A build at full load while the foreground window keeps 120 Hz |
| Settings and generations | Typed settings stores per app, undo per setting, exportable generations, boot a previous generation | Undo last week's display change; diff two generations |
| Sync Kit | Local-first sync of settings, documents and the bus log between machines (Eg-walker, Loro/Automerge) | Change a setting on the Tumbleweed box, see it on the Mac Mini |
| Intelligence Kit | On-device transcription and embeddings as shared services feeding the semantic index | "Photos of the lake at sunset" as a live query |
| Runtimes | Wine/Proton (needs `ntsync` on Ginux), web apps as domains with manifests, Waydroid (needs binder) | A Windows game and a web app running side by side, both on the bus |
| Federation | The bus spans machines with signed capability tokens; move a domain between machines | Send a running window from one machine to another |

## Cross-cutting concerns

These start at the phase named and apply to every phase after.

- **Deterministic replay tests.** Record bus sessions as fixtures; CI replays them against new builds and checks invariants — causal order, per-subscriber final state, the set of capability grants — never byte equality, which a log carrying hybrid logical clocks and file descriptors cannot deliver twice.
- **Syscall-trace diffing.** The same workload on Linux and Ginux, traced and compared; the first differing line is the bug.
- **Versioning.** Bus messages, manifests, settings schemas, indexes and generations all carry versions and migrations.
- **QoS everywhere.** Every message and domain carries a QoS class from Phase 3, even before the scheduler uses it.
- **Latency budgets.** Track input-to-photon and frame-time percentiles from Phase 4; regressions fail the build.
- **Accessibility.** One semantic UI tree in the Interface Kit, serving screen readers and agents; bridge AT-SPI for Linux apps.
- **Input methods and international text.** The Wayland text-input protocol bridged to fcitx5 or IBus before the toolkit settles; right-to-left text in the first native app.
- **Ordering across machines.** Hybrid logical clocks in the envelope from Phase 3, so federation later doesn't need a format change.
- **Observability.** Structured logs and crash reports go to the bus; the inspector can show any component's recent messages.
- **Security reviews at contract changes.** Any change to capabilities, effect classes or the trusted UI path gets a written threat note.

## Review and resolutions

Fifteen findings from a review of the first draft. Each is resolved in the phase named below rather than left for whichever phase would have hit it.

| Finding | Kind | Resolved in |
| --- | --- | --- |
| Contract 1's capability handles were to be designed before any adapter existed to say what a capability is | Structural | Phase 0 writes three records, not four; Phase 2 builds the adapter and writes that record against working code |
| The compositor was scheduled before domains, so it had no attested identity to bind an approval or an activity entry to | Structural | Phase 2 precedes Phase 4; Wayland clients bind to their domain by socket peer credentials |
| Phase 3's exit demo claimed provenance, which the old Phase 6 delivered | Inconsistency | Provenance v0 moved into Phase 5, where the verb that writes the file already knows its inputs |
| A manifest cannot constrain a wrapped CLI tool, whose real effect class is "write wherever the arguments point" | Structural | Phase 5 runs wrapped tools in domains granted exactly their declared inputs and outputs, so the class is enforced |
| "Source of truth for window state" sat as an open question while the log format and crash recovery both depended on it | Structural | Decided in Phase 4: the compositor owns live state, the log owns settled state, a drag publishes once on release |
| Byte-for-byte replay equality as a CI gate, over a log holding hybrid logical clocks and file descriptors | Method | Phase 3 asserts causal order, per-subscriber final state and the grant set; byte diffing stays a debugging tool |
| Themes were specified as capability-free and as subscribers to bus events | Inconsistency | Phase 8: a theme declares an allowlist of topics in its manifest and is capability-minimal, not capability-free |
| Union-propagated taint labels saturate within a day of real work, and CaMeL's guarantee does not transfer to a general bus | Method | Phase 10 adopts union propagation plus a recorded declassification gesture as the stated answer, up front |
| No phase installed a single Linux app, though the abstract rests on Linux supplying the catalog | Gap | New Phase 7: Flatpak apps as domains, XWayland per domain, portals bridged, and a dogfood list |
| Input (libinput, xkb, gestures, the Alt-Tab grab) and output (multi-monitor, hotplug, HiDPI) were unlisted | Gap | Named in Phase 4's scope with their cost acknowledged |
| Nothing tested the visuals, which are the project's whole selling point | Gap | Screenshot regression harness in Phase 8, diffing a fixed window set per theme against committed references |
| G3 could stall indefinitely on Venus quirks outside the project's control | Method | G3 gets an abort condition; the Ginux track's value is G4, the adapter, not GPU acceleration in a VM |
| Estimates ran roughly three to five times optimistic, and the phases were long enough that demos fell too far apart | Method | Re-estimated per phase; totals and the demo cadence rule stated under Revised sequence |
| The existing Vitruvian tree, with a working GBM backend, went unmentioned | Gap | Phase 0 writes a one-page decision: harvest, port behind the adapter, or retire |
| Cap'n Proto was named without its two live trade-offs | Method | Phase 3's open decisions name both: pipelining versus an append-only log, and RPC level 3 versus the federation item |

**What the review left alone:** the four-contracts framing, exit demos as the scope defense, the kernel adapter as an explicit seam, control on the bus with data off it, and the Ginux track running in parallel without gating. Those are the parts of the plan carrying the most weight.

## Risks and mitigations

Scope creep is the biggest risk; the phase-exit demos are the main defense.

| Risk | Effect | Mitigation |
| --- | --- | --- |
| Scope creep | Phases never finish; nothing demoable | Fixed exit demos; new ideas go to a parking list, not the current phase |
| Smithay learning curve | Phase 4 runs long | Phase 1 proves the effects in a borrowed compositor before the curve is climbed; then start from `smallvil` and `anvil` |
| Nvidia and Wayland quirks | Flicker, sync bugs in the compositor | Test on the 4090 in Phase 0; pin a known-good driver version |
| Contracts designed too early | Rework across every component | Decision records start "proposed"; contracts freeze only after two phases use them |
| Ginux syscall gaps | Userland won't run on Ginux | Ginux is a separate track; trace diffs make gaps concrete |
| Venus quirks on Nvidia hosts | No GPU acceleration in the VM | Software rendering path (Pixman, lavapipe) stays working; UX work continues on Tumbleweed |
| Bus becomes a bottleneck | Latency in the desktop | Control on the bus, data off it; QoS from day one; benchmark in Phase 3 |
| Security model leaks via Linux apps | False sense of isolation | Labeled developer domains; threat notes at contract changes |
| Motivation dips | Project stalls | Alternate kernel and UX work; keep each demo small enough for a weekend |
| One maintainer across four contracts | A stall anywhere stops everything, and no second reader sees the security-relevant design | Threat note at every contract change; decision records kept current enough for someone else to pick up |
| Estimate drift | Phases quietly become quarters and the demos that defend scope fall months apart | Re-estimate at each phase boundary; no stretch over two weeks without something recordable |

## Open questions

Each question is tied to the phase that needs its answer.

- [ ] Bus IDL: Cap'n Proto, or a custom IDL? (Phase 3)
- [ ] Bus topology: one broker, or peer-to-peer with a broker for discovery and logging? (Phase 3)
- [ ] Verb arguments: how the launcher collects many typed parameters without becoming a form. (Phase 5)
- [ ] App image format: OCI layers, or a Haiku-style single mountable image? (Phase 6)
- [ ] Flatpak permissions: which have no honest adapter equivalent, and what happens to the apps that need them. (Phase 7)
- [ ] Declassification: what the user actually does to mark a tainted value clean, and how that act is shown and recorded. (Phase 10)
- [ ] contextdb boundary: where its schema ends and indexd begins. (Phase 9)
- [ ] Native app format: WebAssembly components now, or after the Interface Kit settles? (Later)
- [ ] Haiku app support: ANSWERED by decision 0008. Source compatibility, not binary, in three stages; Stage A is the messaging core with the BeOS primitives reimplemented in userspace. Still open inside it: whether the kits come from upstream Haiku or the Vitruvian fork. Stage A is blocked on fd passing reaching the bus, which 0001 specifies and the bus lacks.
- [ ] Project name. The repo, the launcher binary and the README all say Aurora now, so this is settled in practice but has never been decided on the record.

## References

**Platform and drivers**

- [Ginux overview (Linuxiac)](https://linuxiac.com/?p=221887) and [It's FOSS](https://itsfoss.com/ginux/)
- [Mesa: Virtio-GPU Venus](https://docs.mesa3d.org/drivers/venus.html)
- [Phoronix: Nova driver at LPC 2026](https://www.phoronix.com/news/Nova-Driver-LPC-2026)
- [Phoronix: FreeBSD port of NVIDIA's open DRM driver](https://www.phoronix.com/news/FreeBSD-Port-Linux-DRM-KO)
- [Ghostty and libghostty](https://github.com/jmr/ghostty)
- [scx\_lavd (sched\_ext)](https://gitea.hillion.co.uk/JakeHillion/scx/src/branch/format-macros/scheds/rust/scx_lavd)

**Rendering**

- Porter and Duff, "Compositing Digital Images", SIGGRAPH 1984
- [Bjørge, "Bandwidth-Efficient Rendering", SIGGRAPH 2015 notes](https://community.arm.com/cfs-file/__key/communityserver-blogs-components-weblogfiles/00-00-00-20-66/siggraph2015_2D00_mmg_2D00_marius_2D00_notes.pdf) (dual-Kawase blur)
- [Levien and Uguray, "GPU-friendly Stroke Expansion", HPG 2024](https://arxiv.org/abs/2405.00127)

**Desktop paradigms**

- Kay and Goldberg, "Personal Dynamic Media", 1977
- Pike, "8½, the Plan 9 Window System", USENIX 1991
- Bederson and Hollan, "Pad++", UIST 1994
- Freeman and Gelernter, "Lifestreams", 1996
- Robertson et al., "The Task Gallery", CHI 2000
- Agarawala and Balakrishnan, "Keepin' It Real" (BumpTop), CHI 2006
- [Arcan: Introducing Pipeworld](https://arcan-fe.com/2021/04/12/introducing-pipeworld)
- [Ink & Switch: Malleable Software](https://inkandswitch.com/malleable-software/)

**Storage, sync and security**

- Giampaolo, *Practical File System Design with the Be File System*, 1999
- Dourish et al., Placeless Documents, Xerox PARC, around 2000
- [Gentle and Kleppmann, Eg-walker, EuroSys 2025](https://martin.kleppmann.com/2025/04/02/eg-walker-collaborative-text.html)
- Watson et al., "Capsicum", USENIX Security 2010
- Moore et al., "Shill: A Secure Shell Scripting Language", OSDI 2014
- Debenedetti et al., "Defeating Prompt Injections by Design" (CaMeL), 2025

**Agents and apps**

- [9to5Mac: Apple working on MCP support in App Intents](https://9to5mac.com/2025/09/22/macos-tahoe-26-1-beta-1-mcp-integration/)
