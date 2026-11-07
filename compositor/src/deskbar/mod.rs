//! The Deskbar: a glass box listing every application that has a window, each opening to its windows
//! (BeOS's expando), with what can honestly be said about its CPU, memory and network. Decision 0021
//! says why it is the compositor's and not a client's, and what it does not show.
//!
//! The compositor draws it because it must list the windows of sandboxed apps, and the only thing that
//! sees every window is the `Space` the compositor already owns. A client that could list them would
//! have to be handed a "see every window" capability, and an always-running app holding that is
//! strictly more dangerous than this box.
//!
//! It is chrome in the scene, like the dock: drawn with chrome glass behind it, present in captures
//! (what a person sees is what a capture sees), and swallowing the pointer over it. It takes no keyboard
//! focus and restyles nothing a client can see.
//!
//! The pieces: `model` (what is true, as data, and the words for it), `sample` (the cgroup and /proc
//! reads that numbers may come from), `feed` (the bus thread that learns network activity), `draw`
//! (layout and pixels). This file is the state and the glue to the `Space` and the pointer.

pub mod draw;
pub mod feed;
pub mod model;
pub mod sample;

use std::{
    collections::{HashMap, HashSet},
    time::{Duration, Instant},
};

use smithay::{
    desktop::Window,
    reexports::wayland_server::Resource,
    wayland::{seat::WaylandFocus, shell::xdg::XdgToplevelSurfaceData},
};

use crate::{
    Aurora,
    shell::{layout::Rect, motion::Ease},
    windeco::win_key,
};
use draw::{Kind, Placed, View};
use model::{Dom, Figures, Group, Net, SysFigures, Win};

/// How often the cgroup and /proc figures are read. A rate over less than this is mostly noise.
const SAMPLE_EVERY: Duration = Duration::from_secs(1);

#[derive(Default)]
pub struct Deskbar {
    /// What the bus thread last said about the network.
    pub net: Net,
    /// A `--bus-socket` was given: silence from the bus then means "nothing", not "no bus".
    pub watching: bool,
    pub(crate) sampler: sample::Sampler,
    figures: HashMap<String, Figures>,
    sys: SysFigures,
    last_sample: Option<Instant>,
    /// Shown now. Sampling stops when it is not: nothing is read for a box nobody can see.
    shown: bool,
    /// The pid that connected for each domain on screen, so its cgroup can be found at the next reading.
    pids: HashMap<String, i32>,
    gseq: HashMap<String, u64>,
    wseq: HashMap<String, u64>,
    next_seq: u64,
    expanded: HashSet<String>,
    ease: HashMap<String, Ease>,
    /// The row under the pointer.
    pub hover: Option<Kind>,
    /// The rows of the last raster, relative to the box.
    placed: Vec<Placed>,
    key: String,
}

/// A window as the Deskbar sees it, before grouping.
struct Seen {
    id: String,
    app_id: String,
    title: String,
    focused: bool,
    dom: Dom,
}

impl Aurora {
    fn deskbar_seen(&mut self) -> (Vec<Seen>, bool) {
        let wins: Vec<Window> = self.space.elements().cloned().collect();
        let (mut out, mut fullscreen) = (vec![], false);
        let mut pids: HashMap<String, i32> = HashMap::new();
        for w in &wins {
            let Some(id) = win_key(w) else { continue };
            if let Some(x) = w.x11_surface() {
                // A menu or tooltip is not an application's window.
                if x.is_override_redirect() {
                    continue;
                }
                fullscreen |= x.is_fullscreen();
                // The client behind an X11 window's surface is Xwayland, whose cgroup says nothing about the
                // application: its domain is unknown, and is said to be.
                out.push(Seen { id, app_id: x.class(), title: x.title(), focused: self.is_focused(w), dom: Dom::Unknown("X11 window") });
                continue;
            }
            let Some(t) = w.toplevel() else { continue };
            fullscreen |= t.with_pending_state(|s| s.states.contains(smithay::reexports::wayland_protocols::xdg::shell::server::xdg_toplevel::State::Fullscreen));
            let (app_id, title) = smithay::wayland::compositor::with_states(t.wl_surface(), |s| {
                let d = s.data_map.get::<XdgToplevelSurfaceData>().and_then(|d| d.lock().ok());
                d.map(|d| (d.app_id.clone().unwrap_or_default(), d.title.clone().unwrap_or_default())).unwrap_or_default()
            });
            let dom = match self.deskbar_peer(w) {
                Some(p) => {
                    pids.insert(p.name.clone(), p.pid);
                    Dom::In { name: p.name }
                }
                None => Dom::None,
            };
            out.push(Seen { id, app_id, title, focused: self.is_focused(w), dom });
        }
        self.deskbar.pids = pids;
        (out, fullscreen)
    }

