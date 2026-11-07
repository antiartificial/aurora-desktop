# Aurora visual system

Status: design, not built. Nothing here is implemented except where it says
"today". This document specifies the look Aurora aims at, the colour pipeline
that look requires, and acceptance criteria a test can check on decoded
capture pixels. `contracts/decisions/0010-visual-system.md` records the
decisions and the implementation order; this is the reference.

Read with `compositor/README.md` ("Glass and the switcher", "Shell chrome",
"Known limits of the backends"), `contracts/theme/aurora-glass.toml`, and
`compositor/src/fx/` plus `compositor/src/scale.rs`.

The sensibility is BeOS and Haiku: a desktop that is quiet, legible and fast,
where the chrome is confident enough to be plain. The techniques are modern —
Apple, Microsoft and KDE have each shipped a translucent material and
published enough to learn from, and the research literature answers most of
the hard questions. The combination is the point: Haiku's restraint applied to
effects Haiku never had.

Every citation is in section 12. Where a platform's effect is art direction
rather than physics, this document says so; where a published technique will
not survive this pipeline, it says that too.

---

## 1. Principles

1. **Legibility is not negotiable.** Every piece of text the compositor draws
   has a contrast floor that holds over *any* backdrop, including a white one.
   A material that cannot guarantee that does not carry text.
2. **Physically honest where it is cheap, and honestly labelled where it is
   not.** Light adds in linear light, so the blur runs in linear light
   (section 2). The edge effect is a screen-space displacement and is called
   that, not "refraction" (section 3.4).
3. **One material, two roles.** *Window glass* may be luminous because nothing
   the compositor draws sits on it. *Chrome glass* is clamped because the
   dock, the switcher and the trusted overlay put text on it.
4. **Effects are structural, not decorative.** Blur separates a foreground
   from a background. A shadow says which surface is nearer. An effect that
   carries no information is removed.
5. **The frame budget is a design constraint.** The effects pass has roughly
   2-4 ms at 60 Hz today and every glass surface re-blurs the scene. A feature
   that does not fit is cut, not shipped at 45 Hz.
6. **Numbers in the theme, not in the code.** Anything a designer would change
   lives in `contracts/theme/aurora-glass.toml` and hot-reloads.
7. **Verified by capture.** The project's way of knowing something is true is
   to decode a frame and assert on pixels. Every claim here has a criterion in
   section 10.
8. **Every material has an opaque fallback, and it is a first-class path.**
   Both platforms that shipped a translucent material shipped an escape hatch:
   Microsoft falls Acrylic and Mica back to a solid `FallbackColor` when
   transparency effects are off, on Battery Saver, on low-end hardware, when
   the window deactivates and under High Contrast [MS-ACRYLIC, MS-MICA]; Apple
   ships Reduce Transparency, which "makes Liquid Glass frostier", and since
   iOS 26.1 a user-facing Clear/Tinted switch [WWDC25-219, MR-26.1]. Aurora
   already has `[glass] enabled = false`, and `tests/glass.rs` already asserts
   the frame differs. That stays the fallback.

### What Aurora will not do

A design system without refusals is decoration. These are refused by decision.

- **No blur as wallpaper.** Blur under a surface only. Never a blurred
  full-screen panel that exists to look expensive, and never blur behind body
  text without the luminance ceiling of section 3.3.
- **No animated glass.** The tint, the blur radius and the edge do not breathe
  or pulse. Apple's own accessibility criteria name "animated blur" as a
  motion trigger to disable [APPLE-RM-CRIT]; we simply never have one. The
  background shader is the one animated surface and it moves at 0.035 of real
  time.
- **No parallax, no tilt-reactive specular, no fake fixed light.** Apple's
  Liquid Glass lighting "responds to device motion" [WWDC25-219]; a desktop
  has no accelerometer, and a fixed light direction means every window is lit
  from the same corner forever. The existing top-to-bottom edge falloff says
  the same thing more cheaply.
- **No chromatic aberration.** The published reimplementations all offer it
  [ZENN-GLASS]; Apple documents no dispersion anywhere. At 6 px of
  displacement over 14 px type it reads as a rendering fault.
- **No per-window chrome drawn by the compositor.** No compositor-drawn title
  bars or decorations. Clients own their insides; the compositor owns the
  desktop, the dock, the switcher and the trusted overlay. This is why window
  glass needs no contrast floor.
- **No depth theatre.** Three elevation levels (section 4). No stacked
  translucency more than two deep: glass over glass is expensive and reads as
  mud. Microsoft says the same — "avoid layering multiple acrylic surfaces"
  and "try not to place multiple pieces of acrylic edge-to-edge"
  [MS-ACRYLIC].
- **No motion over 250 ms** on the input path, and no motion at all where the
  user has asked for less (section 5.4).
- **No Karis average, no 13-tap custom downsample.** Those exist to fix
  pulsating artifacts and fireflies in bloom chains [JIMENEZ-2014]. Bjorge's
  own stability tests found the dual filter and Kawase both stable under
  sub-pixel translation, with and without a very bright pixel; only
  `glGenerateMipmap` failed [BJORGE-2015]. We do not have the problem, so we
  do not pay for the fix. Section 9 makes that a criterion rather than a
  belief.
- **No squircles.** Section 3.6 shows the cost and the benefit; the benefit
  loses, except in one place, which is named.
- **No HDR, no wide gamut, for now.** Section 2.6 says what must be true
  first.
- **No shipped APCA implementation.** Section 8.1: the licence and the pending
  patent make it a question for a lawyer, and for our polarity WCAG 2 is the
  more conservative gate anyway.
- **No accessibility claims we cannot measure.** Reduced motion and reduced
  transparency either do a specific thing a test proves, or they do not exist.

---

## 2. The colour pipeline

`compositor/README.md` states the foundation problem plainly: "Blending and
blur run on sRGB-encoded values (not linear light; Oklab is used only for the
theme's colour tokens)... Textures are 8-bit, so the lowest levels band
slightly."

### 2.1 Why a gamma-encoded blur is wrong, with the arithmetic

sRGB is an encoding, not a quantity of light. The transfer functions
(IEC 61966-2-1, as implemented in `fx/theme.rs::oklab_to_srgb`) are:

```
encode(c) = 12.92 c                        c <= 0.0031308
          = 1.055 c^(1/2.4) - 0.055        otherwise
decode(v) = v / 12.92                      v <= 0.04045
          = ((v + 0.055) / 1.055)^2.4      otherwise
```

A blur is a weighted average, and light averages linearly. The correct average
of two pixels is `encode((decode(a) + decode(b)) / 2)`. What `down.frag` and
`up.frag` compute today is `(a + b) / 2` on encoded values, and because
`encode` is strongly convex the result is always too dark:

| a | b | today | correct | error |
|---|---|---|---|---|
| 0 | 255 | 128 | **188** | -60 codes |
| 0 | 128 | 64 | 92 | -28 |
| 32 | 255 | 144 | 189 | -45 |
| 64 | 192 | 128 | 146 | -18 |
| 128 | 255 | 192 | 205 | -13 |
| 96 | 160 | 128 | 133 | -5 |
| 200 | 255 | 228 | 230 | -2 |

Averaging black and white in encoded space gives 128, which is 21.4% of full
luminance where the right answer is 50%. The midtone is **2.34x too dark, a
loss of 1.22 stops** — at exactly the pixel where a bright thing bleeds into a
dark thing, which is what glass is made of.

Three consequences visible on a real screen:

- **Blur darkens and desaturates.** A bright window edge or a light wallpaper
  highlight loses most of its energy as it spreads. The glow that makes glass
  look lit never appears; on a dark theme the blurred region sags toward the
  darker neighbour and glass reads as a grey film rather than a lit pane. The
  2019 KWin blur review thread names the same artifact from the other
  direction: a non-linear blur produces a visible dark band between
  contrasting colours [KWIN-D18377].
- **Translucent windows composite wrong.** Smithay's GLES frame enables
  `GL_BLEND` with `glBlendFunc(GL_ONE, GL_ONE_MINUS_SRC_ALPHA)` —
  premultiplied source-over in the fixed-function blender, on whatever the
  target holds. At the 5% alpha the glass tests use the error is small; at 50%
  it is the whole look. Section 2.2 explains why this half is *not* obviously
  ours to fix.
- **Antialiased text is the wrong weight.** `Canvas::over` in
  `compositor/src/text.rs` computes `px = (c*a + px*(255-a))/255` on 8-bit
  encoded values. For light-on-dark — all of Aurora's chrome — that produces
  less light than the coverage calls for, so strokes render thin. Section 8.3
  explains why the fix needs a counter-adjustment rather than just
  linearising.

### 2.2 Two stages, and why only the first is clearly right

**Stage A — linear-light blur chain. No Smithay change. Do this.**

Keep `s.scene` as it is: `Abgr8888`, sRGB-encoded, written by the same draws as
today. Change only the chain:

- `down[]` and `up[]` hold linear light (format in section 2.3).
- `down.frag` gains a decode. Only the *first* downsample reads encoded data
  (it samples `s.scene`); passes 2..n read `down[k-1]`, already linear.
  `Fx::progs` holds one program per slot, so compile the decoding downsample
  as a sixth slot rather than branching in the shader.
- `up.frag` is unchanged in structure: linear in, linear out.
- `glass.frag` samples `up[0]` (linear), does its tint, luminance ceiling,
  bevel and edge in linear light, then **encodes and dithers on output**,
  because it writes into the encoded `s.scene`.

Cost: three `pow` on 1.25 x N tap decodes, plus three `pow` per glass pixel on
output. At 2.15 Mpx on an RTX 4090 this is under 0.1 ms and below the noise of
the existing 2.3-4.0 ms. Precision improves rather than degrades (2.3).

**Stage B — a linear scene texture. Probably should never be done.**

The obvious next step is to make `s.scene` linear so the fixed-function blender
is right for translucent windows. It is hard here *and* the one compositor
that shipped it reverted it.

Hard: every encoded source drawn into the scene must be decoded on sample. Our
own shaders are easy. Client surfaces are not, because a
`WaylandSurfaceRenderElement` draws through Smithay's built-in `texture.frag`
and `Element::draw` offers no slot for a custom program. The clean fix is a
vendored patch importing 8-bit client buffers as `GL_SRGB8_ALPHA8` so the
texture unit decodes — gated, because the winit direct-fallback path and plain
`render_output` draw client textures straight into the encoded output
framebuffer and would double-decode. The alternative, an extra encoded "client
layer" texture composited through a decoding shader, costs one full-screen
target and one full-screen draw per window, which is worse than the blur chain
it is meant to help.

Reverted: KWin did exactly this and undid it. Plasma 6.0 moved to linear
blending because it "was considered the most 'correct' thing"; Plasma 6.2
switched to a gamma 2.2 encoding instead. The stated reasons were the
fullscreen copy per frame, the fp16 buffer that 8- or 10-bit linear forces
("you get visible banding in the dark areas"), and — decisively — that the
performance cost meant linear blending could only be enabled for HDR or ICC
displays, so "a semi-transparent surface would look a lot more transparent
just because you enabled HDR... causing the very problem we wanted to avoid"
[HUGL-P4, KWIN-6050].

That last point is the real argument and it applies to us. Client alpha is
authored against encoded-space blending, because that is what X11, GTK, Qt,
browsers and CSS all do — CSS Filter Effects is explicit that "filter
functions must operate in the sRGB color space" [CSS-FILTER]. A 50%-alpha
window composited in linear light looks markedly more transparent than its
author intended. Hugl's own footnote is the right guidance: "If you need some
exactly specific blending behavior in your application, it's best if you do it
yourself."

**One more fact, and it is the uncomfortable one.** KWin does not blur in
linear light either, and not by omission: `drm_output.cpp` declares
`constexpr TransferFunction::Type blendingSpace = TransferFunction::gamma22;`,
and the blur shaders carry no colour-management include and no `pow`, unlike
`opengl/base.frag`, which does [KWIN-BLEND-SPACE]. So the Kawase taps, the
saturation matrix and the additive noise all run on gamma-2.2-encoded values.
As far as this research found, **no shipping Wayland compositor blurs in linear
light**, and Aurora would be departing from the field.

The departure is defensible, and the reasons are specific rather than
appeals to correctness:

- KWin's retreat was about the blending space for the *whole frame*, with a
  per-frame fullscreen shadow-buffer copy and an fp16 buffer as the forced
  cost. Neither applies to a blur chain that is already a chain of offscreen
  targets, and section 2.3 removes the fp16 requirement entirely.
- The consistency argument — that a surface must not change appearance because
  a display setting changed — is about *client alpha*, which we are not
  linearising. Nothing authored the blur.
- The artifact is documented from inside KDE. The 2019 review of KWin's blur
  effect records a reviewer arguing that blur should not run on sRGB values and
  that "intermediate results should be in linear colorspace", because a
  non-linear blur produces a visible dark band between contrasting colours
  [KWIN-D18377].
- Hugl's own closing footnote is the licence for this: "**If you need some
  exactly specific blending behavior in your application, it's best if you do
  it yourself.**"

Criterion C1.1 is what makes this falsifiable rather than an opinion: it
asserts a 40-code difference at a black-to-white midpoint, which is either
there or it is not.

**So Aurora splits the two operations deliberately, and says so:**

| operation | space | why |
|---|---|---|
| blur | **linear light** | a blur is an average of light; the arithmetic in 2.1 and the artifact in [KWIN-D18377] |
| glass tint, luminance ceiling, bevel, shadow alpha | **linear light** | all are physical mixes of light, and all are ours alone |
| client surface translucency over the scene | **encoded (gamma ~2.2)** | client alpha is authored for it; see [HUGL-P4] |
| final quantisation | **encoded**, with dither | section 2.5 |

That is a choice, not an accident of which texture format got bound, and this
table is the place it is written down.

### 2.3 Precision: 10-bit with a gamma-2.0 store, not 8-bit and not fp16

Matching 8-bit sRGB's darkest step in *linear* storage needs
`log2(1/decode(1/255)) = log2(1/3.035e-4) = 11.7` bits. 8-bit linear is
hopeless and 10-bit linear is marginal in the darks — which is exactly the
banding Hugl reports [HUGL-P4]. His fix is the one to copy: a gamma encoding
"allocates more numbers to darker parts of the image", so 10 bits suffice.

A blur must average linear values, so the encoding cannot be left in place
during the average. The resolution is to store `sqrt(linear)` — gamma 2.0,
whose decode is one multiply and whose encode is one `sqrt` — and
decode/encode per pass. Granularity, expressed as the number of 8-bit sRGB
output codes one quantisation step of the store is worth (lower is better):

| store | bytes/px | at linear 0.0003 (sRGB 1) | at 0.0103 (sRGB 26) | at 0.216 (sRGB 128) | worst case |
|---|---|---|---|---|---|
| 8-bit sRGB (today) | 4 | 1.00 | 1.00 | 1.00 | 1.00 |
| 8-bit linear | 4 | 13 | 6.4 | 0.02 | 13 |
| 10-bit linear | 4 | 3.2 | 1.6 | 0.00 | 3.2 |
| **10-bit gamma 2.0** | **4** | **0.001** | **0.001** | **0.001** | **0.001** |
| fp16 linear | 8 | <0.001 | <0.001 | <0.001 | <0.001 |

The relative step of a gamma-2.0 store is `2/1023 = 0.20%` uniformly, which is
sub-code everywhere. So: **`Fourcc::Abgr2101010` holding `sqrt(linear)`, at
the same 4 bytes per pixel as today, with better-than-sRGB precision
everywhere and a mathematically correct linear average.** The per-tap cost is
`t*t` (three multiplies) on each tap and one `sqrt(vec3)` on write: 15 extra
multiplies per downsample output pixel, 24 per upsample.

`Offscreen<GlesTexture>` accepts `Abgr2101010` and `Abgr16161616f`, gated on
`Capability::_10Bit`, which Smithay grants when the context reports GLES >= 3.0
(this host, NVIDIA 580, reports 3.2). A GLES2 context must fall back to
`Abgr8888` and **log a warning**, not degrade silently.

Two accepted limits: `RGB10_A2` is unsigned normalised, so it clamps at 1.0 —
no overbright, no bloom headroom, no HDR; and its alpha is two bits, which is
enough because the chain only ever carries the opaque scene (the clear sets
alpha 1.0 and premultiplied source-over keeps it there). Anything needing real
alpha or values above 1.0 in the chain needs fp16.

`Abgr16161616f` is worth having as `[glass] precision = "fp16"` for A/B
comparison and as the eventual HDR path. Its cost, for reference:

| resolution | glass surfaces | 4 B/px chain | 8 B/px (fp16) |
|---|---|---|---|
| 1856x1160 | 1 | 28 MB/frame, 1.7 GB/s | 57 MB, 3.4 GB/s |
| 1856x1160 | 4 | 113 MB/frame, 6.8 GB/s | 226 MB, 13.6 GB/s |
| 3840x2160 | 1 | 109 MB/frame, 6.5 GB/s | 218 MB, 13.1 GB/s |
| 3840x2160 | 4 | 435 MB/frame, 26 GB/s | 871 MB, 52 GB/s |

Per chain, `blur_passes = 3`: writes are 1.6406 x N bytes of target, reads at
least the same again, 26.1 M texture taps at 1856x1160. On a 4090's ~1008 GB/s,
fp16 with four glass surfaces is 1.3% of bandwidth; on a 50 GB/s integrated
GPU it is 27%, and 105% at 4K. Microsoft's own note on its linear fp16
compositing space says the same: fp16 "doubles GPU bandwidth and memory
consumption" [MS-HDR].

**The important number: none of this is bandwidth-bound.** 113 MB per frame at
1008 GB/s is 0.11 ms. The measured effects pass is 2.3-4.0 ms, 20-36x that.
Whatever costs 2-4 ms, it is not memory traffic. Section 11.

### 2.4 Where every value lives

| stage | space | storage | why |
|---|---|---|---|
| theme tokens | **Oklab** | f32 in TOML | perceptually uniform: a hover ramp or gradient interpolated in Oklab avoids the dead grey midpoint of an sRGB lerp, and lightness means lightness |
| token to uniform | **linear light** | f32 | `fx/theme.rs` gains `oklab_to_linear`, which is `oklab_to_srgb` minus the final encode |
| client surfaces | **encoded** | 8-bit shm | what clients give us; no colour management on the wire here |
| background, dock, clock, cards | linear internally, **encoded** on write | `Abgr8888` scene | our shaders and CPU canvases |
| blur chain | **linear**, stored as `sqrt(linear)` | `Abgr2101010` | section 2.3 |
| glass composite | **linear**, encoded + dithered on write | reads `up[0]`, writes scene | section 2.2 |
| scene | **encoded** | `Abgr8888` | unchanged |
| output framebuffer | **encoded** | 8-bit | unchanged; this is what captures read |
| glyph coverage | **linear** blend with a contrast gamma | 8-bit canvas | section 8.3 |

**Nothing about capture changes.** `render_and_capture` still renders the
scene, serves queued captures from the 8-bit encoded framebuffer, then draws
the trusted overlay. Capture exclusion is still by order. Every change here is
inside `fx`, upstream of that blit.

Oklab's job is authoring and interpolation, never blending. No shader does
arithmetic in Oklab: the cube roots are not worth it per pixel and nothing
here needs them.

### 2.5 Dither

`background.frag` already dithers — `col += (hash(gl_FragCoord.xy +
fract(time)) - 0.5) / 255.0` — which is why the background does not band.
`glass.frag` and `card.frag` do not, which is the README's "lowest levels band
slightly". The glass interior is the smoothest gradient on screen.

Why it is needed, with this theme's numbers: the default background runs Oklab
`[0.27,-0.015,-0.085]` to `[0.15,0.025,-0.045]`, i.e. sRGB (10,36,80) to
(15,5,29), over 1160 rows. The red channel moves 5 codes in 1160 rows — a
contour every 232 rows.

**Specified: triangular-PDF noise, +-1 LSB (2 LSB peak to peak), generated as
the sum of two independent uniforms, applied after the encode and immediately
before quantisation.**

```glsl
// AFTER encoding to the output transfer function, BEFORE the write.
// Two hashes, not one: the sum of two uniforms is triangular-PDF.
float t = hash(gl_FragCoord.xy + seed) + hash(gl_FragCoord.xy + seed + 17.0) - 1.0;
col += t / 255.0;
```

Three details that matter and are each sourced:

- **Why triangular and not the uniform noise we have now.** Lipshitz,
  Wannamaker and Vanderkooy recommend "nonsubtractive, triangular-pdf dither
  of 2-LSB peak-to-peak amplitude", because it "renders the first and second
  moments of the error signal constant with respect to the input". Uniform
  dither linearises the mean but leaves the error *variance* signal-dependent,
  which is noise modulation [LWV-1992]. Gjoel's graphics restatement calls
  noise modulation "very important to achieve a banding-free look"
  [GJOEL-BANDING]. Note precisely what is claimed: the mean and the variance
  become input-independent; higher moments do not.
- **Order of operations.** Dither must be added *before* quantisation, in the
  space the value will be quantised in: `trunc(f + rnd)`, not
  `trunc(f) + rnd`. Gjoel is explicit — "dithering after quantisation does not
  remove banding", "convert first, dither afterwards... dither in whatever
  space the color will be quantized in" [GJOEL-BANDING]. So the dither goes in
  `glass.frag` after the sRGB encode, not in the blur chain.
  (Lottes does the opposite, adding grain in linear before the transform and
  scaling it to the local step size [LOTTES-2016]. Both are documented and
  they conflict; we follow Gjoel because our quantisation is in encoded space
  and the amplitude is then a constant.)
- **Boundaries.** A 2 LSB triangular dither misbehaves near 0.0 and 1.0
  because clamping destroys its symmetry; lerp down to a 1 LSB uniform dither
  there [GJOEL-BANDING]. Dither the three channels independently.

The seed may vary per frame — the current background shader already uses
`fract(time)` — on one condition: **it must come from the same clock
`--freeze-time` pins**, so a frozen frame is byte-reproducible (criterion
C5.1). Blue noise is better than hash noise [ULICHNEY-1993, WOLFE-VAC] and
needs a texture upload and another texture slot; deferred.

Dither is not the same thing as the material noise of section 3.5. Different
amplitude, different space, different purpose, and the material noise must
*not* vary with time.

For what it is worth, KWin conflates the two and gets both slightly wrong, in
a way worth not copying. Its blur applies "an additive noise onto the blurred
image", with the correct stated reason — "the noise is useful to mask banding
artifacts, which often happens due to the smooth color transitions in the
blurred image" — but the implementation is a 256x256 grayscale tile whose
texels are `rand() % noiseStrength`, blended with `glBlendFunc(GL_ONE,
GL_ONE)`. That is a **one-sided brightening** noise, uniform on
`[0, n-1]` in 8-bit units: not zero-mean, not triangular. It therefore lifts
the whole blurred image as well as dithering it [KWIN-BLUR-SRC]. A zero-mean
triangular dither costs the same and is strictly better.

### 2.6 HDR and wide gamut: deferred, and the order of prerequisites

Aurora is SDR, sRGB primaries, 8-bit output. The useful finding from the
research is that **linear compositing is not the prerequisite** people assume:
`wp_color_management_v1` never mentions blending, Microsoft composites in
linear fp16 scRGB [MS-HDR], and KWin deliberately does not [HUGL-P4]. Both
ship. It is a design tradeoff, not a correctness gate.

What actually makes HDR expensive, in the order it must be solved:

1. **A well-defined working transfer function and a documented blending
   space.** Section 2.2's table is that, and it is worth having regardless.
2. **>=10-bit intermediates coupled to that space, with correct dither.**
   Sections 2.3 and 2.5. Hugl's rule: choose the blending space and the buffer
   depth together or you get banding [HUGL-P4].
3. **A >8-bit output and capture path.** Today the output framebuffer and the
   `ext-image-copy-capture` constraints (`shm Xrgb8888`/`Argb8888`) are 8-bit.
4. **Display capability discovery.** The kernel places EDID parsing and the
   choice of blending policy in userspace [DRM-KMS]. `wl_output` carries no
   colour information here at all.
5. **`wp_color_management_v1`.** In wayland-protocols staging since 1.41
   (2025-02-17); KWin and Mutter ship it. Only the perceptual render intent is
   mandatory, so a minimal conformant implementation is small [WP-CM].
6. **Tone mapping, last.** There is no standard operator — Microsoft calls it
   "inherently a lossy process... there's no single standard algorithm"
   [MS-HDR], Weston 16 shipped HDR as experimental "due to the lack of tone
   mapping" [WESTON-16], and KWin's answer is a bespoke ICtCp intensity-only
   mapping [HUGL-P5]. Reference white is a convention, not a constant: BT.2408
   puts HDR reference white at 203 cd/m2 as the *mean* of a distribution
   spanning roughly 123-345 [BT2408]. And the platforms disagree on what 1.0
   means — Windows anchors scRGB 1.0 at 80 nits absolute [MS-HDR], Apple
   anchors EDR 1.0 at the current SDR level [APPLE-EDR].

Items 1 and 2 are this document's sections 2.2 and 2.3. They are worth
building as *blur* infrastructure, not HDR infrastructure: banding in a blur
pyramid is an 8-bit precision problem whether or not the display is HDR.
Claiming HDR before items 3-6 would be the kind of claim this project's
decision records exist to prevent.

---

## 3. Glass

### 3.1 Two materials, one recipe

Today every window gets the same glass and the dock reuses it with a different
corner radius. That single material is the source of the legibility hole in
3.3.

| | **window glass** | **chrome glass** |
|---|---|---|
| under | client windows | dock, switcher backdrop, trusted overlay |
| carries compositor text | no | **yes** |
| luminance ceiling | none | **enforced** |
| tint | light, cool, weak | dark, cool, strong |
| bevel displacement | yes | no |
| opt-out | per-window (new) | n/a |

The split is what lets window glass be beautiful. Nothing the compositor draws
sits on it, so it carries no contrast obligation; a client that puts
unreadable text on its own translucent surface has made that choice itself.

Apple's system makes a related distinction for a related reason: small chrome
elements adapt and "flip from light to dark based on the background", while
large surfaces deliberately do not, because "their surface area is too big and
transitions like these would be distracting" [WWDC25-219]. Aurora's version is
per-pixel rather than per-element, which avoids the popping a threshold flip
produces at the cost of never changing polarity.

### 3.2 Blur

**Keep dual-Kawase.** It is the right kernel. Bjorge's own comparison at a
97x97 effective blur puts the dual filter at 49.78 dB PSNR against a Gaussian
reference and Kawase at 50.02 dB, and the speaker notes put the bandwidth at
"7% of the total bandwidth compared to the linear sampling filter, and less
than half compared to the kawase filter" [BJORGE-2015]. Intel's independent
investigation puts Kawase at "1.5x to 3.0x less computation time" than an
optimised separable Gaussian, with the advantage growing for larger kernels
and lower-power GPUs [STRUGAR-2014].

Its artefacts are real but they are not the ones usually claimed. **There is
no ringing**, and there cannot be: every pass is an all-positive-weight
average, so the composed kernel is non-negative and cannot overshoot. (No
source documenting dual-Kawase ringing was found, which is consistent with
that.) The documented failure modes are two, and KWin's source states them
better than any paper — the comment that generates its strength table says
`minOffset` is "the minimum offset value for an iteration before we get
**blocky artifacts because of the downsampling**" and `maxOffset` is "the
maximum offset value for an iteration before we get **diagonal line artifacts
because of the nature of the dual kawase blur algorithm**" [KWIN-BLUR-SRC].
Bjorge adds that "since all the samples are given equal weight, the shape of
the blurred output appears blocky", and picom switched its *shadows* back to a
true Gaussian because "dual kawase is fast but its blur size is coarse grained,
which sometimes makes the shadow ugly" [PICOM-SHADOW] — which is part of why
section 4 draws shadows analytically rather than through this chain.

**KWin's offset bands are the safe operating envelope, and we should stay
inside them.** Its generator carries `{minOffset, maxOffset, expandSize}` per
iteration count [KWIN-BLUR-SRC]:

| downsample iterations | min offset | max offset | repaint expansion |
|---|---|---|---|
| 1 (size / 2) | 1.0 | 2.0 | 10 px |
| 2 (size / 4) | 2.0 | 3.0 | 20 px |
| 3 (size / 8) | 2.0 | 5.0 | 50 px |
| 4 (size / 16) | 3.0 | 8.0 | 150 px |

KWin's shipped default is maximum strength: 4 iterations at offset 8, with a
150 px repaint expansion. Note also a latent bug worth not copying: its
`offset` is a `float` assigned to an `int`, so fractional slider positions
truncate and several are pairwise identical.

**The theme's rule of thumb is wrong by about 2x and should be replaced.**
`aurora-glass.toml` says "Visible blur extent is roughly
`blur_radius * 2^blur_passes` pixels". Simulating the exact chain in
`fx/blur.rs` — the x-marginals of both kernels, with bilinear sampling and the
real resolution change at each level, against a step edge:

| passes | radius | 10-90% edge width (px) | equivalent Gaussian sigma | one-sided support | `radius * 2^passes` |
|---|---|---|---|---|---|
| 2 | 1.0 | 8.0 | 3.1 | 8 | 4.0 |
| 2 | 2.0 | 15.3 | 6.0 | 16 | 8.0 |
| 2 | 2.9 | 21.4 | 8.4 | 22 | 11.6 |
| 3 | 1.0 | 16.3 | 6.4 | 17 | 8.0 |
| 3 | **2.0** | **31.3** | **12.2** | **33** | 16.0 |
| 3 | **2.9** (= 2.0 at scale 1.45) | **43.7** | **17.1** | **45** | 23.2 |
| 3 | 4.0 | 59.4 | 23.2 | 60 | 32.0 |
| 4 | 2.0 | 63.0 | 24.6 | 67 | 32.0 |

The identities, good to +-7% across passes 2-4 and radius 1-4, where `start`
is the number of halvings the chain's input is already below full resolution:

```
sigma_effective   ~=  0.75 * blur_radius * 2^blur_passes * 2^start   full-res device px
10-90% width      ~=  1.9  * blur_radius * 2^blur_passes * 2^start
one-sided support ~=  1.95 * blur_radius * 2^blur_passes * 2^start
```

**No one has published a pass-count-to-sigma derivation, and that is worth
saying.** Intel states its own pass counts and offsets "were chosen
empirically" and that there is no "deterministic way of generating Kawase
kernels to match arbitrary sized Gauss filter" [STRUGAR-2014]; Bjorge chose
his sequence by eye; and every shipping implementation uses a hand-tuned table
that disagrees with the others — picom's 20-row `strength_levels` whose third
field is commented "approximate gauss-blur with at least this radius and
std-deviation" (conflating radius with sigma), KWin's four bands above,
Wayfire's `pow(2, iterations + 1) * offset * degrade` [PICOM-PARAMS,
WAYFIRE-BLUR]. The theory is easy to state — each pass is a non-negative
average, so by the central limit theorem variances approximately add, giving
`sigma^2 ~ c * offset^2 * (4^(n+1) - 1)/3`, hence sigma roughly linear in
offset and roughly `2^n` in passes — but the constant `c` depends on the
specific 5-tap and 8-tap kernels and has to be calibrated numerically. The
three identities above are that calibration, done against this exact code.
They must be re-measured from a capture after the linear-light change
(criterion C1.3), because a linear blur alters the encoded edge profile.

