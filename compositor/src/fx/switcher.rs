//! The window switcher: key handling (Alt-Tab and friends), card layout (grid,
//! Flip 3D-style stack) and drawing live window textures as perspective cards.

use std::collections::HashSet;

use smithay::{
    backend::{
        input::KeyState,
        renderer::{Bind, Color32F, Frame, Renderer, Texture, gles::{GlesRenderer, Uniform, GlesTexture}},
    },
    desktop::{Space, Window},
    utils::IsAlive,
    input::keyboard::{FilterResult, Keycode, keysyms as k},
    utils::{Buffer as BufferCoords, Physical, Rectangle, SERIAL_COUNTER, Size, Transform},
    reexports::wayland_server::protocol::wl_surface::WlSurface,
};

use super::{Fx, Res, blur, draw_elements, make_tex, theme::{Layout, oklab_to_srgb}, wait, window_elements};
use crate::Aurora;

#[derive(Clone, Copy, Debug, PartialEq)]
pub enum Act { Open, Next, Prev, Close, Commit }

#[derive(Default)]
pub struct Switcher {
    pub open: bool,
    pub order: Vec<Window>, // most recently used first
    pub sel: usize,
    swallowed: HashSet<u32>,
    pending: Option<Act>,
}

impl Switcher {
    /// Decide, for one key event, whether the focused client sees it. Alt+Tab
    /// opens and advances; while open every key goes to the switcher. A
    /// release is swallowed iff its press was, so no client sees half a key.
    /// Alt's own release is forwarded (the client saw Alt go down) and commits.
    fn filter(&mut self, code: Keycode, st: KeyState, alt: bool, sym: u32) -> FilterResult<()> {
        let code = code.raw();
        if st == KeyState::Released {
            if self.swallowed.remove(&code) { return FilterResult::Intercept(()); }
            if self.open && matches!(sym, k::KEY_Alt_L | k::KEY_Alt_R) { self.pending = Some(Act::Commit); }
            return FilterResult::Forward;
        }
        let act = match sym {
            k::KEY_Tab if alt || self.open => Some(Act::Next),
            k::KEY_ISO_Left_Tab if alt || self.open => Some(Act::Prev),
            k::KEY_Right | k::KEY_Down if self.open => Some(Act::Next),
            k::KEY_Left | k::KEY_Up if self.open => Some(Act::Prev),
            k::KEY_Escape if self.open => Some(Act::Close),
            k::KEY_Return | k::KEY_KP_Enter if self.open => Some(Act::Commit),
            _ => None,
        };
        let act = match act { Some(Act::Next) if !self.open => Some(Act::Open), a => a };
        if act.is_none() && !self.open { return FilterResult::Forward; }
        if matches!(sym, k::KEY_Alt_L | k::KEY_Alt_R) { return FilterResult::Forward; }
        self.swallowed.insert(code);
        self.pending = act;
        FilterResult::Intercept(())
    }
}

impl Aurora {
    /// Keyboard path when no trusted overlay is up: through the switcher filter.
    pub fn switcher_key(&mut self, code: Keycode, state: KeyState, time: smithay::backend::input::InputTime) {
        let kb = self.seat.get_keyboard().unwrap();
        kb.input::<(), _>(self, code, state, SERIAL_COUNTER.next_serial(), time, |st, mods, h| {
            st.fx.switcher.filter(code, state, mods.alt, h.modified_sym().raw())
        });
        if let Some(a) = self.fx.switcher.pending.take() { self.switch(a); }
    }

    fn switch(&mut self, a: Act) {
        let n = self.space.elements().len();
        let sw = &mut self.fx.switcher;
        match a {
            Act::Open if n > 0 => {
                sw.order = self.space.elements().rev().cloned().collect();
                sw.sel = 1.min(n - 1);
                sw.open = true;
                tracing::info!("switcher: open, cards={n}");
            }
            Act::Next if sw.open => sw.sel = (sw.sel + 1) % sw.order.len().max(1),
            Act::Prev if sw.open => sw.sel = (sw.sel + sw.order.len().max(1) - 1) % sw.order.len().max(1),
            Act::Close if sw.open => { sw.open = false; tracing::info!("switcher: closed"); }
            Act::Commit if sw.open => {
                sw.open = false;
                let w = sw.order.get(sw.sel).cloned();
                tracing::info!("switcher: commit, selected={}", sw.sel);
                if let Some(w) = w.filter(|w| w.alive()) { self.focus_window(&w); }
            }
            _ => {}
        }
    }

