# Aurora

## Quickstart

From a clean checkout to driving the system by hand. Needs `go`, `cargo`, `bwrap`, `socat` and `python3`; `cmake` and `ninja` are only for the Haiku app (left out if missing). No root and no installs.

    ./aurora-up                  # builds everything (first time: minutes), starts the stack, stays in the foreground

It prints what is running, where each socket is, and the commands to try. Leave it running; Ctrl-C stops all of it and removes its domains, sockets and temp files. In a second terminal:

    source /tmp/aurora-up-$(id -u)/env       # PATH and socket paths for this stack (note: it also sets XDG_RUNTIME_DIR)
    aurora apps                              # apps and verbs come from manifests
    aurora verbs --accepts image/png
    genimage /tmp/photo.png                  # a 1200x800 test image
    aurora open /tmp/photo.png --verb resize@v1 --arg width=300     # Lumen, in its own domain with its own uid
    controller provenance /tmp/photo.resize.png --verify            # grants, cap ids, attested domain; checked with the daemon

Approvals and the compositor (headless: there is no window, and no keyboard, so keys are typed into a test socket):

    key() { echo "key $1" | socat - UNIX-CONNECT:$AURORA_INPUT_SOCK; }
    controller invoke org.aurora.demo delete@v1 --input /tmp/photo.png --output /tmp/gone.png &   # destructive: blocks
    sleep 2; "$AURORA_CAPTURE" --display "$AURORA_WAYLAND" -o /tmp/during.png   # a capture: no prompt in it
    key a                                    # the compositor drew the prompt and reads the key; 'd' denies
    echo "down alt" | socat - UNIX-CONNECT:$AURORA_INPUT_SOCK; key Tab          # the Flip 3D switcher
    "$AURORA_CAPTURE" --display "$AURORA_WAYLAND" -o /tmp/switcher.png
    key Escape; echo "up alt" | socat - UNIX-CONNECT:$AURORA_INPUT_SOCK         # close it

The Haiku app: `aurora-up` starts `haiku_ledger`, a BApplication/BHandler serving verbs on the bus.

    bus send haiku-ledger add@v1 '{"label":"coffee","cents":350}'
    bus send haiku-ledger list@v1 '{}'
    bus inspect; bus log | tail; bus replay --check "${XDG_RUNTIME_DIR%/xdg}/buslog"

The scripted version of all this, with every step asserted, is `demos/full-stack/run.sh` (about 20 seconds, exits non-zero on any failure; it saves captures into `demos/full-stack/`). `aurora-up -h` lists the flags (`--no-compositor`, `--no-haiku`, `--no-fixtures`, `--require-isolation`, `--no-test-input`, ...).

What each piece is for:

- `busd`: the message bus. Typed envelopes, identity from the kernel (uid, cgroup, namespaces), minted capability handles, a log that can be replayed and checked.
- `bus`: command-line client for the bus (send, subscribe, inspect, log, replay).
- `domain`: creates bwrap domains, each with its own subuid, grants and cgroup. A library and CLI; it is not a daemon.
- `controllerd`: loads app manifests, registers domains with the bus, runs verbs in domains granted exactly their input and output, writes provenance, gates destructive verbs on approval.
- `controller`: its command-line client (invoke, decide, provenance --verify).
- `aurora`: the launcher a person drives: open, chain, verbs, apps, watch, and `--pick` (the powerbox file picker, which needs a terminal).
- `aurora-compositor`: a Wayland compositor: real screen capture, glass, the Flip 3D switcher, and the approval overlay that clients cannot see. It runs windowed (winit) or `--headless`; the test suites and `aurora-up` use headless because it needs no screen.
- `lumen`: the demo image app (resize, export, histogram), run inside a domain by the controller.
- `haiku_ledger`: a Haiku-style application on the `kits/` BeOS primitives, exposed on the bus through a bridge.

Troubleshooting on this host:

- **winit works; that claim was wrong three times over.** It was blamed on a locked screen, then on a panic, then on a blit. The real cause was that binding an offscreen texture makes Smithay go surfaceless, after which this NVIDIA driver never presents what we draw -- silently, with every call reporting success. It now uses two shared EGL contexts: one that only ever touches the window, one that does the scene, effects and capture. A real `ghostty` runs in it, is visible, and takes typed input. `compositor/README.md` has the bisection evidence. `aurora-up` still uses `--headless`, because the tests do and it needs no screen.
- **`foot` is absent**, so `ghostty` is the terminal (`harness/nested-up` auto-detects it). Not a bug. To run a terminal in the compositor: `WAYLAND_DISPLAY=$XDG_RUNTIME_DIR/$AURORA_WAYLAND ghostty`, with `DISPLAY` unset.
- **`capnp` and `zig` are absent on purpose.** The IDL choice (decision 0001) and libghostty are deferred; nothing here needs them.
- **`domain limit --memory/--cpu` say NOT ENFORCED.** systemd delegates only `pids` to the user manager here; enforcing the rest needs root once.
- **`aurora-up` says a stack is already running**: stop it, or use `--run DIR` for a second one. A stale run dir from a crash is swept on the next start.
- **An approval never appears**: it needs the compositor (default) or `aurora watch` in a terminal (with `--no-compositor`). Unanswered prompts deny after `--approval-timeout`.

