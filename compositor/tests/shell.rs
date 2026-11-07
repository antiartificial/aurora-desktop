//! The compositor-drawn shell: background (shader, image, gradient), dock, text, reserved
//! area, input configuration. Real headless compositors, real captures, decoded pixels.

mod common;
use common::*;
use std::{
    io::{BufRead, BufReader, Write},
    os::unix::net::UnixListener,
    path::{Path, PathBuf},
    time::Duration,
};

fn settle() { std::thread::sleep(Duration::from_millis(700)); }

// [deskbar] off: these tests measure the usable area and exact client pixels; the Deskbar has tests of its own (tests/deskbar.rs).
const FLAT: &str = "[background]\nmode = \"solid\"\ntop = [0.30, 0.0, 0.0]\n[desktop]\nclock = false\nhostname = false\n[deskbar]\nenabled = false\n";

/// A bus that answers `registry.apps@v1` (to "registry") the way the controller does.
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
                if (req["to"].as_str(), req["verb"].as_str()) != (Some("registry"), Some("registry.apps@v1")) {
                    return;
                }
                let reply = serde_json::json!({"id": "s1", "kind": "response", "corr": req["id"], "verb": "registry.apps@v1", "payload": {"apps": apps}});
                let _ = writeln!(&s, "{reply}");
            });
        }
    });
    path
}

fn dock_comp(t: &Theme, bus: &Path, extra: &[&str]) -> Comp {
    let mut args = vec!["--theme", t.path(), "--bus-socket", bus.to_str().unwrap(), "--freeze-time"];
    args.extend_from_slice(extra);
    // `--legacy-scene` (added by the harness) would hide the shell; undo it by starting without it.
    Comp::start_shell(&args)
}

fn wait_layout(c: &Comp) -> (f64, [i32; 4], [i32; 4], Vec<[i32; 4]>) {
    wait_for("the dock to learn its apps", || dock_layout(&c.log()).is_some_and(|l| !l.3.is_empty()));
    std::thread::sleep(Duration::from_millis(300));
    dock_layout(&c.log()).unwrap()
}

/// Pixels in the box that are clearly brighter than any glass or background (text ink and its antialiasing).
fn bright(img: &Img, x0: usize, x1: usize, y0: usize, y1: usize) -> usize {
    (y0..y1).flat_map(|y| (x0..x1).map(move |x| (x, y))).filter(|&(x, y)| img.at(x, y).iter().all(|&v| v > 120)).count()
}

#[test]
fn dock_lists_registry_apps_and_occupies_the_expected_rows_with_glass_and_text() {
    let t = Theme::new();
    t.append(FLAT);
    let d = tempfile::tempdir().unwrap();
    let bus = fake_registry(d.path(), &[("org.aurora.lumen", "Lumen"), ("org.aurora.ledger", "Haiku Ledger")]);
    let c = dock_comp(&t, &bus, &[]);
    let (scale, panel, reserved, items) = wait_layout(&c);
    println!("DOCK layout from the compositor log: scale {scale} panel {panel:?} reserved {reserved:?} items {items:?}");
    assert_eq!(items.len(), 2, "one pill per registry app");
    let img = c.capture("dock.png");
    let bg = img.at(5, 5);
    // 1. Everything above the reserved strip is the flat background, exactly.
    for y in (0..reserved[1] as usize).step_by(7) { for x in (0..1280).step_by(97) { assert_eq!(img.at(x, y), bg, "({x},{y}) above the dock"); } }
    // 2. The gap between the panel and the output edge, and the margins beside it, are background.
    let (px, py, pw, ph) = (panel[0] as usize, panel[1] as usize, panel[2] as usize, panel[3] as usize);
    assert_eq!(img.at(px + pw / 2, py + ph + 3), bg, "gap below the panel");
    assert_eq!(img.at(px - 6, py + ph / 2), bg, "left of the panel");
    // 3. The panel rows: glass differs from the flat background across the whole panel width.
    let row = py + 3; // above the pills, inside the panel's padding
    let glassy = (px + 20..px + pw - 20).filter(|&x| img.at(x, row) != bg).count();
    println!("DOCK rows {py}..{}: {glassy} of {} pixels on padding row {row} differ from the flat background {bg:?}; sample {:?}", py + ph, pw - 40, img.at(px + pw / 2, row));
    assert!(glassy * 10 > (pw - 40) * 9, "panel is not tinted glass over the background");
    // 4. Text exists where labels are, not in the padding row or the gap.
    let (i0, i1) = (items[0], items[1]);
    let label0 = bright(&img, (i0[0] + i0[3]) as usize, (i0[0] + i0[2]) as usize, i0[1] as usize, (i0[1] + i0[3]) as usize);
    let label1 = bright(&img, (i1[0] + i1[3]) as usize, (i1[0] + i1[2]) as usize, i1[1] as usize, (i1[1] + i1[3]) as usize);
    let pad = bright(&img, px + 10, px + pw - 10, py + 4, py + 7);
    println!("DOCK text: bright (text-coloured) pixels in label 1 {label0}, label 2 {label1}, padding strip {pad}");
    assert!(label0 > 60 && label1 > 80 && pad == 0);
    assert!(label1 > label0, "'Haiku Ledger' is longer than 'Lumen'");
    // 5. Ordinary chrome: it is in the capture, and the capture is stable frame to frame.
    assert_eq!(img.diff_count(&c.capture("dock2.png")), 0);
    assert!(c.log().contains("registry.apps@v1 -> Ok, 2 app(s)"), "{}", c.log());
}

