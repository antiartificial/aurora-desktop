//! Window decorations: the title bar the compositor draws above a window that did not draw its own.
//!
//! Two halves. `Frame` and `hit` are pure arithmetic in LOGICAL units (the unit the `Space`, window
//! geometry, configures and the pointer speak): where the bar is, where the close button is, which
//! edge a press near the window grabs. They are unit-tested below and hit-tested by `windeco.rs`.
//! The drawing half rasterises the bar into a texture at DEVICE pixels (`scale` converts, exactly
//! as it does for the dock) and `fx::render_scene` puts chrome glass behind it, so a title is
//! legible over any backdrop by the same luminance ceiling that protects the dock's labels.
//!
//! The bar hangs ABOVE the window: the client's own pixels are never covered, so a window's geometry
//! is still the client's, and a move or resize grab (which work in client geometry) needs no change.
//! The resize zones lie OUTSIDE the frame for the same reason: nothing is taken from the client.

use std::collections::HashMap;

use smithay::{
    backend::{
        allocator::Fourcc,
        renderer::{ImportMem, gles::{GlesRenderer, GlesTexture}},
    },
    utils::{Buffer as BufferCoords, Logical, Rectangle, Size},
};

use super::{
    Res, Shell,
    layout::Rect,
    motion::Ease,
    settings::clip,
};
use crate::{
    fx::theme::{DecoStyle, oklab_to_u8},
    grabs::resize_grab::ResizeEdge,
    scale::rect_to_physical,
    text::FontSpec,
    trusted::draw::Canvas,
};

pub type R = Rectangle<i32, Logical>;

/// Padding between the bar's parts, logical pixels.
const PAD: i32 = 8;
/// How far inside the bar's top edge a press still grabs the top edge, logical pixels. This is what
/// makes a window at the very top of the screen resizable from the top.
const INNER: f64 = 3.0;
/// Horizontal padding inside the domain badge, logical pixels.
const BADGE_PAD: i32 = 7;

#[derive(Clone, Debug, PartialEq)]
pub struct Frame {
    /// The client's geometry.
    pub window: R,
    pub bar: R,
    pub close: R,
    /// Where the title may be drawn.
    pub title: R,
}

