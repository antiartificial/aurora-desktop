//! Compositor-drawn shell chrome: the desktop background, the clock, and the
//! dock. No UI toolkit and no client surface: layout is arithmetic
//! (`layout.rs`), pixels come from a CPU `Canvas` (`trusted::draw`) through the
//! text stack (`text.rs`), and everything reaches the screen through the same
//! offscreen scene as windows. It is ordinary chrome: it is part of the
//! framebuffer captures read, and it is NOT part of the trusted overlay (which
//! is drawn after captures are served and so is absent from them).
//!
//! Units: the theme speaks logical pixels; `scale` converts to device pixels,
//! which are what windows, the pointer and captures use.

pub mod apps;
mod chrome;
pub mod layout;
pub mod menu;
pub mod motion;
pub mod settings;

use std::time::{Instant, SystemTime};

use smithay::{
    backend::{
        allocator::Fourcc,
        renderer::{
            Color32F, ImportMem,
            gles::{GlesFrame, GlesRenderer, GlesTexProgram, GlesTexture, Uniform},
        },
    },
    utils::{Buffer as BufferCoords, Physical, Rectangle, Size, Transform},
};

use crate::{
    fx::theme::{BgMode, Background, Desktop, Dock, FontCfg, Theme, oklab_to_srgb, oklab_to_u8},
    text::{FontSpec, TextSystem},
    trusted::draw::Canvas,
};
use layout::{Layout, Params, Rect};
use menu::{Action, Menu};
use settings::{Built, Page};

type Res<T> = Result<T, Box<dyn std::error::Error>>;

pub struct Item {
    pub id: String,
    pub label: String,
    pub clickable: bool,
    /// Why a click cannot launch this, from the registry. Empty means it can.
    /// A non-empty reason greys the item and makes its menu say so, rather
    /// than hiding an installed app or leaving a dead icon with no
    /// explanation.
    pub problem: String,
}

/// The settings panel's live state. What it shows is rebuilt from the theme.
pub struct SettingsState {
    pub page: usize,
    pub sel: Option<usize>,
    pub hover: Option<usize>,
    pub status: String,
    /// Keyboard goes to the panel while it is focused (opened, or last clicked).
    pub focused: bool,
    pub appear: motion::Appear,
    /// Where the panel's top-left is, in LOGICAL units. `None` means centred,
    /// which is where it opens; a drag pins it. Logical, not device, so the
    /// panel keeps its place across a scale change (src/scale.rs).
    pub pos: Option<(f64, f64)>,
    /// A move in progress: the offset in device pixels from the panel's
    /// top-left to the point the pointer grabbed it by.
    pub grab: Option<(f64, f64)>,
    /// Where the panel was last placed, device pixels. Only so a move can
    /// re-log without re-rasterising; not the source of truth (that is
    /// `place_settings`).
    rect: Option<Rect>,
    /// The text `settings::draw` placed, panel-relative device pixels, kept so
    /// a move can re-log a complete layout line without re-rasterising.
    placed: Vec<(String, i32, i32, i32)>,
    logged: bool,
    pages: Vec<Page>,
    built: Built,
    min_h: f32,
    tkey: String,
    key: String,
}

/// What a click on chrome means to the caller.
pub enum Press {
    Pass,
    Swallow,
    Act(Action),
}

/// One panel for the effects pipeline to draw: glass behind, texture over.
pub struct ChromeDraw<'a> {
    pub tex: &'a GlesTexture,
    /// Where it is drawn this frame (animated), device pixels.
    pub dst: Rect,
    pub alpha: f32,
}

pub struct Shell {
    pub text: TextSystem,
    pub scale: f64,
    start: Instant,
    dock: Dock,
    desktop: Desktop,
    font: FontCfg,
    bg: Background,
    glass_on: bool,
    glass_radius: f32,
    /// `--legacy-scene`: flat (25,25,25) background, no clock: the pre-shell look.
    pub legacy_scene: bool,
    /// `--freeze-time`: the background shader's `time` stays 0.
    pub freeze_time: bool,
    pub aurora_bin: String,
    pub registry: apps::Registry,
    items: Vec<Item>,
    widths: Vec<i32>,
    seen: (u64, u64),
    rev: u64,
    pub hover: Option<usize>,
    /// What the host (or `--scale`) says the scale is; the theme's `[output] scale` may override it.
    pub host_scale: f64,
    /// The theme as last synced, kept only while the settings panel is open.
    snap: Theme,
    pub menu: Option<Menu>,
    menu_seq: u64,
    pub settings: Option<SettingsState>,
    families: Vec<String>,
    // GPU side
    menu_tex: Option<(String, GlesTexture)>,
    settings_tex: Option<(String, GlesTexture)>,
    dummy: Option<GlesTexture>,
    bg_tex: Option<(String, GlesTexture)>,
    clock_tex: Option<(String, GlesTexture, Rect)>,
    dock_tex: Option<(String, GlesTexture)>,
    img_stamp: Option<(Instant, Option<SystemTime>)>,
}

