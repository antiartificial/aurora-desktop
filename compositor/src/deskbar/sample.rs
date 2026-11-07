//! Where the Deskbar's numbers come from, and the rule that governs them: a figure is shown only if it
//! was read from something that is about THAT application. Everything here returns `Option`, and
//! `None` is never turned into 0 by anything downstream.
//!
//! Per application, that source is a domain's cgroup (`.../aurora-domains/<name>`), found from the
//! kernel's own record of which cgroup the window's client process is in. An application that is not
//! in a domain has no cgroup of its own to read (it shares the session's with everything else), so it
//! has no figures, and the Deskbar says "not in a domain" rather than inventing a share.
//!
//! What a domain's cgroup actually offers depends on the controllers the user's systemd delegates to
//! the user manager. `cpu.stat` (`usage_usec`) and `pids.current` exist in every cgroup. `memory.current`
//! exists only when the memory controller is enabled for the parent, and on a stock systemd user manager
//! it is not (only `pids` is delegated), so memory is usually `None` and is then simply not drawn.
//!
//! The roots are fields, not constants, so the unit tests can stand a fake `/proc` and cgroup tree up.

use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    time::Instant,
};

use super::model::{Figures, SysFigures};

/// The component of a cgroup path under which every domain's cgroup is created (adapter cgroup.go).
pub const DOMAINS: &str = "aurora-domains";

#[derive(Clone, Debug)]
pub struct Roots {
    pub proc: PathBuf,
    pub cgroup: PathBuf,
}

impl Default for Roots {
    fn default() -> Self {
        Roots { proc: "/proc".into(), cgroup: "/sys/fs/cgroup".into() }
    }
}

/// The cgroup path (as `/proc/<pid>/cgroup` spells it) of the domain `name`, from the text of a client's
/// cgroup file: everything up to and including the domain's own component. `None` when that file does not
/// say the process is in a domain of that name, so a pid that has since moved, or been reused, reads nothing.
///
/// Which domain a window belongs to is NOT decided here: that is `crate::peer`'s, read once at connect and
/// the same fact the window's title-bar badge shows. This only turns that name into a directory to read.
pub fn domain_cgroup(cgroup_file: &str, name: &str) -> Option<String> {
    let path = cgroup_file.lines().find_map(|l| l.strip_prefix("0::"))?;
    let comps: Vec<&str> = path.trim().split('/').filter(|c| !c.is_empty()).collect();
    let i = comps.iter().position(|c| *c == DOMAINS)?;
    (comps.get(i + 1) == Some(&name)).then(|| format!("/{}", comps[..=i + 1].join("/")))
}

fn read(p: &Path) -> Option<String> {
    std::fs::read_to_string(p).ok()
}

fn stat_field(text: &str, key: &str) -> Option<u64> {
    text.lines().find_map(|l| l.strip_prefix(key)?.split_whitespace().next()?.parse().ok())
}

/// `usage_usec` of a cgroup: CPU time its processes have used, ever.
pub fn cpu_usage_us(dir: &Path) -> Option<u64> {
    stat_field(&read(&dir.join("cpu.stat"))?, "usage_usec")
}

/// `memory.current`, only when the memory controller is on for this cgroup.
pub fn memory_bytes(dir: &Path) -> Option<u64> {
    read(&dir.join("memory.current"))?.trim().parse().ok()
}

pub fn procs(dir: &Path) -> Option<u32> {
    read(&dir.join("pids.current"))?.trim().parse().ok()
}

/// The first line of `/proc/stat` as (busy, total) jiffies. Idle and iowait are not busy.
pub fn cpu_jiffies(stat: &str) -> Option<(u64, u64)> {
    let v: Vec<u64> = stat.lines().next()?.strip_prefix("cpu ")?.split_whitespace().filter_map(|n| n.parse().ok()).collect();
    if v.len() < 5 {
        return None;
    }
    let total: u64 = v.iter().take(8).sum();
    Some((total - v[3] - v[4], total))
}

/// Percent of RAM in use: 100 * (1 - MemAvailable / MemTotal). `MemAvailable`, not `MemFree`:
/// the page cache is not "used" in any sense a person means.
pub fn mem_used_percent(meminfo: &str) -> Option<f32> {
    let kb = |key: &str| stat_field(meminfo, key).filter(|v| *v > 0);
    let (total, avail) = (kb("MemTotal:")?, stat_field(meminfo, "MemAvailable:")?);
    Some((100.0 * (1.0 - avail.min(total) as f64 / total as f64)) as f32)
}

