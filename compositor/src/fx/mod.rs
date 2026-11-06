//! Phase 1's look inside the compositor: glass (dual-Kawase blur of what is
//! behind a window, tint, highlight edge) and the window switcher, as render
//! passes, with parameters and shaders hot-reloaded from `contracts/theme/`.
//!
//! The scene is composed in an offscreen texture, window by window from the
//! back: before a window is drawn, everything below it is blurred (from a
//! shared downsampled prepass, see `blur`) and drawn under its rectangle. The
//! finished scene is blitted into the output
//! framebuffer. `render.rs` serves captures from that framebuffer afterwards
//! and draws the trusted overlay after that, so ordering is unchanged.

pub mod blur;
pub mod edit;
pub mod switcher;
pub mod theme;

use std::{error::Error, path::PathBuf, time::{Duration, Instant}};

use smithay::{
    backend::{
        allocator::Fourcc,
        renderer::{
            Bind, Color32F, Frame, Offscreen, Renderer,
            element::{Element, RenderElement, surface::WaylandSurfaceRenderElement},
            gles::{Capability, GlesFrame, GlesRenderer, GlesTarget, GlesTexProgram, GlesTexture, Uniform, UniformName, UniformType},
        },
    },
    desktop::{Space, Window},
    output::Output,
    utils::{Buffer as BufferCoords, Logical, Physical, Point, Rectangle, Scale, Size, Transform},
};

use crate::stats::Phase;
use blur::{Chain, Kind, Progs, Scratch};
use theme::{BUILTIN_SHADERS, BUILTIN_THEME, Files, Glass, Precision, SHADERS, THEME_FILE, Theme, oklab_to_linear};

pub type Res<T> = Result<T, Box<dyn Error>>;
const POLL: Duration = Duration::from_millis(250);

pub struct Fx {
    pub theme: Theme,
    files: Option<Files>,
    initialised: bool,
    next_poll: Instant,
    pub progs: [Option<GlesTexProgram>; 6], // down, up, glass, card, background, down0 (`theme::SHADERS`): each keeps its last good version
    pub switcher: switcher::Switcher,
    scratch: Option<Scratch>,
    cards: Vec<GlesTexture>,
    /// Count of successful theme (file or shader) loads; tests and logs read it.
    pub loads: u32,
    /// The theme text when there is no theme directory: edits live here, for the session only.
    mem_text: Option<String>,
    /// Per-phase timing of the effects pass (`--frame-stats`); inert unless enabled.
    pub phases: crate::stats::Phases,
}

fn uniform_names(i: usize) -> Vec<UniformName<'static>> {
    use UniformType::*;
    let l: &[(&'static str, UniformType)] = match i {
        0 | 1 | 5 => &[("halfpixel", _2f), ("offset", _1f), ("linear", _1f)],
        4 => &[("time", _1f), ("res", _2f), ("top", _3f), ("bottom", _3f), ("accent", _3f)],
        2 => &[("rect", _4f), ("texsize", _2f), ("radius", _1f), ("tint", _4f), ("edge", _4f), ("edge_width", _1f), ("lum_max", _1f), ("linear", _1f)],
        _ => &[("screen", _2f), ("pose", _4f), ("csize", _2f), ("focal", _1f), ("shade", _1f), ("sel", _1f), ("accent", _3f)],
    };
    l.iter().map(|(n, t)| UniformName::new(*n, *t)).collect()
}

impl Fx {
    pub fn new(dir: Option<PathBuf>) -> Fx {
        Fx {
            theme: Theme::default(), files: dir.map(Files::new), initialised: false, next_poll: Instant::now(),
            progs: Default::default(), switcher: Default::default(), scratch: None, cards: vec![], loads: 0, mem_text: None, phases: Default::default(),
        }
    }

    /// Read the theme file once, without a renderer, so settings that do not
    /// need one (the seat's keymap) are in force from the first frame. The
    /// first `poll` loads it again for real (and logs it).
    pub fn preload_theme(&mut self) {
        let text = match self.files.as_ref().and_then(|f| f.read_theme()) {
            Some(Ok(t)) => t,
            Some(Err(_)) => return,
            None => BUILTIN_THEME.to_string(),
        };
        if let Ok(t) = Theme::parse(&text) { self.theme = t; }
    }

