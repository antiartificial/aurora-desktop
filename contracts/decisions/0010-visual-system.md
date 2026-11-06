# 0010. Visual system: a split blending space, two glass materials, ease-out motion

- **Status:** proposed
- **Date:** 2026-11-06

## Context

Phase 1's look moved into the compositor and Phase 8's shell chrome landed on
top of it: dual-Kawase blur, an Oklab theme with hot reload, a glass layer
under translucent windows, a Flip 3D switcher, a flow-field background, a
compositor-drawn dock and cosmic-text. The effects pass costs 2.3-4.0 ms per
frame at 1856x1160 on an RTX 4090, and the cost grows per glass window because
each one re-blurs the whole scene.

The plan's Phase 8 wants a theme engine that consumes
`contracts/theme/aurora-glass.toml`, and nothing has yet said what that theme
should *contain*. The parameters in it today are the ones Phase 1 needed,
chosen by eye. They are not a design system: one glass material for every
surface, no shadows, no motion, no type scale, and no stated contrast
obligation — which means no way to tell whether a change made the desktop
better or worse.

Three specific problems force a decision rather than a style guide.

**The pipeline blurs in the wrong space.** `compositor/README.md` states it:
"Blending and blur run on sRGB-encoded values (not linear light; Oklab is used
only for the theme's colour tokens)". A blur is a weighted average and light
averages linearly, so averaging encoded values is wrong in one direction,
always too dark. Averaging black and white gives code 128, which is 21.4% of
full luminance where the right answer is 50%: the midtone is 2.34x too dark, a
loss of 1.22 stops, at exactly the pixel where a bright thing bleeds into a
dark one. That is why the current glass reads as a grey film rather than a lit
pane, and the 2019 review of KWin's blur effect describes the same artifact
from the other side — a non-linear blur produces a visible dark band between
contrasting colours.

**Chrome glass cannot keep text legible.** The dock draws labels at
`text_oklab = [0.97, 0, 0]` over `mix(backdrop, tint, 0.22)` with a light
tint. Computed WCAG 2 contrast: 13.2:1 over black, 3.0:1 over mid grey, and
**1.04:1 over white**. A white window, wallpaper or document behind the dock
makes the labels disappear. The W3C's own ACT rule for text contrast concedes
there is no standard method here — "there is no clear method for determining
legibility when some but not all pixels have sufficient contrast" — so this is
a policy the compositor has to choose. Both Apple and Microsoft suppress
backdrop luminance in some form; Aurora has nothing.

**Cost grows per glass surface, and the growth is not bandwidth.** 113 MB of
blur traffic per frame at 1008 GB/s is 0.11 ms against a measured 2.3-4.0 ms.
Whatever costs 2-4 ms is not memory traffic, and there is no frame-time
instrumentation in the tree with which to find out: `grep Instant::now` in
`compositor/src/` finds the theme poll, the app-list refresh and the
background clock, and nothing that measures a frame.

`docs/visual-system.md` is the design system. This record decides its spine,
and the research behind it is cited there rather than repeated here.

## Decision

### 1. The blending space is split, deliberately, and written down

**Blur runs in linear light. Client translucency does not.** These are
different operations and they get different spaces:

| operation | space |
|---|---|
| blur | linear light |
| glass tint, luminance ceiling, bevel, shadow alpha | linear light |
| client surface translucency over the scene | encoded (gamma ~2.2), as today |
| final quantisation | encoded, with dither |

