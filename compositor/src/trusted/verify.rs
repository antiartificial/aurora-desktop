//! Checking a claimed domain against busd, from the compositor's own
//! connection to the bus session socket.
//!
//! What this establishes, and no more: busd has a domain registered under the
//! claimed identity, with a cgroup it attests connections against. It does NOT
//! establish that the request came from that domain, nor that the invoke will
//! run in it: the compositor cannot know either. Only the controller knows
//! which domain an invoke runs in, and the compositor takes the controller's
//! word for that association (see README.md, "Domain verification").
//!
//! Read path: `bus.inspect@v1` on the session socket, the same reply
//! `bus inspect --json` prints. busd has no narrower read-only verb, and bus/
//! is not ours to extend, so the compositor fetches the whole listing and picks
//! out the one domain. The listing includes clients, services and handle ids
//! (never tokens); the compositor reads only `domains` and `clients[].domain`.

use std::{
    io::{BufRead, BufReader, Read, Write},
    os::unix::net::UnixStream,
    path::Path,
    time::{Duration, Instant},
};

use serde_json::Value;

use super::draw::clean;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Verdict {
    /// busd knows the domain and attests it.
    Verified,
    /// busd answered and does not vouch for the domain.
    Unverified,
    /// busd could not be asked (not configured, unreachable, bad reply).
    Unknown,
}

impl Verdict {
    /// The wire word in the reply: "true", "false" or "unknown".
    pub fn wire(self) -> &'static str {
        match self {
            Verdict::Verified => "true",
            Verdict::Unverified => "false",
            Verdict::Unknown => "unknown",
        }
    }
}

#[derive(Debug, Clone)]
pub struct Verification {
    pub verdict: Verdict,
    /// Printable-ASCII reason, shown on the overlay and returned in the reply.
    pub detail: String,
}

impl Verification {
    fn new(verdict: Verdict, detail: impl Into<String>) -> Self {
        Verification { verdict, detail: clean(&detail.into()) }
    }
    pub fn unchecked() -> Self {
        Self::new(Verdict::Unknown, "not checked")
    }
}

const DEADLINE: Duration = Duration::from_millis(1500);
const MAX_REPLY: u64 = 8 << 20;

/// Ask busd about `identity` (e.g. `domain:app-x-1234:3d7a6e38a013ffaa`),
/// claimed under display name `name`. Blocking: call it off the main loop.
pub fn check(bus_sock: Option<&Path>, name: &str, identity: &str) -> Verification {
    if identity.is_empty() {
        return Verification::new(Verdict::Unverified, "the request names no domain identity to check");
    }
    let Some(sock) = bus_sock else {
        return Verification::new(Verdict::Unknown, "no bus socket configured (--bus-socket / AURORA_BUS_SOCK)");
    };
    match inspect(sock) {
        Ok(reply) => judge(&reply, name, identity),
        Err(e) => Verification::new(Verdict::Unknown, format!("bus unreachable or unreadable: {e}")),
    }
}

fn inspect(sock: &Path) -> Result<Value, String> {
    let start = Instant::now();
    let s = UnixStream::connect(sock).map_err(|e| format!("connect {}: {e}", sock.display()))?;
    s.set_write_timeout(Some(DEADLINE)).ok();
    let id = "v1";
    let req = serde_json::json!({"id": id, "kind": "request", "to": "bus", "verb": "bus.inspect@v1"});
    (&s).write_all(format!("{req}\n").as_bytes()).map_err(|e| format!("write: {e}"))?;
    let mut rd = BufReader::new((&s).take(MAX_REPLY));
    loop {
        let left = DEADLINE.checked_sub(start.elapsed()).ok_or("timed out")?;
        s.set_read_timeout(Some(left.max(Duration::from_millis(1)))).ok();
        let mut line = String::new();
        if rd.read_line(&mut line).map_err(|e| format!("read: {e}"))? == 0 {
            return Err("bus closed the connection".into());
        }
        let Ok(env) = serde_json::from_str::<Value>(&line) else { continue };
        if env["corr"] != id {
            continue;
        }
        return match env["kind"].as_str() {
            Some("response") => Ok(env["payload"].clone()),
            _ => Err(format!("bus refused bus.inspect@v1: {}", env["payload"])),
        };
    }
}

/// Decide from a `bus.inspect@v1` payload. Verified needs ALL of: a registered
/// domain whose identity is exactly the claim, whose identity names the same
/// domain as the display name, and which has a cgroup (busd attests
/// connections to it; without one busd cannot tell the domain's processes
/// from anyone else's).
pub fn judge(reply: &Value, name: &str, identity: &str) -> Verification {
    let parts: Vec<&str> = identity.split(':').collect();
    if parts.len() != 3 || parts[0] != "domain" || parts[1].is_empty() || parts[2].is_empty() {
        return Verification::new(Verdict::Unverified, "claimed identity is not of the form domain:<name>:<id>");
    }
    if parts[1] != name {
        return Verification::new(Verdict::Unverified, format!("claimed name {name:?} does not match its identity"));
    }
    let Some(domains) = reply["domains"].as_array() else {
        return Verification::new(Verdict::Unknown, "bus reply has no domain list");
    };
    let Some(d) = domains.iter().find(|d| d["identity"] == identity) else {
        return Verification::new(Verdict::Unverified, "busd has no domain registered under this identity");
    };
    if d["cgroup"].as_str().unwrap_or("").is_empty() {
        return Verification::new(Verdict::Unverified, "registered, but without a cgroup busd cannot attest it");
    }
    let live = reply["clients"].as_array().map_or(0, |c| c.iter().filter(|c| c["domain"] == identity).count());
    Verification::new(Verdict::Verified, format!("registered and cgroup-attested by busd; {live} live connection(s) now"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    const ID: &str = "domain:alpha:3d7a6e38a013ffaa";

    fn reply(cg: &str) -> Value {
        json!({"domains": [{"identity": ID, "socket": "/s", "cgroup": cg}], "clients": [{"domain": ID}, {"domain": "uid:x"}]})
    }

    #[test]
    fn verified_needs_registration_matching_name_and_a_cgroup() {
        assert_eq!(judge(&reply("/a/b"), "alpha", ID).verdict, Verdict::Verified);
        assert!(judge(&reply("/a/b"), "alpha", ID).detail.contains("1 live"));
        assert_eq!(judge(&reply(""), "alpha", ID).verdict, Verdict::Unverified);
        assert_eq!(judge(&reply("/a/b"), "beta", ID).verdict, Verdict::Unverified);
        assert_eq!(judge(&reply("/a/b"), "alpha", "domain:alpha:ffff").verdict, Verdict::Unverified);
        assert_eq!(judge(&reply("/a/b"), "alpha", "uid:alpha:1").verdict, Verdict::Unverified);
        assert_eq!(judge(&json!({}), "alpha", ID).verdict, Verdict::Unknown);
    }

    #[test]
    fn no_identity_or_no_bus_is_never_verified() {
        assert_eq!(check(None, "a", "").verdict, Verdict::Unverified);
        assert_eq!(check(None, "alpha", ID).verdict, Verdict::Unknown);
        assert_eq!(check(Some(Path::new("/nonexistent/bus.sock")), "alpha", ID).verdict, Verdict::Unknown);
    }
}
