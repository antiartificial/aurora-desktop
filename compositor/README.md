# aurora-compositor

The Wayland compositor, Phase 4 first step. A small Smithay compositor whose
point is **frame capture that returns real pixels** over
`ext-image-copy-capture-v1`. It closes the gap that blocked recorded demo
evidence: no compositor on the dev host could be captured (see
`harness/capture/README.md`).

It is not Compositor v0. It is smallvil plus a working capture path.

## What exists

- Core: `wl_compositor`, `wl_subcompositor`, `wl_shm`, `xdg_shell` (move and
  resize grabs, popups), `wl_seat` with keyboard and pointer, `wl_output`,
  `zxdg_output_manager_v1`, `wl_data_device_manager`. Derived from Smithay's
  `smallvil` at the pinned revision.
- `ext_output_image_capture_source_manager_v1`: a client can pick the output
  as a capture source.
- `ext_image_copy_capture_manager_v1`, implemented (`src/capture.rs`):
  - the `Session` is kept alive; it is not dropped after the constraints;
  - constraints: the output's current mode size, shm `Xrgb8888` and `Argb8888`;
    they are updated on resize, and a frame whose session size no longer
    matches is failed with `buffer_constraints`;
  - `frame` queues the request; on the next rendered frame the framebuffer is
    read back (`ExportMem::copy_framebuffer`, GL read-back), written into the
    client's shm buffer (stride and offset honoured), and only then is
    `ready` sent. Any failure sends `failed`, never a blank success;
  - reported transform is `Normal`; damage is the full buffer.
- Two backends, both through the same render and capture path (`src/render.rs`):
  - winit (default): a window on the host session.
  - `--headless[=WxH]` (default 1280x800): no host window. Surfaceless EGL, an
    offscreen texture, a 16 ms timer for frame callbacks.
- Smithay is a git dependency pinned to rev
  `65aa86fa5236e41775c313408102a5d5c2d1e2ca` in `Cargo.toml`, recorded in
  `Cargo.lock`. That is the same commit `harness/nested-up` checks out for
  smallvil and anvil.

## Run

    cargo build --release                      # in compositor/
    harness/nested-up --compositor aurora      # winit window, plus a terminal if found
    harness/nested-up --compositor aurora --headless=1280x800
    compositor/target/release/aurora-compositor [--headless[=WxH]] [-c COMMAND]

The compositor prints `aurora-compositor: socket wayland-N`. Capture with:

    harness/capture/target/release/aurora-capture --display wayland-N -o shot.png

Run a terminal inside it with `WAYLAND_DISPLAY=wayland-N ghostty` (unset
`DISPLAY` so it uses Wayland).

## Known limits of the backends

- **winit blocks when the host does not present its window.** With the host
  screen locked, the second `eglSwapBuffers` never returned, KWin marked the
  window "Not Responding", and every client stalled. That is why `--headless`
  exists. The winit path was therefore **not verified to deliver a capture**.
  Its row order (the winit surface is rendered with `Flipped180`) is the
  untested part.
- Without a window, nothing draws. An empty compositor captures as one flat
  colour (the clear colour, 25,25,25). That is correct, not a failure.

## What from Phase 4 does NOT exist

- No glass or blur: flat rendering only.
- No Alt-Tab or any window switcher; no window management beyond smallvil's
  move and resize grabs.
- No multi-monitor: one output.
- No HiDPI: scale is fixed at 1.
- No XWayland.
- No client-domain binding: clients are not tied to domains or capabilities.
- No trusted UI path (decision 0003 reserves it).
- No dmabuf: no `linux-dmabuf`, so GL clients fall back to shm. Capture is
  shm only.
- No cursor capture (`paint_cursors`), no toplevel capture source, no
  rotated output.
- Capture is served on the next redraw, so a request waits up to one frame.
- No tests beyond the manual capture runs in `demos/phase-0`.
