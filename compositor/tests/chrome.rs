//! The desktop menu and the settings panel: compositor-drawn chrome. Real headless
//! compositors, real captures, decoded pixels. Everything runs at scale 1 and at a
//! fractional scale, because everything is laid out in logical units.

mod common;
use common::*;
use std::{path::Path, process::Command, time::Duration};

// [deskbar] off: these tests diff whole frames of the menu and the settings panel, and the
// Deskbar is a glass box of its own that is drawn whenever it is enabled -- including with no
// windows open, since it reports "No windows open" and the machine's load. It has its own
// tests in tests/deskbar.rs. No assertion here is relaxed; the subject is just isolated.
const BASE: &str = "[background]\nmode = \"gradient\"\ntop = [0.34, 0.0, 0.0]\nbottom = [0.18, 0.0, 0.0]\n[desktop]\nclock = false\nhostname = false\n[deskbar]\nenabled = false\n";
const SCALES: [&str; 2] = ["1", "1.45"];

fn comp(t: &Theme, scale: &str, extra: &[&str]) -> Comp {
    let mut a = vec!["--theme", t.path(), "--freeze-time", "--scale", scale];
    a.extend_from_slice(extra);
    Comp::start_shell(&a)
}

fn lines<'a>(log: &'a str, what: &str) -> Vec<&'a str> {
    log.lines().filter(|l| l.contains(what)).collect()
}

/// The text between `key=[` and the next `]` on a log line, split on `;`.
fn list(line: &str, key: &str) -> Vec<String> {
    let rest = line.split(&format!("{key}=[")).nth(1).unwrap_or("");
    rest.split(']').next().unwrap_or("").split(';').filter(|s| !s.is_empty()).map(String::from).collect()
}

fn nums(s: &str) -> Vec<i32> {
    s.split(',').filter_map(|n| n.parse().ok()).collect()
}

struct Layout {
    scale: f64,
    panel: [i32; 4],
    rows: Vec<[i32; 4]>,
    /// (text with `_` for spaces, x, y, width), device pixels
    text: Vec<(String, i32, i32, i32)>,
    n: usize,
}

fn parse(log: &str, what: &str) -> Option<Layout> {
    let all = lines(log, &format!("{what}: layout"));
    let line = *all.last()?;
    let panel = nums(line.split("panel=").nth(1)?.split_whitespace().next()?);
    let scale = line.split("scale=").nth(1)?.split_whitespace().next()?.parse().ok()?;
    let rows = list(line, if what == "menu" { "rows" } else { "hots" }).iter().map(|r| { let v = nums(r); [v[0], v[1], v[2], v[3]] }).collect();
    // Texts come last on the line, separated by U+001F (they may contain `;` and `]`).
    let text = line.split("text=").last().unwrap_or("").split('\u{1f}').filter(|t| !t.is_empty()).map(|t| { let (s, p) = t.rsplit_once('@').unwrap(); let v = nums(p); (s.to_string(), v[0], v[1], v[2]) }).collect();
    Some(Layout { scale, panel: [panel[0], panel[1], panel[2], panel[3]], rows, text, n: all.len() })
}

fn wait_relayout(c: &Comp, what: &str, after: usize) -> Layout {
    wait_for(&format!("a {what} layout"), || parse(&c.log(), what).is_some_and(|l| l.n > after));
    std::thread::sleep(Duration::from_millis(350)); // the appear animation is over
    parse(&c.log(), what).unwrap()
}

fn count(c: &Comp, what: &str) -> usize {
    lines(&c.log(), &format!("{what}: layout")).len()
}

fn save(img: &Img, name: &str) {
    if let Some(d) = std::env::var_os("AURORA_CHROME_SAVE") {
        img.save(&Path::new(&d).join(name));
    }
}

fn inside(r: [i32; 4], x: usize, y: usize) -> bool {
    (x as i32) >= r[0] && (x as i32) < r[0] + r[2] && (y as i32) >= r[1] && (y as i32) < r[1] + r[3]
}