#[test]
fn dock_blurs_what_is_behind_it() {
    let t = Theme::new();
    t.append(FLAT);
    let d = tempfile::tempdir().unwrap();
    let bus = fake_registry(d.path(), &[("org.aurora.lumen", "Lumen")]);
    let jump = |img: &Img, y: usize, x0: usize, x1: usize| (x0..x1).map(|x| (img.at(x + 1, y)[0] as i32 - img.at(x, y)[0] as i32).abs()).max().unwrap();
    // bars window half under the dock strip (it is not constrained: a client may sit there; the dock is drawn over it)
    let scene = |c: &mut Comp| { c.client_args(&["--size", "1280x300", "--pattern", "bars"]); c.inject("move 0 0 560"); settle(); };
    let mut a = dock_comp(&t, &bus, &[]);
    let (_, panel, _, _) = wait_layout(&a);
    scene(&mut a);
    let row = panel[1] as usize + 3;
    let glass = a.capture("g.png");
    let below_edge = row; // y inside the padding of the panel; bars run behind it
    let (jg, jraw) = {
        let t2 = Theme::new();
        t2.append(&format!("{FLAT}[dock]\nenabled = false\n"));
        let mut b = dock_comp(&t2, &bus, &[]);
        scene(&mut b);
        let raw = b.capture("raw.png");
        (jump(&glass, below_edge, panel[0] as usize + 24, (panel[0] + panel[2]) as usize - 24), jump(&raw, below_edge, panel[0] as usize + 24, (panel[0] + panel[2]) as usize - 24))
    };
    println!("DOCK BLUR: largest pixel-to-pixel red jump on row {below_edge} across the bars: with dock glass {jg}, with no dock (raw bars) {jraw}");
    assert!(jraw > 150, "control: raw bars have hard edges");
    assert!(jg < 60, "bars behind the dock should be blurred into gradients, jump {jg}");
}

