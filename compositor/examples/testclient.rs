//! Test client for the trusted-path tests: one solid-colour xdg toplevel that
//! prints every keyboard event it receives (`enter`, `leave`, `key <code> <state>`)
//! to stdout, one per line, so a test can see exactly what a focused client sees.
//! Usage: testclient [--size WxH] [--rgb R,G,B] [--pattern solid|checker|split|bars|glass] [--alpha A]
//! (connects via WAYLAND_DISPLAY). `glass` is a translucent ARGB panel with rounded
//! corners (what the compositor's blur shows through); the others are opaque, with
//! sharp edges so blur is easy to see behind them.

use std::{io::Write, os::fd::AsFd};

use wayland_client::{
    Connection, Dispatch, QueueHandle, WEnum, delegate_noop,
    globals::{GlobalListContents, registry_queue_init},
    protocol::{wl_buffer::WlBuffer, wl_compositor::WlCompositor, wl_keyboard, wl_registry::WlRegistry,
        wl_seat::{self, WlSeat}, wl_shm::{self, WlShm}, wl_shm_pool::WlShmPool, wl_surface::WlSurface},
};
use wayland_protocols::xdg::shell::client::{
    xdg_surface::{self, XdgSurface}, xdg_toplevel::XdgToplevel, xdg_wm_base::{self, XdgWmBase},
};

struct App { surface: WlSurface, buffer: WlBuffer, configured: bool }

fn say(s: &str) { println!("{s}"); let _ = std::io::stdout().flush(); }

impl Dispatch<XdgWmBase, ()> for App {
    fn event(_: &mut Self, b: &XdgWmBase, e: xdg_wm_base::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let xdg_wm_base::Event::Ping { serial } = e { b.pong(serial); }
    }
}
impl Dispatch<XdgSurface, ()> for App {
    fn event(s: &mut Self, x: &XdgSurface, e: xdg_surface::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let xdg_surface::Event::Configure { serial } = e {
            x.ack_configure(serial);
            if !s.configured {
                s.configured = true;
                s.surface.attach(Some(&s.buffer), 0, 0);
                s.surface.commit();
                say("mapped");
            }
        }
    }
}
impl Dispatch<WlSeat, ()> for App {
    fn event(_: &mut Self, seat: &WlSeat, e: wl_seat::Event, _: &(), _: &Connection, qh: &QueueHandle<Self>) {
        if let wl_seat::Event::Capabilities { capabilities: WEnum::Value(c) } = e {
            if c.contains(wl_seat::Capability::Keyboard) { seat.get_keyboard(qh, ()); }
        }
    }
}
impl Dispatch<wl_keyboard::WlKeyboard, ()> for App {
    fn event(_: &mut Self, _: &wl_keyboard::WlKeyboard, e: wl_keyboard::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        match e {
            wl_keyboard::Event::Enter { .. } => say("enter"),
            wl_keyboard::Event::Leave { .. } => say("leave"),
            wl_keyboard::Event::Key { key, state, .. } => say(&format!("key {key} {}", match state { WEnum::Value(wl_keyboard::KeyState::Pressed) => "Pressed", WEnum::Value(wl_keyboard::KeyState::Released) => "Released", _ => "Other" })),
            _ => {}
        }
    }
}
impl Dispatch<WlRegistry, GlobalListContents> for App {
    fn event(_: &mut Self, _: &WlRegistry, _: wayland_client::protocol::wl_registry::Event, _: &GlobalListContents, _: &Connection, _: &QueueHandle<Self>) {}
}
delegate_noop!(App: WlCompositor);
delegate_noop!(App: ignore WlShm);
delegate_noop!(App: WlShmPool);
delegate_noop!(App: ignore WlBuffer);
delegate_noop!(App: ignore WlSurface);
delegate_noop!(App: ignore XdgToplevel);

