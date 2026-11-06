//! aurora-capture: ask a Wayland compositor for one frame of an output over
//! ext-image-copy-capture-v1, and write it to a PNG.
//!
//! The compositor does the rendering. A host screenshot of a nested compositor
//! is blank, so the nested compositor must be asked for the pixels itself.

use std::fs::File;
use std::io::{BufWriter, Write};
use std::os::fd::AsFd;
use std::os::unix::net::UnixStream;
use std::path::PathBuf;
use std::process::ExitCode;

use memmap2::MmapMut;
use wayland_client::globals::{registry_queue_init, GlobalList, GlobalListContents};
use wayland_client::protocol::wl_output::{self, WlOutput};
use wayland_client::protocol::wl_registry::WlRegistry;
use wayland_client::protocol::wl_shm::{self, WlShm};
use wayland_client::protocol::wl_shm_pool::WlShmPool;
use wayland_client::protocol::wl_buffer::WlBuffer;
use wayland_client::{delegate_noop, Connection, Dispatch, EventQueue, QueueHandle, WEnum};
use wayland_protocols::ext::image_capture_source::v1::client::{
    ext_image_capture_source_v1::ExtImageCaptureSourceV1,
    ext_output_image_capture_source_manager_v1::ExtOutputImageCaptureSourceManagerV1,
};
use wayland_protocols::ext::image_copy_capture::v1::client::{
    ext_image_copy_capture_frame_v1::{self, ExtImageCopyCaptureFrameV1, FailureReason},
    ext_image_copy_capture_manager_v1::{ExtImageCopyCaptureManagerV1, Options},
    ext_image_copy_capture_session_v1::{self, ExtImageCopyCaptureSessionV1},
};

const COPY_CAPTURE: &str = "ext_image_copy_capture_manager_v1";
const SOURCE_MANAGER: &str = "ext_output_image_capture_source_manager_v1";
const COPY_CAPTURE_PROTOCOL: &str = "ext-image-copy-capture-v1";

/// Formats we can convert, in order of preference. Xrgb8888 first: its alpha
/// byte is undefined, so we ignore it and emit opaque pixels.
const SUPPORTED_FORMATS: [wl_shm::Format; 4] = [
    wl_shm::Format::Xrgb8888,
    wl_shm::Format::Argb8888,
    wl_shm::Format::Xbgr8888,
    wl_shm::Format::Abgr8888,
];

const USAGE: &str = "\
Usage: aurora-capture [-o PATH] [--display NAME] [--list]

Captures one frame of the first output of a Wayland compositor over
ext-image-copy-capture-v1 and writes it as an RGBA PNG.

  -o, --output PATH   PNG to write (default: capture.png)
  --display NAME      Wayland socket name, or an absolute path
                      (default: $WAYLAND_DISPLAY)
  --list              print the globals the compositor advertises, then exit
  -h, --help          show this help

Exit status: 0 on success, 1 on failure, 2 on bad arguments.";

#[derive(Default)]
struct State {
    // Filled by session events. The session is ready once `session_done` is set.
    buffer_size: Option<(u32, u32)>,
    shm_formats: Vec<wl_shm::Format>,
    session_done: bool,
    session_stopped: bool,

    // Filled by frame events for the one frame we request.
    frame_ready: bool,
    frame_failed: Option<FailureReason>,
    frame_transform: Option<wl_output::Transform>,
}

struct Args {
    output: PathBuf,
    display: Option<String>,
    list: bool,
}

fn parse_args() -> Result<Option<Args>, String> {
    let mut args = Args { output: PathBuf::from("capture.png"), display: None, list: false };
    let mut it = std::env::args().skip(1);
    while let Some(a) = it.next() {
        match a.as_str() {
            "-h" | "--help" => {
                println!("{USAGE}");
                return Ok(None);
            }
            "-o" | "--output" => {
                args.output = PathBuf::from(it.next().ok_or("-o needs a PATH")?);
            }
            "--display" => {
                args.display = Some(it.next().ok_or("--display needs a NAME")?);
            }
            "--list" => args.list = true,
            other => return Err(format!("unknown argument: {other}")),
        }
    }
    Ok(Some(args))
}

