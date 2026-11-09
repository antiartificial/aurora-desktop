//! The surface header: what a window offers, drawn by the compositor from a
//! `contracts/surface` descriptor.
//!
//! This is trusted chrome, not a client surface. The descriptor is the wire
//! form (`contracts/surface/surface.go`), and the header is where the OS keeps
//! its promise that a surface's grants are VISIBLE: an app may not quietly
//! widen its access, because the count sits in the same band as its title.
//! Nothing here may look like a warning -- there are no effect badges, no red,
//! and no approval analogue. A write is still just a label; the decision to
//! allow one belongs to the trusted overlay, which the compositor draws after
//! captures are served. A header that borrowed that vocabulary would teach
//! people to dismiss the one prompt that matters.
//!
//! Parsing is `serde_json`, already a dependency: the contract is JSON, and a
//! hand-rolled reader would be a second parser to keep in step with it.
//!
//! A header belongs to ONE window. A descriptor names its app (`app` is the
//! toplevel's `app_id`), and the band is placed against that window's own top
//! edge, not at the output's corner: chrome that floated in the corner would
//! describe a surface the person is not looking at. When no window of that app
//! is mapped the band is not drawn at all -- there is nothing for it to describe
//! -- and it comes back, at the window's new place, if the app maps again.

use std::path::Path;

use serde::Deserialize;
use smithay::{
    backend::{
        allocator::Fourcc,
        renderer::{
            ImportMem,
            gles::{GlesRenderer, GlesTexture},
        },
    },
    desktop::{Space, Window},
    utils::{Buffer as BufferCoords, Physical, Size},
    wayland::{compositor::with_states, shell::xdg::XdgToplevelSurfaceData},
};

use super::{Res, Shell, layout::Rect};
use crate::{
    text::{FontSpec, TextSystem},
    trusted::draw::{Canvas, clean},
    windeco::win_key,
};

/// The band's fill. Opaque, so the header reads the same over any wallpaper and
/// a pixel assertion does not depend on what is behind it.
pub const FILL: [u8; 4] = [16, 18, 26, 255];
/// One device-pixel rule along the bottom, so the band has an edge without glass.
const RULE: [u8; 4] = [38, 42, 54, 255];
const TITLE: [u8; 4] = [238, 240, 244, 255];
const ACTION: [u8; 4] = [168, 176, 190, 255];
/// The grants chip: quiet blue, the colour of information, not of danger.
const GRANT_TEXT: [u8; 4] = [150, 200, 245, 255];
const CHIP: [u8; 4] = [30, 36, 48, 255];
/// Empty margin from the usable area's edge, logical pixels.
const MARGIN: f32 = 18.0;
/// Between the band's bottom edge and the window's top edge, logical pixels: a
/// gap, so the band reads as a label on the window rather than part of its frame.
const EDGE_GAP: f32 = 8.0;
const PAD_X: f32 = 14.0;
const PAD_Y: f32 = 9.0;
const GAP: f32 = 14.0;
const RADIUS: f32 = 10.0;

/// One thing a surface offers. `effect` is validated because an unknown effect
/// is an unpoliced action (the contract says so, and this is the second reader).
#[derive(Clone, Debug, Deserialize)]
pub struct Action {
    pub id: String,
    pub label: String,
    pub effect: String,
    #[serde(default)]
    pub grants: Vec<String>,
}

/// One window's surface, as `contracts/surface` defines it.
#[derive(Clone, Debug, Deserialize)]
pub struct Descriptor {
    pub app: String,
    pub title: String,
    pub actions: Vec<Action>,
    #[serde(default)]
    pub grants: Vec<String>,
}

impl Descriptor {
    /// Parse and validate in one step, like the contract's `Unmarshal`, so a
    /// caller cannot forget the second half.
    pub fn load(path: &Path) -> Result<Descriptor, String> {
        let bytes =
            std::fs::read(path).map_err(|e| format!("cannot read {}: {e}", path.display()))?;
        let mut d: Descriptor =
            serde_json::from_slice(&bytes).map_err(|e| format!("not a surface descriptor: {e}"))?;
        if d.app.is_empty() {
            return Err("surface: app is empty".into());
        }
        let mut seen = std::collections::HashSet::with_capacity(d.actions.len());
        for a in &d.actions {
            if a.id.is_empty() {
                return Err("surface: an action has no id".into());
            }
            if !seen.insert(a.id.as_str()) {
                return Err(format!("surface: duplicate action id {:?}", a.id));
            }
            match a.effect.as_str() {
                "read" | "write" | "nominal" => {}
                other => {
                    return Err(format!(
                        "surface: action {:?} has effect {other:?}, which is not read/write/nominal",
                        a.id
                    ));
                }
            }
        }
        // The wire form is canonical (actions by id, grants sorted). Sorting
        // again here means a producer that forgets cannot make the same tree
        // draw a different header on a later run, which would make a golden
        // frame diff useless.
        d.actions.sort_by(|a, b| a.id.cmp(&b.id));
        d.grants.sort();
        Ok(d)
    }

