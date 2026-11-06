//! An input method (`zwp_input_method_v2`) for the IME tests. It prints what the compositor tells
//! it (`im unavailable`, `im activate`, `im deactivate`, `im content_type H P`, `im done N`) and, each time
//! a text input is activated and the compositor has said `done`, sends a preedit string and then
//! commits a string, the way a real input method does while the user types:
//!
//!   set_preedit_string(PREEDIT, 0, len) ; commit(serial)
//!   commit_string(COMMIT) ; set_preedit_string("", 0, 0) ; commit(serial)
//!
//! Usage: imeclient [--preedit TEXT] [--commit TEXT]
//! (connects via WAYLAND_DISPLAY). `im sent` is printed after the second commit.

use std::io::Write;

use wayland_client::{
    Connection, Dispatch, QueueHandle, WEnum, delegate_noop,
    globals::{GlobalListContents, registry_queue_init},
    protocol::{wl_registry::WlRegistry, wl_seat::WlSeat},
};
use wayland_protocols_misc::zwp_input_method_v2::client::{
    zwp_input_method_manager_v2::ZwpInputMethodManagerV2,
    zwp_input_method_v2::{self, ZwpInputMethodV2},
};

struct App { preedit: String, commit: String, serial: u32, active: bool, sent_for: Option<u32> }

fn say(s: &str) { println!("{s}"); let _ = std::io::stdout().flush(); }

impl Dispatch<ZwpInputMethodV2, ()> for App {
    fn event(s: &mut Self, im: &ZwpInputMethodV2, e: zwp_input_method_v2::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        use zwp_input_method_v2::Event::*;
        match e {
            Unavailable => { say("im unavailable"); std::process::exit(2); }
            Activate => { say("im activate"); s.active = true; }
            Deactivate => { say("im deactivate"); s.active = false; }
            ContentType { hint, purpose } => {
                let (h, p) = (match hint { WEnum::Value(v) => v.bits(), WEnum::Unknown(v) => v }, match purpose { WEnum::Value(v) => v as u32, WEnum::Unknown(v) => v });
                say(&format!("im content_type {h} {p}"));
            }
            SurroundingText { text, cursor, anchor } => say(&format!("im surrounding {text:?} {cursor} {anchor}")),
            Done => {
                s.serial += 1;
                say(&format!("im done {}", s.serial));
                if s.active && s.sent_for != Some(s.serial) {
                    s.sent_for = Some(s.serial);
                    im.set_preedit_string(s.preedit.clone(), 0, s.preedit.len() as i32);
                    im.commit(s.serial);
                    im.commit_string(s.commit.clone());
                    im.set_preedit_string(String::new(), 0, 0);
                    im.commit(s.serial);
                    say("im sent");
                }
            }
            _ => {}
        }
    }
}
impl Dispatch<WlRegistry, GlobalListContents> for App {
    fn event(_: &mut Self, _: &WlRegistry, _: wayland_client::protocol::wl_registry::Event, _: &GlobalListContents, _: &Connection, _: &QueueHandle<Self>) {}
}
delegate_noop!(App: ignore WlSeat);
delegate_noop!(App: ZwpInputMethodManagerV2);

fn main() {
    let (mut preedit, mut commit) = ("ni".to_string(), "\u{4f60}".to_string());
    let mut a = std::env::args().skip(1);
    while let Some(f) = a.next() {
        match f.as_str() {
            "--preedit" => preedit = a.next().unwrap(),
            "--commit" => commit = a.next().unwrap(),
            o => panic!("unknown argument {o}"),
        }
    }
    let conn = Connection::connect_to_env().expect("connect");
    let (globals, mut q) = registry_queue_init::<App>(&conn).unwrap();
    let qh = q.handle();
    let seat: WlSeat = globals.bind(&qh, 1..=5, ()).unwrap();
    let mgr: ZwpInputMethodManagerV2 = globals.bind(&qh, 1..=1, ()).expect("zwp_input_method_manager_v2");
    let _im = mgr.get_input_method(&seat, &qh, ());
    let mut app = App { preedit, commit, serial: 0, active: false, sent_for: None };
    q.roundtrip(&mut app).expect("roundtrip");
    say("im bound");
    loop { q.blocking_dispatch(&mut app).expect("dispatch"); }
}