    pub fn focus_window(&mut self, w: &Window) {
        self.space.raise_element(w, true);
        let surface: WlSurface = w.toplevel().unwrap().wl_surface().clone();
        self.seat.get_keyboard().unwrap().set_focus(self, Some(surface), SERIAL_COUNTER.next_serial());
        self.space.elements().for_each(|w| { w.toplevel().unwrap().send_pending_configure(); });
    }
}

#[derive(Clone, Copy, Debug)]
pub struct Pose { pub c: [f32; 3], pub theta: f32, pub rank: usize, pub sel: bool, pub shade: f32 }

/// Card poses. World units are texture pixels; the eye is at the origin
/// looking down +z with focal length `f`; a card of size s at depth z
/// appears `s * f / z` pixels wide. Returns (focal, poses in `sizes` order).
pub fn layout(l: Layout, angle_deg: f32, sel: usize, sizes: &[(f32, f32)], (sw, sh): (f32, f32)) -> (f32, Vec<Pose>) {
    let f = sw;
    let n = sizes.len();
    let at = |sx: f32, sy: f32, s: f32| [sx / s, sy / s, f / s]; // screen-space centre (from screen centre) at scale s
    let poses = match l {
        Layout::Grid => {
            let cols = (n as f32).sqrt().ceil().max(1.0) as usize;
            let rows = n.div_ceil(cols);
            let (mx, my) = (sw * 0.08, sh * 0.10);
            let (cw, ch) = ((sw - 2.0 * mx) / cols as f32, (sh - 2.0 * my) / rows as f32);
            sizes.iter().enumerate().map(|(i, &(w, h))| {
                let (r, c) = (i / cols, i % cols);
                let in_row = if r == rows - 1 { n - r * cols } else { cols };
                let x0 = mx + (cols - in_row) as f32 * cw / 2.0;
                let s = (cw * 0.88 / w).min(ch * 0.88 / h).min(1.0);
                Pose { c: at(x0 + (c as f32 + 0.5) * cw - sw / 2.0, my + (r as f32 + 0.5) * ch - sh / 2.0, s), theta: 0.0, rank: i, sel: i == sel, shade: 1.0 }
            }).collect()
        }
        Layout::Flip => sizes.iter().enumerate().map(|(i, &(w, h))| {
            let rank = (i + n - sel) % n;
            let s0 = (sh * 0.68 / h).min(sw * 0.62 / w).min(1.0);
            let z = f / s0 * (1.0 + 0.22 * rank as f32);
            let (sx, sy) = (-0.20 * sw + rank as f32 * 0.095 * sw, 0.08 * sh - rank as f32 * 0.035 * sh);
            Pose { c: [sx * z / f, sy * z / f, z], theta: angle_deg.to_radians(), rank, sel: rank == 0, shade: (1.0 - 0.10 * rank as f32).max(0.45) }
        }).collect(),
    };
    (f, poses)
}

