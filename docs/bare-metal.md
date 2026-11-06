# Running Aurora on a bare TTY (`--drm`)

`aurora-compositor --drm` drives a real display through DRM/KMS and reads the
keyboard and mouse through libinput, on a text VT, with no other compositor.
Code: `compositor/src/drm.rs`.

## Status: written and compiled, not yet run on a display

The author could not take the DRM master on the development machine (a KDE
session holds it), so everything below the "Verified" line is untested.

Verified (by running it):

- `cargo build --release` and the full `cargo test --release` suite (130 tests
  from before, plus 2 unit tests in `drm.rs` for the scale rule and the cursor
  bitmap) pass.
- `--drm --headless` is refused before anything starts.
- `--drm` from inside a graphical session fails at once with an actionable
  message and exit status 1 (it never touches the display).

Not verified:

- Any pixel on a connector; the modeset; buffer format and orientation
  (`Transform::Normal` into a dmabuf is assumed to scan out upright).
- The hardware cursor (legacy cursor ioctl, 64x64 ARGB dumb buffer).
- Real keyboard and mouse input, relative-motion handling, tap-to-click.
- VT switching away and back (pause/resume), including that libinput comes back.
- Hotplug of a monitor or the GPU (udev add/change/remove).
- Whether glass effects and captures behave on a KMS-bound renderer.
- The scale chosen from the connector's millimetre size on a real EDID.

## What it does

- Session: libseat (logind or seatd) on `seat0`. It opens the GPU and input
  devices for us, so no root is needed.
- GPU: the seat's primary GPU, or `--drm=/dev/dri/cardN`.
- Output: the first connected connector, at its preferred mode. The `Output`
  carries the connector's physical size in mm; the scale is `--scale F` if given,
  else pixel density / 96 rounded to a quarter, clamped to 1..3 (1.0 when the
  size is unknown). The log line `drm: <name>: WxH@Hz, WxH mm -> scale S` shows
  what it chose. Other connected connectors are logged and left dark: the shell
  assumes one output.
- Frames: a `GbmBufferedSurface` per CRTC, rendered by
  `render::render_and_capture` (same path as headless), flipped on the DRM
  vblank event. One EGL context; the winit two-context workaround does not
  apply (no window surface).
- Input: libinput. Ctrl-Alt-F1..F12 switch VT. Ctrl-Alt-Backspace quits.
- `--quit-after SECS`: quit by itself, on any backend. Use it for the first run.

## Manual test recipe

You need a second way in: another machine with ssh, or at least know Ctrl-Alt-F2
returns to your desktop. Save your work first. Do **not** stop your display
manager for the first attempts; a free VT is enough.

Prerequisites (once). libseat must find a seat backend. On this dev machine the
default probe failed (`Function not implemented`) inside the KDE session, and
no `seatd` is installed. On a TTY login, logind usually works as is. If it says
the session cannot be opened:

    sudo pacman -S seatd        # or your distro's package
    sudo systemctl enable --now seatd
    sudo usermod -aG seat $USER # log out and in again
    export LIBSEAT_BACKEND=seatd

Build:

    cd compositor && cargo build --release

Test 1, free VT, with a timeout:

1. Press Ctrl-Alt-F3. Log in as yourself on the text console.
2. `cd ~/projects/aurora/compositor`
3. `LIBSEAT_BACKEND=seatd ./target/release/aurora-compositor --drm --quit-after 60 2>aurora.log`
   (drop `LIBSEAT_BACKEND` if logind works for you).
4. Expect: the screen goes black for a moment, then the Aurora desktop (dock,
   background) at the display's native mode, and an arrow cursor that follows
   the mouse. It quits by itself after 60 s and the text console comes back.
5. Ctrl-Alt-F2 should return to your desktop at any time (or F1, whichever VT
   it lives on: `echo $XDG_VTNR` there tells you).

Test 2, VT switch: while Aurora runs, press Ctrl-Alt-F4 (a text login), then
Ctrl-Alt-F3. Expect the screen to go back to text, then Aurora to redraw with
a working mouse and keyboard. `aurora.log` should show `session paused` then
`session resumed`.

Test 3, clients: from an ssh session, `WAYLAND_DISPLAY=wayland-N <client>` with
`N` from the `socket wayland-N` line (`grep socket aurora.log`), or start
`harness/nested-up`-style stacks as you would for winit.

Test 4, hotplug: unplug and replug the monitor. Expect `output ... unplugged`
and, on replug, a new output.

Getting back: Ctrl-Alt-Backspace quits Aurora and restores the console. If the
keyboard is dead, wait for `--quit-after`, switch VT with Ctrl-Alt-Fn (handled
by the kernel only when Aurora is not holding the VT in graphics mode, so this
may not work), or ssh in and `kill <pid>` (find it with `ps -o pid= -C aurora-compositor`).
The seat is released when the process exits.

## Failure messages you may see

All start with `aurora-compositor: --drm:` and exit with status 1:

- cannot open a seat session: no seat manager reachable, or another session
  already controls the seat (you are inside a graphical session).
- the seat session is not active: you are not on the foreground VT.
- no GPU belongs to seat0: no DRM driver; try `--drm=/dev/dri/card0`.
- the session refused to open the GPU: something else is DRM master.
- no connector is connected with a usable mode.