    /// A stable string for the texture cache: everything the picture depends on.
    fn key(&self) -> String {
        let acts: String = self
            .actions
            .iter()
            .map(|a| format!("{}={};", a.id, a.label))
            .collect();
        format!("{}|{}|{}|{:?}", self.app, self.title, acts, self.grants)
    }
}

/// Where a window is, as the shell last saw it.
///
/// The shell prepares the header itself and cannot reach the `Space` (its
/// `prepare` signature is the frame path's, and only `main` can hand it a
/// snapshot without widening that path). So `main` calls `note_windows` once per
/// event-loop turn and the placement below is a lookup in that snapshot. The
/// cost is that a band is at most one composed frame behind a move; the gain is
/// that binding lives entirely in this file.
#[derive(Clone, Debug)]
pub struct WindowFrame {
    /// The surface id (`windeco::win_key`). Not drawn: it is what keeps two
    /// windows of one app distinguishable in a log.
    pub id: String,
    /// The toplevel's `app_id` (X11: the class), which is what a descriptor's
    /// `app` names.
    pub app: String,
    pub title: String,
    /// Client geometry, LOGICAL pixels in the Space's global coordinates.
    pub x: i32,
    pub y: i32,
    pub w: i32,
    pub h: i32,
}

/// A loaded header: its descriptor, and the texture the last prepare produced.
pub struct Header {
    pub desc: Descriptor,
    key: String,
    pub(crate) tex: Option<(String, GlesTexture)>,
    pub(crate) rect: Option<Rect>,
    /// Texture size, device pixels: kept so a placement can be recomputed when
    /// the window moves without rasterising the band again.
    w: i32,
    h: i32,
    /// What `render` placed, texture-relative device pixels, so a move can re-log
    /// a complete layout line without re-rasterising.
    placed: Vec<(String, i32, i32, i32)>,
    /// Whether the band was drawn last frame, so disappearing is logged once
    /// instead of every frame, and is not logged before it ever appeared.
    shown: bool,
    /// The line last logged for this header, so a settled layout logs once.
    logged: String,
}

impl Header {
    pub fn new(desc: Descriptor) -> Header {
        let key = desc.key();
        Header {
            desc,
            key,
            tex: None,
            rect: None,
            w: 0,
            h: 0,
            placed: vec![],
            shown: false,
            logged: String::new(),
        }
    }

