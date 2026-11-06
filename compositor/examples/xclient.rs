//! A bare X11 client for the XWayland tests: one solid-colour window, optionally override-redirect
//! (a menu or tooltip: no window manager sees the map, it appears exactly where it asks).
//! Usage: xclient --geometry X,Y,W,H --rgb R,G,B [--override-redirect]
//! Connects via DISPLAY, prints `xclient mapped` once the map request is flushed, then waits for the
//! connection to close.

use std::io::Write;

use x11rb::{
    connection::Connection,
    protocol::xproto::{ConnectionExt, CreateWindowAux, WindowClass},
    COPY_DEPTH_FROM_PARENT,
};

fn main() {
    let (mut geo, mut rgb, mut or) = ([0i32, 0, 200, 100], [200u32, 40, 200], false);
    let mut a = std::env::args().skip(1);
    while let Some(f) = a.next() {
        match f.as_str() {
            "--geometry" => { for (i, v) in a.next().unwrap().split(',').enumerate() { geo[i] = v.parse().unwrap(); } }
            "--rgb" => { for (i, v) in a.next().unwrap().split(',').enumerate() { rgb[i] = v.parse().unwrap(); } }
            "--override-redirect" => or = true,
            o => panic!("unknown argument {o}"),
        }
    }
    let (conn, screen) = x11rb::connect(None).expect("connect to DISPLAY");
    let root = conn.setup().roots[screen].root;
    let win = conn.generate_id().unwrap();
    let aux = CreateWindowAux::new().background_pixel((rgb[0] << 16) | (rgb[1] << 8) | rgb[2]).override_redirect(or as u32);
    conn.create_window(COPY_DEPTH_FROM_PARENT, win, root, geo[0] as i16, geo[1] as i16, geo[2] as u16, geo[3] as u16, 0, WindowClass::INPUT_OUTPUT, 0, &aux).unwrap();
    conn.map_window(win).unwrap();
    conn.flush().unwrap();
    println!("xclient mapped");
    let _ = std::io::stdout().flush();
    while conn.wait_for_event().is_ok() {}
}
