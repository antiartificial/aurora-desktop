use smithay::{
    backend::input::{
        InputTime, AbsolutePositionEvent, Axis, AxisSource, ButtonState, Event, InputBackend, InputEvent,
        KeyboardKeyEvent, PointerAxisEvent, PointerButtonEvent, PointerMotionEvent,
    },
    input::{
        pointer::{AxisFrame, ButtonEvent, MotionEvent},
    },
    reexports::wayland_server::protocol::wl_surface::WlSurface,
    utils::{Logical, Physical, Point, Rectangle, SERIAL_COUNTER},
};

use crate::state::Aurora;

const BTN_LEFT: u32 = 0x110;
const BTN_RIGHT: u32 = 0x111;

use crate::shell::{Press, menu::Action};
use crate::xwayland::send_pending;

impl Aurora {
    /// Do what a menu item or settings control says.
    pub fn run_action(&mut self, a: Action) {
        match a {
            Action::Edit(edits) => {
                let msg = match self.fx.apply_edits(&edits) {
                    Ok(m) => m,
                    Err(e) => {
                        tracing::error!("settings: not applied: {e}");
                        format!("Not saved: {e}")
                    }
                };
                self.shell.set_status(msg);
            }
            Action::OpenTerminal => self.shell.open_terminal(),
            Action::OpenSettings => self.shell.open_settings(),
            Action::WindowSwitcher => self.switch(crate::fx::switcher::Act::Open),
            Action::CloseSettings => self.shell.settings = None,
        }
    }

    /// The dock's layout, in device pixels (the unit `shell::layout` works in).
    fn dock_layout(&self) -> crate::shell::layout::Layout {
        let p = self.output_physical();
        self.shell.layout(p.w, p.h)
    }

    /// The area windows may use: the output minus the dock's reserved strip,
    /// in logical units (rounded inwards, so a window never reaches the dock).
    pub fn usable_area(&self) -> Rectangle<i32, Logical> {
        let p = self.output_physical();
        let u = self.shell.usable(p.w, p.h);
        crate::scale::rect_to_logical_inward(Rectangle::<i32, Physical>::new((u.x, u.y).into(), (u.w, u.h).into()), self.output_scale())
    }

    /// The pointer moved to `pos` (logical coordinates). Over the dock the
    /// pointer belongs to the compositor: no client sees it, and the item under
    /// it is highlighted. The dock is laid out in device pixels, so the
    /// position is converted for the hit-test. Shared by the winit backend and
    /// the headless test input.
    pub fn pointer_moved(&mut self, pos: Point<f64, Logical>, time: InputTime) {
        let l = self.dock_layout();
        let dp = self.to_physical_point(pos);
        let out = self.output_physical();
        let on_chrome = self.shell.chrome_contains((dp.x, dp.y), (out.w, out.h)) || self.shell.chrome_grabbing();
        self.shell.chrome_motion((dp.x, dp.y), (out.w, out.h));
        let on_dock = l.panel.contains(dp.x, dp.y) || on_chrome;
        let hover = if on_dock && !on_chrome { l.item_at(dp.x, dp.y).filter(|&i| self.shell.item(i).is_some_and(|it| it.clickable)) } else { None };
        self.shell.hover = hover;
        let pointer = self.seat.get_pointer().unwrap();
        // A window's title bar and edges belong to the compositor too: no client sees the pointer there.
        // While a grab runs (a move, a resize) the grab owns the pointer and nothing is hit-tested.
        let deco = if on_dock || pointer.is_grabbed() { None } else { self.deco_hit(pos) };
        self.deco.hover = deco.as_ref().and_then(|(w, h)| crate::windeco::win_key(w).map(|k| (k, *h)));
        let under = if on_dock || deco.is_some() { None } else { self.surface_under(pos) };
        pointer.motion(self, under, &MotionEvent { location: pos, serial: SERIAL_COUNTER.next_serial(), time });
        pointer.frame(self);
    }

