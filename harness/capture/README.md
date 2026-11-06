# aurora-capture

`aurora-capture` asks a Wayland compositor for one frame of an output over
`ext-image-copy-capture-v1` and writes it as an RGBA PNG.

## Why it exists

The project plan needs two things that a host screenshot cannot give:

1. A short screen capture as each phase's exit evidence.
2. A Phase 8 screenshot regression harness. It renders a fixed window set per
   theme and diffs the result against committed references.

Host-compositor screenshots of a nested compositor come out blank. The nested
compositor has to render the pixels and hand them over, so the capture is
requested from the compositor itself over a Wayland protocol.

## Which compositors can be captured

| Compositor | Advertises `ext_image_copy_capture_manager_v1` | Can it deliver a frame? |
|---|---|---|
| anvil (Smithay, this Smithay revision) | yes, v1, with `ext_output_image_capture_source_manager_v1` | no. Its session is stopped right after the constraints are sent, and its frame handler fails with `Unknown`. The client reports this as "stopped the capture session". |
| smallvil (Smithay, this Smithay revision) | no | no. It initialises only the core globals, so it has no capture protocol at all. |
| KWin (host, `kwin_wayland`, Plasma on this machine) | no | no. `--list` shows neither capture global. |

`--list` against the host, verified 2026-11-06:

    ext-image-copy-capture-v1: NOT advertised; this compositor cannot be captured by aurora-capture

Which KWin version this is was not checked. Whether a newer KWin advertises the
protocol is unknown.

## Build

    cargo build --release

Binary: `harness/capture/target/release/aurora-capture`. Dependencies are
`wayland-client` 0.31, `wayland-protocols` 0.32 (with the `staging` feature,
which exposes `ext-image-copy-capture-v1` and `ext-image-capture-source-v1`),
`png`, `memmap2` and `tempfile`.

## Use

    aurora-capture --list                         # what does this compositor advertise?
    aurora-capture -o shot.png                    # capture the first output of $WAYLAND_DISPLAY
    aurora-capture --display wayland-2 -o s.png   # a nested compositor's socket

Options: `-o/--output PATH` (default `capture.png`), `--display NAME` (a socket
name in `$XDG_RUNTIME_DIR`, or an absolute path), `--list`, `-h/--help`.

Exit status: 0 on success, 1 on failure, 2 on bad arguments. A failure names the
protocol and says that the compositor cannot be captured when it does not
advertise the protocol.

## How it works

1. Connect and read the globals. Bind the copy-capture manager, the output
   source manager, `wl_shm` and the first `wl_output`.
2. Create an output capture source and a session. Wait for `done`. The session
   sends `buffer_size` and `shm_format` events first.
3. Choose a format in this order: XRGB8888, ARGB8888, XBGR8888, ABGR8888. Allocate
   an shm pool of `width * 4 * height` bytes, in `$XDG_RUNTIME_DIR`, and create a
   buffer with stride `width * 4`.
4. Create a frame, attach the buffer, damage the whole buffer and call
   `capture`. Wait for `ready`, or for `failed` with a reason.
5. Convert to RGBA and write the PNG. A wl_shm 32-bit format is one little-endian
   word, so XRGB8888 is B,G,R,X in memory. The converter reads the byte offsets
   for each format. X formats are written with alpha 255.

The client refuses a rotated output (a non-`Normal` `transform`) rather than
guess the rotation. It also refuses to continue if the session is stopped
before the frame is taken.

## What is verified

- The crate builds with `cargo build --release` and no warnings.
- `--list` against the host KWin: the output above. Exit 0.
- `--list` and capture against the stock nested anvil (`nested-up --compositor
  anvil`): the protocol is advertised. Capture fails with "stopped the capture
  session right after offering constraints". This is reproducible from Smithay's
  code: `Session` is dropped in `anvil/src/state.rs` `new_session`, and that
  drop stops the session (`src/wayland/image_copy_capture/mod.rs`).
- Pixel conversion and orientation, using a test-only anvil. A copy of
  Smithay's anvil was patched in a scratch directory, outside the cache, to keep
  the session alive and write a known pattern into each frame: R=0x80, G=y,
  B=x, A=255. The client captured 1856x1160 Xrgb8888 and decoded it. All
  2,152,960 pixels matched the pattern exactly. This shows that the channel
  order, row order and 4-byte pixel layout are right. It does not show a real
  compositor render.
- `--list` against the user's running smallvil (`wayland-1`): not advertised.

## What is not verified

- A real, non-blank frame from any compositor. No host capture was taken, and
  none of the three compositors above can deliver one as things stand.
- Stride other than `width * 4`. The client always requests that stride, and
  the pattern test used it, so padded strides are untested.
- Multiple outputs. The client takes the first `wl_output`.
- Cursor capture (`PaintCursors`). The client never requests it.
- dmabuf capture. The client uses shm only.
- Transforms other than `Normal`. They are refused, not handled.
- Whether a future anvil or KWin release delivers frames.
