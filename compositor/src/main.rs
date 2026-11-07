#![allow(irrefutable_let_patterns)]

mod handlers;

mod capture;
mod clipboard;
mod dnd;
mod drm;
mod fx;
mod grabs;
mod headless;
mod indicator;
mod render;
mod scale;
mod input;
mod peer;
mod persist;
mod shell;
mod state;
mod stats;
mod text;
mod trusted;
mod windeco;
mod winit;
mod xwayland;

use smithay::reexports::{calloop::EventLoop, wayland_server::Display};
pub use state::Aurora;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    init_logging();

    // parse_args before anything opens a socket: the DRM checks below must be
    // able to fail without having started a wayland display.
    let args = parse_args();
    if args.drm.is_some() && args.headless.is_some() {
        fail("--drm and --headless are mutually exclusive: --drm drives a real display and input devices, --headless has none");
    }
    // Everything the DRM backend needs from the system is checked here, before
    // sockets are opened or anything else starts.
    let drm_prepared = match &args.drm {
        Some(card) => Some(drm::prepare(card.as_deref()).unwrap_or_else(|e| fail(&e))),
        None => None,
    };
    let mut event_loop: EventLoop<'static, Aurora> = EventLoop::try_new()?;

    let display: Display<Aurora> = Display::new()?;

    let mut state = Aurora::new(&mut event_loop, display);

    if args.test_input.is_some() && args.headless.is_none() {
        return Err("--test-input is a test affordance that exists only with --headless (no input device); refusing".into());
    }
    state.fx = fx::Fx::new(args.theme.clone().or_else(default_theme_dir));
    state.fx.preload_theme();
    state.shell.legacy_scene = args.legacy_scene;
    state.shell.freeze_time = args.freeze_time;
    state.shell.registry = shell::apps::Registry::new(args.bus_socket.clone());
    state.shell.aurora_bin = crate::shell::resolve_launcher(args.aurora_bin.as_deref());
    state.apply_input_config();
    state.trusted.dump_path = args.dump_overlay.clone();
    if let Some(uids) = args.trusted_uids.clone() {
        state.trusted.allow.uids = uids;
    }
    state.trusted.allow.deny_prefixes = args.deny_cgroups.clone();
    state.trusted.bus_sock = args.bus_socket.clone();
    if let Some(p) = &args.bus_socket {
        indicator::bus::spawn(&event_loop.handle(), p.clone());
        state.indicator.watching = true;
    }
    match &args.bus_socket {
        Some(p) => eprintln!("aurora-compositor: domain claims are checked against busd at {}", p.display()),
        None => eprintln!("aurora-compositor: WARNING: no --bus-socket / AURORA_BUS_SOCK: every domain claim will show as UNVERIFIED (unknown), and the capability indicator has nothing to learn grants from, so it will NEVER light"),
    }
    let kp = trusted::sign::key_path();
    match trusted::sign::load_key(&kp, true) {
        Ok(k) => {
            state.trusted.sign_key = Some(k);
            eprintln!("aurora-compositor: replies are signed with {} (anything that can read it can forge a reply)", kp.display());
        }
        Err(e) => eprintln!("aurora-compositor: WARNING: approval key {} unusable ({e}): replies are unsigned and the controller will refuse them", kp.display()),
    }
    let tsock = args.trusted_socket.clone().unwrap_or_else(default_trusted_socket);
    match trusted::socket::listen(&event_loop.handle(), &tsock) {
        Ok(()) => println!(
            "aurora-compositor: trusted socket {} (uids {:?}, refusing peers in cgroup component {:?})",
            tsock.display(), state.trusted.allow.uids, state.trusted.allow.deny_components
        ),
        Err(e) => eprintln!("aurora-compositor: WARNING: no trusted socket, approvals cannot be drawn: {e}"),
    }
    if let Some(p) = &args.test_input {
        trusted::testinput::listen(&event_loop.handle(), p)?;
        eprintln!("aurora-compositor: WARNING: --test-input {}: keyboard injection ENABLED (headless only; no real input device exists in this process)", p.display());
    }
    if let Some(p) = &args.frame_stats {
        match stats::Stats::create(p) {
            Ok(st) => {
                state.stats = Some(st);
                state.fx.phases.enabled = true;
                eprintln!("aurora-compositor: WARNING: --frame-stats {}: per-frame timings are written to a file, and the effects pass blocks the CPU on the GPU at each phase end to time it (diagnostic: slightly slower than a normal run)", p.display());
            }
            Err(e) => eprintln!("aurora-compositor: WARNING: --frame-stats {}: cannot create the file ({e}); no timings will be recorded", p.display()),
        }
    }
    if let Some(p) = &args.state_file {
        let (ps, note) = persist::Persist::open(p, std::time::Instant::now());
        eprintln!("aurora-compositor: layout memory {}: {note}", p.display());
        state.persist = Some(ps);
    }
    if let Some(p) = &args.dump_overlay {
        eprintln!("aurora-compositor: WARNING: --dump-overlay {}: the overlay will be written to a file (demo/test only)", p.display());
    }

    // Open a Wayland/X11 window for our nested compositor
    match (args.headless, drm_prepared) {
        (Some((w, h)), _) => crate::headless::init_headless(&mut event_loop, &mut state, w, h, args.scale.unwrap_or(1.0))?,
        (None, Some(p)) => crate::drm::init_drm(&mut event_loop, &mut state, p, args.scale)
            .unwrap_or_else(|e| fail(&e.to_string())),
        (None, None) => crate::winit::init_winit(&mut event_loop, &mut state)?,
    }

    // The host clipboard bridge. Before WAYLAND_DISPLAY is repointed at Aurora's own socket below,
    // and only with a window on a host: headless and DRM have none, and in a user's session
    // WAYLAND_DISPLAY still names their real compositor.
    let clip = if args.no_host_clipboard {
        clipboard::Choice::Disabled
    } else if let Some(d) = args.host_clipboard_display.clone() {
        clipboard::Choice::Display(d)
    } else if args.headless.is_some() {
        clipboard::Choice::NoHost("headless has no host")
    } else if args.drm.is_some() {
        clipboard::Choice::NoHost("--drm has no host compositor")
    } else {
        clipboard::Choice::Env
    };
    clipboard::start(&mut state, &event_loop.handle(), clip);

    // The host drag-and-drop bridge: same rules as the clipboard's, but it listens on winit's own
    // connection (a drag is only delivered to the client that owns the surface).
    let dnd_choice = if args.no_host_dnd {
        dnd::Choice::Disabled
    } else if let Some(d) = args.host_dnd_display.clone() {
        dnd::Choice::StandIn(d)
    } else if args.headless.is_some() {
        dnd::Choice::NoHost("headless has no host")
    } else if args.drm.is_some() {
        dnd::Choice::NoHost("--drm has no host compositor")
    } else if let Some(d) = state.host_display {
        dnd::Choice::Winit(d)
    } else {
        dnd::Choice::NoHost("the host window is not a Wayland window")
    };
    dnd::start(&mut state, &event_loop.handle(), dnd_choice);

    // One timer for every backend, installed after the backend so it cannot be
    // lost by a backend that forgets to honour it. Headless has no window to
    // close and no VT to switch, so without this it only ever stops by being
    // killed, and the clean shutdown path is unreachable.
    if let Some(d) = args.quit_after {
        eprintln!("aurora-compositor: --quit-after: quitting by itself in {d:?}");
        event_loop.handle().insert_source(
            smithay::reexports::calloop::timer::Timer::from_duration(d),
            |_, _, state: &mut Aurora| {
                tracing::info!("--quit-after elapsed; stopping the event loop");
                state.loop_signal.stop();
                smithay::reexports::calloop::timer::TimeoutAction::Drop
            },
        )?;
    }


    // Set WAYLAND_DISPLAY to our socket name, so child processes connect to Aurora rather
    // than the host compositor
    unsafe { std::env::set_var("WAYLAND_DISPLAY", &state.socket_name) };

    println!("aurora-compositor: socket {}", state.socket_name.to_string_lossy());

    if args.xwayland {
        match state.start_xwayland(event_loop.handle()) {
            Ok(n) => println!("aurora-compositor: xwayland DISPLAY=:{n}"),
            Err(e) => eprintln!("aurora-compositor: WARNING: --xwayland: cannot start Xwayland ({e}); X11 clients will not run"),
        }
    }

    // Optionally spawn a client (-c CMD)
    // Spawn a test client, that will run under Aurora
    spawn_client();

    event_loop.run(None, &mut state, move |state| {
        // Replies must not wait for a redraw: with the host window hidden or
        // occluded no Redraw arrives, and a client would block in its first
        // roundtrip.
        let _ = state.display_handle.flush_clients();
    })?;

    // The loop has stopped: a window close, a VT quit, or --quit-after.
    //
    // Then leave without running libc's exit handlers. NVIDIA's EGL driver
    // registers one that logs, the log goes to the EGL debug callback Smithay
    // installed, and that callback reads a thread_local Rust has already
    // destroyed -- so a clean shutdown printed six copies of "cannot access a
    // Thread Local Storage value during or after destruction: AccessError"
    // after the last frame. Harmless (the exit code was still 0) but it buried
    // whatever a real shutdown error would have said. Diagnosed from the
    // backtrace: __run_exit_handlers -> smithay::backend::egl::ffi::egl_debug_log
    // -> LocalKey::with -> panic_access_error.
    //
    // So: do our own cleanup explicitly, flush, and _exit. Anything that must
    // happen at shutdown has to happen above this line, not in a Drop impl.
    // The placement memory writes only after the layout has been still for
    // 400 ms, so a clean quit inside that window would drop the last move.
    if let Some(p) = state.persist.as_mut() {
        p.flush();
    }
    drop(state);
    use std::io::Write;
    let _ = std::io::stdout().flush();
    let _ = std::io::stderr().flush();
    unsafe { libc::_exit(0) }
}

