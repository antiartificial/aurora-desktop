//! Guest to host: a drag started by an Aurora client, handed to the host when the pointer leaves
//! Aurora's window.
//!
//! # Why a hand-off and not a mirror
//!
//! `wl_data_device.start_drag` takes the pointer away from the client that calls it: from the
//! host's point of view Aurora stops being hovered and the host's own drag machinery owns the
//! pointer until the drop. So Aurora cannot start a host drag when a guest drag starts, or every
//! guest-to-guest drag would round-trip through the host and break without one. It starts the host
//! drag at the moment the pointer leaves Aurora's window with the button still down, which is
//! exactly when the user has said "out there, not in here". Until then the drag is an ordinary
//! Smithay drag between guest windows.
//!
//! # The two facts the hand-off needs, and where they come from
//!
//! * **The origin surface.** The host only grants a drag to the surface that holds the implicit
//!   grab, which is winit's window. Its `wl_surface` is not ours, so we wrap the pointer
//!   raw-window-handle gives out (`ObjectId::from_ptr`) as a proxy on our own queue. In tests the
//!   stand-in window is the origin, wrapped the same way, so the test runs the production path.
//! * **The serial of the press that is still held.** Our own `wl_pointer` on the host connection
//!   hears the press with the same serial winit's pointer hears (a compositor sends one pointer
//!   event to every `wl_pointer` of the client), so no winit API is needed. See `host.rs`.
//!
//! # Taking the drag away from Smithay without cancelling it
//!
//! Smithay's `DnDGrab`, when unset without a drop, calls `Source::cancel`, and `wl_data_source.
//! cancelled` means "this drag is over, destroy the source". A handed-off drag is not over. The
//! source the grab holds is therefore [`Wrapped`], which forwards everything to the guest's real
//! source until the hand-off and swallows it after; from then on the host's `wl_data_source`
//! events are what drives the guest source: `target`, `send`, `action`, `dnd_drop_performed`,
//! `dnd_finished`, `cancelled`, each forwarded one for one. The data is never copied through
//! Aurora: the host target's pipe is handed straight to the guest's source.
//!
//! # What the user sees
//!
//! The host draws the drag: its own cursor feedback, and the icon surface we pass to `start_drag`,
//! which is a small translucent card ([`icon`]) drawn on the host connection. A host application
//! that accepts the drag answers `target`/`action`, which the guest source hears as `target` and
//! `action` events, so a guest app can show "this will be a copy" the same way it would inside
//! Aurora.
//!
//! # Files out of a domain
//!
//! A client in a domain sees in-domain paths (`/run/aurora/drops/...`, a grant's mount). Those name
//! nothing on the host, so offering `text/uri-list` out of a confined client would hand the host a
//! path it cannot open and leak the shape of the sandbox. `policy::for_host` withholds the
//! path-bearing types (and the plain-text twin) from a confined source, exactly as the incoming
//! direction withholds host paths from a confined target. An unconfined client (one the kernel
//! places outside every domain) drags its paths out unchanged.

use std::any::Any;
use std::os::fd::{AsFd, OwnedFd};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

use smithay::backend::input::{ButtonState, InputTime};
use smithay::input::dnd::{DndAction, Source, SourceMetadata};
use smithay::input::pointer::ButtonEvent;
use smithay::utils::{IsAlive, SERIAL_COUNTER};
use smithay::reexports::wayland_server::protocol::wl_data_source::WlDataSource;
use wayland_client::{
    Connection, Dispatch, Proxy, QueueHandle, WEnum,
    protocol::{
        wl_buffer::WlBuffer,
        wl_data_device_manager::DndAction as WlDndAction,
        wl_data_source::{self, WlDataSource as HostSource},
        wl_shm::{self},
        wl_shm_pool::WlShmPool,
        wl_surface::WlSurface,
    },
};

use super::host::{BTN_LEFT, DragClient};
use super::{ForeignSurface, HostDnd, policy};
use crate::Aurora;