Beware also a plausible-looking citation that does not exist: search engines
surface arXiv:2512.04556 as containing a sentence about there being "no
systematic mapping from a target Gaussian strength to the corresponding Kawase
or Dual Filtering parameters". The paper does not mention Kawase at all. Do
not cite it.

**Capture the backdrop once per frame, downsampled, and share it.** This is
the largest performance lever available and it is evidenced rather than
guessed: layer archaeology against SwiftUI's private Core Animation classes
finds Apple's backdrop layer configured with `setScale(0.25)`
[SHATTERED-GLASS]. Intel's reference timings make the same point from the
other side — on an R9-290X a 127x127 Gaussian at full resolution costs about
3 ms, a 63x63 at half resolution 0.5 ms, and at quarter resolution 0.17 ms
[STRUGAR-2014]. KDE wants the same thing for low-power hardware: an open,
confirmed bug asks for "a less demanding blur implementation… based on e.g.
pre-blurring the desktop wallpaper as a static texture" because on a Raspberry
Pi 5 blur "leads to extreme lag" [KDE-507501].

Specified: one shared prepass per frame producing **two** levels — a
half-resolution and a quarter-resolution copy of the scene, the second derived
from the first. Window glass chains from the half; chrome glass chains from the
quarter, because its job is to suppress the backdrop rather than show it.

| | today | specified |
|---|---|---|
| shared prepass (full -> half -> quarter) | — | 13.5 MB, 3.4 M taps, **once per frame** |
| window glass chain (from half, 2 passes) | 28.3 MB, 26.1 M taps | 6.7 MB, 6.2 M taps, per surface |
| chrome glass chain (from quarter, 2 passes) | 28.3 MB, 26.1 M taps | 1.7 MB, 1.6 M taps, per surface |

(1856x1160, 4 bytes per pixel. Totals, today versus specified: 1 window +
1 chrome, 56.5 -> 21.9 MB, **2.6x**; 3 + 1, 113.0 -> 35.3 MB, **3.2x**;
7 + 1, 226.1 -> 62.2 MB, **3.6x**. The ratio keeps improving because the
prepass is paid once. At 4K every figure scales by 3.85 and the ratios are
identical.)

The per-surface marginal cost falls **4.2x** for a window and **17x** for a
chrome surface, which is the direct answer to the README's "cost grows with
window count".

**Specified radii, chosen to sit inside KWin's documented safe band for two
iterations (offset 2.0 to 3.0):**

| material | chain input | passes | radius | sigma | sigma (logical at 1.45) | margin |
|---|---|---|---|---|---|---|
| window glass | half | 2 | **2.0** | 12.0 device px | 8.3 | 31 px |
| chrome glass | quarter | 2 | **2.0** | 24.0 device px | 16.6 | 62 px |
| (today, for comparison) | full | 3 | 2.9 | 17.4 device px | 12.0 | 45 px |

So window glass gets a *narrower* blur than today and chrome glass a wider
one, which is the right split: 12 logical px of sigma over-blurs behind a
window — the backdrop loses all structure and the glass stops reading as a
window onto something — while chrome wants the backdrop suppressed.

Two consequences of choosing a downsampled prepass, stated so they are not
discovered later. First, **it sets a floor on sigma**: at quarter resolution
the smallest artefact-free setting (2 passes at offset 2.0) is already sigma
24 device pixels, so chrome glass cannot have a narrow blur without moving to
the half-resolution input. Second, `glass.frag` samples a downsampled texture
with bilinear magnification, so backdrop detail finer than 2 (window) or 4
(chrome) device pixels is gone — which a sigma-12 or sigma-24 blur had already
destroyed. There is nothing else lost.

And one rule the chain already follows and must keep following: **never skip
levels on the way back up.** "Never bilinearly upsample mip6 to mip0
resolution directly. Upsample from mip6 to mip5, from mip5 to mip4 and so on"
[JIMENEZ-2014]. `fx/blur.rs` is correct today; the prepass must not become an
excuse to break it.

### 3.3 The luminance ceiling (the "luminosity layer")

**The hole.** The dock draws labels with `text_oklab = [0.97, 0, 0]`
(sRGB 245,245,245, relative luminance 0.913) over
`mix(backdrop, tint, 0.22)` with `tint = [0.80,-0.01,-0.04]` (sRGB
173,191,217). Computed WCAG 2 contrast of the label against the glass:

| backdrop under the dock | glass result (sRGB) | contrast |
|---|---|---|
| black | 38,42,48 | 13.2:1 |
| theme background, bottom | 49,46,71 | 11.9:1 |
| theme background, top | 46,70,110 | 8.7:1 |
| mid grey (128,128,128) | 138,142,147 | **3.0:1** |
| theme accent | 171,122,197 | **3.1:1** |
| white | 237,241,247 | **1.04:1** |

A white window, wallpaper or document behind the dock makes the labels
invisible. "Nobody has a white wallpaper" is not an argument: the dock's items
come from the bus and the backdrop is whatever the user put there.

**The standards concede there is no method here.** The W3C ACT rule "Text has
minimum contrast" states that a text node fails only "if all background pixels
have a low contrast with all foreground pixels", and then says outright:
"There is no clear method for determining legibility when some but not all
pixels have sufficient contrast" [ACT-AFW4F7]. The practitioner guidance is to
design for the worst case [NNG-TEXT-IMAGES]. So Aurora needs its own policy,
and a compositor is in an unusually good position to have one: unlike a web
page, we compute the backdrop ourselves.

**The policy: bound the backdrop's luminance in linear light before tinting,
as a compression rather than a clamp.**

```glsl
// backdrop b is linear light; Ymax is the luminance ceiling
float Yb = dot(b, vec3(0.2126, 0.7152, 0.0722));
float k  = 1.0 / (1.0 + Yb / Ymax);   // asymptotes to Ymax as Yb -> inf
vec3  bc = b * k;                     // hue and chroma kept, luminance bounded
vec3  col = mix(bc, tint_linear, strength);
```

A hard `min(Yb, Ymax)` flattens every bright backdrop to one value and the
glass stops looking like glass. The `Y/(1+Y/Ymax)` roll-off is the standard
Reinhard form: a backdrop at linear 0.02 with `Ymax = 0.16` is scaled by
0.889, so dark backdrops are essentially untouched, and however bright the
backdrop gets the result asymptotes to `Ymax`. Scaling the linear triple
rather than replacing it preserves hue, so chrome still takes on the colour of
what is behind it — which is the whole appeal of the material.

**Specified, derived so the floor holds by construction:**

```
chrome.tint.oklab    = [0.38, -0.005, -0.020]   # linear Y = 0.055
chrome.tint.strength = 0.45                      # mixed in LINEAR light
chrome.luminance_max = 0.16
```

Worst case `Y_glass = Ymax*(1-s) + Y_tint*s = 0.16*0.55 + 0.055*0.45 =
0.1127`, so the floor against text at Oklab L 0.97 is
`(0.913+0.05)/(0.1127+0.05) = 5.9:1` **for every possible backdrop**, with
headroom over 4.5:1 for dither and for a dimmer text token. In practice:

| backdrop | chrome glass (sRGB) | contrast to the label |
|---|---|---|
| theme background, bottom | 41,45,57 | 12.6:1 |
| theme background, top | 40,52,76 | 11.4:1 |
| theme accent | 95,67,110 | 7.7:1 |
| white | 87,90,93 | 6.4:1 |

The general form, for choosing these again later:

```
Y_glass_max    = Ymax * (1 - strength) + Y_tint * strength
contrast_floor = (Y_text + 0.05) / (Y_glass_max + 0.05)
=> for a target floor R:
   Ymax = ((Y_text + 0.05)/R - 0.05 - Y_tint*strength) / (1 - strength)
```

For a 7:1 floor with the same tint and strength, `Ymax = 0.114`. For white text
(Y = 1.0) the simpler table is: ceiling 0.10 gives 7.00:1, 0.15 gives 5.25:1,
0.179 gives 4.59:1, 0.30 gives 3.00:1 — the non-text floor.

**Precedent, stated accurately.** Both platforms suppress backdrop luminance,
and the usual summary of why Microsoft does is wrong. WinUI's Acrylic applies a
luminosity blend that takes hue and saturation from the blurred backdrop and
lightness from a solid colour at `TintLuminosityOpacity`, and the shipping
source's stated motivation is **shadow bleed-through, not text**: a
`ThemeShadow` cast by an Acrylic surface was "visible through it as a dark
blur", so "a Luminosity effect is now used to reduce contrast in the Acrylic
source and minimize the shadow's contribution". The *public* docs attribute
legibility to an exclusion blend, which is the pre-19H1 recipe that the
shipping code no longer applies — `sc_exclusionColor` and `sc_saturation` are
declared in the header and referenced nowhere [MS-ACRYLIC-SRC, MS-ACRYLIC].
The honest framing is that Microsoft's layer makes text contrast depend mostly
on the tint rather than on the backdrop, ships for shadow reasons, and asserts
a legibility benefit it never quantifies.

Apple's HIG says the regular Liquid Glass variant "blurs and adjusts the
luminosity of background content to maintain legibility of text and other
foreground elements", and the closest thing to a documented clamp anywhere is
visionOS glass, which "limits the range of background color information so a
window can continue to provide contrast for app content" [APPLE-MATERIALS,
APPLE-VISIONOS]. No numbers, in either case. Apple's one hard number in this
area is a 35%-opacity dark dimming layer recommended under clear glass over
bright content — and the SwiftUI `Glass.clear` sample uses 30%, so the two
primary sources disagree by five points [APPLE-MATERIALS, APPLE-GLASS-CLEAR].

Aurora differs from WinUI deliberately: Microsoft takes lightness *entirely*
from a solid colour, which is a stronger suppression than a roll-off and is
why Acrylic panels read flatter than their backdrop warrants. Criterion C2.3
exists to stop our version degenerating into theirs by accident. No published
engineering method for a GPU reduction of backdrop luminance feeding a
worst-case contrast *bound* was found, so this policy should be treated as
ours, with the arithmetic above as its whole justification.

**Specified window glass.** No ceiling, light tint, and a much lower strength
than today:

```
window.tint.oklab    = [0.80, -0.010, -0.040]   # token unchanged
window.tint.strength = 0.10                      # was 0.22, in encoded space
window.luminance_max = 0                         # 0 = off
```

0.10 is a starting point, not a derived value. Mixing in linear light toward a
tint of linear luminance 0.513 adds far more light than the same number did in
encoded space, and no single linear strength reproduces today's appearance:
matching it needs 0.055-0.085 over the dark background and 0.18 over mid grey.
Note also that the tint strengths published by every web reimplementation are
tuned for a **non-linear** blur, because CSS filter functions operate in sRGB
by specification [CSS-FILTER] — so none of their constants transfer. The number
must be set by eye once Stage A lands and then locked by a capture test. Say
so in the commit; do not present it as derived.

### 3.4 Bevel, edge and displacement

Today's edge is a highlight band along the inside of the rounded rectangle,
full strength at the top of the window and 45% at the bottom
(`glass.frag`: `lit = mix(1.0, 0.45, px.y/rect.w)`). It reads as a lit bevel
and it is good. Four changes.

**1. Move the falloff into the theme.** The 0.45 is hard-coded.
`edge.falloff = 0.45`.

**2. Add an outer rim darkening, 1 device pixel.** A single dark line just
*outside* the highlight separates the glass from a backdrop of similar
luminance. This is the cheapest legibility win in the document — the
difference between an edge you can find and one you cannot — and it costs one
`smoothstep` per glass pixel. `rim.oklab = [0.10, 0, 0]`,
`rim.strength = 0.35` (window) / `0.45` (chrome).

**3. Build a bevel height field, and take the normal from its gradient.** Not
from the SDF gradient. The SDF gradient is a unit vector pointing out of the
shape everywhere, with no profile; the thing the eye reads as a bevel is a
*height* that rises from the edge to the interior, and its slope is what
drives the displacement. Let `u = clamp(-d / bevel_width, 0, 1)` (0 at the
edge, 1 at `bevel_width` inside) and use a Lame (superellipse) profile:

```glsl
float u  = clamp(-d / bevel_width, 0.0, 1.0);
float h  = pow(1.0 - pow(1.0 - u, bevel_n), 1.0 / bevel_n);   // 0 at edge, 1 inside
float dh = ...;                                                // analytic derivative
float s  = min(dh / bevel_width, bevel_max_slope);             // CLAMPED
vec2  n  = -grad_d * s / sqrt(1.0 + s * s);                    // 2D normal projection
```

`bevel_n = 3.0`, `bevel_width = 10.0` logical px, `bevel_max_slope = 1.74`.

Two pitfalls, both from source comments in published reimplementations and
both worth more than any constant:

- **The slope must be clamped.** Uncapped, the rim normal goes near-tangent
  and a total-internal-reflection term paints a hard black ring around every
  window. `1.74` is about 60 degrees from vertical [SHATTERED-GLASS-NOTES].
- **A `smoothstep` bevel reads as plastic.** The superellipse profile is what
  makes it read as glass. This is the one place a Lame curve earns its keep
  here, and note that it is a 1-D profile in `d`, not a 2-D corner shape, so
  it costs two `pow` rather than a Newton solve (contrast section 3.6).

**4. Displace the backdrop sample along that normal, near the edge only.**

```glsl
float band  = clamp(-d / max(refract_width, 1.0), 0.0, 1.0);
float curve = pow(1.0 - band, 1.6);            // 1 at the edge, 0 inside
vec2  uv    = v_coords + n * curve * refract_strength / texsize;
vec3  b     = decode(texture2D(tex, uv).rgb);
```

`refract_width = 10.0` logical px, `refract_strength = 6.0` logical px of
maximum displacement. The `pow(1 - band, 1.6)` falloff and the
displacement-as-a-fraction-of-edge-width magnitude come from the clearest
published reimplementation of the effect, which reaches the Apple look with
`uRefraction = 0.7` of `uEdge * 0.9` maximum displacement and no Snell's law
anywhere [ZENN-GLASS]. GLSL ES 1.00 has `dFdx` only with
`GL_OES_standard_derivatives`, so use the analytic gradient of `rbox` rather
than derivatives: three lines, and exact.

**Window glass only.** On chrome glass the displacement moves backdrop samples
under glyphs: the luminance ceiling still holds numerically, because it is
computed from the sample actually taken, but the gradient under a glyph gets
steeper and the text starts to shimmer against it. Chrome glass gets a bevel
highlight and no displacement.

**What this is, precisely.** A UV offset into a captured backdrop — the same
trick as a heat-haze shader. It produces what the eye reads as thickness:
straight lines crossing under the edge bend. It is **not** refraction. No
thickness, no index of refraction, no ray, and content outside the sampled
texture does not exist, so the screen edge behaves wrong.

