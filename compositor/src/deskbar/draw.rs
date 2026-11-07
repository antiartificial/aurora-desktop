//! Laying out and rasterising the Deskbar. Everything is the compositor's own code: one CPU canvas, shaped
//! by the one text stack, uploaded once per change by `Shell::prepare_deskbar`, with chrome glass drawn
//! behind it by the effects pipeline (the same dual-Kawase chain and `[glass.chrome]` values as the dock,
//! not a second blur). Layout is in LOGICAL units and rounded once to device pixels, so it holds at any scale.
//!
//! The canvas is transparent and carries only ink: text, tiles, the hover wash. The glass is not in it.
//!
//! A pure function of its inputs: the key is built from the same data the pixels are, so a frame whose
//! key did not change is not re-rasterised or re-uploaded.

use std::{cmp::Reverse, collections::HashMap};

use super::model::{self, Dom, Figures, Group, Net, SysFigures, Tone, Verdict, safe};
use crate::{
    fx::theme::{self, oklab_to_u8},
    shell::layout::Rect,
    text::{FontSpec, TextSystem},
    trusted::draw::{Canvas, Elide, fit},
};

/// How much width the header's system figures may use, once the label has its own space and a
/// gap between them. Never negative; zero means there is no room and they are dropped rather
/// than drawn over the label.
fn meta_room(box_w: f32, pad: f32, label_w: f32) -> f32 {
    /// A readable gap, so the two halves do not merely touch.
    const GAP: f32 = 12.0;
    (box_w - pad - (pad + label_w) - GAP).max(0.0)
}

const PAD: f32 = 6.0;
const HEADER_H: f32 = 32.0;
const APP_H: f32 = 46.0;
const APP_NET_H: f32 = 15.0;
const WIN_H: f32 = 26.0;
const SUB_H: f32 = 20.0;
const MORE_H: f32 = 22.0;
const INDENT: f32 = 46.0;
const TILE: f32 = 26.0;
/// Hosts listed under an open application before "+N more". The row of refusals sorts first.
const MAX_NET_ROWS: usize = 4;

/// What a row is, for hit-testing and for hover. `Win` and `App` are the clickable ones.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Kind {
    Header,
    App(String),
    Win(String),
    Info,
}

impl Kind {
    pub fn clickable(&self) -> bool {
        matches!(self, Kind::App(_) | Kind::Win(_))
    }
    fn tag(&self) -> &'static str {
        match self {
            Kind::Header => "header",
            Kind::App(_) => "app",
            Kind::Win(_) => "win",
            Kind::Info => "info",
        }
    }
}

/// One drawn row: its hit rectangle (device pixels, relative to the box) and the words on it, which
/// are logged so tests judge what was drawn and not a copy of what was meant.
#[derive(Clone, Debug)]
pub struct Placed {
    pub kind: Kind,
    pub rect: Rect,
    pub text: String,
    pub meta: String,
}

pub struct Raster {
    pub key: String,
    pub canvas: Canvas,
    pub rows: Vec<Placed>,
    /// No row is mid-animation: the layout is final and worth logging.
    pub settled: bool,
}

pub struct View<'a> {
    pub groups: &'a [Group],
    /// 0..1 per group: how open it is.
    pub expand: &'a [f32],
    /// By domain name.
    pub figures: &'a HashMap<String, Figures>,
    pub net: &'a Net,
    /// A `--bus-socket` was given, so silence from the bus means something.
    pub net_watching: bool,
    pub sys: SysFigures,
    pub hover: Option<Kind>,
}

pub struct Style<'a> {
    pub cfg: &'a theme::Deskbar,
    pub scale: f64,
    pub font: f32,
    pub glass_on: bool,
    /// Corner radius, logical.
    pub radius: f32,
    /// The tallest the box may be, logical. More applications than fit end in "+N more".
    pub max_h: f32,
}

enum Body {
    Header,
    App(usize),
    Win { title: String, focused: bool, hidden: bool },
    Dom(String),
    Net { host: String, verdict: Verdict, count: u64 },
    Note { text: String, tone: Tone },
    More(String),
}

struct Row {
    kind: Kind,
    /// Full height, logical. Sub-rows are drawn at `h * e`.
    h: f32,
    e: f32,
    body: Body,
}

fn q(e: f32) -> f32 {
    (e.clamp(0.0, 1.0) * 64.0).round() / 64.0
}

