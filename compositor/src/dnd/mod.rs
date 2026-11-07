//! Drag and drop between the host and Aurora's clients.
//!
//! # What exists
//!
//! **Host to guest**, with MIME types carried through and the data streamed lazily: dragging a
//! file, text or image out of a host application and over an Aurora window is a normal
//! `wl_data_device` drag for whichever Aurora client is under the pointer. Guest to guest already
//! works through Smithay's `wl_data_device`. **Guest to host is not implemented**; see "What does
//! not exist" below for the evidence and the plan.
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
//! `text/uri-list` names host paths; a confined app cannot open them. `policy.rs` therefore
//! withholds path-bearing types from clients that are not provably outside a domain (the same
//! fail-closed test as the clipboard) and logs it. The real answer, a *grant* made by the drop
//! gesture, is specified in `contracts/decisions/0020-drag-and-drop.md` and is not built.
//!
//! # What does not exist (guest to host)
//!
//! Not impossible, not done. A guest-started drag could be mirrored to the host with
//! `wl_data_device.start_drag` on our queue, which needs the origin `wl_surface` (winit's, from
//! raw-window-handle) and the serial of the pointer press (our own `wl_pointer` on the same queue
//! hears the press with its serial). The unsolved part is *when*: the host takes the pointer away
//! from Aurora the instant `start_drag` is called, so Aurora's own guest-to-guest drags would
//! have to round-trip through the host, or the hand-off would have to happen at the window edge
//! while the press is still held. Either needs a source wrapper that can be detached from a
//! Smithay grab without cancelling. That is a design of its own and cannot be verified against a
//! real host from here, so it is left out rather than shipped half-working.

pub mod host;
pub mod policy;
pub mod standin;

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

/// Where to listen for drags.
pub enum Choice {
    /// `--no-host-dnd`.
    Disabled,
    /// No host exists (headless, DRM), or it is not Wayland.
    NoHost(&'static str),
    /// winit's Wayland connection.
    Winit(ForeignDisplay),
    /// `--host-dnd-display NAME`: open a stand-in window on that socket (tests point this at a second Aurora).
    StandIn(OsString),
}

pub struct HostDnd {
    feeder: Feeder,
    standin: Option<standin::StandIn>,
    /// The drag in progress, if any.
    session: Option<Arc<Inner>>,
}

/// Open the bridge if there is a host to bridge to. Logs the outcome once; never fails.
pub fn start(state: &mut Aurora, handle: &LoopHandle<'static, Aurora>, choice: Choice) {
    const OFF: &str = "aurora-compositor: host drag and drop is off";
    let (conn, standin) = match choice {
        Choice::Disabled => return eprintln!("{OFF} (--no-host-dnd); guest-to-guest drag and drop still works"),
        Choice::NoHost(why) => return eprintln!("{OFF} ({why}); guest-to-guest drag and drop still works"),
        Choice::Winit(d) => {
            // SAFETY: the pointer is winit's live wl_display, which outlives the event loop. A
            // foreign backend never disconnects it.
            let backend = unsafe { Backend::from_foreign_display(d.0.cast()) };
            (Connection::from_backend(backend), None)
        }
        Choice::StandIn(name) => match open_standin(&name) {
            Ok(x) => x,
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
    state.host_dnd = Some(HostDnd { feeder, standin, session: None });
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
    eprintln!("aurora-compositor: host drag and drop bridging is on (wl_data_device on the host connection; host to guest only)");
}

fn open_standin(name: &std::ffi::OsStr) -> Result<(Connection, Option<standin::StandIn>), String> {
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
    // The same display, seen through a foreign backend: what production does with winit's.
    let ptr = first.backend().display_ptr();
    // SAFETY: `first` (kept alive inside the stand-in) owns the display.
    let backend = unsafe { Backend::from_foreign_display(ptr) };
    Ok((Connection::from_backend(backend), Some(standin)))
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
    inner.retarget(confined);
    let ptr = state.seat.get_pointer().unwrap();
    ptr.motion(state, under, &MotionEvent { location: pos, serial: SERIAL_COUNTER.next_serial(), time: InputTime::now() });
    ptr.frame(state);
}

/// Is this client not provably outside a domain? The clipboard's fail-closed answer.
fn is_confined(state: &Aurora, s: &WlSurface) -> bool {
    state
        .display_handle
        .get_client(s.id())
        .ok()
        .and_then(|c| c.get_data::<crate::state::ClientState>().map(|d| !d.may_read_clipboard))
        .unwrap_or(true)
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

    /// Choose what the target under the pointer may be shown.
    fn retarget(&self, confined: bool) {
        let allowed = if confined { policy::for_confined(&self.offered) } else { self.offered.clone() };
        if allowed.len() < self.offered.len() && !self.noted.swap(true, Ordering::SeqCst) {
            eprintln!(
                "aurora-compositor: dnd: withholding [{}] from a confined client: a host path means nothing inside a domain (decision 0020: dropping a file onto a confined app is a grant, not built yet){}",
                self.offered.iter().filter(|m| !allowed.contains(m)).cloned().collect::<Vec<_>>().join(", "),
                if allowed.is_empty() { "; nothing is left to offer, so the app sees no drag" } else { "" },
            );
        }
        *self.allowed.lock().unwrap() = allowed;
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

fn to_wl(a: DndAction) -> WlDndAction {
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
