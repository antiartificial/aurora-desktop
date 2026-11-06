//! The compositor's text stack: system fonts (fontdb), shaping, font fallback
//! and glyph rasterisation (swash), all through cosmic-text, drawn into a
//! `Canvas` at DEVICE pixels.
//!
//! HiDPI model. Callers speak LOGICAL units (a font size of 15 means 15
//! logical pixels, positions are logical) and pass the output `scale` (1.0,
//! 1.45, 2.0, ...). Glyphs are shaped and rasterised at `size * scale` device
//! pixels, so text is sharp at every scale and is never a 1x raster scaled up.
//! Glyph bitmaps are cached by cosmic-text's `SwashCache`, keyed by
//! (font id, glyph id, device size, sub-pixel x/y bin): per face, size and
//! sub-pixel offset. The canvas is premultiplied RGBA (see `Canvas::over`).
//!
//! API shape (meant to answer BFont-style queries): [`TextSystem::metrics`]
//! (ascent / descent / leading), [`TextSystem::string_width`],
//! [`TextSystem::escapements`] (per-character advance), [`TextSystem::draw`].
//! All results are logical units. If no font exists at all the built-in 8x16
//! bitmap font is used and [`TextSystem::description`] says so.

use cosmic_text::{Attrs, Buffer, Color, Family, FontSystem, Metrics, Shaping, SwashCache, Weight, fontdb};

use crate::trusted::{draw::Canvas, font::GLYPH_H};

/// A font request in logical units. Empty `family` means the default the
/// `TextSystem` resolved at start.
#[derive(Clone, Debug, PartialEq)]
pub struct FontSpec {
    pub family: String,
    pub size: f32,
    pub weight: u16,
}

impl FontSpec {
    pub fn new(size: f32) -> Self {
        FontSpec { family: String::new(), size, weight: 400 }
    }
    pub fn bold(mut self) -> Self {
        self.weight = 700;
        self
    }
}

/// Vertical metrics in logical units, as `BFont::GetHeight` reports them.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct FontMetrics {
    pub ascent: f32,
    pub descent: f32,
    pub leading: f32,
}

impl FontMetrics {
    pub fn height(&self) -> f32 {
        self.ascent + self.descent + self.leading
    }
}

const PREFERRED: [&str; 6] = ["Noto Sans", "Roboto", "Inter", "DejaVu Sans", "Adwaita Sans", "Liberation Sans"];

pub struct TextSystem {
    fs: FontSystem,
    sc: SwashCache,
    buf: Buffer,
    default_family: Option<String>,
    bitmap: bool,
    /// Which path ran, for logs and the report.
    pub description: String,
}

impl TextSystem {
    /// Discover system fonts (plus `font_file`, if given), then pick the
    /// default family: `family` if installed, else the first installed of a
    /// short preferred list, else cosmic-text's generic sans-serif.
    pub fn new(family: &str, font_file: &str) -> Self {
        let mut fs = FontSystem::new();
        let mut note = String::new();
        if !font_file.is_empty() {
            match std::fs::read(font_file) {
                Ok(bytes) => {
                    fs.db_mut().load_font_data(bytes);
                    note = format!(" + {font_file}");
                }
                Err(e) => note = format!(" (cannot read {font_file}: {e})"),
            }
        }
        let has = |fs: &FontSystem, name: &str| fs.db().faces().any(|f| f.families.iter().any(|(n, _)| n.eq_ignore_ascii_case(name)));
        let nfaces = fs.db().faces().count();
        let default_family = std::iter::once(family).chain(PREFERRED).filter(|n| !n.is_empty()).find(|n| has(&fs, n)).map(str::to_string);
        let bitmap = nfaces == 0;
        let description = if bitmap {
            "bitmap 8x16 fallback: no font found on this system".to_string()
        } else {
            let file = default_family.as_deref().and_then(|n| {
                fs.db().faces().filter(|f| f.families.iter().any(|(m, _)| m.eq_ignore_ascii_case(n))).min_by_key(|f| (f.weight.0 as i32 - 400).abs() + if f.style == fontdb::Style::Normal { 0 } else { 1000 } + if f.stretch == fontdb::Stretch::Normal { 0 } else { 1000 }).and_then(|f| match &f.source {
                    fontdb::Source::File(p) => Some(p.display().to_string()),
                    _ => None,
                })
            });
            format!(
                "cosmic-text ({nfaces} faces from fontdb), default family {} [{}]{note}",
                default_family.as_deref().unwrap_or("generic sans-serif"),
                file.unwrap_or_else(|| "n/a".into())
            )
        };
        TextSystem { fs, sc: SwashCache::new(), buf: Buffer::new_empty(Metrics::new(14.0, 18.0)), default_family, bitmap, description }
    }