impl Shell {
    pub fn new() -> Self {
        let font = FontCfg::default();
        let text = TextSystem::new(&font.family, &font.file);
        Shell {
            text,
            scale: 1.0,
            start: Instant::now(),
            dock: Dock::default(),
            desktop: Desktop::default(),
            font,
            bg: Background::default(),
            glass_on: true,
            glass_radius: 12.0,
            legacy_scene: false,
            freeze_time: false,
            aurora_bin: "aurora".into(),
            registry: apps::Registry::new(None),
            items: vec![],
            widths: vec![],
            seen: (u64::MAX, 0),
            rev: 0,
            hover: None,
            host_scale: 1.0,
            snap: Theme::default(),
            menu: None,
            menu_seq: 0,
            settings: None,
            families: vec![],
            menu_tex: None,
            settings_tex: None,
            dummy: None,
            bg_tex: None,
            clock_tex: None,
            dock_tex: None,
            img_stamp: None,
        }
    }

    pub fn dock_enabled(&self) -> bool {
        self.dock.enabled
    }

    /// Whether the shell draws anything of its own (background, dock).
    pub fn wants_pipeline(&self) -> bool {
        self.dock.enabled || !(self.legacy_scene) || self.menu.is_some() || self.settings.is_some()
    }

    /// Take over the theme (cheap when nothing changed) and recompute text-dependent state.
    pub fn sync(&mut self, theme: &Theme) {
        let mut changed = false;
        if self.dock != theme.dock || self.desktop != theme.desktop || self.bg != theme.background || self.glass_on != theme.glass.enabled || self.glass_radius != theme.glass.corner_radius {
            self.dock = theme.dock.clone();
            self.desktop = theme.desktop.clone();
            self.bg = theme.background.clone();
            self.glass_on = theme.glass.enabled;
            self.glass_radius = theme.glass.corner_radius;
            changed = true;
        }
        if self.rev == 0 || self.font.family != theme.font.family || self.font.file != theme.font.file {
            self.text = TextSystem::new(&theme.font.family, &theme.font.file);
            tracing::info!("font: {}", self.text.description);
            eprintln!("aurora-compositor: font: {}", self.text.description);
            changed = true;
        }
        if self.font != theme.font {
            self.font = theme.font.clone();
            changed = true;
        }
        if self.seen != (self.registry.generation, self.scale.to_bits()) {
            self.seen = (self.registry.generation, self.scale.to_bits());
            changed = true;
        }
        if self.settings.is_some() {
            self.snap = theme.clone();
        }
        if changed || self.rev == 0 {
            self.rebuild_items();
            self.rev += 1;
        }
    }

    fn rebuild_items(&mut self) {
        use apps::Status;
        // An app the registry says cannot be launched is shown, not hidden: it is
        // installed, and a dock that silently omits it is harder to understand
        // than one that shows it greyed with the reason. Kitchen Sink (binary
        // never built) and Haiku Ledger (verbs served on the bus, no run script)
        // are both this case today.
        self.items = self
            .registry
            .apps
            .iter()
            .map(|a| Item { id: a.id.clone(), label: a.name.clone(), clickable: true, problem: a.problem.clone() })
            .collect();
        if self.items.is_empty() {
            let msg = match &self.registry.status {
                Status::NoBus => "no registry (no --bus-socket)",
                Status::Pending => "asking the registry...",
                Status::Failed(_) => "registry unreachable",
                Status::Ok => "no apps installed",
            };
            self.items.push(Item { id: String::new(), label: msg.into(), clickable: false, problem: String::new() });
        }
        let spec = FontSpec::new(self.font.size);
        let scale = self.scale;
        self.widths = self.items.iter().map(|i| (self.text.string_width(&i.label, &spec, scale) as f64 * scale).ceil() as i32 + 2).collect();
        self.hover = None;
    }

