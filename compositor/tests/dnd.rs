//! Drag and drop from a host into Aurora's clients, proved end to end.
//!
//! NO TEST HERE TOUCHES THE REAL HOST'S DRAG STATE OR CLIPBOARD. The "host" is a second headless
//! Aurora. The compositor under test is pointed at it with `--host-dnd-display`, which opens a
//! stand-in for winit's window there and attaches the drag bridge to that connection through a
//! foreign display, exactly as production does with winit's. A `dndclient source` on the host starts
//! a real `wl_data_device` drag; the host's injected pointer drags it over the stand-in window; a
//! `dndclient target` inside the compositor under test is what receives it.
//!
//! Layout: host window 0 is the stand-in (640x400) at (100,100); the source client (200x200) sits at
//! (800,100). A pointer at host (X,Y) over the stand-in is at (X-100,Y-100) in the guest, where the
//! target (400x300) sits at (0,0).

mod common;
use common::*;
use std::path::PathBuf;

const TXT: &str = "text/plain;charset=utf-8";

struct Rig {
    host: Comp,
    guest: Comp,
    target: PathBuf,
}

/// A bridged host and guest. `guest_args` goes to the compositor under test; `target_args` to the target.
fn rig(guest_args: &[&str], target_args: &[&str]) -> Rig {
    let host = Comp::start(&[]);
    let sock = host.dir.path().join(&host.display);
    let sock = sock.to_str().unwrap().to_string();
    let mut args = vec!["--host-dnd-display", sock.as_str()];
    args.extend_from_slice(guest_args);
    let mut guest = Comp::start(&args);
    wait_for("dnd bridge up", || guest.log().contains("host drag and drop bridging is on"));
    host.inject("move 0 100 100");
    let mut t = vec!["target", "--size", "400x300"];
    t.extend_from_slice(target_args);
    let target = guest.example("dndclient", &t);
    wait_for("target mapped", || read(&target).contains("mapped"));
    guest.inject("move 0 0 0");
    Rig { host, guest, target }
}

/// Start a source on the host offering `items` and press on it: the drag is in flight afterwards.
fn pick_up(r: &mut Rig, items: &[&str]) -> PathBuf {
    let mut a = vec!["source", "--size", "200x200"];
    a.extend_from_slice(items);
    let src = r.host.example("dndclient", &a);
    wait_for("source mapped", || read(&src).contains("mapped"));
    r.host.inject("move 1 800 100");
    r.host.inject("pointer 850 150");
    r.host.inject("press");
    wait_for("drag started", || read(&src).contains("drag-started"));
    src
}

fn lines(log: &PathBuf) -> Vec<String> { read(log).lines().map(String::from).collect() }

fn fnv(b: &[u8]) -> u64 { b.iter().fold(0xcbf29ce484222325u64, |h, x| (h ^ *x as u64).wrapping_mul(0x100000001b3)) }
fn pattern(n: usize) -> Vec<u8> { (0..n).map(|i| b"0123456789abcdef"[(i * 7 + i / 16) % 16]).collect() }

