#![allow(irrefutable_let_patterns)]

mod handlers;

mod capture;
mod fx;
mod grabs;
mod headless;
mod render;
mod scale;
mod input;
mod shell;
mod state;
mod stats;
mod text;
mod trusted;
mod winit;

use smithay::reexports::{calloop::EventLoop, wayland_server::Display};
pub use state::Aurora;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    init_logging();

    let mut event_loop: EventLoop<Aurora> = EventLoop::try_new()?;

    let display: Display<Aurora> = Display::new()?;

    let mut state = Aurora::new(&mut event_loop, display);

    let args = parse_args();
    if args.test_input.is_some() && args.headless.is_none() {
        return Err("--test-input is a test affordance that exists only with --headless (no input device); refusing".into());
    }
    state.fx = fx::Fx::new(args.theme.clone().or_else(default_theme_dir));
    state.fx.preload_theme();
    state.shell.legacy_scene = args.legacy_scene;
    state.shell.freeze_time = args.freeze_time;
    state.shell.registry = shell::apps::Registry::new(args.bus_socket.clone());
    if let Some(a) = &args.aurora_bin {
        state.shell.aurora_bin = a.clone();
    }
    state.apply_input_config();
    state.trusted.dump_path = args.dump_overlay.clone();
    if let Some(uids) = args.trusted_uids.clone() {
        state.trusted.allow.uids = uids;
    }
    state.trusted.allow.deny_prefixes = args.deny_cgroups.clone();
    state.trusted.bus_sock = args.bus_socket.clone();
    match &args.bus_socket {
        Some(p) => eprintln!("aurora-compositor: domain claims are checked against busd at {}", p.display()),
        None => eprintln!("aurora-compositor: WARNING: no --bus-socket / AURORA_BUS_SOCK: every domain claim will show as UNVERIFIED (unknown)"),
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
    if let Some(p) = &args.dump_overlay {
        eprintln!("aurora-compositor: WARNING: --dump-overlay {}: the overlay will be written to a file (demo/test only)", p.display());
    }

    // Open a Wayland/X11 window for our nested compositor
    match args.headless {
        Some((w, h)) => crate::headless::init_headless(&mut event_loop, &mut state, w, h, args.scale.unwrap_or(1.0))?,
        None => crate::winit::init_winit(&mut event_loop, &mut state)?,
    }

    // Set WAYLAND_DISPLAY to our socket name, so child processes connect to Aurora rather
    // than the host compositor
    unsafe { std::env::set_var("WAYLAND_DISPLAY", &state.socket_name) };

    println!("aurora-compositor: socket {}", state.socket_name.to_string_lossy());

    // Optionally spawn a client (-c CMD)
    // Spawn a test client, that will run under Aurora
    spawn_client();

    event_loop.run(None, &mut state, move |state| {
        // Replies must not wait for a redraw: with the host window hidden or
        // occluded no Redraw arrives, and a client would block in its first
        // roundtrip.
        let _ = state.display_handle.flush_clients();
    })?;

    Ok(())
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
        headless: None, command: None, trusted_socket: None, trusted_uids: None, bus_socket: std::env::var_os("AURORA_BUS_SOCK").filter(|v| !v.is_empty()).map(Into::into),
        deny_cgroups: vec![], test_input: None, dump_overlay: None, theme: None,
        legacy_scene: false, freeze_time: false, scale: None, aurora_bin: None, frame_stats: None,
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
            "--legacy-scene" => args.legacy_scene = true,
            "--freeze-time" => args.freeze_time = true,
            "--scale" => args.scale = it.next().and_then(|v| v.parse().ok()).filter(|s: &f64| s.is_finite() && *s >= 0.5 && *s <= 8.0),
            "--aurora" => args.aurora_bin = it.next(),
            "--frame-stats" => args.frame_stats = it.next().map(Into::into),
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