    /// Which domain a Wayland window's client is in: the kernel's own record, read once at connect by
    /// `crate::peer` (the same fact the title bar's badge shows, so the two can never disagree), never
    /// anything the client says. `None` for a client the kernel placed outside every domain.
    ///
    /// `peer` reads `None` both for "placed outside every domain" and for "its cgroup could not be read"
    /// (a process that exited between accept and the read); the second is rare enough, and fails in the
    /// direction of "not in a domain", which is the row without figures. Decision 0021 records it.
    fn deskbar_peer(&self, w: &Window) -> Option<crate::peer::PeerDomain> {
        let client = w.wl_surface()?.client()?;
        client.get_data::<crate::state::ClientState>()?.domain.clone()
    }

    /// Per frame, before the scene is composed: decide whether the box is shown and, if what it would
    /// say changed, rasterise it. The upload and the placement are `Shell::prepare_deskbar`'s.
    pub fn deskbar_frame(&mut self) {
        let cfg = self.fx.theme.deskbar.clone();
        let on = cfg.enabled && !self.shell.legacy_scene;
        let (seen, fullscreen) = if on { self.deskbar_seen() } else { (vec![], false) };
        // Hidden only when switched off, or behind something fullscreen that the box would float over.
        //
        // It is NOT hidden for having no windows, which it used to be, on the reasoning that an empty
        // box is clutter. That was wrong twice over. The Deskbar is the thing you look at to see what
        // is running, so "nothing is running" is an answer it should give rather than a reason to
        // vanish -- and it still carries the machine's own CPU and memory, which do not depend on a
        // window existing. Worse, the desktop menu's Show entry could not override it: toggling with
        // no windows open changed nothing visible, which is indistinguishable from the feature being
        // broken. That is how it was first reported.
        if !on || fullscreen {
            self.deskbar.shown = false;
            self.deskbar.placed.clear();
            self.deskbar.key.clear();
            self.deskbar.hover = None;
            self.shell.deskbar.show = false;
            return;
        }
        let groups = self.deskbar_groups(seen);
        let live: HashSet<&str> = groups.iter().map(|g| g.key.as_str()).collect();
        self.deskbar.expanded.retain(|k| live.contains(k.as_str()));
        self.deskbar.ease.retain(|k, _| live.contains(k.as_str()));
        let ms = cfg.ease_ms;
        let expand: Vec<f32> = groups
            .iter()
            .map(|g| {
                let target = self.deskbar.expanded.contains(&g.key) as u8 as f32;
                let e = self.deskbar.ease.entry(g.key.clone()).or_insert_with(|| Ease::at(target));
                e.set(target, ms);
                e.value(ms)
            })
            .collect();
        let st = &self.shell;
        let scale = st.scale;
        let p = self.output_physical();
        let usable = st.usable(p.w, p.h);
        // Below the clock and the margin: what is left of the output's height, logical.
        let max_h = ((usable.h as f64 / scale) as f32 - cfg.margin - 130.0).max(120.0);
        let style = draw::Style { cfg: &cfg, scale, font: st.font_size(), glass_on: st.glass_enabled(), radius: st.glass_radius(), max_h };
        let view = View { groups: &groups, expand: &expand, figures: &self.deskbar.figures, net: &self.deskbar.net, net_watching: self.deskbar.watching, sys: self.deskbar.sys, hover: self.deskbar.hover.clone() };
        let key = draw::key(&view, &style);
        let rebuilt = (key != self.deskbar.key).then(|| draw::build(&view, &style, &mut self.shell.text));
        self.deskbar.shown = true;
        self.shell.deskbar.show = true;
        self.shell.deskbar.edge = cfg.edge;
        self.shell.deskbar.margin = cfg.margin;
        if let Some(r) = rebuilt {
            self.deskbar.key = r.key.clone();
            self.deskbar.placed = r.rows.clone();
            self.shell.deskbar.pending = Some(r);
        }
    }

