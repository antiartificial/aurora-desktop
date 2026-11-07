//! Window decorations: the title bar, close button, drag, resize, double-click maximise, focus, placement.
//! Real headless compositors, real Wayland (and X11) clients, driven with `--test-input` and judged on
//! the decoded pixels of the compositor's own capture plus the `deco: layout` log lines. Everything that
//! has a position runs at scale 1 and at 1.45, because bars are chrome around a LOGICAL window.

mod common;
use common::*;
use std::{process::{Child, Command, Stdio}, time::Duration};

const BASE: &str = "[background]\nmode = \"gradient\"\ntop = [0.34, 0.0, 0.0]\nbottom = [0.18, 0.0, 0.0]\n[desktop]\nclock = false\nhostname = false\n";
const SCALES: [&str; 2] = ["1", "1.45"];
/// The client's solid colour (`testclient`'s default).
const CLIENT_RGB: [u8; 3] = CLIENT;
const BAR_H: i32 = 30;

fn comp(t: &Theme, scale: &str, extra: &[&str]) -> Comp {
    let mut a = vec!["--theme", t.path(), "--freeze-time", "--scale", scale];
    a.extend_from_slice(extra);
    Comp::start_shell(&a)
}

fn themed(scale: &str, extra_theme: &str, extra: &[&str]) -> (Theme, Comp) {
    let t = Theme::new();
    t.append(BASE);
    if !extra_theme.is_empty() {
        t.append(extra_theme);
    }
    let c = comp(&t, scale, extra);
    std::thread::sleep(Duration::from_millis(400));
    (t, c)
}

fn last_for(c: &Comp, text: &str) -> Option<Deco> {
    decos(&c.log()).into_iter().rev().find(|d| d.text == text)
}

fn wait_deco(c: &Comp, text: &str) -> Deco {
    wait_for(&format!("a title bar for {text:?}"), || last_for(c, text).is_some());
    std::thread::sleep(Duration::from_millis(350)); // the ease is over
    last_for(c, text).unwrap()
}

/// The windows as the compositor has them, logical units: (title, x, y, w, h).
fn windows(c: &Comp) -> Vec<(String, i32, i32, i32, i32)> {
    let r = c.inject_reply("state");
    let v: serde_json::Value = serde_json::from_str(r.strip_prefix("ok ").unwrap()).unwrap();
    v["windows"].as_array().unwrap().iter().map(|w| (w["title"].as_str().unwrap().into(), w["x"].as_i64().unwrap() as i32, w["y"].as_i64().unwrap() as i32, w["w"].as_i64().unwrap() as i32, w["h"].as_i64().unwrap() as i32)).collect()
}

fn win_of(c: &Comp, title: &str) -> (i32, i32, i32, i32) {
    let w = windows(c).into_iter().find(|w| w.0 == title).unwrap_or_else(|| panic!("no window {title:?}"));
    (w.1, w.2, w.3, w.4)
}

fn at(c: &Comp, x: f64, y: f64) { c.inject(&format!("pointer {x:.3} {y:.3}")); }

/// Press at `from`, move to `to` in steps, release.
fn drag(c: &Comp, from: (f64, f64), to: (f64, f64)) {
    at(c, from.0, from.1);
    c.inject("press");
    for i in 1..=6 {
        let k = i as f64 / 6.0;
        at(c, from.0 + (to.0 - from.0) * k, from.1 + (to.1 - from.1) * k);
    }
    c.inject("release");
    c.step_frames(3);
}

fn inside(r: [i32; 4], x: usize, y: usize) -> bool {
    (x as i32) >= r[0] && (x as i32) < r[0] + r[2] && (y as i32) >= r[1] && (y as i32) < r[1] + r[3]
}

fn union_has(rs: &[[i32; 4]], x: usize, y: usize) -> bool { rs.iter().any(|r| inside(*r, x, y)) }

/// Pixels that differ between two captures, inside the given rectangles and outside all of them.
fn diff(a: &Img, b: &Img, rs: &[[i32; 4]]) -> (usize, usize) {
    let (mut i, mut o) = (0, 0);
    for y in 0..a.h {
        for x in 0..a.w {
            if a.at(x, y) != b.at(x, y) {
                if union_has(rs, x, y) { i += 1 } else { o += 1 }
            }
        }
    }
    (i, o)
}

fn px(v: f64, s: f64) -> i32 { (v * s).round() as i32 }

fn save(img: &Img, name: &str) {
    if let Some(d) = std::env::var_os("AURORA_DECO_SAVE") {
        img.save(&std::path::Path::new(&d).join(name));
    }
}

/// Where a title can be: inside the bar, right of the close button, clear of the glass's own edge highlight.
fn title_area(d: &Deco) -> [i32; 4] {
    [d.close[0] + d.close[2], d.bar[1] + 3, d.bar[2] - d.close[2] - (14.0 * d.scale).round() as i32, d.bar[3] - 6]
}