    /// Check the theme files (at most every 250 ms) and apply changes. A theme
    /// that does not parse, or a shader that does not compile, is logged and
    /// the previous good one stays in use; the compositor never stops for it.
    pub fn poll(&mut self, r: &mut GlesRenderer) {
        if Instant::now() < self.next_poll { return; }
        self.next_poll = Instant::now() + POLL;
        let changed = match &mut self.files {
            Some(f) => f.changed(),
            None if !self.initialised => vec![true; SHADERS.len() + 1],
            None => return,
        };
        self.initialised = true;
        if changed[0] {
            let text = match self.files.as_ref().and_then(|f| f.read_theme()) {
                Some(Ok(t)) => Some(t),
                Some(Err(e)) => { tracing::error!("theme: cannot read file, keeping last good: {e}"); None }
                None => Some(BUILTIN_THEME.to_string()),
            };
            if let Some(text) = text {
                match Theme::parse(&text) {
                    Ok(t) => { self.theme = t; self.loads += 1; tracing::info!("theme: loaded (glass {}, passes {}, radius {}, tint strength {}, switcher {:?})", self.theme.glass.enabled, self.theme.glass.blur_passes, self.theme.glass.blur_radius, self.theme.glass.tint.strength, self.theme.switcher.layout); }
                    Err(e) => tracing::error!("theme: rejected, keeping last good: {e}"),
                }
            }
        }
        for i in 0..SHADERS.len() {
            if !changed[i + 1] { continue; }
            let src = match self.files.as_ref().and_then(|f| f.read_shader(i)) {
                Some(Ok(s)) => s,
                Some(Err(e)) => { tracing::error!("theme: cannot read shader {}: {e}", SHADERS[i]); continue; }
                None => BUILTIN_SHADERS[i].to_string(),
            };
            match r.compile_custom_texture_shader(&src, &uniform_names(i)) {
                Ok(p) => { self.progs[i] = Some(p); self.loads += 1; tracing::info!("theme: shader {} compiled", SHADERS[i]); }
                Err(e) => {
                    tracing::error!("theme: shader {} failed to compile, keeping last good version: {e}", SHADERS[i]);
                    if self.progs[i].is_none() {
                        self.progs[i] = r.compile_custom_texture_shader(BUILTIN_SHADERS[i], &uniform_names(i)).ok();
                        tracing::warn!("theme: shader {} falls back to the built-in copy", SHADERS[i]);
                    }
                }
            }
        }
    }

    /// Write settings back to the theme file; hot reload then applies them like
    /// any hand edit. Nothing is written that `Theme::parse` or xkb would
    /// reject. With no theme directory the change applies to this session only.
    /// Returns the sentence to show the user.
    pub fn apply_edits(&mut self, edits: &[edit::Edit]) -> Result<String, String> {
        let path = self.files.as_ref().map(|f| f.dir.join(THEME_FILE));
        let current = match &path {
            Some(p) if p.exists() => std::fs::read_to_string(p).map_err(|e| format!("cannot read {}: {e}", p.display()))?,
            _ => self.mem_text.clone().unwrap_or_else(|| BUILTIN_THEME.to_string()),
        };
        let text = edit::apply(&current, edits)?;
        let t = Theme::parse(&text)?;
        let (a, b) = (&self.theme.input, &t.input);
        if (&a.rules, &a.model, &a.layout, &a.variant, &a.options) != (&b.rules, &b.model, &b.layout, &b.variant, &b.options) && !xkb_accepts(b) {
            return Err(format!("xkb has no keymap for layout {:?} variant {:?}; nothing was written", b.layout, b.variant));
        }
        match path {
            Some(p) => {
                let real = std::fs::canonicalize(&p).unwrap_or(p.clone());
                let tmp = real.with_extension("toml.tmp");
                std::fs::write(&tmp, &text).and_then(|_| std::fs::rename(&tmp, &real)).map_err(|e| format!("cannot write {}: {e}", real.display()))?;
                self.next_poll = Instant::now();
                tracing::info!("settings: wrote {} edit(s) to {}", edits.len(), real.display());
                Ok(format!("Saved to {}", real.display()))
            }
            None => {
                self.mem_text = Some(text);
                self.theme = t;
                Ok("No theme directory: applied for this session only".into())
            }
        }
    }

    pub fn ready(&self) -> bool { self.progs.iter().all(Option::is_some) }
    /// Whether the effects pipeline (not the plain renderer) draws this frame.
    pub fn active(&self, shell: &crate::shell::Shell) -> bool { self.ready() && (self.theme.glass.enabled || self.switcher.open || shell.wants_pipeline()) }

    /// Whether the blur chain holds and averages linear light.
    pub fn linear(&self) -> bool {
        let g = &self.theme.glass;
        g.linear_blur && g.precision != Precision::Srgb8
    }

