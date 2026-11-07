//! The Deskbar's one connection to busd: how it learns which hosts a domain has connected to.
//!
//! It is the indicator's bus client (`indicator/bus.rs`) with a different list of topics: a thread,
//! never the render loop, holds a session-socket connection subscribed to `net.conn` and `net.summary`,
//! replays a short stretch of the bus log so a compositor that starts mid-run is not blind, and sends
//! the model's whole state to the loop through a channel whenever it changes. The session socket is
//! the only one that may read `net.` (a domain connection is refused system namespaces, decision 0015),
//! and the compositor is the user's own process, so it is the connection class `verify.rs` already uses.
//!
//! Why not share the indicator's thread: it reconciles against `bus.inspect@v1` and stops watching when
//! nothing is held; this one has no handles to reconcile and must stay subscribed. Two small threads
//! that each do one thing were judged simpler than one that does both.

use std::{
    io::{BufRead, BufReader, Write},
    os::unix::net::UnixStream,
    path::PathBuf,
    time::Duration,
};

use serde_json::{Value, json};
use smithay::reexports::calloop::{LoopHandle, channel};

use super::model::{NET_CONN, NET_SUMMARY, Net, NetModel};
use crate::{Aurora, trusted::verify};

const RETRY: Duration = Duration::from_secs(1);
/// How far back the log is replayed at connect. The proxy's counters are per run and a summary leaves
/// every 30 s, so this is long enough to cover a compositor restart and short enough that a domain that
/// ran this morning is not painted onto an app that started a minute ago.
const REPLAY_SINCE: &str = "15m";

pub fn spawn(handle: &LoopHandle<'static, Aurora>, sock: PathBuf) {
    let (tx, rx) = channel::channel::<Net>();
    let _ = handle.insert_source(rx, |ev, _, state| {
        if let channel::Event::Msg(n) = ev {
            tracing::info!("deskbar: bus snapshot up={} domains={}", n.up, n.domains.len());
            state.deskbar.net = n;
        }
    });
    let _ = std::thread::Builder::new().name("deskbar-bus".into()).spawn(move || run(sock, tx));
}

fn run(sock: PathBuf, tx: channel::Sender<Net>) {
    let mut model = NetModel::default();
    let mut last: Option<Net> = None;
    let mut send = |model: &NetModel, up: bool| {
        let n = Net { domains: model.domains(), up };
        if last.as_ref() != Some(&n) {
            last = Some(n.clone());
            let _ = tx.send(n);
        }
    };
    loop {
        match session(&sock, &mut model, &mut |m| send(m, true)) {
            Ok(()) => {}
            Err(e) => tracing::warn!("deskbar: bus session ended: {e}"),
        }
        send(&model, false);
        std::thread::sleep(RETRY);
    }
}

fn session(sock: &PathBuf, model: &mut NetModel, publish: &mut dyn FnMut(&NetModel)) -> Result<(), String> {
    let s = UnixStream::connect(sock).map_err(|e| format!("connect {}: {e}", sock.display()))?;
    for (i, t) in [NET_CONN, NET_SUMMARY].iter().enumerate() {
        let req = json!({"id": format!("sub{i}"), "kind": "request", "to": "bus", "verb": "bus.subscribe@v1", "payload": {"topic": t}});
        (&s).write_all(format!("{req}\n").as_bytes()).map_err(|e| format!("subscribe: {e}"))?;
    }
    // Subscribed first, replayed second: an event between the two arrives twice, and the model drops the second by id.
    for t in [NET_CONN, NET_SUMMARY] {
        let q = json!({"topic": t, "since": REPLAY_SINCE, "limit": 5000});
        if let Ok(log) = verify::call(sock, "bus.log@v1", Some(q)) {
            for m in log["messages"].as_array().into_iter().flatten() {
                model.apply(m);
            }
        }
    }
    publish(model);
    let mut rd = BufReader::new(s.try_clone().map_err(|e| e.to_string())?);
    s.set_read_timeout(Some(Duration::from_millis(500))).ok();
    let mut line = String::new();
    loop {
        match rd.read_line(&mut line) {
            Ok(0) => return Err("bus closed the connection".into()),
            Ok(_) if line.ends_with('\n') => {
                if let Ok(env) = serde_json::from_str::<Value>(&line) {
                    if model.apply(&env) {
                        publish(model);
                    }
                }
                line.clear();
            }
            Ok(_) => {} // half a line; read_line keeps it in `line`
            Err(e) if matches!(e.kind(), std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut) => {}
            Err(e) => return Err(format!("read: {e}")),
        }
    }
}