/// Bounding box of text ink (all channels above 150: brighter than any chrome glass here, and not the yellow stripe) in `r`.
fn ink(img: &Img, r: [i32; 4]) -> Option<(i32, i32, i32, i32, usize)> {
    let mut b: Option<(i32, i32, i32, i32, usize)> = None;
    for y in r[1].max(0)..(r[1] + r[3]).min(img.h as i32) {
        for x in r[0].max(0)..(r[0] + r[2]).min(img.w as i32) {
            if img.at(x as usize, y as usize).iter().all(|&v| v > 150) {
                b = Some(match b { None => (x, y, x, y, 1), Some((a, bb, c, d, n)) => (a.min(x), bb.min(y), c.max(x), d.max(y), n + 1) });
            }
        }
    }
    b
}

fn client_box(img: &Img) -> Option<(usize, usize, usize, usize, usize)> { img.bbox(CLIENT_RGB) }

/// Logical centre of a device rectangle.
fn centre(r: [i32; 4], s: f64) -> (f64, f64) { ((r[0] as f64 + r[2] as f64 / 2.0) / s, (r[1] as f64 + r[3] as f64 / 2.0) / s) }

#[test]
fn a_title_bar_appears_above_the_window_with_its_title_and_the_client_is_untouched() {
    for s in SCALES {
        let (_t, mut c) = themed(s, "", &[]);
        let before = c.capture("before.png");
        c.client_args(&["--size", "400x300", "--title", "Alpha"]);
        let d = wait_deco(&c, "Alpha");
        let k: f64 = s.parse().unwrap();
        println!("SCALE {s}: {d:?}");
        // Cascaded to the usable area's corner plus one step, below the bar: logical (32, 30 + 24).
        let want = [px(32.0, k), px(54.0, k), px(400.0, k), px(300.0, k)];
        assert_eq!(d.window, want, "scale {s}: where the client goes");
        // The bar is the window's width, hangs exactly above it (no seam, no overlap), and is ~30 logical px tall.
        assert_eq!((d.bar[0], d.bar[2]), (d.window[0], d.window[2]), "scale {s}: the bar spans the window");
        assert_eq!(d.bar[1] + d.bar[3], d.window[1], "scale {s}: the bar meets the window's top edge");
        assert!((d.bar[3] - px(BAR_H as f64, k)).abs() <= 1, "scale {s}: bar height {} for logical {BAR_H}", d.bar[3]);
        assert_eq!(d.close[3], d.bar[3]);
        assert!((d.close[2] - d.bar[3]).abs() <= 1, "the close button is a square target");
        // The capture: the client's pixels are exactly its window (the bar covers none of them) ...
        let img = c.capture("bar.png");
        save(&img, &format!("bar-{s}.png"));
        let (x0, y0, x1, y1, n) = client_box(&img).expect("the client is on screen");
        assert_eq!((x0 as i32, y0 as i32, (x1 + 1 - x0) as i32, (y1 + 1 - y0) as i32), (want[0], want[1], want[2], want[3]), "scale {s}: client pixels");
        assert_eq!(n as i32, want[2] * want[3], "scale {s}: all of the client is visible");
        // ... and the only other change in the frame is the bar (and the dock-free desktop is untouched elsewhere).
        let (inn, out) = diff(&before, &img, &[d.bar, d.window]);
        println!("SCALE {s}: {inn} pixels changed inside bar+window, {out} outside");
        assert_eq!(out, 0, "scale {s}: nothing outside the window and its bar changed");
        let bar_changed = (d.bar[0] as usize..(d.bar[0] + d.bar[2]) as usize).flat_map(|x| (d.bar[1] as usize..(d.bar[1] + d.bar[3]) as usize).map(move |y| (x, y))).filter(|&(x, y)| img.at(x, y) != before.at(x, y)).count();
        assert!(bar_changed * 10 > (d.bar[2] * d.bar[3]) as usize * 9, "scale {s}: glass covers the bar ({bar_changed} of {})", d.bar[2] * d.bar[3]);
        // The title is drawn where the log says, and is Alpha (not another string): ink inside its box, none left of it.
        let (ix0, iy0, ix1, iy1, ink_n) = ink(&img, title_area(&d)).expect("title ink");
        println!("SCALE {s}: title ink ({ix0},{iy0})-({ix1},{iy1}) n={ink_n}, logged box x {} w {}", d.title[0], d.title[2]);
        assert!(ix0 >= d.title[0] - 2 && ix1 <= d.title[0] + d.title[2] + 2, "scale {s}: ink lies in the logged title box");
        assert!(iy0 >= d.bar[1] && iy1 < d.bar[1] + d.bar[3], "scale {s}: ink lies inside the bar vertically");
        let _ = ix0;
        assert!(ix0 >= d.close[0] + d.close[2], "scale {s}: the title starts right of the close button");
        // A different title is a different picture.
        c.client_args(&["--size", "400x300", "--title", "Wide title here"]);
        let e = wait_deco(&c, "Wide title here");
        let img2 = c.capture("bar2.png");
        let (jx0, _, jx1, _, _) = ink(&img2, title_area(&e)).unwrap();
        assert!((jx1 - jx0) > (ix1 - ix0) * 2, "scale {s}: a longer title is wider ink: {} vs {}", jx1 - jx0, ix1 - ix0);
    }
}

