//! The logical/physical boundary, in one place.
//!
//! Two coordinate systems, kept apart by Smithay's marker types:
//!
//! - `Physical`: device pixels. The output mode, the offscreen scene texture,
//!   captures, the dock layout (`shell::layout`), glyph rasters and every GL
//!   draw call are in these.
//! - `Logical`: what Wayland clients and the `Space` speak. Window locations
//!   and sizes, xdg configures, pointer coordinates, popup constraints, the
//!   output geometry as the `Space` sees it (mode / scale).
//!
//! The output's scale (`Output::current_scale`, fractional) is the single
//! factor between them. Conversions happen at exactly these places and
//! nowhere else: `fx::window_elements` and `fx::render_scene` (a window's
//! location and size, the glass rectangle), `fx::switcher::draw` (card size),
//! `input.rs` (pointer to dock hit-test, dock strip to usable area) and the
//! trusted overlay (`trusted::draw`, sized in logical units, rasterised at
//! device pixels). Everything else stays in the unit it was born in.
//!
//! Clients are told the scale two ways. `wl_output.scale` and
//! `wl_surface.preferred_buffer_scale` carry the integer ceiling (a 1.45 output
//! says 2: those clients draw at 2x and the compositor scales their buffer down
//! to 1.45, which is sharp rather than soft). `wp_fractional_scale_v1` carries
//! the exact value; such a client draws at `round(logical * scale)` and uses
//! `wp_viewport` to say how many logical pixels the buffer covers.

use smithay::{
    desktop::Window,
    output::{Output, Scale},
    reexports::wayland_server::protocol::wl_surface::WlSurface,
    utils::{Logical, Physical, Point, Rectangle, Size, Transform},
    wayland::{
        compositor::{send_surface_state, with_states},
        fractional_scale::{FractionalScaleHandler, with_fractional_scale},
    },
};

use crate::Aurora;

/// Scales are carried on the wire in 1/120ths; use that grid everywhere so
/// the compositor and a fractional-scale client agree on the size of a buffer.
pub fn quantise(s: f64) -> f64 {
    ((s.clamp(0.5, 8.0)) * 120.0).round() / 120.0
}

/// What `wl_output` should say for `s`: the exact value as a fraction, which
/// Smithay rounds up for integer-only clients.
pub fn advertised(s: f64) -> Scale {
    if s.fract() == 0.0 { Scale::Integer(s as i32) } else { Scale::Fractional(s) }
}

impl Aurora {
    pub fn output(&self) -> Option<&Output> {
        self.space.outputs().next()
    }

    /// The output scale, 1.0 before an output exists.
    pub fn output_scale(&self) -> f64 {
        self.output().map_or(1.0, |o| o.current_scale().fractional_scale())
    }

    /// The output in device pixels (its mode).
    pub fn output_physical(&self) -> Size<i32, Physical> {
        self.output().and_then(|o| o.current_mode()).map_or((0, 0).into(), |m| m.size)
    }

    pub fn to_physical_point(&self, p: Point<f64, Logical>) -> Point<f64, Physical> {
        p.to_physical(self.output_scale())
    }

    /// The scale the output should have: the theme's `[output] scale` when set, else the host's.
    pub fn sync_scale(&mut self, output: &Output) {
        let want = if self.fx.theme.output.scale > 0.0 { self.fx.theme.output.scale } else { self.shell.host_scale };
        self.set_output_scale(output, want);
    }

    /// The host (or `--scale`) reports a scale. It applies unless the theme overrides it.
    pub fn set_host_scale(&mut self, output: &Output, scale: f64) {
        self.shell.host_scale = quantise(scale);
        self.sync_scale(output);
    }

    /// Change the output scale, and tell every client.
    pub fn set_output_scale(&mut self, output: &Output, scale: f64) {
        let scale = quantise(scale);
        if output.current_scale().fractional_scale() == scale && self.shell.scale == scale {
            return;
        }
        output.change_current_state(None, None, Some(advertised(scale)), None);
        self.shell.scale = scale;
        let windows: Vec<Window> = self.space.elements().cloned().collect();
        for w in &windows {
            w.with_surfaces(|s, _| announce(s, scale));
            for (popup, _) in smithay::desktop::PopupManager::popups_for_surface(w.toplevel().unwrap().wl_surface()) {
                announce(popup.wl_surface(), scale);
            }
        }
    }
}

/// Send both scale hints to one surface (each only if it changed).
pub fn announce(surface: &WlSurface, scale: f64) {
    with_states(surface, |states| {
        send_surface_state(surface, states, scale.ceil() as i32, Transform::Normal);
        with_fractional_scale(states, |fs| fs.set_preferred_scale(scale));
    });
}

impl FractionalScaleHandler for Aurora {
    fn new_fractional_scale(&mut self, surface: WlSurface) {
        announce(&surface, self.output_scale());
    }
}

/// A logical rectangle as the physical pixels it covers at `scale`.
pub fn rect_to_physical(r: Rectangle<i32, Logical>, scale: f64) -> Rectangle<i32, Physical> {
    r.to_physical_precise_round(scale)
}

/// A physical rectangle (the dock's strip) as logical units, rounded inwards:
/// the origin up and the far edge down, so the result never covers a device
/// pixel that was outside the input.
pub fn rect_to_logical_inward(r: Rectangle<i32, Physical>, scale: f64) -> Rectangle<i32, Logical> {
    let (x0, y0) = ((r.loc.x as f64 / scale).ceil() as i32, (r.loc.y as f64 / scale).ceil() as i32);
    let (x1, y1) = (((r.loc.x + r.size.w) as f64 / scale).floor() as i32, ((r.loc.y + r.size.h) as f64 / scale).floor() as i32);
    Rectangle::new((x0, y0).into(), ((x1 - x0).max(0), (y1 - y0).max(0)).into())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scale_sits_on_the_wire_grid() {
        assert_eq!(quantise(1.45), 1.45);
        assert_eq!(quantise(1.4500001), 1.45);
        assert_eq!(quantise(100.0), 8.0);
        // what a client computes from the protocol integer equals the compositor's value
        assert_eq!((quantise(1.45) * 120.0).round() as u32, 174);
    }

    #[test]
    fn wl_output_rounds_up_and_the_fraction_survives() {
        assert_eq!(advertised(1.45).integer_scale(), 2);
        assert_eq!(advertised(1.45).fractional_scale(), 1.45);
        assert_eq!(advertised(1.0).integer_scale(), 1);
        assert_eq!(advertised(2.0).fractional_scale(), 2.0);
    }

    #[test]
    fn a_dock_strip_converts_inwards() {
        // usable area above a 100 px strip at the bottom of a 1160 px high output
        let r = Rectangle::<i32, Physical>::new((0, 0).into(), (1856, 1060).into());
        let l = rect_to_logical_inward(r, 1.45);
        assert_eq!((l.loc.x, l.loc.y, l.size.w, l.size.h), (0, 0, 1280, 731));
        assert!((l.size.h as f64 * 1.45) <= 1060.0, "never reaches into the strip");
        // identity at scale 1
        assert_eq!(rect_to_logical_inward(r, 1.0), Rectangle::<i32, Logical>::new((0, 0).into(), (1856, 1060).into()));
    }

    #[test]
    fn logical_to_device_size_is_the_buffer_a_fractional_client_draws() {
        // 420x300 at 1.45: the client draws round(420*1.45) x round(300*1.45); so does the compositor
        let s: Size<i32, Physical> = Size::<i32, Logical>::from((420, 300)).to_physical_precise_round(1.45);
        assert_eq!((s.w, s.h), (609, 435));
    }
}