    fn ensure(&mut self, r: &mut GlesRenderer, size: Size<i32, Physical>) -> Res<()> {
        let passes = self.theme.glass.blur_passes as usize;
        let format = chain_format(r, self.theme.glass.precision);
        if self.scratch.as_ref().is_some_and(|s| s.size == (size.w, size.h) && s.passes == passes && s.format == format) { return Ok(()); }
        let chain = |r: &mut GlesRenderer, start: u32| -> Res<Chain> {
            let lvl = |r: &mut GlesRenderer, k: u32| make_tex_fmt(r, format, (size.w >> k).max(1), (size.h >> k).max(1));
            let (mut down, mut up) = (vec![], vec![]);
            for k in 1..=passes as u32 { down.push(lvl(r, start + k)?); }
            for k in 0..passes as u32 { up.push(lvl(r, start + k)?); }
            Ok(Chain { down, up })
        };
        let scene = make_tex(r, size.w, size.h)?;
        let half = make_tex_fmt(r, format, (size.w >> 1).max(1), (size.h >> 1).max(1))?;
        let quarter = make_tex_fmt(r, format, (size.w >> 2).max(1), (size.h >> 2).max(1))?;
        let (window, chrome) = (chain(r, Kind::Window.start())?, chain(r, Kind::Chrome.start())?);
        tracing::info!("glass: blur chain {format:?} {}, {passes} passes, {}x{}", if self.linear() { "holding sqrt(linear light)" } else { "holding encoded sRGB (not linear)" }, size.w, size.h);
        self.scratch = Some(Scratch { size: (size.w, size.h), passes, format, scene, half, quarter, window, chrome, valid: false, dirty: vec![], share: true });
        Ok(())
    }

