//! Acting on windows that already exist: the Deskbar's window and application rows, the dock's running
//! items, and Show Desktop. Real headless compositors, real Wayland clients, the real menu (read off the
//! `menu: layout` log line it was drawn from) and the real input path (`pointer`, `click`, `rclick`).
//! Everything with a position runs at scale 1 and at 1.45; every pixel comparison runs `--freeze-time`.

mod common;
use common::*;
use std::{
    io::{BufRead, BufReader, Write},
    os::unix::net::UnixListener,
    path::{Path, PathBuf},
    time::Duration,
};

const SCALES: [&str; 2] = ["1", "1.45"];
const BASE: &str = "[background]\nmode = \"solid\"\ntop = [0.30, 0.0, 0.0]\n[desktop]\nclock = false\nhostname = false\n";
const LUMEN: &str = "org.aurora.lumen";

fn state(c: &Comp) -> serde_json::Value {
    serde_json::from_str(c.inject_reply("state").strip_prefix("ok ").unwrap()).unwrap()
}
fn titles(c: &Comp) -> Vec<String> {
    state(c)["windows"].as_array().unwrap().iter().map(|w| w["title"].as_str().unwrap().to_string()).collect()
}
fn nums(s: &str) -> Vec<i32> { s.split(',').filter_map(|n| n.parse().ok()).collect() }

struct Menu { rows: Vec<[i32; 4]>, text: Vec<(String, i32, i32)>, n: usize }

fn menus(c: &Comp) -> usize { c.log().lines().filter(|l| l.contains("menu: layout")).count() }

fn menu_layout(log: &str) -> Option<Menu> {
    let all: Vec<&str> = log.lines().filter(|l| l.contains("menu: layout")).collect();
    let line = *all.last()?;
    let rows = line.split("rows=[").nth(1)?.split(']').next()?.split(';').filter(|s| !s.is_empty()).map(|r| { let v = nums(r); [v[0], v[1], v[2], v[3]] }).collect();
    let text = line.split("text=").last()?.split('\u{1f}').filter(|t| !t.is_empty()).map(|t| { let (s, p) = t.rsplit_once('@').unwrap(); let v = nums(p); (s.to_string(), v[0], v[1]) }).collect();
    Some(Menu { rows, text, n: all.len() })
}

fn wait_menu(c: &Comp, after: usize) -> Menu {
    wait_for("a menu layout", || menu_layout(&c.log()).is_some_and(|m| m.n > after));
    std::thread::sleep(Duration::from_millis(350));
    menu_layout(&c.log()).unwrap()
}

impl Menu {
    fn texts(&self) -> Vec<&str> { self.text.iter().map(|t| t.0.as_str()).collect() }
    fn has(&self, s: &str) -> bool { self.text.iter().any(|t| t.0 == s) }
    fn row(&self, name: &str) -> [i32; 4] {
        let t = self.text.iter().find(|t| t.0 == name).unwrap_or_else(|| panic!("no entry {name:?} in {:?}", self.texts()));
        *self.rows.iter().find(|r| t.2 >= r[1] && t.2 < r[1] + r[3]).unwrap()
    }
}

fn centre(r: [i32; 4], scale: f64) -> (f64, f64) { ((r[0] as f64 + r[2] as f64 / 2.0) / scale, (r[1] as f64 + r[3] as f64 / 2.0) / scale) }
fn point(c: &Comp, r: [i32; 4], scale: f64) { let (x, y) = centre(r, scale); c.inject(&format!("pointer {x:.3} {y:.3}")); }
fn choose(c: &Comp, m: &Menu, name: &str, scale: f64) { point(c, m.row(name), scale); c.inject("click"); }

// ---- the Deskbar ----------------------------------------------------------------------------------

#[derive(Clone, Debug)]
struct RowL { kind: String, rect: [i32; 4], text: String, meta: String }
struct Bar { scale: f64, rows: Vec<RowL> }