/// Connects to the named socket, or to $WAYLAND_DISPLAY when none is given.
/// A relative name is looked up in $XDG_RUNTIME_DIR, as libwayland does.
fn connect(display: Option<&str>) -> Result<Connection, String> {
    let Some(name) = display else {
        return Connection::connect_to_env()
            .map_err(|e| format!("cannot connect to $WAYLAND_DISPLAY: {e}"));
    };
    let path = if name.starts_with('/') {
        PathBuf::from(name)
    } else {
        let dir = std::env::var_os("XDG_RUNTIME_DIR")
            .ok_or("XDG_RUNTIME_DIR is not set; cannot resolve --display")?;
        PathBuf::from(dir).join(name)
    };
    let stream = UnixStream::connect(&path)
        .map_err(|e| format!("cannot connect to {}: {e}", path.display()))?;
    Connection::from_socket(stream).map_err(|e| format!("Wayland handshake failed: {e}"))
}

/// Prints the advertised globals. Returns whether the copy-capture manager is
/// among them.
fn list_globals(globals: &GlobalList) -> bool {
    let list = globals.contents().clone_list();
    for g in &list {
        println!("{:>4}  {:<40} v{}", g.name, g.interface, g.version);
    }
    list.iter().any(|g| g.interface == COPY_CAPTURE)
}

fn has_global(globals: &GlobalList, interface: &str) -> bool {
    globals.contents().with_list(|list| list.iter().any(|g| g.interface == interface))
}

fn run(args: Args) -> Result<(), String> {
    let conn = connect(args.display.as_deref())?;
    let (globals, mut queue): (GlobalList, EventQueue<State>) =
        registry_queue_init(&conn).map_err(|e| format!("registry setup failed: {e}"))?;
    let qh = queue.handle();

    if args.list {
        let capable = list_globals(&globals);
        if capable && has_global(&globals, SOURCE_MANAGER) {
            println!("{COPY_CAPTURE_PROTOCOL}: advertised; this compositor can be captured");
        } else {
            println!("{COPY_CAPTURE_PROTOCOL}: NOT advertised; this compositor cannot be captured by aurora-capture");
        }
        return Ok(());
    }

    let missing = [COPY_CAPTURE, SOURCE_MANAGER]
        .into_iter()
        .filter(|i| !has_global(&globals, i))
        .collect::<Vec<_>>();
    if !missing.is_empty() {
        return Err(format!(
            "this compositor does not advertise {COPY_CAPTURE_PROTOCOL} (missing: {}). \
             It cannot be captured by aurora-capture. Run with --list to see what it does advertise.",
            missing.join(", ")
        ));
    }

    let mut state = State::default();
    let copy_mgr: ExtImageCopyCaptureManagerV1 = bind(&globals, &qh, COPY_CAPTURE)?;
    let source_mgr: ExtOutputImageCaptureSourceManagerV1 = bind(&globals, &qh, SOURCE_MANAGER)?;
    let shm: WlShm = bind(&globals, &qh, "wl_shm")?;
    let output: WlOutput = bind(&globals, &qh, "wl_output")?;

    // The source names the output; the session is what negotiates buffers.
    let source: ExtImageCaptureSourceV1 = source_mgr.create_source(&output, &qh, ());
    let session: ExtImageCopyCaptureSessionV1 =
        copy_mgr.create_session(&source, Options::empty(), &qh, ());

    // Wait for the session's constraints: buffer size and shm formats, then `done`.
    while !state.session_done {
        if state.session_stopped {
            return Err("compositor stopped the capture session before offering buffer constraints".into());
        }
        queue.blocking_dispatch(&mut state).map_err(|e| format!("dispatch failed: {e}"))?;
    }
    // A compositor can stop the session right after it offers constraints.
    // Anvil does this at this Smithay revision: it drops the session object,
    // which stops it. A roundtrip makes sure any such `stopped` has arrived.
    queue.roundtrip(&mut state).map_err(|e| format!("roundtrip failed: {e}"))?;
    if state.session_stopped {
        return Err("compositor stopped the capture session right after offering constraints, \
                    so it cannot deliver a frame".into());
    }
    let (width, height) = state.buffer_size.ok_or("session sent done without buffer_size")?;
    let format = SUPPORTED_FORMATS
        .into_iter()
        .find(|f| state.shm_formats.contains(f))
        .ok_or_else(|| {
            format!(
                "compositor offers no shm format we can convert (offered: {:?})",
                state.shm_formats
            )
        })?;

    // wl_shm rows are tightly packed here: 4 bytes per pixel, stride = width * 4.
    let stride = width as usize * 4;
    let len = stride * height as usize;
    let file = shm_file(len)?;
    // SAFETY: the file is our own unlinked temp file, sized to `len` above. The
    // compositor writes into it only while we are inside capture(); we read
    // the mapping only after `ready`.
    let map = unsafe { MmapMut::map_mut(&file) }.map_err(|e| format!("mmap failed: {e}"))?;
    let pool: WlShmPool = shm.create_pool(file.as_fd(), len as i32, &qh, ());
    let buffer: WlBuffer = pool.create_buffer(0, width as i32, height as i32, stride as i32, format, &qh, ());

    let frame: ExtImageCopyCaptureFrameV1 = session.create_frame(&qh, ());
    frame.attach_buffer(&buffer);
    frame.damage_buffer(0, 0, width as i32, height as i32);
    frame.capture();

    while !state.frame_ready {
        if let Some(reason) = state.frame_failed {
            return Err(format!("compositor failed the capture: {reason:?}"));
        }
        queue.blocking_dispatch(&mut state).map_err(|e| format!("dispatch failed: {e}"))?;
    }

    // Pixels are only valid if the output is unrotated. Rotating here would
    // be a guess, so refuse rather than write a wrong image.
    if let Some(t) = state.frame_transform {
        if t != wl_output::Transform::Normal {
            return Err(format!("output transform is {t:?}; rotated captures are not supported yet"));
        }
    }

    let rgba = to_rgba(&map[..len], width as usize, height as usize, stride, format);
    write_png(&args.output, width, height, &rgba)?;
    println!(
        "wrote {} ({}x{}, {:?} converted to RGBA)",
        args.output.display(),
        width,
        height,
        format
    );
    Ok(())
}