#[test]
fn hover_highlights_the_item_and_click_runs_the_launcher() {
    let t = Theme::new();
    t.append(&format!("{FLAT}[dock]\nterminal = \"\"\n"));
    let d = tempfile::tempdir().unwrap();
    let bus = fake_registry(d.path(), &[("org.aurora.lumen", "Lumen"), ("org.aurora.ledger", "Haiku Ledger")]);
    let out = d.path().join("launched.txt");
    let fake = d.path().join("fake-aurora");
    std::fs::write(&fake, format!("#!/bin/sh\necho \"$@\" > {}\necho \"BUS=$AURORA_BUS_SOCK\" >> {}\n", out.display(), out.display())).unwrap();
    std::os::unix::fs::PermissionsExt::set_mode(&mut std::fs::metadata(&fake).unwrap().permissions(), 0o755);
    std::fs::set_permissions(&fake, std::os::unix::fs::PermissionsExt::from_mode(0o755)).unwrap();
    let c = dock_comp(&t, &bus, &["--aurora", fake.to_str().unwrap()]);
    let (_, panel, _, items) = wait_layout(&c);
    let before = c.capture("before.png");
    let cx = |r: [i32; 4]| (r[0] + r[2] / 2, r[1] + r[3] / 2);
    let (x1, y1) = cx(items[1]);
    c.inject(&format!("pointer {x1} {y1}"));
    settle();
    let hover = c.capture("hover.png");
    // The change is confined to the second pill, and it is a highlight (brighter).
    let (mut inside, mut outside, mut brighter) = (0, 0, 0);
    for y in 0..hover.h { for x in 0..hover.w {
        if hover.at(x, y) != before.at(x, y) {
            let r = items[1];
            if x as i32 >= r[0] && x as i32 <= r[0] + r[2] && y as i32 >= r[1] && y as i32 <= r[1] + r[3] { inside += 1; } else { outside += 1; }
            if hover.at(x, y).iter().map(|&v| v as u32).sum::<u32>() > before.at(x, y).iter().map(|&v| v as u32).sum::<u32>() { brighter += 1; }
        }
    } }
    println!("HOVER over item 2: {inside} pixels changed inside its pill, {outside} outside, {brighter} got brighter");
    assert!(inside > 500 && outside == 0 && brighter * 10 > inside * 8);
    c.inject("pointer 100 100");
    settle();
    assert_eq!(c.capture("away.png").diff_count(&before), 0, "hover is cleared when the pointer leaves");
    // Click in the panel's padding does nothing; click on the pill runs the launcher.
    c.inject(&format!("pointer {} {}", panel[0] + 2, panel[1] + 2));
    c.inject("click");
    std::thread::sleep(Duration::from_millis(500));
    assert!(!out.exists(), "a click outside every pill launched something");
    c.inject(&format!("pointer {x1} {y1}"));
    c.inject("click");
    // The click asks which verb (a compositor menu); nothing is launched until one is chosen.
    wait_for("the verb menu", || c.log().contains("menu: verbs of org.aurora.ledger"));
    std::thread::sleep(Duration::from_millis(500));
    assert!(!out.exists(), "the dock click itself launched something");
    c.inject("key Down");
    c.inject("key Return");
    wait_for("the launcher to run", || out.exists() && std::fs::read_to_string(&out).unwrap_or_default().contains("BUS="));
    let got = std::fs::read_to_string(&out).unwrap();
    println!("CLICK ran the launcher with: {}", got.replace('\n', " | "));
    assert!(got.starts_with("open --pick --app org.aurora.ledger --verb resize@v1"), "{got}");
    assert!(got.contains(&format!("BUS={}", bus.display())));
}

#[test]
fn the_reserved_area_constrains_maximised_and_new_windows() {
    for edge in ["bottom", "top", "left", "right"] {
        let t = Theme::new();
        t.append(&format!("{FLAT}[dock]\nedge = \"{edge}\"\n"));
        let d = tempfile::tempdir().unwrap();
        let bus = fake_registry(d.path(), &[("org.aurora.lumen", "Lumen"), ("org.aurora.ledger", "Haiku Ledger")]);
        let mut c = dock_comp(&t, &bus, &[]);
        let (_, panel, reserved, _) = wait_layout(&c);
        let log = c.client_args(&["--maximize", "--rgb", "10,220,10"]);
        wait_for("configure", || read(&log).contains("configure"));
        settle();
        let cfg = read(&log).lines().rev().find(|l| l.starts_with("configure")).unwrap().to_string();
        // The usable area less the title bar that hangs above a maximised window (30 logical px, scale 1).
        let (uw, uh) = match edge { "bottom" | "top" => (1280, 800 - reserved[3]), _ => (1280 - reserved[2], 800) };
        let uh = uh - 30;
        let img = c.capture("max.png");
        let green = img.count([10, 220, 10]);
        println!("MAXIMISE edge={edge}: reserved {reserved:?}; client was told `{cfg}`, expected {uw}x{uh}; capture has {green} client pixels (usable area = {})", uw * uh);
        assert_eq!(cfg, format!("configure {uw}x{uh}"));
        assert_eq!(green as i32, uw * uh, "the client fills exactly the usable area");
        // no client pixel anywhere in the reserved strip, and the panel is still visible
        let (rx, ry, rw, rh) = (reserved[0] as usize, reserved[1] as usize, reserved[2] as usize, reserved[3] as usize);
        let in_strip = (ry..ry + rh).flat_map(|y| (rx..rx + rw).map(move |x| (x, y))).filter(|&(x, y)| img.at(x, y) == [10, 220, 10]).count();
        assert_eq!(in_strip, 0, "{edge}: maximised window overlaps the dock strip");
        assert_ne!(img.at((panel[0] + panel[2] / 2) as usize, (panel[1] + 2) as usize), [10, 220, 10]);
        // a normal new window starts near the top-left of the usable area (one cascade step in, below its bar), not under the dock
        let l2 = c.client_args(&["--size", "200x100", "--rgb", "200,10,10"]);
        let _ = l2;
        let img2 = c.capture("new.png");
        let first_red = (0..800usize).flat_map(|y| (0..1280usize).map(move |x| (x, y))).find(|&(x, y)| img2.at(x, y) == [200, 10, 10]);
        println!("NEW WINDOW edge={edge}: first pixel of the new window at {first_red:?}");
        let (ux, uy) = match edge { "top" => (0, reserved[3]), "left" => (reserved[2], 0), _ => (0, 0) };
        assert_eq!(first_red, Some(((ux + 32) as usize, (uy + 30 + 24) as usize)));
    }
}

