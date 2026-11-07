//! The shell's popups: the desktop menu and the settings panel. Both are
//! ordinary compositor chrome like the dock: textures drawn into the scene
//! (so they are in captures), with glass behind them. They are NOT the trusted
//! overlay, which is drawn after captures are served.

use smithay::{
    backend::{
        allocator::Fourcc,
        renderer::{ImportMem, gles::{GlesRenderer, GlesTexture}},
    },
    utils::{Buffer as BufferCoords, Physical, Size},
};

use super::{
    ChromeDraw, Press, Res, SettingsState, Shell,
    layout::Rect,
    apps::{Status, display_name, display_text},
    menu::{Action, Entry, KeyOut as MenuKey, Menu, Style, Tone},
    motion::{Appear, PANEL_MS},
    resolve_terminal,
    settings::{self, Ctx, HotKind},
};
use crate::{
    fx::{
        edit::{Edit, Val},
        theme::{BgMode, DockEdge, oklab_to_u8},
    },
    trusted::draw::Canvas,
};

type Out = (i32, i32);

fn d(v: f32, s: f64) -> i32 {
    (v as f64 * s).round() as i32
}

/// The effect class as shown to a person before they choose. The classes are
/// the manifest's (`read`, `write`, `destructive`, `external`); destructive and
/// external carry the same `!!` mark `aurora open` prints, because they are the
/// ones the controller holds for approval. Anything unrecognised is shown as
/// received (cleaned) and treated as dangerous: an unknown effect is not a safe one.
fn effect_tag(effect: &str) -> (String, Tone) {
    match effect {
        "read" => ("read".into(), Tone::Plain),
        "write" => ("write".into(), Tone::Caution),
        "destructive" | "external" => (format!("!! {effect}"), Tone::Danger),
        "" => ("effect unknown".into(), Tone::Caution),
        other => (format!("!! {}", display_name(other)), Tone::Danger),
    }
}

/// How much of the panel's title band must stay on screen, logical pixels.
const ON_SCREEN: f64 = 140.0;

/// Where the settings panel goes. The single source of truth: `settings_rect`
/// hit-tests with it, `chrome_draws` draws with it, and `prepare_settings`
/// logs it. It used to be computed twice, and a dragged panel then drew in one
/// place and logged another.
///
/// `pos` is the dragged top-left in LOGICAL units, or `None` for centred.
/// `size` and the result are device pixels. Clamping happens here, at read
/// time rather than at drag time, so a resize or a scale change cannot strand
/// a panel that was legally placed before it: the title band is the only way
/// to move the panel, so the invariant is that the band stays reachable.
fn place_settings(pos: Option<(f64, f64)>, size: (i32, i32), drag_h: f32, out: Out, s: f64) -> Rect {
    let (w, h) = size;
    let (x, y) = match pos {
        Some((lx, ly)) => ((lx * s).round() as i32, (ly * s).round() as i32),
        None => ((out.0 - w) / 2, ((out.1 - h) / 2).max(0)),
    };
    let on = (ON_SCREEN * s).round() as i32;
    let band = (drag_h as f64 * s).round() as i32;
    Rect {
        x: x.clamp((on - w).min(0), (out.0 - on).max(0)),
        y: y.clamp(0, (out.1 - band).max(0)),
        w,
        h,
    }
}

impl Shell {
    fn style(&self) -> Style {
        Style {
            font: self.font.size,
            scale: self.scale,
            text: oklab_to_u8(self.dock.text_oklab),
            hover: oklab_to_u8(self.dock.hover_oklab),
            hover_strength: self.dock.hover_strength,
            glass_on: self.glass_on,
            radius: self.glass_radius,
        }
    }

    pub fn menu_rect(&self) -> Option<Rect> {
        let m = self.menu.as_ref()?;
        let s = self.scale;
        Some(Rect { x: d(m.origin.0, s), y: d(m.origin.1, s), w: (m.size.0 as f64 * s).ceil() as i32, h: (m.size.1 as f64 * s).ceil() as i32 })
    }

    pub fn settings_rect(&self, out: Out) -> Option<Rect> {
        let st = self.settings.as_ref()?;
        let s = self.scale;
        let (w, h) = ((st.built.size.0.max(settings::WIDTH) as f64 * s).ceil() as i32, (st.built.size.1 as f64 * s).ceil() as i32);
        Some(place_settings(st.pos, (w, h), st.built.drag_h, out, s))
    }

