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

use super::font::{GLYPHS, GLYPH_W};
use crate::text::{FontSpec, TextSystem};
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
/// Height of one row of body text, logical pixels.
const ROW_H: usize = 18;

pub struct Canvas {
    pub w: usize,
    pub h: usize,
    pub rgba: Vec<u8>,
}

impl Canvas {
    pub fn new(w: usize, h: usize, fill: [u8; 4]) -> Self {
        Canvas { w, h, rgba: fill.iter().copied().cycle().take(w * h * 4).collect() }
    }

    pub fn rect(&mut self, x: usize, y: usize, w: usize, h: usize, c: [u8; 4]) {
        for yy in y..(y + h).min(self.h) {
            for xx in x..(x + w).min(self.w) {
                self.rgba[(yy * self.w + xx) * 4..][..4].copy_from_slice(&c);
            }
        }
    }

    /// Draw `s` (already sanitised) at integer `scale`. Returns the x after it.
    pub fn text(&mut self, x: usize, y: usize, s: &str, scale: usize, c: [u8; 4]) -> usize {
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

/// Make `s` safe to draw without making different strings look the same.
///
/// A request cannot smuggle layout in through text, so every control character
/// and every invisible formatting or bidi control is replaced. It is replaced by
/// a VISIBLE escape naming the code point, not by '?', because the point of this
/// line may be a path and the user's decision rests on telling two paths apart.
///
/// Everything else is kept, Unicode included. This used to reduce to printable
/// ASCII on the grounds that "the font cannot draw more", which was true of the
/// 8x16 bitmap font and has not been true since the text stack became
/// cosmic-text with fontdb fallback (`text.rs`). The cost of keeping it was that
/// every non-ASCII code point became '?', so `~/Музыка/трек.mp3` and
/// `~/Деньги/ключ.mp3` drew as the same line, and a user whose folders are named
/// in their own language could not read the dialog that grants access to them.
/// `to_ascii` is still there for the bitmap fallback, which really cannot draw
/// more.
///
/// Homoglyphs remain possible (`café` against a combining-accent spelling). That
/// is strictly better than the collapse it replaces: distinct names now differ
/// on screen, where before every one of them was identical.
pub fn clean(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        if c.is_control() || is_invisible_format(c) {
            out.push_str(&format!("<U+{:04X}>", c as u32));
        } else {
            out.push(c);
        }
    }
    out
}

/// Format and bidi controls that `char::is_control` does not cover: they are not
/// Cc, they draw as nothing, and several of them reorder what follows. A path is
/// the one string here whose exact spelling is the security decision, so none of
/// them may be invisible.
fn is_invisible_format(c: char) -> bool {
    matches!(c as u32,
        0x00AD                  // soft hyphen
        | 0x061C                // Arabic letter mark
        | 0x180E                // Mongolian vowel separator
        | 0x200B..=0x200F       // zero width space/non-joiner/joiner, LRM, RLM
        | 0x202A..=0x202E       // LRE, RLE, PDF, LRO, RLO
        | 0x2060..=0x2064       // word joiner, invisible operators
        | 0x2066..=0x2069       // LRI, RLI, FSI, PDI
        | 0xFEFF                // zero width no-break space / BOM
        | 0xFFF9..=0xFFFB       // interlinear annotation
    )
}

/// Printable ASCII only. The built-in 8x16 bitmap font has no other glyphs, so
/// this is what `text.rs` reduces to when no system font was found at all.
pub fn to_ascii(s: &str) -> String {
    s.chars().map(|c| if (' '..='~').contains(&c) { c } else { '?' }).collect()
}

/// Which end of an over-long string survives the cut.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub(crate) enum Elide {
    /// Keep the start. The default for prose, where the first words carry it.
    Head,
    /// Keep the end.
    Tail,
    /// Keep both ends and cut the middle. The only honest choice for a PATH: a
    /// tail cut collapses two different files into one line whenever they share
    /// a long enough suffix, and the user is left confirming a grant they cannot
    /// tell apart. `path_grants_that_differ_only_in_their_directory_stay_distinct`
    /// is the demonstration.
    Middle,
}

