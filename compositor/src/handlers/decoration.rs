//! `xdg_decoration_manager_v1`: server-side is the default, and a client that asks to decorate itself
//! is believed. A window is drawn a title bar by `shell::deco` exactly when its mode is not client-side
//! (`Aurora::decorated`), so the answer given here is the whole policy.

use smithay::{
    reexports::wayland_protocols::xdg::decoration::zv1::server::zxdg_toplevel_decoration_v1::Mode,
    wayland::shell::xdg::{ToplevelSurface, decoration::XdgDecorationHandler},
};

use crate::Aurora;

impl Aurora {
    fn set_decoration_mode(&mut self, t: &ToplevelSurface, want: Mode) {
        // Server-side is only on offer while the compositor can draw it.
        let mode = if want == Mode::ServerSide && self.shell.deco_on() { Mode::ServerSide } else { Mode::ClientSide };
        t.with_pending_state(|s| s.decoration_mode = Some(mode));
        tracing::info!("decoration: {:?} asked for {want:?}, told {mode:?}", self.window_title_of(t));
        if t.is_initial_configure_sent() {
            t.send_pending_configure();
        }
    }

    fn window_title_of(&self, t: &ToplevelSurface) -> String {
        smithay::wayland::compositor::with_states(t.wl_surface(), |s| {
            s.data_map.get::<smithay::wayland::shell::xdg::XdgToplevelSurfaceData>().and_then(|d| d.lock().ok()).and_then(|d| d.title.clone()).unwrap_or_default()
        })
    }
}

impl XdgDecorationHandler for Aurora {
    fn new_decoration(&mut self, toplevel: ToplevelSurface) {
        // The initial configure carries it.
        self.set_decoration_mode(&toplevel, Mode::ServerSide);
    }

    fn request_mode(&mut self, toplevel: ToplevelSurface, mode: Mode) {
        self.set_decoration_mode(&toplevel, mode);
    }

    fn unset_mode(&mut self, toplevel: ToplevelSurface) {
        self.set_decoration_mode(&toplevel, Mode::ServerSide);
    }
}
