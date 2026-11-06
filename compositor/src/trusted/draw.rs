//! Rasterising and drawing the approval overlay. Everything here is the
//! compositor's own code: a built-in bitmap font, a CPU canvas, one texture
//! upload, one scrim. No client surface, no client buffer and no client
//! protocol object is involved, and nothing a client sends is interpreted as
//! anything but glyphs.

use smithay::{
    backend::{
        allocator::Fourcc,
        renderer::{Color32F, Frame, ImportMem, Renderer, gles::{GlesRenderer, GlesTarget}},
    },
    output::Output,
    utils::{Buffer as BufferCoords, Physical, Point, Rectangle, Size},
};

use super::font::{GLYPHS, GLYPH_H, GLYPH_W};
use super::{Pending, Trusted, verify::Verdict};

const PANEL: [u8; 4] = [24, 24, 32, 255];
const BORDER: [u8; 4] = [255, 90, 20, 255];
const TEXT: [u8; 4] = [235, 235, 235, 255];
const DIM: [u8; 4] = [150, 150, 160, 255];
const WARN: [u8; 4] = [255, 90, 70, 255];
const OK: [u8; 4] = [90, 220, 120, 255];
/// Solid band behind the unverified-domain warning; no other element uses it.
pub const BANNER: [u8; 4] = [255, 200, 0, 255];
/// Text colour of a verified domain line; no other element uses it.
pub const VERIFIED: [u8; 4] = [80, 200, 255, 255];
/// Band behind a domain mismatch; no other element uses it.
pub const MISMATCH: [u8; 4] = [255, 40, 40, 255];
/// Text colour of a bus-attested domain line; no other element uses it.
pub const ATTESTED: [u8; 4] = [120, 255, 160, 255];
/// Text colour of the note that a domain is only the controller's word; no other element uses it.
pub const CLAIM: [u8; 4] = [235, 160, 255, 255];
const BANNER_H: usize = 22;

pub struct Canvas {
    pub w: usize,
    pub h: usize,
    pub rgba: Vec<u8>,
}

impl Canvas {
    fn new(w: usize, h: usize, fill: [u8; 4]) -> Self {
        Canvas { w, h, rgba: fill.iter().copied().cycle().take(w * h * 4).collect() }
    }

    fn rect(&mut self, x: usize, y: usize, w: usize, h: usize, c: [u8; 4]) {
        for yy in y..(y + h).min(self.h) {
            for xx in x..(x + w).min(self.w) {
                self.rgba[(yy * self.w + xx) * 4..][..4].copy_from_slice(&c);
            }
        }
    }

    /// Draw `s` (already sanitised) at integer `scale`. Returns the x after it.
    fn text(&mut self, x: usize, y: usize, s: &str, scale: usize, c: [u8; 4]) -> usize {
        let mut cx = x;
        for ch in s.chars() {
            let g = &GLYPHS[(ch as usize).clamp(0x20, 0x7e) - 0x20];
            for (row, bits) in g.iter().enumerate() {
                for col in 0..GLYPH_W {
                    if bits & (0x80 >> col) != 0 {
                        self.rect(cx + col * scale, y + row * scale, scale, scale, c);
                    }
                }
            }
            cx += GLYPH_W * scale;
        }
        cx
    }
}

/// Printable ASCII only: anything else, including every control character
/// and every non-ASCII code point, becomes '?'. The font cannot draw more,
/// and a request cannot smuggle layout in through text.
pub fn clean(s: &str) -> String {
    s.chars().map(|c| if (' '..='~').contains(&c) { c } else { '?' }).collect()
}

/// Fit `s` into `cols` columns. Heads keep the start, tails keep the end
/// (for paths, where the end is what matters).
fn fit(s: &str, cols: usize, keep_tail: bool) -> String {
    let n = s.chars().count();
    if n <= cols {
        return s.to_string();
    }
    let keep = cols.saturating_sub(3);
    if keep_tail {
        format!("...{}", s.chars().skip(n - keep).collect::<String>())
    } else {
        format!("{}...", s.chars().take(keep).collect::<String>())
    }
}

fn wrap(s: &str, cols: usize, max_lines: usize) -> Vec<String> {
    let chars: Vec<char> = s.chars().collect();
    let mut out: Vec<String> = chars.chunks(cols.max(1)).map(|c| c.iter().collect()).collect();
    if out.len() > max_lines {
        out.truncate(max_lines);
        let last = out.last_mut().unwrap();
        *last = fit(&format!("{last}xxx"), cols, false);
    }
    if out.is_empty() {
        out.push(String::new());
    }
    out
}