    fn dev(&self, logical: f32) -> i32 {
        (logical as f64 * self.scale).round() as i32
    }

    fn params(&self) -> Params {
        Params {
            edge: self.dock.edge,
            item_h: self.dev(self.dock.item_height),
            gap: self.dev(self.dock.gap),
            pad: self.dev(self.dock.padding),
            spacing: self.dev(self.dock.spacing),
        }
    }

    /// Dock geometry for an output of `w` x `h` device pixels. Empty when the dock is off.
    pub fn layout(&self, w: i32, h: i32) -> Layout {
        if !self.dock.enabled {
            return Layout::default();
        }
        layout::compute(w, h, &self.params(), &self.widths)
    }

    /// The area windows may occupy: the output minus the dock's reserved strip.
    pub fn usable(&self, w: i32, h: i32) -> Rect {
        if !self.dock.enabled {
            return Rect { x: 0, y: 0, w, h };
        }
        layout::usable(w, h, self.dock.edge, self.layout(w, h).reserved)
    }

    pub fn item(&self, i: usize) -> Option<&Item> {
        self.items.get(i)
    }

    /// Per-frame housekeeping outside the renderer: the registry ask.
    pub fn tick(&mut self) {
        self.registry.tick();
    }

    // ---- launching -------------------------------------------------------

    /// Run `verb` of `app`: the launcher does the rest (arguments, then the
    /// powerbox for the file). Which verb is already decided, by the verb menu,
    /// so no terminal is spent on asking that. Returns the argv it ran.
    /// Launch a windowed app: its own binary, directly, with WAYLAND_DISPLAY
    /// pointing at Aurora -- the same way the desktop menu starts a terminal.
    ///
    /// Not a domain. Decision 0012 is blunt about what that costs: a windowed
    /// app runs outside the sandbox that is this project's whole security
    /// argument, because the Wayland socket is not yet a grantable capability.
    /// Nothing here pretends otherwise.
    pub fn run_windowed(&mut self, app: &str, exec: &str) -> Option<Vec<String>> {
        if exec.is_empty() {
            tracing::warn!("dock: {app:?} is windowed but the registry sent no exec path");
            return None;
        }
        let mut cmd = std::process::Command::new(exec);
        cmd.stdin(std::process::Stdio::null());
        if let Some(s) = &self.registry.sock {
            cmd.env("AURORA_BUS_SOCK", s);
        }
        match cmd.spawn() {
            Ok(mut child) => {
                tracing::info!("dock: launched windowed app {app:?} ({exec}), unsandboxed (decision 0012)");
                std::thread::spawn(move || {
                    let _ = child.wait();
                });
                Some(vec![exec.to_string()])
            }
            Err(e) => {
                tracing::error!("dock: cannot launch {app:?} ({exec}): {e}");
                None
            }
        }
    }

    pub fn run_verb(&mut self, app: &str, verb: &str) -> Option<Vec<String>> {
        // The menu should never offer a verb for an unlaunchable app, but a
        // spawn is not the place to find out it did.
        if let Some(p) = self.registry.apps.iter().find(|a| a.id == app).filter(|a| !a.launchable) {
            tracing::warn!("dock: refusing to launch {app:?}: {}", p.problem);
            return None;
        }
        let term = resolve_terminal(&self.dock.terminal);
        let argv = launch_argv(&self.aurora_bin, term.as_deref(), app, verb);
        let mut cmd = std::process::Command::new(&argv[0]);
        cmd.args(&argv[1..]).stdin(std::process::Stdio::null());
        if let Some(s) = &self.registry.sock {
            cmd.env("AURORA_BUS_SOCK", s);
        }
        match cmd.spawn() {
            Ok(mut child) => {
                tracing::info!("dock: launched {app:?} verb {verb:?} via {argv:?}");
                std::thread::spawn(move || {
                    let _ = child.wait();
                });
                Some(argv)
            }
            Err(e) => {
                tracing::error!("dock: cannot launch {argv:?}: {e}");
                None
            }
        }
    }

    /// Open a terminal (the dock's configured one) with no command.
    pub fn open_terminal(&mut self) {
        let Some(term) = resolve_terminal(&self.dock.terminal) else {
            tracing::error!("menu: no terminal to open");
            return;
        };
        match std::process::Command::new(&term).stdin(std::process::Stdio::null()).spawn() {
            Ok(mut child) => {
                tracing::info!("menu: opened terminal {term:?}");
                std::thread::spawn(move || {
                    let _ = child.wait();
                });
            }
            Err(e) => tracing::error!("menu: cannot open terminal {term:?}: {e}"),
        }
    }

