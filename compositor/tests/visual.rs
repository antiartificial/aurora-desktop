//! The visual-system work of decision 0010 (docs/visual-system.md section 10):
//! the instrumentation (`--frame-stats`, `--test-input frame N`, the `Img`
//! helpers), and criteria checked on decoded capture pixels.

mod common;
use common::*;

fn rgb_img(w: usize, px: &[[u8; 3]]) -> Img { Img { w, h: px.len() / w, px: px.iter().flatten().copied().collect() } }

#[test]
fn img_helpers_match_known_values() {
    // black, white, #767676 (the WCAG 4.5:1 reference grey on white), then a run pattern
    let img = rgb_img(4, &[[0, 0, 0], [255, 255, 255], [0x76, 0x76, 0x76], [255, 255, 255]]);
    assert!((img.luminance(1, 0) - 1.0).abs() < 1e-12);
    assert_eq!(img.luminance(0, 0), 0.0);
    assert!((img.contrast((0, 0), (1, 0)) - 21.0).abs() < 1e-9, "black on white is 21:1");
    let c = img.contrast((2, 0), (1, 0));
    assert!((c - 4.54).abs() < 0.01, "#767676 on white is 4.54:1, got {c}");
    assert_eq!(img.contrast((1, 0), (2, 0)), c, "symmetric");
    // mid grey 128: sRGB decode 0.2159; WCAG luminance of (128,128,128) is 0.2159
    let g = rgb_img(1, &[[128, 128, 128]]);
    assert!((g.luminance(0, 0) - 0.2159).abs() < 1e-3);
    // runs are per channel
    let r = rgb_img(6, &[[1, 9, 5], [1, 9, 6], [1, 8, 7], [2, 8, 7], [2, 8, 7], [2, 8, 7]]);
    assert_eq!(r.max_run((0, 0), (5, 0)), [3, 4, 4]);
    assert_eq!(r.max_run((5, 0), (0, 0)), [3, 4, 4], "direction does not matter");
    let col = rgb_img(1, &[[7, 7, 7], [7, 7, 7], [7, 8, 7], [7, 8, 7]]);
    assert_eq!(col.max_run((0, 0), (0, 3)), [4, 2, 4], "vertical scan");
}

fn stats_comp(extra: &[&str]) -> (Comp, std::path::PathBuf, tempfile::TempDir) {
    let d = tempfile::tempdir().unwrap();
    let path = d.path().join("stats.csv");
    let mut args = vec!["--frame-stats", path.to_str().unwrap(), "--freeze-time"];
    args.extend_from_slice(extra);
    (Comp::start_shell(&args), path, d)
}

#[test]
fn frame_stats_records_every_frame_and_frame_n_steps_deterministically() {
    let (mut c, path, _d) = stats_comp(&[]);
    demo_scene(&mut c);
    let before = std::fs::read_to_string(&path).unwrap().lines().filter(|l| !l.starts_with('#') && !l.starts_with("frame,")).count();
    c.step_frames(60);
    let after = std::fs::read_to_string(&path).unwrap().lines().filter(|l| !l.starts_with('#') && !l.starts_with("frame,")).count();
    assert!(after >= before + 60, "`frame 60` returned after only {} frames were recorded", after - before);
    assert!(after <= before + 70, "`frame 60` overshot: {} frames", after - before);
    let s = c.stats(60);
    println!("FRAME-STATS demo scene, last 60 frames: {s}");
    let (p50, p95, p99, max) = (stat(&s, "total", "p50"), stat(&s, "total", "p95"), stat(&s, "total", "p99"), stat(&s, "total", "max"));
    assert!(0.0 < p50 && p50 <= p95 && p95 <= p99 && p99 <= max, "percentiles out of order: {s}");
    let e50 = stat(&s, "effects", "p50");
    assert!(e50 > 0.0 && e50 <= p50 + 1e-9, "the effects pass is part of the frame: {s}");
    // the file is a CSV a script can read: header, then one row per frame with the documented columns
    let text = std::fs::read_to_string(&path).unwrap();
    let hdr = text.lines().find(|l| l.starts_with("frame,")).unwrap();
    let cols = hdr.split(',').count();
    assert!(text.lines().filter(|l| !l.starts_with('#') && !l.starts_with("frame,")).all(|l| l.split(',').count() == cols));
    // frame numbers are consecutive
    let nums: Vec<u64> = text.lines().filter(|l| !l.starts_with('#') && !l.starts_with("frame,")).map(|l| l.split(',').next().unwrap().parse().unwrap()).collect();
    assert!(nums.windows(2).all(|w| w[1] == w[0] + 1), "frame numbers skip");
}

