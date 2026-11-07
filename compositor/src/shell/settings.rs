//! The settings panel: compositor-drawn system UI for what is genuinely
//! settable today. Every control is a pair of "what to show" and "which theme
//! edits a click makes" (`Edit`s, written back to the theme file by
//! `Fx::apply_edits` and applied by the normal hot reload), so the panel
//! never holds a second copy of any setting: it is rebuilt from the theme
//! after every change. Things that are not settable are rows too, shown as
//! read-only with the reason.
//!
//! Layout is arithmetic in LOGICAL units (`build`), producing a display list
//! (`Built`): text, pills and hotspots. Drawing, hit-testing and the log lines
//! tests read all come from that one list.

use super::menu::{Rt, Style};
use crate::{
    fx::{
        edit::{Edit, Val},
        theme::{BgMode, DockEdge, Theme},
    },
    text::{FontSpec, TextSystem},
    trusted::draw::Canvas,
};

pub const WIDTH: f32 = 600.0;
const M: f32 = 22.0;
const X0: f32 = 190.0;

pub struct Ctx<'a> {
    pub theme: &'a Theme,
    pub host_scale: f64,
    pub scale: f64,
    pub out_phys: (i32, i32),
    /// The output's refresh in mHz, as the mode reports it. 0 when there is no mode.
    pub refresh_mhz: i32,
    /// How many outputs there are. One, until the DRM backend drives more than one connector.
    pub outputs: usize,
    pub families: &'a [String],
    /// True when the compositor is a client of another one. Input settings reach libinput, which
    /// only exists on the DRM backend, so nested is the case where they are inert.
    pub nested: bool,
    /// The machine's real network interfaces (deskbar::sysinfo::interfaces). Passed in rather
    /// than read here, so this stays a pure function of its context and the Network page can be
    /// tested against any machine's shape.
    pub ifaces: &'a [crate::deskbar::sysinfo::Iface],
}

#[derive(Clone, Debug)]
pub struct Seg {
    pub label: String,
    pub edits: Vec<Edit>,
    pub on: bool,
}

#[derive(Clone, Debug)]
pub struct Part {
    pub label: String,
    pub dec: Option<Vec<Edit>>,
    pub inc: Option<Vec<Edit>>,
}

#[derive(Clone, Debug)]
pub enum Control {
    Segments(Vec<Seg>),
    Cycle(Part),
    Stepper(Part),
    Toggle { on: bool, flip: Vec<Edit> },
    /// A colour token: swatch and three steppers (L, a, b).
    Oklab { swatch: [u8; 3], comps: Vec<Part> },
    /// Not settable here. `value` is what is true now; the row's note says why.
    Fixed { value: String },
}

#[derive(Clone, Debug)]
pub struct Row {
    pub label: String,
    pub ctl: Control,
    pub note: Option<String>,
    /// Keyboard Left / Right.
    pub left: Option<Vec<Edit>>,
    pub right: Option<Vec<Edit>>,
}

#[derive(Clone, Debug)]
pub struct Page {
    pub title: &'static str,
    pub rows: Vec<Row>,
}

// ---- what is on the pages ---------------------------------------------------

fn clamp_step(v: f64, d: f64, lo: f64, hi: f64) -> Option<f64> {
    let n = ((v + d) * 10000.0).round() / 10000.0;
    ((lo..=hi).contains(&n) && (n - v).abs() > 1e-9).then_some(n)
}

fn stepper(v: f64, (lo, hi, step): (f64, f64, f64), fmt: impl Fn(f64) -> String, mk: impl Fn(f64) -> Vec<Edit>) -> Part {
    Part { label: fmt(v), dec: clamp_step(v, -step, lo, hi).map(&mk), inc: clamp_step(v, step, lo, hi).map(&mk) }
}

fn row(label: &str, ctl: Control) -> Row {
    let (left, right) = match &ctl {
        Control::Segments(s) => {
            let i = s.iter().position(|g| g.on);
            (i.and_then(|i| i.checked_sub(1)).map(|i| s[i].edits.clone()), i.and_then(|i| s.get(i + 1)).map(|g| g.edits.clone()))
        }
        Control::Cycle(p) | Control::Stepper(p) => (p.dec.clone(), p.inc.clone()),
        Control::Toggle { on, flip } => (on.then(|| flip.clone()), (!on).then(|| flip.clone())),
        Control::Oklab { comps, .. } => (comps[0].dec.clone(), comps[0].inc.clone()),
        Control::Fixed { .. } => (None, None),
    };
    Row { label: label.into(), ctl, note: None, left, right }
}

fn noted(mut r: Row, n: impl Into<String>) -> Row {
    r.note = Some(n.into());
    r
}

