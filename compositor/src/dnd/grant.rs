//! Asking the controller to turn a drop into a grant (decision 0020, steps 3 and 4).
//!
//! The compositor cannot mount anything and does not try: grants are the controller's. What it knows
//! that the controller cannot is that a drop happened, on a surface under the pointer, and which
//! domain the kernel put the target client in (`crate::peer`, from its cgroup). It says that, over a
//! Unix socket the controller binds (`controllerd --drop-socket`), in one line of JSON, and gets one
//! line back: the paths the domain can now see. The wire format is pinned by
//! `controller/testdata/drop-wire.json`, which both sides' tests read.
//!
//! Who it talks to is checked on the connection, the way the controller checks the compositor: the peer
//! must be the same user and outside any domain cgroup (the trusted socket's `Allowlist`). A same-user
//! process can still squat the socket; it would learn which file the user dropped and could answer with
//! paths, which is why the reply is not trusted either: every guest path must sit under
//! `/run/aurora/drops/`, and a reply that names anything else is refused as a whole.

use std::io::{BufRead, BufReader, Read, Write};
use std::os::unix::net::UnixStream;
use std::path::Path;
use std::time::Duration;

use serde::Deserialize;

use super::uri::Resolved;
use crate::trusted::socket::{Allowlist, peer_cred};

/// Where a domain sees a drop.
pub const GUEST_ROOT: &str = "/run/aurora/drops/";
/// How long to wait for the controller: an approval prompt (a directory) can legitimately take a minute.
pub const REPLY_WITHIN: Duration = Duration::from_secs(90);
const MAX_REPLY: u64 = 64 * 1024;

#[derive(Deserialize, Debug)]
struct Reply {
    #[serde(default)]
    ok: bool,
    #[serde(default)]
    error: String,
    #[serde(default)]
    code: String,
    #[serde(default)]
    domain: String,
    #[serde(default)]
    grants: Vec<Granted>,
}

#[derive(Deserialize, Debug)]
struct Granted {
    host: String,
    guest: String,
}

/// What the controller granted: in-domain paths, in the order the user dropped them.
#[derive(Debug, PartialEq, Eq)]
pub struct Grant {
    pub guest: Vec<String>,
}

/// The line sent to the controller.
pub fn request_line(domain: &str, paths: &[Resolved]) -> String {
    let p: Vec<&str> = paths.iter().filter_map(|r| r.path.to_str()).collect();
    serde_json::json!({"v": 1, "op": "drop", "domain": domain, "paths": p, "mode": "ro"}).to_string()
}

/// Judge a reply. A guest path outside [`GUEST_ROOT`] (or with `..`), a reply for another domain, or a
/// grant for a path that was not asked about refuses everything: a controller that cannot be trusted to
/// answer inside its own sandbox directory is not trusted with the rest.
pub fn parse_reply(line: &str, domain: &str, asked: &[Resolved]) -> Result<Grant, String> {
    let r: Reply = serde_json::from_str(line).map_err(|e| format!("unreadable reply from the controller: {e}"))?;
    if !r.ok {
        let why = if r.error.is_empty() { "refused".to_string() } else { r.error };
        return Err(format!("the controller refused the grant ({}): {why}", if r.code.is_empty() { "no code" } else { &r.code }));
    }
    if r.domain != domain {
        return Err(format!("the controller answered for domain {:?}, not {domain:?}", r.domain));
    }
    if r.grants.is_empty() {
        return Err("the controller granted nothing".into());
    }
    let mut guest = Vec::new();
    for g in &r.grants {
        let ok = g.guest.starts_with(GUEST_ROOT) && !g.guest.contains("..") && !g.guest.contains('\0') && !g.guest.contains('\n');
        if !ok {
            return Err(format!("the controller named a path outside {GUEST_ROOT}: {:?}", g.guest.chars().take(80).collect::<String>()));
        }
        if !asked.iter().any(|a| a.path.to_str() == Some(g.host.as_str())) {
            return Err("the controller granted a path that was not dropped".into());
        }
        guest.push(g.guest.clone());
    }
    Ok(Grant { guest })
}

/// Is the other end of this connection a process we would talk to? Same rule as the trusted socket.
pub fn peer_ok(allow: &Allowlist, uid: u32, pid: i32, cgroup_file: Option<&str>) -> Result<(), String> {
    allow.check(uid, pid, cgroup_file)
}