/// The bar and its parts for a client at `window`. `tab_w` is the width a `Tab` wants (ignored by `Bar`).
pub fn frame(style: DecoStyle, h: i32, window: R, tab_w: i32) -> Frame {
    let w = match style {
        DecoStyle::Bar => window.size.w,
        DecoStyle::Tab => tab_w.clamp(0, window.size.w),
    };
    let bar = Rectangle::new((window.loc.x, window.loc.y - h).into(), (w, h).into());
    let close = Rectangle::new(bar.loc, (h.min(w), h).into());
    let tx = bar.loc.x + close.size.w;
    let title = Rectangle::new((tx, bar.loc.y).into(), ((bar.loc.x + w - PAD - tx).max(0), h).into());
    Frame { window, bar, close, title }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Hit {
    Close,
    Bar,
    Edge(ResizeEdge),
}

fn has(r: R, p: (f64, f64)) -> bool {
    p.0 >= r.loc.x as f64 && p.0 < (r.loc.x + r.size.w) as f64 && p.1 >= r.loc.y as f64 && p.1 < (r.loc.y + r.size.h) as f64
}

/// What a press at `p` (logical) means for the window with this frame.
///
/// `None`: the client's own pixels, or nothing of this window's. Edges and corners are only offered
/// while `resizable`. `grip` is how far OUTSIDE the frame an edge reaches; `corner` is how far along
/// the edge from a corner the corner reaches.
pub fn hit(f: &Frame, grip: f64, corner: f64, p: (f64, f64), resizable: bool) -> Option<Hit> {
    if has(f.bar, p) {
        if has(f.close, p) {
            return Some(Hit::Close);
        }
        if resizable && p.1 < f.bar.loc.y as f64 + INNER {
            let e = edges_in(f.bar, grip, corner, p, ResizeEdge::TOP);
            return Some(Hit::Edge(e));
        }
        return Some(Hit::Bar);
    }
    if has(f.window, p) {
        return None;
    }
    if !resizable {
        return None;
    }
    // The frame is the window and, for the bar style, the bar above it as one rectangle; a tab is a
    // second, narrower rectangle on top of the window's.
    let whole = Rectangle::new(f.bar.loc, (f.window.size.w, f.window.loc.y + f.window.size.h - f.bar.loc.y).into());
    let refs: &[R] = if f.bar.size.w == f.window.size.w { &[whole] } else { &[f.bar, f.window] };
    for &r in refs {
        let grown = Rectangle::new((r.loc.x - grip.ceil() as i32, r.loc.y - grip.ceil() as i32).into(), (r.size.w + 2 * grip.ceil() as i32, r.size.h + 2 * grip.ceil() as i32).into());
        if has(grown, p) {
            return Some(Hit::Edge(edges_in(r, grip, corner, p, ResizeEdge::empty())));
        }
    }
    None
}

/// The edges a press at `p` grabs, relative to `r`: the side it is outside of, promoted to a corner
/// within `corner` of one. `base` is an edge known already (the bar's top).
fn edges_in(r: R, _grip: f64, corner: f64, p: (f64, f64), base: ResizeEdge) -> ResizeEdge {
    let (x0, y0, x1, y1) = (r.loc.x as f64, r.loc.y as f64, (r.loc.x + r.size.w) as f64, (r.loc.y + r.size.h) as f64);
    let mut e = base;
    if p.0 < x0 {
        e |= ResizeEdge::LEFT;
    } else if p.0 >= x1 {
        e |= ResizeEdge::RIGHT;
    }
    if p.1 < y0 {
        e |= ResizeEdge::TOP;
    } else if p.1 >= y1 {
        e |= ResizeEdge::BOTTOM;
    }
    let only_side = !e.intersects(ResizeEdge::TOP | ResizeEdge::BOTTOM);
    let only_end = !e.intersects(ResizeEdge::LEFT | ResizeEdge::RIGHT);
    if only_side && !e.is_empty() {
        if p.1 < y0 + corner {
            e |= ResizeEdge::TOP;
        } else if p.1 >= y1 - corner {
            e |= ResizeEdge::BOTTOM;
        }
    }
    if only_end && !e.is_empty() {
        if p.0 < x0 + corner {
            e |= ResizeEdge::LEFT;
        } else if p.0 >= x1 - corner {
            e |= ResizeEdge::RIGHT;
        }
    }
    e
}

// ---- the shell's side: state and drawing -----------------------------------------------------

/// What `windeco.rs` tells the shell about one decorated window, once a frame.
#[derive(Clone, Debug)]
pub struct DecoSpec {
    pub id: String,
    pub window: R,
    pub title: String,
    /// The domain the kernel put the client in, or `None`. Drawn by the compositor as a badge the
    /// client cannot imitate inside the bar; the title beside it is only what the client claims.
    pub domain: Option<String>,
    pub focused: bool,
    pub hover_close: bool,
}

pub(super) struct DecoState {
    spec: DecoSpec,
    frame: Frame,
    focus: f32,
    hover: f32,
    n: u32,
}

/// One bar for the effects pipeline to draw: chrome glass behind, the texture over.
pub struct DecoDraw<'a> {
    pub tex: &'a GlesTexture,
    /// The texture's place, device pixels. It reaches `radius` below the bar, under the client, so the
    /// glass's rounded bottom corners are hidden and the bar meets the window square.
    pub dst: Rect,
}

/// Where the title text landed in its bar: left, top, width (device pixels, relative to the bar) and the clipped text.
type Placed = (i32, i32, i32, String);

#[derive(Default)]
pub(super) struct DecoStore {
    states: Vec<Option<DecoState>>,
    tex: HashMap<String, (String, GlesTexture, Placed)>,
    anim: HashMap<String, (Ease, Ease)>,
    seq: HashMap<String, u32>,
    logged: HashMap<String, String>,
    tab_w: HashMap<(String, u64), f32>,
}

fn stroke(c: &mut Canvas, a: (f32, f32), b: (f32, f32), w: f32, col: [u8; 4]) {
    let (x0, y0) = (a.0.min(b.0) - w - 1.0, a.1.min(b.1) - w - 1.0);
    let (x1, y1) = (a.0.max(b.0) + w + 1.0, a.1.max(b.1) + w + 1.0);
    let (dx, dy) = (b.0 - a.0, b.1 - a.1);
    let len2 = dx * dx + dy * dy;
    for y in y0.floor() as i32..=y1.ceil() as i32 {
        for x in x0.floor() as i32..=x1.ceil() as i32 {
            let (px, py) = (x as f32 + 0.5, y as f32 + 0.5);
            let t = if len2 > 0.0 { (((px - a.0) * dx + (py - a.1) * dy) / len2).clamp(0.0, 1.0) } else { 0.0 };
            let d = ((px - (a.0 + t * dx)).powi(2) + (py - (a.1 + t * dy)).powi(2)).sqrt();
            let cov = (w / 2.0 + 0.5 - d).clamp(0.0, 1.0);
            if cov > 0.0 {
                c.over(x, y, [col[0], col[1], col[2], (col[3] as f32 * cov).round() as u8]);
            }
        }
    }
}

