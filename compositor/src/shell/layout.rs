//! Dock geometry, in DEVICE pixels, as pure functions: no GL, no fonts. The
//! caller converts logical theme sizes with the output scale and measures the
//! labels; this decides where everything goes and which strip of the output is
//! reserved (no maximised window may overlap it).

use crate::fx::theme::DockEdge;

#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub struct Rect {
    pub x: i32,
    pub y: i32,
    pub w: i32,
    pub h: i32,
}

impl Rect {
    pub fn contains(&self, px: f64, py: f64) -> bool {
        px >= self.x as f64 && py >= self.y as f64 && px < (self.x + self.w) as f64 && py < (self.y + self.h) as f64
    }
}

/// All values in device pixels.
#[derive(Clone, Copy, Debug)]
pub struct Params {
    pub edge: DockEdge,
    pub item_h: i32,
    /// Empty margin between the output edge and the panel (and beside it).
    pub gap: i32,
    pub pad: i32,
    pub spacing: i32,
}

#[derive(Clone, Debug, Default)]
pub struct Layout {
    /// The strip of the output the dock owns, margins included.
    pub reserved: Rect,
    /// The visible glass panel.
    pub panel: Rect,
    /// One pill per item that fits, in order; absolute output coordinates.
    pub items: Vec<Rect>,
}

impl Layout {
    pub fn item_at(&self, px: f64, py: f64) -> Option<usize> {
        self.items.iter().position(|r| r.contains(px, py))
    }
}

/// Icon tile edge for an item of height `item_h`, and the inner margin around it.
pub fn tile(item_h: i32) -> (i32, i32) {
    let t = (item_h as f32 * 0.72).round() as i32;
    (t, (item_h - t) / 2)
}

/// Width of a pill whose label is `label_w` wide.
pub fn pill_width(item_h: i32, label_w: i32) -> i32 {
    let (t, m) = tile(item_h);
    m + t + m + label_w + 2 * m
}

pub fn compute(out_w: i32, out_h: i32, p: &Params, label_widths: &[i32]) -> Layout {
    let horizontal = matches!(p.edge, DockEdge::Bottom | DockEdge::Top);
    let widths: Vec<i32> = label_widths.iter().map(|&l| pill_width(p.item_h, l)).collect();
    let n = widths.len() as i32;
    let (panel, items) = if horizontal {
        let avail = (out_w - 2 * p.gap).max(0);
        let mut items = Vec::new();
        let mut x = 0;
        for w in &widths {
            if 2 * p.pad + x + w > avail {
                break;
            }
            items.push((x, *w));
            x += w + p.spacing;
        }
        let used = items.last().map_or(0, |(x, w)| x + w);
        let pw = (2 * p.pad + used).min(avail);
        let ph = p.item_h + 2 * p.pad;
        let (px, py) = ((out_w - pw) / 2, if p.edge == DockEdge::Bottom { out_h - p.gap - ph } else { p.gap });
        let rects = items.iter().map(|&(x, w)| Rect { x: px + p.pad + x, y: py + p.pad, w, h: p.item_h }).collect();
        (Rect { x: px, y: py, w: pw, h: ph }, rects)
    } else {
        let w = widths.iter().copied().max().unwrap_or(0);
        let avail = (out_h - 2 * p.gap).max(0);
        let fit = ((avail - 2 * p.pad + p.spacing) / (p.item_h + p.spacing).max(1)).clamp(0, n);
        let ph = (2 * p.pad + fit * p.item_h + (fit - 1).max(0) * p.spacing).min(avail);
        let pw = 2 * p.pad + w;
        let (px, py) = (if p.edge == DockEdge::Left { p.gap } else { out_w - p.gap - pw }, (out_h - ph) / 2);
        let rects = (0..fit).map(|i| Rect { x: px + p.pad, y: py + p.pad + i * (p.item_h + p.spacing), w, h: p.item_h }).collect();
        (Rect { x: px, y: py, w: pw, h: ph }, rects)
    };
    let reserved = match p.edge {
        DockEdge::Bottom => Rect { x: 0, y: out_h - (panel.h + 2 * p.gap), w: out_w, h: panel.h + 2 * p.gap },
        DockEdge::Top => Rect { x: 0, y: 0, w: out_w, h: panel.h + 2 * p.gap },
        DockEdge::Left => Rect { x: 0, y: 0, w: panel.w + 2 * p.gap, h: out_h },
        DockEdge::Right => Rect { x: out_w - (panel.w + 2 * p.gap), y: 0, w: panel.w + 2 * p.gap, h: out_h },
    };
    Layout { reserved, panel, items }
}