/// A guest drag, wrapped so it can be taken away from Smithay's grab without being cancelled.
pub struct GuestDrag {
    inner: Box<dyn Source>,
    /// The guest's `wl_data_source` itself, when the drag came from a Wayland client.
    ///
    /// Smithay's `Source for WlDataSource` leaves `accepted` as the trait's no-op, so nothing ever
    /// sends `wl_data_source.target`: a dragging app is never told whether the thing under the
    /// pointer will take the drop, inside Aurora or out on the host. That is the "did it take?"
    /// feeling drag feedback exists to answer, so the event is sent from here instead.
    wl: Option<WlDataSource>,
    /// The host owns this drag now.
    handed: AtomicBool,
    /// The drag ended inside Aurora (dropped on a guest window, or cancelled) before any hand-off.
    over: AtomicBool,
    /// The client that started it is not provably outside a domain: it may not hand out paths.
    confined: bool,
    /// Whether the reason THIS drag could not be handed to the host has been logged. Per drag, so
    /// the second file drag out of a domain says so too, and a drag that keeps moving outside the
    /// window does not say it over and over.
    noted: AtomicBool,
}

impl GuestDrag {
    /// Tell the dragging app what the thing under the pointer will accept, or `None` for "nothing".
    fn target(&self, mime_type: Option<String>) {
        self.inner.accepted(mime_type.clone());
        if let Some(wl) = self.wl.as_ref().filter(|w| w.alive()) {
            wl.target(mime_type);
        }
    }

    pub fn mimes(&self) -> Vec<String> {
        self.inner.metadata().map(|m| m.mime_types).unwrap_or_default()
    }

    fn wl_actions(&self) -> WlDndAction {
        let mut a = WlDndAction::empty();
        for act in self.inner.metadata().map(|m| m.dnd_actions).unwrap_or_default() {
            a |= super::to_wl(act);
        }
        if a.is_empty() { WlDndAction::Copy } else { a }
    }

    fn handed(&self) -> bool {
        self.handed.load(Ordering::SeqCst)
    }

    /// Can this drag still be handed to the host?
    fn open(&self) -> bool {
        !self.handed() && !self.over.load(Ordering::SeqCst) && self.inner.alive()
    }
}

/// What Smithay's grab talks to while the drag is still Aurora's.
pub struct Wrapped(pub Arc<GuestDrag>);

impl IsAlive for Wrapped {
    fn alive(&self) -> bool {
        self.0.inner.alive()
    }
}

impl Source for Wrapped {
    fn is_client_local(&self, target: &dyn Any) -> bool {
        self.0.inner.is_client_local(target)
    }

    fn metadata(&self) -> Option<SourceMetadata> {
        self.0.inner.metadata()
    }

    fn accepted(&self, mime_type: Option<String>) {
        if !self.0.handed() {
            self.0.target(mime_type);
        }
    }

    fn choose_action(&self, action: DndAction) {
        if !self.0.handed() {
            self.0.inner.choose_action(action);
        }
    }

    fn send(&self, mime_type: &str, fd: OwnedFd) {
        if !self.0.handed() {
            self.0.inner.send(mime_type, fd);
        }
    }

    fn drop_performed(&self) {
        if !self.0.handed() {
            self.0.over.store(true, Ordering::SeqCst);
            self.0.inner.drop_performed();
        }
    }

    fn cancel(&self) {
        if !self.0.handed() {
            self.0.over.store(true, Ordering::SeqCst);
            self.0.inner.cancel();
        }
    }

    fn finished(&self) {
        if !self.0.handed() {
            self.0.over.store(true, Ordering::SeqCst);
            self.0.inner.finished();
        }
    }
}

/// Wrap a guest's drag source and remember it, so the pointer leaving the window can hand it over.
/// Called for every guest drag; with no host bridge it only ever forwards.
pub fn wrap<S: Source>(state: &mut Aurora, source: S, confined: bool) -> Wrapped {
    // `Source` is `'static`, so a Wayland source can be recognised as one; anything else (an
    // Xwayland or client-local drag) keeps the trait's behaviour.
    let wl = (&source as &dyn Any).downcast_ref::<WlDataSource>().cloned();
    let g = Arc::new(GuestDrag { inner: Box::new(source), wl, handed: AtomicBool::new(false), over: AtomicBool::new(false), confined, noted: AtomicBool::new(false) });
    state.guest_drag = Some(g.clone());
    Wrapped(g)
}

/// The host-side half of a handed-off drag: ours to keep alive until the host is done with it.
pub struct Out {
    source: HostSource,
    /// Kept alive for the length of the drag; destroyed with it.
    _icon: Option<Icon>,
}

