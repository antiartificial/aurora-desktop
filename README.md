# Aurora

A toy desktop OS built on the argument that the interesting part of a desktop
isn't the kernel — it's userspace: a typed message bus, apps described by
manifests, and isolation that holds by construction.

Every action is a **typed verb** from a manifest. Running one builds a
**domain** — a bubblewrap sandbox with its own subordinate uid, granted exactly
one input file and one output file. The caller's identity is stamped by the bus
from kernel facts, never from what the client claims. The result carries
**provenance** you can check afterwards: which app, which verb, which grants,
which capability handles — as a file attribute and as an event in an
append-only, hash-chained log.

It runs **nested inside another compositor**. It does not boot. See
[Limits](#limits).

📄 **[Illustrated overview with architecture diagrams](https://antiartificial.github.io/aurora-desktop/overview.html)**

## Quickstart

```sh
./aurora-up                            # one terminal: bus, controller, index, supervisor
source /tmp/aurora-up-1000/env         # another terminal
aurora apps                            # what's installed
genimage /tmp/photo.png
aurora open /tmp/photo.png --verb resize@v1 --app org.aurora.lumen --arg width=300
controller provenance /tmp/photo.resize.png --verify   # who made it, and check it
```

Then the desktop itself:

```sh
cd compositor && ./target/release/aurora-compositor --bus-socket /tmp/aurora-up-1000/bus.sock
WAYLAND_DISPLAY=wayland-1 ghostty      # a real terminal, inside Aurora
```

Right-click the desktop for a menu; **Settings** sets scale, background, dock,
font and keyboard. `auroractl status` shows the services. `aurora activity`
replays everything the system did. `aurora query run images-today --watch`
streams new files as they're made.

## What's real

| | |
|---|---|
| **Bus** | typed envelopes, attested identity, capability handles, sealed-memfd segments, tamper-evident log, invariant-checked replay |
| **Domains** | bubblewrap, one subordinate uid each, grants revoked on exit, cgroups |
| **Compositor** | glass (real dual-Kawase), Flip 3D switcher, frame capture, fractional-scale HiDPI, dock, context menu, settings panel |
| **Trusted path** | approval prompts drawn by the compositor and excluded from client captures |
| **Haiku apps** | 97 translation units of Haiku's **real** Interface Kit, compiled unmodified, with keyboard, scrolling, lists and a file browser |
| **Third-party apps** | ImageMagick and ffmpeg as domain apps, output checked by decoders that never ask the tool |
| **Live queries** | an index built from provenance events; a domain sees only its own files |
| **Agents** | MCP tools generated from manifests; destructive calls stop at a person |

Each `demos/*/run.sh` starts a real stack, asserts decoded output, and exits
non-zero on failure. They're the actual regression suite: 112 compositor tests,
86 Interface Kit checks, 85 live-query, 60 supervisor, 57 gateway.

## Limits

Stated here for the same reason they're stated in the code.

- **It doesn't boot.** No DRM/KMS or libinput; Aurora only runs nested. The
  largest gap.
- **One uid owns everything.** Domains, bus, controller and compositor all run
  as you. The security story holds against *confined apps*, not against you.
- **A domain is easier to hijack than an unsandboxed process.** A user
  namespace grants its owner `CAP_SYS_PTRACE` inside it, so a same-uid stranger
  can inject into a sandboxed process but not an ordinary one. Reproducible:
  `demos/ptrace-ancestry/run.sh`.
- **The log is tamper-evident, not tamper-proof.** The hash is unkeyed — whoever
  can write the directory can rewrite history and its anchor together.
- **Dock labels are invisible on a light background** (1.13:1 contrast, WCAG
  wants 4.5:1). Fix specified in DR 0010, in progress.
- No XWayland, no IME, no crash survival, no app store.

## Layout

```
bus/          busd, capabilities, segments, hash-chained log, replay checker
controller/   verb invocation, grants, approvals, provenance
compositor/   Wayland compositor, glass, dock, menu, trusted path   (Rust)
kits/         BeOS primitives + the app_server driving Haiku's real kit  (C/C++)
shell/        launcher, powerbox picker, activity and query views
adapter/linux bubblewrap domains, subordinate uids, cgroups, snapshots
index/        indexd and saved live queries
gateway/      MCP tools from manifests, effect-class policy
supervisor/   restart policy, bus-level health checks
apps/         lumen, and the ImageMagick and ffmpeg wrappers
contracts/    decision records (10) and the theme contract
```

## Reading the reasoning

- [Illustrated overview](https://antiartificial.github.io/aurora-desktop/overview.html) — architecture diagrams and design notes
- [`contracts/decisions/`](contracts/decisions/) — ten records, each saying what
  it left undecided
- [`docs/visual-system.md`](docs/visual-system.md) — the glass design system,
  ~90 primary sources
- [`docs/toy-desktop-os-plan.md`](docs/toy-desktop-os-plan.md) — the original plan
- [`docs/README-detailed.md`](docs/README-detailed.md) — longer quickstart,
  toolchain and hardware notes

## Building

Go workspace (10 modules), Rust for the compositor, CMake + Ninja for the kits.
`./aurora-up` builds everything. The Haiku kits compile Haiku's source **in
place** — nothing is vendored — so `kits/CMakeLists.txt` wants `HAIKU_SRC`
pointing at a Haiku or [VitruvianOS](https://github.com/VitruvianOS/Vitruvian)
checkout. Without it the rest still builds and the Haiku targets are skipped.

Needs: Linux, `bwrap`, cgroup v2, an entry in `/etc/subuid`. Tested on openSUSE
with an NVIDIA GPU under KWin.

## Status

A research prototype that demonstrates mechanisms and measures their limits.
It is not a security boundary against a same-user attacker, and it does not
boot. Both are on purpose, for now.