**And Aurora will not attempt real refraction.** The evidence that nobody else
does is now strong enough to state as a finding rather than a guess:

- Apple documents the perceptual goal and never the mechanism. Session 219
  names "Lensing" as "the primary way Liquid Glass visually defines itself",
  says the material "dynamically bends, shapes, and concentrates light in real
  time", and at larger sizes "simulate[s] a thicker, more substantial
  material" — note the verb [WWDC25-219]. The written docs say the material
  "blurs content behind it, **reflects** color and light of surrounding
  content" [APPLE-GLASS-CUSTOM]. The words "specular", "refract" (outside app
  icons) and "thickness" (in this sense) do not occur in the HIG's Materials
  page at all.
- The public API exposes no optics: `UIGlassEffect` has `style`, `tintColor`,
  `isInteractive`; `UIGlassContainerEffect` has `spacing`; SwiftUI adds
  `.tint`, `.interactive()`, `glassEffectID`, `glassEffectUnion`. No IOR, no
  thickness, no light direction [APPLE-UIGLASS, APPLE-GLASSCONTAINER].
- The private filter surface, as reconstructed from the live layer tree, is
  parameterised as *amount and height*, not as optics:
  `CAFilter(type:"glassBackground")` with
  `inputInnerRefractionAmount`/`Height`, `inputOuterRefractionAmount`/`Height`,
  `inputRefractionDistance0`/`1`, `inputRefractionOpacity`, and a five-stop
  blur ramp `inputBlurDistance0..4` / `inputBlurOpacity0..4`, over a
  `CABackdropLayer` at `setScale(0.25)` with `setTracksLuma(true)`
  [SHATTERED-GLASS]. The class and parameter *names* are Apple's; the numeric
  values in that project are the author's reconstruction and the README says
  tuning is ongoing, so which were dumped from a live layer tree and which
  were hand-fitted is unclear. Cite the names, not the numbers.
- Every verifiable reimplementation reaches the look by displacing a captured
  backdrop in screen space [ZENN-GLASS, OVERSHIFTED-LG].
- No disassembly-grounded account has been published, and volumetric
  refraction has never been demonstrated or claimed by anyone.

A quarter-resolution backdrop capture is in any case incompatible with
ray-marching a thickness: you cannot refract detail you did not sample. So
Aurora does a bounded screen-space displacement, calls it that, and spends the
saved budget on the shadow and the contrast guarantee instead.

One place where a compositor beats the web implementations, worth noting
because it is free: the CSS/SVG family bakes the displacement into an 8-bit
`feDisplacementMap` image, which caps displacement at -128..127 px per axis
[KUBE-GLASS]. We compute the normal in the shader from a float SDF and have no
such cap.

### 3.5 Material noise

Microsoft's Acrylic includes a noise layer and it is the detail that stops a
blurred panel looking like a plastic sheet; it also hides residual banding.
The shipping constant is `sc_noiseOpacity = 0.02` — 2% — applied as an opacity
over a *tiled* noise texture with `CanvasEdgeBehavior::Wrap`
[MS-ACRYLIC-SRC].

```
window.noise_amplitude = 0.012      # ~3 codes of 255
chrome.noise_amplitude = 0.008      # ~2 codes
```

Applied in linear light, luminance-only (`col *= 1.0 + n`, not `col += n`) so
it does not tint, and **static in screen space**: generated from
`gl_FragCoord` with no time term. A noise field that changes per frame is a
crawling fizz over the whole surface, which is the single most common way this
effect is got wrong. Criterion C5.1 catches it. Lower amplitudes than
Microsoft's because our blur is narrower and our backdrop therefore has more
of its own structure left.

### 3.6 Corner geometry: circular arcs, and the one place a squircle earns it

`glass.frag` uses Quilez's `sdRoundedBox`, an **exact** signed distance field
with circular corners [IQ-SDF2D]. KWin uses the same function verbatim in
`src/opengl/sdf.glsl` as of Plasma 6.5, with per-corner radii in a `vec4` and
`fwidth` antialiasing [KWIN-SDF]. The alternatives are a superellipse
(`|x|^(2K) + |y|^(2K) = 1`) or a curvature-continuous corner.

Start with what Apple actually ships, because the folklore is wrong in three
ways. It is **not a superellipse**: Figma's investigation found that "for all
choices of n… there is a small but systematic discrepancy when compared with
the real icon shape" [FURSE-2018], and Flutter's Impeller independently
confirms this by looking the exponent up from the size-to-radius ratio, with
`n` ranging from 2.0 to 6.43 [IMPELLER-RSE]. It is **not a quintic**: the
curve extracted from the real `UIBezierPath` with `CGPathApply` is **three
cubic Beziers plus two short line segments per corner** [PAINTCODE-2013]. And
it is **not published**: `CALayerCornerCurve` and its `.circular` and
`.continuous` cases have entirely empty documentation, and the only Apple
sentence anywhere is SwiftUI's `RoundedCornerStyle.continuous` — "Continuous
curvature rounded rect corners."

The one number that matters for us is the corner's reach:
**1.52866483 x r along each edge** [PAINTCODE-2013], independently described
by a WebKit engineer as "about 1.5x of the corner radius"
[FRASER-CSSWG]. The deviation region therefore scales linearly with the
radius, and that is the whole scale argument.

Against adopting it here, in order of weight:

1. **The best empirical evidence says it is barely visible, and it comes from
   the engineer who shipped it.** Figma's first candidate was the genuinely
   curvature-continuous family — zero curvature at the join — and Furse
   reports: "the variation over the full range, from xi = 0 to 1 only makes a
   very subtle difference in the corner shape… **This is a barely noticeable
   effect despite its nice mathematical properties.**" He had already made
   perceptibility a hard requirement — "Imagine we did all this work for
   something that people can barely see — it's unacceptable" — and so
   **abandoned the exactly-continuous family and shipped a spikier curvature
   profile because it produced more visible shape change** [FURSE-2018]. The
   thing people call a squircle is tuned for visible shape variation, not for
   curvature continuity. No perceptual study of G1-versus-G2 corners at UI
   scale appears to exist.
2. **The pro-continuity arguments are about shading, not silhouette — and
   that is the one place they partly apply to us.** Quilez's demonstration is
   a Laplacian visualisation of the SDF: a circular arc has `c''(1) = -2`
   against a straight edge's 0, and "this mismatch in the normal regime is
   what makes the lighting respond non-smoothly" [IQ-ROUNDBOX]. Furse's
   analogy is a MacBook corner under a desk lamp. Both mechanisms need normals
   and a specular highlight, which flat 2D chrome does not have — **but
   section 3.4 gives window glass a bevel with a normal and an edge
   highlight**, so the argument is not entirely inapplicable to us. At
   `corner_radius = 12` logical px the deviation region reaches about 18 px
   along each edge with sub-pixel maximum normal deviation; at icon scale
   (radius 25-40 px on a 120-180 px icon, which is what PaintCode and Apple's
   icon grid are about) it reaches 38-61 px. That is the mechanical reason the
   effect reads on an icon and is marginal on a window corner.
3. **The cheap version breaks our antialiasing.** The p-norm shortcut,
   `pow(pow(|qx|,n) + pow(|qy|,n), 1/n) - r`, is condemned by its own authors:
   `hg_sdf`'s variable-exponent `fGDF` carries the comment "**This is slow and
   does not produce correct distances**", and its stated discipline is "stay
   Lipschitz continuous… don't have any distance gradient larger than 1"
   [HG-SDF]. A non-unit gradient means the coverage value
   `cov = clamp(0.5 - d, 0, 1)` — which is exact today and is why the current
   corners look clean — becomes wrong exactly in the corners. Quilez's fix is
   to divide by the gradient length, at the cost of "four extra" evaluations
   of `f` unless an analytic gradient is available [IQ-DISTANCE].
4. **The correct version costs an order of magnitude.** An exact distance to a
   superellipse has no closed form. Quilez's exact-ellipse article notes the
   analytic root finder "is both expensive and not very stable… the distance
   computation returns wrong values sometimes" and his Newton alternative uses
   5 iterations; a G2 cubic corner needs a **12-iteration binary search**,
   because "we cannot solve general quintic equations in close form with the
   instructions we have in the GPUs today" [IQ-ELLIPSE, IQ-ROUNDBOX]. For
   scale: `sdRoundedBox` is roughly 12-14 full-rate ALU operations plus one
   `sqrt`, no `pow`, no divide, no loop, no branch. The p-norm adds three
   `pow` (six transcendental instructions, a quarter rate on RDNA, so roughly
   24 full-rate-equivalent cycles) plus a divide; with gradient normalisation,
   five `pow`. The 12-iteration version is another order of magnitude
   [RDNA-ARCH].

**Decision: circular arcs in every shader.** Two carve-outs, both named:

- The **dock's app tiles** use `tile * 0.28` radius on a ~36 logical px tile:
  ~10 px of radius on a 36 px square, which is the large-relative-radius
  regime where corner shape shows. That is in the CPU canvas
  (`Canvas::round_rect`), where tiles are cached by content and an iterative
  solve costs nothing per frame. If anyone wants squircles in Aurora, that is
  the one place to try them.
- Section 3.4's **bevel height profile** does use a Lame curve — but as a 1-D
  profile in `d`, not a 2-D corner shape, so it costs two `pow` rather than a
  Newton solve, and it is the place where "a smoothstep bevel reads as plastic"
  actually bites [SHATTERED-GLASS-NOTES].

```
window.corner_radius = 12.0   # logical px, unchanged
chrome.corner_radius = 14.0   # dock, switcher backdrop, overlay
tile.corner_radius   = 0.28 * tile_size
```

Radii are logical and multiplied by the output scale in `render_scene`, which
is already right.

If a corner-shape knob is ever exposed, **match the CSS convention**: CSS
Borders Level 4 is standardising `corner-shape: superellipse(K)` with
`x^(2K) + y^(2K) = 1`, where `round` is `superellipse(1)` and `squircle` is
`superellipse(2)` — i.e. the classical `n = 4`, not `n = 5` [CSS-BORDERS-4].
The structurally right implementation, per Quilez, is to rotate the corner 45
degrees into a uv frame and swap one `sdf_corner_*` function, with
per-shape normalisation constants (circular `sqrt(2)-1`, cubic `2/3`, cosine
`2/pi`) so the apparent corner size stays constant across shapes
[IQ-ROUNDBOX].

One protocol-level lesson from KDE, because it bears directly on how a glass
region may ever be specified: **an integer region cannot express a rounded
corner at a fractional scale.** KDE's "Korners" bug — third-party decoration
themes with rounded corners getting a square blur area on HiDPI Wayland — came
down to `QRegion` being integer-only, and it took a round of work to fix
[KDE-453229]. Aurora's glass rectangle is a rounded rect the compositor
computes itself in device pixels, which sidesteps this entirely. If a client
ever supplies a blur region (section 3.9), the rounded clip must stay ours.

### 3.7 Variable blur: free-form refused, edge-distance LOD accepted

A blur whose radius varies across a surface is the effect most often
implemented wrongly, and the depth-of-field literature documents both failure
modes.

- **Cross-fading a sharp image with a blurred one is not a variable-radius
  blur.** The sharp image's high frequencies survive the fade at reduced
  amplitude, so a high-contrast edge shows as a ghost of itself inside the
  "blurred" region.
- **Varying the tap offset per pixel in one pass is not a convolution.**
  Neighbouring output pixels gather from inconsistent neighbourhoods, so sharp
  content leaks into soft regions. This is the DOF "bleeding" artifact, and
  the shipped workarounds in that literature are layer separation rather than
  a per-pixel radius: Garcia's separable circular DOF computes separate near
  and far layers and composites them, with the near field deliberately using a
  min-CoC buffer "so there is edge bleeding when bluring from the front"
  [GARCIA-2017].
- The circulating web idiom — stacking several `backdrop-filter` layers of
  increasing radius, each masked by a gradient band — is structurally the same
  thing as a pyramid with per-pixel level selection, forced into that shape
  because CSS has no variable radius.

The correct construction is a blur *pyramid* sampled with a continuous
per-pixel level, blending two adjacent levels. This chain already builds one:
`up[0..passes-1]` are exactly those levels, lying around unused after
`glass.frag` reads `up[0]`.

**Free-form progressive blur is refused.** The refusals in section 1 rule out
the places it is used — blurred full-screen panels and fading toolbar
backdrops — so Aurora would be paying for art direction with no meaning behind
it.

**One bounded case is accepted, because the evidence says it is the real
mechanism and it composes with what we have: derive the blur level from the
local UV compression the displacement itself produces.** Where the bevel bends
the sample coordinates, a screen pixel maps to a larger patch of backdrop, so
sampling that patch at a single point aliases. The footprint is computable:

```glsl
// qc = the displaced sample coordinate, in backdrop pixels
float foot = max(max(length(dFdx(qc)), length(dFdy(qc))), 0.25 * px);
float lod  = clamp(log2(foot), 0.0, float(passes - 1));
```

with the floor to keep it from collapsing to a point sample and, critically, a
**cap**: at the rim the compression runs past 20x, and an uncapped level
reaches content the rim never sees, producing a glow of colours from the wrong
side of the window [SHATTERED-GLASS-NOTES]. This is the same relationship
Apple's private filter expresses as `inputBlurDistance0..4` /
`inputBlurOpacity0..4` — a blur ramp indexed by distance, not a free-form
radius field [SHATTERED-GLASS].

Three implementation facts, so this is costed honestly:

- `dFdx`/`dFdy` need `GL_OES_standard_derivatives` in GLSL ES 1.00. The
  analytic alternative is the Jacobian of the displacement, which we have in
  closed form because `n`, `curve` and `refract_strength` are all analytic;
  prefer that, and it is exact.
- Blending two pyramid levels needs **two samplers**, and Smithay's custom
  texture programs bind exactly one `tex`. `GlesFrame::with_context` gives
  raw `&ffi::Gles2`, so binding texture unit 1 and setting a second sampler
  uniform by hand is possible — with Smithay's own warning that the renderer's
  GL state is an implementation detail and anything changed must be restored.
  That is the actual cost of this item, and it is why it is last in the
  sequence in decision 0010.
- The levels are separate textures at different resolutions, not mips of one,
  so `lod` selects between two named samplers rather than feeding
  `textureLod`. Fine: there are at most three levels in play.

### 3.8 Theme shape

`[glass]` becomes two materials with shared defaults. Unknown keys are already
ignored and missing keys already take defaults, so this is backward
compatible: an existing `aurora-glass.toml` keeps working and gets window
glass with the old look.

```toml
[glass]
enabled = true
precision = "rgb10"          # "rgb10" (sqrt-linear store) | "fp16" | "srgb8" (today)
linear_blur = true           # false reproduces today's wrong blur, for A/B
dither = "triangular"        # "triangular" | "uniform" | "off"
# The shared prepass: how many halvings of the scene to produce and keep.
# 2 gives a half- and a quarter-resolution copy; see 3.2.
prepass_levels = 2
blur_passes = 2              # per material chain, on its own input level

[glass.window]               # under client windows; no text obligation
backdrop_level = 1           # chain from the half-resolution copy
blur_radius = 2.0            # inside KWin's documented 2-iteration band (2.0-3.0)
corner_radius = 12.0
tint = { oklab = [0.80, -0.010, -0.040], strength = 0.10 }
luminance_max = 0            # 0 = no ceiling
noise_amplitude = 0.012
bevel_width = 10.0           # logical px
bevel_n = 3.0                # Lame exponent of the height profile
bevel_max_slope = 1.74       # REQUIRED: uncapped, the rim goes black
refract_width = 10.0
refract_strength = 6.0       # logical px of maximum displacement
edge = { oklab = [0.98, 0.0, 0.0], width = 1.5, strength = 0.75, falloff = 0.45 }
rim  = { oklab = [0.10, 0.0, 0.0], width = 1.0, strength = 0.35 }
shadow_contact = { y = 2.0, sigma = 3.0,  spread = 0.0, alpha = 0.40 }
shadow_ambient = { y = 5.0, sigma = 10.0, spread = 0.0, alpha = 0.26 }

[glass.chrome]               # dock, switcher backdrop, trusted overlay
backdrop_level = 2           # chain from the quarter-resolution copy
blur_radius = 2.0            # same offset, 2x the sigma, 4x cheaper
corner_radius = 14.0
tint = { oklab = [0.38, -0.005, -0.020], strength = 0.45 }
luminance_max = 0.16         # enforced; see the derivation in 3.3
noise_amplitude = 0.008
bevel_width = 6.0
refract_strength = 0         # refused on text-bearing glass
edge = { oklab = [0.98, 0.0, 0.0], width = 1.0, strength = 0.50, falloff = 0.45 }
rim  = { oklab = [0.10, 0.0, 0.0], width = 1.0, strength = 0.45 }
shadow_contact = { y = 4.0, sigma = 5.0,  spread = -2.0, alpha = 0.44 }
shadow_ambient = { y = 12.0, sigma = 18.0, spread = 5.0, alpha = 0.30 }

[motion]
enabled = true               # false = no animation at all (KWin's factor-0 tier)
reduced = false              # vestibular tier: see 5.4 for exactly what changes
scale = 1.0                  # duration multiplier, as KWin's AnimationDurationFactor

[font]
size = 14.0                  # the base of the 1.2 modular scale in section 6
gamma = 1.0                  # glyph coverage gamma; 1.0 = physically correct (8.3)
```

`[glass] blur_radius`, `corner_radius`, `tint` and `edge` stay readable at the
top level as the fallback for both materials, so the current file is still
valid.

Three notes on what this costs in Rust, because the theme is not free-form.
`fx::Fx::progs` is `[Option<GlesTexProgram>; 5]` and `uniform_names(i)` is a
fixed match, so the sixth shader slot (the decoding downsample) and every new
uniform above is a code change, not a theme change. The design deliberately
folds the shadow, the ceiling and the bevel into `glass.frag`'s existing slot
to keep that surface small. And `[glass.window]` / `[glass.chrome]` as nested
tables means `Glass` gains a `Material` struct; `render_scene` picks the
material per call site, which is fine for four call sites and would want
rethinking at twenty.

### 3.9 Per-surface opt-out: use `ext-background-effect-v1`

`compositor/README.md` notes that "every window gets glass under its geometry;
there is no per-window opt-out". When one is added, it should be the merged
standard protocol, not a bespoke mechanism.

**`ext-background-effect-v1` is in wayland-protocols staging, merged
2025-05-26, with ACKs from Smithay/COSMIC and KDE, and KWin ships it as of
Plasma 6.7.0** [EXT-BG-EFFECT, EXT-BG-MR, KWIN-BG-EFFECT]. It supersedes
`org_kde_kwin_blur`, which KWin *removed* in the same release [KWIN-DROP-BLUR].
Its semantics are the ones the old protocol left undefined, and each clause is
a correction of a real mistake:

- "The blur region is specified in the surface-local coordinates, and clipped
  by the compositor to the surface size."
- "**The initial value for the blur region is empty.**" In `org_kde_kwin_blur`,
  an empty region meant *the whole surface* — a convention written down only
  inside KWin's effect, never in the XML, which is a large part of why that
  protocol was never standardisable as-is.
- "A **NULL** `wl_region` removes the effect."
- "The blur region is **double-buffered state**, and will be applied on the
  next `wl_surface.commit`." The old protocol self-latched on its own `commit`
  request instead.
- "**The blur algorithm is subject to compositor policies.**" Which is the
  sentence that makes everything in this document legitimate: a client asks for
  a region, not for a look.

Interfaces: `ext_background_effect_manager_v1` (`get_background_effect`, a
`capabilities(flags)` event, a `blur = 1` capability bit) and
`ext_background_effect_surface_v1` (`set_blur_region`). Both version 1, and the
header still says "the protocol described in this file is currently in the
testing phase".

Two Aurora-specific notes. The region is a *request*; the rounded clip and the
material choice stay ours, for the reason in section 3.6 — an integer region
cannot express a rounded corner at a fractional scale, which is exactly how
KDE's "Korners" bug happened [KDE-453229]. And glass should remain *on* by
default for every window rather than opt-in, for the reason KDE gives: "the
blur has to be always on so that opaque surfaces can transition to being
translucent without jarring transitions" [KDE-507904]. Note that KDE also
claims blur "is basically a no-op when the surface is opaque, so there isn't a
performance penalty" — which is not visible in `BlurEffect::shouldBlur()`,
where there is no opacity-based early-out at all. Treat it as a position, not
a verified property, and do not copy the claim.

---

## 4. Elevation and shadow

There is no shadow in the compositor today. Windows sit on the background with
nothing but their own edges, which is why the edge highlight is doing work it
should not have to.

**Three levels, and no more.** Each is a (contact, ambient) shadow pair plus a
glass material. Depth beyond three is theatre, and Material 3 reached the same
conclusion from the other direction: it compressed its elevation ladder from
MD2's 0-24 dp to 0, 1, 3, 6, 8, 12 dp, and its `Surface` API documentation says
"**To prevent shadow creep, only apply shadow elevation when absolutely
necessary**, such as when the surface requires visual separation from a
patterned background" [M3-ELEVATION].

| level | what sits here | material | contact shadow | ambient shadow |
|---|---|---|---|---|
| 0 | desktop background | none | none | none |
| 1 | client windows, dock | window / chrome glass | `y 2, sigma 3, a 0.40` | `y 5, sigma 10, a 0.26` |
| 2 | switcher cards, popups, menus | chrome glass | `y 4, sigma 5, a 0.44` | `y 12, sigma 18, a 0.30` |
| 3 | trusted overlay | chrome glass + scrim | none (it is modal) | `y 0, sigma 24, a 0.45` |

Offsets and sigmas are **logical pixels**, multiplied by the output scale.
`x` is always 0, as on every platform that publishes a shadow spec: Material
says "on the web, shadows are depicted by manipulating the y-axis only"
[M1-ENVIRONMENT], and KDE's (now deleted) HIG said shadows "should be
horizontally-centered, but vertically offset, to provide the illusion that a
light source is over the top of the screen" [KDE-HIG-ELEVATION].

**Two shadows, not one, and the reason is physical rather than stylistic.** A
single soft shadow makes a surface look like it is floating a long way above
the background with no sense of contact; a single tight shadow looks like a
sticker. The pair reads as "resting slightly above". The underlying fact is
contact hardening, stated most cleanly by Fernando: "soft shadows provide
valuable cues about the relationships between objects, **becoming sharper as
objects contact each other and more blurry (softer) the further they are
apart**", with penumbra width
`w = (d_receiver - d_blocker) * w_light / d_blocker` [FERNANDO-2005]. The
machinery in that paper — percentage-closer soft shadows with a shadow map and
a blocker search — is wildly wrong for a compositor; the *observation* is what
we are using.

Material's two-light model is the vendor precedent: "**key lights create
directional shadows, while ambient light creates soft shadows from all angles.
Shadows in the material environment are cast by these two light sources**"
[M1-ENVIRONMENT]. No non-vendor source names the tight-contact-plus-wide-
ambient pair as a perceptual principle, so treat the two-layer model as derived
here rather than cited.

