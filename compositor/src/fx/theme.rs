//! The theme parameter file (`contracts/theme/aurora-glass.toml`): parsing,
//! validation, Oklab to sRGB, and change detection for hot reload.

use std::{path::{Path, PathBuf}, time::SystemTime};

use serde::Deserialize;

pub const THEME_FILE: &str = "aurora-glass.toml";
/// Slot order is `Fx::progs` order. `down0` is the sixth slot decision 0010 asked for: the first
/// downsample, the only one that reads the encoded scene and so the only one that decodes it.
pub const SHADERS: [&str; 6] = ["down", "up", "glass", "card", "background", "down0"];

/// Built-in copies, used when no theme directory exists and as the "last
/// good" shader when the first load of an on-disk one fails.
pub const BUILTIN_THEME: &str = include_str!("../../../contracts/theme/aurora-glass.toml");
pub const BUILTIN_SHADERS: [&str; 6] = [
    include_str!("../../../contracts/theme/shaders/down.frag"),
    include_str!("../../../contracts/theme/shaders/up.frag"),
    include_str!("../../../contracts/theme/shaders/glass.frag"),
    include_str!("../../../contracts/theme/shaders/card.frag"),
    // Not in contracts/theme/shaders yet (that directory is outside compositor/): the
    // built-in default lives here, and a copy in the theme dir overrides it, hot-reloaded.
    include_str!("background.frag"),
    include_str!("../../../contracts/theme/shaders/down0.frag"),
];

#[derive(Deserialize, Clone, Debug)]
#[serde(default)]
pub struct Tint { pub oklab: [f32; 3], pub strength: f32 }
#[derive(Deserialize, Clone, Debug)]
#[serde(default)]
pub struct Edge { pub oklab: [f32; 3], pub width: f32, pub strength: f32 }
/// Storage of the blur chain's intermediate targets (decision 0010, section 2.3).
#[derive(Deserialize, Clone, Copy, Debug, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum Precision {
    /// `Abgr2101010` holding sqrt(linear). Falls back to 8 bits, loudly, without `Capability::_10Bit` (GLES 2).
    Rgb10,
    /// `Abgr16161616f` holding sqrt(linear): an A/B instrument, twice the bandwidth.
    Fp16,
    /// `Abgr8888` holding sRGB-encoded values, blurred as encoded: the pre-0010 look, for comparison.
    Srgb8,
}
/// Chrome glass: what sits under compositor-drawn text (the dock, menus, the settings panel, the
/// switcher's backdrop). It differs from window glass in having a dark tint and an ENFORCED
/// luminance ceiling, so text on it keeps a contrast floor over any backdrop (section 3.3).
#[derive(Deserialize, Clone, Debug)]
#[serde(default)]
pub struct Chrome {
    /// Replaced as a whole: give `oklab` and `strength` together.
    pub tint: Tint,
    /// Reinhard roll-off ceiling on backdrop luminance (linear light, before tinting). 0 = off.
    pub luminance_max: f32,
    /// Offset of the chain run from the QUARTER-resolution copy (sigma = 0.75 * r * 2^passes * 4).
    pub blur_radius: f32,
}
#[derive(Deserialize, Clone, Debug)]
#[serde(default)]
pub struct Glass {
    pub enabled: bool,
    pub blur_radius: f32,
    pub blur_passes: u32,
    pub corner_radius: f32,
    pub tint: Tint,
    pub edge: Edge,
    /// Blur in linear light. false blurs encoded values (today's artefact), for A/B.
    pub linear_blur: bool,
    pub precision: Precision,
    /// Share one prepass between glass surfaces that do not touch what was drawn since it ran.
    /// false re-runs it for every surface (A/B, and the oracle the sharing is tested against).
    pub share_prepass: bool,
    pub chrome: Chrome,
}
#[derive(Deserialize, Clone, Copy, Debug, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum Layout { Grid, Flip }
#[derive(Deserialize, Clone, Debug)]
#[serde(default)]
pub struct SwitcherParams {
    pub layout: Layout,
    pub backdrop_dim: f32,
    pub flip_angle_deg: f32,
    pub accent: [f32; 3],
}
#[derive(Deserialize, Clone, Copy, Debug, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum BgMode { Shader, Image, Gradient, Solid }
/// The desktop surface behind every window. `shader`: the `background` shader
/// (time-animated); `image`: `image` file, scaled to cover and centred;
/// `gradient`: vertical Oklab gradient top to bottom; `solid`: `top` only.
/// An image that cannot be read falls back to the gradient.
#[derive(Deserialize, Clone, Debug, PartialEq)]
#[serde(default)]
pub struct Background {
    pub mode: BgMode,
    pub image: String,
    pub top: [f32; 3],
    pub bottom: [f32; 3],
    pub accent: [f32; 3],
    /// Shader time multiplier; 0 freezes the animation.
    pub speed: f32,
}
#[derive(Deserialize, Clone, Debug, PartialEq)]
#[serde(default)]
pub struct Desktop { pub clock: bool, pub hostname: bool, pub clock_oklab: [f32; 3] }
#[derive(Deserialize, Clone, Copy, Debug, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum DockEdge { Bottom, Top, Left, Right }
/// Sizes are LOGICAL pixels (multiplied by the output scale to draw).
#[derive(Deserialize, Clone, Debug, PartialEq)]
#[serde(default)]
pub struct Dock {
    pub enabled: bool,
    pub edge: DockEdge,
    pub item_height: f32,
    pub gap: f32,
    pub padding: f32,
    pub spacing: f32,
    pub hover_oklab: [f32; 3],
    pub hover_strength: f32,
    pub text_oklab: [f32; 3],
    /// "auto" (first terminal found), "" (run the launcher directly), or a command.
    pub terminal: String,
}
#[derive(Deserialize, Clone, Debug, PartialEq)]
#[serde(default)]
pub struct FontCfg {
    /// A font file to load in addition to the system fonts ("" = none).
    pub file: String,
    /// Preferred family ("" = first installed of a built-in preference list).
    pub family: String,
    /// Logical size of UI text.
    pub size: f32,
}
/// The seat's keyboard. Hot-reloaded: a keymap change is applied to the live
/// seat and re-sent to clients; an invalid layout is rejected and the old one stays.
#[derive(Deserialize, Clone, Debug, PartialEq)]
#[serde(default)]
pub struct Input {
    pub rules: String,
    pub model: String,
    pub layout: String,
    pub variant: String,
    pub options: String,
    pub repeat_delay: i32,
    pub repeat_rate: i32,
}
/// The output. `scale` overrides the host's scale factor (nested) or the
/// backend's default; 0 means "follow the host". Written by the settings panel.
#[derive(Deserialize, Clone, Debug, PartialEq)]
#[serde(default)]
pub struct OutputCfg {
    pub scale: f64,
}
impl Default for OutputCfg { fn default() -> Self { OutputCfg { scale: 0.0 } } }

