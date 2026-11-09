//! The surface header, drawn from a `contracts/surface` descriptor.
//!
//! A real headless compositor, a real frame dump, decoded pixels. The fixture
//! descriptor is written here and passed with `--surface-header`, so this does
//! not depend on descriptors arriving over the bus (the relay is a separate
//! change). The fixture's actions and grants are deliberately out of order:
//! the wire form is canonical, and the header must sort the same way or two
//! runs of the same tree would draw different pictures.

mod common;
use common::*;
use std::{fs, path::Path};

/// Mirrors `header::FILL` in src/shell/header.rs. Tests cannot import the
/// binary's internals, and a pixel assertion should name the colour it expects.
const FILL: [u8; 3] = [16, 18, 26];
const MARGIN: f64 = 18.0;

/// The descriptor a surface would publish: unsorted on purpose.
const FIXTURE: &str = r#"{
  "app": "org.aurora.demo",
  "title": "Holiday Photos",
  "actions": [
    {"id": "zoom",   "label": "Zoom In", "effect": "nominal"},
    {"id": "rename", "label": "Rename",  "effect": "write", "grants": ["fs.write"]},
    {"id": "open",   "label": "Open",    "effect": "read"}
  ],
  "grants": ["net.connect", "fs.read"]
}"#;

struct Layout {
    scale: f64,
    panel: [i32; 4],
    grants: usize,
    /// (log text with `_` for spaces, x, y, width), device pixels
    text: Vec<(String, i32, i32, i32)>,
}

fn nums(s: &str) -> Vec<i32> {
    s.split(',').filter_map(|n| n.parse().ok()).collect()
}

fn parse(log: &str) -> Option<Layout> {
    let line = log
        .lines()
        .rev()
        .find(|l| l.contains("surface: header layout"))?;
    let panel = nums(line.split("panel=").nth(1)?.split_whitespace().next()?);
    let scale = line
        .split("scale=")
        .nth(1)?
        .split_whitespace()
        .next()?
        .parse()
        .ok()?;
    let grants = line
        .split("grants=")
        .nth(1)?
        .split_whitespace()
        .next()?
        .parse()
        .ok()?;
    let text = line
        .split("text=")
        .nth(1)
        .unwrap_or("")
        .split('\u{1f}')
        .filter(|t| !t.is_empty())
        .map(|t| {
            let (s, p) = t.rsplit_once('@').unwrap();
            let v = nums(p);
            (s.to_string(), v[0], v[1], v[2])
        })
        .collect();
    Some(Layout {
        scale,
        panel: [panel[0], panel[1], panel[2], panel[3]],
        grants,
        text,
    })
}

fn placed(l: &Layout, name: &str) -> (String, i32, i32, i32) {
    l.text
        .iter()
        .find(|t| t.0 == name)
        .cloned()
        .unwrap_or_else(|| {
            panic!(
                "no text {name:?} in {:?}",
                l.text.iter().map(|t| &t.0).collect::<Vec<_>>()
            )
        })
}

/// Bounding box of text ink inside `r`: all channels above 120, brighter than
/// both the fill and the grants chip, which are dark by design.
fn ink(img: &Img, r: [i32; 4]) -> Option<(i32, i32, i32, i32)> {
    let mut b: Option<(i32, i32, i32, i32)> = None;
    for y in r[1].max(0)..(r[1] + r[3]).min(img.h as i32) {
        for x in r[0].max(0)..(r[0] + r[2]).min(img.w as i32) {
            if img.at(x as usize, y as usize).iter().all(|&v| v > 120) {
                b = Some(match b {
                    None => (x, y, x, y),
                    Some((a, bb, c, d)) => (a.min(x), bb.min(y), c.max(x), d.max(y)),
                });
            }
        }
    }
    b
}

fn count_in(img: &Img, c: [u8; 3], r: [i32; 4]) -> usize {
    let mut n = 0;
    for y in r[1].max(0)..(r[1] + r[3]).min(img.h as i32) {
        for x in r[0].max(0)..(r[0] + r[2]).min(img.w as i32) {
            if img.at(x as usize, y as usize) == c {
                n += 1;
            }
        }
    }
    n
}

fn start(fixture: &Path, scale: &str) -> Comp {
    Comp::start(&[
        "--surface-header",
        fixture.to_str().unwrap(),
        "--scale",
        scale,
        "--freeze-time",
    ])
}

fn dump(c: &Comp, name: &str) -> Img {
    let p = c.dir.path().join(name);
    c.inject(&format!("dump {}", p.display()));
    wait_for("the compositor's own frame", || p.exists());
    Img::load_file(&p)
}