    // ---- GPU resources ---------------------------------------------------

    /// Build or refresh every texture the frame will draw. Needs the renderer,
    /// so it runs before frames are opened.
    pub fn prepare(&mut self, r: &mut GlesRenderer, size: Size<i32, Physical>) -> Res<()> {
        if self.dummy.is_none() {
            self.dummy = Some(r.import_memory(&[0, 0, 0, 255], Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((1, 1)), false)?);
        }
        self.prepare_background(r, size)?;
        self.prepare_clock(r, size)?;
        self.prepare_dock(r, size)?;
        self.prepare_chrome(r, size)
    }

    fn bg_mode(&self) -> (BgMode, Option<[u8; 3]>) {
        if self.legacy_scene { (BgMode::Solid, Some([25, 25, 25])) } else { (self.bg.mode, None) }
    }

    fn prepare_background(&mut self, r: &mut GlesRenderer, size: Size<i32, Physical>) -> Res<()> {
        let (mode, fixed) = self.bg_mode();
        if mode == BgMode::Shader {
            self.bg_tex = None;
            return Ok(());
        }
        let stamp = if mode == BgMode::Image { self.image_mtime() } else { None };
        let key = format!("{mode:?}{fixed:?}{:?}{:?}{}{stamp:?}{}x{}", self.bg.top, self.bg.bottom, self.bg.image, size.w, size.h);
        if self.bg_tex.as_ref().is_some_and(|(k, _)| *k == key) {
            return Ok(());
        }
        let (w, h) = (size.w.max(1) as usize, size.h.max(1) as usize);
        let (pix, tw, th) = match mode {
            BgMode::Image => match load_cover(&self.bg.image, w, h) {
                Ok(p) => (p, w, h),
                Err(e) => {
                    tracing::error!("background: cannot use image {:?}: {e}; drawing the gradient instead", self.bg.image);
                    (gradient_strip(self.bg.top, self.bg.bottom, h), 1, h)
                }
            },
            BgMode::Gradient => (gradient_strip(self.bg.top, self.bg.bottom, h), 1, h),
            _ => {
                let c = fixed.unwrap_or_else(|| oklab_to_u8(self.bg.top));
                (vec![c[0], c[1], c[2], 255], 1, 1)
            }
        };
        let tex = r.import_memory(&pix, Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((tw as i32, th as i32)), false)?;
        self.bg_tex = Some((key, tex));
        Ok(())
    }

    /// Modification time of the wallpaper, looked up at most twice a second.
    fn image_mtime(&mut self) -> Option<SystemTime> {
        if let Some((t, m)) = &self.img_stamp {
            if t.elapsed().as_millis() < 500 {
                return *m;
            }
        }
        let m = std::fs::metadata(&self.bg.image).and_then(|m| m.modified()).ok();
        self.img_stamp = Some((Instant::now(), m));
        m
    }

    fn clock_strings(&self) -> (String, String) {
        let time = local_time("%H:%M");
        let mut second = String::new();
        if self.desktop.clock {
            second = local_time("%A, %-d %B");
        }
        if self.desktop.hostname {
            if !second.is_empty() {
                second.push_str("  \u{b7}  ");
            }
            second.push_str(&hostname());
        }
        (if self.desktop.clock { time } else { String::new() }, second)
    }