    // ---- opening ---------------------------------------------------------------

    /// Open the desktop menu with its corner at `at` (logical coordinates).
    pub fn open_menu(&mut self, at: (f64, f64), out: Out, windows: usize) {
        let term = resolve_terminal(&self.dock.terminal);
        let mut e = vec![];
        e.push(match &term {
            Some(t) => Entry::item("New Terminal", Action::OpenTerminal).note(t),
            None => Entry::item("New Terminal", Action::OpenTerminal).disabled("no terminal found"),
        });
        e.push(Entry::item("Settings\u{2026}", Action::OpenSettings));
        e.push(if windows > 0 { Entry::item("Window Switcher", Action::WindowSwitcher).note("Alt+Tab") } else { Entry::item("Window Switcher", Action::WindowSwitcher).disabled("no windows") });
        e.push(Entry::Separator);
        e.push(Entry::Header("Background".into()));
        for (m, key, label) in [(BgMode::Shader, "shader", "Shader"), (BgMode::Image, "image", "Image"), (BgMode::Gradient, "gradient", "Gradient"), (BgMode::Solid, "solid", "Solid colour")] {
            let it = Entry::item(label, Action::Edit(vec![Edit::s("background", "mode", key)])).checked(self.bg.mode == m);
            e.push(if m == BgMode::Image && self.bg.image.is_empty() { it.disabled("no image set") } else { it });
        }
        e.push(Entry::Separator);
        let (next, name) = match self.dock.edge {
            DockEdge::Bottom => ("left", "Left"),
            DockEdge::Left => ("top", "Top"),
            DockEdge::Top => ("right", "Right"),
            DockEdge::Right => ("bottom", "Bottom"),
        };
        e.push(Entry::item(&format!("Move Dock to {name}"), Action::Edit(vec![Edit::s("dock", "edge", next)])));
        let on = self.dock.enabled;
        e.push(Entry::item(if on { "Hide Dock" } else { "Show Dock" }, Action::Edit(vec![Edit::new("dock", "enabled", Val::Bool(!on))])));
        // The Deskbar floats over windows, so a way to put it away has to be one right-click from the desktop.
        e.push(Entry::item(if self.deskbar_enabled { "Hide Deskbar" } else { "Show Deskbar" }, Action::Edit(vec![Edit::new("deskbar", "enabled", Val::Bool(!self.deskbar_enabled))])));
        let mut m = Menu::new(e);
        let s = self.scale as f32;
        let font = self.font.size;
        m.layout(&mut self.text, font, self.scale, (at.0 as f32, at.1 as f32), (out.0 as f32 / s, out.1 as f32 / s));
        self.menu_seq += 1;
        tracing::info!("menu: open at {:.0},{:.0} ({} entries); ease-out cubic, {} ms", at.0, at.1, m.entries.len(), super::motion::MENU_MS);
        self.menu = Some(m);
        self.menu_tex = None;
    }

    /// A dock click: the verbs of the app in item `i`, as a menu beside the dock
    /// item (above a bottom dock, and so on), with each verb's summary and its
    /// effect class. An app is only ever asked for through one of its verbs, so
    /// this is the whole of "launching" it. `out` is the output in device pixels.
    /// A dock click. A windowed app (decision 0012) opens its own window and
    /// has no verb to choose, so it is launched directly and no menu appears.
    /// Everything else gets the verb menu.
    pub fn dock_click(&mut self, i: usize, out: Out) {
        let Some(item) = self.items.get(i).filter(|it| it.clickable) else { return };
        let id = item.id.clone();
        if let Some(a) = self.registry.apps.iter().find(|a| a.id == id).filter(|a| a.windowed && a.launchable) {
            let exec = a.exec.clone();
            self.run_windowed(&id, &exec);
            return;
        }
        self.open_verb_menu(i, out);
    }

