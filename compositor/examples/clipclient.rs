//! A data-control clipboard client for the clipboard tests (like wl-copy / wl-paste, no focus needed).
//! Connects via WAYLAND_DISPLAY and XDG_RUNTIME_DIR. It never touches anything but the compositor
//! it is pointed at.
//!
//!   clipclient set   [--primary] MIME DATA     own the selection and serve DATA until killed
//!                                              (prints `set`, `send MIME` per paste, `cancelled`)
//!   clipclient get   [--primary] MIME          print `data <DATA:?>` for the current selection, or `none`
//!   clipclient watch [--primary] SECONDS       print `sel <types>` / `sel none` per selection event
//!   clipclient clear [--primary]               unset the selection

use std::io::{Read, Write};
use std::os::fd::AsFd;
use std::sync::Mutex;
use std::time::{Duration, Instant};

use wayland_client::{
    Connection, Dispatch, Proxy, QueueHandle, delegate_noop, event_created_child,
    globals::{GlobalListContents, registry_queue_init},
    protocol::{wl_registry::WlRegistry, wl_seat::WlSeat},
};
use wayland_protocols::ext::data_control::v1::client::{
    ext_data_control_device_v1::{self, ExtDataControlDeviceV1},
    ext_data_control_manager_v1::ExtDataControlManagerV1,
    ext_data_control_offer_v1::{self, ExtDataControlOfferV1},
    ext_data_control_source_v1::{self, ExtDataControlSourceV1},
};

struct Offer(Mutex<Vec<String>>);

struct App {
    primary: bool,
    verbose: bool,
    serve: Option<(String, Vec<u8>)>,
    current: Option<ExtDataControlOfferV1>,
    own: Option<ExtDataControlSourceV1>,
    events: u32,
}

fn say(s: &str) { println!("{s}"); let _ = std::io::stdout().flush(); }

impl Dispatch<ExtDataControlDeviceV1, ()> for App {
    fn event(s: &mut Self, _: &ExtDataControlDeviceV1, e: ext_data_control_device_v1::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        use ext_data_control_device_v1::Event::*;
        let id = match e {
            Selection { id } if !s.primary => id,
            PrimarySelection { id } if s.primary => id,
            _ => return,
        };
        s.events += 1;
        let types = id.as_ref().map(|o| o.data::<Offer>().unwrap().0.lock().unwrap().clone());
        if s.verbose { match &types {
            Some(t) => say(&format!("sel {}", t.join(","))),
            None => say("sel none"),
        } }
        if let Some(old) = std::mem::replace(&mut s.current, id) { old.destroy(); }
    }
    event_created_child!(App, ExtDataControlDeviceV1, [
        ext_data_control_device_v1::EVT_DATA_OFFER_OPCODE => (ExtDataControlOfferV1, Offer(Mutex::new(vec![]))),
    ]);
}
impl Dispatch<ExtDataControlOfferV1, Offer> for App {
    fn event(_: &mut Self, _: &ExtDataControlOfferV1, e: ext_data_control_offer_v1::Event, d: &Offer, _: &Connection, _: &QueueHandle<Self>) {
        if let ext_data_control_offer_v1::Event::Offer { mime_type } = e { d.0.lock().unwrap().push(mime_type); }
    }
}
impl Dispatch<ExtDataControlSourceV1, ()> for App {
    fn event(s: &mut Self, src: &ExtDataControlSourceV1, e: ext_data_control_source_v1::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        match e {
            ext_data_control_source_v1::Event::Send { mime_type, fd } => {
                say(&format!("send {mime_type}"));
                if let Some((m, data)) = &s.serve {
                    if *m == mime_type {
                        let mut f = std::fs::File::from(fd);
                        let _ = f.write_all(data);
                    }
                }
            }
            ext_data_control_source_v1::Event::Cancelled => { say("cancelled"); src.destroy(); if s.own.as_ref() == Some(src) { s.own = None; } }
            _ => {}
        }
    }
}
impl Dispatch<WlRegistry, GlobalListContents> for App {
    fn event(_: &mut Self, _: &WlRegistry, _: wayland_client::protocol::wl_registry::Event, _: &GlobalListContents, _: &Connection, _: &QueueHandle<Self>) {}
}
delegate_noop!(App: ignore WlSeat);
delegate_noop!(App: ExtDataControlManagerV1);