## Status

What works, what is partial, what is not built. Evidence is in each component's README and `demos/`; decisions are in `contracts/decisions/`.

**Works** (run end to end by `demos/full-stack/run.sh` unless noted)
- Bus: typed envelopes, per-domain attested identity (uid, cgroup, namespaces), minted capability handles, sealed-memfd segments, a log, invariant-checked replay (0001, 0006; `bus/README.md`). Segment contents are not replayable.
- Domains on Linux without root: bwrap, one subuid each, grants, cgroups, pids limit, btrfs snapshots where available (`adapter/linux/README.md`; snapshots not in the walkthrough).
- Controller: manifests, verbs run in domains granted exactly input and output, provenance with grants, cap ids and approval, `--verify` against the daemon, powerbox tickets (0002, 0007; the powerbox is covered by `shell/demo-powerbox.sh`, not the walkthrough).
- Launcher: apps, verbs by type, open, chain (chain and `--pick` are not in the walkthrough).
- Lumen: resize, export, histogram on real PNG and JPEG.
- Compositor, headless: real `ext-image-copy-capture-v1` frames, glass (a measured blur, not a tint), Flip 3D and grid switchers, a trusted approval overlay that is excluded from captures by draw order (0003).
- Kits: BeOS primitives, a BApplication, and a Haiku app serving a bus verb through a bridge (0008). Haiku's own `Message.cpp` and `Looper.cpp` compile in `kits/haiku-compat/`; that is not part of the stack and not in the walkthrough.

**Partial**
- Approval is a compositor prompt only on the headless backend, with keys injected through a test-only socket. A real keyboard has never answered one. winit was not verified.
- Isolation is a mount namespace and a distinct uid. No Landlock, no seccomp, network grants enforced by name through a proxy (0015; a domain that tunnels over a granted host is not stopped), memory and CPU limits are recorded but not enforced here.
- Compositor: one output, scale 1, shm capture only, no XWayland, no window management beyond move and resize, no animation, no client-to-domain binding.
- Provenance is a record the controller writes about itself; `--verify` proves the daemon issued the handles, not that the app used them.
- Ginux: the harness (`harness/ginux-up`) is written and has never run; decision 0005 changed its goal.
- Phase 0's recorded exit demo is still outstanding (`demos/phase-0/`).

**Not built**: a shell beyond the CLI launcher (dock, notification area, layer-shell UI), a theme engine, the indexer and agent gateway, the kernel adapter on Ginux, app installation and consent, signed manifests, ports as bus endpoints (0008 Stage A), an app_server for Haiku apps, bare metal.

**The security ceiling, plainly.**
- **One uid owns everything.** Domains, the bus, the controller and the compositor all run as the same host user. A same-user host process can `setns` into a domain's namespaces, join its cgroup, take its subuid and speak to the bus as that domain (attack 8g, `bus/README.md`, `adapter/linux/README.md`). Per-domain uids stop the cheap attacks, not that one. Closing it needs a uid the user does not control (root-made namespaces) or a policy denying `setns` to the user's other processes. Nothing here defends a domain against its own owner.
- **A domain process is *easier* to hijack than an unsandboxed one.** Yama (`ptrace_scope=1` here) lets a process be traced only by an ancestor, so a same-uid non-ancestor cannot touch an ordinary host process. It can touch anything inside a domain: a user namespace grants its owner `CAP_SYS_PTRACE` within it, and bwrap always creates one. Demonstrated in `demos/ptrace-ancestry/` (plain process DENIED, both bwrap variants ATTACHED). busd's ancestry check cannot see this -- an injected process descends from the sandbox init exactly as it should, so the attested identity stays intact and only the words on the connection are the attacker's. That makes it a worse version of 8g, which at least needed a `setns`.
- **The powerbox proves key possession, not a human.** A ticket says its holder had the 0600 key; any same-user process that can read the key can mint one, or drive the picker's pty. It is not a trusted picker (0007).
- **0003's "which domain asked" is half met.** The compositor checks that the domain the controller names exists in busd and is attested, and says so on the prompt (VERIFIED or UNVERIFIED). It cannot check that the request came from that domain or that the invoke runs in it; the controller says so and any same-user process on the trusted socket can say the same. The controller likewise authenticates the compositor only by uid and cgroup, so a same-user process that binds the socket first can answer `allow`. The key press is not proven to be a person's, and a look-alike window is not prevented (0003; `compositor/README.md`, "Trusted path").