**Where the numbers come from.** Material's implementation is actually *three*
layers, not two, and its table is the best published starting point
[MDC-ELEVATION]. Alphas are umbra 0.20, penumbra 0.14, ambient 0.12, and the
geometry at each elevation `e` reads, structurally: umbra has `y ~= e/2`, a
small blur, and a **negative spread** — pulled inward, which is literally a
contact shadow; penumbra has `y ~= e` and blur `~= 1.5 e`; ambient has a small
`y` and the largest blur, `~= 2 e`, with positive spread. For example at 6 dp:
umbra `0 3px 5px -1px`, penumbra `0 6px 10px 0`, ambient `0 1px 18px 0`.
Also directly relevant: "**Desktop resting elevation is 2dp below the listed
values** to accommodate mouse and non-touch environments" [M1-ENVIRONMENT].

Aurora merges umbra and penumbra into one contact shadow, for two reasons.
CSS-level layering costs linearly — "if we layer 5 shadows, our device has to
do 5x more work" [COMEAU-2021] — and three layers of a
15-instruction evaluation over an inflated footprint is 50% more cost for a
difference that is below 8-bit quantisation at these sizes. Levels 1 and 2 are
derived from Material's 6 dp and 16 dp rows, with CSS blur converted to sigma
(CSS box-shadow blur is conventionally twice the standard deviation) and the
alphas converted to linear light.

**Alphas are linear-light coverage, and that conversion is not optional.** A
shadow composited in encoded space at alpha 0.3 leaves `0.7 v` of the encoded
value, which is `0.7^2.4 = 0.42` of the light — it blocks 58% of the light
while the number says 30%. So a shadow drawn the way this pipeline draws
everything today is roughly twice as dark as its number claims, and the same
alpha applied in linear light will look *lighter*. To port any CSS or Material
shadow alpha into this pipeline:

```
alpha_linear = 1 - (1 - alpha_encoded)^2.4
```

which maps Material's 0.20 / 0.14 / 0.12 to 0.41 / 0.30 / 0.26. The table above
is those values, merged and rounded, and the contact-to-ambient ratio of about
1.5:1 matches Material's 1.67:1. (Android's own light model uses a much more
extreme ratio — `spot_shadow_alpha 0.19` against `ambient_shadow_alpha 0.039`,
roughly 5:1, with an 800 dp area light 500 dp above the screen at the top edge
[AOSP-DIMENS]. Those are physical light intensities for a 3D shadow renderer,
not stacked 2D layers, and the CSS ladder is the one authored for the thing we
are doing.)

**How to draw it: an SDF plus an erf, in the same draw as the glass.**
`glass.frag` already computes the exact signed distance `d` to the window's
rounded rectangle. The blurred rounded rectangle has no closed form — Wallace
states it plainly: "**Unfortunately there's no closed-form solution for a
rounded rectangle drop shadow**" [WALLACE-2015] — so there are two credible
approximations:

- **Wallace's**: exact erf along one axis, four-sample midpoint integration
  along the other, support clamped to +-3 sigma, with an Abramowitz & Stegun
  7.1.27 erf approximation (cubic term dropped; measured maximum error about
  8e-4). Constant time, one pass, no SDF.
- **Levien's**: the rounded-box SDF plus a sigmoid, with the corner radius
  inflated for the blur as `sqrt(r_corner^2 + 1.25 * sigma^2)`, "motivated by
  the rule for the probability distribution of a sum of Gaussians, with the
  constant factor chosen empirically" [LEVIEN-2020].

**Aurora uses Levien's**, because we already have the SDF, it is a single
evaluation with no loop, and Qt shipped the same family in 6.9 with real
numbers on weak hardware (30 dynamic shadows at a steady 60 fps on a MediaTek
MT6762 / PowerVR GE8320) [QT-RECTSHADOW]. Levien's own scoping note is worth
keeping in view: "Evan's existing solution is almost certainly good enough for
most practical uses."

```glsl
// Levien 2020, ported. erf7 approximates erf to ~2e-4 and uses one rsqrt.
float erf7(float x) {
    x *= 1.1283791670955126;                 // 2 / sqrt(pi)
    float xx = x * x;
    x = x + (0.24295 + (0.03395 + 0.0104 * xx) * xx) * (x * xx);
    return x * inversesqrt(1.0 + x * x);
}

// d is sdRoundedBox(p - vec2(0.0, y_offset), half_extent + spread,
//                   sqrt(r * r + 1.25 * sigma * sigma))
float shadow(float d, float sigma) {
    return 0.5 - 0.5 * erf7(d * (0.70710678 / sigma));   // 1 / sqrt(2)
}
```

**Do not use `1 - smoothstep(-1.5*sigma, 1.5*sigma, d)`**, which is the obvious
cheap substitute and is wrong in a visible way. The Gaussian CDF is still at
about 0.067 and 0.933 at -1.5 and +1.5 sigma, so a smoothstep over that range
hard-clamps a 6.7-percentage-point step into existence at both ends of the
penumbra — precisely where a soft shadow is supposed to fade out. Widening to
+-3 sigma does not fix it either: smoothstep is a cubic with zero slope at both
endpoints while the Gaussian CDF has small but non-zero slope there, so the
edge discontinuity is traded for a mid-range shape error. `erf7` is a handful
of ALU operations and one `inversesqrt`; it is not meaningfully more expensive
than `smoothstep`, and it is right.

The shadow is drawn in the **same draw** as the glass, before the glass clip,
over the window rectangle inflated by `y_offset + spread + 3 sigma` — 35
logical px at level 1, 66 at level 2. Two `erf7` evaluations per pixel of that
footprint. No extra texture, no extra pass, and crucially not through the blur
chain, which is the mistake picom had to undo: "dual kawase is fast but its
blur size is coarse grained, which sometimes makes the shadow ugly"
[PICOM-SHADOW].

Cost: for a 900x600 logical window at scale 1.45, the level-1 footprint is
about 0.16 Mpx of a ~25-instruction shader, roughly 7% of a full-screen pass.
**0.05-0.1 ms per window**, to be measured rather than believed.

**Two refusals and one deliberate difference.**

- **No interaction between the shadow and the bevel displacement.** The shadow
  is outside the glass rectangle, the displacement is inside it; they do not
  meet and must not be made to.
- **No animated shadow.** Comeau's warning applies: "it's probably a bad idea
  to try animating a layered shadow" [COMEAU-2021]. Elevation changes snap.
- **Our level 2 is larger than level 1; KDE's equivalent is smaller.** KDE's
  deleted HIG specified a 48 px window shadow and a 20 px shadow for menus and
  tooltips — the inverse of Material's ladder [KDE-HIG-ELEVATION]. That is
  right for menus, which are small and sit directly on their parent window, so
  a large shadow swamps them. Aurora's level 2 is the switcher and popups,
  which float above everything, so Material's ladder is the right shape here.
  Worth knowing it is a real disagreement and not an oversight.

Two cautions from KWin's experience with shadows, both about scope rather than
maths. KWin does not blur shadows at composite time at all: its Wayland shadow
protocol is a **nine-tile bitmap** protocol in which the client ships
pre-rendered, pre-blurred edge and corner textures and the compositor tiles
them [KWIN-SHADOW-XML]. That is cheaper than anything here and a genuinely
different strategy, available to us if the analytic shadow ever proves too
expensive. And KWin's attempt to draw shadows server-side for everything was
**reverted**, because "that regressed the visuals of a reasonable number of
apps. It appears like applications use normal toplevel windows to implement
popups instead of relying on more appropriate window types"
[ZAHORODNII-SHADOWS]. Aurora draws shadows for windows it already draws glass
for, which is the same set and the same risk; the per-surface opt-out of
section 3.9 is where that gets handled.

---

## 5. Motion

**The rule: decelerating curves for anything arriving or responding to input.**

An ease-out starts at maximum velocity, so the surface moves on the first
frame after the input arrived. An ease-in is nearly stationary for its first
frames and reads as latency no matter how short the total duration. This is
arithmetic, not taste. Progress completed after the first frame of a 200 ms
animation at 60 Hz:

| curve | 1st frame | t = 1/8 | t = 1/4 | t = 1/2 |
|---|---|---|---|---|
| M3 emphasised-decelerate `(0.05,0.7,0.1,1)` | **57.6%** | 67.6% | 83.2% | 95.0% |
| M3 standard-decelerate `(0,0,0,1)` | **40.6%** | 50.0% | 69.1% | 89.0% |
| M2 decelerate `(0,0,0.2,1)` | 26.4% | 35.9% | 57.8% | 83.9% |
| CSS/Apple `ease-out` `(0,0,0.58,1)` | 13.5% | 19.9% | 37.8% | 68.5% |
| M3 standard `(0.2,0,0,1)` | 9.9% | 25.6% | 60.7% | 87.8% |
| linear | 8.3% | 12.5% | 25.0% | 50.0% |
| M2 accelerate `(0.4,0,1,1)` | 1.3% | 2.8% | 9.9% | 32.5% |
| CSS `ease-in` `(0.42,0,1,1)` | 1.2% | 2.6% | 9.3% | 31.5% |
| M3 emphasised-accelerate `(0.3,0,0.8,0.15)` | 0.4% | 0.9% | 3.5% | 15.4% |

The spread at the first frame is about 150x. Note the trap in the middle of
that table: **M3 `standard` is not a responsive-onset curve.** `(0.2,0,0,1)`
has zero initial velocity by construction — for a cubic bezier the initial
velocity is `y1/x1`, and `y1 = 0` with `x1 > 0` means literally stationary at
onset. Adopting the M3 tokens wholesale and using `standard` for input
responses loses the property we are designing for.

**The asymmetry rule: ease-out on entry, ease-in on exit.** This is not a
contradiction. An exit has no input to feel responsive to — the input already
happened and was acknowledged by the thing now leaving — so accelerating away
reads as decisive and clears the screen sooner, where a decelerating exit
spends its last frames almost gone. Exits are also shorter: nobody needs to
read something that is leaving.

This is KDE's rule and it is in KWin's shipping code: the HIG said "items
animating from visible to invisible should be InCubic; items animating from
invisible to visible should be OutCubic", and the Scale effect that provides
the default window open and close uses exactly `QEasingCurve.OutCubic` on add
and `InCubic` on close, at a 200 ms default [KDE-HIG-ANIM, KWIN-SCALE]. It is
*not* universal: GNOME Shell uses ease-out for destroy as well as show
(`DESTROY_WINDOW_ANIMATION_TIME = 150` with `EASE_OUT_QUAD`), and KWin's own
Glide effect sets the curves the other way round from its Scale effect
[GNOME-WM, KWIN-GLIDE]. Aurora follows KDE's rule, knowing it is a real
disagreement and not a settled fact.

### 5.1 Curves

| name | cubic-bezier | source | use |
|---|---|---|---|
| `enter` | `(0,0,0,1)` | M3 standard-decelerate [M3-TOKENS] | the default for anything arriving |
| `enter-emphasised` | `(0.05,0.7,0.1,1)` | M3 emphasised-decelerate [M3-TOKENS] | large or important arrivals: window open, overlay rise |
| `exit` | `(0.4,0,1,1)` | M2 accelerate / `fast_out_linear_in` [MDC-ANIM, AOSP-INTERP] | anything leaving |
| `move` | `(0.2,0,0,1)` | M3 standard [M3-TOKENS] | visible-to-visible moves of a surface already on screen |
| `fade` | identity | CSS `linear` [CSS-EASING] | opacity only; never position |

<figure>
<svg viewBox="0 0 560 390" role="img" aria-labelledby="fig-curves">
  <title id="fig-curves">The five curves of the system, on one unit square</title>
  <rect x="56" y="24" width="240" height="240" class="dg-alt"/>
  <rect x="56" y="24" width="240" height="240" class="dg-line"/>
  <line x1="56" y1="264" x2="296" y2="24" class="dg-line" stroke-dasharray="3 3"/>
  <path d="M 56 264 C 56 264 56 24 296 24" class="dg-mark" stroke-width="2" opacity="1.00"/>
  <line x1="310" y1="54" x2="326" y2="54" class="dg-mark" stroke-width="2" opacity="1.00"/>
  <text x="334" y="58" class="dg-mono">enter</text>
  <text x="334" y="70" class="dg-label">anything arriving</text>
  <path d="M 56 264 C 68 96 80 24 296 24" class="dg-mark" stroke-width="2" opacity="0.86"/>
  <line x1="310" y1="80" x2="326" y2="80" class="dg-mark" stroke-width="2" opacity="0.86"/>
  <text x="334" y="84" class="dg-mono">enter-emphasised</text>
  <text x="334" y="96" class="dg-label">a large arrival</text>
  <path d="M 56 264 C 152 264 296 24 296 24" class="dg-mark" stroke-width="2" opacity="0.72"/>
  <line x1="310" y1="106" x2="326" y2="106" class="dg-mark" stroke-width="2" opacity="0.72"/>
  <text x="334" y="110" class="dg-mono">exit</text>
  <text x="334" y="122" class="dg-label">anything leaving</text>
  <path d="M 56 264 C 104 264 56 24 296 24" class="dg-mark" stroke-width="2" opacity="0.58"/>
  <line x1="310" y1="132" x2="326" y2="132" class="dg-mark" stroke-width="2" opacity="0.58"/>
  <text x="334" y="136" class="dg-mono">move</text>
  <text x="334" y="148" class="dg-label">already on screen</text>
  <line x1="310" y1="158" x2="326" y2="158" class="dg-line" stroke-dasharray="3 3" stroke-width="2"/>
  <text x="334" y="162" class="dg-mono">linear</text>
  <text x="334" y="174" class="dg-label">opacity only</text>
  <text x="176" y="282" class="dg-label" text-anchor="middle">time</text>
  <text x="38" y="144" class="dg-label" text-anchor="middle" transform="rotate(-90 38 144)">progress</text>
  <text x="20" y="310" class="dg-label">Every curve but `exit` leaves at full speed and arrives slowly, which is</text>
  <text x="20" y="326" class="dg-label">what makes something the person started feel immediate. `move` is the</text>
  <text x="20" y="342" class="dg-label">one place the zero onset is right: the surface is already on screen, so</text>
  <text x="20" y="358" class="dg-label">continuity matters more than onset.</text>
</svg>
<figcaption>Section 5.1. A curve is a value a caller passes, not a branch inside each animating node, which is what makes adding one a row of a table.</figcaption>
</figure>


`move` is the one place the zero-onset curve is correct: the surface is
already on screen and continuity matters more than onset, which is also the
one case with real experimental support in the other direction (section 5.5).

### 5.2 Springs

A bezier is right when the duration is known and the animation will not be
interrupted. A spring is right when it can be interrupted or inherits a
velocity, because a spring is defined by state rather than by elapsed time: a
new target mid-flight changes only where it is heading. Apple's own argument
is the clearest statement of this: "springs are the only type of animation
that maintains continuity both for static cases and cases with an initial
velocity", and of a bezier, "this type of animation is just a prespecified
curve, so there's no way to represent an initial velocity" [WWDC23-10158].
GNOME says the same of libadwaita's spring: initial velocity "adjusts the
curve without changing the duration. This makes spring animations useful for
deceleration at the end of gestures" [ADW-SPRING].

Specify springs the way Apple does, as `(duration, bounce)`, and derive the
physical constants:

```
stiffness k = m * (2*pi / duration)^2
zeta        = 1 - bounce
damping c   = 2 * zeta * sqrt(k * m)
```

This reproduces both of Apple's documented worked examples exactly:
`Spring(duration: 0.5, bounce: 0.3)` gives `(m 1.0, k 157.9, c 17.6)`, and
`Spring(mass: 1, stiffness: 100, damping: 10)` gives `(duration 0.63, bounce
0.5)` [APPLE-SPRING]. The `k = m(2*pi/duration)^2` relation is not documented
by Apple; it is consistent with those examples and should be cited that way.

| use | duration | bounce | k | c | zeta | 1% settle |
|---|---|---|---|---|---|---|
| switcher selection move | 0.30 | 0.15 | 438.6 | 35.6 | 0.85 | 259 ms |
| switcher open / close | 0.25 | 0.00 | 631.7 | 50.3 | 1.00 | 183 ms |
| dock pill press | 0.20 | 0.20 | 987.0 | 50.3 | 0.80 | 183 ms |
| drag release / fling | 0.35 | 0.10 | 322.3 | 32.3 | 0.90 | 285 ms |

The switcher selection is the case that forces a spring: Alt held and Tab
pressed repeatedly retargets the selection before the previous move finishes.
Anything modal — the trusted overlay — must be critically damped or
over-damped: an approval prompt that bounces looks like a toy, and looking
serious is a security property for that surface. Apple's guidance is the same
bound from the other side: bounce 0.15 "doesn't feel very bouncy yet", 0.3 is
where "you do start to feel some noticeable bounciness", and "you should be
cautious about using values higher than around 0.4" [WWDC23-10158].

**Do not wait for settling time to call a transition done.** Apple's own
warning: the perceptual duration is "chosen to be predictable and not move
around", and "because of its unpredictable nature, you shouldn't wait for the
settling duration for user-facing changes" [WWDC23-10158].

### 5.3 Durations

Snapped to the grid the platforms actually use — Kirigami 50/100/200/400
[KIRIGAMI-UNITS], Fluent 50/100/150/200/250/300/400/500 [FLUENT-DURATIONS],
M3 50..1000 in 50 ms steps [M3-TOKENS]:

| interaction | enter | exit | curve | comparable platform value |
|---|---|---|---|---|
| dock item hover highlight | 100 | 100 | `fade` | Kirigami `shortDuration` 100, documented for "hover events" |
| dock item press | 50 | 100 | spring | Fluent `durationUltraFast` 50 |
| window open | 200 | — | `enter-emphasised` | KWin Scale 200; GNOME `SHOW_WINDOW` 150 |
| window close | — | 150 | `exit` | GNOME `DESTROY_WINDOW` 150 |
| popup / menu open | 100 | 100 | `enter` / `exit` | GNOME `DIALOG_SHOW_WINDOW` 100 |
| switcher open | 200 | — | spring, critically damped | GNOME overview `ANIMATION_TIME` 250 |
| switcher close | — | 150 | `exit` | |
| switcher selection move | emergent | — | spring, retargetable | |
| trusted overlay rise | 150 | — | `enter-emphasised` | |
| trusted overlay fall | — | 100 | `exit` | |
| theme hot reload | 0 | 0 | none | a theme change snaps; animating it would hide what changed |

<figure class="wide">
<svg viewBox="0 0 1000 440" role="img" aria-labelledby="fig-dur">
  <title id="fig-dur">Every duration in the system, against the latency thresholds they were chosen against</title>
  <line x1="300" y1="44" x2="300" y2="330" class="dg-line"/>
  <text x="300" y="36" class="dg-mono" text-anchor="middle">0</text>
  <line x1="400" y1="44" x2="400" y2="330" class="dg-line"/>
  <text x="400" y="36" class="dg-mono" text-anchor="middle">50</text>
  <line x1="500" y1="44" x2="500" y2="330" class="dg-line"/>
  <text x="500" y="36" class="dg-mono" text-anchor="middle">100</text>
  <line x1="600" y1="44" x2="600" y2="330" class="dg-line"/>
  <text x="600" y="36" class="dg-mono" text-anchor="middle">150</text>
  <line x1="700" y1="44" x2="700" y2="330" class="dg-line"/>
  <text x="700" y="36" class="dg-mono" text-anchor="middle">200</text>
  <line x1="800" y1="44" x2="800" y2="330" class="dg-line"/>
  <text x="800" y="36" class="dg-mono" text-anchor="middle">250</text>
  <text x="560" y="20" class="dg-mono" text-anchor="middle">milliseconds</text>
  <text x="20" y="64" class="dg-mono">dock item hover highlight</text>
  <text x="836" y="64" class="dg-label">fade</text>
  <rect x="300" y="52" width="200" height="7" rx="3" class="dg-accent"/>
  <text x="505" y="59" class="dg-mono">100</text>
  <rect x="300" y="62" width="200" height="7" rx="3" class="dg-soft" opacity="0.5"/>
  <text x="505" y="69" class="dg-mono">100</text>
  <text x="20" y="90" class="dg-mono">dock item press</text>
  <text x="836" y="90" class="dg-label">spring</text>
  <rect x="300" y="78" width="100" height="7" rx="3" class="dg-accent"/>
  <text x="405" y="85" class="dg-mono">50</text>
  <rect x="300" y="88" width="200" height="7" rx="3" class="dg-soft" opacity="0.5"/>
  <text x="505" y="95" class="dg-mono">100</text>
  <text x="20" y="116" class="dg-mono">window open</text>
  <text x="836" y="116" class="dg-label">enter-emphasised</text>
  <rect x="300" y="104" width="400" height="7" rx="3" class="dg-accent"/>
  <text x="705" y="111" class="dg-mono">200</text>
  <text x="20" y="142" class="dg-mono">window close</text>
  <text x="836" y="142" class="dg-label">exit</text>
  <rect x="300" y="140" width="300" height="7" rx="3" class="dg-soft" opacity="0.5"/>
  <text x="605" y="147" class="dg-mono">150</text>
  <text x="20" y="168" class="dg-mono">popup / menu open</text>
  <text x="836" y="168" class="dg-label">enter / exit</text>
  <rect x="300" y="156" width="200" height="7" rx="3" class="dg-accent"/>
  <text x="505" y="163" class="dg-mono">100</text>
  <rect x="300" y="166" width="200" height="7" rx="3" class="dg-soft" opacity="0.5"/>
  <text x="505" y="173" class="dg-mono">100</text>
  <text x="20" y="194" class="dg-mono">switcher open</text>
  <text x="836" y="194" class="dg-label">spring</text>
  <rect x="300" y="182" width="400" height="7" rx="3" class="dg-accent"/>
  <text x="705" y="189" class="dg-mono">200</text>
  <text x="20" y="220" class="dg-mono">switcher close</text>
  <text x="836" y="220" class="dg-label">exit</text>
  <rect x="300" y="218" width="300" height="7" rx="3" class="dg-soft" opacity="0.5"/>
  <text x="605" y="225" class="dg-mono">150</text>
  <text x="20" y="246" class="dg-mono">trusted overlay rise</text>
  <text x="836" y="246" class="dg-label">enter-emphasised</text>
  <rect x="300" y="234" width="300" height="7" rx="3" class="dg-accent"/>
  <text x="605" y="241" class="dg-mono">150</text>
  <text x="20" y="272" class="dg-mono">trusted overlay fall</text>
  <text x="836" y="272" class="dg-label">exit</text>
  <rect x="300" y="270" width="200" height="7" rx="3" class="dg-soft" opacity="0.5"/>
  <text x="505" y="277" class="dg-mono">100</text>
  <text x="20" y="298" class="dg-mono">Atlas reveal / conceal</text>
  <text x="836" y="298" class="dg-label">enter / exit</text>
  <rect x="300" y="286" width="320" height="7" rx="3" class="dg-accent"/>
  <text x="625" y="293" class="dg-mono">160</text>
  <rect x="300" y="296" width="240" height="7" rx="3" class="dg-soft" opacity="0.5"/>
  <text x="545" y="303" class="dg-mono">120</text>
  <text x="20" y="324" class="dg-mono">theme hot reload</text>
  <text x="836" y="324" class="dg-label">none</text>
  <text x="304" y="324" class="dg-mono">it snaps: animating a theme change would hide what changed</text>
  <line x1="364" y1="44" x2="364" y2="348" class="dg-mark" stroke-dasharray="4 4"/>
  <text x="364" y="362" class="dg-mono" text-anchor="middle">32</text>
  <line x1="410" y1="44" x2="410" y2="348" class="dg-mark" stroke-dasharray="4 4"/>
  <text x="410" y="362" class="dg-mono" text-anchor="middle">55</text>
  <line x1="492" y1="44" x2="492" y2="348" class="dg-mark" stroke-dasharray="4 4"/>
  <text x="492" y="362" class="dg-mono" text-anchor="middle">96</text>
  <line x1="700" y1="44" x2="700" y2="348" class="dg-mark" stroke-dasharray="4 4"/>
  <text x="700" y="362" class="dg-mono" text-anchor="middle">200</text>
  <text x="20" y="390" class="dg-label">The dashed marks are measurements, not taste. 32 ms is where visual feedback to a press is still perceived as simultaneous; 55 and 96 ms are the just-noticeable</text>
  <text x="20" y="406" class="dg-label">differences for added latency when dragging and when tapping; 200 ms is the ceiling that follows from them. The solid bar is the enter, the faint one the exit. An ease-in</text>
  <text x="20" y="422" class="dg-label">curve on a 200 ms animation covers 1.2 per cent in the first frame, which manufactures 30 to 60 ms of "nothing visibly happened" -- squarely inside that discrimination</text>
  <text x="20" y="438" class="dg-label">range. Window open is the longest thing on the list because it is the only one where the person has to find out WHERE the new window went.</text>
