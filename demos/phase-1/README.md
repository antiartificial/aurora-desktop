# Phase 1 evidence: glass and a Flip 3D-style switcher

Rendered by `compositor/` (`--headless=1280x800`) and captured over `ext-image-copy-capture-v1` with `harness/capture`. Every PNG is a capture, not a host screenshot. Regenerate with:

    cd compositor && cargo build --release --examples
    AURORA_EVIDENCE_DIR=$PWD/../demos/phase-1 cargo test --release --test evidence

The scene (`demo_scene` in `compositor/tests/common/mod.rs`): five clients from `examples/testclient.rs`. At the back, three opaque windows with hard edges: a 40 px checkerboard, 50 px colour bars, and a black/white split. In front, two translucent ARGB "glass" windows (14% alpha, rounded corners). The translucent windows are the only place glass is visible; an opaque window covers its own glass layer.

| File | What it shows |
|---|---|
| `01-glass-off.png` | The scene with `glass.enabled = false`. The translucent windows are flat veils: the checkerboard and bars show through sharp. |
| `02-glass-on.png` | The same scene, glass on. What is behind each translucent window is blurred (dual-Kawase, 3 passes, radius 2), tinted and clipped to rounded corners, with a light edge along the inside of the border. Checker squares and bar edges turn into soft gradients under the glass and stay sharp outside it. Toggled by editing the theme file while the compositor ran. |
| `03-switcher-flip.png` | Alt+Tab, `layout = "flip"`: five live window textures as a receding, rotated stack (42 degrees, perspective). The selected card (the previously used window) is in front with the accent border. Backdrop: the blurred, dimmed desktop. |
| `04-switcher-grid.png` | The same switcher after editing `layout = "grid"` in the theme file with the switcher open (hot reload). Five cards in a 3 + 2 grid. |
| `05-switcher-grid-next.png` | After one Right-arrow key: the accent border moved to the next card. |
| `06-edge-glass-on.png`, `07-edge-glass-off.png` | The blur-proof scene: a window that is black on the left half and white on the right (edge at x = 300), and a glass window over it. Tint and edge highlight are set to 0 here, so the only thing that can change a pixel across the edge is the blur. |
| `edge-profile.txt` | The pixel values for that scene, below. |

## Proof that it is a blur and not a tint

Red channel along row y = 350 (inside the glass window), x = 260 to 339, across the black/white edge at x = 300:

    glass OFF: 13 x40 | 255 x40                      (a step: 13 to 255 between two adjacent pixels)
    glass ON:  13 13 13 13 13 13 13 13 13 13 14 14 15 15 16 17 18 20 21 22 25 27 31 34 38 42 46 52 57 62
               69 75 81 88 95 101 109 115 123 131 137 145 153 159 167 173 180 187 193 199 206 211 216 222
               226 230 234 237 241 243 246 247 248 250 251 252 253 253 254 254 255 255 255 255 255 255 ...

A flat tint would shift both sides by a constant and keep the step. Here the far ends are unchanged (13 and 255), the transition is monotone and spans about 50 pixels (3 passes at offset 2 reach roughly 2 x 2^3 x 2 px either side), and the largest jump between neighbouring pixels is 8 levels. `tests/glass.rs::blur_is_a_gradient_across_a_high_contrast_edge_not_a_step` asserts this: at least 10 intermediate values, no neighbouring jump of 40 or more, monotone, ends still dark and light, and the glass-off control is a step.

## What is approximate or missing

- Blur and blend run in sRGB-encoded values, not linear light.
- The cards of translucent windows in the switcher show no glass behind them (a card is the window's own surfaces), so the selected glass card in `03` and `05` looks hollow.
- No animation, titles or icons on cards, and no click handling in the switcher.
- Measured only on this host's headless EGL (no timing was taken; "holds frame rate on the 4090" is not claimed).