    /// Compose the scene (and the switcher, if open) and blit it to `fb`.
    pub fn render_scene(&mut self, r: &mut GlesRenderer, fb: &mut GlesTarget<'_>, output: &Output, size: Size<i32, Physical>, space: &Space<Window>, shell: &mut crate::shell::Shell) -> Res<()> {
        self.ensure(r, size)?;
        shell.prepare(r, size)?;
        let scale = output.current_scale().fractional_scale();
        let linear = self.linear();
        // The theme speaks logical pixels: radii, edge width and blur spread grow with the scale.
        let mut glass = self.theme.glass.clone();
        glass.corner_radius *= scale as f32;
        glass.blur_radius *= scale as f32;
        glass.edge.width *= scale as f32;
        let chrome_radius = glass.chrome.blur_radius * scale as f32;
        let full = Rectangle::<i32, Physical>::from_size(size);
        let (Some(down), Some(up), Some(glass_p), Some(bg_p), Some(down0)) = (&self.progs[0], &self.progs[1], &self.progs[2], &self.progs[4], &self.progs[5]) else { return Err("shaders missing".into()) };
        let bp = Progs { down0, down, up };
        let s = self.scratch.as_mut().unwrap();
        s.share = self.theme.glass.share_prepass;
        {
            let mut t = r.bind(&mut s.scene)?;
            let mut f = r.render(&mut t, size, Transform::Normal)?;
            f.clear(Color32F::new(crate::render::CLEAR[0], crate::render::CLEAR[1], crate::render::CLEAR[2], 1.0), &[full])?;
            shell.draw_background(&mut f, bg_p, size)?;
            let sy = f.finish()?; self.phases.lap(Phase::Scene, &sy); wait(r, sy)?;
        }
        s.reset();
        for (i, w) in space.elements().enumerate() {
            let geo = crate::scale::rect_to_physical(space.element_geometry(w).ok_or("window without geometry")?, scale);
            let els = window_elements(r, w, space.element_location(w).unwrap_or_default() - w.geometry().loc, scale);
            // Everything this window puts in the scene: its glass, and its surfaces (which may overhang it).
            let mut touched = els.iter().map(|e| e.geometry(Scale::from(scale))).fold(rect_of(geo), |a, g| union(a, rect_of(g)));
            if glass.enabled { blur::backdrop(r, s, &bp, Kind::Window, rect_of(geo), glass.blur_radius, linear, &mut self.phases)?; }
            // The title bar hangs above the window: window glass, then the bar (chrome glass and its
            // texture), then the client's surfaces. The bar's texture reaches a corner radius below
            // its edge, under the client, so the glass's rounded bottom corners are hidden by it.
            let bar = shell.deco_draw(i).map(|d| (d.tex, d.dst));
            let split = bar.is_some();
            {
                let mut t = r.bind(&mut s.scene)?;
                let mut f = r.render(&mut t, size, Transform::Normal)?;
                if glass.enabled { draw_glass(&mut f, glass_p, &s.window.up[0], geo, size, &glass, 0.0, linear)?; self.phases.surfaces += 1; }
                if !split { draw_elements(&mut f, &els, scale)?; }
                let sy = f.finish()?; self.phases.lap(Phase::Composite, &sy); wait(r, sy)?;
            }
            if let Some((tex, dst)) = bar {
                s.mark_dirty(rect_of(geo));
                let bg = Rectangle::<i32, Physical>::new((dst.x, dst.y).into(), (dst.w, dst.h).into());
                if glass.enabled { blur::backdrop(r, s, &bp, Kind::Chrome, rect_of(bg), chrome_radius, linear, &mut self.phases)?; }
                {
                    let mut t = r.bind(&mut s.scene)?;
                    let mut f = r.render(&mut t, size, Transform::Normal)?;
                    if glass.enabled {
                        let mut g = chrome_glass(&glass);
                        g.corner_radius = shell.dock_radius();
                        draw_glass(&mut f, glass_p, &s.chrome.up[0], bg, size, &g, glass.chrome.luminance_max, linear)?;
                        self.phases.surfaces += 1;
                    }
                    crate::shell::draw_tex(&mut f, tex, dst)?;
                    draw_elements(&mut f, &els, scale)?;
                    let sy = f.finish()?; self.phases.lap(Phase::Composite, &sy); wait(r, sy)?;
                }
                touched = union(touched, rect_of(bg));
            }
            s.mark_dirty(touched);
        }
        if let Some((tex, panel)) = shell.dock_draw(size) {
            let geo = Rectangle::<i32, Physical>::new((panel.x, panel.y).into(), (panel.w, panel.h).into());
            if glass.enabled { blur::backdrop(r, s, &bp, Kind::Chrome, rect_of(geo), chrome_radius, linear, &mut self.phases)?; }
            {
                let mut t = r.bind(&mut s.scene)?;
                let mut f = r.render(&mut t, size, Transform::Normal)?;
                if glass.enabled {
                    let mut g = chrome_glass(&glass);
                    g.corner_radius = shell.dock_radius();
                    draw_glass(&mut f, glass_p, &s.chrome.up[0], geo, size, &g, glass.chrome.luminance_max, linear)?;
                    self.phases.surfaces += 1;
                }
                crate::shell::draw_tex(&mut f, tex, panel)?;
                let sy = f.finish()?; self.phases.lap(Phase::Composite, &sy); wait(r, sy)?;
            }
            s.mark_dirty(rect_of(geo));
        }
        // Menu and settings panel: ordinary chrome, in the scene, so captures see them.
        // Glass fades in with the animation by scaling its tint and edge strength; the luminance
        // ceiling is relaxed by the same factor so it does not pop in at full strength.
        for p in shell.chrome_draws(size) {
            let geo = Rectangle::<i32, Physical>::new((p.dst.x, p.dst.y).into(), (p.dst.w, p.dst.h).into());
            if glass.enabled { blur::backdrop(r, s, &bp, Kind::Chrome, rect_of(geo), chrome_radius, linear, &mut self.phases)?; }
            {
                let mut t = r.bind(&mut s.scene)?;
                let mut f = r.render(&mut t, size, Transform::Normal)?;
                if glass.enabled {
                    let mut g = chrome_glass(&glass);
                    g.corner_radius = shell.dock_radius();
                    g.tint.strength *= p.alpha;
                    g.edge.strength *= p.alpha;
                    let ymax = if glass.chrome.luminance_max > 0.0 { glass.chrome.luminance_max / p.alpha.max(0.05) } else { 0.0 };
                    draw_glass(&mut f, glass_p, &s.chrome.up[0], geo, size, &g, ymax, linear)?;
                    self.phases.surfaces += 1;
                }
                crate::shell::draw_tex_alpha(&mut f, p.tex, p.dst, p.alpha)?;
                let sy = f.finish()?; self.phases.lap(Phase::Composite, &sy); wait(r, sy)?;
            }
            s.mark_dirty(rect_of(geo));
        }
        if self.switcher.open {
            switcher::draw(self, r, size, scale, space)?;
            self.phases.lap_cpu(Phase::Switcher);
        }
        let s = self.scratch.as_ref().unwrap();
        let mut f = r.render(fb, size, output.current_transform())?;
        let src = Rectangle::<f64, BufferCoords>::from_size((size.w as f64, size.h as f64).into());
        f.render_texture_from_to(&s.scene, src, full, &[full], &[], Transform::Normal, 1.0, None, &[])?;
        let sy = f.finish()?;
        self.phases.lap(Phase::Blit, &sy);
        wait(r, sy)
    }
}