    pub fn open_verb_menu(&mut self, i: usize, out: Out) {
        let Some(item) = self.items.get(i).filter(|it| it.clickable) else { return };
        let (id, label) = (item.id.clone(), item.label.clone());
        let l = self.layout(out.0, out.1);
        let Some(r) = l.items.get(i).copied() else { return };
        let s = self.scale as f32;
        let (px, py) = (l.panel.x as f32 / s, l.panel.y as f32 / s);
        let (pr, pb) = ((l.panel.x + l.panel.w) as f32 / s, (l.panel.y + l.panel.h) as f32 / s);
        let at = match self.dock.edge {
            DockEdge::Bottom => (r.x as f32 / s, py),
            DockEdge::Top => (r.x as f32 / s, pb),
            DockEdge::Left => (pr, r.y as f32 / s),
            DockEdge::Right => (px, r.y as f32 / s),
        };
        let entries = self.verb_entries(&id, &label);
        let mut m = Menu::new(entries);
        let font = self.font.size;
        m.layout(&mut self.text, font, self.scale, at, (out.0 as f32 / s, out.1 as f32 / s));
        self.menu_seq += 1;
        tracing::info!("menu: verbs of {id} ({} entries); ease-out cubic, {} ms", m.entries.len(), super::motion::MENU_MS);
        self.menu = Some(m);
        self.menu_tex = None;
    }

    /// The rows of an app's verb menu. A header with the app's name, then one
    /// row per verb: name, summary, effect tag. When there is nothing to
    /// choose, one disabled row says why, from the registry's `Status`.
    fn verb_entries(&self, app: &str, name: &str) -> Vec<Entry> {
        let reg = &self.registry;
        let mut e = vec![Entry::Header(name.to_string())];
        // Installed but not launchable: say why instead of listing verbs that
        // cannot run. The registry decided this (apps.App.Launchable).
        if let Some(a) = reg.apps.iter().find(|a| a.id == app).filter(|a| !a.launchable) {
            e.push(Entry::item("Cannot launch", Action::CloseSettings).disabled(&a.problem));
            return e;
        }
        let names: &[String] = reg.apps.iter().find(|a| a.id == app).map_or(&[], |a| &a.verbs);
        if names.is_empty() {
            let why = match &reg.status {
                Status::NoBus => "no registry (no --bus-socket)".to_string(),
                Status::Pending => "asking the registry...".to_string(),
                Status::Failed(_) => "registry unreachable".to_string(),
                Status::Ok => "this app declares no verbs".to_string(),
            };
            e.push(Entry::item("No verbs", Action::CloseSettings).disabled(&why));
            return e;
        }
        for v in names {
            let detail = reg.verbs.as_ref().and_then(|all| all.iter().find(|d| d.app == app && d.name == *v));
            let mut row = Entry::item(&display_name(v), Action::RunVerb { app: app.into(), verb: v.clone() });
            row = match (reg.verbs.as_ref(), detail) {
                (_, Some(d)) => {
                    let (text, tone) = effect_tag(&d.effect);
                    let row = row.tag(&text, tone);
                    if d.summary.is_empty() { row } else { row.note(&display_text(&d.summary, 60)) }
                }
                // The registry answered the app list but not the verb details: say that, do not guess.
                _ => row.tag("effect unknown", Tone::Caution),
            };
            e.push(row);
        }
        if let Status::Failed(_) = &reg.status {
            e.push(Entry::Separator);
            e.push(Entry::item("Registry unreachable", Action::CloseSettings).disabled("showing the last answer"));
        }
        e
    }

    pub fn open_settings(&mut self) {
        self.menu = None;
        self.families = self.text.ui_families();
        tracing::info!("settings: open; ease-out cubic, {PANEL_MS} ms");
        self.settings = Some(SettingsState {
            page: 0,
            sel: None,
            hover: None,
            status: "Changes are saved to the theme file and apply at once.".into(),
            focused: true,
            appear: Appear::new(PANEL_MS),
            pos: None,
            grab: None,
            rect: None,
            placed: vec![],
            logged: false,
            pages: vec![],
            built: Default::default(),
            min_h: 0.0,
            tkey: String::new(),
            key: String::new(),
        });
        self.settings_tex = None;
    }

    pub fn set_status(&mut self, s: String) {
        if let Some(st) = &mut self.settings {
            st.status = s;
        }
    }

    // ---- pointer ---------------------------------------------------------------

    fn local(r: Rect, dp: (f64, f64), s: f64) -> (f32, f32) {
        (((dp.0 - r.x as f64) / s) as f32, ((dp.1 - r.y as f64) / s) as f32)
    }

