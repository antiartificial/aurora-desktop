//! Rasterising and drawing the indicator. Everything here is the compositor's
//! own code: one CPU canvas per element, shaped by the one text stack, one
//! texture upload each, drawn after the capture was served. Layout is in
//! LOGICAL units and rounded once to device pixels, so it holds at any scale.

use std::time::{SystemTime, UNIX_EPOCH};

use smithay::{
    backend::{
        allocator::Fourcc,
        renderer::{Frame, ImportMem, Renderer, gles::{GlesRenderer, GlesTarget}},
    },
    output::Output,
    utils::{Buffer as BufferCoords, Physical, Point, Rectangle, Size, Transform},
};

use super::{Indicator, model::Held};
use crate::{
    fx::theme::{self, oklab_to_u8},
    shell::{layout::Rect, motion::Appear},
    text::{FontSpec, TextSystem},
    trusted::{draw::{Canvas, clean, fit}, verify::Verdict},
};

/// Text colour of a domain busd does not vouch for. Contrast over the panel is asserted on pixels.
pub const WARN: [u8; 4] = [255, 120, 100, 255];

const PILL_H: f32 = 24.0;
const PAD: f32 = 10.0;
const DOT: f32 = 10.0;
const GAP: f32 = 8.0;
const PANEL_W: f32 = 300.0;
const PANEL_GAP: f32 = 6.0;
const MAX_ROWS: usize = 5;

/// "Display" for the pill, "Display access" for the panel.
fn labels(resource: &str) -> (String, String) {
    match resource {
        "device:wayland" => ("Display".into(), "Display access".into()),
        "device:gpu" => ("GPU".into(), "GPU access".into()),
        "device:camera" => ("Camera".into(), "Camera".into()),
        "device:mic" | "device:microphone" => ("Mic".into(), "Microphone".into()),
        other => (clean(other), clean(other)),
    }
}

/// "12s", "3m 05s", "1h 02m". `since_ns` of 0 means busd gave no time.
pub fn since_text(now_ns: i128, since_ns: i128) -> String {
    if since_ns <= 0 {
        return "unknown".into();
    }
    let s = ((now_ns - since_ns).max(0) / 1_000_000_000) as u64;
    match s {
        0..=59 => format!("{s}s"),
        60..=3599 => format!("{}m {:02}s", s / 60, s % 60),
        _ => format!("{}h {:02}m", s / 3600, (s % 3600) / 60),
    }
}

fn now_ns() -> i128 {
    SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_nanos() as i128)
}

/// What the panel says about one held capability, as three lines.
#[derive(Debug)]
struct Row {
    head: String,
    who: String,
    attested: bool,
    held_for: String,
    cap: String,
}

fn rows(vis: &[&Held], now: i128) -> Vec<Row> {
    vis.iter()
        .take(MAX_ROWS)
        .map(|h| Row {
            head: labels(&h.resource).1,
            who: h.name.clone(),
            attested: h.verdict == Verdict::Verified,
            held_for: since_text(now, h.since_ns),
            cap: clean(&h.id).chars().take(20).collect(),
        })
        .collect()
}

struct Colours {
    dot: [u8; 4],
    text: [u8; 4],
    dim: [u8; 4],
    panel: [u8; 4],
}

fn colours(cfg: &theme::Indicator) -> Colours {
    let c = |o: [f32; 3]| {
        let [r, g, b] = oklab_to_u8(o);
        [r, g, b, 255]
    };
    Colours { dot: c(cfg.dot_oklab), text: c(cfg.text_oklab), dim: c(cfg.text_dim_oklab), panel: c(cfg.panel_oklab) }
}

fn pill(t: &mut TextSystem, label: &str, scale: f64, k: &Colours) -> Canvas {
    let d = |v: f32| (v as f64 * scale).round() as i32;
    let bold = FontSpec::new(13.0).bold();
    let w = PAD + DOT + GAP + t.string_width(label, &bold, scale).ceil() + PAD;
    let (cw, ch) = (d(w), d(PILL_H));
    let mut c = Canvas::new(cw as usize, ch as usize, [0, 0, 0, 0]);
    let r = ch as f32 / 2.0;
    let b = d(1.0).max(1);
    c.round_rect(0, 0, cw, ch, r, [k.dim[0], k.dim[1], k.dim[2], 120]);
    c.round_rect(b, b, cw - 2 * b, ch - 2 * b, r - b as f32, k.panel);
    let dd = d(DOT);
    c.round_rect(d(PAD), (ch - dd) / 2, dd, dd, dd as f32 / 2.0, k.dot);
    let m = t.metrics(&bold);
    t.draw(&mut c, PAD + DOT + GAP, (PILL_H - m.height()) / 2.0, label, &bold, k.text, scale);
    c
}