fn main() {
    let mut a = std::env::args().skip(1);
    let mode = a.next().expect("mode: set|get|watch|clear");
    let mut rest: Vec<String> = a.collect();
    let primary = rest.first().is_some_and(|s| s == "--primary");
    if primary { rest.remove(0); }

    let conn = Connection::connect_to_env().expect("connect");
    let (globals, mut q) = registry_queue_init::<App>(&conn).expect("registry");
    let qh = q.handle();
    let mgr: ExtDataControlManagerV1 = globals.bind(&qh, 1..=1, ()).expect("no ext_data_control_manager_v1");
    let seat: WlSeat = globals.bind(&qh, 1..=1, ()).expect("no wl_seat");
    let dev = mgr.get_data_device(&seat, &qh, ());
    let mut app = App { primary, verbose: mode == "watch", serve: None, current: None, own: None, events: 0 };
    q.roundtrip(&mut app).unwrap();
    q.roundtrip(&mut app).unwrap();

    match mode.as_str() {
        "set" => {
            let (mime, data) = (rest[0].clone(), rest[1].clone());
            let src = mgr.create_data_source(&qh, ());
            src.offer(mime.clone());
            if primary { dev.set_primary_selection(Some(&src)); } else { dev.set_selection(Some(&src)); }
            app.serve = Some((mime, data.into_bytes()));
            app.own = Some(src);
            q.roundtrip(&mut app).unwrap();
            say("set");
            loop { q.blocking_dispatch(&mut app).unwrap(); }
        }
        "get" => {
            let mime = rest[0].clone();
            let Some(offer) = app.current.clone() else { say("none"); return };
            let (mut r, w) = std::io::pipe().unwrap();
            offer.receive(mime, w.as_fd());
            drop(w);
            conn.flush().unwrap();
            // Our own end must not be the thing that blocks: dispatch while the data arrives.
            let start = Instant::now();
            let reader = std::thread::spawn(move || { let mut v = Vec::new(); let _ = r.read_to_end(&mut v); v });
            while !reader.is_finished() && start.elapsed() < Duration::from_secs(5) {
                q.dispatch_pending(&mut app).unwrap();
                std::thread::sleep(Duration::from_millis(10));
            }
            if reader.is_finished() { say(&format!("data {:?}", String::from_utf8_lossy(&reader.join().unwrap()))); } else { say("timeout"); }
        }
        "watch" => {
            let secs: f64 = rest[0].parse().unwrap();
            let end = Instant::now() + Duration::from_secs_f64(secs);
            app.events = 0; // the initial state arrives during setup; count changes only
            say("watching");
            while Instant::now() < end {
                q.flush().unwrap();
                if let Some(g) = q.prepare_read() {
                    // poll the socket with a short timeout instead of blocking forever
                    let fd = g.connection_fd().try_clone_to_owned().unwrap();
                    let mut p = [libc::pollfd { fd: std::os::fd::AsRawFd::as_raw_fd(&fd), events: libc::POLLIN, revents: 0 }];
                    let n = unsafe { libc::poll(p.as_mut_ptr(), 1, 50) };
                    if n > 0 { let _ = g.read(); } else { drop(g); }
                }
                q.dispatch_pending(&mut app).unwrap();
            }
            say(&format!("events {}", app.events));
        }
        "clear" => {
            if primary { dev.set_primary_selection(None); } else { dev.set_selection(None); }
            q.roundtrip(&mut app).unwrap();
            say("cleared");
        }
        m => panic!("unknown mode {m}"),
    }
}