This is not the obvious "do it all in linear" answer, and the reason is that
KWin already tried that and reverted it. Plasma 6.0 chose linear blending
because it "was considered the most 'correct' thing"; Plasma 6.2 switched to a
gamma 2.2 encoding, because of the per-frame fullscreen shadow-buffer copy, the
fp16 buffer that 8- or 10-bit linear forces ("you get visible banding in the
dark areas"), and — decisively — that the cost meant linear could only be
enabled for HDR or ICC displays, so "a semi-transparent surface would look a
lot more transparent just because you enabled HDR... causing the very problem
we wanted to avoid". KWin blurs in gamma 2.2 by declaration today
(`constexpr TransferFunction::Type blendingSpace = TransferFunction::gamma22;`),
and as far as this research found **no shipping Wayland compositor blurs in
linear light.**

Aurora departs from the field, for specific reasons rather than an appeal to
correctness: KWin's costs were about the blending space for the whole frame,
not a chain of offscreen targets that already exists; decision 2 below removes
the fp16 requirement; the consistency argument is about client alpha, which we
are *not* linearising; and Hugl's own footnote is the licence — "if you need
some exactly specific blending behavior in your application, it's best if you
do it yourself."

**Scope: only the intermediate targets inside `fx` change.** The output
framebuffer stays 8-bit and encoded. `render_and_capture` still renders the
scene, serves queued captures from that framebuffer, and only then draws the
trusted overlay. Capture exclusion is still by order. Nothing in decision 0003's
trusted-path properties is touched.

**The transfer functions are applied in our own shaders.** The pinned Smithay
(`65aa86fa…`) has no sRGB support in `src/backend/renderer/gles/`: no
`SRGB8_ALPHA8`, no `GL_FRAMEBUFFER_SRGB`. So `down.frag` decodes and
`glass.frag` encodes, using the exact IEC 61966-2-1 piecewise functions
(0.04045 / 0.0031308, 12.92, 1.055, 2.4). No gamma-2.2 approximation: a `pow`
at these pixel counts is free and an approximation would make the acceptance
criteria fuzzy.

**Oklab is for authoring and interpolation, never blending.** Theme tokens stay
Oklab; `fx/theme.rs` gains `oklab_to_linear`, which is `oklab_to_srgb` minus
the final encode. No shader does arithmetic in Oklab.

**Dither is triangular-PDF, +-1 LSB, added after the encode and immediately
before quantisation**, per Lipshitz/Wannamaker/Vanderkooy and Gjoel's graphics
restatement, with a lerp down to a 1 LSB uniform dither at the 0.0 and 1.0
boundaries and the three channels dithered independently. `background.frag`
upgrades from its single hash; `glass.frag` and `card.frag` gain it. The seed
may vary per frame only because it derives from the clock `--freeze-time` pins.

### 2. Precision: 10 bits holding `sqrt(linear)`

Matching 8-bit sRGB's darkest step in *linear* storage needs 11.7 bits, so
8-bit linear is unusable and 10-bit linear is marginal in the darks — exactly
the banding KWin reports. The fix is KWin's own insight applied to a blur
chain: a gamma encoding "allocates more numbers to darker parts of the image",
so 10 bits suffice. Since a blur must average *linear* values, the encoding
cannot stay in place during the average, so store `sqrt(linear)` — gamma 2.0,
decode is one multiply, encode is one `sqrt` — and decode and re-encode per
pass.

**`Fourcc::Abgr2101010` holding `sqrt(linear)`, at the same 4 bytes per pixel
as today.** Its relative step is a uniform `2/1023 = 0.20%`, which is
sub-sRGB-code everywhere; 10-bit *linear* would be 1.6 codes at the theme's
dark background and 3.2 codes near black. Per-tap cost is three multiplies and
one `sqrt(vec3)` on write.

`Offscreen<GlesTexture>` accepts `Abgr2101010` and `Abgr16161616f` gated on
`Capability::_10Bit`, which Smithay grants for GLES >= 3.0 (this host reports
3.2). A GLES2 context falls back to `Abgr8888` and **must log a warning**, not
degrade silently. `[glass] precision = "fp16" | "rgb10" | "srgb8"` exposes all
three; `"srgb8"` reproduces today's behaviour for comparison.

Two accepted limits: `RGB10_A2` is unsigned normalised, so it clamps at 1.0 —
no overbright, no bloom headroom, no HDR — and its alpha is two bits, which is
enough only because the chain carries the opaque scene.

### 3. One shared, downsampled backdrop prepass

Blur the scene **once per frame**, not once per glass surface. One prepass
produces a half- and a quarter-resolution copy (the second derived from the
first); window glass chains from the half, chrome glass from the quarter.

This is evidenced on both sides. Apple's backdrop layer is configured
`setScale(0.25)`, found by layer archaeology against SwiftUI's private Core
Animation classes. Intel's reference timings put a 63x63 Gaussian at 0.5 ms at
half resolution and 0.17 ms at quarter against ~3 ms for 127x127 at full. KDE
has a confirmed bug asking for exactly this for low-power hardware, because on
a Raspberry Pi 5 blur "leads to extreme lag".

At 1856x1160 and 4 bytes per pixel: the prepass is 13.5 MB once per frame, a
window chain 6.7 MB per surface and a chrome chain 1.7 MB, against 28.3 MB per
surface today. Totals go from 56.5 to 21.9 MB at one window plus the dock
(2.6x), and from 226.1 to 62.2 MB at seven windows plus the dock (3.6x). The
**per-surface marginal cost falls 4.2x for a window and 17x for chrome**, which
is the property the README's "cost grows with window count" becomes.

**Radii are chosen to sit inside KWin's documented artefact envelope.** Its
strength-table generator records, per iteration count, a `minOffset` below
which you get "blocky artifacts because of the downsampling" and a `maxOffset`
above which you get "diagonal line artifacts because of the nature of the dual
kawase blur algorithm"; for two iterations that band is 2.0 to 3.0. So:
`blur_passes = 2` and `blur_radius = 2.0` for both materials, which gives
sigma 12 full-resolution device pixels for window glass and 24 for chrome
(8.3 and 16.6 logical at this host's 1.45 scale).

Note the consequence: a downsampled prepass **sets a floor on sigma**. Chrome
glass cannot have a narrow blur without moving to the half-resolution input.
That is accepted; chrome's job is to suppress the backdrop.

Also recorded: the theme file's rule of thumb is wrong. "Visible blur extent is
roughly `blur_radius * 2^blur_passes` pixels" understates the one-sided support
by about 1.95x. The correct identities, calibrated against a simulation of
`fx/blur.rs` because **no published derivation exists** (Intel says its own
values "were chosen empirically"; every shipping implementation uses a
hand-tuned table and they disagree), are in the design doc, and must be
re-measured from a capture after the linear-light change.

### 4. Two glass materials

`[glass]` splits into `[glass.window]` and `[glass.chrome]`, with the existing
top-level keys kept as the shared fallback so today's theme file stays valid.

- **Window glass** sits under client windows, carries no compositor text, has
  no luminance ceiling, keeps a light cool tint, and gets the bevel
  displacement of decision 5.
- **Chrome glass** sits under the dock, the switcher backdrop and the trusted
  overlay, carries compositor-drawn text, and **has an enforced luminance
  ceiling**. It gets no displacement, because displacing backdrop samples under
  a glyph steepens the gradient the glyph sits on.

**The ceiling is a bounded compression, not a clamp.** In linear light, before
tinting: `Yb = dot(b, vec3(0.2126,0.7152,0.0722)); b *= 1/(1 + Yb/Ymax)`.
Scaling the linear triple keeps the backdrop's hue, so chrome still takes the
colour of what is behind it; the Reinhard form asymptotes to `Ymax` rather than
flattening every bright backdrop to one value.

Derived rather than chosen: with
`chrome.tint.oklab = [0.38, -0.005, -0.020]` (linear Y 0.055),
`chrome.tint.strength = 0.45` in linear light and
`chrome.luminance_max = 0.16`, worst-case glass luminance is
`0.16*0.55 + 0.055*0.45 = 0.1127`, so contrast against text at Oklab L 0.97 is
**5.9:1 for every possible backdrop**. Over white it reads sRGB (87,90,93) at
6.4:1 instead of today's (237,241,247) at 1.04:1. The general form and the
solution for a 7:1 floor are in the design doc.

Window glass keeps its token and drops `tint.strength` from 0.22 to **0.10**.
**0.10 is a starting point, not a derived value.** A linear mix toward a tint
of linear luminance 0.513 adds far more light than the same number did in
encoded space, and no single strength reproduces today's look (0.055-0.085 over
the dark background, 0.18 over mid grey). Note also that the tint strengths
published by web reimplementations do not transfer, because CSS filter
functions operate in sRGB by specification. The number must be set by eye and
then locked by a capture test, and the commit must say so.

**Precedent, attributed accurately.** The usual summary of Microsoft's
luminosity layer is wrong: the shipping `AcrylicBrush.cpp` says it was added
"to improve integration of Acrylic surfaces with Shadows", because a
`ThemeShadow` cast by an Acrylic surface was "visible through it as a dark
blur". The public docs still attribute legibility to an exclusion blend, which
is the pre-19H1 recipe the shipping code no longer applies. The mechanism does
compress backdrop lightness and that does help text, but citing Microsoft as
having added it *for* text contrast misstates the record. Apple's HIG says the
regular Liquid Glass variant "blurs and adjusts the luminosity of background
content to maintain legibility", with no numbers anywhere.

**WCAG 2 is the tested gate; APCA is not shipped.** WCAG 2's formula has no
traceable empirical derivation and its 4.5:1 threshold was reverse-fitted to
admit `#767676` on white. APCA is a better model and its polarity asymmetry is
the right idea. It is not shipped because its licence is "W3 License for
Compliant Code Only", reserves all rights, states "Patent(s) pending" and
licenses use "for web-delivered and web-based content only" — a question for a
lawyer, not a judgement call; because it is not in WCAG 3, whose draft says the
contrast algorithm is "yet to be determined"; and because for Aurora's
light-on-dark polarity WCAG 2 is the *more conservative* gate, so using it
costs nothing in strictness and buys an unambiguous number a capture test can
assert.

### 5. Bevel and edge, called a displacement

Four changes to `glass.frag`'s edge, in order of value: a 1-device-pixel
**outer rim darkening** (`rim.strength = 0.35`), which is the cheapest
legibility win in the document because it is the difference between an edge you
can find and one you cannot; the existing top-to-bottom highlight falloff moved
out of the shader into `edge.falloff = 0.45`; a **bevel height field** whose
gradient gives the normal; and a bounded **screen-space displacement** of the
backdrop sample, on window glass only.

The normal comes from the gradient of the bevel *height*, not of the SDF — the
SDF gradient is a unit vector with no profile. The height profile is a Lame
curve, `h(u) = (1 - (1-u)^n)^(1/n)` with `u = clamp(-d/bevel_width, 0, 1)` and
`n = 3`, and **the slope must be clamped** (`bevel_max_slope = 1.74`, about 60
degrees): uncapped, the rim normal goes near-tangent and a total-internal-
reflection term paints a hard black ring around every window. A `smoothstep`
profile instead of the Lame curve reads as plastic. Both pitfalls come from
source comments in published reimplementations, not from Apple.

The displacement is
`uv += n * pow(1 - clamp(-d/refract_width, 0, 1), 1.6) * refract_strength /
texsize`, with `refract_width = 10.0` and `refract_strength = 6.0` logical px.
The falloff exponent and the magnitude-as-a-fraction-of-edge-width come from
the clearest published reimplementation. GLSL ES 1.00 has `dFdx` only with
`GL_OES_standard_derivatives`, so use the analytic gradient of `rbox`.

**It is called a displacement because that is what it is**, and Aurora will not
attempt ray-marched refraction. Apple documents the perceptual goal ("Lensing";
the material "dynamically bends, shapes, and concentrates light"; at larger
sizes it "simulate[s] a thicker, more substantial material") and documents the
mechanism nowhere. Its public API exposes only `style`, `tintColor`,
`isInteractive` and a container `spacing` — no IOR, no thickness, no light
direction. Its private filter is parameterised as refraction *amount* and
*height* over a quarter-resolution backdrop capture driven by a signed distance
field, with a five-stop blur ramp indexed by distance. Every verifiable
reimplementation reaches the look by displacing a captured backdrop in screen
space. No disassembly-grounded account has been published and volumetric
refraction has never been demonstrated or claimed by anyone. A
quarter-resolution backdrop is in any case incompatible with refracting a
thickness: you cannot refract detail you did not sample.

The reconstructed class and parameter *names* above are Apple's; the numeric
values in the project that recovered them are its author's reconstruction, with
tuning ongoing. This record cites the names, not the numbers, and makes no
claim about how Apple's material is built.

**Rejected:** chromatic aberration (Apple documents no dispersion; at 6 px it
reads as a rendering fault) and specular highlights from a fixed fake light
(Apple's lighting "responds to device motion", which a desktop lacks; the
existing edge falloff already implies a light more cheaply).

### 6. Three elevation levels, two shadows, drawn analytically

No shadow exists today. Three levels: windows and the dock at 1, switcher cards
and popups at 2, the trusted overlay at 3. Each is a contact and an ambient
shadow; `x` offset is always 0.

**Two layers, not Material's three**, because a third evaluation is 50% more
cost for a difference below 8-bit quantisation at these sizes, and layering
costs linearly. The geometry ratios come from Material's published table
(umbra `y ~= e/2` with a small blur and negative spread — literally a contact
shadow; penumbra `y ~= e`, blur `~= 1.5e`; ambient small `y`, blur `~= 2e`),
with umbra and penumbra merged. The physical justification for the pair is
contact hardening: "soft shadows provide valuable cues about the relationships
between objects, becoming sharper as objects contact each other and more blurry
the further they are apart" (Fernando, PCSS 2005) — the observation, not the
machinery. No non-vendor source names the two-layer model as a perceptual
principle, so it is recorded here as derived.

**Alphas are linear-light coverage, and the conversion is mandatory.** A shadow
composited in encoded space at alpha 0.3 leaves `0.7 v`, which is `0.7^2.4 =
0.42` of the light: it blocks 58% while the number says 30%. To port any CSS or
Material alpha: `alpha_linear = 1 - (1 - alpha_encoded)^2.4`, which maps
Material's 0.20 / 0.14 / 0.12 to 0.41 / 0.30 / 0.26.

**Drawn as an SDF plus an erf, in the same draw as the glass.** A blurred
rounded rectangle has no closed form. Aurora uses Levien's construction — the
rounded-box SDF with the corner radius inflated as
`sqrt(r_corner^2 + 1.25*sigma^2)`, mapped through an `erf7` sigmoid — because
we already compute that SDF, it is one evaluation with no loop, and Qt shipped
the same family in 6.9 with 30 dynamic shadows at a steady 60 fps on a
PowerVR GE8320. Wallace's alternative (exact erf along one axis, four-sample
integration along the other, clamped to +-3 sigma) is the other credible
option.

