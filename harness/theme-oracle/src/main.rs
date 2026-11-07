//! The compositor's reading of the theme file, as an oracle for the toolkit's.
//!
//! `compositor/src/fx/theme.rs` is included, unmodified, by path: this binary runs the compositor's own `Theme::parse`
//! (so a file the compositor would reject is rejected here) and its own `oklab_to_u8` (the Oklab-to-sRGB rule), and
//! prints what it gets. `demos/theme-drift/run.sh` compares that with what the Interface Kit's reader
//! (`kits/interface/aurora_theme.cpp`) makes of the same file, and with pixels the compositor actually draws.
//!
//!   theme-oracle FILE        one line per token: "theme.colors.NAME R G B", then "background.top R G B"
#[allow(dead_code)]
#[path = "../../../compositor/src/fx/theme.rs"]
mod theme;

fn main() {
    let path = std::env::args().nth(1).expect("usage: theme-oracle FILE");
    let text = std::fs::read_to_string(&path).expect("cannot read the theme file");
    let parsed = theme::Theme::parse(&text).unwrap_or_else(|e| {
        eprintln!("the compositor would reject this file: {e}");
        std::process::exit(1)
    });
    let doc: toml::Table = toml::from_str(&text).expect("not TOML");
    let mut lines = vec![];
    if let Some(colors) = doc.get("theme").and_then(|t| t.get("colors")).and_then(|c| c.as_table()) {
        for (name, v) in colors {
            let a = v.as_array().unwrap_or_else(|| panic!("{name}: not an array"));
            assert!(a.len() == 3, "{name}: not three numbers");
            // serde reads a TOML float as f64 and narrows it to f32 into `[f32; 3]`: the same cast.
            let lab: [f32; 3] = std::array::from_fn(|i| a[i].as_float().or_else(|| a[i].as_integer().map(|n| n as f64)).unwrap() as f32);
            let [r, g, b] = theme::oklab_to_u8(lab);
            lines.push(format!("theme.colors.{name} {r} {g} {b}"));
        }
    }
    let [r, g, b] = theme::oklab_to_u8(parsed.background.top);
    lines.push(format!("background.top {r} {g} {b}"));
    lines.sort();
    for l in lines {
        println!("{l}");
    }
}