fn rows(v: &View, st: &Style) -> Vec<Row> {
    let mut out = vec![Row { kind: Kind::Header, h: HEADER_H, e: 1.0, body: Body::Header }];
    for (i, g) in v.groups.iter().enumerate() {
        let dom = g.dom.name();
        let lines = model::meta(g, dom.and_then(|n| v.figures.get(n)), dom.and_then(|n| v.net.domains.get(n)));
        let h = APP_H + (lines.len().saturating_sub(1)) as f32 * APP_NET_H;
        out.push(Row { kind: Kind::App(g.key.clone()), h, e: 1.0, body: Body::App(i) });
        let e = q(v.expand.get(i).copied().unwrap_or(0.0));
        if e <= 0.0 {
            continue;
        }
        for w in &g.wins {
            out.push(Row { kind: Kind::Win(w.id.clone()), h: WIN_H, e, body: Body::Win { title: safe(&w.title), focused: w.focused, hidden: w.hidden } });
        }
        let Some(name) = dom else { continue };
        out.push(Row { kind: Kind::Info, h: SUB_H, e, body: Body::Dom(safe(name)) });
        let net = v.net.domains.get(name).filter(|n| !n.hosts.is_empty());
        match net {
            Some(n) => {
                // Refusals first: they are what a person opened the row to see.
                let mut hs: Vec<_> = n.hosts.iter().collect();
                hs.sort_by_key(|h| (h.verdict != Verdict::Refused, Reverse(h.count), h.host.clone()));
                for h in hs.iter().take(MAX_NET_ROWS) {
                    out.push(Row { kind: Kind::Info, h: SUB_H, e, body: Body::Net { host: safe(&h.host), verdict: h.verdict, count: h.count } });
                }
                if hs.len() > MAX_NET_ROWS {
                    out.push(Row { kind: Kind::Info, h: SUB_H, e, body: Body::Note { text: format!("+{} more hosts", hs.len() - MAX_NET_ROWS), tone: Tone::Dim } });
                }
            }
            None => {
                let (text, tone) = match (v.net_watching, v.net.up) {
                    (false, _) => ("network: no bus configured", Tone::Dim),
                    (true, false) => ("network: bus unreachable", Tone::Warn),
                    (true, true) => ("no connections reported", Tone::Dim),
                };
                out.push(Row { kind: Kind::Info, h: SUB_H, e, body: Body::Note { text: text.into(), tone } });
            }
        }
    }
    // Too tall for the output: cut at a row, and say how many applications went.
    let avail = (st.max_h - 2.0 * PAD).max(HEADER_H);
    let total: f32 = out.iter().map(|r| r.h * r.e).sum();
    if total > avail {
        let (mut acc, mut keep) = (0.0, 0);
        for (i, r) in out.iter().enumerate() {
            if acc + r.h * r.e > avail - MORE_H {
                break;
            }
            acc += r.h * r.e;
            keep = i + 1;
        }
        let hidden = out[keep..].iter().filter(|r| matches!(r.kind, Kind::App(_))).count();
        out.truncate(keep.max(1));
        let text = if hidden > 0 { format!("+{hidden} more applications") } else { "+ more".to_string() };
        out.push(Row { kind: Kind::Info, h: MORE_H, e: 1.0, body: Body::More(text) });
    }
    out
}

/// A string of everything that decides the pixels.
pub fn key(v: &View, st: &Style) -> String {
    let e: Vec<f32> = v.expand.iter().map(|e| q(*e)).collect();
    // Figures and net only matter for the domains on screen; the Debug of the whole map is bounded (<= 64 each).
    let mut figs: Vec<_> = v.figures.iter().collect();
    figs.sort_by(|a, b| a.0.cmp(b.0));
    format!(
        "{:?}|{e:?}|{figs:?}|{:?}|{}|{:?}|{:?}|{:?}|{}|{}|{}|{}|{}",
        v.groups, v.net, v.net_watching, v.sys, v.hover, st.cfg, st.scale, st.font, st.glass_on, st.max_h, st.radius
    )
}

fn col(o: [f32; 3], a: u8) -> [u8; 4] {
    let c = oklab_to_u8(o);
    [c[0], c[1], c[2], a]
}

fn scaled(c: [u8; 4], k: f32) -> [u8; 4] {
    [c[0], c[1], c[2], (c[3] as f32 * k).round() as u8]
}

