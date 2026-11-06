//! The loop-prevention state machine, with no Wayland in it so it can be tested to death.
//!
//! One `Slot` per selection target (clipboard, primary). It records which side *originated*
//! the selection now in effect and turns events from either side into at most one action on
//! the other. The rules:
//!
//! * Bridging only ever happens in the direction the selection travelled. A selection the host
//!   gave us is installed in the guest as a compositor-side selection, and Smithay does not call
//!   `new_selection` for those, so it cannot bounce back by construction. A selection a guest
//!   client set is published to the host as an `ext_data_control_source_v1`.
//! * The host sends our own source back to us as an ordinary `selection` event (the setter is a
//!   data-control device like any other). That echo is recognised -- we own a live source and the
//!   offer carries exactly its MIME types -- and swallowed.
//! * The echo test is a heuristic, so the offer is also *held*: if the source is then cancelled,
//!   it was not an echo but a real foreign selection with the same types, and the held offer is
//!   adopted. Nothing is lost, and nothing is applied twice.
//! * A circuit breaker is the last line. Two clipboard managers on opposite sides that each
//!   re-publish whatever they see would ping-pong forever through any bridge. If the slot flips
//!   direction more than `MAX_FLIPS` times in `WINDOW` it stops bridging for `COOLDOWN` and says so.

use std::collections::VecDeque;
use std::time::{Duration, Instant};

/// More than this many bridged changes inside `WINDOW` is a loop, not a person copying.
pub const MAX_FLIPS: usize = 12;
pub const WINDOW: Duration = Duration::from_secs(2);
pub const COOLDOWN: Duration = Duration::from_secs(10);
/// A selection offering more types than this is not a clipboard, it is an attack.
pub const MAX_MIME_TYPES: usize = 128;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Origin {
    /// Nothing bridged is in effect.
    None,
    /// The host set the selection in effect (the guest holds a compositor-side copy of its types).
    Host,
    /// A guest client set it (the host holds our source for it).
    Guest,
}

/// What the glue must do next. At most one, always towards the side that did not cause the event.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Act {
    Nothing,
    /// Install a compositor-side selection with these types in the guest.
    SetGuest(Vec<String>),
    ClearGuest,
    /// Create a host source with these types and make it the host selection.
    PublishHost(Vec<String>),
    /// Unset the host selection (our source is dropped).
    ClearHost,
}

/// MIME negotiation: what we pass across. Order is kept (clients treat the first as preferred),
/// duplicates and empty names go, the list is capped. `None` when nothing is left to offer.
pub fn negotiate(offered: &[String]) -> Option<Vec<String>> {
    let mut out: Vec<String> = Vec::new();
    for m in offered {
        if m.is_empty() || out.contains(m) {
            continue;
        }
        out.push(m.clone());
        if out.len() == MAX_MIME_TYPES {
            break;
        }
    }
    (!out.is_empty()).then_some(out)
}

fn same_set(a: &[String], b: &[String]) -> bool {
    a.len() == b.len() && a.iter().all(|m| b.contains(m))
}

#[derive(Debug)]
pub struct Slot {
    origin: Origin,
    /// MIME types of our source while the host still holds it live.
    ours_on_host: Option<Vec<String>>,
    /// The latest host offer swallowed as a presumed echo.
    held: Option<Vec<String>>,
    flips: VecDeque<Instant>,
    tripped_until: Option<Instant>,
}

impl Default for Slot {
    fn default() -> Self {
        Slot { origin: Origin::None, ours_on_host: None, held: None, flips: VecDeque::new(), tripped_until: None }
    }
}

impl Slot {
    #[cfg(test)]
    pub fn origin(&self) -> Origin {
        self.origin
    }

    /// Whether the circuit breaker is open at `now`.
    pub fn tripped(&self, now: Instant) -> bool {
        self.tripped_until.is_some_and(|t| now < t)
    }

    /// Record a bridged change; open the breaker if there were too many.
    fn gate(&mut self, now: Instant, act: Act) -> Act {
        if act == Act::Nothing {
            return act;
        }
        if self.tripped(now) {
            return Act::Nothing;
        }
        self.tripped_until = None;
        while self.flips.front().is_some_and(|t| now.duration_since(*t) > WINDOW) {
            self.flips.pop_front();
        }
        self.flips.push_back(now);
        if self.flips.len() > MAX_FLIPS {
            self.tripped_until = Some(now + COOLDOWN);
            self.flips.clear();
            tracing::warn!("clipboard: selection changed {MAX_FLIPS}+ times in {WINDOW:?}: a clipboard manager on each side is probably re-publishing in a loop; not bridging this selection for {COOLDOWN:?}");
            return Act::Nothing;
        }
        act
    }