/// Cut `s` to at most `max_w` logical pixels, marking the cut with "...".
/// Binary search on the kept length: shaping is the cost, and a field can be
/// 2048 characters.
pub(crate) fn fit(t: &mut TextSystem, s: &str, spec: &FontSpec, scale: f64, max_w: f32, elide: Elide) -> String {
    if t.string_width(s, spec, scale) <= max_w {
        return s.to_string();
    }
    let chars: Vec<char> = s.chars().collect();
    let head = |n: usize| chars[..n].iter().collect::<String>();
    let tail = |n: usize| chars[chars.len() - n..].iter().collect::<String>();
    let cut = |keep: usize| -> String {
        match elide {
            Elide::Tail => format!("...{}", tail(keep)),
            Elide::Head => format!("{}...", head(keep)),
            // The front half gets the odd character: the root of a path is what
            // distinguishes it, so when there is one char to spare it goes there.
            Elide::Middle => {
                let h = keep.div_ceil(2);
                format!("{}...{}", head(h), tail(keep - h))
            }
        }
    };
    let (mut lo, mut hi) = (0usize, chars.len());
    while lo < hi {
        let mid = (lo + hi).div_ceil(2);
        if t.string_width(&cut(mid), spec, scale) <= max_w { lo = mid } else { hi = mid - 1 }
    }
    cut(lo)
}

/// Break `s` into lines of at most `max_w` logical pixels, at most `max_lines`
/// of them; what does not fit ends the last line as "...".
fn wrap(t: &mut TextSystem, s: &str, spec: &FontSpec, scale: f64, max_w: f32, max_lines: usize) -> Vec<String> {
    let adv = t.escapements(s, spec, scale);
    let chars: Vec<char> = s.chars().collect();
    let mut lines: Vec<String> = Vec::new();
    let (mut start, mut acc) = (0usize, 0.0f32);
    for (i, a) in adv.iter().enumerate() {
        if acc + a > max_w && i > start {
            if lines.len() + 1 == max_lines {
                // the rest goes on the last line, which `fit` then cuts
                let rest: String = chars[start..].iter().collect();
                lines.push(fit(t, &format!("{rest}xxxxxxxx"), spec, scale, max_w, Elide::Head));
                return lines;
            }
            lines.push(chars[start..i].iter().collect());
            start = i;
            acc = 0.0;
        }
        acc += a;
    }
    lines.push(chars[start..].iter().collect());
    lines
}

