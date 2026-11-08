package sysnet

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// tree writes a /sys/class/net-shaped directory. A path ending in "/" is a directory, which is
// how `wireless` and `device` appear in sysfs.
func tree(t *testing.T, paths map[string]string) Roots {
	t.Helper()
	d := t.TempDir()
	for p, v := range paths {
		full := filepath.Join(d, p)
		if v == "" && p[len(p)-1] == '/' {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Roots{Net: d}
}

func lister(ifs ...Iface) Lister {
	return func() ([]Iface, error) { return ifs, nil }
}

// thisMachine is this machine's shape: a wired interface with a gigabit link, a wireless one with
// no speed, loopback, and the virtual ones docker and tailscale leave behind.
func thisMachine(t *testing.T) (Roots, Lister) {
	t.Helper()
	r := tree(t, map[string]string{
		"eno1/device/":      "",
		"eno1/operstate":    "up\n",
		"eno1/speed":        "1000\n",
		"wlp11s0/device/":   "",
		"wlp11s0/wireless/": "",
		"wlp11s0/operstate": "up\n",
		"lo/operstate":      "unknown\n",
		"docker0/operstate": "down\n",
	})
	return r, lister(
		Iface{Name: "eno1", MTU: 1500, Addrs: []string{"192.168.4.107/22", "fe80::c981:f001:e540:227e/64"}},
		Iface{Name: "wlp11s0", MTU: 1500, Addrs: []string{"192.168.4.190/22"}},
		Iface{Name: "lo", MTU: 65536, Addrs: []string{"127.0.0.1/8"}},
		Iface{Name: "docker0", MTU: 1500, Addrs: []string{"172.17.0.1/16"}},
		Iface{Name: "tailscale0", MTU: 1280, Addrs: []string{"100.64.0.1/32"}},
	)
}

func find(t *testing.T, st State, name string) Iface {
	t.Helper()
	for _, i := range st.Interfaces {
		if i.Name == name {
			return i
		}
	}
	t.Fatalf("no interface %q in %+v", name, st.Interfaces)
	return Iface{}
}

func TestHardwareInterfacesAreReportedWithTheirKernelState(t *testing.T) {
	st := Read(thisMachine(t))
	if !st.Available || len(st.Interfaces) != 2 {
		t.Fatalf("got %+v", st)
	}
	w := find(t, st, "eno1")
	if w.Kind != "wired" || !w.Up || w.OperState != "up" || w.MTU != 1500 {
		t.Fatalf("eno1: %+v", w)
	}
	if w.MBit == nil || *w.MBit != 1000 {
		t.Fatalf("eno1 speed: %+v", w.MBit)
	}
	if len(w.Addrs) != 2 || w.Addrs[0] != "192.168.4.107/22" {
		t.Fatalf("eno1 addrs: %v", w.Addrs)
	}
	// A wireless driver reports no speed. nil, not 0: "0 Mbit/s" is a figure and this is the
	// absence of one.
	if wl := find(t, st, "wlp11s0"); wl.Kind != "wireless" || wl.MBit != nil {
		t.Fatalf("wlp11s0: %+v", wl)
	}
}

func TestVirtualInterfacesAreNamedRatherThanSilentlyDropped(t *testing.T) {
	// docker0, bridges, veth pairs and VPN tunnels are not things the user plugged in, so they
	// are not rows. The filter is published so it is inspectable: a missing row with no
	// explanation is harder to debug than a name in a list.
	st := Read(thisMachine(t))
	if len(st.Virtual) != 2 || st.Virtual[0] != "docker0" || st.Virtual[1] != "tailscale0" {
		t.Fatalf("virtual: %v", st.Virtual)
	}
	for _, i := range st.Interfaces {
		if i.Name == "lo" {
			t.Fatal("loopback is not a network the user is on")
		}
	}
	// And loopback is not in `virtual` either: it is skipped before that test, because
	// listing it as something that was filtered out invites the question "can I have it".
	for _, n := range st.Virtual {
		if n == "lo" {
			t.Fatalf("virtual: %v", st.Virtual)
		}
	}
}

func TestADownLinkReportsNoSpeedEvenWhenTheDriverStillHasOne(t *testing.T) {
	// `speed` holds the driver's last answer. Reporting it would print "1000 Mbit/s" next to
	// "down", which is the exact shape of claim this package exists to avoid.
	r := tree(t, map[string]string{"eno1/device/": "", "eno1/operstate": "down\n", "eno1/speed": "1000\n"})
	i := find(t, Read(r, lister(Iface{Name: "eno1", Addrs: []string{}})), "eno1")
	if i.Up || i.OperState != "down" {
		t.Fatalf("got %+v", i)
	}
	if i.MBit != nil {
		t.Fatalf("a down link reported %d Mbit/s", *i.MBit)
	}
}

func TestMinusOneIsNotASpeed(t *testing.T) {
	// What a wired interface with nothing plugged in reports. operstate can still read "up"
	// on some drivers, so the -1 has to be rejected on its own.
	r := tree(t, map[string]string{"eno1/device/": "", "eno1/operstate": "up\n", "eno1/speed": "-1\n"})
	if i := find(t, Read(r, lister(Iface{Name: "eno1", Addrs: []string{}})), "eno1"); i.MBit != nil {
		t.Fatalf("-1 became %d Mbit/s", *i.MBit)
	}
}

func TestAnUnknownOperstateIsNeitherUpNorInvented(t *testing.T) {
	// A tunnel reports "unknown". `up` is false, and the raw word is carried so a caller can
	// tell "unknown" from "down".
	r := tree(t, map[string]string{"tun0/device/": "", "tun0/operstate": "unknown\n"})
	i := find(t, Read(r, lister(Iface{Name: "tun0", Addrs: []string{}})), "tun0")
	if i.Up || i.OperState != "unknown" {
		t.Fatalf("got %+v", i)
	}
}

func TestAnInterfaceWithNoAddressIsATrueAnswer(t *testing.T) {
	// Up with no address is a real and diagnosable state. It must not become `null`.
	r := tree(t, map[string]string{"eno1/device/": "", "eno1/operstate": "up\n"})
	i := find(t, Read(r, lister(Iface{Name: "eno1"})), "eno1")
	if i.Addrs == nil || len(i.Addrs) != 0 {
		t.Fatalf("addrs: %#v", i.Addrs)
	}
}

func TestANetlinkFailureSaysSoRatherThanReportingNoNetwork(t *testing.T) {
	st := Read(Roots{Net: t.TempDir()}, func() ([]Iface, error) { return nil, fmt.Errorf("boom") })
	if st.Available || st.Reason == "" {
		t.Fatalf("got %+v", st)
	}
	if st.Interfaces == nil || st.Virtual == nil {
		t.Fatal("both lists must marshal as [] rather than null")
	}
}