/// One request, one reply, blocking. Call it off the main loop.
pub fn ask(socket: &Path, allow: &Allowlist, domain: &str, paths: &[Resolved]) -> Result<Grant, String> {
    let s = UnixStream::connect(socket).map_err(|e| format!("cannot reach the controller at {}: {e}", socket.display()))?;
    let (uid, pid) = peer_cred(&s).map_err(|e| format!("no peer credentials: {e}"))?;
    let cg = std::fs::read_to_string(format!("/proc/{pid}/cgroup")).ok();
    peer_ok(allow, uid, pid, cg.as_deref()).map_err(|e| format!("not talking to {}: {e}", socket.display()))?;
    s.set_read_timeout(Some(REPLY_WITHIN)).ok();
    s.set_write_timeout(Some(Duration::from_secs(5))).ok();
    let mut w = &s;
    writeln!(w, "{}", request_line(domain, paths)).map_err(|e| format!("cannot send to the controller: {e}"))?;
    // Do NOT half-close: the controller reads a hang-up as "withdrawn".
    let mut line = String::new();
    BufReader::new(s.take(MAX_REPLY)).read_line(&mut line).map_err(|e| format!("no reply from the controller: {e}"))?;
    parse_reply(line.trim_end(), domain, paths)
}

/// The answer given to the target: `text/uri-list` naming the in-domain paths.
pub fn rewritten_list(g: &Grant) -> Vec<u8> {
    g.guest.iter().map(|p| format!("{}\r\n", super::uri::to_uri(p))).collect::<String>().into_bytes()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn r(p: &str) -> Resolved {
        Resolved { path: PathBuf::from(p), dir: false }
    }

    #[test]
    fn the_request_is_the_documented_line() {
        let fix: serde_json::Value = serde_json::from_str(include_str!("../../../controller/testdata/drop-wire.json")).unwrap();
        let want = &fix["request"];
        let got: serde_json::Value = serde_json::from_str(&request_line("win-text-1a2b3c4d-aabbcc", &[r("/home/u/photo.png")])).unwrap();
        assert_eq!(&got, want, "controller/testdata/drop-wire.json and the compositor disagree about the request");
    }

    #[test]
    fn the_documented_reply_is_understood() {
        let fix: serde_json::Value = serde_json::from_str(include_str!("../../../controller/testdata/drop-wire.json")).unwrap();
        let got = parse_reply(&fix["reply"].to_string(), "win-text-1a2b3c4d-aabbcc", &[r("/home/u/photo.png")]).unwrap();
        assert_eq!(got.guest, vec!["/run/aurora/drops/1/photo.png"]);
        assert_eq!(rewritten_list(&got), b"file:///run/aurora/drops/1/photo.png\r\n");
    }

    fn reply(guest: &str, host: &str, domain: &str) -> String {
        serde_json::json!({"v": 1, "ok": true, "domain": domain, "grants": [{"id": "1", "host": host, "guest": guest}]}).to_string()
    }

    #[test]
    fn a_reply_that_is_not_what_was_asked_is_refused_whole() {
        let asked = [r("/home/u/a.png")];
        let d = "win-x";
        assert!(parse_reply(&reply("/run/aurora/drops/1/a.png", "/home/u/a.png", d), d, &asked).is_ok());
        for (name, line) in [
            ("outside the drops root", reply("/etc/passwd", "/home/u/a.png", d)),
            ("a traversal", reply("/run/aurora/drops/../bus.sock", "/home/u/a.png", d)),
            ("a path nobody dropped", reply("/run/aurora/drops/1/b.png", "/home/u/b.png", d)),
            ("another domain", reply("/run/aurora/drops/1/a.png", "/home/u/a.png", "win-other")),
            ("a newline smuggled into the list", reply("/run/aurora/drops/1/a\nfile:///etc/shadow", "/home/u/a.png", d)),
            ("not json", "nonsense".to_string()),
            ("no grants", serde_json::json!({"ok": true, "domain": d, "grants": []}).to_string()),
            ("a refusal", serde_json::json!({"ok": false, "code": "approval_denied", "error": "denied"}).to_string()),
        ] {
            assert!(parse_reply(&line, d, &asked).is_err(), "{name} was accepted");
        }
    }

    #[test]
    fn the_controllers_own_refusal_is_carried_to_the_log() {
        let e = parse_reply(r#"{"ok":false,"code":"no_such_window","error":"domain is not a window"}"#, "d", &[]).unwrap_err();
        assert!(e.contains("no_such_window") && e.contains("not a window"), "{e}");
    }

    #[test]
    fn a_peer_in_a_domain_or_of_another_user_is_not_the_controller() {
        let allow = Allowlist { uids: vec![1000], ..Default::default() };
        let host = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/controllerd.service\n";
        let dom = "0::/user.slice/user-1000.slice/user@1000.service/aurora-domains/win-x\n";
        assert!(peer_ok(&allow, 1000, 5, Some(host)).is_ok());
        assert!(peer_ok(&allow, 1001, 5, Some(host)).is_err());
        assert!(peer_ok(&allow, 1000, 5, Some(dom)).is_err());
        assert!(peer_ok(&allow, 1000, 5, None).is_err());
    }
}