/// Reads on a timer and turns two readings into a rate. CPU is a rate, so the first reading of
/// anything gives no CPU figure: there is nothing to difference against, and the answer is "not yet",
/// not 0.
#[derive(Default)]
pub struct Sampler {
    pub roots: Roots,
    /// Where network and battery are read from. A field for the same reason `roots` is: a test
    /// cannot assert anything about whatever hardware the machine running it happens to have.
    pub sys_roots: super::sysinfo::Roots,
    prev: HashMap<String, (u64, Instant)>,
    sys_prev: Option<(u64, u64)>,
}

/// A reading closer together than this is not divided by: a few milliseconds of wall time make a
/// quotient that is mostly noise.
const MIN_INTERVAL_MS: u128 = 400;

impl Sampler {
    #[cfg(test)]
    pub fn with_roots(roots: Roots) -> Self {
        Sampler { roots, ..Default::default() }
    }

    pub fn cgroup_dir(&self, cgroup: &str) -> PathBuf {
        self.roots.cgroup.join(cgroup.trim_start_matches('/'))
    }

    /// Figures for each domain (name, cgroup path), read now. A domain whose cgroup cannot be read is
    /// absent from the result, which the Deskbar draws as nothing.
    pub fn domains(&mut self, doms: &[(String, String)], now: Instant) -> HashMap<String, Figures> {
        let mut out = HashMap::new();
        for (name, cg) in doms {
            let dir = self.cgroup_dir(cg);
            let usage = cpu_usage_us(&dir);
            let cpu = usage.and_then(|u| {
                let (pu, pt) = *self.prev.get(name)?;
                let dt = now.saturating_duration_since(pt);
                (dt.as_millis() >= MIN_INTERVAL_MS && u >= pu).then(|| 100.0 * (u - pu) as f32 / dt.as_micros() as f32)
            });
            match usage {
                // Keep the older reading until it is far enough back to divide by.
                Some(u) if cpu.is_some() || !self.prev.contains_key(name) => {
                    self.prev.insert(name.clone(), (u, now));
                }
                Some(_) => {}
                None => {
                    self.prev.remove(name);
                }
            }
            let f = Figures { cpu, mem: memory_bytes(&dir), procs: procs(&dir) };
            if f != Figures::default() {
                out.insert(name.clone(), f);
            }
        }
        self.prev.retain(|k, _| doms.iter().any(|(n, _)| n == k));
        out
    }

