//! Aurora's own, second connection to the host compositor, used for nothing but the clipboard.
//!
//! winit owns the first connection and does not let anyone bind other protocols on it, so this
//! is an independent `wayland-client` connection (own socket, own event queue). It binds the
//! host's `wl_seat` and `ext_data_control_manager_v1` and makes an `ext_data_control_device_v1`.
//! That protocol needs no keyboard focus and no input serial, which is the whole point: this
//! connection never has either.
//!
//! This file speaks Wayland and nothing else. Decisions (what to bridge, loop prevention) live
//! in `slot.rs`; applying them to Aurora's seat lives in `mod.rs`.

use std::os::fd::{AsFd, OwnedFd};
use std::sync::Mutex;

use smithay::wayland::selection::SelectionTarget;
use wayland_client::{
    Connection, Dispatch, EventQueue, Proxy, QueueHandle, delegate_noop, event_created_child,
    globals::{GlobalListContents, registry_queue_init},
    protocol::{wl_registry::WlRegistry, wl_seat::WlSeat},
};
use wayland_protocols::ext::data_control::v1::client::{
    ext_data_control_device_v1::{self, ExtDataControlDeviceV1},
    ext_data_control_manager_v1::ExtDataControlManagerV1,
    ext_data_control_offer_v1::{self, ExtDataControlOfferV1},
    ext_data_control_source_v1::{self, ExtDataControlSourceV1},
};

pub fn idx(t: SelectionTarget) -> usize {
    match t {
        SelectionTarget::Clipboard => 0,
        SelectionTarget::Primary => 1,
    }
}

/// Something the host did, queued by the dispatch callbacks and applied by `mod.rs` afterwards
/// (the callbacks cannot reach Aurora's state).
#[derive(Debug)]
pub enum HostEvent {
    /// The host's selection changed; `None` is cleared. The matching offer is `HostClient::offer`.
    Selected(SelectionTarget, Option<Vec<String>>),
    /// The host cancelled our current source for this target.
    CancelledOurs(SelectionTarget),
    /// Someone on the host pasted our source: write these bytes to `fd`.
    Send(SelectionTarget, String, OwnedFd),
    /// The host's device is gone (seat removed).
    Finished,
}

struct OfferData(Mutex<Vec<String>>);
struct SourceData(SelectionTarget);

pub struct HostClient {
    pub events: Vec<HostEvent>,
    /// The offer for the host's selection in effect, per target. Kept alive until the next
    /// `selection` event replaces it: this is what makes lazy reads possible (see `mod.rs`).
    offers: [Option<ExtDataControlOfferV1>; 2],
    /// The source we last gave the host, per target.
    sources: [Option<ExtDataControlSourceV1>; 2],
}

pub struct Host {
    pub conn: Connection,
    qh: QueueHandle<HostClient>,
    pub client: HostClient,
    manager: ExtDataControlManagerV1,
    device: ExtDataControlDeviceV1,
}

/// Why there is no host bridge. Never fatal; logged once.
pub fn connect(display: Option<&std::ffi::OsStr>) -> Result<(Host, EventQueue<HostClient>), String> {
    let conn = match display {
        // Explicit socket (tests drive a second Aurora as the "host"). Resolved against
        // XDG_RUNTIME_DIR like WAYLAND_DISPLAY is.
        Some(name) => {
            let path = std::path::Path::new(name);
            let path = if path.is_absolute() {
                path.to_path_buf()
            } else {
                let dir = std::env::var_os("XDG_RUNTIME_DIR").ok_or("XDG_RUNTIME_DIR is not set")?;
                std::path::Path::new(&dir).join(path)
            };
            let stream = std::os::unix::net::UnixStream::connect(&path).map_err(|e| format!("cannot connect to {}: {e}", path.display()))?;
            Connection::from_socket(stream).map_err(|e| e.to_string())?
        }
        None => Connection::connect_to_env().map_err(|e| format!("no host Wayland connection ({e})"))?,
    };
    let (globals, mut queue) = registry_queue_init::<HostClient>(&conn).map_err(|e| format!("host registry: {e}"))?;
    let qh = queue.handle();
    let manager: ExtDataControlManagerV1 = globals
        .bind(&qh, 1..=1, ())
        .map_err(|_| "the host does not offer ext_data_control_manager_v1 (a wlr_data_control or wl_data_device fallback is not implemented: wl_data_device.set_selection needs a serial from a real input event, which this connection can never have)".to_string())?;
    // Any seat will do for a clipboard manager; the first the host lists.
    let seat: WlSeat = globals.bind(&qh, 1..=1, ()).map_err(|_| "the host has no wl_seat".to_string())?;
    let device = manager.get_data_device(&seat, &qh, ());
    let mut client = HostClient { events: vec![], offers: [None, None], sources: [None, None] };
    // Learn the current selection now rather than at the first change.
    queue.roundtrip(&mut client).map_err(|e| format!("host roundtrip: {e}"))?;
    Ok((Host { conn, qh, client, manager, device }, queue))
}