</svg>
<figcaption>Section 5.3, with the evidence of 5.5 on the same axis. The values are snapped to the grid the platforms already use: Kirigami 50/100/200/400, Fluent 50 to 500, M3 in 50 ms steps.</figcaption>
</figure>


**And there is a second, compositor-specific reason to keep the effects pass
cheap, which belongs here rather than in section 11.** Blur does not only cost
GPU time; it raises render-time *variance*. KWin's work on old hardware found
that "render times are *very* volatile at times; KWin's rendering can be
delayed by other apps using the CPU and GPU, by the hardware changing power
management states, by additional windows opening, **by KWin effects starting to
render something heavy**, by input events taking CPU time", and the fix was to
"analyze past render times for how volatile they are, and start compositing
much earlier if they're volatile" [HUGL-PERF]. In a deadline-driven compositor
that translates directly into added input latency on *every* frame, including
cheap ones. So an expensive, bursty effect does not merely drop frames: it
spends the latency budget this whole section is trying to protect. That is a
stronger argument for bounding blur cost than any millisecond figure.

Nothing exceeds 200 ms. The budget comes from the latency-perception
literature rather than from the 100 ms folk rule (section 5.5): for the
indirect input a desktop actually has, the measured JND for added latency is
about 55 ms when dragging and 96 ms when tapping, and visual feedback to a
press is perceived as simultaneous up to about 32 ms [DEBER-2015,
KAARESOJA-2014]. An ease-in curve on a 200 ms animation covers 1.2% in the
first frame and about 5% by 33 ms, which manufactures 30-60 ms of "nothing
visibly happened" — squarely inside the discrimination range for every input
modality. Window open is the longest thing on the list because it is the only
one where the user needs to find out *where* the new window went.

### 5.4 Reduced motion

The standards are narrower than the folklore. `prefers-reduced-motion` means
the user "prefers an interface that removes or replaces the types of
motion-based animation that either trigger discomfort" for vestibular
sensitivity; the spec contains no durations and no list of forbidden effects
[MQ5]. WCAG 2.1 SC 2.3.3 requires that interaction-triggered motion animation
"can be disabled, unless the animation is essential", and it is **Level AAA**
[WCAG-233]. The feature's own author is explicit that "entirely stopped
animations aren't necessary for vestibular accommodation" [CRAIG-CSSWG].

What the guidance actually targets, named by the sources: scaling and zooming,
spinning and vortex effects, multi-speed or multi-directional movement such as
parallax, dimensionality and plane shifting (2.5D), and constant peripheral
motion near text [WEBKIT-RDM]. Apple's qualifying criteria for declaring
Reduced Motion support name parallax, **animated blur**, depth of field,
multi-axis motion, multi-speed motion, spinning, vortex, auto-advancing
carousels and ongoing motion — and **plain translation or scale of a single
surface is not on that list** [APPLE-RM-CRIT]. Val Head's is the closest thing
to a sourced statement that fades are safe: "animation that involves only
non-moving properties, like opacity, color, and blurs, are unlikely to be
problematic" [HEAD-2015]. Note that the canonical WebKit post does not
recommend cross-fading and does not use the word; the cross-fade substitution
is Apple's *end-user* behaviour ("screen transitions and effects use the
dissolve effect instead of zoom or slide effects") plus community practice
[APPLE-RM-USER].

So `[motion] reduced = true` in the theme does this, targeted at what Aurora
actually has that the guidance names:

- **The switcher's Flip 3D layout falls back to `grid`.** A receding, rotated
  stack is plane shifting and multi-axis motion — the most clearly implicated
  thing in Aurora. The theme already has the switch (`switcher.layout`).
- **`background.speed` goes to 0.** Constant peripheral motion.
  `--freeze-time` already does exactly this; reduced motion sets it from the
  theme.
- **Window open and close keep their translate and scale, but the scale
  distance is halved** (0.96 -> 0.98) and the duration is unchanged. Plain
  single-surface translate and scale is not what the guidance targets, and
  removing it would mean windows appearing with no indication of where they
  came from.
- **Springs become their settled value.** Not a faster spring: no spring.
- Cross-fades survive at 100 ms, which is what the only sourced statement in
  the area supports.

And separately, a true zero: `[motion] enabled = false` means no animation at
all, for the user who wants that rather than the user the vestibular guidance
describes. There is shipping precedent for both tiers — KWin's animation-speed
slider reaches a factor of 0, with the in-source comment "0 is a special case,
which means 'instant speed / no animations'", and libadwaita skips animations
entirely when `adw_get_enable_animations()` is false [KWIN-ANIMKCM, ADW-ANIM].
KWin's floor is 1 ms rather than 0, "otherwise 0ms times can break some
things", which is the implementation note to copy [KWIN-EFFECT].

Reduced motion does **not** touch transparency or blur. That is
`[glass] enabled`, which already exists and is already tested. Conflating them
would mean claiming an accessibility property nobody measured. (If a
`prefers-reduced-transparency` equivalent ever arrives on the Wayland side,
that is the hook it maps to.)

### 5.5 Where this rule is *not* supported by research, stated plainly

The claim "ease-out feels faster than ease-in at equal duration" is designer
consensus, not a measured finding. No controlled experiment on easing-curve
shape and perceived duration could be found. The nearest real work, Harrison,
Yeo and Hudson's progress-bar study, shows perceived duration *can* be
manipulated by how something moves, but it manipulates pacing and visual
augmentation rather than UI easing curves and publishes no effect sizes
[HARRISON-2010].

Worse for the simple story: the one directly on-point study of easing curves
favours the *opposite* curve for its task. Dragicevic and colleagues compared
constant speed, slow-in/slow-out, fast-in/fast-out and complexity-adaptive
pacing on object tracking in one-second transitions, and found
slow-in/slow-out best, recommending it "for short-duration animated
transitions to show state changes in an interactive application"
[DRAGICEVIC-2011]. Their own framing opens by noting that the slow-in/slow-out
guideline had "no empirical data" behind it either.

The two positions reconcile, and the reconciliation is why this section's
rules are shaped as they are. Dragicevic's mechanism is *predictability* — a
slow start helps the viewer notice the animation beginning, explicitly
assuming "the user is not expecting the animation to happen in the first
place" — and the task was tracking one target among distractors. An
input-triggered response is the opposite situation: the user caused it and is
looking for confirmation. So:

- **Input-initiated arrival: ease-out.** Supported by the first-frame
  arithmetic above plus the latency-perception literature [JOTA-2013,
  DEBER-2015, KAARESOJA-2014], not by curve-shape research.
- **System-initiated or multi-object transitions the user must visually
  track: ease-in-out.** Supported by [DRAGICEVIC-2011]. Aurora has one
  candidate today, `move` in section 5.1, and would have more if window
  management ever animates a re-layout.

Do not write "research shows ease-out reads faster". Write the table.

---

## 6. Type

`compositor/src/scale.rs` already states the architecture and `text.rs`
implements it: layout in logical units, glyphs rasterised at `size * scale`
device pixels, cached per (face, size, sub-pixel bin) by swash, with
`wp_fractional_scale_v1` on the 1/120 grid. Nothing here changes that. What is
missing is a scale: the sizes in use today are `14` (dock labels, body),
`14 * 1.05` (clock date), `14 * 1.15` bold (dock tile initial), `14 * 3.2`
(clock), and `14`, `24` bold, `26` bold in the trusted overlay — six ad-hoc
multipliers of one base.

**A modular scale, ratio 1.2, base 14 logical px**, rounded to the half pixel
so every step lands on a whole device pixel at scale 1.0:

| token | logical px | at scale 1.45 | use |
|---|---|---|---|
| `caption` | 11.5 | 16.7 | secondary labels, timestamps |
| `body` | 14.0 | 20.3 | dock labels, overlay body, the default |
| `body-lg` | 17.0 | 24.7 | dock tile initial (bold), emphasis |
| `title` | 20.0 | 29.0 | section headings |
| `title-lg` | 24.0 | 34.8 | overlay buttons (bold) |
| `display` | 29.0 | 42.1 | overlay heading (was 26) |
| `display-lg` | 35.0 | 50.8 | — |
| `clock` | 42.0 | 60.9 | desktop clock (was 44.8) |

Line height `1.35 * size` for `body` and below, `1.2 * size` above, close to
the `size * 1.3` `text.rs` already uses when shaping. Weights 400 and 700
only: the project cannot assume a variable font is installed, and asking
fontconfig for a weight it does not have produces a synthetic smear.

Two rules that come from the pipeline rather than from taste:

- **Never scale a rasterised glyph.** A cached glyph belongs to one
  (face, size, scale) triple; if the scale changes, re-rasterise. `text.rs`
  already keys the cache that way and the clock texture is rebuilt on a scale
  change for exactly this reason.
- **Round text origins to device pixels in the vertical only.** `draw`
  currently rounds both (`(x * scale).round()`), which is right for the
  baseline and discards swash's sub-pixel binning horizontally — the reason
  the cache has a sub-pixel bin key at all. Keeping horizontal sub-pixel
  positioning is a small, cheap improvement in text evenness at 14 px.

One note on size and contrast: Microsoft warns that accent-coloured text on
Acrylic is "likely to not pass minimum contrast ratio requirements at the
default 14px font size" and advises against hyperlinks over it [MS-ACRYLIC].
Aurora's dock labels are 14 logical px on chrome glass. Section 3.3's ceiling
is what makes that safe; coloured text on glass is still refused.

---

## 7. What we take from each platform, and what we leave

| technique | source | verdict |
|---|---|---|
| Dual-Kawase blur | ARM, [BJORGE-2015] | **keep.** Already implemented correctly. 49.78 dB against a Gaussian reference, 7% of a linear-sampling Gaussian's bandwidth, stable under sub-pixel translation. |
| KWin's per-iteration offset bands | [KWIN-BLUR-SRC] | **take as an envelope.** Below `minOffset` you get blocky downsampling artefacts, above `maxOffset` diagonal lines. Our radii sit inside the two-iteration band. |
| Downsampled, shared backdrop capture | Apple's `setScale(0.25)` [SHATTERED-GLASS]; Intel's half/quarter-res timings [STRUGAR-2014] | **take.** 2.6-3.6x less traffic, and the per-surface marginal cost falls 4.2x (window) to 17x (chrome). The single biggest lever here. |
| Pre-blurring the wallpaper as a static texture | KDE's own low-power request [KDE-507501]; Mica [MS-MICA] | **skip, for now.** It is the right answer for a Raspberry Pi and the wrong one for a live backdrop. Revisit if a low-power target appears. |
| Luminance suppression of the backdrop | Microsoft luminosity blend [MS-ACRYLIC-SRC], Apple regular glass [APPLE-MATERIALS] | **take the mechanism, not the implementation.** Ours is a Reinhard roll-off that preserves backdrop structure; Microsoft replaces lightness outright, which is why their panels read flat. Note their documented motivation was shadow bleed-through, not text. |
| Noise over the blurred panel | Microsoft, 2% [MS-ACRYLIC-SRC] | **take, at lower amplitude.** Our blur is narrower so the backdrop keeps more structure of its own. |
| Edge displacement from an SDF-derived bevel | Apple (goal only) [WWDC25-219]; [ZENN-GLASS], [SHATTERED-GLASS] | **take, bounded, window glass only, and call it displacement.** |
| Clamping the bevel slope | [SHATTERED-GLASS-NOTES] | **take.** Without it, total internal reflection paints a hard black ring. |
| Blur level from local UV compression | [SHATTERED-GLASS-NOTES]; Apple's `inputBlurDistance0..4` [SHATTERED-GLASS] | **take, last.** Needs a second sampler through `GlesFrame::with_context`. |
| Chromatic aberration | [ZENN-GLASS] and others | **skip.** Apple documents no dispersion; at 6 px it reads as a fault. |
| Fake fixed-light specular | [ZENN-GLASS] | **skip.** Apple's lighting responds to device motion, which a desktop lacks. Our edge falloff already implies a light. |
| Light/dark polarity flip of the material and its text | Apple [WWDC25-219] | **skip.** It pops at the threshold and needs hysteresis Apple does not publish. Our per-pixel ceiling gets the legibility without the flip. Apple's own rule — flip small chrome, never large surfaces — is the tell that it is awkward. |
| Mica: sample the wallpaper once, no live backdrop | Microsoft [MS-MICA] | **skip the material, keep the lesson.** Mica exists because live backdrop blur is expensive; our answer to the same problem is the quarter-res shared prepass, which keeps the live backdrop. Worth revisiting if a low-power target ever appears. |
| Inactive-window fallback to a neutral colour | Microsoft [MS-MICA] | **skip for now.** Documented as a focus/hierarchy rule, not a performance one; it would need a focus-aware material, which is window management we do not have. |
| Karis average, 13-tap custom downsample, 3x3 tent upsample | [JIMENEZ-2014] | **skip.** They fix pulsating artifacts and fireflies in bloom chains. Bjorge's stability tests show the dual filter does not have that problem [BJORGE-2015]. Criterion C5.2 verifies rather than assumes. |
| Linear-light blending of client translucency | Microsoft scRGB fp16 [MS-HDR]; KWin tried and reverted [HUGL-P4] | **skip, deliberately.** Client alpha is authored for encoded-space blending. Section 2.2. |
| Linear-light blur | the arithmetic in 2.1; the artifact in [KWIN-D18377] | **take, as a deliberate departure.** KWin blurs in gamma 2.2 by declaration [KWIN-BLEND-SPACE] and no shipping compositor blurs in linear. Section 2.2 argues why their reasons do not transfer; criterion C1.1 is what makes it falsifiable. |
| One-sided brightening noise over the blur | KWin [KWIN-BLUR-SRC] | **skip.** Right reason ("mask banding artifacts"), wrong distribution: uniform on `[0, n-1]`, `GL_ONE, GL_ONE`, so it lifts the image as well as dithering it. Zero-mean triangular costs the same. |
| `ext-background-effect-v1` for per-surface opt-in | wayland-protocols staging, merged 2025-05-26, Smithay/COSMIC and KDE ACKs [EXT-BG-EFFECT] | **take when a per-window opt-out is built.** Its predecessor `org_kde_kwin_blur` left every semantic undefined and KWin removed it in 6.7.0. |
| Nine-tile client-supplied shadow bitmaps | KWin `org_kde_kwin_shadow` [KWIN-SHADOW-XML] | **skip, keep in reserve.** Cheaper than anything analytic, but it makes the client own the look. |
| Analytic rounded-rect shadow via SDF + erf | Levien 2020 [LEVIEN-2020]; shipped by Qt 6.9 [QT-RECTSHADOW] | **take.** One evaluation, no loop, reuses the SDF we already compute. |
| Shadows through the blur chain | picom tried it [PICOM-SHADOW] | **skip.** "Dual kawase is fast but its blur size is coarse grained, which sometimes makes the shadow ugly." |
| Material's three-layer umbra/penumbra/ambient ladder | [MDC-ELEVATION] | **take the geometry, merge to two layers.** Three evaluations cost 50% more for a difference below 8-bit quantisation at our sizes. |
| Server-side shadows for everything | KWin tried and reverted [ZAHORODNII-SHADOWS] | **skip.** Apps use plain toplevels for popups, so a blanket rule regresses them. |
| Gamma encoding to cut buffer depth | KWin [HUGL-P4, KWIN-6050] | **take, adapted.** Store `sqrt(linear)` in 10 bits: same trick, applied to a blur chain that must still average linearly. |
| Ease-out on entry, ease-in on exit | KDE HIG and KWin Scale [KDE-HIG-ANIM, KWIN-SCALE] | **take**, knowing GNOME disagrees about exits. |
| Spring parameterised as (duration, bounce) | Apple [APPLE-SPRING, WWDC23-10158] | **take.** Two numbers a designer can reason about, with an exact mapping to k and c. |
| Material/Fluent/Kirigami duration grids | [M3-TOKENS, FLUENT-DURATIONS, KIRIGAMI-UNITS] | **take the grid**, pick our own values on it. |
| M3 `standard` as the default entry curve | [M3-TOKENS] | **skip.** Zero initial velocity; 9.9% in the first frame. Use `standard-decelerate`. |
| APCA as the shipped contrast gate | [APCA] | **skip.** Section 8.1. |
| Squircles in the shader | Apple's three-cubic corner [PAINTCODE-2013]; Figma [FURSE-2018] | **skip** (section 3.6). Figma abandoned the exactly-curvature-continuous family as "barely noticeable"; the cheap p-norm breaks our exact antialias; the correct version is a 12-iteration solve. The 1-D bevel profile keeps its Lame curve. |
| The CSS `superellipse(K)` convention | [CSS-BORDERS-4] | **take if a knob is ever exposed.** `squircle` = `superellipse(2)` = classical n=4, not n=5. |
| Progressive blur as a general effect | web idiom, GEGL, [GARCIA-2017] | **skip.** Section 3.7. |

Two general observations worth keeping in mind when reading any of the above.

**The two vendors document opposite things.** Microsoft publishes a layer stack
and ships the constants under MIT, but says little about perceptual intent.
Apple publishes perceptual intent in detail and no implementation detail at
all, and ships no source. Any "Apple's recipe" table anywhere, including the
rows above, is reconstruction.

**Only one of them makes performance claims in design documentation.** The
Acrylic page says plainly that "rendering acrylic surfaces is GPU-intensive,
which can increase device power consumption and shorten battery life" and that
the effect is "automatically disabled when a device enters Battery Saver mode"
[MS-ACRYLIC]. Apple's design sessions 219 and 356 contain no GPU, battery or
power statement; the only Apple performance guidance is in the SwiftUI API
reference, which says too many effects "can degrade performance" and to batch
them in a `GlassEffectContainer` [APPLE-GLASS-CUSTOM]. That container's
existence — "renders multiple glass elements into a combined effect" — is also
the best available evidence that Apple shares one backdrop pass between
surfaces, which is item 8 of the sequence in decision 0010.

---

## 8. Text legibility

### 8.1 WCAG 2 is the gate; APCA informs the design but is not shipped

WCAG 2's contrast ratio is `(L1 + 0.05)/(L2 + 0.05)` on relative luminance
`0.2126 R + 0.7152 G + 0.0722 B` computed from sRGB-decoded channels, with
4.5:1 for normal text and 3:1 for large text and non-text [WCAG22].

Its weaknesses are real and documented. The central tracking issue says the
formula "is not perceptually uniform and as a result creates 'contrast ratios'
that are not meaningful"; it was opened in 2019, moved to the WCAG 3 tracker
in 2025, and is still open with no resolution [WCAG-695]. An independent
academic traced the formula's provenance and reported that he "ha[d] not thus
far been able to find any traceable empirical or theoretical evidence to
support this formula" [WALLER-2022]. The 4.5:1 threshold itself is a 1.5x
multiplier on 3:1 chosen partly so that `#767676` on white would pass — which
it does, at 4.542:1 [WCAG-1705].

APCA is the proposed replacement and it is a better model: a pure 2.4 power
function rather than the piecewise curve, a soft black clamp
(`if Y <= 0.022 then Y += (0.022 - Y)^1.414`), and — the part that matters most
here — **polarity-asymmetric exponents**, 0.65/0.62 for light-on-dark against
0.56/0.57 for dark-on-light [APCA]. The cleanest demonstration: white and
`#767676` give WCAG 4.542:1 in *both* directions, but APCA gives Lc +71.6 for
`#767676` text on white and Lc -77.0 for white text on `#767676`. Same two
colours, 5.5 Lc apart depending on which is the text.

Three reasons Aurora does not ship it.

1. **Licence and patent.** The apca-w3 licence is "W3 License for Compliant
   Code Only", reserves all rights, states "Patent(s) pending", licenses use
   "for web-delivered and web-based content only", and prohibits "uses which
   are not specific to web based content presented on self-illuminated
   displays or devices" [APCA-LICENSE]. A Wayland compositor is not obviously
   inside that, and the trademark grant is conditioned on compliance. That is
   a question for a lawyer, not a judgement call.
2. **It is not a standard.** APCA is not in the current WCAG 3 Working Draft,
   which says the contrast measure is "to be determined"; it was removed under
   the exploratory-content rule in 2023 [WCAG3, ROSELLI-2026]. No independent
   validation of the Lc scale or its thresholds was found; the model's author
   cites named reading research but has not published a granular bibliography,
   and a W3C staff member criticised its single 2.4 exponent as
   underestimating luminance "for all colors, being worst in the mid range"
   [APCA-REVIEW]. Neither metric is a validated standard. That is the honest
   state of the field.
3. **For our polarity, WCAG 2 is the more conservative gate.** Aurora's chrome
   is light-on-dark, which is the polarity APCA rates *more* favourably — the
   example above passes APCA's Lc 75 body-text minimum while failing a 4.5:1
   reading in the other direction. Gating on WCAG 2 therefore costs us nothing
   in strictness and gains us an unambiguous, trivially computable number that
   a capture test can assert.

What we take from APCA is its *ideas*, in our own arithmetic: the luminance
ceiling of section 3.3 is a worst-case bound, not an average; the published Lc
thresholds (Lc 75 minimum for columns of body text, Lc 60 for non-body text,
Lc 45 for headlines, Lc 15 as the floor for discernible non-text [APCA-NUT])
are a useful sanity check at design time; and the polarity asymmetry is a
reason to be suspicious of any single symmetric number. If a second, advisory
metric is ever wanted in the repo, it should be independently derived and
carry no APCA branding.