    fn prepare_clock(&mut self, r: &mut GlesRenderer, size: Size<i32, Physical>) -> Res<()> {
        if self.legacy_scene || !(self.desktop.clock || self.desktop.hostname) {
            self.clock_tex = None;
            return Ok(());
        }
        let (big, small) = self.clock_strings();
        let key = format!("{big}|{small}|{}|{}|{:?}|{}|{}", self.scale, self.font.size, self.desktop.clock_oklab, self.font.family, self.rev);
        if self.clock_tex.as_ref().is_some_and(|(k, _, _)| *k == key) {
            return Ok(());
        }
        let s = self.scale;
        let (bs, ss) = (self.font.size * 3.2, self.font.size * 1.05);
        let (bspec, sspec) = (FontSpec::new(bs), FontSpec::new(ss));
        let (bw, sw) = (self.text.string_width(&big, &bspec, s), self.text.string_width(&small, &sspec, s));
        let (bm, sm) = (self.text.metrics(&bspec), self.text.metrics(&sspec));
        let lw = bw.max(sw) + 8.0;
        let (bh, sh) = (if big.is_empty() { 0.0 } else { bm.ascent + bm.descent + 6.0 }, sm.ascent + sm.descent + 6.0);
        let (cw, ch) = ((lw as f64 * s).ceil() as usize, ((bh + sh) as f64 * s).ceil() as usize);
        let mut c = Canvas::new(cw.max(1), ch.max(1), [0, 0, 0, 0]);
        let col = oklab_to_u8(self.desktop.clock_oklab);
        let shadow = [0, 0, 0, 90];
        let off = 1.0 / s as f32 * 1.5;
        if !big.is_empty() {
            let x = lw - bw - 4.0;
            self.text.draw(&mut c, x + off, 2.0 + off, &big, &bspec, shadow, s);
            self.text.draw(&mut c, x, 2.0, &big, &bspec, [col[0], col[1], col[2], 235], s);
        }
        let x = lw - sw - 4.0;
        self.text.draw(&mut c, x + off, bh + 2.0 + off, &small, &sspec, shadow, s);
        self.text.draw(&mut c, x, bh + 2.0, &small, &sspec, [col[0], col[1], col[2], 200], s);
        let tex = r.import_memory(&c.rgba, Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((c.w as i32, c.h as i32)), false)?;
        let margin = self.dev(28.0);
        let usable = self.usable(size.w, size.h);
        let rect = Rect { x: usable.x + usable.w - c.w as i32 - margin, y: usable.y + margin, w: c.w as i32, h: c.h as i32 };
        self.clock_tex = Some((key, tex, rect));
        Ok(())
    }

    fn prepare_dock(&mut self, r: &mut GlesRenderer, size: Size<i32, Physical>) -> Res<()> {
        if !self.dock.enabled {
            self.dock_tex = None;
            return Ok(());
        }
        let l = self.layout(size.w, size.h);
        let key = format!("{}x{}|{:?}|{}|{}|{}", l.panel.w, l.panel.h, self.hover, self.rev, self.glass_on, self.scale);
        if self.dock_tex.as_ref().is_some_and(|(k, _)| *k == key) {
            return Ok(());
        }
        let r4 = |r: &Rect| format!("{},{},{},{}", r.x, r.y, r.w, r.h);
        tracing::info!(
            "dock: layout scale={} panel={} reserved={} items=[{}]",
            self.scale, r4(&l.panel), r4(&l.reserved), l.items.iter().map(r4).collect::<Vec<_>>().join(";")
        );
        let c = self.draw_dock(&l);
        let tex = r.import_memory(&c.rgba, Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((c.w as i32, c.h as i32)), false)?;
        self.dock_tex = Some((key, tex));
        Ok(())
    }

    /// Rasterise the dock panel (premultiplied RGBA, device pixels).
    pub fn draw_dock(&mut self, l: &Layout) -> Canvas {
        let (pw, ph) = (l.panel.w.max(1) as usize, l.panel.h.max(1) as usize);
        let mut c = Canvas::new(pw, ph, [0, 0, 0, 0]);
        let s = self.scale;
        if !self.glass_on {
            c.round_rect(0, 0, pw as i32, ph as i32, self.glass_radius * s as f32, [18, 20, 30, 215]);
        }
        let text = oklab_to_u8(self.dock.text_oklab);
        let hov = oklab_to_u8(self.dock.hover_oklab);
        let item_h = self.dev(self.dock.item_height);
        let (tile, m) = layout::tile(item_h);
        let spec = FontSpec::new(self.font.size);
        let tile_spec = FontSpec::new(self.font.size * 1.15).bold();
        let (met, tmet) = (self.text.metrics(&spec), self.text.metrics(&tile_spec));
        for (i, r) in l.items.iter().enumerate() {
            let Some(item) = self.items.get(i) else { break };
            let (lx, ly) = (r.x - l.panel.x, r.y - l.panel.y);
            let dim = !item.clickable || !item.problem.is_empty();
            if self.hover == Some(i) && !dim {
                c.round_rect(lx, ly, r.w, r.h, item_h as f32 * 0.3, [hov[0], hov[1], hov[2], (self.dock.hover_strength * 255.0) as u8]);
            }
            if dim {
                let y = ((ly as f32 + r.h as f32 / 2.0) / s as f32) - (met.ascent + met.descent) / 2.0;
                self.text.draw(&mut c, (lx + m) as f32 / s as f32, y, &item.label, &spec, [text[0], text[1], text[2], 150], s);
                continue;
            }
            let tc = tile_colour(&item.id);
            c.round_rect(lx + m, ly + m, tile, tile, tile as f32 * 0.28, [tc[0], tc[1], tc[2], 255]);
            let initial: String = item.label.chars().next().map(|ch| ch.to_uppercase().collect()).unwrap_or_default();
            let iw = self.text.string_width(&initial, &tile_spec, s);
            let tx = (lx + m) as f32 / s as f32 + (tile as f32 / s as f32 - iw) / 2.0;
            let ty = ((ly + m) as f32 + tile as f32 / 2.0) / s as f32 - (tmet.ascent + tmet.descent) / 2.0;
            self.text.draw(&mut c, tx, ty, &initial, &tile_spec, [255, 255, 255, 255], s);
            let y = (ly as f32 + r.h as f32 / 2.0) / s as f32 - (met.ascent + met.descent) / 2.0;
            self.text.draw(&mut c, (lx + m + tile + m) as f32 / s as f32, y, &item.label, &spec, [text[0], text[1], text[2], 255], s);
        }
        c
    }