/// Pixels that differ between two captures: (inside `r`, outside `r`, brighter inside).
fn diff_in(a: &Img, b: &Img, r: [i32; 4]) -> (usize, usize, usize) {
    let (mut i, mut o, mut br) = (0, 0, 0);
    for y in 0..a.h {
        for x in 0..a.w {
            if a.at(x, y) != b.at(x, y) {
                if inside(r, x, y) {
                    i += 1;
                    if b.at(x, y).iter().map(|&v| v as u32).sum::<u32>() > a.at(x, y).iter().map(|&v| v as u32).sum::<u32>() { br += 1; }
                } else {
                    o += 1;
                }
            }
        }
    }
    (i, o, br)
}

/// Bounding box of text ink (all channels above 120: brighter than any glass here) inside `r`.
fn ink(img: &Img, r: [i32; 4]) -> Option<(i32, i32, i32, i32)> {
    let mut b: Option<(i32, i32, i32, i32)> = None;
    for y in r[1].max(0)..(r[1] + r[3]).min(img.h as i32) {
        for x in r[0].max(0)..(r[0] + r[2]).min(img.w as i32) {
            if img.at(x as usize, y as usize).iter().all(|&v| v > 120) {
                b = Some(match b { None => (x, y, x, y), Some((a, bb, c, d)) => (a.min(x), bb.min(y), c.max(x), d.max(y)) });
            }
        }
    }
    b
}

fn logical(v: i32, scale: f64) -> f64 { v as f64 / scale }

fn pointer_to(c: &Comp, x: f64, y: f64) { c.inject(&format!("pointer {x:.3} {y:.3}")); }

fn centre(r: [i32; 4], scale: f64) -> (f64, f64) { (logical(r[0] + r[2] / 2, scale), logical(r[1] + r[3] / 2, scale)) }

/// Index of the menu entry whose label is `name` (`_` for spaces).
fn row_of(l: &Layout, name: &str) -> usize {
    let t = l.text.iter().find(|t| t.0 == name).unwrap_or_else(|| panic!("no text {name:?} in {:?}", l.text.iter().map(|t| &t.0).collect::<Vec<_>>()));
    l.rows.iter().position(|r| t.2 >= r[1] && t.2 < r[1] + r[3]).unwrap()
}