fn panel(t: &mut TextSystem, rows: &[Row], extra: usize, up: bool, w: f32, scale: f64, k: &Colours) -> Canvas {
    let d = |v: f32| (v as f64 * scale).round() as i32;
    let (bold, body, small) = (FontSpec::new(13.0).bold(), FontSpec::new(13.0), FontSpec::new(12.0));
    let line = 17.0f32;
    let h = 12.0 + line + 6.0 + rows.len() as f32 * (4.0 * line + 8.0) + if extra > 0 { line } else { 0.0 } + if up { 0.0 } else { line } + line + 12.0;
    let (cw, ch) = (d(w), d(h));
    let mut c = Canvas::new(cw as usize, ch as usize, [0, 0, 0, 0]);
    let r = d(10.0) as f32;
    let b = d(1.0).max(1);
    c.round_rect(0, 0, cw, ch, r, [k.dim[0], k.dim[1], k.dim[2], 120]);
    c.round_rect(b, b, cw - 2 * b, ch - 2 * b, r - b as f32, k.panel);
    let inner = w - 2.0 * PAD;
    let mut y = 12.0;
    t.draw(&mut c, PAD, y, "In use now", &bold, k.text, scale);
    y += line + 6.0;
    for row in rows {
        let dd = d(8.0);
        c.round_rect(d(PAD), d(y + 4.5), dd, dd, dd as f32 / 2.0, k.dot);
        t.draw(&mut c, PAD + 8.0 + 6.0, y, &row.head, &bold, k.text, scale);
        y += line;
        let x = t.draw(&mut c, PAD, y, "Domain  ", &body, k.dim, scale);
        let name = fit(t, &row.who, &body, scale, inner - (x - PAD), false);
        t.draw(&mut c, x, y, &name, &body, k.text, scale);
        y += line;
        let x = if row.attested {
            t.draw(&mut c, PAD, y, "attested by busd", &small, k.dim, scale)
        } else {
            t.draw(&mut c, PAD, y, "NOT ATTESTED", &FontSpec::new(12.0).bold(), WARN, scale)
        };
        t.draw(&mut c, x, y, &format!("   held for {}", row.held_for), &small, k.dim, scale);
        y += line;
        t.draw(&mut c, PAD, y, &row.cap, &small, k.dim, scale);
        y += line + 8.0;
    }
    if extra > 0 {
        t.draw(&mut c, PAD, y, &format!("+{extra} more not shown"), &small, WARN, scale);
        y += line;
    }
    if !up {
        t.draw(&mut c, PAD, y, "Bus lost: this may be out of date.", &small, WARN, scale);
        y += line;
    }
    t.draw(&mut c, PAD, y, "Drawn by the compositor. Not in captures.", &small, k.dim, scale);
    c
}