/// The part of the output windows may use: the output minus the reserved strip.
pub fn usable(out_w: i32, out_h: i32, edge: DockEdge, reserved: Rect) -> Rect {
    match edge {
        DockEdge::Bottom => Rect { x: 0, y: 0, w: out_w, h: reserved.y },
        DockEdge::Top => Rect { x: 0, y: reserved.h, w: out_w, h: out_h - reserved.h },
        DockEdge::Left => Rect { x: reserved.w, y: 0, w: out_w - reserved.w, h: out_h },
        DockEdge::Right => Rect { x: 0, y: 0, w: reserved.x, h: out_h },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn params(edge: DockEdge) -> Params {
        Params { edge, item_h: 40, gap: 10, pad: 8, spacing: 6 }
    }
    const LABELS: [i32; 3] = [60, 90, 40];

    fn inside(outer: Rect, inner: Rect) -> bool {
        inner.x >= outer.x && inner.y >= outer.y && inner.x + inner.w <= outer.x + outer.w && inner.y + inner.h <= outer.y + outer.h
    }

    #[test]
    fn bottom_dock_reserves_a_full_width_strip_and_centres_the_panel() {
        let l = compute(1280, 800, &params(DockEdge::Bottom), &LABELS);
        assert_eq!(l.reserved, Rect { x: 0, y: 800 - (56 + 20), w: 1280, h: 76 });
        assert!(inside(l.reserved, l.panel));
        assert_eq!(l.panel.y + l.panel.h + 10, 800, "gap below the panel");
        assert!((l.panel.x - (1280 - l.panel.x - l.panel.w)).abs() <= 1, "centred");
        assert_eq!(l.items.len(), 3);
        for r in &l.items {
            assert!(inside(l.panel, *r));
        }
        assert!(l.items.windows(2).all(|w| w[0].x + w[0].w + 6 == w[1].x), "items are spacing apart and do not overlap");
        let u = usable(1280, 800, DockEdge::Bottom, l.reserved);
        assert_eq!(u, Rect { x: 0, y: 0, w: 1280, h: 724 });
    }

    #[test]
    fn every_edge_leaves_a_usable_area_that_does_not_touch_the_reserved_strip() {
        for edge in [DockEdge::Bottom, DockEdge::Top, DockEdge::Left, DockEdge::Right] {
            let l = compute(1280, 800, &params(edge), &LABELS);
            let u = usable(1280, 800, edge, l.reserved);
            let disjoint = u.x + u.w <= l.reserved.x || l.reserved.x + l.reserved.w <= u.x || u.y + u.h <= l.reserved.y || l.reserved.y + l.reserved.h <= u.y;
            assert!(disjoint, "{edge:?}: usable {u:?} overlaps reserved {:?}", l.reserved);
            assert_eq!(u.w * u.h + l.reserved.w * l.reserved.h, 1280 * 800, "{edge:?}: usable plus reserved is the whole output");
            assert!(inside(l.reserved, l.panel) && l.items.iter().all(|r| inside(l.panel, *r)), "{edge:?}");
            assert_eq!(l.items.len(), 3, "{edge:?}");
        }
    }

    #[test]
    fn hit_testing_finds_the_item_under_a_point() {
        let l = compute(1280, 800, &params(DockEdge::Bottom), &LABELS);
        for (i, r) in l.items.iter().enumerate() {
            assert_eq!(l.item_at((r.x + r.w / 2) as f64, (r.y + r.h / 2) as f64), Some(i));
        }
        assert_eq!(l.item_at(5.0, 5.0), None);
        let gap = l.items[0].x + l.items[0].w + 3;
        assert_eq!(l.item_at(gap as f64, (l.items[0].y + 5) as f64), None, "between pills");
    }

    #[test]
    fn items_that_do_not_fit_are_dropped_and_the_panel_stays_on_the_output() {
        let l = compute(300, 800, &params(DockEdge::Bottom), &[100, 100, 100]);
        assert!(l.items.len() < 3);
        assert!(l.panel.x >= 10 && l.panel.x + l.panel.w <= 290);
        let l = compute(1280, 200, &params(DockEdge::Left), &[50; 10]);
        assert!(l.items.len() < 10 && l.panel.y >= 0 && l.panel.y + l.panel.h <= 200);
    }

    #[test]
    fn device_pixel_geometry_scales_with_the_output_scale() {
        let at = |s: f32| Params { edge: DockEdge::Bottom, item_h: (36.0 * s).round() as i32, gap: (10.0 * s).round() as i32, pad: (8.0 * s).round() as i32, spacing: (6.0 * s).round() as i32 };
        let (a, b) = (compute(1280, 800, &at(1.0), &[60]), compute(2560, 1600, &at(2.0), &[120]));
        assert_eq!(b.reserved.h, 2 * a.reserved.h);
    }
}