#[test]
fn stats_without_the_flag_and_a_bad_frame_count_are_refused() {
    let c = Comp::start(&[]);
    assert_eq!(c.inject_reply("stats"), "err no --frame-stats");
    assert!(c.inject_reply("frame -3").starts_with("err"), "negative count");
    assert!(c.inject_reply("frame x").starts_with("err"));
    assert_eq!(c.inject_reply("frame 0"), "ok");
}

/// C7 measurements, printed. Not a pass/fail gate: the numbers depend on a GPU that
/// other things share. Run with `--ignored --nocapture`.
///
/// `n` glass surfaces (client windows with the dock off, so n is exact) on a 1856x1160 output
/// at scale 1.45, either tiled so no two overlap or cascaded so each overlaps the last.
fn budget_run(n: usize, tiled: bool) -> String {
    let d = tempfile::tempdir().unwrap();
    let path = d.path().join("stats.csv");
    let t = Theme::new();
    t.append("[dock]\nenabled = false\n[desktop]\nclock = false\nhostname = false");
    // AURORA_BUDGET_SIZE=WxH and AURORA_BUDGET_SCALE=F override the output (320x200 at 1.0 shows the per-pass cost floor).
    let size = format!("--headless={}", std::env::var("AURORA_BUDGET_SIZE").unwrap_or("1856x1160".into()));
    let scale = std::env::var("AURORA_BUDGET_SCALE").unwrap_or("1.45".into());
    let mut c = Comp::start_shell(&[&size, "--scale", &scale, "--frame-stats", path.to_str().unwrap(), "--freeze-time", "--theme", t.path()]);
    for i in 0..n {
        c.client_args(&["--size", "250x230", "--pattern", "glass", "--rgb", "255,255,255", "--alpha", "0.14"]);
        // Tiled windows are further apart than any blur reaches, so they can share one prepass.
        let (x, y) = if tiled { (20 + (i % 4) * 315, 20 + (i / 4) * 300) } else { (20 + i * 60, 20 + i * 45) };
        c.inject(&format!("move {i} {x} {y}"));
    }
    c.step_frames(40);
    c.step_frames(300);
    c.stats(300)
}

#[test]
#[ignore = "measurement: run with --ignored --nocapture"]
fn frame_budget_report() {
    for tiled in [true, false] {
        for n in [1usize, 2, 4, 8] {
            let s = budget_run(n, tiled);
            println!("BUDGET {} n={n}: {s}", if tiled { "tiled  " } else { "stacked" });
        }
    }
}

// ---- item 2: the chrome glass luminance ceiling (docs/visual-system.md 3.3, criteria C2.x) ----------

use std::{io::{BufRead, BufReader, Write}, os::unix::net::UnixListener, path::{Path, PathBuf}, time::Duration};

/// A bus that answers `registry.apps@v1`, so the dock has labels to draw.
fn fake_registry(dir: &Path, apps: &[(&str, &str)]) -> PathBuf {
    let path = dir.join("bus.sock");
    let l = UnixListener::bind(&path).unwrap();
    let apps: Vec<serde_json::Value> = apps.iter().map(|(id, n)| serde_json::json!({"id": id, "name": n, "version": "0.1.0", "verbs": ["resize@v1"]})).collect();
    std::thread::spawn(move || {
        for s in l.incoming().flatten() {
            let apps = apps.clone();
            std::thread::spawn(move || {
                let mut line = String::new();
                let _ = BufReader::new(&s).read_line(&mut line);
                let Ok(req) = serde_json::from_str::<serde_json::Value>(&line) else { return };
                if (req["to"].as_str(), req["verb"].as_str()) != (Some("registry"), Some("registry.apps@v1")) { return; }
                let reply = serde_json::json!({"id": "s1", "kind": "response", "corr": req["id"], "verb": "registry.apps@v1", "payload": {"apps": apps}});
                let _ = writeln!(&s, "{reply}");
            });
        }
    });
    path
}

