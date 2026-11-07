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
    // The six keys above are swallowed whether or not the prompt is armed, which is what the
    // assertion just checked. This one is meant to DECIDE, so it has to be past the window.
    // In a debug build the six injections and the sleep happened to exceed it; in release they
    // do not, which is why `make test` caught this and `cargo test` did not.
    wait_until_armed();
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
        wait_until_armed();
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
fn a_key_pressed_before_the_prompt_is_armed_does_not_answer_it() {
    // `a` allows on one press with no modifier, and a prompt takes the keyboard
    // the instant it is queued. Any same-uid process may raise one, and it is a
    // Wayland client, so it knows when the user is typing: without a delay the
    // user's next `a` answers a prompt they have not read. The letters before it
    // are swallowed, so the only clue is a missing character.
    let mut c = Comp::start(&[]);
    c.client();
    let p = ask(&c, &request_json("arm", 60_000));
    wait_for("overlay", || c.log().contains("queued"));

    // Inside the window: this is the user mid-word.
    for _ in 0..4 {
        c.inject("key a");
        std::thread::sleep(Duration::from_millis(40));
    }
    std::thread::sleep(Duration::from_millis(100));
    assert!(c.log().contains("queued"), "the prompt is gone");
    assert!(
        !c.log().contains("-> allow"),
        "a key inside the arming window answered the prompt:\n{}",
        c.log().lines().filter(|l| l.contains("trusted:")).collect::<Vec<_>>().join("\n")
    );

    // Past it, the same key decides, so the delay is a delay and not a deafness.
    std::thread::sleep(Duration::from_millis(450));
    c.inject("key a");
    let a = answer(p);
    assert_eq!((a["decision"].as_str(), a["key"].as_str()), (Some("allow"), Some("a")));
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

fn prompt_frame_for_path(path: &str) -> Img {
    let c = Comp::start(&["--freeze-time"]);
    let req = serde_json::json!({
        "v": 1, "op": "approve", "request_id": "cjk", "app": "org.aurora.demo", "domain": "app-org-aurora-demo",
        "verb": "delete@v1", "effect": "destructive",
        "grants": [{"path": path, "mode": "rw", "source": "gesture"}],
        "args": {"confirm": true}, "timeout_ms": 60_000,
    });
    let pending = ask(&c, &req.to_string());
    let img = overlay_up(&c);
    drop(pending);
    img
}

/// A Chinese file name in the trusted approval prompt must draw as real glyphs. Two different
/// names of the same length must produce different pixels, and neither may equal the prompt
/// drawn with a code point no font has (the missing-glyph box, which is what an absent CJK face
/// produces for every character alike).
#[test]
fn chinese_file_names_in_the_approval_prompt_are_not_identical_tofu() {
    let a = prompt_frame_for_path("/home/user/文件夹/图片报告.png");
    let b = prompt_frame_for_path("/home/user/音乐库/下载桌面.png");
    let tofu = prompt_frame_for_path("/home/user/\u{378}\u{378}\u{378}/\u{378}\u{378}\u{378}\u{378}.png");
    let (ab, at, bt) = (a.diff_count(&b), a.diff_count(&tofu), b.diff_count(&tofu));
    println!("APPROVAL PROMPT pixels: 文件夹/图片报告 vs 音乐库/下载桌面 = {ab}; first vs tofu = {at}; second vs tofu = {bt}");
    assert!(ab > 300, "two different Chinese names drew alike ({ab} px apart): tofu");
    assert!(at > 300 && bt > 300, "a Chinese name drew as the missing-glyph box ({at}, {bt} px from tofu)");
}
