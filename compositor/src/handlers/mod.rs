mod compositor;
mod decoration;
mod xdg_shell;

use crate::Aurora;

//
// Wl Seat
//

use smithay::input::dnd::{DnDGrab, DndGrabHandler, GrabType, Source};
use smithay::input::pointer::Focus;
use smithay::input::{Seat, SeatHandler, SeatState};
use smithay::reexports::wayland_server::Resource;
use smithay::reexports::wayland_server::protocol::wl_surface::WlSurface;
use smithay::utils::Serial;
use smithay::wayland::output::OutputHandler;
use smithay::wayland::pointer_constraints::PointerConstraintsHandler;
use smithay::wayland::selection::{SelectionHandler, SelectionSource, SelectionTarget};
use smithay::wayland::selection::ext_data_control::{DataControlHandler, DataControlState};
use smithay::wayland::selection::primary_selection::{PrimarySelectionHandler, PrimarySelectionState, set_primary_focus};
use smithay::wayland::selection::data_device::{
    DataDeviceHandler, DataDeviceState, WaylandDndGrabHandler, set_data_device_focus,
};

impl SeatHandler for Aurora {
    type KeyboardFocus = WlSurface;
    type PointerFocus = WlSurface;
    type TouchFocus = WlSurface;

    fn seat_state(&mut self) -> &mut SeatState<Aurora> {
        &mut self.seat_state
    }

    fn cursor_image(&mut self, _seat: &Seat<Self>, _image: smithay::input::pointer::CursorImageStatus) {}

    fn focus_changed(&mut self, seat: &Seat<Self>, focused: Option<&WlSurface>) {
        let dh = &self.display_handle;
        let client = focused.and_then(|s| dh.get_client(s.id()).ok());
        set_data_device_focus(dh, seat, client.clone());
        set_primary_focus(dh, seat, client);
    }
}

impl PointerConstraintsHandler for Aurora {}

//
// Text input and input method
//
// Focus follows the keyboard: Smithay's keyboard focus hook enters and leaves the text-input
// for the focused surface and (de)activates the input method. The compositor draws nothing for
// the preedit: the text-input client renders its own preedit, and an input method's candidate
// window is a popup surface that is placed beside the cursor rectangle and drawn as part of its
// parent window.
//

impl smithay::wayland::text_input::TextInputActivation for Aurora {}

impl smithay::wayland::input_method::InputMethodHandler for Aurora {
    fn new_popup(&mut self, surface: smithay::wayland::input_method::PopupSurface) {
        if let Err(e) = self.popups.track_popup(smithay::desktop::PopupKind::from(surface)) {
            tracing::warn!("ime: popup not tracked: {e}");
        }
    }

    fn popup_repositioned(&mut self, _surface: smithay::wayland::input_method::PopupSurface) {}

    fn dismiss_popup(&mut self, surface: smithay::wayland::input_method::PopupSurface) {
        if let Some(parent) = surface.get_parent().map(|p| p.surface.clone()) {
            let _ = smithay::desktop::PopupManager::dismiss_popup(&parent, &smithay::desktop::PopupKind::from(surface));
        }
    }

    fn parent_geometry(&self, parent: &WlSurface) -> smithay::utils::Rectangle<i32, smithay::utils::Logical> {
        self.window_for_surface(parent).map(|w| w.geometry()).unwrap_or_default()
    }
}

//
// Wl Data Device
//

impl SelectionHandler for Aurora {
    /// Compositor-side selections are only ever the host's (see `crate::clipboard`).
    type SelectionUserData = ();

    fn new_selection(&mut self, ty: SelectionTarget, source: Option<SelectionSource>, _seat: Seat<Self>) {
        crate::clipboard::guest_selected(self, ty, source);
    }

    fn send_selection(&mut self, ty: SelectionTarget, mime_type: String, fd: std::os::fd::OwnedFd, _seat: Seat<Self>, _: &()) {
        crate::clipboard::guest_pastes(self, ty, mime_type, fd);
    }
}

impl PrimarySelectionHandler for Aurora {
    fn primary_selection_state(&mut self) -> &mut PrimarySelectionState {
        &mut self.primary_selection_state
    }
}

impl DataControlHandler for Aurora {
    fn data_control_state(&mut self) -> &mut DataControlState {
        &mut self.data_control_state
    }
}

impl DataDeviceHandler for Aurora {
    fn data_device_state(&mut self) -> &mut DataDeviceState {
        &mut self.data_device_state
    }
}

impl DndGrabHandler for Aurora {}
impl WaylandDndGrabHandler for Aurora {
    fn dnd_requested<S: Source>(
        &mut self,
        source: S,
        _icon: Option<WlSurface>,
        seat: Seat<Self>,
        serial: Serial,
        type_: GrabType,
    ) {
        match type_ {
            GrabType::Pointer => {
                let ptr = seat.get_pointer().unwrap();
                let start_data = ptr.grab_start_data().unwrap();
                // Whether this drag could be handed to the host, and what it may offer it, is
                // decided by the client that started it: the surface the press landed on. A client
                // in a domain may not hand the host a path only that domain can see. See
                // `crate::dnd::out`.
                let confined = start_data.focus.as_ref().map_or(true, |(s, _)| crate::dnd::is_confined(self, s));
                let source = crate::dnd::out::wrap(self, source, confined);

                // create a dnd grab to start the operation
                let grab = DnDGrab::new_pointer(&self.display_handle, start_data, source, seat);
                ptr.set_grab(self, grab, serial, Focus::Keep);
            }
            GrabType::Touch => {
                // aurora lacks touch handling
                source.cancel();
            }
        }
    }
}

//
// Wl Output & Xdg Output
//

impl OutputHandler for Aurora {}

smithay::delegate_dispatch2!(Aurora);
