//! What the indicator believes is held, and why it believes it. Pure: no
//! sockets, no clock, no drawing, so every rule below has a unit test.
//!
//! A grant is "held" only when two statements agree about one capability id:
//!
//! 1. `bus.cap.minted`, then no `bus.cap.revoked`. The `bus.` namespace is the
//!    daemon's own voice (a client's publish to it is refused, bus/server/server.go),
//!    so existence, resource name, minter and time are busd's word. The minter
//!    must not be a `domain:` identity: a domain connection may call `bus.mint@v1`
//!    for any resource name, and the daemon would stamp that truthfully, so the
//!    event proves a domain minted a handle, not that the controller granted one.
//! 2. `provenance.grant.held`, published by the SAME connection that minted the
//!    handle (the event's `from`, stamped by busd from the connection, equals the
//!    handle's `minter`), naming the domain the grant was composed into. A domain
//!    publishing the topic is stamped `domain:...` and is not the minter.
//!
//! The domain name shown is parsed from the attested identity string
//! `domain:<name>:<id>`; nothing else in a payload is ever displayed.

use std::collections::HashMap;

use serde_json::Value;

use crate::trusted::verify::Verdict;

pub const MINTED: &str = "bus.cap.minted";
pub const REVOKED: &str = "bus.cap.revoked";
pub const HELD: &str = "provenance.grant.held";

/// One held capability, as the indicator shows it.
#[derive(Clone, Debug, PartialEq)]
pub struct Held {
    pub id: String,
    /// `device:wayland`: exactly what the daemon minted the handle for.
    pub resource: String,
    /// `domain:<name>:<id>`
    pub identity: String,
    pub name: String,
    /// Wall-clock nanoseconds when busd minted the handle.
    pub since_ns: i128,
    /// busd currently has this domain registered and cgroup-attested.
    pub verdict: Verdict,
}

#[derive(Clone, Debug)]
struct Minted {
    resource: String,
    minter: String,
    since_ns: i128,
}

#[derive(Clone, Debug)]
struct Attr {
    from: String,
    resource: String,
    identity: String,
}

#[derive(Default)]
struct Entry {
    minted: Option<Minted>,
    /// Every attribution seen, whoever sent it; only the minter's counts (see `held_entry`).
    attrs: Vec<Attr>,
    revoked: bool,
    verdict: Option<Verdict>,
}

#[derive(Default)]
pub struct Model {
    caps: HashMap<String, Entry>,
}

/// `domain:<name>:<id>` -> its name, or None when it is not that shape.
pub fn domain_name(identity: &str) -> Option<&str> {
    let p: Vec<&str> = identity.split(':').collect();
    (p.len() == 3 && p[0] == "domain" && !p[1].is_empty() && !p[2].is_empty()).then(|| p[1])
}

pub fn wall_ns(ts: &str) -> Option<i128> {
    ts.get(..19)?.parse().ok()
}

impl Model {
    /// Take one bus envelope (a live event or a log message). Returns true when it concerned a capability.
    pub fn apply(&mut self, env: &Value) -> bool {
        if env["kind"] != "event" {
            return false;
        }
        let p = &env["payload"];
        let from = env["from"].as_str().unwrap_or("");
        match env["topic"].as_str().unwrap_or("") {
            MINTED if from == "bus" => {
                let (Some(id), Some(res), Some(minter)) = (p["id"].as_str(), p["resource"].as_str(), p["minter"].as_str()) else { return false };
                let e = self.caps.entry(id.to_string()).or_default();
                e.minted = Some(Minted { resource: res.to_string(), minter: minter.to_string(), since_ns: wall_ns(env["ts"].as_str().unwrap_or("")).unwrap_or(0) });
                true
            }
            REVOKED if from == "bus" => {
                let Some(id) = p["id"].as_str() else { return false };
                self.caps.entry(id.to_string()).or_default().revoked = true;
                true
            }
            HELD => {
                let (Some(id), Some(res), Some(ident)) = (p["cap"].as_str(), p["resource"].as_str(), p["domain"].as_str()) else { return false };
                // Kept whoever sent it: an early forgery must not shadow the minter's own, which may come later.
                // The first one that passes `held_entry` stands, so a later one cannot re-point a grant.
                let e = self.caps.entry(id.to_string()).or_default();
                if e.attrs.len() < 8 {
                    e.attrs.push(Attr { from: from.to_string(), resource: res.to_string(), identity: ident.to_string() });
                }
                true
            }
            _ => false,
        }
    }

    pub fn set_verdict(&mut self, id: &str, v: Verdict) {
        if let Some(e) = self.caps.get_mut(id) {
            e.verdict = Some(v);
        }
    }

    /// busd's list of outstanding handle ids: a live one that is not in it was
    /// revoked or expired without an event we saw.
    pub fn reconcile(&mut self, outstanding: &[String]) {
        for (id, e) in self.caps.iter_mut() {
            if e.minted.is_some() && !e.revoked && !outstanding.iter().any(|o| o == id) {
                e.revoked = true;
            }
        }
    }

    fn held_entry(&self, id: &str) -> Option<(&Minted, &Attr)> {
        let e = self.caps.get(id)?;
        let m = e.minted.as_ref()?;
        if e.revoked || domain_name(&m.minter).is_some() || m.minter.is_empty() {
            return None;
        }
        let a = e.attrs.iter().find(|a| a.from == m.minter && a.resource == m.resource && domain_name(&a.identity).is_some())?;
        Some((m, a))
    }