fn bind<I>(globals: &GlobalList, qh: &QueueHandle<State>, interface: &str) -> Result<I, String>
where
    I: wayland_client::Proxy + 'static,
    State: Dispatch<I, ()>,
{
    // bind() panics if the upper bound is above the version this build's
    // protocol XML knows. The compositor's advertised version is clamped below.
    globals
        .bind::<I, _, _>(qh, 1..=I::interface().version, ())
        .map_err(|e| format!("cannot bind {interface}: {e}"))
}

/// Creates an unlinked file for the shm pool. It lives on the runtime dir's
/// tmpfs, the same place Wayland clients put their shm buffers.
fn shm_file(len: usize) -> Result<File, String> {
    let file = match std::env::var_os("XDG_RUNTIME_DIR") {
        Some(dir) => tempfile::tempfile_in(dir),
        None => tempfile::tempfile(),
    }
    .map_err(|e| format!("cannot create shm file: {e}"))?;
    file.set_len(len as u64).map_err(|e| format!("cannot size shm file: {e}"))?;
    Ok(file)
}

/// Converts a wl_shm frame to tightly packed RGBA8.
///
/// A wl_shm 32-bit format is one little-endian word, so the bytes in memory
/// come out in reverse order of the name: XRGB8888 is B,G,R,X in memory, and
/// XBGR8888 is R,G,B,X. Each format's byte offsets are listed below. The
/// stride comes from the buffer we created, not from width * 4 assumed here.
fn to_rgba(src: &[u8], width: usize, height: usize, stride: usize, format: wl_shm::Format) -> Vec<u8> {
    // Byte offsets of R, G, B and (optionally) A within one 4-byte pixel.
    let (r, g, b, a) = match format {
        wl_shm::Format::Xrgb8888 => (2, 1, 0, None),
        wl_shm::Format::Argb8888 => (2, 1, 0, Some(3)),
        wl_shm::Format::Xbgr8888 => (0, 1, 2, None),
        wl_shm::Format::Abgr8888 => (0, 1, 2, Some(3)),
        other => unreachable!("format {other:?} is not in SUPPORTED_FORMATS"),
    };
    let mut out = Vec::with_capacity(width * height * 4);
    for row in src.chunks(stride).take(height) {
        for px in row[..width * 4].chunks_exact(4) {
            // X formats carry no alpha, so the output is opaque.
            let alpha = a.map_or(255, |i| px[i]);
            out.extend_from_slice(&[px[r], px[g], px[b], alpha]);
        }
    }
    out
}

