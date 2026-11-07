//! The Deskbar (decision 0021): real headless compositors, real Wayland clients, and a fake busd that speaks
//! the real `bus.subscribe@v1` / `bus.log@v1` shapes and the proxy's `net.conn` / `net.summary` events
//! (the real ones: demos/network-grants/run.sh). Judged on decoded pixels of the compositor's own capture
//! and on the `deskbar: layout` log line, whose rows carry the words that were rasterised. Everything with a
//! position runs at scale 1 and at 1.45.
//!
//! The honesty rule is tested here as behaviour: a window that is not in a domain gets no figure, and the one
//! test that CAN put a client in a real domain (a cgroup under the user's delegated tree) asserts that the
//! numbers appear and are read from the cgroup, and skips loudly when this machine will not allow it.

mod common;
use common::*;
use std::{
    io::{BufRead, BufReader, Write},
    os::unix::{net::{UnixListener, UnixStream}, process::CommandExt},
    path::{Path, PathBuf},
    process::{Child, Command},
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

const SCALES: [&str; 2] = ["1", "1.45"];
const BASE: &str = "[background]\nmode = \"gradient\"\ntop = [0.34, 0.0, 0.0]\nbottom = [0.18, 0.0, 0.0]\n[desktop]\nclock = false\nhostname = false\n";
const DOT: char = '\u{b7}';

fn themed(scale: &str, extra_theme: &str, extra: &[&str]) -> (Theme, Comp) {
    let t = Theme::new();
    t.append(BASE);
    t.append(&format!("[deskbar]\nenabled = true\n{extra_theme}"));
    let mut a = vec!["--theme", t.path(), "--freeze-time", "--scale", scale];
    a.extend_from_slice(extra);
    let c = Comp::start_shell(&a);
    std::thread::sleep(Duration::from_millis(300));
    (t, c)
}

#[derive(Clone, Debug)]
struct RowL {
    kind: String,
    /// Absolute device pixels.
    rect: [i32; 4],
    text: String,
    meta: String,
}

#[derive(Clone, Debug)]
struct Bar {
    scale: f64,
    rect: [i32; 4],
    rows: Vec<RowL>,
}

impl Bar {
    fn apps(&self) -> Vec<&RowL> { self.rows.iter().filter(|r| r.kind == "app").collect() }
    fn wins(&self) -> Vec<&RowL> { self.rows.iter().filter(|r| r.kind == "win").collect() }
    fn app(&self, label: &str) -> &RowL { self.rows.iter().find(|r| r.kind == "app" && r.text == label).unwrap_or_else(|| panic!("no app row {label:?} in {self:#?}")) }
    fn win(&self, title: &str) -> &RowL { self.rows.iter().find(|r| r.kind == "win" && r.text == title).unwrap_or_else(|| panic!("no window row {title:?} in {self:#?}")) }
}

fn nums<const N: usize>(s: &str) -> [i32; N] {
    let v: Vec<i32> = s.split(',').filter_map(|n| n.parse().ok()).collect();
    std::array::from_fn(|i| v[i])
}

fn bar(c: &Comp) -> Option<Bar> {
    let log = c.log();
    let l = log.lines().rev().find(|l| l.contains("deskbar: layout "))?;
    let body = l.split("deskbar: layout ").nth(1)?;
    let field = |k: &str| body.split(&format!("{k}=")).nth(1).map(|r| r.split(' ').next().unwrap_or("").to_string());
    let rect: [i32; 4] = nums(&field("box")?);
    let rows = body.split_once("rows=[")?.1.rsplit_once(']')?.0;
    let rows = rows.split(';').filter(|s| !s.is_empty()).map(|r| {
        let mut p = r.splitn(4, '|');
        let (kind, rr, text, meta) = (p.next().unwrap(), nums::<4>(p.next().unwrap()), p.next().unwrap_or(""), p.next().unwrap_or(""));
        RowL { kind: kind.into(), rect: [rect[0] + rr[0], rect[1] + rr[1], rr[2], rr[3]], text: text.into(), meta: meta.into() }
    }).collect();
    Some(Bar { scale: field("scale")?.parse().ok()?, rect, rows })
}

/// The Deskbar once it has been laid out and its ease-out is over.
fn settled(c: &Comp) -> Bar {
    wait_for("the Deskbar to be laid out", || bar(c).is_some());
    std::thread::sleep(Duration::from_millis(450));
    bar(c).unwrap()
}

fn state(c: &Comp) -> serde_json::Value {
    serde_json::from_str(c.inject_reply("state").strip_prefix("ok ").unwrap()).unwrap()
}

fn deskbar_rect(c: &Comp) -> Option<[i32; 4]> {
    state(c)["deskbar"].as_array().map(|a| std::array::from_fn(|i| a[i].as_i64().unwrap() as i32))
}

fn titles(c: &Comp) -> Vec<String> {
    state(c)["windows"].as_array().unwrap().iter().map(|w| w["title"].as_str().unwrap().to_string()).collect()
}

fn centre(r: [i32; 4], scale: f64) -> (f64, f64) { ((r[0] as f64 + r[2] as f64 / 2.0) / scale, (r[1] as f64 + r[3] as f64 / 2.0) / scale) }

fn click(c: &Comp, r: [i32; 4], scale: f64) {
    let (x, y) = centre(r, scale);
    c.inject(&format!("pointer {x:.3} {y:.3}"));
    c.inject("click");
}

fn inside(r: [i32; 4], x: usize, y: usize) -> bool {
    (x as i32) >= r[0] && (x as i32) < r[0] + r[2] && (y as i32) >= r[1] && (y as i32) < r[1] + r[3]
}

/// Pixels that differ between two captures: (inside `r`, outside it).
fn diff(a: &Img, b: &Img, r: [i32; 4]) -> (usize, usize) {
    let (mut i, mut o) = (0, 0);
    for y in 0..a.h {
        for x in 0..a.w {
            if a.at(x, y) != b.at(x, y) {
                if inside(r, x, y) { i += 1 } else { o += 1 }
            }
        }
    }
    (i, o)
}

fn count_in(img: &Img, r: [i32; 4], f: impl Fn([u8; 3]) -> bool) -> usize {
    let mut n = 0;
    for y in r[1].max(0)..(r[1] + r[3]).min(img.h as i32) {
        for x in r[0].max(0)..(r[0] + r[2]).min(img.w as i32) {
            if f(img.at(x as usize, y as usize)) { n += 1 }
        }
    }
    n
}

fn light(p: [u8; 3]) -> bool { p.iter().all(|c| *c > 200) }

fn three_apps(c: &mut Comp) {
    c.client_args(&["--size", "300x200", "--app-id", "org.test.alpha", "--title", "alpha one", "--rgb", "200,40,200"]);
    c.client_args(&["--size", "300x200", "--app-id", "org.test.alpha", "--title", "alpha two", "--rgb", "40,40,200"]);
    c.client_args(&["--size", "300x200", "--app-id", "org.test.beta", "--title", "beta one", "--rgb", "200,200,40"]);
    // the left of the output, clear of the box
    for (i, (x, y)) in [(40, 60), (120, 140), (200, 220)].iter().enumerate() {
        c.inject(&format!("move {i} {x} {y}"));
    }
    std::thread::sleep(Duration::from_millis(200));
}

#[test]
fn it_appears_as_glass_over_windows_and_groups_them_by_application() {
    for scale in SCALES {
        let (t, mut c) = themed(scale, "", &[]);
        // With nothing running the box is still there and says so: it is what you look at to see
        // what is running, and it carries the machine's load, which needs no window. It used to
        // hide, and then the desktop menu's Show entry could not bring it back -- a toggle with no
        // visible effect, which is how a user reported it as broken.
        let empty = settled(&c);
        assert!(empty.rows.iter().any(|r| r.text.contains("No windows open")), "scale {scale}: the empty box says so: {empty:#?}");
        assert!(empty.apps().is_empty(), "scale {scale}: no app rows with nothing running");
        three_apps(&mut c);
        let b = settled(&c);
        // a window that reaches under the box, to see that the box is glass over it and not under it
        let (bx, by) = (b.rect[0] as f64 / b.scale, b.rect[1] as f64 / b.scale);
        c.inject(&format!("move 1 {} {}", (bx - 150.0) as i32, (by + 40.0) as i32));
        std::thread::sleep(Duration::from_millis(400));
        println!("scale {scale}: box {:?} rows {:?}", b.rect, b.rows.iter().map(|r| (r.kind.as_str(), r.text.as_str(), r.meta.as_str())).collect::<Vec<_>>());
        assert_eq!(b.scale.to_string(), if scale == "1" { "1" } else { "1.45" });
        assert_eq!(deskbar_rect(&c), Some(b.rect));
        // right edge, a logical margin in; top margin in (the clock is off here)
        let m = (10.0 * b.scale).round() as i32;
        assert_eq!((b.rect[0] + b.rect[2], b.rect[1]), (W as i32 - m, m), "scale {scale}: not at the top-right");
        // grouped: two applications, three windows, in the order they appeared
        assert_eq!(b.rows[0].kind, "header");
        assert_eq!(b.rows[0].text, format!("2 apps {DOT} 3 windows"));
        assert_eq!(b.apps().iter().map(|r| r.text.as_str()).collect::<Vec<_>>(), ["org.test.alpha", "org.test.beta"]);
        assert!(b.wins().is_empty(), "collapsed: no window rows");

        let with = c.capture("with.png");
        // it is ink and glass, drawn: light text inside the first app's row ...
        let a0 = b.apps()[0].rect;
        let ink = count_in(&with, [a0[0] + (40.0 * b.scale) as i32, a0[1], a0[2] - (70.0 * b.scale) as i32, a0[3]], light);
        assert!(ink > 25, "scale {scale}: only {ink} light pixels where the application's name is");
        // ... and the client that reaches under the box is not seen through it as itself: its colour is gone from the box
        let own = |p: [u8; 3]| p == [40, 40, 200];
        let under_with = count_in(&with, b.rect, own);
        // (a few pixels of text anti-aliasing can land on the colour by chance; the window itself is hundreds of pixels here)
        assert!(under_with < 60, "scale {scale}: the box is not over the window that overlaps it ({under_with} pixels of its exact colour)");

        // switching it off changes only the box, and the window's colour is back there
        t.edit("[deskbar]\nenabled = true", "[deskbar]\nenabled = false");
        wait_for("the Deskbar to go", || deskbar_rect(&c).is_none());
        std::thread::sleep(Duration::from_millis(300));
        let without = c.capture("without.png");
        let (i, o) = diff(&with, &without, b.rect);
        assert_eq!(o, 0, "scale {scale}: {o} pixels outside the box changed when it went away");
        assert!(i as f64 > 0.3 * (b.rect[2] * b.rect[3]) as f64, "scale {scale}: only {i} pixels inside the box changed");
        assert!(count_in(&without, b.rect, own) > 500, "scale {scale}: the window under the box is not there without it");
    }
}

#[test]
fn an_application_opens_to_its_windows_and_closes_again() {
    for scale in SCALES {
        let (_t, mut c) = themed(scale, "", &[]);
        three_apps(&mut c);
        let closed = settled(&c);
        click(&c, closed.app("org.test.alpha").rect, closed.scale);
        wait_for("alpha to open", || bar(&c).is_some_and(|b| b.wins().len() == 2));
        let open = settled(&c);
        assert_eq!(open.wins().iter().map(|r| r.text.as_str()).collect::<Vec<_>>(), ["alpha one", "alpha two"], "scale {scale}");
        assert_eq!(open.apps().len(), 2, "beta is still a row of its own");
        assert!(open.rect[3] > closed.rect[3], "scale {scale}: the box grew ({} -> {})", closed.rect[3], open.rect[3]);
        // window rows sit between alpha and beta
        let (a, w0, w1, bt) = (open.app("org.test.alpha").rect[1], open.win("alpha one").rect[1], open.win("alpha two").rect[1], open.app("org.test.beta").rect[1]);
        assert!(a < w0 && w0 < w1 && w1 < bt, "scale {scale}: order {a} {w0} {w1} {bt}");
        // a domain-less app says so in its open row too: nothing numeric
        assert!(open.rows.iter().all(|r| r.kind != "info"), "an app outside a domain has no domain or network rows: {open:#?}");
        // clicking it again closes it
        click(&c, open.app("org.test.alpha").rect, open.scale);
        wait_for("alpha to close", || bar(&c).is_some_and(|b| b.wins().is_empty()));
        let again = settled(&c);
        assert_eq!(again.rect[3], closed.rect[3], "scale {scale}: back to the height it started at");
    }
}

#[test]
fn clicking_a_window_raises_and_focuses_it() {
    for scale in SCALES {
        let (_t, mut c) = themed(scale, "", &[]);
        c.client_args(&["--size", "300x220", "--app-id", "org.test.a", "--title", "A", "--rgb", "200,40,200"]);
        c.client_args(&["--size", "300x220", "--app-id", "org.test.b", "--title", "B", "--rgb", "40,200,40"]);
        c.inject("move 0 100 200");
        c.inject("move 1 150 250");
        std::thread::sleep(Duration::from_millis(300));
        let k: f64 = scale.parse().unwrap();
        let px = |lx: f64, ly: f64| ((lx * k) as usize, (ly * k) as usize);
        // (200, 300) is inside both windows; B is on top
        let (x, y) = px(200.0, 300.0);
        assert_eq!(c.capture("before.png").at(x, y), [40, 200, 40], "scale {scale}: control: B is on top");
        assert_eq!(titles(&c), ["A", "B"]);

        let b = settled(&c);
        click(&c, b.app("org.test.a").rect, b.scale);
        wait_for("A to open", || bar(&c).is_some_and(|b| !b.wins().is_empty()));
        let b = settled(&c);
        click(&c, b.win("A").rect, b.scale);
        wait_for("A to be on top", || titles(&c) == ["B", "A"]);
        std::thread::sleep(Duration::from_millis(300));
        assert_eq!(c.capture("after.png").at(x, y), [200, 40, 200], "scale {scale}: A's pixels are on top where the two overlap");
        // focused: the window bar of A is the focused one now, and the Deskbar says which
        let d = decos(&c.log());
        assert!(d.iter().rev().find(|d| d.text.contains("A")).is_some_and(|d| d.focused), "scale {scale}: A's bar is not focused");
        let b = settled(&c);
        assert!(b.win("A").rect[3] > 0);
        // The click was the compositor's: A's client was not sent a pointer press from it.
        assert!(!read(&c.dir.path().join("client0.log")).contains("button"), "the press reached the client");
    }
}

#[test]
fn a_window_that_is_not_in_a_domain_gets_no_figure_and_says_so() {
    for scale in SCALES {
        let d = tempfile::tempdir().unwrap();
        let bus = FakeBus::start(d.path());
        let (_t, mut c) = themed(scale, "system = false\n", &["--bus-socket", bus.path.to_str().unwrap()]);
        three_apps(&mut c);
        // The bus knows a domain with a busy network. No window is in it.
        bus.conn("ghost-domain", "evil.example", "refused");
        bus.summary("ghost-domain", &[("evil.example", "refused", 41), ("ok.example", "allowed", 700)]);
        wait_for("the Deskbar's bus thread to learn it", || c.log().contains("deskbar: bus snapshot up=true domains=1"));
        std::thread::sleep(Duration::from_millis(1300)); // past the one-second sampling tick, so a figure WOULD have appeared if one were invented
        let b = settled(&c);
        for a in b.apps() {
            assert_eq!(a.meta, "not in a domain", "scale {scale}: {a:?}");
            assert!(!a.meta.chars().any(|ch| ch.is_ascii_digit() || ch == '%'), "a numeral on an app that is not in a domain: {a:?}");
        }
        assert_eq!(b.rows[0].meta, "", "with `system = false` the header carries no figure at all");
        // pixels: the meta line is drawn (it says something) and is no wider than those words need
        let a0 = b.app("org.test.alpha").rect;
        let line = [a0[0] + (44.0 * b.scale) as i32, a0[1] + (24.0 * b.scale) as i32, a0[2] - (70.0 * b.scale) as i32, (16.0 * b.scale) as i32];
        let ink = count_in(&c.capture("meta.png"), line, light);
        assert!(ink > 0, "scale {scale}: nothing is drawn on the meta line");
        // opened, there are still no domain or network rows for it
        click(&c, b.app("org.test.alpha").rect, b.scale);
        wait_for("alpha to open", || bar(&c).is_some_and(|b| b.wins().len() == 2));
        assert!(settled(&c).rows.iter().all(|r| r.kind != "info"));
    }
}

#[test]
fn the_header_shows_machine_figures_labelled_as_such_and_only_ones_it_read() {
    // A wide deskbar, because this test is about how the figures are LABELLED and not about
    // how they fit. At the shipped 288 the header cuts the figures to leave the label its room
    // (deskbar::draw::meta_room), and a cut ends in an ellipsis that is not a figure. The fit
    // itself is asserted in draw.rs's header_fit_tests.
    let (_t, mut c) = themed("1", "width = 900.0\n", &[]);
    c.client_args(&["--size", "200x150", "--app-id", "org.test.alpha", "--title", "a"]);
    wait_for("a header figure", || bar(&c).is_some_and(|b| b.rows[0].meta.contains("cpu") || b.rows[0].meta.contains("mem")));
    std::thread::sleep(Duration::from_millis(2500));
    let b = settled(&c);
    let h = &b.rows[0].meta;
    // "cpu 12% . mem 61%": a CPU share needs two readings and a memory share one; whichever is there is a percentage
    for part in h.split(DOT) {
        let p = part.trim();
        // The link is a STATE, not a figure: one word, no number, and nothing to mislabel.
        if matches!(p, "wired" | "wi-fi" | "offline") {
            continue;
        }
        let (name, val) = p.split_once(' ').unwrap_or((p, ""));
        assert!(matches!(name, "cpu" | "mem" | "batt"), "an unlabelled figure in the header: {h:?}");
        // A leading "+" on the battery means charging and still parses as a number.
        assert!(val.ends_with('%') && val.trim_end_matches('%').trim_start_matches('<').parse::<f32>().is_ok(), "{h:?}");
    }
    assert!(h.contains("mem"), "this machine has /proc/meminfo: {h:?}");
}

#[test]
fn it_is_hidden_behind_a_fullscreen_window_and_under_legacy_scene_but_not_for_being_empty() {
    let (_t, mut c) = themed("1", "", &[]);
    // Emptiness is NOT a reason to hide (see the grouping test): the box is up from the start.
    assert!(deskbar_rect(&c).is_some(), "the box is shown with no windows");
    c.client_args(&["--size", "200x150", "--title", "plain"]);
    wait_for("a window to be listed", || settled(&c).apps().len() == 1);
    c.client_args(&["--fullscreen", "--hidpi", "--rgb", "10,200,60"]);
    wait_for("the box to get out of a fullscreen window's way", || deskbar_rect(&c).is_none());
    let cap = c.capture("fs.png");
    assert!(cap.count([10, 200, 60]) as f64 >= 0.9 * (W * H) as f64, "control: the fullscreen client covers the output");
    // the compositor started with --legacy-scene (the harness's `start`) never draws it, windows or not
    let t = Theme::new();
    t.append(BASE);
    let mut l = Comp::start(&["--theme", t.path(), "--freeze-time"]);
    l.client_args(&["--size", "200x150", "--title", "plain"]);
    std::thread::sleep(Duration::from_millis(400));
    assert_eq!(deskbar_rect(&l), None);
}

#[test]
fn the_pointer_over_it_is_the_compositors_and_hovering_restyles_nothing_outside_it() {
    for scale in SCALES {
        let (_t, mut c) = themed(scale, "", &[]);
        c.client_args(&["--size", "300x200", "--app-id", "org.test.a", "--title", "A", "--rgb", "200,40,200"]);
        c.client_args(&["--size", "300x200", "--app-id", "org.test.b", "--title", "B", "--rgb", "40,200,40"]);
        c.inject("move 0 100 200");
        c.inject("move 1 150 250");
        let b = settled(&c);
        c.inject("pointer 600 700");
        std::thread::sleep(Duration::from_millis(300));
        let before = c.capture("rest.png");
        let focused_before = decos(&c.log()).iter().filter(|d| d.focused).count();
        let (x, y) = centre(b.app("org.test.a").rect, b.scale);
        c.inject(&format!("pointer {x:.3} {y:.3}"));
        std::thread::sleep(Duration::from_millis(400));
        let hover = c.capture("hover.png");
        let (i, o) = diff(&before, &hover, b.rect);
        assert!(i > 50, "scale {scale}: hovering a row changed only {i} pixels in the box");
        assert_eq!(o, 0, "scale {scale}: hovering the Deskbar restyled {o} pixels outside it (a window bar, the desktop, ...)");
        // a click on it is taken: B stays focused and on top, and no window moved
        c.inject("click");
        std::thread::sleep(Duration::from_millis(300));
        assert_eq!(titles(&c), ["A", "B"], "scale {scale}: expanding a row must not raise or reorder anything");
        let d = decos(&c.log());
        assert_eq!(d.iter().filter(|d| d.focused).count(), focused_before, "scale {scale}: focus moved when a row was opened");
        assert!(d.iter().rev().find(|d| d.text.contains('B')).is_some_and(|d| d.focused), "scale {scale}: B lost focus");
    }
}

#[test]
fn a_row_opens_over_time_rather_than_snapping_open() {
    // What only an integration test can say: a click on an app row actually animates the
    // deskbar, over time, in one direction.
    //
    // It does NOT re-check the shape of the curve. That claim is exact in
    // shell::motion (`the_curve_starts_fast_and_ends_flat` asserts ease_out_cubic(0.33) > 0.6,
    // which IS "ahead of linear", with no clock involved). Asserting it again here meant
    // comparing a wall-clock sample against a wall-clock curve while the machine ran nineteen
    // other test binaries, and a starved compositor produces no frame: a sample at 145 ms read
    // "0.00 of the way" when the curve says 0.37, so `make test` failed about one run in three.
    // An earlier attempt softened it to a mean over the early window and it still flaked.
    //
    // Every assertion below is a LOWER bound on how long something took, or a direction. Both
    // survive starvation, because being starved can only make a thing slower, never faster.
    let (_t, mut c) = themed("1", "ease_ms = 1000\n", &[]);
    three_apps(&mut c);
    let closed = settled(&c);
    let h0 = closed.rect[3] as f64;
    click(&c, closed.app("org.test.alpha").rect, closed.scale);
    let start = Instant::now();

    let mut samples: Vec<(f64, f64)> = vec![];
    while start.elapsed() < Duration::from_millis(1600) {
        if let Some(r) = deskbar_rect(&c) {
            samples.push((start.elapsed().as_secs_f64() * 1000.0, r[3] as f64 - h0));
        }
        std::thread::sleep(Duration::from_millis(40));
    }
    let full = samples.last().unwrap().1;
    assert!(full > 20.0, "the row did not open ({full}px): {samples:?}");

    // It animated rather than snapping. The configured ease is 1000 ms, so the first sample
    // that is all the way open cannot be early. 300 ms is far below 1000, so a slow machine
    // still passes; only an instant jump fails.
    let first_full = samples.iter().find(|(_, g)| *g >= full - 1.0).expect("it reached full").0;
    assert!(first_full > 300.0, "it was fully open after {first_full:.0} ms, so it snapped rather than animated: {samples:?}");

    // And it only ever opened. A row that goes backwards is a flicker, and no amount of
    // starvation produces one.
    for w in samples.windows(2) {
        assert!(w[1].1 >= w[0].1 - 1.0, "the row went backwards, {:?} then {:?}: {samples:?}", w[0], w[1]);
    }
}

// ---- a real domain ------------------------------------------------------------------------------------------

/// The user's delegated cgroup subtree (the adapter's `delegatedBase`), as a path under /sys/fs/cgroup.
fn delegated_base() -> Option<PathBuf> {
    let text = std::fs::read_to_string("/proc/self/cgroup").ok()?;
    let path = text.lines().find_map(|l| l.strip_prefix("0::"))?;
    let want = format!("user@{}.service", unsafe { libc::getuid() });
    let mut acc = PathBuf::from("/sys/fs/cgroup");
    for part in path.trim().trim_matches('/').split('/') {
        acc.push(part);
        if part == want {
            return Some(acc);
        }
    }
    None
}

/// A cgroup under `aurora-domains` for the test's own use, removed again on drop.
struct TestDomain {
    name: String,
    dir: PathBuf,
}

impl TestDomain {
    fn create() -> Option<TestDomain> {
        let name = format!("deskbar-test-{}", std::process::id());
        let dir = delegated_base()?.join("aurora-domains").join(&name);
        std::fs::create_dir_all(&dir).ok()?;
        Some(TestDomain { name, dir })
    }
}

impl Drop for TestDomain {
    fn drop(&mut self) {
        // it can only be removed once empty; the client is killed before this runs
        for _ in 0..50 {
            if std::fs::remove_dir(&self.dir).is_ok() { return; }
            std::thread::sleep(Duration::from_millis(40));
        }
    }
}

struct Killed(Child);
impl Drop for Killed {
    fn drop(&mut self) { let _ = self.0.kill(); let _ = self.0.wait(); }
}

/// The test client, moved into `dom`'s cgroup between fork and exec by writing its own pid to cgroup.procs.
fn client_in(c: &Comp, dom: &TestDomain, args: &[&str]) -> Option<Killed> {
    let exe = std::env::current_exe().unwrap().parent().unwrap().parent().unwrap().join("examples/testclient");
    let procs = std::fs::OpenOptions::new().write(true).open(dom.dir.join("cgroup.procs")).ok()?;
    let fd = std::os::fd::AsRawFd::as_raw_fd(&procs);
    let log = c.dir.path().join("domclient.log");
    let mut cmd = Command::new(exe);
    cmd.args(args).env("XDG_RUNTIME_DIR", c.dir.path()).env("WAYLAND_DISPLAY", &c.display).stdout(std::fs::File::create(&log).unwrap());
    unsafe {
        cmd.pre_exec(move || {
            // async-signal-safe: a raw write of "0" (this process) to a descriptor opened before the fork
            if libc::write(fd, b"0".as_ptr().cast(), 1) < 0 { return Err(std::io::Error::last_os_error()); }
            Ok(())
        });
    }
    let child = cmd.spawn().ok()?;
    drop(procs);
    wait_for("the domain client to map", || read(&log).contains("mapped"));
    Some(Killed(child))
}

#[test]
fn a_client_in_a_real_domain_cgroup_gets_the_figures_that_cgroup_can_give_and_the_networks_of_its_domain() {
    let Some(dom) = TestDomain::create() else {
        eprintln!("SKIP: cannot create a cgroup under the user's delegated tree here (not a systemd user session, or no delegation)");
        return;
    };
    let d = tempfile::tempdir().unwrap();
    let bus = FakeBus::start(d.path());
    let (_t, mut c) = themed("1", "", &["--bus-socket", bus.path.to_str().unwrap()]);
    c.client_args(&["--size", "300x200", "--app-id", "org.test.plain", "--title", "plain one"]);
    let Some(_client) = client_in(&c, &dom, &["--size", "300x200", "--app-id", "org.test.boxed", "--title", "boxed one"]) else {
        eprintln!("SKIP: a process cannot be moved into the test cgroup here");
        return;
    };
    // the kernel agrees the client is in the domain, which is what the Deskbar reads
    let pid = _client.0.id();
    assert!(read(Path::new(&format!("/proc/{pid}/cgroup"))).contains(&format!("aurora-domains/{}", dom.name)), "the client is not in the cgroup");

    bus.conn(&dom.name, "evil.example", "refused");
    bus.conn(&dom.name, "ok.example", "allowed");
    bus.summary(&dom.name, &[("evil.example", "refused", 41), ("ok.example", "allowed", 700)]);
    // a CPU figure needs two readings a second apart
    wait_for("a CPU figure for the domain", || bar(&c).is_some_and(|b| b.rows.iter().any(|r| r.kind == "app" && r.text == "org.test.boxed" && r.meta.contains("cpu "))));
    let b = settled(&c);
    let boxed = b.app("org.test.boxed");
    let plain = b.app("org.test.plain");
    println!("domain row: {:?}\nplain row: {:?}", boxed.meta, plain.meta);
    // line one: the figures this cgroup offers. cpu.stat and pids.current always exist; memory.current only when the controller is on
    let line1 = boxed.meta.split(" / ").next().unwrap();
    assert!(line1.starts_with("cpu "), "{line1}");
    let mem_file = dom.dir.join("memory.current").exists();
    assert_eq!(line1.contains("mem "), mem_file, "a memory figure is shown exactly when the cgroup has memory.current ({line1})");
    assert!(line1.contains("1 proc") || line1.contains("procs"), "{line1}");
    // line two: the network of the domain, with the refusal counted
    assert!(boxed.meta.ends_with(&format!("net 2 hosts {DOT} 1 refused")), "{}", boxed.meta);
    // and the row of the app outside any domain, in the same box, still carries none of it
    assert_eq!(plain.meta, "not in a domain");

    // opened: the domain, then the refused host first, carrying the word, in the warn colour
    click(&c, boxed.rect, b.scale);
    wait_for("the domain row to open", || bar(&c).is_some_and(|b| b.rows.iter().any(|r| r.kind == "info" && r.text.starts_with("in domain "))));
    let open = settled(&c);
    let infos: Vec<(&str, &str)> = open.rows.iter().filter(|r| r.kind == "info").map(|r| (r.text.as_str(), r.meta.as_str())).collect();
    assert_eq!(infos[0].0, format!("in domain {}", dom.name));
    assert_eq!(infos[1], ("ok.example", "allowed 700").min(("evil.example", "refused 41")), "{infos:?}");
    assert_eq!(&infos[1..], [("evil.example", "refused 41"), ("ok.example", "allowed 700")], "refusals sort first");
    let img = c.capture("open.png");
    let warn = oklab_warn();
    let refused = open.rows.iter().find(|r| r.text == "evil.example").unwrap().rect;
    let n = count_in(&img, refused, |p| p.iter().zip(warn).all(|(a, b)| (*a as i32 - b as i32).abs() <= 24));
    assert!(n > 15, "the refused host's row has only {n} pixels near the warn colour {warn:?}");
}

/// `[deskbar] warn_oklab` default as sRGB, by the same rule the compositor uses (oklab_to_u8).
fn oklab_warn() -> [u8; 3] {
    let [l, a, b] = [0.93f32, 0.045, 0.02];
    let (l_, m_, s_) = (l + 0.3963377774 * a + 0.2158037573 * b, l - 0.1055613458 * a - 0.0638541728 * b, l - 0.0894841775 * a - 1.2914855480 * b);
    let (l, m, s) = (l_ * l_ * l_, m_ * m_ * m_, s_ * s_ * s_);
    let lin = [4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s, -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s, -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s];
    lin.map(|c: f32| { let c = c.clamp(0.0, 1.0); let e = if c <= 0.0031308 { 12.92 * c } else { 1.055 * c.powf(1.0 / 2.4) - 0.055 }; (e * 255.0).round() as u8 })
}

#[test]
fn the_network_note_says_what_is_known_about_the_bus_and_never_claims_silence_it_cannot_know() {
    // no --bus-socket at all, and a domain row to open: the note is "no bus", not "no connections"
    let Some(dom) = TestDomain::create() else {
        eprintln!("SKIP: cannot create a cgroup under the user's delegated tree here");
        return;
    };
    let (_t, c) = themed("1", "", &[]);
    let Some(_client) = client_in(&c, &dom, &["--size", "300x200", "--app-id", "org.test.boxed", "--title", "boxed one"]) else {
        eprintln!("SKIP: a process cannot be moved into the test cgroup here");
        return;
    };
    wait_for("the row", || bar(&c).is_some());
    let b = settled(&c);
    click(&c, b.app("org.test.boxed").rect, b.scale);
    wait_for("the note", || bar(&c).is_some_and(|b| b.rows.iter().any(|r| r.text.starts_with("network:"))));
    assert!(bar(&c).unwrap().rows.iter().any(|r| r.text == "network: no bus configured"));
}

// ---- a fake busd ----------------------------------------------------------------------------------------------

#[derive(Default)]
struct BusState {
    history: Vec<serde_json::Value>,
    subs: Vec<Arc<Mutex<UnixStream>>>,
    seq: u64,
}

#[derive(Clone)]
struct FakeBus {
    path: PathBuf,
    st: Arc<Mutex<BusState>>,
}

fn send(s: &Arc<Mutex<UnixStream>>, v: &serde_json::Value) {
    let _ = s.lock().unwrap().write_all(format!("{v}\n").as_bytes());
}

impl FakeBus {
    fn start(dir: &Path) -> FakeBus {
        let path = dir.join("bus.sock");
        let l = UnixListener::bind(&path).unwrap();
        let bus = FakeBus { path, st: Arc::default() };
        let b = bus.clone();
        std::thread::spawn(move || {
            for s in l.incoming().flatten() {
                let b = b.clone();
                std::thread::spawn(move || b.serve(s));
            }
        });
        bus
    }

    fn serve(&self, s: UnixStream) {
        let out = Arc::new(Mutex::new(s.try_clone().unwrap()));
        for line in BufReader::new(s).lines().map_while(Result::ok) {
            let Ok(req) = serde_json::from_str::<serde_json::Value>(&line) else { continue };
            let payload = match req["verb"].as_str().unwrap_or("") {
                "bus.subscribe@v1" => {
                    self.st.lock().unwrap().subs.push(out.clone());
                    serde_json::json!({"subscribed": req["payload"]["topic"]})
                }
                "bus.log@v1" => {
                    let want = req["payload"]["topic"].as_str().unwrap_or("").to_string();
                    let st = self.st.lock().unwrap();
                    let msgs: Vec<_> = st.history.iter().filter(|m| m["topic"] == want.as_str()).cloned().collect();
                    serde_json::json!({"messages": msgs})
                }
                _ => serde_json::json!({}),
            };
            send(&out, &serde_json::json!({"id": "r", "kind": "response", "corr": req["id"], "verb": req["verb"], "payload": payload}));
        }
    }

    fn emit(&self, topic: &str, payload: serde_json::Value) {
        let mut st = self.st.lock().unwrap();
        st.seq += 1;
        let ev = serde_json::json!({"id": format!("e{}", st.seq), "kind": "event", "topic": topic, "from": "domain:x:1", "ts": format!("{:019}", st.seq), "payload": payload});
        st.history.push(ev.clone());
        for s in &st.subs {
            send(s, &ev);
        }
    }

    /// busd's `net.conn`: the first connection of a (host, verdict, reason), as the proxy reports it.
    fn conn(&self, domain: &str, host: &str, verdict: &str) {
        self.emit("net.conn", serde_json::json!({"domain": domain, "host": host, "port": 443, "verdict": verdict, "reason": "r", "first": true}));
    }

    fn summary(&self, domain: &str, entries: &[(&str, &str, u64)]) {
        let es: Vec<_> = entries.iter().map(|(h, v, n)| serde_json::json!({"host": h, "verdict": v, "reason": "r", "count": n})).collect();
        self.emit("net.summary", serde_json::json!({"domain": domain, "entries": es, "dropped": 0}));
    }
}

// ---- cost -------------------------------------------------------------------------------------------------------

/// What the Deskbar costs a frame, measured, not gated (the numbers depend on a GPU others share): the same
/// 1920x1200 desktop, six clients in three applications, the Deskbar off, on, and on with two applications open
/// (the open box is taller, so it blurs and composites more pixels). Run with `--ignored --nocapture`.
fn cost_run(deskbar: bool, expand: bool, n: usize) -> String {
    let d = tempfile::tempdir().unwrap();
    let path = d.path().join("stats.csv");
    let t = Theme::new();
    t.append(&format!("[deskbar]\nenabled = {deskbar}\n"));
    let mut c = Comp::start_shell(&["--headless=1920x1200", "--scale", "1", "--frame-stats", path.to_str().unwrap(), "--theme", t.path()]);
    for (i, app) in ["org.test.a", "org.test.a", "org.test.b", "org.test.b", "org.test.c", "org.test.c"].iter().take(n).enumerate() {
        c.client_args(&["--size", "400x300", "--app-id", app, "--title", &format!("window {i}")]);
        c.inject(&format!("move {i} {} {}", 40 + i * 90, 60 + i * 70));
    }
    if deskbar && expand && n >= 3 {
        let b = settled(&c);
        click(&c, b.app("org.test.a").rect, b.scale);
        std::thread::sleep(Duration::from_millis(500));
        let b = bar(&c).unwrap();
        click(&c, b.app("org.test.b").rect, b.scale);
        std::thread::sleep(Duration::from_millis(600));
    }
    c.step_frames(40);
    c.step_frames(300);
    c.stats(300)
}

#[test]
#[ignore = "measurement: run with --ignored --nocapture"]
fn deskbar_frame_cost_report() {
    for n in [1usize, 6] {
      for (name, on, expand) in [("off     ", false, false), ("on      ", true, false), ("on, open", true, true)] {
        for _ in 0..2 {
            let s = cost_run(on, expand, n);
            println!("COST {n} window(s), deskbar {name}: {}", s.split(" | ").find(|g| g.trim_start().starts_with("total")).unwrap_or(&s));
        }
      }
    }
}
