//! Trusted-path tests. Each starts a real headless compositor and real
//! clients. Pixel claims are checked on decoded PNG pixels, not asserted.

mod common;
use common::*;
use std::time::Duration;

fn overlay_up(c: &Comp) -> Img {
    wait_for("the compositor to dump its own frame", || c.dump.exists());
    Img::load_file(&c.dump)
}

#[test]
fn control_capture_without_overlay_contains_the_client_window() {
    let mut c = Comp::start(&[]);
    c.client();
    let img = c.capture("plain.png");
    assert_eq!((img.w, img.h), (W, H));
    // client window is 1000x700 at (0,0), background is (25,25,25)
    assert_eq!(img.at(10, 10), CLIENT);
    assert_eq!(img.at(640, 400), CLIENT);
    assert_eq!(img.at(1200, 780), [25, 25, 25]);
    println!("CONTROL capture: (10,10)={:?} (640,400)={:?} (1200,780)={:?} border-coloured pixels={}", img.at(10, 10), img.at(640, 400), img.at(1200, 780), img.count(BORDER));
}

#[test]
fn capture_taken_while_overlay_is_up_does_not_contain_it() {
    let mut c = Comp::start(&[]);
    c.client();
    let before = c.capture("before.png");
    let pending = ask(&c, &request_json("r1", 60_000));
    // The compositor's own frame (not via any client) shows the overlay.
    let own = overlay_up(&c);
    let own_border = own.count(BORDER);
    assert!(own_border > 5_000, "overlay was not drawn by the compositor: {own_border} border pixels");
    assert_ne!(own.at(640, 400), CLIENT, "centre of the compositor's own frame still shows the client");
    // Now capture over the wire, overlay still up.
    let during = c.capture("during.png");
    let cap_border = during.count(BORDER);
    println!("COMPOSITOR OWN FRAME: border-coloured pixels={own_border}, (10,10)={:?} (centre)={:?}", own.at(10, 10), own.at(640, 400));
    println!("CAPTURE while overlay up: border-coloured pixels={cap_border}, (10,10)={:?} (centre)={:?}", during.at(10, 10), during.at(640, 400));
    assert_eq!(cap_border, 0, "capture contains overlay border pixels");
    assert_eq!(during.at(640, 400), CLIENT);
    assert_eq!(during.at(10, 10), CLIENT, "capture shows the scrim");
    assert!(before.px == during.px, "capture during overlay differs from capture before it");
    println!("CAPTURE during == CAPTURE before overlay: all {} bytes identical", before.px.len());
    drop(pending);
}

#[test]
fn keyboard_goes_to_the_overlay_and_not_to_the_focused_client() {
    let mut c = Comp::start(&[]);
    let log = c.client();
    c.inject("key z"); // control: reaches the client
    wait_for("control key", || read(&log).contains("key 44 Released"));
    let pending = ask(&c, &request_json("r2", 60_000));
    overlay_up(&c);
    wait_for("client to lose focus", || read(&log).contains("leave"));
    let mark = read(&log).len();
    for k in ["key z", "key q", "down s", "up s", "key Return", "key space"] { c.inject(k); }
    std::thread::sleep(Duration::from_millis(300));
    assert_eq!(&read(&log)[mark..], "", "client saw input while the overlay was up");
    c.inject("key a");
    let ans = answer(pending);
    assert_eq!(ans["decision"], "allow");
    assert_eq!(ans["key"], "a");
    assert_eq!(ans["request_id"], "r2");
    std::thread::sleep(Duration::from_millis(300));
    let after = read(&log)[mark..].to_string();
    assert!(!after.contains("key"), "the deciding key or earlier keys reached the client: {after:?}");
    assert!(after.contains("enter"), "focus not restored: {after:?}");
    c.inject("key z");
    wait_for("input reaching the client after dismissal", || read(&log)[mark..].contains("key 44 Pressed"));
    println!("CLIENT LOG:\n{}", read(&log));
}

#[test]
fn deny_keys_timeout_and_withdrawal() {
    let mut c = Comp::start(&[]);
    c.client();
    for (k, name) in [("d", "d"), ("Escape", "Escape")] {
        let p = ask(&c, &request_json("rd", 60_000));
        wait_for("overlay", || c.log().matches("queued").count() >= 1);
        std::thread::sleep(Duration::from_millis(150));
        c.inject(&format!("key {k}"));
        let a = answer(p);
        assert_eq!((a["decision"].as_str(), a["key"].as_str()), (Some("deny"), Some(name)));
    }
    let a = answer(ask(&c, &request_json("rt", 1_000)));
    assert_eq!((a["decision"].as_str(), a["reason"].as_str()), (Some("deny"), Some("timeout")));
    // an unrelated key does nothing: still pending until it times out
    let p = ask(&c, &request_json("ru", 1_500));
    std::thread::sleep(Duration::from_millis(300));
    c.inject("key x");
    let a = answer(p);
    assert_eq!(a["reason"], "timeout");
    // requester hangs up: overlay withdrawn, input flows to the client again
    let log2 = c.dir.path().join("client0.log");
    let p = ask(&c, &request_json("rw", 60_000));
    wait_for("leave", || read(&log2).matches("leave").count() >= 3);
    drop(p);
    wait_for("focus restored after withdrawal", || read(&log2).matches("enter").count() >= 4);
}

#[test]
fn a_peer_that_is_not_allowlisted_cannot_request_an_overlay() {
    // uid not ours
    let mut c = Comp::start(&["--trusted-uid", "4242"]);
    c.client();
    let a = answer(ask(&c, &request_json("x", 60_000)));
    assert!(a["error"].as_str().unwrap().contains("not allowed"), "{a}");
    std::thread::sleep(Duration::from_millis(400));
    assert!(!c.dump.exists(), "an overlay was drawn for a refused peer");
    println!("REFUSED (uid): {a}");
    drop(c);
    // right uid, but inside a denied cgroup: this test process's own
    let own: String = std::fs::read_to_string("/proc/self/cgroup").unwrap().lines().find_map(|l| l.strip_prefix("0::").map(String::from)).expect("cgroup v2");
    let c = Comp::start(&["--deny-cgroup", &own]);
    let a = answer(ask(&c, &request_json("y", 60_000)));
    assert!(a["error"].as_str().unwrap().contains("denied cgroup"), "{a}");
    std::thread::sleep(Duration::from_millis(300));
    assert!(!c.dump.exists());
    println!("REFUSED (cgroup {own}): {a}");
}

#[test]
fn clients_have_no_protocol_object_for_the_overlay() {
    let c = Comp::start(&[]);
    let g = c.globals();
    println!("GLOBALS:\n{g}");
    for bad in ["trusted", "approval", "overlay", "secure", "attention"] {
        assert!(!g.to_lowercase().contains(bad), "global mentions {bad}: {g}");
    }
}

#[test]
fn test_input_refuses_to_exist_without_headless() {
    let st = std::process::Command::new(env!("CARGO_BIN_EXE_aurora-compositor"))
        .args(["--test-input", "/tmp/never.sock"]).env_remove("WAYLAND_DISPLAY").env_remove("DISPLAY")
        .output().unwrap();
    assert!(!st.status.success());
    assert!(String::from_utf8_lossy(&st.stderr).contains("only with --headless"));
}