fn write_png(path: &PathBuf, width: u32, height: u32, rgba: &[u8]) -> Result<(), String> {
    let file = File::create(path).map_err(|e| format!("cannot create {}: {e}", path.display()))?;
    let mut enc = png::Encoder::new(BufWriter::new(file), width, height);
    enc.set_color(png::ColorType::Rgba);
    enc.set_depth(png::BitDepth::Eight);
    let mut writer = enc.write_header().map_err(|e| format!("PNG header: {e}"))?;
    writer.write_image_data(rgba).map_err(|e| format!("PNG data: {e}"))?;
    writer.finish().map_err(|e| format!("PNG finish: {e}"))?;
    std::io::stdout().flush().ok();
    Ok(())
}

fn main() -> ExitCode {
    let args = match parse_args() {
        Ok(Some(args)) => args,
        Ok(None) => return ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("aurora-capture: {e}\n\n{USAGE}");
            return ExitCode::from(2);
        }
    };
    match run(args) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("aurora-capture: {e}");
            ExitCode::FAILURE
        }
    }
}

// Registry: the globals are already collected by registry_queue_init.
impl Dispatch<WlRegistry, GlobalListContents> for State {
    fn event(_: &mut Self, _: &WlRegistry, _: wayland_client::protocol::wl_registry::Event, _: &GlobalListContents, _: &Connection, _: &QueueHandle<Self>) {}
}

impl Dispatch<ExtImageCopyCaptureSessionV1, ()> for State {
    fn event(state: &mut Self, _: &ExtImageCopyCaptureSessionV1, event: ext_image_copy_capture_session_v1::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        use ext_image_copy_capture_session_v1::Event;
        match event {
            Event::BufferSize { width, height } => state.buffer_size = Some((width, height)),
            Event::ShmFormat { format } => {
                if let WEnum::Value(f) = format {
                    state.shm_formats.push(f);
                }
            }
            // The compositor sends dmabuf_device and dmabuf_format too. We only
            // use shm, so they are ignored.
            Event::Done => state.session_done = true,
            Event::Stopped => state.session_stopped = true,
            _ => {}
        }
    }
}

impl Dispatch<ExtImageCopyCaptureFrameV1, ()> for State {
    fn event(state: &mut Self, _: &ExtImageCopyCaptureFrameV1, event: ext_image_copy_capture_frame_v1::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        use ext_image_copy_capture_frame_v1::Event;
        match event {
            Event::Transform { transform } => {
                if let WEnum::Value(t) = transform {
                    state.frame_transform = Some(t);
                }
            }
            // Ready means the buffer now holds the frame.
            Event::Ready => state.frame_ready = true,
            Event::Failed { reason } => {
                state.frame_failed = Some(match reason {
                    WEnum::Value(r) => r,
                    WEnum::Unknown(_) => FailureReason::Unknown,
                });
            }
            _ => {}
        }
    }
}

// These objects send no events we need.
delegate_noop!(State: ignore ExtImageCopyCaptureManagerV1);
delegate_noop!(State: ignore ExtOutputImageCaptureSourceManagerV1);
delegate_noop!(State: ignore ExtImageCaptureSourceV1);
delegate_noop!(State: ignore WlOutput);
delegate_noop!(State: ignore WlShm);
delegate_noop!(State: ignore WlShmPool);
delegate_noop!(State: ignore WlBuffer);
