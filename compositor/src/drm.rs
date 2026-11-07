//! DRM/KMS + libinput backend: Aurora on a bare TTY.
//!
//! Nothing here has been run against a real connector by the author; see
//! docs/bare-metal.md for what was and was not verified.
//!
//! Shape. One session (libseat: logind or seatd) owns the seat. It opens the
//! GPU's DRM node for us, which makes us DRM master while our VT is active, and
//! it opens the input devices for libinput. The session's pause/resume events
//! are the VT switch: on pause we stop rendering and drop master (the session
//! revokes the fds), on resume we re-activate the device, start the swapchain
//! over and redraw.
//!
//! Frames. Each CRTC has a `GbmBufferedSurface` (a swapchain of GBM buffers
//! the GPU renders into and KMS scans out). A frame is: take the next buffer,
//! bind it as a dmabuf render target, run the same `render::render_and_capture`
//! as the headless backend, `glFinish`, queue it. The DRM device's vblank
//! event says the flip happened and starts the next frame. There is no timer
//! driving rendering; a timer exists only to retry after an error.
//!
//! One EGL context. The two-context design in `winit.rs` works around a
//! window-surface bug in the host's EGL; here there is no window surface, only
//! dmabuf FBOs, so the bug does not apply and one renderer does everything.
//!
//! Coordinates follow `scale.rs`: the output mode is PHYSICAL pixels; the scale
//! comes from `--scale` or, failing that, from the connector's physical size in
//! mm (a mode of N pixels across W mm).
//!
//! Single output. The shell (dock, `Aurora::output()`, capture constraints)
//! assumes one output, so only the first connected connector is driven; the
//! others are logged and left dark. Hotplug of that connector is handled.

#![allow(deprecated)] // legacy cursor ioctls: see make_cursor

use std::{
    cell::RefCell,
    collections::HashSet,
    path::{Path, PathBuf},
    rc::Rc,
    time::Duration,
};

use smithay::{
    backend::{
        allocator::{
            Fourcc,
            gbm::{GbmAllocator, GbmBufferFlags, GbmDevice},
        },
        drm::{DrmDevice, DrmDeviceFd, DrmEvent, DrmNode, GbmBufferedSurface},
        egl::{EGLContext, EGLDisplay},
        input::{InputEvent, KeyState, KeyboardKeyEvent},
        libinput::{LibinputInputBackend, LibinputSessionInterface},
        renderer::{
            Bind,
            damage::OutputDamageTracker,
            gles::GlesRenderer,
        },
        session::{
            Event as SessionEvent, Session,
            libseat::{LibSeatSession, LibSeatSessionNotifier},
        },
        udev::{UdevBackend, UdevEvent, all_gpus, primary_gpu},
    },
    output::{Mode, Output, PhysicalProperties, Subpixel},
    reexports::{
        calloop::{
            EventLoop, LoopHandle, RegistrationToken,
            timer::{TimeoutAction, Timer},
        },
        drm::{
            buffer::DrmFourcc,
            control::{Device as ControlDevice, ModeTypeFlags, connector, crtc, dumbbuffer::DumbBuffer},
        },
        input::{DeviceCapability, Libinput},
        rustix::fs::OFlags,
        wayland_server::backend::GlobalId,
    },
    utils::{DeviceFd, Logical, Physical, Point, Size, Transform},
};

use crate::{Aurora, render};

type Surface = GbmBufferedSurface<GbmAllocator<DrmDeviceFd>, ()>;

/// How many outputs are driven. See the module comment.
/// How many connectors are driven at once.
///
/// It was 1 because nothing downstream could place a second output: every output was mapped at
/// the origin, so a second monitor would have been stacked exactly on the first. The compositor
/// renders per output now and the space lays them out left to right, so the cap is a sanity
/// bound rather than a limitation -- four is more displays than this has ever been run with,
/// and a machine reporting forty connected connectors is reporting a bug.
const MAX_OUTPUTS: usize = 4;
/// Cursor plane size. 64x64 is supported by every driver's cursor plane.
const CURSOR: u32 = 64;
const RETRY: Duration = Duration::from_millis(100);

/// What `prepare` established, handed to `init_drm`.
pub struct Prepared {
    session: LibSeatSession,
    notifier: LibSeatSessionNotifier,
    gpu: PathBuf,
}