    /// Rasterise the band. `avail` is the usable output width in LOGICAL units;
    /// the canvas and the returned placements are DEVICE pixels. The band is
    /// never wider than the output, so a long descriptor cannot push chrome off
    /// screen: the title ellipsises and actions that do not fit are dropped.
    fn render(
        &self,
        text: &mut TextSystem,
        scale: f64,
        font: f32,
        avail: f32,
    ) -> (Canvas, Vec<(String, i32, i32, i32)>) {
        let s = scale;
        let dv = |v: f32| (v as f64 * s).round() as i32;
        let dlen = |v: f32| ((v as f64 * s).ceil() as i32).max(1);

        // A descriptor is app-authored text: a title is a claim, so bidi
        // controls and zero-width characters are spelled out rather than
        // allowed to reorder or hide what the header says.
        let mut title = clean(if self.desc.title.is_empty() {
            &self.desc.app
        } else {
            &self.desc.title
        });
        let grant_label = match self.desc.grants.len() {
            0 => "no grants".to_string(),
            1 => "1 grant".to_string(),
            n => format!("{n} grants"),
        };
        let grant_label = clean(&grant_label);

        let tspec = FontSpec::new(font * 1.12).bold();
        let aspec = FontSpec::new(font * 0.95);
        let (tm, am) = (text.metrics(&tspec), text.metrics(&aspec));
        let row = (tm.ascent + tm.descent).max(am.ascent + am.descent);
        let h = row + PAD_Y * 2.0;

        let chip_pad = 7.0;
        let grant_w = text.string_width(&grant_label, &aspec, s);
        let chip_w = grant_w + chip_pad * 2.0;
        let chip_h = am.ascent + am.descent + 8.0;

        // The grants count is the promise, so its width is reserved FIRST: a
        // long title or a pile of actions is what gives way, never the count.
        let title_room = (avail - PAD_X * 2.0 - GAP - chip_w).max(40.0);
        if text.string_width(&title, &tspec, s) > title_room {
            title = ellipsize(text, &title, &tspec, s, title_room);
        }
        let title_w = text.string_width(&title, &tspec, s);
        let acts: Vec<(String, f32)> = self
            .desc
            .actions
            .iter()
            .map(|a| {
                let l = clean(if a.label.is_empty() { &a.id } else { &a.label });
                let w = text.string_width(&l, &aspec, s);
                (l, w)
            })
            .collect();

        let gaps = if acts.is_empty() {
            0.0
        } else {
            GAP * acts.len() as f32
        };
        let natural = PAD_X
            + title_w
            + GAP
            + acts.iter().map(|(_, w)| *w).sum::<f32>()
            + gaps
            + GAP
            + chip_w
            + PAD_X;
        let w = natural.min(avail).max(chip_w + PAD_X * 2.0 + GAP);

        let (pw, ph) = (dlen(w).max(1) as usize, dlen(h).max(1) as usize);
        let mut c = Canvas::new(pw, ph, [0, 0, 0, 0]);
        c.round_rect(0, 0, pw as i32, ph as i32, RADIUS * s as f32, FILL);
        // The rule is drawn INSIDE the rounded rectangle, so the corners stay round.
        c.rect(0, ph.saturating_sub(1), pw, 1, RULE);

        let mut placed = vec![];
        // Each line box is centred in the row; metrics differ between the bold
        // title and the action row, and a baseline offset would show at scale.
        let base = |mh: f32| PAD_Y + (row - mh) / 2.0;
        let tx = PAD_X;
        let ty = base(tm.ascent + tm.descent);
        text.draw(&mut c, tx, ty, &title, &tspec, TITLE, s);
        placed.push((title, dv(tx), dv(ty), dlen(title_w)));

        let mut x = tx + title_w + GAP;
        // The chip's left edge is the hard stop for actions: a label that
        // cannot fit is dropped, not allowed to run under the grants count.
        let limit = w - PAD_X - chip_w - GAP;
        for (label, lw) in &acts {
            if x + *lw > limit {
                break;
            }
            let ay = base(am.ascent + am.descent);
            text.draw(&mut c, x, ay, label, &aspec, ACTION, s);
            placed.push(((*label).to_string(), dv(x), dv(ay), dlen(*lw)));
            x += *lw + GAP;
        }

        let chip_x = w - PAD_X - chip_w;
        let chip_y = (h - chip_h) / 2.0;
        c.round_rect(
            dv(chip_x),
            dv(chip_y),
            dlen(chip_w),
            dlen(chip_h),
            6.0 * s as f32,
            CHIP,
        );
        let gy = chip_y + (chip_h - (am.ascent + am.descent)) / 2.0;
        let gx = chip_x + chip_pad;
        text.draw(&mut c, gx, gy, &grant_label, &aspec, GRANT_TEXT, s);
        placed.push((grant_label, dv(gx), dv(gy), dlen(grant_w)));

        (c, placed)
    }
}

/// Shorten `s` to fit `max` logical pixels, ending in an ellipsis. Binary search
/// is unnecessary: a title is short, and this runs once per descriptor.
fn ellipsize(text: &mut TextSystem, s: &str, spec: &FontSpec, scale: f64, max: f32) -> String {
    if text.string_width(s, spec, scale) <= max {
        return s.to_string();
    }
    let mut out = String::new();
    for ch in s.chars() {
        let mut cand = out.clone();
        cand.push(ch);
        cand.push('\u{2026}');
        if text.string_width(&cand, spec, scale) > max {
            break;
        }
        out.push(ch);
    }
    out.push('\u{2026}');
    out
}