    pub fn chrome_contains(&self, dp: (f64, f64), out: Out) -> bool {
        self.menu_rect().is_some_and(|r| r.contains(dp.0, dp.1)) || self.settings_rect(out).is_some_and(|r| r.contains(dp.0, dp.1))
    }

    /// A panel move is in progress, so the pointer belongs to chrome wherever
    /// it is: dragging a panel must not also drag through to a client.
    pub fn chrome_grabbing(&self) -> bool {
        self.settings.as_ref().is_some_and(|st| st.grab.is_some())
    }

    pub fn menu_contains(&self, dp: (f64, f64)) -> bool {
        self.menu_rect().is_some_and(|r| r.contains(dp.0, dp.1))
    }

    /// The pointer moved to `dp` (device pixels): update hover highlights.
    pub fn chrome_motion(&mut self, dp: (f64, f64), out: Out) {
        let s = self.scale;
        if let (Some(r), Some(m)) = (self.menu_rect(), self.menu.as_mut()) {
            m.hover = if r.contains(dp.0, dp.1) {
                let (x, y) = Self::local(r, dp, s);
                m.hit(x, y)
            } else {
                None
            };
        }
        if let Some(r) = self.settings_rect(out) {
            // A move in progress owns the pointer: no hover, no hit-testing.
            if let Some(st) = &mut self.settings {
                if let Some((gx, gy)) = st.grab {
                    st.pos = Some(((dp.0 - gx) / s, (dp.1 - gy) / s));
                    st.hover = None;
                    return;
                }
            }
            if let Some(st) = &mut self.settings {
                st.hover = if r.contains(dp.0, dp.1) {
                    let (x, y) = Self::local(r, dp, s);
                    st.built.hots.iter().position(|h| h.r.contains(x, y))
                } else {
                    None
                };
            }
        }
    }

    /// A left-button press at `dp`.
    pub fn chrome_press(&mut self, dp: (f64, f64), out: Out) -> Press {
        let s = self.scale;
        if let Some(r) = self.menu_rect() {
            let m = self.menu.as_ref().unwrap();
            if r.contains(dp.0, dp.1) {
                let (x, y) = Self::local(r, dp, s);
                return match m.hit(x, y).and_then(|i| m.action_at(i)) {
                    Some(a) => {
                        self.menu = None;
                        Press::Act(a)
                    }
                    None => Press::Swallow,
                };
            }
            // A click anywhere else dismisses the menu and goes no further.
            self.menu = None;
            return Press::Swallow;
        }
        if let Some(r) = self.settings_rect(out) {
            let st = self.settings.as_mut().unwrap();
            if r.contains(dp.0, dp.1) {
                st.focused = true;
                let (x, y) = Self::local(r, dp, s);
                let kind = st.built.hots.iter().find(|h| h.r.contains(x, y)).map(|h| (h.kind.clone(), h.row));
                return match kind {
                    Some((HotKind::Do(e), row)) => {
                        st.sel = row;
                        Press::Act(Action::Edit(e))
                    }
                    Some((HotKind::Tab(i), _)) => {
                        st.page = i;
                        st.sel = None;
                        Press::Swallow
                    }
                    Some((HotKind::Close, _)) => Press::Act(Action::CloseSettings),
                    None => {
                        // The title band is the handle. Grab it by the offset
                        // from the panel's top-left, so the point under the
                        // pointer stays under the pointer for the whole move.
                        if y <= st.built.drag_h {
                            st.grab = Some((dp.0 - r.x as f64, dp.1 - r.y as f64));
                            tracing::debug!("settings: move started, grabbed at {:.0},{:.0} inside the panel", dp.0 - r.x as f64, dp.1 - r.y as f64);
                        }
                        Press::Swallow
                    }
                };
            }
            st.focused = false;
        }
        Press::Pass
    }

    /// The button that chrome took came up. Ends a panel move, if one was
    /// running. Called for every release chrome swallowed, so it must be
    /// cheap and must not care whether a move was in progress.
    pub fn chrome_release(&mut self) {
        if let Some(st) = &mut self.settings {
            if st.grab.take().is_some() {
                tracing::info!("settings: moved to {:?} (logical top-left; None would be centred)", st.pos);
            }
        }
    }

