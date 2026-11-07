//! Drag and drop between the host and Aurora's clients.
//!
//! # What exists
//!
//! **Both ways.** Host to guest, with MIME types carried through and the data streamed lazily:
//! dragging a file, text or image out of a host application and over an Aurora window is a normal
//! `wl_data_device` drag for whichever Aurora client is under the pointer. Guest to host, by
//! handing the drag to the host when the pointer leaves Aurora's window with the button still
//! down: see `out.rs`, which is where that half lives. Guest to guest already works through
//! Smithay's `wl_data_device` and is deliberately untouched -- a drag that stays inside Aurora
//! never goes near the host.
//!
//! # What winit exposes (investigated, not assumed)
//!
//! Aurora is a winit client of the host, so the host sees ONE surface and delivers every drag to
//! it. winit 0.31 (the version Smithay's backend links) *does* implement Wayland drag and drop:
//! `WindowEvent::DragEntered / DragPosition / DragDropped / DragLeft`, plus
//! `ActiveEventLoop::{fetch_data_transfer, set_valid_dnd_actions, start_drag}`. None of it
//! reaches Aurora: Smithay's `WinitEventLoopApp::window_event` owns the `ApplicationHandler`,
//! forwards only resize, focus, keyboard and pointer, and drops everything else in a `_ => ()`
//! arm, and the `ActiveEventLoop` the DnD calls need is only handed to that private handler. So
//! through Smithay's winit backend, as pinned here, drag and drop is invisible.
//!
//! # The design: a second event queue on winit's own connection
//!
//! The clipboard bridge uses a second *connection*. A drag cannot: it is delivered to the client
//! that owns the surface under the pointer, and a second connection owns no surface. But winit
//! publishes its `wl_display` through raw-window-handle, and wayland-client can wrap a foreign
//! display with `Backend::from_foreign_display`, which gives an independent event queue on the
//! same connection. We bind our own `wl_data_device` there (`host.rs`). The host sends
//! `enter`/`motion`/`leave`/`drop` to every data device of the client on the seat, so ours hears
//! exactly what winit's own device hears, with real serials and the real surface, and no new
//! window, seat or focus is invented.
//!
//! The one cost is that winit's device hears the same drag and, with no way to reach winit's
//! `set_valid_dnd_actions`, rejects it on enter (it calls `accept(None)` and `set_actions(none)`).
//! Ours then accepts. Both talk to the host's source through different offers and the host
//! resolves "last request wins". We therefore send our accept/action on `enter` AND again on every
//! `motion`, so ours is the later one for the whole drag. This is the part that depends on the
//! host compositor and is only verified against Aurora-as-host (see README, "Manual round trip").
//!
//! # Guest side
//!
//! A host drag becomes a Smithay `DnDGrab` whose `Source` is `HostSource`, a proxy for the host's
//! `wl_data_offer`: Smithay's `Source` trait was built for exactly this (a nested compositor
//! forwarding a drag). The grab is started by a synthetic left press (there is no real one: the
//! host holds it) and ended by a synthetic release when the host says `drop`, so Smithay's own
//! enter/motion/leave/drop/finish machinery, accept and action negotiation all apply unchanged.
//!
//! * The offer's MIME types are carried through verbatim; the source actions too.
//! * **Lazy and zero-copy**, like the clipboard: when the guest asks for data, the pipe it gave is
//!   passed straight to `wl_data_offer.receive` on the host. Aurora never reads or buffers bytes.
//! * **Leave without drop** cancels cleanly: the grab is unset (guest gets `leave`), the host
//!   offer is destroyed, and the synthetic press is released so no button is left stuck.
//! * A drag that arrives while another pointer grab is active (a window move, a guest drag) is
//!   declined, not queued.
//!
//! # Files and domains
//!
//! `text/uri-list` names host paths; a confined app cannot open them. A client in a domain is
//! therefore never shown a host path. What it gets instead depends on whether a drop can be a grant:
//!
//! * **A client the kernel placed in a domain (`ClientState::domain`) and a controller to ask
//!   (`--drop-socket`, default `$XDG_RUNTIME_DIR/aurora-drop.sock`, present):** the drop is a grant
//!   (decision 0020). The target is offered `text/uri-list` (and no other path-bearing type). At the
//!   host's `drop`, Aurora, not the target, reads the host's list ONCE into a bounded buffer
//!   (`uri.rs`), resolves symlinks, refuses anything that is not a regular file or a directory the
//!   user can read, and asks the controller (`grant.rs`) to mount exactly those into that domain
//!   read-only. The target's request for the list is answered with a list naming the in-domain paths;
//!   the host's bytes never reach it. A refusal (no controller, denied approval, bad list) answers the
//!   target with an empty list: end of file, no path.
//! * **Any other confined client** (no domain known, no controller socket): `policy.rs` withholds
//!   path-bearing types and logs it, as before.
//!
//! The data request is checked again at `send`: a confined client that asks for a file type it was
//! not offered gets end of file, and one that asks for the list before the drop gets end of file too.
//!
//! # Guest to host
//!
//! In `out.rs`, in full. The short version: our own `wl_pointer` on this connection gives the two
//! facts winit does not (the serial of the press that is still held, and where the pointer is in
//! the window), the window's own surface is wrapped as a proxy on our queue to be the drag's
//! origin, and the guest's source is wrapped so that unsetting Smithay's grab at the hand-off does
//! not tell the app its drag was cancelled. It needs `state.host_surface`, set from the window
//! handle in `winit.rs`; without it the startup line says `host to guest only`.

