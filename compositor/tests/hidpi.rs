//! HiDPI for clients: the compositor tells clients the real (fractional) scale,
//! honours `wp_viewport`, and places buffers by LOGICAL size at any scale.
//!
//! Everything is decoded from captured pixels. The test client's `bars` pattern
//! has four exact colours; its rectangle in the capture is measured by the
//! bounding box and count of pixels that are exactly those colours.

mod common;
use common::*;

const BARS: [[u8; 3]; 4] = [[230, 30, 30], [30, 200, 60], [40, 70, 240], [250, 220, 20]];

/// Union bounding box and total count of the four bar colours in `img`.
fn bars(img: &Img) -> (usize, usize, usize, usize, usize) {
    let all: Vec<_> = BARS.iter().map(|c| img.bbox(*c).unwrap_or_else(|| panic!("bar colour {c:?} absent from the capture"))).collect();
    (
        all.iter().map(|b| b.0).min().unwrap(), all.iter().map(|b| b.1).min().unwrap(),
        all.iter().map(|b| b.2).max().unwrap(), all.iter().map(|b| b.3).max().unwrap(),
        all.iter().map(|b| b.4).sum(),
    )
}

/// Run the test client with `args` on a headless compositor of `size` at `scale`; return the client's
/// log and the capture.
fn at_scale(scale: &str, size: &str, args: &[&str]) -> (String, Img) {
    let head = format!("--headless={size}");
    let mut c = Comp::start(&[&head, "--scale", scale]);
    let mut a = vec!["--size", "420x300", "--pattern", "bars"];
    a.extend_from_slice(args);
    let log = c.client_args(&a);
    let img = c.capture("c.png");
    (read(&log), img)
}

#[test]
fn the_compositor_advertises_fractional_scale_and_viewporter() {
    let c = Comp::start(&["--scale", "1.45"]);
    let g = c.globals();
    println!("GLOBALS:\n{g}");
    assert!(g.contains("wp_fractional_scale_manager_v1"), "{g}");
    assert!(g.contains("wp_viewporter"), "{g}");
}

#[test]
fn a_client_is_told_the_fractional_scale_and_the_integer_ceiling() {
    let (log, _) = at_scale("1.45", "1856x1160", &["--hidpi"]);
    println!("CLIENT LOG:\n{log}");
    assert!(log.contains("fractional_scale 1.45 (174/120)"), "client was not told 1.45: {log}");
    assert!(log.contains("output_scale 2"), "wl_output carries the integer ceiling: {log}");
    assert!(log.contains("buffer 609x435 logical 420x300 scale 1.45 via fractional+viewport"), "{log}");
}

#[test]
fn a_fractional_scale_client_is_sized_in_logical_units_and_pixel_exact() {
    // (scale, headless size, expected device rectangle of a 420x300 logical client)
    for (scale, size, w, h) in [("1", "1280x800", 420usize, 300usize), ("1.45", "1856x1160", 609, 435), ("2", "1280x800", 840, 600)] {
        let (log, img) = at_scale(scale, size, &["--hidpi"]);
        let (x0, y0, x1, y1, n) = bars(&img);
        println!("SCALE {scale}: client rectangle ({x0},{y0})-({x1},{y1}) = {}x{}, {n} exact bar pixels (buffer: {})", x1 - x0 + 1, y1 - y0 + 1, log.lines().find(|l| l.starts_with("buffer")).unwrap_or(""));
        assert_eq!((x0, y0), (0, 0));
        assert_eq!((x1 - x0 + 1, y1 - y0 + 1), (w, h), "scale {scale}: the client covers its logical size in device pixels");
        // the buffer is mapped 1:1 (no resampling), so every pixel is an exact bar colour
        assert_eq!(n, w * h, "scale {scale}: bar colours are exact over the whole rectangle");
    }
}

#[test]
fn the_same_client_covers_four_times_the_pixels_at_scale_two() {
    let (_, a) = at_scale("1", "1280x800", &["--hidpi"]);
    let (_, b) = at_scale("2", "1280x800", &["--hidpi"]);
    let ((_, _, _, _, n1), (_, _, _, _, n2)) = (bars(&a), bars(&b));
    println!("exact bar pixels: scale 1 = {n1}, scale 2 = {n2}, ratio {}", n2 as f64 / n1 as f64);
    assert_eq!(n2, 4 * n1);
}

#[test]
fn an_integer_only_client_lands_at_its_logical_size() {
    // It reads wl_output.scale (2 for 2.0 and for 1.45), draws 840x600 and says buffer_scale 2.
    for (scale, size, w, h) in [("2", "1280x800", 840usize, 600usize), ("1.45", "1856x1160", 609, 435)] {
        let (log, img) = at_scale(scale, size, &["--integer-scale"]);
        let (x0, y0, x1, y1, n) = bars(&img);
        println!("INTEGER client at scale {scale}: log {:?}; rectangle ({x0},{y0})-({x1},{y1}) = {}x{}, {n} exact pixels", log.lines().filter(|l| l.starts_with("output_scale") || l.starts_with("buffer")).collect::<Vec<_>>(), x1 - x0 + 1, y1 - y0 + 1);
        assert!(log.contains("output_scale 2") && log.contains("buffer 840x600 logical 420x300 scale 2 via integer"), "{log}");
        assert!(!log.contains("fractional_scale"), "an integer-only client never binds the fractional protocol: {log}");
        // allow the one-pixel resampling fringe at 1.45 (840 -> 609)
        assert!((x1 - x0 + 1).abs_diff(w) <= 1 && (y1 - y0 + 1).abs_diff(h) <= 1, "scale {scale}: lands at {w}x{h}");
    }
}