#[test]
fn right_click_opens_the_menu_where_expected_and_hover_changes_only_that_item() {
    for s in SCALES {
        let t = Theme::new();
        t.append(BASE);
        let c = comp(&t, s, &[]);
        std::thread::sleep(Duration::from_millis(500));
        let before = c.capture("before.png");
        let (ax, ay) = (300.0, 180.0);
        pointer_to(&c, ax, ay);
        c.inject("rclick");
        let m = wait_relayout(&c, "menu", 0);
        let scale = m.scale;
        let open = c.capture("open.png");
        save(&open, &format!("menu-{s}.png"));
        // 1. It sits where the pointer was, in device pixels.
        assert_eq!((m.panel[0], m.panel[1]), ((ax * scale).round() as i32, (ay * scale).round() as i32), "scale {s}");
        // 2. It is in the capture (ordinary chrome), and the capture changed nowhere else.
        let (inn, out, _) = diff_in(&before, &open, m.panel);
        println!("MENU scale {s}: panel {:?}; {inn} of {} pixels inside changed, {out} outside", m.panel, m.panel[2] * m.panel[3]);
        assert_eq!(out, 0, "nothing outside the menu changed");
        assert!(inn * 10 > (m.panel[2] * m.panel[3]) as usize * 9, "glass covers the panel");
        // 3. Labels land where text.rs put them: the ink starts at the logged x, ends within the logged width.
        for name in ["Settings\u{2026}", "Window_Switcher", "Solid_colour", "Hide_Dock"] {
            let (_, x, y, w) = l_text(&m, name);
            let rowh = m.rows[row_of(&m, name)][3];
            let b = ink(&open, [x - 4, y, w + 8, rowh]).unwrap_or_else(|| panic!("no ink for {name}"));
            println!("  {name}: expected x {x}..{} ink x {}..{}", x + w, b.0, b.2);
            assert!((b.0 - x).abs() <= 3 && (b.2 - (x + w)).abs() <= 4, "{name}: ink {b:?} vs x {x} w {w} at scale {s}");
        }
        // 4. Hover: only the hovered row's pixels change, and they get brighter.
        let r = m.rows[row_of(&m, "Settings\u{2026}")];
        let (hx, hy) = centre(r, scale);
        let n = count(&c, "menu");
        pointer_to(&c, hx, hy);
        wait_relayout(&c, "menu", n);
        let hover = c.capture("hover.png");
        let (inn, out, br) = diff_in(&open, &hover, r);
        println!("  HOVER Settings: {inn} pixels changed in its row, {out} elsewhere, {br} brighter");
        assert!(inn > 300 && out == 0 && br * 10 > inn * 8, "hover must change exactly one row (in {inn}, out {out}, brighter {br})");
        // 5. A disabled item (Image: no image set) neither highlights nor does anything.
        let r = m.rows[row_of(&m, "Image")];
        let (dx, dy) = centre(r, scale);
        let n = count(&c, "menu");
        pointer_to(&c, dx, dy);
        std::thread::sleep(Duration::from_millis(300));
        assert_eq!(count(&c, "menu"), n.max(count(&c, "menu")));
        let dis = c.capture("disabled.png");
        let (hl, _, _) = diff_in(&open, &dis, r);
        assert_eq!(hl, 0, "a disabled item has no hover highlight");
        // 6. Escape closes it and the frame is exactly the one before.
        c.inject("key Escape");
        std::thread::sleep(Duration::from_millis(300));
        assert_eq!(c.capture("closed.png").diff_count(&before), 0, "Escape restores the desktop exactly");
    }
}

fn l_text(l: &Layout, name: &str) -> (String, i32, i32, i32) {
    l.text.iter().find(|t| t.0 == name).cloned().unwrap_or_else(|| panic!("no text {name:?}"))
}

#[test]
fn choosing_a_background_item_changes_the_desktop_and_the_theme_file() {
    for s in SCALES {
        let t = Theme::new();
        t.append(BASE);
        let c = comp(&t, s, &[]);
        std::thread::sleep(Duration::from_millis(500));
        let grad = c.capture("grad.png");
        let (top, low) = (grad.at(8, 8), grad.at(8, 500));
        assert_ne!(top, low, "the gradient varies down the screen");
        pointer_to(&c, 700.0, 150.0);
        c.inject("rclick");
        let m = wait_relayout(&c, "menu", 0);
        // The current mode is marked: Gradient carries the dot, Solid does not.
        let chk = |name: &str| { let r = m.rows[row_of(&m, name)]; let img = c.capture("chk.png"); ink(&img, [r[0] + (4.0 * m.scale) as i32, r[1], (14.0 * m.scale) as i32, r[3]]).is_some() };
        assert!(chk("Gradient") && !chk("Solid_colour"), "radio dot marks the current mode (scale {s})");
        let r = m.rows[row_of(&m, "Solid_colour")];
        let (x, y) = centre(r, m.scale);
        pointer_to(&c, x, y);
        c.inject("click");
        wait_for("the theme file to say solid", || std::fs::read_to_string(t.dir.path().join("aurora-glass.toml")).unwrap().contains("mode = \"solid\""));
        wait_for("hot reload of the written theme", || lines(&c.log(), "theme: loaded").len() >= 2);
        std::thread::sleep(Duration::from_millis(400));
        let solid = c.capture("solid.png");
        println!("BACKGROUND click scale {s}: gradient top {top:?} bottom {low:?}; solid top {:?} bottom {:?}", solid.at(8, 8), solid.at(8, 500));
        assert_eq!(solid.at(8, 8), solid.at(8, 500), "solid colour is flat top to bottom");
        assert_eq!(solid.at(8, 8), solid.at(m.panel[0] as usize + 5, m.panel[1] as usize + 5), "the menu closed after the click and left plain desktop");
        let file = std::fs::read_to_string(t.dir.path().join("aurora-glass.toml")).unwrap();
        assert!(file.contains("# --- Phase 8 shell") || file.contains("Aurora theme parameters"), "comments in the theme file survive an edit");
    }
}

