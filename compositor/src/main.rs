#![allow(irrefutable_let_patterns)]

mod handlers;

mod capture;
mod grabs;
mod headless;
mod render;
mod input;
mod state;
mod winit;

use smithay::reexports::{calloop::EventLoop, wayland_server::Display};
pub use state::Aurora;

fn main() -> Result<(), Box<dyn std::error::Error>> {
    init_logging();

    let mut event_loop: EventLoop<Aurora> = EventLoop::try_new()?;

    let display: Display<Aurora> = Display::new()?;

    let mut state = Aurora::new(&mut event_loop, display);

    // Open a Wayland/X11 window for our nested compositor
    match parse_args().headless {
        Some((w, h)) => crate::headless::init_headless(&mut event_loop, &mut state, w, h)?,
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
        tracing_subscriber::fmt().with_env_filter(env_filter).init();
    } else {
        tracing_subscriber::fmt().init();
    }
}

struct Args {
    /// `--headless[=WxH]`: no host window; offscreen output of this size.
    headless: Option<(i32, i32)>,
    command: Option<String>,
}

fn parse_args() -> Args {
    let mut args = Args { headless: None, command: None };
    let mut it = std::env::args().skip(1);
    while let Some(a) = it.next() {
        match a.as_str() {
            "-c" | "--command" => args.command = it.next(),
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
