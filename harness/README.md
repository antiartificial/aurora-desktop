# harness

Scripts that bring up the Phase 0 demos and report what the host has. They are
bash, need no root, and install nothing. Anything they build or run lives under
`${XDG_CACHE_HOME:-$HOME/.cache}/aurora`.

## Scripts

### `toolchain-report`

Read-only. Prints a markdown table with the columns Tool | Needed for |
Version | Status. That table is the source for the root README version table:
paste it as printed. `--help` says the same.

Exit status is 0 when nothing in the blocking set is missing, and 1 otherwise.

Blocking (exit 1 if any is missing):

- a terminal (or `AURORA_TERMINAL=none`, which means smallvil runs alone)
- a host display session (Wayland or X11)
- `git`, `cargo`, `rustc`, `pkg-config`
- the Ginux source: `ginux/upstream/Cargo.toml`, or `GINUX_SRC` if set

Later (listed, reported, does not change the exit status): these serve the
deferred bootable-kernel path (see "Deferred: the bootable-kernel path" below)
and eventual bare metal, not the userspace Ginux build.

- `qemu-system-x86_64`, with a version floor of 10 or newer. Below 10 its
  status is `FAIL (below version floor)`. The floor is still checked and
  reported, but a failing QEMU does not change the exit status, because QEMU is
  not blocking.
- `/dev/kvm`
- both OVMF files under `/usr/share/qemu`
- the Rust target `x86_64-unknown-uefi`
- `capnp` (decision 0001 leaves Cap'n Proto versus a custom IDL open until
  Phase 3), `virglrenderer`, `zig`, `ninja`, `cmake`

Advisory (reported, does not change the exit status): `go`, `gcc`,
`/dev/dri/*`, `nvidia-smi`.

The terminal row follows `nested-up`: `AURORA_TERMINAL` if set, otherwise the
first installed of foot, ghostty, konsole, gnome-terminal, xterm.

