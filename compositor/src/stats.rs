//! Frame-time instrumentation (`--frame-stats PATH`) and the per-phase timer the
//! effects pipeline reports into.
//!
//! Why it exists: decision 0010 found that the effects pass costs 2.3-4.0 ms
//! against 0.11 ms of memory traffic, and that nothing in the tree could say
//! where the rest goes. It also makes render-time VARIANCE a first-class
//! budget (criterion C7.5): a compositor that misses its deadline
//! unpredictably starts compositing earlier to compensate, which costs input
//! latency on every frame including cheap ones. So the summary is not a mean;
//! it is p50 / p95 / p99 / max, the standard deviation, and the worst frame
//! relative to the median.
//!
//! Which fence, and why it matters. `AURORA_STATS_FENCE=frame` blocks the CPU
//! exactly once, after the frame's last draw -- where the winit backend already
//! does (`gl.Finish()` before it presents) -- instead of at every phase end.
//! `total_ms` is then the honest cost of the frame and the phase columns are
//! submission time only. The default, per-phase fencing, is the opposite trade:
//! the breakdown is real but every column, and the total, is an upper bound.
//!
//! What is measured, and what is not. Wall clock around the real work. The
//! effects pipeline submits its passes and does not wait on them (one fence a
//! frame; see `fx`), so on its own the wall clock would measure command
//! submission and not the GPU. With stats enabled the pipeline therefore ALSO
//! blocks the CPU on the fence at the end of each phase (`Phases::lap`). That
//! serialises CPU and GPU at phase boundaries, which the production path does
//! not do: a stats run costs more than a normal one, and the numbers are
//! GPU-inclusive. `AURORA_STATS_FENCE=frame` is the other trade, above.
//! `GL_EXT_disjoint_timer_query` is not used.

use std::{
    fs::File,
    io::Write,
    path::Path,
    time::{Duration, Instant},
};

use smithay::backend::renderer::sync::SyncPoint;

/// The parts of the effects pass a lap is attributed to.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Phase {
    /// Clear and the desktop background.
    Scene,
    /// The shared downsample of the scene (full to half to quarter).
    Prepass,
    /// Per-surface blur chains.
    Chain,
    /// Glass, windows, dock, menus: everything drawn into the scene.
    Composite,
    /// The switcher.
    Switcher,
    /// The final blit into the output framebuffer.
    Blit,
}
const PHASES: usize = 6;

/// Accumulates per-phase time for the frame being rendered. A no-op unless enabled.
#[derive(Default)]
pub struct Phases {
    pub enabled: bool,
    mark: Option<Instant>,
    acc: [f64; PHASES],
    /// Glass surfaces drawn this frame.
    pub surfaces: u32,
    /// Prepass runs this frame (1 when the whole scene shares one).
    pub prepasses: u32,
}

impl Phases {
    /// Start the clock for the effects pass.
    pub fn begin(&mut self) {
        if self.enabled {
            self.acc = [0.0; PHASES];
            self.surfaces = 0;
            self.prepasses = 0;
            self.mark = Some(Instant::now());
        }
    }

    /// End a phase: block the CPU until the GPU work behind `sync` is done, then
    /// attribute the time since the last lap to `p`.
    ///
    /// With `AURORA_STATS_FENCE=frame` the per-phase CPU block is skipped, so the
    /// phase columns become submission time only and `total_ms` (fenced once, at
    /// the end of the frame, by `render.rs`) is the honest per-frame cost.
    pub fn lap(&mut self, p: Phase, sync: &SyncPoint) {
        if !self.enabled {
            return;
        }
        if phase_fence() {
            let _ = sync.wait();
        }
        self.lap_cpu(p);
    }

    /// End a phase that has no fence of its own (the work was already waited on).
    pub fn lap_cpu(&mut self, p: Phase) {
        if let Some(m) = self.mark.as_mut() {
            let now = Instant::now();
            self.acc[p as usize] += now.duration_since(*m).as_secs_f64() * 1e3;
            *m = now;
        }
    }

    pub fn ms(&self, p: Phase) -> f64 {
        self.acc[p as usize]
    }
}