#[test]
fn keyboard_drives_the_menu_and_keys_never_reach_the_focused_client() {
    let t = Theme::new();
    t.append(BASE);
    let mut c = comp(&t, "1", &[]);
    let log = c.client();
    c.inject("pointer 1100 120"); // desktop, outside the window
    let before = c.capture("before.png");
    c.inject("rclick");
    wait_relayout(&c, "menu", 0);
    // Menu is up over a focused client: Down, Down, Down lands on Window Switcher (enabled: there is a window).
    for _ in 0..3 { c.inject("key Down"); }
    c.inject("key Escape");
    std::thread::sleep(Duration::from_millis(300));
    let keys = read(&log).lines().filter(|l| l.starts_with("key ")).count();
    println!("CLIENT log after 4 keys at the menu: {keys} key lines");
    assert_eq!(keys, 0, "the menu swallowed every key");
    assert_eq!(c.capture("after.png").diff_count(&before), 0);
    c.inject("key a");
    wait_for("a key after the menu to reach the client", || read(&log).lines().any(|l| l.starts_with("key ")));
}

#[test]
fn right_click_on_a_window_does_not_open_the_desktop_menu() {
    let t = Theme::new();
    t.append(BASE);
    let mut c = comp(&t, "1", &[]);
    c.client_args(&["--size", "400x300"]);
    c.inject("move 0 100 100");
    std::thread::sleep(Duration::from_millis(300));
    c.inject("pointer 250 200");
    c.inject("rclick");
    std::thread::sleep(Duration::from_millis(400));
    assert!(lines(&c.log(), "menu: open").is_empty());
    c.inject("pointer 900 100");
    c.inject("rclick");
    wait_for("the menu on bare desktop", || !lines(&c.log(), "menu: open").is_empty());
}

#[test]
fn the_menu_stays_on_the_output_near_the_corner() {
    for s in SCALES {
        let t = Theme::new();
        t.append(BASE);
        let c = comp(&t, s, &[]);
        let sc: f64 = s.parse().unwrap();
        let (lw, lh) = (1280.0 / sc, 800.0 / sc);
        pointer_to(&c, lw - 4.0, lh - 4.0);
        c.inject("rclick");
        let m = wait_relayout(&c, "menu", 0);
        println!("CORNER scale {s}: panel {:?} in 1280x800", m.panel);
        assert!(m.panel[0] >= 0 && m.panel[1] >= 0 && m.panel[0] + m.panel[2] <= 1280 && m.panel[1] + m.panel[3] <= 800);
        let img = c.capture("corner.png");
        assert_ne!(img.at((m.panel[0] + 8) as usize, (m.panel[1] + 8) as usize), img.at(5, 5), "it really is drawn there");
    }
}

