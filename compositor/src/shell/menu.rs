//! The menu primitive: a glass popup of items, drawn by the compositor.
//!
//! A `Menu` is data (entries) plus geometry in LOGICAL units; it knows nothing
//! about GL. `Menu::layout` measures labels with `text.rs`, `Menu::draw`
//! rasterises into a `Canvas` at device pixels, and the shell turns that canvas
//! into a glass panel the same way it does for the dock. Input is `hit`
//! (pointer), `key` (keyboard) and `activate`; each yields an `Action`, and the
//! owner runs it. Entries: items (enabled or disabled, optionally checked and
//! annotated), headers and separators.

use super::motion::Appear;
use crate::{
    fx::edit::Edit,
    text::{FontSpec, TextSystem},
    trusted::draw::Canvas,
};

/// What choosing an item does. Menu and settings panel share it; `Aurora::run_action` runs it.
#[derive(Clone, Debug, PartialEq)]
pub enum Action {
    /// Write these settings to the theme file.
    Edit(Vec<Edit>),
    OpenTerminal,
    /// Start Atlas, the file explorer built on aurora::ui.
    OpenAtlas,
    /// Start the launcher's picker (`aurora open --pick`).
    OpenLauncher,
    OpenSettings,
    WindowSwitcher,
    CloseSettings,
    /// A dock click chose a verb: run `verb` of app `app` (names exactly as the manifest has them).
    RunVerb { app: String, verb: String },
    /// Bring these windows (surface ids, bottom to top) to the front and focus the topmost; a window
    /// hidden by Show Desktop is brought back first.
    RaiseWindows(Vec<String>),
    /// Ask these windows to close (`xdg_toplevel.close`): a request the client may refuse, not a kill.
    CloseWindows(Vec<String>),
    /// Hide every window, or, if they are already hidden, bring them back.
    ShowDesktop,
    /// Start another window of a windowed app (the dock item's own click).
    NewWindow(String),
}

/// How loud a tag is. Effect classes map onto it: read is plain, write is
/// caution, destructive and external are danger (and carry `!!` as well, so
/// the class never rests on colour alone; it is also how `aurora open` marks them).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Tone {
    Plain,
    Caution,
    Danger,
}