/// What is behind the dock.
#[derive(Clone, Copy, Debug)]
enum Behind {
    /// The desktop itself, solid (Oklab L 1.0 = white): the case measured when the defect was found.
    DesktopWhite,
    /// An opaque client filling the dock strip.
    Client(&'static str, &'static str),
}

/// What the sweep measures for one backdrop.
#[derive(Debug, Clone)]
struct Reading {
    behind: String,
    /// WCAG contrast of each label's lightest pixel against the brightest glass pixel directly above it.
    label_contrast: Vec<f64>,
    /// Maximum luminance of any glass pixel in the panel's padding strips.
    glass_max: f64,
    glass_mean: f64,
    /// sRGB of the glass in the middle of the top padding strip.
    glass_rgb: [u8; 3],
    /// Red channel of the glass at the left and right ends of the strip (for backdrops that differ across the dock).
    ends: (u8, u8),
    panel: [i32; 4],
}

/// A capture of the dock over `behind`: the image, the panel rectangle and the item rectangles (device px).
fn dock_capture(behind: Behind, extra_theme: &str) -> (Img, String, [i32; 4], Vec<[i32; 4]>) {
    let t = Theme::new();
    let bg = match behind {
        Behind::DesktopWhite => "[background]\nmode = \"solid\"\ntop = [1.0, 0.0, 0.0]\nbottom = [1.0, 0.0, 0.0]\n",
        Behind::Client(..) => "[background]\nmode = \"solid\"\ntop = [0.30, 0.0, 0.0]\nbottom = [0.30, 0.0, 0.0]\n",
    };
    t.append(&format!("{bg}[desktop]\nclock = false\nhostname = false\n{extra_theme}"));
    let d = tempfile::tempdir().unwrap();
    let bus = fake_registry(d.path(), &[("org.aurora.lumen", "Lumen"), ("org.aurora.ledger", "Haiku Ledger")]);
    let mut c = Comp::start_shell(&["--theme", t.path(), "--bus-socket", bus.to_str().unwrap(), "--freeze-time"]);
    wait_for("the dock to learn its apps", || dock_layout(&c.log()).is_some_and(|l| !l.3.is_empty()));
    std::thread::sleep(Duration::from_millis(300));
    let (_, panel, _, items) = dock_layout(&c.log()).unwrap();
    let label = match behind {
        Behind::DesktopWhite => "desktop white".to_string(),
        Behind::Client(pat, rgb) => {
            c.client_args(&["--size", "1280x300", "--pattern", pat, "--rgb", rgb]);
            c.inject("move 0 0 560");
            format!("client {pat} {rgb}")
        }
    };
    c.step_frames(4);
    (c.capture("dock.png"), label, panel, items)
}

fn sweep_one(behind: Behind, extra_theme: &str) -> Reading {
    let (img, label, panel, items) = dock_capture(behind, extra_theme);
    let (px, py, pw, ph) = (panel[0] as usize, panel[1] as usize, panel[2] as usize, panel[3] as usize);
    // Glass: the padding strips above and below the pills, clear of the corners and the label rows.
    let strips = [(py + 3)..(py + 6), (py + ph - 6)..(py + ph - 3)];
    let glass: Vec<(usize, usize)> = strips.iter().flat_map(|ys| ys.clone().flat_map(|y| ((px + 30)..(px + pw - 30)).map(move |x| (x, y)))).collect();
    let lums: Vec<f64> = glass.iter().map(|&(x, y)| img.luminance(x, y)).collect();
    let mut label_contrast = vec![];
    for it in &items {
        // The label is the part of the pill right of the tile (a square of the pill's height).
        let (x0, x1) = ((it[0] + it[3] + 2) as usize, (it[0] + it[2]) as usize);
        let (y0, y1) = (it[1] as usize, (it[1] + it[3]) as usize);
        let ink = (y0..y1).flat_map(|y| (x0..x1).map(move |x| (x, y))).max_by(|a, b| img.luminance(a.0, a.1).total_cmp(&img.luminance(b.0, b.1))).unwrap();
        // The glass straight above the label: the brightest padding-strip pixel over its x range.
        let above = strips[0].clone().flat_map(|y| (x0..x1).map(move |x| (x, y))).max_by(|a, b| img.luminance(a.0, a.1).total_cmp(&img.luminance(b.0, b.1))).unwrap();
        label_contrast.push(img.contrast(ink, above));
    }
    let mid = img.at(px + pw / 2, py + 4);
    let end = |x: usize| img.at(x, py + 4)[0];
    Reading {
        behind: label,
        label_contrast,
        glass_max: lums.iter().cloned().fold(0.0, f64::max),
        glass_mean: lums.iter().sum::<f64>() / lums.len() as f64,
        glass_rgb: mid,
        ends: (end(px + 40), end(px + pw - 40)),
        panel,
    }
}

