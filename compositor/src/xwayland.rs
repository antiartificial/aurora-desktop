//! XWayland: X11 clients as ordinary windows in the same `Space` the Wayland
//! ones use, so the dock, the switcher, captures and the trusted overlay see
//! no difference.
//!
//! `--xwayland` starts Xwayland at launch. The X sockets are bound and
//! `DISPLAY` is exported before Xwayland has finished starting, so a client
//! started right away just waits for it. If Xwayland dies (a crash, or it is
//! killed), Aurora starts a new one on the same display number, so `DISPLAY`
//! stays valid; after 3 starts that never reach Ready it gives up. (Smithay
//! passes `-terminate`, but with the window manager attached Xwayland stays up
//! after its last client leaves.) Start is eager: there is no lazy mode.
//!
//! # Scale
//!
//! X11 clients get scale 1. Xwayland is told its surfaces carry a client scale
//! of 1 (`set_client_scale(1.0)`), so an X window of N x M X pixels is N x M
//! LOGICAL units, the same unit Wayland clients and the `Space` use, and the
//! existing logical -> physical step in `fx::window_elements` scales it to
//! N*s x M*s device pixels. At scale 2 an X11 window is the same size on screen
//! as at scale 1 but drawn from a 1x buffer, so it is soft, not small. The
//! alternative (client scale = output scale, X pixels = device pixels) is sharp
//! but makes every X app tiny and splits the `Space` into two coordinate
//! systems. X11 has no wp_fractional_scale, so `scale::announce` skips X11
//! surfaces. X11 geometry never crosses into `Physical`.
//!
//! # Windows
//!
//! A managed window is mapped at the top-left of the usable area (never under
//! the dock) and takes keyboard focus once its `wl_surface` is associated
//! (that happens after the map request, via xwayland-shell). An override-redirect
//! window (menu, tooltip, dropdown) is mapped exactly where it asked and is not
//! focused. Selections are not bridged (clipboard between X11 and Wayland is
//! not done).

use std::process::Stdio;

use smithay::{
    desktop::Window,
    input::pointer::Focus,
    reexports::{calloop::{LoopHandle, RegistrationToken}, wayland_server::protocol::wl_surface::WlSurface},
    utils::{Logical, Rectangle, SERIAL_COUNTER},
    wayland::{compositor::CompositorHandler, seat::WaylandFocus, xwayland_shell::{XWaylandShellHandler, XWaylandShellState}},
    xwayland::{X11Surface, X11Wm, XWayland, XWaylandEvent, XwmHandler, xwm::{Reorder, ResizeEdge, XwmId}},
};

use crate::Aurora;

/// How many times Xwayland is restarted after dying before it has been Ready.
const MAX_FAILED_STARTS: u32 = 3;

pub struct XState {
    pub display: u32,
    pub xwm: Option<X11Wm>,
    /// The event-loop source that owns the running Xwayland (and the lock on its display number).
    source: Option<RegistrationToken>,
    failed_starts: u32,
}

/// Send the pending configure of an xdg window; X11 windows have none.
pub fn send_pending(w: &Window) {
    if let Some(t) = w.toplevel() {
        t.send_pending_configure();
    }
}

impl Aurora {
    /// Start Xwayland and its window manager, and export `DISPLAY`.
    pub fn start_xwayland(&mut self, handle: LoopHandle<'static, Aurora>) -> std::io::Result<u32> {
        let display = self.spawn_xwayland(handle, None)?;
        // Children started by the shell (dock launches, `-c`) inherit this.
        unsafe { std::env::set_var("DISPLAY", format!(":{display}")) };
        Ok(display)
    }

    fn spawn_xwayland(&mut self, handle: LoopHandle<'static, Aurora>, display: Option<u32>) -> std::io::Result<u32> {
        let (xwayland, client) = XWayland::spawn(
            &self.display_handle,
            display,
            std::iter::empty::<(String, String)>(),
            std::iter::empty::<String>(),
            true,
            Stdio::null(),
            Stdio::null(),
            |_| (),
        )?;
        let number = xwayland.display_number();
        let h2 = handle.clone();
        let token = handle
            .insert_source(xwayland, move |event, _, state| match event {
                XWaylandEvent::Ready { x11_socket, display_number } => {
                    // Scale 1: see the module docs.
                    state.client_compositor_state(&client).set_client_scale(1.0);
                    match X11Wm::start_wm(h2.clone(), &state.display_handle, x11_socket, client.clone()) {
                        Ok(wm) => {
                            let x = state.x.get_or_insert(XState { display: display_number, xwm: None, source: None, failed_starts: 0 });
                            x.xwm = Some(wm);
                            x.failed_starts = 0;
                            tracing::info!("xwayland: ready on DISPLAY=:{display_number}");
                            println!("aurora-compositor: xwayland ready DISPLAY=:{display_number}");
                        }
                        Err(e) => tracing::error!("xwayland: window manager failed to attach: {e}"),
                    }
                }
                XWaylandEvent::Error => {
                    tracing::warn!("xwayland: died during startup");
                    state.restart_xwayland(h2.clone(), number);
                }
            })
            .map_err(|e| std::io::Error::other(e.to_string()))?;
        self.x.get_or_insert(XState { display: number, xwm: None, source: None, failed_starts: 0 }).source = Some(token);
        Ok(number)
    }

