//! Where the dock learns which apps exist: `registry.apps@v1`, asked of the
//! controller's registry service over the bus session socket (the same socket
//! and the same wire the domain verification already uses), off the main
//! thread so a slow or hung bus never stalls a frame. The same ask also reads
//! `registry.verbs@v1`, which carries what the verb picker shows per verb:
//! its summary and its effect class.

use std::{
    path::PathBuf,
    sync::{
        Arc, mpsc,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};

use serde_json::{Value, json};

use crate::trusted::verify::call_to;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AppEntry {
    pub id: String,
    pub name: String,
    /// Verb names exactly as the manifest spells them (`resize@v1`), in manifest order.
    pub verbs: Vec<String>,
    /// Whether a click can do anything, and why not. Decided by the registry
    /// (`apps.App.Launchable`), not here: the shell must not stat app
    /// directories to work out what it may offer. An older registry that does
    /// not send the field is treated as launchable, so the dock does not grey
    /// out every app when talking to one.
    pub launchable: bool,
    pub problem: String,
    /// A windowed app (decision 0012) is launched directly: it opens its own
    /// window and has no verb to pick. `exec` is the resolved binary.
    pub windowed: bool,
    pub exec: String,
    /// The section token the manifest filed this app under, or `None` for an
    /// older registry or an app that names none. Presentation only. Its
    /// position is `Registry::categories`; this side never learns what a
    /// token means.
    pub category: Option<String>,
}

/// One verb as `registry.verbs@v1` describes it. Text here is untrusted
/// (it comes from a manifest); `name` is kept raw because it is passed on as
/// `--verb`, and is cleaned only where it is drawn.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct VerbEntry {
    pub app: String,
    pub name: String,
    pub effect: String,
    pub summary: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Status {
    /// No bus socket configured: nothing to ask.
    NoBus,
    /// Never answered yet.
    Pending,
    Ok,
    /// The last ask failed; the previous list (if any) is kept.
    Failed(String),
}

const REFRESH: Duration = Duration::from_secs(5);
/// Bound on an untrusted reply, not on the dock: `layout.rs` draws one pill per item that fits.
const MAX_APPS: usize = 256;
const MAX_VERBS: usize = 256;
const MAX_CATEGORIES: usize = 32;

/// Untrusted display text: control characters out, length bounded.
pub fn display_name(s: &str) -> String {
    let t: String = s.chars().map(|c| if c.is_control() { '?' } else { c }).collect();
    if t.chars().count() > 28 { format!("{}...", t.chars().take(25).collect::<String>()) } else { t }
}

/// Bounded, control characters out, for text shown in a row (a summary is longer than a name).
pub fn display_text(s: &str, max: usize) -> String {
    let t: String = s.chars().map(|c| if c.is_control() { '?' } else { c }).collect();
    if t.chars().count() > max { format!("{}...", t.chars().take(max.saturating_sub(3)).collect::<String>()) } else { t }
}

pub fn parse_verbs(reply: &Value) -> Result<Vec<VerbEntry>, String> {
    let verbs = reply["verbs"].as_array().ok_or("reply has no verbs list")?;
    Ok(verbs
        .iter()
        .filter_map(|v| {
            let name = v["name"].as_str().filter(|n| !n.is_empty())?;
            let app = v["app"].as_str().filter(|a| !a.is_empty())?;
            Some(VerbEntry { app: app.into(), name: name.into(), effect: v["effect"].as_str().unwrap_or("").into(), summary: v["summary"].as_str().unwrap_or("").into() })
        })
        .take(MAX_VERBS)
        .collect())
}

/// The category tokens in the order the registry sent them; an app's position in this list is
/// its section's sort key. A reply without the list (an older registry) yields none.
pub fn parse_categories(reply: &Value) -> Vec<String> {
    reply["categories"]
        .as_array()
        .map(|c| c.iter().filter_map(|c| c["id"].as_str().filter(|i| !i.is_empty())).map(|i| display_text(i, 32)).take(MAX_CATEGORIES).collect())
        .unwrap_or_default()
}

pub fn parse(reply: &Value) -> Result<Vec<AppEntry>, String> {
    let apps = reply["apps"].as_array().ok_or("reply has no apps list")?;
    Ok(apps
        .iter()
        .filter_map(|a| {
            let id = a["id"].as_str().filter(|i| !i.is_empty())?;
            let name = a["name"].as_str().filter(|n| !n.is_empty()).unwrap_or(id);
            let verbs = a["verbs"].as_array().map(|v| v.iter().filter_map(|n| n.as_str().filter(|n| !n.is_empty()).map(String::from)).collect()).unwrap_or_default();
            let launchable = a["launchable"].as_bool().unwrap_or(true);
            let problem = display_text(a["launch_problem"].as_str().unwrap_or(""), 90);
            let windowed = a["windowed"].as_bool().unwrap_or(false);
            let exec = a["exec"].as_str().unwrap_or("").to_string();
            let category = a["category"].as_str().filter(|c| !c.is_empty()).map(|c| display_text(c, 32));
            Some(AppEntry { id: id.to_string(), name: display_name(name), verbs, launchable, problem, windowed, exec, category })
        })
        .take(MAX_APPS)
        .collect())
}

/// One finished ask. `verbs` is `None` when `registry.verbs@v1` failed: the
/// apps are still good, but their effect classes are then unknown, and the
/// picker says so rather than guessing.
type Snapshot = (Vec<AppEntry>, Vec<String>, Option<Vec<VerbEntry>>);

pub struct Registry {
    pub sock: Option<PathBuf>,
    pub apps: Vec<AppEntry>,
    /// Category tokens in display order, from `registry.apps@v1`.
    pub categories: Vec<String>,
    /// Details for the verbs in `apps`; `None` until `registry.verbs@v1` has answered.
    pub verbs: Option<Vec<VerbEntry>>,
    pub status: Status,
    /// Bumped whenever `apps` or `status` changes.
    pub generation: u64,
    /// The last audio state the bus answered with, or None when nothing did.
    pub audio: Option<Audio>,
    audio_rx: Option<mpsc::Receiver<Option<Audio>>>,
    rx: Option<mpsc::Receiver<Result<Snapshot, String>>>,
    last: Option<Instant>,
    /// Set by a launch that just finished: ask again now instead of at the next refresh, so a
    /// failed launch's `launch_problem` shows on the dock at once.
    pub refresh_now: Arc<AtomicBool>,
}

impl Registry {
    pub fn new(sock: Option<PathBuf>) -> Self {
        let status = if sock.is_some() { Status::Pending } else { Status::NoBus };
        Registry { sock, apps: vec![], categories: vec![], verbs: None, status, generation: 0, audio: None, audio_rx: None, rx: None, last: None, refresh_now: Arc::new(AtomicBool::new(false)) }
    }

    /// Collect a finished ask and start the next one when due. Cheap; call per frame.
    pub fn tick(&mut self) {
        if let Some(rx) = &self.rx {
            if let Some(arx) = &self.audio_rx {
                if let Ok(a) = arx.try_recv() {
                    self.audio = a;
                    self.audio_rx = None;
                }
            }
            match rx.try_recv() {
                Ok(r) => {
                    self.rx = None;
                    let (apps, categories, verbs, status) = match r {
                        Ok((a, c, v)) => (a, c, v, Status::Ok),
                        Err(e) => (self.apps.clone(), self.categories.clone(), self.verbs.clone(), Status::Failed(e)),
                    };
                    if apps != self.apps || categories != self.categories || verbs != self.verbs || status != self.status {
                        tracing::info!("dock: registry.apps@v1 -> {status:?}, {} app(s): {:?}", apps.len(), apps.iter().map(|a| a.id.as_str()).collect::<Vec<_>>());
                        self.apps = apps;
                        self.categories = categories;
                        self.verbs = verbs;
                        self.status = status;
                        self.generation += 1;
                    }
                }
                Err(mpsc::TryRecvError::Empty) => return,
                Err(mpsc::TryRecvError::Disconnected) => self.rx = None,
            }
        }
        let Some(sock) = self.sock.clone() else { return };
        if self.last.is_some_and(|t| t.elapsed() < REFRESH) && !self.refresh_now.swap(false, Ordering::Relaxed) {
            return;
        }
        self.last = Some(Instant::now());
        let (tx, rx) = mpsc::channel();
        self.rx = Some(rx);
        let (atx, arx) = mpsc::channel();
        self.audio_rx = Some(arx);
        std::thread::spawn(move || {
            // Audio rides along on the registry's existing poll rather than opening a second
            // one. It is a figure that changes when a person moves a slider, so five seconds
            // late is visible -- but a thread of its own, dialling the bus on its own clock,
            // would be a second moving part for a number in a status line.
            let audio = call_to(&sock, "registry", "system.audio@v1", Some(json!({}))).ok().and_then(|v| parse_audio(&v));
            let r = call_to(&sock, "registry", "registry.apps@v1", Some(json!({}))).and_then(|v| Ok((parse(&v)?, parse_categories(&v)))).map(|(apps, categories)| {
                let verbs = call_to(&sock, "registry", "registry.verbs@v1", Some(json!({}))).and_then(|v| parse_verbs(&v));
                if let Err(e) = &verbs {
                    tracing::warn!("dock: registry.verbs@v1 failed ({e}); verb effects are unknown");
                }
                (apps, categories, verbs.ok())
            });
            let _ = atx.send(audio);
            let _ = tx.send(r);
        });
    }
}

/// The audio state from `system.audio@v1`.
///
/// `available` false is the whole point of the type: it is the difference between "the machine
/// is at 0%" and "nothing answered", and a status line must not render the second as the first.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Audio {
    pub volume: i64,
    pub muted: bool,
}

