//! Window placement memory, written to a state file so a restarted compositor
//! puts windows back where they were (`--state-file PATH`; off without it).
//!
//! What this is and is not. A window is its client's surface: when the
//! compositor dies its clients lose their `wl_display` and exit, and a new
//! compositor cannot conjure them back. So this is a *placement memory* keyed
//! by (app_id, title, instance): when a client with that identity maps a window
//! again, the window is moved to where its predecessor was last left. Nothing
//! is restored for a client that does not come back. Position only; size and
//! stacking order are recorded but not re-applied (the client owns its size).
//!
//! "Settled": the file is rewritten only after the live layout has been
//! unchanged for [`SETTLE`], so a drag in progress never reaches disk, and a
//! kill -9 mid-drag restores the last resting place, not a half-way point.
//! Writes are atomic (temp file + rename), so a crash mid-write leaves the
//! previous good file. Closed windows stay in the memory (bounded by [`CAP`],
//! oldest forgotten first): an empty desktop after a crash must not erase what
//! the crash is about to be recovered from.
//!
//! Shell state (scale, background mode, dock side and visibility, font) is not
//! in this file: the settings panel already persists it by editing the theme
//! file (`fx::edit`), which a restart re-reads. See `trusted::testinput` `state`.

use std::{
    collections::HashMap,
    hash::Hash,
    path::{Path, PathBuf},
    time::{Duration, Instant},
};

use serde::{Deserialize, Serialize};


/// How long the layout must sit still before it counts as settled.
pub const SETTLE: Duration = Duration::from_millis(400);
/// Most placements remembered.
pub const CAP: usize = 64;

#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Eq)]
pub struct Entry {
    pub app_id: String,
    pub title: String,
    /// Nth live window with this (app_id, title), in the order they were first seen.
    pub instance: u32,
    pub x: i32,
    pub y: i32,
    pub w: i32,
    pub h: i32,
}

impl Entry {
    fn same_window(&self, o: &Entry) -> bool {
        (&self.app_id, &self.title, self.instance) == (&o.app_id, &o.title, o.instance)
    }
}

#[derive(Serialize, Deserialize)]
struct File {
    version: u32,
    windows: Vec<Entry>,
}

/// One mapped window as the compositor sees it right now.
pub struct Live<I> {
    pub id: I,
    pub app_id: String,
    pub title: String,
    pub x: i32,
    pub y: i32,
    pub w: i32,
    pub h: i32,
}

pub struct Persist<I> {
    path: PathBuf,
    mem: Vec<Entry>,
    /// Windows whose identity is known and which have been placed (or found nothing to restore).
    placed: HashMap<I, (String, String, u32)>,
    last_seen: Vec<Entry>,
    changed_at: Instant,
    dirty: bool,
    /// Placements applied from the file since start.
    pub restored: usize,
}

/// Keep a restored window reachable: some of it must lie inside the output.
pub fn clamp(x: i32, y: i32, w: i32, h: i32, out: (i32, i32)) -> (i32, i32) {
    let _ = h;
    let grip = 80.min(w.max(1));
    let (lo, hi) = (grip - w, (out.0 - grip).max(grip - w));
    (x.clamp(lo, hi), y.clamp(0, (out.1 - 40).max(0)))
}

impl<I: Eq + Hash + Clone> Persist<I> {
    /// Load `path` if it holds a good file; a missing or unreadable one starts empty.
    pub fn open(path: &Path, now: Instant) -> (Self, String) {
        let (mem, note) = match std::fs::read_to_string(path) {
            Ok(t) => match serde_json::from_str::<File>(&t) {
                Ok(f) if f.version == 1 => {
                    let n = f.windows.len();
                    (f.windows.into_iter().take(CAP).collect(), format!("{n} remembered placement(s) loaded"))
                }
                Ok(f) => (vec![], format!("ignoring {}: unknown version {}", path.display(), f.version)),
                Err(e) => (vec![], format!("ignoring {}: not valid ({e})", path.display())),
            },
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => (vec![], "no earlier layout (first run)".into()),
            Err(e) => (vec![], format!("cannot read {}: {e}", path.display())),
        };
        let p = Persist { path: path.into(), mem, placed: HashMap::new(), last_seen: vec![], changed_at: now, dirty: false, restored: 0 };
        (p, note)
    }