/// A client outside every domain (a host process, which is what a test client is) is attributed to
/// nothing: its bar has no badge, its layout line has no `domain=` field, and the compositor's own
/// state says `domain: null`. A window with no domain is normal and must keep working. The other
/// half, a client the kernel puts in `aurora-domains/<name>`, needs a real cgroup and is proved in
/// demos/windowed-domain; `peer::tests` covers the parsing.
#[test]
fn a_window_with_no_domain_is_unbadged_and_the_state_says_null() {
    let (_t, mut c) = themed("1", "", &[]);
    c.client_args(&["--size", "300x200", "--title", "Plain"]);
    let _ = wait_deco(&c, "Plain");
    let line = c.log().lines().rev().find(|l| l.contains("deco: layout") && l.contains("text=Plain")).expect("a layout line").to_string();
    assert!(!line.contains("domain="), "a window with no domain logs no domain: {line}");
    let r = c.inject_reply("state");
    let v: serde_json::Value = serde_json::from_str(r.strip_prefix("ok ").unwrap()).unwrap();
    let w = v["windows"].as_array().unwrap().iter().find(|w| w["title"] == "Plain").expect("the window");
    assert!(w["domain"].is_null(), "state says what the kernel says, and the kernel says no domain: {w}");
}

#[test]
fn a_long_title_is_clipped_with_an_ellipsis_to_the_room_beside_the_close_button() {
    for s in SCALES {
        let (_t, mut c) = themed(s, "", &[]);
        let long = "A title far too long for a window this narrow to ever hold";
        c.client_args(&["--size", "200x120", "--title", long]);
        wait_for("a clipped bar", || decos(&c.log()).iter().any(|d| d.text.ends_with("...")));
        std::thread::sleep(Duration::from_millis(350));
        let d = decos(&c.log()).into_iter().rev().find(|d| d.text.ends_with("...")).unwrap();
        let k: f64 = s.parse().unwrap();
        assert!(d.text.len() < long.len() && long.starts_with(d.text.trim_end_matches("...")), "{:?}", d.text);
        // Room: the window less the button (30) and the right padding (8), in device pixels.
        assert!(d.title[2] <= px(200.0 - 30.0 - 8.0, k) + 1, "scale {s}: the text ({}) fits the room ({})", d.title[2], px(162.0, k));
        let img = c.capture("clip.png");
        let (_, _, ix1, _, _) = ink(&img, title_area(&d)).unwrap();
        assert!(ix1 <= d.bar[0] + d.bar[2] - (8.0 * k).round() as i32, "scale {s}: the clipped title stops short of the bar's right edge");
    }
}

#[test]
fn the_close_button_asks_the_client_to_close_and_the_window_goes() {
    for s in SCALES {
        let (_t, mut c) = themed(s, "", &[]);
        let log = c.client_args(&["--size", "400x300", "--title", "Closer"]);
        let d = wait_deco(&c, "Closer");
        let k: f64 = s.parse().unwrap();
        let before = c.capture("open.png");
        assert!(client_box(&before).is_some());
        let (cx, cy) = centre(d.close, k);
        at(&c, cx, cy);
        // Hover: the button's disc fills, only inside the button.
        c.step_frames(12);
        let hov = c.capture("hover.png");
        let (inn, out) = diff(&before, &hov, &[d.close]);
        println!("SCALE {s}: hover changed {inn} pixels in the button, {out} outside");
        assert!(inn > 30 && out == 0, "scale {s}: hover is a change of the close button alone ({inn}/{out})");
        // The pointer is over the bar, so the client never sees it.
        assert!(!read(&log).contains("pointer_enter"), "the client saw the pointer over its bar");
        c.inject("click");
        wait_for("the client received xdg_toplevel.close", || read(&log).lines().any(|l| l == "close"));
        // The client ended on it; the window is gone and so is its bar.
        wait_for("the window left the frame", || client_box(&c.capture("gone.png")).is_none());
        std::thread::sleep(Duration::from_millis(200));
        let gone = c.capture("gone.png");
        assert_eq!(ink(&gone, title_area(&d)), None, "scale {s}: the bar is gone with the window");
        assert!(windows(&c).is_empty());
    }
}

#[test]
fn close_is_a_request_a_client_can_refuse() {
    let (_t, mut c) = themed("1", "", &[]);
    let log = c.client_args(&["--size", "400x300", "--title", "Stubborn", "--refuse-close"]);
    let d = wait_deco(&c, "Stubborn");
    let (cx, cy) = centre(d.close, 1.0);
    at(&c, cx, cy);
    c.inject("click");
    wait_for("the close request", || read(&log).lines().any(|l| l == "close"));
    std::thread::sleep(Duration::from_millis(400));
    assert!(client_box(&c.capture("still.png")).is_some(), "a refused close leaves the window");
    assert_eq!(windows(&c).len(), 1);
    assert!(read(&log).lines().filter(|l| *l == "close").count() == 1, "one press is one request");
    // A press that slides off the button before release closes nothing.
    at(&c, cx, cy);
    c.inject("press");
    at(&c, cx + 200.0, cy + 100.0);
    c.inject("release");
    std::thread::sleep(Duration::from_millis(300));
    assert!(read(&log).lines().filter(|l| *l == "close").count() == 1, "release off the button is not a click");
}