#[test]
fn a_client_that_knows_nothing_of_scale_is_still_logical() {
    // The common case: a plain scale-1 buffer. It is 420x300 LOGICAL, so it covers 840x600 at scale 2.
    let (_, one) = at_scale("1", "1280x800", &[]);
    let (_, two) = at_scale("2", "1280x800", &[]);
    let (a, b) = (bars(&one), bars(&two));
    println!("legacy client: scale 1 covers {}x{}, scale 2 covers {}x{}", a.2 - a.0 + 1, a.3 - a.1 + 1, b.2 - b.0 + 1, b.3 - b.1 + 1);
    assert_eq!((a.2 - a.0 + 1, a.3 - a.1 + 1), (420, 300));
    assert_eq!((b.2 - b.0 + 1, b.3 - b.1 + 1), (840, 600));
    // bar interiors survive the upscale exactly
    assert!(two.at(10, 10) == BARS[0] && two.at(125, 10) == BARS[1] && two.at(225, 10) == BARS[2] && two.at(325, 10) == BARS[3]);
}

#[test]
fn pointer_coordinates_are_logical() {
    // At scale 2 the 420x300 client ends at logical x = 420 (device x = 840). The client reports
    // surface-local coordinates, which are logical by protocol: the injected logical point must
    // arrive unchanged, and 421 must be outside the window.
    let mut c = Comp::start(&["--scale", "2"]);
    let log = c.client_args(&["--size", "420x300", "--pattern", "bars", "--hidpi"]);
    c.inject("pointer 419 150");
    wait_for("pointer enter", || read(&log).contains("pointer_enter"));
    let l = read(&log);
    let enter = l.lines().find(|l| l.starts_with("pointer_enter")).unwrap().to_string();
    println!("client saw: {enter}");
    assert_eq!(enter, "pointer_enter 419 150", "a logical point reaches the client as the same surface-local point");
    c.inject("pointer 421 150");
    wait_for("pointer leave", || read(&log).contains("pointer_leave"));
    // and the device point 419 (logical 209.5) is inside the window too, but 841 is not: the other direction
    c.inject("pointer 209 100");
    wait_for("second enter", || read(&log).matches("pointer_enter").count() >= 2);
    assert!(read(&log).lines().filter(|l| l.starts_with("pointer_enter")).nth(1).unwrap() == "pointer_enter 209 100");
}

#[test]
fn the_trusted_overlay_scales_and_stays_out_of_captures() {
    // 2560x1600 at scale 2 is the same logical output as 1280x800 at scale 1, so the
    // overlay's panel is the same logical size and should be exactly twice as big in pixels.
    let mut dims = vec![];
    for (scale, size) in [("1", "1280x800"), ("2", "2560x1600")] {
        let head = format!("--headless={size}");
        let mut c = Comp::start(&[&head, "--scale", scale]);
        c.client_args(&["--size", "420x300", "--pattern", "bars", "--hidpi"]);
        let before = c.capture("before.png");
        let pending = ask(&c, &request_json("hidpi1", 60_000));
        wait_for("the compositor to dump its own frame", || c.dump.exists());
        let own = Img::load_file(&c.dump);
        let during = c.capture("during.png");
        let (x0, y0, x1, y1, n) = own.bbox(BORDER).expect("overlay border in the compositor's own frame");
        println!("OVERLAY at scale {scale}: panel border box ({x0},{y0})-({x1},{y1}) = {}x{}, {n} border pixels in own frame, {} in capture", x1 - x0 + 1, y1 - y0 + 1, during.count(BORDER));
        assert_eq!(during.count(BORDER), 0, "capture contains overlay pixels at scale {scale}");
        assert_eq!(before.diff_count(&during), 0, "capture during the overlay differs from before it at scale {scale}");
        dims.push((x1 - x0 + 1, y1 - y0 + 1));
        drop(pending);
    }
    assert_eq!(dims[1].0, 2 * dims[0].0, "panel width doubles: {dims:?}");
    assert_eq!(dims[1].1, 2 * dims[0].1, "panel height doubles: {dims:?}");
}

#[test]
fn switcher_cards_scale_with_the_output() {
    // Same logical scene at scale 1 (1280x800) and scale 2 (2560x1600): each card is the
    // window drawn at device size, so its pixel count is four times as large.
    let mut seen = vec![];
    for (scale, size) in [("1", "1280x800"), ("2", "2560x1600")] {
        let t = Theme::new();
        t.edit("layout = \"flip\"", "layout = \"grid\"");
        let head = format!("--headless={size}");
        let mut c = Comp::start(&["--theme", t.path(), &head, "--scale", scale]);
        for (i, rgb) in ["255,0,0", "0,255,0"].iter().enumerate() {
            c.client_args(&["--size", "300x200", "--rgb", rgb, "--hidpi"]);
            c.inject(&format!("move {i} {} {}", 50 + i * 40, 50 + i * 30));
        }
        std::thread::sleep(std::time::Duration::from_millis(700));
        c.inject("down alt");
        c.inject("key Tab");
        std::thread::sleep(std::time::Duration::from_millis(700));
        assert!(c.log().contains("switcher: open, cards=2"), "{}", c.log());
        let img = c.capture("sw.png");
        let n = [img.count([255, 0, 0]), img.count([0, 255, 0])];
        println!("SWITCHER at scale {scale}: card pixels {n:?}");
        assert!(n[0] > 10_000 && n[1] > 10_000);
        seen.push(n);
    }
    for i in 0..2 {
        let ratio = seen[1][i] as f64 / seen[0][i] as f64;
        println!("card {i}: pixel ratio scale2/scale1 = {ratio:.2}");
        assert!((3.7..4.3).contains(&ratio), "card {i} is not sized in logical units: {ratio}");
    }
}
