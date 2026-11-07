//! The indicator's one connection to busd: how the compositor learns that a
//! grant is held, without anything the grant's holder says.
//!
//! A thread (never the render loop) keeps a session-socket connection
//! subscribed to the three topics `model.rs` reasons about, replays the bus
//! log once so a compositor that starts mid-grant still shows it, and, while
//! anything is held, asks `bus.inspect@v1` every `RECONCILE` for the handles
//! busd still has outstanding and for the registered, cgroup-attested domains.
//! The model's verdict goes to the loop through a channel as a whole snapshot.
//!
//! Why the session socket and not a domain socket: the compositor is the
//! user's own process. A domain's socket would stamp it `domain:...`, and busd
//! refuses system-namespace subscriptions on those (bus/server/server.go,
//! `systemPrefixes`). This is the same connection class `verify.rs` already uses.

use std::{
    io::{BufRead, BufReader, Write},
    os::unix::net::UnixStream,
    path::PathBuf,
    time::{Duration, Instant},
};

use serde_json::{Value, json};
use smithay::reexports::calloop::{LoopHandle, channel};

use super::model::{HELD, Held, MINTED, Model, REVOKED};
use crate::{Aurora, trusted::verify};

const RECONCILE: Duration = Duration::from_secs(2);
const RETRY: Duration = Duration::from_secs(1);
/// How far back the log is replayed at connect. A grant outlives its invoke by seconds, not hours.
const REPLAY_SINCE: &str = "6h";

/// What the worker tells the loop. Always the whole truth as the worker sees it.
pub struct Snapshot {
    pub held: Vec<Held>,
    /// False while the connection to busd is down: what is shown is the last thing known.
    pub up: bool,
}

pub fn spawn(handle: &LoopHandle<'static, Aurora>, sock: PathBuf) {
    let (tx, rx) = channel::channel::<Snapshot>();
    let _ = handle.insert_source(rx, |ev, _, state| {
        if let channel::Event::Msg(s) = ev {
            state.indicator.on_snapshot(s);
        }
    });
    let _ = std::thread::Builder::new().name("indicator-bus".into()).spawn(move || run(sock, tx));
}

fn run(sock: PathBuf, tx: channel::Sender<Snapshot>) {
    let mut model = Model::default();
    let mut last: Option<(Vec<Held>, bool)> = None;
    let send = |model: &Model, up: bool, last: &mut Option<(Vec<Held>, bool)>| {
        let held = model.held();
        if last.as_ref() != Some(&(held.clone(), up)) {
            *last = Some((held.clone(), up));
            let _ = tx.send(Snapshot { held, up });
        }
    };
    loop {
        match session(&sock, &mut model, &mut |m| send(m, true, &mut last)) {
            Ok(()) => {}
            Err(e) => tracing::warn!("indicator: bus session ended: {e}"),
        }
        send(&model, false, &mut last);
        std::thread::sleep(RETRY);
    }
}

fn session(sock: &PathBuf, model: &mut Model, publish: &mut dyn FnMut(&Model)) -> Result<(), String> {
    let s = UnixStream::connect(sock).map_err(|e| format!("connect {}: {e}", sock.display()))?;
    for (i, t) in [MINTED, REVOKED, HELD].iter().enumerate() {
        let req = json!({"id": format!("sub{i}"), "kind": "request", "to": "bus", "verb": "bus.subscribe@v1", "payload": {"topic": t}});
        (&s).write_all(format!("{req}\n").as_bytes()).map_err(|e| format!("subscribe: {e}"))?;
    }
    // Subscribed first, replayed second: an event between the two is seen twice (harmless, the model is idempotent), never zero times.
    for t in [MINTED, REVOKED, HELD] {
        let q = json!({"topic": t, "since": REPLAY_SINCE, "limit": 5000});
        if let Ok(log) = verify::call(sock, "bus.log@v1", Some(q)) {
            for m in log["messages"].as_array().into_iter().flatten() {
                model.apply(m);
            }
        }
    }
    reconcile(sock, model);
    publish(model);
    let mut rd = BufReader::new(s.try_clone().map_err(|e| e.to_string())?);
    s.set_read_timeout(Some(Duration::from_millis(500))).ok();
    let mut line = String::new();
    let mut next = Instant::now() + RECONCILE;
    loop {
        match rd.read_line(&mut line) {
            Ok(0) => return Err("bus closed the connection".into()),
            Ok(_) if line.ends_with('\n') => {
                if let Ok(env) = serde_json::from_str::<Value>(&line) {
                    if model.apply(&env) {
                        reconcile(sock, model);
                        publish(model);
                    }
                }
                line.clear();
            }
            Ok(_) => {} // half a line; read_line keeps it in `line`
            Err(e) if matches!(e.kind(), std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut) => {}
            Err(e) => return Err(format!("read: {e}")),
        }
        if Instant::now() >= next {
            next = Instant::now() + RECONCILE;
            if !model.held().is_empty() {
                reconcile(sock, model);
                publish(model);
            }
        }
    }
}

/// Ask busd what it has outstanding and which domains it vouches for; apply both.
fn reconcile(sock: &PathBuf, model: &mut Model) {
    let Ok(reply) = verify::inspect(sock) else { return };
    let ids: Vec<String> = reply["handles"].as_array().into_iter().flatten().filter_map(|h| h["id"].as_str().map(String::from)).collect();
    model.reconcile(&ids);
    for h in model.held() {
        let v = verify::judge(&reply, &h.name, &h.identity).verdict;
        model.set_verdict(&h.id, v);
    }
}
