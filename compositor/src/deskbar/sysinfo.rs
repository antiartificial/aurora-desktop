//! Network and battery, read from sysfs.
//!
//! The rule from `sample.rs` applies here too and is the whole design: a figure is shown only if
//! it was read, everything is an `Option`, and `None` is never turned into a zero or a guess
//! downstream. A desktop that says "0%" because it could not find the battery is worse than one
//! that says nothing, because the first is a claim.
//!
//! What this does NOT do, stated rather than discovered: it does not CHANGE anything. Reading
//! the system's state is the compositor's business in the same way reading a cgroup is; setting
//! a volume or joining a network is an effect on the machine, and effects go through a verb in a
//! domain (decisions 0007 and 0011), not through the process that draws the trusted path.
//!
//! Sound is absent for a reason worth writing down: volume is not in sysfs. Reading it means
//! talking to PipeWire or ALSA through a client library, or shelling out to `wpctl`, and neither
//! belongs on the compositor's frame path. It wants the same treatment as the app registry --
//! asked for over the bus, answered by something whose job it is.
//!
//! The roots are fields rather than constants so the tests can stand up a fake sysfs, which is
//! how `sample.rs` does it and the only way any of this is testable on a machine whose real
//! hardware is whatever it happens to be.

use std::path::{Path, PathBuf};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Link {
    /// A cable, carrier up.
    Wired,
    /// Wireless, associated. The SSID is not read here: it needs a netlink round trip, and the
    /// name of the network is not what the one-glance readout is for.
    Wireless,
    /// Interfaces exist but none of them is up.
    Down,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Battery {
    /// 0-100.
    pub percent: u8,
    pub charging: bool,
}

#[derive(Clone, Debug)]
pub struct Roots {
    pub net: PathBuf,
    pub power: PathBuf,
}

impl Default for Roots {
    fn default() -> Self {
        Roots { net: PathBuf::from("/sys/class/net"), power: PathBuf::from("/sys/class/power_supply") }
    }
}

fn read_trim(p: &Path) -> Option<String> {
    std::fs::read_to_string(p).ok().map(|s| s.trim().to_string())
}

/// Which link the machine actually has, or `None` when there is nothing to say.
///
/// Loopback is skipped: it is always up and saying so would mean the readout never reports a
/// disconnected machine, which is the one thing it exists to report.
pub fn link(r: &Roots) -> Option<Link> {
    let mut any = false;
    let mut wired = false;
    let mut wireless = false;
    for e in std::fs::read_dir(&r.net).ok()? {
        let Ok(e) = e else { continue };
        let name = e.file_name().to_string_lossy().to_string();
        if name == "lo" {
            continue;
        }
        // Only interfaces backed by real hardware. A `device` directory is how the kernel says
        // so, and without this check the readout is simply wrong on any machine with docker or
        // a VPN: docker0 is permanently "up", so an offline laptop reports "wired". Found by
        // looking at an actual /sys/class/net rather than at the idealised one in these tests.
        if !e.path().join("device").exists() {
            continue;
        }
        any = true;
        if read_trim(&e.path().join("operstate")).as_deref() != Some("up") {
            continue;
        }
        // The presence of a `wireless` directory is how the kernel distinguishes them, and it
        // needs no netlink and no privileges.
        if e.path().join("wireless").is_dir() {
            wireless = true;
        } else {
            wired = true;
        }
    }
    if !any {
        return None;
    }
    // Wired wins when both are up: it is the one carrying the traffic, and a machine docked with
    // wifi still associated should not report wifi.
    Some(if wired {
        Link::Wired
    } else if wireless {
        Link::Wireless
    } else {
        Link::Down
    })
}

/// One network interface, as the settings page reports it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Iface {
    pub name: String,
    pub wireless: bool,
    /// `operstate` is "up". The kernel's word, not a guess from an address.
    pub up: bool,
    /// Link speed in Mbit/s, when the driver reports one. Absent on wireless, and on a wired
    /// interface with no carrier, where the file reads -1 or errors.
    pub mbit: Option<u32>,
}