fn bar(c: &Comp) -> Option<Bar> {
    let log = c.log();
    let l = log.lines().rev().find(|l| l.contains("deskbar: layout "))?;
    let body = l.split("deskbar: layout ").nth(1)?;
    let field = |k: &str| body.split(&format!("{k}=")).nth(1).map(|r| r.split(' ').next().unwrap_or("").to_string());
    let rect = nums(&field("box")?);
    let rows = body.split_once("rows=[")?.1.rsplit_once(']')?.0;
    let rows = rows.split(';').filter(|s| !s.is_empty()).map(|r| {
        let mut p = r.splitn(4, '|');
        let (kind, rr, text, meta) = (p.next().unwrap(), nums(p.next().unwrap()), p.next().unwrap_or(""), p.next().unwrap_or(""));
        RowL { kind: kind.into(), rect: [rect[0] + rr[0], rect[1] + rr[1], rr[2], rr[3]], text: text.into(), meta: meta.into() }
    }).collect();
    Some(Bar { scale: field("scale")?.parse().ok()?, rows })
}

fn settled(c: &Comp) -> Bar {
    wait_for("the Deskbar to be laid out", || bar(c).is_some());
    std::thread::sleep(Duration::from_millis(450));
    bar(c).unwrap()
}

impl Bar {
    fn app(&self, label: &str) -> &RowL { self.rows.iter().find(|r| r.kind == "app" && r.text == label).unwrap_or_else(|| panic!("no app row {label:?}")) }
    fn win(&self, title: &str) -> &RowL { self.rows.iter().find(|r| r.kind == "win" && r.text == title).unwrap_or_else(|| panic!("no window row {title:?}: {:?}", self.rows)) }
}

fn rig(scale: &str) -> (Theme, Comp) {
    let t = Theme::new();
    t.append(BASE);
    t.append("[deskbar]\nenabled = true\n");
    let c = Comp::start_shell(&["--theme", t.path(), "--freeze-time", "--scale", scale]);
    std::thread::sleep(Duration::from_millis(300));
    (t, c)
}

/// Two alpha windows and one beta, expanded so every window has a row.
fn open_three(c: &mut Comp, refuse_alpha_two: bool) -> Bar {
    c.client_args(&["--size", "300x200", "--app-id", "org.test.alpha", "--title", "alpha one", "--rgb", "200,40,200"]);
    let two: Vec<&str> = if refuse_alpha_two { vec!["--refuse-close"] } else { vec![] };
    let mut a = vec!["--size", "300x200", "--app-id", "org.test.alpha", "--title", "alpha two", "--rgb", "40,40,200"];
    a.extend(two);
    c.client_args(&a);
    c.client_args(&["--size", "300x200", "--app-id", "org.test.beta", "--title", "beta one", "--rgb", "200,200,40"]);
    for (i, (x, y)) in [(40, 60), (120, 140), (200, 220)].iter().enumerate() {
        c.inject(&format!("move {i} {x} {y}"));
    }
    let b = settled(c);
    for app in ["org.test.alpha", "org.test.beta"] {
        let b = settled(c);
        let r = b.app(app).rect;
        let (x, y) = centre(r, b.scale);
        c.inject(&format!("pointer {x:.3} {y:.3}"));
        c.inject("click");
        std::thread::sleep(Duration::from_millis(100));
    }
    let _ = b;
    wait_for("every window row", || bar(c).is_some_and(|b| b.rows.iter().filter(|r| r.kind == "win").count() == 3));
    settled(c)
}

fn rclick_row(c: &Comp, r: &RowL, scale: f64) -> Menu {
    let n = menus(c);
    point(c, r.rect, scale);
    c.inject("rclick");
    wait_menu(c, n)
}

#[test]
fn a_window_row_menu_closes_that_window_and_only_that_one() {
    for s in SCALES {
        let (_t, mut c) = rig(s);
        let b = open_three(&mut c, false);
        let m = rclick_row(&c, b.win("alpha one"), b.scale);
        println!("scale {s}: window menu {:?}", m.texts());
        assert!(m.has("Close") && m.has("Bring_to_Front") && m.has("Show_Desktop"), "{:?}", m.texts());
        assert!(!m.texts().iter().any(|t| t.starts_with("Close_All")), "one window: no 'all'");
        choose(&c, &m, "Close", b.scale);
        wait_for("alpha one to close", || !titles(&c).contains(&"alpha one".to_string()));
        let mut left = titles(&c);
        left.sort();
        assert_eq!(left, ["alpha two", "beta one"], "scale {s}: only the chosen window went");
    }
}

