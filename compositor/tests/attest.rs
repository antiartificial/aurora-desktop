//! Bus-attested domain identity on the trusted path: a real headless compositor,
//! a fake busd speaking the real `bus.inspect@v1` / `bus.log@v1` shapes, and
//! the compositor's own dumped frame for what was drawn. (The real busd and a
//! real in-domain helper: controller/demo-attested.sh.)

mod common;
use common::*;
use std::{
    io::{BufRead, BufReader, Write},
    os::unix::net::UnixListener,
    path::PathBuf,
    time::{SystemTime, UNIX_EPOCH},
};

const MISMATCH: [u8; 3] = [255, 40, 40];
const ATTESTED: [u8; 3] = [120, 255, 160];
const CLAIM: [u8; 3] = [235, 160, 255];
const BANNER: [u8; 3] = [255, 200, 0];
const A: &str = "app-demo-aaaaaaaaaaaaaaaa";
const IDA: &str = "domain:app-demo-aaaaaaaaaaaaaaaa:1111111111111111";
const IDB: &str = "domain:app-demo-bbbbbbbbbbbbbbbb:2222222222222222";

/// A busd with domains A and B registered and attested, whose log holds `events`
/// (sender, request id) published as approval.preflight for the standard request.
fn fake_bus(dir: &std::path::Path, events: Vec<(&'static str, &'static str)>) -> PathBuf {
    let path = dir.join("bus.sock");
    let l = UnixListener::bind(&path).unwrap();
    std::thread::spawn(move || {
        for s in l.incoming().flatten() {
            let events = events.clone();
            std::thread::spawn(move || {
                let mut line = String::new();
                let _ = BufReader::new(&s).read_line(&mut line);
                let req: serde_json::Value = serde_json::from_str(&line).unwrap();
                let payload = match req["verb"].as_str().unwrap() {
                    "bus.inspect@v1" => serde_json::json!({
                        "clients": [], "services": [], "subscriptions": [], "handles": [], "clock": "x",
                        "domains": [{"identity": IDA, "socket": "/a", "cgroup": "/aurora-domains/a"}, {"identity": IDB, "socket": "/b", "cgroup": "/aurora-domains/b"}]}),
                    // The compositor is not the only thing that reads the log: the
                    // deskbar polls net.conn and the indicator polls
                    // bus.cap.minted, on this same socket. Asserting the topic
                    // here made the test fail whenever one of those polls landed
                    // before the preflight, which under load it usually does.
                    // Answer per topic and let the preflight assertions do the work.
                    "bus.log@v1" if req["payload"]["topic"] != "approval.preflight" => {
                        serde_json::json!({"messages": []})
                    }
                    "bus.log@v1" => {
                        let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
                        let msgs: Vec<_> = events.iter().map(|(from, id)| serde_json::json!({
                            "kind": "event", "topic": "approval.preflight", "from": from, "ts": format!("{now:019}-00000000000000000001"),
                            "payload": {"request_id": id, "app": "org.aurora.demo", "verb": "delete@v1", "effect": "destructive",
                                        "grants": ["rw /home/user/Pictures/holiday.png (gesture)"]}})).collect();
                        serde_json::json!({"messages": msgs})
                    }
                    // Anything else the compositor asks for gets an empty answer.
                    //
                    // This used to panic. The compositor has grown bus consumers
                    // since this fake was written (bus.subscribe@v1,
                    // registry.apps@v1, system.audio@v1 from the deskbar and the
                    // registry), and a panic here kills the handler thread without
                    // replying, so the connection dies and the test blocks on a
                    // reply that is never coming. The test is about attestation;
                    // it should not break every time something else learns to talk
                    // to the bus.
                    _ => serde_json::json!({}),
                };
                let reply = serde_json::json!({"id": "s1", "kind": "response", "corr": req["id"], "verb": req["verb"], "payload": payload});
                let _ = writeln!(&s, "{reply}");
            });
        }
    });
    path
}

fn claim(id: &str, name: &str, identity: &str) -> String {
    let mut v: serde_json::Value = serde_json::from_str(&request_json(id, 60_000)).unwrap();
    v["domain"] = name.into();
    v["domain_identity"] = identity.into();
    v.to_string()
}