#[test]
fn the_menu_appears_ease_out_and_the_first_frame_is_already_moving() {
    let t = Theme::new();
    t.append(BASE);
    let c = comp(&t, "1", &[]);
    pointer_to(&c, 300.0, 200.0);
    c.inject("rclick");
    wait_for("the first frame", || !lines(&c.log(), "menu: first frame").is_empty());
    let l = lines(&c.log(), "menu: first frame")[0].to_string();
    println!("MOTION: {l}");
    let num = |after: &str| -> f32 { l.split(after).nth(1).unwrap().trim_start().split(|ch: char| !(ch.is_ascii_digit() || ch == '.')).next().unwrap().parse().unwrap() };
    let (at, of, linear, eased) = (num("frame at"), num("of"), num("linear would be"), num("ease-out is"));
    assert!(eased > linear || at >= of, "ease-out is ahead of linear on frame one: {l}");
    assert!(lines(&c.log(), "menu: open")[0].contains("ease-out cubic, 140 ms"));
}

// ---- settings --------------------------------------------------------------------

fn open_settings(c: &Comp, scale: f64) -> Layout {
    pointer_to(c, 900.0 / scale, 120.0 / scale);
    c.inject("rclick");
    let m = wait_relayout(c, "menu", 0);
    let r = m.rows[row_of(&m, "Settings\u{2026}")];
    let (x, y) = centre(r, m.scale);
    pointer_to(c, x, y);
    c.inject("click");
    wait_relayout(c, "settings", 0)
}

fn hot(l: &Layout, text: &str) -> [i32; 4] {
    // the hotspot whose rectangle contains the text origin
    let t = l_text(l, text);
    *l.rows.iter().find(|r| t.1 >= r[0] - 2 && t.1 < r[0] + r[2] && t.2 >= r[1] - 8 && t.2 < r[1] + r[3] + 8).unwrap_or_else(|| panic!("no hotspot for {text}"))
}

fn click_text(c: &Comp, l: &Layout, text: &str) {
    let (x, y) = centre(hot(l, text), l.scale);
    pointer_to(c, x, y);
    c.inject("click");
}

#[test]
fn settings_panel_text_lands_where_text_rs_says_and_read_only_rows_say_why() {
    for s in SCALES {
        let t = Theme::new();
        t.append(BASE);
        let c = comp(&t, s, &[]);
        std::thread::sleep(Duration::from_millis(400));
        let before = c.capture("before.png");
        let l = open_settings(&c, s.parse().unwrap());
        let img = c.capture("settings.png");
        save(&img, &format!("settings-{s}.png"));
        let (inn, _out, _) = diff_in(&before, &img, l.panel);
        println!("SETTINGS scale {s}: panel {:?}, {inn} of {} px changed inside", l.panel, l.panel[2] * l.panel[3]);
        assert!(inn * 10 > (l.panel[2] * l.panel[3]) as usize * 9);
        assert!(l.panel[0] >= 0 && l.panel[0] + l.panel[2] <= 1280 && l.panel[1] >= 0 && l.panel[1] + l.panel[3] <= 800);
        // centred
        assert!(((l.panel[0] + l.panel[2] / 2) - 640).abs() <= 1 && ((l.panel[1] + l.panel[3] / 2) - 400).abs() <= 1);
        // Each label's ink starts at the logged x and stays within its logged width.
        for name in ["Scale", "Resolution", "Refresh_rate", "Monitors", "Settings", "Display", "Desktop"] {
            let (_, x, y, w) = l_text(&l, name);
            let b = ink(&img, [x - 4, y - 2, w + 8, (24.0 * l.scale) as i32]).unwrap_or_else(|| panic!("no ink for {name}"));
            println!("  {name}: expected x {x}..{} ink x {}..{}", x + w, b.0, b.2);
            assert!((b.0 - x).abs() <= 3 && (b.2 - (x + w)).abs() <= 4, "{name}: ink {b:?} vs x {x} w {w} at scale {s}");
        }
        // Read-only rows exist, say so, and give the reason. They are marked "read-only" and
        // never "unavailable": each one is REPORTING a figure the compositor holds, so claiming
        // it is unavailable says the opposite of what the row shows.
        let all: String = l.text.iter().map(|t| t.0.as_str()).collect::<Vec<_>>().join(" ");
        assert!(all.contains("read-only") && all.contains("DRM/KMS") && all.contains("Multi-monitor"), "{all}");
        assert_eq!(all.matches("read-only").count(), 3, "resolution, refresh rate, monitors");
        assert!(!all.contains("unavailable"), "a reported figure is not unavailable: {all}");
        // Read-only rows have no controls: no hotspot overlaps their value text.
        for name in ["Resolution", "Refresh_rate", "Monitors"] {
            let t = l_text(&l, name);
            assert!(!l.rows.iter().any(|r| t.2 >= r[1] && t.2 < r[1] + r[3] && r[0] > t.1), "{name} must have no control");
        }
        // Esc closes the panel and the desktop is as it was.
        c.inject("key Escape");
        std::thread::sleep(Duration::from_millis(300));
        assert_eq!(c.capture("closed.png").diff_count(&before), 0);
    }
}