This is a research prototype that demonstrates mechanisms and measures their limits. It is not a security boundary against a same-user attacker.

## About

Aurora is a toy desktop OS that explores what a desktop could be in 2026, starting from BeOS and Haiku's ideas: one integrated design across the layers people actually touch. Most of the work lives in userspace, on a message bus that apps, the shell and agents share, with apps described by typed-verb manifests, isolated in domains, and drawn by a compositor that owns animation and trusted UI. The first target is openSUSE Tumbleweed on the host GPU, behind a small kernel-adapter interface; a parallel track brings the same userland up on Ginux. Ginux began as an independent Rust kernel but is now a userspace kernel that runs as an ordinary Linux process, providing isolated Linux-compatibility domains; see decision 0005. Running on bare metal, where the kernel-to-pixels framing would hold in full, remains a goal but is deferred.

Aurora is the working name for this repository; the plan still lists the project name as an open question.

The plan lives in `docs/` (`docs/toy-desktop-os-plan.md`) and is exported from a living document. Current status: Phase 0 (Foundations).

## Repo layout

| Folder | Contents | Language |
| --- | --- | --- |
| `contracts/` | IDL for bus messages, app manifest schema, decision records | IDL, generating Go and Rust (Cap'n Proto is the leading candidate; decision 0001 leaves it open until Phase 3) |
| `bus/` | Message bus: durable, typed, replayable log; capability handles | Go |
| `controller/` | Manifest registry and supervisor | Go |
| `compositor/` | Wayland compositor (Smithay-based) | Rust |
| `shell/` | Dock, launcher, notification area as layer-shell clients | Not fixed yet |
| `kits/` | Interface Kit and other toolkits | Rust |
| `apps/` | First-party apps and their manifests | Not fixed yet |
| `adapter/linux/` | Kernel adapter, Linux backend | Not fixed yet |
| `adapter/ginux/` | Kernel adapter, Ginux backend | Not fixed yet |
| `ginux/` | Ginux kernel, as the git submodule `ginux/upstream` (https://github.com/gmkurtzer/ginux.git, pinned at `3f2fe7df`). Ginux is a userspace kernel that runs as an ordinary Linux process (decision 0005). | Rust |
| `harness/` | Phase 0 bash scripts: `toolchain-report`, `nested-up`, `ginux-up` | Bash |
| `aurora-up`, `demos/full-stack/` | The one-command stack launcher, and the scripted full-stack walkthrough with its captures | Bash |
| `demos/` | Phase recordings, kept as exit evidence (tracked on purpose) | Video |
| `docs/` | The plan (`toy-desktop-os-plan.md`) and the phase-sequence diagram (`revised-phase-sequence.png`) | Markdown, PNG |

The plan also names an indexer and an agent gateway, both in Go. They have no folder yet.

Languages, per the plan: Rust for the compositor, Interface Kit and anything on the frame path; Go for the bus, controller, indexer and agent gateway; Zig only through libghostty.

## Toolchain status

The table below is generated by `bash harness/toolchain-report`. The report exits 0 today. It was captured on 2026-11-06 on openSUSE Tumbleweed-Slowroll, kernel 7.2.5-1-default.

| Tool | Needed for | Version | Status |
|---|---|---|---|
| terminal | Phase 0 exit demo: a terminal inside smallvil (nested-up) | ghostty (auto-detected) | present |
| display session | Phase 0 exit demo: a host Wayland or X11 session for smallvil to nest in | Wayland (wayland-0) | present |
| git | Phase 0 repository; nested-up clones Smithay | git version 2.55.0 | present |
| cargo | Phase 0 Rust builds: smallvil (nested-up) and the Ginux workspace (ginux-up) | cargo 1.95.0 (f2d3ce0bd 2026-03-21) | present |
| rustc | Phase 0 Rust compositor and Ginux workspace | rustc 1.95.0 (59807616e 2026-04-14) | present |
| pkg-config | Phase 0 finds system libraries for smithay | 2.5.1 | present |
| Ginux source | Phase 0 Ginux workspace the harness builds (a userspace Cargo workspace, not a crate compiled into GINUX.EFI) | ginux/upstream/Cargo.toml | present |
| /dev/kvm | Later: KVM for QEMU, used only by the deferred bootable-kernel path and eventual bare metal (decision 0005) | present, read/write | present |
| /usr/share/qemu/ovmf-x86_64-4m-code.bin | Later: OVMF UEFI firmware, used only by the deferred bootable-kernel path and eventual bare metal (decision 0005) | readable | present |
| /usr/share/qemu/ovmf-x86_64-4m-vars.bin | Later: OVMF UEFI firmware, used only by the deferred bootable-kernel path and eventual bare metal (decision 0005) | readable | present |
| rust target x86_64-unknown-uefi | Later: builds GINUX.EFI for the deferred bootable-kernel path (tag bootable-kernel-final) and eventual bare metal; not used by the userspace Ginux build (decision 0005) | installed | present |
| qemu-system-x86_64 | Later: QEMU 10+ for the deferred bootable-kernel path and eventual bare metal (decision 0005) | QEMU emulator version 11.1.1 | present |
| go | Phase 0 Go bus and controller | go version go1.27.1 linux/amd64 | present |
| gcc | Phase 0 linker and C build of crate dependencies | gcc (SUSE Linux) 16.2.0 | present |
| /dev/dri/* | Phase 0 nested compositor on the GPU | /dev/dri/card1 /dev/dri/renderD128 | present |
| nvidia-smi | Phase 0 Nvidia check: records the driver version | NVIDIA GeForce RTX 4090, 580.178.04 | present |
| capnp | Cap'n Proto compiler for contracts/; decision 0001 leaves Cap'n Proto vs. a custom IDL open until Phase 3 | not found | missing (needed later) |
| virglrenderer | Later: was for the GPU track (G3, virtio-gpu Venus), which decision 0005 drops for userspace Ginux; kept for bare-metal GPU work | libvirglrenderer.so.1 (library; no pkg-config file) | present |
| zig | Zig, only for the libghostty terminal | not found | missing (needed later) |
| ninja | Build helper for virglrenderer from source | 1.13.2 | present |
| cmake | Build helper for virglrenderer from source | cmake version 4.4.3 | present |

Package names, verified on this host:

- `foot`: openSUSE package `foot`. Not installed, and not needed. ghostty, konsole, gnome-terminal and xterm are installed, and `nested-up` auto-detects them.
- `capnp`: openSUSE package `capnproto`. Not installed, deliberately. Decision 0001 leaves Cap'n Proto vs. a custom IDL open until Phase 3, so installing it now would pre-commit a deferred decision.
- `virglrenderer`: the runtime library is installed as `libvirglrenderer1`. `virglrenderer-devel` is not installed and is not needed.
- `zig`: not installed. It is only needed for libghostty, a "Later" item.

## Hardware notes

Recorded on 2026-11-06.

- GPU: NVIDIA GeForce RTX 4090
- Driver: 580.178.04
- DRI nodes: `/dev/dri/card1` and `/dev/dri/renderD128` present
- KVM: `/dev/kvm` present

## How to run things

- `harness/toolchain-report`: works. Exits 0 today.
- `harness/nested-up`: run successfully on 2026-11-06. Builds and runs Smithay's smallvil nested and launches a terminal inside it. `AURORA_TERMINAL` selects the terminal; `--no-terminal` runs smallvil alone. `SMITHAY_REV` is pinned to `65aa86fa` (v0.7.0-513-g65aa86fa), the revision proven by that run.
- `harness/ginux-up`: written, never run. Builds the Ginux userspace workspace via upstream's `make build`. It stops at step 5 (domain start) with exit 3 because the host-side domain-start invocation is not documented upstream; it names the candidates rather than guessing.

## Phase 0 tasks

- [x] Repo and folder layout: done.
- [x] Toolchains installed and versions recorded: done. `capnp` and `zig` are absent, both deliberately deferred.
- [x] Build and run smallvil nested, launch a terminal: done on 2026-11-06. ghostty ran inside the nested compositor. Evidence in `demos/phase-0/README.md`.
- [ ] Ginux harness: partly done. `harness/ginux-up` is written and has never run. The goal changed under decision 0005, and the exit demo needs restating.
- [x] Decision records: done. 0001 bus envelope, 0002 app manifest, 0003 compositor protocol, 0004 Vitruvian reuse, 0005 Ginux retarget. All are `proposed`.
- [x] Vitruvian reuse decision: done (0004).

The Nvidia check is DONE. smallvil ran holding `/dev/dri/card1` and `/dev/dri/renderD128` on the RTX 4090, driver 580.178.04. See `demos/phase-0/README.md`.

Still outstanding for Phase 0: a recorded screen capture as exit evidence. Host screenshots of the nested compositor come out blank because smallvil implements no capture protocol; `harness/capture` is being built to request frames from the compositor directly.

## Phase 0 exit demo

From the plan: "a terminal typing inside the nested compositor, then `harness/ginux-up` dropping into an Alpine shell on Ginux."

- The first half, a terminal typing inside the nested compositor, is DONE as a live run; a recorded capture of it is not. See `demos/phase-0/README.md`.
- The second half, `harness/ginux-up` dropping into an Alpine shell on Ginux, is VOID under decision 0005. Ginux is now a userspace kernel: there is no GINUX.EFI, and domain images are Rocky OCI images, not Alpine. Its replacement has not been defined. The exit demo must be restated before Phase 0 closes.