#[test]
fn the_header_shows_the_title_action_labels_and_grants_count_with_no_danger_chrome() {
    let dir = tempfile::tempdir().unwrap();
    let fixture = dir.path().join("surface.json");
    fs::write(&fixture, FIXTURE).unwrap();

    let mut first: Option<(f64, [i32; 4])> = None;
    for scale in ["1", "1.45"] {
        let c = start(&fixture, scale);
        wait_for("a surface header layout", || parse(&c.log()).is_some());
        let l = parse(&c.log()).unwrap();
        let img = dump(&c, &format!("header-{scale}.png"));
        println!(
            "HEADER scale {scale}: panel {:?}, grants {}, text {:?}",
            l.panel,
            l.grants,
            l.text.iter().map(|t| &t.0).collect::<Vec<_>>()
        );

        // 1. It is a real panel in the compositor's own frame, at the margin,
        //    inside the output.
        let m = (MARGIN * l.scale).round() as i32;
        assert_eq!(
            [l.panel[0], l.panel[1]],
            [m, m],
            "the header sits at the usable area's margin"
        );
        assert!(
            l.panel[2] > 100 && l.panel[3] > 20,
            "a header with content has a body: {:?}",
            l.panel
        );
        assert!(
            l.panel[0] + l.panel[2] <= img.w as i32 && l.panel[1] + l.panel[3] <= img.h as i32,
            "header {:?} is inside {}x{}",
            l.panel,
            img.w,
            img.h
        );
        let area = (l.panel[2] * l.panel[3]) as usize;
        let fill = count_in(&img, FILL, l.panel);
        assert!(
            fill * 4 > area * 3,
            "the band is the fill colour ({} of {area} px)",
            fill
        );

        // 2. The title is drawn where text.rs placed it.
        let (title, x, y, w) = placed(&l, "Holiday_Photos");
        let b = ink(&img, [x - 6, y - 4, w + 12, l.panel[3]])
            .unwrap_or_else(|| panic!("no ink for {title:?}"));
        println!("  {title}: expected x {x}..{} ink {b:?}", x + w);
        assert!(
            (b.0 - x).abs() <= 3 && (b.2 - (x + w)).abs() <= 5,
            "title ink {b:?} vs x {x} w {w} at scale {scale}"
        );

        // 3. Every action label is there, in canonical order (by id: open,
        //    rename, zoom) even though the fixture listed them 1, 2, 3.
        let acts: Vec<&(String, i32, i32, i32)> = l
            .text
            .iter()
            .skip(1)
            .filter(|t| t.0 != "2_grants")
            .collect();
        let labels: Vec<&str> = acts.iter().map(|t| t.0.as_str()).collect();
        assert_eq!(
            labels,
            ["Open", "Rename", "Zoom_In"],
            "actions are drawn sorted by id, and all are drawn"
        );
        let mut last = x;
        for (label, ax, ay, aw) in acts {
            assert!(*ax > last, "{label} follows the previous label");
            last = *ax;
            let ab = ink(&img, [*ax - 4, *ay - 3, *aw + 8, l.panel[3]])
                .unwrap_or_else(|| panic!("no ink for {label}"));
            assert!(
                (ab.0 - *ax).abs() <= 4,
                "{label}: ink {ab:?} vs x {ax} at scale {scale}"
            );
        }

        // 4. The grants count is visible, in a chip, on the right of the band:
        //    this is the OS's promise that a surface's access is not hidden.
        assert_eq!(
            l.grants, 2,
            "the count is the descriptor's grants, not the actions'"
        );
        let (g, gx, gy, gw) = placed(&l, "2_grants");
        assert_eq!(
            g.replace('_', " "),
            "2 grants",
            "the count is drawn as words, not a number alone"
        );
        assert!(
            gx + gw <= l.panel[0] + l.panel[2],
            "the chip is inside the band"
        );
        assert!(
            gx > last,
            "the grants count is past the actions, pinned right"
        );
        let gb = ink(&img, [gx - 4, gy - 3, gw + 8, l.panel[3]])
            .unwrap_or_else(|| panic!("no ink for the grants count"));
        assert!((gb.0 - gx).abs() <= 4, "grants ink {gb:?} vs x {gx}");
        // A chip behind it, in the one colour nothing else uses.
        let chip = count_in(&img, [30, 36, 48], l.panel);
        assert!(chip > 200, "the count sits in a chip ({chip} px)");

        // 5. Nothing destructive-looking: no red warning colour anywhere in the
        //    band. The trusted overlay owns that vocabulary; a header that used
        //    it would train people to ignore the prompt that matters.
        let mut red = 0;
        for yy in l.panel[1]..l.panel[1] + l.panel[3] {
            for xx in l.panel[0]..l.panel[0] + l.panel[2] {
                // i32, because u8 addition wraps: 238 + 60 is 42, and that
                // would call ordinary white text a danger colour.
                let (r, g, b) = {
                    let p = img.at(xx as usize, yy as usize);
                    (p[0] as i32, p[1] as i32, p[2] as i32)
                };
                if r > 140 && r > g + 60 && r > b + 60 {
                    red += 1;
                }
            }
        }
        assert_eq!(
            red, 0,
            "no danger colour in the trusted header at scale {scale}"
        );

        // 6. Layout scales with the output, because it is laid out in logical units.
        match first {
            None => first = Some((l.scale, l.panel)),
            Some((s1, p1)) => {
                let ratio = l.panel[2] as f64 / p1[2] as f64;
                assert!(
                    (ratio / (l.scale / s1) - 1.0).abs() < 0.03,
                    "band width scales: ratio {ratio} for scale {scale} from {s1}"
                );
                let ratio_h = l.panel[3] as f64 / p1[3] as f64;
                assert!(
                    (ratio_h / (l.scale / s1) - 1.0).abs() < 0.03,
                    "band height scales: ratio {ratio_h}"
                );
                let logical_x = x as f64 / l.scale;
                let first_logical = MARGIN + 14.0; // margin + PAD_X: both logical constants
                assert!(
                    (logical_x - first_logical).abs() < 1.5,
                    "the title starts at the same logical x at every scale: {logical_x} vs {first_logical}"
                );
            }
        }
    }
}
