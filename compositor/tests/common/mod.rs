//! Harness for the trusted-path tests: a real headless compositor process,
//! a real Wayland test client, a real capture client, real sockets.

#![allow(dead_code)]

use std::{
    fs,
    io::{BufRead, BufReader, Read, Write},
    os::unix::net::UnixStream,
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    time::{Duration, Instant},
};

pub const W: usize = 1280;
pub const H: usize = 800;
pub const CLIENT: [u8; 3] = [200, 40, 200];
pub const BORDER: [u8; 3] = [255, 90, 20];

pub fn wait_for(what: &str, mut f: impl FnMut() -> bool) {
    let t = Instant::now();
    while !f() {
        assert!(t.elapsed() < Duration::from_secs(15), "timed out waiting for {what}");
        std::thread::sleep(Duration::from_millis(30));
    }
}

fn example(name: &str) -> PathBuf {
    let exe = std::env::current_exe().unwrap(); // target/<profile>/deps/<test>
    let p = exe.parent().unwrap().parent().unwrap().join("examples").join(name);
    assert!(p.exists(), "{} missing: run `cargo build --release --examples` first", p.display());
    p
}

fn capture_bin() -> PathBuf {
    let p = Path::new(env!("CARGO_MANIFEST_DIR")).join("../harness/capture/target/release/aurora-capture");
    assert!(p.exists(), "{} missing: build harness/capture first (cargo build --release there)", p.display());
    p
}

pub struct Comp {
    child: Child,
    pub dir: tempfile::TempDir,
    pub display: String,
    pub trusted: PathBuf,
    pub input: PathBuf,
    pub dump: PathBuf,
    clients: Vec<Child>,
}

impl Comp {
    pub fn start(extra: &[&str]) -> Comp {
        let dir = tempfile::tempdir().unwrap();
        // the runtime dir must be 0700 or wayland sockets are refused
        fs::set_permissions(dir.path(), std::os::unix::fs::PermissionsExt::from_mode(0o700)).unwrap();
        let (trusted, input, dump) = (dir.path().join("trusted.sock"), dir.path().join("input.sock"), dir.path().join("overlay.png"));
        let mut child = Command::new(env!("CARGO_BIN_EXE_aurora-compositor"))
            .args(["--headless=1280x800", "--trusted-socket"]).arg(&trusted)
            .arg("--test-input").arg(&input).arg("--dump-overlay").arg(&dump).args(extra)
            .env("XDG_RUNTIME_DIR", dir.path()).env_remove("WAYLAND_DISPLAY")
            .stdout(Stdio::piped()).stderr(fs::File::create(dir.path().join("comp.log")).unwrap())
            .spawn().expect("spawn compositor");
        let mut out = BufReader::new(child.stdout.take().unwrap());
        let display;
        loop {
            let mut l = String::new();
            assert!(out.read_line(&mut l).unwrap() > 0, "compositor exited early: {}", fs::read_to_string(dir.path().join("comp.log")).unwrap_or_default());
            if let Some(d) = l.trim().strip_prefix("aurora-compositor: socket ") {
                display = d.to_string();
                break;
            }
        }
        std::thread::spawn(move || { let mut s = String::new(); let _ = out.read_to_string(&mut s); });
        wait_for("trusted socket", || trusted.exists() && input.exists());
        Comp { child, dir, display, trusted, input, dump, clients: vec![] }
    }

    /// Start the test client and wait until it is mapped and focused.
    pub fn client(&mut self) -> PathBuf {
        let log = self.dir.path().join(format!("client{}.log", self.clients.len()));
        let c = Command::new(example("testclient")).env("XDG_RUNTIME_DIR", self.dir.path()).env("WAYLAND_DISPLAY", &self.display)
            .stdout(fs::File::create(&log).unwrap()).spawn().unwrap();
        self.clients.push(c);
        wait_for("client mapped and focused", || { let l = read(&log); l.contains("mapped") && l.contains("enter") });
        log
    }

    pub fn capture(&self, name: &str) -> Img {
        let out = self.dir.path().join(name);
        let st = Command::new(capture_bin()).args(["--display", &self.display, "-o"]).arg(&out)
            .env("XDG_RUNTIME_DIR", self.dir.path()).status().unwrap();
        assert!(st.success(), "aurora-capture failed");
        Img::load(&out)
    }

    pub fn globals(&self) -> String {
        let o = Command::new(capture_bin()).args(["--display", &self.display, "--list"]).env("XDG_RUNTIME_DIR", self.dir.path()).output().unwrap();
        String::from_utf8_lossy(&o.stdout).to_string() + &String::from_utf8_lossy(&o.stderr)
    }

    pub fn inject(&self, cmd: &str) {
        let mut s = UnixStream::connect(&self.input).unwrap();
        writeln!(s, "{cmd}").unwrap();
        let mut ans = String::new();
        BufReader::new(s).read_line(&mut ans).unwrap();
        assert_eq!(ans.trim(), "ok", "inject {cmd}");
    }

    pub fn log(&self) -> String { fs::read_to_string(self.dir.path().join("comp.log")).unwrap_or_default() }
}

impl Drop for Comp {
    fn drop(&mut self) {
        for c in &mut self.clients { let _ = c.kill(); let _ = c.wait(); }
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

pub fn read(p: &Path) -> String { fs::read_to_string(p).unwrap_or_default() }

pub fn request_json(id: &str, timeout_ms: u64) -> String {
    serde_json::json!({
        "v": 1, "op": "approve", "request_id": id, "app": "org.aurora.demo", "domain": "app-org-aurora-demo",
        "verb": "delete@v1", "effect": "destructive",
        "grants": [{"path": "/home/user/Pictures/holiday.png", "mode": "rw", "source": "gesture"}],
        "args": {"confirm": true}, "timeout_ms": timeout_ms,
    }).to_string()
}

/// Open the trusted socket and send a request; the stream is the pending reply.
pub fn ask(c: &Comp, line: &str) -> UnixStream {
    let mut s = UnixStream::connect(&c.trusted).expect("connect trusted socket");
    writeln!(s, "{line}").unwrap();
    s
}

pub fn answer(s: UnixStream) -> serde_json::Value {
    s.set_read_timeout(Some(Duration::from_secs(15))).unwrap();
    let mut l = String::new();
    BufReader::new(s).read_line(&mut l).unwrap();
    serde_json::from_str(&l).unwrap_or_else(|e| panic!("bad reply {l:?}: {e}"))
}

pub struct Img { pub w: usize, pub h: usize, pub px: Vec<u8> } // RGB

impl Img {
    fn load(p: &Path) -> Img {
        let mut r = png::Decoder::new(fs::File::open(p).unwrap()).read_info().unwrap();
        let mut buf = vec![0; r.output_buffer_size()];
        let info = r.next_frame(&mut buf).unwrap();
        let ch = info.color_type.samples();
        let px = buf[..info.buffer_size()].chunks(ch).flat_map(|c| [c[0], c[1], c[2]]).collect();
        Img { w: info.width as usize, h: info.height as usize, px }
    }
    pub fn at(&self, x: usize, y: usize) -> [u8; 3] { let i = (y * self.w + x) * 3; [self.px[i], self.px[i + 1], self.px[i + 2]] }
    pub fn count(&self, c: [u8; 3]) -> usize { self.px.chunks(3).filter(|p| **p == c).count() }
    pub fn load_file(p: &Path) -> Img { Img::load(p) }
}