**Explicitly rejected: `1 - smoothstep(-1.5*sigma, 1.5*sigma, d)`.** It is the
obvious cheap substitute and it is wrong in a visible way: the Gaussian CDF is
still at ~0.067 and ~0.933 at -1.5 and +1.5 sigma, so a smoothstep over that
range hard-clamps a 6.7-percentage-point step into existence at both ends of
the penumbra — precisely where a soft shadow should fade out. Widening to
+-3 sigma trades that for a mid-range shape error, because smoothstep has zero
slope at its endpoints and the Gaussian CDF does not. `erf7` is a handful of
ALU operations and one `inversesqrt`. Criterion C4.1b in the design doc is what
would catch this.

**And not through the blur chain**, which is the mistake picom had to undo:
"dual kawase is fast but its blur size is coarse grained, which sometimes makes
the shadow ugly."

### 7. Motion: ease-out for arrival, ease-in for departure

**Anything arriving or responding to input uses a decelerating curve.** An
ease-out starts at maximum velocity, so the surface moves on the first frame
after the input; an ease-in is nearly stationary for its first frames and reads
as latency regardless of total duration. This is arithmetic, not taste:
measured as the fraction of total displacement completed in the first frame of
a 200 ms animation at 60 Hz, M3 emphasised-decelerate covers 57.6%, M3
standard-decelerate 40.6%, and CSS `ease-in` 1.2%. The spread is about 150x.