#[test]
fn a_host_drag_reaches_the_client_under_the_pointer_with_its_types_and_data() {
    let mut r = rig(&[], &[]);
    let src = pick_up(&mut r, &[&format!("{TXT}|hello from the host"), "text/uri-list|file:///tmp/x.txt\r\n", "text/html|<b>hi</b>"]);
    // over the stand-in = over the target: guest (200,100)
    r.host.inject("pointer 250 180");
    r.host.inject("pointer 300 200");
    wait_for("target sees the drag", || read(&r.target).contains("enter"));
    let enter = lines(&r.target).into_iter().find(|l| l.starts_with("enter")).unwrap();
    // the types arrive verbatim and in order (outside a domain nothing is withheld)
    assert!(enter.ends_with(&format!("types={TXT},text/uri-list,text/html")), "{enter}");
    let xy: Vec<i32> = enter.split_whitespace().skip(1).take(2).map(|n| n.parse().unwrap()).collect();
    assert!((xy[0] - 150).abs() <= 60 && (xy[1] - 80).abs() <= 60, "enter position {xy:?} should be near (150,80)..(200,100)");
    r.host.inject("pointer 310 210");
    wait_for("motion reaches the target", || read(&r.target).contains("motion 210 110"));
    r.host.inject("release");
    wait_for("data arrives", || read(&r.target).contains("finished"));
    let t = read(&r.target);
    assert!(t.contains(&format!("data {TXT} len 19 fnv {:016x} text \"hello from the host\"", fnv(b"hello from the host"))), "{t}");
    assert!(t.contains("data text/uri-list len 19"), "{t}");
    assert!(t.contains("data text/html len 9"), "{t}");
    // and the source on the host is told, so the host drag ends as a completed copy
    wait_for("source finished", || read(&src).contains("finished"));
    let s = read(&src);
    assert!(s.contains(&format!("send {TXT}")) && s.contains("dropped"), "{s}");
    assert!(!s.contains("cancelled"), "{s}");
    let log = r.guest.log();
    assert!(log.contains("a host drag entered Aurora's window offering"), "{log}");
}

#[test]
fn a_big_payload_streams_through_intact() {
    let n = 3 * 1024 * 1024 + 17;
    let mut r = rig(&[], &[]);
    pick_up(&mut r, &[&format!("application/octet-stream|repeat:{n}")]);
    r.host.inject("pointer 300 200");
    wait_for("target sees the drag", || read(&r.target).contains("enter"));
    r.host.inject("release");
    wait_for("data arrives", || read(&r.target).contains("finished"));
    let want = format!("data application/octet-stream len {n} fnv {:016x}", fnv(&pattern(n)));
    assert!(read(&r.target).contains(&want), "wanted {want}, got {}", read(&r.target));
}

#[test]
fn a_drag_that_leaves_without_dropping_cancels_cleanly_and_the_next_works() {
    let mut r = rig(&[], &[]);
    let src = pick_up(&mut r, &[&format!("{TXT}|first")]);
    r.host.inject("pointer 300 200");
    wait_for("target sees the drag", || read(&r.target).contains("enter"));
    // out of the stand-in window, onto empty host desktop, and let go there
    r.host.inject("pointer 1200 700");
    wait_for("target told the drag left", || read(&r.target).contains("leave"));
    r.host.inject("release");
    wait_for("source cancelled", || read(&src).contains("cancelled"));
    assert!(!read(&r.target).contains("drop"), "a drop reached the target after the drag left: {}", read(&r.target));
    assert!(r.guest.log().contains("left Aurora's window without a drop; cancelling"), "{}", r.guest.log());
    // nothing is stuck: a second drag, picked up and dropped, still arrives
    let before = read(&r.target).matches("finished").count();
    let mut a = vec!["source", "--size", "200x200"];
    let item = format!("{TXT}|second");
    a.push(&item);
    let src2 = r.host.example("dndclient", &a);
    wait_for("second source mapped", || read(&src2).contains("mapped"));
    r.host.inject("move 2 800 100");
    r.host.inject("pointer 850 150");
    r.host.inject("press");
    wait_for("second drag started", || read(&src2).contains("drag-started"));
    r.host.inject("pointer 300 200");
    wait_for("target sees the second drag", || read(&r.target).matches("enter").count() >= 2);
    r.host.inject("release");
    wait_for("second data arrives", || read(&r.target).matches("finished").count() > before);
    assert!(read(&r.target).contains("text \"second\""), "{}", read(&r.target));
}

#[test]
fn a_target_that_accepts_nothing_gets_no_drop_and_the_source_is_cancelled() {
    let mut r = rig(&[], &["--reject"]);
    let src = pick_up(&mut r, &[&format!("{TXT}|nope")]);
    r.host.inject("pointer 300 200");
    wait_for("target sees the drag", || read(&r.target).contains("enter"));
    r.host.inject("release");
    wait_for("source cancelled", || read(&src).contains("cancelled"));
    assert!(!read(&r.target).contains("data "), "{}", read(&r.target));
}

