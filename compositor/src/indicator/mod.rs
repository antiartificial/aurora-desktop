//! The capability-in-use indicator: a dot (and, on hover or click, a small
//! inspector) that the compositor draws while a domain HOLDS a privacy-relevant
//! capability. Decision 0016 says what it shows, what it does not, and why.
//!
//! It lives in the trusted layer, beside the approval overlay and for the same
//! reasons. It is not a surface and has no protocol object; it is drawn after
//! `serve_pending` has filled every capture, so no capture can contain it; and it
//! is drawn after everything else, so no client window, fullscreen or not, can
//! cover it. The pointer over it belongs to the compositor: no client sees it.
//!
//! What it shows comes from `model` (grant lifecycle read off the bus), fed by
//! `bus` (one thread, one connection). The theme's `[indicator] classes` decides
//! which held capabilities count.

pub mod bus;
pub mod draw;
pub mod model;

use std::path::PathBuf;

use crate::{
    fx::theme,
    shell::{layout::Rect, motion::Appear},
    trusted::draw::Canvas,
};
use model::Held;

#[derive(Default)]
pub struct Indicator {
    /// Everything the bus says is held (not yet filtered by the theme's class list).
    pub held: Vec<Held>,
    /// False while busd is unreachable: what is shown is the last thing known.
    pub up: bool,
    /// The bus thread was started (a `--bus-socket` was given).
    pub watching: bool,
    pub(crate) appear: Option<Appear>,
    pub(crate) panel_appear: Option<Appear>,
    /// The pointer is over the pill or the panel.
    pub hover: bool,
    /// A click pinned the panel open.
    pub pinned: bool,
    /// Where the pill and the open panel (if any) are, device pixels, as last drawn.
    pub pill: Option<Rect>,
    pub panel: Option<Rect>,
    pub(crate) cache: Option<(String, Canvas, Option<Canvas>)>,
    pub(crate) logged: Option<String>,
    /// `--test-input dump PATH`: write the compositor's own final frame, indicator included, after the next draw.
    pub dump: Option<PathBuf>,
}

impl Indicator {
    pub fn on_snapshot(&mut self, s: bus::Snapshot) {
        tracing::info!("indicator: bus snapshot held={} up={} {}", s.held.len(), s.up, s.held.iter().map(|h| format!("{}@{}#{}", h.resource, h.name, h.id)).collect::<Vec<_>>().join(","));
        self.held = s.held;
        self.up = s.up;
    }

    /// The held capabilities the theme says count.
    pub fn visible<'a>(&'a self, cfg: &theme::Indicator) -> Vec<&'a Held> {
        if !cfg.enabled {
            return vec![];
        }
        self.held.iter().filter(|h| cfg.classes.iter().any(|c| covers(c, &h.resource))).collect()
    }

    /// Whether a point (device pixels) is on the indicator, which then owns the pointer.
    pub fn contains(&self, x: f64, y: f64) -> bool {
        match (self.pill, self.panel) {
            (Some(p), None) => p.contains(x, y),
            (Some(p), Some(q)) => {
                // the pill, the panel and the gap between them: moving from one to the other must not close it
                let (x0, y0) = (p.x.min(q.x), p.y.min(q.y));
                let (x1, y1) = ((p.x + p.w).max(q.x + q.w), (p.y + p.h).max(q.y + q.h));
                Rect { x: x0, y: y0, w: x1 - x0, h: y1 - y0 }.contains(x, y)
            }
            _ => false,
        }
    }
}

impl crate::Aurora {
    /// The pointer moved to `dp` (device pixels). True when it is on the indicator.
    pub fn indicator_hover(&mut self, dp: (f64, f64)) -> bool {
        let on = self.indicator.contains(dp.0, dp.1);
        self.indicator.hover = on;
        on
    }

    /// A button press at `dp`. Returns true when the indicator took it (no client sees it, nor its release).
    pub fn indicator_press(&mut self, dp: (f64, f64), left: bool) -> bool {
        if !self.indicator.contains(dp.0, dp.1) {
            return false;
        }
        if left {
            self.indicator.pinned = !self.indicator.pinned;
            tracing::info!("indicator: panel {}", if self.indicator.pinned { "pinned open" } else { "unpinned" });
        }
        true
    }
}

/// Does the theme's class name `c` cover the held resource `r`?
///
/// Exact match, or `c` followed by a ':' separator. The second case exists
/// because not every capability's resource is just its class: the
/// accessibility grant (decision 0017) is minted as `ui:read:<domain
/// identity>`, so a theme listing `ui:read` would match nothing and the dot
/// would silently stay dark for a capability that is genuinely held.
///
/// The ':' is required rather than a bare `starts_with`, which would let
/// `device:gpu` light up for a `device:gpuzzz` nobody listed. The separator is
/// what makes this a match on the class path and not on the spelling.
fn covers(c: &str, r: &str) -> bool {
    r == c || (r.len() > c.len() && r.starts_with(c) && r.as_bytes()[c.len()] == b':')
}

#[cfg(test)]
mod covers_tests {
    use super::covers;

    #[test]
    fn a_class_covers_its_own_sub_resources_and_nothing_else() {
        // the case this exists for: a grant whose resource carries the holder
        assert!(covers("ui:read", "ui:read:domain:a11y-beta-1:abcd"));
        assert!(covers("device:wayland", "device:wayland"));
        // and the trap a bare starts_with would fall into
        assert!(!covers("device:gpu", "device:gpuzzz"));
        assert!(!covers("ui:read", "ui:readall"));
        // a longer theme entry never matches a shorter resource
        assert!(!covers("device:wayland", "device"));
        assert!(!covers("ui:read:other", "ui:read"));
        // an unrelated class is not covered
        assert!(!covers("device:gpu", "device:wayland"));
        // an empty theme entry must not match everything
        assert!(!covers("", "device:gpu"));
    }
}