    pub fn pointer_button(&mut self, button: u32, button_state: ButtonState, time: InputTime) {
        let pointer = self.seat.get_pointer().unwrap();
        let keyboard = self.seat.get_keyboard().unwrap();
        let serial = SERIAL_COUNTER.next_serial();
        let loc = pointer.current_location();
        let l = self.dock_layout();
        let dp = self.to_physical_point(loc);
        // The menu and the settings panel are compositor chrome: a click they take goes to nobody else,
        // and neither does its release.
        if button_state == ButtonState::Released && self.chrome_buttons.remove(&button) {
            self.shell.chrome_release();
            self.deco_release(loc);
            return;
        }
        if button_state == ButtonState::Pressed {
            let out = self.output_physical();
            let at = (dp.x, dp.y);
            let o = (out.w, out.h);
            if button == BTN_LEFT {
                match self.shell.chrome_press(at, o) {
                    Press::Pass => {}
                    Press::Swallow => {
                        self.chrome_buttons.insert(button);
                        return;
                    }
                    Press::Act(a) => {
                        self.chrome_buttons.insert(button);
                        self.run_action(a);
                        return;
                    }
                }
            } else if button == BTN_RIGHT && !self.shell.chrome_contains(at, o) {
                // Right-click on the bare desktop (no window, not the dock) opens the menu; elsewhere it closes it.
                let bare = self.space.element_under(loc).is_none() && !l.panel.contains(dp.x, dp.y);
                self.shell.menu = None;
                self.chrome_buttons.insert(button);
                if bare {
                    let n = self.space.elements().count();
                    self.shell.open_menu((loc.x, loc.y), o, n);
                    self.shell.chrome_motion(at, o);
                    return;
                }
                self.chrome_buttons.remove(&button);
            } else if self.shell.chrome_contains(at, o) {
                self.chrome_buttons.insert(button);
                return;
            }
        }
        if l.panel.contains(dp.x, dp.y) {
            // The dock swallows the click: no window is raised or focused, no client sees it.
            if button_state == ButtonState::Pressed {
                if let Some(i) = l.item_at(dp.x, dp.y) {
                    self.shell.launch(i);
                }
            }
            return;
        }
        if button == BTN_LEFT && button_state == ButtonState::Pressed && !pointer.is_grabbed() {
            if let Some((w, hit)) = self.deco_hit(loc) {
                let press = ButtonEvent { button, state: button_state, serial, time };
                if self.deco_press(w, hit, loc, serial, &press) {
                    return;
                }
            }
        }
        if ButtonState::Pressed == button_state && !pointer.is_grabbed() {
            if let Some((window, _loc)) = self.space.element_under(loc).map(|(w, l)| (w.clone(), l)) {
                self.space.raise_element(&window, true);
                self.focus_and_activate(&window);
            } else {
                self.space.elements().for_each(|window| {
                    window.set_activated(false);
                    send_pending(window);
                });
                keyboard.set_focus(self, Option::<WlSurface>::None, serial);
            }
        };
        pointer.button(self, &ButtonEvent { button, state: button_state, serial, time });
        pointer.frame(self);
    }

    /// Apply the theme's `[input]` section to the seat if it changed: keymap
    /// (rules/model/layout/variant/options) and repeat. A keymap xkb cannot
    /// compile is rejected and the previous one stays.
    pub fn apply_input_config(&mut self) {
        use smithay::input::keyboard::XkbConfig;
        let cfg = self.fx.theme.input.clone();
        if self.applied_input.as_ref() == Some(&cfg) {
            return;
        }
        let kb = self.seat.get_keyboard().unwrap();
        let prev = self.applied_input.clone().unwrap_or_default();
        let keymap = |i: &crate::fx::theme::Input| (i.rules.clone(), i.model.clone(), i.layout.clone(), i.variant.clone(), i.options.clone());
        if self.applied_input.is_none() || keymap(&prev) != keymap(&cfg) {
            let xkb = XkbConfig {
                rules: &cfg.rules,
                model: &cfg.model,
                layout: &cfg.layout,
                variant: &cfg.variant,
                options: (!cfg.options.is_empty()).then(|| cfg.options.clone()),
            };
            match kb.set_xkb_config(self, xkb) {
                Ok(()) => tracing::info!("input: keymap applied (layout {:?}, variant {:?}, model {:?}, options {:?})", cfg.layout, cfg.variant, cfg.model, cfg.options),
                Err(e) => {
                    // The rejected config is remembered as applied so it is not retried every frame;
                    // the live keymap stays the previous one.
                    tracing::error!("input: keymap rejected ({e:?}); keeping the previous one (was layout {:?} variant {:?})", prev.layout, prev.variant);
                }
            }
        }
        kb.change_repeat_info(cfg.repeat_rate, cfg.repeat_delay);
        tracing::info!("input: repeat {} /s after {} ms", cfg.repeat_rate, cfg.repeat_delay);
        self.applied_input = Some(cfg);
    }