impl Shell {
    /// Load a descriptor for `--surface-header`, or report why not. A descriptor
    /// the header cannot draw is refused: drawing a guessed header would be worse
    /// than drawing none, because it would claim a surface the compositor did not
    /// understand.
    ///
    /// Repeatable: one descriptor per app, because the band is bound to a window
    /// and the descriptor's `app` is what names it. A second descriptor for the
    /// same app replaces the first rather than stacking two bands on one window.
    pub fn set_surface_header(&mut self, path: &Path) -> Result<(), String> {
        let d = Descriptor::load(path)?;
        tracing::info!(
            "surface: header {} ({:?}): {} actions, {} grants",
            d.app,
            d.title,
            d.actions.len(),
            d.grants.len()
        );
        // Replacing is the shape a later descriptor-over-the-bus would have too:
        // the newest statement about an app wins.
        self.clear_surface_header(&d.app);
        self.surface_headers.push(Header::new(d));
        Ok(())
    }

    /// Forget an app's descriptor, so its band goes away even while the window
    /// stays mapped. `--surface-header` is the only loader today; this is the
    /// path a cleared descriptor will take.
    pub fn clear_surface_header(&mut self, app: &str) {
        let before = self.surface_headers.len();
        self.surface_headers.retain(|h| h.desc.app != app);
        if self.surface_headers.len() != before {
            tracing::info!("surface: header cleared for {app}");
        }
    }

    /// A snapshot of every mapped window, for the header to bind to. Called once
    /// per event-loop turn from `main`, after dispatched events and before the
    /// next frame is composed.
    pub fn note_windows(&mut self, space: &Space<Window>) {
        // Most sessions run no descriptors at all; do not walk the Space for them.
        if self.surface_headers.is_empty() {
            self.surface_targets.clear();
            return;
        }
        self.surface_targets.clear();
        for w in space.elements() {
            let Some(id) = win_key(w) else { continue };
            let Some(geo) = space.element_geometry(w) else { continue };
            let (app, title) = window_identity(w);
            self.surface_targets.push(WindowFrame {
                id,
                app,
                title,
                x: geo.loc.x,
                y: geo.loc.y,
                w: geo.size.w,
                h: geo.size.h,
            });
        }
    }

    /// Upload every header's texture when what it depends on changed, and place
    /// it against the window its descriptor names.
    pub(crate) fn prepare_surface_header(
        &mut self,
        r: &mut GlesRenderer,
        size: Size<i32, Physical>,
    ) -> Res<()> {
        if self.surface_headers.is_empty() {
            return Ok(());
        }
        let u = self.usable(size.w, size.h);
        let scale = self.scale;
        let font = self.font.size;
        let output_avail = (u.w as f64 / scale) - 2.0 * MARGIN as f64;
        // The window's "top" for this purpose is the top of its title bar when the
        // compositor draws one: the bar is chrome above the client, and a band
        // level with the client's first pixel would be drawn over by the bar.
        let bar = if self.deco_on() { self.deco_height() } else { 0 };
        let edge = (EDGE_GAP as f64 * scale).round() as i32;
        // `prepare` only has `&mut self`, and the renderer borrows nothing of it:
        // split the fields the loop needs so a header can render into the text
        // system while it is itself borrowed from the list.
        let targets = self.surface_targets.clone();
        let text = &mut self.text;
        for h in self.surface_headers.iter_mut() {
            let target = targets
                .iter()
                .find(|t| !h.desc.app.is_empty() && t.app == h.desc.app);
            let Some(t) = target else {
                if h.shown {
                    tracing::info!(
                        "surface: header hidden app={} (no window)",
                        h.desc.app
                    );
                }
                h.shown = false;
                h.rect = None;
                // So a window that comes back logs its placement again.
                h.logged.clear();
                continue;
            };
            // Never wider than the window it describes (nor than the output): a
            // band wider than its window would float over its neighbour and
            // describe the wrong surface.
            let avail = (t.w as f64).min(output_avail).max(120.0) as f32;
            let key = format!("{}|{}|{}|{}", h.key, scale, font, avail.round() as i32);
            if !h.tex.as_ref().is_some_and(|(k, _)| *k == key) {
                let (c, placed) = h.render(text, scale, font, avail);
                let tex = r.import_memory(
                    &c.rgba,
                    Fourcc::Abgr8888,
                    Size::<i32, BufferCoords>::from((c.w as i32, c.h as i32)),
                    false,
                )?;
                h.w = c.w as i32;
                h.h = c.h as i32;
                h.placed = placed;
                h.tex = Some((key, tex));
            }
            // Left edge on the window's left edge, bottom edge `EDGE_GAP` above the
            // window (or its bar). Clamped to the output so a window pushed against
            // an edge does not hang its band off screen.
            let left = (t.x as f64 * scale).round() as i32;
            let top = ((t.y - bar) as f64 * scale).round() as i32;
            let x = left.clamp(0, (size.w - h.w).max(0));
            let y = (top - h.h - edge).max(0);
            let rect = Rect {
                x,
                y,
                w: h.w,
                h: h.h,
            };
            h.rect = Some(rect);
            h.shown = true;
            // One line per settled layout. Tests read the geometry and the exact
            // strings from it, and `app`/`window` say WHICH surface this band is
            // for, which is the whole point of the binding.
            let line = format!(
                "app={} surface={} scale={scale} panel={},{},{},{} window={},{},{},{} grants={} text={}",
                h.desc.app,
                t.id,
                rect.x,
                rect.y,
                rect.w,
                rect.h,
                t.x,
                t.y,
                t.w,
                t.h,
                h.desc.grants.len(),
                h.placed
                    .iter()
                    .map(|(s, x, y, w)| format!(
                        "{}@{},{},{}",
                        s.replace(' ', "_"),
                        rect.x + x,
                        rect.y + y,
                        w
                    ))
                    .collect::<Vec<_>>()
                    .join("\u{1f}")
            );
            if h.logged != line {
                tracing::info!("surface: header layout {line}");
                h.logged = line;
            }
        }
        Ok(())
    }
}