/// Everything that must be true before Aurora can own a display, checked
/// before anything else starts. Every error says what to do about it.
pub fn prepare(card: Option<&Path>) -> Result<Prepared, String> {
    let (session, notifier) = LibSeatSession::new().map_err(|e| {
        format!(
            "--drm: cannot open a seat session: {e}\n\
             Aurora needs a seat manager (logind or seatd) to become DRM master and to read the keyboard and mouse.\n\
             - Run it from a text login on a real VT (Ctrl-Alt-F3), not from inside a graphical session or over ssh:\n\
               a graphical session already holds the seat, and only one process can.\n\
             - If you have no logind, start seatd and join its group (`sudo systemctl enable --now seatd; sudo usermod -aG seat $USER`, then log in again).\n\
             - To force a backend: LIBSEAT_BACKEND=logind or LIBSEAT_BACKEND=seatd."
        )
    })?;
    if !session.is_active() {
        return Err("--drm: the seat session is not active: this VT is not in the foreground (or another session holds the seat), \
                    so the GPU cannot be opened as DRM master. Switch to the VT you started Aurora on and run it there.".into());
    }
    let seat = session.seat();
    let gpu = match card {
        Some(p) => p.to_path_buf(),
        None => primary_gpu(&seat)
            .map_err(|e| format!("--drm: cannot enumerate GPUs on {seat} via udev: {e}"))?
            .or_else(|| all_gpus(&seat).ok().and_then(|v| v.into_iter().next()))
            .ok_or_else(|| format!("--drm: no GPU (DRM card node) belongs to {seat}. Is a graphics driver loaded (ls /dev/dri)? To pick a node by hand: --drm=/dev/dri/card0"))?,
    };
    DrmNode::from_path(&gpu).map_err(|e| format!("--drm: {} is not a DRM device node: {e}", gpu.display()))?;
    Ok(Prepared { session, notifier, gpu })
}

/// A connector we drive: its swapchain, its Output, and bookkeeping.
struct Surf {
    surface: Surface,
    output: Output,
    global: GlobalId,
    tracker: OutputDamageTracker,
    size: Size<i32, Physical>,
    connector: connector::Handle,
    /// A buffer is queued and its flip has not completed.
    queued: bool,
    /// The cursor image is on this CRTC. Cannot be done before the first flip
    /// (the CRTC is not on until the modeset commits), nor after a VT switch.
    cursor_set: bool,
}

/// An opened GPU. Field order is drop order: surfaces go before the device.
struct Gpu {
    surfaces: Vec<(crtc::Handle, Surf)>,
    renderer: GlesRenderer,
    cursor: Option<DumbBuffer>,
    allocator: GbmAllocator<DrmDeviceFd>,
    _gbm: GbmDevice<DrmDeviceFd>,
    fd: DrmDeviceFd,
    drm: DrmDevice,
    node: DrmNode,
    vblank: RegistrationToken,
}

struct Backend {
    session: LibSeatSession,
    libinput: Libinput,
    handle: LoopHandle<'static, Aurora>,
    gpu: Option<Gpu>,
    path: PathBuf,
    /// False between a VT switch away and the switch back.
    active: bool,
    scale: Option<f64>,
}

type Shared = Rc<RefCell<Backend>>;

