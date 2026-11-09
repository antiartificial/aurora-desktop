//! The surface header, drawn from a `contracts/surface` descriptor and bound to
//! the window that descriptor names.
//!
//! A real headless compositor, a real frame dump, decoded pixels. The fixture
//! descriptors are written here and passed with `--surface-header`, so this does
//! not depend on descriptors arriving over the bus (the relay is a separate
//! change). Two properties are what this file is for: the band is drawn against
//! its OWN window's frame, not at the output's corner, and a descriptor with no
//! mapped window draws nothing at all.
//!
//! The fixture's actions and grants are deliberately out of order: the wire form
//! is canonical, and the header must sort the same way or two runs of the same
//! tree would draw different pictures.

mod common;
use common::*;
use std::{collections::HashMap, fs, path::Path};

/// Mirrors `header::FILL` in src/shell/header.rs. Tests cannot import the
/// binary's internals, and a pixel assertion should name the colour it expects.
const FILL: [u8; 3] = [16, 18, 26];
/// Mirrors `header::EDGE_GAP`: between the band's bottom and the window's top.
const EDGE_GAP: f64 = 8.0;
const PHOTOS: &str = "org.aurora.demo.photos";
const NOTES: &str = "org.aurora.demo.notes";
const ABSENT: &str = "org.aurora.demo.absent";

/// The descriptor a surface would publish: unsorted on purpose.
const FIXTURE: &str = r#"{
  "app": "org.aurora.demo.photos",
  "title": "Holiday Photos",
  "actions": [
    {"id": "zoom",   "label": "Zoom In", "effect": "nominal"},
    {"id": "rename", "label": "Rename",  "effect": "write", "grants": ["fs.write"]},
    {"id": "open",   "label": "Open",    "effect": "read"}
  ],
  "grants": ["net.connect", "fs.read"]
}"#;

const NOTES_FIXTURE: &str = r#"{
  "app": "org.aurora.demo.notes",
  "title": "Field Notes",
  "actions": [{"id": "pin", "label": "Pin", "effect": "nominal"}],
  "grants": ["fs.read"]
}"#;

const ABSENT_FIXTURE: &str = r#"{
  "app": "org.aurora.demo.absent",
  "title": "Never Mapped",
  "actions": [{"id": "open", "label": "Open", "effect": "read"}],
  "grants": []
}"#;

#[derive(Clone, Debug)]
struct Layout {
    app: String,
    surface: String,
    scale: f64,
    panel: [i32; 4],
    window: [i32; 4],
    grants: usize,
    /// (log text with `_` for spaces, x, y, width), device pixels
    text: Vec<(String, i32, i32, i32)>,
}

fn nums(s: &str) -> Vec<i32> {
    s.split(',').filter_map(|n| n.parse().ok()).collect()
}

fn rect(s: &str) -> [i32; 4] {
    let v = nums(s.split_whitespace().next().unwrap_or(""));
    [v[0], v[1], v[2], v[3]]
}

fn parse_line(line: &str) -> Option<Layout> {
    let field = |k: &str| line.split(&format!("{k}=")).nth(1);
    let app = field("app")?.split_whitespace().next()?.to_string();
    let surface = field("surface")?.split_whitespace().next()?.to_string();
    let scale = field("scale")?.split_whitespace().next()?.parse().ok()?;
    let grants = field("grants")?.split_whitespace().next()?.parse().ok()?;
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
        app,
        surface,
        scale,
        panel: rect(field("panel")?),
        window: rect(field("window")?),
        grants,
        text,
    })
}