/// Draw the switcher over the finished scene: blurred, dimmed backdrop, then
/// one live card per window, far to near.
pub fn draw(fx: &mut Fx, r: &mut GlesRenderer, size: Size<i32, Physical>, space: &Space<Window>) -> Res<()> {
    fx.switcher.order.retain(|w| w.alive());
    if fx.switcher.order.is_empty() { fx.switcher.open = false; return Ok(()); }
    fx.switcher.sel = fx.switcher.sel.min(fx.switcher.order.len() - 1);
    let (theme, progs) = (fx.theme.clone(), fx.progs.clone());
    let [Some(down), Some(up), _, Some(card)] = &progs else { return Err("shaders missing".into()) };
    let full = Rectangle::<i32, Physical>::from_size(size);
    let s = fx.scratch.as_mut().unwrap();
    blur::run(r, s, down, up, &theme.glass, size)?;
    let order = fx.switcher.order.clone();
    let mut sizes = vec![];
    for (i, w) in order.iter().enumerate() {
        let g = w.geometry().size;
        sizes.push((g.w.max(1), g.h.max(1)));
        if fx.cards.len() <= i { fx.cards.push(make_tex(r, 1, 1)?); }
        let ts = fx.cards[i].size();
        if (ts.w, ts.h) != (g.w.max(1), g.h.max(1)) { fx.cards[i] = make_tex(r, g.w, g.h)?; }
        let els = window_elements(r, w, smithay::utils::Point::<i32, smithay::utils::Logical>::from((0, 0)) - w.geometry().loc);
        let csz: Size<i32, Physical> = (g.w.max(1), g.h.max(1)).into();
        let mut t = r.bind(&mut fx.cards[i])?;
        let mut f = r.render(&mut t, csz, Transform::Normal)?;
        f.clear(Color32F::new(0.0, 0.0, 0.0, 0.0), &[Rectangle::from_size(csz)])?;
        draw_elements(&mut f, &els)?;
        let sy = f.finish()?; wait(r, sy)?;
    }
    let fsizes: Vec<(f32, f32)> = sizes.iter().map(|&(w, h)| (w as f32, h as f32)).collect();
    let (focal, poses) = layout(theme.switcher.layout, theme.switcher.flip_angle_deg, fx.switcher.sel, &fsizes, (size.w as f32, size.h as f32));
    let mut idx: Vec<usize> = (0..poses.len()).collect();
    idx.sort_by_key(|&i| std::cmp::Reverse(poses[i].rank));
    let accent = oklab_to_srgb(theme.switcher.accent);
    let s = fx.scratch.as_mut().unwrap();
    let mut t = r.bind(&mut s.scene)?;
    let mut f = r.render(&mut t, size, Transform::Normal)?;
    let fsrc = Rectangle::<f64, BufferCoords>::from_size((size.w as f64, size.h as f64).into());
    f.render_texture_from_to(&s.up[0], fsrc, full, &[full], &[], Transform::Normal, 1.0, None, &[])?;
    f.draw_solid(full, &[full], Color32F::new(0.0, 0.0, 0.0, theme.switcher.backdrop_dim))?;
    for i in idx {
        let (p, (w, h)) = (poses[i], sizes[i]);
        let tex: &GlesTexture = &fx.cards[i];
        let src = Rectangle::<f64, BufferCoords>::from_size((w as f64, h as f64).into());
        let u = [
            Uniform::new("screen", (size.w as f32, size.h as f32)),
            Uniform::new("pose", [p.c[0], p.c[1], p.c[2], p.theta]),
            Uniform::new("csize", (w as f32, h as f32)),
            Uniform::new("focal", focal),
            Uniform::new("shade", p.shade),
            Uniform::new("sel", if p.sel { 1.0f32 } else { 0.0 }),
            Uniform::new("accent", accent),
        ];
        f.render_texture_from_to(tex, src, full, &[full], &[], Transform::Normal, 1.0, Some(card), &u)?;
    }
    let _ = space;
    let sy = f.finish()?;
    wait(r, sy)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn layout_has_one_pose_per_window_and_unique_ranks() {
        for n in 1..=9usize {
            for l in [Layout::Grid, Layout::Flip] {
                let sizes = vec![(600.0, 400.0); n];
                let (_, p) = layout(l, 40.0, n / 2, &sizes, (1280.0, 800.0));
                assert_eq!(p.len(), n);
                let mut r: Vec<_> = p.iter().map(|p| p.rank).collect();
                r.sort();
                assert_eq!(r, (0..n).collect::<Vec<_>>());
                assert_eq!(p.iter().filter(|p| p.sel).count(), 1);
            }
        }
    }
    #[test]
    fn grid_cards_stay_on_screen_and_do_not_overlap() {
        let sizes = vec![(900.0, 600.0); 5];
        let (f, p) = layout(Layout::Grid, 0.0, 0, &sizes, (1280.0, 800.0));
        let rects: Vec<[f32; 4]> = p.iter().map(|p| { let s = f / p.c[2]; let (w, h) = (900.0 * s, 600.0 * s); let (cx, cy) = (p.c[0] * s, p.c[1] * s); [cx - w / 2.0, cy - h / 2.0, cx + w / 2.0, cy + h / 2.0] }).collect();
        for (i, a) in rects.iter().enumerate() {
            assert!(a[0] >= -640.0 && a[2] <= 640.0 && a[1] >= -400.0 && a[3] <= 400.0, "{a:?}");
            for b in &rects[i + 1..] { assert!(a[2] <= b[0] + 0.5 || b[2] <= a[0] + 0.5 || a[3] <= b[1] + 0.5 || b[3] <= a[1] + 0.5, "{a:?} {b:?}"); }
        }
    }
}