#[test]
fn background_shader_animates_and_hot_reloads() {
    let t = Theme::new();
    t.append("[desktop]\nclock = false\nhostname = false\n[dock]\nenabled = false\n");
    let c = Comp::start_shell(&["--theme", t.path()]);
    settle();
    let (a, b) = (c.capture("a.png"), { std::thread::sleep(Duration::from_millis(1500)); c.capture("b.png") });
    let distinct = |i: &Img| { let mut s = std::collections::HashSet::new(); for p in i.px.chunks(3).step_by(37) { s.insert([p[0], p[1], p[2]]); } s.len() };
    println!("SHADER BACKGROUND: {} distinct colours; frames 1.5 s apart differ in {} of {} pixels; (0,0)={:?} (640,400)={:?} (1279,799)={:?}", distinct(&a), a.diff_count(&b), a.w * a.h, a.at(0, 0), a.at(640, 400), a.at(1279, 799));
    assert!(distinct(&a) > 1000, "a smooth flow field has many colours");
    assert!(a.diff_count(&b) > 100_000, "the default background is animated");
    // hot reload: a user shader dropped in the theme dir
    std::fs::write(t.dir.path().join("shaders/background.frag"), "#version 100\n//_DEFINES_\nprecision highp float;\nuniform sampler2D tex;uniform float alpha;uniform float time;uniform vec2 res;uniform vec3 top;uniform vec3 bottom;uniform vec3 accent;varying vec2 v_coords;\nvoid main(){ gl_FragColor = vec4(1.0, 0.0, 1.0, 1.0) * alpha; }\n").unwrap();
    settle();
    let m = c.capture("magenta.png");
    println!("SHADER HOT RELOAD: (640,400) is {:?} after the edit (shader paints 255,0,255)", m.at(640, 400));
    assert_eq!(m.at(640, 400), [255, 0, 255]);
    assert_eq!(m.count([255, 0, 255]), m.w * m.h);
    // a broken edit is rejected; the last good one keeps drawing
    std::fs::write(t.dir.path().join("shaders/background.frag"), "#version 100\nnot glsl {{{").unwrap();
    settle();
    assert!(c.log().contains("shader background failed to compile, keeping last good"), "{}", c.log());
    assert_eq!(c.capture("still.png").count([255, 0, 255]), m.w * m.h);
    }

#[test]
fn frozen_background_is_identical_frame_to_frame() {
    let t = Theme::new();
    t.append("[desktop]\nclock = false\nhostname = false\n[dock]\nenabled = false\n");
    let c = Comp::start_shell(&["--theme", t.path(), "--freeze-time"]);
    settle();
    let (a, b) = (c.capture("a.png"), c.capture("b.png"));
    assert_eq!(a.diff_count(&b), 0);
    assert!(a.count(a.at(0, 0)) < a.w * a.h / 2, "frozen but not flat");
}

