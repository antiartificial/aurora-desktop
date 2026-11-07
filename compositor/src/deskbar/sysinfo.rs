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