/// A startup refusal: the message, plainly, and a failing exit. Not an `Error:
/// "..."` Debug dump, which would escape the newlines in the advice.
fn fail(msg: &str) -> ! {
    eprintln!("aurora-compositor: {}", msg.trim_start_matches("aurora-compositor: "));
    std::process::exit(1)
}

fn init_logging() {
    if let Ok(env_filter) = tracing_subscriber::EnvFilter::try_from_default_env() {
        tracing_subscriber::fmt().with_writer(std::io::stderr).with_env_filter(env_filter).init();
    } else {
        tracing_subscriber::fmt().with_writer(std::io::stderr).init();
    }
}

struct Args {
    /// `--headless[=WxH]`: no host window; offscreen output of this size.
    headless: Option<(i32, i32)>,
    /// `--drm[=CARD]`: DRM/KMS + libinput on a bare TTY. `Some(None)` picks the GPU.
    drm: Option<Option<std::path::PathBuf>>,
    /// `--quit-after SECS`: stop the event loop by itself, on any backend. For
    /// a first run on hardware that could leave you with no way back, and for
    /// exercising the clean shutdown path, which otherwise only a window close
    /// can reach.
    quit_after: Option<std::time::Duration>,
    command: Option<String>,
    trusted_socket: Option<std::path::PathBuf>,
    trusted_uids: Option<Vec<u32>>,
    bus_socket: Option<std::path::PathBuf>,
    deny_cgroups: Vec<String>,
    test_input: Option<std::path::PathBuf>,
    dump_overlay: Option<std::path::PathBuf>,
    theme: Option<std::path::PathBuf>,
    legacy_scene: bool,
    freeze_time: bool,
    /// `--scale F`: the output scale for headless (winit reads the host's).
    scale: Option<f64>,
    aurora_bin: Option<String>,
    /// `--frame-stats PATH`: write per-frame timings (diagnostic; makes the effects pass block on the GPU at phase ends).
    frame_stats: Option<std::path::PathBuf>,
    /// `--xwayland`: start Xwayland and export `DISPLAY`.
    xwayland: bool,
    /// `--state-file PATH`: remember where windows were and put them back after a restart.
    state_file: Option<std::path::PathBuf>,
    /// `--no-host-clipboard`: do not bridge copy/paste to the host.
    no_host_clipboard: bool,
    /// `--host-clipboard-display NAME`: bridge to this host socket instead of `WAYLAND_DISPLAY` (test affordance: a second Aurora can stand in for the host).
    host_clipboard_display: Option<std::ffi::OsString>,
    /// `--no-host-dnd`: do not bridge drag and drop from the host.
    no_host_dnd: bool,
    /// `--host-dnd-display NAME`: open a stand-in window on this host socket and bridge drags to it (test affordance).
    host_dnd_display: Option<std::ffi::OsString>,
}

