//! TEST AFFORDANCE: a stand-in for winit's window, so the drag bridge can be tested without a real
//! host.
//!
//! In production the bridge listens on winit's connection and the window is winit's. Tests have no
//! winit: the "host" is a second headless Aurora. `--host-dnd-display NAME` connects to that host,
//! opens one plain xdg toplevel there (what winit's window is to a real host) and then attaches the
//! bridge to that very connection with `Backend::from_foreign_display`, exactly as production does
//! with winit's. Nothing here runs without the flag, and the flag only ever points at a socket the
//! caller names.

use std::os::fd::AsFd;

use wayland_client::{
    Connection, Dispatch, EventQueue, QueueHandle, delegate_noop,
    globals::{GlobalListContents, registry_queue_init},
    protocol::{
        wl_buffer::WlBuffer, wl_compositor::WlCompositor, wl_registry::WlRegistry, wl_shm::{self, WlShm},
        wl_shm_pool::WlShmPool, wl_surface::WlSurface,
    },
};
use wayland_protocols::xdg::shell::client::{
    xdg_surface::{self, XdgSurface},
    xdg_toplevel::XdgToplevel,
    xdg_wm_base::{self, XdgWmBase},
};

pub const SIZE: (i32, i32) = (640, 400);

pub struct StandIn {
    pub conn: Connection,
    queue: EventQueue<Win>,
    win: Win,
    _keep: (XdgToplevel, XdgSurface, WlSurface),
}

struct Win {
    surface: WlSurface,
    buffer: WlBuffer,
}

pub fn open(conn: Connection) -> Result<StandIn, String> {
    let (globals, mut queue) = registry_queue_init::<Win>(&conn).map_err(|e| e.to_string())?;
    let qh = queue.handle();
    let compositor: WlCompositor = globals.bind(&qh, 1..=4, ()).map_err(|e| e.to_string())?;
    let shm: WlShm = globals.bind(&qh, 1..=1, ()).map_err(|e| e.to_string())?;
    let wm: XdgWmBase = globals.bind(&qh, 1..=1, ()).map_err(|e| e.to_string())?;

    let (w, h) = SIZE;
    let len = (w * h * 4) as usize;
    // SAFETY: memfd_create with a static name; the fd is wrapped at once.
    let fd = unsafe { libc::memfd_create(c"aurora-dnd-standin".as_ptr(), libc::MFD_CLOEXEC) };
    if fd < 0 {
        return Err(std::io::Error::last_os_error().to_string());
    }
    let file = unsafe { <std::fs::File as std::os::fd::FromRawFd>::from_raw_fd(fd) };
    file.set_len(len as u64).map_err(|e| e.to_string())?;
    std::io::Write::write_all(&mut &file, &vec![0x58u8; len]).map_err(|e| e.to_string())?;
    let pool = shm.create_pool(file.as_fd(), len as i32, &qh, ());
    let buffer = pool.create_buffer(0, w, h, w * 4, wl_shm::Format::Xrgb8888, &qh, ());

    let surface = compositor.create_surface(&qh, ());
    let xs = wm.get_xdg_surface(&surface, &qh, ());
    let top = xs.get_toplevel(&qh, ());
    top.set_title("aurora host drop stand-in".into());
    top.set_app_id("org.aurora.test.hostdnd".into());
    surface.commit();
    let mut win = Win { surface: surface.clone(), buffer };
    queue.roundtrip(&mut win).map_err(|e| e.to_string())?;
    Ok(StandIn { conn, queue, win, _keep: (top, xs, surface) })
}

impl StandIn {
    /// Keep the stand-in window alive and acknowledging configures. Never blocks.
    pub fn dispatch(&mut self) {
        let _ = self.queue.dispatch_pending(&mut self.win);
        if let Some(guard) = self.queue.prepare_read() {
            let _ = guard.read();
        }
        let _ = self.queue.dispatch_pending(&mut self.win);
        let _ = self.conn.flush();
    }
}

impl Dispatch<XdgWmBase, ()> for Win {
    fn event(_: &mut Self, b: &XdgWmBase, e: xdg_wm_base::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let xdg_wm_base::Event::Ping { serial } = e {
            b.pong(serial);
        }
    }
}
impl Dispatch<XdgSurface, ()> for Win {
    fn event(s: &mut Self, x: &XdgSurface, e: xdg_surface::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let xdg_surface::Event::Configure { serial } = e {
            x.ack_configure(serial);
            s.surface.attach(Some(&s.buffer), 0, 0);
            s.surface.commit();
        }
    }
}
impl Dispatch<WlRegistry, GlobalListContents> for Win {
    fn event(_: &mut Self, _: &WlRegistry, _: wayland_client::protocol::wl_registry::Event, _: &GlobalListContents, _: &Connection, _: &QueueHandle<Self>) {}
}
delegate_noop!(Win: WlCompositor);
delegate_noop!(Win: ignore WlShm);
delegate_noop!(Win: WlShmPool);
delegate_noop!(Win: ignore WlBuffer);
delegate_noop!(Win: ignore WlSurface);
delegate_noop!(Win: ignore XdgToplevel);
