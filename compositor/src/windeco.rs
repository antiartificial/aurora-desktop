//! Window management by the compositor's own chrome: which windows are decorated, what a press on a
//! title bar or a window's edge does, maximise and restore, and where a new window goes.
//!
//! The arithmetic (where the bar is, which edge a point grabs) is `shell::deco`, in logical units; the
//! drawing is `shell::deco` too. This file is the glue to the `Space`, the seat and the two grabs that
//! already existed for clients that decorate themselves (`grabs/`): a title bar drives the SAME
//! `MoveSurfaceGrab` an `xdg_toplevel.move` does, and an edge drives the same `ResizeSurfaceGrab`.

use std::time::Instant;

use smithay::{
    desktop::Window,
    input::pointer::{Focus, GrabStartData as PointerGrabStartData},
    reexports::{
        wayland_protocols::xdg::{decoration::zv1::server::zxdg_toplevel_decoration_v1::Mode, shell::server::xdg_toplevel},
        wayland_server::{Resource, protocol::wl_surface::WlSurface},
    },
    utils::{Logical, Point, Rectangle, Serial},
    wayland::{
        compositor::{get_parent, with_states},
        seat::WaylandFocus,
        shell::xdg::{SurfaceCachedState, XdgToplevelSurfaceData},
    },
};

use crate::{
    Aurora,
    grabs::{MoveSurfaceGrab, ResizeSurfaceGrab, resize_grab::ResizeEdge},
    shell::deco::{self, DecoSpec, Hit},
};

type R = Rectangle<i32, Logical>;

const BTN_LEFT: u32 = 0x110;
/// Where the first cascaded window goes, from the usable area's corner (below the bar), and the step.
const CASCADE_START: i32 = 32;
const CASCADE_STEP: i32 = 32;
/// Two presses further apart than this are not a double-click, logical pixels.
const DOUBLE_CLICK_SLOP: f64 = 8.0;

/// A window's key in the compositor's own tables: its root surface's id. An X11 window has none until
/// Xwayland has associated its surface, and is not decorated until then.
pub fn win_key(w: &Window) -> Option<String> {
    w.wl_surface().map(|s| s.id().to_string())
}

fn root_of(s: &WlSurface) -> WlSurface {
    let mut root = s.clone();
    while let Some(p) = get_parent(&root) {
        root = p;
    }
    root
}

/// What the compositor remembers between presses on a bar.
#[derive(Default)]
pub struct DecoInput {
    /// The close button is held: window key. The close happens on release, if the pointer is still on it.
    pub pressed_close: Option<(Window, String)>,
    /// The last press on a bar: when, which window, where. A second one soon enough is a double-click.
    pub last_bar: Option<(Instant, String, Point<f64, Logical>)>,
    /// What the pointer is over now, for the close button's hover.
    pub hover: Option<(String, Hit)>,
    /// Where a maximised window was, by key, so restore puts it back.
    pub restore: std::collections::HashMap<String, R>,
    /// Windows that have not had their first placement yet.
    pub unplaced: std::collections::HashSet<String>,
    /// How many windows the cascade has placed since it last wrapped.
    pub cascade: i32,
}

impl Aurora {
    /// Whether the compositor decorates `w`. Wayland: the client did not ask to decorate itself. X11: a
    /// managed window (never an override-redirect menu or tooltip) that did not ask for none. Never a
    /// fullscreen window.
    pub fn decorated(&self, w: &Window) -> bool {
        if !self.shell.deco_on() {
            return false;
        }
        if let Some(t) = w.toplevel() {
            return t.with_pending_state(|s| s.decoration_mode != Some(Mode::ClientSide) && !s.states.contains(xdg_toplevel::State::Fullscreen));
        }
        w.x11_surface().is_some_and(|x| !x.is_override_redirect() && !x.is_decorated() && !x.is_fullscreen())
    }

    pub fn window_title(&self, w: &Window) -> String {
        if let Some(x) = w.x11_surface() {
            return x.title();
        }
        let Some(t) = w.toplevel() else { return String::new() };
        with_states(t.wl_surface(), |s| {
            let d = s.data_map.get::<XdgToplevelSurfaceData>()?.lock().ok()?;
            Some(d.title.clone().filter(|t| !t.is_empty()).or_else(|| d.app_id.clone()).unwrap_or_default())
        })
        .unwrap_or_default()
    }