/// The repo's theme directory, if this binary runs from its build tree.
fn default_theme_dir() -> Option<std::path::PathBuf> {
    let d = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../contracts/theme");
    d.is_dir().then_some(d)
}

fn default_trusted_socket() -> std::path::PathBuf {
    let dir = std::env::var_os("XDG_RUNTIME_DIR").map(std::path::PathBuf::from).unwrap_or_else(|| "/tmp".into());
    dir.join("aurora-trusted.sock")
}

fn parse_args() -> Args {
    let mut args = Args {
        headless: None, drm: None, quit_after: None, command: None, trusted_socket: None, trusted_uids: None, bus_socket: std::env::var_os("AURORA_BUS_SOCK").filter(|v| !v.is_empty()).map(Into::into),
        deny_cgroups: vec![], test_input: None, dump_overlay: None, theme: None,
        legacy_scene: false, freeze_time: false, scale: None, aurora_bin: None, frame_stats: None, xwayland: false, state_file: None, no_host_clipboard: false, host_clipboard_display: None, no_host_dnd: false, host_dnd_display: None,
    };
    let mut it = std::env::args().skip(1);
    while let Some(a) = it.next() {
        match a.as_str() {
            "-c" | "--command" => args.command = it.next(),
            "--trusted-socket" => args.trusted_socket = it.next().map(Into::into),
            "--trusted-uid" => {
                let uid = it.next().and_then(|v| v.parse().ok()).expect("--trusted-uid wants a number");
                args.trusted_uids.get_or_insert_with(Vec::new).push(uid);
            }
            "--bus-socket" => args.bus_socket = it.next().map(Into::into),
            "--deny-cgroup" => args.deny_cgroups.extend(it.next()),
            "--test-input" => args.test_input = it.next().map(Into::into),
            "--theme" => args.theme = it.next().map(Into::into),
            "--dump-overlay" => args.dump_overlay = it.next().map(Into::into),
            "--xwayland" => args.xwayland = true,
            "--no-host-clipboard" => args.no_host_clipboard = true,
            "--host-clipboard-display" => args.host_clipboard_display = it.next().map(Into::into),
            "--no-host-dnd" => args.no_host_dnd = true,
            "--host-dnd-display" => args.host_dnd_display = it.next().map(Into::into),
            "--legacy-scene" => args.legacy_scene = true,
            "--freeze-time" => args.freeze_time = true,
            "--scale" => args.scale = it.next().and_then(|v| v.parse().ok()).filter(|s: &f64| s.is_finite() && *s >= 0.5 && *s <= 8.0),
            "--aurora" => args.aurora_bin = it.next(),
            "--state-file" => args.state_file = it.next().map(Into::into),
            "--frame-stats" => args.frame_stats = it.next().map(Into::into),
            "--drm" => args.drm = Some(None),
            s if s.starts_with("--drm=") => args.drm = Some(Some(s["--drm=".len()..].into())),
            "--quit-after" => {
                let secs: u64 = it.next().and_then(|v| v.parse().ok()).filter(|s| *s > 0).expect("--quit-after wants a number of seconds");
                args.quit_after = Some(std::time::Duration::from_secs(secs));
            }
            "--headless" => args.headless = Some((1280, 800)),
            s if s.starts_with("--headless=") => {
                let dims = s["--headless=".len()..].split_once('x');
                let parsed = dims.and_then(|(w, h)| Some((w.parse().ok()?, h.parse().ok()?)));
                args.headless = Some(parsed.unwrap_or_else(|| {
                    eprintln!("aurora-compositor: bad --headless size, want WxH; using 1280x800");
                    (1280, 800)
                }));
            }
            other => eprintln!("aurora-compositor: ignoring unknown argument {other}"),
        }
    }
    args
}

fn spawn_client() {
    if let Some(command) = parse_args().command {
        std::process::Command::new(command).spawn().ok();
    }
}