/// A frame's rectangles in device pixels: (window, bar, close). The window's is what `fx::render_scene`
/// draws it at; the bar's lower edge IS the window's upper edge, whatever the rounding, so the bar
/// meets the window with no seam and no overlap, at any fractional scale.
pub fn device(f: &Frame, s: f64) -> (Rect, Rect, Rect) {
    let w = rect_to_physical(f.window, s);
    let by = (f.bar.loc.y as f64 * s).round() as i32;
    let bx = w.loc.x + ((f.bar.loc.x - f.window.loc.x) as f64 * s).round() as i32;
    let bw = if f.bar.size.w == f.window.size.w { w.size.w } else { (f.bar.size.w as f64 * s).round() as i32 };
    let bar = Rect { x: bx, y: by, w: bw, h: (w.loc.y - by).max(1) };
    let cw = ((f.close.size.w as f64 * s).round() as i32).min(bw);
    let close = Rect { x: bx, y: by, w: cw, h: bar.h };
    (Rect { x: w.loc.x, y: w.loc.y, w: w.size.w, h: w.size.h }, bar, close)
}

fn lerp(a: [u8; 3], b: [u8; 3], t: f32) -> [u8; 3] {
    [0, 1, 2].map(|i| (a[i] as f32 + (b[i] as f32 - a[i] as f32) * t).round() as u8)
}

impl Shell {
    /// Whether the compositor draws decorations at all: the theme says so, and this is not the
    /// pre-shell `--legacy-scene` look (which tests that compare frames byte for byte rely on).
    pub fn deco_on(&self) -> bool {
        self.deco.enabled && !self.legacy_scene
    }

    pub fn deco_height(&self) -> i32 {
        self.deco.height.round() as i32
    }

    /// A bar's frame for a client at `window`. The tab style measures `title` (in the bold face, the
    /// wider, so focus never changes the tab's width).
    pub fn deco_frame(&mut self, window: R, title: &str, domain: Option<&str>) -> Frame {
        let h = self.deco_height();
        let badge = domain.map_or(0, |d| self.badge_logical_w(d) + PAD);
        let tab_w = if self.deco.style == DecoStyle::Tab {
            let key = (title.to_string(), self.scale.to_bits());
            let w = match self.decos.tab_w.get(&key) {
                Some(w) => *w,
                None => {
                    let w = self.text.string_width(title, &FontSpec::new(self.font.size).bold(), self.scale);
                    if self.decos.tab_w.len() > 256 {
                        self.decos.tab_w.clear();
                    }
                    self.decos.tab_w.insert(key, w);
                    w
                }
            };
            (h + (w.ceil() as i32) + PAD + badge).max(h * 3)
        } else {
            0
        };
        let mut f = frame(self.deco.style, h, window, tab_w);
        // The badge sits at the bar's right end; the title's room ends before it.
        f.title.size.w = (f.title.size.w - badge).max(0);
        f
    }

    /// The windows to decorate this frame, in `Space` order (`None` for one that is not).
    pub fn set_decos(&mut self, specs: Vec<Option<DecoSpec>>) {
        let ms = self.deco.ease_ms;
        let mut live: Vec<String> = vec![];
        let mut states = Vec::with_capacity(specs.len());
        for s in specs {
            let Some(spec) = s else {
                states.push(None);
                continue;
            };
            let frame = self.deco_frame(spec.window, &spec.title, spec.domain.as_deref());
            let (f, h) = self.decos.anim.entry(spec.id.clone()).or_insert_with(|| (Ease::at(spec.focused as u8 as f32), Ease::at(0.0)));
            f.set(spec.focused as u8 as f32, ms);
            h.set(spec.hover_close as u8 as f32, ms);
            let (focus, hover) = (f.value(ms), h.value(ms));
            let next = self.decos.seq.len() as u32 + 1;
            let n = *self.decos.seq.entry(spec.id.clone()).or_insert(next);
            live.push(spec.id.clone());
            states.push(Some(DecoState { spec, frame, focus, hover, n }));
        }
        self.decos.anim.retain(|k, _| live.contains(k));
        self.decos.tex.retain(|k, _| live.contains(k));
        self.decos.logged.retain(|k, _| live.contains(k));
        self.decos.states = states;
    }