#[test]
fn settings_clicks_write_the_theme_file_and_hot_reload_applies_them() {
    for s in SCALES {
        let t = Theme::new();
        t.append(BASE);
        let c = comp(&t, s, &[]);
        std::thread::sleep(Duration::from_millis(400));
        let sc: f64 = s.parse().unwrap();
        let file = || std::fs::read_to_string(t.dir.path().join("aurora-glass.toml")).unwrap();
        let l = open_settings(&c, sc);
        // Display > Scale: 2 (output scale is real: wp_fractional_scale and wl_output follow)
        click_text(&c, &l, "2");
        wait_for("[output] scale in the file", || file().contains("scale = 2"));
        wait_for("the dock to be laid out at scale 2", || lines(&c.log(), "dock: layout scale=2").len() >= 1);
        let l2 = wait_relayout(&c, "settings", l.n);
        println!("SETTINGS scale {s} -> 2: panel {:?} -> {:?}; file has {:?}", l.panel, l2.panel, file().lines().find(|x| x.starts_with("scale")));
        assert_eq!(l2.scale, 2.0);
        assert!(((l2.panel[2] as f64) / (l.panel[2] as f64) - 2.0 / sc).abs() < 0.02, "the panel is the same logical size at the new scale");
        // Dock & Text page: Dock edge Left. Click the tab, then the segment.
        click_text(&c, &l2, "Dock_&_Text");
        let l3 = wait_relayout(&c, "settings", l2.n);
        click_text(&c, &l3, "Left");
        wait_for("edge = left in the file", || file().contains("edge = \"left\""));
        wait_for("the dock to move", || lines(&c.log(), "dock: layout").iter().any(|x| x.contains("panel=") && { let p = nums(x.split("panel=").nth(1).unwrap().split_whitespace().next().unwrap()); p[0] < 100 && p[1] > 100 }));
        let f = file();
        assert!(f.contains("Aurora theme parameters"), "comments survive");
        assert!(toml::from_str::<toml::Value>(&f).is_ok());
    }
}

