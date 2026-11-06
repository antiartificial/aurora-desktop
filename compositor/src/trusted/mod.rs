//! The trusted UI path: approval prompts that only the compositor draws.
//!
//! A pending approval is not a surface. It is a record in `Trusted::queue`,
//! rendered by `draw`, answered from the keyboard by `Aurora::trusted_key`, and
//! requested over a separate Unix socket (`socket`). A Wayland client has no
//! protocol object for any of it, and the capture path runs before the overlay
//! is drawn so it cannot read it. See README.md, "Trusted path".

pub mod draw;
pub mod dump;
mod font;
pub mod socket;
pub mod testinput;
pub mod verify;

use std::{collections::VecDeque, io::Write, os::unix::net::UnixStream, path::PathBuf, time::Instant};

use smithay::{
    backend::input::{InputTime, KeyState},
    input::keyboard::{FilterResult, Keycode, xkb},
    reexports::wayland_server::{Resource, protocol::wl_surface::WlSurface},
    utils::SERIAL_COUNTER,
};

use crate::Aurora;

/// What the controller asked the user to approve. Every string is untrusted
/// display text (it comes from an app's verb, args and paths).
#[derive(Debug, Clone)]
pub struct Request {
    pub request_id: String,
    pub app: String,
    /// The requester's CLAIM of the domain's display name. Not verified.
    pub domain: String,
    /// The requester's claim of the domain's bus identity; what is checked.
    pub domain_identity: String,
    /// What the compositor itself established about the claim (verify.rs).
    pub verification: verify::Verification,
    pub verb: String,
    pub effect: String,
    pub grants: Vec<String>,
    pub args: String,
}

pub struct Pending {
    pub id: u64,
    pub req: Request,
    pub deadline: Instant,
    pub out: UnixStream,
}

#[derive(Default)]
pub struct Trusted {
    pub queue: VecDeque<Pending>,
    /// Keyboard focus to give back when the last overlay goes away.
    saved_focus: Option<WlSurface>,
    next_id: u64,
    pub panel: Option<draw::Canvas>,
    pub panel_key: Option<(u64, u64, usize)>,
    /// `--dump-overlay`: where the compositor writes its own final frame.
    pub dump_path: Option<PathBuf>,
    pub dumped: Option<u64>,
    pub allow: socket::Allowlist,
    /// Bus session socket for domain verification; None means every claim is `unknown`.
    pub bus_sock: Option<PathBuf>,
    /// Keys pressed while an overlay was up. Their releases are swallowed
    /// too, even after the overlay is gone, so a client never sees half a keystroke.
    swallowed: std::collections::HashSet<u32>,
}

impl Trusted {
    pub fn active(&self) -> bool {
        !self.queue.is_empty()
    }
}

/// Map a key press to a decision. Only `a` allows; `d` and Escape deny;
/// every other key is swallowed and does nothing.
fn decide(sym: xkb::Keysym) -> Option<(bool, String)> {
    let name = xkb::keysym_get_name(sym);
    match name.as_str() {
        "a" | "A" => Some((true, name)),
        "d" | "D" | "Escape" => Some((false, name)),
        _ => None,
    }
}

fn reply(mut out: &UnixStream, req: &Request, allow: bool, key: &str, reason: &str) {
    let body = serde_json::json!({
        "v": 1, "request_id": req.request_id,
        "domain_verified": req.verification.verdict.wire(), "domain_verify_detail": req.verification.detail,
        "decision": if allow { "allow" } else { "deny" }, "key": key, "reason": reason,
    });
    let _ = writeln!(out, "{body}");
    let _ = out.shutdown(std::net::Shutdown::Both);
}

impl Aurora {
    pub fn overlay_up(&self) -> bool {
        self.trusted.active()
    }