One adjacent precedent worth knowing, because it is a real shipped constant:
Firefox picks white or black text against a background by comparing relative
luminance to `sqrt(1.05 * 0.05) - 0.05 ~= 0.1791`, which is the luminance at
which the two polarities have equal WCAG contrast [FIREFOX-COLOR]. Aurora does
not need it, because chrome glass never gets bright enough to want dark text —
that is what the ceiling guarantees.

### 8.2 Contrast floors

| text | over | floor | enforced by |
|---|---|---|---|
| dock label, overlay body, clock | chrome glass | **4.5:1**, achieved 5.9:1 worst case | section 3.3's ceiling, criterion C2.1 |
| overlay heading and buttons (>= 24 logical px bold) | chrome glass | 3:1 | same ceiling, with margin |
| dock tile initial | tile colour | 4.5:1 | the tile is opaque; `tile_colour` must be checked at generation |
| switcher accent border | card edge | 3:1 (non-text) | `backdrop_dim` plus the accent token |
| any coloured text | glass | refused | section 6 |

### 8.3 Glyph antialiasing and the gamma trap

`Canvas::over` blends glyph coverage in 8-bit encoded space. Linearising it is
physically correct and will make light-on-dark text render **heavier** than it
does now, because encoded-space coverage blending currently produces less
light than the coverage calls for. That is the opposite of the usual complaint,
and it may not be what is wanted: the standard counter-adjustment is a gamma
applied to the coverage value before the linear blend, so that the apparent
stem weight is tuned independently of the physics.

This document specifies the structure and not the value:

```
alpha_linear = pow(coverage, text_gamma)
dst = mix(dst_linear, src_linear, alpha_linear)      // in linear light
```

`text_gamma = 1.0` is the physically correct case. Values above 1.0 thin
light-on-dark text back toward today's appearance. The right number is an
empirical question about the installed font at 14 logical px on this display,
and it belongs in the theme as `[font] gamma`, defaulted to 1.0 and changed
only with a side-by-side capture to justify it. Decision 0010 lists it as an
open question rather than deciding it.

---

## 9. Temporal stability

Blur chains flicker for two documented reasons, and it is worth being precise
about which ones apply to us.

**The sourced causes.** Jimenez's list is: "Filtering. Downsampling:
information must fit in the lower mips. Upsampling: information must be
correctly synthesized. Fireflies (very bright subpixels)" [JIMENEZ-2014,
slide 146]. The mechanism, in his words: "any non-linear intensity mapping
before averaging produces pulsating artifacts on small objects that move in
steps similar to their size. Linear motion of the objects do not translate to
linear color changes" [slide 166]. A naive 4-texel-average downsample "creates
pulsating artifacts and temporal stability issues" [slide 149]; even the
16-texel version "mitigates the issues - but does not completely fix them"
[slide 150]. Note that slide 166 is also an argument *for* section 2's linear
blur: the non-linear intensity mapping he names is exactly what we are
removing.

**Why we probably do not have the problem.** Bjorge tested stability directly:
a blocky image translated in sub-pixel increments, the same plus one very
bright pixel, and a rotating bright square behind grey bars. "Stability is very
good - except for the naive implementation which really suffers here", the
naive implementation being `glGenerateMipmap`; Kawase and the dual filter were
both good in all three [BJORGE-2015]. We use the dual filter. So the Karis
average and the 13-tap downsample are refused (section 1), and criterion C5.2
exists to verify that rather than trust it.

The common explanation — that the sampling grid is in screen space, so
sub-pixel motion changes which samples land where — could not be found stated
in those words in any source. Jimenez's slide 166 is close and is the thing to
cite. Treat the grid formulation as synthesis.

**What we must get right regardless.** Three things, each with a criterion:

- **The material noise must not vary with time.** Section 3.5. A per-frame
  noise field is a crawling fizz. This is the most likely way for Aurora to
  acquire a flicker problem it does not currently have.
- **The dither seed must be reproducible under `--freeze-time`.** Temporal
  dither is good for perception, and the background shader already uses
  `fract(time)`; it is only acceptable because `--freeze-time` pins that
  clock, which makes a frozen frame byte-identical frame to frame.
- **The blur chain must never skip levels on the way back up.** "Never
  bilinearly upsample mip6 to mip0 resolution directly. Upsample from mip6 to
  mip5, from mip5 to mip4 and so on" [JIMENEZ-2014, slides 159-161].
  `fx/blur.rs` already does this correctly; the downsampled prepass of
  section 3.2 must not become an excuse to break it.

---

## 10. Acceptance criteria

This project verifies by decoding captures pixel by pixel
(`compositor/tests/common/mod.rs`: `Comp::capture` returns an `Img` with
`at(x, y) -> [u8; 3]`, `diff_count`, `bbox`; `tests/glass.rs` already asserts
on rows of red-channel values). Every criterion below is checkable from one or
two captures. Values read from a capture are **sRGB-encoded 8-bit**, because
that is what the output framebuffer holds.

Three helpers belong in `tests/common/mod.rs` first:

```rust
impl Img {
    /// WCAG 2 relative luminance: sRGB decode, then 0.2126 R + 0.7152 G + 0.0722 B.
    pub fn luminance(&self, x: usize, y: usize) -> f64;
    /// WCAG 2 contrast ratio between two pixels: (Lhi + 0.05) / (Llo + 0.05).
    pub fn contrast(&self, a: (usize, usize), b: (usize, usize)) -> f64;
    /// Longest run of byte-identical pixels along a scan, per channel.
    pub fn max_run(&self, from: (usize, usize), to: (usize, usize)) -> [usize; 3];
}
```

### C1. Linear-light blur (section 2)

Scene: the existing `edge_scene` in `tests/glass.rs` — an opaque window whose
left half is black and right half white, with a glass window over the
boundary, tint and edge strength 0.

- **C1.1** The value at the step must be **>= 170 and <= 205** with
  `linear_blur = true` (correct answer 188), and 128 +- 4 with it false. Assert
  both and assert the difference is **>= 40 codes**. That is the whole change
  in one number.
- **C1.2** Monotonic: scanning the 80-px row across the step, no sample may be
  lower than the previous by more than 1 code (dither slack).
- **C1.3** The 10-90% width, measured on **luminance**, must be
  `1.9 * blur_radius * 2^blur_passes * 2^start * scale` device pixels +-15%,
  where `start` is the number of halvings of the chain's input (1 for window
  glass, 2 for chrome). At `radius 2.0, passes 2, start 1, scale 1.0` that is
  30.4 px: accept 26-35. For chrome glass at `start 2` it is 60.8 px: accept
  52-70. This is what keeps `blur_radius` meaning something. Re-derive the
  constant from a capture after Stage A rather than adjusting the test to
  pass.
- **C1.4** With `precision = "rgb10"` and a 256-step grey ramp as the
  backdrop, no banding: see C3.2.

### C2. Text contrast over any backdrop (section 3.3)

Scene: the dock up, with a client filling the dock strip, run three times
painting black, mid grey (128,128,128) and white.

- **C2.1** For each run, the dock label's lightest pixel against the glass
  immediately around it must give `contrast >= 4.5`. Today's build gives 1.04
  over white; the target gives 6.4.
- **C2.2** The chrome glass panel's maximum luminance anywhere inside it must
  be **<= 0.19** in all three runs. That is `luminance_max` 0.16 plus the
  tint's contribution, with 8-bit and dither slack. This enforces the ceiling
  directly rather than through its effect.
- **C2.3** Over the white backdrop, the glass must still vary: brightest minus
  darkest interior pixel **>= 4 codes**. Otherwise the compression has become
  a clamp and the material has stopped being glass.
- **C2.4** Over the white backdrop, the glass must be *bounded*, not merely
  dark: its mean luminance must be **>= 0.06**, so the ceiling is visibly
  responding to a bright backdrop rather than the tint doing all the work.

### C3. Banding and dither (section 2.5)

- **C3.1** On a 256-px vertical scan down the middle of the desktop
  background, `max_run` in the channel with the largest end-to-end delta must
  be **<= 6 px**. Undithered, the default background's red channel runs 232 px
  per code: this criterion fails today and should.
- **C3.2** The same ceiling on a 128-px scan inside a glass panel over a
  smooth backdrop.
- **C3.3** Dither must not be noise. With `background.mode = "solid"`, the
  standard deviation of a 64x64 block must be **<= 1.0 code** per channel.
  Triangular +-1 LSB plus quantisation gives about 0.5.
- **C3.4** Dither must be present at the boundaries without clipping: with a
  solid background at sRGB (1,1,1) and at (254,254,254), the block mean must
  be within 0.5 code of the requested value. This catches the asymmetry a
  2 LSB triangular dither has at 0.0 and 1.0.

### C4. Shadow, bevel and edge (sections 3.4, 4)

- **C4.1** A window on the plain background has a monotonically decreasing
  luminance profile outward from its edge over at least 25 device pixels, and
  the luminance 2 px outside the edge is **at least 20% lower** than the
  background 60 px away. The contact shadow exists and is a shadow, not a
  line.
- **C4.1b No step at the penumbra edge.** Along the same outward scan, within
  the outer third of the shadow footprint (`y + spread + 2 sigma` to
  `y + spread + 3 sigma`), the largest single-pixel luminance jump must be
  **<= 2 codes**. This is the criterion that would have caught the
  `smoothstep(-1.5 sigma, 1.5 sigma, d)` approximation section 4 rejects: that
  form hard-clamps a 6.7-percentage-point step into existence at exactly this
  point.
- **C4.2** The rim: the darkest pixel within 3 px outside the glass edge is at
  least **6 codes** below the local background. The edge is findable.
- **C4.3** Edge highlight falloff: the band's peak luminance at 10% of the
  window height is **>= 1.8x** its peak at 90%, matching `falloff = 0.45`
  within 8-bit slack.
- **C4.4** No black ring. Scanning the 3 device pixels just inside the glass
  edge all the way round, no pixel may be more than **8 codes** darker than
  the glass 12 px inside. This is the total-internal-reflection failure the
  bevel slope clamp prevents; without the clamp it is a hard, obvious ring.
- **C4.5** The displacement does something: with a window glass panel over a
  backdrop containing a straight high-contrast vertical line, the line's
  x position measured 4 px inside the glass edge must differ from its position
  measured 40 px inside by **>= 2 device pixels**. Over chrome glass, the same
  measurement must differ by **0 pixels** — chrome gets no displacement.

### C5. Temporal stability (section 9)

`--freeze-time` pins the background shader's clock and `--test-input move N X Y`
places windows, so this is deterministic.

- **C5.1 No crawl.** With `--freeze-time` and a static scene, two captures 10
  frames apart must be **byte-identical**. This catches a time-varying
  material noise and a dither seed that is not derived from the frozen clock.
  It is a strictly stronger assertion than the existing `diff_count` tests.
- **C5.2 Bounded flicker under motion.** Move a window by one device pixel
  between captures. For every pixel inside a glass region the change must be
  **<= 10 codes**. This is the criterion that *verifies* Bjorge's stability
  result on our chain rather than assuming it, and it is what would tell us we
  were wrong to refuse the Karis average.
- **C5.3 Fireflies.** Repeat C5.2 with a single sRGB (255,255,255) pixel in an
  otherwise black backdrop under the glass. Same bound. If this fails and C5.2
  passes, the refusal in section 1 is wrong and a partial Karis average on the
  first downsample is the documented fix [JIMENEZ-2014, slides 167-168].
- **C5.4** Moving a window must not change the dither of the background
  outside it.

### C6. Motion (section 5)

Needs a way to step N frames and capture (`--test-input frame N` alongside the
existing injection commands). Without it none of this is checkable and the
motion spec is decoration.

- **C6.1 Deceleration, as a measurable fact.** For any entry animation of
  nominal duration D, the fraction of total displacement completed at
  `0.25 D` must be **>= 0.65**. For `enter` = `(0,0,0,1)` the true value is
  69.1% and for `enter-emphasised` 83.2%; for every accelerating curve in
  section 5 it is under 10%. The threshold is 0.65 rather than 0.60 on purpose:
  `move` = `(0.2,0,0,1)` reaches 60.7%, so a 0.65 floor also catches the one
  mistake section 5.1 warns about — using M3 `standard` for an input response,
  where its zero initial velocity loses exactly the property being designed
  for. One assertion separates the families and encodes the decision rather
  than the curve.
- **C6.2 First-frame response.** At one frame (16.67 ms) after the input, the
  capture must differ from the capture at frame 0 by at least 200 pixels.
  "It moved on the next frame" is the property the whole section is about.
  `enter` covers 40.6% of a 200 ms animation in that frame; `exit` would cover
  1.3%.
- **C6.3 Exit is faster than entry.** Frames from input to within 1 device
  pixel of the settled state must be strictly fewer for the exit than for the
  matching entry.
- **C6.4 Reduced motion.** With `[motion] reduced = true`, `switcher.layout`
  must report `grid` and a switcher capture must show no perspective slant
  (the inverse of the existing flip-layout assertion in `tests/glass.rs`); and
  the background must be identical across 10 frames without `--freeze-time`,
  because `speed` is 0.
- **C6.5 No motion at all.** With `[motion] enabled = false`, the capture at
  frame 1 after a window open must have the same `bbox` as the capture at
  frame 20.
- **C6.6 Modal prompts do not overshoot.** The trusted overlay's bounding box
  must never exceed its settled size by more than 1 device pixel on any frame.

### C7. Frame budget (section 11)

- **C7.1** With 4 glass surfaces at 1856x1160, the effects pass stays
  **<= 6.0 ms** at p99 over 300 frames. Today it is 2.3-4.0 ms with no
  shadows, no bevel and no ceiling; the increment this document's additions are
  allowed is 2 ms. If they cost more, something gets cut.
- **C7.2** Frame interval p99 stays **<= 20 ms** on a 60 Hz output, which is
  what `compositor/README.md` already records for the two-context winit path
  (p50 16.67, p99 19-21). No regression.
- **C7.3** With the shared prepass on, the total effects-pass time for 8 glass
  surfaces must be **< 3x** the time for 1. The bandwidth model predicts 2.8x
  (62.2 MB against 21.9 MB); today the same comparison is 8x by construction,
  because every surface re-blurs. That is the scaling property of section 3.2,
  asserted directly, and it is the criterion the README's "cost grows with
  window count" becomes.
- **C7.5 Render-time variance, not just the mean.** Over 300 frames with 4
  glass surfaces, the standard deviation of the effects-pass time must be
  **<= 1.0 ms**, and no single frame may exceed 3x the median. A bursty effect
  spends input latency on every frame in a deadline-driven compositor, even
  the cheap ones [HUGL-PERF], so the variance is a first-class budget and not
  a diagnostic.
- **C7.4** All three need instrumentation that does not exist. There is no
  timing code in the compositor: `grep Instant::now` in `src/` finds the theme
  poll, the app-list refresh and the background clock. A `--frame-stats PATH`
  flag writing per-frame (scene, prepass, blur, glass, overlay, total)
  timings, with GPU timer queries where `GL_EXT_disjoint_timer_query` exists
  and wall clock otherwise, is a prerequisite for C7 and for section 11.

---

## 11. The frame budget, and what to measure first

The measured effects pass is 2.3-4.0 ms. The bandwidth-bound time for the same
work is 0.11 ms (section 2.3). The gap is 20-36x, so before optimising
anything here, find out where it goes.

And measure the *variance*, not only the mean. A compositor that misses its
deadline unpredictably starts compositing earlier to compensate, which costs
input latency on every frame including the cheap ones — KWin's own diagnosis,
with "KWin effects starting to render something heavy" named as one of the
causes [HUGL-PERF]. Criterion C7.5 is the bound.

The likely answer is per-pass overhead, and the code says why. `fx::blur::pass`
does, for each of `2 * blur_passes` passes: `r.bind(dst)` ->
`r.render(...)` -> one textured quad -> `f.finish()` -> `wait(r, sy)`. Each
`bind` is an FBO switch preceded by `egl.make_current`; each `finish` creates
a sync point; each `wait` inserts an EGL fence wait (GPU-side where
`GL_OES_EGL_sync` is present, a CPU block otherwise). For four glass surfaces
at `blur_passes = 3` that is **24 bind/render/finish/wait cycles per frame**,
plus one per scene composite step, to move 113 MB.

Ordered by expected gain over expected cost:

1. **Measure it.** `--frame-stats` (C7.4) and the `Img` helpers in section 10.
   Zero visual gain; everything below is guesswork without it.
2. **Shared downsampled backdrop prepass** (section 3.2): one full-to-half-to-
   quarter chain per frame, window glass blurring from the half and chrome
   from the quarter. 2.6-3.6x less traffic depending on surface count, 4.2x to
   17x lower per-surface marginal cost, and it is the change that fixes "cost
   grows with window count". Evidence-backed on both sides
   [SHATTERED-GLASS, STRUGAR-2014]. Visual cost: backdrop detail finer than
   2 or 4 device pixels, which a sigma-12 or sigma-24 blur has already
   destroyed.
3. **Drop the per-pass fence.** The chain is a strictly ordered sequence of
   draws into textures the same context then samples; GL already guarantees
   that ordering, so one sync at the end should do. Gain unknown until
   measured, but it is the only thing in the pipeline that costs 24 of
   anything per frame. Risk: exactly the kind of change that works on NVIDIA
   and breaks elsewhere. Needs a flag and a second host.
4. **Scissor the chain to each surface's rect plus the blur margin**
   (`1.95 * blur_radius * 2^blur_passes`, 36 device px at the specified
   radius). Gain proportional to the uncovered area: 2-4x for typical window
   sizes, on top of item 2. Cost: the viewport and UV mapping per level,
   fiddly but mechanical.
5. **Share one blur between non-overlapping glass surfaces.** Today each
   surface re-blurs, which is *correct* — a glass window above another sees the
   lower one's glass — but for surfaces that do not overlap each other it is
   waste. Blur once per layer of mutually non-overlapping surfaces. Note this
   is what Apple's `GlassEffectContainer` appears to be for: it "renders
   multiple glass elements into a combined effect" and exists "to achieve the
   best rendering performance" [APPLE-GLASSCONTAINER].
6. **Cap the chain's input resolution above a threshold.** Item 2 already does
   this at 4x; at 4K a further halving is free-looking and keeps the sigma in
   logical units. Section 3.2's table has the numbers.

Items 1-5 are optimisations of existing work, not new effects, and together
they are what buys the budget for the shadow, the ceiling and the bevel.
Decision 0010's sequence is ordered so they land before the effects that need
them.

---

## 12. References

Sorted by the tag used above. Primary sources are preferred; where a claim is
inference it says so in the text.

**Colour, precision, dither**

- [WCAG22] *Web Content Accessibility Guidelines (WCAG) 2.2*, W3C
  Recommendation, 12 December 2024. Relative luminance and contrast ratio
  definitions. https://www.w3.org/TR/WCAG22/#dfn-relative-luminance
- [LWV-1992] Stanley P. Lipshitz, Robert A. Wannamaker, John Vanderkooy,
  "Quantization and Dither: A Theoretical Survey", *Journal of the Audio
  Engineering Society* 40(5):355-375, May 1992.
  https://hajim.rochester.edu/ece/sites/zduan/teaching/ece472/reading/Lipshitz_1992.pdf
- [GJOEL-BANDING] Mikkel Gjoel (Playdead), "Banding in Games: A Noisy Rant",
  revision 5, undated (c. 2014-2016), 72 pp. https://loopit.dk/banding_in_games.pdf
  — the deck names Gjoel as sole author, carries no venue marking and no year;
  do not attribute it to GDC 2016.
- [LOTTES-2016] Timothy Lottes (AMD), "Advanced Techniques and Optimization of
  HDR/VDR Color Pipelines", GDC 2016.
  https://gpuopen.com/download/GdcVdrLottes.pdf
- [ULICHNEY-1993] Robert A. Ulichney, "The void-and-cluster method for dither
  array generation", *Proc. SPIE* 1913 (*Human Vision, Visual Processing, and
  Digital Display IV*), pp. 332-343, 1993.
- [WOLFE-VAC] Alan Wolfe, "Generating Blue Noise Textures With Void And
  Cluster", 25 June 2019.
  https://blog.demofox.org/2019/06/25/generating-blue-noise-textures-with-void-and-cluster/
- [CSS-FILTER] *Filter Effects Module Level 1/2*, W3C / CSSWG. "Filter
  functions must operate in the sRGB color space"; SVG filter *primitives*
  default to linearRGB. https://drafts.csswg.org/filter-effects-2/

**Blur and post-processing**

- [BJORGE-2015] Marius Bjorge (ARM), "Bandwidth-Efficient Rendering", SIGGRAPH
  2015 Mobile Graphics course. Slides and speaker notes.
  https://community.arm.com/cfs-file/__key/communityserver-blogs-components-weblogfiles/00-00-00-20-66/siggraph2015_2D00_mmg_2D00_marius_2D00_slides.pdf
  — the dual-filter kernels this code implements, the 49.78 dB vs 50.02 dB
  PSNR comparison, and the sub-pixel stability tests.
- [JIMENEZ-2014] Jorge Jimenez (Activision Blizzard), "Next Generation Post
  Processing in Call of Duty: Advanced Warfare", SIGGRAPH 2014 "Advances in
  Real-Time Rendering in Games" course.
  https://www.iryoku.com/next-generation-post-processing-in-call-of-duty-advanced-warfare/
  (slides are PPTX only). Slides 146-170 are the temporal-stability material.
- [GARCIA-2017] Kleber Garcia (EA/Frostbite), "Circular Separable Convolution
  Depth of Field", SIGGRAPH 2017 Talks, DOI 10.1145/3084363.3085022.
  https://history.siggraph.org/wp-content/uploads/2022/09/2017-Talks-Garcia_Circular-Separable-Convolution-Depth-of-Field.pdf
- [STRUGAR-2014] Filip Strugar (Intel), "An Investigation Of Fast Real-Time
  GPU-based Image Blur Algorithms", Intel Developer Zone, updated 2014-07-15.
  https://www.intel.com/content/www/us/en/developer/articles/technical/an-investigation-of-fast-real-time-gpu-based-image-blur-algorithms.html
  — Kawase at "1.5x to 3.0x less computation time" than an optimised separable
  Gaussian; a 5-pass filter with kernels 0,1,2,2,3 matches a 35x35 Gaussian
  "closely (but not perfectly)"; R9-290X reference timings of ~3 ms for a
  127x127 Gaussian at full resolution, 0.5 ms for 63x63 at half and 0.17 ms at
  quarter; and the admission that the pass counts and offsets "were chosen
  empirically" with no "deterministic way of generating Kawase kernels to match
  arbitrary sized Gauss filter". (The page's byline is blank; the attribution
  comes from Intel's companion video page, which spells the name "Philipe
  Strugar".)
- [PICOM-SHADOW] Yuxuan Shui, picom commit
  `5ac8b7fc562b393ab91021b149706a7dab060496`, "backend: gl: use gaussian blur
  for shadow".
  https://github.com/yshui/picom/commit/5ac8b7fc562b393ab91021b149706a7dab060496
  — the whole message: "dual kawase is fast but its blur size is coarse
  grained, which sometimes makes the shadow ugly."