#[test]
fn dragging_the_bar_moves_the_window_by_the_pointer_delta() {
    for s in SCALES {
        let (_t, mut c) = themed(s, "", &[]);
        c.client_args(&["--size", "400x300", "--title", "Mover"]);
        let d = wait_deco(&c, "Mover");
        let k: f64 = s.parse().unwrap();
        let (x, y, w, _h) = win_of(&c, "Mover");
        let from = (x as f64 + w as f64 / 2.0, y as f64 - 15.0);
        let delta = (90.0, 61.0);
        drag(&c, from, (from.0 + delta.0, from.1 + delta.1));
        let (x2, y2, ..) = win_of(&c, "Mover");
        assert_eq!((x2 - x, y2 - y), (delta.0 as i32, delta.1 as i32), "scale {s}: the window moved by exactly the pointer's travel");
        // The pixels followed: the client's box moved by the logical delta in device pixels, the bar with it.
        let e = wait_deco_at(&c, "Mover", px(y2 as f64 - BAR_H as f64, k));
        let img = c.capture("moved.png");
        let (x0, y0, ..) = client_box(&img).unwrap();
        assert_eq!((x0 as i32, y0 as i32), (e.window[0], e.window[1]), "scale {s}");
        assert_eq!((e.window[0] - d.window[0], e.window[1] - d.window[1]), (px(x2 as f64, k) - px(x as f64, k), px(y2 as f64, k) - px(y as f64, k)));
        assert!(ink(&img, title_area(&e)).is_some(), "the bar moved with it");
        assert_eq!(ink(&img, title_area(&d)), None, "and left its old place");
        // Dragging does not reach the client as a pointer press.
        // A bar dragged to the top of the screen stays reachable: the bar cannot leave the usable area.
        drag(&c, (x2 as f64 + 50.0, y2 as f64 - 15.0), (x2 as f64 + 50.0, -300.0));
        let (_, y3, ..) = win_of(&c, "Mover");
        assert!(y3 >= BAR_H, "scale {s}: the bar stays on screen (window y {y3})");
    }
}

fn wait_deco_at(c: &Comp, text: &str, bar_y: i32) -> Deco {
    wait_for("the bar at its new place", || last_for(c, text).is_some_and(|d| d.bar[1] == bar_y));
    std::thread::sleep(Duration::from_millis(200));
    last_for(c, text).unwrap()
}

#[test]
fn a_corner_drag_resizes_the_window_and_the_grip_is_a_usable_target_at_both_scales() {
    for s in SCALES {
        let (_t, mut c) = themed(s, "", &[]);
        c.client_args(&["--size", "400x300", "--title", "Sizer", "--follow", "--min-size", "300x200"]);
        wait_deco(&c, "Sizer");
        let (x, y, w, h) = win_of(&c, "Sizer");
        let (right, bottom) = ((x + w) as f64, (y + h) as f64);
        // 4 logical px OUTSIDE the bottom-right corner: a 1-px border could not be hit here, the grip can.
        drag(&c, (right + 4.0, bottom + 4.0), (right + 4.0 + 80.0, bottom + 4.0 + 50.0));
        wait_for("the client followed the configure", || win_of(&c, "Sizer").2 == w + 80);
        let (x2, y2, w2, h2) = win_of(&c, "Sizer");
        assert_eq!((x2, y2, w2, h2), (x, y, w + 80, h + 50), "scale {s}: the bottom-right corner grows the window by the pointer's travel");
        std::thread::sleep(Duration::from_millis(300));
        let img = c.capture("grown.png");
        let (x0, y0, x1, y1, _) = client_box(&img).unwrap();
        let kk: f64 = s.parse().unwrap();
        assert_eq!(((x1 + 1 - x0) as i32, (y1 + 1 - y0) as i32), (px(w2 as f64, kk), px(h2 as f64, kk)), "scale {s}: and so do the pixels");
        // Beyond the grip: 20 px outside is the desktop, not an edge.
        drag(&c, (x2 as f64 + w2 as f64 + 20.0, y2 as f64 + h2 as f64 + 20.0), (x2 as f64 + w2 as f64 + 60.0, y2 as f64 + h2 as f64 + 60.0));
        assert_eq!(win_of(&c, "Sizer"), (x2, y2, w2, h2), "scale {s}: outside the grip nothing resizes");
        // The top-left corner (just above-left of the bar) moves the origin as it grows; here it SHRINKS.
        let (bx, by) = (x2 as f64 - 3.0, (y2 - BAR_H) as f64 - 3.0);
        drag(&c, (bx, by), (bx + 40.0, by + 30.0));
        wait_for("the top-left resize landed", || win_of(&c, "Sizer").2 == w2 - 40);
        std::thread::sleep(Duration::from_millis(300));
        let (x3, y3, w3, h3) = win_of(&c, "Sizer");
        assert_eq!((w3, h3), (w2 - 40, h2 - 30), "scale {s}: shrunk by the travel");
        assert_eq!((x3, y3), (x2 + 40, y2 + 30), "scale {s}: the far corner stayed put, so the origin moved");
        // The client's minimum (300x200) is respected: try to shrink far past it.
        let (r3, b3) = ((x3 + w3) as f64, (y3 + h3) as f64);
        drag(&c, (r3 + 4.0, b3 + 4.0), (r3 - 400.0, b3 - 400.0));
        wait_for("clamped at the minimum", || win_of(&c, "Sizer").2 == 300);
        std::thread::sleep(Duration::from_millis(300));
        let (_, _, w4, h4) = win_of(&c, "Sizer");
        assert_eq!((w4, h4), (300, 200), "scale {s}: min size hint honoured");
    }
}