#[test]
fn closing_is_a_request_a_client_can_refuse() {
    let (_t, mut c) = rig("1");
    let b = open_three(&mut c, true);
    let m = rclick_row(&c, b.win("alpha two"), b.scale);
    choose(&c, &m, "Close", b.scale);
    let log = c.dir.path().join("client1.log");
    wait_for("the client to receive the close request", || read(&log).contains("close"));
    std::thread::sleep(Duration::from_millis(600));
    assert!(titles(&c).contains(&"alpha two".to_string()), "a client that refuses keeps its window: {:?}", titles(&c));
    // and a client that did not refuse is gone, so the refusal above is the client's doing and not a broken Close
    let m = rclick_row(&c, bar(&c).unwrap().win("beta one"), b.scale);
    choose(&c, &m, "Close", b.scale);
    wait_for("beta to close", || !titles(&c).contains(&"beta one".to_string()));
}

#[test]
fn an_application_row_menu_closes_all_of_its_windows_and_says_how_many() {
    let (_t, mut c) = rig("1");
    let b = open_three(&mut c, false);
    let m = rclick_row(&c, b.app("org.test.alpha"), b.scale);
    assert!(m.has("Close_All_(2)"), "the count of what will close is on the row: {:?}", m.texts());
    choose(&c, &m, "Close_All_(2)", b.scale);
    wait_for("both alpha windows to close", || titles(&c) == ["beta one"]);
}

#[test]
fn bring_to_front_raises_a_window_that_is_behind_another() {
    for s in SCALES {
        let (_t, mut c) = rig(s);
        let b = open_three(&mut c, false);
        // alpha one (200,40,200) is under alpha two (40,40,200) where they overlap: logical (200,160) is inside both
        let sc = b.scale;
        let probe = |c: &Comp| { let img = c.capture("p.png"); img.at((200.0 * sc) as usize, (160.0 * sc) as usize) };
        assert_eq!(probe(&c), [40, 40, 200], "scale {s}: alpha two is in front to begin with");
        let m = rclick_row(&c, b.win("alpha one"), sc);
        choose(&c, &m, "Bring_to_Front", sc);
        wait_for("alpha one in front", || { std::thread::sleep(Duration::from_millis(200)); probe(&c) == [200, 40, 200] });
        assert_eq!(titles(&c).len(), 3, "nothing closed");
    }
}

#[test]
fn show_desktop_hides_every_window_and_a_second_use_brings_them_back_where_they_were() {
    for s in SCALES {
        let (_t, mut c) = rig(s);
        let b = open_three(&mut c, false);
        let sc = b.scale;
        let before = state(&c)["windows"].clone();
        let shown = c.capture("shown.png");
        let own = |p: [u8; 3]| p == [200, 200, 40];
        assert!(shown.px.chunks(3).filter(|p| own([p[0], p[1], p[2]])).count() > 500, "beta is on screen to begin with");

        // From the Deskbar: a secondary press on its header, away from any application.
        let header = b.rows.iter().find(|r| r.kind == "header").unwrap();
        let n = menus(&c);
        point(&c, header.rect, sc);
        c.inject("rclick");
        let m = wait_menu(&c, n);
        assert!(m.has("Show_Desktop") && !m.has("Restore_Windows"), "{:?}", m.texts());
        choose(&c, &m, "Show_Desktop", sc);
        wait_for("every window to go", || titles(&c).is_empty());
        std::thread::sleep(Duration::from_millis(300));
        let empty = c.capture("empty.png");
        let colours = [[200, 40, 200], [40, 40, 200], [200, 200, 40]];
        for col in colours {
            assert_eq!(empty.px.chunks(3).filter(|p| [p[0], p[1], p[2]] == col).count(), 0, "scale {s}: {col:?} is still on the desktop");
        }
        // the windows are not gone: the Deskbar still lists them, and says they are hidden
        let hb = settled(&c);
        for t in ["alpha one", "alpha two", "beta one"] {
            assert_eq!(hb.win(t).meta, "hidden", "scale {s}: {t} is listed, marked hidden");
        }
        // two things must not render alike: a hidden row's pixels differ from the same row visible
        let r = b.win("beta one").rect;
        let (mut changed, mut total) = (0, 0);
        for y in r[1]..r[1] + r[3] { for x in r[0]..r[0] + r[2] { total += 1; changed += (shown.at(x as usize, y as usize) != empty.at(x as usize, y as usize)) as usize; } }
        assert!(changed * 25 > total, "scale {s}: the hidden row looks like the visible one ({changed}/{total} pixels differ)");
        // the desktop menu offers the way back, by name
        let n = menus(&c);
        c.inject("pointer 20 20");
        c.inject("rclick");
        let m = wait_menu(&c, n);
        assert!(m.has("Restore_Windows") && !m.has("Show_Desktop"), "{:?}", m.texts());
        choose(&c, &m, "Restore_Windows", sc);
        wait_for("the windows to return", || titles(&c).len() == 3);
        assert_eq!(state(&c)["windows"], before, "scale {s}: same windows, same places, same order");
        std::thread::sleep(Duration::from_millis(500));
        let back = c.capture("back.png");
        for col in colours {
            assert!(back.px.chunks(3).filter(|p| [p[0], p[1], p[2]] == col).count() > 100, "scale {s}: {col:?} did not come back");
        }
    }
}