/// This test's own cgroup path: every client of the compositor under test lives in it, so naming it
/// in `--deny-cgroup` makes all of them "inside a domain" to the clipboard/DnD policy.
fn my_cgroup() -> String {
    let c = std::fs::read_to_string("/proc/self/cgroup").unwrap();
    c.lines().find_map(|l| l.strip_prefix("0::")).expect("cgroup v2").to_string()
}

#[test]
fn a_confined_client_is_not_shown_host_paths_but_still_gets_the_rest() {
    let cg = my_cgroup();
    let mut r = rig(&["--deny-cgroup", &cg], &[]);
    let src = pick_up(&mut r, &["text/uri-list|file:///home/arti/photo.png\r\n", &format!("{TXT}|/home/arti/photo.png"), "image/png|PNGDATA"]);
    r.host.inject("pointer 300 200");
    wait_for("target sees the drag", || read(&r.target).contains("enter"));
    let enter = lines(&r.target).into_iter().find(|l| l.starts_with("enter")).unwrap();
    assert!(enter.ends_with("types=image/png"), "a confined client was shown {enter}");
    r.host.inject("release");
    wait_for("data arrives", || read(&r.target).contains("finished"));
    let t = read(&r.target);
    assert!(t.contains("data image/png len 7"), "{t}");
    assert!(!t.contains("photo.png"), "a host path reached a confined client: {t}");
    wait_for("source finished", || read(&src).contains("finished"));
    assert!(!read(&src).contains(&format!("send {TXT}")) && !read(&src).contains("send text/uri-list"), "{}", read(&src));
    let log = r.guest.log();
    assert!(log.contains("withholding [text/uri-list, text/plain;charset=utf-8] from a confined client"), "{log}");
}

#[test]
fn a_pure_file_drag_onto_a_confined_client_is_refused_loudly_and_never_leaks_a_path() {
    let cg = my_cgroup();
    let mut r = rig(&["--deny-cgroup", &cg], &[]);
    let src = pick_up(&mut r, &["text/uri-list|file:///home/arti/photo.png\r\n", &format!("{TXT}|/home/arti/photo.png")]);
    r.host.inject("pointer 300 200");
    r.host.inject("pointer 310 210");
    wait_for("the refusal is logged", || r.guest.log().contains("nothing is left to offer, so the app sees no drag"));
    r.host.inject("release");
    wait_for("source cancelled", || read(&src).contains("cancelled"));
    let t = read(&r.target);
    assert!(!t.contains("enter") && !t.contains("data "), "a confined client saw a file drag: {t}");
    assert!(!read(&src).contains("send "), "the host was asked for a path: {}", read(&src));
}

#[test]
fn an_unconfined_client_gets_the_paths_under_the_same_flags_without_the_deny() {
    // the control for the two tests above: same drag, no --deny-cgroup
    let mut r = rig(&[], &[]);
    pick_up(&mut r, &["text/uri-list|file:///home/arti/photo.png\r\n"]);
    r.host.inject("pointer 300 200");
    wait_for("target sees the drag", || read(&r.target).contains("enter"));
    r.host.inject("release");
    wait_for("data arrives", || read(&r.target).contains("finished"));
    assert!(read(&r.target).contains("file:///home/arti/photo.png"), "{}", read(&r.target));
}

#[test]
fn without_a_host_the_bridge_says_so_and_guest_to_guest_is_untouched() {
    let c = Comp::start(&[]);
    wait_for("log line", || c.log().contains("host drag and drop is off (headless has no host)"));
    let c2 = Comp::start(&["--no-host-dnd"]);
    wait_for("log line", || c2.log().contains("--no-host-dnd"));
    let c3 = Comp::start(&["--host-dnd-display", "no-such-socket"]);
    wait_for("log line", || c3.log().contains("host drag and drop is off (cannot connect"));
}
