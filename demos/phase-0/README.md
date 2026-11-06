# Phase 0 exit evidence — nested compositor

Run on 2026-11-06 on openSUSE Tumbleweed-Slowroll, kernel 7.2.5-1-default.

## What ran

`AURORA_TERMINAL=ghostty harness/nested-up`

Smithay was cloned, `smallvil` was built and launched as a window on the host
KDE Plasma Wayland session, and ghostty was started inside it. The terminal was
visible and usable in the nested compositor.

## Verified facts

| Fact | Value | How it was checked |
|---|---|---|
| Smithay revision built | `65aa86fa5236e41775c313408102a5d5c2d1e2ca` (`v0.7.0-513-g65aa86fa`) | `git -C ~/.cache/aurora/smithay rev-parse HEAD` |
| smallvil location | `smithay/smallvil`, not `examples/smallvil` | directory listing of the clone |
| Process running | `~/.cache/aurora/target-nested/debug/smallvil` | `pgrep -a smallvil` |
| GPU nodes held | `/dev/dri/card1`, `/dev/dri/renderD128` | `/proc/<pid>/fd` of the smallvil process |
| GPU and driver | NVIDIA GeForce RTX 4090, driver 580.178.04 | `nvidia-smi` |
| Nested Wayland socket | `wayland-1` created alongside the host's `wayland-0` | `ls $XDG_RUNTIME_DIR/wayland-*` |
| Host session | KDE Plasma Wayland, `kwin_wayland` | `XDG_CURRENT_DESKTOP`, `ps` |

This satisfies the plan's Nvidia check: the nested compositor runs on the 4090
and the driver version is recorded.

Two unknowns in `harness/nested-up` are now settled, and the script has been
updated: `SMITHAY_REV` is pinned to the commit above, and the example path is
`smallvil/`.

## What is NOT here, and why

No screen capture. Two full-screen captures via `spectacle` produced the
smallvil window frame with empty content — the nested compositor's surface does
not appear in the host compositor's screenshot path. The plan asks for a short
screen capture as each phase's exit evidence, so this deliverable is
outstanding, not met.

Options not yet tried:
- A Wayland capture client run against the nested compositor itself
  (`WAYLAND_DISPLAY=wayland-1`). This needs the compositor to implement a
  screencopy protocol; smallvil is minimal and may not.
- An external capture (camera, or a recorder that composites differently).
- Deferring recorded evidence until Phase 4, when the compositor is ours and a
  capture path can be built in deliberately.

The table above is the evidence of record until one of those lands.

## Trusted-path overlay

- `trusted-overlay-compositor-frame.png`: the compositor's own final frame (written by `aurora-compositor --dump-overlay`, not via any client) with an approval overlay up over a client window.
- `trusted-overlay-client-capture.png`: a capture taken over `ext-image-copy-capture-v1` at the same moment. It shows the client window and no overlay and no dimming.

Both come from `controller/demo-trusted.sh` against a headless compositor (1280x800); see `compositor/README.md`, "Trusted path".