    /// Queue a request. The first one raises the overlay: the focused client
    /// is told it lost focus (so it sees no further keys and no stuck keys)
    /// and the pointer leaves it.
    pub fn trusted_enqueue(&mut self, req: Request, timeout: std::time::Duration, out: UnixStream) -> u64 {
        let was_up = self.trusted.active();
        self.trusted.next_id += 1;
        let id = self.trusted.next_id;
        tracing::info!("trusted: approval {} queued: {} {} of {}", req.request_id, req.effect, req.verb, req.app);
        self.trusted.queue.push_back(Pending { id, req, deadline: Instant::now() + timeout, out });
        if !was_up {
            let kb = self.seat.get_keyboard().unwrap();
            self.trusted.saved_focus = kb.current_focus();
            kb.set_focus(self, None, SERIAL_COUNTER.next_serial());
            let ptr = self.seat.get_pointer().unwrap();
            let loc = ptr.current_location();
            ptr.motion(self, None, &smithay::input::pointer::MotionEvent { location: loc, serial: SERIAL_COUNTER.next_serial(), time: InputTime::now() });
            ptr.frame(self);
        }
        id
    }

    fn trusted_finish(&mut self, id: u64, allow: bool, key: &str, reason: &str) {
        let Some(i) = self.trusted.queue.iter().position(|p| p.id == id) else { return };
        let p = self.trusted.queue.remove(i).unwrap();
        tracing::info!("trusted: approval {} -> {} (key {key:?}, {reason})", p.req.request_id, if allow { "allow" } else { "deny" });
        reply(&p.out, &p.req, allow, key, reason);
        if !self.trusted.active() {
            let saved = self.trusted.saved_focus.take().filter(|s| s.is_alive());
            self.seat.get_keyboard().unwrap().set_focus(self, saved, SERIAL_COUNTER.next_serial());
        }
    }

    /// The requester hung up: drop its overlay without a reply.
    pub fn trusted_withdraw(&mut self, id: u64) {
        if let Some(i) = self.trusted.queue.iter().position(|p| p.id == id) {
            self.trusted.queue.remove(i);
            if !self.trusted.active() {
                let saved = self.trusted.saved_focus.take().filter(|s| s.is_alive());
                self.seat.get_keyboard().unwrap().set_focus(self, saved, SERIAL_COUNTER.next_serial());
            }
        }
    }

    /// Per-frame housekeeping: undecided requests past their deadline are denied.
    pub fn trusted_tick(&mut self) {
        let now = Instant::now();
        let expired: Vec<u64> = self.trusted.queue.iter().filter(|p| p.deadline <= now).map(|p| p.id).collect();
        for id in expired {
            self.trusted_finish(id, false, "", "timeout");
        }
    }

    /// The one entry point for keyboard events, real or injected. With an
    /// overlay up the key goes to the overlay and to nobody else.
    pub fn keyboard_input(&mut self, code: Keycode, state: KeyState, time: InputTime) {
        let serial = SERIAL_COUNTER.next_serial();
        let kb = self.seat.get_keyboard().unwrap();
        if !self.trusted.active() {
            if state == KeyState::Released && self.trusted.swallowed.remove(&code.raw()) {
                kb.input::<(), _>(self, code, state, serial, time, |_, _, _| FilterResult::Intercept(()));
                return;
            }
            let _ = serial;
            self.switcher_key(code, state, time);
            return;
        }
        let head = self.trusted.queue.front().map(|p| p.id);
        if state == KeyState::Pressed {
            self.trusted.swallowed.insert(code.raw());
        } else {
            self.trusted.swallowed.remove(&code.raw());
        }
        let verdict = kb.input::<Option<(bool, String)>, _>(self, code, state, serial, time, |_, _, h| {
            FilterResult::Intercept(if state == KeyState::Pressed { decide(h.modified_sym()) } else { None })
        });
        if let (Some(id), Some(Some((allow, key)))) = (head, verdict) {
            self.trusted_finish(id, allow, &key, "key");
        }
    }
}