pub mod grant;
pub mod host;
pub mod out;
pub mod policy;
pub mod standin;
pub mod uri;

use std::ffi::OsString;
use std::os::fd::OwnedFd;
use std::sync::{Arc, Mutex, atomic::{AtomicBool, Ordering}};

use smallvec::SmallVec;
use smithay::backend::input::{ButtonState, InputTime};
use smithay::input::dnd::{DnDGrab, DndAction, Source, SourceMetadata};
use smithay::input::pointer::{ButtonEvent, Focus, GrabStartData, MotionEvent};
use smithay::reexports::calloop::{Interest, LoopHandle, Mode, PostAction, generic::Generic};
use smithay::reexports::wayland_server::Resource;
use smithay::reexports::wayland_server::protocol::wl_surface::WlSurface;
use smithay::utils::{IsAlive, SERIAL_COUNTER};
use wayland_client::{Connection, Proxy, backend::Backend, protocol::{wl_data_device_manager::DndAction as WlDndAction, wl_data_offer::WlDataOffer}};

use crate::Aurora;
use host::{DragEvent, Feeder, OfferData};

const BTN_LEFT: u32 = 0x110;

/// A `wl_display*` that winit owns, as raw-window-handle gives it out.
#[derive(Clone, Copy)]
pub struct ForeignDisplay(pub *mut std::ffi::c_void);

/// A `wl_surface*` that winit owns: the window a guest drag is handed to the host FROM. The host
/// only grants `start_drag` to the surface holding the implicit grab, which is this one.
#[derive(Clone, Copy)]
pub struct ForeignSurface(pub *mut std::ffi::c_void);

