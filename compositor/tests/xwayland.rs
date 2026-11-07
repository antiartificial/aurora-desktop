//! XWayland: real X11 clients, a headless compositor started with `--xwayland`, and assertions on a
//! captured frame. X11 windows are ordinary windows in the `Space`, so the capture path shows them.
//!
//! Scale: X11 clients get scale 1, i.e. an X window of N x M pixels is N x M LOGICAL units and
//! covers N*s x M*s device pixels at output scale s (see `src/xwayland.rs`). The tests below check
//! that at s = 1 and s = 2.
//!
//! The tests need `Xwayland` (and `xmessage` for the real-client one) on PATH; without them they
//! say so and return, because the machine, not the compositor, is what is missing.

mod common;
use common::*;

use std::process::{Child, Command, Stdio};

/// Aurora's background with `--legacy-scene`.
const BG: [u8; 3] = [25, 25, 25];

fn on_path(bin: &str) -> bool {
    std::env::var_os("PATH").is_some_and(|p| std::env::split_paths(&p).any(|d| d.join(bin).is_file()))
}

fn have_xwayland() -> bool {
    let ok = on_path("Xwayland");
    if !ok { eprintln!("SKIPPED: Xwayland is not installed on this machine; XWayland is untested here"); }
    ok
}

/// Kills the X client when dropped.
struct X(Child);
impl Drop for X { fn drop(&mut self) { let _ = self.0.kill(); let _ = self.0.wait(); } }

/// The display Aurora's Xwayland is on, from the compositor's log. Never the user's own `DISPLAY`.
fn x_display(c: &Comp) -> String {
    wait_for("xwayland ready", || c.log().contains("xwayland: ready on DISPLAY=:"));
    let log = c.log();
    let i = log.find("xwayland: ready on DISPLAY=:").unwrap() + "xwayland: ready on DISPLAY=".len();
    log[i..].chars().take_while(|ch| *ch == ':' || ch.is_ascii_digit()).collect()
}

fn start_x(c: &Comp, display: &str, cmd: &str, args: &[&str]) -> X {
    let bin = if cmd == "xclient" { example_path("xclient") } else { cmd.into() };
    X(Command::new(bin).args(args).env("DISPLAY", display).env_remove("WAYLAND_DISPLAY")
        .env("XDG_RUNTIME_DIR", c.dir.path()).stdout(Stdio::null()).stderr(Stdio::null()).spawn().unwrap_or_else(|e| panic!("start {cmd}: {e}")))
}

fn example_path(name: &str) -> std::path::PathBuf {
    let exe = std::env::current_exe().unwrap();
    exe.parent().unwrap().parent().unwrap().join("examples").join(name)
}

fn comp(scale: &str) -> Comp { Comp::start(&["--xwayland", "--scale", scale]) }

#[test]
fn a_real_x_client_is_mapped_and_drawn_at_scale_1_and_2() {
    if !have_xwayland() { return; }
    if !on_path("xmessage") { eprintln!("SKIPPED: xmessage is not installed"); return; }
    for (scale, k) in [("1", 1usize), ("2", 2)] {
        let c = comp(scale);
        let d = x_display(&c);
        let _x = start_x(&c, &d, "xmessage", &["-geometry", "300x120+0+0", "-default", "okay", "hello from X11"]);
        wait_for("the window mapped", || c.log().contains("xwayland: mapped \"xmessage\""));
        wait_for("the window has a surface", || c.log().contains("has its surface"));
        let log = c.log();
        assert!(log.contains("size Size<smithay::utils::geometry::Logical> { w: 300, h: 120 }"), "X size 300x120 is 300x120 logical at scale {scale}: {log}");
        // xmessage paints a white window (text and a button on it): wait for the first frame that has it.
        let mut img = c.capture("x.png");
        for _ in 0..50 {
            if img.count([255, 255, 255]) > 0 { break; }
            std::thread::sleep(std::time::Duration::from_millis(100));
            img = c.capture("x.png");
        }
        let (x0, y0, x1, y1, n) = img.bbox([255, 255, 255]).expect("no white pixels: the X window is not on screen");
        println!("SCALE {scale}: xmessage white pixels bbox ({x0},{y0})-({x1},{y1}), {n} of {} in its rectangle", 300 * k * 120 * k);
        // At the origin of the usable area, covering exactly 300x120 logical units = 300k x 120k device pixels.
        assert_eq!((x0, y0, x1 + 1, y1 + 1), (0, 0, 300 * k, 120 * k), "scale {scale}: the X window covers its logical size in device pixels");
        assert!(n > (300 * k * 120 * k) / 2, "most of an xmessage window is white");
        assert_eq!(img.at(300 * k + 10, 60 * k), BG, "background right of the window");
        assert_eq!(img.at(100 * k, 120 * k + 10), BG, "background below the window");
    }
}