**Specified curves**, with the trap named: `enter` is M3
`standard-decelerate (0,0,0,1)`; `enter-emphasised` is M3
`emphasized-decelerate (0.05,0.7,0.1,1)`; `exit` is M2
`accelerate (0.4,0,1,1)`; `move` is M3 `standard (0.2,0,0,1)`; `fade` is
linear. **M3 `standard` must not be used for input responses**, which is the
obvious mistake when adopting the M3 tokens wholesale: for a cubic bezier the
initial velocity is `y1/x1`, so `(0.2,0,0,1)` is literally stationary at onset
and covers only 9.9% in the first frame. It is correct only for
visible-to-visible moves, where the surface is already on screen and continuity
matters more than onset.

**Anything leaving uses an accelerating curve, and this is not a
contradiction.** An exit has no input to feel responsive to — the input already
happened and was acknowledged by the thing now departing — so accelerating away
reads as decisive and clears the screen sooner, where a decelerating exit
spends its last frames almost gone. Exits are also shorter than entries. This
is KDE's rule and it is in KWin's shipping Scale effect (`OutCubic` on add,
`InCubic` on close, 200 ms). It is **not** universal: GNOME Shell uses ease-out
for destroy too, and KWin's own Glide effect sets the curves the other way
round from its Scale effect. Aurora follows KDE's rule knowing it is a real
disagreement.

