//! The host side of a drag: a `wl_data_device` on the host connection that receives the drags
//! entering Aurora's window.
//!
//! This file speaks Wayland and nothing else. What to do with a drag (offer it to a guest, refuse
//! it, cancel it) lives in `mod.rs`.
//!
//! The connection is not a new one: it is a second *event queue* on the connection winit already
//! owns (`Backend::from_foreign_display` on the `wl_display` winit hands out through
//! raw-window-handle). That is the point. A drag is delivered to the client that owns the surface
//! under the pointer, so only winit's own connection ever sees one; the clipboard's trick of a
//! separate connection cannot work here, a separate connection has no surface to be dragged over.
//! The compositor sends `enter`/`motion`/`leave`/`drop` to every `wl_data_device` of that client on
//! the seat, so ours hears exactly what winit's own device hears.

use std::sync::Mutex;

use wayland_client::{
    Connection, Dispatch, EventQueue, QueueHandle, WEnum, delegate_noop, event_created_child,
    globals::{GlobalListContents, registry_queue_init},
    protocol::{
        wl_compositor::WlCompositor,
        wl_data_device::{self, WlDataDevice},
        wl_data_device_manager::{DndAction as WlDndAction, WlDataDeviceManager},
        wl_data_offer::{self, WlDataOffer},
        wl_pointer::{self, WlPointer},
        wl_registry::WlRegistry,
        wl_seat::{self, WlSeat},
        wl_shm::WlShm,
        wl_shm_pool::WlShmPool,
        wl_surface::WlSurface,
        wl_buffer::WlBuffer,
    },
};

/// `BTN_LEFT`, as `wl_pointer` spells it.
pub const BTN_LEFT: u32 = 0x110;

/// What the offer said about itself, filled in by `wl_data_offer` events before `enter`.
#[derive(Default)]
pub struct OfferInfo {
    pub mimes: Vec<String>,
    /// `source_actions`; `None` when the host speaks a version without it (copy only).
    pub actions: Option<WlDndAction>,
}

#[derive(Default)]
pub struct OfferData(Mutex<OfferInfo>);

impl OfferData {
    pub fn snapshot(&self) -> (Vec<String>, Option<WlDndAction>) {
        let i = self.0.lock().unwrap();
        (i.mimes.clone(), i.actions)
    }
}

/// Something the host did, queued by the dispatch callbacks and applied by `mod.rs`.
pub enum DragEvent {
    Enter { offer: WlDataOffer, serial: u32, pos: (f64, f64) },
    Motion { pos: (f64, f64) },
    Leave,
    Drop,
}

/// What the host's pointer is doing to Aurora's own window, heard on our own `wl_pointer`.
///
/// This is how a guest-to-host drag gets its two missing facts without winit: the serial of the
/// press that is still held (`wl_data_device.start_drag` is only granted for an implicit grab) and
/// where the pointer is in the window, so that leaving the window can be the hand-off.
#[derive(Default, Clone, Copy)]
pub struct Ptr {
    /// Surface-local position of the last `enter` or `motion`: Aurora's own output coordinates.
    pub pos: (f64, f64),
    /// The serial of the left press, while it is held.
    pub press: Option<u32>,
}

#[derive(Default)]
pub struct DragClient {
    pub events: Vec<DragEvent>,
    pub ptr: Ptr,
    /// Set when the host finished or cancelled a drag we handed to it (see `out.rs`).
    pub out_over: bool,
    pointer: Option<WlPointer>,
}

/// Our `wl_data_device` and the queue it is dispatched on.
pub struct Feeder {
    pub conn: Connection,
    pub queue: EventQueue<DragClient>,
    pub client: DragClient,
    pub device: WlDataDevice,
    /// For a drag going the other way: the source and the drag icon are ours to create.
    pub manager: WlDataDeviceManager,
    pub compositor: WlCompositor,
    pub shm: WlShm,
}

impl Feeder {
    /// Bind a data device on `conn`. `conn` may be a foreign wrapper of winit's connection.
    pub fn attach(conn: Connection) -> Result<Feeder, String> {
        let (globals, mut queue) = registry_queue_init::<DragClient>(&conn).map_err(|e| format!("host registry: {e}"))?;
        let qh = queue.handle();
        let manager: WlDataDeviceManager = globals.bind(&qh, 1..=3, ()).map_err(|_| "the host has no wl_data_device_manager".to_string())?;
        // Up to 5 so the seat reports its capabilities: a `wl_pointer` may only be created once the
        // host says it has one.
        let seat: WlSeat = globals.bind(&qh, 1..=5, ()).map_err(|_| "the host has no wl_seat".to_string())?;
        let compositor: WlCompositor = globals.bind(&qh, 1..=4, ()).map_err(|_| "the host has no wl_compositor".to_string())?;
        let shm: WlShm = globals.bind(&qh, 1..=1, ()).map_err(|_| "the host has no wl_shm".to_string())?;
        let device = manager.get_data_device(&seat, &qh, ());
        let mut client = DragClient::default();
        // Two roundtrips: the first brings the seat's capabilities, which is when the pointer is
        // created; the second brings whatever the pointer has to say about the window already.
        queue.roundtrip(&mut client).map_err(|e| format!("host roundtrip: {e}"))?;
        queue.roundtrip(&mut client).map_err(|e| format!("host roundtrip: {e}"))?;
        Ok(Feeder { conn, queue, client, device, manager, compositor, shm })
    }