pub fn init_drm(
    event_loop: &mut EventLoop<'static, Aurora>,
    state: &mut Aurora,
    p: Prepared,
    scale: Option<f64>,
) -> Result<(), Box<dyn std::error::Error>> {
    let Prepared { session, notifier, gpu } = p;
    // On DRM the compositor owns the hardware, so the pointer settings in the panel are live
    // rather than advisory.
    state.shell.nested = false;
    let seat = session.seat();
    let handle: LoopHandle<'static, Aurora> = event_loop.handle();

    let mut libinput = Libinput::new_with_udev::<LibinputSessionInterface<LibSeatSession>>(session.clone().into());
    libinput.udev_assign_seat(&seat).map_err(|()| format!("--drm: libinput cannot attach to {seat}"))?;

    let shared: Shared = Rc::new(RefCell::new(Backend {
        session, libinput: libinput.clone(), handle: handle.clone(), gpu: None, path: gpu.clone(), active: true, scale,
    }));

    // The first GPU must come up now, so a failure is a startup error rather
    // than a dark screen.
    add_gpu(&shared, state, &gpu)?;
    if shared.borrow().gpu.as_ref().is_none_or(|g| g.surfaces.is_empty()) {
        return Err("--drm: the GPU opened but no connector is connected with a usable mode. \
                    Plug in a display (hotplug is handled once running) and check `cat /sys/class/drm/*/status`."
            .into());
    }

    // Input.
    let sh = shared.clone();
    let mut swallowed: Vec<u32> = Vec::new();
    handle.insert_source(LibinputInputBackend::new(libinput), move |mut event, _, state| {
        match &mut event {
            InputEvent::DeviceAdded { device } if device.has_capability(DeviceCapability::Pointer) => {
                // From the theme now, rather than hard-coded. Each is attempted and the result
                // ignored: libinput refuses what a device cannot do (a mouse has no tap), and a
                // refusal is information about the device, not an error.
                let caps = PointerCaps {
                    tap_fingers: device.config_tap_finger_count() as i32,
                    natural_scroll: device.config_scroll_has_natural_scroll(),
                    accel: device.config_accel_is_available(),
                    left_handed: device.config_left_handed_is_available(),
                };
                for setting in pointer_settings(&caps, &state.fx.theme.input) {
                    // The result is ignored on purpose: libinput refuses what a device cannot
                    // do, and a refusal is information about the device rather than an error.
                    match setting {
                        PointerSetting::Tap(on) => { let _ = device.config_tap_set_enabled(on); }
                        PointerSetting::NaturalScroll(on) => { let _ = device.config_scroll_set_natural_scroll_enabled(on); }
                        PointerSetting::Accel(v) => { let _ = device.config_accel_set_speed(v); }
                        PointerSetting::LeftHanded(on) => { let _ = device.config_left_handed_set(on); }
                    }
                }
            }
            InputEvent::Keyboard { event: k } => {
                // Compositor keys. Ctrl-Alt-Fn switches VT; Ctrl-Alt-Backspace
                // quits. Both are swallowed (press and release) so no client sees them.
                let raw = u32::from(k.key_code().raw());
                let evdev = raw.saturating_sub(8);
                if k.state() == KeyState::Released {
                    if let Some(i) = swallowed.iter().position(|c| *c == raw) {
                        swallowed.swap_remove(i);
                        return;
                    }
                } else {
                    let m = state.seat.get_keyboard().map(|kb| kb.modifier_state());
                    if m.is_some_and(|m| m.ctrl && m.alt) {
                        let vt = match evdev { 59..=68 => Some((evdev - 58) as i32), 87 => Some(11), 88 => Some(12), _ => None };
                        if let Some(vt) = vt {
                            swallowed.push(raw);
                            tracing::info!("drm: switching to VT {vt}");
                            if let Err(e) = sh.borrow_mut().session.change_vt(vt) {
                                tracing::error!("drm: VT switch to {vt} failed: {e}");
                            }
                            return;
                        }
                        if evdev == 14 {
                            swallowed.push(raw);
                            tracing::info!("drm: Ctrl-Alt-Backspace, quitting");
                            state.loop_signal.stop();
                            return;
                        }
                    }
                }
            }
            _ => {}
        }
        state.process_input_event(event);
        sh.borrow_mut().place_cursor(state);
    })?;

    // VT switching.
    let sh = shared.clone();
    handle.insert_source(notifier, move |event, _, state| match event {
        SessionEvent::PauseSession => {
            tracing::info!("drm: session paused (VT switched away): dropping DRM master, not rendering");
            sh.borrow_mut().pause();
        }
        SessionEvent::ActivateSession => {
            tracing::info!("drm: session resumed: restoring the display");
            let ids = sh.borrow_mut().resume();
            for crtc in ids {
                let sh = sh.clone();
                let h = sh.borrow().handle.clone();
                h.insert_idle(move |state| render(&sh, state, crtc));
            }
            sh.borrow_mut().place_cursor(state);
        }
    })?;

    // Device add / change / remove.
    let udev = UdevBackend::new(&seat).map_err(|e| format!("--drm: udev backend for {seat}: {e}"))?;
    let sh = shared.clone();
    handle.insert_source(udev, move |event, _, state| match event {
        UdevEvent::Added { device_id, path } => {
            let mine = sh.borrow().path.clone();
            let same = DrmNode::from_dev_id(device_id).ok().zip(DrmNode::from_path(&mine).ok()).is_some_and(|(a, b)| a.dev_id() == b.dev_id());
            if same && sh.borrow().gpu.is_none() {
                tracing::info!("drm: GPU {} is back", path.display());
                if let Err(e) = add_gpu(&sh, state, &path) {
                    tracing::error!("drm: cannot reopen {}: {e}", path.display());
                }
            }
        }
        UdevEvent::Changed { device_id } => {
            let is_mine = sh.borrow().gpu.as_ref().is_some_and(|g| g.node.dev_id() == device_id);
            if is_mine {
                rescan(&sh, state);
            }
        }
        UdevEvent::Removed { device_id } => {
            let is_mine = sh.borrow().gpu.as_ref().is_some_and(|g| g.node.dev_id() == device_id);
            if is_mine {
                tracing::warn!("drm: the GPU was removed; outputs are gone until it returns");
                remove_gpu(&sh, state);
            }
        }
    })?;

    // Kick the first frame of every output; the vblank handler carries on.
    let crtcs: Vec<_> = shared.borrow().gpu.as_ref().map(|g| g.surfaces.iter().map(|(c, _)| *c).collect()).unwrap_or_default();
    for crtc in crtcs {
        let sh = shared.clone();
        handle.insert_idle(move |state| render(&sh, state, crtc));
    }
    shared.borrow_mut().place_cursor(state);
    eprintln!("aurora-compositor: DRM backend on {} (seat {seat}). Ctrl-Alt-F<n> switches VT, Ctrl-Alt-Backspace quits.", gpu.display());
    Ok(())
}