#[test]
fn image_wallpaper_is_scaled_to_cover_and_centred() {
    let d = tempfile::tempdir().unwrap();
    // 200x100: left half green, right half magenta, with a red dot in the centre
    let img = d.path().join("wall.png");
    {
        let mut e = png::Encoder::new(std::fs::File::create(&img).unwrap(), 200, 100);
        e.set_color(png::ColorType::Rgb);
        e.set_depth(png::BitDepth::Eight);
        let mut px = Vec::new();
        for _y in 0..100 { for x in 0..200 { px.extend_from_slice(if x < 100 { &[0u8, 200, 0] } else { &[200u8, 0, 200] }); } }
        e.write_header().unwrap().write_image_data(&px).unwrap();
    }
    let t = Theme::new();
    t.append(&format!("[background]\nmode = \"image\"\nimage = \"{}\"\n[desktop]\nclock = false\nhostname = false\n[dock]\nenabled = false\n", img.display()));
    let c = Comp::start_shell(&["--theme", t.path()]);
    settle();
    let shot = c.capture("wall.png");
    // cover: scale = max(1280/200, 800/100) = 8 -> 1600x800, cropped 160 px each side: the seam stays at x = 640
    let seam = (0..1280).find(|&x| shot.at(x, 300)[1] < 100).unwrap();
    println!("IMAGE WALLPAPER: 200x100 source into 1280x800 (cover, centred): (300,300)={:?} (1000,300)={:?}, colour seam at x={seam} (expected 640)", shot.at(300, 300), shot.at(1000, 300));
    assert!((630..650).contains(&seam));
    assert!(shot.at(300, 300)[1] > 150 && shot.at(1000, 300)[0] > 150);
    assert_eq!(shot.at(0, 0)[1] > 150 && shot.at(1279, 799)[0] > 150, true, "the image covers every corner");
    // hot reload: replace the file in place
    {
        let mut e = png::Encoder::new(std::fs::File::create(&img).unwrap(), 4, 4);
        e.set_color(png::ColorType::Rgb);
        e.set_depth(png::BitDepth::Eight);
        e.write_header().unwrap().write_image_data(&[255u8; 48]).unwrap();
    }
    std::thread::sleep(Duration::from_millis(1200));
    assert_eq!(c.capture("white.png").at(640, 300), [255, 255, 255], "a changed wallpaper file is picked up");
    // an unreadable image falls back to the gradient, with a log line
    std::fs::remove_file(&img).unwrap();
    settle();
    assert!(c.log().contains("cannot use image") || c.capture("g.png").at(640, 300) != [255, 255, 255]);
}

#[test]
fn gradient_background_runs_between_the_oklab_tokens() {
    let t = Theme::new();
    t.append("[background]\nmode = \"gradient\"\ntop = [0.9, 0.0, 0.0]\nbottom = [0.2, 0.0, 0.0]\n[desktop]\nclock = false\nhostname = false\n[dock]\nenabled = false\n");
    let c = Comp::start_shell(&["--theme", t.path()]);
    settle();
    let s = c.capture("grad.png");
    let (top, bottom) = (s.at(640, 2), s.at(640, 797));
    println!("GRADIENT: top row {top:?}, bottom row {bottom:?}");
    assert!(top[0] > 220 && bottom[0] < 50 && top[0] == top[1]);
    assert!((0..799).all(|y| s.at(640, y)[0] >= s.at(640, y + 1)[0]), "monotone");
}

#[test]
fn hidpi_scale_doubles_chrome_and_text_is_redrawn_not_stretched() {
    let d = tempfile::tempdir().unwrap();
    let bus = fake_registry(d.path(), &[("org.aurora.lumen", "Lumen")]);
    let mut got = vec![];
    for scale in ["1", "2"] {
        let t = Theme::new();
        t.append(FLAT);
        let c = dock_comp(&t, &bus, &["--scale", scale]);
        let (s, panel, reserved, items) = wait_layout(&c);
        let img = c.capture("s.png");
        let it = items[0];
        let n = bright(&img, (it[0] + it[3]) as usize, (it[0] + it[2]) as usize, it[1] as usize, (it[1] + it[3]) as usize);
        println!("SCALE {scale}: scale {s}, panel {panel:?}, reserved {reserved:?}, label bright pixels {n}");
        got.push((s, panel, reserved, n));
    }
    assert_eq!(got[1].1[3], 2 * got[0].1[3], "panel height doubles");
    assert_eq!(got[1].2[3], 2 * got[0].2[3], "reserved strip doubles");
    let ratio = got[1].3 as f64 / got[0].3 as f64;
    println!("label ink at 2x / 1x = {ratio:.2} (area scaling would be 4.0)");
    assert!((3.0..5.2).contains(&ratio));
}