    // ---- keyboard --------------------------------------------------------------

    pub fn captures_keys(&self) -> bool {
        self.menu.is_some() || self.settings.as_ref().is_some_and(|s| s.focused)
    }

    /// One key press (a keysym) while chrome has the keyboard.
    pub fn chrome_key(&mut self, sym: u32) -> Option<Action> {
        use smithay::input::keyboard::keysyms as k;
        if let Some(m) = &mut self.menu {
            return match m.key(sym) {
                MenuKey::Handled => None,
                MenuKey::Close => {
                    self.menu = None;
                    None
                }
                MenuKey::Act(a) => {
                    self.menu = None;
                    Some(a)
                }
            };
        }
        let st = self.settings.as_mut()?;
        let n = st.pages.len().max(1);
        let rows = st.pages.get(st.page).map_or(0, |p| p.rows.len());
        match sym {
            k::KEY_Escape => return Some(Action::CloseSettings),
            k::KEY_Tab => {
                st.page = (st.page + 1) % n;
                st.sel = None;
            }
            k::KEY_ISO_Left_Tab => {
                st.page = (st.page + n - 1) % n;
                st.sel = None;
            }
            k::KEY_Down if rows > 0 => st.sel = Some(st.sel.map_or(0, |i| (i + 1) % rows)),
            k::KEY_Up if rows > 0 => st.sel = Some(st.sel.map_or(rows - 1, |i| (i + rows - 1) % rows)),
            k::KEY_Left | k::KEY_Right => {
                let row = st.sel.and_then(|i| st.pages.get(st.page)?.rows.get(i));
                let e = row.and_then(|r| if sym == k::KEY_Left { r.left.clone() } else { r.right.clone() });
                return e.map(Action::Edit);
            }
            _ => {}
        }
        None
    }

    // ---- GPU -------------------------------------------------------------------

    pub(super) fn prepare_chrome(&mut self, r: &mut GlesRenderer, size: Size<i32, Physical>) -> Res<()> {
        self.prepare_menu(r)?;
        self.prepare_settings(r, size)
    }

    fn import(r: &mut GlesRenderer, c: &Canvas) -> Res<GlesTexture> {
        Ok(r.import_memory(&c.rgba, Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((c.w as i32, c.h as i32)), false)?)
    }

    fn prepare_menu(&mut self, r: &mut GlesRenderer) -> Res<()> {
        let Some(mut m) = self.menu.take() else {
            self.menu_tex = None;
            return Ok(());
        };
        if !m.logged {
            m.logged = true;
            tracing::info!("menu: {}", m.appear.describe());
        }
        let key = format!("{}|{:?}|{}|{}|{}|{}", self.menu_seq, m.hover, self.scale, self.rev, self.glass_on, self.font.size);
        if self.menu_tex.as_ref().is_none_or(|(k, _)| *k != key) {
            let st = self.style();
            let (c, placed) = m.draw(&mut self.text, &st);
            let s = self.scale;
            let (px, py) = (d(m.origin.0, s), d(m.origin.1, s));
            tracing::info!(
                "menu: layout scale={} panel={},{},{},{} rows=[{}] hover={:?} text={}",
                s, px, py, c.w, c.h,
                m.rows.iter().map(|r| format!("{},{},{},{}", px + d(r.x, s), py + d(r.y, s), d(r.w, s), d(r.h, s))).collect::<Vec<_>>().join(";"),
                m.hover,
                placed.iter().map(|(t, x, y, w)| format!("{}@{},{},{}", t.replace(' ', "_"), px + x, py + y, w)).collect::<Vec<_>>().join("\u{1f}")
            );
            let tex = Self::import(r, &c)?;
            self.menu_tex = Some((key, tex));
        }
        self.menu = Some(m);
        Ok(())
    }