    /// Read what the host sent and dispatch it. Never blocks.
    pub fn dispatch(&mut self) -> Result<(), String> {
        self.queue.dispatch_pending(&mut self.client).map_err(|e| e.to_string())?;
        match self.conn.flush() {
            Ok(()) => {}
            Err(wayland_client::backend::WaylandError::Io(e)) if e.kind() == std::io::ErrorKind::WouldBlock => {}
            Err(e) => return Err(e.to_string()),
        }
        if let Some(guard) = self.queue.prepare_read() {
            match guard.read() {
                Ok(_) => {}
                Err(wayland_client::backend::WaylandError::Io(e)) if e.kind() == std::io::ErrorKind::WouldBlock => {}
                Err(e) => return Err(e.to_string()),
            }
        }
        self.queue.dispatch_pending(&mut self.client).map_err(|e| e.to_string())?;
        Ok(())
    }

    /// Dispatch what is already queued, without touching the socket. For the winit backend's frame
    /// tick: winit's own reads may have moved our events into our queue without waking our fd.
    pub fn dispatch_queued(&mut self) -> Result<(), String> {
        self.queue.dispatch_pending(&mut self.client).map(|_| ()).map_err(|e| e.to_string())
    }
}

impl Dispatch<WlDataDevice, ()> for DragClient {
    fn event(s: &mut Self, _: &WlDataDevice, e: wl_data_device::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        use wl_data_device::Event::*;
        match e {
            Enter { serial, x, y, id: Some(offer), .. } => s.events.push(DragEvent::Enter { offer, serial, pos: (x, y) }),
            Motion { x, y, .. } => s.events.push(DragEvent::Motion { pos: (x, y) }),
            Leave => s.events.push(DragEvent::Leave),
            Drop => s.events.push(DragEvent::Drop),
            // The clipboard is the other bridge's job; free the offer it carries.
            Selection { id: Some(offer) } => offer.destroy(),
            _ => {}
        }
    }

    event_created_child!(DragClient, WlDataDevice, [
        wl_data_device::EVT_DATA_OFFER_OPCODE => (WlDataOffer, OfferData::default()),
    ]);
}

impl Dispatch<WlDataOffer, OfferData> for DragClient {
    fn event(_: &mut Self, _: &WlDataOffer, e: wl_data_offer::Event, d: &OfferData, _: &Connection, _: &QueueHandle<Self>) {
        let mut i = d.0.lock().unwrap();
        match e {
            wl_data_offer::Event::Offer { mime_type } => i.mimes.push(mime_type),
            wl_data_offer::Event::SourceActions { source_actions: WEnum::Value(a) } => i.actions = Some(a),
            _ => {}
        }
    }
}

impl Dispatch<WlSeat, ()> for DragClient {
    fn event(s: &mut Self, seat: &WlSeat, e: wl_seat::Event, _: &(), _: &Connection, qh: &QueueHandle<Self>) {
        if let wl_seat::Event::Capabilities { capabilities: WEnum::Value(c) } = e {
            if c.contains(wl_seat::Capability::Pointer) && s.pointer.is_none() {
                s.pointer = Some(seat.get_pointer(qh, ()));
            }
        }
    }
}

/// Our own view of the host pointer. We never act on it as input (winit's pointer is the input
/// path); we only remember where it is and whether the left button is held, which is what starting
/// a drag on the host needs.
impl Dispatch<WlPointer, ()> for DragClient {
    fn event(s: &mut Self, _: &WlPointer, e: wl_pointer::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        match e {
            wl_pointer::Event::Enter { surface_x, surface_y, .. } => s.ptr.pos = (surface_x, surface_y),
            wl_pointer::Event::Motion { surface_x, surface_y, .. } => s.ptr.pos = (surface_x, surface_y),
            wl_pointer::Event::Button { serial, button: BTN_LEFT, state: WEnum::Value(st), .. } => {
                s.ptr.press = (st == wl_pointer::ButtonState::Pressed).then_some(serial);
            }
            _ => {}
        }
    }
}

impl Dispatch<WlRegistry, GlobalListContents> for DragClient {
    fn event(_: &mut Self, _: &WlRegistry, _: wayland_client::protocol::wl_registry::Event, _: &GlobalListContents, _: &Connection, _: &QueueHandle<Self>) {}
}
delegate_noop!(DragClient: WlDataDeviceManager);
delegate_noop!(DragClient: WlCompositor);
delegate_noop!(DragClient: ignore WlShm);
delegate_noop!(DragClient: WlShmPool);
delegate_noop!(DragClient: ignore WlBuffer);
delegate_noop!(DragClient: ignore WlSurface);
