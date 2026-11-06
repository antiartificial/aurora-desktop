//! Test client for the trusted-path tests: one solid-colour xdg toplevel that
//! prints every keyboard event it receives (`enter`, `leave`, `key <code> <state>`)
//! to stdout, one per line, so a test can see exactly what a focused client sees.
//! It also prints `keymap <xkb_symbols line>` and `repeat <rate> <delay>` as the seat sends them,
//! `configure WxH` for each non-zero toplevel configure, and with `--maximize` asks to be
//! maximised and redraws at the size it is given.
//! Usage: testclient [--size WxH] [--rgb R,G,B] [--pattern solid|checker|fine|split|bars|glass] [--alpha A] [--maximize]
//!                   [--hidpi | --integer-scale] [--title T] [--app-id ID]
//!                   [--decor client|server] [--min-size WxH] [--max-size WxH] [--refuse-close] [--child WxH]
//! `--follow` redraws at the size a configure gives (as `--maximize` does, without maximising).
//! `xdg_toplevel.close` prints `close` and ends the client, unless `--refuse-close`. `--decor` binds
//! `zxdg_decoration_manager_v1` and asks for that mode (printing `decoration mode ...` for the answer);
//! without it the manager is never bound. `--child WxH` maps a second toplevel with `set_parent` on the
//! first (a dialog), titled `aurora-testclient-child`, and prints `child mapped`.
//! `--size` is the LOGICAL size. By default the client knows nothing about scale (a scale-1 buffer).
//! `--hidpi` is a modern client: it reads `wp_fractional_scale_v1`, draws a buffer of
//! round(logical * scale) pixels and sets `wp_viewport.set_destination(logical)`; without
//! those globals it falls back to `--integer-scale`. `--integer-scale` reads only
//! `wl_output.scale` and draws a buffer N times the logical size with `set_buffer_scale(N)`.
//! It prints `output_scale N`, `fractional_scale X` and `buffer WxH logical WxH scale S`.
//! (connects via WAYLAND_DISPLAY). `glass` is a translucent ARGB panel with rounded
//! corners (what the compositor's blur shows through); the others are opaque, with
//! sharp edges so blur is easy to see behind them.

use std::{io::Write, os::fd::AsFd};

use wayland_client::{
    Connection, Dispatch, QueueHandle, WEnum, delegate_noop,
    globals::{GlobalListContents, registry_queue_init},
    protocol::{wl_buffer::WlBuffer, wl_compositor::WlCompositor, wl_keyboard, wl_pointer::{self, WlPointer}, wl_registry::WlRegistry,
        wl_output::{self, WlOutput}, wl_seat::{self, WlSeat}, wl_shm::{self, WlShm}, wl_shm_pool::WlShmPool, wl_surface::WlSurface},
};
use wayland_protocols::wp::{
    fractional_scale::v1::client::{
        wp_fractional_scale_manager_v1::WpFractionalScaleManagerV1,
        wp_fractional_scale_v1::{self, WpFractionalScaleV1},
    },
    viewporter::client::{wp_viewport::WpViewport, wp_viewporter::WpViewporter},
};
use wayland_protocols::wp::text_input::zv3::client::{
    zwp_text_input_manager_v3::ZwpTextInputManagerV3,
    zwp_text_input_v3::{self, ZwpTextInputV3},
};
use wayland_protocols::xdg::shell::client::{
    xdg_surface::{self, XdgSurface}, xdg_toplevel::{self, XdgToplevel}, xdg_wm_base::{self, XdgWmBase},
};
use wayland_protocols::xdg::decoration::zv1::client::{
    zxdg_decoration_manager_v1::ZxdgDecorationManagerV1,
    zxdg_toplevel_decoration_v1::{self, ZxdgToplevelDecorationV1},
};

/// User data of the `--child` dialog's surfaces.
struct Child;