Virglrenderer is found by pkg-config, or by `ldconfig -p` at
`/usr/sbin/ldconfig` (not on a user's PATH). The runtime library is installed
here, but the `-devel` package is not, so pkg-config cannot see it.

### `nested-up`

Goal: Smithay's `smallvil` runs as a window on the current desktop, and a
terminal runs inside it when one is available.

Steps:

1. Preflight: `XDG_RUNTIME_DIR` is set; `git`, `cargo`, `pkg-config` are on
   PATH; a Wayland or X11 host session exists; the terminal is chosen (below).
   Each failure names what is missing.
2. Clone Smithay into the cache if there is no checkout. A directory without
   `.git` (an interrupted clone) is removed and cloned again. Fetch unless
   `AURORA_OFFLINE=1` or `SMITHAY_REV` is a full commit that is already present.
   Check out `SMITHAY_REV`, which is a branch for now (see the TODO in the
   script).
3. `cargo build` smallvil into the cache target dir.
4. Start smallvil. Wait for the new `wayland-N` socket it creates.
5. Start the terminal with `WAYLAND_DISPLAY` set to that socket. With no
   terminal, smallvil runs alone.

Terminal choice:

- `AURORA_TERMINAL=<command>`: use it. If it is not on PATH, the script stops.
- `AURORA_TERMINAL=none`, or `--no-terminal`: run smallvil alone. This is how
  to run the Nvidia check without a terminal.
- unset: use the first of foot, ghostty, konsole, gnome-terminal, xterm that is
  on PATH. If none is found, the script warns and runs smallvil alone.

Stop with Ctrl-C, or close the smallvil window. Both exit 0.

### `ginux-up`

Goal: build the Ginux userspace kernel from its own source tree, with upstream's
Makefile, check host readiness, and start a managed domain.

Ginux is a configurable userspace kernel for isolated Linux compatibility
domains. It runs as an ordinary Linux process. Its source is the
`ginux/upstream` submodule, pinned at `3f2fe7df`. Nothing in `ginux-up` uses
QEMU, KVM, OVMF, an ESP or an Alpine rootfs.

Steps:

1. Source check. `$GINUX_SRC/Cargo.toml` and `$GINUX_SRC/Makefile` must exist.
   `GINUX_SRC` defaults to `ginux/upstream`. If the source is absent, the script
   names the submodule and `git submodule update --init ginux/upstream`, and
   stops.
2. Host tools: `make`, `cargo`, `rustc`. Missing tools are named.
3. `make doctor` in the source tree. This is advisory: its result is printed,
   and the build still runs. `GINUX_SKIP_DOCTOR=1` skips it.
4. `make build` in the source tree. This runs cargo for `x86_64-unknown-linux-gnu`,
   release, for the crates ginux-init, managed-payload, baseline-runner,
   runtime-fixture and process-native-fixture. The script then checks that
   `target/x86_64-unknown-linux-gnu/release/ginux` exists and is executable.
   The build writes to the submodule's own `target/` directory.
5. Start a managed domain. Not implemented. The script stops with exit status
   3 and names the unknown. See "Domain start" below.

Exit status: 0 would mean a domain started, which the script cannot do yet. 3
means the build passed and the domain step stopped. 1 means a failure. 2 means
a bad argument.

Log: `$XDG_CACHE_HOME/aurora/ginux/ginux-up.log` (default `~/.cache/aurora/ginux`),
overwritten on each run. It holds the output of the run, so a run leaves
evidence for `demos/`.

#### Domain start

The upstream forms found so far do not settle how to start a managed domain
from the host:

- `ginux launch <config> <root> [--result-file <path>]` is documented in the
  upstream README under "Configured native launch". It needs chroot and
  credential-change privileges, so it needs root. This harness does not use
  root. It also needs `profile = "balanced"` and `backend = "ptrace-native"`,
  and a host root directory.
- `ginux lifecycle <config>` and `ginux domain <config> <fixture>` are used by
  `scripts/test-domain.sh`, which runs inside the Rocky host VM through
  `with-rocky-host.sh`. The upstream README says that VM runs under QEMU TCG.
  Host-side use of these commands is not documented.

The Makefile targets that exercise domains are `demo-gate-4` and `test-domain`.
Their domain configurations (for example `config/domain-gate4.toml`) name a
Rocky root, and building that root needs Docker.

`ginux-up` does not pick one of these. Once the host invocation is confirmed in
upstream, it is added to the script as step 5.

#### Deferred: the bootable-kernel path

The earlier `ginux-up` built `GINUX.EFI`, booted it under QEMU/KVM with OVMF,
attached an Alpine root, and opened a serial console. That path is no longer
part of Phase 0. Upstream keeps the UEFI kernel at the tag
`bootable-kernel-final` (branch `bootable-kernel`). Bare metal remains a
project goal, and this path returns when that work does. See
`contracts/decisions/0005-ginux-retarget.md`.

## What blocks each script on this host

Verified 2026-11-06 on openSUSE Tumbleweed-Slowroll, kernel 7.2.5-1-default.

| Script | Status today |
|---|---|
| `nested-up` | The terminal is not blocked. foot is the plan's named terminal and is not installed. ghostty, konsole, gnome-terminal and xterm are installed, and `AURORA_TERMINAL` selects one. The nested run is UNTRIED, not blocked. |
| `nested-up` | The host session is present (Wayland, `wayland-0`, with `XDG_RUNTIME_DIR=/run/user/1000`). Smithay is not yet built or tested here. |
| `ginux-up` | The source `ginux/upstream/Cargo.toml` is present, pinned at `3f2fe7df`. `make build` has not been run here, and `make doctor` has not been run here. Neither result is known. Step 5 (domain start) is not implemented. |
| `toolchain-report` | Exit 0 on 2026-11-06: every blocking item is present (ghostty auto-detected; Wayland; Ginux source at `ginux/upstream`). Later rows (QEMU 11.1.1, KVM, OVMF, the UEFI target) are present and do not block. |

`capnp` and `zig` are not installed. Both are listed for later phases, so they
do not block. `ninja` and `cmake` are present and only needed to build
virglrenderer from source.

## Phase 0 demo

The Phase 0 demo that exists as a script is `nested-up`:

1. `harness/nested-up` opens the smallvil window on the host desktop with a
   terminal inside it. foot is the plan's named terminal. It is not installed,
   but ghostty, konsole, gnome-terminal and xterm are, and `AURORA_TERMINAL`
   selects one. Run `harness/nested-up --no-terminal` for the Nvidia check.

Neither step has run successfully on this host yet. The nested run is UNTRIED,
not blocked.

The Ginux track has no Phase 0 exit demo yet. The old demo, "dropping into an
Alpine shell on Ginux", is void under decision 0005, and its replacement is not
defined. It must be restated before Phase 0 closes.

## What is not verified

- Smithay's `smallvil` path and package name. The script looks in
  `smallvil/` and `examples/smallvil/`. Confirm on the first run.
- The `SMITHAY_REV` branch. It is not pinned.
- The system library list checked by `nested-up`. It is advisory.
- `nested-up` has never run. The terminal auto-detect order, the start of the
  terminal under `WAYLAND_DISPLAY`, and the window-close and Ctrl-C exit path
  (both exit 0) are all untested.
- `xterm` is an X11 client. Inside a Wayland-nested smallvil with `DISPLAY`
  unset, it is expected not to connect. Only the auto-detect fallback reaches
  it, and that path is untested.
- `AURORA_OFFLINE=1` and the skip for an already-present pinned commit are
  untested. `cargo` still needs the network for crates, so `AURORA_OFFLINE`
  covers git only.
- Whether `ginux-up` step 4 (`make build`) succeeds on this host. It has not
  been run, and `ginux-up` has not been run at all.
- Whether upstream's `make doctor` passes on this host. Its checks include
  `docker`, `qemu-system-x86_64`, `hdiutil` and a macOS arm64 reference host,
  so it may report problems here. Those problems would not block the build.
- The host invocation that starts a managed domain. Upstream documents
  `ginux launch` (which needs root) and `ginux lifecycle` and `ginux domain`
  (which are used inside a Rocky VM). None is confirmed for a host process
  without root. Until one is, `ginux-up` stops at step 5.
- The Ginux submodule's commit is pinned at `3f2fe7df`, and no pin-bump policy
  exists (decision 0005 leaves it open). Whether the `.gitmodules` entry and the
  submodule addition are committed is for git to confirm.
- Upstream's own test harness runs a Rocky host under QEMU TCG. This is a
  property of upstream's tests, not of `ginux-up`. Whether that counts as
  "deferred QEMU" in the project plan is not yet decided.
- Whether the nested compositor runs on the RTX 4090 with driver 580.178.04.
  The Phase 0 Nvidia check depends on this run.

## Capture (`harness/capture`)

`aurora-capture` is a Wayland client that requests frames over
`ext-image-copy-capture-v1` and writes a PNG. Built because host-compositor
screenshots of a nested compositor come out blank, and because Phase 8 needs a
screenshot regression harness.

`--list` prints a compositor's globals and says whether it can be captured at
all. That is the question to ask first.

**Nothing on this host can currently be captured.** Verified 2026-11-06:

| Compositor | Advertises the protocol | Delivers frames |
|---|---|---|
| KWin (host, `wayland-0`) | no | n/a |
| smallvil (nested) | no | n/a |
| anvil (nested) | yes | **no** — `frame.fail(Unknown)`; upstream comment says "Anvil doesn't implement actual capture" |

The client itself is verified correct. Against a scratch-patched anvil that
wrote a known pattern, it captured 1856x1160 with 0 mismatches across 2,152,960
pixels, confirming channel order, row order and pixel layout. The patch was not
kept; it was a test fixture.

Conclusion: frame capture is a **Phase 4 compositor deliverable**. Our
compositor must implement `ext-image-copy-capture-v1` properly, at which point
this client works unchanged and Phase 8's regression harness has its mechanism.

Not handled: padded strides, multiple outputs, cursor capture, dmabuf, rotated
outputs.

`nested-up` gained `--compositor smallvil|anvil`. Anvil needs
`--no-default-features --features winit,egl`; its defaults (udev, x11,
xwayland, libei) are off.
