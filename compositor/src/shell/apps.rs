//! Where the dock learns which apps exist: `registry.apps@v1`, asked of the
//! controller's registry service over the bus session socket (the same socket
//! and the same wire the domain verification already uses), off the main
//! thread so a slow or hung bus never stalls a frame.

use std::{path::PathBuf, sync::mpsc, time::{Duration, Instant}};

use serde_json::{Value, json};

use crate::trusted::verify::call_to;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AppEntry {
    pub id: String,
    pub name: String,
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
const MAX_APPS: usize = 24;

/// Untrusted display text: control characters out, length bounded.
pub fn display_name(s: &str) -> String {
    let t: String = s.chars().map(|c| if c.is_control() { '?' } else { c }).collect();
    if t.chars().count() > 28 { format!("{}...", t.chars().take(25).collect::<String>()) } else { t }
}

pub fn parse(reply: &Value) -> Result<Vec<AppEntry>, String> {
    let apps = reply["apps"].as_array().ok_or("reply has no apps list")?;
    Ok(apps
        .iter()
        .filter_map(|a| {
            let id = a["id"].as_str().filter(|i| !i.is_empty())?;
            let name = a["name"].as_str().filter(|n| !n.is_empty()).unwrap_or(id);
            Some(AppEntry { id: id.to_string(), name: display_name(name) })
        })
        .take(MAX_APPS)
        .collect())
}

pub struct Registry {
    pub sock: Option<PathBuf>,
    pub apps: Vec<AppEntry>,
    pub status: Status,
    /// Bumped whenever `apps` or `status` changes.
    pub generation: u64,
    rx: Option<mpsc::Receiver<Result<Vec<AppEntry>, String>>>,
    last: Option<Instant>,
}

impl Registry {
    pub fn new(sock: Option<PathBuf>) -> Self {
        let status = if sock.is_some() { Status::Pending } else { Status::NoBus };
        Registry { sock, apps: vec![], status, generation: 0, rx: None, last: None }
    }

    /// Collect a finished ask and start the next one when due. Cheap; call per frame.
    pub fn tick(&mut self) {
        if let Some(rx) = &self.rx {
            match rx.try_recv() {
                Ok(r) => {
                    self.rx = None;
                    let (apps, status) = match r {
                        Ok(a) => (a, Status::Ok),
                        Err(e) => (self.apps.clone(), Status::Failed(e)),
                    };
                    if apps != self.apps || status != self.status {
                        tracing::info!("dock: registry.apps@v1 -> {status:?}, {} app(s): {:?}", apps.len(), apps.iter().map(|a| a.id.as_str()).collect::<Vec<_>>());
                        self.apps = apps;
                        self.status = status;
                        self.generation += 1;
                    }
                }
                Err(mpsc::TryRecvError::Empty) => return,
                Err(mpsc::TryRecvError::Disconnected) => self.rx = None,
            }
        }
        let Some(sock) = self.sock.clone() else { return };
        if self.last.is_some_and(|t| t.elapsed() < REFRESH) {
            return;
        }
        self.last = Some(Instant::now());
        let (tx, rx) = mpsc::channel();
        self.rx = Some(rx);
        std::thread::spawn(move || {
            let r = call_to(&sock, "registry", "registry.apps@v1", Some(json!({}))).and_then(|v| parse(&v));
            let _ = tx.send(r);
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_the_registry_reply_and_cleans_names() {
        let v = json!({"apps": [{"id": "org.aurora.lumen", "name": "Lumen", "version": "0.1.0", "verbs": ["resize@v1"]}, {"id": "x.y", "name": "bad\u{1b}name"}, {"name": "no id"}]});
        let a = parse(&v).unwrap();
        assert_eq!(a.len(), 2);
        assert_eq!(a[0], AppEntry { id: "org.aurora.lumen".into(), name: "Lumen".into() });
        assert_eq!(a[1].name, "bad?name");
        assert!(parse(&json!({})).is_err());
        assert!(display_name(&"x".repeat(100)).chars().count() <= 28);
    }
}