#[test]
fn a_managed_window_is_placed_in_the_usable_area_and_gone_when_the_client_exits() {
    if !have_xwayland() { return; }
    let c = comp("1");
    let d = x_display(&c);
    let x = start_x(&c, &d, "xclient", &["--geometry", "300,300,200,100", "--rgb", "10,200,240"]);
    wait_for("the window has a surface", || c.log().contains("has its surface"));
    std::thread::sleep(std::time::Duration::from_millis(300));
    let img = c.capture("m.png");
    let (x0, y0, x1, y1, n) = img.bbox([10, 200, 240]).expect("the X window is not on screen");
    println!("managed window ({x0},{y0})-({x1},{y1}) n={n}");
    // A managed client does not choose its position: Aurora puts it at the origin of the usable area.
    assert_eq!((x0, y0, x1 + 1, y1 + 1, n), (0, 0, 200, 100, 200 * 100));
    drop(x);
    wait_for("the window is gone from the frame", || c.capture("m2.png").count([10, 200, 240]) == 0);
}

#[test]
fn an_override_redirect_window_appears_exactly_where_it_asked() {
    if !have_xwayland() { return; }
    // (scale, X geometry x,y,w,h) in logical units; the expected device rectangle is the geometry times scale.
    for (scale, k, geo) in [("1", 1usize, [400usize, 300, 200, 100]), ("2", 2, [100, 50, 120, 60])] {
        let c = comp(scale);
        let d = x_display(&c);
        let g = format!("{},{},{},{}", geo[0], geo[1], geo[2], geo[3]);
        let _x = start_x(&c, &d, "xclient", &["--geometry", &g, "--rgb", "240,120,10", "--override-redirect"]);
        wait_for("override-redirect window mapped", || c.log().contains("xwayland: override-redirect window mapped"));
        std::thread::sleep(std::time::Duration::from_millis(300));
        let img = c.capture("o.png");
        let (x0, y0, x1, y1, n) = img.bbox([240, 120, 10]).expect("the override-redirect window is not on screen");
        println!("SCALE {scale}: override-redirect ({x0},{y0})-({x1},{y1}) n={n}");
        assert_eq!((x0, y0, x1 + 1, y1 + 1, n), (geo[0] * k, geo[1] * k, (geo[0] + geo[2]) * k, (geo[1] + geo[3]) * k, geo[2] * k * geo[3] * k));
        // It is not given the keyboard: only managed windows log `has its surface`.
        assert!(!c.log().contains("has its surface"), "an override-redirect window is not focused");
    }
}

#[test]
fn xwayland_is_restarted_on_the_same_display_if_it_dies() {
    if !have_xwayland() { return; }
    let c = comp("1");
    let d = x_display(&c);
    let x = start_x(&c, &d, "xclient", &["--geometry", "0,0,50,50", "--rgb", "10,200,240"]);
    wait_for("first window", || c.log().contains("has its surface"));
    // Kill the Xwayland that is a child of THIS compositor (the host's own Xwayland is not).
    let kids = Command::new("ps").args(["-o", "pid=,comm=", "--ppid", &c.pid().to_string()]).output().unwrap();
    let pids: Vec<String> = String::from_utf8_lossy(&kids.stdout).lines().filter_map(|l| l.trim_start().split_once(char::is_whitespace).filter(|(_, comm)| comm.trim() == "Xwayland").map(|(p, _)| p.trim().to_string())).collect();
    assert_eq!(pids.len(), 1, "exactly one Xwayland child of the compositor: {pids:?}");
    assert!(Command::new("kill").args(["-KILL", &pids[0]]).status().unwrap().success());
    drop(x);
    wait_for("xwayland restarted", || c.log().matches("xwayland: ready on").count() == 2);
    assert_eq!(c.log().matches(&format!("xwayland: ready on DISPLAY={d}")).count(), 2, "same display number: {}", c.log());
    wait_for("the dead window left the scene", || c.capture("r0.png").count([10, 200, 240]) == 0);
    let _y = start_x(&c, &d, "xclient", &["--geometry", "0,0,60,40", "--rgb", "200,10,10"]);
    wait_for("second client's window on screen", || c.capture("r.png").count([200, 10, 10]) == 60 * 40);
}