fn parse_audio(v: &serde_json::Value) -> Option<Audio> {
    if !v.get("available").and_then(|a| a.as_bool()).unwrap_or(false) {
        return None;
    }
    Some(Audio {
        volume: v.get("volume").and_then(|x| x.as_i64()).unwrap_or(0).clamp(0, 1000),
        muted: v.get("muted").and_then(|x| x.as_bool()).unwrap_or(false),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn an_unavailable_sink_is_not_a_volume_of_zero() {
        // The reply shape when no audio server answered. Rendering this as "vol 0%" would be
        // the status line inventing a fact.
        assert_eq!(parse_audio(&json!({"available": false, "volume": 0, "reason": "none"})), None);
        // And a reply with no `available` field at all is treated the same way, not as present.
        assert_eq!(parse_audio(&json!({"volume": 40})), None);
    }

    #[test]
    fn an_available_sink_is_read_with_its_mute() {
        assert_eq!(parse_audio(&json!({"available": true, "volume": 45, "muted": true})), Some(Audio { volume: 45, muted: true }));
        // Over-amplification survives; the controller already decided 150 is expressible.
        assert_eq!(parse_audio(&json!({"available": true, "volume": 150})).unwrap().volume, 150);
    }

    #[test]
    fn parses_the_registry_reply_and_cleans_names() {
        let v = json!({"apps": [{"id": "org.aurora.lumen", "name": "Lumen", "version": "0.1.0", "verbs": ["resize@v1"]}, {"id": "x.y", "name": "bad\u{1b}name"}, {"name": "no id"}]});
        let a = parse(&v).unwrap();
        assert_eq!(a.len(), 2);
        assert_eq!(
            a[0],
            AppEntry {
                id: "org.aurora.lumen".into(),
                name: "Lumen".into(),
                verbs: vec!["resize@v1".into()],
                // a reply without the field means an older registry: assume launchable
                launchable: true,
                problem: String::new(),
                windowed: false,
                exec: String::new(),
                category: None,
            }
        );
        assert!(a[1].verbs.is_empty());
        assert_eq!(a[1].name, "bad?name");
        assert!(parse(&json!({})).is_err());
        assert!(display_name(&"x".repeat(100)).chars().count() <= 28);
    }

    #[test]
    fn parses_category_and_order() {
        let v = json!({
            "apps": [{"id": "a.b", "name": "Ab", "category": "media"}, {"id": "c.d", "category": ""}],
            "categories": [{"id": "files", "label": "Files"}, {"id": "media", "label": "Media"}, {"label": "no id"}],
        });
        let a = parse(&v).unwrap();
        assert_eq!(a[0].category.as_deref(), Some("media"));
        assert_eq!(a[1].category, None);
        assert_eq!(parse_categories(&v), vec!["files".to_string(), "media".to_string()]);
    }

    #[test]
    fn an_older_registry_without_either_field_still_parses() {
        let v = json!({"apps": [{"id": "a.b", "name": "Ab", "verbs": ["x@v1"]}]});
        let a = parse(&v).unwrap();
        assert_eq!(a.len(), 1);
        assert_eq!(a[0].category, None);
        assert!(parse_categories(&v).is_empty());
    }

    #[test]
    fn caps_bound_an_oversized_reply() {
        let apps: Vec<Value> = (0..MAX_APPS * 2).map(|i| json!({"id": format!("a.b{i}"), "category": "x".repeat(500)})).collect();
        let cats: Vec<Value> = (0..MAX_CATEGORIES * 2).map(|i| json!({"id": format!("c{i}")})).collect();
        let v = json!({"apps": apps, "categories": cats});
        let a = parse(&v).unwrap();
        assert_eq!(a.len(), MAX_APPS);
        assert!(a[0].category.as_ref().unwrap().chars().count() <= 32);
        assert_eq!(parse_categories(&v).len(), MAX_CATEGORIES);
    }

    #[test]
    fn parses_verb_details_and_bounds_text() {
        let v = json!({"verbs": [{"name": "resize@v1", "app": "a.b", "effect": "write", "accepts": ["image/png"], "summary": "Resize\u{1b}"}, {"app": "a.b"}]});
        let r = parse_verbs(&v).unwrap();
        assert_eq!(r, vec![VerbEntry { app: "a.b".into(), name: "resize@v1".into(), effect: "write".into(), summary: "Resize\u{1b}".into() }]);
        assert_eq!(display_text(&r[0].summary, 60), "Resize?");
        assert!(display_text(&"x".repeat(200), 60).chars().count() <= 60);
        assert!(parse_verbs(&json!({})).is_err());
    }
}