/// The `wl_surface` of winit's window, for `state.host_surface`.
///
/// It lives here, next to the only thing that uses it, so that the line `winit.rs` needs is one
/// call (`state.host_surface = crate::dnd::out::surface_of(backend.window());`) and the
/// raw-window-handle matching is compiled and reviewed with the drag code it serves.
pub fn surface_of(window: &dyn smithay::reexports::winit::window::Window) -> Option<ForeignSurface> {
    use smithay::reexports::winit::raw_window_handle::{HasWindowHandle, RawWindowHandle};
    match window.window_handle().ok()?.as_raw() {
        RawWindowHandle::Wayland(w) => Some(ForeignSurface(w.surface.as_ptr())),
        // An X11 host has no wl_surface; the bridge is off there anyway.
        _ => None,
    }
}

/// Wrap a `wl_surface` another connection owns as a proxy on ours. The host sees one object either
/// way; this is how a surface winit created can be named in our own requests.
pub fn adopt_surface(conn: &Connection, s: ForeignSurface) -> Option<WlSurface> {
    let ptr = std::ptr::NonNull::new(s.0)?;
    // SAFETY: the caller's pointer is a live wl_surface proxy of this connection (winit's window,
    // or the test stand-in's), which outlives the bridge.
    let id = unsafe { wayland_client::backend::ObjectId::from_ptr(WlSurface::interface(), ptr.as_ptr().cast()) }.ok()?;
    WlSurface::from_id(conn, id).ok()
}

/// The pointer moved, or a button changed: is this the moment to give the drag to the host?
///
/// Returns true when the drag was handed over.
pub fn check(state: &mut Aurora, hd: &mut HostDnd) -> bool {
    if hd.feeder.client.out_over {
        hd.feeder.client.out_over = false;
        hd.out = None;
    }
    let Some(g) = state.guest_drag.clone() else { return false };
    if !g.open() {
        if !g.inner.alive() {
            state.guest_drag = None;
        }
        return false;
    }
    // An incoming host drag owns the pointer; the two cannot be in flight at once. A FINISHED one
    // does not: its session is left in place after a drop (the guest still reads from the offer),
    // and treating that as a live drag would mean no guest drag could ever be handed out after the
    // first drop INTO Aurora.
    if hd.session.as_ref().is_some_and(|i| i.live()) {
        return false;
    }
    hd.session = None;
    let ptr = state.seat.get_pointer().unwrap();
    if !ptr.is_grabbed() {
        return false;
    }
    let p = hd.feeder.client.ptr;
    let Some(serial) = p.press else { return false };
    let (w, h) = hd.window.size(state);
    // Still inside Aurora's window: the drag stays Aurora's.
    if p.pos.0 >= 0.0 && p.pos.1 >= 0.0 && p.pos.0 < w && p.pos.1 < h {
        return false;
    }
    hand_off(state, hd, &g, serial)
}

fn hand_off(state: &mut Aurora, hd: &mut HostDnd, g: &Arc<GuestDrag>, serial: u32) -> bool {
    let all = g.mimes();
    if all.is_empty() {
        return false;
    }
    let mimes = policy::for_host(&all, g.confined);
    if mimes.is_empty() {
        if !g.noted.swap(true, Ordering::SeqCst) {
            eprintln!(
                "aurora-compositor: dnd: a drag out of a client in a domain offers only [{}], which name paths only that domain can see; it is not handed to the host",
                all.join(", "),
            );
        }
        return false;
    }
    let Some(origin) = hd.origin.clone() else {
        if !g.noted.swap(true, Ordering::SeqCst) {
            eprintln!("aurora-compositor: dnd: a guest drag left the window, but the host window's surface is not known, so it cannot be handed to the host");
        }
        return false;
    };
    if mimes.len() < all.len() {
        eprintln!(
            "aurora-compositor: dnd: withholding [{}] from the host: a client in a domain cannot hand out a path the host could open",
            all.iter().filter(|m| !mimes.contains(m)).cloned().collect::<Vec<_>>().join(", "),
        );
    }
    let qh = hd.feeder.queue.handle();
    let source = hd.feeder.manager.create_data_source(&qh, g.clone());
    for m in &mimes {
        source.offer(m.clone());
    }
    if source.version() >= 3 {
        source.set_actions(g.wl_actions());
    }
    let icon = icon::make(&hd.feeder, &qh);
    hd.feeder.device.start_drag(Some(&source), &origin, icon.as_ref().map(|i| &i.surface), serial);
    if let Some(i) = &icon {
        i.show();
    }
    let _ = hd.feeder.conn.flush();
    g.handed.store(true, Ordering::SeqCst);
    hd.out = Some(Out { source, _icon: icon });
    eprintln!("aurora-compositor: dnd: a guest drag left Aurora's window; the host now has it, offering [{}]", mimes.join(", "));
    // Let the guest targets go (they get `leave`), and let go of the button inside Aurora: the
    // host owns the pointer from here, and will not tell us when it is released.
    let t = InputTime::now();
    ptr_of(state).unset_grab(state, SERIAL_COUNTER.next_serial(), t);
    let ev = ButtonEvent { serial: SERIAL_COUNTER.next_serial(), time: t, button: BTN_LEFT, state: ButtonState::Released };
    ptr_of(state).button(state, &ev);
    ptr_of(state).frame(state);
    true
}