/// Draw the indicator over the already-rendered, already-captured frame.
/// Called after `serve_pending` and never before.
pub fn draw_indicator(
    ind: &mut Indicator,
    cfg: &theme::Indicator,
    t: &mut TextSystem,
    renderer: &mut GlesRenderer,
    framebuffer: &mut GlesTarget<'_>,
    output: &Output,
    size: Size<i32, Physical>,
) -> Result<(), Box<dyn std::error::Error>> {
    let vis: Vec<Held> = ind.visible(cfg).into_iter().cloned().collect();
    if vis.is_empty() {
        ind.appear = None;
        ind.panel_appear = None;
        ind.pill = None;
        ind.panel = None;
        ind.hover = false;
        ind.pinned = false;
        ind.logged = None;
        return Ok(());
    }
    let scale = output.current_scale().fractional_scale();
    let d = |v: f32| (v as f64 * scale).round() as i32;
    let open = ind.hover || ind.pinned;
    let appear_new = || (cfg.ease_ms > 0).then(|| Appear::new(cfg.ease_ms));
    if ind.appear.is_none() {
        ind.appear = Some(appear_new().unwrap_or_else(|| Appear::new(1)));
    }
    match (open, ind.panel_appear.is_some()) {
        (true, false) => ind.panel_appear = Some(appear_new().unwrap_or_else(|| Appear::new(1))),
        (false, true) => ind.panel_appear = None,
        _ => {}
    }
    let k = colours(cfg);
    let now = now_ns();
    let shown = rows(&vis.iter().collect::<Vec<_>>(), now);
    let extra = vis.len().saturating_sub(MAX_ROWS);
    let (first, more) = (&vis[0], vis.len() - 1);
    let name = fit(t, &first.name, &FontSpec::new(13.0).bold(), scale, d(240.0) as f32 / scale as f32, false);
    let label = format!("{}: {}{}", labels(&first.resource).0, name, if more > 0 { format!(" +{more}") } else { String::new() });
    let out_w = size.w as f64 / scale;
    let pw = PANEL_W.min((out_w as f32 - 2.0 * cfg.margin).max(120.0));
    let key = format!("{cfg:?}|{scale}|{}x{}|{label}|{open}|{shown:?}|{extra}|{}", size.w, size.h, ind.up);
    if ind.cache.as_ref().map(|c| c.0.as_str()) != Some(key.as_str()) {
        let p = pill(t, &label, scale, &k);
        let q = open.then(|| panel(t, &shown, extra, ind.up, pw, scale, &k));
        ind.cache = Some((key, p, q));
    }
    let (_, pc, qc) = ind.cache.as_ref().unwrap();
    let pill_r = Rect { x: size.w - d(cfg.margin) - pc.w as i32, y: d(cfg.margin), w: pc.w as i32, h: pc.h as i32 };
    let panel_r = qc.as_ref().map(|q| Rect {
        x: (pill_r.x + pill_r.w - q.w as i32).max(0),
        y: pill_r.y + pill_r.h + d(PANEL_GAP),
        w: q.w as i32,
        h: q.h as i32,
    });
    ind.pill = Some(pill_r);
    ind.panel = panel_r;
    let (dot_d, dot_x) = (d(DOT), pill_r.x + d(PAD));
    let line = format!(
        "indicator: layout scale={scale} pill={},{},{},{} dot={},{},{},{} held={} since={} text={label:?} rows=[{}] panel={} dot_rgb={},{},{} text_rgb={},{},{} dim_rgb={},{},{} panel_rgb={},{},{}",
        pill_r.x, pill_r.y, pill_r.w, pill_r.h, dot_x, pill_r.y + (pill_r.h - dot_d) / 2, dot_d, dot_d, vis.len(), first.since_ns,
        shown.iter().map(|r| format!("{}|{}|{}", r.head, r.who, if r.attested { "attested" } else { "unattested" })).collect::<Vec<_>>().join(";"),
        panel_r.map_or("none".to_string(), |r| format!("{},{},{},{}", r.x, r.y, r.w, r.h)),
        k.dot[0], k.dot[1], k.dot[2], k.text[0], k.text[1], k.text[2], k.dim[0], k.dim[1], k.dim[2], k.panel[0], k.panel[1], k.panel[2],
    );
    if ind.logged.as_ref() != Some(&line) {
        tracing::info!("{line}");
        ind.logged = Some(line);
    }
    let alpha = |a: &Option<Appear>| a.map_or(1.0, |a| if cfg.ease_ms == 0 { 1.0 } else { a.eased() });
    let (pa, qa) = (alpha(&ind.appear), alpha(&ind.panel_appear));
    let mut import = |c: &Canvas| renderer.import_memory(&c.rgba, Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((c.w as i32, c.h as i32)), false);
    let tp = import(pc)?;
    let tq = match qc {
        Some(q) => Some(import(q)?),
        None => None,
    };
    let mut frame = renderer.render(framebuffer, size, output.current_transform())?;
    let mut blit = |tex: &smithay::backend::renderer::gles::GlesTexture, c: &Canvas, r: Rect, a: f32| -> Result<(), Box<dyn std::error::Error>> {
        let dst = Rectangle::new(Point::<i32, Physical>::from((r.x, r.y)), (c.w as i32, c.h as i32).into());
        let src = Rectangle::<f64, BufferCoords>::from_size((c.w as f64, c.h as f64).into());
        let local = Rectangle::<i32, Physical>::from_size(dst.size);
        frame.render_texture_from_to(tex, src, dst, &[local], &[], Transform::Normal, a, None, &[])?; // no opaque region: the corners are transparent
        Ok(())
    };
    blit(&tp, pc, pill_r, pa)?;
    if let (Some(q), Some(r), Some(tq)) = (qc, panel_r, &tq) {
        blit(tq, q, r, qa)?;
    }
    let sync = frame.finish()?;
    renderer.wait(&sync)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn elapsed_time_reads_as_a_person_would() {
        let s = 1_000_000_000i128;
        assert_eq!(since_text(100 * s, 0), "unknown");
        assert_eq!(since_text(100 * s + 5, 100 * s), "0s");
        assert_eq!(since_text(112 * s, 100 * s), "12s");
        assert_eq!(since_text(100 * s + 185 * s, 100 * s), "3m 05s");
        assert_eq!(since_text(100 * s + 3725 * s, 100 * s), "1h 02m");
        assert_eq!(since_text(90 * s, 100 * s), "0s", "a clock that stepped back is not a negative time");
    }

    #[test]
    fn unknown_classes_are_named_by_their_cleaned_resource() {
        assert_eq!(labels("device:wayland").1, "Display access");
        assert_eq!(labels("device:x\u{1b}[2J").0, "device:x?[2J");
    }
}