    /// Xwayland is gone: start another on the same display number.
    fn restart_xwayland(&mut self, handle: LoopHandle<'static, Aurora>, number: u32) {
        let Some(x) = self.x.as_mut() else { return };
        x.xwm = None;
        x.failed_starts += 1;
        if x.failed_starts > MAX_FAILED_STARTS {
            tracing::error!("xwayland: giving up after {MAX_FAILED_STARTS} failed starts; DISPLAY=:{number} is dead");
            return;
        }
        // Deferred: this may run inside the callback of the source being replaced.
        let h = handle.clone();
        handle.insert_idle(move |state| {
            // The dead instance still holds the lock on its display number until its source is dropped.
            if let Some(t) = state.x.as_mut().and_then(|x| x.source.take()) {
                state.loop_handle.remove(t);
            }
            if let Err(e) = state.spawn_xwayland(h, Some(number)) {
                tracing::error!("xwayland: restart failed: {e}");
            }
        });
    }

    fn x11_window(&self, s: &X11Surface) -> Option<Window> {
        self.space.elements().find(|w| w.x11_surface() == Some(s)).cloned()
    }

    /// The window whose root surface is `s`, Wayland or X11.
    pub fn window_for_surface(&self, s: &WlSurface) -> Option<Window> {
        self.space.elements().find(|w| w.wl_surface().as_deref() == Some(s)).cloned()
    }

    /// Raise `w`, give it the keyboard, and tell every window whether it is active.
    pub fn focus_and_activate(&mut self, w: &Window) {
        self.space.raise_element(w, true);
        if let Some(x) = w.x11_surface() {
            if let Some(xwm) = self.x.as_mut().and_then(|x| x.xwm.as_mut()) {
                let _ = xwm.raise_window(x);
            }
            let _ = x.set_activated(true);
        }
        let keyboard = self.seat.get_keyboard().unwrap();
        if let Some(s) = w.wl_surface().map(|s| s.into_owned()) {
            keyboard.set_focus(self, Some(s), SERIAL_COUNTER.next_serial());
        }
        let all: Vec<Window> = self.space.elements().cloned().collect();
        for o in &all {
            if o != w {
                if let Some(x) = o.x11_surface() {
                    let _ = x.set_activated(false);
                }
            }
            // xdg windows learn whether they are the active one (their own focus styling follows it).
            if o.toplevel().is_some() {
                o.set_activated(o == w);
            }
            send_pending(o);
        }
    }
}

impl XWaylandShellHandler for Aurora {
    fn xwayland_shell_state(&mut self) -> &mut XWaylandShellState {
        &mut self.xwayland_shell_state
    }

    fn surface_associated(&mut self, _xwm: XwmId, _wl_surface: WlSurface, surface: X11Surface) {
        // The surface exists now; a window that was already mapped can take the keyboard.
        if surface.is_override_redirect() || self.overlay_up() {
            return;
        }
        if let Some(w) = self.x11_window(&surface) {
            tracing::info!("xwayland: window {:?} ({}) has its surface", surface.title(), surface.class());
            self.focus_and_activate(&w);
        }
    }
}

impl XwmHandler for Aurora {
    fn xwm_state(&mut self, _xwm: XwmId) -> &mut X11Wm {
        self.x.as_mut().and_then(|x| x.xwm.as_mut()).expect("xwm callback with no window manager")
    }

    fn new_window(&mut self, _xwm: XwmId, _window: X11Surface) {}
    fn new_override_redirect_window(&mut self, _xwm: XwmId, _window: X11Surface) {}