fn main() {
    let (mut w, mut h, mut rgb) = (1000usize, 700usize, [200u8, 40, 200]);
    let (mut pattern, mut alpha) = ("solid".to_string(), 0.14f32);
    let mut a = std::env::args().skip(1);
    while let Some(f) = a.next() {
        match f.as_str() {
            "--size" => { let v = a.next().unwrap(); let (x, y) = v.split_once('x').unwrap(); w = x.parse().unwrap(); h = y.parse().unwrap(); }
            "--rgb" => { let v: Vec<u8> = a.next().unwrap().split(',').map(|n| n.parse().unwrap()).collect(); rgb = [v[0], v[1], v[2]]; }
            "--pattern" => pattern = a.next().unwrap(),
            "--alpha" => alpha = a.next().unwrap().parse().unwrap(),
            o => panic!("unknown argument {o}"),
        }
    }
    let conn = Connection::connect_to_env().expect("connect");
    let (globals, mut q) = registry_queue_init::<App>(&conn).unwrap();
    let qh = q.handle();
    let comp: WlCompositor = globals.bind(&qh, 1..=4, ()).unwrap();
    let shm: WlShm = globals.bind(&qh, 1..=1, ()).unwrap();
    let wm: XdgWmBase = globals.bind(&qh, 1..=1, ()).unwrap();
    let _seat: WlSeat = globals.bind(&qh, 1..=5, ()).unwrap();

    let mut file = tempfile::tempfile().unwrap();
    let px = pixels(&pattern, w, h, rgb, alpha);
    let format = if pattern == "glass" { wl_shm::Format::Argb8888 } else { wl_shm::Format::Xrgb8888 };
    file.write_all(&px).unwrap();
    let pool = shm.create_pool(file.as_fd(), (w * h * 4) as i32, &qh, ());
    let buffer = pool.create_buffer(0, w as i32, h as i32, (w * 4) as i32, format, &qh, ());
    let surface = comp.create_surface(&qh, ());
    let xs = wm.get_xdg_surface(&surface, &qh, ());
    let top = xs.get_toplevel(&qh, ());
    top.set_title("aurora-testclient".into());
    surface.commit();
    let mut app = App { surface, buffer, configured: false };
    loop { q.blocking_dispatch(&mut app).expect("dispatch"); }
}

/// BGRA bytes (premultiplied for `glass`).
fn pixels(pattern: &str, w: usize, h: usize, rgb: [u8; 3], alpha: f32) -> Vec<u8> {
    let mut out = Vec::with_capacity(w * h * 4);
    let r = 12.0f32; // corner radius, matches the theme's glass.corner_radius
    for y in 0..h {
        for x in 0..w {
            let c: [u8; 4] = match pattern {
                "checker" => { let v = if (x / 40 + y / 40) % 2 == 0 { 0 } else { 255 }; [v, v, v, 255] }
                "split" => { let v = if x < w / 2 { 0 } else { 255 }; [v, v, v, 255] }
                "bars" => { let b: [[u8; 3]; 4] = [[230, 30, 30], [30, 200, 60], [40, 70, 240], [250, 220, 20]]; let c = b[(x / 50) % 4]; [c[2], c[1], c[0], 255] }
                "glass" => {
                    // rounded-corner coverage
                    let (dx, dy) = ((x as f32 + 0.5 - w as f32 / 2.0).abs() - (w as f32 / 2.0 - r), (y as f32 + 0.5 - h as f32 / 2.0).abs() - (h as f32 / 2.0 - r));
                    let d = dx.max(0.0).hypot(dy.max(0.0)) + dx.max(dy).min(0.0) - r;
                    let cov = (0.5 - d).clamp(0.0, 1.0);
                    let a = if y < 28 { alpha + 0.18 } else { alpha } * cov;
                    let p = |v: u8| (v as f32 * a).round() as u8;
                    [p(rgb[2]), p(rgb[1]), p(rgb[0]), (a * 255.0).round() as u8]
                }
                _ => [rgb[2], rgb[1], rgb[0], 255],
            };
            out.extend_from_slice(&c);
        }
    }
    out
}