/// How a window's title bar is shaped. `Bar` spans the window's width; `Tab` is a tab hung from the
/// window's top-left corner, just wide enough for its title (BeOS's tab, in glass).
#[derive(Deserialize, Clone, Copy, Debug, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum DecoStyle { Bar, Tab }
/// Window decorations: the title bar the compositor draws above a window that did not draw its own.
/// Sizes are LOGICAL pixels. Colours are Oklab tokens, like everything else here.
#[derive(Deserialize, Clone, Debug, PartialEq)]
#[serde(default)]
pub struct Decoration {
    pub enabled: bool,
    pub style: DecoStyle,
    /// Height of the title bar.
    pub height: f32,
    /// Resize hit tolerance: how far OUTSIDE the window's frame a press still grabs its edge.
    pub grip: f32,
    /// How far along each edge, from a corner, a press grabs the corner rather than the edge.
    pub corner: f32,
    /// Two presses on the bar within this many milliseconds toggle maximise.
    pub double_click_ms: u64,
    /// Ease-out duration of the focus and hover transitions.
    pub ease_ms: u64,
    /// Title text of the focused window and of the others. Both must keep a 4.5:1 contrast over chrome glass.
    pub text_oklab: [f32; 3],
    pub text_dim_oklab: [f32; 3],
    /// The focused window's stripe, and its close button's glyph.
    pub accent_oklab: [f32; 3],
    /// The close button's fill under the pointer.
    pub close_oklab: [f32; 3],
}
impl Default for Decoration {
    fn default() -> Self {
        Decoration {
            enabled: true, style: DecoStyle::Bar, height: 30.0, grip: 8.0, corner: 20.0, double_click_ms: 400, ease_ms: 140,
            text_oklab: [0.98, 0.0, 0.0], text_dim_oklab: [0.90, 0.0, 0.0], accent_oklab: [0.86, 0.0, 0.16], close_oklab: [0.62, 0.20, 0.10],
        }
    }
}