    /// The host's selection changed (`None`: cleared). `offered` is the raw type list.
    pub fn host_selected(&mut self, offered: Option<&[String]>, now: Instant) -> Act {
        match offered.and_then(negotiate) {
            Some(m) => {
                if self.ours_on_host.as_deref().is_some_and(|ours| same_set(ours, &m)) {
                    // Our own source coming back (or, rarely, a twin: see `host_cancelled_ours`).
                    self.held = Some(m);
                    return Act::Nothing;
                }
                let act = self.gate(now, Act::SetGuest(m));
                if matches!(act, Act::SetGuest(_)) {
                    self.origin = Origin::Host;
                    self.held = None;
                }
                act
            }
            None => {
                self.held = None;
                if self.origin == Origin::None {
                    return Act::Nothing;
                }
                let act = self.gate(now, Act::ClearGuest);
                if act == Act::ClearGuest {
                    self.origin = Origin::None;
                }
                act
            }
        }
    }

    /// A guest client set the selection (`None`: cleared it).
    pub fn guest_selected(&mut self, offered: Option<&[String]>, now: Instant) -> Act {
        self.held = None;
        match offered.and_then(negotiate) {
            Some(m) => {
                let act = self.gate(now, Act::PublishHost(m.clone()));
                if act != Act::Nothing {
                    self.origin = Origin::Guest;
                    self.ours_on_host = Some(m);
                }
                act
            }
            // A client clearing the selection only clears the host's if that selection was the
            // guest's own. Otherwise any app that clears on startup would wipe the user's clipboard.
            None if self.origin == Origin::Guest => {
                let act = self.gate(now, Act::ClearHost);
                if act == Act::ClearHost {
                    self.origin = Origin::None;
                    self.ours_on_host = None;
                }
                act
            }
            None => Act::Nothing,
        }
    }