- [PICOM-PARAMS] picom `generate_dual_kawase_params()`,
  `src/backend/backend_common.c` — a 20-row hand-tuned `strength_levels` table
  with no explanation, whose third field is commented "approximate gauss-blur
  with at least this radius and std-deviation", conflating radius with sigma.
  `expand = (1 << iterations) * 2 * ceilf(offset) + 1`.
- [WAYFIRE-BLUR] Wayfire `plugins/blur/kawase.cpp`,
  `wf_kawase_blur::calculate_blur_radius()` returns
  `pow(2, iterations_opt + 1) * offset_opt * degrade_opt` — a heuristic with no
  derivation and no sigma.
- Kawase's original (Masaki Kawase, GDC 2003, "Frame Buffer Postprocessing
  Effects in DOUBLE-S.T.E.A.L") is cited by [BJORGE-2015] but **no primary
  copy was located**. An archived deck is reported at
  `web.archive.org/web/20060909063116/http://www.daionet.gr.jp/~masa/archives/GDC2003_DSTEAL.ppt`
  — **unverified**.
- **Do not cite** arXiv:2512.04556 for anything about Kawase. Search engines
  attribute a sentence about the missing Gaussian-strength mapping to it; the
  paper does not mention Kawase or dual filtering at all.

**KDE / KWin**

- [HUGL-P4] Xaver Hugl, "HDR and color management in KWin, part 4: nonlinear
  blending", 2 November 2024.
  https://zamundaaa.github.io/wayland/2024/11/02/hdr-and-color-management-in-kwin-part-4.html
- [KWIN-6050] KWin MR !6050, Xaver Hugl, 4 July 2024, "backends/drm: do
  blending in gamma 2.2 space with color management too".
  https://invent.kde.org/plasma/kwin/-/merge_requests/6050
- [HUGL-P5] Xaver Hugl, "HDR and color management in KWin, part 5",
  6 November 2024. (The series is "HDR and color management in KWin", parts
  1-5; there is no post titled "Colorful Future".)
- [KWIN-D18377] KWin Phabricator D18377, Alex Nemeth, January 2019,
  "[effects/blur] Update blur to be more natural" — the review thread where a
  non-linear blur is identified as producing a dark band between contrasting
  colours. https://phabricator.kde.org/D18377
- [KDE-HIG-ANIM] KDE HIG, "Animations". **The live page no longer exists**;
  archived 2024-05-12 at
  https://web.archive.org/web/20240512155327/https://develop.kde.org/hig/style/animations/
  Origin: Phabricator D25343, davidedmundson, landed 7 January 2020.
- [KWIN-SCALE] KWin Scale effect (default window open/close):
  `defaultDuration = 200`, `OutCubic` on add, `InCubic` on close.
  https://github.com/KDE/kwin/blob/master/src/plugins/scale/package/contents/code/main.js
- [KWIN-GLIDE] KWin Glide effect, 160 ms default, with the curves set the
  opposite way round from Scale.
  https://github.com/KDE/kwin/blob/master/src/plugins/glide/glide.cpp
- [KWIN-ANIMKCM] KWin animation-speed KCM: factors `[4, 2, 1.5, 1, 0.75, 0.5,
  0]`, with "0 is a special case, which means 'instant speed / no
  animations'". https://github.com/KDE/kwin/blob/master/src/kcms/animations/ui/main.qml
- [KWIN-EFFECT] `Effect::animationTime`: `max(actualTime, 1)`, "at least 1ms,
  otherwise 0ms times can break some things".
  https://github.com/KDE/kwin/blob/master/src/effect/effect.cpp
- [KWIN-BLEND-SPACE] KWin `src/backends/drm/drm_output.cpp`:
  `constexpr TransferFunction::Type blendingSpace = TransferFunction::gamma22;`
  — and the blur shaders carry no `colormanagement.glsl` include and no `pow`,
  unlike `src/opengl/base.frag`. KWin blurs in gamma-2.2-encoded space by
  declaration.
- [KWIN-BLUR-SRC] KWin `src/plugins/blur/blur.{cpp,kcfg}` and
  `BlurEffect::initBlurStrengthValues()`. The artefact envelope comment
  (`minOffset` -> "blocky artifacts because of the downsampling";
  `maxOffset` -> "diagonal line artifacts because of the nature of the dual
  kawase blur algorithm"); the four `{minOffset, maxOffset, expandSize}` bands
  `{1,2,10} {2,3,20} {2,5,50} {3,8,150}`; `BlurStrength` default 15
  (= 4 iterations at offset 8); the `float`-to-`int` offset truncation; and the
  noise comment "the noise is useful to mask banding artifacts, which often
  happens due to the smooth color transitions in the blurred image", with a
  256x256 grayscale tile of `rand() % noiseStrength` blended
  `glBlendFunc(GL_ONE, GL_ONE)`.
- [KWIN-SDF] KWin `src/opengl/sdf.glsl`, added 2025-07-01 (Vlad Zahorodnii),
  Plasma 6.5 — Quilez's `sdRoundedBox` verbatim with per-corner radii in a
  `vec4`, antialiased with `fwidth`.
- [KWIN-SHADOW-XML] `org_kde_kwin_shadow` version 2, `src/protocols/shadow.xml`
  in plasma-wayland-protocols — a nine-tile bitmap protocol: eight `attach_*`
  requests each taking a `wl_buffer`, four `set_*_offset`, `commit`, `destroy`.
  The client ships pre-blurred edge and corner textures; the compositor tiles
  them.
- [ZAHORODNII-SHADOWS] Vlad Zahorodnii, "Server-side Drop Shadows",
  2026-07-16. https://blog.vladzahorodnii.com/2026/07/16/server-side-drop-shadows/
  — and its own update: "Automatic server-side drop shadows were reverted
  because that regressed the visuals of a reasonable number of apps. It appears
  like applications use normal toplevel windows to implement popups instead of
  relying on more appropriate window types."
- [KWIN-DROP-BLUR] KWin MR "plugins/blur: Drop contrast and kde-blur protocol
  support", Vlad Zahorodnii, 2026-02-13, shipped Plasma 6.7.0.
  https://invent.kde.org/plasma/kwin/-/merge_requests/8798
- [KWIN-BG-EFFECT] KWin MR 4890, "wayland: support ext-background-effect-v1",
  Xaver Hugl, merged 2026-01-28, `src/wayland/backgroundeffect_v1.{cpp,h}`,
  shipped Plasma 6.7.0.
- [HUGL-PERF] Xaver Hugl, "Fixing KWin's performance on old hardware",
  2024-06-25.
  https://zamundaaa.github.io/wayland/2024/06/25/fixing-kwin-perf-on-old-hardware.html
  — "render times are *very* volatile at times… by KWin effects starting to
  render something heavy…"; KWin "starts compositing much earlier if they're
  volatile".
- [KDE-507501] KDE Bugzilla 507501, "Implement a less demanding fake blur for
  lower powered systems so themes are still readable", filed 2025-07-26,
  CONFIRMED. https://bugs.kde.org/show_bug.cgi?id=507501 — on a Raspberry Pi 5
  blur "leads to extreme lag"; the request is for an implementation "based on
  e.g. pre-blurring the desktop wallpaper as a static texture".
- [KDE-507904] KDE Bugzilla 507904, WONTFIX.
  https://bugs.kde.org/show_bug.cgi?id=507904 — Nate Graham, 2025-08-12: "the
  blur has to be always on so that opaque surfaces can transition to being
  translucent without jarring transitions. The blur is basically a no-op when
  the surface is opaque, so there isn't a performance penalty." **The no-op
  claim is not visible in `BlurEffect::shouldBlur()`, which has no
  opacity-based early-out.** Treat it as a position, not a property.
- [KDE-453229] KDE Bugzilla 453229, "New setBlurRegion() API gives
  blocky/aliased blur region corners on Wayland HiDPI", Paul McAuley,
  2022-04-30, RESOLVED FIXED. https://bugs.kde.org/show_bug.cgi?id=453229 —
  the "Korners" bug: "you can… only construct a QRegion using integers", so an
  integer region cannot express a rounded corner at fractional scale.
- [KDE-HIG-ELEVATION] "Depth, Elevation and Shadows", KDE HIG,
  `content/hig/style/elevation.md` at commit
  `9cbaa88601e7557b0c07396e1d6e64bfddfec23c` — **deleted** in the 2024 HIG
  rewrite. "Shadow color: Black (#000000) / Shadow opacity: 100% / Window
  shadow size: 48px / Menu and tooltip shadows size: 20px", and shadows
  "should be horizontally-centered, but vertically offset, to provide the
  illusion that a light source is over the top of the screen". Note: the
  current KDE HIG contains **no** guidance on translucency, blur or opacity at
  all — all ten pages were grepped and there are zero matches.
- [KIRIGAMI-UNITS] Kirigami `Units`: `veryShortDuration 50`,
  `shortDuration 100` ("hover events"), `longDuration 200`,
  `veryLongDuration 400`, `humanMoment 2000`.
  https://invent.kde.org/frameworks/kirigami/-/blob/master/src/platform/units.cpp

**Apple**

- [WWDC25-219] "Meet Liquid Glass", WWDC25 session 219.
  https://developer.apple.com/videos/play/wwdc2025/219 — "Lensing"; "bends,
  shapes, and concentrates light"; "simulate a thicker, more substantial
  material"; the highlights layer; the light/dark flip rule for small vs large
  elements; the accessibility modifiers.
- [APPLE-MATERIALS] Human Interface Guidelines, *Materials*.
  https://developer.apple.com/design/human-interface-guidelines/materials —
  the regular variant "blurs and adjusts the luminosity of background content
  to maintain legibility"; the 35% dimming layer.
- [APPLE-GLASS-CLEAR] SwiftUI `Glass.clear` reference — the sample uses
  `.background(.black.opacity(0.3))`, i.e. 30%, against the HIG's 35%.
- [APPLE-GLASS-CUSTOM] "Applying Liquid Glass to custom views", SwiftUI.
  https://developer.apple.com/documentation/SwiftUI/Applying-Liquid-Glass-to-custom-views
  — "blurs content behind it, reflects color and light of surrounding
  content"; the performance warnings and `GlassEffectContainer` guidance.
- [APPLE-UIGLASS] `UIGlassEffect`.
  https://developer.apple.com/documentation/UIKit/UIGlassEffect
- [APPLE-GLASSCONTAINER] `UIGlassContainerEffect`.
  https://developer.apple.com/documentation/UIKit/UIGlassContainerEffect —
  "renders multiple glass elements into a combined effect".
- [APPLE-VISIONOS] visionOS materials: glass "limits the range of background
  color information".
- [APPLE-RM-CRIT] "Reduced Motion evaluation criteria", App Store Connect
  Help.
  https://developer.apple.com/help/app-store-connect/manage-app-accessibility/reduced-motion-evaluation-criteria
  — names parallax, animated blur, depth of field, multi-axis and multi-speed
  motion, spinning, vortex, carousels, ongoing motion.
- [APPLE-RM-USER] "Reduce motion on your iPhone", Apple Support.
  https://support.apple.com/en-us/111781 — "screen transitions and effects use
  the dissolve effect instead of zoom or slide effects".
- [APPLE-SPRING] SwiftUI `Spring`.
  https://developer.apple.com/documentation/swiftui/spring — the two worked
  examples `(0.5, 0.3) -> (1.0, 157.9, 17.6)` and
  `(1, 100, 10) -> (0.63, 0.5)`; `bounce` 0 is critical damping. The
  `k = m(2*pi/duration)^2` relation is **not** documented and is inferred from
  those examples.
- [WWDC23-10158] "Animate with springs", WWDC23 session 10158.
  https://developer.apple.com/videos/play/wwdc2023/10158/ — continuity,
  velocity hand-off, retargeting, the settling-duration warning, the bounce
  guidance.
- [APPLE-EDR] "Performing your own tone mapping", Metal.
  https://developer.apple.com/documentation/metal/performing-your-own-tone-mapping
  — EDR anchors 1.0 at the current SDR level.
- [SHATTERED-GLASS] AlexStrNik, *ShatteredGlass* — "Deconstruction of MacOS 26
  LiquidGlass effects", Swift, created 14 June 2025.
  https://github.com/AlexStrNik/ShatteredGlass — layer and filter archaeology
  against SwiftUI's private Core Animation classes (**not** Metal
  disassembly). Reconstructed hierarchy `SwiftUI.SDFLayer -> CABackdropLayer
  -> CASDFLayer -> CALayer -> CASDFElementLayer`; filters
  `CAFilter(type:"glassBackground")`, `CASDFOutputEffect`,
  `CASDFGlassHighlightEffect`, `CAFilter(type:"vibrantColorMatrix")`; the
  `glassBackground` parameters `inputInnerRefractionAmount`/`Height`,
  `inputOuterRefractionAmount`/`Height`, `inputRefractionDistance0`/`1`,
  `inputRefractionOpacity`, `inputBlurDistance0..4`/`inputBlurOpacity0..4`;
  backdrop `setScale(0.25)` and `setTracksLuma(true)`. **The class and
  parameter names are Apple's; the numeric values are the author's
  reconstruction and the README says tuning is ongoing.** Cite the names, not
  the numbers.
- [MR-26.1] Juli Clover, "iOS 26.1 Beta 4 Lets Users Control Liquid Glass
  Transparency with New Toggle", MacRumors, 20 October 2025.
  https://www.macrumors.com/2025/10/20/ios-26-1-liquid-glass-toggle/ — the
  Clear/Tinted switch. The article does not quote Apple's own wording; **no
  Apple release note for it was found**.

**Reimplementations of the Liquid Glass look**

- [ZENN-GLASS] orectic, "Creating 'Refractive Glass' with WebGL Shaders:
  Beyond the Limits of CSS backdrop-filter", Zenn, 16 June 2026.
  https://zenn.dev/orectic/articles/liquid-glass-webgl-refraction?locale=en —
  the clearest verified recipe: Quilez rounded-box SDF, gradient normal,
  `band = clamp(-d/uEdge, 0, 1)`, `curve = pow(1 - band, 1.6)`,
  `disp = grad * curve * uRefraction * maxDisp` with `maxDisp = uEdge * 0.9`
  and `uRefraction = 0.7`, 12-tap directional blur, fixed-light specular
  `pow(dot(n, lightDir), 12)`, `coverage = 1 - smoothstep(-1, 1, d)`. **No
  Snell's law anywhere.**
- [SHATTERED-GLASS-NOTES] Source-comment findings carried forward from the
  wider survey of reimplementations: the bevel slope must be clamped
  (`bevelMaxSlope ~ 1.74`) or total internal reflection paints a hard black
  ring; a `smoothstep` bevel reads as plastic where a Lame curve reads as
  glass; the normal comes from the gradient of a bevel **height field**, not of
  the SDF; the blur footprint `foot = max(max(length(dFdx(qc)),
  length(dFdy(qc))), 0.25*px)` must be capped because rim compression exceeds
  20x. Primary repository: zanwei/glass-icon-reveal. **These are third-party
  source comments, not Apple statements, and the constants are configuration
  values rather than physics.**
- [OVERSHIFTED-LG] OverShifted/LiquidGlass.
  https://github.com/OverShifted/LiquidGlass — "renders a squircle with a
  shader that approximates a signed distance field to emulate refraction. With
  some blur, noise and a bit of glow, it ends up looking somewhat like glass."
- [KUBE-GLASS] kube.io, "Liquid Glass in the Browser", 4 September 2025.
  https://kube.io/blog/liquid-glass-css-svg/ — the canonical
  `feDisplacementMap` write-up, and the source of the 8-bit displacement-map
  limit of -128..127 px per axis that a compositor is not bound by.
- Deliberately **not** cited: `medium.com/@aghajari` (existence not
  corroborated); the Codrops "Infinite Liquid Glass Grid" article (403, byline
  and body unread — attribute any of its constants to the third-party rebuild
  `codyhxyz/infinite-liquid-glass` instead); `davidpconqueror/liquid-glass-fx`
  (created 2026-11-06, one star; its README advertises dual-surface Snell
  refraction while the shader hard-codes `ior 1.5` with a thin-lens
  approximation and no `refract()` call — not established, and its 9-tap
  Gaussian weights and Blinn-Phong setup are config values, not physics).

**Microsoft**

- [MS-ACRYLIC] "Acrylic material", Microsoft Learn, page updated 2026-10-02.
  https://learn.microsoft.com/en-us/windows/apps/design/style/acrylic — the
  recipe caption "background, blur, exclusion blend, color/tint overlay,
  noise"; "GPU-intensive... automatically disabled when a device enters
  Battery Saver mode"; the layering and accent-text warnings; the 14px note.
  **No numeric blur radius or noise opacity appears in this or any Microsoft
  doc, including the 2020 revision in the public docs repo.**
- [MS-ACRYLIC-SRC] `microsoft/microsoft-ui-xaml`, `AcrylicBrush.h` /
  `AcrylicBrush.cpp` (MIT). `sc_blurRadius = 30.0f`,
  `sc_noiseOpacity = 0.02f`, `sc_exclusionColor{26,255,255,255}`,
  `sc_saturation = 1.25f` — the last two are declared and **referenced
  nowhere**. The "Shadow-friendly Luminosity-based Recipe" comment:
  "In 19H1, the Acryic recipe was altered to improve integration of Acrylic
  surfaces with Shadows using a Luminosity effect... A Luminosity effect is now
  used to reduce contrast in the Acrylic source and minimize the shadow's
  contribution to Acrylic output." The luminosity blend's purpose: "The purpose
  is to reduce contrast - allowing colors to come through, but blocking sharp
  differences in brightness". Auto-derivation: `minLuminosityOpacity = 0.15`,
  `maxLuminosityOpacity = 1.03`. Tint suppression: `whiteMaxOpacity = 0.45`,
  `midPointMaxOpacity = 0.90`, `blackMaxOpacity = 0.85`. Note the in-source
  warning that the names of `BlendEffectMode::Luminosity` and `::Color` are
  flipped.
- [MS-MICA] "Mica material", Microsoft Learn, updated 2026-07-22.
  https://learn.microsoft.com/en-us/windows/apps/design/style/mica — "Mica is
  specifically designed for app performance as it only samples the desktop
  wallpaper once"; the inactive-window fallback and its stated focus
  rationale. **Mica's blur parameters are documented nowhere.**
- [MS-HDR] "Use DirectX with Advanced Color on high/standard dynamic range
  displays", Microsoft Learn.
  https://learn.microsoft.com/en-us/windows/win32/direct3darticles/high-dynamic-range
  — DWM's canonical composition space is linear scRGB at fp16; "FP16 has
  excellent precision for linear gamma blend operations"; fp16 "doubles GPU
  bandwidth and memory consumption"; scRGB 1.0 is D65 white at 80 nits; tone
  mapping is "inherently a lossy process... there's no single standard
  algorithm".
- [FLUENT-DURATIONS] `@fluentui/tokens` `durations.ts`: 50, 100, 150, 200,
  250, 300, 400, 500 ms.
  https://github.com/microsoft/fluentui/blob/master/packages/tokens/src/global/durations.ts
  — the Fluent 2 design site publishes no numbers and no curve values.

**Protocols**

- [EXT-BG-EFFECT] `ext-background-effect-v1`, wayland-protocols staging, MIT.
  https://gitlab.freedesktop.org/wayland/wayland-protocols/-/raw/main/staging/ext-background-effect/ext-background-effect-v1.xml
  — "The initial value for the blur region is empty"; "A NULL wl_region removes
  the effect"; "The blur region is double-buffered state, and will be applied
  on the next wl_surface.commit"; "The blur algorithm is subject to compositor
  policies."
- [EXT-BG-MR] wayland-protocols MR !272, "staging: add ext-background-effect-v1",
  Xaver Hugl, created 2024-01-12, **merged 2025-05-26**, with ACKs recorded
  from "Smithay/Cosmic @Drakulix" and "KDE @Zamundaaa".
  https://gitlab.freedesktop.org/wayland/wayland-protocols/-/merge_requests/272
  — "roughly based on the org_kde_kwin_blur protocol, which has been in use
  since 2015… made more generically though". Its predecessor MR !43
  ("ext-blur: New protocol", Vlad Zahorodnii, 2020-08-25) is still open and was
  superseded.

**Corners, SDFs and shadows**

- [IQ-SDF2D] Inigo Quilez, "2D distance functions".
  https://iquilezles.org/articles/distfunctions2d/ — `sdRoundedBox`, labelled
  "exact", with per-corner radii in a `vec4`; the form `glass.frag` uses. Code
  snippets on the site are MIT. (The page refuses generic clients; a browser
  user-agent is required.)
- [IQ-ROUNDBOX] Inigo Quilez, "Smooth Rounded Boxes".
  https://iquilezles.org/articles/roundedboxes/ — the derivation that a
  circular arc is not G2 against a straight edge (`c''(1) = -2` versus 0), and
  "this mismatch in the normal regime is what makes the lighting respond
  non-smoothly"; the dismissal of generalized ellipses as "pretty awkward
  looking shapes, without any parallel sides"; "we cannot solve general quintic
  equations in close form with the instructions we have in the GPUs today… we'll
  use a binary search", implemented with 12 iterations; and the per-shape
  normalisation constants (circular `sqrt(2)-1`, cubic `2/3`, cosine `2/pi`).
- [IQ-ELLIPSE] Inigo Quilez, "Distance to an ellipse".
  https://iquilezles.org/articles/ellipsedist/ — the analytic root finder "is
  both expensive and not very stable… returns wrong values sometimes"; the
  Newton alternative uses 5 iterations.
- [IQ-DISTANCE] Inigo Quilez, "Approximating the distance to implicits".
  https://iquilezles.org/articles/distance/ — `de = abs(v)/length(g)` as the
  fix for a non-unit gradient, with "this method evaluates f four extra times,
  which makes it more expensive" and "when analytical gradients can be
  computed, it is strongly recommended to use them instead".
- [HG-SDF] Korndoerfer, Keinert, Ganse, Saenger, Ley, Burkhardt, Spuler,
  Heusipp, "HG_SDF: A glsl library for building signed distance functions",
  2015. https://mercury.sexy/hg_sdf — the variable-exponent `fGDF` carries
  "This is slow and does not produce correct distances"; the stated discipline
  is "stay Lipschitz continuous… don't have any distance gradient larger
  than 1".
- [RDNA-ARCH] AMD, *RDNA Architecture* (public deck), GPUOpen, slide 18:
  "transcendental instructions are 1/4 rate (like GCN)".
  https://gpuopen.com/download/RDNA_Architecture_public.pdf
- [CSS-BORDERS-4] *CSS Borders and Box Decorations Module Level 4*, W3C
  Editor's Draft. https://drafts.csswg.org/css-borders-4/ —
  `x^(2K) + y^(2K) = 1`, `round` = `superellipse(1)`, `squircle` =
  `superellipse(2)`, "the traditional 'squircle' uses a K of 2". CSS WG
  resolutions 2025-01-29.