/// 10-90% width in pixels of a rising edge sampled in `lum`, interpolating the two crossings.
fn width_10_90(lum: &[f64]) -> f64 {
    let (lo, hi) = (lum[0], *lum.last().unwrap());
    let cross = |level: f64| { let i = lum.windows(2).position(|w| w[0] <= level && w[1] > level).expect("no crossing"); i as f64 + (level - lum[i]) / (lum[i + 1] - lum[i]) };
    cross(lo + 0.9 * (hi - lo)) - cross(lo + 0.1 * (hi - lo))
}

#[test]
fn chrome_glass_blur_has_the_width_the_theme_identity_predicts() {
    // C1.3 for chrome: sigma ~ 0.75 r 2^passes 2^level, 10-90% width = 1.9 r 2^passes 2^level = 1.9*2*4*4 = 60.8 px at scale 1.
    // The ceiling and the tint are switched off so the glass is the blur alone, in linear light.
    let off = "[glass.chrome]\nluminance_max = 0.0\ntint = { oklab = [0.38, -0.005, -0.020], strength = 0.0 }\n";
    let (img, _, panel, _) = dock_capture(Behind::Client("split", "0,0,0"), off);
    let (px, py, pw) = (panel[0] as usize, panel[1] as usize, panel[2] as usize);
    let lum: Vec<f64> = (px + 20..px + pw - 20).map(|x| img.luminance(x, py + 4)).collect();
    let w = width_10_90(&lum);
    println!("CHROME BLUR 10-90% luminance width over a black|white split: {w:.1} px (identity predicts 60.8, accept 52-70)");
    assert!((52.0..=70.0).contains(&w), "chrome blur width {w:.1} px is outside the identity's 60.8 +-15%");
}

const BACKDROPS: [Behind; 6] = [
    Behind::DesktopWhite,
    Behind::Client("solid", "255,255,255"),
    Behind::Client("solid", "0,0,0"),
    Behind::Client("solid", "128,128,128"),
    Behind::Client("fine", "0,0,0"),
    Behind::Client("split", "0,0,0"),
];

fn sweep(extra_theme: &str) -> Vec<Reading> {
    let rows: Vec<Reading> = BACKDROPS.iter().map(|b| sweep_one(*b, extra_theme)).collect();
    for r in &rows {
        println!(
            "DOCK LABEL over {:<24} panel {:?}: label contrast {:?}  glass sRGB {:?}  glass luminance max {:.4} mean {:.4}  ends {:?}",
            r.behind, r.panel, r.label_contrast.iter().map(|c| format!("{c:.2}")).collect::<Vec<_>>(), r.glass_rgb, r.glass_max, r.glass_mean, r.ends
        );
    }
    rows
}

/// WCAG 2 body-text floor, and the ceiling the glass is allowed (docs/visual-system.md C2.1, C2.2).
const FLOOR: f64 = 4.5;
const GLASS_Y_MAX: f64 = 0.19;