/// A filled triangle, anti-aliased by 4x4 supersampling. Device pixels.
fn tri(c: &mut Canvas, p: [(f32, f32); 3], colour: [u8; 4]) {
    let (x0, x1) = (p.iter().map(|q| q.0).fold(f32::MAX, f32::min).floor() as i32, p.iter().map(|q| q.0).fold(f32::MIN, f32::max).ceil() as i32);
    let (y0, y1) = (p.iter().map(|q| q.1).fold(f32::MAX, f32::min).floor() as i32, p.iter().map(|q| q.1).fold(f32::MIN, f32::max).ceil() as i32);
    let side = |a: (f32, f32), b: (f32, f32), x: f32, y: f32| (b.0 - a.0) * (y - a.1) - (b.1 - a.1) * (x - a.0);
    for y in y0..y1 {
        for x in x0..x1 {
            let mut n = 0;
            for sy in 0..4 {
                for sx in 0..4 {
                    let (px, py) = (x as f32 + (sx as f32 + 0.5) / 4.0, y as f32 + (sy as f32 + 0.5) / 4.0);
                    let s = [side(p[0], p[1], px, py), side(p[1], p[2], px, py), side(p[2], p[0], px, py)];
                    if s.iter().all(|v| *v >= 0.0) || s.iter().all(|v| *v <= 0.0) {
                        n += 1;
                    }
                }
            }
            if n > 0 {
                c.over(x, y, scaled(colour, n as f32 / 16.0));
            }
        }
    }
}

