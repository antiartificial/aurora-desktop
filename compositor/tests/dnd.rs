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

// ---------------------------------------------------------------------------------------------
// Files dropped into a domain (decision 0020, "The grant"). The compositor under test is pointed
// at a stand-in controller (`--drop-socket`), a thread of this test that records what it is asked
// and answers as a controller would. Clients are put in a REAL cgroup under `aurora-domains`, so the
// compositor attributes them to a domain the same way it does in production.
// ---------------------------------------------------------------------------------------------

use std::io::{BufRead, BufReader, Write};
use std::os::unix::net::UnixListener;
use std::sync::{Arc, Mutex};

/// A cgroup directory under this user's `aurora-domains`, removed when dropped. `None` when this host
/// does not let the test make one (no delegated cgroup subtree): the tests that need it say so and stop.
struct DomainCgroup(PathBuf);

impl DomainCgroup {
    fn make() -> Option<DomainCgroup> {
        static N: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
        let cg = my_cgroup();
        let end = cg.find(".service/").map(|i| i + ".service".len())?;
        let parent = Path::new("/sys/fs/cgroup").join(cg[..end].trim_start_matches('/')).join("aurora-domains");
        let dir = parent.join(format!("zz-dnd-{}-{}", std::process::id(), N.fetch_add(1, std::sync::atomic::Ordering::SeqCst)));
        std::fs::create_dir(&dir).ok()?;
        Some(DomainCgroup(dir))
    }

    fn name(&self) -> String {
        self.0.file_name().unwrap().to_string_lossy().into_owned()
    }
}

impl Drop for DomainCgroup {
    fn drop(&mut self) {
        for _ in 0..50 {
            if std::fs::remove_dir(&self.0).is_ok() {
                return;
            }
            std::thread::sleep(std::time::Duration::from_millis(100));
        }
    }
}

use std::path::Path;

/// A stand-in controller: records each request line and answers with `reply(request)`.
struct FakeController {
    sock: PathBuf,
    seen: Arc<Mutex<Vec<serde_json::Value>>>,
}

impl FakeController {
    fn start(dir: &Path, name: &str, reply: impl Fn(&serde_json::Value) -> String + Send + 'static) -> FakeController {
        let sock = dir.join(name);
        let l = UnixListener::bind(&sock).unwrap();
        let seen = Arc::new(Mutex::new(Vec::new()));
        let s2 = seen.clone();
        std::thread::spawn(move || {
            for c in l.incoming() {
                let Ok(mut c) = c else { return };
                let mut line = String::new();
                if BufReader::new(&c).read_line(&mut line).is_err() {
                    continue;
                }
                let Ok(v) = serde_json::from_str::<serde_json::Value>(&line) else { continue };
                let out = reply(&v);
                s2.lock().unwrap().push(v);
                let _ = writeln!(c, "{out}");
            }
        });
        FakeController { sock, seen }
    }

    fn requests(&self) -> Vec<serde_json::Value> {
        self.seen.lock().unwrap().clone()
    }
}

/// Answer every drop by granting each path under /run/aurora/drops/<n>/<base>.
fn grants_everything(req: &serde_json::Value) -> String {
    let grants: Vec<_> = req["paths"].as_array().unwrap().iter().enumerate().map(|(i, p)| {
        let host = p.as_str().unwrap();
        let base = Path::new(host).file_name().unwrap().to_string_lossy();
        serde_json::json!({"id": (i + 1).to_string(), "host": host, "guest": format!("/run/aurora/drops/{}/{base}", i + 1), "mode": "ro", "dir": false, "source": "powerbox", "ticket": "t"})
    }).collect();
    serde_json::json!({"v": 1, "ok": true, "app": "org.example", "domain": req["domain"], "grants": grants}).to_string()
}

/// A bridged host, a guest with a stand-in controller, and a target inside a domain cgroup.
struct DomainRig {
    r: Rig,
    ctl: FakeController,
    domain: String,
    _cg: DomainCgroup,
}

fn domain_rig(reply: impl Fn(&serde_json::Value) -> String + Send + 'static, with_controller: bool) -> Option<DomainRig> {
    domain_rig_with(reply, with_controller, &[])
}