    pub fn remembered(&self) -> &[Entry] {
        &self.mem
    }

    /// One housekeeping step. Returns the moves to apply (`id`, x, y) for windows
    /// that just got an identity and have a remembered place; writes the file when
    /// the layout has settled. `out` is the output's logical size.
    pub fn tick(&mut self, live: &[Live<I>], out: (i32, i32), now: Instant) -> Vec<(I, i32, i32)> {
        self.placed.retain(|id, _| live.iter().any(|l| &l.id == id));
        let mut moves = vec![];
        let mut fresh: Vec<I> = vec![];
        for l in live.iter().filter(|l| !l.app_id.is_empty() || !l.title.is_empty()) {
            if self.placed.contains_key(&l.id) {
                continue;
            }
            let inst = self.placed.values().filter(|(a, t, _)| (a, t) == (&l.app_id, &l.title)).count() as u32;
            self.placed.insert(l.id.clone(), (l.app_id.clone(), l.title.clone(), inst));
            fresh.push(l.id.clone());
            let want = Entry { app_id: l.app_id.clone(), title: l.title.clone(), instance: inst, x: 0, y: 0, w: 0, h: 0 };
            if let Some(m) = self.mem.iter().find(|m| m.same_window(&want)) {
                let (x, y) = clamp(m.x, m.y, l.w, l.h, out);
                if (x, y) != (l.x, l.y) {
                    moves.push((l.id.clone(), x, y));
                }
                self.restored += 1;
            }
        }
        // What is on screen now, for windows placed before this tick.
        let mut seen: Vec<Entry> = vec![];
        for l in live {
            if fresh.contains(&l.id) {
                continue;
            }
            if let Some((a, t, i)) = self.placed.get(&l.id) {
                seen.push(Entry { app_id: a.clone(), title: t.clone(), instance: *i, x: l.x, y: l.y, w: l.w, h: l.h });
            }
        }
        if !moves.is_empty() || seen != self.last_seen {
            self.changed_at = now;
            self.last_seen = seen.clone();
            for e in seen {
                self.remember(e);
            }
        }
        if self.dirty && now.duration_since(self.changed_at) >= SETTLE {
            self.dirty = false;
            if let Err(e) = self.write() {
                tracing::warn!("layout state {}: {e}", self.path.display());
            }
        }
        moves
    }

    /// Write now if anything is pending, ignoring the settle delay. For a
    /// clean shutdown: the per-frame tick only writes once the layout has been
    /// still for `SETTLE`, so quitting inside that window would otherwise drop
    /// the last move. A crash still loses it -- that is what SETTLE buys, a
    /// resting place rather than a half-finished drag.
    pub fn flush(&mut self) {
        if !self.dirty {
            return;
        }
        self.dirty = false;
        if let Err(e) = self.write() {
            tracing::warn!("layout state {}: {e}", self.path.display());
        }
    }

    fn remember(&mut self, e: Entry) {
        if let Some(i) = self.mem.iter().position(|m| m.same_window(&e)) {
            if self.mem[i] == e {
                return;
            }
            self.mem.remove(i);
        }
        self.mem.push(e);
        if self.mem.len() > CAP {
            self.mem.remove(0);
        }
        self.dirty = true;
    }

    fn write(&self) -> std::io::Result<()> {
        use std::io::Write;
        use std::os::unix::fs::OpenOptionsExt;
        let tmp = self.path.with_extension("tmp");
        let body = serde_json::to_vec_pretty(&File { version: 1, windows: self.mem.clone() }).map_err(std::io::Error::other)?;
        {
            let mut f = std::fs::OpenOptions::new().write(true).create(true).truncate(true).mode(0o600).open(&tmp)?;
            f.write_all(&body)?;
            f.sync_all()?;
        }
        std::fs::rename(&tmp, &self.path)
    }
}

use smithay::{reexports::wayland_server::{Resource, backend::ObjectId}, wayland::{compositor::with_states, shell::xdg::XdgToplevelSurfaceData}};