/// Every interface backed by real hardware, in name order.
///
/// Same `device` test as `link`, and for the same reason: without it this lists docker0, veth
/// pairs and VPN tunnels, which is a list of things the user did not plug in. Loopback is
/// skipped for the same reason it is there.
pub fn interfaces(r: &Roots) -> Vec<Iface> {
    let mut out: Vec<Iface> = Vec::new();
    let Ok(dir) = std::fs::read_dir(&r.net) else { return out };
    for e in dir.flatten() {
        let name = e.file_name().to_string_lossy().to_string();
        if name == "lo" || !e.path().join("device").exists() {
            continue;
        }
        let up = read_trim(&e.path().join("operstate")).as_deref() == Some("up");
        // -1 means "the driver has no answer", which is what a wired interface with nothing
        // plugged in reports. It is not a speed and must not be shown as one.
        let mbit = read_trim(&e.path().join("speed"))
            .and_then(|v| v.parse::<i64>().ok())
            .filter(|v| *v > 0)
            .map(|v| v as u32);
        out.push(Iface { name, wireless: e.path().join("wireless").is_dir(), up, mbit });
    }
    out.sort_by(|a, b| a.name.cmp(&b.name));
    out
}

/// The first battery that reports a capacity, or `None` on a machine with none.
pub fn battery(r: &Roots) -> Option<Battery> {
    let mut entries: Vec<PathBuf> = std::fs::read_dir(&r.power).ok()?.filter_map(|e| e.ok()).map(|e| e.path()).collect();
    // Sorted, so a machine with BAT0 and BAT1 reports the same one every time rather than
    // whichever the directory happened to yield first.
    entries.sort();
    for p in entries {
        if read_trim(&p.join("type")).as_deref() != Some("Battery") {
            continue;
        }
        // A wireless mouse reports a battery too, with `scope` = Device. Without this the
        // machine's charge reads as whatever the mouse has left -- on the machine this was
        // written on, `hid-F0T...-battery-144` is the first entry in the directory.
        if read_trim(&p.join("scope")).as_deref() == Some("Device") {
            continue;
        }
        let Some(cap) = read_trim(&p.join("capacity")).and_then(|s| s.parse::<u32>().ok()) else { continue };
        let status = read_trim(&p.join("status")).unwrap_or_default();
        return Some(Battery { percent: cap.min(100) as u8, charging: status == "Charging" || status == "Full" });
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    fn write(p: &Path, s: &str) {
        std::fs::create_dir_all(p.parent().unwrap()).unwrap();
        std::fs::write(p, s).unwrap();
    }

    fn roots(d: &Path) -> Roots {
        Roots { net: d.join("net"), power: d.join("power") }
    }

    #[test]
    fn a_virtual_interface_is_not_a_connection() {
        // docker0, bridges, VPN taps: permanently "up" and backed by nothing. Counting them
        // means an offline laptop reports "wired", which is the readout lying.
        let d = tempfile::tempdir().unwrap();
        write(&d.path().join("net/docker0/operstate"), "up\n");
        write(&d.path().join("net/tailscale0/operstate"), "unknown\n");
        std::fs::create_dir_all(d.path().join("power")).unwrap();
        assert_eq!(link(&roots(d.path())), None, "nothing real is present, so there is nothing to say");

        // The same tree plus one real NIC, down: now there IS something to say.
        write(&d.path().join("net/eno1/operstate"), "down\n");
        std::fs::create_dir_all(d.path().join("net/eno1/device")).unwrap();
        assert_eq!(link(&roots(d.path())), Some(Link::Down));
    }

    #[test]
    fn a_mouse_battery_is_not_the_machines() {
        // A wireless mouse reports a battery with scope=Device, and sorts first on the machine
        // this was written on. Reading it would show the laptop at the mouse's charge.
        let d = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(d.path().join("net")).unwrap();
        write(&d.path().join("power/hid-ABC-battery-144/type"), "Battery\n");
        write(&d.path().join("power/hid-ABC-battery-144/scope"), "Device\n");
        write(&d.path().join("power/hid-ABC-battery-144/capacity"), "20\n");
        write(&d.path().join("power/hid-ABC-battery-144/status"), "Discharging\n");
        assert_eq!(battery(&roots(d.path())), None, "a mouse is not the machine");

        write(&d.path().join("power/zBAT0/type"), "Battery\n");
        write(&d.path().join("power/zBAT0/capacity"), "91\n");
        write(&d.path().join("power/zBAT0/status"), "Discharging\n");
        assert_eq!(battery(&roots(d.path())).unwrap().percent, 91, "the system battery, even sorting last");
    }

    #[test]
    fn a_machine_with_nothing_says_nothing() {
        let d = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(d.path().join("net")).unwrap();
        std::fs::create_dir_all(d.path().join("power")).unwrap();
        // No interfaces and no battery: None, never a zero and never "Down".
        assert_eq!(link(&roots(d.path())), None);
        assert_eq!(battery(&roots(d.path())), None);
    }

    #[test]
    fn loopback_alone_is_not_a_connection() {
        // lo is always up. Counting it would mean the readout could never report a machine with
        // no network, which is the one thing it is for.
        let d = tempfile::tempdir().unwrap();
        write(&d.path().join("net/lo/operstate"), "up\n");
        std::fs::create_dir_all(d.path().join("net/lo/device")).unwrap();
        std::fs::create_dir_all(d.path().join("power")).unwrap();
        assert_eq!(link(&roots(d.path())), None);
    }

    #[test]
    fn a_cable_beats_an_associated_radio() {
        let d = tempfile::tempdir().unwrap();
        write(&d.path().join("net/eth0/operstate"), "up\n");
        std::fs::create_dir_all(d.path().join("net/eth0/device")).unwrap();
        write(&d.path().join("net/wlan0/operstate"), "up\n");
        std::fs::create_dir_all(d.path().join("net/wlan0/wireless")).unwrap();
        std::fs::create_dir_all(d.path().join("net/wlan0/device")).unwrap();
        std::fs::create_dir_all(d.path().join("power")).unwrap();
        assert_eq!(link(&roots(d.path())), Some(Link::Wired), "docked with wifi still up is wired");
    }

    #[test]
    fn a_radio_alone_is_wireless_and_a_dead_cable_is_down() {
        let d = tempfile::tempdir().unwrap();
        write(&d.path().join("net/wlan0/operstate"), "up\n");
        std::fs::create_dir_all(d.path().join("net/wlan0/wireless")).unwrap();
        std::fs::create_dir_all(d.path().join("net/wlan0/device")).unwrap();
        std::fs::create_dir_all(d.path().join("power")).unwrap();
        assert_eq!(link(&roots(d.path())), Some(Link::Wireless));

        let d2 = tempfile::tempdir().unwrap();
        write(&d2.path().join("net/eth0/operstate"), "down\n");
        std::fs::create_dir_all(d2.path().join("net/eth0/device")).unwrap();
        std::fs::create_dir_all(d2.path().join("power")).unwrap();
        assert_eq!(link(&roots(d2.path())), Some(Link::Down), "an interface that is down is Down, not None");
    }

    #[test]
    fn a_battery_is_read_and_a_full_one_counts_as_charging() {
        let d = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(d.path().join("net")).unwrap();
        write(&d.path().join("power/BAT0/type"), "Battery\n");
        write(&d.path().join("power/BAT0/capacity"), "72\n");
        write(&d.path().join("power/BAT0/status"), "Discharging\n");
        assert_eq!(battery(&roots(d.path())), Some(Battery { percent: 72, charging: false }));

        write(&d.path().join("power/BAT0/status"), "Full\n");
        assert_eq!(battery(&roots(d.path())).unwrap().charging, true, "Full is not discharging");
    }

    #[test]
    fn the_mains_adapter_is_not_a_battery_and_a_silly_capacity_is_clamped() {
        let d = tempfile::tempdir().unwrap();
        std::fs::create_dir_all(d.path().join("net")).unwrap();
        // AC first alphabetically, and it has no capacity: it must be skipped, not reported as 0.
        write(&d.path().join("power/AC/type"), "Mains\n");
        write(&d.path().join("power/AC/online"), "1\n");
        write(&d.path().join("power/BAT0/type"), "Battery\n");
        write(&d.path().join("power/BAT0/capacity"), "137\n");
        write(&d.path().join("power/BAT0/status"), "Charging\n");
        let b = battery(&roots(d.path())).expect("the battery, not the adapter");
        assert_eq!(b.percent, 100, "a capacity over 100 is clamped, not shown as 137%");
        assert!(b.charging);
    }
}

#[cfg(test)]
mod iface_probe {
    use super::*;

    /// Not an assertion about this machine, which has whatever hardware it has. It prints what
    /// the reader makes of the REAL /sys, because the `device` rule in both readers exists
    /// entirely because the idealised fixtures agreed with a wrong assumption and one `ls` of
    /// the real thing did not.
    #[test]
    fn what_this_machine_actually_has() {
        let r = Roots::default();
        let all: Vec<String> = std::fs::read_dir(&r.net)
            .map(|d| d.flatten().map(|e| e.file_name().to_string_lossy().to_string()).collect())
            .unwrap_or_default();
        println!("/sys/class/net holds: {all:?}");
        for i in interfaces(&r) {
            println!("  reported: {} wireless={} up={} mbit={:?}", i.name, i.wireless, i.up, i.mbit);
        }
        println!("  link(): {:?}", link(&r));
    }
}

#[cfg(test)]
mod interfaces_tests {
    use super::*;

    fn iface(root: &Path, name: &str, dev: bool, wireless: bool, state: &str, speed: Option<&str>) {
        let d = root.join(name);
        std::fs::create_dir_all(&d).unwrap();
        if dev { std::fs::create_dir_all(d.join("device")).unwrap(); }
        if wireless { std::fs::create_dir_all(d.join("wireless")).unwrap(); }
        std::fs::write(d.join("operstate"), state).unwrap();
        if let Some(s) = speed { std::fs::write(d.join("speed"), s).unwrap(); }
    }

    #[test]
    fn only_hardware_is_listed_and_it_is_sorted() {
        let tmp = tempfile::tempdir().unwrap();
        let net = tmp.path();
        // Shaped like the real /sys/class/net on the machine this was written on, where SIX of
        // eight entries are virtual: a veth pair, two docker bridges, a VPN tunnel, loopback.
        iface(net, "lo", false, false, "up", None);
        iface(net, "docker0", false, false, "up", None);
        iface(net, "veth38a23d9", false, false, "up", None);
        iface(net, "br-cab4e5ab663e", false, false, "up", None);
        iface(net, "tailscale0", false, false, "unknown", None);
        iface(net, "wlp11s0", true, true, "up", None);
        iface(net, "eno1", true, false, "up", Some("1000"));

        let r = Roots { net: net.to_path_buf(), power: net.to_path_buf() };
        let got = interfaces(&r);
        assert_eq!(
            got,
            vec![
                Iface { name: "eno1".into(), wireless: false, up: true, mbit: Some(1000) },
                Iface { name: "wlp11s0".into(), wireless: true, up: true, mbit: None },
            ],
            "a virtual interface reached the list"
        );
    }

    #[test]
    fn a_wired_interface_with_nothing_plugged_in_reports_no_speed() {
        // The driver writes -1 when it has no answer. Showing that as "-1 Mbit/s" would be a
        // figure the machine never reported.
        let tmp = tempfile::tempdir().unwrap();
        let net = tmp.path();
        iface(net, "eno1", true, false, "down", Some("-1"));
        let r = Roots { net: net.to_path_buf(), power: net.to_path_buf() };
        let got = interfaces(&r);
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].mbit, None, "-1 was reported as a speed");
        assert!(!got[0].up);
    }

    #[test]
    fn a_machine_with_no_hardware_lists_nothing_rather_than_guessing() {
        let tmp = tempfile::tempdir().unwrap();
        iface(tmp.path(), "lo", false, false, "up", None);
        let r = Roots { net: tmp.path().to_path_buf(), power: tmp.path().to_path_buf() };
        assert!(interfaces(&r).is_empty());
    }
}