/// The capability-in-use indicator (decision 0016): which held grants it shows and how it looks.
/// `classes` are capability class paths as the bus carries them (`device:wayland`). An entry
/// matches a resource exactly, or as a prefix followed by a ':' separator, so `ui:read` covers
/// the accessibility grant's `ui:read:<domain identity>` (see `indicator::covers`). Not
/// patterns: a class that nothing lists is not shown, and a typo shows nothing rather than everything.
#[derive(Deserialize, Clone, Debug, PartialEq)]
#[serde(default)]
pub struct Indicator {
    pub enabled: bool,
    pub classes: Vec<String>,
    /// Margin from the output's top-right corner, logical pixels.
    pub margin: f32,
    /// Ease-out duration of the pill and the detail panel appearing.
    pub ease_ms: u64,
    /// The dot. No other pixel the compositor draws has this colour (tests count on that).
    pub dot_oklab: [f32; 3],
    /// Text on the indicator's own opaque panel. 4.5:1 or better over `panel_oklab` (asserted on pixels).
    pub text_oklab: [f32; 3],
    pub text_dim_oklab: [f32; 3],
    /// The panel behind the text: opaque, so contrast does not depend on what is behind it.
    pub panel_oklab: [f32; 3],
}
impl Default for Indicator {
    fn default() -> Self {
        Indicator {
            enabled: true, classes: vec!["device:wayland".into(), "ui:read".into()], margin: 10.0, ease_ms: 140,
            dot_oklab: [0.72, 0.17, 0.14], text_oklab: [0.97, 0.0, 0.0], text_dim_oklab: [0.84, 0.0, 0.0], panel_oklab: [0.22, 0.0, 0.0],
        }
    }
}

#[derive(Deserialize, Clone, Debug, Default)]
#[serde(default)]
pub struct Theme {
    pub indicator: Indicator,
    pub decoration: Decoration,
    pub glass: Glass,
    pub switcher: SwitcherParams,
    pub background: Background,
    pub desktop: Desktop,
    pub dock: Dock,
    pub font: FontCfg,
    pub input: Input,
    pub output: OutputCfg,
}

impl Default for Background {
    fn default() -> Self {
        Background { mode: BgMode::Shader, image: String::new(), top: [0.27, -0.015, -0.085], bottom: [0.15, 0.025, -0.045], accent: [0.62, 0.11, -0.10], speed: 1.0 }
    }
}
impl Default for Desktop { fn default() -> Self { Desktop { clock: true, hostname: true, clock_oklab: [0.97, 0.0, 0.0] } } }
impl Default for Dock {
    fn default() -> Self {
        Dock { enabled: true, edge: DockEdge::Bottom, item_height: 36.0, gap: 10.0, padding: 8.0, spacing: 6.0, hover_oklab: [0.95, 0.0, 0.0], hover_strength: 0.22, text_oklab: [0.97, 0.0, 0.0], terminal: "auto".into() }
    }
}
impl Default for FontCfg { fn default() -> Self { FontCfg { file: String::new(), family: String::new(), size: 14.0 } } }
impl Default for Input {
    fn default() -> Self {
        Input { rules: String::new(), model: String::new(), layout: String::new(), variant: String::new(), options: String::new(), repeat_delay: 200, repeat_rate: 25 }
    }
}

impl Default for Tint { fn default() -> Self { Tint { oklab: [0.80, -0.01, -0.04], strength: 0.10 } } }
impl Default for Chrome {
    fn default() -> Self { Chrome { tint: Tint { oklab: [0.38, -0.005, -0.020], strength: 0.45 }, luminance_max: 0.16, blur_radius: 2.0 } }
}
impl Default for Edge { fn default() -> Self { Edge { oklab: [0.98, 0.0, 0.0], width: 1.5, strength: 0.75 } } }
impl Default for Glass {
    fn default() -> Self {
        Glass { enabled: true, blur_radius: 2.0, blur_passes: 2, corner_radius: 12.0, tint: Tint::default(), edge: Edge::default(), linear_blur: true, precision: Precision::Rgb10, share_prepass: true, chrome: Chrome::default() }
    }
}
impl Default for SwitcherParams {
    fn default() -> Self { SwitcherParams { layout: Layout::Flip, backdrop_dim: 0.45, flip_angle_deg: 42.0, accent: [0.72, 0.10, 0.12] } }
}

