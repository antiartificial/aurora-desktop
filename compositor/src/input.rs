use smithay::{
    backend::input::{
        InputTime, AbsolutePositionEvent, Axis, AxisSource, ButtonState, Event, InputBackend, InputEvent,
        KeyboardKeyEvent, PointerAxisEvent, PointerButtonEvent,
    },
    input::{
        pointer::{AxisFrame, ButtonEvent, MotionEvent},
    },
    reexports::wayland_server::protocol::wl_surface::WlSurface,
    utils::{Logical, Point, Rectangle, SERIAL_COUNTER},
};

use crate::state::Aurora;

impl Aurora {
    /// Output size in device pixels (the compositor's single output).
    pub fn output_size(&self) -> (i32, i32) {
        self.space.outputs().next().and_then(|o| self.space.output_geometry(o)).map_or((0, 0), |g| (g.size.w, g.size.h))
    }

    /// The area windows may use: the output minus the dock's reserved strip.
    pub fn usable_area(&self) -> Rectangle<i32, Logical> {
        let (w, h) = self.output_size();
        let u = self.shell.usable(w, h);
        Rectangle::new((u.x, u.y).into(), (u.w, u.h).into())
    }

    /// The pointer moved to `pos` (output pixels). Over the dock the pointer
    /// belongs to the compositor: no client sees it, and the item under it is
    /// highlighted. Shared by the winit backend and the headless test input.
    pub fn pointer_moved(&mut self, pos: Point<f64, Logical>, time: InputTime) {
        let (w, h) = self.output_size();
        let l = self.shell.layout(w, h);
        let on_dock = l.panel.contains(pos.x, pos.y);
        let hover = if on_dock { l.item_at(pos.x, pos.y).filter(|&i| self.shell.item(i).is_some_and(|it| it.clickable)) } else { None };
        self.shell.hover = hover;
        let under = if on_dock { None } else { self.surface_under(pos) };
        let pointer = self.seat.get_pointer().unwrap();
        pointer.motion(self, under, &MotionEvent { location: pos, serial: SERIAL_COUNTER.next_serial(), time });
        pointer.frame(self);
    }

    pub fn pointer_button(&mut self, button: u32, button_state: ButtonState, time: InputTime) {
        let pointer = self.seat.get_pointer().unwrap();
        let keyboard = self.seat.get_keyboard().unwrap();
        let serial = SERIAL_COUNTER.next_serial();
        let loc = pointer.current_location();
        let (w, h) = self.output_size();
        let l = self.shell.layout(w, h);
        if l.panel.contains(loc.x, loc.y) {
            // The dock swallows the click: no window is raised or focused, no client sees it.
            if button_state == ButtonState::Pressed {
                if let Some(i) = l.item_at(loc.x, loc.y) {
                    self.shell.launch(i);
                }
            }
            return;
        }
        if ButtonState::Pressed == button_state && !pointer.is_grabbed() {
            if let Some((window, _loc)) = self.space.element_under(loc).map(|(w, l)| (w.clone(), l)) {
                self.space.raise_element(&window, true);
                keyboard.set_focus(self, Some(window.toplevel().unwrap().wl_surface().clone()), serial);
                self.space.elements().for_each(|window| {
                    window.toplevel().unwrap().send_pending_configure();
                });
            } else {
                self.space.elements().for_each(|window| {
                    window.set_activated(false);
                    window.toplevel().unwrap().send_pending_configure();
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
            InputEvent::PointerMotion { .. } => {}
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