    /// Group the windows by (app id, domain) and give both groups and windows a stable order.
    fn deskbar_groups(&mut self, seen: Vec<Seen>) -> Vec<Group> {
        let mut groups: Vec<Group> = vec![];
        for s in seen {
            let key = format!("{}\u{1f}{}", s.app_id, draw::domain_label(&s.dom));
            let label = match self.shell.registry.apps.iter().find(|a| !s.app_id.is_empty() && a.id == s.app_id) {
                Some(a) => model::safe(&a.name),
                None if s.app_id.is_empty() => "Unidentified".to_string(),
                None => model::safe(&s.app_id),
            };
            let win = Win { id: s.id.clone(), title: s.title, focused: s.focused };
            if !self.deskbar.wseq.contains_key(&s.id) {
                self.deskbar.next_seq += 1;
                self.deskbar.wseq.insert(s.id, self.deskbar.next_seq);
            }
            match groups.iter_mut().find(|g| g.key == key) {
                Some(g) => g.wins.push(win),
                None => groups.push(Group { key, label, dom: s.dom, wins: vec![win] }),
            }
        }
        for g in &groups {
            if !self.deskbar.gseq.contains_key(&g.key) {
                self.deskbar.next_seq += 1;
                self.deskbar.gseq.insert(g.key.clone(), self.deskbar.next_seq);
            }
        }
        let ids: HashSet<&str> = groups.iter().flat_map(|g| g.wins.iter().map(|w| w.id.as_str())).collect();
        self.deskbar.wseq.retain(|k, _| ids.contains(k.as_str()));
        let keys: HashSet<&str> = groups.iter().map(|g| g.key.as_str()).collect();
        self.deskbar.gseq.retain(|k, _| keys.contains(k.as_str()));
        model::order(&mut groups, &self.deskbar.gseq, &self.deskbar.wseq);
        groups
    }

    /// After a frame: read the cgroup and /proc figures once a second, while the box is shown.
    pub fn deskbar_tick(&mut self) {
        if !self.deskbar.shown {
            self.deskbar.last_sample = None;
            return;
        }
        if self.deskbar.last_sample.is_some_and(|t| t.elapsed() < SAMPLE_EVERY) {
            return;
        }
        let now = Instant::now();
        self.deskbar.last_sample = Some(now);
        // Each domain's directory comes from where its client's pid is NOW: a pid that has left the domain
        // (or been reused) names nothing, and the domain simply has no figures this second.
        let mut doms: Vec<(String, String)> = vec![];
        for (name, pid) in self.deskbar.pids.iter().take(32) {
            let file = self.deskbar.sampler.roots.proc.join(pid.to_string()).join("cgroup");
            if let Some(cg) = std::fs::read_to_string(file).ok().and_then(|t| sample::domain_cgroup(&t, name)) {
                doms.push((name.clone(), cg));
            }
        }
        self.deskbar.figures = self.deskbar.sampler.domains(&doms, now);
        self.deskbar.sys = self.deskbar.sampler.system();
    }

    fn deskbar_row_at(&self, dp: (f64, f64)) -> Option<(Rect, Option<Kind>)> {
        let r = self.shell.deskbar_rect().filter(|r| r.contains(dp.0, dp.1))?;
        let (x, y) = (dp.0 - r.x as f64, dp.1 - r.y as f64);
        let hit = self.deskbar.placed.iter().find(|p| p.rect.contains(x, y)).map(|p| p.kind.clone());
        Some((r, hit))
    }

    /// The pointer moved to `dp` (device pixels). True when it is on the box, which then owns it.
    pub fn deskbar_hover(&mut self, dp: (f64, f64)) -> bool {
        let on = self.deskbar_row_at(dp);
        self.deskbar.hover = on.as_ref().and_then(|(_, k)| k.clone()).filter(Kind::clickable);
        on.is_some()
    }

    /// A button press at `dp`. True when the box took it, whichever button: no client sees the press or its release.
    /// A left press on an application opens or closes it; on a window, raises and focuses that window.
    pub fn deskbar_press(&mut self, dp: (f64, f64), left: bool) -> bool {
        let Some((_, hit)) = self.deskbar_row_at(dp) else { return false };
        if !left {
            return true;
        }
        match hit {
            Some(Kind::App(key)) => {
                let open = !self.deskbar.expanded.remove(&key);
                if open {
                    self.deskbar.expanded.insert(key.clone());
                }
                tracing::info!("deskbar: {} {}", if open { "expand" } else { "collapse" }, key.replace('\u{1f}', " @ "));
            }
            Some(Kind::Win(id)) => {
                let w = self.space.elements().find(|w| win_key(w).as_deref() == Some(id.as_str())).cloned();
                if let Some(w) = w {
                    tracing::info!("deskbar: raise window {id}");
                    self.focus_and_activate(&w);
                }
            }
            _ => {}
        }
        true
    }
}
