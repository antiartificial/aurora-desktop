//! Dual-Kawase blur, run as a shared prepass plus a short chain per glass
//! surface (decision 0010, section 3).
//!
//! **Prepass.** The scene is downsampled once, full to half to quarter, and the
//! two copies are shared by every glass surface that does not need a fresher
//! one. The first downsample (`down0`) reads the encoded scene and decodes it;
//! the chain stores sqrt(linear light) (see `down.frag`).
//!
//! **Chains.** Window glass blurs from the half-resolution copy, chrome glass
//! from the quarter. Each chain is `passes` downsamples and as many upsamples,
//! never skipping a level on the way back up. Texture `down[k-1]` is level k of
//! the chain, `up[k]` is level k, so `up[0]` is the result: half the output size
//! for window glass, a quarter for chrome.
//!
//! **When the prepass is stale.** Glass above other glass must see the glass
//! below it (that was true when every surface re-blurred the whole scene and it
//! stays true here), and the dock must blur the windows it sits over. So the
//! prepass is valid only until something is drawn into the scene; the next glass
//! surface re-runs it only if what was drawn lies within the blur's reach of
//! that surface's rectangle (`Scratch::dirty`). Surfaces that do not touch
//! anything drawn since share one prepass.
//!
//! The kernel's reach is bounded: `sigma ~ 0.75 * r * 2^passes * 2^level` and the
//! one-sided support `1.95 * r * 2^passes * 2^level` full-resolution pixels
//! (measured in `docs/visual-system.md` section 3.2). `support` adds slack.

use smithay::{
    backend::renderer::{
        Bind, Frame, Renderer, Texture,
        gles::{GlesRenderer, GlesTexProgram, GlesTexture, Uniform},
        sync::SyncPoint,
    },
    utils::{Buffer as BufferCoords, Physical, Rectangle, Size, Transform},
};

use super::{Res, wait};
use crate::stats::{Phase, Phases};

/// The two glass materials, which differ in where their chain starts.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    /// Under client windows: chain from the half-resolution copy.
    Window,
    /// Under compositor-drawn text: chain from the quarter-resolution copy.
    Chrome,
}

impl Kind {
    /// Halvings of the output the chain's input is already below.
    pub fn start(self) -> u32 {
        match self {
            Kind::Window => 1,
            Kind::Chrome => 2,
        }
    }
}

/// One material's blur chain.
pub struct Chain {
    pub down: Vec<GlesTexture>,
    pub up: Vec<GlesTexture>,
}

pub struct Scratch {
    pub size: (i32, i32),
    pub passes: usize,
    /// Storage of every intermediate target; part of the cache key.
    pub format: smithay::backend::allocator::Fourcc,
    /// The composed scene, encoded sRGB 8-bit.
    pub scene: GlesTexture,
    /// Prepass levels, in the chain format.
    pub half: GlesTexture,
    pub quarter: GlesTexture,
    pub window: Chain,
    pub chrome: Chain,
    /// Whether `half`/`quarter` reflect the scene minus everything in `dirty`.
    pub valid: bool,
    /// Device-pixel rectangles drawn into the scene since the prepass ran.
    pub dirty: Vec<Rect>,
    /// `[glass] share_prepass`: false re-runs the prepass for every surface.
    pub share: bool,
}

/// x, y, w, h in device pixels.
pub type Rect = (i32, i32, i32, i32);

pub struct Progs<'a> {
    pub down0: &'a GlesTexProgram,
    pub down: &'a GlesTexProgram,
    pub up: &'a GlesTexProgram,
}

impl Scratch {
    /// Record that `r` was drawn into the scene.
    pub fn mark_dirty(&mut self, r: Rect) {
        self.dirty.push(r);
    }

    /// The scene was redrawn from scratch.
    pub fn reset(&mut self) {
        self.valid = false;
        self.dirty.clear();
    }
}

/// Pixels of backdrop, beyond a surface's own rectangle, that its blur can see.
pub fn support(radius_dev: f32, passes: usize, k: Kind) -> i32 {
    let level = (1u32 << passes) as f32 * (1u32 << k.start()) as f32;
    (2.2 * radius_dev * level + 2.0 * (1u32 << k.start()) as f32 + 2.0).ceil() as i32
}

fn overlaps(a: Rect, b: Rect) -> bool {
    a.0 < b.0 + b.2 && b.0 < a.0 + a.2 && a.1 < b.1 + b.3 && b.1 < a.1 + a.3
}

