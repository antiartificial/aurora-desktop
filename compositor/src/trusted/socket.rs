//! The privileged request channel: a Unix socket that is not the Wayland
//! socket. One request per connection, one JSON line each way.
//!
//! request:  {"v":1,"op":"approve","request_id":"..","app":"..","domain":"..","domain_identity":"domain:<name>:<id>","verb":"..",
//!            "effect":"destructive","grants":[{"path":"..","mode":"rw","source":"gesture"}],
//!            "args":<any json>,"timeout_ms":60000}
//! response: {"v":1,"request_id":"..","decision":"allow"|"deny","key":"a","reason":"key"|"timeout",
//!            "domain_verified":"true"|"false"|"unknown","domain_verify_detail":"..",
//!            "domain_source":"bus"|"claim","domain_identity":"<the identity displayed>","domain_mismatch":"true"|"false"}
//!           (source "bus": the domain came from an approval.preflight event busd stamped, see attest.rs)
//!           or {"error":".."} when the peer or the request is refused.
//!
//! Who may connect is decided on the connection, from the kernel's
//! SO_PEERCRED, before a byte is read. See `Allowlist` for what that checks.

use std::{
    cell::{Cell, RefCell},
    io::{ErrorKind, Read, Write},
    os::{fd::AsRawFd, unix::net::{UnixListener, UnixStream}},
    path::Path,
    rc::Rc,
    time::Duration,
};

use serde::Deserialize;
use smithay::reexports::calloop::{
    Interest,
    channel, LoopHandle, Mode, PostAction,
    generic::Generic,
    timer::{TimeoutAction, Timer},
};

use super::{Request, attest, draw::clean};
use crate::Aurora;

const MAX_LINE: usize = 64 * 1024;
const FIRST_LINE_WITHIN: Duration = Duration::from_secs(5);
const MAX_FIELD: usize = 2048;
const MAX_GRANTS: usize = 64;

/// Which peers may request an overlay.
#[derive(Debug, Clone)]
pub struct Allowlist {
    /// Peer euid must be one of these. Default: the compositor's own euid.
    pub uids: Vec<u32>,
    /// Peer is refused if any path component of its cgroup equals one of
    /// these. Default: `aurora-domains`, the parent of every domain cgroup
    /// the adapter makes (adapter/linux/internal/dom/cgroup.go).
    pub deny_components: Vec<String>,
    /// Peer is refused if its cgroup is at or below one of these paths.
    pub deny_prefixes: Vec<String>,
}

impl Default for Allowlist {
    fn default() -> Self {
        Allowlist {
            uids: vec![unsafe { libc::geteuid() }],
            deny_components: vec!["aurora-domains".to_string()],
            deny_prefixes: vec![],
        }
    }
}

impl Allowlist {
    /// Decide on a peer's credentials and its `/proc/<pid>/cgroup` text
    /// (`None` when it could not be read, which refuses: fail closed).
    pub fn check(&self, uid: u32, pid: i32, cgroup_file: Option<&str>) -> Result<(), String> {
        if !self.uids.contains(&uid) {
            return Err(format!("uid {uid} is not allowed to request approvals"));
        }
        let Some(text) = cgroup_file else {
            return Err(format!("cannot read the cgroup of pid {pid}, refusing"));
        };
        let Some(path) = text.lines().find_map(|l| l.strip_prefix("0::")) else {
            return Err(format!("pid {pid} has no cgroup v2 path, cannot show it is outside a domain, refusing"));
        };
        if path.split('/').any(|c| self.deny_components.iter().any(|d| d == c)) {
            return Err("peer is inside a domain cgroup".to_string());
        }
        if self.deny_prefixes.iter().any(|p| path == p || path.starts_with(&format!("{p}/"))) {
            return Err("peer is inside a denied cgroup".to_string());
        }
        Ok(())
    }
}

pub fn peer_cred(s: &UnixStream) -> std::io::Result<(u32, i32)> {
    let mut cred = libc::ucred { pid: 0, uid: 0, gid: 0 };
    let mut len = std::mem::size_of::<libc::ucred>() as libc::socklen_t;
    // SAFETY: cred and len are valid for the call; the fd is a live socket.
    let rc = unsafe {
        libc::getsockopt(s.as_raw_fd(), libc::SOL_SOCKET, libc::SO_PEERCRED, (&mut cred as *mut libc::ucred).cast(), &mut len)
    };
    if rc != 0 {
        return Err(std::io::Error::last_os_error());
    }
    Ok((cred.uid, cred.pid))
}

#[derive(Deserialize)]
struct WireGrant {
    path: String,
    #[serde(default)]
    mode: String,
    #[serde(default)]
    source: String,
}