struct App {
    surface: WlSurface, buffers: Vec<WlBuffer>, configured: bool,
    shm: WlShm, pattern: String, rgb: [u8; 3], alpha: f32, size: (usize, usize), pending: Option<(usize, usize)>,
    /// `--hidpi` / `--integer-scale`
    hidpi: bool, integer_only: bool,
    /// `wl_output.scale`, and `wp_fractional_scale` in 120ths (0 = none received).
    int_scale: i32, frac120: u32,
    viewport: Option<WpViewport>,
    /// `--text-input`: a text field. It enables itself when it gets the keyboard and prints what the input method sends.
    ti: Option<ZwpTextInputV3>,
    comp: WlCompositor, wm: XdgWmBase, top: Option<XdgToplevel>,
    refuse_close: bool,
    /// `--child WxH`, and the child's surface once made.
    child: Option<(usize, usize)>, child_surface: Option<WlSurface>, child_done: bool,
}

delegate_noop!(App: ZxdgDecorationManagerV1);
impl Dispatch<ZxdgToplevelDecorationV1, ()> for App {
    fn event(_: &mut Self, _: &ZxdgToplevelDecorationV1, e: zxdg_toplevel_decoration_v1::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let zxdg_toplevel_decoration_v1::Event::Configure { mode } = e { say(&format!("decoration mode {}", match mode { WEnum::Value(m) => format!("{m:?}"), WEnum::Unknown(n) => format!("unknown({n})") })); }
    }
}
impl Dispatch<XdgSurface, Child> for App {
    fn event(s: &mut Self, x: &XdgSurface, e: xdg_surface::Event, _: &Child, _: &Connection, qh: &QueueHandle<Self>) {
        if let xdg_surface::Event::Configure { serial } = e {
            x.ack_configure(serial);
            if !s.child_done {
                s.child_done = true;
                let (w, h) = s.child.unwrap();
                let surf = s.child_surface.clone().unwrap();
                let buf = make_buffer(&s.shm, qh, "solid", (w, h), 1.0, [40, 200, 60], 1.0);
                surf.attach(Some(&buf), 0, 0);
                surf.commit();
                s.buffers.push(buf);
                say("child mapped");
            }
        }
    }
}
impl Dispatch<XdgToplevel, Child> for App {
    fn event(_: &mut Self, _: &XdgToplevel, e: xdg_toplevel::Event, _: &Child, _: &Connection, _: &QueueHandle<Self>) {
        if let xdg_toplevel::Event::Close = e { say("child close"); }
    }
}

impl App {
    /// Print one `ti ...` line, so a test sees the text-input traffic in order.
    fn ti_say(&self, what: &str) { say(&format!("ti {what}")); }
}

impl Dispatch<ZwpTextInputV3, ()> for App {
    fn event(s: &mut Self, ti: &ZwpTextInputV3, e: zwp_text_input_v3::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        use zwp_text_input_v3::Event::*;
        match e {
            Enter { .. } => {
                s.ti_say("enter");
                ti.enable();
                ti.set_surrounding_text("".into(), 0, 0);
                ti.set_cursor_rectangle(10, 20, 2, 16);
                ti.commit();
            }
            Leave { .. } => { s.ti_say("leave"); ti.disable(); ti.commit(); }
            PreeditString { text, cursor_begin, cursor_end } => s.ti_say(&format!("preedit {:?} {cursor_begin} {cursor_end}", text.unwrap_or_default())),
            CommitString { text } => s.ti_say(&format!("commit {:?}", text.unwrap_or_default())),
            DeleteSurroundingText { before_length, after_length } => s.ti_say(&format!("delete {before_length} {after_length}")),
            Done { serial } => s.ti_say(&format!("done {serial}")),
            _ => {}
        }
    }
}
delegate_noop!(App: ZwpTextInputManagerV3);

impl App {
    /// Draw the logical `self.size` for the scale this client knows about, attach and commit.
    fn present(&mut self, qh: &QueueHandle<App>) {
        let (lw, lh) = self.size;
        let (k, how) = if self.hidpi && !self.integer_only && self.frac120 != 0 && self.viewport.is_some() {
            (self.frac120 as f64 / 120.0, "fractional+viewport")
        } else if self.hidpi || self.integer_only {
            (self.int_scale.max(1) as f64, "integer")
        } else {
            (1.0, "none")
        };
        let (bw, bh) = ((lw as f64 * k).round() as usize, (lh as f64 * k).round() as usize);
        let buf = make_buffer(&self.shm, qh, &self.pattern, (bw, bh), k as f32, self.rgb, self.alpha);
        match how {
            "fractional+viewport" => self.viewport.as_ref().unwrap().set_destination(lw as i32, lh as i32),
            "integer" => self.surface.set_buffer_scale(k as i32),
            _ => {}
        }
        say(&format!("buffer {bw}x{bh} logical {lw}x{lh} scale {k} via {how}"));
        self.surface.attach(Some(&buf), 0, 0);
        self.surface.commit();
        self.buffers.push(buf);
    }
}