#[test]
fn chrome_glass_keeps_the_dock_label_legible_over_every_backdrop() {
    let rows = sweep("");
    for r in &rows {
        for (i, c) in r.label_contrast.iter().enumerate() {
            assert!(*c >= FLOOR, "C2.1: label {i} over {} has contrast {c:.2}:1, floor {FLOOR}:1 (glass sRGB {:?})", r.behind, r.glass_rgb);
        }
        assert!(r.glass_max <= GLASS_Y_MAX, "C2.2: glass over {} reaches luminance {:.4}, ceiling {GLASS_Y_MAX}", r.behind, r.glass_max);
    }
    // C2.4: the ceiling is a compression, not the tint doing all the work: a white backdrop still
    // lifts the glass above what black does.
    let white = rows.iter().find(|r| r.behind == "client solid 255,255,255").unwrap();
    let black = rows.iter().find(|r| r.behind == "client solid 0,0,0").unwrap();
    assert!(white.glass_mean >= 0.06, "C2.4: glass over white has mean luminance {:.4}", white.glass_mean);
    assert!(white.glass_mean > black.glass_mean * 1.5, "glass over white {:.4} is not visibly above glass over black {:.4}", white.glass_mean, black.glass_mean);
    // C2.3: it still varies with the backdrop (not a clamp): across a black|white split the glass differs by far more than 4 codes.
    let split = rows.iter().find(|r| r.behind == "client split 0,0,0").unwrap();
    assert!(split.ends.1 as i32 - split.ends.0 as i32 >= 4, "C2.3: glass across a black|white split is flat: {:?}", split.ends);
}

/// The control: the same sweep with the ceiling removed must violate the floor over white. If this
/// stops failing the floor, the assertion above has stopped measuring the ceiling.
#[test]
fn without_the_ceiling_the_same_sweep_breaks_the_floor() {
    let rows = sweep("[glass.chrome]\nluminance_max = 0.0\ntint = { oklab = [0.38, -0.005, -0.020], strength = 0.45 }\n");
    let white = rows.iter().find(|r| r.behind == "client solid 255,255,255").unwrap();
    let worst = white.label_contrast.iter().cloned().fold(f64::MAX, f64::min);
    assert!(worst < FLOOR, "with luminance_max = 0 the label over white still reads {worst:.2}:1; the sweep cannot see the ceiling");
    assert!(white.glass_max > GLASS_Y_MAX, "ceiling off: glass luminance {:.4} should exceed {GLASS_Y_MAX}", white.glass_max);
}

// ---- window decorations: the title text meets the same floor as the dock's labels ----------------------------

/// Contrast of a window title's lightest pixel against the brightest glass directly above it in the bar, over
/// `behind`, for a FOCUSED title and an UNFOCUSED one (two probe windows in the same run, one created after the other).
/// The glass sample strip is the bar's top rows (clear of the corners), as the dock's is its padding.
fn title_contrast(behind: Behind, extra_theme: &str) -> (String, f64, f64) {
    let t = Theme::new();
    let bg = match behind {
        Behind::DesktopWhite => "[background]\nmode = \"solid\"\ntop = [1.0, 0.0, 0.0]\nbottom = [1.0, 0.0, 0.0]\n",
        Behind::Client(..) => "[background]\nmode = \"solid\"\ntop = [0.30, 0.0, 0.0]\nbottom = [0.30, 0.0, 0.0]\n",
    };
    t.append(&format!("{bg}[desktop]\nclock = false\nhostname = false\n{extra_theme}"));
    let mut c = Comp::start_shell(&["--theme", t.path(), "--freeze-time"]);
    let label = match behind {
        Behind::DesktopWhite => "desktop white".to_string(),
        Behind::Client(pat, rgb) => {
            c.client_args(&["--size", "1280x700", "--pattern", pat, "--rgb", rgb, "--title", "Backdrop"]);
            c.inject("move 0 0 0");
            format!("client {pat} {rgb}")
        }
    };
    let skip = if matches!(behind, Behind::Client(..)) { 1 } else { 0 };
    c.client_args(&["--size", "420x200", "--pattern", "solid", "--rgb", "120,120,120", "--title", "Unfocused probe title"]);
    c.inject(&format!("move {} 100 200", skip));
    c.client_args(&["--size", "420x200", "--pattern", "solid", "--rgb", "120,120,120", "--title", "Focused probe title"]);
    c.inject(&format!("move {} 600 200", skip + 1));
    wait_for("both probe bars", || ["Unfocused probe title", "Focused probe title"].iter().all(|t| decos(&c.log()).iter().any(|d| d.text == *t)));
    std::thread::sleep(Duration::from_millis(500));
    c.step_frames(4);
    let img = c.capture("title.png");
    let read = |text: &str| {
        let d = decos(&c.log()).into_iter().rev().find(|d| d.text == text).unwrap();
        // The log is written when a bar is drawn, and the `move` came after the first draw: find the bar where it is now.
        let (x0, x1) = ((d.title[0]) as usize, (d.title[0] + d.title[2]) as usize);
        let (y0, y1) = ((d.bar[1] + 3) as usize, (d.bar[1] + d.bar[3] - 6) as usize);
        let ink = (y0..y1).flat_map(|y| (x0..x1).map(move |x| (x, y))).max_by(|a, b| img.luminance(a.0, a.1).total_cmp(&img.luminance(b.0, b.1))).unwrap();
        let above = (d.bar[1] as usize + 3..d.bar[1] as usize + 6).flat_map(|y| (x0..x1).map(move |x| (x, y))).max_by(|a, b| img.luminance(a.0, a.1).total_cmp(&img.luminance(b.0, b.1))).unwrap();
        (img.contrast(ink, above), d.focused)
    };
    let (unfocused, uf) = read("Unfocused probe title");
    let (focused, ff) = read("Focused probe title");
    assert!(!uf && ff, "the probes are in the focus states the sweep says they are ({uf}, {ff})");
    (label, focused, unfocused)
}