fn ptr_of(state: &Aurora) -> smithay::input::pointer::PointerHandle<Aurora> {
    state.seat.get_pointer().unwrap()
}

impl Drop for Out {
    fn drop(&mut self) {
        self.source.destroy();
    }
}

/// The host's own `wl_data_source` events, forwarded one for one to the guest's source.
impl Dispatch<HostSource, Arc<GuestDrag>> for DragClient {
    fn event(s: &mut Self, _: &HostSource, e: wl_data_source::Event, g: &Arc<GuestDrag>, _: &Connection, _: &QueueHandle<Self>) {
        use wl_data_source::Event::*;
        match e {
            Target { mime_type } => g.target(mime_type),
            // Straight through: the host target's pipe goes to the guest app, which writes into it.
            Send { mime_type, fd } => g.inner.send(&mime_type, fd),
            Action { dnd_action: WEnum::Value(a) } => {
                for (flag, act) in [(WlDndAction::Copy, DndAction::Copy), (WlDndAction::Move, DndAction::Move), (WlDndAction::Ask, DndAction::Ask)] {
                    if a.contains(flag) {
                        g.inner.choose_action(act);
                    }
                }
            }
            DndDropPerformed => g.inner.drop_performed(),
            DndFinished => {
                g.inner.finished();
                s.out_over = true;
            }
            Cancelled => {
                g.inner.cancel();
                s.out_over = true;
            }
            _ => {}
        }
    }
}

/// The card the host carries under the cursor while a guest drag is out there.
///
/// Drag feedback is the host's to draw, and all it has to draw is the icon surface the drag was
/// started with. A drag with no icon is a bare cursor, which is the "did it take?" feeling this is
/// meant to avoid, so there is always one.
pub mod icon {
    use super::*;

    pub const SIZE: i32 = 72;

    pub struct Icon {
        pub surface: WlSurface,
        buffer: WlBuffer,
        pool: WlShmPool,
    }

    impl Icon {
        /// Attach and commit, which is what makes the host show it. After `start_drag`, because
        /// that is what gives the surface its drag-icon role.
        pub fn show(&self) {
            self.surface.attach(Some(&self.buffer), 0, 0);
            self.surface.damage(0, 0, SIZE, SIZE);
            self.surface.commit();
        }
    }

    impl Drop for Icon {
        fn drop(&mut self) {
            self.buffer.destroy();
            self.pool.destroy();
            self.surface.destroy();
        }
    }

    pub fn make(f: &super::super::host::Feeder, qh: &QueueHandle<DragClient>) -> Option<Icon> {
        let len = (SIZE * SIZE * 4) as usize;
        // SAFETY: memfd_create with a static name; the fd is wrapped at once.
        let fd = unsafe { libc::memfd_create(c"aurora-drag-icon".as_ptr(), libc::MFD_CLOEXEC) };
        if fd < 0 {
            return None;
        }
        // SAFETY: a fresh fd from memfd_create, owned from here.
        let file = unsafe { <std::fs::File as std::os::fd::FromRawFd>::from_raw_fd(fd) };
        file.set_len(len as u64).ok()?;
        std::io::Write::write_all(&mut &file, &pixels()).ok()?;
        let pool = f.shm.create_pool(file.as_fd(), len as i32, qh, ());
        let buffer = pool.create_buffer(0, SIZE, SIZE, SIZE * 4, wl_shm::Format::Argb8888, qh, ());
        let surface = f.compositor.create_surface(qh, ());
        Some(Icon { surface, buffer, pool })
    }