    /// Machine-wide CPU and memory. Each part is `None` until it can be computed.
    pub fn system(&mut self) -> SysFigures {
        let j = read(&self.roots.proc.join("stat")).and_then(|t| cpu_jiffies(&t));
        let cpu = match (self.sys_prev, j) {
            (Some((pb, pt)), Some((b, t))) if t > pt && b >= pb => Some(100.0 * (b - pb) as f32 / (t - pt) as f32),
            _ => None,
        };
        self.sys_prev = j;
        SysFigures {
            cpu,
            mem: read(&self.roots.proc.join("meminfo")).and_then(|t| mem_used_percent(&t)),
            link: super::sysinfo::link(&self.sys_roots),
            battery: super::sysinfo::battery(&self.sys_roots),
            // Not from a file: the bus answers for the audio graph, and the caller fills it in.
            audio: None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    const BASE: &str = "/user.slice/user-1000.slice/user@1000.service/aurora-domains";

    #[test]
    fn the_cgroup_of_a_domain_is_the_path_through_its_own_component() {
        assert_eq!(domain_cgroup(&format!("0::{BASE}/app-demo\n"), "app-demo"), Some(format!("{BASE}/app-demo")));
        // a process in a sub-cgroup of the domain: the figures are the domain's, not the leaf's
        assert_eq!(domain_cgroup(&format!("0::{BASE}/app-demo/inner/leaf\n"), "app-demo"), Some(format!("{BASE}/app-demo")));
    }

    #[test]
    fn a_pid_that_is_no_longer_in_that_domain_reads_nothing() {
        assert_eq!(domain_cgroup(&format!("0::{BASE}/other\n"), "app-demo"), None, "a different domain");
        assert_eq!(domain_cgroup("0::/user.slice/session-2.scope\n", "app-demo"), None, "no domain at all");
        assert_eq!(domain_cgroup("12:cpu:/foo\n", "app-demo"), None, "cgroup v1 only");
        assert_eq!(domain_cgroup(&format!("0::{BASE}\n"), "app-demo"), None);
    }

    #[test]
    fn system_cpu_counts_idle_and_iowait_as_not_busy() {
        let a = "cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 1 1 1 1 0 0 0 0 0\n";
        assert_eq!(cpu_jiffies(a), Some((200, 1000)));
        assert_eq!(cpu_jiffies("nonsense"), None);
        assert_eq!(cpu_jiffies("cpu  1 2\n"), None);
    }

    #[test]
    fn memory_in_use_is_total_minus_available() {
        let m = "MemTotal:       1000 kB\nMemFree:          10 kB\nMemAvailable:    250 kB\n";
        assert_eq!(mem_used_percent(m), Some(75.0));
        assert_eq!(mem_used_percent("MemTotal: 1000 kB\n"), None, "no MemAvailable, no guess from MemFree");
        assert_eq!(mem_used_percent("MemTotal: 0 kB\nMemAvailable: 0 kB\n"), None);
    }

    fn tree(cpu: &str, mem: Option<&str>, pids: Option<&str>) -> (tempfile::TempDir, String) {
        let dir = tempfile::tempdir().unwrap();
        let cg = "/u/aurora-domains/d1".to_string();
        let d = dir.path().join("u/aurora-domains/d1");
        std::fs::create_dir_all(&d).unwrap();
        std::fs::write(d.join("cpu.stat"), cpu).unwrap();
        if let Some(m) = mem { std::fs::write(d.join("memory.current"), m).unwrap(); }
        if let Some(p) = pids { std::fs::write(d.join("pids.current"), p).unwrap(); }
        (dir, cg)
    }

    #[test]
    fn cpu_is_a_rate_so_the_first_reading_gives_none_and_the_second_gives_a_share() {
        let (dir, cg) = tree("usage_usec 1000000\nuser_usec 1\n", None, Some("3\n"));
        let mut s = Sampler::with_roots(Roots { proc: dir.path().join("proc"), cgroup: dir.path().to_path_buf() });
        let t0 = Instant::now();
        let doms = vec![("d1".to_string(), cg.clone())];
        let first = s.domains(&doms, t0);
        assert_eq!(first["d1"], Figures { cpu: None, mem: None, procs: Some(3) }, "no baseline yet: not 0%");
        std::fs::write(dir.path().join("u/aurora-domains/d1/cpu.stat"), "usage_usec 1500000\n").unwrap();
        let second = s.domains(&doms, t0 + Duration::from_secs(2));
        let cpu = second["d1"].cpu.expect("a second reading makes a rate");
        assert!((cpu - 25.0).abs() < 0.01, "0.5 s of CPU over 2 s is 25% of one core, got {cpu}");
        assert_eq!(second["d1"].mem, None, "no memory.current, no memory figure");
    }

    #[test]
    fn a_domain_whose_cgroup_cannot_be_read_has_no_figures_at_all() {
        let dir = tempfile::tempdir().unwrap();
        let mut s = Sampler::with_roots(Roots { proc: dir.path().into(), cgroup: dir.path().into() });
        assert!(s.domains(&[("gone".into(), "/nowhere/aurora-domains/gone".into())], Instant::now()).is_empty());
    }

    #[test]
    fn a_counter_that_went_backwards_is_not_a_negative_share() {
        let (dir, cg) = tree("usage_usec 5000000\n", Some("1048576\n"), None);
        let mut s = Sampler::with_roots(Roots { proc: dir.path().join("proc"), cgroup: dir.path().to_path_buf() });
        let t0 = Instant::now();
        let doms = vec![("d1".to_string(), cg)];
        s.domains(&doms, t0);
        std::fs::write(dir.path().join("u/aurora-domains/d1/cpu.stat"), "usage_usec 10\n").unwrap();
        let f = s.domains(&doms, t0 + Duration::from_secs(1));
        assert_eq!(f["d1"], Figures { cpu: None, mem: Some(1048576), procs: None });
    }

    #[test]
    fn system_cpu_needs_two_readings_too() {
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("stat"), "cpu  100 0 100 700 100 0 0 0 0 0\n").unwrap();
        std::fs::write(dir.path().join("meminfo"), "MemTotal: 100 kB\nMemAvailable: 40 kB\n").unwrap();
        let mut s = Sampler::with_roots(Roots { proc: dir.path().into(), cgroup: dir.path().into() });
        let a = s.system();
        assert_eq!((a.cpu, a.mem), (None, Some(60.0)));
        std::fs::write(dir.path().join("stat"), "cpu  200 0 200 800 100 0 0 0 0 0\n").unwrap();
        let b = s.system();
        assert!((b.cpu.unwrap() - 66.6667).abs() < 0.01);
    }
}