/// Where to listen for drags.
pub enum Choice {
    /// `--no-host-dnd`.
    Disabled,
    /// No host exists (headless, DRM), or it is not Wayland.
    NoHost(&'static str),
    /// winit's Wayland connection and its window's surface (absent on an X11 host, and then a
    /// guest drag cannot be handed out). The window's size is the output's, read per event: a
    /// window the user resizes would make a cached one wrong, and the bounds are what decides
    /// whether a drag is leaving.
    Winit { display: ForeignDisplay, surface: Option<ForeignSurface> },
    /// `--host-dnd-display NAME`: open a stand-in window on that socket (tests point this at a second Aurora).
    StandIn(OsString),
}

/// How big Aurora's window on the host is.
pub enum Window {
    /// winit's window IS the output, so the output's logical size is the window's, whenever asked.
    Output,
    /// The test stand-in's window, which is its own fixed size and not the output's.
    Fixed(f64, f64),
}

impl Window {
    /// The window's size in the host's logical coordinates, now.
    pub fn size(&self, state: &Aurora) -> (f64, f64) {
        match *self {
            Window::Output => {
                let (p, s) = (state.output_physical(), state.output_scale());
                (p.w as f64 / s, p.h as f64 / s)
            }
            Window::Fixed(w, h) => (w, h),
        }
    }
}

pub struct HostDnd {
    feeder: Feeder,
    standin: Option<standin::StandIn>,
    /// The drag in progress, if any.
    session: Option<Arc<Inner>>,
    /// Where Aurora's window on the host ends, in the host's logical coordinates: leaving it with a
    /// guest drag in flight is the hand-off to the host (`out.rs`).
    window: Window,
    /// The window's surface as a proxy on our queue: a guest drag's origin.
    origin: Option<wayland_client::protocol::wl_surface::WlSurface>,
    /// A drag of ours the host now owns.
    out: Option<out::Out>,
}

/// Open the bridge if there is a host to bridge to. Logs the outcome once; never fails.
pub fn start(state: &mut Aurora, handle: &LoopHandle<'static, Aurora>, choice: Choice) {
    const OFF: &str = "aurora-compositor: host drag and drop is off";
    let (conn, standin, foreign_surface, window) = match choice {
        Choice::Disabled => return eprintln!("{OFF} (--no-host-dnd); guest-to-guest drag and drop still works"),
        Choice::NoHost(why) => return eprintln!("{OFF} ({why}); guest-to-guest drag and drop still works"),
        Choice::Winit { display, surface } => {
            // SAFETY: the pointer is winit's live wl_display, which outlives the event loop. A
            // foreign backend never disconnects it.
            let backend = unsafe { Backend::from_foreign_display(display.0.cast()) };
            (Connection::from_backend(backend), None, surface, Window::Output)
        }
        Choice::StandIn(name) => match open_standin(&name) {
            Ok((conn, standin, surface)) => (conn, standin, Some(surface), Window::Fixed(standin::SIZE.0 as f64, standin::SIZE.1 as f64)),
            Err(e) => return eprintln!("{OFF} ({e}); guest-to-guest drag and drop still works"),
        },
    };
    let fd = match conn.backend().poll_fd().try_clone_to_owned() {
        Ok(fd) => fd,
        Err(e) => return eprintln!("{OFF} (cannot watch the connection: {e}); guest-to-guest drag and drop still works"),
    };
    let feeder = match Feeder::attach(conn) {
        Ok(f) => f,
        Err(e) => return eprintln!("{OFF} ({e}); guest-to-guest drag and drop still works"),
    };
    let origin = foreign_surface.and_then(|s| out::adopt_surface(&feeder.conn, s));
    let out_direction = if origin.is_some() {
        "both ways"
    } else {
        "host to guest only (the host window's surface is not known, so a guest drag cannot be handed out)"
    };
    state.host_dnd = Some(HostDnd { feeder, standin, session: None, window, origin, out: None });
    let inserted = handle.insert_source(Generic::new(fd, Interest::READ, Mode::Level), |_, _, state| {
        let Some(hd) = state.host_dnd.as_mut() else { return Ok(PostAction::Remove) };
        if let Some(s) = hd.standin.as_mut() {
            s.dispatch();
        }
        match hd.feeder.dispatch() {
            Ok(()) => {
                pump(state);
                Ok(PostAction::Continue)
            }
            Err(e) => {
                eprintln!("aurora-compositor: host drag and drop connection lost ({e}); bridging stopped");
                state.host_dnd = None;
                Ok(PostAction::Remove)
            }
        }
    });
    if inserted.is_err() {
        state.host_dnd = None;
        return eprintln!("{OFF} (cannot watch the connection)");
    }
    eprintln!("aurora-compositor: host drag and drop bridging is on (wl_data_device on the host connection; {out_direction})");
}

fn open_standin(name: &std::ffi::OsStr) -> Result<(Connection, Option<standin::StandIn>, ForeignSurface), String> {
    let path = std::path::Path::new(name);
    let path = if path.is_absolute() {
        path.to_path_buf()
    } else {
        let dir = std::env::var_os("XDG_RUNTIME_DIR").ok_or("XDG_RUNTIME_DIR is not set")?;
        std::path::Path::new(&dir).join(path)
    };
    let stream = std::os::unix::net::UnixStream::connect(&path).map_err(|e| format!("cannot connect to {}: {e}", path.display()))?;
    let first = Connection::from_socket(stream).map_err(|e| e.to_string())?;
    let standin = standin::open(first.clone())?;
    // The same display, seen through a foreign backend: what production does with winit's. The
    // stand-in's surface stands in for winit's window, pointer and all, so the guest-to-host
    // hand-off runs the production code path (`out::adopt_surface`).
    let surface = ForeignSurface(standin.surface_ptr());
    let ptr = first.backend().display_ptr();
    // SAFETY: `first` (kept alive inside the stand-in) owns the display.
    let backend = unsafe { Backend::from_foreign_display(ptr) };
    Ok((Connection::from_backend(backend), Some(standin), surface))
}

/// Frame tick from the winit backend: winit's own reads can move our events into our queue
/// without waking our fd, so look at the queue once per frame.
pub fn tick(state: &mut Aurora) {
    let Some(hd) = state.host_dnd.as_mut() else { return };
    if hd.feeder.dispatch_queued().is_ok() {
        pump(state);
    }
}

fn pump(state: &mut Aurora) {
    let Some(mut hd) = state.host_dnd.take() else { return };
    let events = std::mem::take(&mut hd.feeder.client.events);
    for ev in events {
        match ev {
            DragEvent::Enter { offer, serial, pos } => enter(state, &mut hd, offer, serial, pos),
            DragEvent::Motion { pos } => {
                if let Some(inner) = hd.session.clone().filter(|i| i.live()) {
                    inner.reassert();
                    motion(state, &inner, pos);
                }
            }
            DragEvent::Leave => leave(state, &mut hd),
            DragEvent::Drop => dropped(state, &mut hd),
        }
    }
    // The host pointer may have left the window with a guest drag in flight.
    out::check(state, &mut hd);
    let _ = hd.feeder.conn.flush();
    state.host_dnd = Some(hd);
}

fn enter(state: &mut Aurora, hd: &mut HostDnd, offer: WlDataOffer, serial: u32, pos: (f64, f64)) {
    if let Some(old) = hd.session.take() {
        old.close();
    }
    let (mimes, acts) = offer.data::<OfferData>().map(|d| d.snapshot()).unwrap_or_default();
    let ptr = state.seat.get_pointer().unwrap();
    if ptr.is_grabbed() {
        eprintln!("aurora-compositor: dnd: a host drag arrived while another pointer grab is active; declining it");
        offer.destroy();
        return;
    }
    if mimes.is_empty() {
        offer.destroy();
        return;
    }
    let version = offer.version();
    let mut actions: SmallVec<[DndAction; 3]> = SmallVec::new();
    if let Some(a) = acts.filter(|_| version >= 3) {
        for (flag, act) in [(WlDndAction::Copy, DndAction::Copy), (WlDndAction::Move, DndAction::Move), (WlDndAction::Ask, DndAction::Ask)] {
            if a.contains(flag) {
                actions.push(act);
            }
        }
    }
    if actions.is_empty() {
        actions.push(DndAction::Copy);
    }
    eprintln!("aurora-compositor: dnd: a host drag entered Aurora's window offering [{}]", mimes.join(", "));
    let inner = Arc::new(Inner {
        offer,
        conn: hd.feeder.conn.clone(),
        serial,
        version,
        offered: mimes.clone(),
        actions,
        allowed: Mutex::new(mimes),
        dropped: AtomicBool::new(false),
        closed: AtomicBool::new(false),
        noted: AtomicBool::new(false),
        last_accept: Mutex::new(None),
        grant_socket: state.drop_socket.clone().filter(|p| p.exists()),
        allow: state.trusted.allow.clone(),
        confined: AtomicBool::new(false),
        grant_domain: Mutex::new(None),
        grant_slot: Mutex::new(None),
    });
    let location = pos.into();
    let start = GrabStartData { focus: None, button: BTN_LEFT, location };
    let grab = DnDGrab::new_pointer(&state.display_handle, start, HostSource(inner.clone()), state.seat.clone());
    let s = SERIAL_COUNTER.next_serial();
    ptr.set_grab(state, grab, s, Focus::Clear);
    // The host holds the real button; the grab ends when no button is pressed, so press one.
    ptr.button(state, &ButtonEvent { serial: s, time: InputTime::now(), button: BTN_LEFT, state: ButtonState::Pressed });
    ptr.frame(state);
    hd.session = Some(inner.clone());
    motion(state, &inner, pos);
}

/// Move the pointer to `pos` (the host's surface-local logical coordinates are Aurora's output
/// coordinates: the window is the output), choosing what this target may be shown first.
fn motion(state: &mut Aurora, inner: &Arc<Inner>, pos: (f64, f64)) {
    let pos = pos.into();
    let l = state.dock_layout();
    let dp = state.to_physical_point(pos);
    let under = if l.panel.contains(dp.x, dp.y) { None } else { state.surface_under(pos) };
    let confined = under.as_ref().is_some_and(|(s, _)| is_confined(state, s));
    let domain = under.as_ref().filter(|_| confined).and_then(|(s, _)| domain_of(state, s));
    inner.retarget(confined, domain.as_deref());
    let ptr = state.seat.get_pointer().unwrap();
    ptr.motion(state, under, &MotionEvent { location: pos, serial: SERIAL_COUNTER.next_serial(), time: InputTime::now() });
    ptr.frame(state);
}

/// Is this client not provably outside a domain? The clipboard's fail-closed answer.
pub fn is_confined(state: &Aurora, s: &WlSurface) -> bool {
    state
        .display_handle
        .get_client(s.id())
        .ok()
        .and_then(|c| c.get_data::<crate::state::ClientState>().map(|d| !d.may_read_clipboard))
        .unwrap_or(true)
}

/// The domain the kernel placed this surface's client in, if any.
fn domain_of(state: &Aurora, s: &WlSurface) -> Option<String> {
    state.display_handle.get_client(s.id()).ok().and_then(|c| c.get_data::<crate::state::ClientState>().and_then(|d| d.domain.as_ref().map(|d| d.name.clone())))
}

fn leave(state: &mut Aurora, hd: &mut HostDnd) {
    // After a drop the offer is still needed (the guest reads it); a leave then is just the host
    // tidying up.
    let Some(inner) = hd.session.clone().filter(|i| i.live() && !i.dropped.load(Ordering::SeqCst)) else { return };
    eprintln!("aurora-compositor: dnd: the host drag left Aurora's window without a drop; cancelling");
    let ptr = state.seat.get_pointer().unwrap();
    let t = InputTime::now();
    ptr.unset_grab(state, SERIAL_COUNTER.next_serial(), t);
    // Let go of the synthetic press, to nobody (the grab cleared the pointer focus).
    ptr.button(state, &ButtonEvent { serial: SERIAL_COUNTER.next_serial(), time: t, button: BTN_LEFT, state: ButtonState::Released });
    ptr.frame(state);
    inner.close();
    hd.session = None;
}

fn dropped(state: &mut Aurora, hd: &mut HostDnd) {
    let Some(inner) = hd.session.clone().filter(|i| i.live()) else { return };
    eprintln!("aurora-compositor: dnd: the host drag was dropped");
    inner.dropped.store(true, Ordering::SeqCst);
    inner.begin_grant();
    let ptr = state.seat.get_pointer().unwrap();
    // The synthetic release makes Smithay's grab perform the drop on the focused client.
    ptr.button(state, &ButtonEvent { serial: SERIAL_COUNTER.next_serial(), time: InputTime::now(), button: BTN_LEFT, state: ButtonState::Released });
    ptr.frame(state);
}

/// The host's offer, seen by Smithay as a drag source.
struct Inner {
    offer: WlDataOffer,
    conn: Connection,
    serial: u32,
    version: u32,
    /// Everything the host offered.
    offered: Vec<String>,
    actions: SmallVec<[DndAction; 3]>,
    /// What the target under the pointer may be shown (`retarget`): `offered`, or less for a confined client.
    allowed: Mutex<Vec<String>>,
    dropped: AtomicBool,
    closed: AtomicBool,
    /// Whether the refusal for this drag has been logged.
    noted: AtomicBool,
    /// The last accept we sent the host, repeated on motion (see the module docs).
    last_accept: Mutex<Option<(Option<String>, Option<DndAction>)>>,
    /// The controller's drop socket, if one was there when the drag entered (`--drop-socket`).
    grant_socket: Option<std::path::PathBuf>,
    allow: crate::trusted::socket::Allowlist,
    /// Is the target under the pointer a confined client? Decides, at `send`, whether path bytes may pass through.
    confined: AtomicBool,
    /// Set while the target is in a domain a drop on it could be granted into.
    grant_domain: Mutex<Option<String>>,
    /// The grant in progress, started at the host's `drop`.
    grant_slot: Mutex<Option<Arc<Slot>>>,
}

/// The outcome of a grant, filled in by the worker thread and waited on by whoever answers the target.
#[derive(Default)]
struct Slot {
    done: Mutex<Option<Result<Vec<u8>, String>>>,
    cv: std::sync::Condvar,
}

impl Slot {
    fn set(&self, r: Result<Vec<u8>, String>) {
        *self.done.lock().unwrap() = Some(r);
        self.cv.notify_all();
    }