    pub fn is_bitmap(&self) -> bool {
        self.bitmap
    }

    fn shape(&mut self, spec: &FontSpec, s: &str, scale: f64) {
        let size = spec.size * scale as f32;
        self.buf.set_metrics(Metrics::new(size, (size * 1.3).ceil()));
        self.buf.set_size(None, None);
        let fam = if spec.family.is_empty() { self.default_family.as_deref() } else { Some(spec.family.as_str()) };
        let attrs = Attrs::new().family(fam.map_or(Family::SansSerif, Family::Name)).weight(Weight(spec.weight));
        self.buf.set_text(s, &attrs, Shaping::Advanced, None);
        self.buf.shape_until_scroll(&mut self.fs, false);
    }

    /// Ascent, descent and leading of the face `spec` resolves to, scaled to
    /// `spec.size`, in logical units.
    pub fn metrics(&mut self, spec: &FontSpec) -> FontMetrics {
        if self.bitmap {
            let k = spec.size / GLYPH_H as f32;
            return FontMetrics { ascent: 13.0 * k, descent: 3.0 * k, leading: 0.0 };
        }
        self.shape(spec, "Hg", 1.0);
        let id = self.buf.layout_runs().next().and_then(|r| r.glyphs.first().map(|g| (g.font_id, g.font_weight)));
        id.and_then(|(id, w)| self.fs.get_font(id, w))
            .map(|f| {
                let m = f.as_swash().metrics(&[]).scale(spec.size);
                FontMetrics { ascent: m.ascent, descent: m.descent, leading: m.leading }
            })
            .unwrap_or(FontMetrics { ascent: spec.size * 0.95, descent: spec.size * 0.25, leading: 0.0 })
    }

    /// Per-character advances in logical units, in `s`'s character order (a
    /// ligature's advance goes to its first character).
    pub fn escapements(&mut self, s: &str, spec: &FontSpec, scale: f64) -> Vec<f32> {
        if self.bitmap {
            let k = bitmap_k(spec.size, scale) as f32 * 8.0 / scale as f32;
            return s.chars().map(|_| k).collect();
        }
        self.shape(spec, s, scale);
        let mut out = vec![0.0f32; s.chars().count()];
        let idx: Vec<usize> = s.char_indices().map(|(i, _)| i).collect();
        for run in self.buf.layout_runs() {
            for g in run.glyphs {
                if let Ok(ci) = idx.binary_search(&g.start) {
                    out[ci] += g.w / scale as f32;
                }
            }
        }
        out
    }

    /// Advance width of `s` in logical units.
    pub fn string_width(&mut self, s: &str, spec: &FontSpec, scale: f64) -> f32 {
        if self.bitmap {
            return s.chars().count() as f32 * bitmap_k(spec.size, scale) as f32 * 8.0 / scale as f32;
        }
        self.shape(spec, s, scale);
        self.buf.layout_runs().map(|r| r.line_w).fold(0.0, f32::max) / scale as f32
    }