impl Theme {
    pub fn parse(text: &str) -> Result<Theme, String> {
        let mut t: Theme = toml::from_str(text).map_err(|e| e.to_string())?;
        let g = &mut t.glass;
        for v in [g.blur_radius, g.corner_radius, g.tint.strength, g.edge.width, g.edge.strength, t.switcher.backdrop_dim, t.switcher.flip_angle_deg, g.chrome.tint.strength, g.chrome.luminance_max, g.chrome.blur_radius]
            .into_iter().chain(g.tint.oklab).chain(g.edge.oklab).chain(t.switcher.accent).chain(g.chrome.tint.oklab)
        {
            if !v.is_finite() { return Err("non-finite number in theme".into()); }
        }
        let (b, d, f) = (&mut t.background, &mut t.dock, &mut t.font);
        for v in [b.speed, d.item_height, d.gap, d.padding, d.spacing, d.hover_strength, f.size]
            .into_iter().chain(b.top).chain(b.bottom).chain(b.accent).chain(d.hover_oklab).chain(d.text_oklab).chain(t.desktop.clock_oklab)
        {
            if !v.is_finite() { return Err("non-finite number in theme".into()); }
        }
        let c = &mut t.decoration;
        for v in [c.height, c.grip, c.corner].into_iter().chain(c.text_oklab).chain(c.text_dim_oklab).chain(c.accent_oklab).chain(c.close_oklab) {
            if !v.is_finite() { return Err("non-finite number in theme".into()); }
        }
        let i = &mut t.indicator;
        for v in [i.margin].into_iter().chain(i.dot_oklab).chain(i.text_oklab).chain(i.text_dim_oklab).chain(i.panel_oklab) {
            if !v.is_finite() { return Err("non-finite number in theme".into()); }
        }
        i.margin = i.margin.clamp(0.0, 64.0);
        i.ease_ms = i.ease_ms.clamp(0, 1000);
        i.classes.truncate(64);
        c.height = c.height.clamp(16.0, 64.0).round();
        c.grip = c.grip.clamp(2.0, 32.0);
        c.corner = c.corner.clamp(c.grip, 64.0);
        c.double_click_ms = c.double_click_ms.clamp(100, 2000);
        c.ease_ms = c.ease_ms.clamp(0, 1000);
        if !t.output.scale.is_finite() { return Err("non-finite output scale".into()); }
        t.output.scale = if t.output.scale <= 0.0 { 0.0 } else { t.output.scale.clamp(0.5, 8.0) };
        b.speed = b.speed.clamp(0.0, 20.0);
        d.item_height = d.item_height.clamp(16.0, 128.0);
        d.gap = d.gap.clamp(0.0, 64.0);
        d.padding = d.padding.clamp(0.0, 32.0);
        d.spacing = d.spacing.clamp(0.0, 32.0);
        d.hover_strength = d.hover_strength.clamp(0.0, 1.0);
        f.size = f.size.clamp(6.0, 64.0);
        t.input.repeat_delay = t.input.repeat_delay.clamp(1, 5000);
        t.input.repeat_rate = t.input.repeat_rate.clamp(0, 200);
        let g = &mut t.glass;
        g.blur_passes = g.blur_passes.clamp(1, 6);
        g.blur_radius = g.blur_radius.clamp(0.0, 16.0);
        g.corner_radius = g.corner_radius.clamp(0.0, 64.0);
        g.tint.strength = g.tint.strength.clamp(0.0, 1.0);
        g.edge.strength = g.edge.strength.clamp(0.0, 1.0);
        g.edge.width = g.edge.width.clamp(0.0, 32.0);
        g.chrome.tint.strength = g.chrome.tint.strength.clamp(0.0, 1.0);
        g.chrome.luminance_max = g.chrome.luminance_max.clamp(0.0, 16.0);
        g.chrome.blur_radius = g.chrome.blur_radius.clamp(0.0, 16.0);
        t.switcher.backdrop_dim = t.switcher.backdrop_dim.clamp(0.0, 1.0);
        t.switcher.flip_angle_deg = t.switcher.flip_angle_deg.clamp(0.0, 80.0);
        Ok(t)
    }
}

