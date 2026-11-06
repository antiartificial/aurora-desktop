//! Clipboard and primary selection, shared between the host and Aurora's clients, both ways.
//!
//! # What is served to guests
//!
//! `wl_data_device` (clipboard + DnD), `zwp_primary_selection_device_manager_v1` (middle-click
//! paste) and `ext_data_control_manager_v1` (clipboard managers, `wl-copy`/`wl-paste` style
//! tools that read and write the selection without keyboard focus). All three are Smithay's;
//! `handlers/mod.rs` wires them to the seat. Guest-to-guest copy needs nothing else.
//!
//! # The host bridge
//!
//! Aurora runs nested as a winit client of the host, and winit does not expose its connection
//! for binding other protocols. So `host.rs` opens a **second, independent connection** to the
//! host and binds `ext_data_control_manager_v1` there. It is driven from calloop like any other
//! source (`start`).
//!
//! **Why `ext_data_control` and not `wl_data_device`.** `wl_data_device.set_selection` needs a
//! serial from a real input event and the data device only works for a surface with keyboard
//! focus. A side connection has neither, ever. `ext_data_control` is the standardised successor
//! of `wlr-data-control`, made for clipboard managers: it can read and set the selection with no
//! focus and no serial. Hosts that lack it (no fallback is implemented, deliberately: the
//! alternatives cannot work from a focus-less connection) simply get no bridge.
//!
//! **Host to guest.** A host `selection` event gives an offer and its MIME types. Aurora sets
//! its own seat selection (`set_data_device_selection` / `set_primary_selection`) to the same
//! types, with no data. When a guest pastes, Smithay calls `send_selection` with the pipe the
//! guest gave; we call `ext_data_control_offer_v1.receive(mime, fd)` on the host offer and hand
//! it that very fd. The bytes flow from the host's client to the guest's client through the
//! pipe; Aurora never reads, buffers or blocks on them.
//!
//! **Lazy, not eager.** Eager would copy every selection on every change: a screenshot or a
//! 200 MB image is read whether or not anyone pastes, the read blocks on the host client
//! (which can be slow or hung) and needs buffering and a size policy, and it can be stale if
//! the source changes representation. Lazy needs the host offer to still be valid at paste
//! time. It is: an offer stays usable until the host sends the next `selection` event, and
//! at that same moment we replace the guest's selection, so a guest can never be offered
//! something whose offer we have dropped. (The tiny window in between is a paste whose fd the
//! host client then closes: the reader sees EOF, an empty paste, never a hang or stale
//! data.) The cost: if the host application that owns the selection exits, the host clears the
//! selection and so do we, as on a bare host.
//!
//! **Guest to host.** When any guest client sets the selection (`SelectionHandler::new_selection`,
//! regardless of which of the three protocols it used) we create an `ext_data_control_source_v1`
//! offering the same MIME types and make it the host selection. When a host client pastes, the
//! source's `send` event carries an fd, and we ask the guest's selection source to write
//! into it (`request_data_device_client_selection`): again straight through, no buffering.
//! The copy lives as long as Aurora does.
//!
//! **Loops.** See `slot.rs`: each target (clipboard, primary) has a state machine tracking which
//! side originated the selection in effect. Host-origin selections are installed in the guest
//! as compositor-side ones, for which Smithay never calls `new_selection`, so they cannot come
//! back; the host's echo of our own source is recognised and swallowed; and a circuit breaker
//! stops two clipboard managers that re-publish each other's changes from becoming a livelock.
//!
//! # Degrading
//!
//! No host (headless, DRM), `--no-host-clipboard`, no `ext_data_control_manager_v1` on the
//! host, or a connection that later dies: one log line, and guest-to-guest keeps working.
//! Startup never fails because of it.

pub mod host;
pub mod slot;

use std::ffi::OsString;
use std::os::fd::OwnedFd;
use std::time::Instant;

use smithay::reexports::calloop::{Interest, LoopHandle, Mode, PostAction, generic::Generic};
use smithay::wayland::selection::{
    SelectionSource, SelectionTarget,
    data_device::{clear_data_device_selection, request_data_device_client_selection, set_data_device_selection},
    primary_selection::{clear_primary_selection, request_primary_client_selection, set_primary_selection},
};

use crate::Aurora;
use host::{Host, HostEvent, idx};
use slot::{Act, Slot};