/// The newest layout line per app: a window that moved logs again, and the last
/// statement is where the band is now.
fn layouts(log: &str) -> HashMap<String, Layout> {
    let mut out = HashMap::new();
    for line in log.lines() {
        if let Some(rest) = line.split("surface: header layout ").nth(1) {
            if let Some(l) = parse_line(rest) {
                out.insert(l.app.clone(), l);
            }
        }
    }
    out
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

fn dump(c: &Comp, name: &str) -> Img {
    let p = c.dir.path().join(name);
    c.inject(&format!("dump {}", p.display()));
    wait_for("the compositor's own frame", || p.exists());
    Img::load_file(&p)
}

fn write(dir: &Path, name: &str, body: &str) -> String {
    let p = dir.join(name);
    fs::write(&p, body).unwrap();
    p.to_str().unwrap().to_string()
}

/// A window of `app` at `size`, moved to `(x, y)` LOGICAL once it is mapped.
fn window(c: &mut Comp, app: &str, title: &str, size: &str, at: (i32, i32), index: usize) {
    c.client_args(&[
        "--size", size, "--pattern", "solid", "--rgb", "200,40,200", "--app-id", app,
        "--title", title,
    ]);
    c.inject(&format!("move {index} {} {}", at.0, at.1));
}

#[test]
fn the_header_shows_the_title_action_labels_and_grants_count_at_its_window_edge() {
    let dir = tempfile::tempdir().unwrap();
    let fixture = write(dir.path(), "surface.json", FIXTURE);

    let mut first: Option<(f64, [i32; 4])> = None;
    for scale in ["1", "1.45"] {
        let mut c = Comp::start(&[
            "--surface-header",
            &fixture,
            "--scale",
            scale,
            "--freeze-time",
        ]);
        window(&mut c, PHOTOS, "Holiday Photos", "640x360", (140, 260), 0);
        // The layout is first logged when the client maps, before the move
        // lands; wait for the shell's snapshot to show the moved frame, which is
        // the placement this test is about.
        wait_for("a surface header layout at the moved window", || {
            layouts(&c.log())
                .get(PHOTOS)
                .is_some_and(|l| l.window == [140, 260, 640, 360])
        });
        let l = layouts(&c.log()).remove(PHOTOS).unwrap();
        let img = dump(&c, &format!("header-{scale}.png"));
        println!(
            "HEADER scale {scale}: panel {:?}, window {:?}, grants {}, text {:?}",
            l.panel,
            l.window,
            l.grants,
            l.text.iter().map(|t| &t.0).collect::<Vec<_>>()
        );

        // 1. It is a real panel, against ITS window's top-left and above it:
        //    the band does not sit at the output's corner any more. The log's
        //    window is LOGICAL, the panel is DEVICE pixels, so the comparison
        //    goes through the scale the same way the placement did.
        let sx = |v: i32| (v as f64 * l.scale).round() as i32;
        let edge = (EDGE_GAP * l.scale).round() as i32;
        assert_eq!(
            l.panel[0],
            sx(l.window[0]),
            "the band's left edge is the window's left edge"
        );
        assert_eq!(
            l.panel[1] + l.panel[3],
            sx(l.window[1]) - edge,
            "the band hangs {EDGE_GAP} logical px above the window's top edge"
        );
        assert!(
            l.panel[2] > 200 && l.panel[3] > 20,
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
        assert!(
            l.panel[2] <= sx(l.window[2]),
            "the band is no wider than the window it describes"
        );
        let area = (l.panel[2] * l.panel[3]) as usize;
        let fill = count_in(&img, FILL, l.panel);
        assert!(
            fill * 3 > area * 2,
            "the band is mostly the fill colour ({} of {area} px)",
            fill
        );

        // 2. Nothing is drawn at the corner the band used to occupy. The
        //    background is the legacy scene's flat (25,25,25), so a fill pixel
        //    there would be the header and nothing else.
        let corner = count_in(&img, FILL, [0, 0, 60, 120]);
        assert_eq!(corner, 0, "no header at the output's corner any more");

        // 3. The title is drawn where text.rs placed it, inside the band.
        let (title, x, y, w) = placed(&l, "Holiday_Photos");
        let b = ink(&img, [x - 6, y - 4, w + 12, l.panel[3]])
            .unwrap_or_else(|| panic!("no ink for {title:?}"));
        println!("  {title}: expected x {x}..{} ink {b:?}", x + w);
        assert!(
            (b.0 - x).abs() <= 3 && (b.2 - (x + w)).abs() <= 5,
            "title ink {b:?} vs x {x} w {w} at scale {scale}"
        );

        // 4. Every action label is there, in canonical order (by id: open,
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

        // 5. The grants count is visible, in a chip, on the right of the band:
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

        // 6. Nothing destructive-looking: no red warning colour anywhere in the
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

        // 7. Layout scales with the output, because it is laid out in logical units.
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
                // The band sits at the SAME logical place against the window,
                // because the placement is the window's frame, not a pixel offset.
                let logical_x = l.panel[0] as f64 / l.scale;
                assert!(
                    (logical_x - 140.0).abs() < 1.5,
                    "the band starts at the window's logical x at every scale: {logical_x}"
                );
            }
        }
    }
}