/// Oklab (Bjorn Ottosson's matrices) to LINEAR sRGB, clamped to 0..1: `oklab_to_srgb` minus the encode.
pub fn oklab_to_linear([l, a, b]: [f32; 3]) -> [f32; 3] {
    let l_ = l + 0.3963377774 * a + 0.2158037573 * b;
    let m_ = l - 0.1055613458 * a - 0.0638541728 * b;
    let s_ = l - 0.0894841775 * a - 1.2914855480 * b;
    let (l, m, s) = (l_ * l_ * l_, m_ * m_ * m_, s_ * s_ * s_);
    let lin = [
        4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
        -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
        -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s,
    ];
    lin.map(|c| c.clamp(0.0, 1.0))
}

/// Oklab to gamma-encoded sRGB (IEC 61966-2-1), clamped.
pub fn oklab_to_srgb(c: [f32; 3]) -> [f32; 3] {
    oklab_to_linear(c).map(|c| if c <= 0.0031308 { 12.92 * c } else { 1.055 * c.powf(1.0 / 2.4) - 0.055 })
}

/// Oklab token to 8-bit sRGB.
pub fn oklab_to_u8(c: [f32; 3]) -> [u8; 3] { oklab_to_srgb(c).map(|v| (v * 255.0).round() as u8) }

/// Where the theme comes from, and what it looked like when last read.
pub struct Files { pub dir: PathBuf, stamps: Vec<Option<SystemTime>> }

impl Files {
    pub fn new(dir: PathBuf) -> Files { Files { dir, stamps: vec![] } }
    fn paths(&self) -> Vec<PathBuf> {
        std::iter::once(self.dir.join(THEME_FILE)).chain(SHADERS.iter().map(|s| self.dir.join("shaders").join(format!("{s}.frag")))).collect()
    }
    /// Which of [theme, down, up, glass, card] changed since the last call
    /// (all of them on the first call).
    pub fn changed(&mut self) -> Vec<bool> {
        let now: Vec<Option<SystemTime>> = self.paths().iter().map(|p| std::fs::metadata(p).and_then(|m| m.modified()).ok()).collect();
        let first = self.stamps.is_empty();
        let ch = now.iter().enumerate().map(|(i, s)| first || self.stamps.get(i) != Some(s)).collect();
        self.stamps = now;
        ch
    }
    pub fn read_theme(&self) -> Option<std::io::Result<String>> { read(&self.dir.join(THEME_FILE)) }
    pub fn read_shader(&self, i: usize) -> Option<std::io::Result<String>> { read(&self.dir.join("shaders").join(format!("{}.frag", SHADERS[i]))) }
}