impl Tone {
    fn rgb(self, text: [u8; 3]) -> [u8; 3] {
        match self {
            Tone::Plain => text,
            Tone::Caution => [244, 196, 92],
            Tone::Danger => [255, 128, 116],
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub enum Entry {
    Item { label: String, action: Action, enabled: bool, checked: bool, note: Option<String>, tag: Option<(String, Tone)> },
    Header(String),
    Separator,
}

impl Entry {
    pub fn item(label: &str, action: Action) -> Entry {
        Entry::Item { label: label.into(), action, enabled: true, checked: false, note: None, tag: None }
    }
    pub fn checked(mut self, on: bool) -> Entry {
        if let Entry::Item { checked, .. } = &mut self {
            *checked = on;
        }
        self
    }
    pub fn note(mut self, n: &str) -> Entry {
        if let Entry::Item { note, .. } = &mut self {
            *note = Some(n.into());
        }
        self
    }
    /// A short bold label at the row's right edge, after the note.
    pub fn tag(mut self, text: &str, tone: Tone) -> Entry {
        if let Entry::Item { tag, .. } = &mut self {
            *tag = Some((text.into(), tone));
        }
        self
    }
    /// Disabled, with the reason shown where a note would be.
    pub fn disabled(mut self, why: &str) -> Entry {
        if let Entry::Item { enabled, note, .. } = &mut self {
            *enabled = false;
            *note = Some(why.into());
        }
        self
    }
    fn selectable(&self) -> bool {
        matches!(self, Entry::Item { enabled: true, .. })
    }
}

/// A logical rectangle, relative to the menu's top-left.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct Rt {
    pub x: f32,
    pub y: f32,
    pub w: f32,
    pub h: f32,
}

impl Rt {
    pub fn contains(&self, x: f32, y: f32) -> bool {
        x >= self.x && y >= self.y && x < self.x + self.w && y < self.y + self.h
    }
}

/// Everything drawing needs from the theme.
#[derive(Clone, Debug)]
pub struct Style {
    pub font: f32,
    pub scale: f64,
    pub text: [u8; 3],
    pub hover: [u8; 3],
    pub hover_strength: f32,
    pub glass_on: bool,
    pub radius: f32,
}

pub struct Menu {
    pub entries: Vec<Entry>,
    /// Index into `entries` of the highlighted (hovered or keyboard-selected) item.
    pub hover: Option<usize>,
    /// Top-left in logical output coordinates, after fitting to the output.
    pub origin: (f32, f32),
    /// The corner nearest the pointer: the animation grows from here.
    pub anchor: (f32, f32),
    pub rows: Vec<Rt>,
    pub size: (f32, f32),
    pub appear: Appear,
    pub logged: bool,
}

const PAD: f32 = 6.0;
const MIN_W: f32 = 190.0;

impl Menu {
    pub fn new(entries: Vec<Entry>) -> Menu {
        Menu { entries, hover: None, origin: (0.0, 0.0), anchor: (0.0, 0.0), rows: vec![], size: (0.0, 0.0), appear: Appear::new(super::motion::MENU_MS), logged: false }
    }

    fn item_h(font: f32) -> f32 {
        (font * 1.9).round()
    }

    /// Measure and place the menu so its corner is at `at` (the pointer),
    /// flipped to the other side of the pointer where it would leave the
    /// output, and finally clamped inside it. `out` is the output in logical units.
    pub fn layout(&mut self, text: &mut TextSystem, font: f32, scale: f64, at: (f32, f32), out: (f32, f32)) {
        let spec = FontSpec::new(font);
        let hdr = FontSpec::new(font * 0.85);
        let note_spec = FontSpec::new(font * 0.85);
        let ih = Self::item_h(font);
        let gutter = (font * 1.5).round();
        let mut label_w = 0.0f32;
        let mut note_w = 0.0f32;
        let mut tag_w = 0.0f32;
        let tag_spec = FontSpec::new(font * 0.85).bold();
        for e in &self.entries {
            match e {
                Entry::Item { label, note, tag, .. } => {
                    if let Some((t, _)) = tag {
                        tag_w = tag_w.max(text.string_width(t, &tag_spec, scale));
                    }
                    label_w = label_w.max(text.string_width(label, &spec, scale));
                    if let Some(n) = note {
                        note_w = note_w.max(text.string_width(n, &note_spec, scale));
                    }
                }
                Entry::Header(h) => label_w = label_w.max(text.string_width(h, &hdr, scale) - gutter),
                Entry::Separator => {}
            }
        }
        let gap = if note_w > 0.0 { 28.0 } else { 0.0 };
        let tag_col = if tag_w > 0.0 { tag_w + 20.0 } else { 0.0 };
        let w = (PAD * 2.0 + gutter + label_w + gap + note_w + tag_col + 14.0).ceil().max(MIN_W);
        let mut y = PAD;
        self.rows.clear();
        for e in &self.entries {
            let h = match e {
                Entry::Item { .. } => ih,
                Entry::Header(_) => (ih * 0.8).round(),
                Entry::Separator => 9.0,
            };
            self.rows.push(Rt { x: PAD, y, w: w - 2.0 * PAD, h });
            y += h;
        }
        let h = y + PAD;
        self.size = (w, h);
        let flip_x = at.0 + w > out.0;
        let flip_y = at.1 + h > out.1;
        let x = if flip_x { at.0 - w } else { at.0 };
        let y = if flip_y { at.1 - h } else { at.1 };
        self.origin = (x.clamp(0.0, (out.0 - w).max(0.0)), y.clamp(0.0, (out.1 - h).max(0.0)));
        self.anchor = (if flip_x { self.origin.0 + w } else { self.origin.0 }, if flip_y { self.origin.1 + h } else { self.origin.1 });
    }

    /// The selectable item under a point, relative to the menu's top-left.
    pub fn hit(&self, x: f32, y: f32) -> Option<usize> {
        self.rows.iter().position(|r| r.contains(x, y)).filter(|&i| self.entries[i].selectable())
    }

    pub fn action_at(&self, i: usize) -> Option<Action> {
        match self.entries.get(i) {
            Some(Entry::Item { action, enabled: true, .. }) => Some(action.clone()),
            _ => None,
        }
    }

    pub fn activate(&self) -> Option<Action> {
        self.hover.and_then(|i| self.action_at(i))
    }

    /// Move the highlight to the next selectable item in direction `dir`
    /// (+1 / -1), wrapping. With nothing highlighted, Down takes the first and Up the last.
    pub fn step(&mut self, dir: i32) {
        let n = self.entries.len() as i32;
        let mut i = match (self.hover, dir) {
            (Some(i), _) => i as i32,
            (None, d) if d > 0 => -1,
            _ => n,
        };
        for _ in 0..n {
            i = (i + dir).rem_euclid(n);
            if self.entries[i as usize].selectable() {
                self.hover = Some(i as usize);
                return;
            }
        }
    }

    pub fn key(&mut self, sym: u32) -> KeyOut {
        use smithay::input::keyboard::keysyms as k;
        match sym {
            k::KEY_Escape => KeyOut::Close,
            k::KEY_Down => {
                self.step(1);
                KeyOut::Handled
            }
            k::KEY_Up => {
                self.step(-1);
                KeyOut::Handled
            }
            k::KEY_Return | k::KEY_KP_Enter | k::KEY_space => self.activate().map_or(KeyOut::Handled, KeyOut::Act),
            _ => KeyOut::Handled,
        }
    }

    /// Rasterise at device pixels. Returns the canvas and the display list's
    /// text, as `(label, device x, device y, device width)`, which the log shows
    /// and tests compare against decoded pixels.
    pub fn draw(&self, text: &mut TextSystem, st: &Style) -> (Canvas, Vec<(String, i32, i32, i32)>) {
        let s = st.scale;
        let d = |v: f32| (v as f64 * s).round() as i32;
        let (cw, ch) = ((self.size.0 as f64 * s).ceil() as usize, (self.size.1 as f64 * s).ceil() as usize);
        let mut c = Canvas::new(cw.max(1), ch.max(1), [0, 0, 0, 0]);
        if !st.glass_on {
            c.round_rect(0, 0, cw as i32, ch as i32, st.radius * s as f32, [18, 20, 30, 235]);
        }
        let spec = FontSpec::new(st.font);
        let small = FontSpec::new(st.font * 0.85);
        let tag_spec = FontSpec::new(st.font * 0.85).bold();
        let (m, sm) = (text.metrics(&spec), text.metrics(&small));
        let gutter = (st.font * 1.5).round();
        let t = st.text;
        let mut placed = vec![];
        for (i, (e, r)) in self.entries.iter().zip(&self.rows).enumerate() {
            match e {
                Entry::Separator => {
                    c.over_rect(d(r.x + 6.0), d(r.y + r.h / 2.0), d(r.w - 12.0).max(1), (s.round() as i32).max(1), [t[0], t[1], t[2], 60]);
                }
                Entry::Header(h) => {
                    let y = r.y + (r.h - (sm.ascent + sm.descent)) / 2.0;
                    text.draw(&mut c, r.x + 8.0, y, h, &small, [t[0], t[1], t[2], 140], s);
                }
                Entry::Item { label, enabled, checked, note, tag, .. } => {
                    if self.hover == Some(i) && *enabled {
                        let hv = st.hover;
                        c.round_rect(d(r.x), d(r.y), d(r.w), d(r.h), 6.0 * s as f32, [hv[0], hv[1], hv[2], (st.hover_strength * 255.0) as u8]);
                    }
                    let a = if *enabled { 255 } else { 110 };
                    if *checked {
                        let dot = (st.font * 0.42).round().max(4.0);
                        c.round_rect(d(r.x + 10.0), d(r.y + (r.h - dot) / 2.0), d(dot), d(dot), d(dot) as f32 / 2.0, [t[0], t[1], t[2], a]);
                    }
                    let (lx, ly) = (r.x + gutter + 4.0, r.y + (r.h - (m.ascent + m.descent)) / 2.0);
                    text.draw(&mut c, lx, ly, label, &spec, [t[0], t[1], t[2], a], s);
                    placed.push((label.clone(), d(lx), d(ly), (text.string_width(label, &spec, s) as f64 * s).round() as i32));
                    let ny = r.y + (r.h - (sm.ascent + sm.descent)) / 2.0;
                    let mut right = r.x + r.w - 8.0;
                    if let Some((tg, tone)) = tag {
                        let tw = text.string_width(tg, &tag_spec, s);
                        let col = tone.rgb(t);
                        text.draw(&mut c, right - tw, ny, tg, &tag_spec, [col[0], col[1], col[2], if *enabled { 255 } else { 110 }], s);
                        placed.push((tg.clone(), d(right - tw), d(ny), (tw as f64 * s).round() as i32));
                        right -= tw + 20.0;
                    }
                    if let Some(n) = note {
                        let nw = text.string_width(n, &small, s);
                        text.draw(&mut c, right - nw, ny, n, &small, [t[0], t[1], t[2], if *enabled { 150 } else { 95 }], s);
                    }
                }
            }
        }
        (c, placed)
    }
}

pub enum KeyOut {
    Handled,
    Close,
    Act(Action),
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample() -> Menu {
        let mut m = Menu::new(vec![
            Entry::Header("Background".into()),
            Entry::item("Shader", Action::OpenSettings),
            Entry::item("Image", Action::OpenSettings).disabled("no image set"),
            Entry::Separator,
            Entry::item("Gradient", Action::OpenTerminal).checked(true),
        ]);
        let mut t = TextSystem::new("", "");
        m.layout(&mut t, 14.0, 1.0, (10.0, 10.0), (800.0, 600.0));
        m
    }

    #[test]
    fn keyboard_skips_headers_separators_and_disabled_items_and_wraps() {
        let mut m = sample();
        m.step(1);
        assert_eq!(m.hover, Some(1));
        m.step(1);
        assert_eq!(m.hover, Some(4), "Image is disabled, the separator is not an item");
        m.step(1);
        assert_eq!(m.hover, Some(1), "wraps past the end and the header");
        m.step(-1);
        assert_eq!(m.hover, Some(4));
        m.hover = None;
        m.step(-1);
        assert_eq!(m.hover, Some(4), "Up from nothing takes the last item");
    }

    #[test]
    fn disabled_items_cannot_be_hit_or_activated() {
        let mut m = sample();
        let r = m.rows[2];
        assert_eq!(m.hit(r.x + 3.0, r.y + 3.0), None);
        assert_eq!(m.action_at(2), None);
        m.hover = Some(2);
        assert_eq!(m.activate(), None);
        let r = m.rows[1];
        assert_eq!(m.hit(r.x + 3.0, r.y + 3.0), Some(1));
    }

    #[test]
    fn the_menu_flips_and_clamps_to_stay_inside_the_output() {
        let mut m = sample();
        let mut t = TextSystem::new("", "");
        let (w, h) = m.size;
        m.layout(&mut t, 14.0, 1.0, (795.0, 595.0), (800.0, 600.0));
        assert_eq!(m.origin, (795.0 - w, 595.0 - h), "opens up and to the left of the pointer");
        assert_eq!(m.anchor, (795.0, 595.0));
        m.layout(&mut t, 14.0, 1.0, (100.0, 50.0), (800.0, 600.0));
        assert_eq!((m.origin, m.anchor), ((100.0, 50.0), (100.0, 50.0)));
        m.layout(&mut t, 14.0, 1.0, (5.0, 5.0), (100.0, 80.0));
        assert!(m.origin.0 >= 0.0 && m.origin.1 >= 0.0 && m.origin.0 + w <= 100.0 + w);
    }

    #[test]
    fn escape_closes_and_return_activates_the_highlight() {
        use smithay::input::keyboard::keysyms as k;
        let mut m = sample();
        assert!(matches!(m.key(k::KEY_Return), KeyOut::Handled), "nothing highlighted: nothing to do");
        m.key(k::KEY_Down);
        assert!(matches!(m.key(k::KEY_Return), KeyOut::Act(Action::OpenSettings)));
        assert!(matches!(m.key(k::KEY_Escape), KeyOut::Close));
    }
}
