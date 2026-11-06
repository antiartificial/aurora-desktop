//! Writes the Phase 1 evidence PNGs and the blur pixel profile. Runs only when
//! AURORA_EVIDENCE_DIR is set:
//!   AURORA_EVIDENCE_DIR=$PWD/../demos/phase-1 cargo test --release --test evidence -- --nocapture

mod common;
use common::*;
use std::{fs, path::PathBuf, time::Duration};

fn settle() { std::thread::sleep(Duration::from_millis(700)); }

#[test]
fn write_phase_1_evidence() {
    let Some(dir) = std::env::var_os("AURORA_EVIDENCE_DIR").map(PathBuf::from) else { eprintln!("AURORA_EVIDENCE_DIR not set; skipping"); return };
    fs::create_dir_all(&dir).unwrap();
    let t = Theme::new();
    let mut c = Comp::start(&["--theme", t.path()]);
    demo_scene(&mut c);
    t.edit("enabled = true", "enabled = false");
    settle();
    c.capture("a.png").save(&dir.join("01-glass-off.png"));
    t.edit("enabled = false", "enabled = true");
    settle();
    c.capture("b.png").save(&dir.join("02-glass-on.png"));
    c.inject("down alt");
    c.inject("key Tab");
    settle();
    c.capture("c.png").save(&dir.join("03-switcher-flip.png"));
    t.edit("layout = \"flip\"", "layout = \"grid\"");
    settle();
    c.capture("d.png").save(&dir.join("04-switcher-grid.png"));
    c.inject("key Right"); // move the selection one card; the border follows
    settle();
    c.capture("e.png").save(&dir.join("05-switcher-grid-next.png"));
    drop(c);

    // The blur profile: plain split window, glass window over the edge, no tint, no highlight.
    let t = Theme::new();
    t.edit("strength = 0.22", "strength = 0.0");
    t.edit("strength = 0.75", "strength = 0.0");
    let mut c = Comp::start(&["--theme", t.path()]);
    c.client_args(&["--size", "400x500", "--pattern", "split"]);
    c.client_args(&["--size", "400x300", "--pattern", "glass", "--rgb", "255,255,255", "--alpha", "0.05"]);
    c.inject("move 0 100 50");
    c.inject("move 1 200 150");
    settle();
    let on = c.capture("on.png");
    on.save(&dir.join("06-edge-glass-on.png"));
    t.edit("enabled = true", "enabled = false");
    settle();
    let off = c.capture("off.png");
    off.save(&dir.join("07-edge-glass-off.png"));
    let r = |i: &Img, y: usize| (260..340).map(|x| i.at(x, y)[0].to_string()).collect::<Vec<_>>().join(" ");
    let txt = format!(
        "Red channel along one pixel row, x = 260..339, across a black/white edge at x = 300.\n\
         The row is inside the glass window (y = 150..450). Tint and edge highlight are 0 in this run,\n\
         so any change across the edge is the blur. Window pixels left of the edge are 13 (5% white\n\
         of the glass window's own translucent fill over black); right of it, 255.\n\n\
         y=350, glass OFF:\n{}\n\ny=350, glass ON:\n{}\n\ny=250, glass ON:\n{}\n",
        r(&off, 350), r(&on, 350), r(&on, 250));
    fs::write(dir.join("edge-profile.txt"), txt).unwrap();
}