fn cycle(label: &str, opts: &[(&str, Vec<Edit>)], cur: usize) -> Row {
    let n = opts.len();
    row(label, Control::Cycle(Part { label: opts[cur].0.to_string(), dec: Some(opts[(cur + n - 1) % n].1.clone()), inc: Some(opts[(cur + 1) % n].1.clone()) }))
}

fn oklab(label: &str, key: &'static str, v: [f32; 3]) -> Row {
    let part = |i: usize, name: &str, step: f64, lo: f64, hi: f64, dp: usize| {
        let mk = |n: f64| {
            let mut c = v;
            c[i] = n as f32;
            vec![Edit::new("background", key, Val::Vec3(c))]
        };
        let cur = v[i] as f64;
        Part { label: format!("{name} {cur:.dp$}"), dec: clamp_step(cur, -step, lo, hi).map(&mk), inc: clamp_step(cur, step, lo, hi).map(&mk) }
    };
    let swatch = crate::fx::theme::oklab_to_u8(v);
    row(label, Control::Oklab { swatch, comps: vec![part(0, "L", 0.02, 0.0, 1.0, 2), part(1, "a", 0.01, -0.4, 0.4, 3), part(2, "b", 0.01, -0.4, 0.4, 3)] })
}

pub fn pages(c: &Ctx) -> Vec<Page> {
    let t = c.theme;
    // Display
    let scales: [(f64, &str); 7] = [(0.0, "Auto"), (1.0, "1"), (1.25, "1.25"), (1.5, "1.5"), (1.75, "1.75"), (2.0, "2"), (3.0, "3")];
    let scale_row = noted(
        row("Scale", Control::Segments(scales.iter().map(|&(v, l)| Seg { label: l.into(), edits: vec![Edit::n("output", "scale", v)], on: (t.output.scale - v).abs() < 1e-6 }).collect())),
        format!("Now {:.2}. Auto follows the host, which reports {:.2}.", c.scale, c.host_scale),
    );
    let fixed = |label: &str, value: String, reason: &str| noted(row(label, Control::Fixed { value }), reason);
    let display = Page {
        title: "Display",
        rows: vec![
            scale_row,
            fixed(
                "Resolution",
                format!("{} \u{d7} {}", c.out_phys.0, c.out_phys.1),
                if c.nested { "The host window sets it; resize the window to change it." } else { "From the connector's current mode." },
            ),
            // The refresh is READ rather than described. It was "set by the host", which was
            // true and useless: the compositor has always known the number, because the output
            // carries a mode, and a settings page that declines to show a figure it holds is
            // just being coy.
            fixed(
                "Refresh rate",
                if c.refresh_mhz > 0 { format!("{:.2} Hz", c.refresh_mhz as f64 / 1000.0) } else { "unknown".into() },
                if c.nested { "The host compositor paces our frames." } else { "From the connector's current mode." },
            ),
            fixed(
                "Monitors",
                if c.nested { "1 (the host window)".into() } else { format!("{}", c.outputs.max(1)) },
                if c.nested { "Multi-monitor needs the DRM/KMS backend." } else { "Driving more than one connector is not built." },
            ),
        ],
    };
    // Desktop
    let b = &t.background;
    let modes = [(BgMode::Shader, "shader", "Shader"), (BgMode::Image, "image", "Image"), (BgMode::Gradient, "gradient", "Gradient"), (BgMode::Solid, "solid", "Solid")];
    let mut mode_row = row("Background", Control::Segments(modes.iter().map(|&(m, k, l)| Seg { label: l.into(), edits: vec![Edit::s("background", "mode", k)], on: b.mode == m }).collect()));
    if b.image.is_empty() {
        mode_row = noted(mode_row, "Image needs a path: set [background] image in the theme file.");
    }
    // Network. Read-only on purpose: changing an interface's state is NetworkManager's job and
    // decision 0015's, not a compositor's. What the page is for is answering "am I actually on
    // the network, and by what", which until now only existed as one word in the deskbar.
    let network = Page {
        title: "Network",
        rows: if c.ifaces.is_empty() {
            vec![noted(
                row("Interfaces", Control::Fixed { value: "none".into() }),
                "No interface is backed by hardware. Virtual ones (docker, bridges, VPN tunnels) are not listed.",
            )]
        } else {
            c.ifaces
                .iter()
                .map(|i| {
                    let kind = if i.wireless { "wireless" } else { "wired" };
                    let state = if i.up { "up" } else { "down" };
                    let value = match i.mbit {
                        Some(m) if i.up => format!("{kind}, {state}, {m} Mbit/s"),
                        _ => format!("{kind}, {state}"),
                    };
                    noted(
                        row(&i.name, Control::Fixed { value }),
                        if i.wireless {
                            "The network's name needs netlink and is not read here."
                        } else {
                            "Speed is what the driver reports for the link."
                        },
                    )
                })
                .collect()
        },
    };

    let desktop = Page { title: "Desktop", rows: vec![mode_row, oklab("Top", "top", b.top), oklab("Bottom", "bottom", b.bottom), oklab("Accent", "accent", b.accent)] };
    // Dock and text
    let d = &t.dock;
    let edges = [(DockEdge::Bottom, "bottom", "Bottom"), (DockEdge::Left, "left", "Left"), (DockEdge::Top, "top", "Top"), (DockEdge::Right, "right", "Right")];
    let edge_row = row("Dock edge", Control::Segments(edges.iter().map(|&(e, k, l)| Seg { label: l.into(), edits: vec![Edit::s("dock", "edge", k)], on: d.edge == e }).collect()));
    let show = row("Dock", Control::Toggle { on: d.enabled, flip: vec![Edit::new("dock", "enabled", Val::Bool(!d.enabled))] });
    let size = row("Dock size", Control::Stepper(stepper(d.item_height as f64, (24.0, 64.0, 4.0), |v| format!("{v:.0} px"), |v| vec![Edit::n("dock", "item_height", v)])));
    let mut fam: Vec<(&str, Vec<Edit>)> = vec![("Automatic", vec![Edit::s("font", "family", "")])];
    fam.extend(c.families.iter().map(|f| (f.as_str(), vec![Edit::s("font", "family", f)])));
    let fcur = fam.iter().position(|(l, e)| e[0].val == Val::Str(t.font.family.clone()) && (*l != "Automatic" || t.font.family.is_empty())).unwrap_or_else(|| {
        fam.push((t.font.family.as_str(), vec![Edit::s("font", "family", &t.font.family)]));
        fam.len() - 1
    });
    let font = cycle("Font", &fam, fcur);
    let fsize = row("Font size", Control::Stepper(stepper(t.font.size as f64, (10.0, 24.0, 1.0), |v| format!("{v:.0} px"), |v| vec![Edit::n("font", "size", v)])));
    let dock = Page { title: "Dock & Text", rows: vec![edge_row, show, size, font, fsize] };
    // Keyboard
    let i = &t.input;
    let layouts = ["", "us", "gb", "de", "fr", "es", "it", "se", "no", "dk", "fi", "pl", "ru", "br", "pt", "ch", "cz"];
    let variants = ["", "dvorak", "colemak", "intl", "altgr-intl", "nodeadkeys", "mac", "phonetic"];
    let build = |all: &[&'static str], cur: &str, key: &'static str, blank: &'static str| {
        let mut o: Vec<(String, Vec<Edit>)> = all.iter().map(|&v| (if v.is_empty() { blank.to_string() } else { v.to_string() }, vec![Edit::s("input", key, v)])).collect();
        let at = all.iter().position(|v| *v == cur).unwrap_or_else(|| {
            o.push((cur.to_string(), vec![Edit::s("input", key, cur)]));
            o.len() - 1
        });
        let refs: Vec<(&str, Vec<Edit>)> = o.iter().map(|(l, e)| (l.as_str(), e.clone())).collect();
        cycle(if key == "layout" { "Layout" } else { "Variant" }, &refs, at)
    };
    let keyboard = Page {
        title: "Keyboard",
        rows: vec![
            noted(build(&layouts, &i.layout, "layout", "Default (us)"), "xkb checks the pair first; a combination it rejects is not written."),
            build(&variants, &i.variant, "variant", "None"),
            row("Repeat rate", Control::Stepper(stepper(i.repeat_rate as f64, (5.0, 60.0, 5.0), |v| format!("{v:.0} / s"), |v| vec![Edit::new("input", "repeat_rate", Val::Int(v as i64))]))),
            row("Repeat delay", Control::Stepper(stepper(i.repeat_delay as f64, (100.0, 800.0, 50.0), |v| format!("{v:.0} ms"), |v| vec![Edit::new("input", "repeat_delay", Val::Int(v as i64))]))),
        ],
    };
    // Pointer. These reach libinput, which only exists on the DRM backend: while nested, the
    // host owns the devices and nothing here would take effect. Saying so on every row beats a
    // control that looks live and is not -- the Display page already takes that line about
    // resolution and refresh rate.
    let only_on_drm = if c.nested { " Applies on DRM/KMS; the host owns input while nested." } else { "" };
    let pointer = Page {
        title: "Pointer",
        rows: vec![
            noted(
                row("Pointer speed", Control::Stepper(stepper(i.pointer_speed, (-1.0, 1.0, 0.25), |v| if v == 0.0 { "Default".into() } else { format!("{v:+.2}") }, |v| vec![Edit::n("input", "pointer_speed", v)]))),
                format!("libinput acceleration, -1 slowest to 1 fastest.{only_on_drm}"),
            ),
            noted(
                row("Natural scrolling", Control::Toggle { on: i.natural_scroll, flip: vec![Edit::new("input", "natural_scroll", Val::Bool(!i.natural_scroll))] }),
                format!("Scrolling moves the content, not the view.{only_on_drm}"),
            ),
            noted(
                row("Tap to click", Control::Toggle { on: i.tap_to_click, flip: vec![Edit::new("input", "tap_to_click", Val::Bool(!i.tap_to_click))] }),
                format!("Touchpads only; a mouse has no tap.{only_on_drm}"),
            ),
            noted(
                row("Left handed", Control::Toggle { on: i.left_handed, flip: vec![Edit::new("input", "left_handed", Val::Bool(!i.left_handed))] }),
                format!("Swaps the buttons.{only_on_drm}"),
            ),
        ],
    };
    vec![display, network, desktop, dock, keyboard, pointer]
}

