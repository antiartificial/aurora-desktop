//! A dock click asks "which verb" in the compositor, not in a terminal. Real headless
//! compositors, a fake registry that answers `registry.apps@v1` and `registry.verbs@v1`,
//! decoded captures, and a fake terminal and launcher that record whether they ran.
//! Everything runs at scale 1 and at a fractional scale.

mod common;
use common::*;
use std::{
    io::{BufRead, BufReader, Write},
    os::unix::net::UnixListener,
    path::{Path, PathBuf},
    time::Duration,
};

// [deskbar] off: this file diffs frames of the dock and the verb menu. The Deskbar draws
// whenever enabled, with no windows included, so it would appear in every before/after pair.
// Its own tests are in tests/deskbar.rs; nothing here is relaxed to accommodate it.
const FLAT: &str = "[background]\nmode = \"solid\"\ntop = [0.30, 0.0, 0.0]\n[desktop]\nclock = false\nhostname = false\n[deskbar]\nenabled = false\n";
const SCALES: [&str; 2] = ["1", "1.45"];
const LUMEN: &str = "org.aurora.lumen";

/// A bus that answers the registry's two reads the way the controller does. With
/// `verbs_ok` false it answers only `registry.apps@v1`, like a controller too old to know the other.
fn fake_registry(dir: &Path, verbs_ok: bool) -> PathBuf {
    let path = dir.join("bus.sock");
    let l = UnixListener::bind(&path).unwrap();
    let apps = serde_json::json!([
        {"id": LUMEN, "name": "Lumen", "version": "0.1.0", "verbs": ["resize@v1", "histogram@v1", "purge@v1"]},
        {"id": "org.aurora.empty", "name": "Hollow", "version": "0.1.0", "verbs": []},
    ]);
    let verbs = serde_json::json!([
        {"name": "resize@v1", "app": LUMEN, "effect": "write", "accepts": ["image/png"], "summary": "Scale an image"},
        {"name": "histogram@v1", "app": LUMEN, "effect": "read", "accepts": ["image/png"], "summary": "Count pixels per level"},
        {"name": "purge@v1", "app": LUMEN, "effect": "destructive", "accepts": ["image/png"], "summary": "Erase the original"},
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
                    (Some("registry"), Some("registry.verbs@v1")) if verbs_ok => serde_json::json!({"verbs": verbs}),
                    _ => return,
                };
                let reply = serde_json::json!({"id": "s1", "kind": "response", "corr": req["id"], "verb": req["verb"], "payload": payload});
                let _ = writeln!(&s, "{reply}");
            });
        }
    });
    path
}

fn script(path: &Path, body: &str) {
    std::fs::write(path, format!("#!/bin/sh\n{body}\n")).unwrap();
    std::fs::set_permissions(path, std::os::unix::fs::PermissionsExt::from_mode(0o755)).unwrap();
}

struct Rig {
    c: Comp,
    _t: Theme,
    d: tempfile::TempDir,
    /// Written by the fake terminal / the fake launcher when they run.
    term_out: PathBuf,
    aurora_out: PathBuf,
}

/// `terminal`: whether the dock has a (fake) terminal configured.
fn rig(scale: &str, terminal: bool, verbs_ok: bool) -> Rig {
    let d = tempfile::tempdir().unwrap();
    let (term, aurora) = (d.path().join("fake-term"), d.path().join("fake-aurora"));
    let (term_out, aurora_out) = (d.path().join("term.txt"), d.path().join("aurora.txt"));
    script(&term, &format!("echo \"$@\" > {}", term_out.display()));
    script(&aurora, &format!("echo \"$@\" > {}", aurora_out.display()));
    let t = Theme::new();
    t.append(&format!("{FLAT}[dock]\nterminal = \"{}\"\n", if terminal { term.to_str().unwrap() } else { "" }));
    let bus = fake_registry(d.path(), verbs_ok);
    let c = Comp::start_shell(&["--theme", t.path(), "--bus-socket", bus.to_str().unwrap(), "--freeze-time", "--scale", scale, "--aurora", aurora.to_str().unwrap()]);
    Rig { c, _t: t, d, term_out, aurora_out }
}