**Durations** sit on the grid the platforms use (Kirigami 50/100/200/400,
Fluent 50-500, M3 50-1000 in 50 ms steps) and none exceeds 200 ms: hover 100,
press 50/100, window open 200 and close 150, popup 100/100, switcher 200/150,
overlay rise 150 and fall 100, theme reload 0. The budget comes from the
latency-perception literature rather than the 100 ms folk rule: for the
indirect input a desktop has, the measured JND for added latency is ~55 ms
dragging and ~96 ms tapping (Deber et al., CHI 2015), and visual press feedback
is perceived as simultaneous up to ~32 ms (Kaaresoja et al., TAP 2014). An
ease-in curve on a 200 ms animation manufactures 30-60 ms of "nothing visibly
happened", squarely inside that range.

**Springs where a transition can be interrupted or inherits a velocity**, and
specified as Apple does, `(duration, bounce)`, with
`k = m(2*pi/duration)^2`, `zeta = 1 - bounce`, `c = 2*zeta*sqrt(km)`. That
mapping reproduces both of Apple's documented worked examples exactly, and the
`k` relation is cited as consistent with them rather than as documented. The
switcher selection forces this: Alt held with Tab pressed repeatedly retargets
before the previous move finishes, and "springs are the only type of animation
that maintains continuity both for static cases and cases with an initial
velocity" (WWDC23 session 10158). **Anything modal is critically damped or
over-damped**: an approval prompt that bounces looks like a toy, and looking
serious is a security property for that surface.

**Honesty requirement, recorded so it does not get dropped:** the claim
"ease-out feels faster than ease-in at equal duration" is designer consensus,
not a measured finding, and the one directly on-point study of easing-curve
shape favours slow-in/slow-out — for its task, which was tracking one target
among distractors in a one-second system-initiated transition (Dragicevic et
al., CHI 2011, whose own opening notes that the slow-in/slow-out guideline had
"no empirical data" behind it either). The two reconcile: that study's mechanism
is predictability, explicitly assuming "the user is not expecting the animation
to happen", which is the opposite of an input-triggered response. So the rule is
ease-out for input-initiated arrival, supported by the first-frame arithmetic
and the latency literature; ease-in-out for system-initiated multi-object
transitions the user must track. **Nothing in the repository may claim that
research shows ease-out reads faster.**

**Reduced motion** is targeted at what the guidance actually names. The spec
contains no durations and no list of forbidden effects; WCAG 2.1 SC 2.3.3 is
**Level AAA** and requires that interaction-triggered motion be *disableable*;
and the feature's own author states that "entirely stopped animations aren't
necessary for vestibular accommodation". Apple's qualifying criteria name
parallax, **animated blur**, depth of field, multi-axis and multi-speed motion,
spinning, vortex and ongoing motion — and plain translation or scale of a
single surface is **not** on that list. So `[motion] reduced = true`: the
switcher falls back to the `grid` layout (plane shifting, multi-axis);
`background.speed` goes to 0 (peripheral motion); window open and close keep
their translate and scale with the scale distance halved; springs become their
settled value; cross-fades survive at 100 ms. Separately,
`[motion] enabled = false` is a true-zero tier, for which there is shipping
precedent in both KWin (animation factor 0, "instant speed / no animations",
with a 1 ms floor "otherwise 0ms times can break some things") and libadwaita.

Reduced motion does **not** touch transparency or blur. That is
`[glass] enabled`, which exists and is tested. Conflating them would mean
claiming an accessibility property nobody measured.

### 8. Measured, or it did not happen