fn read(p: &Path) -> Option<std::io::Result<String>> { p.exists().then(|| std::fs::read_to_string(p)) }

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn builtin_theme_parses() {
        let t = Theme::parse(BUILTIN_THEME).unwrap();
        assert!(t.glass.enabled && t.glass.blur_passes == 2 && t.switcher.layout == Layout::Flip);
    }
    #[test]
    fn bad_theme_is_an_error_and_values_are_clamped() {
        assert!(Theme::parse("[glass\nenabled=").is_err());
        let t = Theme::parse("[glass]\nblur_passes = 99\n[glass.tint]\nstrength = 7.0").unwrap();
        assert_eq!((t.glass.blur_passes, t.glass.tint.strength), (6, 1.0));
    }
    /// The test harness strips `[glass.chrome]` from its copy of the reference theme (a test that
    /// appends its own would otherwise repeat a table, a hard TOML error), which is only harmless
    /// while the parser's defaults ARE the reference values. This is what keeps that true.
    #[test]
    fn the_reference_theme_spells_out_exactly_the_defaults() {
        let (r, d) = (Theme::parse(BUILTIN_THEME).unwrap().glass, Glass::default());
        assert_eq!((r.chrome.tint.oklab, r.chrome.tint.strength, r.chrome.luminance_max, r.chrome.blur_radius), (d.chrome.tint.oklab, d.chrome.tint.strength, d.chrome.luminance_max, d.chrome.blur_radius));
        assert_eq!((r.blur_radius, r.blur_passes, r.corner_radius, r.tint.strength, r.linear_blur, r.precision, r.share_prepass), (d.blur_radius, d.blur_passes, d.corner_radius, d.tint.strength, d.linear_blur, d.precision, d.share_prepass));
        assert!(BUILTIN_THEME.contains("[glass.chrome]"), "the reference theme documents chrome glass");
    }
    /// The harness strips `[indicator]` too: the file must spell out exactly the parser's defaults.
    /// The default class list is decision 0016's (display yes, GPU no) plus 0017's accessibility
    /// read, which is the clearest "something is using this" the system has.
    #[test]
    fn the_reference_indicator_table_is_the_default_and_shows_the_display_and_ui_reads_but_not_the_gpu() {
        let (r, d) = (Theme::parse(BUILTIN_THEME).unwrap().indicator, Indicator::default());
        assert_eq!(r, d);
        assert_eq!(d.classes, vec!["device:wayland".to_string(), "ui:read".to_string()]);
        assert!(!d.classes.iter().any(|c| c == "device:gpu"), "a dot for every GPU kit trains the eye to ignore it");
        assert!(BUILTIN_THEME.contains("\n[indicator]\n"));
        let t = Theme::parse("[indicator]\nmargin = 900.0\nease_ms = 99999\nclasses = [\"device:gpu\"]\n").unwrap().indicator;
        assert_eq!((t.margin, t.ease_ms, t.classes), (64.0, 1000, vec!["device:gpu".to_string()]));
    }
    /// The harness strips `[decoration]` from its copy of the reference theme too (tests append their own), so the
    /// same holds: the file must spell out exactly the parser's defaults, and a bad value must be clamped, not trusted.
    #[test]
    fn the_reference_decoration_table_is_the_default_and_its_numbers_are_kept_sane() {
        let (r, d) = (Theme::parse(BUILTIN_THEME).unwrap().decoration, Decoration::default());
        assert_eq!(r, d);
        assert!(BUILTIN_THEME.contains("\n[decoration]\n"), "the reference theme documents the decoration table");
        let t = Theme::parse("[decoration]\nstyle = \"tab\"\nheight = 900.0\ngrip = 0.0\ncorner = 1.0\ndouble_click_ms = 1\n").unwrap().decoration;
        assert_eq!((t.style, t.height, t.grip, t.double_click_ms), (DecoStyle::Tab, 64.0, 2.0, 100));
        assert!(t.corner >= t.grip, "a corner reaches at least as far as the edge it joins");
        assert!(Theme::parse("[decoration]\nstyle = \"pillow\"\n").is_err());
    }
    #[test]
    fn linear_and_encoded_tokens_agree() {
        // Oklab L 0.6 grey is linear 0.6^3 = 0.216, which encodes to 0.502 (code 128)
        let (l, e) = (oklab_to_linear([0.6, 0.0, 0.0]), oklab_to_srgb([0.6, 0.0, 0.0]));
        assert!((l[0] - 0.2160).abs() < 0.005, "{l:?}");
        assert!((e[0] - 0.5021).abs() < 0.002, "{e:?}");
        // the chrome tint's luminance is the 0.055 the contrast derivation in 3.3 uses
        let t = oklab_to_linear(Chrome::default().tint.oklab);
        let y = 0.2126 * t[0] + 0.7152 * t[1] + 0.0722 * t[2];
        assert!((y - 0.055).abs() < 0.003, "chrome tint luminance {y}");
    }
    #[test]
    fn oklab_white_black_and_grey() {
        let w = oklab_to_srgb([1.0, 0.0, 0.0]);
        assert!(w.iter().all(|c| (c - 1.0).abs() < 0.01), "{w:?}");
        assert!(oklab_to_srgb([0.0, 0.0, 0.0]).iter().all(|c| c.abs() < 0.01));
        let g = oklab_to_srgb([0.6, 0.0, 0.0]);
        assert!((g[0] - g[1]).abs() < 0.01 && (g[1] - g[2]).abs() < 0.01);
    }
}