    pub fn process_input_event<I: InputBackend>(&mut self, event: InputEvent<I>) {
        // While a trusted overlay is up, only the keyboard (to the overlay,
        // see `keyboard_input`) is live. Pointer events must not reach or
        // refocus a client behind it.
        if self.overlay_up() && !matches!(event, InputEvent::Keyboard { .. }) {
            return;
        }
        match event {
            InputEvent::Keyboard { event, .. } => {
                self.keyboard_input(event.key_code(), event.state(), Event::time(&event));
            }
            InputEvent::PointerMotion { event } => {
                // Relative devices (a real mouse via libinput). Winit reports
                // absolute positions only, so this is the DRM backend's path.
                let output = self.space.outputs().next().unwrap();
                let geo = self.space.output_geometry(output).unwrap();
                let at = self.seat.get_pointer().unwrap().current_location() + event.delta();
                let x = at.x.clamp(geo.loc.x as f64, (geo.loc.x + geo.size.w) as f64 - 1.0);
                let y = at.y.clamp(geo.loc.y as f64, (geo.loc.y + geo.size.h) as f64 - 1.0);
                self.pointer_moved((x, y).into(), event.time());
            }
            InputEvent::PointerMotionAbsolute { event, .. } => {
                let output = self.space.outputs().next().unwrap();
                let output_geo = self.space.output_geometry(output).unwrap();
                let pos = event.position_transformed(output_geo.size) + output_geo.loc.to_f64();
                self.pointer_moved(pos, event.time());
            }
            InputEvent::PointerButton { event, .. } => {
                self.pointer_button(event.button_code(), event.state(), event.time());
            }
            InputEvent::PointerAxis { event, .. } => {
                let source = event.source();

                let horizontal_amount = event
                    .amount(Axis::Horizontal)
                    .unwrap_or_else(|| event.amount_v120(Axis::Horizontal).unwrap_or(0.0) * 15.0 / 120.);
                let vertical_amount = event
                    .amount(Axis::Vertical)
                    .unwrap_or_else(|| event.amount_v120(Axis::Vertical).unwrap_or(0.0) * 15.0 / 120.);
                let horizontal_amount_discrete = event.amount_v120(Axis::Horizontal);
                let vertical_amount_discrete = event.amount_v120(Axis::Vertical);

                let mut frame = AxisFrame::new(event.time()).source(source);
                if horizontal_amount != 0.0 {
                    frame = frame.value(Axis::Horizontal, horizontal_amount);
                    if let Some(discrete) = horizontal_amount_discrete {
                        frame = frame.v120(Axis::Horizontal, discrete as i32);
                    }
                }
                if vertical_amount != 0.0 {
                    frame = frame.value(Axis::Vertical, vertical_amount);
                    if let Some(discrete) = vertical_amount_discrete {
                        frame = frame.v120(Axis::Vertical, discrete as i32);
                    }
                }

                if source == AxisSource::Finger {
                    if event.amount(Axis::Horizontal) == Some(0.0) {
                        frame = frame.stop(Axis::Horizontal);
                    }
                    if event.amount(Axis::Vertical) == Some(0.0) {
                        frame = frame.stop(Axis::Vertical);
                    }
                }

                let pointer = self.seat.get_pointer().unwrap();
                pointer.axis(self, frame);
                pointer.frame(self);
            }
            _ => {}
        }
    }
}
