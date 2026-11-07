//! The capability-in-use indicator (decision 0016): real headless compositors, a real Wayland
//! client, a fake busd that speaks the real `bus.subscribe@v1` / `bus.log@v1` / `bus.inspect@v1`
//! shapes and the daemon's `bus.cap.*` events (the real busd and a real controller grant:
//! demos/device-grants/run.sh). Judged on the decoded pixels of the compositor's own frame
//! (`--test-input dump`), of a client capture, and on the `indicator:` log lines. Everything with
//! a position runs at scale 1 and at 1.45.

mod common;
use common::*;
use std::{
    io::{BufRead, BufReader, Write},
    os::unix::net::{UnixListener, UnixStream},
    path::PathBuf,
    sync::{Arc, Mutex},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

const SCALES: [&str; 2] = ["1", "1.45"];
const IDA: &str = "domain:app-demo-aaaaaaaaaaaaaaaa:1111111111111111";
const IDB: &str = "domain:app-demo-bbbbbbbbbbbbbbbb:2222222222222222";
/// Never registered with the fake busd.
const IDC: &str = "domain:app-demo-cccccccccccccccc:3333333333333333";
const CTL: &str = "uid1000:controllerd";
const GREEN: [u8; 3] = [10, 200, 60];

fn now_ns() -> u128 { SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos() }

#[derive(Default)]
struct BusState {
    history: Vec<serde_json::Value>,
    subs: Vec<Arc<Mutex<UnixStream>>>,
    outstanding: Vec<String>,
    seq: u64,
}

/// A busd that keeps a log, delivers subscribed events, and answers inspect with its outstanding handles.
#[derive(Clone)]
struct FakeBus {
    path: PathBuf,
    st: Arc<Mutex<BusState>>,
}

fn send(s: &Arc<Mutex<UnixStream>>, v: &serde_json::Value) {
    let _ = s.lock().unwrap().write_all(format!("{v}\n").as_bytes());
}

impl FakeBus {
    fn start(dir: &std::path::Path) -> FakeBus {
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
                "bus.inspect@v1" => {
                    let st = self.st.lock().unwrap();
                    let handles: Vec<_> = st.outstanding.iter().map(|id| serde_json::json!({"id": id, "resource": "x", "minter": CTL, "holder": CTL})).collect();
                    serde_json::json!({"clients": [], "services": [], "subscriptions": [], "handles": handles, "clock": "x",
                        "domains": [{"identity": IDA, "socket": "/a", "cgroup": "/aurora-domains/a"}, {"identity": IDB, "socket": "/b", "cgroup": "/aurora-domains/b"}]})
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

    fn emit_at(&self, ts: u128, topic: &str, from: &str, payload: serde_json::Value) {
        let mut st = self.st.lock().unwrap();
        st.seq += 1;
        let ev = serde_json::json!({"id": format!("e{}", st.seq), "kind": "event", "topic": topic, "from": from,
            "ts": format!("{ts:019}-{:020}", st.seq), "payload": payload});
        st.history.push(ev.clone());
        for s in &st.subs {
            send(s, &ev);
        }
    }

    /// busd's own event: from "bus", and the handle is outstanding until it is revoked.
    fn mint_at(&self, ts: u128, id: &str, resource: &str, minter: &str) {
        self.st.lock().unwrap().outstanding.push(id.to_string());
        self.emit_at(ts, "bus.cap.minted", "bus", serde_json::json!({"id": id, "resource": resource, "by": minter, "minter": minter, "holder": minter}));
    }
    fn mint(&self, id: &str, resource: &str, minter: &str) { self.mint_at(now_ns(), id, resource, minter) }

    /// What the controller publishes once the grant is composed into the domain.
    fn attribute(&self, id: &str, resource: &str, from: &str, identity: &str) {
        self.emit_at(now_ns(), "provenance.grant.held", from, serde_json::json!({"cap": id, "resource": resource, "domain": identity, "app": "org.aurora.demo", "verb": "display@v1"}));
    }

    fn revoke(&self, id: &str) {
        self.st.lock().unwrap().outstanding.retain(|o| o != id);
        self.emit_at(now_ns(), "bus.cap.revoked", "bus", serde_json::json!({"id": id, "resource": "x", "by": CTL, "minter": CTL, "holder": CTL}));
    }

    /// The controller's whole act: mint, then say which domain it was composed into.
    fn grant(&self, id: &str, resource: &str, identity: &str) {
        self.mint(id, resource, CTL);
        self.attribute(id, resource, CTL, identity);
    }
}

/// A compositor with the fake bus and the scale. `theme_toml` becomes `[indicator]` and friends in a private theme.
fn comp(scale: &str, bus: &FakeBus, theme: Option<&Theme>) -> Comp {
    let mut a = vec!["--bus-socket", bus.path.to_str().unwrap(), "--scale", scale];
    if let Some(t) = theme {
        a.extend_from_slice(&["--theme", t.path()]);
    }
    Comp::start(&a)
}

/// The numbers on the last `indicator: layout` line.
#[derive(Debug, Clone)]
struct Ind {
    scale: f64,
    pill: [i32; 4],
    dot: [i32; 4],
    held: usize,
    since: i128,
    text: String,
    rows: String,
    panel: Option<[i32; 4]>,
    dot_rgb: [u8; 3],
    text_rgb: [u8; 3],
    dim_rgb: [u8; 3],
    panel_rgb: [u8; 3],
}

fn nums<const N: usize>(s: &str) -> [i32; N] {
    let v: Vec<i32> = s.split(',').filter_map(|n| n.parse().ok()).collect();
    std::array::from_fn(|i| v[i])
}

fn between<'a>(l: &'a str, a: &str, b: &str) -> &'a str {
    let s = l.split_once(a).unwrap_or_else(|| panic!("no {a:?} in {l}")).1;
    s.split_once(b).map_or(s, |x| x.0)
}

fn layout(c: &Comp) -> Option<Ind> {
    let log = c.log();
    let l = log.lines().rev().find(|l| l.contains("indicator: layout"))?;
    let f = |k: &str| between(l, &format!(" {k}="), " ").to_string();
    let rgb = |k: &str| { let v = nums::<3>(&f(k)); [v[0] as u8, v[1] as u8, v[2] as u8] };
    let panel = f("panel");
    Some(Ind {
        scale: f("scale").parse().unwrap(), pill: nums(&f("pill")), dot: nums(&f("dot")), held: f("held").parse().unwrap(), since: f("since").parse().unwrap(),
        text: between(l, " text=\"", "\" rows=").to_string(), rows: between(l, "rows=[", "] panel=").to_string(),
        panel: (panel != "none").then(|| nums(&panel)), dot_rgb: rgb("dot_rgb"), text_rgb: rgb("text_rgb"), dim_rgb: rgb("dim_rgb"), panel_rgb: rgb("panel_rgb"),
    })
}

/// The last `indicator: bus snapshot` line: how many caps the bus thread says are held, and which.
fn snapshot(c: &Comp) -> Option<String> {
    let log = c.log();
    let l = log.lines().rev().find(|l| l.contains("indicator: bus snapshot"))?;
    Some(l.split("indicator: bus snapshot ").nth(1).unwrap().to_string())
}

fn wait_snapshot(c: &Comp, want: &str) {
    wait_for(&format!("bus snapshot {want:?}"), || snapshot(c).is_some_and(|s| s.starts_with(want)));
}

fn shown(c: &Comp) -> Ind {
    wait_for("the indicator to be laid out", || layout(c).is_some());
    std::thread::sleep(Duration::from_millis(450)); // the ease-out is over
    layout(c).unwrap()
}

/// The compositor's own final frame: where the indicator is, and everything under it.
fn frame(c: &Comp, name: &str) -> Img {
    let p = c.dir.path().join(name);
    let _ = std::fs::remove_file(&p);
    c.inject(&format!("dump {}", p.display()));
    wait_for("the compositor to write its frame", || p.exists());
    Img::load_file(&p)
}

fn in_rect(img: &Img, r: [i32; 4], rgb: [u8; 3]) -> usize {
    let mut n = 0;
    for y in r[1].max(0)..(r[1] + r[3]).min(img.h as i32) {
        for x in r[0].max(0)..(r[0] + r[2]).min(img.w as i32) {
            if img.at(x as usize, y as usize) == rgb { n += 1; }
        }
    }
    n
}

fn contrast(a: [u8; 3], b: [u8; 3]) -> f64 {
    let l = |c: [u8; 3]| 0.2126 * srgb_to_linear(c[0]) + 0.7152 * srgb_to_linear(c[1]) + 0.0722 * srgb_to_linear(c[2]);
    (l(a).max(l(b)) + 0.05) / (l(a).min(l(b)) + 0.05)
}

fn centre(r: [i32; 4], scale: f64) -> (f64, f64) { ((r[0] as f64 + r[2] as f64 / 2.0) / scale, (r[1] as f64 + r[3] as f64 / 2.0) / scale) }

/// The dot is lit: its own colour fills a good part of its box, and its middle is exactly that colour.
fn assert_dot(img: &Img, i: &Ind) {
    let d = i.dot;
    let n = in_rect(img, d, i.dot_rgb);
    assert!(n as f64 >= 0.4 * (d[2] * d[3]) as f64, "the dot colour {:?} fills only {n} of {} pixels of its box {d:?}", i.dot_rgb, d[2] * d[3]);
    assert_eq!(img.at((d[0] + d[2] / 2) as usize, (d[1] + d[3] / 2) as usize), i.dot_rgb, "the middle of the dot");
}

#[test]
fn the_dot_is_there_while_a_domain_holds_the_display_and_gone_when_it_is_revoked() {
    for scale in SCALES {
        let d = tempfile::tempdir().unwrap();
        let bus = FakeBus::start(d.path());
        let mut c = comp(scale, &bus, None);
        c.client_args(&["--size", "400x300", "--pattern", "checker"]);
        let before = frame(&c, "before.png");
        assert!(layout(&c).is_none(), "an indicator with nothing held");

        bus.grant("cap:aaaaaaaa", "device:wayland", IDA);
        let i = shown(&c);
        let held = frame(&c, "held.png");
        println!("scale {scale}: HELD pill {:?} dot {:?} text {:?} dot_rgb {:?}", i.pill, i.dot, i.text, i.dot_rgb);
        assert_eq!(i.scale.to_string(), if scale == "1" { "1" } else { "1.45" });
        assert_dot(&held, &i);
        // top-right, a logical margin from the corner, whatever the scale
        let m = (10.0 * i.scale).round() as i32;
        assert_eq!((i.pill[0] + i.pill[2], i.pill[1]), (W as i32 - m, m), "pill is not at the top-right corner");
        // and it is a pill, not a repaint of the frame: only its box differs from before
        let changed = held.diff_count(&before);
        assert!(changed > 100 && changed <= (i.pill[2] * i.pill[3]) as usize, "{changed} pixels differ; the pill box is {:?}", i.pill);
        assert!(i.text.contains("Display") && i.text.contains("app-demo-aaaaaaaaaaaaaaaa"), "{:?}", i.text);
        // it is a rounded pill: its corner pixel is the desktop behind it, not a black box
        assert_eq!(held.at(i.pill[0] as usize, i.pill[1] as usize), before.at(i.pill[0] as usize, i.pill[1] as usize), "the pill's corner is not transparent");

        bus.revoke("cap:aaaaaaaa");
        wait_snapshot(&c, "held=0");
        std::thread::sleep(Duration::from_millis(250));
        let after = frame(&c, "after.png");
        assert_eq!(in_rect(&after, i.dot, i.dot_rgb), 0, "the dot is still lit after the revoke");
        assert_eq!(after.count(i.dot_rgb), 0, "a pixel of the dot colour survives anywhere in the frame");
        assert_eq!(after.diff_count(&before), 0, "after the revoke the frame is exactly what it was before the grant");
        println!("scale {scale}: after revoke {} differing pixels from before the grant", after.diff_count(&before));
    }
}

#[test]
fn a_compositor_that_starts_while_a_grant_is_held_still_shows_it_and_says_since_when_busd_minted_it() {
    let d = tempfile::tempdir().unwrap();
    let bus = FakeBus::start(d.path());
    let ago = 90_000_000_000u128;
    let minted = now_ns() - ago;
    bus.mint_at(minted, "cap:early", "device:wayland", CTL);
    bus.attribute("cap:early", "device:wayland", CTL, IDB);
    let c = comp("1", &bus, None);
    let i = shown(&c);
    assert_eq!(i.since, minted as i128, "the time shown is busd's mint time, not the compositor's");
    assert!(i.text.contains("app-demo-bbbbbbbbbbbbbbbb"), "{:?}", i.text);
    let img = frame(&c, "f.png");
    assert_dot(&img, &i);
}

#[test]
fn a_fullscreen_client_cannot_cover_it() {
    for scale in SCALES {
        let d = tempfile::tempdir().unwrap();
        let bus = FakeBus::start(d.path());
        let mut c = comp(scale, &bus, None);
        c.client_args(&["--fullscreen", "--hidpi", "--rgb", "10,200,60"]);
        // control: the client really does cover the output (all but the dock, which the compositor still draws over a fullscreen client: 2% of the frame)
        let bare = frame(&c, "bare.png");
        let g = bare.count(GREEN);
        println!("scale {scale}: CONTROL fullscreen client covers {g} of {} pixels, bbox {:?}", W * H, bare.bbox(GREEN));
        assert!(g as f64 >= 0.95 * (W * H) as f64, "the control failed: the client covers only {g} of {} pixels, so 'it cannot cover the dot' would prove nothing", W * H);

        bus.grant("cap:fffffff1", "device:wayland", IDA);
        let i = shown(&c);
        let img = frame(&c, "full.png");
        assert_dot(&img, &i);
        let rest = img.count(GREEN);
        println!("scale {scale}: with the grant the dot is lit ({} dot pixels) and the client still covers {rest} pixels", in_rect(&img, i.dot, i.dot_rgb));
        assert!(rest as f64 >= 0.9 * (W * H) as f64, "the client no longer covers the output; the test would be vacuous");
        assert!(rest < g, "the pill took no pixels from the client");
        // it is above even a client raised to the top afterwards
        c.client_args(&["--fullscreen", "--hidpi", "--rgb", "200,30,30"]);
        let two = frame(&c, "second.png");
        assert!(two.count([200, 30, 30]) as f64 >= 0.9 * (W * H) as f64, "the second fullscreen client is not on top");
        assert_dot(&two, &i);
    }
}

#[test]
fn it_is_not_in_a_client_capture_but_is_in_the_compositors_own_frame() {
    for scale in SCALES {
        let d = tempfile::tempdir().unwrap();
        let bus = FakeBus::start(d.path());
        let mut c = comp(scale, &bus, None);
        c.client_args(&["--fullscreen", "--hidpi", "--rgb", "10,200,60"]);
        let cap_before = c.capture("cap-before.png");
        bus.grant("cap:cccccccc", "device:wayland", IDA);
        let i = shown(&c);
        let cap = c.capture("cap-held.png");
        let own = frame(&c, "own.png");
        assert_dot(&own, &i);
        println!("scale {scale}: compositor frame has {} dot pixels in its box; the capture has {} anywhere", in_rect(&own, i.dot, i.dot_rgb), cap.count(i.dot_rgb));
        assert_eq!(cap.count(i.dot_rgb), 0, "the capture contains the dot");
        assert_eq!(in_rect(&cap, i.pill, i.panel_rgb), 0, "the capture contains the pill's own colour");
        assert_eq!(cap.diff_count(&cap_before), 0, "the capture while the indicator is lit differs from the capture before it");
        assert!(own.diff_count(&cap) > 100, "the compositor's own frame and the capture should differ by the pill");
        // with the detail panel open too
        let (cx, cy) = centre(i.pill, i.scale);
        c.inject(&format!("pointer {cx:.2} {cy:.2}"));
        wait_for("the panel", || layout(&c).is_some_and(|l| l.panel.is_some()));
        std::thread::sleep(Duration::from_millis(450));
        let cap2 = c.capture("cap-panel.png");
        let own2 = frame(&c, "own-panel.png");
        let p = layout(&c).unwrap().panel.unwrap();
        assert!(own2.diff_count(&own) > 500, "the panel did not draw into the compositor's frame");
        assert_eq!(cap2.diff_count(&cap_before), 0, "the capture with the panel open differs from the capture before ({:?})", p);
        println!("scale {scale}: panel open ({} pixels differ in the compositor frame), capture still identical to before", own2.diff_count(&own));
    }
}

#[test]
fn hovering_shows_what_is_held_by_whom_and_the_pointer_over_it_belongs_to_the_compositor() {
    for scale in SCALES {
        let d = tempfile::tempdir().unwrap();
        let bus = FakeBus::start(d.path());
        let mut c = comp(scale, &bus, None);
        let log = c.client_args(&["--fullscreen", "--hidpi", "--rgb", "10,200,60"]);
        bus.grant("cap:dddddddd", "device:wayland", IDA);
        let i = shown(&c);
        assert!(i.panel.is_none(), "the panel is open with no pointer near");
        // a pointer on the client first: the client hears it
        c.inject("pointer 200 400");
        wait_for("the client's pointer_enter", || read(&log).contains("pointer_enter"));
        let before = read(&log).matches("pointer_motion").count();
        let (cx, cy) = centre(i.pill, i.scale);
        c.inject(&format!("pointer {cx:.2} {cy:.2}"));
        wait_for("the panel to open", || layout(&c).is_some_and(|l| l.panel.is_some()));
        std::thread::sleep(Duration::from_millis(450));
        let l = layout(&c).unwrap();
        let panel = l.panel.unwrap();
        assert!(l.rows.contains("Display access|app-demo-aaaaaaaaaaaaaaaa|attested"), "rows: {}", l.rows);
        let img = frame(&c, "hover.png");
        assert!(in_rect(&img, panel, l.panel_rgb) > (panel[2] * panel[3]) as usize / 2, "the panel is not mostly its own colour");
        assert!(in_rect(&img, panel, l.text_rgb) > 30, "no text in the panel");
        assert!(in_rect(&img, panel, l.dim_rgb) > 3, "no dim text in the panel");
        assert_dot(&img, &l);
        assert_eq!(img.at(panel[0] as usize, panel[1] as usize), GREEN, "the panel's corner is not transparent");
        // the pointer is the compositor's while it is over the indicator: the client saw it leave and hears nothing more
        wait_for("the client's pointer_leave", || read(&log).contains("pointer_leave"));
        c.inject(&format!("pointer {:.2} {:.2}", cx - 3.0, cy + 2.0));
        c.inject("click"); // pins the panel ...
        c.inject("click"); // ... and unpins it
        c.inject("rclick");
        std::thread::sleep(Duration::from_millis(200));
        let text = read(&log);
        assert_eq!(text.matches("pointer_motion").count(), before, "the client heard pointer motion over the indicator");
        // a pointer on the panel keeps it open; leaving closes it
        let (px, py) = (panel[0] as f64 / i.scale + 20.0, (panel[1] + panel[3] / 2) as f64 / i.scale);
        c.inject(&format!("pointer {px:.2} {py:.2}"));
        std::thread::sleep(Duration::from_millis(100));
        assert!(layout(&c).unwrap().panel.is_some() && frame(&c, "x.png").count(l.text_rgb) > 0, "the panel closed with the pointer on it");
        c.inject("pointer 200 400");
        wait_for("the panel to close", || layout(&c).is_some_and(|l| l.panel.is_none()));
        // a click pins it open, another unpins it
        c.inject(&format!("pointer {cx:.2} {cy:.2}"));
        c.inject("click");
        c.inject("pointer 200 400");
        std::thread::sleep(Duration::from_millis(300));
        assert!(layout(&c).unwrap().panel.is_some(), "a click did not pin the panel open");
        c.inject(&format!("pointer {cx:.2} {cy:.2}"));
        c.inject("click");
        c.inject("pointer 200 400");
        wait_for("the panel to close after unpinning", || layout(&c).is_some_and(|l| l.panel.is_none()));
        println!("scale {scale}: hover opened the panel ({}), a click pinned and unpinned it; client pointer_motion count unchanged at {before}", l.rows);
    }
}

#[test]
fn the_detail_text_meets_the_contrast_floor_and_the_check_can_fail() {
    const FLOOR: f64 = 4.5;
    for scale in SCALES {
        let d = tempfile::tempdir().unwrap();
        let bus = FakeBus::start(d.path());
        let mut c = comp(scale, &bus, None);
        c.client_args(&["--fullscreen", "--hidpi", "--rgb", "255,255,255"]); // the worst backdrop: white
        bus.grant("cap:eeeeeeee", "device:wayland", IDA);
        bus.grant("cap:eeeeeee2", "device:wayland", IDC); // a domain busd does not vouch for: its warning must be legible too
        let i = shown(&c);
        let (cx, cy) = centre(i.pill, i.scale);
        c.inject(&format!("pointer {cx:.2} {cy:.2}"));
        wait_for("the panel", || layout(&c).is_some_and(|l| l.panel.is_some()));
        std::thread::sleep(Duration::from_millis(450));
        let l = layout(&c).unwrap();
        let (panel, img) = (l.panel.unwrap(), frame(&c, "contrast.png"));
        assert!(l.rows.contains("unattested"), "{}", l.rows);
        let bg = img.at((panel[0] + (6.0 * i.scale) as i32) as usize, (panel[1] + (6.0 * i.scale) as i32) as usize);
        assert_eq!(bg, l.panel_rgb, "the panel's own pixel is not the colour the log says");
        let warn = [255u8, 120, 100];
        for (name, ink) in [("text", l.text_rgb), ("dim text", l.dim_rgb), ("unattested warning", warn)] {
            let n = in_rect(&img, panel, ink);
            let r = contrast(ink, bg);
            println!("scale {scale}: {name} {ink:?} on panel {bg:?}: {n} pixels, contrast {r:.2}:1 (floor {FLOOR}:1)");
            assert!(n > 10, "no {name} pixels of {ink:?} in the panel");
            assert!(r >= FLOOR, "{name} has contrast {r:.2}:1 over the panel, floor {FLOOR}:1");
        }
    }
    // control: the same measurement, with a theme whose panel is nearly the colour of the text, fails the floor
    let t = Theme::new();
    t.append("[indicator]\npanel_oklab = [0.9, 0.0, 0.0]\n");
    let d = tempfile::tempdir().unwrap();
    let bus = FakeBus::start(d.path());
    let c = comp("1", &bus, Some(&t));
    bus.grant("cap:eeeeeee3", "device:wayland", IDA);
    let i = shown(&c);
    let (cx, cy) = centre(i.pill, i.scale);
    c.inject(&format!("pointer {cx:.2} {cy:.2}"));
    wait_for("the panel", || layout(&c).is_some_and(|l| l.panel.is_some()));
    std::thread::sleep(Duration::from_millis(450));
    let l = layout(&c).unwrap();
    let r = contrast(l.text_rgb, l.panel_rgb);
    println!("CONTROL: text {:?} on a light panel {:?}: contrast {r:.2}:1", l.text_rgb, l.panel_rgb);
    assert!(r < FLOOR, "the control should fail the floor and passed with {r:.2}:1, so the assertion above could not fail");
}

#[test]
fn a_client_cannot_cause_a_false_indicator() {
    let d = tempfile::tempdir().unwrap();
    let bus = FakeBus::start(d.path());
    let mut c = comp("1", &bus, None);
    // a Wayland client that says everything it can say about itself
    c.client_args(&["--title", "device:wayland held by domain:evil:1234", "--app-id", "device:wayland"]);
    let base = frame(&c, "base.png");
    // (a) a domain connection mints a handle for the display: busd truthfully stamps the DOMAIN as minter
    bus.mint("cap:forged01", "device:wayland", IDB);
    bus.attribute("cap:forged01", "device:wayland", IDB, IDB);
    bus.attribute("cap:forged01", "device:wayland", CTL, IDB); // even with the controller's voice: the mint was not the controller's
    // (b) a domain publishes the attribution topic for the controller's REAL handle, naming itself
    bus.mint("cap:real0001", "device:gpu", CTL);
    bus.attribute("cap:real0001", "device:gpu", IDB, IDB);
    // (c) an envelope that says it is a mint but is not stamped by the daemon
    bus.emit_at(now_ns(), "bus.cap.minted", IDB, serde_json::json!({"id": "cap:forged03", "resource": "device:wayland", "by": CTL, "minter": CTL, "holder": CTL}));
    bus.attribute("cap:forged03", "device:wayland", CTL, IDB);
    // (d) an attribution naming a different resource than was minted
    bus.mint("cap:forged04", "device:gpu", CTL);
    bus.attribute("cap:forged04", "device:wayland", CTL, IDB);
    // (e) an attribution for a handle that was never minted
    bus.attribute("cap:forged05", "device:wayland", CTL, IDB);
    // sentinel: a REAL controller grant of a class the theme does not show. The bus thread handles events in order, so
    // once this is in the snapshot every forgery above has been processed.
    bus.grant("cap:sentinel", "device:gpu", IDA);
    wait_snapshot(&c, "held=1");
    std::thread::sleep(Duration::from_millis(300));
    let snap = snapshot(&c).unwrap();
    println!("SNAPSHOT after seven forgeries and one real device:gpu grant: {snap}");
    // Only the sentinel is held. The controller's real gpu handle cap:real0001 was attributed only by a domain, so it is not.
    assert!(snap.starts_with("held=1 up=true device:gpu@app-demo-aaaaaaaaaaaaaaaa"), "{snap}");
    assert!(layout(&c).is_none(), "an indicator was laid out: {}", c.log().lines().rev().find(|l| l.contains("indicator: layout")).unwrap_or(""));
    let img = frame(&c, "forged.png");
    assert_eq!(img.diff_count(&base), 0, "the frame changed after the forgeries");
    // the client's own pixels at the dot's place do not make one: hover over the corner opens nothing
    c.inject("pointer 1260 20");
    std::thread::sleep(Duration::from_millis(300));
    assert!(layout(&c).is_none());
    // A client may PAINT anything, including a dot-coloured screen. That is a picture, not the indicator: nothing is
    // laid out, hovering it does not open the panel, and the pointer over it still belongs to the client.
    let mut painter = Comp::start_with(|_| {}, &["--bus-socket", bus.path.to_str().unwrap()]);
    let log = painter.client_args(&["--fullscreen", "--hidpi", "--rgb", "255,100,24"]);
    painter.inject("pointer 1200 20");
    painter.inject("pointer 1210 22");
    painter.inject("pointer 1215 24");
    wait_for("the painter to hear the pointer", || read(&log).contains("pointer_enter") && read(&log).contains("pointer_motion"));
    assert!(layout(&painter).is_none(), "a client painting the dot colour caused an indicator");
    assert!(painter.log().lines().all(|l| !l.contains("panel pinned")));
    drop(painter);
    // the control: the bus path works. A real controller grant of the display lights it.
    bus.grant("cap:real0002", "device:wayland", IDA);
    let i = shown(&c);
    assert!(i.text.contains("app-demo-aaaaaaaaaaaaaaaa") && !i.text.contains("evil"), "{:?}", i.text);
    assert_eq!(i.held, 1, "only the real display grant counts");
    assert_dot(&frame(&c, "real.png"), &i);
    // and a forged attribution sent BEFORE the real one cannot hide it
    bus.revoke("cap:real0002");
    bus.mint("cap:real0003", "device:wayland", CTL);
    bus.attribute("cap:real0003", "device:wayland", IDB, IDB);
    bus.attribute("cap:real0003", "device:wayland", CTL, IDA);
    wait_for("the real grant after a forged attribution", || snapshot(&c).is_some_and(|s| s.contains("device:wayland@app-demo-aaaaaaaaaaaaaaaa")));
    let i = shown(&c);
    assert!(i.text.contains("app-demo-aaaaaaaaaaaaaaaa"), "{:?}", i.text);
    assert_dot(&frame(&c, "real2.png"), &i);
}

#[test]
fn the_wayland_protocol_has_no_object_for_it() {
    let d = tempfile::tempdir().unwrap();
    let bus = FakeBus::start(d.path());
    let c = comp("1", &bus, None);
    let g = c.globals().to_lowercase();
    for bad in ["indicator", "capab", "privacy", "grant"] {
        assert!(!g.contains(bad), "a global mentions {bad}: {g}");
    }
}

#[test]
fn which_classes_count_is_the_themes_decision_and_the_default_is_the_display_alone() {
    let d = tempfile::tempdir().unwrap();
    let bus = FakeBus::start(d.path());
    let t = Theme::new();
    let c = comp("1", &bus, Some(&t));
    bus.grant("cap:gpu00001", "device:gpu", IDA);
    wait_snapshot(&c, "held=1");
    std::thread::sleep(Duration::from_millis(400));
    assert!(layout(&c).is_none(), "the default shows a GPU grant");
    println!("DEFAULT: a held device:gpu grant shows nothing");
    // the same grant, after the theme lists it (hot reload)
    t.append("[indicator]\nclasses = [\"device:gpu\"]\n");
    let i = shown(&c);
    assert!(i.text.contains("GPU: app-demo-aaaaaaaaaaaaaaaa"), "{:?}", i.text);
    println!("THEME classes = [device:gpu]: {:?}", i.text);
    // and the display, no longer listed, is not shown
    bus.grant("cap:wl000001", "device:wayland", IDB);
    wait_snapshot(&c, "held=2");
    std::thread::sleep(Duration::from_millis(400));
    assert_eq!(layout(&c).unwrap().held, 1, "a class the theme does not list is counted");
    // and the whole thing can be switched off
    t.edit("classes = [\"device:gpu\"]", "classes = [\"device:gpu\"]\nenabled = false");
    wait_for("the indicator to go", || { std::thread::sleep(Duration::from_millis(100)); frame(&c, "off.png").count(i.dot_rgb) == 0 });
}