struct Menu {
    scale: f64,
    panel: [i32; 4],
    rows: Vec<[i32; 4]>,
    /// (text with `_` for spaces, x, y, width), device pixels
    text: Vec<(String, i32, i32, i32)>,
    n: usize,
}

fn nums(s: &str) -> Vec<i32> {
    s.split(',').filter_map(|n| n.parse().ok()).collect()
}

fn menu_layout(log: &str) -> Option<Menu> {
    let all: Vec<&str> = log.lines().filter(|l| l.contains("menu: layout")).collect();
    let line = *all.last()?;
    let p = nums(line.split("panel=").nth(1)?.split_whitespace().next()?);
    let rows = line.split("rows=[").nth(1)?.split(']').next()?.split(';').filter(|s| !s.is_empty()).map(|r| { let v = nums(r); [v[0], v[1], v[2], v[3]] }).collect();
    let text = line.split("text=").last()?.split('\u{1f}').filter(|t| !t.is_empty()).map(|t| { let (s, p) = t.rsplit_once('@').unwrap(); let v = nums(p); (s.to_string(), v[0], v[1], v[2]) }).collect();
    Some(Menu { scale: line.split("scale=").nth(1)?.split_whitespace().next()?.parse().ok()?, panel: [p[0], p[1], p[2], p[3]], rows, text, n: all.len() })
}

fn wait_menu(c: &Comp, after: usize) -> Menu {
    wait_for("a menu layout", || menu_layout(&c.log()).is_some_and(|m| m.n > after));
    std::thread::sleep(Duration::from_millis(350)); // the appear animation is over
    menu_layout(&c.log()).unwrap()
}

fn menus(c: &Comp) -> usize {
    c.log().lines().filter(|l| l.contains("menu: layout")).count()
}

fn centre(r: [i32; 4], scale: f64) -> (f64, f64) {
    ((r[0] + r[2] / 2) as f64 / scale, (r[1] + r[3] / 2) as f64 / scale)
}

fn point(c: &Comp, p: (f64, f64)) {
    c.inject(&format!("pointer {:.3} {:.3}", p.0, p.1));
}

fn texts(m: &Menu) -> Vec<&str> {
    m.text.iter().map(|t| t.0.as_str()).collect()
}

fn row_of(m: &Menu, name: &str) -> usize {
    let t = m.text.iter().find(|t| t.0 == name).unwrap_or_else(|| panic!("no text {name:?} in {:?}", texts(m)));
    m.rows.iter().position(|r| t.2 >= r[1] && t.2 < r[1] + r[3]).unwrap()
}

fn dock_ready(c: &Comp) -> (f64, [i32; 4], Vec<[i32; 4]>) {
    wait_for("the dock to list both apps", || dock_layout(&c.log()).is_some_and(|l| l.3.len() == 2));
    wait_for("the registry details", || c.log().contains("registry.apps@v1 -> Ok, 2 app(s)"));
    std::thread::sleep(Duration::from_millis(300));
    let l = dock_layout(&c.log()).unwrap();
    (l.0, l.1, l.3)
}

fn click_dock_item(c: &Comp, scale: f64, item: [i32; 4]) -> Menu {
    let n = menus(c);
    point(c, centre(item, scale));
    c.inject("click");
    wait_menu(c, n)
}

fn inside(r: [i32; 4], x: usize, y: usize) -> bool {
    (x as i32) >= r[0] && (x as i32) < r[0] + r[2] && (y as i32) >= r[1] && (y as i32) < r[1] + r[3]
}

/// Pixels in `r` that look like the tag colour for `kind`: reddish (danger), amber (caution), or none.
fn coloured(img: &Img, r: [i32; 4], kind: &str) -> usize {
    let mut n = 0;
    for y in 0..img.h {
        for x in 0..img.w {
            if !inside(r, x, y) {
                continue;
            }
            let [pr, pg, pb] = img.at(x, y);
            let hit = match kind {
                "danger" => pr > 200 && (90..180).contains(&pg) && (80..170).contains(&pb) && pr as i32 - pg as i32 > 60,
                _ => pr > 200 && pg > 150 && pb < 140 && pr as i32 - pb as i32 > 90,
            };
            n += hit as usize;
        }
    }
    n
}