    /// Whether any bar is mid-transition (the frame needs redrawing even if nothing else changed).
    pub fn deco_animating(&self) -> bool {
        self.decos.states.iter().flatten().any(|s| s.focus.fract() != 0.0 || s.hover.fract() != 0.0)
    }

    /// Rasterise every bar whose look changed.
    pub(super) fn prepare_decos(&mut self, r: &mut GlesRenderer) -> Res<()> {
        let s = self.scale;
        let states = std::mem::take(&mut self.decos.states);
        let res = (|| -> Res<()> {
            for st in states.iter().flatten() {
                let (win, bar, _) = device(&st.frame, s);
                let ext = (self.glass_radius as f64 * s).round() as i32;
                let ext = ext.min(win.h).max(0);
                let (fq, hq) = ((st.focus * 16.0).round() as i32, (st.hover * 16.0).round() as i32);
                let key = format!(
                    "{}x{}|{}|{:?}|{fq}|{hq}|{s}|{}|{}|{:?}|{}|{}|{:?}",
                    bar.w, bar.h, st.spec.title, st.spec.domain, self.glass_on, self.font.size, self.deco.style, self.font.family, self.rev, self.deco
                );
                let placed = match self.decos.tex.get(&st.spec.id) {
                    Some((k, _, p)) if *k == key => p.clone(),
                    _ => {
                        let (c, placed) = self.draw_bar(st, bar.w, bar.h, ext);
                        let tex = r.import_memory(&c.rgba, Fourcc::Abgr8888, Size::<i32, BufferCoords>::from((c.w as i32, c.h as i32)), false)?;
                        self.decos.tex.insert(st.spec.id.clone(), (key, tex, placed.clone()));
                        placed
                    }
                };
                // One line per change of layout, not per animation frame: tests read geometry off it.
                let r4 = |r: Rect| format!("{},{},{},{}", r.x, r.y, r.w, r.h);
                let (_, _, close) = device(&st.frame, s);
                let line = format!(
                    "scale={s} style={:?} win={} window={} bar={} close={} focused={} title={},{},{} text={}{}",
                    self.deco.style, st.n, r4(win), r4(bar), r4(close), st.spec.focused,
                    bar.x + placed.0, bar.y + placed.1, placed.2, placed.3,
                    // only a domain window has this field, so a window without one logs exactly what it always did
                    st.spec.domain.as_ref().map(|d| format!(" domain={d}")).unwrap_or_default()
                );
                if self.decos.logged.get(&st.spec.id) != Some(&line) {
                    tracing::info!("deco: layout {line}");
                    self.decos.logged.insert(st.spec.id.clone(), line);
                }
            }
            Ok(())
        })();
        self.decos.states = states;
        res
    }

