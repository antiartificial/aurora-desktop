//! Glass, hot reload, shader fallback and the switcher, on decoded pixels
//! captured over ext-image-copy-capture from a real headless compositor.

mod common;
use common::*;
use std::time::Duration;

fn contracts_glass() -> String {
    std::fs::read_to_string(std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../contracts/theme/shaders/glass.frag")).unwrap()
}
fn theme_args(t: &Theme) -> [&str; 2] { ["--theme", t.path()] }
fn settle() { std::thread::sleep(Duration::from_millis(700)); } // > poll interval + a frame

/// Scene for the edge tests: an opaque window at x 100..500, black on its left half and
/// white on its right, so the contrast edge is at x = 300; a glass window at x 200..600,
/// y 150..450 covers that edge.
const SPLIT_X: usize = 300;
fn edge_scene(c: &mut Comp) {
    c.client_args(&["--size", "400x500", "--pattern", "split"]);
    c.client_args(&["--size", "400x300", "--pattern", "glass", "--rgb", "255,255,255", "--alpha", "0.05"]);
    c.inject("move 0 100 50");
    c.inject("move 1 200 150");
    settle();
}

/// Red channel along row `y` from x0..x1.
fn row(img: &Img, y: usize, x0: usize, x1: usize) -> Vec<u8> { (x0..x1).map(|x| img.at(x, y)[0]).collect() }

#[test]
fn glass_on_and_off_produce_different_frames() {
    let t = Theme::new();
    let mut c = Comp::start(&theme_args(&t));
    demo_scene(&mut c);
    let on = c.capture("on.png");
    t.edit("enabled = true", "enabled = false");
    settle();
    let off = c.capture("off.png");
    let d = on.diff_count(&off);
    println!("GLASS on vs off: {d} of {} pixels differ", on.w * on.h);
    assert!(d > 50_000, "glass on and off barely differ: {d}");
}

#[test]
fn blur_is_a_gradient_across_a_high_contrast_edge_not_a_step() {
    let t = Theme::new();
    t.edit("strength = 0.22", "strength = 0.0"); // no tint: any change across the edge is the blur
    t.edit("strength = 0.75", "strength = 0.0"); // no edge highlight
    let mut c = Comp::start(&theme_args(&t));
    edge_scene(&mut c);
    let on = c.capture("on.png");
    // y = 350 is inside the glass window (y 150..450) and below its title strip; the black/white edge is at x = 300.
    let glass = row(&on, 350, SPLIT_X - 40, SPLIT_X + 40);
    t.edit("enabled = true", "enabled = false");
    settle();
    let flat = row(&c.capture("off.png"), 350, SPLIT_X - 40, SPLIT_X + 40);
    println!("ROW y=350, x={}..{} glass OFF (red channel): {flat:?}", SPLIT_X - 40, SPLIT_X + 40);
    println!("ROW y=350, x={}..{} glass ON  (red channel): {glass:?}", SPLIT_X - 40, SPLIT_X + 40);
    let mid = |r: &[u8]| r.iter().filter(|&&v| (40..215).contains(&v)).count();
    assert!(mid(&flat) <= 2, "control: without glass the edge should be a step");
    assert!(mid(&glass) >= 10, "blurred edge should pass through many intermediate values, got {}: {glass:?}", mid(&glass));
    let steps = glass.windows(2).map(|w| (w[1] as i32 - w[0] as i32).abs()).max().unwrap();
    assert!(steps < 40, "largest single-pixel jump {steps} is a step, not a gradient");
    let (lo, hi) = (glass[0], *glass.last().unwrap());
    assert!(lo < 60 && hi > 195, "far ends should still be dark and light: {lo} {hi}");
    assert!(glass.windows(2).all(|w| w[1] as i32 + 3 >= w[0] as i32), "gradient not monotone: {glass:?}");
    println!("max single-pixel jump with glass: {steps}; intermediate pixels: {} of {}", mid(&glass), glass.len());
}

#[test]
fn hot_reload_picks_up_a_parameter_change() {
    let t = Theme::new();
    t.edit("strength = 0.22", "strength = 0.0");
    let mut c = Comp::start(&theme_args(&t));
    edge_scene(&mut c);
    let a = c.capture("a.png");
    t.edit("strength = 0.0", "strength = 0.9"); // heavy tint
    settle();
    let b = c.capture("b.png");
    let d = a.diff_count(&b);
    println!("HOT RELOAD tint 0 -> 0.9: {d} pixels changed, (400,400) {:?} -> {:?}; compositor log: {}", a.at(400, 400), b.at(400, 400), c.log().matches("theme: loaded").count());
    assert!(d > 20_000);
    assert!(c.log().matches("theme: loaded").count() >= 2, "no reload logged");
    t.edit("blur_passes = 3", "blur_passes = 5"); // the chain is rebuilt live
    settle();
    let e = c.capture("c.png");
    assert!(b.diff_count(&e) > 100, "changing the pass count changed nothing");
}

#[test]
fn a_broken_shader_falls_back_to_the_last_good_one_and_the_compositor_survives() {
    let t = Theme::new();
    let mut c = Comp::start(&theme_args(&t));
    edge_scene(&mut c);
    let good = c.capture("good.png");
    t.write_shader("glass", "#version 100\n//_DEFINES_\nthis is not glsl {{{\n");
    settle();
    let log = c.log();
    assert!(log.contains("shader glass failed to compile, keeping last good"), "{log}");
    let after = c.capture("after.png"); // still serving frames
    assert_eq!(good.diff_count(&after), 0, "frame changed although the shader was rejected");
    // the same for the theme file, then recovery with a valid edit
    std::fs::write(t.dir.path().join("aurora-glass.toml"), "[glass\nnope").unwrap();
    settle();
    assert!(c.log().contains("theme: rejected, keeping last good"));
    assert_eq!(good.diff_count(&c.capture("after2.png")), 0);
    // recovery: a valid edit is picked up again, and a valid shader change is visible
    t.write_shader("glass", &contracts_glass().replace("col = mix(col, tint.rgb, tint.a);", "col = vec3(1.0, 0.0, 0.0) * 0.5 + col * 0.5;"));
    settle();
    let red = c.capture("red.png");
    assert!(good.diff_count(&red) > 20_000, "a valid shader edit was not picked up");
    println!("SHADER HOT RELOAD: valid edit after the broken one changed {} pixels", good.diff_count(&red));
    println!("BROKEN SHADER: compositor alive, frame identical to last good ({} px differ)", good.diff_count(&after));
}

#[test]
fn switcher_shows_one_card_per_window() {
    for n in [2usize, 3, 4] {
        let t = Theme::new();
        t.edit("layout = \"flip\"", "layout = \"grid\""); // flat cards: colours stay exact, so they can be counted
        let mut c = Comp::start(&theme_args(&t));
        let colours: [[u8; 3]; 4] = [[255, 0, 0], [0, 255, 0], [0, 0, 255], [255, 255, 0]];
        for (i, rgb) in colours.iter().take(n).enumerate() {
            c.client_args(&["--size", "300x200", "--rgb", &format!("{},{},{}", rgb[0], rgb[1], rgb[2])]);
            c.inject(&format!("move {i} {} {}", 50 + i * 40, 50 + i * 30));
        }
        settle();
        c.inject("down alt");
        c.inject("key Tab");
        settle();
        assert!(c.log().contains(&format!("switcher: open, cards={n}")), "{}", c.log());
        let img = c.capture("sw.png");
        let seen: Vec<usize> = colours.iter().take(n).map(|k| img.count(*k)).collect();
        println!("SWITCHER n={n}: pixels of each window's colour in the capture: {seen:?}");
        // Each card is a full thumbnail: far more pixels than the sliver of a cascaded window left visible.
        for (i, px) in seen.iter().enumerate() { assert!(*px > 10_000, "card {i} of {n} missing ({px} px)"); }
        c.inject("up alt"); // release alt: commits and closes
        settle();
        assert!(c.log().contains("switcher: commit"));
    }
}

#[test]
fn flip_layout_draws_perspective_cards() {
    let mut c = Comp::start(&[]);
    for rgb in ["255,0,0", "0,255,0", "0,0,255"] { c.client_args(&["--size", "500x350", "--rgb", rgb]); }
    c.inject("down alt");
    c.inject("key Tab");
    settle();
    let img = c.capture("flip.png");
    // A card turned about the vertical axis has slanted top and bottom edges: where the
    // green card's top edge sits depends on x. (An unrotated card would give equal values.)
    let top = |x: usize| (0..img.h).find(|&y| { let p = img.at(x, y); p[1] > 200 && p[0] < 40 && p[2] < 40 });
    let (a, b) = (top(200), top(500));
    println!("FLIP: first green row at x=200: {a:?}, at x=500: {b:?}");
    assert!(matches!((a, b), (Some(a), Some(b)) if (a as i32 - b as i32).abs() > 10), "card edge is not slanted: {a:?} {b:?}");
}

#[test]
fn overlay_is_still_excluded_from_captures_of_a_glass_scene() {
    let mut c = Comp::start(&[]);
    demo_scene(&mut c);
    let before = c.capture("before.png");
    let pending = ask(&c, &request_json("g1", 60_000));
    wait_for("the compositor to dump its own frame", || c.dump.exists());
    let own = Img::load_file(&c.dump);
    let during = c.capture("during.png");
    println!("GLASS SCENE: overlay border pixels in compositor's own frame {}, in capture {}; capture before == during: {}", own.count(BORDER), during.count(BORDER), before.diff_count(&during) == 0);
    assert!(own.count(BORDER) > 5_000);
    assert_eq!(during.count(BORDER), 0);
    assert_eq!(before.diff_count(&during), 0, "capture during overlay differs from before");
    drop(pending);
}