impl Host {
    /// Ask the host's current selection to be written to `fd` (lazy read: the data flows from
    /// the host client straight into the pipe, never through Aurora).
    pub fn receive(&mut self, t: SelectionTarget, mime: &str, fd: OwnedFd) -> bool {
        let Some(offer) = self.client.offers[idx(t)].as_ref() else { return false };
        offer.receive(mime.to_string(), fd.as_fd());
        let _ = self.conn.flush();
        true
    }

    /// Make `mimes` the host's selection, served by us on `send`.
    pub fn publish(&mut self, t: SelectionTarget, mimes: &[String]) {
        let source = self.manager.create_data_source(&self.qh, SourceData(t));
        for m in mimes {
            source.offer(m.clone());
        }
        match t {
            SelectionTarget::Clipboard => self.device.set_selection(Some(&source)),
            SelectionTarget::Primary => self.device.set_primary_selection(Some(&source)),
        }
        // The host cancels the replaced source; the `Cancelled` handler destroys it.
        self.client.sources[idx(t)] = Some(source);
        let _ = self.conn.flush();
    }

    /// Unset the host selection, if it is ours to unset.
    pub fn unpublish(&mut self, t: SelectionTarget) {
        if self.client.sources[idx(t)].take().is_some() {
            match t {
                SelectionTarget::Clipboard => self.device.set_selection(None),
                SelectionTarget::Primary => self.device.set_primary_selection(None),
            }
        }
        let _ = self.conn.flush();
    }
}

impl HostClient {
    fn selection(&mut self, t: SelectionTarget, id: Option<ExtDataControlOfferV1>) {
        let mimes = id.as_ref().map(|o| o.data::<OfferData>().map(|d| d.0.lock().unwrap().clone()).unwrap_or_default());
        if let Some(old) = std::mem::replace(&mut self.offers[idx(t)], id) {
            old.destroy();
        }
        self.events.push(HostEvent::Selected(t, mimes));
    }
}

impl Dispatch<ExtDataControlDeviceV1, ()> for HostClient {
    fn event(s: &mut Self, _: &ExtDataControlDeviceV1, e: ext_data_control_device_v1::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        use ext_data_control_device_v1::Event::*;
        match e {
            DataOffer { .. } => {}
            Selection { id } => s.selection(SelectionTarget::Clipboard, id),
            PrimarySelection { id } => s.selection(SelectionTarget::Primary, id),
            Finished => s.events.push(HostEvent::Finished),
            _ => {}
        }
    }

    event_created_child!(HostClient, ExtDataControlDeviceV1, [
        ext_data_control_device_v1::EVT_DATA_OFFER_OPCODE => (ExtDataControlOfferV1, OfferData(Mutex::new(vec![]))),
    ]);
}

impl Dispatch<ExtDataControlOfferV1, OfferData> for HostClient {
    fn event(_: &mut Self, _: &ExtDataControlOfferV1, e: ext_data_control_offer_v1::Event, d: &OfferData, _: &Connection, _: &QueueHandle<Self>) {
        if let ext_data_control_offer_v1::Event::Offer { mime_type } = e {
            d.0.lock().unwrap().push(mime_type);
        }
    }
}

impl Dispatch<ExtDataControlSourceV1, SourceData> for HostClient {
    fn event(s: &mut Self, src: &ExtDataControlSourceV1, e: ext_data_control_source_v1::Event, d: &SourceData, _: &Connection, _: &QueueHandle<Self>) {
        let t = d.0;
        match e {
            ext_data_control_source_v1::Event::Send { mime_type, fd } => {
                // A source that was already replaced just gets its fd closed (EOF at the reader).
                if s.sources[idx(t)].as_ref() == Some(src) {
                    s.events.push(HostEvent::Send(t, mime_type, fd));
                }
            }
            ext_data_control_source_v1::Event::Cancelled => {
                // The cancel of a source we replaced ourselves is not news.
                if s.sources[idx(t)].as_ref() == Some(src) {
                    s.sources[idx(t)] = None;
                    s.events.push(HostEvent::CancelledOurs(t));
                }
                src.destroy();
            }
            _ => {}
        }
    }
}

impl Dispatch<WlRegistry, GlobalListContents> for HostClient {
    fn event(_: &mut Self, _: &WlRegistry, _: wayland_client::protocol::wl_registry::Event, _: &GlobalListContents, _: &Connection, _: &QueueHandle<Self>) {}
}
delegate_noop!(HostClient: ignore WlSeat);
delegate_noop!(HostClient: ExtDataControlManagerV1);