#[test]
fn a_window_whose_client_pinned_its_size_has_no_resize_edges() {
    let (_t, mut c) = themed("1", "", &[]);
    c.client_args(&["--size", "400x300", "--title", "Pinned", "--follow", "--min-size", "400x300", "--max-size", "400x300"]);
    wait_deco(&c, "Pinned");
    let (x, y, w, h) = win_of(&c, "Pinned");
    drag(&c, ((x + w) as f64 + 4.0, (y + h) as f64 + 4.0), ((x + w) as f64 + 80.0, (y + h) as f64 + 60.0));
    std::thread::sleep(Duration::from_millis(300));
    assert_eq!(win_of(&c, "Pinned"), (x, y, w, h));
}

/// The usable area in logical units: the output less the dock's strip at the bottom, rounded inwards
/// (as `Aurora::usable_area` does). The strip comes from the compositor's own `dock: layout` line.
fn usable(c: &Comp, s: f64) -> (i32, i32, i32, i32) {
    let (_, _, reserved, _) = dock_layout(&c.log()).expect("a dock layout line");
    (0, 0, (W as f64 / s).floor() as i32, (reserved[1] as f64 / s).floor() as i32)
}

#[test]
fn double_click_maximises_into_the_usable_area_and_again_restores() {
    for s in SCALES {
        let (_t, mut c) = themed(s, "", &[]);
        c.client_args(&["--size", "400x300", "--title", "Maxi", "--follow"]);
        let d = wait_deco(&c, "Maxi");
        let k: f64 = s.parse().unwrap();
        let (x, y, w, h) = win_of(&c, "Maxi");
        let (ux, uy, uw, uh) = usable(&c, k);
        let pt = (x as f64 + w as f64 / 2.0, y as f64 - 15.0);
        at(&c, pt.0, pt.1);
        c.inject("click");
        c.inject("click");
        wait_for("maximised", || win_of(&c, "Maxi").2 == uw);
        std::thread::sleep(Duration::from_millis(400));
        let (mx, my, mw, mh) = win_of(&c, "Maxi");
        println!("SCALE {s}: usable ({ux},{uy},{uw},{uh}); maximised ({mx},{my},{mw},{mh})");
        assert_eq!((mx, my, mw), (ux, uy + BAR_H, uw), "scale {s}: the client fills the usable width, its bar at the top of it");
        assert_eq!(mh, uh - BAR_H, "scale {s}: and the usable height less the bar, not the whole output");
        let img = c.capture("max.png");
        let (x0, y0, x1, y1, _) = client_box(&img).unwrap();
        let e = last_for(&c, "Maxi").unwrap();
        assert_eq!((x0 as i32, y0 as i32, x1 as i32 + 1 - x0 as i32, y1 as i32 + 1 - y0 as i32), (e.window[0], e.window[1], e.window[2], e.window[3]), "scale {s}: the client's pixels are the window the compositor laid out");
        assert!((e.window[0], e.window[1]) == (px(mx as f64, k), px(my as f64, k)) && (e.window[0] + e.window[2] - px(uw as f64, k)).abs() <= 1, "scale {s}: pixels at the usable origin and to its edge");
        assert!(y1 as i32 + 1 <= (dock_layout(&c.log()).unwrap().2)[1], "scale {s}: not under the dock");
        assert_eq!(e.bar[1] + e.bar[3], e.window[1]);
        assert!(e.bar[1] >= 0, "the bar is on screen");
        let _ = d;
        // A maximised window cannot be dragged away (the press is the bar's but moves nothing) ...
        drag(&c, (mx as f64 + 200.0, my as f64 - 15.0), (mx as f64 + 300.0, my as f64 + 80.0));
        assert_eq!(win_of(&c, "Maxi"), (mx, my, mw, mh), "scale {s}: a maximised window stays put");
        // ... and a double-click restores exactly the old place and size.
        std::thread::sleep(Duration::from_millis(500));
        at(&c, mx as f64 + 200.0, my as f64 - 15.0);
        c.inject("click");
        c.inject("click");
        wait_for("restored", || win_of(&c, "Maxi").2 == w);
        std::thread::sleep(Duration::from_millis(300));
        assert_eq!(win_of(&c, "Maxi"), (x, y, w, h), "scale {s}: restored to where it was");
        assert!(client_box(&c.capture("rest.png")).is_some_and(|b| b.0 as i32 == px(x as f64, k)));
    }
}

