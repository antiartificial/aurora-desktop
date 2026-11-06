//! Phase 1's look inside the compositor: glass (dual-Kawase blur of what is
//! behind a window, tint, highlight edge) and the window switcher, as render
//! passes, with parameters and shaders hot-reloaded from `contracts/theme/`.
//!
//! The scene is composed in an offscreen texture, window by window from the
//! back: before a window is drawn, everything below it is blurred and drawn
//! under its rectangle. The finished scene is blitted into the output
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
            gles::{GlesFrame, GlesRenderer, GlesTarget, GlesTexProgram, GlesTexture, Uniform, UniformName, UniformType},
        },
    },
    desktop::{Space, Window},
    output::Output,
    utils::{Buffer as BufferCoords, Logical, Physical, Point, Rectangle, Scale, Size, Transform},
};

use theme::{BUILTIN_SHADERS, BUILTIN_THEME, Files, SHADERS, THEME_FILE, Theme, oklab_to_srgb};

pub type Res<T> = Result<T, Box<dyn Error>>;
const POLL: Duration = Duration::from_millis(250);

pub struct Scratch {
    pub size: (i32, i32),
    pub passes: usize,
    pub scene: GlesTexture,
    pub down: Vec<GlesTexture>,
    pub up: Vec<GlesTexture>,
}

pub struct Fx {
    pub theme: Theme,
    files: Option<Files>,
    initialised: bool,
    next_poll: Instant,
    pub progs: [Option<GlesTexProgram>; 5], // down, up, glass, card, background: each keeps its last good version
    pub switcher: switcher::Switcher,
    scratch: Option<Scratch>,
    cards: Vec<GlesTexture>,
    /// Count of successful theme (file or shader) loads; tests and logs read it.
    pub loads: u32,
    /// The theme text when there is no theme directory: edits live here, for the session only.
    mem_text: Option<String>,
}

fn uniform_names(i: usize) -> Vec<UniformName<'static>> {
    use UniformType::*;
    let l: &[(&'static str, UniformType)] = match i {
        0 | 1 => &[("halfpixel", _2f), ("offset", _1f)],
        4 => &[("time", _1f), ("res", _2f), ("top", _3f), ("bottom", _3f), ("accent", _3f)],
        2 => &[("rect", _4f), ("texsize", _2f), ("radius", _1f), ("tint", _4f), ("edge", _4f), ("edge_width", _1f)],
        _ => &[("screen", _2f), ("pose", _4f), ("csize", _2f), ("focal", _1f), ("shade", _1f), ("sel", _1f), ("accent", _3f)],
    };
    l.iter().map(|(n, t)| UniformName::new(*n, *t)).collect()
}

