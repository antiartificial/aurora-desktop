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

/// A window opening. Longer than a menu because it is a bigger object arriving from further
/// away, and shorter than anything a person would call slow: past about 200 ms an opening window
/// stops reading as "arriving" and starts reading as "the machine is busy".
pub const WINDOW_MS: u64 = 170;

/// A dock item arriving. Shorter than a window: it is small, close, and there may be several at
/// once when a session restores.
pub const DOCK_ITEM_MS: u64 = 150;

/// Where an opening WINDOW starts. Nearer its final size than chrome does, because a window is
/// large: the same 0.94 on a 1200 px window is a 72 px leap, which reads as a lurch rather than
/// as an arrival.
pub const WINDOW_START_SCALE: f32 = 0.97;

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

/// How far along a window's arrival is at `elapsed_ms`: `(alpha, scale)`, or `None` once it is
/// over. The policy lives here rather than in the Shell so it can be checked without building a
/// compositor, and so there is one place that decides what "arriving" looks like.
pub fn window_arrival(elapsed_ms: f32) -> Option<(f32, f32)> {
    if elapsed_ms >= WINDOW_MS as f32 {
        return None;
    }
    let t = ease_out_cubic(elapsed_ms / WINDOW_MS as f32);
    Some((t, WINDOW_START_SCALE + (1.0 - WINDOW_START_SCALE) * t))
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

#[cfg(test)]
mod arrival_tests {
    use super::*;

    #[test]
    fn the_curve_starts_fast_and_ends_flat() {
        // Ease-OUT: already moving on the first frame after the input, and settling at the end.
        // Ease-in would make the same click feel late, which is the whole reason for the choice.
        assert!(ease_out_cubic(0.0) == 0.0);
        assert!((ease_out_cubic(1.0) - 1.0).abs() < 1e-6);
        // A third of the way through time, well past a third of the way through distance.
        assert!(ease_out_cubic(0.33) > 0.6, "{}", ease_out_cubic(0.33));
        // And the last third covers almost nothing, which is what "settling" means.
        assert!(ease_out_cubic(1.0) - ease_out_cubic(0.66) < 0.05);
    }

    #[test]
    fn the_curve_is_clamped_outside_its_domain() {
        // elapsed/duration can exceed 1 on a slow frame, and a window at 1.4 of its size would
        // be a very visible way to find that out.
        assert_eq!(ease_out_cubic(1.4), 1.0);
        assert_eq!(ease_out_cubic(-0.2), 0.0);
    }

    #[test]
    fn an_arrival_starts_small_and_faded_and_then_ends() {
        let (a0, s0) = window_arrival(0.0).expect("just started");
        assert_eq!(a0, 0.0, "it starts invisible");
        assert_eq!(s0, WINDOW_START_SCALE, "and at its starting size");

        let (a1, s1) = window_arrival(WINDOW_MS as f32 * 0.5).expect("half way through time");
        assert!(a1 > 0.5 && s1 > s0, "ease-out is past half way by half time: {a1}");

        // Over, and over for good: the entry has to retire or the compositor transforms that
        // window forever, for every window ever opened, with nothing on screen looking wrong.
        assert!(window_arrival(WINDOW_MS as f32).is_none(), "done exactly at the end");
        assert!(window_arrival(WINDOW_MS as f32 * 4.0).is_none(), "and stays done");
    }

    #[test]
    fn a_dock_item_arrives_faster_than_a_window() {
        // It is small, close, and several may arrive at once when a session restores. A window's
        // timing on a dock pill would read as the dock being slow to agree that an app started.
        assert!(DOCK_ITEM_MS < WINDOW_MS);
        assert!(DOCK_ITEM_MS >= MENU_MS, "and not so fast it is a flicker");
    }

    #[test]
    fn a_window_starts_nearer_its_size_than_chrome_does() {
        // 0.94 of a 1200 px window is a 72 px leap, which reads as a lurch rather than as an
        // arrival. Chrome is small and can afford the bigger move.
        assert!(WINDOW_START_SCALE > START_SCALE);
        assert!(WINDOW_START_SCALE < 1.0);
    }
}