Every claim above has a criterion in `docs/visual-system.md` section 10,
written against the existing capture-decoding test idiom, plus three helpers
(`Img::luminance`, `Img::contrast`, `Img::max_run`) and two compositor flags
(`--frame-stats PATH`, `--test-input frame N`) that do not exist yet. The
load-bearing ones: a >= 40-code difference at a black-to-white blur midpoint
(C1.1); a 4.5:1 contrast floor for the dock label over black, mid grey and
white (C2.1) with the chrome panel's maximum luminance <= 0.19 (C2.2) and still
varying by >= 4 codes (C2.3); a <= 6 px ceiling on identical-value runs across
the background gradient (C3.1); no step at the penumbra edge (C4.1b); no black
ring inside the glass edge (C4.4); byte-identical captures 10 frames apart
under `--freeze-time` (C5.1); >= 0.60 of the displacement completed at a
quarter of the duration (C6.1); and a <= 6.0 ms p99 effects pass with a <= 1.0 ms
standard deviation at four glass surfaces (C7.1, C7.5).

### 9. What is deferred or refused, and why

- **A linear scene texture (fixing client translucency). Refused for now, not
  merely deferred.** It is hard here — `WaylandSurfaceRenderElement` draws
  through Smithay's built-in `texture.frag` and `Element::draw` has no slot for
  a custom program, so it needs a vendored patch importing 8-bit client buffers
  as `GL_SRGB8_ALPHA8`, gated so the winit direct-fallback and plain
  `render_output` paths do not double-decode — *and* it is the thing KWin
  reverted. Client alpha is authored for encoded-space blending. Revisit only
  with evidence that real clients look better, not because it is more
  "correct".
- **HDR and wide gamut.** Deferred until, in order: a documented blending space
  (decision 1), >=10-bit intermediates with correct dither (decision 2), a
  >8-bit output and capture path, display capability discovery (the kernel puts
  EDID parsing in userspace; `wl_output` carries no colour information here),
  `wp_color_management_v1` at its minimum feature set, and tone mapping last —
  which is where Weston stopped, shipping HDR as "experimental due to the lack
  of tone mapping". Notably **linear compositing is not a prerequisite**: the
  protocol never mentions blending, Microsoft composites in linear fp16 scRGB
  and KWin deliberately does not, and both ship.
- **Free-form progressive blur. Refused.** The two naive forms are
  structurally wrong (a sharp/blurred cross-fade leaves the sharp image's high
  frequencies as a ghost; a per-pixel tap offset is not a convolution, which is
  the depth-of-field bleeding artifact). The correct form is per-pixel level
  selection over the pyramid the chain already builds — but GLSL ES 1.00 has no
  `textureLod` without an extension, the levels are separate textures, Smithay's
  custom programs bind exactly one `tex`, and nothing in this design wants the
  effect.
- **One bounded case is accepted, last in the sequence: a blur level derived
  from the local UV compression the displacement produces.** Where the bevel
  bends the sample coordinates, a screen pixel maps to a larger patch of
  backdrop, so a point sample aliases. The footprint is computable from the
  analytic Jacobian of the displacement, and it must be **capped**, because at
  the rim the compression exceeds 20x and an uncapped level reaches content the
  rim never sees. This is the same relationship Apple's private filter
  expresses as a distance-indexed blur ramp. The real cost is plumbing: two
  samplers, which means binding texture unit 1 by hand through
  `GlesFrame::with_context`, with Smithay's own warning that the renderer's GL
  state is an implementation detail.
- **Squircles / G2 corners. Refused, with the strongest reason being empirical
  and from the people who shipped them.** Figma's first candidate was the
  genuinely curvature-continuous family and its author reports it "is a barely
  noticeable effect despite its nice mathematical properties"; Figma abandoned
  it and shipped a spikier curvature profile *because* that produced more
  visible shape change. The pro-continuity arguments are about shading — a
  circular arc's `c''(1) = -2` against a straight edge's 0, so "the lighting
  responds non-smoothly" — which partly applies to us because decision 5 adds
  a bevel normal; but at `corner_radius = 12` logical px the deviation region
  reaches ~18 px with sub-pixel normal deviation, against 38-61 px at icon
  scale. And the cheap p-norm shortcut breaks the exact analytic antialias
  (`cov = clamp(0.5 - d, 0, 1)`) that makes the current corners clean, while the
  exact version needs a 12-iteration binary search. The dock's app tiles
  (~10 px radius on a 36 px square, in a content-cached CPU canvas) are the one
  place to try it. Section 3.4's 1-D bevel profile does use a Lame curve, where
  it costs two `pow` and visibly matters.
- **Blue-noise dither.** Needs a texture upload and another slot; hash-based
  triangular ships.
- **`ext-background-effect-v1` for the per-window opt-out.** Merged in
  wayland-protocols staging 2025-05-26 with ACKs from Smithay/COSMIC and KDE,
  implemented in KWin 6.7.0, superseding `org_kde_kwin_blur` (which KWin removed
  in the same release). When a per-surface opt-out is built, it is this
  protocol, and the rounded clip and material choice stay compositor-side —
  because an integer region cannot express a rounded corner at a fractional
  scale, which is exactly how KDE's "Korners" bug happened. Deferred to
  whatever slice gives windows properties.