// ---- layout ----------------------------------------------------------------

#[derive(Clone, Debug)]
pub enum Prim {
    Text { x: f32, y: f32, s: String, size: f32, bold: bool, a: u8, white: bool },
    Pill { r: Rt, rad: f32, col: [u8; 4] },
    Swatch { r: Rt, col: [u8; 3] },
}

#[derive(Clone, Debug, PartialEq)]
pub enum HotKind {
    Do(Vec<Edit>),
    Tab(usize),
    Close,
}

#[derive(Clone, Debug)]
pub struct Hot {
    pub r: Rt,
    pub kind: HotKind,
    pub row: Option<usize>,
}

#[derive(Clone, Debug, Default)]
pub struct Built {
    pub size: (f32, f32),
    pub prims: Vec<Prim>,
    pub hots: Vec<Hot>,
    /// The rows' rectangles (for the keyboard selection highlight).
    pub rows: Vec<Rt>,
    /// Height of the title band, the panel's drag handle: a press anywhere in
    /// `y < drag_h` that misses a hotspot starts a move. Set before any early
    /// return from `build`, so a panel with no page is still draggable.
    pub drag_h: f32,
}

pub fn clip(t: &mut TextSystem, s: &str, spec: &FontSpec, max: f32, scale: f64) -> String {
    if t.string_width(s, spec, scale) <= max {
        return s.to_string();
    }
    let mut out: String = s.to_string();
    while !out.is_empty() && t.string_width(&format!("{out}..."), spec, scale) > max {
        out.pop();
    }
    format!("{out}...")
}