/// Rasterise the panel for `p` at `scale`. `secs_left` and `queued` are the
/// only non-request inputs. Layout is in LOGICAL units (what it always was, in
/// pixels, at scale 1); the canvas is `logical * scale` device pixels and the
/// text is shaped and rasterised at that size by the compositor's one text
/// stack (`text.rs`), so the prompt is as sharp as the rest of the chrome.
/// `out_w` is the output width in logical units. Text is still reduced to
/// printable ASCII before it gets here.
pub fn panel(p: &Pending, queued: usize, secs_left: u64, out_w: usize, t: &mut TextSystem, scale: f64) -> Canvas {
    let w = out_w.saturating_sub(48).clamp(320, 800);
    let d = |v: usize| (v as f64 * scale).round() as usize;
    let (text_w, value_x, value_w) = ((w - 40) as f32, 20.0 + 64.0, (w - 104) as f32);
    let (body, status, head, button) = (FontSpec::new(14.0), FontSpec::new(14.0).bold(), FontSpec::new(26.0).bold(), FontSpec::new(24.0).bold());
    let r = &p.req;
    // label, value, colour, elide, bold
    let mut rows: Vec<(String, String, [u8; 4], Elide, bool)> = Vec::new();
    rows.push(("App".into(), r.app.clone(), TEXT, Elide::Head, false));
    let v = &r.verification;
    let verified = v.verdict == Verdict::Verified;
    if v.bus_attested {
        rows.push(("Domain".into(), r.domain.clone(), TEXT, Elide::Head, false));
        rows.push(("".into(), "BUS-ATTESTED: this request came from inside it.".into(), ATTESTED, Elide::Head, true));
        rows.push(("".into(), "busd stamped the sender; verb and grants text are the controller's.".into(), DIM, Elide::Head, false));
        if let Some(m) = &v.mismatch {
            rows.push(("Claimed".into(), format!("{m} by the controller (REJECTED)"), WARN, Elide::Head, false));
        }
    } else if verified {
        rows.push(("Domain".into(), r.domain.clone(), TEXT, Elide::Head, false));
        rows.push(("".into(), format!("VERIFIED: {}", v.detail), VERIFIED, Elide::Head, true));
        rows.push(("".into(), "CONTROLLER'S CLAIM: nothing from inside it confirms this request.".into(), CLAIM, Elide::Head, true));
    } else {
        rows.push(("Domain".into(), format!("CLAIMED \"{}\" (NOT VERIFIED)", r.domain), WARN, Elide::Head, false));
        rows.push(("".into(), format!("busd: {}", v.detail), WARN, Elide::Head, false));
    }
    if !r.domain_identity.is_empty() {
        rows.push(("Ident".into(), r.domain_identity.clone(), DIM, Elide::Tail, false));
    }
    rows.push(("Verb".into(), r.verb.clone(), TEXT, Elide::Head, false));
    rows.push(("Effect".into(), r.effect.to_uppercase(), WARN, Elide::Head, false));
    if r.grants.is_empty() {
        rows.push(("Grants".into(), "(none)".into(), DIM, Elide::Head, false));
    }
    for (i, g) in r.grants.iter().take(4).enumerate() {
        rows.push((if i == 0 { "Grants" } else { "" }.into(), g.clone(), TEXT, Elide::Middle, false));
    }
    if r.grants.len() > 4 {
        rows.push((String::new(), format!("(+{} more grants not shown)", r.grants.len() - 4), WARN, Elide::Head, false));
    }
    for (i, l) in wrap(t, &r.args, &body, scale, value_w, 3).into_iter().enumerate() {
        rows.push((if i == 0 { "Args" } else { "" }.into(), l, TEXT, Elide::Head, false));
    }
    let banner = if verified && v.mismatch.is_none() { 0 } else { BANNER_H + 8 };
    let h = banner + 32 + 8 + 16 + 14 + rows.len() * ROW_H + 14 + 32 + 10 + 16 + 30;
    let mut c = Canvas::new(d(w), d(h), PANEL);
    let (b, ew, eh) = (d(6), d(w), d(h));
    c.rect(0, 0, ew, b, BORDER);
    c.rect(0, eh - b, ew, b, BORDER);
    c.rect(0, 0, b, eh, BORDER);
    c.rect(ew - b, 0, b, eh, BORDER);
    let mut y = 16;
    t.draw(&mut c, 20.0, y as f32, "APPROVAL REQUIRED", &head, BORDER, scale);
    y += 32 + 6;
    let note = fit(t, "Drawn by the compositor itself, not by an app.", &body, scale, text_w, Elide::Head);
    t.draw(&mut c, 20.0, y as f32, &note, &body, DIM, scale);
    y += ROW_H + 14 - 2;
    if !verified || v.mismatch.is_some() {
        let (band, msg) = match (&v.mismatch, v.verdict) {
            (Some(_), _) => (MISMATCH, "DOMAIN MISMATCH: the controller named another domain. Showing the bus's.".to_string()),
            (_, Verdict::Unknown) => (BANNER, "UNVERIFIED DOMAIN: busd could not be asked. The name is a claim.".to_string()),
            _ => (BANNER, "UNVERIFIED DOMAIN: busd does not vouch for it. The name is a claim.".to_string()),
        };
        c.rect(d(14), d(y - 2), d(w - 28), d(BANNER_H), band);
        let msg = fit(t, &msg, &status, scale, text_w, Elide::Head);
        t.draw(&mut c, 20.0, (y + 1) as f32, &msg, &status, [20, 20, 20, 255], scale);
        y += BANNER_H + 8;
    }
    for (label, value, colour, elide, bold) in rows {
        t.draw(&mut c, 20.0, y as f32, &label, &body, DIM, scale);
        let spec = if bold { &status } else { &body };
        let value = fit(t, &value, spec, scale, value_w, elide);
        t.draw(&mut c, value_x, y as f32, &value, spec, colour, scale);
        y += ROW_H;
    }
    y += 14;
    let x = t.draw(&mut c, 20.0, y as f32, "[A] Allow", &button, OK, scale);
    t.draw(&mut c, x + 40.0, y as f32, "[D] / [Esc] Deny", &button, WARN, scale);
    y += 32 + 10;
    let more = if queued > 1 { format!("  ({} more waiting)", queued - 1) } else { String::new() };
    t.draw(&mut c, 20.0, y as f32, &format!("Denies in {secs_left}s if nobody decides.{more}"), &body, DIM, scale);
    c
}