    fn prepare_settings(&mut self, r: &mut GlesRenderer, size: Size<i32, Physical>) -> Res<()> {
        let Some(mut st) = self.settings.take() else {
            self.settings_tex = None;
            return Ok(());
        };
        if !st.logged {
            st.logged = true;
            tracing::info!("settings: {}", st.appear.describe());
        }
        let (scale, font) = (self.scale, self.font.size);
        let t = &self.snap;
        let tkey = format!("{:?}|{:?}|{:?}|{:?}|{:?}|{scale}|{}|{}x{}", t.dock, t.background, t.font, t.input, t.output, self.host_scale, size.w, size.h);
        if st.tkey != tkey {
            let ifaces = crate::deskbar::sysinfo::interfaces(&Default::default());
            let ctx = Ctx { theme: &self.snap, host_scale: self.host_scale, scale, out_phys: (size.w, size.h), refresh_mhz: self.refresh_mhz, outputs: self.outputs, families: &self.families, nested: self.nested, ifaces: &ifaces };
            st.pages = settings::pages(&ctx);
            st.min_h = (0..st.pages.len()).map(|p| settings::build(&st.pages, p, "", font, scale, 0.0, &mut self.text).size.1).fold(0.0, f32::max);
            st.tkey = tkey.clone();
        }
        let key = format!("{tkey}|{}|{:?}|{:?}|{}", st.page, st.sel, st.hover, st.status);
        // A move changes where the panel is but not what it contains, so it
        // must re-log (tests read the hotspot coordinates off that line) without
        // re-rasterising and re-importing the texture on every pointer motion.
        let moved = {
            let (w, h) = ((st.built.size.0.max(settings::WIDTH) as f64 * scale).ceil() as i32, (st.built.size.1 as f64 * scale).ceil() as i32);
            let now = place_settings(st.pos, (w, h), st.built.drag_h, (size.w, size.h), scale);
            st.rect.is_some() && st.rect != Some(now)
        };
        if st.key != key || self.settings_tex.is_none() {
            st.built = settings::build(&st.pages, st.page, &st.status, font, scale, st.min_h, &mut self.text);
            let style = self.style();
            let accent = oklab_to_u8(self.bg.accent);
            let (c, placed) = settings::draw(&st.built, st.hover, st.sel, &style, accent, &mut self.text);
            let rect = place_settings(st.pos, (c.w as i32, c.h as i32), st.built.drag_h, (size.w, size.h), scale);
            let sd = |v: f32| d(v, scale);
            tracing::info!(
                "settings: layout scale={scale} panel={},{},{},{} page={} hots=[{}] text={}",
                rect.x, rect.y, rect.w, rect.h, st.page,
                st.built.hots.iter().map(|h| format!("{},{},{},{}", rect.x + sd(h.r.x), rect.y + sd(h.r.y), sd(h.r.w), sd(h.r.h))).collect::<Vec<_>>().join(";"),
                placed.iter().map(|(t, x, y, w)| format!("{}@{},{},{}", t.replace(' ', "_"), rect.x + x, rect.y + y, w)).collect::<Vec<_>>().join("\u{1f}")
            );
            self.settings_tex = Some((key.clone(), Self::import(r, &c)?));
            st.key = key;
            st.rect = Some(rect);
            st.placed = placed.iter().map(|(t, x, y, w)| (t.clone(), *x, *y, *w)).collect();
        } else if moved {
            let (w, h) = ((st.built.size.0.max(settings::WIDTH) as f64 * scale).ceil() as i32, (st.built.size.1 as f64 * scale).ceil() as i32);
            let rect = place_settings(st.pos, (w, h), st.built.drag_h, (size.w, size.h), scale);
            st.rect = Some(rect);
            let sd = |v: f32| d(v, scale);
            tracing::info!(
                "settings: layout scale={scale} panel={},{},{},{} page={} hots=[{}] text={}",
                rect.x, rect.y, rect.w, rect.h, st.page,
                st.built.hots.iter().map(|h| format!("{},{},{},{}", rect.x + sd(h.r.x), rect.y + sd(h.r.y), sd(h.r.w), sd(h.r.h))).collect::<Vec<_>>().join(";"),
                st.placed.iter().map(|(t, x, y, w)| format!("{}@{},{},{}", t.replace(' ', "_"), rect.x + x, rect.y + y, w)).collect::<Vec<_>>().join("\u{1f}")
            );
        }
        self.settings = Some(st);
        Ok(())
    }