fn pass(r: &mut GlesRenderer, prog: &GlesTexProgram, src: &GlesTexture, dst: &mut GlesTexture, offset: f32, linear: bool) -> Res<SyncPoint> {
    let (ss, ds) = (src.size(), dst.size());
    let size = Size::<i32, Physical>::from((ds.w, ds.h));
    let full = Rectangle::<i32, Physical>::from_size(size);
    let u = [
        Uniform::new("halfpixel", (0.5 / ss.w as f32, 0.5 / ss.h as f32)),
        Uniform::new("offset", offset),
        Uniform::new("linear", if linear { 1.0f32 } else { 0.0 }),
    ];
    let mut t = r.bind(dst)?;
    let mut f = r.render(&mut t, size, Transform::Normal)?;
    let srcr = Rectangle::<f64, BufferCoords>::from_size((ss.w as f64, ss.h as f64).into());
    f.render_texture_from_to(src, srcr, full, &[full], &[], Transform::Normal, 1.0, Some(prog), &u)?;
    let sy = f.finish()?;
    let last = sy.clone();
    wait(r, sy)?;
    Ok(last)
}

/// Downsample the scene into `half`, and `half` into `quarter`.
fn prepass(r: &mut GlesRenderer, s: &mut Scratch, p: &Progs<'_>, off: f32, linear: bool, tm: &mut Phases) -> Res<()> {
    let _ = pass(r, p.down0, &s.scene, &mut s.half, off, linear)?;
    let last = pass(r, p.down, &s.half, &mut s.quarter, off, linear)?;
    tm.prepasses += 1;
    tm.lap(Phase::Prepass, &last);
    s.valid = true;
    s.dirty.clear();
    Ok(())
}

/// Blur `input` through `chain` into `chain.up[0]`.
fn run_chain(r: &mut GlesRenderer, input: &GlesTexture, chain: &mut Chain, p: &Progs<'_>, off: f32, linear: bool) -> Res<SyncPoint> {
    let n = chain.down.len();
    let _ = pass(r, p.down, input, &mut chain.down[0], off, linear)?;
    for k in 1..n {
        let (a, b) = chain.down.split_at_mut(k);
        let _ = pass(r, p.down, &a[k - 1], &mut b[0], off, linear)?;
    }
    let mut last = pass(r, p.up, &chain.down[n - 1], &mut chain.up[n - 1], off, linear)?;
    for k in (0..n - 1).rev() {
        let (a, b) = chain.up.split_at_mut(k + 1);
        last = pass(r, p.up, &b[0], &mut a[k], off, linear)?;
    }
    Ok(last)
}

/// Make `s.result(kind)` the blurred backdrop for a glass surface covering `region`
/// (device pixels). `radius_dev` is the chain's tap offset (the theme's radius times the
/// output scale). Re-runs the prepass only if something drawn since it ran is within the
/// blur's reach of `region`.
pub fn backdrop(r: &mut GlesRenderer, s: &mut Scratch, p: &Progs<'_>, kind: Kind, region: Rect, radius_dev: f32, linear: bool, tm: &mut Phases) -> Res<()> {
    let m = support(radius_dev, s.passes, kind);
    let need = (region.0 - m, region.1 - m, region.2 + 2 * m, region.3 + 2 * m);
    if !s.share || !s.valid || s.dirty.iter().any(|d| overlaps(*d, need)) {
        // The prepass's own tap offset is fixed at 1.0: it is a plain 2x downsample, not a blur
        // setting. All blur width comes from the chains, whose offset is the theme's radius.
        prepass(r, s, p, 1.0, linear, tm)?;
    }
    let Scratch { half, quarter, window, chrome, .. } = s;
    let (input, chain) = match kind {
        Kind::Window => (&*half, window),
        Kind::Chrome => (&*quarter, chrome),
    };
    let last = run_chain(r, input, chain, p, radius_dev, linear)?;
    tm.lap(Phase::Chain, &last);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn support_covers_the_measured_one_sided_reach() {
        // docs/visual-system.md 3.2: one-sided support = 1.95 * r * 2^passes * 2^start (+-7%).
        for (r, p, k) in [(2.0, 2usize, Kind::Window), (2.9, 2, Kind::Window), (2.0, 2, Kind::Chrome), (2.9, 2, Kind::Chrome), (2.0, 3, Kind::Window)] {
            let measured = 1.95 * r * (1 << p) as f32 * (1 << k.start()) as f32 * 1.07;
            assert!(support(r, p, k) as f32 >= measured, "{r} {p} {k:?}: {} < {measured}", support(r, p, k));
        }
    }

    #[test]
    fn only_rectangles_that_actually_overlap_count() {
        assert!(overlaps((0, 0, 10, 10), (9, 9, 5, 5)));
        assert!(!overlaps((0, 0, 10, 10), (10, 0, 5, 5)), "touching is not overlapping");
        assert!(!overlaps((0, 0, 10, 10), (0, 20, 5, 5)));
    }
}