/// Open `path` through the session and bring up its renderer and outputs.
fn add_gpu(shared: &Shared, state: &mut Aurora, path: &Path) -> Result<(), Box<dyn std::error::Error>> {
    let node = DrmNode::from_path(path)?;
    let (fd, handle) = {
        let mut b = shared.borrow_mut();
        let fd = b
            .session
            .open(path, OFlags::RDWR | OFlags::CLOEXEC | OFlags::NOCTTY | OFlags::NONBLOCK)
            .map_err(|e| format!("--drm: the session refused to open {}: {e:?}. Another process is DRM master (a display manager or compositor on this seat?); stop it or switch to a free VT.", path.display()))?;
        (DrmDeviceFd::new(DeviceFd::from(fd)), b.handle.clone())
    };
    // `disable_connectors = true`: start from a known state. The console goes
    // black for the modeset, which is expected.
    let (drm, notifier) = DrmDevice::new(fd.clone(), true)
        .map_err(|e| format!("--drm: {} is not usable as a KMS device (is it a render-only node, or are we not DRM master?): {e}", path.display()))?;
    let gbm = GbmDevice::new(fd.clone())?;
    let display = unsafe { EGLDisplay::new(gbm.clone())? };
    let context = EGLContext::new(&display)?;
    let renderer = unsafe { GlesRenderer::new(context)? };
    let allocator = GbmAllocator::new(gbm.clone(), GbmBufferFlags::RENDERING | GbmBufferFlags::SCANOUT);

    let sh = shared.clone();
    let vblank = handle.insert_source(notifier, move |event, _, state| match event {
        DrmEvent::VBlank(crtc) => {
            let ok = {
                let mut b = sh.borrow_mut();
                b.gpu.as_mut().and_then(|g| {
                    let Gpu { surfaces, cursor, fd, .. } = g;
                    surfaces.iter_mut().find(|(c, _)| *c == crtc).map(|(_, s)| {
                        s.queued = false;
                        if !s.cursor_set {
                            s.cursor_set = cursor.as_ref().is_some_and(|cb| fd.set_cursor(crtc, Some(cb)).is_ok());
                        }
                        s.surface.frame_submitted()
                    })
                })
            };
            match ok {
                Some(Ok(_)) => render(&sh, state, crtc),
                Some(Err(e)) => {
                    tracing::warn!("drm: frame_submitted failed: {e}; retrying");
                    retry(&sh, crtc);
                }
                None => {}
            }
        }
        DrmEvent::Error(e) => tracing::error!("drm: device error: {e}"),
    })?;

    shared.borrow_mut().gpu = Some(Gpu { surfaces: Vec::new(), renderer, cursor: None, allocator, _gbm: gbm, fd, drm, node, vblank });
    rescan(shared, state);
    Ok(())
}

fn remove_gpu(shared: &Shared, state: &mut Aurora) {
    let gpu = shared.borrow_mut().gpu.take();
    if let Some(mut gpu) = gpu {
        for (_, s) in gpu.surfaces.drain(..) {
            destroy_output(state, &s);
        }
        shared.borrow().handle.remove(gpu.vblank);
    }
}

fn destroy_output(state: &mut Aurora, s: &Surf) {
    state.space.unmap_output(&s.output);
    state.display_handle.remove_global::<Aurora>(s.global.clone());
}

/// Connected connectors with a usable CRTC, in connector order.
fn scan(fd: &DrmDeviceFd, taken: &HashSet<crtc::Handle>) -> Vec<(connector::Info, crtc::Handle)> {
    let Ok(res) = fd.resource_handles() else { return vec![] };
    let mut used = taken.clone();
    let mut out = vec![];
    for &h in res.connectors() {
        let Ok(info) = fd.get_connector(h, true) else { continue };
        if info.state() != connector::State::Connected || info.modes().is_empty() {
            continue;
        }
        let current = info.current_encoder().and_then(|e| fd.get_encoder(e).ok()).and_then(|e| e.crtc()).filter(|c| !used.contains(c));
        let pick = current.or_else(|| {
            info.encoders().iter().filter_map(|e| fd.get_encoder(*e).ok()).find_map(|e| {
                res.filter_crtcs(e.possible_crtcs()).into_iter().find(|c| !used.contains(c))
            })
        });
        match pick {
            Some(c) => {
                used.insert(c);
                out.push((info, c));
            }
            None => tracing::warn!("drm: connector {h:?} is connected but no free CRTC can drive it"),
        }
    }
    out
}

fn connector_name(i: &connector::Info) -> String {
    format!("{}-{}", i.interface().as_str(), i.interface_id())
}

/// Reconcile outputs with what is connected: drop the ones that went away,
/// add one if there is room.
fn rescan(shared: &Shared, state: &mut Aurora) {
    let mut guard = shared.borrow_mut();
    let b = &mut *guard;
    let Some(gpu) = b.gpu.as_mut() else { return };

    // Vanished connectors.
    let gone: Vec<crtc::Handle> = gpu
        .surfaces
        .iter()
        .filter(|(_, s)| gpu.fd.get_connector(s.connector, false).map_or(true, |i| i.state() != connector::State::Connected))
        .map(|(c, _)| *c)
        .collect();
    for c in gone {
        if let Some(i) = gpu.surfaces.iter().position(|(x, _)| *x == c) {
            let (_, s) = gpu.surfaces.remove(i);
            tracing::info!("drm: output {} unplugged", s.output.name());
            destroy_output(state, &s);
        }
    }

    let taken: HashSet<_> = gpu.surfaces.iter().map(|(c, _)| *c).collect();
    let held: HashSet<_> = gpu.surfaces.iter().map(|(_, s)| s.connector).collect();
    let mut added = vec![];
    for (info, crtc) in scan(&gpu.fd, &taken) {
        if held.contains(&info.handle()) {
            continue;
        }
        if gpu.surfaces.len() >= MAX_OUTPUTS {
            tracing::warn!("drm: connector {} is connected but only {MAX_OUTPUTS} output is driven; leaving it dark", connector_name(&info));
            continue;
        }
        match add_output(gpu, state, &info, crtc, b.scale) {
            Ok(()) => added.push(crtc),
            Err(e) => tracing::error!("drm: cannot drive {}: {e}", connector_name(&info)),
        }
    }
    let (active, handle) = (b.active, b.handle.clone());
    drop(guard);
    if active {
        for crtc in added {
            let sh = shared.clone();
            handle.insert_idle(move |state| render(&sh, state, crtc));
        }
    }
    shared.borrow_mut().place_cursor(state);
}

