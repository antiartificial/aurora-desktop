//! Clipboard and primary selection, shared with a host, proved end to end.
//!
//! NO TEST HERE TOUCHES THE REAL HOST CLIPBOARD. The "host" is a second headless Aurora, which
//! serves `ext_data_control` like a real compositor; the compositor under test is pointed at it
//! with `--host-clipboard-display`, an absolute socket path, and every client is given an
//! explicit WAYLAND_DISPLAY / XDG_RUNTIME_DIR (the harness removes the inherited ones). The
//! host session's WAYLAND_DISPLAY is never read.
//!
//! Clients are `examples/clipclient` (ext_data_control: no focus, no serial needed).

mod common;
use common::*;
use std::path::PathBuf;

fn bridged() -> (Comp, Comp) {
    let host = Comp::start(&[]);
    let sock = host.dir.path().join(&host.display);
    let sock = sock.to_str().unwrap().to_string();
    let guest = Comp::start(&["--host-clipboard-display", &sock]);
    wait_for("bridge up", || guest.log().contains("host clipboard bridging is on"));
    (host, guest)
}

/// Run `clipclient get` against `c` and return its one-line verdict: `data "..."`, `none` or `timeout`.
fn get(c: &mut Comp, primary: bool, mime: &str) -> String {
    let mut args = vec!["get"];
    if primary { args.push("--primary"); }
    args.push(mime);
    let log = c.example("clipclient", &args);
    wait_for("clipclient get", || !read(&log).is_empty());
    read(&log).trim().to_string()
}

/// Poll `get` until it says `want`.
fn eventually_get(c: &mut Comp, primary: bool, mime: &str, want: &str) {
    let want = format!("data {want:?}");
    let t = std::time::Instant::now();
    loop {
        let g = get(c, primary, mime);
        if g == want { return; }
        assert!(t.elapsed().as_secs() < 10, "wanted {want}, last saw {g}");
        std::thread::sleep(std::time::Duration::from_millis(100));
    }
}

fn set(c: &mut Comp, primary: bool, mime: &str, data: &str) -> PathBuf {
    let mut args = vec!["set"];
    if primary { args.push("--primary"); }
    args.extend([mime, data]);
    let log = c.example("clipclient", &args);
    wait_for("clipclient set", || read(&log).lines().any(|l| l == "set"));
    log
}

fn watch(c: &mut Comp, primary: bool, secs: &str) -> PathBuf {
    let mut args = vec!["watch"];
    if primary { args.push("--primary"); }
    args.push(secs);
    let log = c.example("clipclient", &args);
    wait_for("watcher up", || read(&log).contains("watching"));
    log
}

fn events(log: &PathBuf) -> u32 {
    wait_for("watcher done", || read(log).contains("events "));
    read(log).lines().find_map(|l| l.strip_prefix("events ")).unwrap().parse().unwrap()
}

const TXT: &str = "text/plain;charset=utf-8";

#[test]
fn compositor_advertises_the_selection_globals() {
    let c = Comp::start(&[]);
    let g = c.globals();
    for want in ["ext_data_control_manager_v1", "zwp_primary_selection_device_manager_v1", "wl_data_device_manager"] {
        assert!(g.contains(want), "{want} missing from:\n{g}");
    }
}

#[test]
fn guest_to_guest_works_without_any_host() {
    let mut c = Comp::start(&[]);
    set(&mut c, false, TXT, "local clipboard");
    eventually_get(&mut c, false, TXT, "local clipboard");
    set(&mut c, true, TXT, "local primary");
    eventually_get(&mut c, true, TXT, "local primary");
    // the two selections are independent
    eventually_get(&mut c, false, TXT, "local clipboard");
}

#[test]
fn host_clipboard_reaches_a_guest_client() {
    let (mut host, mut guest) = bridged();
    set(&mut host, false, TXT, "copied on the host");
    eventually_get(&mut guest, false, TXT, "copied on the host");
}

#[test]
fn guest_clipboard_reaches_a_host_client() {
    let (mut host, mut guest) = bridged();
    set(&mut guest, false, TXT, "copied in aurora");
    eventually_get(&mut host, false, TXT, "copied in aurora");
}

#[test]
fn primary_selection_goes_both_ways_and_stays_apart_from_the_clipboard() {
    let (mut host, mut guest) = bridged();
    set(&mut host, true, TXT, "host primary");
    eventually_get(&mut guest, true, TXT, "host primary");
    assert_eq!(get(&mut guest, false, TXT), "none", "primary leaked into the clipboard");
    set(&mut guest, true, TXT, "guest primary");
    eventually_get(&mut host, true, TXT, "guest primary");
    assert_eq!(get(&mut host, false, TXT), "none", "primary leaked into the clipboard");
}