fn gone(p: &Path) -> bool {
    std::thread::sleep(Duration::from_millis(700));
    !p.exists()
}

#[test]
fn a_dock_click_shows_that_apps_verbs_with_effect_and_spawns_no_terminal() {
    for s in SCALES {
        let r = rig(s, true, true);
        let c = &r.c;
        let (scale, dock, items) = dock_ready(c);
        // Hover first, so the dock's own highlight is in the "before" frame.
        point(c, centre(items[0], scale));
        std::thread::sleep(Duration::from_millis(300));
        let before = c.capture("before.png");
        let m = click_dock_item(c, scale, items[0]);
        let open = c.capture("open.png");
        if let Some(d) = std::env::var_os("AURORA_CHROME_SAVE") {
            open.save(&Path::new(&d).join(format!("verbs-{s}.png")));
        }
        println!("VERBS scale {s}: panel {:?} text {:?}", m.panel, texts(&m));
        // 1. The right verbs, with their effect class, and nothing from the other app.
        for v in ["resize@v1", "histogram@v1", "purge@v1"] {
            assert!(texts(&m).contains(&v), "{v} missing at scale {s}: {:?}", texts(&m));
        }
        for (tag, verb) in [("write", "resize@v1"), ("read", "histogram@v1"), ("!!_destructive", "purge@v1")] {
            let t = m.text.iter().find(|t| t.0 == tag).unwrap_or_else(|| panic!("no tag {tag:?}: {:?}", texts(&m)));
            assert_eq!(row_of(&m, tag), row_of(&m, verb), "{tag} sits on the {verb} row");
            assert!(t.1 > m.text.iter().find(|x| x.0 == verb).unwrap().1, "the tag is to the right of the name");
        }
        assert_eq!(m.rows.len(), 4, "a header and three verbs");
        assert_eq!(m.scale, scale);
        // 2. It opens beside the dock item (above a bottom dock), inside the screen.
        assert!(m.panel[1] + m.panel[3] <= dock[1] && m.panel[0] >= 0 && m.panel[0] + m.panel[2] <= W as i32 * 2, "{:?} vs dock {dock:?}", m.panel);
        // 3. It is in the capture, and nothing outside it changed.
        let (mut inn, mut out) = (0, 0);
        for y in 0..before.h {
            for x in 0..before.w {
                if before.at(x, y) != open.at(x, y) {
                    if inside(m.panel, x, y) { inn += 1 } else { out += 1 }
                }
            }
        }
        assert_eq!(out, 0, "only the menu changed (scale {s})");
        assert!(inn * 10 > (m.panel[2] * m.panel[3]) as usize * 9, "glass covers the panel");
        // 4. The effect is visible in the pixels: danger is red, write is amber, read is neither.
        let tag_box = |name: &str| { let t = m.text.iter().find(|t| t.0 == name).unwrap(); [t.1 - 2, t.2 - 2, t.3 + 4, (14.0 * scale) as i32] };
        let (danger, write, read) = (coloured(&open, tag_box("!!_destructive"), "danger"), coloured(&open, tag_box("write"), "caution"), coloured(&open, tag_box("read"), "danger") + coloured(&open, tag_box("read"), "caution"));
        println!("  tag ink at scale {s}: destructive {danger} red px, write {write} amber px, read {read} coloured px");
        assert!(danger > 15 && write > 10 && read == 0, "effect colours: {danger} {write} {read}");
        // 5. No terminal was spawned, and no launcher ran, to ask which verb.
        assert!(gone(&r.term_out) && !r.aurora_out.exists(), "a dock click ran something");
        assert!(!c.log().contains("dock: launched"), "{}", c.log());
        // 6. Escape closes it and the desktop is exactly as before.
        c.inject("key Escape");
        std::thread::sleep(Duration::from_millis(300));
        assert_eq!(c.capture("closed.png").diff_count(&before), 0, "Escape restores the frame");
        assert!(gone(&r.term_out) && !r.aurora_out.exists());
        drop(r.d);
    }
}