    /// Draw one line. `x`,`y` are the LOGICAL top-left of the line box; canvas
    /// pixels are logical * scale. Returns the logical x after the text.
    /// `colour` is straight RGBA.
    pub fn draw(&mut self, canvas: &mut Canvas, x: f32, y: f32, s: &str, spec: &FontSpec, colour: [u8; 4], scale: f64) -> f32 {
        let s: String = s.chars().map(|c| if c.is_control() { '?' } else { c }).collect();
        let (xd, yd) = ((x as f64 * scale).round() as i32, (y as f64 * scale).round() as i32);
        if self.bitmap {
            let k = bitmap_k(spec.size, scale);
            let end = canvas.text(xd.max(0) as usize, yd.max(0) as usize, &crate::trusted::draw::clean(&s), k, [colour[0], colour[1], colour[2], 255]);
            return end as f32 / scale as f32;
        }
        self.shape(spec, &s, scale);
        let width = self.buf.layout_runs().map(|r| r.line_w).fold(0.0, f32::max);
        let Self { fs, sc, buf, .. } = self;
        buf.draw(fs, sc, Color::rgba(colour[0], colour[1], colour[2], colour[3]), |px, py, w, h, c| {
            canvas.over_rect(xd + px, yd + py, w as i32, h as i32, [c.r(), c.g(), c.b(), c.a()]);
        });
        x + width / scale as f32
    }
}

/// Integer glyph multiplier for the 8x16 fallback (it cannot do fractions).
fn bitmap_k(size: f32, scale: f64) -> usize {
    ((size as f64 * scale) / GLYPH_H as f64).round().max(1.0) as usize
}

impl Canvas {
    /// Composite straight-alpha `c` over the pixel, keeping the canvas
    /// premultiplied. Where the canvas is opaque this is the ordinary "over".
    pub fn over(&mut self, x: i32, y: i32, c: [u8; 4]) {
        if x < 0 || y < 0 || x as usize >= self.w || y as usize >= self.h || c[3] == 0 {
            return;
        }
        let i = (y as usize * self.w + x as usize) * 4;
        let a = c[3] as u32;
        let px = &mut self.rgba[i..i + 4];
        for k in 0..3 {
            px[k] = ((c[k] as u32 * a + px[k] as u32 * (255 - a) + 127) / 255) as u8;
        }
        px[3] = (a + px[3] as u32 * (255 - a) / 255).min(255) as u8;
    }

    pub fn over_rect(&mut self, x: i32, y: i32, w: i32, h: i32, c: [u8; 4]) {
        for yy in y..y + h {
            for xx in x..x + w {
                self.over(xx, yy, c);
            }
        }
    }

