//! What the Deskbar says, as data, before any pixel: the applications and their windows, what is known
//! about each, and the words for it. Pure, so the honesty rule is tested here rather than hoped for in
//! the rasteriser: a figure that is `None` has no text, and there is no path from `None` to "0%".

use std::collections::{BTreeMap, HashMap};

use serde_json::Value;

/// Where a window's client process runs, as far as the kernel told us.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub enum Dom {
    /// The kernel placed the client outside every domain: it shares the session's cgroup, so it has no figures of its own.
    None,
    In { name: String },
    /// Cannot be placed (an X11 window's client is Xwayland, whose cgroup says nothing about the app).
    /// Said as such: "not in a domain" would be a claim about a process we could not look at.
    Unknown(&'static str),
}

impl Dom {
    pub fn name(&self) -> Option<&str> {
        match self {
            Dom::In { name, .. } => Some(name),
            _ => None,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Win {
    pub id: String,
    pub title: String,
    pub focused: bool,
}

/// One row of applications. Grouped by (what the client calls itself, where it runs), NOT by name
/// alone: an `app_id` is a claim any client can make, so a confined window that calls itself
/// `org.aurora.terminal` gets its own row, with its domain on it, instead of being folded into the
/// real one and borrowing its look.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Group {
    pub key: String,
    pub label: String,
    pub dom: Dom,
    pub wins: Vec<Win>,
}

#[derive(Clone, Copy, Debug, PartialEq, Default)]
pub struct Figures {
    /// Percent of ONE core, so a busy multi-threaded app can pass 100.
    pub cpu: Option<f32>,
    pub mem: Option<u64>,
    pub procs: Option<u32>,
}

/// The whole machine. Percent of all CPU time, and of RAM in use.
#[derive(Clone, Copy, Debug, PartialEq, Default)]
pub struct SysFigures {
    pub cpu: Option<f32>,
    pub mem: Option<f32>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub enum Verdict {
    Allowed,
    Refused,
    Failed,
}

impl Verdict {
    pub fn parse(s: &str) -> Option<Verdict> {
        match s {
            "allowed" => Some(Verdict::Allowed),
            "refused" => Some(Verdict::Refused),
            "failed" => Some(Verdict::Failed),
            _ => None,
        }
    }
    pub fn word(self) -> &'static str {
        match self {
            Verdict::Allowed => "allowed",
            Verdict::Refused => "refused",
            Verdict::Failed => "failed",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct NetHost {
    pub host: String,
    pub verdict: Verdict,
    /// Connections counted by `net.summary` events. 0 means "seen (a `net.conn`), count not yet reported":
    /// the proxy sends a summary every 30 s, so a count can trail the host by that long.
    pub count: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct DomainNet {
    pub hosts: Vec<NetHost>,
}

impl DomainNet {
    pub fn refused(&self) -> usize {
        self.hosts.iter().filter(|h| h.verdict == Verdict::Refused).count()
    }
}

/// What the bus thread last learned about the network, whole.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct Net {
    pub domains: BTreeMap<String, DomainNet>,
    /// False while busd is unreachable: what is shown is the last thing known.
    pub up: bool,
}

pub const NET_CONN: &str = "net.conn";
pub const NET_SUMMARY: &str = "net.summary";
const MAX_HOSTS: usize = 64;
const MAX_DOMAINS: usize = 64;
const SEEN_IDS: usize = 4096;

/// Folds `net.conn` and `net.summary` events (decision 0015) into per-domain host tallies.
#[derive(Default)]
pub struct NetModel {
    domains: BTreeMap<String, DomainNet>,
    seen: std::collections::VecDeque<String>,
}

impl NetModel {
    pub fn domains(&self) -> BTreeMap<String, DomainNet> {
        self.domains.clone()
    }

    fn entry(&mut self, domain: &str, host: &str, v: Verdict) -> Option<&mut NetHost> {
        if !self.domains.contains_key(domain) && self.domains.len() >= MAX_DOMAINS {
            return None;
        }
        let d = self.domains.entry(domain.to_string()).or_default();
        let i = match d.hosts.iter().position(|h| h.host == host && h.verdict == v) {
            Some(i) => i,
            None if d.hosts.len() >= MAX_HOSTS => return None,
            None => {
                d.hosts.push(NetHost { host: host.to_string(), verdict: v, count: 0 });
                d.hosts.len() - 1
            }
        };
        d.hosts.get_mut(i)
    }

    /// Apply one bus envelope. True if what is shown changed. Counts are ADDED (a summary is the delta
    /// since the last one), so an envelope seen twice (subscribe-then-replay overlap) must be dropped
    /// by id, unlike the indicator's idempotent set.
    pub fn apply(&mut self, env: &Value) -> bool {
        let topic = env["topic"].as_str().unwrap_or("");
        if topic != NET_CONN && topic != NET_SUMMARY {
            return false;
        }
        if let Some(id) = env["id"].as_str() {
            if self.seen.iter().any(|s| s == id) {
                return false;
            }
            self.seen.push_back(id.to_string());
            if self.seen.len() > SEEN_IDS {
                self.seen.pop_front();
            }
        }
        let p = &env["payload"];
        let Some(domain) = p["domain"].as_str().filter(|d| !d.is_empty()) else { return false };
        let mut changed = false;
        if topic == NET_CONN {
            let (Some(host), Some(v)) = (p["host"].as_str(), p["verdict"].as_str().and_then(Verdict::parse)) else { return false };
            let before = self.domains.get(domain).map_or(0, |d| d.hosts.len());
            self.entry(domain, host, v);
            changed = self.domains.get(domain).map_or(0, |d| d.hosts.len()) != before;
        } else {
            for e in p["entries"].as_array().into_iter().flatten() {
                let (Some(host), Some(v)) = (e["host"].as_str(), e["verdict"].as_str().and_then(Verdict::parse)) else { continue };
                let n = e["count"].as_u64().unwrap_or(0);
                if let Some(h) = self.entry(domain, host, v) {
                    h.count = h.count.saturating_add(n);
                    changed = true;
                }
            }
        }
        changed
    }
}

/// How a CPU share reads. A real zero is "0.0%", a real sliver is "<0.1%", and nothing is ever rounded
/// to a plausible 0 that the reading did not say.
pub fn cpu_text(p: f32) -> String {
    if p > 0.0 && p < 0.1 {
        "<0.1%".into()
    } else if p < 10.0 {
        format!("{p:.1}%")
    } else {
        format!("{p:.0}%")
    }
}

pub fn mem_text(bytes: u64) -> String {
    const MIB: u64 = 1 << 20;
    if bytes >= 1024 * MIB { format!("{:.1} GB", bytes as f64 / (1024 * MIB) as f64) } else { format!("{} MB", bytes.div_ceil(MIB)) }
}

pub fn plural(n: usize, one: &str, many: &str) -> String {
    format!("{n} {}", if n == 1 { one } else { many })
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Tone {
    Dim,
    Warn,
}

/// Text from outside (a window title, a host a proxy reported) as it may be drawn: control characters and
/// the Unicode formatting characters that reorder or hide text become `?`. A title that ends in a
/// right-to-left override must not make "evil.example" read as something else.
pub fn safe(s: &str) -> String {
    s.chars().map(|c| if c.is_control() || matches!(c, '\u{200b}'..='\u{200f}' | '\u{202a}'..='\u{202e}' | '\u{2066}'..='\u{2069}' | '\u{feff}') { '?' } else { c }).collect()
}

/// The lines under an application's name, each a list of segments joined by " \u{b7} ".
///
/// Every segment is something that was read. Not in a domain says that and nothing else. In a domain
/// lists only the figures that exist, and when none do, says "in a domain" rather than anything numeric.
/// The network, which can be long and which carries the refusals a person most wants to see, is its own
/// line so that a narrow box cuts the figures and never the word "refused".
pub fn meta(g: &Group, fig: Option<&Figures>, net: Option<&DomainNet>) -> Vec<Vec<(String, Tone)>> {
    match &g.dom {
        Dom::None => vec![vec![("not in a domain".into(), Tone::Dim)]],
        Dom::Unknown(why) => vec![vec![(format!("domain unknown ({why})"), Tone::Dim)]],
        Dom::In { .. } => {
            let mut a: Vec<(String, Tone)> = vec![];
            if let Some(f) = fig {
                if let Some(c) = f.cpu {
                    a.push((format!("cpu {}", cpu_text(c)), Tone::Dim));
                }
                if let Some(m) = f.mem {
                    a.push((format!("mem {}", mem_text(m)), Tone::Dim));
                }
                if let Some(p) = f.procs {
                    a.push((plural(p as usize, "proc", "procs"), Tone::Dim));
                }
            }
            if a.is_empty() {
                a.push(("in a domain".into(), Tone::Dim));
            }
            let mut v = vec![a];
            if let Some(n) = net.filter(|n| !n.hosts.is_empty()) {
                let mut b = vec![(format!("net {}", plural(n.hosts.len(), "host", "hosts")), Tone::Dim)];
                if n.refused() > 0 {
                    b.push((format!("{} refused", n.refused()), Tone::Warn));
                }
                v.push(b);
            }
            v
        }
    }
}

/// Machine-wide header figures, only those that were read.
pub fn system_text(s: &SysFigures) -> Vec<String> {
    let mut v = vec![];
    if let Some(c) = s.cpu {
        v.push(format!("cpu {}", cpu_text(c)));
    }
    if let Some(m) = s.mem {
        v.push(format!("mem {m:.0}%"));
    }
    v
}

/// Stable ordering of groups and windows: by when each was first seen, never by stacking order, so
/// raising a window does not shuffle the list under the pointer. `order` maps a key to its sequence.
pub fn order(groups: &mut [Group], gseq: &HashMap<String, u64>, wseq: &HashMap<String, u64>) {
    for g in groups.iter_mut() {
        g.wins.sort_by_key(|w| wseq.get(&w.id).copied().unwrap_or(u64::MAX));
    }
    groups.sort_by_key(|g| gseq.get(&g.key).copied().unwrap_or(u64::MAX));
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn grp(dom: Dom) -> Group {
        Group { key: "k".into(), label: "App".into(), dom, wins: vec![] }
    }
    fn text(m: &[Vec<(String, Tone)>]) -> String {
        m.iter().map(|l| l.iter().map(|s| s.0.as_str()).collect::<Vec<_>>().join(" | ")).collect::<Vec<_>>().join(" / ")
    }
    fn in_dom() -> Dom {
        Dom::In { name: "d".into() }
    }

    #[test]
    fn an_app_outside_a_domain_gets_no_figure_even_if_figures_exist_for_its_name() {
        // Figures are keyed by domain; a stray entry must not leak onto a row that is not in one.
        let f = Figures { cpu: Some(12.0), mem: Some(1 << 30), procs: Some(4) };
        let m = text(&meta(&grp(Dom::None), Some(&f), None));
        assert_eq!(m, "not in a domain");
        assert!(!m.chars().any(|c| c.is_ascii_digit()));
        let m = text(&meta(&grp(Dom::Unknown("X11 window")), Some(&f), None));
        assert_eq!(m, "domain unknown (X11 window)");
    }

    #[test]
    fn a_domain_shows_only_the_figures_that_were_read() {
        assert_eq!(text(&meta(&grp(in_dom()), None, None)), "in a domain");
        let only_procs = Figures { cpu: None, mem: None, procs: Some(1) };
        assert_eq!(text(&meta(&grp(in_dom()), Some(&only_procs), None)), "1 proc");
        let all = Figures { cpu: Some(3.2), mem: Some(41 << 20), procs: Some(2) };
        assert_eq!(text(&meta(&grp(in_dom()), Some(&all), None)), "cpu 3.2% | mem 41 MB | 2 procs");
        let none_digits = text(&meta(&grp(in_dom()), Some(&Figures::default()), None));
        assert!(!none_digits.chars().any(|c| c.is_ascii_digit()), "an all-None reading says nothing numeric: {none_digits}");
    }

    #[test]
    fn cpu_text_never_rounds_a_real_reading_to_a_plausible_zero() {
        assert_eq!(cpu_text(0.0), "0.0%");
        assert_eq!(cpu_text(0.04), "<0.1%");
        assert_eq!(cpu_text(0.4), "0.4%");
        assert_eq!(cpu_text(9.96), "10.0%");
        assert_eq!(cpu_text(143.0), "143%");
    }

    #[test]
    fn text_from_outside_cannot_reorder_or_hide_itself() {
        assert_eq!(safe("a\u{202e}b\u{1b}[2Jc\u{200b}d"), "a?b?[2Jc?d");
        assert_eq!(safe("plain, and \u{e9}"), "plain, and \u{e9}");
    }

    #[test]
    fn memory_reads_in_megabytes_then_gigabytes() {
        assert_eq!(mem_text(1), "1 MB");
        assert_eq!(mem_text(41 << 20), "41 MB");
        assert_eq!(mem_text(3 << 30), "3.0 GB");
    }

    fn conn(id: &str, d: &str, host: &str, verdict: &str) -> Value {
        json!({"id": id, "topic": "net.conn", "payload": {"domain": d, "host": host, "port": 443, "verdict": verdict, "reason": "r", "first": true}})
    }
    fn summary(id: &str, d: &str, es: Value) -> Value {
        json!({"id": id, "topic": "net.summary", "payload": {"domain": d, "entries": es, "dropped": 0}})
    }

    #[test]
    fn a_refused_host_shows_at_once_and_a_summary_adds_its_count_once() {
        let mut m = NetModel::default();
        assert!(m.apply(&conn("1", "d", "evil.example", "refused")));
        let d = &m.domains()["d"];
        assert_eq!(d.refused(), 1);
        assert_eq!(d.hosts[0].count, 0, "seen, count not reported yet: no number");
        let s = summary("2", "d", json!([{"host": "evil.example", "verdict": "refused", "reason": "r", "count": 7}, {"host": "ok.example", "verdict": "allowed", "reason": "r", "count": 3}]));
        assert!(m.apply(&s));
        assert!(!m.apply(&s), "the same envelope twice (subscribe then replay) is not counted twice");
        let d = &m.domains()["d"];
        assert_eq!(d.hosts.iter().map(|h| (h.host.as_str(), h.count)).collect::<Vec<_>>(), [("evil.example", 7), ("ok.example", 3)]);
        // a later summary is a delta and accumulates
        assert!(m.apply(&summary("3", "d", json!([{"host": "ok.example", "verdict": "allowed", "reason": "r", "count": 2}]))));
        assert_eq!(m.domains()["d"].hosts[1].count, 5);
    }

    #[test]
    fn the_same_host_with_two_verdicts_is_two_rows_and_nonsense_is_ignored() {
        let mut m = NetModel::default();
        m.apply(&conn("1", "d", "a.example", "allowed"));
        m.apply(&conn("2", "d", "a.example", "refused"));
        assert_eq!(m.domains()["d"].hosts.len(), 2);
        assert!(!m.apply(&conn("3", "d", "a.example", "maybe")), "an unknown verdict is dropped, not guessed");
        assert!(!m.apply(&json!({"id": "4", "topic": "net.conn", "payload": {"host": "x", "verdict": "allowed"}})), "no domain, no row");
        assert!(!m.apply(&json!({"id": "5", "topic": "other.topic", "payload": {}})));
    }

    #[test]
    fn the_tables_are_bounded() {
        let mut m = NetModel::default();
        for i in 0..200 {
            m.apply(&conn(&format!("h{i}"), "d", &format!("h{i}.example"), "allowed"));
            m.apply(&conn(&format!("d{i}"), &format!("dom{i}"), "x.example", "allowed"));
        }
        assert!(m.domains()["d"].hosts.len() <= MAX_HOSTS);
        assert!(m.domains().len() <= MAX_DOMAINS);
    }

    #[test]
    fn net_in_the_meta_line_counts_hosts_and_marks_refusals_in_the_warn_tone() {
        let n = DomainNet { hosts: vec![NetHost { host: "a".into(), verdict: Verdict::Allowed, count: 4 }, NetHost { host: "b".into(), verdict: Verdict::Refused, count: 0 }] };
        let m = meta(&grp(in_dom()), None, Some(&n));
        assert_eq!(text(&m), "in a domain / net 2 hosts | 1 refused");
        assert_eq!(m.last().unwrap().last().unwrap().1, Tone::Warn);
        assert_eq!(text(&meta(&grp(Dom::None), None, Some(&n))), "not in a domain", "net is a domain's; an app outside one shows none");
    }

    #[test]
    fn groups_keep_the_order_they_first_appeared_in_whatever_the_stacking_does() {
        let mk = |k: &str, ws: &[&str]| Group { key: k.into(), label: k.into(), dom: Dom::None, wins: ws.iter().map(|w| Win { id: (*w).into(), title: String::new(), focused: false }).collect() };
        let mut g = vec![mk("b", &["w3", "w2"]), mk("a", &["w1"])];
        let gs: HashMap<String, u64> = [("a".to_string(), 1), ("b".to_string(), 2)].into();
        let ws: HashMap<String, u64> = [("w1".to_string(), 1), ("w2".to_string(), 2), ("w3".to_string(), 3)].into();
        order(&mut g, &gs, &ws);
        assert_eq!(g.iter().map(|g| g.key.as_str()).collect::<Vec<_>>(), ["a", "b"]);
        assert_eq!(g[1].wins.iter().map(|w| w.id.as_str()).collect::<Vec<_>>(), ["w2", "w3"]);
    }
}