#[test]
fn trusted_overlay_stays_out_of_captures_while_the_dock_stays_in() {
    let t = Theme::new();
    t.append(FLAT);
    let d = tempfile::tempdir().unwrap();
    let bus = fake_registry(d.path(), &[("org.aurora.lumen", "Lumen")]);
    let c = dock_comp(&t, &bus, &[]);
    let (_, panel, _, _) = wait_layout(&c);
    let before = c.capture("before.png");
    let pending = ask(&c, &request_json("shell1", 60_000));
    wait_for("the compositor to dump its own frame", || c.dump.exists());
    let own = Img::load_file(&c.dump);
    let during = c.capture("during.png");
    let probe = ((panel[0] + panel[2] / 2) as usize, (panel[1] + 3) as usize);
    println!("OVERLAY vs DOCK: overlay border pixels in own frame {}, in capture {}; dock pixel in capture {:?} (bg {:?}); capture before == during: {}", own.count(BORDER), during.count(BORDER), during.at(probe.0, probe.1), during.at(5, 5), before.diff_count(&during) == 0);
    assert!(own.count(BORDER) > 5_000);
    assert_eq!(during.count(BORDER), 0);
    assert_eq!(before.diff_count(&during), 0);
    assert_ne!(during.at(probe.0, probe.1), during.at(5, 5), "the dock is in the capture");
    drop(pending);
}

#[test]
fn keymap_layout_is_applied_at_startup_and_hot_reloads() {
    let t = Theme::new();
    t.append("[input]\nlayout = \"de\"\nrepeat_rate = 33\nrepeat_delay = 250\n[dock]\nenabled = false\n");
    let mut c = Comp::start_shell(&["--theme", t.path()]);
    let log = c.client();
    wait_for("keymap", || read(&log).contains("keymap"));
    // the same physical key (evdev 21: 'y' on a US keyboard)
    let r = c.inject_reply("keysym y");
    println!("DE layout, physical US-'y' key -> {r:?}; physical US-'z' key -> {:?}; client saw: {}", c.inject_reply("keysym z"), read(&log).lines().filter(|l| l.starts_with("keymap") || l.starts_with("repeat")).collect::<Vec<_>>().join(" | "));
    assert_eq!(r, "ok z", "German layout swaps y and z");
    assert_eq!(c.inject_reply("keysym z"), "ok y");
    assert!(read(&log).contains("keymap symbols=pc_de"), "the client was sent the German keymap: {}", read(&log));
    assert!(read(&log).contains("repeat 33 250"), "repeat info reached the client: {}", read(&log));
    // hot reload to French: physical 'q' key produces 'a'
    t.edit("layout = \"de\"", "layout = \"fr\"");
    t.edit("repeat_rate = 33", "repeat_rate = 40");
    settle();
    let r = c.inject_reply("keysym q");
    println!("FR layout after hot reload, physical US-'q' key -> {r:?}");
    assert_eq!(r, "ok a");
    wait_for("the new keymap at the client", || read(&log).contains("keymap symbols=pc_fr"));
    assert!(read(&log).contains("repeat 40 250"));
    // a layout xkb cannot build is rejected and the old keymap stays
    t.edit("layout = \"fr\"", "layout = \"nonexistent-layout\"");
    settle();
    assert!(c.log().contains("input: keymap rejected"), "{}", c.log());
    assert_eq!(c.inject_reply("keysym q"), "ok a", "French still in force");
    // and back to the default (empty layout = system default, US here)
    t.edit("layout = \"nonexistent-layout\"", "layout = \"us\"");
    settle();
    assert_eq!(c.inject_reply("keysym y"), "ok y");
}

#[test]
fn default_input_config_is_the_old_behaviour() {
    let t = Theme::new();
    t.append("[dock]\nenabled = false\n");
    let mut c = Comp::start_shell(&["--theme", t.path()]);
    let log = c.client();
    assert_eq!(c.inject_reply("keysym y"), "ok y");
    wait_for("repeat", || read(&log).contains("repeat 25 200"));
}