pub fn build(v: &View, st: &Style, t: &mut TextSystem) -> Raster {
    let key = key(v, st);
    let list = rows(v, st);
    let d = |x: f32| (x as f64 * st.scale).round() as i32;
    let w = st.cfg.width;
    let total: f32 = PAD * 2.0 + list.iter().map(|r| r.h * r.e).sum::<f32>();
    let (cw, ch) = (d(w), d(total).max(1));
    let mut c = Canvas::new(cw as usize, ch as usize, [0, 0, 0, 0]);
    if !st.glass_on {
        // As the dock does without glass: a flat panel, so the text still has a floor under it.
        c.round_rect(0, 0, cw, ch, st.radius * st.scale as f32, [18, 20, 30, 215]);
    }
    let (text, dim, warn, accent) = (col(st.cfg.text_oklab, 255), col(st.cfg.text_dim_oklab, 255), col(st.cfg.warn_oklab, 255), col(st.cfg.accent_oklab, 255));
    let tone = |t: Tone| match t {
        Tone::Dim => dim,
        Tone::Warn => warn,
    };
    let (body, bold, small, tiny) = (FontSpec::new(st.font), FontSpec::new(st.font).bold(), FontSpec::new(st.font * 0.82), FontSpec::new(st.font * 0.78));
    let (mb, ms, mt) = (t.metrics(&body), t.metrics(&small), t.metrics(&tiny));
    let mid = |top: f32, h: f32, m: &crate::text::FontMetrics| top + (h - (m.ascent + m.descent)) / 2.0;
    let pad_x = 12.0;
    let mut placed: Vec<Placed> = vec![];
    let mut y = PAD;
    let apps = v.groups.len();
    let wins: usize = v.groups.iter().map(|g| g.wins.len()).sum();
    for r in &list {
        let h = r.h * r.e;
        let (top, bot) = (d(y), d(y + h));
        let rect = Rect { x: 0, y: top, w: cw, h: (bot - top).max(0) };
        let a = r.e;
        let hovered = v.hover.as_ref() == Some(&r.kind) && r.kind.clickable();
        if hovered {
            c.round_rect(d(4.0), top, cw - 2 * d(4.0), rect.h, d(7.0) as f32, col(st.cfg.hover_oklab, (st.cfg.hover_strength * 255.0) as u8));
        }
        let mut label: String;
        let mut meta_text = String::new();
        match &r.body {
            Body::Header => {
                // With nothing running, say so in words. "0 apps - 0 windows" is accurate and reads
                // like a failed lookup; the box is answering a question, so it should answer it.
                label = if apps == 0 {
                    "No windows open".to_string()
                } else {
                    format!("{} \u{b7} {}", model::plural(apps, "app", "apps"), model::plural(wins, "window", "windows"))
                };
                t.draw(&mut c, pad_x, mid(y, h, &mb), &label, &bold, text, st.scale);
                let sys = if st.cfg.system { model::system_text(&v.sys).join(" \u{b7} ") } else { String::new() };
                if !sys.is_empty() {
                    // The two halves of this row used to be drawn without either knowing the
                    // other was there: the label left-aligned at pad_x with no limit, the figures
                    // right-aligned at their own width. As soon as both together were wider than
                    // the box they overlapped, which they did at three windows, so the row read
                    // "1 app . 3 windowswired . cpu 35%".
                    //
                    // The label answers the question the box is for, so it keeps its space and
                    // the figures take what is left. Cut from the TAIL, because the first figure
                    // is the network and losing "mem 42%" costs less than losing "wired".
                    let room = meta_room(w, pad_x, t.string_width(&label, &bold, st.scale));
                    let shown = if room > 0.0 { fit(t, &sys, &small, st.scale, room, Elide::Head) } else { String::new() };
                    if !shown.is_empty() {
                        let sw = t.string_width(&shown, &small, st.scale);
                        t.draw(&mut c, w - pad_x - sw, mid(y, h, &ms), &shown, &small, dim, st.scale);
                        meta_text = shown;
                    }
                }
                // a hairline under the header, so the first app does not read as part of it
                c.over_rect(d(pad_x), bot - 1.max(d(0.5)), cw - 2 * d(pad_x), 1.max(d(0.5)), [dim[0], dim[1], dim[2], 50]);
            }
            Body::App(i) => {
                let g = &v.groups[*i];
                let dom = g.dom.name();
                let lines = model::meta(g, dom.and_then(|n| v.figures.get(n)), dom.and_then(|n| v.net.domains.get(n)));
                let tc = crate::shell::tile_colour(&g.label);
                let tile = d(TILE);
                c.round_rect(d(pad_x), top + d(10.0), tile, tile, tile as f32 * 0.28, [tc[0], tc[1], tc[2], 255]);
                // The last dotted part of an id: "org.demo.alpha" is an A, not an O like every other org.* app.
                let initial: String = g.label.rsplit('.').find(|s| !s.is_empty()).and_then(|s| s.chars().next()).map(|ch| ch.to_uppercase().collect()).unwrap_or_default();
                let tspec = FontSpec::new(st.font * 1.1).bold();
                let (iw, tm) = (t.string_width(&initial, &tspec, st.scale), t.metrics(&tspec));
                t.draw(&mut c, pad_x + (TILE - iw) / 2.0, y + 10.0 + (TILE - (tm.ascent + tm.descent)) / 2.0, &initial, &tspec, [255, 255, 255, 255], st.scale);
                let x = pad_x + TILE + 10.0;
                let maxw = w - x - 28.0;
                label = fit(t, &g.label, &bold, st.scale, maxw, Elide::Head);
                let focused = g.wins.iter().any(|w| w.focused);
                t.draw(&mut c, x, y + 6.0, &label, &if focused { bold.clone() } else { body.clone() }, text, st.scale);
                let mut ly = y + 6.0 + mb.ascent + mb.descent + 3.0;
                let mut all = vec![];
                for line in &lines {
                    let mut lx = x;
                    let mut joined = String::new();
                    for (n, (s, tn)) in line.iter().enumerate() {
                        let sep = if n > 0 { " \u{b7} " } else { "" };
                        let piece = format!("{sep}{s}");
                        let left = maxw - (lx - x);
                        let shown = fit(t, &piece, &small, st.scale, left, Elide::Head);
                        lx = t.draw(&mut c, lx, ly, &shown, &small, tone(*tn), st.scale);
                        joined.push_str(&shown);
                        if shown != piece {
                            break;
                        }
                    }
                    all.push(joined);
                    ly += APP_NET_H;
                }
                meta_text = all.join(" / ");
                // The chevron: right when closed, down when open. Open past the half-way point of its own ease.
                let (cx, cy) = (d(w - 16.0) as f32, (top + d(APP_H / 2.0)) as f32);
                let s = d(5.0) as f32;
                if q(v.expand.get(*i).copied().unwrap_or(0.0)) > 0.5 {
                    tri(&mut c, [(cx - s, cy - s * 0.5), (cx + s, cy - s * 0.5), (cx, cy + s * 0.6)], dim);
                } else {
                    tri(&mut c, [(cx - s * 0.5, cy - s), (cx - s * 0.5, cy + s), (cx + s * 0.6, cy)], dim);
                }
            }
            Body::Win { title, focused, hidden } => {
                // Sub-rows fade and settle as they open: `a` is the row's own ease, so the text is already
                // legible at a third of the way (ease-out), and `y` centres it in the part that has opened.
                let x = INDENT;
                if *focused {
                    c.round_rect(d(INDENT - 10.0), top + d(5.0 * a), d(3.0), (rect.h - d(10.0 * a)).max(1), d(1.5) as f32, accent);
                }
                // A window Show Desktop put away says so on the row, in the dim colour: it is running but not
                // on the desktop, and the row must not read the same as one that is.
                let tag_w = if *hidden { t.string_width("hidden", &tiny, st.scale) + 8.0 } else { 0.0 };
                if *hidden {
                    t.draw(&mut c, w - 12.0 - (tag_w - 8.0), mid(y, h, &mt), "hidden", &tiny, scaled(dim, a), st.scale);
                    meta_text = "hidden".into();
                }
                label = fit(t, title, &if *focused { bold.clone() } else { body.clone() }, st.scale, w - x - 12.0 - tag_w, Elide::Head);
                if label.is_empty() {
                    label = "(untitled)".into();
                }
                t.draw(&mut c, x, mid(y, h, &mb), &label, &if *focused { bold.clone() } else { body.clone() }, scaled(if *hidden { dim } else { text }, a), st.scale);
            }
            Body::Dom(name) => {
                label = fit(t, &format!("in domain {name}"), &tiny, st.scale, w - INDENT - 12.0, Elide::Head);
                t.draw(&mut c, INDENT, mid(y, h, &mt), &label, &tiny, scaled(dim, a), st.scale);
            }
            Body::Net { host, verdict, count } => {
                let right = if *count > 0 { format!("{} {}", verdict.word(), count) } else { verdict.word().to_string() };
                let colour = if *verdict == Verdict::Refused { warn } else { dim };
                let rw = t.string_width(&right, &tiny, st.scale);
                t.draw(&mut c, w - 12.0 - rw, mid(y, h, &mt), &right, &tiny, scaled(colour, a), st.scale);
                label = fit(t, host, &tiny, st.scale, w - INDENT - 12.0 - rw - 8.0, Elide::Head);
                t.draw(&mut c, INDENT, mid(y, h, &mt), &label, &tiny, scaled(if *verdict == Verdict::Refused { warn } else { text }, a), st.scale);
                meta_text = right;
            }
            Body::Note { text: n, tone: tn } => {
                label = fit(t, n, &tiny, st.scale, w - INDENT - 12.0, Elide::Head);
                t.draw(&mut c, INDENT, mid(y, h, &mt), &label, &tiny, scaled(tone(*tn), a), st.scale);
            }
            Body::More(n) => {
                label = n.clone();
                t.draw(&mut c, pad_x, mid(y, h, &ms), n, &small, dim, st.scale);
            }
        }
        placed.push(Placed { kind: r.kind.clone(), rect, text: label, meta: meta_text });
        y += h;
    }
    // Judged on the quantised value, the one the key and the pixels use: a raw 0.004 that rounds to closed is settled,
    // and nothing would rebuild (and so log) the box again if it were not counted so.
    let settled = v.expand.iter().all(|e| { let e = q(*e); e <= 0.0 || e >= 1.0 });
    Raster { key, canvas: c, rows: placed, settled }
}

