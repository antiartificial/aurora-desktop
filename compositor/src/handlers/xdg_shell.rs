use smithay::{
    desktop::{
        PopupKeyboardGrab, PopupKind, PopupManager, PopupPointerGrab, PopupUngrabStrategy, Space,
        Window, find_popup_root_surface, get_popup_toplevel_coords,
    },
    input::{
        Seat,
        pointer::{Focus, GrabStartData as PointerGrabStartData},
    },
    reexports::{
        wayland_protocols::xdg::shell::server::xdg_toplevel,
        wayland_server::{
            Resource,
            protocol::{wl_seat, wl_surface::WlSurface},
        },
    },
    utils::Serial,
    wayland::{
        compositor::with_states,
        shell::xdg::{
            PopupSurface, PositionerState, ToplevelSurface, XdgShellHandler, XdgShellState,
            XdgToplevelSurfaceData,
        },
    },
};

use crate::{
    Aurora,
    grabs::MoveSurfaceGrab,
};

impl XdgShellHandler for Aurora {
    fn xdg_shell_state(&mut self) -> &mut XdgShellState {
        &mut self.xdg_shell_state
    }

    fn new_toplevel(&mut self, surface: ToplevelSurface) {
        let wl = surface.wl_surface().clone();
        let window = Window::new_wayland_window(surface);
        // New windows start inside the usable area, never under the dock or with their bar off the top.
        // Where they really go is decided at their first commit, when their size is known
        // (`place_on_first_commit`).
        let mut origin = self.usable_area().loc;
        if self.shell.deco_on() {
            origin.y += self.shell.deco_height();
        }
        self.deco.unplaced.insert(wl.id().to_string());
        self.space.map_element(window, origin, false);
        // A new window takes keyboard focus, except while the trusted overlay
        // is up: then it must not receive keys until the overlay is gone.
        if !self.overlay_up() {
            self.seat.get_keyboard().unwrap().set_focus(self, Some(wl), smithay::utils::SERIAL_COUNTER.next_serial());
        }
    }

    fn new_popup(&mut self, surface: PopupSurface, _positioner: PositionerState) {
        self.unconstrain_popup(&surface);
        let _ = self.popups.track_popup(PopupKind::Xdg(surface));
    }

    fn reposition_request(&mut self, surface: PopupSurface, positioner: PositionerState, token: u32) {
        surface.with_pending_state(|state| {
            let geometry = positioner.get_geometry();
            state.geometry = geometry;
            state.positioner = positioner;
        });
        self.unconstrain_popup(&surface);
        surface.send_repositioned(token);
    }

    fn move_request(&mut self, surface: ToplevelSurface, seat: wl_seat::WlSeat, serial: Serial) {
        let seat = Seat::from_resource(&seat).unwrap();

        let wl_surface = surface.wl_surface();

        if let Some(start_data) = check_grab(&seat, wl_surface, serial) {
            let pointer = seat.get_pointer().unwrap();

            let window = self
                .space
                .elements()
                .find(|w| w.toplevel().is_some_and(|t| t.wl_surface() == wl_surface))
                .unwrap()
                .clone();
            let initial_window_location = self.space.element_location(&window).unwrap();

            let grab = MoveSurfaceGrab {
                start_data,
                window,
                initial_window_location,
            };

            pointer.set_grab(self, grab, serial, Focus::Clear);
        }
    }

    fn resize_request(
        &mut self,
        surface: ToplevelSurface,
        seat: wl_seat::WlSeat,
        serial: Serial,
        edges: xdg_toplevel::ResizeEdge,
    ) {
        let seat = Seat::from_resource(&seat).unwrap();

        let wl_surface = surface.wl_surface();

        if let Some(start_data) = check_grab(&seat, wl_surface, serial) {
            let window = self
                .space
                .elements()
                .find(|w| w.toplevel().is_some_and(|t| t.wl_surface() == wl_surface))
                .unwrap()
                .clone();
            self.start_resize(window, edges.into(), start_data, serial);
        }
    }

    fn maximize_request(&mut self, surface: ToplevelSurface) {
        // Maximise to the usable area: the output minus the dock's reserved strip (and the bar's height).
        let wl = surface.wl_surface().clone();
        let win = self.space.elements().find(|w| w.toplevel().is_some_and(|t| t.wl_surface() == &wl)).cloned();
        if let Some(w) = win {
            self.set_maximized(&w, true);
        }
    }

    fn unmaximize_request(&mut self, surface: ToplevelSurface) {
        let wl = surface.wl_surface().clone();
        let win = self.space.elements().find(|w| w.toplevel().is_some_and(|t| t.wl_surface() == &wl)).cloned();
        if let Some(w) = win {
            self.set_maximized(&w, false);
        }
    }

    fn fullscreen_request(&mut self, surface: ToplevelSurface, _output: Option<smithay::reexports::wayland_server::protocol::wl_output::WlOutput>) {
        let wl = surface.wl_surface().clone();
        let win = self.space.elements().find(|w| w.toplevel().is_some_and(|t| t.wl_surface() == &wl)).cloned();
        if let Some(w) = win {
            self.set_fullscreen(&w, true);
        }
    }