    /// The domain the kernel placed this window's client in, learned at connect (`crate::peer`).
    /// `None` for a host process, XWayland and a test client: a window with no domain is normal.
    /// Unlike `window_title`, nothing the client sends can change this.
    pub fn window_domain(&self, w: &Window) -> Option<String> {
        let surface = w.wl_surface()?;
        let client = surface.client()?;
        client.get_data::<crate::state::ClientState>()?.domain.as_ref().map(|d| d.name.clone())
    }

    pub fn is_maximized(&self, w: &Window) -> bool {
        if let Some(t) = w.toplevel() {
            return t.with_pending_state(|s| s.states.contains(xdg_toplevel::State::Maximized));
        }
        w.x11_surface().is_some_and(|x| x.is_maximized())
    }

    fn size_hints(&self, w: &Window) -> (smithay::utils::Size<i32, Logical>, smithay::utils::Size<i32, Logical>) {
        if let Some(t) = w.toplevel() {
            return with_states(t.wl_surface(), |s| {
                let mut g = s.cached_state.get::<SurfaceCachedState>();
                let c = g.current();
                (c.min_size, c.max_size)
            });
        }
        match w.x11_surface() {
            Some(x) => (x.min_size().unwrap_or_default(), x.max_size().unwrap_or_default()),
            None => Default::default(),
        }
    }

    /// Whether the edges of `w` may be grabbed: not maximised, not fullscreen, and not a window whose
    /// client pinned its size (min == max, which is what `B_NOT_RESIZABLE` becomes).
    pub fn resizable(&self, w: &Window) -> bool {
        let (min, max) = self.size_hints(w);
        let pinned = max.w > 0 && max.h > 0 && min.w == max.w && min.h == max.h;
        !pinned && !self.is_maximized(w)
    }

    pub(crate) fn is_focused(&self, w: &Window) -> bool {
        let Some(mine) = w.wl_surface() else { return false };
        self.seat.get_keyboard().and_then(|k| k.current_focus()).is_some_and(|f| root_of(&f) == *mine)
    }

    /// What to draw this frame, in `Space` order.
    pub fn deco_specs(&mut self) -> Vec<Option<DecoSpec>> {
        let wins: Vec<Window> = self.space.elements().cloned().collect();
        wins.iter()
            .map(|w| {
                if !self.decorated(w) {
                    return None;
                }
                let window = self.space.element_geometry(w)?;
                if window.size.w <= 0 || window.size.h <= 0 {
                    return None;
                }
                let id = win_key(w)?;
                let hover_close = self.deco.hover.as_ref() == Some(&(id.clone(), Hit::Close));
                Some(DecoSpec { id, window, title: self.window_title(w), domain: self.window_domain(w), focused: self.is_focused(w), hover_close })
            })
            .collect()
    }

    /// What a press at `pos` would mean to the topmost window it touches: its bar, its close button or
    /// one of its edges. `None` when it lands on a client's own pixels or on nothing.
    pub fn deco_hit(&mut self, pos: Point<f64, Logical>) -> Option<(Window, Hit)> {
        if !self.shell.deco_on() {
            return None;
        }
        let wins: Vec<Window> = self.space.elements().rev().cloned().collect();
        let (grip, corner) = (self.shell.deco.grip as f64, self.shell.deco.corner as f64);
        for w in wins {
            let Some(geo) = self.space.element_geometry(&w) else { continue };
            if geo.size.w <= 0 || geo.size.h <= 0 {
                continue;
            }
            if self.decorated(&w) {
                let title = self.window_title(&w);
                let domain = self.window_domain(&w);
                let f = self.shell.deco_frame(geo, &title, domain.as_deref());
                if let Some(h) = deco::hit(&f, grip, corner, (pos.x, pos.y), self.resizable(&w)) {
                    return Some((w, h));
                }
            }
            if geo.contains(pos.to_i32_floor()) {
                return None;
            }
        }
        None
    }