#[derive(Deserialize)]
struct Wire {
    #[serde(default)]
    op: String,
    #[serde(default)]
    request_id: String,
    #[serde(default)]
    app: String,
    #[serde(default)]
    domain: String,
    #[serde(default)]
    domain_identity: String,
    #[serde(default)]
    verb: String,
    #[serde(default)]
    effect: String,
    #[serde(default)]
    grants: Vec<WireGrant>,
    #[serde(default)]
    args: serde_json::Value,
    timeout_ms: Option<u64>,
}

fn field(s: &str) -> Result<String, String> {
    if s.chars().count() > MAX_FIELD {
        return Err("a field is too long".into());
    }
    Ok(clean(s))
}

fn parse(line: &[u8]) -> Result<(Request, Duration), String> {
    let w: Wire = serde_json::from_slice(line).map_err(|e| format!("bad request: {e}"))?;
    // Over the bytes as they arrived, before any field is cleaned or truncated, so the digest
    // is of the request the sender actually wrote.
    let digest = crate::trusted::sign::hex(&crate::trusted::sign::sha256(line));
    if w.op != "approve" {
        return Err(format!("unknown op {:?}, want \"approve\"", clean(&w.op)));
    }
    if w.grants.len() > MAX_GRANTS {
        return Err("too many grants".into());
    }
    let grants = w
        .grants
        .iter()
        .map(|g| field(&format!("{} {} ({})", g.mode, g.path, g.source)))
        .collect::<Result<Vec<_>, _>>()?;
    let args = match &w.args {
        serde_json::Value::Null => String::new(),
        serde_json::Value::String(s) => s.clone(),
        v => v.to_string(),
    };
    let req = Request {
        request_id: field(&w.request_id)?,
        app: field(&w.app)?,
        domain: field(&w.domain)?,
        domain_identity: field(&w.domain_identity)?,
        verification: super::verify::Verification::unchecked(),
        verb: field(&w.verb)?,
        effect: field(&w.effect)?,
        grants,
        args: field(&args)?,
        request_sha256: digest,
    };
    Ok((req, Duration::from_millis(w.timeout_ms.unwrap_or(60_000).clamp(1_000, 600_000))))
}

fn refuse(mut s: &UnixStream, why: &str) {
    let _ = writeln!(s, "{}", serde_json::json!({"v": 1, "error": why}));
    let _ = s.shutdown(std::net::Shutdown::Both);
}

/// Bind the socket (0600, replacing only a stale one) and serve it on the loop.
pub fn listen(handle: &LoopHandle<'static, Aurora>, path: &Path) -> Result<(), Box<dyn std::error::Error>> {
    if UnixStream::connect(path).is_ok() {
        return Err(format!("{} is in use by a live process", path.display()).into());
    }
    let _ = std::fs::remove_file(path);
    let old = unsafe { libc::umask(0o177) };
    let l = UnixListener::bind(path);
    unsafe { libc::umask(old) };
    let l = l?;
    l.set_nonblocking(true)?;
    let h = handle.clone();
    handle.insert_source(Generic::new(l, Interest::READ, Mode::Level), move |_, l, state| {
        loop {
            match l.accept() {
                Ok((s, _)) => accept(&h, state, s),
                Err(e) if e.kind() == ErrorKind::WouldBlock => break,
                Err(e) => {
                    tracing::warn!("trusted: accept: {e}");
                    break;
                }
            }
        }
        Ok(PostAction::Continue)
    })?;
    Ok(())
}