/// CPU-visible GPU fence waits since the last frame.
///
/// Why this is counted and not just timed. The effects pass used to end every
/// blur pass and every composite round trip with `Renderer::wait`, which costs
/// an `eglCreateSync`, an `eglWaitSync` and two `eglMakeCurrent` calls. Those
/// waits bought nothing -- the next pass reads what the last one wrote, on the
/// SAME GL context, and GL orders that for us -- and they dominated the frame:
/// removing them took a still 1856x1160 scale-1.45 frame from 47-55 ms to
/// 5.2 ms on this machine. A COUNT survives a loaded GPU in a way a millisecond
/// threshold does not, so `waits` is the number a regression test should read.
static WAITS: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);

/// One CPU-visible fence wait happened (called by `fx::wait`).
pub fn note_wait() {
    WAITS.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
}

/// How many have happened since the last call, and start counting again.
pub fn take_waits() -> u32 {
    WAITS.swap(0, std::sync::atomic::Ordering::Relaxed)
}

/// Where `--frame-stats` blocks the CPU on the GPU.
///
/// `phase` (the default) fences at every phase end: the phase columns are
/// GPU-inclusive, and the serialisation makes every column, and the total, an
/// UPPER BOUND on a normal run. `frame` fences exactly once, after the last
/// draw of the frame, which is what the winit backend already does before it
/// presents: `total_ms` is then the honest cost of the frame and the phase
/// columns are submission time only.
pub fn phase_fence() -> bool {
    static F: std::sync::OnceLock<bool> = std::sync::OnceLock::new();
    *F.get_or_init(|| !matches!(std::env::var("AURORA_STATS_FENCE").as_deref(), Ok("frame")))
}

/// One rendered frame.
#[derive(Clone, Copy, Debug, Default)]
pub struct Sample {
    pub total: f64,
    pub effects: f64,
    pub scene: f64,
    pub prepass: f64,
    pub chain: f64,
    pub composite: f64,
    pub switcher: f64,
    pub blit: f64,
    pub overlay: f64,
    pub surfaces: u32,
    pub prepasses: u32,
    /// Whether the effects pipeline (not the flat renderer) drew this frame.
    pub fx: bool,
    /// CPU-visible GPU fence waits this frame (see `WAITS`).
    pub waits: u32,
}

const COLUMNS: &str = "frame,total_ms,effects_ms,scene_ms,prepass_ms,chain_ms,composite_ms,switcher_ms,blit_ms,overlay_ms,surfaces,prepasses,fx,waits";

/// Samples kept in memory for the summary (the file keeps every frame).
const KEEP: usize = 200_000;

pub struct Stats {
    file: File,
    pub samples: Vec<Sample>,
    next_log: usize,
}

impl Stats {
    pub fn create(path: &Path) -> std::io::Result<Stats> {
        let mut file = File::create(path)?;
        let fence = if phase_fence() {
            "the CPU blocks on a fence at each phase end, so every column is GPU-inclusive and an upper bound"
        } else {
            "AURORA_STATS_FENCE=frame: one fence, after the frame's last draw, so total_ms is the honest frame cost and the phase columns are submission only"
        };
        writeln!(file, "# aurora-compositor frame-stats v1; times in ms; {fence}")?;
        writeln!(file, "{COLUMNS}")?;
        Ok(Stats { file, samples: vec![], next_log: 300 })
    }

    pub fn push(&mut self, frame: u64, s: Sample) {
        // One unbuffered write per frame: a killed compositor loses nothing.
        let _ = writeln!(
            self.file,
            "{frame},{:.3},{:.3},{:.3},{:.3},{:.3},{:.3},{:.3},{:.3},{:.3},{},{},{},{}",
            s.total, s.effects, s.scene, s.prepass, s.chain, s.composite, s.switcher, s.blit, s.overlay, s.surfaces, s.prepasses, s.fx as u8, s.waits
        );
        if self.samples.len() >= KEEP {
            self.samples.drain(..KEEP / 2);
        }
        self.samples.push(s);
        if self.samples.len() >= self.next_log {
            self.next_log = self.samples.len() + 300;
            tracing::info!("frame-stats: {}", self.summary(0));
        }
    }