    /// The pointer went down on a bar, a close button or an edge. Returns true when the press is
    /// the compositor's (the client sees nothing of it).
    pub fn deco_press(&mut self, w: Window, hit: Hit, loc: Point<f64, Logical>, serial: Serial, press: &smithay::input::pointer::ButtonEvent) -> bool {
        let Some(key) = win_key(&w) else { return false };
        self.space.raise_element(&w, true);
        self.focus_and_activate(&w);
        let pointer = self.seat.get_pointer().unwrap();
        let start = PointerGrabStartData { focus: None, button: BTN_LEFT, location: loc };
        match hit {
            Hit::Close => {
                self.deco.pressed_close = Some((w, key));
                self.chrome_buttons.insert(BTN_LEFT);
            }
            Hit::Bar => {
                let ms = self.shell.deco.double_click_ms as u128;
                let double = self.deco.last_bar.take().is_some_and(|(t, k, at)| {
                    k == key && t.elapsed().as_millis() <= ms && (at.x - loc.x).hypot(at.y - loc.y) <= DOUBLE_CLICK_SLOP
                });
                if double {
                    self.toggle_maximize(&w);
                    self.chrome_buttons.insert(BTN_LEFT);
                    return true;
                }
                self.deco.last_bar = Some((Instant::now(), key, loc));
                if self.is_maximized(&w) {
                    // A maximised window does not move; the press still belongs to the bar.
                    self.chrome_buttons.insert(BTN_LEFT);
                    return true;
                }
                let Some(initial_window_location) = self.space.element_location(&w) else { return true };
                pointer.set_grab(self, MoveSurfaceGrab { start_data: start, window: w, initial_window_location }, serial, Focus::Clear);
                // The grab sees the press, as it would after an xdg_toplevel.move, so its release ends it.
                pointer.button(self, press);
                pointer.frame(self);
            }
            Hit::Edge(e) => {
                self.start_resize(w, e, start, serial);
                pointer.button(self, press);
                pointer.frame(self);
            }
        }
        true
    }

    /// The left button came up after a press on the close button: close if the pointer is still on it.
    pub fn deco_release(&mut self, loc: Point<f64, Logical>) {
        let Some((w, key)) = self.deco.pressed_close.take() else { return };
        if let Some((h, k)) = self.deco_hit(loc).and_then(|(w, h)| win_key(&w).map(|k| (h, k))) {
            if h == Hit::Close && k == key {
                tracing::info!("deco: close pressed on {:?}", self.window_title(&w));
                self.close_window(&w);
            }
        }
    }

    /// Ask the client to close: `xdg_toplevel.close`, or `WM_DELETE_WINDOW` for X11. It is a request, not a
    /// kill: the client may refuse (a Haiku window turns it into `B_QUIT_REQUESTED`).
    pub fn close_window(&mut self, w: &Window) {
        // From here until the client actually goes, keep a picture of it, so there is something
        // to fade out when it does. This is the only moment anyone knows a window is about to
        // leave: afterwards its buffers are gone and there is nothing left to photograph.
        if let Some(k) = win_key(w) {
            self.fx.note_closing(&k);
        }
        if let Some(t) = w.toplevel() {
            t.send_close();
        } else if let Some(x) = w.x11_surface() {
            let _ = x.close();
        }
    }

    /// Start a resize grab on `w` from `edges`. Shared by `xdg_toplevel.resize` and the window's edges.
    pub fn start_resize(&mut self, w: Window, edges: ResizeEdge, start: PointerGrabStartData<Aurora>, serial: Serial) {
        let Some(loc) = self.space.element_location(&w) else { return };
        let size = w.geometry().size;
        if let Some(t) = w.toplevel() {
            t.with_pending_state(|s| {
                s.states.set(xdg_toplevel::State::Resizing);
            });
            t.send_pending_configure();
        }
        let grab = ResizeSurfaceGrab::start(start, w, edges, Rectangle::new(loc, size));
        self.seat.get_pointer().unwrap().set_grab(self, grab, serial, Focus::Clear);
    }

    // ---- maximise ------------------------------------------------------------------------

    /// The client rectangle of a maximised window: the usable area, less the bar that hangs above it.
    pub fn maximized_rect(&self, w: &Window) -> R {
        let area = self.usable_area();
        let bar = if self.decorated(w) { self.shell.deco_height() } else { 0 };
        Rectangle::new((area.loc.x, area.loc.y + bar).into(), (area.size.w, (area.size.h - bar).max(1)).into())
    }