impl crate::Aurora {
    /// The windows now mapped, with the identity their client gave them.
    pub fn live_windows(&self) -> Vec<Live<ObjectId>> {
        self.space
            .elements()
            .filter_map(|w| {
                let t = w.toplevel()?;
                let (app_id, title) = with_states(t.wl_surface(), |s| {
                    let a = s.data_map.get::<XdgToplevelSurfaceData>()?.lock().ok()?;
                    Some((a.app_id.clone().unwrap_or_default(), a.title.clone().unwrap_or_default()))
                })?;
                let loc = self.space.element_location(w)?;
                let size = w.geometry().size;
                let cap = |s: String| s.chars().take(200).collect::<String>();
                Some(Live { id: t.wl_surface().id(), app_id: cap(app_id), title: cap(title), x: loc.x, y: loc.y, w: size.w, h: size.h })
            })
            .collect()
    }

    /// Per-frame: restore remembered places for newly identified windows, and save the settled layout.
    pub fn persist_tick(&mut self) {
        let Some(mut p) = self.persist.take() else { return };
        let live = self.live_windows();
        let (ph, sc) = (self.output_physical(), self.output_scale());
        let out = ((ph.w as f64 / sc) as i32, (ph.h as f64 / sc) as i32);
        for (id, x, y) in p.tick(&live, out, Instant::now()) {
            let win = self.space.elements().find(|w| w.toplevel().is_some_and(|t| t.wl_surface().id() == id)).cloned();
            if let Some(w) = win {
                self.space.relocate_element(&w, (x, y));
            }
        }
        self.persist = Some(p);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn live(id: u32, app: &str, x: i32, y: i32) -> Live<u32> {
        Live { id, app_id: app.into(), title: "t".into(), x, y, w: 300, h: 200 }
    }
    fn tmp(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("aurora-persist-{}-{name}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d.join("layout.json")
    }

    #[test]
    fn nothing_is_written_until_the_layout_settles_and_the_file_survives_a_restart() {
        let p = tmp("settle");
        let t0 = Instant::now();
        let (mut a, _) = Persist::<u32>::open(&p, t0);
        a.tick(&[live(1, "a", 0, 0)], (1280, 800), t0);
        a.tick(&[live(1, "a", 100, 50)], (1280, 800), t0 + Duration::from_millis(100));
        a.tick(&[live(1, "a", 100, 50)], (1280, 800), t0 + Duration::from_millis(300));
        assert!(!p.exists(), "still inside the settle window after the last move");
        a.tick(&[live(1, "a", 100, 50)], (1280, 800), t0 + Duration::from_millis(600));
        assert!(p.exists());
        // A new compositor: same app maps at the origin and is moved to the remembered place.
        let (mut b, note) = Persist::<u32>::open(&p, t0);
        assert!(note.contains("1 remembered"), "{note}");
        let m = b.tick(&[live(9, "a", 0, 0)], (1280, 800), t0);
        assert_eq!(m, vec![(9, 100, 50)]);
        assert_eq!(b.restored, 1);
    }

    #[test]
    fn same_identity_windows_are_told_apart_by_instance_and_closed_windows_are_remembered() {
        let p = tmp("inst");
        let t0 = Instant::now();
        let (mut a, _) = Persist::<u32>::open(&p, t0);
        let two = [live(1, "a", 10, 10), live(2, "a", 500, 400)];
        a.tick(&two, (1280, 800), t0);
        a.tick(&two, (1280, 800), t0);
        a.tick(&two, (1280, 800), t0 + SETTLE * 2);
        a.tick(&[], (1280, 800), t0 + SETTLE * 4); // everything closed
        a.tick(&[], (1280, 800), t0 + SETTLE * 8);
        let (mut b, _) = Persist::<u32>::open(&p, t0);
        let m = b.tick(&[live(7, "a", 0, 0), live(8, "a", 0, 0)], (1280, 800), t0);
        assert_eq!(m, vec![(7, 10, 10), (8, 500, 400)]);
    }

    #[test]
    fn corrupt_files_start_empty_and_off_screen_places_are_pulled_back() {
        let p = tmp("bad");
        std::fs::write(&p, "{nope").unwrap();
        let (a, note) = Persist::<u32>::open(&p, Instant::now());
        assert!(a.remembered().is_empty() && note.contains("not valid"), "{note}");
        let (x, y) = clamp(5000, -300, 300, 200, (1280, 800));
        assert!(x <= 1280 - 80 && x >= 80 - 300 && y >= 0, "({x},{y})");
        assert_eq!(clamp(100, 50, 300, 200, (1280, 800)), (100, 50));
    }
}