    fn wait(&self, within: std::time::Duration) -> Result<Vec<u8>, String> {
        let g = self.done.lock().unwrap();
        let (g, _) = self.cv.wait_timeout_while(g, within, |d| d.is_none()).unwrap();
        g.clone().unwrap_or_else(|| Err("the grant did not finish in time".into()))
    }
}

impl Inner {
    fn live(&self) -> bool {
        !self.closed.load(Ordering::SeqCst)
    }

    fn flush(&self) {
        let _ = self.conn.flush();
    }

    fn close(&self) {
        if !self.closed.swap(true, Ordering::SeqCst) {
            self.offer.destroy();
            self.flush();
        }
    }

    /// Choose what the target under the pointer may be shown. `domain` is the target's domain when it is
    /// confined and the kernel placed it in one.
    fn retarget(&self, confined: bool, domain: Option<&str>) {
        self.confined.store(confined, Ordering::SeqCst);
        let grantable = confined && self.grant_socket.is_some() && self.offered.iter().any(|m| policy::is_uri_list(m));
        let domain = domain.filter(|_| grantable);
        *self.grant_domain.lock().unwrap() = domain.map(str::to_string);
        let allowed = match (confined, domain) {
            (false, _) => self.offered.clone(),
            (true, Some(_)) => policy::for_grant(&self.offered),
            (true, None) => policy::for_confined(&self.offered),
        };
        if let Some(d) = domain {
            if !self.noted.swap(true, Ordering::SeqCst) {
                eprintln!(
                    "aurora-compositor: dnd: a file drag is over a client in domain {d}: dropping it asks the controller for a read-only grant of exactly the dropped files (decision 0020); withholding [{}]",
                    self.offered.iter().filter(|m| !allowed.contains(m)).cloned().collect::<Vec<_>>().join(", "),
                );
            }
        } else if allowed.len() < self.offered.len() && !self.noted.swap(true, Ordering::SeqCst) {
            eprintln!(
                "aurora-compositor: dnd: withholding [{}] from a confined client: a host path means nothing inside a domain{}{}",
                self.offered.iter().filter(|m| !allowed.contains(m)).cloned().collect::<Vec<_>>().join(", "),
                if confined && self.grant_socket.is_none() { " (no controller drop socket, so a drop cannot be a grant; see decision 0020)" } else { " (the client is in no known domain, so a drop cannot be a grant)" },
                if allowed.is_empty() { "; nothing is left to offer, so the app sees no drag" } else { "" },
            );
        }
        *self.allowed.lock().unwrap() = allowed;
    }

