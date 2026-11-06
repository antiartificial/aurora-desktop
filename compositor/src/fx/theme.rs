//! The theme parameter file (`contracts/theme/aurora-glass.toml`): parsing,
//! validation, Oklab to sRGB, and change detection for hot reload.

use std::{path::{Path, PathBuf}, time::SystemTime};

use serde::Deserialize;

pub const THEME_FILE: &str = "aurora-glass.toml";
pub const SHADERS: [&str; 4] = ["down", "up", "glass", "card"];

/// Built-in copies, used when no theme directory exists and as the "last
/// good" shader when the first load of an on-disk one fails.
pub const BUILTIN_THEME: &str = include_str!("../../../contracts/theme/aurora-glass.toml");
pub const BUILTIN_SHADERS: [&str; 4] = [
    include_str!("../../../contracts/theme/shaders/down.frag"),
    include_str!("../../../contracts/theme/shaders/up.frag"),
    include_str!("../../../contracts/theme/shaders/glass.frag"),
    include_str!("../../../contracts/theme/shaders/card.frag"),
];

#[derive(Deserialize, Clone, Debug)]
#[serde(default)]
pub struct Tint { pub oklab: [f32; 3], pub strength: f32 }
#[derive(Deserialize, Clone, Debug)]
#[serde(default)]
pub struct Edge { pub oklab: [f32; 3], pub width: f32, pub strength: f32 }
#[derive(Deserialize, Clone, Debug)]
#[serde(default)]
pub struct Glass {
    pub enabled: bool,
    pub blur_radius: f32,
    pub blur_passes: u32,
    pub corner_radius: f32,
    pub tint: Tint,
    pub edge: Edge,
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
#[derive(Deserialize, Clone, Debug, Default)]
#[serde(default)]
pub struct Theme { pub glass: Glass, pub switcher: SwitcherParams }

impl Default for Tint { fn default() -> Self { Tint { oklab: [0.80, -0.01, -0.04], strength: 0.22 } } }
impl Default for Edge { fn default() -> Self { Edge { oklab: [0.98, 0.0, 0.0], width: 1.5, strength: 0.75 } } }
impl Default for Glass {
    fn default() -> Self {
        Glass { enabled: true, blur_radius: 2.0, blur_passes: 3, corner_radius: 12.0, tint: Tint::default(), edge: Edge::default() }
    }
}
impl Default for SwitcherParams {
    fn default() -> Self { SwitcherParams { layout: Layout::Flip, backdrop_dim: 0.45, flip_angle_deg: 42.0, accent: [0.72, 0.10, 0.12] } }
}

impl Theme {
    pub fn parse(text: &str) -> Result<Theme, String> {
        let mut t: Theme = toml::from_str(text).map_err(|e| e.to_string())?;
        let g = &mut t.glass;
        for v in [g.blur_radius, g.corner_radius, g.tint.strength, g.edge.width, g.edge.strength, t.switcher.backdrop_dim, t.switcher.flip_angle_deg]
            .into_iter().chain(g.tint.oklab).chain(g.edge.oklab).chain(t.switcher.accent)
        {
            if !v.is_finite() { return Err("non-finite number in theme".into()); }
        }
        g.blur_passes = g.blur_passes.clamp(1, 6);
        g.blur_radius = g.blur_radius.clamp(0.0, 16.0);
        g.corner_radius = g.corner_radius.clamp(0.0, 64.0);
        g.tint.strength = g.tint.strength.clamp(0.0, 1.0);
        g.edge.strength = g.edge.strength.clamp(0.0, 1.0);
        g.edge.width = g.edge.width.clamp(0.0, 32.0);
        t.switcher.backdrop_dim = t.switcher.backdrop_dim.clamp(0.0, 1.0);
        t.switcher.flip_angle_deg = t.switcher.flip_angle_deg.clamp(0.0, 80.0);
        Ok(t)
    }
}

/// Oklab (Bjorn Ottosson's matrices) to gamma-encoded sRGB, clamped.
pub fn oklab_to_srgb([l, a, b]: [f32; 3]) -> [f32; 3] {
    let l_ = l + 0.3963377774 * a + 0.2158037573 * b;
    let m_ = l - 0.1055613458 * a - 0.0638541728 * b;
    let s_ = l - 0.0894841775 * a - 1.2914855480 * b;
    let (l, m, s) = (l_ * l_ * l_, m_ * m_ * m_, s_ * s_ * s_);
    let lin = [
        4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
        -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
        -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s,
    ];
    lin.map(|c| {
        let c = c.clamp(0.0, 1.0);
        if c <= 0.0031308 { 12.92 * c } else { 1.055 * c.powf(1.0 / 2.4) - 0.055 }
    })
}

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
        assert!(t.glass.enabled && t.glass.blur_passes == 3 && t.switcher.layout == Layout::Flip);
    }
    #[test]
    fn bad_theme_is_an_error_and_values_are_clamped() {
        assert!(Theme::parse("[glass\nenabled=").is_err());
        let t = Theme::parse("[glass]\nblur_passes = 99\n[glass.tint]\nstrength = 7.0").unwrap();
        assert_eq!((t.glass.blur_passes, t.glass.tint.strength), (6, 1.0));
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