    fn unfullscreen_request(&mut self, surface: ToplevelSurface) {
        let wl = surface.wl_surface().clone();
        let win = self.space.elements().find(|w| w.toplevel().is_some_and(|t| t.wl_surface() == &wl)).cloned();
        if let Some(w) = win {
            self.set_fullscreen(&w, false);
        }
    }

    /// `xdg_popup.grab`: give a client's popup the keyboard and route the pointer to it.
    ///
    /// This was a stub, and the symptom was specific: a client menu DREW but could not be driven
    /// by keyboard, because without a grab the compositor never moves keyboard focus to the
    /// popup's surface. `aurora::ui`'s context menu found it -- arrow keys went to the window
    /// underneath while the menu sat there looking focused.
    ///
    /// A grab is only honoured for a client that already holds the relevant input: the serial
    /// has to match an event we actually sent it. Otherwise any client could seize the keyboard
    /// by asking, which is the hole `xdg_popup.grab`'s serial argument exists to close. On a
    /// mismatch the whole popup chain is dismissed rather than left half-grabbed, because a menu
    /// that is up but not receiving input is the worst of the three states.
    fn grab(&mut self, surface: PopupSurface, seat: wl_seat::WlSeat, serial: Serial) {
        let Some(seat) = Seat::<Aurora>::from_resource(&seat) else { return };
        let kind = PopupKind::Xdg(surface);
        let Ok(root) = find_popup_root_surface(&kind) else { return };
        let Ok(mut grab) = self.popups.grab_popup(root, kind, &seat, serial) else { return };

        if let Some(keyboard) = seat.get_keyboard() {
            // Already grabbed by someone else, and not by this chain: refuse.
            if keyboard.is_grabbed()
                && !(keyboard.has_grab(serial)
                    || keyboard.has_grab(grab.previous_serial().unwrap_or(serial)))
            {
                grab.ungrab(PopupUngrabStrategy::All);
                return;
            }
            keyboard.set_focus(self, grab.current_grab(), serial);
            keyboard.set_grab(self, PopupKeyboardGrab::new(&grab), serial);
        }
        if let Some(pointer) = seat.get_pointer() {
            if pointer.is_grabbed()
                && !(pointer.has_grab(serial)
                    || pointer.has_grab(grab.previous_serial().unwrap_or(grab.serial())))
            {
                grab.ungrab(PopupUngrabStrategy::All);
                return;
            }
            // Focus::Keep: the pointer stays where it is. Re-focusing on grab would deliver a
            // leave/enter pair the client did not ask for and would reset its hover.
            pointer.set_grab(self, PopupPointerGrab::new(&grab), serial, Focus::Keep);
        }
        tracing::info!("xdg_popup: grab granted to a client popup");
    }
}

fn check_grab(
    seat: &Seat<Aurora>,
    surface: &WlSurface,
    serial: Serial,
) -> Option<PointerGrabStartData<Aurora>> {
    let pointer = seat.get_pointer()?;

    // Check that this surface has a click grab.
    if !pointer.has_grab(serial) {
        return None;
    }

    let start_data = pointer.grab_start_data()?;

    let (focus, _) = start_data.focus.as_ref()?;
    // If the focus was for a different surface, ignore the request.
    if !focus.id().same_client_as(&surface.id()) {
        return None;
    }

    Some(start_data)
}

/// Should be called on `WlSurface::commit`
pub fn handle_commit(popups: &mut PopupManager, space: &Space<Window>, surface: &WlSurface) {
    // Handle toplevel commits.
    if let Some(window) = space
        .elements()
        .find(|w| w.toplevel().is_some_and(|t| t.wl_surface() == surface))
        .cloned()
    {
        let initial_configure_sent = with_states(surface, |states| {
            states
                .data_map
                .get::<XdgToplevelSurfaceData>()
                .unwrap()
                .lock()
                .unwrap()
                .initial_configure_sent
        });

        if !initial_configure_sent {
            window.toplevel().unwrap().send_configure();
        }
    }

    // Handle popup commits.
    popups.commit(surface);
    if let Some(popup) = popups.find_popup(surface) {
        match popup {
            PopupKind::Xdg(ref xdg) => {
                if !xdg.is_initial_configure_sent() {
                    // NOTE: This should never fail as the initial configure is always
                    // allowed.
                    xdg.send_configure().expect("initial configure failed");
                }
            }
            PopupKind::InputMethod(ref _input_method) => {}
        }
    }
}

impl Aurora {
    fn unconstrain_popup(&self, popup: &PopupSurface) {
        let Ok(root) = find_popup_root_surface(&PopupKind::Xdg(popup.clone())) else {
            return;
        };
        let Some(window) = self
            .space
            .elements()
            .find(|w| w.toplevel().is_some_and(|t| t.wl_surface() == &root))
        else {
            return;
        };

        let output = self.space.outputs().next().unwrap();
        let output_geo = self.space.output_geometry(output).unwrap();
        let window_geo = self.space.element_geometry(window).unwrap();

        // The target geometry for the positioner should be relative to its parent's geometry, so
        // we will compute that here.
        let mut target = output_geo;
        target.loc -= get_popup_toplevel_coords(&PopupKind::Xdg(popup.clone()));
        target.loc -= window_geo.loc;

        popup.with_pending_state(|state| {
            state.geometry = state.positioner.get_unconstrained_geometry(target);
        });
    }
}