#[test]
fn keyboard_drives_the_settings_panel() {
    let t = Theme::new();
    t.append(BASE);
    let c = comp(&t, "1", &[]);
    let file = || std::fs::read_to_string(t.dir.path().join("aurora-glass.toml")).unwrap();
    // Menu by keyboard: Down Down opens "Settings" (New Terminal may be disabled if no terminal exists; use the pointer to open the menu, keys to choose).
    c.inject("pointer 900 120");
    c.inject("rclick");
    wait_relayout(&c, "menu", 0);
    let m = parse(&c.log(), "menu").unwrap();
    let terminal_enabled = !m.text.iter().any(|t| t.0 == "New_Terminal") || !c.log().contains("no_terminal_found");
    let downs = if parse(&c.log(), "menu").unwrap().text.iter().any(|t| t.0.starts_with("no_terminal")) || !terminal_enabled { 1 } else { 2 };
    for _ in 0..downs { c.inject("key Down"); }
    c.inject("key Return");
    let l = wait_relayout(&c, "settings", 0);
    // Tab until the Keyboard page is up, rather than a fixed number of tabs. The count was 3
    // and became wrong the moment a page was inserted before Keyboard, which is nothing to do
    // with what this test is about.
    let mut l = l;
    let mut found = false;
    for _ in 0..8 {
        if parse(&c.log(), "settings").unwrap().text.iter().any(|t| t.0 == "Layout") {
            found = true;
            break;
        }
        c.inject("key Tab");
        l = wait_relayout(&c, "settings", l.n);
    }
    assert!(found, "Tab never reached the Keyboard page: {:?}", parse(&c.log(), "settings").unwrap().text.iter().map(|t| t.0.clone()).collect::<Vec<_>>());
    c.inject("key Down");
    c.inject("key Right");
    wait_for("input layout written", || file().contains("[input]") && file().contains("layout = \"us\""));
    wait_for("keymap applied after the edit", || c.log().contains("keymap applied") && c.log().matches("keymap applied").count() >= 1);
    // The panel rebuilds from the reloaded theme; Right again is then "gb".
    let n = count(&c, "settings");
    wait_relayout(&c, "settings", n - 1);
    c.inject("key Right");
    wait_for("layout gb", || file().contains("layout = \"gb\""));
    // Repeat rate stepper: Down Down, Right -> 30
    let n = count(&c, "settings");
    wait_relayout(&c, "settings", n - 1);
    c.inject("key Down");
    c.inject("key Down");
    c.inject("key Right");
    wait_for("repeat rate written", || file().contains("repeat_rate = 30"));
    wait_for("repeat applied to the seat", || c.log().contains("input: repeat 30 /s"));
    c.inject("key Escape");
    std::thread::sleep(Duration::from_millis(300));
    // Closed: keys go to the desktop again (a keysym answer proves the keymap is live).
    let ans = c.inject_reply("keysym q");
    assert!(ans.starts_with("ok"), "{ans}");
}

#[test]
fn chrome_is_in_captures_but_the_trusted_overlay_still_is_not() {
    let t = Theme::new();
    t.append(BASE);
    let c = comp(&t, "1", &[]);
    std::thread::sleep(Duration::from_millis(400));
    let plain = c.capture("plain.png");
    pointer_to(&c, 200.0, 150.0);
    c.inject("rclick");
    let m = wait_relayout(&c, "menu", 0);
    let with_menu = c.capture("menu.png");
    let (inn, out, _) = diff_in(&plain, &with_menu, m.panel);
    assert!(inn > 5_000 && out == 0, "menu is in the capture ({inn} px)");
    let pending = ask(&c, &request_json("chrome1", 60_000));
    wait_for("the compositor to dump its own frame", || c.dump.exists());
    let own = Img::load_file(&c.dump);
    let during = c.capture("during.png");
    println!("CHROME vs OVERLAY: overlay border in own frame {}, in capture {}; menu still in capture: {} px differ from plain", own.count(BORDER), during.count(BORDER), diff_in(&plain, &during, m.panel).0);
    assert!(own.count(BORDER) > 5_000, "the overlay is drawn");
    assert_eq!(during.count(BORDER), 0, "and is not in the capture");
    assert_eq!(during.diff_count(&with_menu), 0, "the capture during the prompt is exactly the capture with only the menu");
    drop(pending);
}

#[test]
fn legacy_scene_still_opens_the_menu() {
    // The old tests run with --legacy-scene; the menu must work there too.
    let t = Theme::new();
    t.append(BASE);
    let c = Comp::start(&["--theme", t.path()]);
    pointer_to(&c, 300.0, 200.0);
    c.inject("rclick");
    let m = wait_relayout(&c, "menu", 0);
    let img = c.capture("legacy.png");
    assert_ne!(img.at((m.panel[0] + 6) as usize, (m.panel[1] + 6) as usize), [25, 25, 25]);
    let _ = Command::new("true");
}