    /// Everything held right now, oldest first.
    pub fn held(&self) -> Vec<Held> {
        let mut v: Vec<Held> = self
            .caps
            .iter()
            .filter_map(|(id, e)| {
                let (m, a) = self.held_entry(id)?;
                Some(Held {
                    id: id.clone(),
                    resource: m.resource.clone(),
                    identity: a.identity.clone(),
                    name: domain_name(&a.identity)?.to_string(),
                    since_ns: m.since_ns,
                    // Not asked yet is not vouched for.
                    verdict: e.verdict.unwrap_or(Verdict::Unknown),
                })
            })
            .collect();
        v.sort_by(|a, b| (a.since_ns, &a.id).cmp(&(b.since_ns, &b.id)));
        v
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    const ID: &str = "domain:alpha:1111";

    fn minted(id: &str, res: &str, minter: &str) -> Value {
        json!({"kind":"event","topic":MINTED,"from":"bus","ts":"1000000000000000000-0","payload":{"id":id,"resource":res,"by":minter,"minter":minter,"holder":minter}})
    }
    fn held(id: &str, res: &str, from: &str, who: &str) -> Value {
        json!({"kind":"event","topic":HELD,"from":from,"ts":"1000000000000000001-0","payload":{"cap":id,"resource":res,"domain":who}})
    }
    fn revoked(id: &str) -> Value {
        json!({"kind":"event","topic":REVOKED,"from":"bus","ts":"1000000000000000002-0","payload":{"id":id,"resource":"x","by":"c","minter":"c"}})
    }

    #[test]
    fn held_needs_the_daemons_mint_and_the_minters_own_attribution() {
        let mut m = Model::default();
        m.apply(&minted("cap:1", "device:wayland", "uid1000:ctl"));
        assert!(m.held().is_empty(), "minted, not yet composed into a domain");
        m.apply(&held("cap:1", "device:wayland", "uid1000:ctl", ID));
        let h = m.held();
        assert_eq!((h.len(), h[0].name.as_str(), h[0].since_ns), (1, "alpha", 1_000_000_000_000_000_000));
        m.apply(&revoked("cap:1"));
        assert!(m.held().is_empty(), "revoked");
    }

    #[test]
    fn order_does_not_matter_and_a_revoke_is_never_undone_by_a_replayed_mint() {
        let mut m = Model::default();
        m.apply(&held("cap:1", "device:wayland", "c", ID));
        m.apply(&revoked("cap:1"));
        m.apply(&minted("cap:1", "device:wayland", "c")); // replayed from the log after the live revoke
        assert!(m.held().is_empty());
    }

    #[test]
    fn a_domain_cannot_light_it_by_minting_or_by_publishing() {
        let mut m = Model::default();
        // a domain calls bus.mint@v1 for a device resource; the daemon truthfully names the domain as minter
        m.apply(&minted("cap:2", "device:wayland", ID));
        m.apply(&held("cap:2", "device:wayland", ID, ID));
        assert!(m.held().is_empty(), "minter is a domain identity");
        // a domain publishes the attribution for the CONTROLLER's real handle
        m.apply(&minted("cap:3", "device:wayland", "uid1000:ctl"));
        m.apply(&held("cap:3", "device:wayland", "domain:evil:2222", "domain:evil:2222"));
        assert!(m.held().is_empty(), "attribution did not come from the minter");
        // and a forged envelope claiming to be the daemon's but not from "bus"
        let mut f = minted("cap:4", "device:wayland", "uid1000:ctl");
        f["from"] = "domain:evil:2222".into();
        m.apply(&f);
        m.apply(&held("cap:4", "device:wayland", "uid1000:ctl", ID));
        assert!(m.held().is_empty(), "a mint not stamped by the daemon is not a mint");
    }

    #[test]
    fn the_attribution_must_name_the_resource_that_was_minted_and_a_real_identity() {
        let mut m = Model::default();
        m.apply(&minted("cap:5", "device:gpu", "c"));
        m.apply(&held("cap:5", "device:wayland", "c", ID));
        assert!(m.held().is_empty(), "resource differs from the minted one");
        m.apply(&minted("cap:6", "device:wayland", "c"));
        m.apply(&held("cap:6", "device:wayland", "c", "alpha"));
        assert!(m.held().is_empty(), "not of the form domain:<name>:<id>");
        m.apply(&held("cap:7", "device:wayland", "c", ID)); // no mint at all
        assert!(m.held().is_empty());
    }

    #[test]
    fn the_first_attribution_stands() {
        let mut m = Model::default();
        m.apply(&minted("cap:8", "device:wayland", "c"));
        m.apply(&held("cap:8", "device:wayland", "c", ID));
        m.apply(&held("cap:8", "device:wayland", "c", "domain:beta:3333"));
        assert_eq!(m.held()[0].name, "alpha");
    }

    #[test]
    fn a_forged_attribution_sent_first_does_not_shadow_the_real_one() {
        let mut m = Model::default();
        m.apply(&minted("cap:10", "device:wayland", "c"));
        m.apply(&held("cap:10", "device:wayland", "domain:evil:2222", "domain:evil:2222"));
        m.apply(&held("cap:10", "device:wayland", "c", ID));
        assert_eq!(m.held().len(), 1, "a domain must not be able to hide the indicator by speaking first");
        assert_eq!(m.held()[0].name, "alpha");
    }

    #[test]
    fn busd_dropping_a_handle_without_an_event_clears_it() {
        let mut m = Model::default();
        m.apply(&minted("cap:9", "device:wayland", "c"));
        m.apply(&held("cap:9", "device:wayland", "c", ID));
        m.reconcile(&["cap:9".into()]);
        assert_eq!(m.held().len(), 1);
        m.reconcile(&[]);
        assert!(m.held().is_empty(), "expired or daemon restarted");
    }
}