fn domain_rig_with(reply: impl Fn(&serde_json::Value) -> String + Send + 'static, with_controller: bool, target_args: &[&str]) -> Option<DomainRig> {
    let Some(cg) = DomainCgroup::make() else {
        eprintln!("SKIPPED: cannot create a cgroup under aurora-domains on this host, so a client cannot be put in a domain");
        return None;
    };
    let host = Comp::start(&[]);
    let sock = host.dir.path().join(&host.display).to_str().unwrap().to_string();
    let rt = tempfile::tempdir().unwrap();
    let ctl = FakeController::start(rt.path(), "drop.sock", reply);
    let ctl_arg = if with_controller { ctl.sock.to_str().unwrap().to_string() } else { "none".to_string() };
    let mut guest = Comp::start(&["--host-dnd-display", &sock, "--drop-socket", &ctl_arg]);
    wait_for("dnd bridge up", || guest.log().contains("host drag and drop bridging is on"));
    host.inject("move 0 100 100");
    let mut targs = vec!["target", "--size", "400x300"];
    targs.extend_from_slice(target_args);
    let target = guest.example_in_cgroup("dndclient", &targs, &cg.0);
    wait_for("target mapped", || read(&target).contains("mapped"));
    guest.inject("move 0 0 0");
    std::mem::forget(rt); // the socket's directory lives as long as the test process; it is a tempdir under /tmp
    Some(DomainRig { domain: cg.name(), r: Rig { host, guest, target }, ctl, _cg: cg })
}

fn real_file(name: &str, body: &str) -> PathBuf {
    let d = std::fs::canonicalize(tempfile::tempdir().unwrap().keep()).unwrap();
    let p = d.join(name);
    std::fs::write(&p, body).unwrap();
    p
}

fn uri(p: &Path) -> String {
    format!("file://{}\r\n", p.display())
}

fn finish_drop(r: &mut Rig) {
    r.host.inject("pointer 300 200");
    wait_for("target sees the drag", || read(&r.target).contains("enter"));
    r.host.inject("release");
}

#[test]
fn a_file_dropped_on_an_app_in_a_domain_becomes_a_grant_and_the_app_sees_only_the_guest_path() {
    let Some(mut d) = domain_rig(grants_everything, true) else { return };
    let photo = real_file("photo.png", "PNGBYTES");
    let src = pick_up(&mut d.r, &[&format!("text/uri-list|{}", uri(&photo)), &format!("{TXT}|{}", photo.display()), "image/png|PNGDATA"]);
    finish_drop(&mut d.r);
    wait_for("data arrives", || read(&d.r.target).contains("finished"));
    let t = read(&d.r.target);
    let enter = lines(&d.r.target).into_iter().find(|l| l.starts_with("enter")).unwrap();
    // the list is offered, its plain-text twin (which is the host path) is not
    assert!(enter.ends_with("types=text/uri-list,image/png"), "{enter}");
    // the data is the rewritten list, naming the in-domain path
    let want = b"file:///run/aurora/drops/1/photo.png\r\n";
    assert!(t.contains(&format!("data text/uri-list len {} fnv {:016x}", want.len(), fnv(want))), "{t}");
    // and no byte of the host path reached the app, in any form
    let dir = photo.parent().unwrap().to_string_lossy().to_string();
    assert!(!t.contains(&dir) && !t.contains("tmp"), "a host path reached the target: {t}");
    assert!(t.contains("data image/png len 7"), "{t}");
    // what the controller was asked: exactly the resolved path, read-only, for the domain the kernel named
    let reqs = d.ctl.requests();
    assert_eq!(reqs.len(), 1, "{reqs:?}");
    assert_eq!(reqs[0]["op"], "drop");
    assert_eq!(reqs[0]["domain"], d.domain.as_str());
    assert_eq!(reqs[0]["mode"], "ro");
    assert_eq!(reqs[0]["paths"], serde_json::json!([photo.to_str().unwrap()]));
    // Aurora read the host's list exactly once, and the host was never asked for the text twin
    wait_for("source finished", || read(&src).contains("finished"));
    let s = read(&src);
    assert_eq!(s.matches("send text/uri-list").count(), 1, "{s}");
    assert!(!s.contains(&format!("send {TXT}")), "{s}");
    let log = d.r.guest.log();
    assert!(log.contains(&format!("a file drag is over a client in domain {}", d.domain)) && log.contains("the controller granted 1 path(s)"), "{log}");
}