/// What a window says it is: `(app_id, title)`. Read from the toplevel's data
/// map, the same fact the Deskbar and the title bar show, never anything the
/// compositor inferred. An X11 window has no `app_id`, so its class stands in --
/// which is what a descriptor's `app` would have to name for it either way.
fn window_identity(w: &Window) -> (String, String) {
    if let Some(x) = w.x11_surface() {
        return (x.class(), x.title());
    }
    let Some(t) = w.toplevel() else {
        return (String::new(), String::new());
    };
    with_states(t.wl_surface(), |s| {
        s.data_map
            .get::<XdgToplevelSurfaceData>()
            .and_then(|d| d.lock().ok())
            .map(|d| {
                (
                    d.app_id.clone().unwrap_or_default(),
                    d.title.clone().unwrap_or_default(),
                )
            })
            .unwrap_or_default()
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn load(text: &str) -> Result<Descriptor, String> {
        let d = tempfile::tempdir().unwrap();
        let p = d.path().join("surface.json");
        std::fs::write(&p, text).unwrap();
        Descriptor::load(&p)
    }

    #[test]
    fn parsing_is_also_the_validation_step_and_sorts_into_the_canonical_order() {
        let d = load(
            r#"{"app":"org.aurora.demo","title":"T","actions":[
                {"id":"zoom","label":"Zoom In","effect":"nominal"},
                {"id":"open","label":"Open","effect":"read"}],
                "grants":["net.connect","fs.read"]}"#,
        )
        .unwrap();
        let ids: Vec<&str> = d.actions.iter().map(|a| a.id.as_str()).collect();
        assert_eq!(
            ids,
            ["open", "zoom"],
            "actions are in id order, whatever the producer sent"
        );
        assert_eq!(
            d.grants,
            ["fs.read", "net.connect"],
            "grants are sorted too"
        );
    }

    #[test]
    fn an_unpoliced_effect_or_a_duplicate_id_is_refused_rather_than_drawn() {
        let bad_effect =
            r#"{"app":"a","title":"t","actions":[{"id":"x","label":"X","effect":"nuke"}]}"#;
        assert!(
            load(bad_effect)
                .unwrap_err()
                .contains("not read/write/nominal")
        );
        let dup = r#"{"app":"a","title":"t","actions":[
            {"id":"x","label":"X","effect":"read"},{"id":"x","label":"Y","effect":"read"}]}"#;
        assert!(load(dup).unwrap_err().contains("duplicate action id"));
        let no_app = r#"{"app":"","title":"t","actions":[]}"#;
        assert!(load(no_app).unwrap_err().contains("app is empty"));
    }
}
