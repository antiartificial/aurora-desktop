//! Domain verification on the trusted path. A real headless compositor, a
//! fake busd that speaks the real `bus.inspect@v1` wire shape, and the
//! compositor's own dumped frame for what was drawn. (The real busd is
//! exercised by controller/demo-verified.sh.)

mod common;
use common::*;
use std::{
    io::{BufRead, BufReader, Write},
    os::unix::net::UnixListener,
    path::PathBuf,
    time::Duration,
};

const BANNER: [u8; 3] = [255, 200, 0];
const VERIFIED: [u8; 3] = [80, 200, 255];
const ID: &str = "domain:app-demo-0123456789abcdef:3d7a6e38a013ffaa";
const NAME: &str = "app-demo-0123456789abcdef";

/// Serve `bus.inspect@v1` with one registered domain (cgroup set) and one live client.
/// `answer == false` accepts and never replies.
fn fake_bus(dir: &std::path::Path, answer: bool) -> PathBuf {
    let path = dir.join("bus.sock");
    let l = UnixListener::bind(&path).unwrap();
    std::thread::spawn(move || {
        for s in l.incoming().flatten() {
            std::thread::spawn(move || {
                let mut line = String::new();
                let _ = BufReader::new(&s).read_line(&mut line);
                if !answer {
                    std::thread::sleep(Duration::from_secs(30));
                    return;
                }
                let req: serde_json::Value = serde_json::from_str(&line).unwrap();
                assert_eq!((req["to"].as_str(), req["verb"].as_str()), (Some("bus"), Some("bus.inspect@v1")));
                let reply = serde_json::json!({"id": "s1", "kind": "response", "corr": req["id"], "verb": "bus.inspect@v1", "payload": {
                    "clients": [{"id": 1, "domain": ID, "pid": 1, "uid": 1000}],
                    "services": [], "subscriptions": [], "handles": [],
                    "domains": [{"identity": ID, "socket": "/x/bus.sock", "cgroup": "/user.slice/aurora-domains/demo"}],
                    "clock": "x"}});
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

fn run(extra: &[&str], line: &str) -> (serde_json::Value, Img) {
    let mut c = Comp::start(extra);
    c.client();
    let p = ask(&c, line);
    wait_for("the compositor to dump its own frame", || c.dump.exists());
    let img = Img::load_file(&c.dump);
    c.inject("key a"); // the prompt still appears, and is still answerable
    (answer(p), img)
}

#[test]
fn a_domain_busd_knows_and_attests_displays_verified() {
    let d = tempfile::tempdir().unwrap();
    let bus = fake_bus(d.path(), true);
    let (a, img) = run(&["--bus-socket", bus.to_str().unwrap()], &claim("v1", NAME, ID));
    println!("VERIFIED reply: {a}");
    assert_eq!(a["domain_verified"], "true", "{a}");
    assert!(a["domain_verify_detail"].as_str().unwrap().contains("1 live connection"), "{a}");
    assert_eq!(a["decision"], "allow");
    assert_eq!(img.count(BANNER), 0, "a verified domain got the unverified banner");
    assert!(img.count(VERIFIED) > 100, "no verified line drawn: {}", img.count(VERIFIED));
}

#[test]
fn a_domain_busd_does_not_know_displays_unverified_and_still_prompts() {
    let d = tempfile::tempdir().unwrap();
    let bus = fake_bus(d.path(), true);
    let fake = "domain:app-demo-0123456789abcdef:ffffffffffffffff";
    let (a, img) = run(&["--bus-socket", bus.to_str().unwrap()], &claim("v2", NAME, fake));
    println!("UNVERIFIED reply: {a}");
    assert_eq!(a["domain_verified"], "false", "{a}");
    assert!(a["domain_verify_detail"].as_str().unwrap().contains("no domain registered"), "{a}");
    assert_eq!(a["decision"], "allow", "the prompt must still be answerable");
    assert!(img.count(BANNER) > 3000, "no unverified banner: {}", img.count(BANNER));
    assert_eq!(img.count(VERIFIED), 0);
}

#[test]
fn a_name_that_disagrees_with_its_identity_is_unverified() {
    let d = tempfile::tempdir().unwrap();
    let bus = fake_bus(d.path(), true);
    let (a, _) = run(&["--bus-socket", bus.to_str().unwrap()], &claim("v3", "app-other", ID));
    assert_eq!(a["domain_verified"], "false", "{a}");
}

#[test]
fn an_unreachable_bus_gives_unknown_not_verified_and_still_prompts() {
    let d = tempfile::tempdir().unwrap();
    let gone = d.path().join("no-bus.sock");
    let (a, img) = run(&["--bus-socket", gone.to_str().unwrap()], &claim("v4", NAME, ID));
    println!("UNKNOWN reply: {a}");
    assert_eq!(a["domain_verified"], "unknown", "{a}");
    assert_eq!(a["decision"], "allow");
    assert!(img.count(BANNER) > 3000);
    assert_eq!(img.count(VERIFIED), 0);
}

#[test]
fn a_bus_that_hangs_gives_unknown_after_its_deadline_and_cannot_suppress_the_prompt() {
    let d = tempfile::tempdir().unwrap();
    let bus = fake_bus(d.path(), false);
    let t = std::time::Instant::now();
    let (a, img) = run(&["--bus-socket", bus.to_str().unwrap()], &claim("v5", NAME, ID));
    assert_eq!(a["domain_verified"], "unknown", "{a}");
    assert!(t.elapsed() < Duration::from_secs(8), "waited too long on a hung bus");
    assert!(img.count(BANNER) > 3000);
}