#[test]
fn a_controller_that_refuses_leaves_the_app_with_an_empty_list_and_no_path() {
    let Some(mut d) = domain_rig(|_| r#"{"v":1,"ok":false,"code":"approval_denied","error":"denied at the prompt"}"#.into(), true) else { return };
    let photo = real_file("secret.txt", "S");
    pick_up(&mut d.r, &[&format!("text/uri-list|{}", uri(&photo))]);
    finish_drop(&mut d.r);
    wait_for("the refusal is logged", || d.r.guest.log().contains("the controller refused the grant (approval_denied)"));
    wait_for("data arrives", || read(&d.r.target).contains("finished"));
    let t = read(&d.r.target);
    assert!(t.contains("data text/uri-list len 0"), "{t}");
    assert!(!t.contains("secret.txt") && !t.contains("/tmp"), "{t}");
}

#[test]
fn a_file_list_beyond_the_bound_is_refused_without_asking_the_controller() {
    let Some(mut d) = domain_rig(grants_everything, true) else { return };
    let photo = real_file("a.txt", "A");
    let mut big = uri(&photo);
    while big.len() <= 4096 {
        big.push_str("# padding to push the list past the bound\r\n");
    }
    pick_up(&mut d.r, &[&format!("text/uri-list|{big}")]);
    finish_drop(&mut d.r);
    wait_for("the refusal is logged", || d.r.guest.log().contains("the file list is longer than 4096 bytes"));
    wait_for("data arrives", || read(&d.r.target).contains("finished"));
    assert!(read(&d.r.target).contains("data text/uri-list len 0"), "{}", read(&d.r.target));
    assert!(d.ctl.requests().is_empty(), "the controller was asked about an oversized list: {:?}", d.ctl.requests());
}

#[test]
fn a_list_with_anything_but_local_files_asks_nobody() {
    let Some(mut d) = domain_rig(grants_everything, true) else { return };
    let photo = real_file("a.txt", "A");
    pick_up(&mut d.r, &[&format!("text/uri-list|{}http://example.com/x.png\r\n", uri(&photo))]);
    finish_drop(&mut d.r);
    wait_for("the refusal is logged", || d.r.guest.log().contains("is not a file: URI naming a path on this machine"));
    wait_for("data arrives", || read(&d.r.target).contains("finished"));
    assert!(read(&d.r.target).contains("data text/uri-list len 0"), "{}", read(&d.r.target));
    assert!(d.ctl.requests().is_empty(), "{:?}", d.ctl.requests());
}

#[test]
fn symlinks_are_resolved_and_pipes_devices_and_missing_files_never_reach_the_controller() {
    let Some(mut d) = domain_rig(grants_everything, true) else { return };
    let real = real_file("real.txt", "R");
    let dir = real.parent().unwrap().to_path_buf();
    let link = dir.join("link.txt");
    std::os::unix::fs::symlink(&real, &link).unwrap();
    let fifo = dir.join("pipe");
    let c = std::ffi::CString::new(fifo.to_str().unwrap()).unwrap();
    assert_eq!(unsafe { libc::mkfifo(c.as_ptr(), 0o600) }, 0);
    let list = format!("{}{}{}file:///dev/null\r\n{}", uri(&link), uri(&fifo), uri(&dir.join("missing")), uri(&real));
    pick_up(&mut d.r, &[&format!("text/uri-list|{list}")]);
    finish_drop(&mut d.r);
    wait_for("data arrives", || read(&d.r.target).contains("finished"));
    let reqs = d.ctl.requests();
    assert_eq!(reqs.len(), 1, "{reqs:?}");
    // the link was resolved to the real file, which was dropped twice: once, and nothing else
    assert_eq!(reqs[0]["paths"], serde_json::json!([real.to_str().unwrap()]), "{reqs:?}");
    let log = d.r.guest.log();
    for why in ["is a FIFO", "is a device", "cannot be resolved"] {
        assert!(log.contains(why), "{why}: {log}");
    }
}

#[test]
fn a_directory_is_passed_to_the_controller_which_decides_whether_to_ask() {
    let Some(mut d) = domain_rig(grants_everything, true) else { return };
    let dir = std::fs::canonicalize(tempfile::tempdir().unwrap().keep()).unwrap();
    pick_up(&mut d.r, &[&format!("text/uri-list|{}", uri(&dir))]);
    finish_drop(&mut d.r);
    wait_for("data arrives", || read(&d.r.target).contains("finished"));
    let reqs = d.ctl.requests();
    assert_eq!(reqs.len(), 1, "{reqs:?}");
    assert_eq!(reqs[0]["paths"], serde_json::json!([dir.to_str().unwrap()]));
    assert_eq!(reqs[0]["mode"], "ro", "the compositor never asks for more than read-only");
}

#[test]
fn without_a_controller_an_app_in_a_domain_still_gets_no_host_path() {
    let Some(mut d) = domain_rig(grants_everything, false) else { return };
    let photo = real_file("a.txt", "A");
    let src = pick_up(&mut d.r, &[&format!("text/uri-list|{}", uri(&photo)), &format!("{TXT}|{}", photo.display())]);
    d.r.host.inject("pointer 300 200");
    wait_for("the refusal is logged", || d.r.guest.log().contains("no controller drop socket"));
    d.r.host.inject("release");
    wait_for("source cancelled", || read(&src).contains("cancelled"));
    let t = read(&d.r.target);
    assert!(!t.contains("enter") && !t.contains("data "), "{t}");
    assert!(d.ctl.requests().is_empty());
}

#[test]
fn an_app_that_declines_the_drop_is_granted_nothing() {
    let Some(mut d) = domain_rig_with(grants_everything, true, &["--reject"]) else { return };
    let photo = real_file("a.txt", "A");
    let src = pick_up(&mut d.r, &[&format!("text/uri-list|{}", uri(&photo))]);
    d.r.host.inject("pointer 300 200");
    wait_for("target sees the drag", || read(&d.r.target).contains("enter"));
    d.r.host.inject("release");
    wait_for("source cancelled", || read(&src).contains("cancelled"));
    assert!(d.ctl.requests().is_empty(), "the controller was asked to grant a file to an app that declined it: {:?}", d.ctl.requests());
    assert!(!read(&d.r.target).contains("data "), "{}", read(&d.r.target));
}

// ---------------------------------------------------------------------------------------------
// GUEST TO HOST (decision 0020, "Guest to host"). The same two compositors, the other way round:
// the source is an Aurora client, the target is a client of the host, and the drag is handed over
// when the host's pointer leaves Aurora's window with the button still down.
//
// Two presses are injected, one per compositor, because the two compositors do not share an input
// device: the host's press is the implicit grab `start_drag` needs on the host, the guest's press
// is what makes the guest client start its drag. In production they are one physical click.
// ---------------------------------------------------------------------------------------------

/// A bridged host with a drop target on it, and a guest with a drag source inside it.
struct OutRig {
    host: Comp,
    guest: Comp,
    /// The `dndclient target` running on the HOST.
    host_target: PathBuf,
    /// The `dndclient source` running INSIDE Aurora.
    source: PathBuf,
}

fn out_rig(guest_args: &[&str], items: &[&str]) -> OutRig {
    let mut host = Comp::start(&[]);
    let sock = host.dir.path().join(&host.display).to_str().unwrap().to_string();
    let mut args = vec!["--host-dnd-display", sock.as_str()];
    args.extend_from_slice(guest_args);
    let mut guest = Comp::start(&args);
    wait_for("dnd bridge up both ways", || guest.log().contains("host drag and drop bridging is on (wl_data_device on the host connection; both ways)"));
    host.inject("move 0 100 100"); // the stand-in for Aurora's window: 640x400 at (100,100)
    let host_target = host.example("dndclient", &["target", "--size", "400x300"]);
    wait_for("host target mapped", || read(&host_target).contains("mapped"));
    host.inject("move 1 800 100"); // out on the host desktop, beside Aurora's window
    let mut a = vec!["source", "--size", "400x300"];
    a.extend_from_slice(items);
    let source = guest.example("dndclient", &a);
    wait_for("guest source mapped", || read(&source).contains("mapped"));
    guest.inject("move 0 0 0");
    OutRig { host, guest, host_target, source }
}

/// Pick the drag up inside Aurora, with the host's button held over Aurora's window.
fn pick_up_in_guest(r: &mut OutRig) {
    // The two compositors have no shared input device, so the one physical pointer is injected on
    // both, at the same place: host (300,250) IS Aurora's own (200,150), the stand-in being at
    // (100,100).
    r.host.inject("pointer 300 250");
    r.host.inject("press");
    r.guest.inject("pointer 200 150"); // over the guest source window
    r.guest.inject("press");
    wait_for("the guest client started a drag", || read(&r.source).contains("drag-started"));
}

/// Drag out of Aurora's window and over the host's target.
///
/// Two motions, with the hand-off waited for in between: the first takes the pointer out of
/// Aurora's window, which is what hands the drag over; the second is the first motion the HOST's
/// own drag sees, and a Wayland drag only tells a target it is there on a motion. A user moving a
/// mouse sends hundreds of these; a test injecting two in the same millisecond does not.
fn out_to_host(r: &mut OutRig) {
    r.host.inject("pointer 900 200");
    wait_for("the hand-off is decided", || {
        let l = r.guest.log();
        l.contains("the host now has it") || l.contains("not handed to the host")
    });
    r.host.inject("pointer 910 210");
}

#[test]
fn a_guest_drag_that_leaves_the_window_is_handed_to_the_host_with_its_types_and_data() {
    let mut r = out_rig(&[], &[&format!("{TXT}|hello from the guest"), "text/html|<b>guest</b>", "text/uri-list|file:///home/arti/in-aurora.txt\r\n"]);
    pick_up_in_guest(&mut r);
    // while the pointer is inside Aurora's window the host knows nothing of this drag
    r.host.inject("pointer 400 300");
    assert!(!read(&r.host_target).contains("enter"), "the host saw a drag that had not left Aurora: {}", read(&r.host_target));
    out_to_host(&mut r);
    wait_for("the host target sees the drag", || read(&r.host_target).contains("enter"));
    let enter = lines(&r.host_target).into_iter().find(|l| l.starts_with("enter")).unwrap();
    // every type the guest offered, in order: negotiated by the host target, not guessed by Aurora
    assert!(enter.ends_with(&format!("types={TXT},text/html,text/uri-list")), "{enter}");
    r.host.inject("release");
    wait_for("the host target read the data", || read(&r.host_target).contains("finished"));
    let t = read(&r.host_target);
    // the bytes came out of the guest app, through the host, into the host's app
    assert!(t.contains(&format!("data {TXT} len 20 fnv {:016x} text \"hello from the guest\"", fnv(b"hello from the guest"))), "{t}");
    assert!(t.contains(&format!("data text/html len 12 fnv {:016x}", fnv(b"<b>guest</b>"))), "{t}");
    assert!(t.contains("text \"file:///home/arti/in-aurora.txt"), "{t}");
    // and the guest app was told what the host did with it
    let s = read(&r.source);
    assert!(s.contains(&format!("target {TXT}")), "the guest source was not told the host accepted: {s}");
    assert!(s.contains("dropped") && s.contains("finished") && !s.contains("cancelled"), "{s}");
    let log = r.guest.log();
    assert!(log.contains("a guest drag left Aurora's window; the host now has it, offering"), "{log}");
}

#[test]
fn a_drag_between_two_guest_windows_never_reaches_the_host() {
    let mut r = out_rig(&[], &[&format!("{TXT}|stays inside")]);
    let target = r.guest.example("dndclient", &["target", "--size", "200x150"]);
    wait_for("guest target mapped", || read(&target).contains("mapped"));
    r.guest.inject("move 1 420 120");
    pick_up_in_guest(&mut r);
    // Inside Aurora the whole way, with the host pointer moving in tandem as the real one does:
    // guest (500,200) is host (600,300), well inside Aurora's window. The hand-off is decided from
    // the host pointer, so a test that moved only the guest's would prove nothing about it.
    r.guest.inject("pointer 500 200");
    r.host.inject("pointer 600 300");
    wait_for("the guest target sees the drag", || read(&target).contains("enter"));
    r.host.inject("pointer 601 301");
    r.guest.inject("release");
    wait_for("the guest target read the data", || read(&target).contains("finished"));
    assert!(read(&target).contains("text \"stays inside\""), "{}", read(&target));
    // and the dragging app was told, while it dragged, that the window under the pointer would
    // take it: without this a guest drag gives the app no answer at all (see `dnd::out::GuestDrag`)
    assert!(read(&r.source).contains(&format!("target {TXT}")), "the dragging app was never told the target accepts: {}", read(&r.source));
    assert!(!read(&r.host_target).contains("enter"), "the host saw a guest-to-guest drag: {}", read(&r.host_target));
    assert!(!r.guest.log().contains("the host now has it"), "{}", r.guest.log());
}

/// The two directions in one compositor, in the order a user does them: a drop into Aurora, then a
/// drag out of it. Each direction has its own test above; this is the pair, because the first one's
/// leftovers are what can stop the second (they did: a dropped incoming drag leaves its session in
/// place, and a live session is what declines a hand-off).
#[test]
fn a_drag_out_still_works_after_something_was_dropped_in() {
    let mut r = out_rig(&[], &[&format!("{TXT}|out after in")]);
    // in: a host app drags onto a target inside Aurora
    let guest_target = r.guest.example("dndclient", &["target", "--size", "200x150"]);
    wait_for("the guest target mapped", || read(&guest_target).contains("mapped"));
    r.guest.inject("move 1 420 120");
    let host_src = r.host.example("dndclient", &["source", "--size", "200x200", "text/plain;charset=utf-8|dropped in"]);
    wait_for("the host source mapped", || read(&host_src).contains("mapped"));
    r.host.inject("move 2 760 560");
    r.host.inject("pointer 800 600");
    r.host.inject("press");
    wait_for("the host drag started", || read(&host_src).contains("drag-started"));
    r.host.inject("pointer 600 300"); // over Aurora, on the guest target
    wait_for("the guest target sees it", || read(&guest_target).contains("enter"));
    r.host.inject("release");
    wait_for("the guest target read it", || read(&guest_target).contains("finished"));
    assert!(read(&guest_target).contains("text \"dropped in\""), "{}", read(&guest_target));
    // out: now the other way, in the same compositor
    pick_up_in_guest(&mut r);
    out_to_host(&mut r);
    wait_for("the host target sees the drag", || read(&r.host_target).contains("enter"));
    r.host.inject("release");
    wait_for("the host target read the data", || read(&r.host_target).contains("finished"));
    assert!(read(&r.host_target).contains("text \"out after in\""), "{}", read(&r.host_target));
}

#[test]
fn a_host_target_that_refuses_the_drag_tells_the_guest_app_so_and_cancels_it() {
    let mut r = out_rig(&[], &[&format!("{TXT}|nobody wants this")]);
    // the target on the host accepts nothing; the accepting one from the rig is moved out of the
    // way, clear of both Aurora's window and where this drag goes
    r.host.inject("move 1 0 500");
    let rejecting = r.host.example("dndclient", &["target", "--size", "400x300", "--reject"]);
    wait_for("the rejecting host target mapped", || read(&rejecting).contains("mapped"));
    r.host.inject("move 2 800 100");
    pick_up_in_guest(&mut r);
    out_to_host(&mut r);
    wait_for("the host target sees the drag", || read(&rejecting).contains("enter"));
    wait_for("the guest app is told nothing will take it", || read(&r.source).contains("target none"));
    r.host.inject("release");
    wait_for("the guest app is told the drag failed", || read(&r.source).contains("cancelled"));
    assert!(!read(&rejecting).contains("data "), "a rejected target was given data: {}", read(&rejecting));
    assert!(!read(&r.source).contains("dropped"), "{}", read(&r.source));
}

#[test]
fn a_client_in_a_domain_hands_the_host_everything_but_its_paths() {
    let cg = my_cgroup();
    let mut r = out_rig(&["--deny-cgroup", &cg], &["text/uri-list|file:///run/aurora/drops/1/photo.png\r\n", &format!("{TXT}|/run/aurora/drops/1/photo.png"), "image/png|PNGDATA"]);
    pick_up_in_guest(&mut r);
    out_to_host(&mut r);
    wait_for("the host target sees the drag", || read(&r.host_target).contains("enter"));
    let enter = lines(&r.host_target).into_iter().find(|l| l.starts_with("enter")).unwrap();
    assert!(enter.ends_with("types=image/png"), "a domain path was offered to the host: {enter}");
    r.host.inject("release");
    wait_for("the host target read the data", || read(&r.host_target).contains("finished"));
    let t = read(&r.host_target);
    assert!(t.contains("data image/png len 7"), "{t}");
    assert!(!t.contains("drops") && !t.contains("photo.png"), "a domain path reached the host: {t}");
    assert!(r.guest.log().contains("withholding [text/uri-list, text/plain;charset=utf-8] from the host"), "{}", r.guest.log());
}

#[test]
fn a_pure_file_drag_out_of_a_domain_is_never_handed_to_the_host() {
    let cg = my_cgroup();
    let mut r = out_rig(&["--deny-cgroup", &cg], &["text/uri-list|file:///run/aurora/drops/1/photo.png\r\n"]);
    pick_up_in_guest(&mut r);
    out_to_host(&mut r);
    wait_for("the refusal is logged", || r.guest.log().contains("it is not handed to the host"));
    r.host.inject("release");
    let t = read(&r.host_target);
    assert!(!t.contains("enter") && !t.contains("data "), "the host saw a drag it could not use: {t}");
}