    // ---- drawing into an open frame -------------------------------------

    /// The desktop: shader, image, gradient or solid colour, over the whole output.
    pub fn draw_background(&self, f: &mut GlesFrame<'_, '_>, bg_prog: &GlesTexProgram, size: Size<i32, Physical>) -> Res<()> {
        let full = Rectangle::<i32, Physical>::from_size(size);
        let (mode, _) = self.bg_mode();
        if mode == BgMode::Shader {
            let tex = self.dummy.as_ref().ok_or("no placeholder texture")?;
            let t = if self.freeze_time { 0.0 } else { self.start.elapsed().as_secs_f32() * self.bg.speed };
            let (top, bottom, accent) = (oklab_to_srgb(self.bg.top), oklab_to_srgb(self.bg.bottom), oklab_to_srgb(self.bg.accent));
            let u = [
                Uniform::new("time", t),
                Uniform::new("res", (size.w as f32, size.h as f32)),
                Uniform::new("top", top),
                Uniform::new("bottom", bottom),
                Uniform::new("accent", accent),
            ];
            let src = Rectangle::<f64, BufferCoords>::from_size((1.0, 1.0).into());
            f.render_texture_from_to(tex, src, full, &[full], &[], Transform::Normal, 1.0, Some(bg_prog), &u)?;
        } else if let Some((_, tex)) = &self.bg_tex {
            use smithay::backend::renderer::Texture;
            let ts = tex.size();
            let src = Rectangle::<f64, BufferCoords>::from_size((ts.w as f64, ts.h as f64).into());
            f.render_texture_from_to(tex, src, full, &[full], &[], Transform::Normal, 1.0, None, &[])?;
        }
        if let Some((_, tex, rect)) = &self.clock_tex {
            draw_tex(f, tex, *rect)?;
        }
        Ok(())
    }

    /// Where the dock panel is and its texture, when there is one to draw.
    pub fn dock_draw(&self, size: Size<i32, Physical>) -> Option<(&GlesTexture, Rect)> {
        let (_, tex) = self.dock_tex.as_ref()?;
        Some((tex, self.layout(size.w, size.h).panel))
    }

    pub fn dock_radius(&self) -> f32 {
        self.glass_radius * self.scale as f32
    }

    pub fn clear_colour() -> Color32F {
        let c = crate::render::CLEAR;
        Color32F::new(c[0], c[1], c[2], 1.0)
    }
}

/// A texture over `dst` (device pixels) at `alpha`, whatever the texture's own size.
pub fn draw_tex_alpha(f: &mut GlesFrame<'_, '_>, tex: &GlesTexture, dst: Rect, alpha: f32) -> Res<()> {
    use smithay::backend::renderer::Texture;
    let ts = tex.size();
    let d = Rectangle::<i32, Physical>::new((dst.x, dst.y).into(), (dst.w, dst.h).into());
    let src = Rectangle::<f64, BufferCoords>::from_size((ts.w as f64, ts.h as f64).into());
    let local = Rectangle::<i32, Physical>::from_size(d.size);
    f.render_texture_from_to(tex, src, d, &[local], &[], Transform::Normal, alpha, None, &[])?;
    Ok(())
}