#[test]
fn a_new_host_selection_replaces_the_old_one_and_a_dead_source_clears_it() {
    let (mut host, mut guest) = bridged();
    let a = set(&mut host, false, TXT, "first");
    eventually_get(&mut guest, false, TXT, "first");
    set(&mut host, false, TXT, "second");
    eventually_get(&mut guest, false, TXT, "second");
    // the first source was cancelled by the host when replaced
    wait_for("first cancelled", || read(&a).contains("cancelled"));
    // the host copies something in another format: the guest is offered that, not the old text
    let png = host.example("clipclient", &["set", "image/png", "not really a png"]);
    wait_for("png set", || read(&png).lines().any(|l| l == "set"));
    eventually_get(&mut guest, false, "image/png", "not really a png");
}

#[test]
fn bridging_does_not_echo_or_loop() {
    let (mut host, mut guest) = bridged();
    // Quiet first: watchers on both sides, then ONE copy on each side in turn.
    let hw = watch(&mut host, false, "3");
    let gw = watch(&mut guest, false, "3");
    set(&mut host, false, TXT, "one copy");
    eventually_get(&mut guest, false, TXT, "one copy");
    std::thread::sleep(std::time::Duration::from_millis(1500));
    // The host's own copy: one event for the host's watcher, one for the guest's. A bounce would
    // re-set the host selection and show up as more host events (and more guest ones).
    assert_eq!(events(&hw), 1, "host watcher saw a bounce");
    assert_eq!(events(&gw), 1, "guest watcher saw a bounce");

    let hw = watch(&mut host, false, "3");
    let gw = watch(&mut guest, false, "3");
    set(&mut guest, false, TXT, "another copy");
    eventually_get(&mut host, false, TXT, "another copy");
    std::thread::sleep(std::time::Duration::from_millis(1500));
    assert_eq!(events(&hw), 1, "host watcher saw an echo of the guest's copy");
    assert_eq!(events(&gw), 1, "guest watcher saw a bounce");
}

#[test]
fn a_guest_clearing_its_own_selection_clears_the_host_but_cannot_wipe_a_host_one() {
    let (mut host, mut guest) = bridged();
    set(&mut host, false, TXT, "host owns this");
    eventually_get(&mut guest, false, TXT, "host owns this");
    // a guest app clearing the selection (as some do on startup) leaves the host's alone
    let c = guest.example("clipclient", &["clear"]);
    wait_for("cleared", || read(&c).contains("cleared"));
    std::thread::sleep(std::time::Duration::from_millis(500));
    assert_eq!(get(&mut host, false, TXT), format!("data {:?}", "host owns this"));

    // but the guest's own copy is cleared on the host when the guest clears it
    set(&mut guest, false, TXT, "guest owns this");
    eventually_get(&mut host, false, TXT, "guest owns this");
    let c = guest.example("clipclient", &["clear"]);
    wait_for("cleared", || read(&c).contains("cleared"));
    let t = std::time::Instant::now();
    while get(&mut host, false, TXT) != "none" {
        assert!(t.elapsed().as_secs() < 10, "the host selection was never cleared");
        std::thread::sleep(std::time::Duration::from_millis(100));
    }
}

#[test]
fn no_host_clipboard_flag_keeps_the_guest_private() {
    let host = Comp::start(&[]);
    let sock = host.dir.path().join(&host.display).to_str().unwrap().to_string();
    let mut host = host;
    let mut guest = Comp::start(&["--host-clipboard-display", &sock, "--no-host-clipboard"]);
    set(&mut host, false, TXT, "must not arrive");
    std::thread::sleep(std::time::Duration::from_millis(1000));
    assert_eq!(get(&mut guest, false, TXT), "none");
    // and guest-to-guest still works
    set(&mut guest, false, TXT, "still local");
    eventually_get(&mut guest, false, TXT, "still local");
    assert_eq!(get(&mut host, false, TXT), format!("data {:?}", "must not arrive"));
}

#[test]
fn an_unreachable_host_is_not_fatal() {
    let mut guest = Comp::start(&["--host-clipboard-display", "/nonexistent/wayland-host"]);
    set(&mut guest, false, TXT, "fine without a host");
    eventually_get(&mut guest, false, TXT, "fine without a host");
}

#[test]
fn a_host_without_data_control_is_not_fatal() {
    // A "host" that is a plain Unix listener speaking nothing: the registry roundtrip fails.
    let dir = tempfile::tempdir().unwrap();
    let sock = dir.path().join("mute");
    let l = std::os::unix::net::UnixListener::bind(&sock).unwrap();
    let h = std::thread::spawn(move || { let _ = l.accept(); std::thread::sleep(std::time::Duration::from_secs(1)); });
    let mut guest = Comp::start(&["--host-clipboard-display", sock.to_str().unwrap()]);
    set(&mut guest, false, TXT, "fine");
    eventually_get(&mut guest, false, TXT, "fine");
    let _ = h.join();
}