pub fn build(pages: &[Page], page: usize, status: &str, font: f32, scale: f64, min_h: f32, t: &mut TextSystem) -> Built {
    let mut b = Built::default();
    let spec = FontSpec::new(font);
    let small = FontSpec::new(font * 0.85);
    let (m, sm) = (t.metrics(&spec), t.metrics(&small));
    let ih = (font * 1.9).round();
    let faint = [255, 255, 255, 26];
    let line = |met: &crate::text::FontMetrics, top: f32, h: f32| top + (h - (met.ascent + met.descent)) / 2.0;
    let text = |b: &mut Built, x: f32, y: f32, s: &str, size: f32, bold: bool, a: u8, white: bool| b.prims.push(Prim::Text { x, y, s: s.into(), size, bold, a, white });

    // title and close
    let tspec = FontSpec::new(font * 1.35).bold();
    let tm = t.metrics(&tspec);
    let th = (tm.ascent + tm.descent).ceil();
    text(&mut b, M, 16.0, "Settings", font * 1.35, true, 255, false);
    let close = Rt { x: WIDTH - M - ih, y: 16.0 + (th - ih) / 2.0, w: ih, h: ih };
    b.prims.push(Prim::Pill { r: close, rad: 6.0, col: faint });
    let xw = t.string_width("\u{d7}", &spec, scale);
    text(&mut b, close.x + (ih - xw) / 2.0, line(&m, close.y, ih), "\u{d7}", font, false, 255, false);
    b.hots.push(Hot { r: close, kind: HotKind::Close, row: None });

    // tabs
    let ty = 16.0 + th + 10.0;
    // Everything above the tabs is the drag handle: the title, and the gap
    // beside it. The close button is a hotspot and wins the press.
    b.drag_h = ty;
    let mut x = M;
    for (i, p) in pages.iter().enumerate() {
        let w = (t.string_width(p.title, &spec, scale) + 26.0).ceil();
        let r = Rt { x, y: ty, w, h: ih };
        if i == page {
            b.prims.push(Prim::Pill { r, rad: 7.0, col: [255, 255, 255, 52] });
        }
        text(&mut b, x + 13.0, line(&m, ty, ih), p.title, font, i == page, if i == page { 255 } else { 170 }, false);
        b.hots.push(Hot { r, kind: HotKind::Tab(i), row: None });
        x += w + 6.0;
    }

    // rows
    let base = (font * 2.5).round();
    let extra = (font * 1.2).round();
    let cw = WIDTH - M - X0;
    let mut y = ty + ih + 14.0;
    let Some(pg) = pages.get(page) else { return b };
    for (ri, r) in pg.rows.iter().enumerate() {
        let h = base + if r.note.is_some() { extra } else { 0.0 };
        b.rows.push(Rt { x: M - 8.0, y, w: WIDTH - 2.0 * M + 16.0, h });
        let fixed = matches!(r.ctl, Control::Fixed { .. });
        text(&mut b, M, line(&m, y, base), &r.label, font, false, if fixed { 150 } else { 255 }, false);
        let cy = y + (base - ih) / 2.0;
        let hot = |b: &mut Built, r: Rt, e: &Option<Vec<Edit>>| {
            if let Some(e) = e {
                b.hots.push(Hot { r, kind: HotKind::Do(e.clone()), row: Some(ri) });
            }
        };
        let zone = ih;
        let stepper = |b: &mut Built, t: &mut TextSystem, x: f32, w: f32, p: &Part| {
            for (gx, g, e) in [(x, "\u{2212}", &p.dec), (x + w - zone, "+", &p.inc)] {
                let zr = Rt { x: gx, y: cy, w: zone, h: ih };
                b.prims.push(Prim::Pill { r: zr, rad: 6.0, col: faint });
                let gw = t.string_width(g, &spec, scale);
                text(b, gx + (zone - gw) / 2.0, line(&m, cy, ih), g, font, false, if e.is_some() { 255 } else { 70 }, false);
                hot(b, zr, e);
            }
            let inner = w - 2.0 * zone - 12.0;
            let lab = clip(t, &p.label, &spec, inner, scale);
            let lw = t.string_width(&lab, &spec, scale);
            text(b, x + zone + 6.0 + (inner - lw) / 2.0, line(&m, cy, ih), &lab, font, false, 255, false);
        };
        match &r.ctl {
            Control::Segments(segs) => {
                let widths: Vec<f32> = segs.iter().map(|s| t.string_width(&s.label, &spec, scale)).collect();
                let pad = ((cw - widths.iter().sum::<f32>() - 4.0 * segs.len() as f32) / segs.len() as f32).min(22.0).max(6.0);
                let mut sx = X0;
                for (s, w) in segs.iter().zip(widths) {
                    let sr = Rt { x: sx, y: cy, w: (w + pad).ceil(), h: ih };
                    // The fully white pill is the marker for "accent"; `draw` swaps it.
                    b.prims.push(Prim::Pill { r: sr, rad: 7.0, col: if s.on { [255, 255, 255, 255] } else { faint } });
                    text(&mut b, sx + (sr.w - w) / 2.0, line(&m, cy, ih), &s.label, font, false, 255, s.on);
                    b.hots.push(Hot { r: sr, kind: HotKind::Do(s.edits.clone()), row: Some(ri) });
                    sx += sr.w + 4.0;
                }
            }
            Control::Cycle(p) => stepper(&mut b, t, X0, cw, p),
            Control::Stepper(p) => stepper(&mut b, t, X0, 230.0, p),
            Control::Toggle { on, flip } => {
                let (tw, th2) = (46.0, (ih * 0.78).round());
                let tr = Rt { x: X0, y: cy + (ih - th2) / 2.0, w: tw, h: th2 };
                b.prims.push(Prim::Pill { r: tr, rad: th2 / 2.0, col: if *on { [255, 255, 255, 255] } else { [255, 255, 255, 50] } });
                let k = th2 - 6.0;
                b.prims.push(Prim::Pill { r: Rt { x: tr.x + if *on { tw - k - 3.0 } else { 3.0 }, y: tr.y + 3.0, w: k, h: k }, rad: k / 2.0, col: [255, 255, 255, 235] });
                text(&mut b, X0 + tw + 12.0, line(&m, cy, ih), if *on { "On" } else { "Off" }, font, false, 255, false);
                b.hots.push(Hot { r: Rt { x: X0, y: cy, w: tw + 60.0, h: ih }, kind: HotKind::Do(flip.clone()), row: Some(ri) });
            }
            Control::Oklab { swatch, comps } => {
                b.prims.push(Prim::Swatch { r: Rt { x: X0, y: cy, w: 32.0, h: ih }, col: *swatch });
                let mut cx = X0 + 42.0;
                for p in comps {
                    stepper(&mut b, t, cx, 110.0, p);
                    cx += 114.0;
                }
            }
            Control::Fixed { value } => {
                // "read-only", not "unavailable". The VALUE is right there and is perfectly
                // available; what cannot be had is the ability to change it from here, and the
                // note underneath already says why. Reading "1280 x 800 . unavailable" as a
                // report that the resolution is unknown is the obvious misreading and it is the
                // opposite of what the row is saying.
                let v = clip(t, &format!("{value}   \u{b7}   read-only"), &spec, cw, scale);
                text(&mut b, X0, line(&m, y, base), &v, font, false, 165, false);
            }
        }
        if let Some(n) = &r.note {
            let n = clip(t, n, &small, cw, scale);
            text(&mut b, X0, y + base - 5.0 + (extra - (sm.ascent + sm.descent)) / 2.0, &n, font * 0.85, false, 135, false);
        }
        y += h;
    }
    // The footer sits at the bottom of a panel at least `min_h` tall, so every page is the same size.
    let foot = 8.0 + 2.0 * (sm.ascent + sm.descent).ceil() + 3.0 + 14.0;
    y = (y + 8.0).max(min_h - foot);
    let sep = Rt { x: M, y, w: WIDTH - 2.0 * M, h: 1.0 };
    b.prims.push(Prim::Pill { r: sep, rad: 0.0, col: [255, 255, 255, 40] });
    y += 8.0;
    let st = clip(t, status, &small, WIDTH - 2.0 * M, scale);
    text(&mut b, M, y, &st, font * 0.85, false, 170, false);
    y += (sm.ascent + sm.descent).ceil() + 3.0;
    text(&mut b, M, y, "Esc closes   Tab next page   Up/Down row   Left/Right change", font * 0.85, false, 110, false);
    y += (sm.ascent + sm.descent).ceil() + 14.0;
    b.size = (WIDTH, y.max(min_h));
    b
}