fn run(events: Vec<(&'static str, &'static str)>, line: &str) -> (serde_json::Value, Img) {
    let d = tempfile::tempdir().unwrap();
    let bus = fake_bus(d.path(), events);
    let mut c = Comp::start(&["--bus-socket", bus.to_str().unwrap()]);
    c.client();
    let p = ask(&c, line);
    wait_for("the compositor to dump its own frame", || c.dump.exists());
    let img = Img::load_file(&c.dump);
    wait_until_armed();
    c.inject("key a");
    (answer(p), img)
}

#[test]
fn a_request_from_inside_the_domain_shows_the_bus_attested_identity() {
    let (a, img) = run(vec![(IDA, "q1")], &claim("q1", A, IDA));
    println!("BUS-ATTESTED reply: {a}");
    assert_eq!((a["domain_source"].as_str(), a["domain_verified"].as_str(), a["domain_mismatch"].as_str()), (Some("bus"), Some("true"), Some("false")), "{a}");
    assert_eq!(a["domain_identity"], IDA);
    assert!(a["domain_verify_detail"].as_str().unwrap().contains("published from inside the domain"), "{a}");
    assert_eq!(a["decision"], "allow");
    assert!(img.count(ATTESTED) > 100, "no bus-attested line drawn");
    assert_eq!((img.count(MISMATCH), img.count(BANNER), img.count(CLAIM)), (0, 0, 0));
}

#[test]
fn a_controller_naming_another_domain_than_the_bus_attests_is_overruled_and_flagged() {
    // The controller says A; the event came from inside B.
    let (a, img) = run(vec![(IDB, "q2")], &claim("q2", A, IDA));
    println!("MISMATCH reply: {a}");
    assert_eq!(a["domain_source"], "bus", "{a}");
    assert_eq!(a["domain_identity"], IDB, "the bus's answer must be the one shown: {a}");
    assert_eq!(a["domain_mismatch"], "true", "{a}");
    assert_eq!(a["decision"], "allow", "the prompt must still be answerable");
    assert!(img.count(MISMATCH) > 3000, "no mismatch banner: {}", img.count(MISMATCH));
    assert!(img.count(ATTESTED) > 100);
}

#[test]
fn a_same_uid_process_naming_a_real_domain_with_no_bus_event_is_shown_as_a_claim() {
    // Nothing in the log for q3 (an event exists, for another request id).
    let (a, img) = run(vec![(IDA, "other")], &claim("q3", A, IDA));
    println!("NO-EVENT reply: {a}");
    assert_eq!(a["domain_source"], "claim", "{a}");
    assert_eq!(a["domain_verified"], "true", "the domain does exist: {a}");
    assert!(a["domain_verify_detail"].as_str().unwrap().contains("no approval.preflight event"), "{a}");
    assert_eq!(a["decision"], "allow", "the prompt must still be shown and answerable");
    assert!(img.count(CLAIM) > 100, "no claim note drawn");
    assert_eq!((img.count(ATTESTED), img.count(MISMATCH)), (0, 0));
}

#[test]
fn an_event_is_accepted_once_and_conflicting_senders_attest_nothing() {
    let d = tempfile::tempdir().unwrap();
    let bus = fake_bus(d.path(), vec![(IDA, "q4"), (IDB, "q4"), (IDA, "q5")]);
    let mut c = Comp::start(&["--bus-socket", bus.to_str().unwrap()]);
    c.client();
    let n = std::cell::Cell::new(0);
    let first = |c: &Comp, line: String| {
        let p = ask(c, &line);
        n.set(n.get() + 1);
        wait_for("the request to be queued", || c.log().matches("queued").count() >= n.get());
        wait_until_armed();
        c.inject("key a");
        answer(p)
    };
    let a = first(&c, claim("q4", A, IDA));
    assert_eq!(a["domain_source"], "claim", "two domains for one id: {a}");
    assert!(a["domain_verify_detail"].as_str().unwrap().contains("several domains"), "{a}");
    let a = first(&c, claim("q5", A, IDA));
    assert_eq!(a["domain_source"], "bus", "{a}");
    let a = first(&c, claim("q5", A, IDA)); // replay of the same request id
    assert_eq!(a["domain_source"], "claim", "{a}");
    assert!(a["domain_verify_detail"].as_str().unwrap().contains("already attested"), "{a}");
}
