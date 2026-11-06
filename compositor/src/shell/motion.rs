//! Motion for system chrome. One curve, ease-out: it starts at its maximum
//! velocity and decelerates, so a surface is already moving on the first frame
//! after the input that opened it. Ease-in (slow start) would make the same
//! click feel late; that is the stated preference this encodes.
//!
//! The curve is cubic ease-out, `1 - (1 - t)^3`. Its slope is 3 at t = 0 (three
//! times a linear ramp) and 0 at t = 1.

use std::time::{Duration, Instant};

/// Context menus: short, they sit under the pointer.
pub const MENU_MS: u64 = 140;
/// The settings panel is bigger and travels no further than a small scale-up,
/// so it gets a little longer.
pub const PANEL_MS: u64 = 180;

/// Where the animated surface starts, as a fraction of its final size.
pub const START_SCALE: f32 = 0.94;

pub fn ease_out_cubic(t: f32) -> f32 {
    let t = t.clamp(0.0, 1.0);
    1.0 - (1.0 - t).powi(3)
}

/// An appearance animation: `progress` runs 0 to 1 through the ease-out curve.
#[derive(Clone, Copy, Debug)]
pub struct Appear {
    start: Instant,
    dur: Duration,
}

impl Appear {
    pub fn new(ms: u64) -> Self {
        Appear { start: Instant::now(), dur: Duration::from_millis(ms) }
    }

    pub fn elapsed_ms(&self) -> f32 {
        self.start.elapsed().as_secs_f32() * 1000.0
    }

    pub fn duration_ms(&self) -> f32 {
        self.dur.as_secs_f32() * 1000.0
    }

    /// The first frame of an animation, for the log: how far in it was and how far along the curve.
    pub fn describe(&self) -> String {
        let (k, alpha) = self.frame();
        format!("first frame at {:.1} ms of {:.0}: linear would be {:.2}, ease-out is {:.2} (size {:.3})", self.elapsed_ms(), self.duration_ms(), (self.elapsed_ms() / self.duration_ms()).min(1.0), alpha, k)
    }

    pub fn eased(&self) -> f32 {
        ease_out_cubic(self.start.elapsed().as_secs_f32() / self.dur.as_secs_f32())
    }

    pub fn done(&self) -> bool {
        self.start.elapsed() >= self.dur
    }

    /// Size factor and opacity for the current frame. Exactly (1, 1) once done,
    /// so a settled surface is drawn exactly as its texture is.
    pub fn frame(&self) -> (f32, f32) {
        if self.done() {
            return (1.0, 1.0);
        }
        let e = self.eased();
        (START_SCALE + (1.0 - START_SCALE) * e, e)
    }
}

/// A value that eases to a new target on the same cubic ease-out, however often the target changes:
/// a change starts from wherever the value is NOW, so reversing mid-way never jumps. Used for the
/// window decorations' focus and hover (`shell/deco.rs`).
#[derive(Clone, Copy, Debug)]
pub struct Ease {
    from: f32,
    to: f32,
    start: Instant,
}

impl Ease {
    pub fn at(v: f32) -> Self {
        Ease { from: v, to: v, start: Instant::now() }
    }

    pub fn value(&self, ms: u64) -> f32 {
        if ms == 0 {
            return self.to;
        }
        let k = ease_out_cubic(self.start.elapsed().as_secs_f32() * 1000.0 / ms as f32);
        self.from + (self.to - self.from) * k
    }

    /// Head for `to`. Calling it every frame with the same target is a no-op.
    pub fn set(&mut self, to: f32, ms: u64) {
        if self.to != to {
            *self = Ease { from: self.value(ms), to, start: Instant::now() };
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ease_out_is_fastest_at_the_start_and_settles_at_one() {
        assert_eq!(ease_out_cubic(0.0), 0.0);
        assert_eq!(ease_out_cubic(1.0), 1.0);
        // Ease-out is ahead of linear everywhere inside the interval.
        for i in 1..10 {
            let t = i as f32 / 10.0;
            assert!(ease_out_cubic(t) > t, "t={t}");
        }
        // Velocity only falls: each tenth of the time covers less distance than the one before.
        let steps: Vec<f32> = (0..10).map(|i| ease_out_cubic((i + 1) as f32 / 10.0) - ease_out_cubic(i as f32 / 10.0)).collect();
        assert!(steps.windows(2).all(|w| w[0] > w[1]), "{steps:?}");
        // Already 27% of the way after a tenth of the duration (linear would be 10%).
        assert!(ease_out_cubic(0.1) > 0.27);
    }

    #[test]
    fn a_finished_animation_is_exactly_full_size_and_opaque() {
        let a = Appear { start: Instant::now() - Duration::from_millis(500), dur: Duration::from_millis(MENU_MS) };
        assert_eq!(a.frame(), (1.0, 1.0));
        let b = Appear::new(10_000);
        let (k, alpha) = b.frame();
        assert!((START_SCALE..=1.0).contains(&k) && (0.0..=1.0).contains(&alpha));
    }

    #[test]
    fn an_ease_reverses_from_where_it_is_and_is_ahead_of_linear() {
        let mut e = Ease::at(0.0);
        e.set(1.0, 10_000);
        std::thread::sleep(Duration::from_millis(60));
        let v = e.value(10_000);
        assert!(v > 0.0 && v < 1.0);
        e.set(0.0, 10_000);
        assert!((e.value(10_000) - v).abs() < 0.05, "reversing starts from the current value, not from 1");
        assert_eq!(Ease::at(0.3).value(140), 0.3);
        let mut z = Ease::at(0.0);
        z.set(1.0, 0);
        assert_eq!(z.value(0), 1.0, "0 ms is instant");
    }
}