- **Glyph antialiasing gamma.** Linearising `Canvas::over` is correct and will
  make light-on-dark text render *heavier*, which may not be wanted. The
  structure is decided (`alpha_linear = pow(coverage, text_gamma)`, blended in
  linear light); `[font] gamma` defaults to 1.0 and the right value is an open
  question.
- **Frame-time instrumentation.** Does not exist. `--frame-stats PATH` is item
  1 of the sequence.

### 10. Prioritised implementation sequence

Cheapest high-impact first. Costs are for 1856x1160 with four glass surfaces.
Every one should be *measured*: the bandwidth-bound time for the whole existing
blur chain is 0.11 ms against a measured 2.3-4.0 ms, a 20-36x gap that nothing
in this list explains.

| # | Item | Expected visual gain | Expected frame-time cost |
|---|---|---|---|
| 1 | `--frame-stats PATH`; `Img::luminance`/`contrast`/`max_run` test helpers | none | none (off by default) |
| 2 | Triangular dither in `glass.frag` and `card.frag`; upgrade `background.frag` | banding gone from the glass interior and the switcher cards; closes the README's "lowest levels band slightly" | 2 hashes/px over glass and card areas: **< 0.05 ms** |
| 3 | Chrome glass luminance ceiling + dark chrome tint | the dock becomes legible over every backdrop: 1.04:1 -> 6.4:1 over white. The only item here that fixes a defect a user can be harmed by | one dot product, one divide, one mix per chrome pixel: **< 0.05 ms** |
| 4 | Outer rim darkening on both materials | glass edges findable against a similar-luminance backdrop | one `smoothstep` per glass pixel: **< 0.05 ms** |
| 5 | **Shared downsampled prepass** (full -> half -> quarter), window glass from the half, chrome from the quarter | none directly; it is what pays for items 7 and 11 | **saves 2.6-3.6x** of blur traffic and drops the per-surface marginal cost 4.2x (window) / 17x (chrome) |
| 6 | **Linear-light blur chain**: `Abgr2101010` holding `sqrt(linear)`, decode in downsample pass 1, encode + dither in `glass.frag` | the largest single change in the document. Blurred midtones stop being 2.34x too dark; glass goes from grey film to lit pane; highlight bleed appears | 3 `pow` on the first pass's taps, 3 multiplies per tap elsewhere, 1 `sqrt` per write, 3 `pow` per glass pixel. Same bandwidth: **< 0.1 ms** |
| 7 | Contact + ambient shadows via SDF + `erf7` in the glass draw | windows and the dock acquire elevation; the edge highlight stops carrying the whole separation burden | ~25 instructions over each window's footprint inflated by 35 logical px: **0.05-0.1 ms per window** |
| 8 | Drop the per-pass fence in `fx::blur::pass` | none | unknown until item 1; it is the only thing costing 24 of anything per frame |
| 9 | Scissor each chain to its surface's rect plus the blur margin (31 px window, 62 px chrome) | none | **saves a further 2-4x** for typical window sizes |
| 10 | Share one chain between non-overlapping glass surfaces | none | **saves close to Nx** for N non-overlapping surfaces. Apple's `GlassEffectContainer` ("renders multiple glass elements into a combined effect", for "the best rendering performance") is the precedent |
| 11 | Bevel height field + slope clamp + edge displacement, window glass only | the "lensing" read: lines bend under the window edge | one extra dependent texture read plus two `pow` per glass pixel within `refract_width`: **0.1-0.2 ms** |
| 12 | Type scale (ratio 1.2 on 14 logical px) replacing the six ad-hoc multipliers | consistency; the overlay and dock stop disagreeing about sizes | none |
| 13 | Motion: curves, durations, springs, `--test-input frame N` | the desktop stops snapping between states. The largest *perceived* quality change after item 6 | a uniform update; the cost is the extra redraws, i.e. frames that would otherwise be skipped |
| 14 | Reduced-motion and true-zero-motion paths, with criteria | honest accessibility | negative |
| 15 | `precision = "fp16"` as an option | none (an A/B instrument) | doubles chain bandwidth; 1.3% of a 4090's bandwidth at this resolution, 27% of a 50 GB/s iGPU |
| 16 | UV-compression blur LOD | aliasing gone from the displaced rim | one extra sampler and a level blend: **0.05-0.1 ms**, plus the `with_context` plumbing |

Items 1-6 are expected to stay inside the current 2.3-4.0 ms, because item 5
*reduces* cost and items 2, 3, 4 and 6 are a handful of ALU operations each.
Items 7 and 11 are the two that add real cost, and 5, 9 and 10 are what pay for
them: **the sequence is deliberately ordered so the optimisations land before
the effects that need them.** The ceiling is 6.0 ms at p99 with a <= 1.0 ms
standard deviation (C7.1, C7.5); anything that breaches it gets cut rather than
shipped at 45 Hz.

## Open questions