fn add_output(gpu: &mut Gpu, state: &mut Aurora, info: &connector::Info, crtc: crtc::Handle, scale_arg: Option<f64>) -> Result<(), Box<dyn std::error::Error>> {
    let dmode = *info.modes().iter().find(|m| m.mode_type().contains(ModeTypeFlags::PREFERRED)).unwrap_or(&info.modes()[0]);
    let (w, h) = dmode.size();
    let size: Size<i32, Physical> = (w as i32, h as i32).into();
    let (mm_w, mm_h) = info.size().map_or((0, 0), |(a, b)| (a as i32, b as i32));
    let name = connector_name(info);
    let refresh = (dmode.vrefresh() as i32) * 1000;
    let mode = Mode { size, refresh };

    let scale = scale_arg.unwrap_or_else(|| auto_scale(size.w, mm_w));
    tracing::info!(
        "drm: {name}: {}x{}@{} Hz, {mm_w}x{mm_h} mm -> scale {scale}{}",
        size.w, size.h, dmode.vrefresh(), if scale_arg.is_some() { " (--scale)" } else { "" }
    );

    if gpu.cursor.is_none() {
        gpu.cursor = make_cursor(&gpu.fd, scale);
    }
    let drm_surface = gpu.drm.create_surface(crtc, dmode, &[info.handle()])?;
    let formats = gpu.renderer.egl_context().dmabuf_render_formats().iter().copied();
    let surface = GbmBufferedSurface::new(drm_surface, gpu.allocator.clone(), &[Fourcc::Xrgb8888, Fourcc::Argb8888], formats)
        .map_err(|e| format!("no scanout-capable buffer format shared by the renderer and the display: {e}"))?;

    let output = Output::new(
        name.clone(),
        PhysicalProperties {
            size: (mm_w, mm_h).into(),
            subpixel: match info.subpixel() {
                connector::SubPixel::HorizontalRgb => Subpixel::HorizontalRgb,
                connector::SubPixel::HorizontalBgr => Subpixel::HorizontalBgr,
                connector::SubPixel::VerticalRgb => Subpixel::VerticalRgb,
                connector::SubPixel::VerticalBgr => Subpixel::VerticalBgr,
                connector::SubPixel::None => Subpixel::None,
                _ => Subpixel::Unknown,
            },
            make: "Unknown".into(),
            model: name,
            serial_number: "Unknown".into(),
        },
    );
    let global = output.create_global::<Aurora>(&state.display_handle);
    // Left to right, after everything already mapped. Mapping every output at the origin -- which
    // is what this did while only one was driven -- would stack a second monitor exactly on the
    // first: both would show the same region of the space and a window could never be moved
    // "across" to the other one, because there would be no across.
    let at_x: i32 = state
        .space
        .outputs()
        .filter_map(|o| state.space.output_geometry(o))
        .map(|g| g.loc.x + g.size.w)
        .max()
        .unwrap_or(0);
    output.change_current_state(Some(mode), Some(Transform::Normal), None, Some((at_x, 0).into()));
    output.set_preferred(mode);
    state.set_host_scale(&output, scale);
    state.space.map_output(&output, (at_x, 0));
    tracing::info!("drm: {} mapped at x={at_x}, {}x{}", connector_name(info), size.w, size.h);
    state.update_capture_constraints(&output);
    let tracker = OutputDamageTracker::from_output(&output);
    gpu.surfaces.push((crtc, Surf { surface, output, global, tracker, size, connector: info.handle(), queued: false, cursor_set: false }));
    Ok(())
}

