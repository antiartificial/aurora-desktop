//! What a dropped file list is allowed to be, before anyone is asked to grant anything.
//!
//! This is the one place Aurora reads drag bytes (decision 0020, step 2), and it reads a path list,
//! not content. Everything here is hostile-input handling: the list comes from whatever host
//! application started the drag. The rules:
//!
//! * The list is read into a BOUNDED buffer ([`MAX_LIST`]) and refused, whole, if it is longer. A
//!   source that never finishes is cut off at [`READ_WITHIN`].
//! * Only `file:` URIs with no host (or `localhost`) name files. `http:`, `ftp:`, `data:` and the
//!   rest are refused; so is a relative path and a path with a NUL.
//! * A path is percent-decoded, then resolved with `canonicalize` (symlinks and `..` gone), and what
//!   is left must be a regular file or a directory the user can open for reading. A FIFO, socket,
//!   device or anything that vanished is refused; the controller checks all of it again, because
//!   this process is not the one that mounts.
//! * At most [`MAX_PATHS`] entries.

use std::io::Read;
use std::os::fd::{AsRawFd, OwnedFd};
use std::os::unix::ffi::OsStringExt;
use std::os::unix::fs::{FileTypeExt, OpenOptionsExt};
use std::path::PathBuf;
use std::time::{Duration, Instant};

/// The largest uri-list Aurora will read. A few KiB is sixteen long paths with room to spare.
pub const MAX_LIST: usize = 4096;
/// The most paths one drop may name. The controller applies the same limit.
pub const MAX_PATHS: usize = 16;
/// How long the host's source has to write its list.
pub const READ_WITHIN: Duration = Duration::from_secs(3);

#[derive(Debug, PartialEq, Eq)]
pub enum Refusal {
    TooLarge,
    Timeout,
    Io(String),
    NotUtf8OrEmpty,
    TooMany,
    /// A line that is not a `file:` URI naming a path on this machine.
    NotAFile(String),
}

impl std::fmt::Display for Refusal {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Refusal::TooLarge => write!(f, "the file list is longer than {MAX_LIST} bytes"),
            Refusal::Timeout => write!(f, "the source did not finish its file list within {READ_WITHIN:?}"),
            Refusal::Io(e) => write!(f, "cannot read the file list: {e}"),
            Refusal::NotUtf8OrEmpty => write!(f, "the file list is empty"),
            Refusal::TooMany => write!(f, "the drop names more than {MAX_PATHS} files"),
            Refusal::NotAFile(l) => write!(f, "{l:?} is not a file: URI naming a path on this machine"),
        }
    }
}

/// Read at most [`MAX_LIST`] bytes from `fd` until EOF; refuse a longer list or a slow source.
/// Never reads more than `MAX_LIST + 1` bytes, so a source that streams forever costs one buffer.
pub fn read_bounded(fd: OwnedFd) -> Result<Vec<u8>, Refusal> {
    let deadline = Instant::now() + READ_WITHIN;
    let raw = fd.as_raw_fd();
    // Non-blocking, so the deadline is ours and a stuck source cannot hold the thread.
    // SAFETY: fcntl on an fd we own.
    unsafe {
        let fl = libc::fcntl(raw, libc::F_GETFL);
        libc::fcntl(raw, libc::F_SETFL, fl | libc::O_NONBLOCK);
    }
    let mut f = std::fs::File::from(fd);
    let mut out = Vec::new();
    let mut buf = [0u8; 1024];
    loop {
        match f.read(&mut buf) {
            Ok(0) => return Ok(out),
            Ok(n) => {
                out.extend_from_slice(&buf[..n.min(MAX_LIST + 1 - out.len())]);
                if out.len() > MAX_LIST {
                    return Err(Refusal::TooLarge);
                }
            }
            Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                let left = deadline.checked_duration_since(Instant::now()).ok_or(Refusal::Timeout)?;
                let mut p = libc::pollfd { fd: raw, events: libc::POLLIN, revents: 0 };
                // SAFETY: one valid pollfd.
                unsafe { libc::poll(&mut p, 1, left.as_millis().min(i32::MAX as u128) as i32) };
            }
            Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
            Err(e) => return Err(Refusal::Io(e.to_string())),
        }
    }
}