#[test]
fn window_titles_keep_the_contrast_floor_over_every_backdrop_focused_or_not() {
    for b in BACKDROPS {
        let (label, focused, unfocused) = title_contrast(b, "");
        println!("TITLE over {label:<24}: focused {focused:.2}:1, unfocused {unfocused:.2}:1");
        assert!(focused >= FLOOR, "the focused title over {label} has contrast {focused:.2}:1, floor {FLOOR}:1");
        assert!(unfocused >= FLOOR, "the unfocused title over {label} has contrast {unfocused:.2}:1, floor {FLOOR}:1");
    }
}

/// The control for the sweep above: remove the glass's luminance ceiling and the same measurement over white must
/// fall under the floor. If it stops doing so, the assertion above has stopped measuring the ceiling.
#[test]
fn without_the_ceiling_the_title_sweep_breaks_the_floor_too() {
    let (_, focused, unfocused) = title_contrast(Behind::Client("solid", "255,255,255"), "[glass.chrome]\nluminance_max = 0.0\ntint = { oklab = [0.38, -0.005, -0.020], strength = 0.45 }\n");
    println!("TITLE without the ceiling: focused {focused:.2}:1, unfocused {unfocused:.2}:1");
    assert!(focused.min(unfocused) < FLOOR, "with luminance_max = 0 the titles over white still read {focused:.2} / {unfocused:.2}; the sweep cannot see the ceiling");
}

// ---- item 3: the shared prepass --------------------------------------------------------------------

/// (surfaces, prepasses) of the last frame in a `--frame-stats` file.
fn last_counts(path: &Path) -> (u32, u32) {
    let text = std::fs::read_to_string(path).unwrap();
    let row = text.lines().rev().find(|l| !l.starts_with('#') && !l.starts_with("frame,")).unwrap();
    let f: Vec<&str> = row.split(',').collect();
    (f[10].parse().unwrap(), f[11].parse().unwrap())
}

/// A scene of opaque patterned and translucent windows at the given positions, the dock up; its frame and counts.
/// The counts are of WINDOW glass: the bars the compositor draws above windows are chrome glass of their own,
/// switched off here (`bars_scene` below runs the same comparison with them on).
fn prepass_scene(share: bool, spots: &[(i32, i32)]) -> (Img, (u32, u32)) {
    prepass_scene_with(share, spots, "[decoration]\nenabled = false\n")
}

fn prepass_scene_with(share: bool, spots: &[(i32, i32)], decoration: &str) -> (Img, (u32, u32)) {
    let t = Theme::new();
    t.append("[background]\nmode = \"gradient\"\ntop = [0.60, 0.0, 0.0]\nbottom = [0.25, 0.05, -0.05]\n[desktop]\nclock = false\nhostname = false\n");
    t.append(decoration);
    if !share { t.edit("share_prepass = true", "share_prepass = false"); }
    let d = tempfile::tempdir().unwrap();
    let path = d.path().join("s.csv");
    let bus = fake_registry(d.path(), &[("org.aurora.lumen", "Lumen")]);
    let mut c = Comp::start_shell(&["--theme", t.path(), "--bus-socket", bus.to_str().unwrap(), "--freeze-time", "--frame-stats", path.to_str().unwrap()]);
    let looks: [&[&str]; 4] = [&["checker"], &["bars"], &["split"], &["glass", "--rgb", "255,255,255", "--alpha", "0.30"]];
    for (i, &(x, y)) in spots.iter().enumerate() {
        let mut a = vec!["--size", "250x230", "--pattern"];
        a.extend_from_slice(looks[i % 4]);
        c.client_args(&a);
        c.inject(&format!("move {i} {x} {y}"));
    }
    wait_for("the dock", || dock_layout(&c.log()).is_some());
    c.step_frames(6);
    let img = c.capture("scene.png");
    c.step_frames(2);
    (img, last_counts(&path))
}