fn accept(h: &LoopHandle<'static, Aurora>, state: &mut Aurora, s: UnixStream) {
    let (uid, pid) = match peer_cred(&s) {
        Ok(c) => c,
        Err(e) => return refuse(&s, &format!("no peer credentials: {e}")),
    };
    let cg = std::fs::read_to_string(format!("/proc/{pid}/cgroup")).ok();
    if let Err(why) = state.trusted.allow.check(uid, pid, cg.as_deref()) {
        tracing::warn!("trusted: REFUSED peer uid {uid} pid {pid}: {why}");
        return refuse(&s, &why);
    }
    if s.set_nonblocking(true).is_err() {
        return;
    }
    let Ok(read_half) = s.try_clone() else { return };
    // `sent`: the request line arrived and is being verified. `id`: it is queued.
    // `gone`: the peer hung up. All three live on the loop thread.
    let sent = Rc::new(Cell::new(false));
    let gone = Rc::new(Cell::new(false));
    let id: Rc<Cell<Option<u64>>> = Rc::new(Cell::new(None));
    let silent = sent.clone();
    let victim = s.try_clone().ok();
    let _ = h.insert_source(Timer::from_duration(FIRST_LINE_WITHIN), move |_, _, _| {
        if !silent.get() {
            if let Some(v) = &victim {
                let _ = v.shutdown(std::net::Shutdown::Both);
            }
        }
        TimeoutAction::Drop
    });
    let write_half = Rc::new(RefCell::new(Some(s)));
    // The bus check blocks (up to 1.5s), so it runs on a thread and comes back
    // here through a channel; the compositor loop never waits on busd.
    let (tx, rx) = channel::channel::<(Request, Duration)>();
    let (w2, id2, gone2) = (write_half.clone(), id.clone(), gone.clone());
    let _ = h.insert_source(rx, move |ev, _, state| {
        let channel::Event::Msg((req, timeout)) = ev else { return };
        let Some(w) = w2.borrow_mut().take() else { return };
        if gone2.get() {
            return;
        }
        tracing::info!("trusted: request from uid {uid} pid {pid}; domain_verified={} bus_attested={} mismatch={:?} shown={} ({})", req.verification.verdict.wire(), req.verification.bus_attested, req.verification.mismatch, req.domain_identity, req.verification.detail);
        id2.set(Some(state.trusted_enqueue(req, timeout, w)));
    });
    let mut buf: Vec<u8> = Vec::new();
    let _ = h.insert_source(Generic::new(read_half, Interest::READ, Mode::Level), move |_, r, state| {
        let mut chunk = [0u8; 4096];
        loop {
            match (&**r).read(&mut chunk) {
                Ok(0) => {
                    gone.set(true);
                    if let Some(i) = id.get() {
                        state.trusted_withdraw(i);
                    }
                    return Ok(PostAction::Remove);
                }
                Ok(n) if !sent.get() => {
                    buf.extend_from_slice(&chunk[..n]);
                    if buf.len() > MAX_LINE {
                        if let Some(w) = write_half.borrow_mut().take() { refuse(&w, "request too large"); }
                        return Ok(PostAction::Remove);
                    }
                    if let Some(nl) = buf.iter().position(|&b| b == b'\n') {
                        match parse(&buf[..nl]) {
                            Ok((mut req, timeout)) => {
                                sent.set(true);
                                let sock = state.trusted.bus_sock.clone();
                                let used = state.trusted.used.clone();
                                let tx = tx.clone();
                                std::thread::spawn(move || {
                                    attest::resolve(sock.as_deref(), &mut req, &used);
                                    let _ = tx.send((req, timeout));
                                });
                            }
                            Err(why) => {
                                if let Some(w) = write_half.borrow_mut().take() { refuse(&w, &why); }
                                return Ok(PostAction::Remove);
                            }
                        }
                    }
                }
                Ok(_) => {} // bytes after the request are ignored
                Err(e) if e.kind() == ErrorKind::WouldBlock => return Ok(PostAction::Continue),
                Err(_) => return Ok(PostAction::Remove),
            }
        }
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    const HOST: &str = "0::/user.slice/user-1000.slice/user@1000.service/app.slice/controllerd.service\n";
    const DOMAIN: &str = "0::/user.slice/user-1000.slice/user@1000.service/aurora-domains/alpha\n";

    fn al() -> Allowlist {
        Allowlist { uids: vec![1000], ..Default::default() }
    }

    #[test]
    fn allows_the_listed_uid_outside_a_domain() {
        assert!(al().check(1000, 5, Some(HOST)).is_ok());
    }

    #[test]
    fn refuses_other_uids_domain_cgroups_and_unreadable_cgroups() {
        assert!(al().check(1001, 5, Some(HOST)).is_err());
        assert!(al().check(1000, 5, Some(DOMAIN)).is_err());
        assert!(al().check(1000, 5, None).is_err());
        assert!(al().check(1000, 5, Some("1:name=x:/\n")).is_err());
        let mut a = al();
        a.deny_prefixes.push("/user.slice/user-1000.slice/user@1000.service/app.slice".into());
        assert!(a.check(1000, 5, Some(HOST)).is_err());
    }

    #[test]
    fn display_text_is_made_printable() {
        let (r, _) = parse(br#"{"op":"approve","app":"a\u001b[2Jb\n\u00e9","verb":"v","effect":"destructive","args":{"x":1}}"#).unwrap();
        // The escape and the newline are neutralised, so a request can neither
        // drive the terminal nor forge a row. They are now NAMED rather than both
        // becoming '?', because two different invisible characters that print the
        // same are what let two different paths read alike. The accented letter is
        // ordinary text and is kept: see draw::clean.
        assert_eq!(r.app, "a<U+001B>[2Jb<U+000A>\u{e9}");
        assert!(!r.app.contains('\u{1b}'), "a raw escape reached the drawn string");
        assert!(!r.app.contains('\n'), "a raw newline reached the drawn string");
        assert_eq!(r.args, r#"{"x":1}"#);
    }
}