#[test]
fn a_client_that_asks_to_decorate_itself_is_left_bare_and_one_that_asks_for_the_server_gets_a_bar() {
    for s in SCALES {
        let (_t, mut c) = themed(s, "", &[]);
        let k: f64 = s.parse().unwrap();
        // Client-side: no bar, the compositor said so, and the pointer over the top of the window reaches the client.
        let log = c.client_args(&["--size", "400x300", "--title", "Selfish", "--decor", "client"]);
        wait_for("the decoration answer", || read(&log).contains("decoration mode"));
        assert!(read(&log).contains("decoration mode ClientSide"), "{}", read(&log));
        std::thread::sleep(Duration::from_millis(500));
        assert!(decos(&c.log()).iter().all(|d| d.text != "Selfish"), "no bar was drawn for a client-side window");
        assert!(c.log().contains("decoration: \"Selfish\" asked for ClientSide, told ClientSide"), "{}", c.log());
        let img = c.capture("selfish.png");
        let (x0, y0, ..) = client_box(&img).unwrap();
        let above = [x0 as i32, y0 as i32 - px(BAR_H as f64, k), 400, px(BAR_H as f64, k) - 2];
        assert_eq!(ink(&img, above), None, "scale {s}: nothing drawn above the client");
        let bg = img.at(x0 + 10, y0.saturating_sub(10));
        let reference = img.at(x0 + 10, 5);
        assert!((0..3).all(|i| (bg[i] as i32 - reference[i] as i32).abs() < 12), "scale {s}: the strip above is plain desktop ({bg:?} vs {reference:?})");
        let (x, y, w, _) = win_of(&c, "Selfish");
        at(&c, x as f64 + w as f64 / 2.0, y as f64 + 3.0);
        c.step_frames(2);
        wait_for("the client sees the pointer at its own top edge", || read(&log).contains("pointer_enter"));
        // A client that asks for server-side gets a bar and is told so.
        let log2 = c.client_args(&["--size", "400x300", "--title", "Trusting", "--decor", "server"]);
        wait_for("the second answer", || read(&log2).contains("decoration mode"));
        assert!(read(&log2).contains("decoration mode ServerSide"));
        wait_deco(&c, "Trusting");
        // And one that never binds the manager (every Haiku window) is decorated too.
        c.client_args(&["--size", "300x200", "--title", "Silent"]);
        wait_deco(&c, "Silent");
    }
}

#[test]
fn the_focused_bar_differs_from_an_unfocused_one_in_more_than_colour() {
    for s in SCALES {
        let (_t, mut c) = themed(s, "", &[]);
        c.client_args(&["--size", "300x200", "--title", "Same title"]);
        let a1 = wait_deco(&c, "Same title");
        assert!(a1.focused, "a new window takes focus");
        c.client_args(&["--size", "300x200", "--title", "Other"]);
        let b = wait_deco(&c, "Other");
        assert!(b.focused);
        // `Same title` lost focus: its bar was re-drawn unfocused. Same string, so the difference is the style.
        wait_for("the first window's bar redrawn unfocused", || decos(&c.log()).iter().any(|d| d.win == a1.win && !d.focused));
        std::thread::sleep(Duration::from_millis(350));
        let a2 = decos(&c.log()).into_iter().rev().find(|d| d.win == a1.win).unwrap();
        assert!(!a2.focused);
        assert!(a1.title[2] > a2.title[2], "scale {s}: the focused title is bold, hence wider: {} vs {}", a1.title[2], a2.title[2]);
        let img = c.capture("two.png");
        // The stripe: the accent yellow along the focused bar's bottom edge and not the unfocused one's.
        let yellow = |d: &Deco| {
            let y = (d.bar[1] + d.bar[3] - 2) as usize;
            (d.bar[0] as usize + 40..(d.bar[0] + d.bar[2]) as usize - 10).filter(|&x| { let p = img.at(x, y); p[0] > 200 && p[1] > 150 && p[2] < 90 }).count()
        };
        let (fy, uy) = (yellow(&b), yellow(&a2));
        println!("SCALE {s}: stripe pixels focused {fy}, unfocused {uy}");
        assert!(fy > (b.bar[2] as usize) / 2 && uy == 0, "scale {s}: only the focused bar has the stripe");
        // Clicking the other window swaps them.
        let (x, y, ..) = win_of(&c, "Same title");
        at(&c, x as f64 + 10.0, y as f64 + 5.0); // a corner of it that the newer window does not cover
        c.inject("click");
        wait_for("focus moved", || last_for(&c, "Same title").is_some_and(|d| d.focused));
    }
}