    /// Fullscreen: the whole output, over the dock, no bar. Wayland toplevels only. What it can NOT do is
    /// cover the capability indicator, which is drawn after every window (`indicator::draw`).
    pub fn set_fullscreen(&mut self, w: &Window, on: bool) {
        let Some(t) = w.toplevel() else { return };
        let Some(geo) = self.space.outputs().next().and_then(|o| self.space.output_geometry(o)) else { return };
        let key = win_key(w);
        let was = self.space.element_location(w).map(|l| Rectangle::new(l, w.geometry().size)).filter(|r| r.size.w > 0 && r.size.h > 0);
        if on {
            if let (Some(k), Some(r)) = (&key, was) {
                if !t.with_pending_state(|s| s.states.contains(xdg_toplevel::State::Fullscreen)) {
                    self.deco.restore.insert(k.clone(), r);
                }
            }
        }
        let back = if on { None } else { key.as_ref().and_then(|k| self.deco.restore.remove(k)) };
        t.with_pending_state(|s| {
            if on {
                s.states.set(xdg_toplevel::State::Fullscreen);
                s.size = Some(geo.size);
            } else {
                s.states.unset(xdg_toplevel::State::Fullscreen);
                s.size = back.map(|r| r.size);
            }
        });
        if t.is_initial_configure_sent() {
            t.send_pending_configure();
        }
        if let Some(r) = if on { Some(geo) } else { back } {
            self.space.map_element(w.clone(), r.loc, true);
        }
        tracing::info!("deco: {} {:?}", if on { "fullscreen" } else { "left fullscreen" }, self.window_title(w));
    }

    pub fn toggle_maximize(&mut self, w: &Window) {
        let on = !self.is_maximized(w);
        self.set_maximized(w, on);
    }

    pub fn set_maximized(&mut self, w: &Window, on: bool) {
        let key = win_key(w);
        let was = self.space.element_location(w).map(|l| Rectangle::new(l, w.geometry().size)).filter(|r| r.size.w > 0 && r.size.h > 0);
        let area = self.maximized_rect(w);
        if on {
            if let (Some(k), Some(r)) = (&key, was) {
                if !self.is_maximized(w) {
                    self.deco.restore.insert(k.clone(), r);
                }
            }
        }
        let back = if on { None } else { key.as_ref().and_then(|k| self.deco.restore.remove(k)) };
        if let Some(t) = w.toplevel() {
            t.with_pending_state(|s| {
                if on {
                    s.states.set(xdg_toplevel::State::Maximized);
                    s.size = Some(area.size);
                } else {
                    s.states.unset(xdg_toplevel::State::Maximized);
                    s.size = back.map(|r| r.size);
                }
            });
            if t.is_initial_configure_sent() {
                t.send_pending_configure();
            }
        } else if let Some(x) = w.x11_surface() {
            let _ = x.set_maximized(on);
            if let Some(r) = if on { Some(area) } else { back } {
                let _ = x.configure(r);
            }
        }
        if let Some(r) = if on { Some(area) } else { back } {
            self.space.map_element(w.clone(), r.loc, false);
        }
        tracing::info!("deco: {} {:?} -> {:?}", if on { "maximised" } else { "restored" }, self.window_title(w), if on { Some(area) } else { back });
    }

    // ---- placement -----------------------------------------------------------------------

    /// The lowest a client's origin may be: its bar sits above it, and the bar stays in the usable area.
    pub fn min_origin_y(&self, w: &Window) -> i32 {
        let bar = if self.decorated(w) { self.shell.deco_height() } else { 0 };
        self.usable_area().loc.y + bar
    }

    /// `loc` kept where the window can be reached: the bar inside the usable area, and a grip of the
    /// window horizontally on screen. Used when a window is dropped, restored or placed.
    pub fn constrain(&self, w: &Window, loc: Point<i32, Logical>, size: smithay::utils::Size<i32, Logical>) -> Point<i32, Logical> {
        let area = self.usable_area();
        let grip = 80.min(size.w.max(1));
        let x = loc.x.clamp(area.loc.x + grip - size.w, (area.loc.x + area.size.w - grip).max(area.loc.x + grip - size.w));
        let y = loc.y.clamp(self.min_origin_y(w), (area.loc.y + area.size.h - 40).max(self.min_origin_y(w)));
        (x, y).into()
    }

