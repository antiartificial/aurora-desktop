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
//! A descriptor reaches this file two ways: a file named by `--surface-header`
//! (fixtures, and a way to pin one app's surface), and the live relay
//! (`SurfaceRelay`), which asks the controller's registry which surfaces exist.
//! The relay exists because a compositor reads `--surface-header` ONCE at
//! startup, so an app that publishes when it opens got no header at all.
//!
//! The relay has two sources on one poll, because the registry has two kinds of
//! app. `registry.apps@v1` + `registry.describe@v1` describes apps that have a
//! manifest (a verb to describe); `surface.list@v1` is the registry's list of
//! surfaces APPS PUBLISH THEMSELVES, which is how an app with no manifest
//! (Atlas) gets a header at all. A published descriptor wins over a
//! manifest-derived one for the same app id: the publication is the app's own
//! claim about itself, describe is a claim about its manifest. Both paths go
//! through the same validation and land in `Shell::surface_headers`, keyed by
//! `app`.
//!
//! A header belongs to ONE window. A descriptor names its app (`app` is the
//! toplevel's `app_id`), and the band is placed against that window's own top
//! edge, not at the output's corner: chrome that floated in the corner would
//! describe a surface the person is not looking at. When no window of that app
//! is mapped the band is not drawn at all -- there is nothing for it to describe
//! -- and it comes back, at the window's new place, if the app maps again.

use std::{
    path::{Path, PathBuf},
    sync::mpsc,
    time::{Duration, Instant},
};

use serde::Deserialize;
use smithay::{
    backend::{
        allocator::Fourcc,
        renderer::{
            gles::{GlesRenderer, GlesTexture},
            ImportMem,
        },
    },
    desktop::{Space, Window},
    utils::{Buffer as BufferCoords, Physical, Size},
    wayland::{compositor::with_states, shell::xdg::XdgToplevelSurfaceData},
};

use super::{apps::display_text, layout::Rect, Res, Shell};
use crate::{
    text::{FontSpec, TextSystem},
    trusted::draw::{clean, Canvas},
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
/// How far an action's hit region reaches past its ink, logical pixels. The
/// labels are separated by `GAP`, so half of the gap on each side makes the
/// whole row between two labels clickable without ever overlapping a
/// neighbour: a control you have to aim at is a label pretending to be one.
const HIT_PAD: f32 = 5.0;
/// How long an action's answer stays on the band, seconds. A receipt is
/// evidence about ONE click, not a standing claim about the window, so it is
/// retired even if the person does nothing. Without this the band would
/// accumulate history and stop being chrome.
const NOTICE_TTL: Duration = Duration::from_secs(8);
/// Between the title row and the answer beneath it, logical pixels.
const NOTICE_GAP: f32 = 4.0;

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
    /// The app says the shell draws its chrome. Only then is a band drawn: an app that keeps its own
    /// toolbar would otherwise get two headers, which is worse than no header at all.
    #[serde(default)]
    pub shell_chrome: bool,
}

impl Descriptor {
    /// Parse and validate in one step, like the contract's `Unmarshal`, so a
    /// caller cannot forget the second half.
    pub fn load(path: &Path) -> Result<Descriptor, String> {
        let bytes =
            std::fs::read(path).map_err(|e| format!("cannot read {}: {e}", path.display()))?;
        let d: Descriptor =
            serde_json::from_slice(&bytes).map_err(|e| format!("not a surface descriptor: {e}"))?;
        d.validated()
    }

    /// Parse a `registry.describe@v1` reply's `surface` object and validate it
    /// by the SAME rules as a file. A relayed descriptor is untrusted in a way
    /// a `--surface-header` file is not -- it is whatever some app published
    /// -- so it must not be drawable under looser rules than a fixture.
    pub fn from_value(v: &serde_json::Value) -> Result<Descriptor, String> {
        let d: Descriptor = serde_json::from_value(v.clone())
            .map_err(|e| format!("not a surface descriptor: {e}"))?;
        d.validated()
    }