/// The material under compositor text: the shared `glass` with chrome's dark tint.
pub fn chrome_glass(g: &Glass) -> Glass {
    let mut c = g.clone();
    c.tint = g.chrome.tint.clone();
    c
}

fn rect_of(r: Rectangle<i32, Physical>) -> blur::Rect { (r.loc.x, r.loc.y, r.size.w, r.size.h) }

fn union(a: blur::Rect, b: blur::Rect) -> blur::Rect {
    let (x0, y0) = (a.0.min(b.0), a.1.min(b.1));
    let (x1, y1) = ((a.0 + a.2).max(b.0 + b.2), (a.1 + a.3).max(b.1 + b.3));
    (x0, y0, x1 - x0, y1 - y0)
}

/// Storage for the blur chain: `Abgr2101010` (or fp16) holding sqrt(linear), or plain 8-bit.
/// Without `Capability::_10Bit` (a GLES 2 context) the 10-bit and fp16 choices cannot be had:
/// that is logged, loudly, and the chain is 8-bit.
fn chain_format(r: &GlesRenderer, p: Precision) -> Fourcc {
    let ten = r.capabilities().contains(&Capability::_10Bit);
    match (p, ten) {
        (Precision::Srgb8, _) => Fourcc::Abgr8888,
        (Precision::Rgb10, true) => Fourcc::Abgr2101010,
        (Precision::Fp16, true) => Fourcc::Abgr16161616f,
        (p, false) => {
            tracing::warn!("glass: precision {p:?} needs Capability::_10Bit (GLES 3.0), which this context lacks: the blur chain falls back to 8-bit Abgr8888 holding sqrt(linear). Dark gradients may band");
            eprintln!("aurora-compositor: WARNING: glass precision {p:?} unavailable on this GL context; blur chain is 8-bit");
            Fourcc::Abgr8888
        }
    }
}

pub fn make_tex(r: &mut GlesRenderer, w: i32, h: i32) -> Res<GlesTexture> {
    make_tex_fmt(r, Fourcc::Abgr8888, w, h)
}

/// An offscreen texture. Smithay's `create_buffer` leaves the wrap mode at GL's default, REPEAT, so
/// a blur tap off one edge of the screen would read the opposite edge (the dock, at the bottom, would
/// pick up whatever is at the top). Our own targets are clamped.
pub fn make_tex_fmt(r: &mut GlesRenderer, f: Fourcc, w: i32, h: i32) -> Res<GlesTexture> {
    use smithay::backend::renderer::gles::ffi;
    let tex: GlesTexture = r.create_buffer(f, Size::<i32, BufferCoords>::from((w.max(1), h.max(1))))?;
    let id = tex.tex_id();
    r.with_context(|gl| unsafe {
        gl.BindTexture(ffi::TEXTURE_2D, id);
        gl.TexParameteri(ffi::TEXTURE_2D, ffi::TEXTURE_WRAP_S, ffi::CLAMP_TO_EDGE as i32);
        gl.TexParameteri(ffi::TEXTURE_2D, ffi::TEXTURE_WRAP_T, ffi::CLAMP_TO_EDGE as i32);
        gl.BindTexture(ffi::TEXTURE_2D, 0);
    })?;
    Ok(tex)
}

pub fn wait(r: &mut GlesRenderer, sync: smithay::backend::renderer::sync::SyncPoint) -> Res<()> {
    r.wait(&sync)?;
    Ok(())
}

/// A window's surface elements. `loc` is the logical place of the window's
/// surface origin; the elements are in device pixels at `scale`.
pub fn window_elements(r: &mut GlesRenderer, w: &Window, loc: Point<i32, Logical>, scale: f64) -> Vec<WaylandSurfaceRenderElement<GlesRenderer>> {
    use smithay::backend::renderer::element::AsRenderElements;
    w.render_elements(r, loc.to_physical_precise_round(scale), Scale::from(scale), 1.0)
}