#[test]
fn the_tab_style_is_a_tab_not_a_bar() {
    for s in SCALES {
        let (_t, mut c) = themed(s, "[decoration]\nstyle = \"tab\"\n", &[]);
        let before = c.capture("before.png");
        c.client_args(&["--size", "400x300", "--title", "Tabby"]);
        let d = wait_deco(&c, "Tabby");
        let k: f64 = s.parse().unwrap();
        assert!(d.bar[2] < d.window[2] / 2, "scale {s}: the tab ({}) is narrower than the window ({})", d.bar[2], d.window[2]);
        assert_eq!((d.bar[0], d.bar[1] + d.bar[3]), (d.window[0], d.window[1]));
        let img = c.capture("tab.png");
        // Right of the tab, above the window: bare desktop.
        let (sx, sy) = ((d.window[0] + d.window[2] - 20) as usize, (d.window[1] - 10) as usize);
        assert_eq!(img.at(sx, sy), before.at(sx, sy), "scale {s}: empty beside the tab");
        assert!(ink(&img, title_area(&d)).is_some());
        // The tab drags the window; the bare strip beside it does not.
        let (x, y, ..) = win_of(&c, "Tabby");
        drag(&c, (x as f64 + 55.0, y as f64 - 15.0), (x as f64 + 105.0, y as f64 + 5.0));
        assert_eq!(win_of(&c, "Tabby").0, x + 50, "scale {s}: dragged by its tab");
        let (x, y, w, _) = (win_of(&c, "Tabby").0, win_of(&c, "Tabby").1, win_of(&c, "Tabby").2, 0);
        drag(&c, (x as f64 + w as f64 - 30.0, y as f64 - 15.0), (x as f64 + w as f64 + 40.0, y as f64 + 40.0));
        assert_eq!(win_of(&c, "Tabby").0, x, "scale {s}: the empty strip beside the tab moves nothing");
        let _ = k;
    }
}

#[test]
fn new_windows_cascade_and_a_dialog_centres_on_its_parent_inside_the_usable_area() {
    for s in SCALES {
        let (_t, mut c) = themed(s, "", &[]);
        let k: f64 = s.parse().unwrap();
        c.client_args(&["--size", "400x300", "--title", "First"]);
        c.client_args(&["--size", "400x300", "--title", "Second"]);
        let (a, b) = (win_of(&c, "First"), win_of(&c, "Second"));
        assert_ne!((a.0, a.1), (0, 0), "not at the origin");
        assert_eq!((b.0 - a.0, b.1 - a.1), (32, 32), "scale {s}: the second window is one cascade step from the first");
        // A dialog is centred on its parent.
        c.client_args(&["--size", "500x350", "--title", "Parent", "--child", "200x100"]);
        wait_for("the dialog mapped", || windows(&c).iter().any(|w| w.0 == "aurora-testclient-child"));
        std::thread::sleep(Duration::from_millis(300));
        let (p, ch) = (win_of(&c, "Parent"), win_of(&c, "aurora-testclient-child"));
        let (pc, cc) = ((p.0 * 2 + p.2, p.1 * 2 + p.3), (ch.0 * 2 + ch.2, ch.1 * 2 + ch.3));
        assert!((pc.0 - cc.0).abs() <= 1 && (pc.1 - cc.1).abs() <= 1, "scale {s}: the dialog's centre {cc:?} is its parent's {pc:?}");
        // Everything lies in the usable area, bars included.
        let (ux, uy, uw, uh) = usable(&c, k);
        for w in windows(&c) {
            assert!(w.1 >= ux && w.2 >= uy + BAR_H && w.1 + w.3 <= ux + uw && w.2 + w.4 <= uy + uh, "scale {s}: {w:?} inside usable ({ux},{uy},{uw},{uh}) with its bar");
        }
        // A window too big for the cascade slot is still kept inside: a huge one lands flush in the corner.
        c.client_args(&["--size", "840x440", "--title", "Big"]);
        let big = win_of(&c, "Big");
        assert!(big.0 + big.2 <= uw && big.1 + big.3 <= uh && big.1 >= BAR_H, "scale {s}: {big:?}");
    }
}

#[test]
fn a_remembered_place_beats_the_cascade() {
    let dir = tempfile::tempdir().unwrap();
    let state = dir.path().join("layout.json");
    let st = state.to_str().unwrap();
    let (x, y);
    {
        let (_t, mut c) = themed("1", "", &["--state-file", st]);
        c.client_args(&["--size", "300x200", "--title", "Remember me", "--app-id", "org.test.mem"]);
        wait_deco(&c, "Remember me");
        let (wx, wy, ww, _) = win_of(&c, "Remember me");
        drag(&c, (wx as f64 + ww as f64 / 2.0, wy as f64 - 15.0), (wx as f64 + ww as f64 / 2.0 + 211.0, wy as f64 - 15.0 + 137.0));
        (x, y) = (wx + 211, wy + 137);
        assert_eq!(win_of(&c, "Remember me").0, x);
        wait_for("the settled layout reaches disk", || read(&state).contains("Remember me") && read(&state).contains(&format!("\"x\": {x}")));
    }
    let (_t, mut c) = themed("1", "", &["--state-file", st]);
    c.client_args(&["--size", "300x200", "--title", "Remember me", "--app-id", "org.test.mem"]);
    wait_deco(&c, "Remember me");
    std::thread::sleep(Duration::from_millis(500));
    let (rx, ry, ..) = win_of(&c, "Remember me");
    assert_eq!((rx, ry), (x, y), "persist wins over the cascade: {}", c.log());
}