impl Dispatch<WlOutput, ()> for App {
    fn event(s: &mut Self, _: &WlOutput, e: wl_output::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let wl_output::Event::Scale { factor } = e { s.int_scale = factor; say(&format!("output_scale {factor}")); }
    }
}
impl Dispatch<WpFractionalScaleV1, ()> for App {
    fn event(s: &mut Self, _: &WpFractionalScaleV1, e: wp_fractional_scale_v1::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let wp_fractional_scale_v1::Event::PreferredScale { scale } = e { s.frac120 = scale; say(&format!("fractional_scale {} ({}/120)", scale as f64 / 120.0, scale)); }
    }
}
delegate_noop!(App: WpFractionalScaleManagerV1);
delegate_noop!(App: WpViewporter);
delegate_noop!(App: WpViewport);

/// `k` is buffer pixels per logical pixel.
fn make_buffer(shm: &WlShm, qh: &QueueHandle<App>, pattern: &str, (w, h): (usize, usize), k: f32, rgb: [u8; 3], alpha: f32) -> WlBuffer {
    let mut file = tempfile::tempfile().unwrap();
    file.write_all(&pixels(pattern, w, h, k, rgb, alpha)).unwrap();
    let format = if pattern == "glass" { wl_shm::Format::Argb8888 } else { wl_shm::Format::Xrgb8888 };
    let pool = shm.create_pool(file.as_fd(), (w * h * 4) as i32, qh, ());
    pool.create_buffer(0, w as i32, h as i32, (w * 4) as i32, format, qh, ())
}

fn say(s: &str) { println!("{s}"); let _ = std::io::stdout().flush(); }