    /// At the host's `drop`, for a target a drop can be granted into: read the host's list once, off the
    /// main loop, and ask the controller. The answer is waited on by `serve_grant`.
    fn begin_grant(&self) {
        let Some(domain) = self.grant_domain.lock().unwrap().clone() else { return };
        // Only for a target that said, for the list itself, that it will take the drop. An app that
        // declined (or took some other type) is granted nothing: a grant is for a drop that landed.
        let accepted = self.last_accept.lock().unwrap().as_ref().and_then(|l| l.0.clone());
        if !accepted.as_deref().is_some_and(policy::is_uri_list) {
            return eprintln!("aurora-compositor: dnd: the target in domain {domain} did not accept the file list, so nothing is granted");
        }
        let (Some(socket), Some(mime)) = (self.grant_socket.clone(), self.offered.iter().find(|m| policy::is_uri_list(m)).cloned()) else { return };
        use std::os::fd::{AsFd, FromRawFd};
        let mut fds = [0; 2];
        // SAFETY: pipe2 fills two fds; both are owned from here on.
        let (r, w) = unsafe {
            if libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC) != 0 {
                return eprintln!("aurora-compositor: dnd: cannot make a pipe for the file list; the drop is refused");
            }
            (OwnedFd::from_raw_fd(fds[0]), OwnedFd::from_raw_fd(fds[1]))
        };
        self.offer.receive(mime, w.as_fd());
        self.flush();
        drop(w);
        let slot = Arc::new(Slot::default());
        *self.grant_slot.lock().unwrap() = Some(slot.clone());
        let allow = self.allow.clone();
        std::thread::spawn(move || slot.set(grant_flow(r, &domain, &socket, &allow)));
    }