    /// A rounded translucent card with a lighter edge and a page glyph: premultiplied ARGB8888,
    /// little-endian, which is what `wl_shm` means by `Argb8888`.
    pub fn pixels() -> Vec<u8> {
        let n = SIZE as f32;
        let mut px = vec![0u8; (SIZE * SIZE * 4) as usize];
        for y in 0..SIZE {
            for x in 0..SIZE {
                let (fx, fy) = (x as f32 + 0.5, y as f32 + 0.5);
                let d = round_rect(fx, fy, n, n, 14.0);
                if d > 1.0 {
                    continue;
                }
                let inside = (1.0 - d).clamp(0.0, 1.0);
                // the card, and a brighter 2px rim
                let rim = if d > -2.0 { 1.0 } else { 0.0 };
                let page = page_glyph(fx, fy, n);
                let (mut r, mut g, mut b) = (0.12, 0.14, 0.18);
                if rim > 0.0 {
                    (r, g, b) = (0.55, 0.62, 0.72);
                }
                if page > 0.0 {
                    (r, g, b) = (0.88, 0.91, 0.96);
                }
                let a = 0.88 * inside;
                let i = ((y * SIZE + x) * 4) as usize;
                px[i] = (b * a * 255.0) as u8;
                px[i + 1] = (g * a * 255.0) as u8;
                px[i + 2] = (r * a * 255.0) as u8;
                px[i + 3] = (a * 255.0) as u8;
            }
        }
        px
    }

    /// Signed distance to a rounded rectangle centred in `w` x `h`; negative inside.
    fn round_rect(x: f32, y: f32, w: f32, h: f32, r: f32) -> f32 {
        let (hw, hh) = (w / 2.0 - 1.0, h / 2.0 - 1.0);
        let (px, py) = ((x - w / 2.0).abs() - (hw - r), (y - h / 2.0).abs() - (hh - r));
        let (qx, qy) = (px.max(0.0), py.max(0.0));
        (qx * qx + qy * qy).sqrt() + px.max(py).min(0.0) - r
    }

    /// A sheet of paper with a folded corner, drawn in the middle of the card.
    fn page_glyph(x: f32, y: f32, n: f32) -> f32 {
        let (w, h) = (n * 0.34, n * 0.44);
        let (x0, y0) = ((n - w) / 2.0, (n - h) / 2.0);
        if x < x0 || y < y0 || x > x0 + w || y > y0 + h {
            return 0.0;
        }
        // the fold: the top-right triangle is cut away
        let fold = w * 0.38;
        if x > x0 + w - fold && y < y0 + fold && (x - (x0 + w - fold)) + (y0 + fold - y) > fold {
            return 0.0;
        }
        1.0
    }

    #[cfg(test)]
    mod tests {
        use super::*;

        /// The icon is what the user sees of a drag on the host, so it has to be a visible card,
        /// not an empty or opaque square.
        #[test]
        fn the_drag_icon_is_a_translucent_card_with_a_page_on_it() {
            let px = pixels();
            let at = |x: i32, y: i32| {
                let i = ((y * SIZE + x) * 4) as usize;
                [px[i], px[i + 1], px[i + 2], px[i + 3]]
            };
            // a corner is outside the rounding: fully transparent
            assert_eq!(at(0, 0), [0, 0, 0, 0], "the card's corner is not rounded");
            // the middle is the page glyph: bright, and premultiplied (no channel above alpha)
            let m = at(SIZE / 2, SIZE / 2);
            assert!(m[3] > 200 && m[0] > 150 && m[0] <= m[3] && m[1] <= m[3] && m[2] <= m[3], "middle {m:?}");
            // the body of the card is dark and translucent
            let body = at(6, SIZE / 2);
            assert!(body[3] > 200 && body[0] < 120, "card body {body:?}");
            // and something is drawn in a good third of it
            let drawn = px.chunks(4).filter(|c| c[3] > 0).count();
            assert!(drawn > (SIZE * SIZE / 2) as usize, "only {drawn} pixels of the icon are drawn");
        }
    }
}

use icon::Icon;