fn unhex(b: u8) -> Option<u8> {
    match b {
        b'0'..=b'9' => Some(b - b'0'),
        b'a'..=b'f' => Some(b - b'a' + 10),
        b'A'..=b'F' => Some(b - b'A' + 10),
        _ => None,
    }
}

fn percent_decode(s: &[u8]) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(s.len());
    let mut i = 0;
    while i < s.len() {
        if s[i] == b'%' {
            let (h, l) = (unhex(*s.get(i + 1)?)?, unhex(*s.get(i + 2)?)?);
            out.push(h << 4 | l);
            i += 3;
        } else {
            out.push(s[i]);
            i += 1;
        }
    }
    Some(out)
}

/// The paths a `text/uri-list` names. Comment lines (`#`) and blank lines are skipped, per RFC 2483.
/// Any other line that is not a local `file:` URI refuses the whole list: a drop that mixes a file
/// with a web link is not a file drop to be partly obeyed.
pub fn parse_uri_list(list: &[u8]) -> Result<Vec<PathBuf>, Refusal> {
    let mut out = Vec::new();
    for raw in list.split(|b| *b == b'\n') {
        let line = raw.strip_suffix(b"\r").unwrap_or(raw);
        let line = line.trim_ascii();
        if line.is_empty() || line[0] == b'#' {
            continue;
        }
        let shown = || String::from_utf8_lossy(line).chars().take(80).collect::<String>();
        let rest = match line.get(..5) {
            Some(s) if s.eq_ignore_ascii_case(b"file:") => &line[5..],
            _ => return Err(Refusal::NotAFile(shown())),
        };
        // file:///path, file://localhost/path. file:/path is also seen from older toolkits.
        let path = if let Some(r) = rest.strip_prefix(b"//") {
            let slash = r.iter().position(|b| *b == b'/').ok_or_else(|| Refusal::NotAFile(shown()))?;
            let host = &r[..slash];
            if !host.is_empty() && !host.eq_ignore_ascii_case(b"localhost") {
                return Err(Refusal::NotAFile(shown()));
            }
            &r[slash..]
        } else {
            rest
        };
        // A query or fragment is not part of a path we would open.
        if path.iter().any(|b| *b == b'?' || *b == b'#') {
            return Err(Refusal::NotAFile(shown()));
        }
        let bytes = percent_decode(path).ok_or_else(|| Refusal::NotAFile(shown()))?;
        if bytes.first() != Some(&b'/') || bytes.contains(&0) {
            return Err(Refusal::NotAFile(shown()));
        }
        if out.len() == MAX_PATHS {
            return Err(Refusal::TooMany);
        }
        out.push(PathBuf::from(std::ffi::OsString::from_vec(bytes)));
    }
    if out.is_empty() {
        return Err(Refusal::NotUtf8OrEmpty);
    }
    Ok(out)
}

/// A path that survived [`resolve`]: canonical, and a regular file or a directory.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Resolved {
    pub path: PathBuf,
    pub dir: bool,
}

/// Resolve symlinks and refuse anything that is not a regular file or directory the user can read.
/// Never opens a FIFO or device (the type is read from `lstat` first, and the readability probe is
/// `O_NONBLOCK`), so a hostile list cannot hang the compositor's worker.
pub fn resolve(p: &std::path::Path) -> Result<Resolved, String> {
    let real = std::fs::canonicalize(p).map_err(|e| format!("cannot be resolved ({e})"))?;
    let md = std::fs::symlink_metadata(&real).map_err(|e| format!("cannot be inspected ({e})"))?;
    let ft = md.file_type();
    let dir = ft.is_dir();
    if !dir && !ft.is_file() {
        let what = if ft.is_fifo() { "a FIFO" } else if ft.is_socket() { "a socket" } else if ft.is_block_device() || ft.is_char_device() { "a device" } else { "not a regular file" };
        return Err(format!("is {what}, not a regular file or directory"));
    }
    std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NONBLOCK | libc::O_CLOEXEC | libc::O_NOFOLLOW)
        .open(&real)
        .map_err(|e| format!("cannot be read by the user ({e})"))?;
    Ok(Resolved { path: real, dir })
}