#[test]
fn a_hidden_windows_row_brings_it_back_alone() {
    let (_t, mut c) = rig("1");
    let b = open_three(&mut c, false);
    let n = menus(&c);
    c.inject("pointer 20 20");
    c.inject("rclick");
    let m = wait_menu(&c, n);
    choose(&c, &m, "Show_Desktop", b.scale);
    wait_for("every window to go", || titles(&c).is_empty());
    let hb = settled(&c);
    point(&c, hb.win("beta one").rect, hb.scale);
    c.inject("click");
    wait_for("beta to return", || titles(&c) == ["beta one"]);
    let after = settled(&c);
    assert_eq!(after.win("alpha one").meta, "hidden");
    assert_eq!(after.win("beta one").meta, "", "the window that came back is no longer marked hidden");
}

// ---- the dock -------------------------------------------------------------------------------------

fn fake_registry(dir: &Path) -> PathBuf {
    let path = dir.join("bus.sock");
    let l = UnixListener::bind(&path).unwrap();
    let apps = serde_json::json!([
        {"id": LUMEN, "name": "Lumen", "version": "0.1.0", "verbs": ["resize@v1"]},
        {"id": "org.aurora.empty", "name": "Hollow", "version": "0.1.0", "verbs": ["hum@v1"]},
    ]);
    let verbs = serde_json::json!([
        {"name": "resize@v1", "app": LUMEN, "effect": "write", "accepts": ["image/png"], "summary": "Scale an image"},
        {"name": "hum@v1", "app": "org.aurora.empty", "effect": "read", "accepts": ["image/png"], "summary": "Hum"},
    ]);
    std::thread::spawn(move || {
        for s in l.incoming().flatten() {
            let (apps, verbs) = (apps.clone(), verbs.clone());
            std::thread::spawn(move || {
                let mut line = String::new();
                let _ = BufReader::new(&s).read_line(&mut line);
                let Ok(req) = serde_json::from_str::<serde_json::Value>(&line) else { return };
                let payload = match (req["to"].as_str(), req["verb"].as_str()) {
                    (Some("registry"), Some("registry.apps@v1")) => serde_json::json!({"apps": apps}),
                    (Some("registry"), Some("registry.verbs@v1")) => serde_json::json!({"verbs": verbs}),
                    _ => return,
                };
                let reply = serde_json::json!({"id": "s1", "kind": "response", "corr": req["id"], "verb": req["verb"], "payload": payload});
                let _ = writeln!(&s, "{reply}");
            });
        }
    });
    path
}