// ---- drawing -----------------------------------------------------------------

/// Rasterise `b` at device pixels. `hover` is a hotspot index, `sel` a row.
/// Also returns each text drawn as `(string, device x, device y, device width)`.
pub fn draw(b: &Built, hover: Option<usize>, sel: Option<usize>, st: &Style, accent: [u8; 3], t: &mut TextSystem) -> (Canvas, Vec<(String, i32, i32, i32)>) {
    let s = st.scale;
    let d = |v: f32| (v as f64 * s).round() as i32;
    let (cw, ch) = ((b.size.0 as f64 * s).ceil() as usize, (b.size.1 as f64 * s).ceil() as usize);
    let mut c = Canvas::new(cw.max(1), ch.max(1), [0, 0, 0, 0]);
    if !st.glass_on {
        c.round_rect(0, 0, cw as i32, ch as i32, st.radius * s as f32, [18, 20, 30, 235]);
    }
    if let Some(r) = sel.and_then(|i| b.rows.get(i)) {
        c.round_rect(d(r.x), d(r.y), d(r.w), d(r.h), 8.0 * s as f32, [255, 255, 255, 18]);
    }
    for p in &b.prims {
        match p {
            Prim::Pill { r, rad, col } if col[3] > 0 => {
                // The one fully-white pill is a selected segment or an "on" switch: accent.
                let col = if *col == [255, 255, 255, 255] { [accent[0], accent[1], accent[2], 255] } else { *col };
                c.round_rect(d(r.x), d(r.y), d(r.w), d(r.h), rad * s as f32, col);
            }
            Prim::Swatch { r, col } => {
                c.round_rect(d(r.x) - 1, d(r.y) - 1, d(r.w) + 2, d(r.h) + 2, 7.0 * s as f32, [255, 255, 255, 90]);
                c.round_rect(d(r.x), d(r.y), d(r.w), d(r.h), 6.0 * s as f32, [col[0], col[1], col[2], 255]);
            }
            _ => {}
        }
    }
    if let Some(h) = hover.and_then(|i| b.hots.get(i)) {
        let hv = st.hover;
        c.round_rect(d(h.r.x), d(h.r.y), d(h.r.w), d(h.r.h), 7.0 * s as f32, [hv[0], hv[1], hv[2], (st.hover_strength * 255.0) as u8]);
    }
    let mut placed = vec![];
    for p in &b.prims {
        if let Prim::Text { x, y, s: txt, size, bold, a, white } = p {
            let spec = if *bold { FontSpec::new(*size).bold() } else { FontSpec::new(*size) };
            let col = if *white { [255, 255, 255] } else { st.text };
            t.draw(&mut c, *x, *y, txt, &spec, [col[0], col[1], col[2], *a], s);
            placed.push((txt.clone(), d(*x), d(*y), (t.string_width(txt, &spec, s) as f64 * s).round() as i32));
        }
    }
    (c, placed)
}