/// Rasterise the panel for `p`. `secs_left` and `queued` are the only
/// non-request inputs.
pub fn panel(p: &Pending, queued: usize, secs_left: u64, out_w: usize) -> Canvas {
    let w = out_w.saturating_sub(48).clamp(320, 800);
    let cols = (w - 40) / GLYPH_W;
    let vcols = cols.saturating_sub(8);
    let r = &p.req;
    let mut rows: Vec<(String, String, [u8; 4], bool)> = Vec::new(); // label, value, colour, keep_tail
    rows.push(("App".into(), r.app.clone(), TEXT, false));
    let v = &r.verification;
    let verified = v.verdict == Verdict::Verified;
    if v.bus_attested {
        rows.push(("Domain".into(), r.domain.clone(), TEXT, false));
        rows.push(("".into(), "BUS-ATTESTED: this request came from inside it.".into(), ATTESTED, false));
        rows.push(("".into(), "busd stamped the sender; verb and grants text are the controller's.".into(), DIM, false));
        if let Some(m) = &v.mismatch {
            rows.push(("Claimed".into(), format!("{m} by the controller (REJECTED)"), WARN, false));
        }
    } else if verified {
        rows.push(("Domain".into(), r.domain.clone(), TEXT, false));
        rows.push(("".into(), format!("VERIFIED: {}", v.detail), VERIFIED, false));
        rows.push(("".into(), "CONTROLLER'S CLAIM: nothing from inside it confirms this request.".into(), CLAIM, false));
    } else {
        rows.push(("Domain".into(), format!("CLAIMED \"{}\" (NOT VERIFIED)", r.domain), WARN, false));
        rows.push(("".into(), format!("busd: {}", v.detail), WARN, false));
    }
    if !r.domain_identity.is_empty() {
        rows.push(("Ident".into(), r.domain_identity.clone(), DIM, true));
    }
    rows.push(("Verb".into(), r.verb.clone(), TEXT, false));
    rows.push(("Effect".into(), r.effect.to_uppercase(), WARN, false));
    if r.grants.is_empty() {
        rows.push(("Grants".into(), "(none)".into(), DIM, false));
    }
    for (i, g) in r.grants.iter().take(4).enumerate() {
        rows.push((if i == 0 { "Grants" } else { "" }.into(), g.clone(), TEXT, true));
    }
    if r.grants.len() > 4 {
        rows.push((String::new(), format!("(+{} more grants not shown)", r.grants.len() - 4), WARN, false));
    }
    for (i, l) in wrap(&r.args, vcols, 3).into_iter().enumerate() {
        rows.push((if i == 0 { "Args" } else { "" }.into(), l, TEXT, false));
    }
    let banner = if verified && v.mismatch.is_none() { 0 } else { BANNER_H + 8 };
    let h = banner + 32 + 8 + 16 + 14 + rows.len() * (GLYPH_H + 2) + 14 + 32 + 10 + 16 + 30;
    let mut c = Canvas::new(w, h, PANEL);
    c.rect(0, 0, w, 6, BORDER);
    c.rect(0, h - 6, w, 6, BORDER);
    c.rect(0, 0, 6, h, BORDER);
    c.rect(w - 6, 0, 6, h, BORDER);
    let mut y = 16;
    c.text(20, y, "APPROVAL REQUIRED", 2, BORDER);
    y += 32 + 6;
    c.text(20, y, &fit("Drawn by the compositor itself, not by an app.", cols, false), 1, DIM);
    y += GLYPH_H + 14;
    if !verified || v.mismatch.is_some() {
        let (band, msg) = match (&v.mismatch, v.verdict) {
            (Some(_), _) => (MISMATCH, "DOMAIN MISMATCH: the controller named another domain. Showing the bus's.".to_string()),
            (_, Verdict::Unknown) => (BANNER, "UNVERIFIED DOMAIN: busd could not be asked. The name is a claim.".to_string()),
            _ => (BANNER, "UNVERIFIED DOMAIN: busd does not vouch for it. The name is a claim.".to_string()),
        };
        c.rect(14, y - 2, w - 28, BANNER_H, band);
        c.text(20, y + 3, &fit(&msg, cols, false), 1, [20, 20, 20, 255]);
        y += BANNER_H + 8;
    }
    for (label, value, colour, tail) in rows {
        c.text(20, y, &label, 1, DIM);
        c.text(20 + 8 * GLYPH_W, y, &fit(&value, vcols, tail), 1, colour);
        y += GLYPH_H + 2;
    }
    y += 14;
    let x = c.text(20, y, "[A] Allow", 2, OK);
    c.text(x + 40, y, "[D] / [Esc] Deny", 2, WARN);
    y += 32 + 10;
    let more = if queued > 1 { format!("  ({} more waiting)", queued - 1) } else { String::new() };
    c.text(20, y, &format!("Denies in {secs_left}s if nobody decides.{more}"), 1, DIM);
    c
}

/// Draw the overlay over the already-rendered, already-captured frame.
/// Called after `serve_pending` and never before: a capture reads the
/// framebuffer before this runs, so the overlay is not in it.
pub fn draw_overlay(
    t: &mut Trusted,
    renderer: &mut GlesRenderer,
    framebuffer: &mut GlesTarget<'_>,
    output: &Output,
    size: Size<i32, Physical>,
) -> Result<bool, Box<dyn std::error::Error>> {
    let Some(head) = t.queue.front() else { return Ok(false) };
    let secs = head.deadline.saturating_duration_since(std::time::Instant::now()).as_secs();
    let key = (head.id, secs, t.queue.len());
    if t.panel_key != Some(key) {
        t.panel = Some(panel(head, key.2, secs, size.w as usize));
        t.panel_key = Some(key);
    }
    let canvas = t.panel.as_ref().unwrap();
    let tex = renderer.import_memory(
        &canvas.rgba,
        Fourcc::Abgr8888,
        Size::<i32, BufferCoords>::from((canvas.w as i32, canvas.h as i32)),
        false,
    )?;
    let pos: Point<i32, Physical> = ((size.w - canvas.w as i32) / 2, (size.h - canvas.h as i32) / 2).into();
    let full = Rectangle::<i32, Physical>::from_size(size);
    let mut frame = renderer.render(framebuffer, size, output.current_transform())?;
    frame.draw_solid(full, &[full], Color32F::new(0.0, 0.0, 0.0, 0.55))?;
    let dst = Rectangle::new(pos, (canvas.w as i32, canvas.h as i32).into());
    let src = Rectangle::<f64, BufferCoords>::from_size((canvas.w as f64, canvas.h as f64).into());
    let local = Rectangle::<i32, Physical>::from_size(dst.size);
    frame.render_texture_from_to(&tex, src, dst, &[local], &[local], smithay::utils::Transform::Normal, 1.0, None, &[])?;
    let sync = frame.finish()?;
    renderer.wait(&sync)?;
    Ok(true)
}