#[test]
fn two_windows_get_two_headers_at_their_own_frames_not_at_the_corner() {
    let dir = tempfile::tempdir().unwrap();
    let photos = write(dir.path(), "photos.json", FIXTURE);
    let notes = write(dir.path(), "notes.json", NOTES_FIXTURE);
    // A descriptor for an app with no window: it must draw nothing at all, not a
    // stray band in the corner. That is the binding refusing to invent a surface.
    let absent = write(dir.path(), "absent.json", ABSENT_FIXTURE);

    let mut c = Comp::start(&[
        "--surface-header",
        &photos,
        "--surface-header",
        &notes,
        "--surface-header",
        &absent,
        "--scale",
        "1",
        "--freeze-time",
    ]);
    window(&mut c, PHOTOS, "Holiday Photos", "640x360", (100, 240), 0);
    // Apart in x as well as y: the photos window must not be over the notes
    // band, or the fill assertion would be measuring the other window.
    window(&mut c, NOTES, "Field Notes", "480x240", (760, 640), 1);
    wait_for("both surface header layouts", || {
        let l = layouts(&c.log());
        l.contains_key(PHOTOS) && l.contains_key(NOTES)
    });
    // The windows are placed by stacking index, which is not the order the
    // fixtures were given; wait until the shell's own snapshot shows both in
    // their moved places, in whichever order the Space stacked them.
    wait_for("both windows to be where the test put them", || {
        let l = layouts(&c.log());
        let mut xs: Vec<i32> = l.values().map(|l| l.window[0]).collect();
        xs.sort();
        xs == [100, 760]
    });

    let log = c.log();
    let all = layouts(&log);
    let (p, n) = (all.get(PHOTOS).unwrap(), all.get(NOTES).unwrap());
    println!(
        "TWO HEADERS: photos panel {:?} window {:?}; notes panel {:?} window {:?}",
        p.panel, p.window, n.panel, n.window
    );

    assert_eq!(all.len(), 2, "two descriptors with windows, two bands");
    assert!(
        !log.contains(&format!("app={ABSENT}")),
        "a descriptor with no mapped window is not drawn anywhere"
    );
    assert_ne!(
        p.surface, n.surface,
        "the two bands are bound to two different surfaces"
    );
    assert_ne!(
        p.window, n.window,
        "the test put the windows in two different places"
    );

    let edge = EDGE_GAP.round() as i32;
    for l in [p, n] {
        let sx = |v: i32| (v as f64 * l.scale).round() as i32;
        assert_eq!(
            l.panel[0],
            sx(l.window[0]),
            "{}: band left edge is its own window's left edge",
            l.app
        );
        assert_eq!(
            l.panel[1] + l.panel[3],
            sx(l.window[1]) - edge,
            "{}: band bottom is {} above its own window's top",
            l.app,
            EDGE_GAP
        );
        assert_ne!(l.panel[0], 0, "{}: the band is not at the output corner", l.app);
        assert!(
            l.panel[2] <= sx(l.window[2]),
            "{}: the band is no wider than its window",
            l.app
        );
    }
    assert_ne!(
        (p.panel[0], p.panel[1]),
        (n.panel[0], n.panel[1]),
        "two windows, two bands at two places -- not both at the corner"
    );

    let img = dump(&c, "two-headers.png");

    // Each band is really there, in ink and in fill, at ITS frame.
    for l in [p, n] {
        let area = (l.panel[2] * l.panel[3]) as usize;
        let fill = count_in(&img, FILL, l.panel);
        assert!(
            fill * 3 > area * 2,
            "{}: the band at {:?} is mostly the fill colour ({} of {area} px)",
            l.app,
            l.panel,
            fill
        );
        let title = if l.app == PHOTOS {
            "Holiday_Photos"
        } else {
            "Field_Notes"
        };
        let (t, x, y, w) = placed(l, title);
        let b = ink(&img, [x - 6, y - 4, w + 12, l.panel[3]])
            .unwrap_or_else(|| panic!("{}: no ink for {t:?}", l.app));
        assert!(
            (b.0 - x).abs() <= 3 && (b.2 - (x + w)).abs() <= 5,
            "{}: title ink {b:?} vs x {x} w {w}",
            l.app
        );
        // The grants count belongs to the right descriptor: 2 for the photos,
        // 1 for the notes. A band bound to the wrong window would show the
        // wrong promise.
        let want = if l.app == PHOTOS { 2 } else { 1 };
        assert_eq!(l.grants, want, "{}: grants from its own descriptor", l.app);
    }

    // And nothing at the corner where both used to be drawn.
    assert_eq!(
        count_in(&img, FILL, [0, 0, 60, 120]),
        0,
        "the corner is empty: the bands are at the windows"
    );
}
