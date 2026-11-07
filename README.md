<img src="branding/png/128.png" alt="" width="96" align="right">

# Aurora

**A desktop where every action is a typed verb, every program runs in a sandbox
built for that one action, and every file remembers who made it.**

It runs nested inside another compositor. It does not boot. See
[Limits](#limits).

📄 **[Illustrated overview](https://antiartificial.github.io/aurora-desktop/)** ·
🗺 **[Roadmap](ROADMAP.md)** ·
🎨 **[Atlas design spec](https://antiartificial.github.io/aurora-desktop/atlas-design.html)** ·
⚡ **[Performance architecture](https://antiartificial.github.io/aurora-desktop/performance-architecture.html)**

---

## What this does for you

Most desktops answer "can this program open that file?" with a yes or no about
*you*. Aurora answers it about *this one action*.

**Open a photo in an editor and the editor gets the photo.** Not your home
directory, not your SSH keys, not the rest of the folder. A bubblewrap sandbox
with its own subordinate uid, granted exactly one input file and one output
file, destroyed when the verb ends.

```sh
aurora open /tmp/photo.png --verb resize@v1 --app org.aurora.lumen --arg width=300
```

**Then ask the file who made it, and check the answer.**

```console
$ controller provenance /tmp/photo.resize.png --verify
app     org.aurora.lumen
verb    resize@v1
grants  ro /tmp/photo.png, rw /tmp/photo.resize.png
chain   verified (event 4812, anchor ok)
```

That is not a log line someone wrote. It is a file attribute plus an event in
a hash-chained log, and `--verify` recomputes the chain.

**Hand the same system to an agent and nothing changes.** The MCP tools the
gateway exposes are generated from the same manifests apps use, so an agent
gets exactly the verbs a person gets, with the same grants, the same
provenance, and destructive calls stopping at a human.

**Give a program a file by picking it, not by configuring it.** The powerbox
draws the chooser outside the asking program. The program never sees a path it
was not given, and a confirm is drawn by the compositor itself, so a client can
neither read it nor forge it.

**Ask what the system did, and what it has.**

```sh
aurora activity                        # replay everything the system did
aurora query run images-today --watch  # stream new files as they are made
```

---

## Show, don't tell

Every claim here has a demo that starts a real stack, asserts on decoded
output, and exits non-zero when it is wrong. They are the regression suite.

```sh
make demos          # all 21
demos/full-stack/run.sh
```

| run this | and you will see |
|---|---|
| `demos/full-stack/run.sh` | a verb invoked, a domain built and destroyed, the output decoded and its provenance verified |
| `demos/kitchen-sink/run.sh` | Haiku's **unmodified** Interface Kit, compiled in place, driven through real widgets |
| `demos/drag-drop/run.sh` | files and rich text crossing in and out of Aurora, and a sandboxed app's paths **withheld** from the host |
| `demos/ptrace-ancestry/run.sh` | the system demonstrating its own weakness: why a sandboxed process is easier to inject into than an ordinary one |
| `demos/crash-survival/run.sh` | the compositor killed with `-9` and coming back with its scale, background, dock and font |
| `demos/buffer-pool/run.sh` | a client's buffer pool refusing to show a frame the app never drew |
| `demos/resize-storm/run.sh` | a drag costing 2 window reallocations instead of 43 |

---

## The programs

| | |
|---|---|
| **Atlas** | The file manager. Details and icon views, multi-select, rename, trash, type-ahead, sort. Being rebuilt to a written [design spec](docs/atlas-design.md) in reviewable stages. |
| **aurora-chooser** | The powerbox picker: a graphical file chooser drawn outside the program asking for the file, sharing Atlas's browsing model. |
| **Lumen** | The image app, and the worked example of a manifest app: verbs in, verbs out, provenance on everything. |
| **The Haiku apps** | Haiku's real Interface Kit as Wayland clients: browser, controls, kitchen sink, layout, list, menu, text, ledger. |
| **ImageMagick, ffmpeg** | Third-party binaries as domain apps, their output checked by decoders that never ask the tool what it produced. |
| **The agent gateway** | MCP tools generated from manifests, with effect-class policy. |
| **Deskbar and dock** | Window list with close, bring-to-front and show-desktop; app launching; the capability indicator that lights when something holds a grant. |

## The systems underneath

| | |
|---|---|
| **The bus** | Typed envelopes. Identity is stamped from kernel facts, never from what a client claims. Capability handles, sealed-memfd segments. |
| **Domains** | One bubblewrap sandbox and one subordinate uid per action. Grants revoked on exit. cgroups. |
| **The chronicle** | An append-only, hash-chained log of everything the system did, with invariant-checked replay, feeding both `aurora activity` and the live-query index. *(Unnamed in the code today — see [open question](#open-questions).)* |
| **The powerbox** | Grants created by a human gesture, carried as single-use HMAC tickets, redeemed once. |
| **The trusted path** | Prompts drawn by the compositor, excluded from client captures, with an arming delay so a stray keystroke cannot answer one. |
| **The compositor** | Real dual-Kawase glass, Flip 3D, fractional-scale HiDPI, decorations with edge resize, XWayland, IME, clipboard and drag-and-drop bridged both ways with the host. |
| **The toolkit** | `aurora::ui`: damage-tracked software rendering, columns that drop rather than clip, motion as a table of values. |

---

## Quickstart

```sh
make desktop        # build everything, start the stack, open the desktop in a window
```

`make` on its own lists the rest (`make up`, `make test`, `make demos`, `make
env`, `make icon`). Right-click the desktop for a menu; **Settings** sets
scale, background, dock, font and keyboard. `auroractl status` shows the
services.

---

## Limits

Stated here for the same reason they are stated in the code.

- **It has never booted.** There is a DRM/KMS + libinput backend (`--drm`,
  [`docs/bare-metal.md`](docs/bare-metal.md)), and `--probe-drm` has read this
  machine's connectors for real, but the modeset, scanout, hardware cursor,
  real input, VT switching and hotplug have never run.
- **One uid owns everything.** Domains, bus, controller and compositor all run
  as you. The security story holds against *confined apps*, not against you.
- **A domain is easier to hijack than an unsandboxed process.** A user
  namespace grants its owner `CAP_SYS_PTRACE` inside it, so a same-uid stranger
  can inject into a sandboxed process but not an ordinary one.
  `demos/ptrace-ancestry/run.sh`.
- **The log is tamper-evident, not tamper-proof.** The hash is unkeyed, so
  whoever can write the directory can rewrite history and its anchor together.
- **The compositor survives a crash; its clients do not.** Window layout comes
  back as a placement memory only: the `wl_display` socket dies with the
  compositor, so clients lose their connection and exit.
- **X11 clients are not HiDPI.** They get client scale 1: right-sized at any
  output scale, soft above 1.
- **A window can do anything the Wayland protocol allows.** A dock click
  launches an app in its own domain with `device:wayland` and what its manifest
  declares, but the display grant is wide (decision 0014).
- **A sandboxed app cannot drag a file out.** Mapping its in-domain path back
  to a host path would undo the boundary, so it refuses loudly instead.
- No app store, no generations.

---

## Status

A research prototype that demonstrates mechanisms and measures their limits.
It is not a security boundary against a same-user attacker, and it does not
boot. Both are on purpose, for now.

Current test surface: **388 compositor tests** across 21 binaries, **28 kits
suites** carrying about 1,240 Interface Kit checks, **47 Go packages** across
13 modules, and **21 demos**. `make test` runs the first three; `make demos`
the last.

### Open questions

- **A name for the chronicle.** The append-only provenance log is the system's
  most distinctive piece and has no name in the code. It is referred to above
  as "the chronicle"; nothing is named that yet.

---

## Layout

```
bus/          busd, capabilities, segments, hash-chained log, replay checker
controller/   verb invocation, grants, approvals, provenance
compositor/   Wayland compositor, glass, dock, Deskbar, trusted path   (Rust)
kits/         BeOS primitives, Haiku's real Interface Kit, aurora::ui  (C/C++)
shell/        launcher, powerbox picker, activity and query views
adapter/linux bubblewrap domains, subordinate uids, cgroups, snapshots
index/        indexd and saved live queries
gateway/      MCP tools from manifests, effect-class policy
supervisor/   restart policy, bus-level health checks
apps/         lumen, the Haiku apps, the ImageMagick and ffmpeg wrappers
branding/     the icon, and `make icon` to install it
contracts/    24 decision records and the theme contract
docs/         the design specs, and the site
```

## Reading the reasoning

- [Illustrated overview](https://antiartificial.github.io/aurora-desktop/) — architecture diagrams and design notes
- [`ROADMAP.md`](ROADMAP.md) — what is built, next, and deliberately not being built
- [`docs/atlas-design.md`](docs/atlas-design.md) — the file manager, specified before it is built
- [`docs/performance-architecture.md`](docs/performance-architecture.md) — the frame budget, measured
- [`docs/visual-system.md`](docs/visual-system.md) — the glass design system
- [`contracts/decisions/`](contracts/decisions/) — 24 records, each saying what it left undecided

## Building

Go workspace (13 modules), Rust for the compositor, CMake + Ninja for the kits.
`./aurora-up` builds everything. The Haiku kits compile Haiku's source **in
place** — nothing is vendored — so `kits/CMakeLists.txt` wants `HAIKU_SRC`
pointing at a Haiku or [VitruvianOS](https://github.com/VitruvianOS/Vitruvian)
checkout. Without it the rest still builds and the Haiku targets are skipped.

Needs: Linux, `bwrap`, cgroup v2, an entry in `/etc/subuid`. Tested on openSUSE
with an NVIDIA GPU under KWin.
