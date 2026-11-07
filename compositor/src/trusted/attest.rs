//! Learning which domain asked from the BUS, not from the controller.
//!
//! Before it shows a prompt the controller runs `aurora-preflight` inside the
//! app's domain. That helper publishes `approval.preflight` on the domain's own
//! bus socket, and busd stamps the event's `from` with the identity it derives
//! from the accepting socket plus the connector's uid/cgroup/namespaces. The
//! compositor reads that event back from the bus log by request id. Nothing the
//! controller types into its request decides the identity.
//!
//! What an event must satisfy to count: topic `approval.preflight`, `from` of
//! the form `domain:<name>:<id>` (a session-socket sender is `uid..:comm` and is
//! ignored), same request id, same app, verb, effect and grants as the prompt
//! request (so an old event cannot be replayed under a different request), a
//! timestamp within `FRESH` of now, never used for a prompt before, and the
//! domain still registered and cgroup-attested in busd. Events for one request
//! id from two different domains are a conflict and attest nothing.
//!
//! What this does not establish: see README.md, "Domain verification".

use std::{
    collections::{HashSet, VecDeque},
    path::Path,
    sync::{Arc, Mutex},
    time::{SystemTime, UNIX_EPOCH},
};

use serde_json::Value;

use super::{
    Request,
    draw::clean,
    verify::{self, Verdict, Verification},
};

pub const TOPIC: &str = "approval.preflight";
const FRESH_NS: i128 = 60_000_000_000;
const REMEMBER: usize = 4096;

/// Request ids already used to attest a prompt. A recorded event is accepted once.
#[derive(Clone, Default)]
pub struct Used(Arc<Mutex<(HashSet<String>, VecDeque<String>)>>);

impl Used {
    /// True the first time `id` is claimed.
    pub fn claim(&self, id: &str) -> bool {
        let mut g = self.0.lock().unwrap();
        if !g.0.insert(id.to_string()) {
            return false;
        }
        g.1.push_back(id.to_string());
        if g.1.len() > REMEMBER {
            if let Some(old) = g.1.pop_front() {
                g.0.remove(&old);
            }
        }
        true
    }
}

enum Found {
    None(String),
    One(String),
    Conflict(Vec<String>),
}

fn wall_ns(ts: &str) -> Option<i128> {
    ts.get(..19)?.parse().ok()
}

fn identity_ok(from: &str) -> bool {
    let p: Vec<&str> = from.split(':').collect();
    p.len() == 3 && p[0] == "domain" && !p[1].is_empty() && !p[2].is_empty()
}

fn same(v: &Value, want: &str) -> bool {
    v.as_str().is_some_and(|s| clean(s) == want)
}

/// Pick the attested sender of `req` out of a `bus.log@v1` reply.
fn find(log: &Value, req: &Request, now_ns: i128) -> Found {
    let (mut ids, mut ignored) = (Vec::<String>::new(), Vec::<String>::new());
    for m in log["messages"].as_array().into_iter().flatten() {
        let p = &m["payload"];
        if m["kind"] != "event" || m["topic"] != TOPIC || p["request_id"].as_str().map(clean).as_deref() != Some(&req.request_id) {
            continue;
        }
        let from = m["from"].as_str().unwrap_or("");
        let age = wall_ns(m["ts"].as_str().unwrap_or("")).map(|w| (now_ns - w).abs());
        let grants: Vec<&Value> = p["grants"].as_array().map(|g| g.iter().collect()).unwrap_or_default();
        let why = if !identity_ok(from) {
            Some(format!("sender {:?} is not a domain identity", clean(from)))
        } else if age.is_none_or(|a| a > FRESH_NS) {
            Some("event is stale".to_string())
        } else if !(same(&p["app"], &req.app) && same(&p["verb"], &req.verb) && same(&p["effect"], &req.effect)
            && grants.len() == req.grants.len() && grants.iter().zip(&req.grants).all(|(g, w)| same(g, w)))
        {
            Some("event content differs from the prompt request".to_string())
        } else {
            None
        };
        match why {
            None if !ids.iter().any(|i| i == from) => ids.push(from.to_string()),
            None => {}
            Some(w) => ignored.push(w),
        }
    }
    match ids.len() {
        0 if ignored.is_empty() => Found::None("no approval.preflight event for this request id in the bus log".into()),
        0 => Found::None(format!("no usable approval.preflight event ({})", ignored.join("; "))),
        1 => Found::One(ids.remove(0)),
        _ => Found::Conflict(ids),
    }
}

