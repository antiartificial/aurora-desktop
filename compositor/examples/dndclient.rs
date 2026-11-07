//! A drag-and-drop client for the DnD tests: one solid toplevel, either end of a drag.
//! Connects via WAYLAND_DISPLAY and XDG_RUNTIME_DIR and touches nothing else.
//!
//!   dndclient source [--size WxH] MIME|DATA ...   on a left press over the window, start a drag
//!                                                 offering each MIME. DATA is text, or `repeat:N`
//!                                                 for N bytes of a fixed pattern.
//!   dndclient target [--size WxH] [--reject]      accept drags (first offered type, copy) and read
//!                                                 every type on drop. `--reject` accepts nothing.
//!
//! Everything it sees is one line on stdout: `mapped`, `enter X Y types=a,b`, `motion X Y`,
//! `leave`, `drop`, `data MIME len N fnv HEX [text "..."]`, `finished`; and from a source
//! `drag-started`, `send MIME`, `target MIME|none`, `action NAME`, `dropped`, `finished`, `cancelled`.

use std::io::{Read, Write};
use std::os::fd::AsFd;

use wayland_client::{
    Connection, Dispatch, Proxy, QueueHandle, WEnum, delegate_noop, event_created_child,
    globals::{GlobalListContents, registry_queue_init},
    protocol::{
        wl_buffer::WlBuffer, wl_compositor::WlCompositor,
        wl_data_device::{self, WlDataDevice},
        wl_data_device_manager::{DndAction, WlDataDeviceManager},
        wl_data_offer::{self, WlDataOffer},
        wl_data_source::{self, WlDataSource},
        wl_pointer::{self, WlPointer}, wl_registry::WlRegistry, wl_seat::{self, WlSeat},
        wl_shm::{self, WlShm}, wl_shm_pool::WlShmPool, wl_surface::WlSurface,
    },
};
use wayland_protocols::xdg::shell::client::{
    xdg_surface::{self, XdgSurface}, xdg_toplevel::XdgToplevel, xdg_wm_base::{self, XdgWmBase},
};

fn say(s: &str) { println!("{s}"); let _ = std::io::stdout().flush(); }

fn pattern(n: usize) -> Vec<u8> { (0..n).map(|i| b"0123456789abcdef"[(i * 7 + i / 16) % 16]).collect() }
fn fnv(b: &[u8]) -> u64 { b.iter().fold(0xcbf29ce484222325u64, |h, x| (h ^ *x as u64).wrapping_mul(0x100000001b3)) }

struct Offer(std::sync::Mutex<Vec<String>>);

struct App {
    surface: WlSurface, buffer: WlBuffer,
    source_mode: bool, reject: bool,
    _seat: WlSeat, manager: WlDataDeviceManager, device: Option<WlDataDevice>,
    items: Vec<(String, Vec<u8>)>,
    serial_pointer_enter: u32,
    /// The offer of the drag in progress, as target.
    offer: Option<(WlDataOffer, u32)>,
    source: Option<WlDataSource>,
}

impl Dispatch<WlDataDevice, ()> for App {
    fn event(s: &mut Self, _: &WlDataDevice, e: wl_data_device::Event, _: &(), conn: &Connection, _: &QueueHandle<Self>) {
        use wl_data_device::Event::*;
        match e {
            Enter { serial, x, y, id, .. } => {
                let Some(o) = id else { return };
                let types = o.data::<Offer>().unwrap().0.lock().unwrap().clone();
                say(&format!("enter {x:.0} {y:.0} types={}", types.join(",")));
                s.accept(&o, serial, &types);
                s.offer = Some((o, serial));
            }
            Motion { x, y, .. } => {
                say(&format!("motion {x:.0} {y:.0}"));
                // Repeat what we want on every motion: a host's own toolkit may have said no.
                if let Some((o, serial)) = s.offer.clone() {
                    let types = o.data::<Offer>().unwrap().0.lock().unwrap().clone();
                    s.accept(&o, serial, &types);
                }
            }
            Leave => { say("leave"); if let Some((o, _)) = s.offer.take() { o.destroy(); } }
            Drop => {
                say("drop");
                let Some((o, _)) = s.offer.take() else { return };
                let types = o.data::<Offer>().unwrap().0.lock().unwrap().clone();
                for t in &types {
                    let (r, w) = rustix_pipe();
                    o.receive(t.clone(), w.as_fd());
                    drop(w);
                    let _ = conn.flush();
                    let mut buf = vec![];
                    let _ = std::fs::File::from(r).read_to_end(&mut buf);
                    let text = if buf.len() <= 300 { format!(" text {:?}", String::from_utf8_lossy(&buf)) } else { String::new() };
                    say(&format!("data {t} len {} fnv {:016x}{text}", buf.len(), fnv(&buf)));
                }
                if o.version() >= 3 { o.finish(); }
                o.destroy();
                let _ = conn.flush();
                say("finished");
            }
            _ => {}
        }
    }
    event_created_child!(App, WlDataDevice, [
        wl_data_device::EVT_DATA_OFFER_OPCODE => (WlDataOffer, Offer(std::sync::Mutex::new(vec![]))),
    ]);
}