#[cfg(test)]
mod tests {
    use super::*;

    pub(super) fn ctx<'a>(t: &'a Theme, fams: &'a [String]) -> Ctx<'a> {
        // Pages are looked up by TITLE, never by position. These tests indexed pages[2] and
        // pages[3] and broke the moment a page was inserted before them, which says nothing
        // about the thing they are testing.
        Ctx { theme: t, host_scale: 1.45, scale: 1.45, out_phys: (1856, 1160), refresh_mhz: 59_950, outputs: 1, families: fams, nested: true, ifaces: &[] }
    }

    #[test]
    fn unavailable_settings_are_rows_with_reasons_and_no_controls() {
        let (t, f) = (Theme::default(), vec![]);
        let p = pages(&ctx(&t, &f));
        let d = &p[0];
        // Still not editable, and still explained. What changed is that two of them now REPORT
        // a figure the compositor holds instead of describing where it comes from.
        for name in ["Resolution", "Refresh rate", "Monitors"] {
            let r = d.rows.iter().find(|r| r.label == name).unwrap();
            assert!(matches!(r.ctl, Control::Fixed { .. }), "{name} should not be editable");
            assert!(!r.note.as_deref().unwrap_or("").is_empty(), "{name} needs a reason");
            assert!(r.left.is_none() && r.right.is_none());
        }
        // Multi-monitor is the one that genuinely needs a backend we do not have.
        let m = d.rows.iter().find(|r| r.label == "Monitors").unwrap();
        assert!(m.note.as_deref().unwrap().contains("DRM/KMS"));
    }