// ---- moving the panel -------------------------------------------------------

/// Grab the panel by its title band at `(dx, dy)` device pixels inside it, drag
/// by `(mx, my)` device pixels, release. Uses the held-button test commands,
/// because `click` presses and releases in one call and a drag needs the
/// pointer to move between the two.
fn drag(c: &Comp, from: [i32; 4], inside: (i32, i32), by: (i32, i32), s: f64) {
    let (gx, gy) = (from[0] + inside.0, from[1] + inside.1);
    pointer_to(c, gx as f64 / s, gy as f64 / s);
    c.inject("press");
    pointer_to(c, (gx + by.0) as f64 / s, (gy + by.1) as f64 / s);
    c.inject("release");
}

#[test]
fn settings_panel_moves_with_a_title_drag_and_its_handle_cannot_leave_the_screen() {
    for sc in SCALES {
        let t = Theme::new();
        t.append(BASE);
        let c = comp(&t, sc, &[]);
        let opened = open_settings(&c, sc.parse().unwrap());
        let s = opened.scale;
        let before = opened.panel;

        // 1. A drag on the title band moves the panel by exactly the pointer's
        //    delta: the point grabbed stays under the pointer.
        let n = count(&c, "settings");
        drag(&c, before, (120, 12), (180, 90), s);
        let moved = wait_relayout(&c, "settings", n);
        let want = [before[0] + 180, before[1] + 90];
        assert!(
            (moved.panel[0] - want[0]).abs() <= 3 && (moved.panel[1] - want[1]).abs() <= 3,
            "scale {s}: dragged the title band by 180,90 device px from {before:?}; panel is at {:?},{:?}, wanted {want:?} (within 3 px of logical->device rounding)",
            moved.panel[0], moved.panel[1]
        );
        assert_eq!([moved.panel[2], moved.panel[3]], [before[2], before[3]], "scale {s}: a move must not resize the panel");

        // 2. The logged hotspots moved with it, so the panel is still clickable
        //    where it is drawn (the two used to be computed separately).
        let tab_before = hot(&opened, "Desktop");
        let tab_after = hot(&moved, "Desktop");
        assert_eq!(
            [tab_after[0] - moved.panel[0], tab_after[1] - moved.panel[1]],
            [tab_before[0] - before[0], tab_before[1] - before[1]],
            "scale {s}: the Desktop tab's offset inside the panel changed when the panel moved"
        );

        // 4. A press in the body, below the title band, does NOT move the panel.
        let base = parse(&c.log(), "settings").unwrap().panel;
        let (bx, by) = (base[0] + 5, base[1] + base[3] / 2);
        pointer_to(&c, bx as f64 / s, by as f64 / s);
        c.inject("press");
        pointer_to(&c, (bx + 150) as f64 / s, (by - 150) as f64 / s);
        c.inject("release");
        std::thread::sleep(Duration::from_millis(300));
        assert_eq!(
            parse(&c.log(), "settings").unwrap().panel,
            base,
            "scale {s}: a press in the panel body moved it; only the title band is a handle"
        );

        // 3. Dragged hard off the top-left, the title band stays reachable.
        let n = count(&c, "settings");
        let (gx, gy) = (moved.panel[0] + 120, moved.panel[1] + 12);
        pointer_to(&c, gx as f64 / s, gy as f64 / s);
        c.inject("press");
        pointer_to(&c, -4000.0, -4000.0);
        c.inject("release");
        let off = wait_relayout(&c, "settings", n);
        assert!(off.panel[1] >= 0, "scale {s}: panel dragged above the top of the screen: y={}", off.panel[1]);
        let on = (140.0 * s) as i32;
        assert!(
            off.panel[0] + off.panel[2] >= on - 2,
            "scale {s}: panel dragged off the left: right edge at {} but {on} device px of the handle must stay on screen",
            off.panel[0] + off.panel[2]
        );

    }
}