pub fn draw_elements(f: &mut GlesFrame<'_, '_>, els: &[WaylandSurfaceRenderElement<GlesRenderer>], scale: f64) -> Res<()> {
    for e in els.iter().rev() {
        let geo = e.geometry(Scale::from(scale));
        e.draw(f, e.src(), geo, &[Rectangle::from_size(geo.size)], &[], None)?;
    }
    Ok(())
}

/// Draw glass over `geo` (device pixels) from a blurred backdrop `blurred`, which may be smaller
/// than the output (the chain is downsampled): the source rectangle is `geo` scaled into it, and the
/// shader is told the OUTPUT size so its rectangle maths stays in output pixels. `lum_max` is the
/// luminance ceiling (0 = none); tint and edge colours go in as LINEAR light.
pub fn draw_glass(f: &mut GlesFrame<'_, '_>, prog: &GlesTexProgram, blurred: &GlesTexture, geo: Rectangle<i32, Physical>, out: Size<i32, Physical>, g: &theme::Glass, lum_max: f32, linear: bool) -> Res<()> {
    use smithay::backend::renderer::Texture;
    let ts = blurred.size();
    let (kx, ky) = (ts.w as f64 / out.w as f64, ts.h as f64 / out.h as f64);
    let dst = Rectangle::<i32, Physical>::new((geo.loc.x, geo.loc.y).into(), (geo.size.w, geo.size.h).into());
    let src = Rectangle::<f64, BufferCoords>::new((geo.loc.x as f64 * kx, geo.loc.y as f64 * ky).into(), (geo.size.w as f64 * kx, geo.size.h as f64 * ky).into());
    let (t, e) = (oklab_to_linear(g.tint.oklab), oklab_to_linear(g.edge.oklab));
    let u = [
        Uniform::new("rect", [geo.loc.x as f32, geo.loc.y as f32, geo.size.w as f32, geo.size.h as f32]),
        Uniform::new("texsize", (out.w as f32, out.h as f32)),
        Uniform::new("radius", g.corner_radius),
        Uniform::new("tint", [t[0], t[1], t[2], g.tint.strength]),
        Uniform::new("edge", [e[0], e[1], e[2], g.edge.strength]),
        Uniform::new("edge_width", g.edge.width),
        Uniform::new("lum_max", lum_max),
        Uniform::new("linear", if linear { 1.0f32 } else { 0.0 }),
    ];
    let local = Rectangle::<i32, Physical>::from_size(dst.size);
    f.render_texture_from_to(blurred, src, dst, &[local], &[], Transform::Normal, 1.0, Some(prog), &u)?;
    Ok(())
}

/// Whether xkb can compile a keymap for this input config.
fn xkb_accepts(i: &theme::Input) -> bool {
    use smithay::input::keyboard::xkb;
    let ctx = xkb::Context::new(xkb::CONTEXT_NO_FLAGS);
    xkb::Keymap::new_from_names(&ctx, &i.rules, &i.model, &i.layout, &i.variant, (!i.options.is_empty()).then(|| i.options.clone()), xkb::KEYMAP_COMPILE_NO_FLAGS).is_some()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn xkb_check_accepts_real_layouts_and_refuses_invented_ones() {
        let mut i = theme::Input::default();
        i.layout = "us".into();
        assert!(xkb_accepts(&i));
        i.layout = "no-such-layout-zz".into();
        assert!(!xkb_accepts(&i));
    }

    #[test]
    fn settings_edits_reach_the_file_and_a_bad_keymap_is_never_written() {
        let dir = std::env::temp_dir().join(format!("aurora-fx-edit-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join(THEME_FILE), "# kept\n[dock]\nedge = \"bottom\" # where\n").unwrap();
        let mut fx = Fx::new(Some(dir.clone()));
        fx.apply_edits(&[edit::Edit::s("dock", "edge", "left")]).unwrap();
        let text = std::fs::read_to_string(dir.join(THEME_FILE)).unwrap();
        assert!(text.contains("# kept") && text.contains("edge = \"left\" # where"), "{text}");
        let err = fx.apply_edits(&[edit::Edit::s("input", "layout", "no-such-layout-zz")]).unwrap_err();
        assert!(err.contains("nothing was written"), "{err}");
        assert_eq!(std::fs::read_to_string(dir.join(THEME_FILE)).unwrap(), text, "the file is untouched");
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn without_a_theme_directory_edits_apply_to_the_session_only() {
        let mut fx = Fx::new(None);
        let msg = fx.apply_edits(&[edit::Edit::n("output", "scale", 2.0)]).unwrap();
        assert!(msg.contains("session"), "{msg}");
        assert_eq!(fx.theme.output.scale, 2.0);
    }
}