    #[test]
    fn the_refresh_rate_is_shown_when_it_is_known_and_said_to_be_unknown_when_it_is_not() {
        let (t, f) = (Theme::default(), vec![]);
        let mut c = super::tests::ctx(&t, &f);
        c.refresh_mhz = 59_950;
        let p = pages(&c);
        let r = p[0].rows.iter().find(|r| r.label == "Refresh rate").unwrap();
        let Control::Fixed { value } = &r.ctl else { panic!() };
        assert_eq!(value, "59.95 Hz", "mHz, shown as Hz to two places");

        // No mode means no number -- not 0 Hz, which would be a claim about the display.
        c.refresh_mhz = 0;
        let p = pages(&c);
        let r = p[0].rows.iter().find(|r| r.label == "Refresh rate").unwrap();
        let Control::Fixed { value } = &r.ctl else { panic!() };
        assert_eq!(value, "unknown");
    }

    #[test]
    fn the_pointer_page_writes_the_input_table_and_says_when_it_is_inert() {
        let (t, f) = (Theme::default(), vec![]);
        let p = pages(&ctx(&t, &f));
        let page = p.iter().find(|p| p.title == "Pointer").expect("a Pointer page");
        let labels: Vec<&str> = page.rows.iter().map(|r| r.label.as_str()).collect();
        assert_eq!(labels, ["Pointer speed", "Natural scrolling", "Tap to click", "Left handed"]);

        // Every row says it will not take effect while nested. A control that looks live and
        // does nothing is worse than one that explains itself, which is the line the Display
        // page already takes about resolution.
        for r in &page.rows {
            assert!(r.note.as_deref().unwrap_or("").contains("nested"), "{} has no reason", r.label);
        }

        // And they edit the input table, which is what the theme and libinput both read.
        let Control::Toggle { on, flip } = &page.rows[2].ctl else { panic!("tap is a toggle") };
        assert!(*on, "tap to click defaults on, as it was hard-coded before it was a setting");
        assert_eq!(flip, &vec![Edit::new("input", "tap_to_click", Val::Bool(false))]);
    }

    #[test]
    fn on_drm_the_pointer_rows_do_not_claim_to_be_inert() {
        let (t, f) = (Theme::default(), vec![]);
        let mut c = super::tests::ctx(&t, &f);
        c.nested = false;
        let p = pages(&c);
        let page = p.iter().find(|p| p.title == "Pointer").unwrap();
        for r in &page.rows {
            assert!(!r.note.as_deref().unwrap_or("").contains("nested"), "{} still disclaims", r.label);
        }
    }