pub fn draw_tex(f: &mut GlesFrame<'_, '_>, tex: &GlesTexture, r: Rect) -> Res<()> {
    let dst = Rectangle::<i32, Physical>::new((r.x, r.y).into(), (r.w, r.h).into());
    let src = Rectangle::<f64, BufferCoords>::from_size((r.w as f64, r.h as f64).into());
    let local = Rectangle::<i32, Physical>::from_size(dst.size);
    f.render_texture_from_to(tex, src, dst, &[local], &[], Transform::Normal, 1.0, None, &[])?;
    Ok(())
}

/// A vertical Oklab gradient as a 1 x `h` RGBA strip (stretched across the output when drawn).
pub fn gradient_strip(top: [f32; 3], bottom: [f32; 3], h: usize) -> Vec<u8> {
    let mut out = Vec::with_capacity(h * 4);
    for y in 0..h {
        let t = if h > 1 { y as f32 / (h - 1) as f32 } else { 0.0 };
        let lab = [0, 1, 2].map(|k| top[k] + (bottom[k] - top[k]) * t);
        let c = oklab_to_u8(lab);
        out.extend_from_slice(&[c[0], c[1], c[2], 255]);
    }
    out
}

/// Decode `path` and scale it to COVER `w` x `h` (the smaller axis exactly fills,
/// the other overflows), cropped to the centre. RGBA bytes.
pub fn load_cover(path: &str, w: usize, h: usize) -> Result<Vec<u8>, String> {
    let img = image::open(path).map_err(|e| e.to_string())?.to_rgba8();
    Ok(cover(&img, w, h).into_raw())
}

pub fn cover(img: &image::RgbaImage, w: usize, h: usize) -> image::RgbaImage {
    use image::imageops::{FilterType, crop_imm, resize};
    let (iw, ih) = (img.width() as f64, img.height() as f64);
    let k = (w as f64 / iw).max(h as f64 / ih);
    let (sw, sh) = (((iw * k).ceil() as u32).max(w as u32), ((ih * k).ceil() as u32).max(h as u32));
    let scaled = resize(img, sw, sh, FilterType::Lanczos3);
    let (x, y) = ((sw - w as u32) / 2, (sh - h as u32) / 2);
    crop_imm(&scaled, x, y, w as u32, h as u32).to_image()
}

fn tile_colour(id: &str) -> [u8; 3] {
    let h = id.bytes().fold(2166136261u32, |a, b| (a ^ b as u32).wrapping_mul(16777619));
    let hue = (h % 360) as f32 * std::f32::consts::PI / 180.0;
    oklab_to_u8([0.62, 0.11 * hue.cos(), 0.11 * hue.sin()])
}

pub fn hostname() -> String {
    let mut buf = [0u8; 256];
    let ok = unsafe { libc::gethostname(buf.as_mut_ptr().cast(), buf.len() - 1) } == 0;
    if !ok {
        return String::new();
    }
    let n = buf.iter().position(|&b| b == 0).unwrap_or(0);
    String::from_utf8_lossy(&buf[..n]).into_owned()
}

fn local_time(fmt: &str) -> String {
    unsafe {
        let now = libc::time(std::ptr::null_mut());
        let mut tm: libc::tm = std::mem::zeroed();
        libc::localtime_r(&now, &mut tm);
        let mut buf = [0u8; 128];
        let f = std::ffi::CString::new(fmt).unwrap();
        let n = libc::strftime(buf.as_mut_ptr().cast(), buf.len(), f.as_ptr(), &tm);
        String::from_utf8_lossy(&buf[..n]).into_owned()
    }
}

/// `auto` -> the first terminal found on PATH; empty -> none; else the command as given.
pub fn resolve_terminal(cfg: &str) -> Option<String> {
    match cfg {
        "" => None,
        "auto" => ["ghostty", "foot", "kitty", "alacritty", "konsole", "xterm"].into_iter().find(|t| on_path(t)).map(String::from),
        t => Some(t.to_string()),
    }
}

fn on_path(cmd: &str) -> bool {
    std::env::var_os("PATH").is_some_and(|p| std::env::split_paths(&p).any(|d| d.join(cmd).is_file()))
}