impl App {
    fn accept(&self, o: &WlDataOffer, serial: u32, types: &[String]) {
        if self.reject { o.accept(serial, None); return; }
        o.accept(serial, types.first().cloned());
        if o.version() >= 3 { o.set_actions(DndAction::Copy | DndAction::Move, DndAction::Copy); }
    }
}

fn rustix_pipe() -> (std::os::fd::OwnedFd, std::os::fd::OwnedFd) {
    let mut fds = [0i32; 2];
    assert_eq!(unsafe { libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC) }, 0);
    unsafe { (std::os::fd::FromRawFd::from_raw_fd(fds[0]), std::os::fd::FromRawFd::from_raw_fd(fds[1])) }
}

impl Dispatch<WlDataOffer, Offer> for App {
    fn event(_: &mut Self, _: &WlDataOffer, e: wl_data_offer::Event, d: &Offer, _: &Connection, _: &QueueHandle<Self>) {
        if let wl_data_offer::Event::Offer { mime_type } = e { d.0.lock().unwrap().push(mime_type); }
    }
}

impl Dispatch<WlDataSource, ()> for App {
    fn event(s: &mut Self, src: &WlDataSource, e: wl_data_source::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        use wl_data_source::Event::*;
        match e {
            Target { mime_type } => say(&format!("target {}", mime_type.as_deref().unwrap_or("none"))),
            Send { mime_type, fd } => {
                say(&format!("send {mime_type}"));
                if let Some((_, data)) = s.items.iter().find(|(m, _)| *m == mime_type) {
                    let data = data.clone();
                    std::thread::spawn(move || { let _ = std::fs::File::from(fd).write_all(&data); });
                }
            }
            Action { dnd_action } => say(&format!("action {dnd_action:?}")),
            DndDropPerformed => say("dropped"),
            DndFinished => { say("finished"); src.destroy(); s.source = None; }
            Cancelled => { say("cancelled"); src.destroy(); s.source = None; }
            _ => {}
        }
    }
}

impl Dispatch<WlSeat, ()> for App {
    fn event(s: &mut Self, seat: &WlSeat, e: wl_seat::Event, _: &(), _: &Connection, qh: &QueueHandle<Self>) {
        if let wl_seat::Event::Capabilities { capabilities: WEnum::Value(c) } = e {
            if c.contains(wl_seat::Capability::Pointer) { seat.get_pointer(qh, ()); }
            let _ = s;
        }
    }
}

impl Dispatch<WlPointer, ()> for App {
    fn event(s: &mut Self, _: &WlPointer, e: wl_pointer::Event, _: &(), _: &Connection, qh: &QueueHandle<Self>) {
        match e {
            wl_pointer::Event::Enter { serial, .. } => s.serial_pointer_enter = serial,
            wl_pointer::Event::Button { serial, state: WEnum::Value(wl_pointer::ButtonState::Pressed), .. } if s.source_mode && s.source.is_none() => {
                let src = s.manager.create_data_source(qh, ());
                for (m, _) in &s.items { src.offer(m.clone()); }
                src.set_actions(DndAction::Copy | DndAction::Move);
                s.device.as_ref().unwrap().start_drag(Some(&src), &s.surface, None, serial);
                s.source = Some(src);
                say("drag-started");
            }
            _ => {}
        }
    }
}