    /// The panels to draw over the dock, bottom to top: settings, then the menu.
    pub fn chrome_draws(&self, size: Size<i32, Physical>) -> Vec<ChromeDraw<'_>> {
        let mut v = vec![];
        let grow = |r: Rect, anchor: (f64, f64), (k, alpha): (f32, f32), tex| {
            let k = k as f64;
            let dst = Rect {
                x: (anchor.0 + (r.x as f64 - anchor.0) * k).round() as i32,
                y: (anchor.1 + (r.y as f64 - anchor.1) * k).round() as i32,
                w: (r.w as f64 * k).round() as i32,
                h: (r.h as f64 * k).round() as i32,
            };
            ChromeDraw { tex, dst, alpha }
        };
        // The Deskbar is under the menu and the settings panel (a popup is above the box it opened from), over windows.
        if let (Some((_, tex)), Some(r)) = (&self.deskbar.tex, self.deskbar.rect) {
            v.push(ChromeDraw { tex, dst: r, alpha: 1.0 });
        }
        if let (Some((_, tex)), Some(r), Some(st)) = (&self.settings_tex, self.settings_rect((size.w, size.h)), &self.settings) {
            let centre = (r.x as f64 + r.w as f64 / 2.0, r.y as f64 + r.h as f64 / 2.0);
            v.push(grow(r, centre, st.appear.frame(), tex));
        }
        if let (Some((_, tex)), Some(r), Some(m)) = (&self.menu_tex, self.menu_rect(), &self.menu) {
            let anchor = (m.anchor.0 as f64 * self.scale, m.anchor.1 as f64 * self.scale);
            v.push(grow(r, anchor, m.appear.frame(), tex));
        }
        v
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::shell::apps::{AppEntry, VerbEntry};

    fn shell(status: Status, verbs: Vec<String>, details: Option<Vec<VerbEntry>>) -> Shell {
        let mut s = Shell::new();
        s.registry.apps = vec![AppEntry { id: "a.b".into(), name: "Ab".into(), verbs, launchable: true, problem: String::new(), windowed: false, exec: String::new() }];
        s.registry.verbs = details;
        s.registry.status = status;
        s
    }

    fn rows(e: &[Entry]) -> Vec<(String, bool, Option<String>, Option<(String, Tone)>)> {
        e.iter().filter_map(|e| match e {
            Entry::Item { label, enabled, note, tag, .. } => Some((label.clone(), *enabled, note.clone(), tag.clone())),
            _ => None,
        }).collect()
    }

    #[test]
    fn an_app_without_verbs_gets_one_disabled_row_with_the_reason_from_the_registry_status() {
        for (st, why) in [(Status::Ok, "this app declares no verbs"), (Status::Pending, "asking the registry..."), (Status::NoBus, "no registry (no --bus-socket)"), (Status::Failed("x".into()), "registry unreachable")] {
            let r = rows(&shell(st, vec![], None).verb_entries("a.b", "Ab"));
            assert_eq!(r, vec![("No verbs".into(), false, Some(why.into()), None)]);
        }
    }

    #[test]
    fn each_effect_class_is_tagged_and_unknown_or_missing_ones_are_never_plain() {
        assert_eq!(effect_tag("read"), ("read".into(), Tone::Plain));
        assert_eq!(effect_tag("write"), ("write".into(), Tone::Caution));
        assert_eq!(effect_tag("destructive"), ("!! destructive".into(), Tone::Danger));
        assert_eq!(effect_tag("external"), ("!! external".into(), Tone::Danger));
        assert_eq!(effect_tag("").1, Tone::Caution);
        assert_eq!(effect_tag("nuke\u{1b}").1, Tone::Danger);
        let d = |n: &str, e: &str| VerbEntry { app: "a.b".into(), name: n.into(), effect: e.into(), summary: "S".into() };
        let r = rows(&shell(Status::Ok, vec!["x@v1".into(), "y@v1".into()], Some(vec![d("x@v1", "read")])).verb_entries("a.b", "Ab"));
        assert_eq!(r[0].3, Some(("read".into(), Tone::Plain)));
        assert_eq!(r[0].2.as_deref(), Some("S"));
        assert_eq!(r[1].3, Some(("effect unknown".into(), Tone::Caution)), "a verb the registry did not describe is not safe");
        let r = rows(&shell(Status::Ok, vec!["x@v1".into()], None).verb_entries("a.b", "Ab"));
        assert_eq!(r[0].3.as_ref().map(|t| t.0.as_str()), Some("effect unknown"));
    }
}