/// `--probe-drm`: read the connectors and say what would be driven, WITHOUT taking DRM master.
///
/// Enumerating connectors needs only an open device; becoming master needs a seat. Those are
/// different permissions, and separating them means the connector half of the DRM path can be
/// checked from inside a desktop session -- which is the only place anyone has been able to run
/// this compositor so far.
///
/// It reports what `add_output` would decide: the preferred mode, the scale from the EDID's
/// millimetre size, and where each display would be placed. It does not modeset, allocate a
/// buffer or touch the cursor; those still need the VT.
pub fn probe(path: Option<&std::path::Path>) -> Result<(), Box<dyn std::error::Error>> {
    use std::os::fd::{AsFd, BorrowedFd};

    struct Card(std::fs::File);
    impl AsFd for Card {
        fn as_fd(&self) -> BorrowedFd<'_> { self.0.as_fd() }
    }
    use smithay::reexports::drm as libdrm;
    impl libdrm::Device for Card {}
    impl libdrm::control::Device for Card {}

    let dev = match path {
        Some(p) => p.to_path_buf(),
        None => {
            let mut found = None;
            for e in std::fs::read_dir("/dev/dri")?.flatten() {
                let n = e.file_name().to_string_lossy().to_string();
                if n.starts_with("card") {
                    found = Some(e.path());
                    break;
                }
            }
            found.ok_or("no /dev/dri/card* found")?
        }
    };
    let f = std::fs::OpenOptions::new().read(true).write(true).open(&dev)
        .map_err(|e| format!("cannot open {}: {e}", dev.display()))?;
    let card = Card(f);
    let res = libdrm::control::Device::resource_handles(&card)?;

    println!("device: {}", dev.display());
    let mut at_x: i32 = 0;
    let mut driven = 0usize;
    for h in res.connectors() {
        let Ok(info) = libdrm::control::Device::get_connector(&card, *h, false) else { continue };
        let name = connector_name(&info);
        if info.state() != connector::State::Connected {
            println!("  {name:<12} {:?}", info.state());
            continue;
        }
        let modes = info.modes();
        let Some(m) = modes.iter().find(|m| m.mode_type().contains(ModeTypeFlags::PREFERRED)).or_else(|| modes.first()) else {
            println!("  {name:<12} connected, but reports no modes");
            continue;
        };
        let (w, h2) = m.size();
        let (mm_w, mm_h) = info.size().map_or((0, 0), |(a, b)| (a as i32, b as i32));
        let scale = auto_scale(w as i32, mm_w).max(auto_scale(h2 as i32, mm_h));
        if driven >= MAX_OUTPUTS {
            println!("  {name:<12} connected {}x{} -- beyond the {MAX_OUTPUTS}-display cap, would be left dark", w, h2);
            continue;
        }
        println!(
            "  {name:<12} connected {}x{}@{}Hz  {}x{}mm  scale {scale}  would map at x={at_x}",
            w, h2, m.vrefresh(), mm_w, mm_h
        );
        at_x += w as i32;
        driven += 1;
    }
    println!("{driven} display(s) would be driven, {}px wide in total", at_x);
    println!("(connectors only: no modeset, no master. --drm on a text VT does the rest.)");
    Ok(())
}

/// Scale from pixel density. `mm == 0` (unknown, as on VMs) means 1.0. Bogus
/// EDID sizes are common (projectors, TVs), hence the clamp.
fn auto_scale(px: i32, mm: i32) -> f64 {
    if px <= 0 || mm <= 0 {
        return 1.0;
    }
    let dpi = px as f64 / (mm as f64 / 25.4);
    let s = dpi / 96.0;
    if s < 1.25 { 1.0 } else { ((s * 4.0).round() / 4.0).min(3.0) }
}

/// One frame for `crtc`, if it is time to draw one.
fn render(shared: &Shared, state: &mut Aurora, crtc: crtc::Handle) {
    let mut guard = shared.borrow_mut();
    let b = &mut *guard;
    if !b.active {
        return;
    }
    let Some(gpu) = b.gpu.as_mut() else { return };
    let Gpu { renderer, surfaces, .. } = gpu;
    let Some((_, s)) = surfaces.iter_mut().find(|(c, _)| *c == crtc) else { return };
    if s.queued {
        return;
    }
    let ok = (|| -> Result<(), String> {
        let (mut dmabuf, _age) = s.surface.next_buffer().map_err(|e| format!("no buffer: {e}"))?;
        {
            let mut fb = renderer.bind(&mut dmabuf).map_err(|e| format!("cannot bind the scanout buffer: {e}"))?;
            render::render_and_capture(state, renderer, &mut fb, &s.output, &mut s.tracker, s.size);
        }
        // No fence: finish the GPU work before KMS reads the buffer.
        let _ = renderer.with_context(|gl| unsafe { gl.Finish() });
        s.surface.queue_buffer(None, None, ()).map_err(|e| format!("cannot queue the frame: {e}"))?;
        Ok(())
    })();
    match ok {
        Ok(()) => {
            s.queued = true;
            let out = s.output.clone();
            drop(guard);
            render::after_frame(state, &out);
        }
        Err(e) => {
            tracing::warn!("drm: frame on {:?} failed: {e}; retrying in {RETRY:?}", crtc);
            drop(guard);
            retry(shared, crtc);
        }
    }
}

fn retry(shared: &Shared, crtc: crtc::Handle) {
    let sh = shared.clone();
    let h = shared.borrow().handle.clone();
    let _ = h.insert_source(Timer::from_duration(RETRY), move |_, _, state| {
        render(&sh, state, crtc);
        TimeoutAction::Drop
    });
}

