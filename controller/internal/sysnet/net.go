// Package sysnet reads the machine's network interfaces. READ ONLY, and that is a decision
// rather than a stage.
//
// Decision 0015 settled what a network grant means in Aurora: a domain has no network namespace
// at all, and reaches named hosts through a proxy on the host that decides by name and reports
// every decision on the bus. Nothing in that record is a knob for changing the machine's own
// network configuration, and adding one here would put a second, unrecorded way to affect
// networking next to it. Bringing an interface up, changing an address or joining a network is
// NetworkManager's or systemd-networkd's job, needs privilege this process does not have and
// should not acquire, and deserves its own decision record. `system.network@v1` answers "what is
// this machine connected to", which is the question a settings page and a diagnostic both ask,
// and nothing else.
//
// WHO MAY CALL IT: the session socket only. This is the strongest case of the five. 0015's whole
// construction is that a confined domain cannot see the network: it has no resolv.conf, no
// route, and `/proc/net/dev` inside it lists `lo` and nothing else. A verb that handed a domain
// the host's interface list, link states and addresses would give back by the front door exactly
// the picture that record takes away, and it would do it without appearing in the `net.` events
// 0015 built so a person can see what is connected to what.
//
// Two identifiers are handled differently and deliberately. The hardware address is read by the
// netlink call this uses and is never returned: it is a stable per-device identifier and nothing
// a settings page does with it. IPv6 link-local addresses ARE returned, and under EUI-64 they
// encode that same hardware address, so dropping the MAC field is not a privacy boundary and is
// not claimed as one. It is tidiness. The boundary is the caller rule above.
package sysnet

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Roots is where the sysfs half is read from. Replaced in tests.
type Roots struct {
	// Net is /sys/class/net.
	Net string
}

func Real() Roots { return Roots{Net: "/sys/class/net"} }

// Lister returns the kernel's interface list with addresses. net.Interfaces in the standard
// library, which is a netlink call; replaced in tests so the shape can be checked without
// depending on the machine's hardware.
type Lister func() ([]Iface, error)

// Iface is one interface.
type Iface struct {
	Name string `json:"name"`
	// Kind is "wireless" when sysfs has a `wireless` directory for it, else "wired". The same
	// test the compositor's deskbar uses, so the two agree about any one interface.
	Kind string `json:"kind"`
	// Up is `operstate` == "up": the kernel's word, not inferred from having an address. An
	// interface can hold a stale address with no carrier. OperState carries the raw word,
	// because "unknown" (which is what a tunnel reports) is neither up nor down.
	Up        bool   `json:"up"`
	OperState string `json:"operstate"`
	// MBit is the driver's link speed in Mbit/s, when it reports one. nil when it does not:
	// the file reads -1 on a wired interface with nothing plugged in, and wireless drivers
	// have no answer at all. -1 is not a speed and must not be shown as one.
	MBit *int `json:"mbit,omitempty"`
	MTU  int  `json:"mtu,omitempty"`
	// Addrs are the addresses in CIDR form, IPv4 and IPv6, sorted. Empty is a true answer: an
	// interface that is up with no address is a real and diagnosable state.
	Addrs []string `json:"addrs"`
}

// State is the reply of system.network@v1.
type State struct {
	Interfaces []Iface `json:"interfaces"`
	// Virtual names the interfaces that were listed by the kernel and left out of Interfaces,
	// so the filter is visible rather than silent. docker0, veth pairs, bridges and VPN
	// tunnels land here: they are things the user did not plug in, and a settings page that
	// showed them would be answering a different question. A caller that wants them has the
	// names and can ask the kernel itself.
	Virtual   []string `json:"virtual"`
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
}

// RealLister is net.Interfaces plus each interface's addresses.
func RealLister() ([]Iface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(ifs))
	for _, i := range ifs {
		f := Iface{Name: i.Name, MTU: i.MTU, Addrs: []string{}}
		// A per-interface address failure is not a reason to answer nothing about the
		// machine: the interface is reported with no addresses, which Addrs already means.
		if addrs, err := i.Addrs(); err == nil {
			for _, a := range addrs {
				f.Addrs = append(f.Addrs, a.String())
			}
			sort.Strings(f.Addrs)
		}
		out = append(out, f)
	}
	return out, nil
}

func readTrim(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// Read lists the hardware-backed interfaces.
//
// The kernel's list comes from `lister` (netlink), because that is where addresses and MTU live;
// everything else comes from sysfs under `r.Net`. An interface the kernel names and sysfs has no
// directory for is treated as virtual rather than dropped silently: it is still a fact about the
// machine, and a name in `virtual` is cheaper to explain than a missing row.
func Read(r Roots, lister Lister) State {
	ifs, err := lister()
	if err != nil {
		return State{Interfaces: []Iface{}, Virtual: []string{}, Reason: fmt.Sprintf("cannot list interfaces: %v", err)}
	}
	st := State{Interfaces: []Iface{}, Virtual: []string{}, Available: true}
	for _, f := range ifs {
		dir := filepath.Join(r.Net, f.Name)
		// Loopback is skipped for the same reason the virtual ones are: it is not a network
		// the user is on. The `device` symlink is what says an interface is backed by
		// hardware -- the same test compositor/src/deskbar/sysinfo.rs uses, so the deskbar
		// and this verb never disagree about which interfaces exist.
		if f.Name == "lo" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "device")); err != nil {
			st.Virtual = append(st.Virtual, f.Name)
			continue
		}
		f.OperState, _ = readTrim(filepath.Join(dir, "operstate"))
		f.Up = f.OperState == "up"
		f.Kind = "wired"
		if fi, err := os.Stat(filepath.Join(dir, "wireless")); err == nil && fi.IsDir() {
			f.Kind = "wireless"
		}
		if v, ok := readTrim(filepath.Join(dir, "speed")); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				f.MBit = &n
			}
		}
		// A speed read from a link that is down is the driver's last answer, not this link's.
		// Reporting it would say "1000 Mbit/s" next to "down", which is the shape of claim
		// this whole package is written to avoid.
		if !f.Up {
			f.MBit = nil
		}
		if f.Addrs == nil {
			f.Addrs = []string{}
		}
		st.Interfaces = append(st.Interfaces, f)
	}
	sort.Slice(st.Interfaces, func(i, j int) bool { return st.Interfaces[i].Name < st.Interfaces[j].Name })
	sort.Strings(st.Virtual)
	return st
}