    /// Answer the target's request for the list with the rewritten one, or with nothing.
    fn serve_grant(&self, fd: OwnedFd) {
        let Some(slot) = self.grant_slot.lock().unwrap().clone() else {
            eprintln!("aurora-compositor: dnd: a confined client asked for the file list before any drop; giving it nothing");
            return;
        };
        std::thread::spawn(move || {
            use std::io::Write;
            match slot.wait(grant::REPLY_WITHIN + std::time::Duration::from_secs(5)) {
                Ok(bytes) => {
                    let _ = std::fs::File::from(fd).write_all(&bytes);
                }
                Err(why) => eprintln!("aurora-compositor: dnd: the drop was refused, so the target gets an empty file list: {why}"),
            }
        });
    }

    /// Say again what the target last said, so that it is the later request next to winit's own
    /// device, which rejects on enter. Cheap: the host ignores a repeat.
    fn reassert(&self) {
        let last = self.last_accept.lock().unwrap().clone();
        if let Some((mime, action)) = last {
            self.send_accept(mime, action);
        }
    }

    fn send_accept(&self, mime: Option<String>, action: Option<DndAction>) {
        if !self.live() || self.dropped.load(Ordering::SeqCst) {
            return;
        }
        self.offer.accept(self.serial, mime);
        if let (Some(a), true) = (action, self.version >= 3) {
            let wl = to_wl(a);
            self.offer.set_actions(wl, wl);
        }
        self.flush();
    }
}