fn dock_rig(scale: &str) -> (Theme, tempfile::TempDir, Comp, f64, Vec<[i32; 4]>) {
    let d = tempfile::tempdir().unwrap();
    let t = Theme::new();
    t.append(&format!("{BASE}[deskbar]\nenabled = false\n"));
    let bus = fake_registry(d.path());
    let c = Comp::start_shell(&["--theme", t.path(), "--bus-socket", bus.to_str().unwrap(), "--freeze-time", "--scale", scale]);
    wait_for("the dock to list both apps", || dock_layout(&c.log()).is_some_and(|l| l.3.len() == 2));
    wait_for("the registry details", || c.log().contains("registry.apps@v1 -> Ok, 2 app(s)"));
    std::thread::sleep(Duration::from_millis(300));
    let l = dock_layout(&c.log()).unwrap();
    (t, d, c, l.0, l.3)
}

#[test]
fn a_secondary_click_on_a_running_dock_item_offers_its_windows_and_keeps_its_verbs() {
    for s in SCALES {
        let (_t, _d, mut c, sc, items) = dock_rig(s);
        c.client_args(&["--size", "300x200", "--app-id", LUMEN, "--title", "lumen one", "--rgb", "200,40,200"]);
        c.client_args(&["--size", "300x200", "--app-id", LUMEN, "--title", "lumen two", "--rgb", "40,40,200"]);
        c.client_args(&["--size", "300x200", "--app-id", "org.other", "--title", "other", "--rgb", "200,200,40"]);
        let n = menus(&c);
        point(&c, items[0], sc);
        c.inject("rclick");
        let m = wait_menu(&c, n);
        println!("scale {s}: running item menu {:?}", m.texts());
        assert!(m.has("Close_All_(2)") && m.has("Bring_to_Front"), "{:?}", m.texts());
        assert!(m.has("resize@v1"), "what the item offered before is still offered: {:?}", m.texts());
        choose(&c, &m, "Close_All_(2)", sc);
        wait_for("lumen's windows to close", || titles(&c) == ["other"]);
        std::thread::sleep(Duration::from_millis(300));
        // not running any more: a secondary click is the old click, verbs and nothing about windows
        let n = menus(&c);
        point(&c, items[0], sc);
        c.inject("rclick");
        let m = wait_menu(&c, n);
        assert!(m.has("resize@v1") && !m.has("Bring_to_Front") && !m.texts().iter().any(|t| t.starts_with("Close")), "{:?}", m.texts());
    }
}

#[test]
fn a_dock_item_that_is_not_running_keeps_exactly_its_old_menu() {
    let (_t, _d, mut c, sc, items) = dock_rig("1");
    c.client_args(&["--size", "300x200", "--app-id", LUMEN, "--title", "lumen one", "--rgb", "200,40,200"]);
    let n = menus(&c);
    point(&c, items[1], sc); // Hollow: nothing of it is running
    c.inject("rclick");
    let m = wait_menu(&c, n);
    assert!(m.has("hum@v1") && !m.has("Bring_to_Front") && !m.texts().iter().any(|t| t.starts_with("Close")), "{:?}", m.texts());
}

#[test]
fn bring_to_front_from_the_dock_raises_the_apps_window() {
    let (_t, _d, mut c, sc, items) = dock_rig("1");
    c.client_args(&["--size", "300x200", "--app-id", LUMEN, "--title", "lumen", "--rgb", "200,40,200"]);
    c.client_args(&["--size", "300x200", "--app-id", "org.other", "--title", "other", "--rgb", "40,40,200"]);
    c.inject("move 0 100 100");
    c.inject("move 1 150 140");
    std::thread::sleep(Duration::from_millis(500));
    let probe = |c: &Comp| c.capture("p.png").at((250.0 * sc) as usize, (200.0 * sc) as usize);
    assert_eq!(probe(&c), [40, 40, 200], "the other window starts in front");
    let n = menus(&c);
    point(&c, items[0], sc);
    c.inject("rclick");
    let m = wait_menu(&c, n);
    choose(&c, &m, "Bring_to_Front", sc);
    wait_for("lumen in front", || { std::thread::sleep(Duration::from_millis(200)); probe(&c) == [200, 40, 200] });
}