impl Dispatch<XdgWmBase, ()> for App {
    fn event(_: &mut Self, b: &XdgWmBase, e: xdg_wm_base::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let xdg_wm_base::Event::Ping { serial } = e { b.pong(serial); }
    }
}
impl Dispatch<XdgSurface, ()> for App {
    fn event(s: &mut Self, x: &XdgSurface, e: xdg_surface::Event, _: &(), _: &Connection, qh: &QueueHandle<Self>) {
        if let xdg_surface::Event::Configure { serial } = e {
            x.ack_configure(serial);
            let follow = std::env::args().any(|a| a == "--follow");
            if let Some(sz) = s.pending.take().filter(|sz| *sz != s.size) {
                s.size = sz;
                s.present(qh);
            } else if follow && s.configured {
                // A client that follows configures commits after every ack, as the protocol asks: the
                // compositor's resize finishes on the commit that answers the last configure.
                s.present(qh);
            }
            if !s.configured {
                s.configured = true;
                s.present(qh);
                say("mapped");
                if let (Some((_, _)), Some(top)) = (s.child, s.top.clone()) {
                    let surf = s.comp.create_surface(qh, ());
                    let xs = s.wm.get_xdg_surface(&surf, qh, Child);
                    let ctop = xs.get_toplevel(qh, Child);
                    ctop.set_title("aurora-testclient-child".into());
                    ctop.set_parent(Some(&top));
                    surf.commit();
                    s.child_surface = Some(surf);
                }
            }
        }
    }
}
impl Dispatch<WlSeat, ()> for App {
    fn event(_: &mut Self, seat: &WlSeat, e: wl_seat::Event, _: &(), _: &Connection, qh: &QueueHandle<Self>) {
        if let wl_seat::Event::Capabilities { capabilities: WEnum::Value(c) } = e {
            if c.contains(wl_seat::Capability::Keyboard) { seat.get_keyboard(qh, ()); }
            if c.contains(wl_seat::Capability::Pointer) { seat.get_pointer(qh, ()); }
        }
    }
}
impl Dispatch<WlPointer, ()> for App {
    fn event(_: &mut Self, _: &WlPointer, e: wl_pointer::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        match e {
            wl_pointer::Event::Enter { surface_x, surface_y, .. } => say(&format!("pointer_enter {surface_x} {surface_y}")),
            wl_pointer::Event::Motion { surface_x, surface_y, .. } => say(&format!("pointer_motion {surface_x} {surface_y}")),
            wl_pointer::Event::Leave { .. } => say("pointer_leave"),
            _ => {}
        }
    }
}
impl Dispatch<wl_keyboard::WlKeyboard, ()> for App {
    fn event(_: &mut Self, _: &wl_keyboard::WlKeyboard, e: wl_keyboard::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        use std::os::unix::fs::FileExt;
        match e {
            wl_keyboard::Event::Keymap { fd, size, .. } => {
                let mut buf = vec![0u8; size as usize];
                let _ = std::fs::File::from(fd).read_at(&mut buf, 0);
                let text = String::from_utf8_lossy(&buf);
                let tag = |k: &str| text.find(k).map(|i| text[i + k.len()..].split('"').next().unwrap_or("").to_string()).unwrap_or_else(|| "?".into());
                let sym = format!("symbols={} keycodes={}", tag("xkb_symbols \""), tag("xkb_keycodes \""));
                say(&format!("keymap {sym}"));
            }
            wl_keyboard::Event::RepeatInfo { rate, delay } => say(&format!("repeat {rate} {delay}")),
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
impl Dispatch<XdgToplevel, ()> for App {
    fn event(s: &mut Self, _: &XdgToplevel, e: xdg_toplevel::Event, _: &(), _: &Connection, _: &QueueHandle<Self>) {
        if let xdg_toplevel::Event::Close = e {
            say("close");
            if !s.refuse_close { std::process::exit(0); }
        }
        if let xdg_toplevel::Event::Configure { width, height, .. } = e {
            if width > 0 && height > 0 {
                say(&format!("configure {width}x{height}"));
                if s.pattern != "glass" && s.pending_allowed() { s.pending = Some((width as usize, height as usize)); }
            }
        }
    }
}
impl App { fn pending_allowed(&self) -> bool { std::env::args().any(|a| a == "--maximize" || a == "--follow") } }

fn main() {
    let (mut w, mut h, mut rgb) = (1000usize, 700usize, [200u8, 40, 200]);
    let (mut pattern, mut alpha) = ("solid".to_string(), 0.14f32);
    let (mut maximize, mut hidpi, mut integer_only, mut text_input) = (false, false, false, false);
    let (mut title, mut app_id) = ("aurora-testclient".to_string(), None::<String>);
    let (mut decor, mut min_size, mut max_size, mut refuse_close, mut child) = (None::<String>, None::<(i32, i32)>, None::<(i32, i32)>, false, None::<(usize, usize)>);
    let wh = |v: String| { let (x, y) = v.split_once('x').unwrap(); (x.parse().unwrap(), y.parse().unwrap()) };
    let mut a = std::env::args().skip(1);
    while let Some(f) = a.next() {
        match f.as_str() {
            "--size" => { let v = a.next().unwrap(); let (x, y) = v.split_once('x').unwrap(); w = x.parse().unwrap(); h = y.parse().unwrap(); }
            "--rgb" => { let v: Vec<u8> = a.next().unwrap().split(',').map(|n| n.parse().unwrap()).collect(); rgb = [v[0], v[1], v[2]]; }
            "--pattern" => pattern = a.next().unwrap(),
            "--alpha" => alpha = a.next().unwrap().parse().unwrap(),
            "--title" => title = a.next().unwrap(),
            "--app-id" => app_id = a.next(),
            "--decor" => decor = a.next(),
            "--min-size" => { let (x, y) = wh(a.next().unwrap()); min_size = Some((x as i32, y as i32)); }
            "--max-size" => { let (x, y) = wh(a.next().unwrap()); max_size = Some((x as i32, y as i32)); }
            "--refuse-close" => refuse_close = true,
            "--follow" => {}
            "--child" => child = Some(wh(a.next().unwrap())),
            "--maximize" => maximize = true,
            "--hidpi" => hidpi = true,
            "--integer-scale" => integer_only = true,
            "--text-input" => text_input = true,
            o => panic!("unknown argument {o}"),
        }
    }
    let conn = Connection::connect_to_env().expect("connect");
    let (globals, mut q) = registry_queue_init::<App>(&conn).unwrap();
    let qh = q.handle();
    let comp: WlCompositor = globals.bind(&qh, 1..=4, ()).unwrap();
    let shm: WlShm = globals.bind(&qh, 1..=1, ()).unwrap();
    let wm: XdgWmBase = globals.bind(&qh, 1..=1, ()).unwrap();
    let seat: WlSeat = globals.bind(&qh, 1..=5, ()).unwrap();

    let surface = comp.create_surface(&qh, ());
    let mut app = App { surface: surface.clone(), buffers: vec![], configured: false, shm, pattern, rgb, alpha, size: (w, h), pending: None, hidpi, integer_only, int_scale: 1, frac120: 0, viewport: None, ti: None,
        comp: comp.clone(), wm: wm.clone(), top: None, refuse_close, child, child_surface: None, child_done: false };
    if hidpi || integer_only {
        let _out: WlOutput = globals.bind(&qh, 2..=4, ()).unwrap();
    }
    if hidpi {
        if let Ok(m) = globals.bind::<WpFractionalScaleManagerV1, _, _>(&qh, 1..=1, ()) { m.get_fractional_scale(&surface, &qh, ()); }
        if let Ok(v) = globals.bind::<WpViewporter, _, _>(&qh, 1..=1, ()) { app.viewport = Some(v.get_viewport(&surface, &qh, ())); }
    }
    if text_input {
        let m: ZwpTextInputManagerV3 = globals.bind(&qh, 1..=1, ()).expect("zwp_text_input_manager_v3");
        app.ti = Some(m.get_text_input(&seat, &qh, ()));
    }
    // Learn the scale before the first buffer: output and fractional-scale events come on bind.
    q.roundtrip(&mut app).expect("roundtrip");
    let xs = wm.get_xdg_surface(&surface, &qh, ());
    let top = xs.get_toplevel(&qh, ());
    top.set_title(title);
    if let Some(id) = app_id { top.set_app_id(id); }
    if let Some((x, y)) = min_size { top.set_min_size(x, y); }
    if let Some((x, y)) = max_size { top.set_max_size(x, y); }
    if let Some(mode) = decor {
        let m: ZxdgDecorationManagerV1 = globals.bind(&qh, 1..=1, ()).expect("zxdg_decoration_manager_v1");
        let d = m.get_toplevel_decoration(&top, &qh, ());
        d.set_mode(if mode == "client" { zxdg_toplevel_decoration_v1::Mode::ClientSide } else { zxdg_toplevel_decoration_v1::Mode::ServerSide });
    }
    app.top = Some(top.clone());
    if maximize { top.set_maximized(); }
    surface.commit();
    loop { q.blocking_dispatch(&mut app).expect("dispatch"); }
}

/// BGRA bytes (premultiplied for `glass`).
/// `k` buffer pixels per logical pixel: patterns are laid out in logical units.
fn pixels(pattern: &str, w: usize, h: usize, k: f32, rgb: [u8; 3], alpha: f32) -> Vec<u8> {
    let mut out = Vec::with_capacity(w * h * 4);
    let r = 12.0f32 * k; // corner radius, matches the theme's glass.corner_radius
    for y in 0..h {
        for x in 0..w {
            let (lx, ly) = ((x as f32 / k) as usize, (y as f32 / k) as usize);
            let c: [u8; 4] = match pattern {
                "checker" => { let v = if (lx / 40 + ly / 40) % 2 == 0 { 0 } else { 255 }; [v, v, v, 255] }
                // 2x2-pixel black/white checker in BUFFER pixels: the highest frequency a blur can be asked to hide.
                "fine" => { let v = if (x / 2 + y / 2) % 2 == 0 { 0 } else { 255 }; [v, v, v, 255] }
                "split" => { let v = if x < w / 2 { 0 } else { 255 }; [v, v, v, 255] }
                "bars" => { let b: [[u8; 3]; 4] = [[230, 30, 30], [30, 200, 60], [40, 70, 240], [250, 220, 20]]; let c = b[(lx / 50) % 4]; [c[2], c[1], c[0], 255] }
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