    fn map_window_request(&mut self, _xwm: XwmId, window: X11Surface) {
        if window.set_mapped(true).is_err() {
            return;
        }
        let w = Window::new_x11_window(window.clone());
        let mut origin = self.usable_area().loc;
        self.space.map_element(w.clone(), origin, false);
        let bbox = self.space.element_bbox(&w).unwrap_or_default();
        if !self.shell.legacy_scene {
            // Same placement as a Wayland window: a dialog centred on the window it is transient for,
            // anything else cascaded, below the bar when there is one.
            let size = if bbox.size.w > 0 { bbox.size } else { window.geometry().size };
            let parent = window.is_transient_for().and_then(|id| self.space.elements().find(|e| e.x11_surface().is_some_and(|x| x.window_id() == id)).cloned());
            origin = self.placement(&w, size, parent.as_ref());
            self.space.map_element(w.clone(), origin, false);
        }
        let _ = window.configure(Rectangle::new(origin, bbox.size));
        tracing::info!("xwayland: mapped {:?} ({}) at {:?} size {:?}", window.title(), window.class(), origin, bbox.size);
        if !self.overlay_up() && w.wl_surface().is_some() {
            self.focus_and_activate(&w);
        }
    }

    fn mapped_override_redirect_window(&mut self, _xwm: XwmId, window: X11Surface) {
        let loc = window.last_configure().loc;
        tracing::info!("xwayland: override-redirect window mapped at {:?} size {:?}", loc, window.last_configure().size);
        self.space.map_element(Window::new_x11_window(window), loc, true);
    }

    fn unmapped_window(&mut self, _xwm: XwmId, window: X11Surface) {
        if let Some(w) = self.x11_window(&window) {
            self.space.unmap_elem(&w);
        }
        if !window.is_override_redirect() {
            let _ = window.set_mapped(false);
        }
    }

    fn destroyed_window(&mut self, _xwm: XwmId, window: X11Surface) {
        if let Some(w) = self.x11_window(&window) {
            self.space.unmap_elem(&w);
        }
    }

    fn configure_request(&mut self, _xwm: XwmId, window: X11Surface, x: Option<i32>, y: Option<i32>, w: Option<u32>, h: Option<u32>, _reorder: Option<Reorder>) {
        // A managed window may resize itself but not move itself; an override-redirect one
        // (a menu) is placed where it asks.
        let mut geo = window.last_configure();
        if window.is_override_redirect() {
            if let Some(x) = x { geo.loc.x = x; }
            if let Some(y) = y { geo.loc.y = y; }
        }
        if let Some(w) = w { geo.size.w = w as i32; }
        if let Some(h) = h { geo.size.h = h as i32; }
        let _ = window.configure(geo);
    }

    fn configure_notify(&mut self, _xwm: XwmId, window: X11Surface, geometry: Rectangle<i32, Logical>, _above: Option<u32>) {
        if let Some(w) = self.x11_window(&window) {
            self.space.map_element(w, geometry.loc, false);
        }
    }

    fn maximize_request(&mut self, _xwm: XwmId, window: X11Surface) {
        let area = self.usable_area();
        let _ = window.set_maximized(true);
        let _ = window.configure(area);
        if let Some(w) = self.x11_window(&window) {
            self.space.map_element(w, area.loc, false);
        }
    }

    fn unmaximize_request(&mut self, _xwm: XwmId, window: X11Surface) {
        let _ = window.set_maximized(false);
    }

    fn resize_request(&mut self, _xwm: XwmId, _window: X11Surface, _button: u32, _edges: ResizeEdge) {}

    fn move_request(&mut self, _xwm: XwmId, window: X11Surface, _button: u32) {
        let Some(w) = self.x11_window(&window) else { return };
        let pointer = self.seat.get_pointer().unwrap();
        let Some(start_data) = pointer.grab_start_data() else { return };
        let Some(initial_window_location) = self.space.element_location(&w) else { return };
        let grab = crate::grabs::MoveSurfaceGrab { start_data, window: w, initial_window_location };
        pointer.set_grab(self, grab, SERIAL_COUNTER.next_serial(), Focus::Clear);
    }

    fn disconnected(&mut self, _xwm: XwmId) {
        // Xwayland exited (`-terminate`, or it crashed). Its windows are gone with it.
        let gone: Vec<Window> = self.space.elements().filter(|w| w.is_x11()).cloned().collect();
        for w in &gone {
            self.space.unmap_elem(w);
        }
        let Some(x) = self.x.as_mut() else { return };
        x.xwm = None;
        let num = x.display;
        tracing::info!("xwayland: exited; restarting on :{num}");
        let handle = self.loop_handle.clone();
        self.restart_xwayland(handle, num);
    }
}
