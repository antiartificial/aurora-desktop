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
        Comp::start_with(|_| {}, extra)
    }

    /// Like `start`, after `prep` has seen the (empty) runtime dir.
    /// A compositor with the shell as users get it: no `--legacy-scene`.
    pub fn start_shell(extra: &[&str]) -> Comp {
        Comp::start_inner(|_| {}, extra, false)
    }

    pub fn start_with(prep: impl FnOnce(&Path), extra: &[&str]) -> Comp {
        Comp::start_inner(prep, extra, true)
    }

    fn start_inner(prep: impl FnOnce(&Path), extra: &[&str], legacy: bool) -> Comp {
        let dir = tempfile::tempdir().unwrap();
        // the runtime dir must be 0700 or wayland sockets are refused
        fs::set_permissions(dir.path(), std::os::unix::fs::PermissionsExt::from_mode(0o700)).unwrap();
        prep(dir.path());
        let (trusted, input, dump) = (dir.path().join("trusted.sock"), dir.path().join("input.sock"), dir.path().join("overlay.png"));
        let mut child = Command::new(env!("CARGO_BIN_EXE_aurora-compositor"))
            .args(["--headless=1280x800", "--trusted-socket"]).arg(&trusted)
            .arg("--test-input").arg(&input).arg("--dump-overlay").arg(&dump)
            // The pre-shell scene (flat background, no clock): these tests compare frames for
            // byte equality, which an animated desktop would defeat. The shell has its own tests.
            .args(if legacy { &["--legacy-scene"][..] } else { &[][..] }).args(extra)
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

    /// A test client with extra arguments (see examples/testclient.rs); waits until it is mapped.
    pub fn client_args(&mut self, args: &[&str]) -> PathBuf {
        let log = self.dir.path().join(format!("client{}.log", self.clients.len()));
        let c = Command::new(example("testclient")).args(args).env("XDG_RUNTIME_DIR", self.dir.path()).env("WAYLAND_DISPLAY", &self.display)
            .stdout(fs::File::create(&log).unwrap()).spawn().unwrap();
        self.clients.push(c);
        wait_for("client mapped", || read(&log).contains("mapped"));
        std::thread::sleep(Duration::from_millis(150));
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

impl Img {
    pub fn diff_count(&self, o: &Img) -> usize { self.px.chunks(3).zip(o.px.chunks(3)).filter(|(a, b)| a != b).count() }
    pub fn save(&self, p: &Path) {
        let mut e = png::Encoder::new(fs::File::create(p).unwrap(), self.w as u32, self.h as u32);
        e.set_color(png::ColorType::Rgb);
        e.set_depth(png::BitDepth::Eight);
        e.write_header().unwrap().write_image_data(&self.px).unwrap();
    }
}

/// A private copy of `contracts/theme/` that a test may edit while the compositor runs.
pub struct Theme { pub dir: tempfile::TempDir }

impl Theme {
    pub fn new() -> Theme {
        let src = Path::new(env!("CARGO_MANIFEST_DIR")).join("../contracts/theme");
        let dir = tempfile::tempdir().unwrap();
        fs::create_dir(dir.path().join("shaders")).unwrap();
        fs::copy(src.join("aurora-glass.toml"), dir.path().join("aurora-glass.toml")).unwrap();
        for f in ["down", "up", "glass", "card"] {
            fs::copy(src.join(format!("shaders/{f}.frag")), dir.path().join(format!("shaders/{f}.frag"))).unwrap();
        }
        Theme { dir }
    }
    pub fn path(&self) -> &str { self.dir.path().to_str().unwrap() }
    /// Replace `from` with `to` in the theme file (and assert it was there).
    pub fn edit(&self, from: &str, to: &str) {
        let p = self.dir.path().join("aurora-glass.toml");
        let t = fs::read_to_string(&p).unwrap();
        assert!(t.contains(from), "theme has no {from:?}");
        fs::write(&p, t.replace(from, to)).unwrap();
    }
    pub fn write_shader(&self, name: &str, src: &str) { fs::write(self.dir.path().join(format!("shaders/{name}.frag")), src).unwrap(); }
}

/// The demo scene: opaque patterned windows at the back, translucent glass ones in front.
pub fn demo_scene(c: &mut Comp) {
    c.client_args(&["--size", "760x520", "--pattern", "checker"]);
    c.client_args(&["--size", "600x420", "--pattern", "bars"]);
    c.client_args(&["--size", "520x360", "--pattern", "split"]);
    c.client_args(&["--size", "480x320", "--pattern", "glass", "--rgb", "255,255,255", "--alpha", "0.14"]);
    c.client_args(&["--size", "420x300", "--pattern", "glass", "--rgb", "200,230,255", "--alpha", "0.14"]);
    for (i, (x, y)) in [(40, 60), (520, 40), (120, 400), (380, 200), (760, 360)].iter().enumerate() {
        c.inject(&format!("move {i} {x} {y}"));
    }
    std::thread::sleep(Duration::from_millis(300));
}

impl Theme {
    /// Append TOML (new sections) to the theme file; picked up by hot reload.
    pub fn append(&self, toml: &str) {
        use std::io::Write;
        let mut f = fs::OpenOptions::new().append(true).open(self.dir.path().join("aurora-glass.toml")).unwrap();
        writeln!(f, "\n{toml}").unwrap();
    }
}

impl Comp {
    /// Like `inject` but returns the reply text (`ok ...`).
    pub fn inject_reply(&self, cmd: &str) -> String {
        let mut s = UnixStream::connect(&self.input).unwrap();
        writeln!(s, "{cmd}").unwrap();
        let mut ans = String::new();
        BufReader::new(s).read_line(&mut ans).unwrap();
        ans.trim().to_string()
    }
}

/// `x,y,w,h` rectangles from the compositor's `dock: layout` log line: (scale, panel, reserved, items).
pub fn dock_layout(log: &str) -> Option<(f64, [i32; 4], [i32; 4], Vec<[i32; 4]>)> {
    let line = log.lines().rev().find(|l| l.contains("dock: layout"))?;
    let field = |k: &str| line.split(&format!("{k}=")).nth(1).map(|r| r.split_whitespace().next().unwrap_or("").to_string());
    let rect = |t: &str| -> [i32; 4] { let v: Vec<i32> = t.split(',').filter_map(|n| n.parse().ok()).collect(); [v[0], v[1], v[2], v[3]] };
    let items = field("items")?;
    let items = items.trim_matches(|c| c == '[' || c == ']');
    Some((
        field("scale")?.parse().ok()?,
        rect(&field("panel")?),
        rect(&field("reserved")?),
        items.split(';').filter(|s| !s.is_empty()).map(rect).collect(),
    ))
}
