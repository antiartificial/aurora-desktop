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

use theme::{BUILTIN_SHADERS, BUILTIN_THEME, Files, SHADERS, Theme, oklab_to_srgb};

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
            progs: Default::default(), switcher: Default::default(), scratch: None, cards: vec![], loads: 0,
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
        let glass = self.theme.glass.clone();
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
            let geo = space.element_geometry(w).ok_or("window without geometry")?;
            let els = window_elements(r, w, space.element_location(w).unwrap_or_default() - w.geometry().loc);
            if glass.enabled { blur::run(r, s, down, up, &glass, size)?; }
            let mut t = r.bind(&mut s.scene)?;
            let mut f = r.render(&mut t, size, Transform::Normal)?;
            if glass.enabled { draw_glass(&mut f, glass_p, &s.up[0], geo, &glass)?; }
            draw_elements(&mut f, &els)?;
            let sy = f.finish()?; wait(r, sy)?;
        }
        if let Some((tex, panel)) = shell.dock_draw(size) {
            let geo = Rectangle::<i32, Logical>::new((panel.x, panel.y).into(), (panel.w, panel.h).into());
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
        if self.switcher.open {
            switcher::draw(self, r, size, space)?;
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

pub fn window_elements(r: &mut GlesRenderer, w: &Window, loc: Point<i32, Logical>) -> Vec<WaylandSurfaceRenderElement<GlesRenderer>> {
    use smithay::backend::renderer::element::AsRenderElements;
    w.render_elements(r, loc.to_physical(1), Scale::from(1.0), 1.0)
}

pub fn draw_elements(f: &mut GlesFrame<'_, '_>, els: &[WaylandSurfaceRenderElement<GlesRenderer>]) -> Res<()> {
    for e in els.iter().rev() {
        let geo = e.geometry(Scale::from(1.0));
        e.draw(f, e.src(), geo, &[Rectangle::from_size(geo.size)], &[], None)?;
    }
    Ok(())
}

fn draw_glass(f: &mut GlesFrame<'_, '_>, prog: &GlesTexProgram, blurred: &GlesTexture, geo: Rectangle<i32, Logical>, g: &theme::Glass) -> Res<()> {
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