/// The log line's row list: `kind|x,y,w,h|text|meta`, `;`-separated, in device pixels relative to the box.
/// Tests read click targets and the drawn words off it.
pub fn describe(rows: &[Placed]) -> String {
    let clean = |s: &str| s.replace(['|', ';', '\n'], "_");
    rows.iter().map(|r| format!("{}|{},{},{},{}|{}|{}", r.kind.tag(), r.rect.x, r.rect.y, r.rect.w, r.rect.h, clean(&r.text), clean(&r.meta))).collect::<Vec<_>>().join(";")
}

pub fn domain_label(d: &Dom) -> String {
    match d {
        Dom::None => "none".into(),
        Dom::In { name, .. } => format!("domain:{name}"),
        Dom::Unknown(w) => format!("unknown:{w}"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use model::{Win, Dom};

    pub(super) fn grp(key: &str, label: &str, dom: Dom, wins: &[(&str, &str, bool)]) -> Group {
        Group { key: key.into(), label: label.into(), dom, wins: wins.iter().map(|(i, t, f)| Win { id: (*i).into(), title: (*t).into(), focused: *f, hidden: false }).collect() }
    }

    pub(super) fn st(cfg: &theme::Deskbar, max_h: f32) -> Style<'_> {
        Style { cfg, scale: 1.0, font: 14.0, glass_on: true, radius: 12.0, max_h }
    }

    #[test]
    fn an_open_app_lists_its_windows_and_a_closed_one_does_not() {
        let cfg = theme::Deskbar::default();
        let mut t = TextSystem::new("", "");
        let g = vec![grp("a", "Notes", Dom::None, &[("1", "one", false), ("2", "two", true)]), grp("b", "Term", Dom::None, &[("3", "three", false)])];
        let (figs, net) = (HashMap::new(), Net::default());
        let mut mk = |expand: &[f32]| {
            let v = View { groups: &g, expand, figures: &figs, net: &net, net_watching: false, sys: SysFigures::default(), hover: None };
            build(&v, &st(&cfg, 900.0), &mut t)
        };
        let closed = mk(&[0.0, 0.0]);
        assert_eq!(closed.rows.iter().map(|r| r.kind.tag()).collect::<Vec<_>>(), ["header", "app", "app"]);
        let open = mk(&[1.0, 0.0]);
        assert_eq!(open.rows.iter().map(|r| r.kind.tag()).collect::<Vec<_>>(), ["header", "app", "win", "win", "app"]);
        assert_eq!(open.rows[2].text, "one");
        assert!(open.canvas.h > closed.canvas.h);
        assert!(open.settled && closed.settled);
        assert!(!mk(&[0.5, 0.0]).settled, "mid-ease is not final");
        assert_ne!(open.key, closed.key);
    }

    #[test]
    fn a_row_not_in_a_domain_draws_no_digit_in_its_meta_and_a_domain_one_draws_only_what_exists() {
        let cfg = theme::Deskbar::default();
        let mut t = TextSystem::new("", "");
        let dom = Dom::In { name: "d1".into() };
        let g = vec![grp("a", "Plain", Dom::None, &[("1", "w", false)]), grp("b", "Boxed", dom, &[("2", "w", false)])];
        let mut figs = HashMap::new();
        figs.insert("d1".to_string(), Figures { cpu: Some(2.5), mem: None, procs: Some(3) });
        // a figure keyed under a name that is NOT a domain a row is in must go nowhere
        figs.insert("Plain".to_string(), Figures { cpu: Some(99.0), mem: Some(1 << 30), procs: Some(9) });
        let net = Net::default();
        let v = View { groups: &g, expand: &[0.0, 0.0], figures: &figs, net: &net, net_watching: false, sys: SysFigures::default(), hover: None };
        let r = build(&v, &st(&cfg, 900.0), &mut t);
        let apps: Vec<&Placed> = r.rows.iter().filter(|r| r.kind.tag() == "app").collect();
        assert_eq!(apps[0].meta, "not in a domain");
        assert_eq!(apps[1].meta, "cpu 2.5% \u{b7} 3 procs");
        assert!(!apps[1].meta.contains("mem"), "memory was not readable, so it is not there");
    }

    #[test]
    fn the_header_shows_machine_figures_only_when_read_and_only_when_asked_for() {
        let mut cfg = theme::Deskbar::default();
        // Wide enough that the figures are not cut, because what this test is about is WHICH
        // figures appear and in what order, not how they fit. The bitmap font the test harness
        // falls back to is far wider than the real one, so at the default 288 even a short set
        // is elided. The row's fit is asserted in header_room_tests instead.
        cfg.width = 900.0;
        let mut t = TextSystem::new("", "");
        let g = vec![grp("a", "A", Dom::None, &[("1", "w", false)])];
        let (figs, net) = (HashMap::new(), Net::default());
        let mut head = |cfg: &theme::Deskbar, sys: SysFigures| {
            let v = View { groups: &g, expand: &[0.0], figures: &figs, net: &net, net_watching: false, sys, hover: None };
            build(&v, &st(cfg, 900.0), &mut t).rows[0].meta.clone()
        };
        assert_eq!(head(&cfg, SysFigures::default()), "", "nothing read yet: no 0%");
        assert_eq!(head(&cfg, SysFigures { cpu: Some(12.0), mem: None, ..Default::default() }), "cpu 12%");
        assert_eq!(head(&cfg, SysFigures { cpu: Some(12.0), mem: Some(61.0), ..Default::default() }), "cpu 12% \u{b7} mem 61%");

        // Network and battery lead, because they are what a person is actually checking. An
        // offline laptop at 4% should not have to be read past a CPU figure to find that out.
        let low = SysFigures {
            cpu: Some(12.0),
            mem: None,
            link: Some(crate::deskbar::sysinfo::Link::Down),
            battery: Some(crate::deskbar::sysinfo::Battery { percent: 4, charging: false }),
            audio: None,
        };
        assert_eq!(head(&cfg, low), "offline \u{b7} batt 4% \u{b7} cpu 12%");

        // Muted says so rather than showing the number it will be when unmuted: the one time
        // anyone reads this is when they are wondering why there is no sound.
        let silent = SysFigures {
            cpu: None,
            mem: None,
            link: None,
            battery: None,
            audio: Some(crate::shell::apps::Audio { volume: 45, muted: true }),
        };
        assert_eq!(head(&cfg, silent), "muted");

        let loud = SysFigures {
            cpu: None,
            mem: None,
            link: None,
            battery: None,
            audio: Some(crate::shell::apps::Audio { volume: 45, muted: false }),
        };
        assert_eq!(head(&cfg, loud), "vol 45%");

        let charging = SysFigures {
            cpu: None,
            mem: None,
            link: Some(crate::deskbar::sysinfo::Link::Wireless),
            battery: Some(crate::deskbar::sysinfo::Battery { percent: 88, charging: true }),
            audio: None,
        };
        assert_eq!(head(&cfg, charging), "wi-fi \u{b7} batt +88%");

        // A desktop with no battery says nothing about one, rather than 0%.
        let desktop = SysFigures {
            cpu: None,
            mem: None,
            link: Some(crate::deskbar::sysinfo::Link::Wired),
            battery: None,
            audio: None,
        };
        assert_eq!(head(&cfg, desktop), "wired");
        cfg.system = false;
        assert_eq!(head(&cfg, SysFigures { cpu: Some(12.0), mem: Some(61.0), ..Default::default() }), "");
    }

    #[test]
    fn too_many_apps_end_in_a_count_instead_of_running_off_the_output() {
        let cfg = theme::Deskbar::default();
        let mut t = TextSystem::new("", "");
        let g: Vec<Group> = (0..30).map(|i| grp(&format!("k{i}"), &format!("App {i}"), Dom::None, &[("w", "t", false)])).collect();
        let ex = vec![0.0; 30];
        let (figs, net) = (HashMap::new(), Net::default());
        let v = View { groups: &g, expand: &ex, figures: &figs, net: &net, net_watching: false, sys: SysFigures::default(), hover: None };
        let r = build(&v, &st(&cfg, 400.0), &mut t);
        assert!(r.canvas.h <= 400, "{} px tall", r.canvas.h);
        let last = r.rows.last().unwrap();
        assert!(last.text.starts_with('+') && last.text.ends_with("more applications"), "{}", last.text);
        let shown = r.rows.iter().filter(|r| r.kind.tag() == "app").count();
        assert!(last.text.contains(&(30 - shown).to_string()));
    }

    #[test]
    fn a_domain_with_no_hosts_says_why_by_what_is_known_about_the_bus() {
        let cfg = theme::Deskbar::default();
        let mut t = TextSystem::new("", "");
        let dom = Dom::In { name: "d1".into() };
        let g = vec![grp("b", "Boxed", dom, &[("2", "w", false)])];
        let figs = HashMap::new();
        let note = |watching: bool, up: bool| {
            let net = Net { up, ..Default::default() };
            let v = View { groups: &g, expand: &[1.0], figures: &figs, net: &net, net_watching: watching, sys: SysFigures::default(), hover: None };
            build(&v, &st(&cfg, 900.0), &mut TextSystem::new("", "")).rows.last().unwrap().text.clone()
        };
        let _ = &mut t;
        assert_eq!(note(false, false), "network: no bus configured");
        assert_eq!(note(true, false), "network: bus unreachable");
        assert_eq!(note(true, true), "no connections reported");
    }

    #[test]
    fn refused_hosts_sort_first_and_carry_the_word() {
        let cfg = theme::Deskbar::default();
        let mut t = TextSystem::new("", "");
        let dom = Dom::In { name: "d1".into() };
        let g = vec![grp("b", "Boxed", dom, &[("2", "w", false)])];
        let mut net = Net { up: true, ..Default::default() };
        net.domains.insert("d1".into(), model::DomainNet { hosts: vec![
            model::NetHost { host: "busy.example".into(), verdict: Verdict::Allowed, count: 500 },
            model::NetHost { host: "evil.example".into(), verdict: Verdict::Refused, count: 0 },
        ] });
        let figs = HashMap::new();
        let v = View { groups: &g, expand: &[1.0], figures: &figs, net: &net, net_watching: true, sys: SysFigures::default(), hover: None };
        let r = build(&v, &st(&cfg, 900.0), &mut t);
        let nets: Vec<(&str, &str)> = r.rows.iter().filter(|r| r.kind.tag() == "info" && !r.meta.is_empty()).map(|r| (r.text.as_str(), r.meta.as_str())).collect();
        assert_eq!(nets, [("evil.example", "refused"), ("busy.example", "allowed 500")]);
        let app = r.rows.iter().find(|r| r.kind.tag() == "app").unwrap();
        assert_eq!(app.meta, "in a domain / net 2 hosts \u{b7} 1 refused");
    }
}

#[cfg(test)]
mod header_room_tests {
    use super::*;

    // The header draws a bold label on the left and the machine figures on the right. They used
    // to be positioned independently, so once both were wider than the box they overlapped and
    // the row read "1 app . 3 windowswired . cpu 35% . mem 42%". Whatever the figures get, there
    // must be a gap after the label.
    #[test]
    fn the_figures_never_reach_the_label() {
        let (box_w, pad) = (288.0, PAD);
        for label_w in [0.0, 40.0, 120.0, 200.0, 260.0, 400.0] {
            let room = meta_room(box_w, pad, label_w);
            assert!(room >= 0.0, "negative room for a label of {label_w}");
            // Right-aligned figures of exactly `room` start here.
            let figures_start = box_w - pad - room;
            let label_end = pad + label_w;
            if room > 0.0 {
                assert!(figures_start > label_end, "figures start at {figures_start}, label ends at {label_end}");
            }
        }
    }

    #[test]
    fn a_label_that_fills_the_box_leaves_no_room_rather_than_negative_room() {
        // Dropping the figures is the right answer here. Drawing them at a negative width is
        // how the overlap happened in the first place.
        assert_eq!(meta_room(288.0, PAD, 400.0), 0.0);
        assert_eq!(meta_room(288.0, PAD, 288.0), 0.0);
    }

    #[test]
    fn a_short_label_leaves_most_of_the_box() {
        let room = meta_room(288.0, PAD, 60.0);
        assert!(room > 150.0 && room < 288.0, "room {room}");
    }
}

#[cfg(test)]
mod header_fit_tests {
    use super::*;
    use super::tests::*;

    // At the real width the figures are CUT rather than drawn over the label. The bug this
    // guards was visible rather than logged: the row read "1 app . 3 windowswired . cpu 35%",
    // because the label and the figures were positioned without either knowing the other
    // existed. Asserting the meta text alone cannot catch that, so this asserts the widths.
    #[test]
    fn at_the_real_width_the_figures_are_cut_rather_than_overlapped() {
        let cfg = theme::Deskbar::default();
        assert_eq!(cfg.width, 288.0, "the fixture assumes the shipped width");
        let mut t = TextSystem::new("", "");
        // The fixture has to be able to overflow, or it proves nothing. Four apps and the full
        // set of figures is an ordinary laptop with a few things open, and at 288 the two halves
        // together are far wider than the box.
        let g = vec![
            grp("a", "A", Dom::None, &[("1", "w", false), ("2", "x", false)]),
            grp("b", "B", Dom::None, &[("3", "w", false), ("4", "x", false)]),
            grp("c", "C", Dom::None, &[("5", "w", false), ("6", "x", false)]),
            grp("d", "D", Dom::None, &[("7", "w", false), ("8", "x", false)]),
        ];
        let (figs, net) = (HashMap::new(), Net::default());
        let sys = SysFigures {
            cpu: Some(35.0),
            mem: Some(42.0),
            link: Some(crate::deskbar::sysinfo::Link::Wired),
            battery: Some(crate::deskbar::sysinfo::Battery { percent: 97, charging: false }),
            audio: None,
        };
        let v = View { groups: &g, expand: &[0.0], figures: &figs, net: &net, net_watching: false, sys, hover: None };
        let meta = build(&v, &st(&cfg, 900.0), &mut t).rows[0].meta.clone();

        let label = "4 apps \u{b7} 8 windows";
        let bold = FontSpec::new(14.0).bold();
        let small = FontSpec::new(14.0 - 2.0);
        let lw = t.string_width(label, &bold, 1.0);
        let mw = t.string_width(&meta, &small, 1.0);
        println!("label {lw:.1} + meta {mw:.1} in {}", cfg.width);
        assert!(!meta.is_empty(), "the figures vanished entirely");
        assert!(meta.starts_with("wired"), "the leading figure was cut instead of the trailing one: {meta:?}");
        assert!(
            PAD + lw + mw + PAD <= cfg.width,
            "label {lw:.1} and figures {mw:.1} do not fit in {}: they overlap",
            cfg.width
        );
    }
}