#[test]
fn choosing_a_verb_with_the_pointer_runs_that_verb_in_the_terminal_powerbox() {
    for s in SCALES {
        let r = rig(s, true, true);
        let c = &r.c;
        let (scale, _, items) = dock_ready(c);
        let m = click_dock_item(c, scale, items[0]);
        assert!(gone(&r.term_out));
        let row = m.rows[row_of(&m, "purge@v1")];
        point(c, centre(row, scale));
        std::thread::sleep(Duration::from_millis(250));
        c.inject("click");
        wait_for("the terminal to run the launcher", || r.term_out.exists() && !std::fs::read_to_string(&r.term_out).unwrap_or_default().is_empty());
        let got = std::fs::read_to_string(&r.term_out).unwrap();
        println!("VERB click scale {s}: terminal argv: {}", got.trim());
        assert!(got.contains("fake-aurora open --pick --app org.aurora.lumen --verb purge@v1"), "{got}");
        std::thread::sleep(Duration::from_millis(300));
        let after = menus(c);
        point(c, (40.0, 40.0));
        std::thread::sleep(Duration::from_millis(300));
        assert_eq!(menus(c), after, "the menu closed when a verb was chosen");
    }
}

#[test]
fn keyboard_up_down_enter_chooses_a_verb_and_runs_the_launcher_directly_without_a_terminal() {
    let r = rig("1", false, true);
    let c = &r.c;
    let (scale, _, items) = dock_ready(c);
    click_dock_item(c, scale, items[0]);
    // Enter with nothing highlighted does nothing; Down, Down, Down then Up lands on the second verb.
    c.inject("key Return");
    for k in ["Down", "Down", "Down", "Up"] {
        c.inject(&format!("key {k}"));
    }
    assert!(gone(&r.aurora_out), "nothing is chosen until Enter");
    c.inject("key Return");
    wait_for("the launcher to run", || r.aurora_out.exists() && !std::fs::read_to_string(&r.aurora_out).unwrap_or_default().is_empty());
    let got = std::fs::read_to_string(&r.aurora_out).unwrap();
    assert_eq!(got.trim(), "open --pick --app org.aurora.lumen --verb histogram@v1", "no terminal configured: the launcher runs as is");
    assert!(!r.term_out.exists());
}

#[test]
fn an_app_with_no_verbs_says_so_in_a_disabled_row_that_does_nothing() {
    for s in SCALES {
        let r = rig(s, true, true);
        let c = &r.c;
        let (scale, _, items) = dock_ready(c);
        let m = click_dock_item(c, scale, items[1]);
        assert!(texts(&m).contains(&"No_verbs"), "{:?}", texts(&m));
        let row = m.rows[row_of(&m, "No_verbs")];
        let before = c.capture("b.png");
        point(c, centre(row, scale));
        std::thread::sleep(Duration::from_millis(300));
        c.inject("key Down");
        c.inject("key Return");
        c.inject("click");
        assert!(gone(&r.term_out) && !r.aurora_out.exists(), "a disabled row ran something");
        let _ = before;
        // Its reason is drawn (ink right of the label inside the row), dimmer than a live row's.
        let img = c.capture("hollow.png");
        let label = m.text.iter().find(|t| t.0 == "No_verbs").unwrap();
        let reason_box = [label.1 + label.3 + (8.0 * scale) as i32, row[1], row[0] + row[2] - (label.1 + label.3) - (8.0 * scale) as i32, row[3]];
        let lit = (reason_box[1]..reason_box[1] + reason_box[3]).flat_map(|y| (reason_box[0]..reason_box[0] + reason_box[2]).map(move |x| (x, y))).filter(|&(x, y)| img.at(x as usize, y as usize).iter().all(|&v| v > 90)).count();
        assert!(lit > 20, "the reason is drawn ({lit} px) at scale {s}");
    }
}

#[test]
fn a_registry_that_will_not_say_what_the_effects_are_is_shown_as_unknown_not_as_safe() {
    let r = rig("1", true, false);
    let c = &r.c;
    let (scale, _, items) = dock_ready(c);
    let m = click_dock_item(c, scale, items[0]);
    let unknown = m.text.iter().filter(|t| t.0 == "effect_unknown").count();
    assert_eq!(unknown, 3, "{:?}", texts(&m));
    assert!(!texts(&m).contains(&"read"), "an unknown effect must not read as read");
    assert!(gone(&r.term_out));
}