    /// One line, over the most recent `last` frames (0 = all): the pipeline's
    /// frames only for `effects`, every frame for `total`.
    pub fn summary(&self, last: usize) -> String {
        let all = if last == 0 || last >= self.samples.len() { &self.samples[..] } else { &self.samples[self.samples.len() - last..] };
        let fx: Vec<&Sample> = all.iter().filter(|s| s.fx).collect();
        let pick = |v: &[&Sample], f: fn(&Sample) -> f64| -> Dist { Dist::of(v.iter().map(|s| f(s)).collect()) };
        let every: Vec<&Sample> = all.iter().collect();
        format!(
            "frames={} fx_frames={} | total {} | effects {} | prepass {} | chain {} | composite {} | waits {} | surfaces {}",
            all.len(), fx.len(), pick(&every, |s| s.total), pick(&fx, |s| s.effects), pick(&fx, |s| s.prepass), pick(&fx, |s| s.chain), pick(&fx, |s| s.composite),
            pick(&every, |s| s.waits as f64), pick(&fx, |s| s.surfaces as f64)
        )
    }
}

/// Percentiles (nearest rank), mean, standard deviation and max of a set of times in ms.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Dist {
    pub n: usize,
    pub mean: f64,
    pub sd: f64,
    pub p50: f64,
    pub p95: f64,
    pub p99: f64,
    pub max: f64,
}

impl Dist {
    pub fn of(mut v: Vec<f64>) -> Dist {
        if v.is_empty() {
            return Dist { n: 0, mean: 0.0, sd: 0.0, p50: 0.0, p95: 0.0, p99: 0.0, max: 0.0 };
        }
        v.sort_by(|a, b| a.total_cmp(b));
        let n = v.len();
        let mean = v.iter().sum::<f64>() / n as f64;
        let sd = (v.iter().map(|x| (x - mean).powi(2)).sum::<f64>() / n as f64).sqrt();
        Dist { n, mean, sd, p50: rank(&v, 0.50), p95: rank(&v, 0.95), p99: rank(&v, 0.99), max: v[n - 1] }
    }
}

/// Nearest-rank percentile of a sorted, non-empty slice: the smallest value with at least `q` of the data at or below it.
fn rank(sorted: &[f64], q: f64) -> f64 {
    let k = ((q * sorted.len() as f64).ceil() as usize).clamp(1, sorted.len());
    sorted[k - 1]
}

impl std::fmt::Display for Dist {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "p50={:.3} p95={:.3} p99={:.3} max={:.3} mean={:.3} sd={:.3}", self.p50, self.p95, self.p99, self.max, self.mean, self.sd)
    }
}

pub fn ms(d: Duration) -> f64 {
    d.as_secs_f64() * 1e3
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn nearest_rank_percentiles_on_a_known_set() {
        let d = Dist::of((1..=100).map(f64::from).collect());
        assert_eq!((d.p50, d.p95, d.p99, d.max, d.n), (50.0, 95.0, 99.0, 100.0, 100));
        assert!((d.mean - 50.5).abs() < 1e-9);
        assert!((d.sd - 28.866).abs() < 1e-2, "{}", d.sd);
    }

    #[test]
    fn one_slow_frame_moves_the_tail_and_the_sd_but_not_the_median() {
        let mut v = vec![2.0; 99];
        v.push(40.0);
        let d = Dist::of(v);
        assert_eq!((d.p50, d.p95, d.p99, d.max), (2.0, 2.0, 2.0, 40.0));
        assert!(d.sd > 3.0, "{}", d.sd);
    }

    #[test]
    fn empty_and_single_sample_do_not_panic() {
        assert_eq!(Dist::of(vec![]).n, 0);
        let d = Dist::of(vec![3.0]);
        assert_eq!((d.p50, d.p99, d.max, d.sd), (3.0, 3.0, 3.0, 0.0));
    }

    #[test]
    fn disabled_phases_do_nothing() {
        let mut p = Phases::default();
        p.begin();
        p.lap_cpu(Phase::Scene);
        assert_eq!(p.ms(Phase::Scene), 0.0);
    }
}