struct X(Child);
impl Drop for X { fn drop(&mut self) { let _ = self.0.kill(); let _ = self.0.wait(); } }

fn on_path(bin: &str) -> bool {
    std::env::var_os("PATH").is_some_and(|p| std::env::split_paths(&p).any(|d| d.join(bin).is_file()))
}

fn x_display(c: &Comp) -> String {
    wait_for("xwayland ready", || c.log().contains("xwayland: ready on DISPLAY=:"));
    let log = c.log();
    let i = log.find("xwayland: ready on DISPLAY=:").unwrap() + "xwayland: ready on DISPLAY=".len();
    log[i..].chars().take_while(|ch| *ch == ':' || ch.is_ascii_digit()).collect()
}

fn start_x(c: &Comp, display: &str, args: &[&str]) -> X {
    let exe = std::env::current_exe().unwrap();
    let bin = exe.parent().unwrap().parent().unwrap().join("examples").join("xclient");
    X(Command::new(bin).args(args).env("DISPLAY", display).env_remove("WAYLAND_DISPLAY").env("XDG_RUNTIME_DIR", c.dir.path()).stdout(Stdio::null()).stderr(Stdio::null()).spawn().expect("xclient"))
}

#[test]
fn a_managed_x11_window_is_decorated_and_an_override_redirect_one_never_is() {
    if !on_path("Xwayland") { eprintln!("SKIPPED: Xwayland is not installed"); return; }
    for s in SCALES {
        let (_t, c) = themed(s, "", &["--xwayland"]);
        let d = x_display(&c);
        let k: f64 = s.parse().unwrap();
        // A menu-like override-redirect window, placed exactly where it asks.
        let _menu = start_x(&c, &d, &["--geometry", "500,400,200,100", "--rgb", "240,120,10", "--override-redirect"]);
        wait_for("override-redirect window mapped", || c.log().contains("xwayland: override-redirect window mapped"));
        // A managed one.
        let _win = start_x(&c, &d, &["--geometry", "0,0,300,200", "--rgb", "10,200,240"]);
        wait_for("the managed window has its surface", || c.log().contains("has its surface"));
        wait_for("it has a bar", || !decos(&c.log()).is_empty());
        std::thread::sleep(Duration::from_millis(500));
        let all = decos(&c.log());
        assert_eq!(all.iter().map(|d| d.win).collect::<std::collections::HashSet<_>>().len(), 1, "scale {s}: exactly one window is decorated: {all:?}");
        let bar = all.last().unwrap().clone();
        let img = c.capture("x.png");
        // The override-redirect window is exactly its rectangle with nothing above it.
        let (x0, y0, x1, y1, n) = img.bbox([240, 120, 10]).expect("the override-redirect window is on screen");
        assert_eq!((x0 as i32, y0 as i32, x1 as i32 + 1, y1 as i32 + 1), (px(500.0, k), px(400.0, k), px(700.0, k), px(500.0, k)), "scale {s}");
        let area = (x1 as i32 + 1 - x0 as i32) * (y1 as i32 + 1 - y0 as i32);
        assert!(n as i32 * 100 >= area * 95 && n as i32 <= area, "scale {s}: the window is solid but for the resampled edge ({n} of {area})");
        let strip = [x0 as i32, y0 as i32 - px(BAR_H as f64, k), (x1 + 1 - x0) as i32, px(BAR_H as f64, k) - 2];
        assert_eq!(ink(&img, strip), None, "scale {s}: no title bar over the override-redirect window");
        // The managed one has a bar over it, and the client's own pixels are the whole window.
        let (m0, m1, m2, m3, mn) = img.bbox([10, 200, 240]).expect("the managed window");
        assert_eq!(((m2 + 1 - m0) as i32, (m3 + 1 - m1) as i32), (px(300.0, k), px(200.0, k)), "scale {s}");
        assert!(mn as i32 * 100 >= px(300.0, k) * px(200.0, k) * 95, "scale {s}: all of the client is visible ({mn})");
        assert_eq!((bar.window[0], bar.window[1]), (m0 as i32, m1 as i32), "scale {s}: the bar belongs to the managed window");
        assert!(bar.window[1] > bar.bar[1], "the bar is above it");
        // The managed window drags by its bar like any other: its pixels move by the pointer's travel.
        let (bx, by) = centre([bar.bar[0] + bar.bar[2] / 2, bar.bar[1], 1, bar.bar[3]], k);
        drag(&c, (bx, by), (bx + 100.0, by + 60.0));
        std::thread::sleep(Duration::from_millis(300));
        let moved = c.capture("x2.png").bbox([10, 200, 240]).expect("the managed window");
        assert_eq!((moved.0 as i32 - m0 as i32, moved.1 as i32 - m1 as i32), (px(100.0, k), px(60.0, k)).clone(), "scale {s}: moved by the travel");
        // And the override-redirect window, whose position nobody may change, did not move.
        assert_eq!(c.capture("x3.png").bbox([240, 120, 10]).map(|b| (b.0, b.1)), Some((x0, y0)));
    }
}