#[test]
fn sharing_the_prepass_changes_no_pixel_and_costs_one_prepass_for_surfaces_that_do_not_touch() {
    // Four windows further apart than the blur reaches, plus the dock: five glass surfaces.
    let apart = [(20, 20), (335, 20), (20, 320), (335, 320)];
    let (shared, (surfaces, prepasses)) = prepass_scene(true, &apart);
    let (each, (_, prepasses_each)) = prepass_scene(false, &apart);
    println!("PREPASS tiled: {surfaces} glass surfaces, {prepasses} prepass(es) shared vs {prepasses_each} unshared; frames differ in {} px", shared.diff_count(&each));
    assert_eq!(surfaces, 5);
    assert_eq!(prepasses, 1, "windows that do not touch each other must share one prepass");
    assert_eq!(prepasses_each, 5, "the oracle (share_prepass = false) redoes it for each surface");
    assert_eq!(shared.diff_count(&each), 0, "sharing the prepass changed pixels: the reach used to decide 'does not touch' is too short");
}

#[test]
fn sharing_the_prepass_changes_no_pixel_with_title_bars_either() {
    // The same comparison with the bars on (and their ease off, so two runs cannot differ by where a fade is):
    // each bar is a chrome surface drawn between its window's glass and its client, and none of them may read a stale prepass.
    let apart = [(20, 60), (335, 60), (20, 340), (335, 340)];
    let bars = "[decoration]\nease_ms = 0\n";
    let (shared, (surfaces, prepasses)) = prepass_scene_with(true, &apart, bars);
    let (each, (_, prepasses_each)) = prepass_scene_with(false, &apart, bars);
    println!("PREPASS with bars: {surfaces} glass surfaces, {prepasses} prepass(es) shared vs {prepasses_each} unshared; frames differ in {} px", shared.diff_count(&each));
    assert_eq!(surfaces, 9, "four windows, four bars and the dock");
    assert!(prepasses < prepasses_each, "sharing saves prepasses even with bars ({prepasses} vs {prepasses_each})");
    assert_eq!(shared.diff_count(&each), 0, "sharing the prepass changed pixels once bars were drawn");
}

#[test]
fn glass_over_glass_and_the_dock_over_a_window_still_see_what_is_beneath_them() {
    // Cascaded so each window overlaps the last, and the last reaches down onto the dock strip.
    let cascade = [(20, 20), (80, 100), (140, 180), (330, 440)];
    let (shared, (_, prepasses)) = prepass_scene(true, &cascade);
    let (each, (_, prepasses_each)) = prepass_scene(false, &cascade);
    println!("PREPASS cascaded: {prepasses} prepasses shared vs {prepasses_each} unshared; frames differ in {} px", shared.diff_count(&each));
    assert_eq!(prepasses, prepasses_each, "every surface here touches something drawn before it, so none may reuse a stale prepass");
    assert_eq!(shared.diff_count(&each), 0);
}

#[test]
fn stale_prepass_would_be_visible_the_dock_blurs_the_window_beneath_it_not_the_desktop() {
    // Black dock strip window versus no window: the dock glass must differ (it blurs what is under it).
    let with = sweep_one(Behind::Client("solid", "255,255,255"), "");
    let without = sweep_one(Behind::DesktopWhite, "");
    let black = sweep_one(Behind::Client("solid", "0,0,0"), "");
    assert!(with.glass_mean > black.glass_mean * 1.5, "the dock did not see the window under it: {:?} vs {:?}", with.glass_mean, black.glass_mean);
    assert!((with.glass_mean - without.glass_mean).abs() < 0.01, "a white window and a white desktop should give the same glass");
}