    /// Where a new window of `size` goes. A dialog is centred on its `parent`; anything else cascades
    /// from the usable area's corner. Always inside the usable area (and below its top by the bar).
    /// Ask a toplevel to shrink to the usable area when it asked for more than exists.
    ///
    /// Returns true when a configure was sent. The window keeps whatever size it already has
    /// until it acks and commits, so the caller re-reads the geometry rather than assuming.
    pub fn fit_to_usable(&mut self, w: &Window) -> bool {
        let Some(xdg) = w.toplevel().cloned() else { return false };
        let area = self.usable_area();
        let bar = if self.decorated(w) { self.shell.deco_height() } else { 0 };
        let room = smithay::utils::Size::<i32, smithay::utils::Logical>::from((
            area.size.w,
            (area.size.h - bar).max(1),
        ));
        let have = w.geometry().size;
        if have.w <= room.w && have.h <= room.h {
            return false;
        }
        let surf = smithay::reexports::wayland_server::protocol::wl_surface::WlSurface::clone(xdg.wl_surface());
        let min = smithay::wayland::compositor::with_states(&surf, |states| {
            let mut guard = states.cached_state.get::<smithay::wayland::shell::xdg::SurfaceCachedState>();
            guard.current().min_size
        });
        let want = smithay::utils::Size::<i32, smithay::utils::Logical>::from((
            have.w.min(room.w).max(min.w.max(1)),
            have.h.min(room.h).max(min.h.max(1)),
        ));
        if want == have {
            return false;
        }
        tracing::info!(
            "place: {:?} asked for {}x{} with {}x{} usable; configured down to {}x{}",
            self.window_title(w), have.w, have.h, room.w, room.h, want.w, want.h
        );
        xdg.with_pending_state(|st| st.size = Some(want));
        xdg.send_pending_configure();
        true
    }

    pub fn placement(&mut self, w: &Window, size: smithay::utils::Size<i32, Logical>, parent: Option<&Window>) -> Point<i32, Logical> {
        let area = self.usable_area();
        let top = self.min_origin_y(w);
        let (mut x, mut y);
        let parent_geo = parent.and_then(|p| self.space.element_geometry(p)).filter(|g| g.size.w > 0);
        if let Some(pg) = parent_geo {
            x = pg.loc.x + (pg.size.w - size.w) / 2;
            y = pg.loc.y + (pg.size.h - size.h) / 2;
        } else {
            let mut n = self.deco.cascade;
            let at = |n: i32| (area.loc.x + CASCADE_START + n * CASCADE_STEP, top + CASCADE_START - 8 + n * CASCADE_STEP);
            let (cx, cy) = at(n);
            if cx + size.w > area.loc.x + area.size.w || cy + size.h > area.loc.y + area.size.h {
                n = 0;
            }
            self.deco.cascade = n + 1;
            (x, y) = at(n);
        }
        x = x.clamp(area.loc.x, (area.loc.x + area.size.w - size.w).max(area.loc.x));
        y = y.clamp(top, (area.loc.y + area.size.h - size.h).max(top));
        (x, y).into()
    }

    /// A toplevel just got its first buffer, so its size is known: put it where it belongs. Skipped for
    /// the pre-shell scene (everything at the origin, as its tests expect), for a window that already
    /// maximised itself, and for one `persist` restored (what the user left wins).
    pub fn place_on_first_commit(&mut self, root: &WlSurface) {
        let key = root.id().to_string();
        if !self.deco.unplaced.contains(&key) {
            return;
        }
        let Some(w) = self.window_for_surface(root) else { return };
        let size = w.geometry().size;
        if size.w <= 0 || size.h <= 0 {
            return;
        }
        self.deco.unplaced.remove(&key);
        // A window that does not FIT is configured down before it is placed.
        //
        // `placement` clamps the origin, and a clamp cannot help a window taller than the space
        // it is going in: the lower bound wins and the window hangs off the bottom, under the
        // dock, with its own status bar behind the panel. The client asked for a size without
        // being told what was available, which is the compositor's job to answer.
        //
        // Only shrink, never grow, and never below what the client said it needs: a window that
        // declares a minimum is refusing to be useful smaller, and overriding that trades one
        // broken frame for another.
        self.fit_to_usable(&w);
        let size = w.geometry().size;
        // A window ARRIVES rather than appearing. Started here and not at new_toplevel, because
        // until the first commit its size is zero and an animation from nothing to nothing is a
        // flash on the first frame.
        self.shell.appearing.insert(key.clone(), crate::shell::motion::Appear::new(crate::shell::motion::WINDOW_MS));
        if self.shell.legacy_scene || self.is_maximized(&w) {
            return;
        }
        if self.persist.as_ref().is_some_and(|p| p.was_restored(&root.id())) {
            // It was put back where it was left. It still takes its slot in the cascade, so where the NEXT window
            // goes does not depend on whether the placement memory got to this one before its first commit or after.
            self.deco.cascade += 1;
            return;
        }
        let parent = w.toplevel().and_then(|t| t.parent()).and_then(|p| self.window_for_surface(&p));
        let at = self.placement(&w, size, parent.as_ref());
        self.space.relocate_element(&w, at);
        tracing::info!("place: {:?} {}x{} at {},{} ({})", self.window_title(&w), size.w, size.h, at.x, at.y, if parent.is_some() { "centred on its parent" } else { "cascaded" });
    }
}