    /// The contract's `Validate`. Separate from `load` so both entry points
    /// share it and neither can drift.
    fn validated(mut self) -> Result<Descriptor, String> {
        if self.app.is_empty() {
            return Err("surface: app is empty".into());
        }
        let mut seen = std::collections::HashSet::with_capacity(self.actions.len());
        for a in &self.actions {
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
        self.actions.sort_by(|a, b| a.id.cmp(&b.id));
        self.grants.sort();
        Ok(self)
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

/// The band's last answer to a click: what the controller did, or why it would
/// not. There is ONE slot, replaced by the next click and retired by
/// `NOTICE_TTL`, because a band that accumulated lines would stop describing a
/// window and start being a log.
#[derive(Clone, Debug)]
pub struct Notice {
    /// Already cleaned and bounded; the band draws it, it never parses it.
    pub text: String,
    /// True only when the controller minted a receipt this header may present
    /// as ordinary chrome. A refusal, a missing bus, and a receipt whose
    /// effect the header must not depict as done all draw in the action
    /// colour and log `receipt=false`.
    pub receipt: bool,
    /// The action id the answer is about, so the log line says which click it
    /// answers rather than merely that something happened.
    pub action: String,
    at: Instant,
}

impl Notice {
    fn new(action: &str, text: String, receipt: bool) -> Notice {
        Notice {
            text,
            receipt,
            action: action.to_string(),
            at: Instant::now(),
        }
    }
}

/// One finished `ui.act@v1`, on its way from the bus thread back to the loop.
pub struct ActDone {
    app: String,
    action: String,
    /// The label the person clicked. The reply names an action id; the band
    /// must say what was asked, not make the person map an id back to a word.
    label: String,
    result: Result<serde_json::Value, String>,
}

/// The band's action requests: a worker thread per click, and the channel its
/// answer lands on.
///
/// The compositor is a single-threaded event loop and a `ui.act` runs the
/// app's verb, which can take as long as the app likes; the same reason the
/// surface relay has its own thread (see `SurfaceRelay`) applies here. The
/// loop only ever calls `take`, which is `try_recv`.
pub struct ActRelay {
    tx: mpsc::Sender<ActDone>,
    rx: mpsc::Receiver<ActDone>,
}

impl ActRelay {
    pub fn new() -> ActRelay {
        let (tx, rx) = mpsc::channel();
        ActRelay { tx, rx }
    }

    /// Ask the controller to run `action` of `app`. Returns only when the
    /// thread is running, never when the bus answered.
    fn ask(&self, sock: PathBuf, app: String, action: String, label: String) -> Result<(), String> {
        let tx = self.tx.clone();
        std::thread::Builder::new()
            .name("header-act".into())
            .spawn(move || {
                // The same call the relay uses, addressed to the service the
                // controller registers (`registry`). It is the CONTROLLER that
                // polices the action: this band never decides that a click is
                // allowed, it only asks.
                let result = crate::trusted::verify::call_to(
                    &sock,
                    "registry",
                    "ui.act@v1",
                    Some(serde_json::json!({ "app": app, "action": action })),
                );
                let _ = tx.send(ActDone {
                    app,
                    action,
                    label,
                    result,
                });
            })
            .map(|_| ())
            .map_err(|e| format!("cannot start a worker thread: {e}"))
    }

    /// A finished answer, if one has landed. Never blocks.
    pub fn take(&self) -> Option<ActDone> {
        self.rx.try_recv().ok()
    }
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
    /// The actions drawn this time, texture-relative device pixels: `(id, x, y,
    /// w, h)`. A label that did not fit is NOT here, because a control that is
    /// not on screen must not be clickable.
    hits: Vec<(String, i32, i32, i32, i32)>,
    /// The last answer drawn on the band, if any.
    notice: Option<Notice>,
    /// Whether the band was drawn last frame, so disappearing is logged once
    /// instead of every frame, and is not logged before it ever appeared.
    shown: bool,
    /// The line last logged for this header, so a settled layout logs once.
    logged: String,
    /// Published over the bus rather than named with `--surface-header`.
    /// Live headers track the registry (`reconcile_surface_headers`); a
    /// fixture is a standing statement and keeps the older hide/return
    /// behaviour instead.
    live: bool,
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
            hits: vec![],
            notice: None,
            shown: false,
            logged: String::new(),
            live: false,
        }
    }

    /// Rasterise the band. `avail` is the usable output width in LOGICAL units;
    /// the canvas and the returned placements are DEVICE pixels. The band is
    /// never wider than the output, so a long descriptor cannot push chrome off
    /// screen: the title ellipsises and actions that do not fit are dropped.
    ///
    /// The third return is the hit map of the actions actually drawn, so a
    /// click and the pixels it lands on are produced by the same pass: a label
    /// drawn but absent from the map, or a map entry with no label, could only
    /// come from a second layout nobody kept in step.
    fn render(
        &self,
        text: &mut TextSystem,
        scale: f64,
        font: f32,
        avail: f32,
    ) -> (
        Canvas,
        Vec<(String, i32, i32, i32)>,
        Vec<(String, i32, i32, i32, i32)>,
    ) {
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
        // The answer sits in a row of its own under the title: the band is the
        // shape of the promise, and a receipt squeezed between two labels would
        // read as one more thing the surface offers.
        let notice = self.notice.as_ref().map(|n| (clean(&n.text), n.receipt));
        let h = row
            + PAD_Y * 2.0
            + if notice.is_some() {
                NOTICE_GAP + row
            } else {
                0.0
            };

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
        // The id travels with the label: the hit map is keyed by what
        // `ui.act@v1` names, not by the word drawn for it.
        let acts: Vec<(String, String, f32)> = self
            .desc
            .actions
            .iter()
            .map(|a| {
                let l = clean(if a.label.is_empty() { &a.id } else { &a.label });
                let w = text.string_width(&l, &aspec, s);
                (a.id.clone(), l, w)
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
            + acts.iter().map(|(_, _, w)| *w).sum::<f32>()
            + gaps
            + GAP
            + chip_w
            + PAD_X;
        // The answer's own width is part of the band's: a receipt that named
        // nothing but "…" would not be evidence of anything. It still cannot
        // push the band past the window, so a long path ellipsises.
        let notice_w = notice
            .as_ref()
            .map(|(t, _)| PAD_X * 2.0 + text.string_width(t, &aspec, s))
            .unwrap_or(0.0);
        let w = natural
            .max(notice_w)
            .min(avail)
            .max(chip_w + PAD_X * 2.0 + GAP);

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
        let mut hits = vec![];
        let ay = base(am.ascent + am.descent);
        for (id, label, lw) in &acts {
            if x + *lw > limit {
                break;
            }
            text.draw(&mut c, x, ay, label, &aspec, ACTION, s);
            placed.push(((*label).to_string(), dv(x), dv(ay), dlen(*lw)));
            // The hit region is the label plus half the gap on each side: the
            // row reads as three controls, not three words.
            hits.push((
                id.clone(),
                dv(x - HIT_PAD),
                dv(PAD_Y - 1.0),
                dlen(*lw + HIT_PAD * 2.0),
                dlen(row + 2.0),
            ));
            x += *lw + GAP;
        }

        // The answer, in the band and in the band's own vocabulary: the
        // receipt blue of the grants chip for a receipt, the ordinary action
        // grey otherwise. No red and no danger mark -- see the module doc.
        if let Some((line, receipt)) = &notice {
            let room = (w - PAD_X * 2.0).max(20.0);
            let mut line = line.clone();
            if text.string_width(&line, &aspec, s) > room {
                line = ellipsize(text, &line, &aspec, s, room);
            }
            let ny = PAD_Y + row + NOTICE_GAP;
            let colour = if *receipt { GRANT_TEXT } else { ACTION };
            text.draw(&mut c, PAD_X, ny, &line, &aspec, colour, s);
            let nw = text.string_width(&line, &aspec, s);
            placed.push((line, dv(PAD_X), dv(ny), dlen(nw)));
        }

        let chip_x = w - PAD_X - chip_w;
        // The chip keeps its place in the title row when the answer adds a row
        // beneath: a grant count that slid down between the two would read as
        // part of the receipt.
        let chip_y = PAD_Y + (row - chip_h) / 2.0;
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

        (c, placed, hits)
    }

    /// Does `dp` (device pixels) land on this band?
    pub fn contains(&self, dp: (f64, f64)) -> bool {
        self.rect.is_some_and(|r| r.contains(dp.0, dp.1))
    }

    /// The action id a point hits, if any.
    ///
    /// The point is converted to texture coordinates rather than the hit map
    /// being moved on every frame: `rect` is where the band is, the map is
    /// what the texture says, and only a move between them could change.
    pub fn action_at(&self, dp: (f64, f64)) -> Option<&str> {
        let r = self.rect?;
        let (x, y) = (dp.0 - r.x as f64, dp.1 - r.y as f64);
        self.hits
            .iter()
            .find(|(_, hx, hy, hw, hh)| {
                let (hx, hy, hw, hh) = (*hx as f64, *hy as f64, *hw as f64, *hh as f64);
                x >= hx && y >= hy && x < hx + hw && y < hy + hh
            })
            .map(|(id, ..)| id.as_str())
    }

    /// The last answer, for the texture key: a receipt appearing, changing or
    /// expiring must repaint the band, and nothing else may.
    fn notice_key(&self) -> String {
        match &self.notice {
            Some(n) => format!("{}|{}", n.receipt, n.text),
            None => String::new(),
        }
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

/// How often the compositor asks the registry which surfaces exist.
///
/// A publish is a startup event, not a per-frame one, and the session bus is
/// request/reply (the shape `registry.apps@v1` already uses), so this is a
/// poll, not a subscription: a second long-lived connection would be a second
/// thing to reconnect after a bus restart. Two seconds is short enough that a
/// demo does not look stuck, long enough that the per-app `describe` calls do
/// not cost anything noticeable.
const RELAY_REFRESH: Duration = Duration::from_secs(2);
/// Bound on an untrusted listing. A registry with hundreds of apps would
/// otherwise turn each poll into hundreds of round trips.
const MAX_SURFACES: usize = 64;

/// The live relay: asks the registry for the surfaces it publishes and hands
/// the answer to the event loop without ever blocking it.
///
/// The compositor is a single-threaded event loop. A bus round trip can take
/// as long as a hung daemon likes, so it runs on its own thread -- the same
/// pattern `apps::Registry::tick` uses for `registry.apps@v1` -- and the loop
/// only ever looks at a channel with `try_recv`.
pub struct SurfaceRelay {
    rx: Option<mpsc::Receiver<Result<Vec<Descriptor>, String>>>,
    last: Option<Instant>,
}

impl SurfaceRelay {
    pub fn new() -> SurfaceRelay {
        SurfaceRelay {
            rx: None,
            last: None,
        }
    }

    /// Start the next ask when one is due. Never blocks: an ask still in
    /// flight leaves `rx` set and this returns at once.
    pub fn tick(&mut self, sock: Option<PathBuf>) {
        if self.rx.is_some() {
            return;
        }
        let Some(sock) = sock else { return };
        if self.last.is_some_and(|t| t.elapsed() < RELAY_REFRESH) {
            return;
        }
        self.last = Some(Instant::now());
        let (tx, rx) = mpsc::channel();
        self.rx = Some(rx);
        let spawned = std::thread::Builder::new()
            .name("surface-relay".into())
            .spawn(move || {
                let _ = tx.send(ask(&sock));
            });
        if spawned.is_err() {
            // No thread means no reply ever arrives; clearing `rx` lets the
            // next tick try again instead of waiting forever on a channel
            // whose sender is gone.
            self.rx = None;
        }
    }

    /// A finished ask, if one has landed.
    pub fn take(&mut self) -> Option<Result<Vec<Descriptor>, String>> {
        let rx = self.rx.as_ref()?;
        match rx.try_recv() {
            Ok(r) => {
                self.rx = None;
                Some(r)
            }
            Err(mpsc::TryRecvError::Empty) => None,
            Err(mpsc::TryRecvError::Disconnected) => {
                self.rx = None;
                None
            }
        }
    }
}

/// One poll: every app the registry lists that has a verb, described, plus every
/// surface the registry says an app published for itself.
///
/// `describe` is per-verb but its reply's `surface` covers the whole window,
/// so any one verb answers for all of them (the same selection the controller's
/// own `surface.List` makes). An app with no verb has no descriptor to fetch
/// and is skipped, not failed: one app's gap must not blank the whole relay.
/// That skip is exactly why `surface.list@v1` is needed: an app with no manifest
/// (Atlas) has nothing to describe, so its only statement about itself is the
/// publication.
fn ask(sock: &Path) -> Result<Vec<Descriptor>, String> {
    let v = crate::trusted::verify::call_to(
        sock,
        "registry",
        "registry.apps@v1",
        Some(serde_json::json!({})),
    )?;
    let apps = v["apps"]
        .as_array()
        .ok_or("registry.apps@v1 reply has no apps list")?;
    let mut out = Vec::new();
    for a in apps.iter().take(MAX_SURFACES) {
        let Some(id) = a["id"].as_str().filter(|i| !i.is_empty()) else {
            continue;
        };
        let Some(verb) = a["verbs"]
            .as_array()
            .and_then(|v| v.iter().find_map(|x| x.as_str().filter(|x| !x.is_empty())))
        else {
            continue;
        };
        let reply = match crate::trusted::verify::call_to(
            sock,
            "registry",
            "registry.describe@v1",
            Some(serde_json::json!({"app": id, "verb": verb})),
        ) {
            Ok(r) => r,
            Err(e) => {
                tracing::warn!("surface: describe {id:?} failed: {e}");
                continue;
            }
        };
        // An older controller answers describe without a surface. There is
        // nothing to draw, and inventing one from the per-verb fields would
        // be a descriptor nobody validated.
        let Some(s) = reply.get("surface").filter(|s| !s.is_null()) else {
            continue;
        };
        match Descriptor::from_value(s) {
            Ok(d) if d.app == id => out.push(d),
            // A descriptor for another app would draw the wrong chrome with
            // the right-looking grants, so a mismatch is refused, not
            // repaired -- the same rule the contract's client applies.
            Ok(d) => tracing::warn!(
                "surface: bus: asked for {id:?} but the descriptor says {:?}; refused",
                d.app
            ),
            Err(e) => tracing::warn!("surface: bus: descriptor for {id:?} refused: {e}"),
        }
    }
    // A failed list must NOT fail the poll: an older controller has no such
    // verb, and warning every two seconds about a feature it never had would
    // bury the describe failures above, which are real. If the bus itself is
    // down, `apps@v1` has already failed the whole ask.
    match published(sock) {
        Ok(ds) => merge_published(ds, &mut out),
        Err(e) => tracing::debug!("surface: surface.list@v1 unavailable: {e}"),
    }
    Ok(out)
}

/// The surfaces apps published for themselves, as `surface.list@v1` returns
/// them. Every entry is validated by the same rules as a describe reply: a
/// publication is app-authored, so it is exactly as untrusted as a relayed
/// descriptor, and an unpoliced effect must not become a band.
fn published(sock: &Path) -> Result<Vec<Descriptor>, String> {
    let v = crate::trusted::verify::call_to(
        sock,
        "registry",
        "surface.list@v1",
        Some(serde_json::json!({})),
    )?;
    let surfaces = v["surfaces"]
        .as_array()
        .ok_or("surface.list@v1 reply has no surfaces list")?;
    let mut out = Vec::with_capacity(surfaces.len().min(MAX_SURFACES));
    for s in surfaces.iter().take(MAX_SURFACES) {
        match Descriptor::from_value(s) {
            Ok(d) => out.push(d),
            // One malformed publication is skipped, not fatal: the other apps
            // in the same list still have headers to draw.
            Err(e) => tracing::warn!("surface: bus: published descriptor refused: {e}"),
        }
    }
    Ok(out)
}

/// Fold the published descriptors into the manifest-derived set.
///
/// A publication wins on the same app id: it is the app's own, current claim
/// about itself, while describe speaks about the manifest, which can lag a
/// running app that has outgrown it. Apps that only appear here are appended;
/// order is not meaningful to the header map (it is keyed by `app`), but keeping
/// the manifest entries in place means a republish does not reshuffle the list.
fn merge_published(published: Vec<Descriptor>, into: &mut Vec<Descriptor>) {
    for d in published {
        match into.iter_mut().find(|x| x.app == d.app) {
            Some(slot) => *slot = d,
            None => into.push(d),
        }
    }
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
    /// stays mapped. Both loaders replace through this, so the newest
    /// statement about an app wins whichever path it came by.
    pub fn clear_surface_header(&mut self, app: &str) {
        let before = self.surface_headers.len();
        self.surface_headers.retain(|h| h.desc.app != app);
        if self.surface_headers.len() != before {
            tracing::info!("surface: header cleared for {app}");
        }
    }

    /// Take a relay snapshot: the descriptors the registry publishes RIGHT NOW.
    ///
    /// The relay polls, so a snapshot is a full statement rather than a delta:
    /// an app that vanished from it has stopped publishing, and its band must
    /// go. The snapshot is only remembered here; `reconcile_surface_headers`
    /// decides which of them have a window to describe.
    pub fn apply_surface_snapshot(&mut self, r: Result<Vec<Descriptor>, String>) {
        match r {
            Ok(d) => {
                tracing::info!(
                    "surface: bus -> {} surface(s): {:?}",
                    d.len(),
                    d.iter().map(|d| d.app.as_str()).collect::<Vec<_>>()
                );
                self.surface_live = d;
            }
            // A failed poll does not blank the live set: a bus restart or a
            // hung controller must not make every header flicker away and
            // back. The last good statement stands until a better one arrives.
            Err(e) => tracing::warn!("surface: bus relay: {e}"),
        }
        self.reconcile_surface_headers();
    }

    /// Bring the header map in line with what the registry publishes and which
    /// windows are mapped.
    ///
    /// A live header exists only while BOTH are true: the app still publishes
    /// a descriptor, and a window of that app is mapped. A band with no window
    /// describes nothing, so keeping the entry would mean the map churns on
    /// every poll (remove, re-add, remove) for an app that is merely closed.
    /// Called once per frame; the work is a scan of two small lists.
    pub fn reconcile_surface_headers(&mut self) {
        // Most sessions publish nothing; do not walk the lists for them.
        if self.surface_live.is_empty() && !self.surface_headers.iter().any(|h| h.live) {
            return;
        }
        // Clone so `clear_surface_header` can borrow the shell mutably while
        // this loop reads the published set.
        let live = self.surface_live.clone();
        for d in &live {
            if !self.surface_targets.iter().any(|t| t.app == d.app) {
                continue;
            }
            // The same descriptor republished every poll must NOT rebuild the
            // texture: compare what the picture depends on, not the JSON.
            if self
                .surface_headers
                .iter()
                .any(|h| h.live && h.desc.app == d.app && h.desc.key() == d.key())
            {
                continue;
            }
            // NOTE: `shell_chrome` is parsed but NOT yet honoured here. Gating the band on it is
            // the obvious move, and it is wrong until the semantics are settled: today a band also
            // carries the grants, which are not opt-out-able, and every describe-derived descriptor
            // defaults the flag to false. Deciding that is a design question, not a patch.
            self.clear_surface_header(&d.app);
            let mut h = Header::new(d.clone());
            h.live = true;
            tracing::info!(
                "surface: header published app={} ({:?}): {} actions, {} grants",
                d.app,
                d.title,
                d.actions.len(),
                d.grants.len()
            );
            self.surface_headers.push(h);
        }
        // Drop what is no longer true. A `--surface-header` fixture is NOT
        // live: it is a standing statement by whoever started the compositor,
        // and its band already hides and returns with the window.
        let published = |app: &str| live.iter().any(|d| d.app == app);
        // Cloned for the same reason: `retain` mutably borrows the header list,
        // so the closure cannot reach back into `self`.
        let targets = self.surface_targets.clone();
        let mut dropped: Vec<String> = vec![];
        self.surface_headers.retain(|h| {
            if !h.live {
                return true;
            }
            let keep = published(&h.desc.app) && targets.iter().any(|t| t.app == h.desc.app);
            if !keep {
                dropped.push(h.desc.app.clone());
            }
            keep
        });
        for app in dropped {
            tracing::info!("surface: header removed app={app} (no window or no longer published)");
        }
    }

    /// A snapshot of every mapped window, for the header to bind to. Called once
    /// per event-loop turn from `main`, after dispatched events and before the
    /// next frame is composed.
    pub fn note_windows(&mut self, space: &Space<Window>) {
        // Most sessions run no descriptors at all; do not walk the Space for
        // them. A descriptor the bus published but that has no header YET
        // still needs the snapshot: its window may be about to map, and
        // `reconcile_surface_headers` can only bind to a window it can see.
        if self.surface_headers.is_empty() && self.surface_live.is_empty() {
            self.surface_targets.clear();
            return;
        }
        self.surface_targets.clear();
        for w in space.elements() {
            let Some(id) = win_key(w) else { continue };
            let Some(geo) = space.element_geometry(w) else {
                continue;
            };
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
        let bar = if self.deco_on() {
            self.deco_height()
        } else {
            0
        };
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
                    tracing::info!("surface: header hidden app={} (no window)", h.desc.app);
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
            // The answer is part of the picture, so it is part of the key: a
            // receipt that arrives or expires must repaint, and an unchanged
            // one must not.
            let key = format!(
                "{}|{}|{}|{}|{}",
                h.key,
                scale,
                font,
                avail.round() as i32,
                h.notice_key()
            );
            if !h.tex.as_ref().is_some_and(|(k, _)| *k == key) {
                let (c, placed, hits) = h.render(text, scale, font, avail);
                let tex = r.import_memory(
                    &c.rgba,
                    Fourcc::Abgr8888,
                    Size::<i32, BufferCoords>::from((c.w as i32, c.h as i32)),
                    false,
                )?;
                h.w = c.w as i32;
                h.h = c.h as i32;
                h.placed = placed;
                h.hits = hits;
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

impl Shell {
    /// A left press at `dp` (device pixels). Returns whether the band took it.
    ///
    /// A band is chrome: the window under it must never see a click on it,
    /// which is why a hit on the band's empty pixels is swallowed too and only
    /// a hit on an action starts anything.
    pub fn header_press(&mut self, dp: (f64, f64)) -> bool {
        let Some(i) = self.surface_headers.iter().position(|h| h.contains(dp)) else {
            return false;
        };
        // A label that was dropped for want of room is not in the hit map, so
        // a click there lands on the band and does nothing, which is exactly
        // what the person sees.
        let Some(id) = self.surface_headers[i].action_at(dp).map(str::to_string) else {
            return true;
        };
        self.start_act(i, &id);
        true
    }

    /// Ask the controller to run action `id` of the header at `i`.
    ///
    /// Every way this can fail to even ask is SAID ON THE BAND: silence would
    /// leave a person clicking a control that does nothing with no way to tell
    /// a slow controller from a missing one.
    fn start_act(&mut self, i: usize, id: &str) {
        let resolved = self.surface_headers.get(i).map(|h| {
            let action = h.desc.actions.iter().find(|a| a.id == id).map(|a| {
                let label = clean(if a.label.is_empty() { &a.id } else { &a.label });
                (a.id.clone(), label)
            });
            (h.desc.app.clone(), action)
        });
        let Some((app, action)) = resolved else {
            return;
        };
        let Some((action, label)) = action else {
            let text = format!("{id}: no action in this surface");
            tracing::warn!("surface: header click {id:?} of {app}: {text}");
            self.set_header_notice(i, Notice::new(id, text, false));
            return;
        };

        let Some(sock) = self.registry.sock.clone() else {
            let text = format!("{label}: no bus, nothing sent");
            tracing::warn!(
                "surface: header action {action:?} of {app}: no --bus-socket, so ui.act@v1 cannot be sent"
            );
            self.set_header_notice(i, Notice::new(&action, text, false));
            return;
        };
        match self
            .header_acts
            .ask(sock, app.clone(), action.clone(), label.clone())
        {
            Ok(()) => {
                // The band says what it is waiting for, so a slow invoke does
                // not look like a dead control. It expires like any answer.
                let text = format!("{label}: asking the controller\u{2026}");
                self.set_header_notice(i, Notice::new(&action, text, false));
            }
            Err(e) => {
                let text = format!("{label}: cannot ask the controller ({e})");
                tracing::warn!("surface: header action {action:?} of {app}: {e}");
                self.set_header_notice(i, Notice::new(&action, text, false));
            }
        }
    }

    /// Put `n` on header `i` and log it. The log line is the band's own
    /// statement, so a test (or a person reading the log) can tell a receipt
    /// from a refusal without decoding pixels.
    fn set_header_notice(&mut self, i: usize, n: Notice) {
        if let Some(h) = self.surface_headers.get_mut(i) {
            tracing::info!(
                "surface: header action app={} id={} receipt={} text={:?}",
                h.desc.app,
                n.action,
                n.receipt,
                n.text
            );
            h.notice = Some(n);
        }
    }

    /// Per-frame: take finished answers, and retire old ones.
    pub fn poll_header_acts(&mut self) {
        while let Some(done) = self.header_acts.take() {
            self.apply_act(done);
        }
        // A receipt is evidence about one click, not a badge. Left up it would
        // become a standing claim about the window, and a stale refusal would
        // look like the current state of the app.
        for h in &mut self.surface_headers {
            if h.notice
                .as_ref()
                .is_some_and(|n| n.at.elapsed() >= NOTICE_TTL)
            {
                h.notice = None;
                tracing::info!("surface: header answer expired app={}", h.desc.app);
            }
        }
    }

    /// Show what the controller answered. A receipt is drawn as one only when
    /// the header can honestly call it that (see `receipt_line`).
    fn apply_act(&mut self, d: ActDone) {
        let Some(i) = self
            .surface_headers
            .iter()
            .position(|h| h.desc.app == d.app)
        else {
            tracing::info!(
                "surface: action {}/{} finished after its band went",
                d.app,
                d.action
            );
            return;
        };
        let (text, receipt) = match &d.result {
            Ok(v) if v["ok"].as_bool() == Some(true) && v["receipt"].is_object() => {
                receipt_line(&d.label, v)
            }
            Ok(v) if v["ok"].as_bool() == Some(true) => {
                // "ok" without a receipt is not evidence of anything: say so
                // rather than invent a success.
                (
                    format!("{}: controller returned no receipt", d.label),
                    false,
                )
            }
            // The controller's own words. Its error payload carries the
            // message; `call_to` renders the envelope, so the message is
            // recovered from it rather than rephrased here.
            Ok(v) => (
                format!(
                    "{}: {}",
                    d.label,
                    v["message"]
                        .as_str()
                        .filter(|m| !m.is_empty())
                        .map(clean)
                        .unwrap_or_else(|| "the controller gave no reason".into())
                ),
                false,
            ),
            Err(e) => (failure_text(&d.label, e), false),
        };
        // A message is untrusted text from another process: one line, bounded.
        let text = display_text(&text, 160);
        self.set_header_notice(
            i,
            Notice {
                text,
                receipt,
                action: d.action,
                at: Instant::now(),
            },
        );
    }
}

/// The band line for a receipt the controller minted.
///
/// The effect is the MANIFEST's (the controller refuses a surface that
/// relabels one), and the header has no danger vocabulary: a destructive
/// action must never be drawn as if it were ordinary chrome that succeeded.
/// So only read/write/nominal are presented as receipts; anything else says
/// exactly what the controller reported and nothing about an outcome.
fn receipt_line(label: &str, v: &serde_json::Value) -> (String, bool) {
    let r = &v["receipt"];
    let effect = r["effect"].as_str().unwrap_or("");
    if !matches!(effect, "read" | "write" | "nominal") {
        let what = if effect.is_empty() {
            "an effect it did not name"
        } else {
            effect
        };
        return (format!("{label}: controller reported {what}"), false);
    }
    let artifact = r["artifact"].as_str().unwrap_or("");
    if artifact.is_empty() {
        // No file to name: the effect is the whole of what the receipt says.
        (format!("{label}: {effect}"), true)
    } else {
        (format!("{label}: {artifact}"), true)
    }
}

/// `call_to` renders a controller refusal as `bus refused <verb>: <payload>`,
/// where the payload is the controller's `{code,message,detail}`. Recovering
/// the message keeps the envelope parser in one place (`verify`) instead of
/// growing a second one here; a payload that does not parse is shown as it
/// arrived, which is still the truth.
fn refusal_message(e: &str) -> Option<String> {
    let rest = e.strip_prefix("bus refused ")?;
    let (_, payload) = rest.split_once(": ")?;
    Some(
        match serde_json::from_str::<serde_json::Value>(payload.trim()) {
            Ok(v) => v["message"]
                .as_str()
                .filter(|m| !m.is_empty())
                .map(clean)
                .unwrap_or_else(|| clean(payload)),
            Err(_) => clean(payload),
        },
    )
}

/// A failed `ui.act`: the controller's refusal if the bus carried one,
/// otherwise the transport error, which is not the same thing as a refusal and
/// must not be described as one.
fn failure_text(label: &str, e: &str) -> String {
    match refusal_message(e) {
        Some(m) => format!("{label} refused: {m}"),
        None => format!("{label}: bus error: {e}"),
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
        assert!(load(bad_effect)
            .unwrap_err()
            .contains("not read/write/nominal"));
        let dup = r#"{"app":"a","title":"t","actions":[
            {"id":"x","label":"X","effect":"read"},{"id":"x","label":"Y","effect":"read"}]}"#;
        assert!(load(dup).unwrap_err().contains("duplicate action id"));
        let no_app = r#"{"app":"","title":"t","actions":[]}"#;
        assert!(load(no_app).unwrap_err().contains("app is empty"));
    }

    #[test]
    fn a_publication_replaces_the_manifest_descriptor_for_the_same_app() {
        let d = |app: &str, title: &str| {
            Descriptor::from_value(&serde_json::json!({
                "app": app,
                "title": title,
                "actions": [{"id": "open", "label": "Open", "effect": "read"}],
            }))
            .unwrap()
        };
        // `a` exists in both sources; `b` only in the manifest, `c` only in the
        // publication. The app's own claim wins for `a`, and neither other app
        // is lost.
        let mut manifest = vec![d("a", "Manifest"), d("b", "Only manifest")];
        merge_published(
            vec![d("a", "Published"), d("c", "Only published")],
            &mut manifest,
        );
        let titles: Vec<(&str, &str)> = manifest
            .iter()
            .map(|d| (d.app.as_str(), d.title.as_str()))
            .collect();
        assert_eq!(
            titles,
            [
                ("a", "Published"),
                ("b", "Only manifest"),
                ("c", "Only published")
            ],
            "a publication wins on its app id and adds the apps only it names"
        );
    }

    #[test]
    fn a_live_header_follows_its_window_and_goes_when_either_does() {
        let mut s = Shell::new();
        let desc = || {
            Descriptor::from_value(&serde_json::json!({
                "app": "org.aurora.demo",
                "title": "T",
                "actions": [{"id": "open", "label": "Open", "effect": "read"}],
            }))
            .unwrap()
        };
        let target = || WindowFrame {
            id: "w1".into(),
            app: "org.aurora.demo".into(),
            title: "T".into(),
            x: 10,
            y: 20,
            w: 300,
            h: 200,
        };

        // Published, but no window yet: there is nothing for a band to
        // describe, so no header is materialised.
        s.surface_live = vec![desc()];
        s.reconcile_surface_headers();
        assert!(s.surface_headers.is_empty(), "no window, no band");

        // The window maps and the band appears.
        s.surface_targets = vec![target()];
        s.reconcile_surface_headers();
        assert_eq!(s.surface_headers.len(), 1);
        assert!(s.surface_headers[0].live);

        // The same descriptor on the next poll keeps the SAME Header: a
        // republish must not throw away a texture that is already right.
        let before = &s.surface_headers[0] as *const Header;
        s.reconcile_surface_headers();
        assert_eq!(s.surface_headers.len(), 1);
        assert_eq!(before, &s.surface_headers[0] as *const Header);

        // The window closes while the app still publishes: the band goes.
        s.surface_targets.clear();
        s.reconcile_surface_headers();
        assert!(s.surface_headers.is_empty(), "window gone, band gone");

        // The app republishes with its window back, then stops publishing:
        // the band goes even though the window is still mapped.
        s.surface_targets = vec![target()];
        s.reconcile_surface_headers();
        assert_eq!(s.surface_headers.len(), 1);
        s.surface_live.clear();
        s.reconcile_surface_headers();
        assert!(s.surface_headers.is_empty(), "app gone, band gone");

        // A `--surface-header` fixture is not live: an empty relay must not
        // clear a standing statement, whose band already hides and returns
        // with its window.
        let mut fixture = Header::new(desc());
        fixture.desc.app = "org.aurora.pinned".into();
        s.surface_headers.push(fixture);
        s.reconcile_surface_headers();
        assert_eq!(
            s.surface_headers.len(),
            1,
            "a fixture survives a poll that publishes nothing"
        );
    }
}