    /// The host cancelled our current source: someone else's selection replaced it.
    pub fn host_cancelled_ours(&mut self, now: Instant) -> Act {
        self.ours_on_host = None;
        if self.origin != Origin::Guest {
            return Act::Nothing;
        }
        // The replacing selection either already arrived (held: it looked like an echo but was a
        // twin) or is about to, as a normal `selection` event.
        match self.held.take() {
            Some(m) => {
                let act = self.gate(now, Act::SetGuest(m));
                if matches!(act, Act::SetGuest(_)) {
                    self.origin = Origin::Host;
                }
                act
            }
            None => Act::Nothing,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn v(s: &[&str]) -> Vec<String> { s.iter().map(|s| s.to_string()).collect() }
    fn t0() -> Instant { Instant::now() }

    #[test]
    fn negotiate_dedupes_keeps_order_and_drops_empties() {
        assert_eq!(negotiate(&v(&["text/plain;charset=utf-8", "", "UTF8_STRING", "text/plain;charset=utf-8"])), Some(v(&["text/plain;charset=utf-8", "UTF8_STRING"])));
        assert_eq!(negotiate(&v(&[""])), None);
        assert_eq!(negotiate(&[]), None);
    }

    #[test]
    fn negotiate_caps_the_list() {
        let many: Vec<String> = (0..1000).map(|i| format!("x/{i}")).collect();
        let n = negotiate(&many).unwrap();
        assert_eq!(n.len(), MAX_MIME_TYPES);
        assert_eq!(n[0], "x/0");
    }

    #[test]
    fn host_selection_goes_to_the_guest_once() {
        let mut s = Slot::default();
        let now = t0();
        assert_eq!(s.host_selected(Some(&v(&["text/plain"])), now), Act::SetGuest(v(&["text/plain"])));
        assert_eq!(s.origin(), Origin::Host);
    }

    #[test]
    fn guest_selection_goes_to_the_host_and_its_echo_is_swallowed() {
        let mut s = Slot::default();
        let now = t0();
        assert_eq!(s.guest_selected(Some(&v(&["text/plain", "UTF8_STRING"])), now), Act::PublishHost(v(&["text/plain", "UTF8_STRING"])));
        assert_eq!(s.origin(), Origin::Guest);
        // The host reports our own source back, types in another order.
        assert_eq!(s.host_selected(Some(&v(&["UTF8_STRING", "text/plain"])), now), Act::Nothing);
        assert_eq!(s.origin(), Origin::Guest);
    }

    #[test]
    fn a_foreign_selection_with_the_same_types_is_adopted_when_ours_is_cancelled() {
        let mut s = Slot::default();
        let now = t0();
        s.guest_selected(Some(&v(&["text/plain"])), now);
        // looks like an echo ...
        assert_eq!(s.host_selected(Some(&v(&["text/plain"])), now), Act::Nothing);
        // ... but the host then cancels our source: it was someone else's copy.
        assert_eq!(s.host_cancelled_ours(now), Act::SetGuest(v(&["text/plain"])));
        assert_eq!(s.origin(), Origin::Host);
    }

    #[test]
    fn cancel_before_the_replacing_selection_event_is_not_a_clear() {
        let mut s = Slot::default();
        let now = t0();
        s.guest_selected(Some(&v(&["text/plain"])), now);
        assert_eq!(s.host_cancelled_ours(now), Act::Nothing);
        assert_eq!(s.host_selected(Some(&v(&["image/png"])), now), Act::SetGuest(v(&["image/png"])));
        assert_eq!(s.origin(), Origin::Host);
    }

    #[test]
    fn a_different_host_selection_replaces_ours() {
        let mut s = Slot::default();
        let now = t0();
        s.guest_selected(Some(&v(&["text/plain"])), now);
        assert_eq!(s.host_selected(Some(&v(&["image/png"])), now), Act::SetGuest(v(&["image/png"])));
        // the late cancel of our source changes nothing
        assert_eq!(s.host_cancelled_ours(now), Act::Nothing);
        assert_eq!(s.origin(), Origin::Host);
    }

    #[test]
    fn host_clear_clears_the_guest_only_if_something_was_bridged() {
        let mut s = Slot::default();
        let now = t0();
        assert_eq!(s.host_selected(None, now), Act::Nothing);
        s.host_selected(Some(&v(&["text/plain"])), now);
        assert_eq!(s.host_selected(None, now), Act::ClearGuest);
        assert_eq!(s.origin(), Origin::None);
    }

    #[test]
    fn a_guest_clear_cannot_wipe_a_host_clipboard() {
        let mut s = Slot::default();
        let now = t0();
        s.host_selected(Some(&v(&["text/plain"])), now);
        assert_eq!(s.guest_selected(None, now), Act::Nothing);
        assert_eq!(s.origin(), Origin::Host);
        // but it clears its own
        s.guest_selected(Some(&v(&["text/plain"])), now);
        assert_eq!(s.guest_selected(None, now), Act::ClearHost);
        assert_eq!(s.origin(), Origin::None);
    }

    #[test]
    fn an_empty_type_list_is_no_selection() {
        let mut s = Slot::default();
        let now = t0();
        assert_eq!(s.host_selected(Some(&v(&[""])), now), Act::Nothing);
        assert_eq!(s.guest_selected(Some(&[]), now), Act::Nothing);
    }

    #[test]
    fn ping_pong_between_two_managers_trips_the_breaker_and_recovers() {
        let mut s = Slot::default();
        let start = t0();
        let mut applied = 0;
        // each side re-publishes whatever the other just did, 100 times, 1 ms apart
        for i in 0..100u32 {
            let now = start + Duration::from_millis(i as u64);
            let types = v(&[&format!("text/plain;n={i}")]);
            let a = if i % 2 == 0 { s.host_selected(Some(&types), now) } else { s.guest_selected(Some(&types), now) };
            if a != Act::Nothing { applied += 1; }
        }
        assert!(applied <= MAX_FLIPS, "applied {applied}");
        assert!(s.tripped(start + Duration::from_millis(100)));
        // a person copying again after the cooldown works
        let later = start + COOLDOWN + Duration::from_secs(1);
        assert!(!s.tripped(later));
        assert_eq!(s.host_selected(Some(&v(&["text/plain"])), later), Act::SetGuest(v(&["text/plain"])));
    }

    #[test]
    fn people_copying_at_human_speed_never_trip() {
        let mut s = Slot::default();
        let start = t0();
        for i in 0..200u64 {
            let now = start + Duration::from_millis(i * 500);
            let t = v(&[&format!("text/plain;n={i}")]);
            let a = if i % 2 == 0 { s.host_selected(Some(&t), now) } else { s.guest_selected(Some(&t), now) };
            assert_ne!(a, Act::Nothing, "change {i} was dropped");
        }
    }
}