    /// Anti-aliased rounded rectangle (device pixels), composited with `over`.
    pub fn round_rect(&mut self, x: i32, y: i32, w: i32, h: i32, r: f32, c: [u8; 4]) {
        let r = r.min(w as f32 / 2.0).min(h as f32 / 2.0);
        for yy in y.max(0)..(y + h).min(self.h as i32) {
            for xx in x.max(0)..(x + w).min(self.w as i32) {
                let (px, py) = (xx as f32 + 0.5 - x as f32 - w as f32 / 2.0, yy as f32 + 0.5 - y as f32 - h as f32 / 2.0);
                let (dx, dy) = (px.abs() - (w as f32 / 2.0 - r), py.abs() - (h as f32 / 2.0 - r));
                let d = dx.max(0.0).hypot(dy.max(0.0)) + dx.max(dy).min(0.0) - r;
                let cov = (0.5 - d).clamp(0.0, 1.0);
                if cov > 0.0 {
                    self.over(xx, yy, [c[0], c[1], c[2], (c[3] as f32 * cov).round() as u8]);
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn alpha(c: &Canvas) -> Vec<u8> {
        c.rgba.chunks(4).map(|p| p[3]).collect()
    }

    fn render(t: &mut TextSystem, s: &str, size: f32, scale: f64) -> Canvas {
        let w = (size as f64 * scale * s.chars().count() as f64 * 1.2).ceil() as usize + 8;
        let h = (size as f64 * scale * 1.8).ceil() as usize + 8;
        let mut c = Canvas::new(w, h, [0, 0, 0, 0]);
        t.draw(&mut c, 2.0 / scale as f32, 2.0 / scale as f32, s, &FontSpec::new(size), [255, 255, 255, 255], scale);
        c
    }

    /// Ink width on the middle row of the glyph: sum of coverage / 255.
    fn stem(c: &Canvas) -> f32 {
        let a = alpha(c);
        let rows: Vec<usize> = (0..c.h).filter(|&y| a[y * c.w..(y + 1) * c.w].iter().any(|&v| v > 0)).collect();
        let mid = rows[rows.len() / 2];
        a[mid * c.w..(mid + 1) * c.w].iter().map(|&v| v as f32 / 255.0).sum()
    }

    #[test]
    fn a_font_is_found_on_this_host_or_the_fallback_says_so() {
        let t = TextSystem::new("", "");
        println!("FONT PATH: {}", t.description);
        assert!(!t.is_bitmap(), "no font found: {}", t.description);
    }

    #[test]
    fn stems_scale_with_the_device_scale_and_text_is_not_an_upscaled_1x_raster() {
        let mut t = TextSystem::new("", "");
        let (s1, s145, s2) = (stem(&render(&mut t, "l", 40.0, 1.0)), stem(&render(&mut t, "l", 40.0, 1.45)), stem(&render(&mut t, "l", 40.0, 2.0)));
        println!("STEM WIDTH of 'l' at 40 logical px: scale 1.0 -> {s1:.2} px, 1.45 -> {s145:.2} px, 2.0 -> {s2:.2} px");
        assert!((s2 / s1 - 2.0).abs() < 0.3, "{s1} {s2}");
        assert!((s145 / s1 - 1.45).abs() < 0.25, "{s1} {s145}");
        assert!(s1 < s145 && s145 < s2);
        // Pixel-doubled 1x text consists only of uniform 2x2 blocks; real 2x text does not.
        let c2 = render(&mut t, "ag", 24.0, 2.0);
        let a = alpha(&c2);
        let (mut edge, mut uniform) = (0, 0);
        for by in 0..c2.h / 2 {
            for bx in 0..c2.w / 2 {
                let at = |dx: usize, dy: usize| a[(2 * by + dy) * c2.w + 2 * bx + dx];
                let v = [at(0, 0), at(1, 0), at(0, 1), at(1, 1)];
                if v.iter().any(|&p| p > 0 && p < 255) {
                    edge += 1;
                    if v.iter().all(|&p| p == v[0]) {
                        uniform += 1;
                    }
                }
            }
        }
        println!("2x raster: {uniform} of {edge} edge 2x2 blocks are uniform (pixel-doubled 1x would give all)");
        assert!(edge > 20 && (uniform as f32) < edge as f32 * 0.5, "{uniform}/{edge}");
    }

    #[test]
    fn metrics_and_widths_answer_font_queries() {
        let mut t = TextSystem::new("", "");
        let spec = FontSpec::new(20.0);
        let m = t.metrics(&spec);
        assert!(m.ascent > 10.0 && m.ascent < 30.0 && m.descent > 1.0 && m.descent < 10.0, "{m:?}");
        let w = t.string_width("Hello", &spec, 1.0);
        let e = t.escapements("Hello", &spec, 1.0);
        println!("METRICS 20px: {m:?}; width(Hello)={w:.2}; escapements={e:?}");
        assert_eq!(e.len(), 5);
        assert!((e.iter().sum::<f32>() - w).abs() < 0.5);
        assert!((w - t.string_width("Hello", &spec, 2.0)).abs() < 1.5, "width is a logical quantity");
        assert!(t.string_width("Hello world", &spec, 1.0) > w);
    }

    #[test]
    fn fallback_draws_glyphs_missing_from_the_primary_face() {
        let mut t = TextSystem::new("", "");
        let c = render(&mut t, "a\u{4e2d}\u{0416}", 24.0, 1.0);
        let n = alpha(&c).iter().filter(|&&v| v > 128).count();
        println!("FALLBACK: {n} solid pixels for 'a', a CJK and a Cyrillic glyph");
        assert!(n > 100);
    }
}