    #[test]
    fn the_scale_row_marks_the_current_value_and_writes_the_output_table() {
        let mut t = Theme::default();
        t.output.scale = 1.5;
        let f = vec![];
        let p = pages(&ctx(&t, &f));
        let page = p.iter().find(|p| p.title == "Display").expect("Display page");
        let Control::Segments(s) = &page.rows[0].ctl else { panic!("Display row 0 is not segments") };
        assert_eq!(s.iter().filter(|g| g.on).map(|g| g.label.as_str()).collect::<Vec<_>>(), ["1.5"]);
        assert_eq!(s[0].edits, vec![Edit::n("output", "scale", 0.0)], "Auto is 0: follow the host");
    }

    #[test]
    fn steppers_stop_at_their_limits() {
        let mut t = Theme::default();
        t.dock.item_height = 64.0;
        let f = vec![];
        let all = pages(&ctx(&t, &f));
        let page = all.iter().find(|p| p.title == "Dock & Text").expect("Dock & Text page");
        let Control::Stepper(p) = &page.rows[2].ctl else { panic!("Dock & Text row 2 is not a stepper") };
        assert!(p.inc.is_none() && p.dec.is_some());
        assert_eq!(p.label, "64 px");
    }

    #[test]
    fn a_hand_set_value_outside_the_list_is_still_shown_and_selectable() {
        let mut t = Theme::default();
        t.input.layout = "latam".into();
        let f = vec![];
        let all = pages(&ctx(&t, &f));
        let page = all.iter().find(|p| p.title == "Keyboard").expect("Keyboard page");
        let Control::Cycle(p) = &page.rows[0].ctl else { panic!("Keyboard row 0 is not a cycle") };
        assert_eq!(p.label, "latam");
    }

    #[test]
    fn every_hotspot_lies_inside_the_panel_and_the_layout_is_deterministic() {
        let (t, f) = (Theme::default(), vec!["Noto Sans".to_string()]);
        let p = pages(&ctx(&t, &f));
        let mut ts = TextSystem::new("", "");
        for pg in 0..p.len() {
            let a = build(&p, pg, "ready", 14.0, 1.45, 0.0, &mut ts);
            let b = build(&p, pg, "ready", 14.0, 1.45, 0.0, &mut ts);
            assert_eq!(a.size, b.size);
            assert!(!a.hots.is_empty());
            for h in &a.hots {
                assert!(h.r.x >= 0.0 && h.r.y >= 0.0 && h.r.x + h.r.w <= a.size.0 + 0.5 && h.r.y + h.r.h <= a.size.1, "page {pg}: {:?} outside {:?}", h.r, a.size);
            }
        }
    }
}

#[cfg(test)]
mod network_page_tests {
    use super::*;
    use crate::deskbar::sysinfo::Iface;

    fn page_with(ifaces: &[Iface]) -> Page {
        let t = Theme::default();
        let f: Vec<String> = vec![];
        let mut c = super::tests::ctx(&t, &f);
        c.ifaces = ifaces;
        pages(&c).into_iter().find(|p| p.title == "Network").expect("a Network page")
    }

    #[test]
    fn each_interface_is_a_read_only_row_saying_what_it_is() {
        let p = page_with(&[
            Iface { name: "eno1".into(), wireless: false, up: true, mbit: Some(1000) },
            Iface { name: "wlp11s0".into(), wireless: true, up: true, mbit: None },
        ]);
        assert_eq!(p.rows.len(), 2);
        let eno = &p.rows[0];
        assert_eq!(eno.label, "eno1");
        let Control::Fixed { value } = &eno.ctl else { panic!("an interface is not editable from here") };
        assert_eq!(value, "wired, up, 1000 Mbit/s");
        assert!(eno.left.is_none() && eno.right.is_none(), "a read-only row has no controls");

        let Control::Fixed { value } = &p.rows[1].ctl else { panic!() };
        assert_eq!(value, "wireless, up", "wireless has no link speed to report");
    }

    #[test]
    fn a_link_that_is_down_reports_no_speed_even_if_one_was_read() {
        // A wired interface keeps its last speed in sysfs after the cable goes. Printing it
        // beside "down" would be two statements that contradict each other.
        let p = page_with(&[Iface { name: "eno1".into(), wireless: false, up: false, mbit: Some(1000) }]);
        let Control::Fixed { value } = &p.rows[0].ctl else { panic!() };
        assert_eq!(value, "wired, down");
    }

    #[test]
    fn a_machine_with_no_hardware_says_so_rather_than_showing_an_empty_page() {
        let p = page_with(&[]);
        assert_eq!(p.rows.len(), 1);
        let note = p.rows[0].note.as_deref().unwrap_or("");
        assert!(note.contains("docker"), "the reason should name what is NOT listed: {note}");
    }
}