- [FURSE-2018] Daniel Furse, "Desperately seeking squircles", Figma Blog,
  **2018-04-03**. https://www.figma.com/blog/desperately-seeking-squircles/ —
  "the curvature of a squircle's perimeter is continuous, whereas a rounded
  square's is not"; "for all choices of n… there is a small but systematic
  discrepancy when compared with the real icon shape"; "the iOS corner only
  uses three" Beziers; and the decisive admission about the exactly-continuous
  family: "the variation over the full range, from xi = 0 to 1 only makes a
  very subtle difference in the corner shape… **This is a barely noticeable
  effect despite its nice mathematical properties**", against his own
  requirement that "changing the smoothing should actually do something
  perceptible". **The article never uses the term "G2"** — that framing is
  this document's.
- [PAINTCODE-2013] PixelCut, "Drawing Code for iOS 7 Rounded Rectangles",
  2013-11-18. https://www.paintcodeapp.com/news/code-for-ios-7-rounded-rectangles
  — curve extracted from the real `UIBezierPath` with `CGPathApply`: three
  cubic Beziers plus two short line segments per corner, corner influence
  reaching `1.52866483 * r` along each edge, radius clamped to
  `min(w,h) / 2 / 1.52866483`. "The old design produced a subtle but noticeable
  'hump' in places where arc touches some of the straight lines."
- [FRASER-CSSWG] Simon Fraser (Apple/WebKit), w3c/csswg-drafts#10993,
  2024-10-20.
  https://github.com/w3c/csswg-drafts/issues/10993#issuecomment-2424305155 —
  "the continuous rounded rect is a Bezier curve with four points per corner…
  their influence on the flat sides extents about 1.5x of the corner radius".
- [IMPELLER-RSE] Flutter, `impeller/geometry/round_superellipse_param.cc`
  (BSD) — looks the superellipse degree up from the size-to-radius ratio, `n`
  from 2.0 at ratio 2.00 to 6.43 at ratio 5.00, joining a superellipse arc to a
  circular arc per octant. Independent confirmation that one exponent cannot
  track Apple's family.
- **Do not cite** a Bjango article about the squircle: the full article index
  at https://bjango.com/articles/ was enumerated and no such article exists.
  Marc Edwards' superellipse claim was a 2013 tweet, later retracted; neither
  is fetchable. The "quintic" in circulation traces to an `n ~= 5` superellipse
  guess that John D. Cook himself labels "rumor has it"
  (https://www.johndcook.com/blog/2018/02/13/squircle-curvature/) and to
  Quilez's note that a cubic corner *profile* yields a degree-5 stationarity
  equation.
- [WALLACE-2015] Evan Wallace, "Fast Rounded Rectangle Shadows",
  madebyevan.com, 2015 (code CC0; the page is undated, the site index lists the
  collection as "Shader Articles (2015)" and the earliest Wayback capture is
  2015-03-16).
  https://madebyevan.com/shaders/fast-rounded-rectangle-shadows/ —
  "unfortunately there's no closed-form solution for a rounded rectangle drop
  shadow"; exact erf along one axis, four-sample midpoint integration along the
  other, support clamped to +-3 sigma, quad padded by `3.0 * sigma`. His erf
  polynomial is Abramowitz & Stegun 7.1.27 with the cubic term dropped
  (measured maximum error ~8e-4, against ~4.7e-4 for the full form — **do not
  quote A&S's 5e-4 for his version**). Note also that the page's *prose*
  listing of the plain-rectangle `boxShadow` has `upper - point` where the
  demo shader on the same page has `point - upper`; the demo form is the
  correct one, and the listing returns identically zero. Prior art he credits:
  Michael Herf, "Fast Shadows on Rectangles", stereopsis.com, 2001.
  http://stereopsis.com/shadowrect/
- [LEVIEN-2020] Raph Levien, "Blurred rounded rectangles", 2020-04-21.
  https://raphlinus.github.io/graphics/2020/04/21/blurred-rounded-rects.html —
  the SDF-plus-sigmoid approach this document specifies, with the corner radius
  combined as `sqrt(r_c^2 + 1.25 * r_b^2)`, "motivated by the rule for the
  probability distribution of a sum of Gaussians, with the constant factor
  chosen empirically"; the `compute_erf7` polynomial; "I like using reciprocal
  square root — it is particularly well supported in SIMD and GPU"; the clean
  statement that "a boxcar is the difference of two step functions… and the
  convolution of a step and a Gaussian is erf"; the warning that this does not
  extend to rounded rectangles, "which aren't separable in this way"; and the
  scoping note "Evan's existing solution is almost certainly good enough for
  most practical uses". Timings in his "Sigmoid functions for audio"
  (2018-09-05) are CPU-only.
- [QT-RECTSHADOW] "RectangularShadow — Fast rectangle shadows in Qt 6.9",
  Qt blog. https://www.qt.io/blog/rectangularshadow-fast-rectangle-shadows-in-qt-6.9
  — an SDF implementation of the Levien family, no cached shadow textures, 30
  dynamically changing shadows at a steady 60 fps on a MediaTek MT6762 /
  PowerVR GE8320. Read via a summariser, not raw HTML.
- [FERNANDO-2005] Randima Fernando (NVIDIA), "Percentage-Closer Soft Shadows",
  ACM SIGGRAPH 2005 Sketches.
  https://developer.download.nvidia.com/shaderlibrary/docs/shadow_PCSS.pdf —
  "soft shadows provide valuable cues about the relationships between objects,
  becoming sharper as objects contact each other and more blurry (softer) the
  further they are apart"; `wPenumbra = (dReceiver - dBlocker) * wLight /
  dBlocker`. Cited for the observation, not the machinery.
- [M1-ENVIRONMENT] "Environment", Material Design 1.
  https://m1.material.io/material-design/environment.html — "key lights create
  directional shadows, while ambient light creates soft shadows from all
  angles. Shadows in the material environment are cast by these two light
  sources"; "on the web, shadows are depicted by manipulating the y-axis only";
  "desktop resting elevation is 2dp below the listed values to accommodate
  mouse and non-touch environments". The m2 and m3 sites are JS-rendered and
  serve no body text to a fetcher, so this static page and the source files
  below are the citable record.
- [MDC-ELEVATION] `@material/elevation`,
  `packages/mdc-elevation/_elevation-theme.scss`, (c) 2017 Google Inc.
  https://raw.githubusercontent.com/material-components/material-components-web/master/packages/mdc-elevation/_elevation-theme.scss
  — `$umbra-opacity: 0.2`, `$penumbra-opacity: 0.14`,
  `$ambient-opacity: 0.12`, and the full dp table in
  `offset-x offset-y blur spread` order (e.g. 6 dp: umbra `0 3px 5px -1px`,
  penumbra `0 6px 10px 0`, ambient `0 1px 18px 0`).
- [M3-ELEVATION] `androidx.compose.material3.tokens.ElevationTokens`:
  Level0 0, Level1 1, Level2 3, Level3 6, Level4 8, Level5 12 dp — half MD2's
  range. The normative line is in `Surface`'s KDoc: "to prevent shadow creep,
  only apply shadow elevation when absolutely necessary, such as when the
  surface requires visual separation from a patterned background."
- [AOSP-DIMENS] AOSP `frameworks/base/core/res/res/values/dimens.xml`,
  "Lighting and shadow properties": `light_y 0dp`, `light_z 500dp`,
  `light_radius 800dp`, `ambient_shadow_alpha 0.039`,
  `spot_shadow_alpha 0.19`.
- [COMEAU-2021] Joshua Comeau, "Designing Beautiful Shadows in CSS",
  2021-09-13. https://www.joshwcomeau.com/css/designing-shadows/ — "every
  shadow on the page should share the same ratio"; "if we layer 5 shadows, our
  device has to do 5x more work!… it's probably a bad idea to try animating a
  layered shadow". See also Tobias Ahlin, "Smoother & sharper shadows with
  layered box-shadows", 2019-09-19,
  https://tobiasahlin.com/blog/layered-smooth-box-shadows/
- **Negative result worth recording:** no credible non-vendor source names the
  tight-contact-plus-wide-ambient two-layer model as a perceptual principle,
  and Apple publishes no shadow blur, offset or opacity numbers anywhere in the
  HIG. The two-layer model in section 4 is derived, not cited. And no
  perceptual study of G1-versus-G2 corner continuity at UI scale appears to
  exist.

**Motion, easing, latency**

- [M3-TOKENS] Material Design 3 motion tokens, verified from two Google-owned
  source files that agree exactly:
  https://github.com/material-components/material-web/blob/main/tokens/versions/v0_192/_md-sys-motion.scss
  and
  https://github.com/androidx/androidx/blob/androidx-main/compose/material3/material3/src/commonMain/kotlin/androidx/compose/material3/tokens/MotionTokens.kt
  — `standard (0.2,0,0,1)`, `standard-decelerate (0,0,0,1)`,
  `standard-accelerate (0.3,0,1,1)`, `emphasized-decelerate (0.05,0.7,0.1,1)`,
  `emphasized-accelerate (0.3,0,0.8,0.15)`, `legacy (0.4,0,0.2,1)`; durations
  50-1000 ms. Note `emphasized` equals `standard` in Google's shipping tokens;
  the widely repeated claim that it is "really" a two-segment piecewise curve
  **could not be verified from any Google source**.
- [MDC-ANIM] MDC-Web `_animation.scss`: deceleration `(0,0,0.2,1)`, standard
  `(0.4,0,0.2,1)`, acceleration `(0.4,0,1,1)`, sharp `(0.4,0,0.6,1)`.
  https://github.com/material-components/material-components-web/blob/master/packages/mdc-animation/_animation.scss
- [AOSP-INTERP] AOSP framework interpolators: `fast_out_slow_in`,
  `linear_out_slow_in`, `fast_out_linear_in`.
  https://github.com/aosp-mirror/platform_frameworks_base/blob/master/core/res/res/interpolator/
- [CSS-EASING] *CSS Easing Functions Level 1*, W3C CR Draft, 13 February 2023.
  https://www.w3.org/TR/css-easing-1/ — `ease (0.25,0.1,0.25,1)`,
  `ease-in (0.42,0,1,1)`, `ease-out (0,0,0.58,1)`,
  `ease-in-out (0.42,0,0.58,1)`; `linear` is the identity, not a bezier.
- [ADW-SPRING] libadwaita `SpringParams` and `SpringAnimation`.
  https://gnome.pages.gitlab.gnome.org/libadwaita/doc/main/struct.SpringParams.html
  — the damping-ratio table and "useful for deceleration at the end of
  gestures".
- [ADW-ANIM] libadwaita `adw_get_enable_animations()`.
- [GNOME-WM] gnome-shell `js/ui/windowManager.js`:
  `SHOW_WINDOW_ANIMATION_TIME = 150` (`EASE_OUT_EXPO`),
  `DIALOG_SHOW_WINDOW_ANIMATION_TIME = 100` (`EASE_OUT_QUAD`),
  `DESTROY_WINDOW_ANIMATION_TIME = 150` (`EASE_OUT_QUAD`),
  `MINIMIZE_WINDOW_ANIMATION_TIME = 400`.
  https://github.com/GNOME/gnome-shell/blob/main/js/ui/windowManager.js
- [JOTA-2013] Ricardo Jota, Albert Ng, Paul Dietz, Daniel Wigdor, "How fast is
  fast enough? A study of the effects of latency in direct-touch pointing
  tasks", CHI '13, pp. 2291-2300, DOI 10.1145/2470654.2481317.
  https://www.tactuallabs.com/papers/howFastIsFastEnoughCHI13.pdf — land-on
  latency JND 20-100 ms, mean 64 ms (sd 24); the "window of opportunity"
  between 20 ms (100% of participants) and 40 ms (85%).
- [DEBER-2015] Jonathan Deber, Ricardo Jota, Clifton Forlines, Daniel Wigdor,
  "How Much Faster is Fast Enough? User Perception of Latency & Latency
  Improvements in Direct and Indirect Touch", CHI '15, pp. 1827-1836, DOI
  10.1145/2702123.2702300.
  https://www.tactuallabs.com/papers/howMuchFasterIsFastEnoughCHI15.pdf —
  **indirect** (mouse-like) JNDs: dragging 55 ms, tapping 96 ms, combined
  75 ms; direct touch 11 / 69 / 40 ms.
- [KAARESOJA-2014] Topi Kaaresoja, Stephen Brewster, Vuokko Lantz, "Towards
  the temporally perfect virtual button", *ACM TAP* 11(2), Article 9, 2014,
  DOI 10.1145/2611387. https://eprints.gla.ac.uk/104653/ — point of subjective
  simultaneity ~32 ms for visual feedback; guideline range 30-85 ms.
- [DRAGICEVIC-2011] Pierre Dragicevic, Anastasia Bezerianos, Waqas Javed,
  Niklas Elmqvist, Jean-Daniel Fekete, "Temporal Distortion for Animated
  Transitions", CHI '11, pp. 2009-2018, DOI 10.1145/1978942.1979233.
  https://cs.au.dk/~elm/pdf/timedistort.pdf — slow-in/slow-out outperformed
  constant and fast-in/fast-out on object tracking; opens by noting the
  guideline previously had "no empirical data" behind it.
- [HARRISON-2010] Chris Harrison, Zhiquan Yeo, Scott E. Hudson, "Faster
  progress bars: manipulating perceived duration with visual augmentations",
  CHI '10, pp. 1545-1548, DOI 10.1145/1753326.1753556.
  https://www.figlab.com/research/2010/faster-progress-bars
- [NIELSEN-1993] Jakob Nielsen, "Response Times: The 3 Important Limits",
  1993. https://www.nngroup.com/articles/response-times-3-important-limits/ —
  the 0.1/1/10 s limits, restated from Miller, R. B. (1968), "Response time in
  man-computer conversational transactions", AFIPS FJCC 33:267-277, DOI
  10.1145/1476589.1476628. **These are design heuristics, not a measured
  motion-onset threshold**; use [DEBER-2015] and [KAARESOJA-2014] for that.
- [MQ5] *CSS Media Queries Level 5*, `prefers-reduced-motion`, §12.1.
  https://www.w3.org/TR/mediaqueries-5/ — no durations, no list of forbidden
  effects.
- [WCAG-233] WCAG 2.1 SC 2.3.3 "Animation from Interactions", **Level AAA**.
  https://www.w3.org/WAI/WCAG21/Understanding/animation-from-interactions.html
- [CRAIG-CSSWG] James Craig, public-css-archive, 21 December 2023 — "entirely
  stopped animations aren't necessary for vestibular accommodation".
  https://lists.w3.org/Archives/Public/public-css-archive/2023Dec/0676.html
- [WEBKIT-RDM] James Craig, "Responsive Design for Motion", WebKit blog,
  15 May 2017. https://webkit.org/blog/7551/responsive-design-for-motion/ —
  the canonical post and the origin of the media feature. **It does not
  recommend cross-fade and does not use the word.**
- [HEAD-2015] Val Head, "Designing Safer Web Animation For Motion
  Sensitivity", A List Apart, 8 September 2015 (updated 30 April 2020).
  https://alistapart.com/article/designing-safer-web-animation-for-motion-sensitivity/
  — "animation that involves only non-moving properties, like opacity, color,
  and blurs, are unlikely to be problematic".

**Contrast**

- [WCAG-695] w3c/wcag issue #695, "Contrast Ratio Math and Related Visual
  Issues", opened by Myndex 14 April 2019, transferred to w3c/wcag3 #192 in
  January 2025, **still open**. https://github.com/w3c/wcag/issues/695 — note
  that the thread's headline "179:1" example is the ratio with the 0.05 flare
  term *removed*; WCAG itself gives 5.19:1 for that pair.
- [WALLER-2022] Sam Waller (University of Cambridge EDC), www-archive,
  9 June 2022 — "I have not thus far been able to find any traceable empirical
  or theoretical evidence to support this formula".
  https://lists.w3.org/Archives/Public/www-archive/2022Jun/0003.html
- [WCAG-1705] w3c/wcag issue #1705 (converted to Discussion #3853 and locked
  10 May 2024) — the 1.5x multiplier's "rational basis, just not maybe a great
  one", and 4.5:1 chosen partly so `#767676` on white would pass.
- [APCA] Andrew Somers / Myndex, `apca-w3`, Beta 0.1.9, constant set "4g".
  https://raw.githubusercontent.com/Myndex/apca-w3/master/src/apca-w3.js —
  `mainTRC 2.4`; `normBG/normTXT 0.56/0.57`; `revTXT/revBG 0.62/0.65`;
  `blkThrs/blkClmp 0.022/1.414`; `scaleBoW/scaleWoB 1.14`; offsets 0.027;
  `loClip 0.1` (the documentation page says 0.001 — trust the code).
- [APCA-NUT] "APCA in a Nutshell", git.apcacontrast.com — Lc 90 preferred for
  body text, Lc 75 minimum for columns of body text, Lc 60 for non-body text,
  Lc 45 for headlines, Lc 30 absolute minimum for any text, Lc 15 the floor
  for discernible non-text.
- [APCA-LICENSE] apca-w3 `LICENSE.md` — "W3 License for Compliant Code Only";
  "All Rights Reserved"; "Patent(s) pending"; licensed "for web-delivered and
  web-based content only"; prohibits "uses which are not specific to web based
  content presented on self-illuminated displays or devices".
- [WCAG3] *WCAG 3.0* Working Draft, 10 September 2026.
  https://www.w3.org/TR/wcag-3.0/ — "the contrast algorithm used in WCAG 3 is
  yet to be determined"; **APCA is not mentioned**.
- [ROSELLI-2026] Adrian Roselli, "WCAG 3 Contrast as of April 2026".
  https://adrianroselli.com/2026/04/wcag3-contrast-as-of-april-2026.html — the
  exploratory-content removal (w3c/silver PR #663, January 2023).
- [APCA-REVIEW] W3C public-silver and w3c-wai-gl list traffic, 3-4 August
  2023 — Somers' named sources (Whittaker, Lovie-Kitchin, Bailey, Legge) and
  the absence of a published granular bibliography; Chris Lilley's criticism
  of the single 2.4 exponent. **No independent validation study of the Lc
  scale or its thresholds was found.**
- [ACT-AFW4F7] W3C ACT rule "Text has minimum contrast" (proposed).
  https://www.w3.org/WAI/standards-guidelines/act/rules/afw4f7/proposed/ —
  "There is no clear method for determining legibility when some but not all
  pixels have sufficient contrast."
- [NNG-TEXT-IMAGES] Aurora Harley, "Ensure High Contrast for Text Over
  Images", Nielsen Norman Group, 18 October 2015, reviewed 13 January 2026.
  https://www.nngroup.com/articles/text-over-images/ — design for the
  worst-case background and text placement.
- [FIREFOX-COLOR] Mozilla Firefox, `toolkit/modules/Color.sys.mjs`:
  `CONTRAST_BRIGHTTEXT_THRESHOLD = Math.sqrt(1.05 * 0.05) - 0.05` (~0.1791).

**HDR and colour management**

- [WP-CM] `wp_color_management_v1`, wayland-protocols staging; released in
  1.41 (2025-02-17). https://gitlab.freedesktop.org/wayland/wayland-protocols
  — only the perceptual render intent is mandatory; the protocol never
  mentions blending.
- [WESTON-16] Marius Vlad (Collabora), "Weston 16: HDR-ready, improved
  debugging and DRM backend features", 16 July 2026 — HDR remains
  "experimental due to the lack of tone mapping".
  https://www.collabora.com/news-and-blog/news-and-events/weston-16-hdr-ready-improved-debugging-and-drm-backend-features.html
- [BT2408] ITU-R Report BT.2408-9 (03/2026), "Guidelines for operational
  practices in high dynamic range television production".
  https://www.itu.int/pub/R-REP-BT.2408 — HDR reference / graphics white at
  203 cd/m2 (58% PQ, 75% HLG), as the mean of a distribution spanning roughly
  123-345 cd/m2; "BT.2100 specifies that 1.0 should map to reference white...
  facilitates the mixing of different types of content onto a computer screen
  as may occur in windowed display systems".
- [DRM-KMS] Linux kernel DRM/KMS documentation.
  https://docs.kernel.org/gpu/drm-kms.html — `HDR_OUTPUT_METADATA`,
  `Colorspace`, the CRTC `DEGAMMA_LUT`/`CTM`/`GAMMA_LUT` pipeline, and the
  statement that userspace must parse the EDID to detect sink capabilities.

**This repository**

- `compositor/README.md`, "Glass and the switcher", "Shell chrome", "Trusted
  path", "Known limits of the backends".
- `compositor/src/fx/{blur.rs,mod.rs,theme.rs,switcher.rs}`,
  `compositor/src/{render.rs,text.rs,scale.rs,shell/}`.
- `contracts/theme/aurora-glass.toml`,
  `contracts/theme/shaders/{down,up,glass,card}.frag`,
  `compositor/src/fx/background.frag`.
- `contracts/decisions/0003-compositor-protocol.md`,
  `0007-ambient-and-gesture-grants.md`, `0009-agent-effect-policy.md`,
  `0010-visual-system.md`.
- Smithay at the pinned revision `65aa86fa5236e41775c313408102a5d5c2d1e2ca`:
  `src/backend/renderer/gles/{mod.rs,format.rs,shaders/mod.rs}` — the format
  table, `Capability::_10Bit`, `Offscreen<GlesTexture>::create_buffer`,
  `compile_custom_texture_shader`, `GlesFrame::with_context`, and the
  `glBlendFunc(GL_ONE, GL_ONE_MINUS_SRC_ALPHA)` blend state.

**Derived in this document, not cited**

- The sRGB error table and the 2.34x / 1.22-stop figure (2.1).
- The intermediate-format precision comparison, including the 11.7-bit figure
  and the gamma-2.0 store's uniform 0.20% relative step (2.3).
- Every bandwidth and tap figure (2.3, 3.2), from the pass structure in
  `fx/blur.rs`.
- The three dual-Kawase sigma identities (3.2), calibrated against a
  simulation of that same file, because no published derivation exists.
- The prepass cost table and the 2.6-3.6x and 4.2x/17x figures (3.2).
- The contrast tables, the `Y/(1+Y/Ymax)` policy and its floor derivation
  (3.3). No published engineering method for a worst-case contrast bound over a
  variable backdrop was found; the arithmetic is the whole justification.
- The 12 logical px deviation-reach figure for corners (3.6), from PaintCode's
  verified `1.52866483 * r`.
- The two-layer shadow model and the `alpha_linear = 1 - (1-alpha)^2.4`
  conversion (4). Material's three-layer table is the source for the geometry
  ratios; the merge to two and the linear alphas are ours.
- The bezier first-frame progress table and the spring constants (5), the
  latter verified against both of Apple's documented worked examples.

Each is arithmetic over a published formula or a simulation of
`compositor/src/fx/blur.rs`, and each should be re-derived rather than trusted
if the code changes. Where a number is a guess rather than a derivation —
`window.tint.strength = 0.10`, the shadow alphas, `[font] gamma` — the text
says so at the point it appears.