pub(crate) fn to_wl(a: DndAction) -> WlDndAction {
    match a {
        DndAction::Copy => WlDndAction::Copy,
        DndAction::Move => WlDndAction::Move,
        DndAction::Ask => WlDndAction::Ask,
        DndAction::None => WlDndAction::None,
    }
}

struct HostSource(Arc<Inner>);

impl IsAlive for HostSource {
    fn alive(&self) -> bool {
        self.0.live()
    }
}

impl Source for HostSource {
    fn metadata(&self) -> Option<SourceMetadata> {
        Some(SourceMetadata { mime_types: self.0.allowed.lock().unwrap().clone(), dnd_actions: self.0.actions.clone() })
    }

    fn accepted(&self, mime_type: Option<String>) {
        let mut last = self.0.last_accept.lock().unwrap();
        let action = last.as_ref().and_then(|l| l.1);
        *last = Some((mime_type.clone(), action));
        drop(last);
        self.0.send_accept(mime_type, action);
    }

    fn choose_action(&self, action: DndAction) {
        let mut last = self.0.last_accept.lock().unwrap();
        let mime = last.as_ref().and_then(|l| l.0.clone());
        *last = Some((mime.clone(), Some(action)));
        drop(last);
        self.0.send_accept(mime, Some(action));
    }

