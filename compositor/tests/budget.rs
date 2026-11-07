//! The frame budget, as a gate rather than a report.
//!
//! `visual.rs::frame_budget_report` prints the same measurement and asserts
//! nothing, because it is a sweep over window counts whose absolute numbers
//! depend on the GPU. This file asserts two things that must not regress: a
//! STILL scene -- nothing moving, one window, the dock and the clock up --
//! costs less than one 60 Hz frame at the user's real display geometry and at
//! scale 1; and composing a frame costs ONE GPU fence wait however much glass
//! is on screen. The second is a count rather than a duration, and is the one
//! to trust on a machine whose GPU other work is using.
//!
//! What the number means. `--frame-stats` normally blocks the CPU on a GPU
//! fence at every phase end, which serialises CPU and GPU six times a frame
//! and makes every column an upper bound. `AURORA_STATS_FENCE=frame` (see
//! `src/stats.rs`) turns that off and fences exactly once, after the frame's
//! last draw -- which is what the winit backend already does before it
//! presents -- so `total` is the honest cost of the frame, GPU included.
//! The test sets it, so it measures what a real frame costs.
//!
//! Why 16.7 ms and not something tighter. It is the 60 Hz budget the project
//! states, not a tuned threshold. When these tests were written the measured
//! p50 of a still frame was 2.8-4.5 ms on a GPU already at 96% from other
//! work, and reverting the fix that bought that (the per-pass fences in
//! `fx/blur.rs` and `fx/mod.rs`) put it at 24.9-55.2 ms. So the ceiling has
//! roughly 4x of headroom above a healthy frame and is exceeded 1.5x or more
//! by the defect it exists to catch.

mod common;

use common::*;

/// One 60 Hz frame.
const BUDGET_MS: f64 = 16.7;

/// A still scene at `size`/`scale` with `windows` client windows, the shell as
/// users get it, time frozen so nothing ticks. Returns the frame-stats summary
/// of the last 120 frames.
fn still_scene_n(size: &str, scale: &str, windows: usize) -> String {
    let d = tempfile::tempdir().unwrap();
    let path = d.path().join("stats.csv");
    let mut c = Comp::start_shell(&[
        &format!("--headless={size}"),
        "--scale",
        scale,
        "--frame-stats",
        path.to_str().unwrap(),
        "--freeze-time",
    ]);
    for i in 0..windows {
        c.client();
        // Spread them out: windows further apart than any blur reaches share one
        // prepass, so the count under test is the per-surface chain, not the prepass.
        c.inject(&format!("move {i} {} {}", 40 + i * 420, 40 + i * 120));
    }
    // Let the arrival animation finish before the frames that are measured.
    c.step_frames(40);
    c.step_frames(160);
    let s = c.stats(120);
    // The pipeline, not the flat fallback, has to have drawn these frames: a
    // frame with no glass in it would pass the budget by not doing the work.
    let n: f64 = s.split("fx_frames=").nth(1).and_then(|t| t.split_whitespace().next()).and_then(|t| t.parse().ok()).unwrap_or(0.0);
    assert!(n >= 120.0, "the effects pipeline did not draw the measured frames: {s}");
    s
}

fn assert_in_budget(what: &str, s: &str) {
    let (p50, p95) = (stat(s, "total", "p50"), stat(s, "total", "p95"));
    println!("BUDGET {what}: {s}");
    assert!(
        p50 < BUDGET_MS,
        "a still scene at {what} costs p50={p50:.3} ms, over the {BUDGET_MS} ms 60 Hz budget, with nothing moving: {s}"
    );
    assert!(
        p95 < BUDGET_MS,
        "a still scene at {what} misses the {BUDGET_MS} ms budget one frame in twenty (p95={p95:.3} ms): {s}"
    );
}

/// One test, not four, because it sets an environment variable the child
/// compositors inherit (`AURORA_STATS_FENCE`), and `set_var` is only sound
/// while no other thread is reading the environment -- which every other test
/// in this binary would be doing as it spawns its own compositor.
#[test]
fn a_still_scene_fits_in_one_frame_and_costs_one_fence_wait() {
    // One fence per frame, not one per phase: see this file's own docs.
    unsafe { std::env::set_var("AURORA_STATS_FENCE", "frame") };

    let real = still_scene_n("1856x1160", "1.45", 1);
    let unscaled = still_scene_n("1856x1160", "1", 1);
    // Two bugs this month showed up only at fractional scale, so both are gated.
    assert_in_budget("1856x1160 scale 1.45", &real);
    assert_in_budget("1856x1160 scale 1", &unscaled);

    // The gate that survives a loaded GPU.
    //
    // A frame of the effects pipeline costs exactly ONE CPU-visible GPU fence
    // wait -- the one that hands the finished framebuffer to whoever reads it
    // next -- and that number does not grow with the glass on screen. Every
    // blur pass and every composite step used to end with its own
    // `Renderer::wait`, which is an `eglCreateSync`, an `eglWaitSync` and two
    // `eglMakeCurrent` calls apiece: measured at 28 a frame for one window and
    // 56 for four. They bought nothing, because consecutive passes run on one
    // GL context, which orders them. This is a COUNT, so a GPU that other work
    // is using cannot make it flap -- which the budget above, honest as it is,
    // can.
    let four = still_scene_n("1856x1160", "1.45", 4);
    println!("WAITS four windows: {four}");
    for (what, s) in [("one window", &real), ("four windows", &four)] {
        let (p50, max) = (stat(s, "waits", "p50"), stat(s, "waits", "max"));
        assert_eq!(p50, 1.0, "a frame with {what} did {p50} fence waits, not 1: {s}");
        assert_eq!(max, 1.0, "the worst frame with {what} did {max} fence waits, not 1: {s}");
    }
    // The second scene really did draw more glass, so the count above is a count
    // over more work and not over a scene that quietly stopped doing any.
    let (g1, g4) = (stat(&real, "surfaces", "p50"), stat(&four, "surfaces", "p50"));
    assert!(g4 > g1, "four windows drew {g4} glass surfaces, one drew {g1}: the scenes are not different");
}
