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
        wl_data_device::{self, WlDataDevice},
        wl_data_device_manager::{DndAction as WlDndAction, WlDataDeviceManager},
        wl_data_offer::{self, WlDataOffer},
        wl_registry::WlRegistry,
        wl_seat::WlSeat,
    },
};

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

#[derive(Default)]
pub struct DragClient {
    pub events: Vec<DragEvent>,
}

/// Our `wl_data_device` and the queue it is dispatched on.
pub struct Feeder {
    pub conn: Connection,
    pub queue: EventQueue<DragClient>,
    pub client: DragClient,
    _device: WlDataDevice,
}

impl Feeder {
    /// Bind a data device on `conn`. `conn` may be a foreign wrapper of winit's connection.
    pub fn attach(conn: Connection) -> Result<Feeder, String> {
        let (globals, mut queue) = registry_queue_init::<DragClient>(&conn).map_err(|e| format!("host registry: {e}"))?;
        let qh = queue.handle();
        let manager: WlDataDeviceManager = globals.bind(&qh, 1..=3, ()).map_err(|_| "the host has no wl_data_device_manager".to_string())?;
        let seat: WlSeat = globals.bind(&qh, 1..=1, ()).map_err(|_| "the host has no wl_seat".to_string())?;
        let device = manager.get_data_device(&seat, &qh, ());
        let mut client = DragClient::default();
        queue.roundtrip(&mut client).map_err(|e| format!("host roundtrip: {e}"))?;
        Ok(Feeder { conn, queue, client, _device: device })
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

impl Dispatch<WlRegistry, GlobalListContents> for DragClient {
    fn event(_: &mut Self, _: &WlRegistry, _: wayland_client::protocol::wl_registry::Event, _: &GlobalListContents, _: &Connection, _: &QueueHandle<Self>) {}
}
delegate_noop!(DragClient: ignore WlSeat);
delegate_noop!(DragClient: WlDataDeviceManager);