    /// The bar's texture and where it goes, for the window at `i` in `Space` order.
    pub fn deco_draw(&self, i: usize) -> Option<DecoDraw<'_>> {
        let st = self.decos.states.get(i)?.as_ref()?;
        let (_, tex, _) = self.decos.tex.get(&st.spec.id)?;
        let (win, bar, _) = device(&st.frame, self.scale);
        let ext = ((self.glass_radius as f64 * self.scale).round() as i32).min(win.h).max(0);
        Some(DecoDraw { tex, dst: Rect { x: bar.x, y: bar.y, w: bar.w, h: bar.h + ext } })
    }

    fn badge_spec(&self) -> FontSpec {
        FontSpec::new(self.font.size * 0.85).bold()
    }

    /// What the badge says: the domain's name, bounded. The cgroup reader already refused any name
    /// the adapter would not create, so this only has to keep it short.
    fn badge_label(domain: &str) -> String {
        if domain.chars().count() > 32 { format!("{}...", domain.chars().take(29).collect::<String>()) } else { domain.to_string() }
    }

    /// The badge's width in logical pixels, text plus its padding.
    pub(super) fn badge_logical_w(&mut self, domain: &str) -> i32 {
        let spec = self.badge_spec();
        (self.text.string_width(&Self::badge_label(domain), &spec, self.scale).ceil() as i32) + 2 * BADGE_PAD
    }

    /// The domain badge: a filled, rounded pill at the bar's right end. It is drawn by the
    /// compositor into the bar, which no client can reach, so a client's title cannot pass for it.
    fn draw_badge(&mut self, c: &mut Canvas, domain: &str, bw: i32, bh: i32, accent: [u8; 3]) {
        let s = self.scale;
        let d = |v: f32| (v as f64 * s).round() as i32;
        let spec = self.badge_spec();
        let label = Self::badge_label(domain);
        let tw = self.text.string_width(&label, &spec, s);
        let pw = d(tw + 2.0 * BADGE_PAD as f32);
        let m = self.text.metrics(&spec);
        let ph = d(m.ascent + m.descent + 6.0).min(bh - 2).max(1);
        let (px, py) = (bw - d(PAD as f32) - pw, (bh - ph) / 2);
        if px <= 0 {
            return;
        }
        c.round_rect(px, py, pw, ph, ph as f32 / 2.0, [accent[0], accent[1], accent[2], 235]);
        let ty = py as f32 / s as f32 + (ph as f32 / s as f32 - (m.ascent + m.descent)) / 2.0;
        self.text.draw(c, (px as f32 / s as f32) + BADGE_PAD as f32, ty, &label, &spec, [16, 18, 26, 255], s);
    }

    /// Draw one bar: device pixels, premultiplied. Returns the canvas and the title's placement
    /// (x and y relative to the bar, device width, and the clipped text).
    fn draw_bar(&mut self, st: &DecoState, bw: i32, bh: i32, ext: i32) -> (Canvas, (i32, i32, i32, String)) {
        let s = self.scale;
        let d = |v: f32| (v as f64 * s).round() as i32;
        let mut c = Canvas::new(bw.max(1) as usize, (bh + ext).max(1) as usize, [0, 0, 0, 0]);
        let cfg = self.deco.clone();
        let (accent, text, dim, close) = (oklab_to_u8(cfg.accent_oklab), oklab_to_u8(cfg.text_oklab), oklab_to_u8(cfg.text_dim_oklab), oklab_to_u8(cfg.close_oklab));
        let (f, hv) = (st.focus, st.hover);
        if !self.glass_on {
            c.round_rect(0, 0, bw, bh + ext, self.glass_radius * s as f32, [18, 20, 30, 215]);
        }
        // Focus is shown three ways besides colour: a stripe along the bar's bottom edge, a bold title,
        // and (below) a filled close button. An unfocused bar has a hairline, a regular title and a bare glyph.
        let stripe = d(2.0).max(2);
        c.over_rect(0, bh - 1, bw, 1, [255, 255, 255, 34]);
        if f > 0.0 {
            let cr = self.glass_radius * s as f32;
            c.over_rect((cr * 0.0) as i32, bh - stripe, bw, stripe, [accent[0], accent[1], accent[2], (230.0 * f).round() as u8]);
            if cfg.style == DecoStyle::Tab {
                c.round_rect(0, 0, bw, bh + ext, cr, [accent[0], accent[1], accent[2], (38.0 * f).round() as u8]);
            }
        }
        // Close button: a disc and a cross, centred in the button's square.
        let (_, _, cl) = device(&st.frame, s);
        let (cx, cy) = (cl.w as f32 / 2.0, cl.h as f32 / 2.0);
        let rad = (cfg.height * 0.25).max(5.0) * s as f32;
        let disc = [255, 255, 255, (14.0 + 26.0 * f).round() as u8];
        c.round_rect((cx - rad).round() as i32, (cy - rad).round() as i32, (rad * 2.0).round() as i32, (rad * 2.0).round() as i32, rad, disc);
        if hv > 0.0 {
            c.round_rect((cx - rad).round() as i32, (cy - rad).round() as i32, (rad * 2.0).round() as i32, (rad * 2.0).round() as i32, rad, [close[0], close[1], close[2], (240.0 * hv).round() as u8]);
        }
        let glyph = lerp(lerp(dim, accent, f), [255, 255, 255], hv);
        let k = rad * 0.42;
        let lw = (1.5 * s as f32).max(1.5);
        stroke(&mut c, (cx - k, cy - k), (cx + k, cy + k), lw, [glyph[0], glyph[1], glyph[2], 255]);
        stroke(&mut c, (cx - k, cy + k), (cx + k, cy - k), lw, [glyph[0], glyph[1], glyph[2], 255]);
        // Title: clipped to its room with the same `clip` the settings panel uses.
        let spec = if f >= 0.5 { FontSpec::new(self.font.size).bold() } else { FontSpec::new(self.font.size) };
        let m = self.text.metrics(&spec);
        let room = st.frame.title.size.w as f32;
        let shown = clip(&mut self.text, &st.spec.title, &spec, room, s);
        let col = lerp(dim, text, f);
        let tx = (st.frame.title.loc.x - st.frame.bar.loc.x) as f32;
        let ty = (cfg.height - (m.ascent + m.descent)) / 2.0;
        let end = if room > 0.0 { self.text.draw(&mut c, tx, ty, &shown, &spec, [col[0], col[1], col[2], 255], s) } else { tx };
        let placed = (d(tx), d(ty), d(end - tx), shown);
        if let Some(dom) = &st.spec.domain {
            self.draw_badge(&mut c, dom, bw, bh, accent);
        }
        (c, placed)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn r(x: i32, y: i32, w: i32, h: i32) -> R {
        Rectangle::new((x, y).into(), (w, h).into())
    }

    fn win() -> Frame {
        frame(DecoStyle::Bar, 30, r(100, 100, 400, 300), 0)
    }

    #[test]
    fn the_bar_hangs_above_the_window_and_the_close_button_is_a_square_at_its_left() {
        let f = win();
        assert_eq!(f.bar, r(100, 70, 400, 30));
        assert_eq!(f.close, r(100, 70, 30, 30));
        assert_eq!(f.title, r(130, 70, 400 - 30 - PAD, 30));
        let t = frame(DecoStyle::Tab, 30, r(100, 100, 400, 300), 150);
        assert_eq!((t.bar.size.w, t.title.loc.x), (150, 130));
        assert_eq!(frame(DecoStyle::Tab, 30, r(0, 50, 100, 80), 500).bar.size.w, 100, "a tab never outgrows its window");
    }

    #[test]
    fn presses_resolve_to_close_bar_edges_corners_or_the_client() {
        let f = win();
        let h = |x: f64, y: f64| hit(&f, 8.0, 20.0, (x, y), true);
        assert_eq!(h(110.0, 85.0), Some(Hit::Close));
        assert_eq!(h(300.0, 85.0), Some(Hit::Bar));
        assert_eq!(h(300.0, 200.0), None, "the client's own pixels");
        // 5 px outside the right edge, mid-height: the right edge. 5 px below the bottom: the bottom.
        assert_eq!(h(505.0, 250.0), Some(Hit::Edge(ResizeEdge::RIGHT)));
        assert_eq!(h(300.0, 405.0), Some(Hit::Edge(ResizeEdge::BOTTOM)));
        assert_eq!(h(95.0, 250.0), Some(Hit::Edge(ResizeEdge::LEFT)));
        // Above the bar: the top. Near either end: a corner.
        assert_eq!(h(300.0, 65.0), Some(Hit::Edge(ResizeEdge::TOP)));
        assert_eq!(h(105.0, 65.0), Some(Hit::Edge(ResizeEdge::TOP_LEFT)));
        assert_eq!(h(495.0, 65.0), Some(Hit::Edge(ResizeEdge::TOP_RIGHT)));
        // The bottom corners, and a side press that is close to the bottom.
        assert_eq!(h(503.0, 398.0), Some(Hit::Edge(ResizeEdge::BOTTOM_RIGHT)));
        assert_eq!(h(97.0, 405.0), Some(Hit::Edge(ResizeEdge::BOTTOM_LEFT)));
        // Beyond the grip: nothing.
        assert_eq!(h(520.0, 250.0), None);
        // The bar's own top 3 px grab the top edge; the rest of the bar drags.
        assert_eq!(h(300.0, 71.0), Some(Hit::Edge(ResizeEdge::TOP)));
        assert_eq!(h(300.0, 74.0), Some(Hit::Bar));
        // A window that cannot be resized offers a bar and a close button and no edges.
        assert_eq!(hit(&f, 8.0, 20.0, (505.0, 250.0), false), None);
        assert_eq!(hit(&f, 8.0, 20.0, (300.0, 71.0), false), Some(Hit::Bar));
    }

    #[test]
    fn a_tab_is_a_drag_handle_only_as_wide_as_itself() {
        let f = frame(DecoStyle::Tab, 30, r(100, 100, 400, 300), 150);
        assert_eq!(hit(&f, 8.0, 20.0, (200.0, 85.0), true), Some(Hit::Bar));
        assert_ne!(hit(&f, 8.0, 20.0, (400.0, 85.0), true), Some(Hit::Bar), "right of the tab is empty desktop");
        assert_eq!(hit(&f, 8.0, 20.0, (400.0, 95.0), true), Some(Hit::Edge(ResizeEdge::TOP)), "just above the window, beside the tab");
    }
}