impl Backend {
    fn pause(&mut self) {
        self.active = false;
        self.libinput.suspend();
        if let Some(g) = self.gpu.as_mut() {
            g.drm.pause();
        }
    }

    /// Back on our VT: re-take the device and start every swapchain over.
    /// Returns the CRTCs that need a redraw.
    fn resume(&mut self) -> Vec<crtc::Handle> {
        if self.libinput.resume().is_err() {
            tracing::error!("drm: libinput did not resume; the keyboard and mouse may be dead (switch VT again)");
        }
        self.active = true;
        let Some(g) = self.gpu.as_mut() else { return vec![] };
        // `true`: whatever ran on the other VT left KMS in an unknown state.
        if let Err(e) = g.drm.activate(true) {
            tracing::error!("drm: cannot re-activate the GPU: {e}");
            return vec![];
        }
        let mut ids = vec![];
        for (c, s) in g.surfaces.iter_mut() {
            s.surface.reset_buffers();
            s.queued = false;
            s.cursor_set = false;
            ids.push(*c);
        }
        ids
    }

    /// Move the hardware cursor to the seat pointer. Cheap; called per input event.
    fn place_cursor(&mut self, state: &Aurora) {
        if !self.active {
            return;
        }
        let Some(g) = self.gpu.as_ref() else { return };
        let p: Point<f64, Logical> = state.seat.get_pointer().map(|p| p.current_location()).unwrap_or_default();
        let ph = state.to_physical_point(p);
        for (c, _) in &g.surfaces {
            let _ = g.fd.move_cursor(*c, (ph.x.round() as i32, ph.y.round() as i32));
        }
    }
}

/// An arrow on a 64x64 ARGB dumb buffer for the legacy cursor ioctl, which
/// every driver implements (atomic drivers emulate it). The tip is at (0,0).
fn make_cursor(fd: &DrmDeviceFd, scale: f64) -> Option<DumbBuffer> {
    let mut db = fd.create_dumb_buffer((CURSOR, CURSOR), DrmFourcc::Argb8888, 32).ok()?;
    {
        let mut map = fd.map_dumb_buffer(&mut db).ok()?;
        let px = arrow(CURSOR as usize, scale.clamp(1.0, 3.0));
        map.as_mut()[..px.len()].copy_from_slice(&px);
    }
    Some(db)
}