/// A path as a `file:` URI, every byte outside the unreserved set percent-encoded.
pub fn to_uri(p: &str) -> String {
    let mut s = String::from("file://");
    for b in p.as_bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' | b'/' => s.push(*b as char),
            _ => s.push_str(&format!("%{b:02X}")),
        }
    }
    s
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::ffi::OsStrExt;
    use std::os::unix::fs::symlink;
    use std::io::Write;

    fn list(s: &str) -> Result<Vec<PathBuf>, Refusal> {
        parse_uri_list(s.as_bytes())
    }

    #[test]
    fn a_plain_list_parses_with_decoding_comments_and_crlf() {
        let got = list("# a comment\r\nfile:///home/arti/photo%20one.png\r\n\r\nfile://localhost/tmp/a%C3%A9.txt\nfile:/tmp/old.txt\n").unwrap();
        assert_eq!(got, vec![PathBuf::from("/home/arti/photo one.png"), PathBuf::from("/tmp/aé.txt"), PathBuf::from("/tmp/old.txt")]);
    }

    #[test]
    fn anything_that_is_not_a_local_file_uri_refuses_the_whole_list() {
        for bad in [
            "http://example.com/a.png\n",
            "file:///tmp/ok\nhttps://example.com\n",
            "file://otherhost/tmp/a\n",
            "file:relative/path\n",
            "file:///tmp/a%00b\n",
            "file:///tmp/a%zz\n",
            "file:///tmp/a%4\n",
            "file:///tmp/a?x=1\n",
            "file:///tmp/a#frag\n",
            "/tmp/just-a-path\n",
            "data:text/plain,hi\n",
            "file://\n",
        ] {
            assert!(matches!(list(bad), Err(Refusal::NotAFile(_))), "{bad:?} -> {:?}", list(bad));
        }
        assert_eq!(list("\n# only a comment\n"), Err(Refusal::NotUtf8OrEmpty));
    }

    #[test]
    fn at_most_sixteen_paths() {
        let ok: String = (0..MAX_PATHS).map(|i| format!("file:///tmp/f{i}\n")).collect();
        assert_eq!(list(&ok).unwrap().len(), MAX_PATHS);
        assert_eq!(list(&(ok + "file:///tmp/one-more\n")), Err(Refusal::TooMany));
    }

    /// A pipe whose write end is fed by a thread: `close` ends it after the data, else it holds it open.
    fn pipe_with(data: &[u8], close: bool) -> (OwnedFd, std::thread::JoinHandle<()>) {
        use std::os::fd::FromRawFd;
        let mut fds = [0; 2];
        // SAFETY: pipe2 fills two fds, which are owned from here on.
        let (r, mut w) = unsafe {
            assert_eq!(libc::pipe2(fds.as_mut_ptr(), libc::O_CLOEXEC), 0);
            (OwnedFd::from_raw_fd(fds[0]), std::fs::File::from_raw_fd(fds[1]))
        };
        let data = data.to_vec();
        let h = std::thread::spawn(move || {
            let _ = w.write_all(&data);
            if !close {
                std::thread::sleep(READ_WITHIN + Duration::from_secs(1));
            }
        });
        (r, h)
    }

    #[test]
    fn a_list_at_the_bound_is_read_and_one_byte_over_is_refused_without_reading_on() {
        let (r, h) = pipe_with(&vec![b'a'; MAX_LIST], true);
        assert_eq!(read_bounded(r).unwrap().len(), MAX_LIST);
        h.join().unwrap();
        let (r, h) = pipe_with(&vec![b'a'; MAX_LIST + 1], true);
        assert_eq!(read_bounded(r), Err(Refusal::TooLarge));
        h.join().unwrap();
        // a source that writes a megabyte is cut off long before the end (the writer's pipe fills; we stop reading)
        let (r, h) = pipe_with(&vec![b'a'; 60_000], false);
        assert_eq!(read_bounded(r), Err(Refusal::TooLarge));
        drop(h);
    }

    #[test]
    fn a_source_that_never_finishes_is_cut_off() {
        let (r, h) = pipe_with(b"file:///tmp/a\n", false);
        let t = Instant::now();
        assert_eq!(read_bounded(r), Err(Refusal::Timeout));
        assert!(t.elapsed() < READ_WITHIN + Duration::from_secs(1));
        drop(h);
    }

    #[test]
    fn resolve_follows_symlinks_to_the_real_file_and_names_the_real_path() {
        let d = tempfile::tempdir().unwrap();
        let real = std::fs::canonicalize(d.path()).unwrap().join("real.txt");
        std::fs::write(&real, "x").unwrap();
        let link = d.path().join("link.txt");
        symlink(&real, &link).unwrap();
        let dotted = d.path().join("sub/../real.txt");
        std::fs::create_dir(d.path().join("sub")).unwrap();
        assert_eq!(resolve(&link).unwrap(), Resolved { path: real.clone(), dir: false });
        assert_eq!(resolve(&dotted).unwrap(), Resolved { path: real, dir: false });
        let sub = resolve(&d.path().join("sub")).unwrap();
        assert!(sub.dir);
    }

    #[test]
    fn resolve_refuses_what_is_not_a_readable_file_or_directory() {
        let d = tempfile::tempdir().unwrap();
        let fifo = d.path().join("pipe");
        let c = std::ffi::CString::new(fifo.as_os_str().as_bytes()).unwrap();
        // SAFETY: valid path.
        assert_eq!(unsafe { libc::mkfifo(c.as_ptr(), 0o600) }, 0);
        assert!(resolve(&fifo).unwrap_err().contains("FIFO"));
        assert!(resolve(std::path::Path::new("/dev/null")).unwrap_err().contains("device"));
        assert!(resolve(&d.path().join("missing")).unwrap_err().contains("cannot be resolved"));
        let dangling = d.path().join("dangling");
        symlink(d.path().join("nowhere"), &dangling).unwrap();
        assert!(resolve(&dangling).is_err());
        let secret = d.path().join("secret");
        std::fs::write(&secret, "x").unwrap();
        std::fs::set_permissions(&secret, std::os::unix::fs::PermissionsExt::from_mode(0)).unwrap();
        // SAFETY: geteuid has no preconditions.
        if unsafe { libc::geteuid() } != 0 {
            assert!(resolve(&secret).unwrap_err().contains("cannot be read"));
        }
        let sock = d.path().join("sock");
        let _l = std::os::unix::net::UnixListener::bind(&sock).unwrap();
        assert!(resolve(&sock).unwrap_err().contains("socket"));
    }

    #[test]
    fn a_rewritten_uri_encodes_every_odd_byte() {
        assert_eq!(to_uri("/run/aurora/drops/1/photo.png"), "file:///run/aurora/drops/1/photo.png");
        assert_eq!(to_uri("/run/aurora/drops/1/a b\r\n#?.txt"), "file:///run/aurora/drops/1/a%20b%0D%0A%23%3F.txt");
    }

    #[test]
    fn what_to_uri_writes_parse_back_to_the_same_path() {
        for p in ["/run/aurora/drops/1/a b#?%.txt", "/tmp/é.png"] {
            assert_eq!(parse_uri_list(to_uri(p).as_bytes()).unwrap(), vec![PathBuf::from(p)]);
        }
    }
}