/// Where to look for a host.
pub enum Choice {
    /// `--no-host-clipboard`.
    Disabled,
    /// No host exists (headless, DRM).
    NoHost(&'static str),
    /// The host named by `WAYLAND_DISPLAY` (winit).
    Env,
    /// `--host-clipboard-display NAME`: a specific socket (tests point this at a second Aurora).
    Display(OsString),
}

pub struct HostClipboard {
    host: Host,
    slots: [Slot; 2],
}

/// Open the bridge if there is a host to bridge to. Logs the outcome once; never fails.
pub fn start(state: &mut Aurora, handle: &LoopHandle<'static, Aurora>, choice: Choice) {
    let display = match choice {
        Choice::Disabled => return eprintln!("aurora-compositor: host clipboard bridging is off (--no-host-clipboard); guest-to-guest copy and paste still work"),
        Choice::NoHost(why) => return eprintln!("aurora-compositor: host clipboard bridging is off ({why}); guest-to-guest copy and paste still work"),
        Choice::Env => None,
        Choice::Display(d) => Some(d),
    };
    // A host that accepts the connection and never answers must not stall startup: bound the
    // handshake. (A timed-out attempt leaves one parked thread that touches nothing of ours.)
    let (tx, rx) = std::sync::mpsc::channel();
    std::thread::spawn(move || { let _ = tx.send(host::connect(display.as_deref())); });
    let connected = rx.recv_timeout(std::time::Duration::from_secs(3)).unwrap_or_else(|_| Err("the host did not answer within 3s".to_string()));
    let (host, mut queue) = match connected {
        Ok(x) => x,
        Err(e) => return eprintln!("aurora-compositor: host clipboard bridging is off ({e}); guest-to-guest copy and paste still work"),
    };
    let fd = match host.conn.backend().poll_fd().try_clone_to_owned() {
        Ok(fd) => fd,
        Err(e) => return eprintln!("aurora-compositor: host clipboard bridging is off (cannot watch the connection: {e}); guest-to-guest copy and paste still work"),
    };
    state.host_clip = Some(HostClipboard { host, slots: Default::default() });
    let src = Generic::new(fd, Interest::READ, Mode::Level);
    let inserted = handle.insert_source(src, move |_, _, state| {
        let Some(hc) = state.host_clip.as_mut() else { return Ok(PostAction::Remove) };
        let r = (|| -> Result<(), String> {
            queue.dispatch_pending(&mut hc.host.client).map_err(|e| e.to_string())?;
            match hc.host.conn.flush() {
                Ok(()) => {}
                Err(wayland_client::backend::WaylandError::Io(e)) if e.kind() == std::io::ErrorKind::WouldBlock => {}
                Err(e) => return Err(e.to_string()),
            }
            if let Some(guard) = queue.prepare_read() {
                match guard.read() {
                    Ok(_) => {}
                    Err(wayland_client::backend::WaylandError::Io(e)) if e.kind() == std::io::ErrorKind::WouldBlock => {}
                    Err(e) => return Err(e.to_string()),
                }
            }
            queue.dispatch_pending(&mut hc.host.client).map_err(|e| e.to_string())?;
            Ok(())
        })();
        match r {
            Ok(()) => {
                pump(state);
                Ok(PostAction::Continue)
            }
            Err(e) => {
                eprintln!("aurora-compositor: host clipboard connection lost ({e}); bridging stopped, guest-to-guest copy and paste still work");
                state.host_clip = None;
                Ok(PostAction::Remove)
            }
        }
    });
    if inserted.is_err() {
        state.host_clip = None;
        return eprintln!("aurora-compositor: host clipboard bridging is off (cannot watch the connection); guest-to-guest copy and paste still work");
    }
    eprintln!("aurora-compositor: host clipboard bridging is on (ext_data_control on the host, clipboard and primary selection)");
    pump(state);
}

/// Apply what the host did since the last call.
fn pump(state: &mut Aurora) {
    let Some(mut hc) = state.host_clip.take() else { return };
    let events = std::mem::take(&mut hc.host.client.events);
    let now = Instant::now();
    for ev in events {
        match ev {
            HostEvent::Selected(t, mimes) => {
                let act = hc.slots[idx(t)].host_selected(mimes.as_deref(), now);
                apply(state, &mut hc.host, t, act);
            }
            HostEvent::CancelledOurs(t) => {
                let act = hc.slots[idx(t)].host_cancelled_ours(now);
                apply(state, &mut hc.host, t, act);
            }
            HostEvent::Send(t, mime, fd) => {
                // Straight through: the guest's source writes into the host client's pipe. If the
                // guest selection changed meanwhile this fails and the fd is dropped (EOF).
                let r = match t {
                    SelectionTarget::Clipboard => request_data_device_client_selection::<Aurora>(&state.seat, mime, fd).map_err(|e| e.to_string()),
                    SelectionTarget::Primary => request_primary_client_selection::<Aurora>(&state.seat, mime, fd).map_err(|e| e.to_string()),
                };
                if let Err(e) = r {
                    tracing::debug!("clipboard: host asked for {t:?}, guest cannot serve it: {e}");
                }
            }
            HostEvent::Finished => eprintln!("aurora-compositor: the host's data-control device finished (its seat went away); host clipboard bridging has stopped"),
        }
    }
    let _ = hc.host.conn.flush();
    state.host_clip = Some(hc);
}

fn apply(state: &mut Aurora, host: &mut Host, t: SelectionTarget, act: Act) {
    let dh = state.display_handle.clone();
    match (act, t) {
        (Act::Nothing, _) => {}
        (Act::SetGuest(m), SelectionTarget::Clipboard) => set_data_device_selection::<Aurora>(&dh, &state.seat, m, ()),
        (Act::SetGuest(m), SelectionTarget::Primary) => set_primary_selection::<Aurora>(&dh, &state.seat, m, ()),
        (Act::ClearGuest, SelectionTarget::Clipboard) => clear_data_device_selection::<Aurora>(&dh, &state.seat),
        (Act::ClearGuest, SelectionTarget::Primary) => clear_primary_selection::<Aurora>(&dh, &state.seat),
        (Act::PublishHost(m), t) => host.publish(t, &m),
        (Act::ClearHost, t) => host.unpublish(t),
    }
}

/// `SelectionHandler::new_selection`: a guest client set (or cleared) the selection.
pub fn guest_selected(state: &mut Aurora, t: SelectionTarget, source: Option<SelectionSource>) {
    let Some(mut hc) = state.host_clip.take() else { return };
    let mimes = source.map(|s| s.mime_types());
    let act = hc.slots[idx(t)].guest_selected(mimes.as_deref(), Instant::now());
    apply(state, &mut hc.host, t, act);
    state.host_clip = Some(hc);
}

/// `SelectionHandler::send_selection`: a guest pastes the host-origin selection.
pub fn guest_pastes(state: &mut Aurora, t: SelectionTarget, mime: String, fd: OwnedFd) {
    if let Some(hc) = state.host_clip.as_mut() {
        if !hc.host.receive(t, &mime, fd) {
            tracing::debug!("clipboard: guest pasted {t:?} but the host offer is gone");
        }
    }
}