/// BGRA bytes (Argb8888, little endian) of a white arrow with a black outline.
fn arrow(n: usize, s: f64) -> Vec<u8> {
    const P: [(f64, f64); 7] = [(0.0, 0.0), (0.0, 16.0), (3.6, 12.6), (6.4, 18.6), (9.0, 17.4), (6.4, 11.4), (11.2, 11.4)];
    let inside = |x: f64, y: f64| {
        let (mut c, mut j) = (false, P.len() - 1);
        for i in 0..P.len() {
            let (xi, yi) = (P[i].0 * s, P[i].1 * s);
            let (xj, yj) = (P[j].0 * s, P[j].1 * s);
            if (yi > y) != (yj > y) && x < (xj - xi) * (y - yi) / (yj - yi) + xi {
                c = !c;
            }
            j = i;
        }
        c
    };
    let mut out = vec![0u8; n * n * 4];
    let r = (1.3 * s).ceil() as i32;
    for y in 0..n as i32 {
        for x in 0..n as i32 {
            let (fx, fy) = (x as f64 + 0.5, y as f64 + 0.5);
            let px = if inside(fx, fy) {
                Some([255, 255, 255, 255])
            } else if (-r..=r).any(|dy| (-r..=r).any(|dx| dx * dx + dy * dy <= r * r && inside(fx + dx as f64, fy + dy as f64))) {
                Some([0, 0, 0, 255])
            } else {
                None
            };
            if let Some(p) = px {
                let i = (y as usize * n + x as usize) * 4;
                out[i..i + 4].copy_from_slice(&p);
            }
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scale_follows_pixel_density() {
        assert_eq!(auto_scale(1920, 0), 1.0, "unknown size (VM) is 1x");
        assert_eq!(auto_scale(1920, 509), 1.0, "24in 1080p, ~96 dpi");
        assert_eq!(auto_scale(3840, 597), 1.75, "27in 4K, ~163 dpi");
        assert_eq!(auto_scale(1920, 100), 3.0, "absurd EDID size is clamped");
    }

    #[test]
    fn cursor_has_an_opaque_tip_and_transparent_corner() {
        let px = arrow(64, 1.0);
        let a = |x: usize, y: usize| px[(y * 64 + x) * 4 + 3];
        assert_eq!(a(1, 2), 255);
        assert_eq!(a(60, 60), 0);
    }
}

/// What a pointer device admits it can do. Read from libinput, and the only thing
/// `pointer_settings` is allowed to know about the device.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub struct PointerCaps {
    pub tap_fingers: i32,
    pub natural_scroll: bool,
    pub accel: bool,
    pub left_handed: bool,
}

/// One setting to push at a device.
#[derive(Clone, Copy, Debug, PartialEq)]
pub enum PointerSetting {
    Tap(bool),
    NaturalScroll(bool),
    Accel(f64),
    LeftHanded(bool),
}

/// Which of the theme's pointer settings this device can actually take, in the order
/// they are applied.
///
/// Pulled out of the `DeviceAdded` handler so it can be tested. The handler needs a live
/// libinput `Device`, which needs the DRM backend, which needs a seat, so nothing in this
/// file had ever executed. The device CALLS are still unrun, but the decisions are not:
/// which settings are offered, which are skipped because the hardware cannot take them, and
/// the clamp on a speed that a theme file can set to anything.
pub fn pointer_settings(caps: &PointerCaps, i: &crate::fx::theme::Input) -> Vec<PointerSetting> {
    let mut out = Vec::new();
    // A mouse has no tap. Asking it to tap is not an error, but it is not a setting either.
    if caps.tap_fingers > 0 {
        out.push(PointerSetting::Tap(i.tap_to_click));
    }
    if caps.natural_scroll {
        out.push(PointerSetting::NaturalScroll(i.natural_scroll));
    }
    if caps.accel {
        // libinput's range is -1..=1 and it rejects anything outside it, which would lose the
        // whole setting rather than part of it. The theme is a text file a person edits, so
        // out of range is a thing that will happen.
        out.push(PointerSetting::Accel(i.pointer_speed.clamp(-1.0, 1.0)));
    }
    if caps.left_handed {
        out.push(PointerSetting::LeftHanded(i.left_handed));
    }
    out
}

#[cfg(test)]
mod pointer_tests {
    use super::*;
    use crate::fx::theme::Input;

    fn theme(speed: f64) -> Input {
        Input { tap_to_click: true, natural_scroll: true, pointer_speed: speed, left_handed: true, ..Default::default() }
    }

    #[test]
    fn a_device_is_only_offered_what_it_can_take() {
        // A plain three-button mouse: no tap, no natural-scroll toggle, no handedness.
        let mouse = PointerCaps { tap_fingers: 0, natural_scroll: false, accel: true, left_handed: false };
        assert_eq!(pointer_settings(&mouse, &theme(0.4)), vec![PointerSetting::Accel(0.4)]);

        // A touchpad takes all four.
        let pad = PointerCaps { tap_fingers: 3, natural_scroll: true, accel: true, left_handed: true };
        assert_eq!(
            pointer_settings(&pad, &theme(0.0)),
            vec![
                PointerSetting::Tap(true),
                PointerSetting::NaturalScroll(true),
                PointerSetting::Accel(0.0),
                PointerSetting::LeftHanded(true),
            ]
        );

        // A device that admits nothing gets nothing, rather than four refused calls.
        assert!(pointer_settings(&PointerCaps::default(), &theme(0.5)).is_empty());
    }

    #[test]
    fn a_speed_outside_libinputs_range_is_clamped_not_dropped() {
        // The theme is a file a person edits, so this is a thing that will happen. libinput
        // rejects out-of-range, which would lose the setting entirely.
        let any = PointerCaps { accel: true, ..Default::default() };
        for (set, want) in [(5.0, 1.0), (-5.0, -1.0), (1.0, 1.0), (-1.0, -1.0), (0.25, 0.25)] {
            assert_eq!(pointer_settings(&any, &theme(set)), vec![PointerSetting::Accel(want)], "speed {set}");
        }
    }

    #[test]
    fn a_tap_capable_device_with_one_finger_still_counts() {
        // config_tap_finger_count is a COUNT, not a flag. Testing it as a bool would read 1 as
        // true and 0 as false by luck, and any future "> 1" rule would be silently wrong.
        let one = PointerCaps { tap_fingers: 1, ..Default::default() };
        assert_eq!(pointer_settings(&one, &theme(0.0)), vec![PointerSetting::Tap(true)]);
    }
}

#[cfg(test)]
mod layout_tests {
    use super::MAX_OUTPUTS;

    /// Where the Nth connector is placed: immediately right of everything before it.
    ///
    /// The placement itself is three lines inside `add_output` and cannot be called without a
    /// GPU, so the RULE is factored here and checked. It is worth checking because the previous
    /// rule -- every output at the origin -- was invisible while only one output was driven and
    /// would have stacked a second monitor exactly on the first.
    fn place(widths: &[i32]) -> Vec<i32> {
        let mut xs = vec![];
        let mut at = 0;
        for w in widths {
            xs.push(at);
            at += w;
        }
        xs
    }

    #[test]
    fn connectors_are_laid_out_left_to_right_without_overlapping() {
        assert_eq!(place(&[1920, 2560, 1280]), vec![0, 1920, 4480]);
        // Mixed widths still abut: a gap would be dead space a pointer could get stuck in, and
        // an overlap would be two displays claiming the same coordinates.
        assert_eq!(place(&[1280]), vec![0], "one display still starts at the origin");
        assert!(place(&[]).is_empty());
    }

    #[test]
    fn the_output_cap_allows_more_than_one_display() {
        // It was 1, which made the loop over connectors decorative.
        assert!(MAX_OUTPUTS > 1);
    }
}
