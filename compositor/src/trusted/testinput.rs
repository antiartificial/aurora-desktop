//! TEST AFFORDANCE: inject keyboard events into a headless compositor.
//!
//! `--test-input PATH` opens a Unix socket (0600, same euid only) taking one
//! command per line: `key <name>` (press then release), `down <name>`,
//! `up <name>`. Events enter through `Aurora::keyboard_input`, the same function
//! real keyboards use, so an injected key meets the same overlay routing.
//!
//! Why it is not a way to approve a prompt on a real machine: it exists only
//! with `--headless`, and the headless backend opens no input device at all, so
//! there is no real keyboard to impersonate. `main` refuses `--test-input`
//! with any other backend. It is NOT safe to reuse with a backend that has real
//! input (libinput, DRM): anything of the same uid could then press `a` on the
//! user's behalf. Do not add that combination without removing this.

use std::{
    io::{BufRead, BufReader, ErrorKind, Write},
    os::unix::net::{UnixListener, UnixStream},
    path::Path,
};

use smithay::{
    backend::input::{InputTime, KeyState},
    input::keyboard::Keycode,
    reexports::calloop::{Interest, LoopHandle, Mode, PostAction, generic::Generic},
};

use crate::Aurora;

/// US-layout evdev code for a key name; xkb keycodes are evdev + 8.
fn keycode(name: &str) -> Option<Keycode> {
    const ROW: [(&str, u32); 25] = [
        ("q", 16), ("w", 17), ("e", 18), ("r", 19), ("t", 20), ("y", 21), ("u", 22), ("i", 23), ("o", 24), ("p", 25),
        ("a", 30), ("s", 31), ("d", 32), ("f", 33), ("g", 34), ("h", 35), ("j", 36), ("k", 37), ("l", 38),
        ("z", 44), ("x", 45), ("c", 46), ("v", 47), ("b", 48), ("n", 49),
    ];
    let evdev = match name {
        "Escape" => 1,
        "Tab" => 15,
        "alt" => 56,
        "shift" => 42,
        "Up" => 103,
        "Left" => 105,
        "Right" => 106,
        "Down" => 108,
        "Return" => 28,
        "space" => 57,
        "m" => 50,
        n => ROW.iter().find(|(k, _)| *k == n)?.1,
    };
    Some(Keycode::new(evdev + 8))
}

pub fn listen(handle: &LoopHandle<'static, Aurora>, path: &Path) -> Result<(), Box<dyn std::error::Error>> {
    let _ = std::fs::remove_file(path);
    let old = unsafe { libc::umask(0o177) };
    let l = UnixListener::bind(path);
    unsafe { libc::umask(old) };
    let l = l?;
    l.set_nonblocking(true)?;
    let h = handle.clone();
    handle.insert_source(Generic::new(l, Interest::READ, Mode::Level), move |_, l, _| {
        loop {
            match l.accept() {
                Ok((s, _)) => serve(&h, s),
                Err(e) if e.kind() == ErrorKind::WouldBlock => break,
                Err(_) => break,
            }
        }
        Ok(PostAction::Continue)
    })?;
    Ok(())
}

fn same_uid(s: &UnixStream) -> bool {
    use std::os::fd::AsRawFd;
    let mut c = libc::ucred { pid: 0, uid: u32::MAX, gid: 0 };
    let mut len = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
    let rc = unsafe { libc::getsockopt(s.as_raw_fd(), libc::SOL_SOCKET, libc::SO_PEERCRED, (&mut c as *mut libc::ucred).cast(), &mut len) };
    rc == 0 && c.uid == unsafe { libc::geteuid() }
}

fn serve(h: &LoopHandle<'static, Aurora>, s: UnixStream) {
    if !same_uid(&s) || s.set_nonblocking(true).is_err() {
        return;
    }
    let mut r = BufReader::new(s);
    let mut line = String::new();
    let _ = h.insert_source(Generic::new(r.get_ref().try_clone().unwrap(), Interest::READ, Mode::Level), move |_, _, state| {
        loop {
            // `line` outlives a WouldBlock: a command that arrives in two reads
            // ("key " then "Tab") must not lose its first half.
            match r.read_line(&mut line) {
                Ok(0) => return Ok(PostAction::Remove),
                Ok(_) if !line.ends_with('\n') => continue,
                Ok(_) => {
                    let ans = run(state, line.trim());
                    line.clear();
                    let _ = writeln!(r.get_mut(), "{ans}");
                }
                Err(e) if e.kind() == ErrorKind::WouldBlock => return Ok(PostAction::Continue),
                Err(_) => return Ok(PostAction::Remove),
            }
        }
    });
}

fn run(state: &mut Aurora, cmd: &str) -> String {
    let t = InputTime::now();
    let mut it = cmd.split_whitespace();
    if cmd.starts_with("move ") {
        // `move N X Y`: place the Nth window in stacking order (0 = bottom); test scenes only.
        let v: Vec<i32> = it.skip(1).filter_map(|t| t.parse().ok()).collect();
        let w = v.first().and_then(|&n| state.space.elements().nth(n as usize).cloned());
        return match (w, v.as_slice()) {
            (Some(w), [_, x, y]) => { state.space.relocate_element(&w, (*x, *y)); "ok".into() }
            _ => "err bad move".into(),
        };
    }
    if cmd.starts_with("pointer ") {
        // `pointer X Y`: move the pointer (LOGICAL coordinates, as clients see them), through the same function winit uses.
        let v: Vec<f64> = it.skip(1).filter_map(|t| t.parse().ok()).collect();
        return match v.as_slice() {
            [x, y] if !state.overlay_up() => { state.pointer_moved((*x, *y).into(), t); "ok".into() }
            _ => "err bad pointer".into(),
        };
    }
    if cmd == "click" {
        if state.overlay_up() { return "err overlay up".into(); }
        state.pointer_button(0x110, smithay::backend::input::ButtonState::Pressed, t);
        state.pointer_button(0x110, smithay::backend::input::ButtonState::Released, t);
        return "ok".into();
    }
    let (verb, name) = (it.next().unwrap_or(""), it.next().unwrap_or(""));
    let Some(code) = keycode(name) else { return format!("err unknown key {name:?}") };
    match verb {
        "key" => {
            state.keyboard_input(code, KeyState::Pressed, t);
            state.keyboard_input(code, KeyState::Released, t);
        }
        "down" => state.keyboard_input(code, KeyState::Pressed, t),
        "up" => state.keyboard_input(code, KeyState::Released, t),
        "keysym" => {
            // Press and release, and answer with the keysym the live keymap produced.
            state.last_keysym = None;
            state.keyboard_input(code, KeyState::Pressed, t);
            state.keyboard_input(code, KeyState::Released, t);
            return format!("ok {}", state.last_keysym.take().unwrap_or_else(|| "none".into()));
        }
        _ => return format!("err unknown command {verb:?}"),
    }
    "ok".to_string()
}