impl Fx {
    pub fn new(dir: Option<PathBuf>) -> Fx {
        Fx {
            theme: Theme::default(), files: dir.map(Files::new), initialised: false, next_poll: Instant::now(),
            progs: Default::default(), switcher: Default::default(), scratch: None, cards: vec![], loads: 0, mem_text: None,
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
            None if !self.initialised => vec![true; 6],
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
        for i in 0..5 {
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

    fn ensure(&mut self, r: &mut GlesRenderer, size: Size<i32, Physical>) -> Res<()> {
        let passes = self.theme.glass.blur_passes as usize;
        if self.scratch.as_ref().is_some_and(|s| s.size == (size.w, size.h) && s.passes == passes) { return Ok(()); }
        let mut lvl = |k: usize| make_tex(r, (size.w >> k).max(1), (size.h >> k).max(1));
        let (mut down, mut up) = (vec![], vec![]);
        for k in 1..=passes { down.push(lvl(k)?); }
        for k in 0..passes { up.push(lvl(k)?); }
        let scene = make_tex(r, size.w, size.h)?;
        self.scratch = Some(Scratch { size: (size.w, size.h), passes, scene, down, up });
        Ok(())
    }

    /// Compose the scene (and the switcher, if open) and blit it to `fb`.
    pub fn render_scene(&mut self, r: &mut GlesRenderer, fb: &mut GlesTarget<'_>, output: &Output, size: Size<i32, Physical>, space: &Space<Window>, shell: &mut crate::shell::Shell) -> Res<()> {
        self.ensure(r, size)?;
        shell.prepare(r, size)?;
        let scale = output.current_scale().fractional_scale();
        // The theme speaks logical pixels: radii, edge width and blur spread grow with the scale.
        let mut glass = self.theme.glass.clone();
        glass.corner_radius *= scale as f32;
        glass.blur_radius *= scale as f32;
        glass.edge.width *= scale as f32;
        let full = Rectangle::<i32, Physical>::from_size(size);
        let progs: Vec<&GlesTexProgram> = self.progs.iter().flatten().collect();
        let (down, up, glass_p, bg_p) = (progs[0], progs[1], progs[2], progs[4]);
        let s = self.scratch.as_mut().unwrap();
        {
            let mut t = r.bind(&mut s.scene)?;
            let mut f = r.render(&mut t, size, Transform::Normal)?;
            f.clear(Color32F::new(crate::render::CLEAR[0], crate::render::CLEAR[1], crate::render::CLEAR[2], 1.0), &[full])?;
            shell.draw_background(&mut f, bg_p, size)?;
            let sy = f.finish()?; wait(r, sy)?;
        }
        for w in space.elements() {
            let geo = crate::scale::rect_to_physical(space.element_geometry(w).ok_or("window without geometry")?, scale);
            let els = window_elements(r, w, space.element_location(w).unwrap_or_default() - w.geometry().loc, scale);
            if glass.enabled { blur::run(r, s, down, up, &glass, size)?; }
            let mut t = r.bind(&mut s.scene)?;
            let mut f = r.render(&mut t, size, Transform::Normal)?;
            if glass.enabled { draw_glass(&mut f, glass_p, &s.up[0], geo, &glass)?; }
            draw_elements(&mut f, &els, scale)?;
            let sy = f.finish()?; wait(r, sy)?;
        }
        if let Some((tex, panel)) = shell.dock_draw(size) {
            let geo = Rectangle::<i32, Physical>::new((panel.x, panel.y).into(), (panel.w, panel.h).into());
            if glass.enabled { blur::run(r, s, down, up, &glass, size)?; }
            let mut t = r.bind(&mut s.scene)?;
            let mut f = r.render(&mut t, size, Transform::Normal)?;
            if glass.enabled {
                let mut g = glass.clone();
                g.corner_radius = shell.dock_radius();
                draw_glass(&mut f, glass_p, &s.up[0], geo, &g)?;
            }
            crate::shell::draw_tex(&mut f, tex, panel)?;
            let sy = f.finish()?; wait(r, sy)?;
        }
        // Menu and settings panel: ordinary chrome, in the scene, so captures see them.
        // Glass fades in with the animation by scaling its tint and edge strength.
        for p in shell.chrome_draws(size) {
            let geo = Rectangle::<i32, Physical>::new((p.dst.x, p.dst.y).into(), (p.dst.w, p.dst.h).into());
            if glass.enabled { blur::run(r, s, down, up, &glass, size)?; }
            let mut t = r.bind(&mut s.scene)?;
            let mut f = r.render(&mut t, size, Transform::Normal)?;
            if glass.enabled {
                let mut g = glass.clone();
                g.corner_radius = shell.dock_radius();
                g.tint.strength *= p.alpha;
                g.edge.strength *= p.alpha;
                draw_glass(&mut f, glass_p, &s.up[0], geo, &g)?;
            }
            crate::shell::draw_tex_alpha(&mut f, p.tex, p.dst, p.alpha)?;
            let sy = f.finish()?; wait(r, sy)?;
        }
        if self.switcher.open {
            switcher::draw(self, r, size, scale, space)?;
        }
        let s = self.scratch.as_ref().unwrap();
        let mut f = r.render(fb, size, output.current_transform())?;
        let src = Rectangle::<f64, BufferCoords>::from_size((size.w as f64, size.h as f64).into());
        f.render_texture_from_to(&s.scene, src, full, &[full], &[], Transform::Normal, 1.0, None, &[])?;
        let sy = f.finish()?;
        wait(r, sy)
    }
}

pub fn make_tex(r: &mut GlesRenderer, w: i32, h: i32) -> Res<GlesTexture> {
    Ok(r.create_buffer(Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((w.max(1), h.max(1))))?)
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

fn draw_glass(f: &mut GlesFrame<'_, '_>, prog: &GlesTexProgram, blurred: &GlesTexture, geo: Rectangle<i32, Physical>, g: &theme::Glass) -> Res<()> {
    use smithay::backend::renderer::Texture;
    let ts = blurred.size();
    let dst = Rectangle::<i32, Physical>::new((geo.loc.x, geo.loc.y).into(), (geo.size.w, geo.size.h).into());
    let src = Rectangle::<f64, BufferCoords>::new((geo.loc.x as f64, geo.loc.y as f64).into(), (geo.size.w as f64, geo.size.h as f64).into());
    let (t, e) = (oklab_to_srgb(g.tint.oklab), oklab_to_srgb(g.edge.oklab));
    let u = [
        Uniform::new("rect", [geo.loc.x as f32, geo.loc.y as f32, geo.size.w as f32, geo.size.h as f32]),
        Uniform::new("texsize", (ts.w as f32, ts.h as f32)),
        Uniform::new("radius", g.corner_radius),
        Uniform::new("tint", [t[0], t[1], t[2], g.tint.strength]),
        Uniform::new("edge", [e[0], e[1], e[2], g.edge.strength]),
        Uniform::new("edge_width", g.edge.width),
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