/// Decide what the prompt shows. Blocking; call off the main loop. On return
/// `req.verification` is set, and when the bus attested a domain `req.domain`
/// and `req.domain_identity` are overwritten with the bus's answer.
pub fn resolve(sock: Option<&Path>, req: &mut Request, used: &Used) {
    let claim = verify::check(sock, &req.domain, &req.domain_identity);
    // An unreachable or hung bus is not asked a second time.
    let Some(sock) = sock.filter(|_| !req.request_id.is_empty() && claim.verdict != Verdict::Unknown) else {
        req.verification = claim.claim_note("no bus event could be looked up");
        return;
    };
    let now = SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_nanos() as i128);
    let q = serde_json::json!({"topic": TOPIC, "since": "2m", "limit": 1000});
    let found = match verify::call(sock, "bus.log@v1", Some(q)) {
        Ok(log) => find(&log, req, now),
        Err(e) => Found::None(format!("bus log unreadable: {e}")),
    };
    let bid = match found {
        Found::None(why) => return req.verification = claim.claim_note(&why),
        Found::Conflict(ids) => {
            let why = format!("events for this request id came from several domains: {}", ids.join(", "));
            return req.verification = claim.claim_note(&why);
        }
        Found::One(id) => id,
    };
    let name = bid.split(':').nth(1).unwrap_or("").to_string();
    let judged = match verify::inspect(sock) {
        Ok(r) => verify::judge(&r, &name, &bid),
        Err(e) => return req.verification = claim.claim_note(&format!("bus event found but busd could not be asked: {e}")),
    };
    if judged.verdict != Verdict::Verified {
        let why = format!("bus event from {} but busd no longer vouches for it: {}", clean(&bid), judged.detail);
        return req.verification = claim.claim_note(&why);
    }
    if !used.claim(&req.request_id) {
        return req.verification = claim.claim_note("this request id already attested a prompt; not accepted twice");
    }
    let mismatch = (req.domain_identity != bid).then(|| format!("{} ({})", req.domain, req.domain_identity));
    let d = format!("published from inside the domain over its own bus socket, stamped by busd; {}", judged.detail);
    req.domain = name;
    req.domain_identity = bid;
    req.verification = Verification::attested(d, mismatch);
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn req() -> Request {
        Request {
            request_id: "r1".into(), app: "a".into(), domain: "x".into(), domain_identity: "domain:x:1".into(),
            verification: Verification::unchecked(), verb: "v".into(), effect: "destructive".into(), grants: vec!["rw /p (gesture)".into()], args: String::new(), request_sha256: String::new(),
        }
    }

    fn ev(from: &str, ts_ns: i128, verb: &str) -> Value {
        json!({"kind": "event", "topic": TOPIC, "from": from, "ts": format!("{ts_ns:019}-00000000000000000001"),
               "payload": {"request_id": "r1", "app": "a", "verb": verb, "effect": "destructive", "grants": ["rw /p (gesture)"]}})
    }

    #[test]
    fn only_a_fresh_matching_domain_event_counts() {
        let now = 10_000_000_000_000i128;
        let one = |e: Value| find(&json!({"messages": [e]}), &req(), now);
        assert!(matches!(one(ev("domain:y:2", now, "v")), Found::One(i) if i == "domain:y:2"));
        assert!(matches!(one(ev("uid1000:bus", now, "v")), Found::None(w) if w.contains("not a domain identity")));
        assert!(matches!(one(ev("domain:y:2", now - 3 * FRESH_NS, "v")), Found::None(w) if w.contains("stale")));
        assert!(matches!(one(ev("domain:y:2", now, "other")), Found::None(w) if w.contains("differs")));
        assert!(matches!(find(&json!({"messages": []}), &req(), now), Found::None(_)));
    }

    #[test]
    fn two_domains_for_one_request_id_is_a_conflict() {
        let now = 10_000_000_000_000i128;
        let log = json!({"messages": [ev("domain:y:2", now, "v"), ev("domain:z:3", now, "v"), ev("domain:y:2", now, "v")]});
        assert!(matches!(find(&log, &req(), now), Found::Conflict(i) if i.len() == 2));
    }

    #[test]
    fn a_request_id_attests_once() {
        let u = Used::default();
        assert!(u.claim("a"));
        assert!(!u.claim("a"));
        assert!(u.claim("b"));
    }
}