- **Is linear-light blur actually better here, or are we wrong where KWin is
  right?** No shipping Wayland compositor blurs in linear light. The argument in
  decision 1 is that KWin's three reasons do not transfer, and criterion C1.1
  makes the claim falsifiable, but "the arithmetic says so" is not the same as
  "a person looked at both". The A/B instrument
  (`linear_blur = false`, `precision = "srgb8"`) exists precisely so this can be
  settled by eye on a real screen rather than argued.
- **Does the per-pass fence cost anything?** `fx::blur::pass` does
  `bind` -> `render` -> one quad -> `finish` -> `wait` for each of
  `2 * blur_passes` passes, so four glass surfaces at three passes is 24
  bind/render/finish/wait cycles per frame to move 113 MB. GL already orders
  draws into a texture the same context then samples, so one sync at the end
  should suffice. Whether that is where the 20-36x gap lives is unknown until
  item 1 measures it, and removing it is exactly the kind of change that works
  on NVIDIA and breaks elsewhere. Needs a flag and a second host.
- **Does `window.tint.strength = 0.10` survive contact with a screen?** It is a
  guess whose only justification is that 0.22 is definitely wrong once the mix
  is linear. Needs an eye and then a locking test. The same applies to the
  shadow alphas, which are a conversion of Material's numbers, not a
  measurement.
- **What is `[font] gamma`?** Linearising glyph coverage is correct and makes
  light-on-dark text heavier. Whether the counter-adjustment belongs in the
  theme, and at what value for the installed font at 14 logical px, is not
  decided.
- **Where does per-surface glass configuration live?** Chrome glass is selected
  by *what the compositor is drawing*, so `fx::render_scene` hard-codes the
  material per call site. Fine for four call sites, wrong for twenty. Whatever
  introduces layer-shell or panels should revisit it, together with
  `ext-background-effect-v1`.
- **Blur extent at 4K: logical or physical?** `render_scene` multiplies
  `blur_radius` by the output scale, so sigma grows with the display and a 4K
  display pays 3.85x for the chain *and* gets a larger sigma. The shared prepass
  absorbs most of the cost; whether the extent should be capped above some scale
  is unresolved.
- **Is a G2 corner worth it once window glass has a bevel normal?** The
  refusal in decision 9 rests on the deviation region being ~18 px at a 12 px
  radius with sub-pixel normal deviation. That is an estimate, not a
  measurement, and the bevel is the one thing in this design that could make
  the shading argument bite. Measurable from a capture once item 11 exists.

## Consequences

- **The existing glass test constants change, and must be re-derived from a
  capture rather than adjusted to pass.** `tests/glass.rs` asserts the blur
  turns a black/white step into a gradient with at least 10 intermediate pixels
  in an 80-pixel row; the README records "27 intermediate pixels, largest
  neighbour jump 8". Under a linear-light blur the midpoint moves from 128 to
  188 and the encoded profile steepens near the dark end, because
  `d(encode)/d(linear)` is 6.4 at linear 0.01 and rises below that — expect the
  largest neighbour jump to roughly double. The blur extent also changes
  (sigma 17.4 -> 12.0 device px for window glass), so the gradient width changes
  too. The new values are measurements, not guesses.
- **The theme file grows two material sections and a `[motion]` section, and
  stays backward compatible.** Unknown keys are already ignored and missing keys
  already take defaults, so an existing `aurora-glass.toml` keeps working and
  gets window glass with the old look. Hot reload continues to cover everything,
  because nothing here moves a number into code.
- **`fx::Fx::progs` is a fixed `[Option<GlesTexProgram>; 5]` and
  `uniform_names(i)` is a fixed match**, so the sixth shader slot (the decoding
  downsample) and every new uniform is a Rust change, not a theme change. The
  design folds the shadow, the ceiling and the bevel into `glass.frag`'s
  existing slot to keep that surface small. Item 16 is the one that breaks it,
  which is why it is last.
- **Chrome glass becomes visibly darker than today's glass.** Over the default
  background it goes from sRGB (49,46,71) to (41,45,57). That is the cost of the
  contrast guarantee and it should be in the commit message, not discovered.
- **Window glass gets a narrower blur and chrome a wider one.** Sigma goes from
  12.0 logical px for everything to 8.3 (window) and 16.6 (chrome). Two
  materials means two sets of numbers and two sets of criteria. Accepted: one
  material that must satisfy both roles is why the dock is illegible over white.
- **The blur radius stops being comparable to the old value**, because it is now
  an offset on a downsampled input. `sigma = 0.75 * radius * 2^passes * 2^level`
  is the identity to quote, and the theme file's old comment must be deleted
  rather than corrected.
- **Nothing here is a security claim.** The trusted overlay gains chrome glass,
  a scrim and an over-damped rise. None of that changes what decisions 0003 and
  0007 say the overlay does or does not authenticate, and a look-alike prompt
  drawn by a client is still not prevented. A nicer-looking prompt is, if
  anything, marginally easier to imitate.
- **Taint labels, which 0009 anticipated at "0010 or later", are not this
  record.** They remain unbuilt and unnumbered.