    fn send(&self, mime_type: &str, fd: OwnedFd) {
        use std::os::fd::AsFd;
        // Straight through: the host's source writes into the guest's pipe. A type the target was
        // never shown (a confined client asking for a path anyway) gets its fd closed: EOF.
        if !self.0.live() || !self.0.allowed.lock().unwrap().iter().any(|m| m == mime_type) {
            return;
        }
        // A path-bearing type for a confined client is never the host's bytes: it is the rewritten list
        // a grant produced, or nothing.
        if self.0.confined.load(Ordering::SeqCst) && policy::names_files(mime_type) {
            return self.0.serve_grant(fd);
        }
        self.0.offer.receive(mime_type.to_string(), fd.as_fd());
        self.0.flush();
    }

    fn drop_performed(&self) {}

    fn cancel(&self) {
        self.0.close();
    }

    fn finished(&self) {
        if self.0.live() && self.0.version >= 3 {
            self.0.offer.finish();
        }
        self.0.close();
    }
}

/// The worker behind a grant: the bounded read, the checks, the controller. Everything that can go wrong
/// is a string for the log; the target sees only an empty list.
fn grant_flow(list: OwnedFd, domain: &str, socket: &std::path::Path, allow: &crate::trusted::socket::Allowlist) -> Result<Vec<u8>, String> {
    let bytes = uri::read_bounded(list).map_err(|e| e.to_string())?;
    let paths = uri::parse_uri_list(&bytes).map_err(|e| e.to_string())?;
    let mut ok = Vec::new();
    for p in &paths {
        match uri::resolve(p) {
            Ok(r) => {
                if !ok.contains(&r) {
                    ok.push(r);
                }
            }
            Err(why) => eprintln!("aurora-compositor: dnd: not granting {}: {why}", p.display()),
        }
    }
    if ok.is_empty() {
        return Err("none of the dropped paths can be granted".into());
    }
    eprintln!("aurora-compositor: dnd: asking the controller to mount {} dropped path(s) read-only into domain {domain}", ok.len());
    let g = grant::ask(socket, allow, domain, &ok)?;
    eprintln!("aurora-compositor: dnd: the controller granted {} path(s) in domain {domain}", g.guest.len());
    Ok(grant::rewritten_list(&g))
}