/// Draw the overlay over the already-rendered, already-captured frame.
/// Called after `serve_pending` and never before: a capture reads the
/// framebuffer before this runs, so the overlay is not in it.
pub fn draw_overlay(
    t: &mut Trusted,
    text: &mut TextSystem,
    renderer: &mut GlesRenderer,
    framebuffer: &mut GlesTarget<'_>,
    output: &Output,
    size: Size<i32, Physical>,
) -> Result<bool, Box<dyn std::error::Error>> {
    let Some(head) = t.queue.front() else { return Ok(false) };
    let secs = head.deadline.saturating_duration_since(std::time::Instant::now()).as_secs();
    let scale = output.current_scale().fractional_scale();
    let key = (head.id, secs, t.queue.len(), scale.to_bits() ^ (size.w as u64) << 40);
    if t.panel_key != Some(key) {
        let out_w = (size.w as f64 / scale).round() as usize;
        t.panel = Some(panel(head, key.2, secs, out_w, text, scale));
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


#[cfg(test)]
mod elide_tests {
    use super::*;
    use crate::text::TextSystem;

    /// The widest a grant's value ever gets: `panel` clamps `w` to 800 and gives the
    /// value column `w - 104`.
    const VALUE_W: f32 = (800 - 104) as f32;

    fn grant(mode: &str, path: &str) -> String {
        format!("{mode} {path} (powerbox)")
    }

    #[test]
    fn path_grants_that_differ_only_in_their_directory_stay_distinct() {
        let mut t = TextSystem::new("", "");
        let body = FontSpec::new(14.0);
        // The same tree reached two ways: once in the user's home, once on a mounted
        // volume. Everything that distinguishes them is in the first few characters, and
        // the shared suffix is long enough to fill the column on its own.
        let tail = "Documents/archive/2024/clients/acme/contracts/signed/final/originals/scan-0001-confidential.pdf";
        let home = grant("ro", &format!("/home/arti/{tail}"));
        let usb = grant("ro", &format!("/run/media/arti/UNTRUSTED-USB/{tail}"));
        assert!(t.string_width(&home, &body, 1.0) > VALUE_W, "the fixture must be long enough to be cut");

        let (h, u) = (
            fit(&mut t, &home, &body, 1.0, VALUE_W, Elide::Middle),
            fit(&mut t, &usb, &body, 1.0, VALUE_W, Elide::Middle),
        );
        println!("home: {h}\n usb: {u}");
        assert_ne!(h, u, "two different files render as the same grant line");
        // Both ends survive, which is what makes them distinguishable AND identifiable.
        assert!(h.starts_with("ro /home/arti/"), "{h}");
        assert!(u.starts_with("ro /run/media/arti/"), "{u}");
        for s in [&h, &u] {
            assert!(s.ends_with("confidential.pdf (powerbox)"), "{s}");
            assert!(t.string_width(s, &body, 1.0) <= VALUE_W, "{s} overflows the column");
        }

        // The control: this is exactly what the tail cut used to do, and why it was wrong.
        assert_eq!(
            fit(&mut t, &home, &body, 1.0, VALUE_W, Elide::Tail),
            fit(&mut t, &usb, &body, 1.0, VALUE_W, Elide::Tail),
            "the tail cut is supposed to collapse these; if it no longer does, this test proves nothing"
        );
    }

    #[test]
    fn a_short_path_is_left_exactly_as_it_is() {
        let mut t = TextSystem::new("", "");
        let s = grant("rw", "/home/arti/notes.txt");
        assert_eq!(fit(&mut t, &s, &FontSpec::new(14.0), 1.0, VALUE_W, Elide::Middle), s);
    }

    #[test]
    fn each_mode_keeps_the_end_it_says_it_keeps() {
        let mut t = TextSystem::new("", "");
        let body = FontSpec::new(14.0);
        let s = "A".repeat(400);
        let (h, tl, m) = (
            fit(&mut t, &s, &body, 1.0, 200.0, Elide::Head),
            fit(&mut t, &s, &body, 1.0, 200.0, Elide::Tail),
            fit(&mut t, &s, &body, 1.0, 200.0, Elide::Middle),
        );
        assert!(h.starts_with('A') && h.ends_with("..."), "{h}");
        assert!(tl.starts_with("...") && tl.ends_with('A'), "{tl}");
        assert!(m.starts_with('A') && m.ends_with('A') && m.contains("..."), "{m}");
        for x in [&h, &tl, &m] {
            assert!(t.string_width(x, &body, 1.0) <= 200.0, "{x} overflows");
        }
    }
}

#[cfg(test)]
mod clean_tests {
    use super::*;

    #[test]
    fn two_paths_in_the_users_own_language_do_not_draw_as_one_line() {
        // Both are plausible folders in one home directory. Reduced to ASCII they
        // were the identical string, so the dialog that grants access to a file
        // could not tell them apart.
        let a = clean("ro /home/arti/Музыка/трек.mp3 (powerbox)");
        let b = clean("ro /home/arti/Деньги/ключ.mp3 (powerbox)");
        assert_ne!(a, b, "two different paths produced one line");
        assert!(a.contains("Музыка"), "{a}");
        assert!(b.contains("ключ"), "{b}");
        // The control: this is what they used to be, and why it was wrong.
        assert_eq!(
            to_ascii("ro /home/arti/Музыка/трек.mp3 (powerbox)"),
            to_ascii("ro /home/arti/Деньги/ключ.mp3 (powerbox)"),
            "to_ascii is supposed to collapse these; if it stopped, this test proves nothing"
        );
    }

    #[test]
    fn an_invisible_character_is_drawn_as_something() {
        // A right-to-left override reorders everything after it, so a name can be
        // made to read as a different name. It must not be invisible, and it must
        // not collapse into the same escape as a different control.
        let rlo = clean("a\u{202E}b.pdf");
        let zwsp = clean("a\u{200B}b.pdf");
        assert_eq!(rlo, "a<U+202E>b.pdf");
        assert_eq!(zwsp, "a<U+200B>b.pdf");
        assert_ne!(rlo, zwsp);
        // A newline cannot be used to forge a row.
        assert_eq!(clean("App: x\nEffect: safe"), "App: x<U+000A>Effect: safe");
        assert_eq!(clean("a\tb"), "a<U+0009>b");
    }

    #[test]
    fn ordinary_text_is_untouched() {
        let s = "rw /home/arti/Documents/café résumé.pdf (powerbox)";
        assert_eq!(clean(s), s);
    }

    #[test]
    fn the_bitmap_fallback_still_reduces_to_ascii() {
        assert_eq!(to_ascii("café"), "caf?");
        for c in to_ascii("Музыка\n\t").chars() {
            assert!((' '..='~').contains(&c), "{c:?} is not printable ASCII");
        }
    }
}

#[cfg(test)]
mod font_reach_probe {
    use super::*;
    use crate::text::TextSystem;

    // Keeping Unicode is only an improvement if the text stack can actually draw
    // it. If no glyph is found the user gets tofu, which is at least distinct per
    // code point, but a width equal to the fallback box for every character would
    // mean this change bought less than it looks.
    #[test]
    fn the_text_stack_reaches_beyond_ascii() {
        let mut t = TextSystem::new("", "");
        println!("font: {}", t.description);
        let body = FontSpec::new(14.0);
        for s in ["Documents", "Музыка", "文件", "café"] {
            let w = t.string_width(s, &body, 1.0);
            println!("  {s:?} chars={} width={w:.1}", s.chars().count());
            assert!(w > 0.0, "{s:?} measured zero width");
        }
        // Two different words of the same length must not measure identically,
        // which is what happens when every character falls back to one box glyph.
        // Asserted for Latin and Cyrillic, which the default font set covers.
        // Different LETTERS, not an accented letter against its base: é and e
        // share an advance width in most faces by design, so that pair proves
        // nothing either way.
        let (wa, wb) = (t.string_width("Музыка", &body, 1.0), t.string_width("Деньги", &body, 1.0));
        println!("  Музыка={wa:.1} Деньги={wb:.1}");
        assert_ne!(wa, wb, "every Cyrillic word measures the same: no real glyphs");
        // And an accented name is not silently the width of the '?' it used to be.
        assert_ne!(
            t.string_width("café", &body, 1.0),
            t.string_width(&to_ascii("café"), &body, 1.0),
            "café measures as its ASCII collapse"
        );
        // CJK is asserted in text.rs (cjk_names_draw_as_distinct_glyphs_not_identical_tofu) and in
        // tests/trusted.rs, now that a CJK face is installed.
    }
}