impl Dispatch<XdgWmBase, ()> for App {
    fn event(_: &mut Self, b: &XdgWmBase, e: xdg_wm_base::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let xdg_wm_base::Event::Ping { serial } = e { b.pong(serial); }
    }
}
impl Dispatch<XdgSurface, ()> for App {
    fn event(s: &mut Self, x: &XdgSurface, e: xdg_surface::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let xdg_surface::Event::Configure { serial } = e {
            x.ack_configure(serial);
            s.surface.attach(Some(&s.buffer), 0, 0);
            s.surface.commit();
            say("mapped");
        }
    }
}
impl Dispatch<WlRegistry, GlobalListContents> for App {
    fn event(_: &mut Self, _: &WlRegistry, _: wayland_client::protocol::wl_registry::Event, _: &GlobalListContents, _: &Connection, _: &QueueHandle<Self>) {}
}
delegate_noop!(App: WlCompositor);
delegate_noop!(App: ignore WlShm);
delegate_noop!(App: WlShmPool);
delegate_noop!(App: WlDataDeviceManager);
delegate_noop!(App: ignore WlBuffer);
delegate_noop!(App: ignore WlSurface);
delegate_noop!(App: ignore XdgToplevel);

fn main() {
    let mut args = std::env::args().skip(1);
    let mode = args.next().expect("source|target");
    let (mut w, mut h) = (300i32, 240i32);
    let mut reject = false;
    let mut items = vec![];
    while let Some(a) = args.next() {
        match a.as_str() {
            "--size" => { let v = args.next().unwrap(); let (a, b) = v.split_once('x').unwrap(); w = a.parse().unwrap(); h = b.parse().unwrap(); }
            "--reject" => reject = true,
            kv => {
                let (m, d) = kv.split_once('|').expect("MIME|DATA");
                let data = match d.strip_prefix("repeat:") { Some(n) => pattern(n.parse().unwrap()), None => d.as_bytes().to_vec() };
                items.push((m.to_string(), data));
            }
        }
    }
    let conn = Connection::connect_to_env().expect("no compositor");
    let (globals, mut queue) = registry_queue_init::<App>(&conn).unwrap();
    let qh = queue.handle();
    let compositor: WlCompositor = globals.bind(&qh, 1..=4, ()).unwrap();
    let shm: WlShm = globals.bind(&qh, 1..=1, ()).unwrap();
    let wm: XdgWmBase = globals.bind(&qh, 1..=1, ()).unwrap();
    let manager: WlDataDeviceManager = globals.bind(&qh, 1..=3, ()).unwrap();
    let seat: WlSeat = globals.bind(&qh, 1..=5, ()).unwrap();

    let len = (w * h * 4) as usize;
    let mut file = tempfile::tempfile().unwrap();
    let rgb = if mode == "source" { [0x30u8, 0x70, 0xd0] } else { [0x30, 0xb0, 0x60] };
    file.write_all(&(0..w * h).flat_map(|_| [rgb[2], rgb[1], rgb[0], 0xff]).collect::<Vec<u8>>()).unwrap();
    let pool = shm.create_pool(file.as_fd(), len as i32, &qh, ());
    let buffer = pool.create_buffer(0, w, h, w * 4, wl_shm::Format::Xrgb8888, &qh, ());
    let surface = compositor.create_surface(&qh, ());
    let xs = wm.get_xdg_surface(&surface, &qh, ());
    let top = xs.get_toplevel(&qh, ());
    top.set_title(format!("dndclient {mode}"));
    top.set_app_id(format!("org.aurora.test.dnd{mode}"));
    let device = manager.get_data_device(&seat, &qh, ());
    surface.commit();

    let mut app = App { surface, buffer, source_mode: mode == "source", reject, _seat: seat, manager, device: Some(device), items, serial_pointer_enter: 0, offer: None, source: None };
    loop { queue.blocking_dispatch(&mut app).expect("dispatch"); }
}