/// Where the `aurora` launcher is. A dock click runs it, and the compositor's
/// PATH is whatever started the compositor -- usually not the build tree -- so
/// a bare name fails with `aurora: command not found` only once you click,
/// which is far too late to be useful.
///
/// `--aurora` wins. Otherwise: PATH, then beside this executable, then the
/// dev layout (`compositor/target/<profile>/aurora-compositor` sits four
/// levels under the repo root, where the Go binaries land in `build/bin`).
/// Found nothing: return the bare name and say so now, not on the click.
pub fn resolve_launcher(given: Option<&str>) -> String {
    if let Some(a) = given {
        if !std::path::Path::new(a).is_file() && !on_path(a) {
            eprintln!("aurora-compositor: WARNING: --aurora {a}: not a file and not on PATH; launching from the dock will fail");
        }
        return a.to_string();
    }
    if on_path("aurora") {
        return "aurora".into();
    }
    let exe = std::env::current_exe().ok();
    let candidates = exe.as_deref().and_then(|e| e.parent()).map(|d| {
        vec![d.join("aurora"), d.join("../../../build/bin/aurora"), d.join("../../../../build/bin/aurora")]
    }).unwrap_or_default();
    for c in candidates {
        if c.is_file() {
            let c = c.canonicalize().unwrap_or(c);
            tracing::info!("dock: launcher is {} (not on PATH; found next to the compositor)", c.display());
            return c.to_string_lossy().into_owned();
        }
    }
    eprintln!("aurora-compositor: WARNING: no `aurora` launcher on PATH or beside this binary; launching from the dock will fail. Pass --aurora /path/to/aurora");
    "aurora".into()
}

/// What choosing a verb runs: `aurora open --pick --app ID --verb NAME` (the
/// powerbox picks the file; an interactive terminal flow), inside a terminal
/// window when one is configured so the prompts have somewhere to appear.
pub fn launch_argv(aurora: &str, terminal: Option<&str>, app_id: &str, verb: &str) -> Vec<String> {
    let inner = [aurora, "open", "--pick", "--app", app_id, "--verb", verb].map(String::from);

    match terminal {
        None => inner.to_vec(),
        Some(t) => {
            let script = "\"$@\"; printf '\\nPress Enter to close '; read -r _";
            let mut v: Vec<String> = [t, "-e", "sh", "-c", script, "aurora-launch"].map(String::from).to_vec();
            v.extend(inner);
            v
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn launch_runs_the_launcher_directly_or_inside_a_terminal() {
        assert_eq!(launch_argv("aurora", None, "org.x.app", "resize@v1"), ["aurora", "open", "--pick", "--app", "org.x.app", "--verb", "resize@v1"]);
        let v = launch_argv("/bin/aurora", Some("xterm"), "org.x.app", "resize@v1");
        assert_eq!(&v[..3], ["xterm", "-e", "sh"]);
        assert_eq!(&v[v.len() - 7..], ["/bin/aurora", "open", "--pick", "--app", "org.x.app", "--verb", "resize@v1"]);
    }

    #[test]
    fn cover_fills_the_output_and_crops_the_centre() {
        // 4x2 image: left half red, right half blue; cover into 2x2 keeps the middle 2 columns' worth.
        let mut img = image::RgbaImage::new(4, 2);
        for (x, _, p) in img.enumerate_pixels_mut() {
            *p = if x < 2 { image::Rgba([255, 0, 0, 255]) } else { image::Rgba([0, 0, 255, 255]) };
        }
        let out = cover(&img, 2, 2);
        assert_eq!((out.width(), out.height()), (2, 2));
        let l = out.get_pixel(0, 0).0;
        let r = out.get_pixel(1, 0).0;
        assert!(l[0] > 200 && r[2] > 200, "centre crop keeps one red and one blue column: {l:?} {r:?}");
        // a wide target from a square image scales by width and crops top and bottom
        let sq = image::RgbaImage::from_pixel(10, 10, image::Rgba([9, 9, 9, 255]));
        let wide = cover(&sq, 40, 10);
        assert_eq!((wide.width(), wide.height()), (40, 10));
    }

    #[test]
    fn gradient_runs_from_the_top_colour_to_the_bottom_colour() {
        let g = gradient_strip([0.9, 0.0, 0.0], [0.1, 0.0, 0.0], 100);
        assert!(g[0] > 200 && g[99 * 4] < 40 && g[3] == 255);
    }
}
