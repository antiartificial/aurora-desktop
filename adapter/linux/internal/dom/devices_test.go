package dom

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func joined(a []string) string { return strings.Join(a, " ") }

func TestNoDeviceGrantNoDeviceArgs(t *testing.T) {
	s := Store{Root: t.TempDir()}
	m := &Meta{Name: "d1", Grants: []Grant{}}
	got := joined(s.BwrapArgs(m, false))
	for _, bad := range []string{"/dev/dri", "dev-bind", DisplayPath, "WAYLAND_DISPLAY"} {
		if strings.Contains(got, bad) {
			t.Fatalf("ungranted domain's argv contains %q: %s", bad, got)
		}
	}
}

func TestWaylandGrantBindsTheSocketAloneNeverItsParent(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("AURORA_WAYLAND", "wayland-9")
	t.Setenv("WAYLAND_DISPLAY", "")
	l, err := net.Listen("unix", filepath.Join(rt, "wayland-9"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	s := Store{Root: t.TempDir()}
	m := &Meta{Name: "d2", Grants: []Grant{{Kind: "device", Device: "wayland"}}}
	a := s.BwrapArgs(m, false)
	got := joined(a)
	if !strings.Contains(got, "--bind "+filepath.Join(rt, "wayland-9")+" "+DisplayPath) {
		t.Fatalf("socket not bound: %s", got)
	}
	if !strings.Contains(got, "--setenv WAYLAND_DISPLAY "+DisplayPath) {
		t.Fatalf("WAYLAND_DISPLAY not set: %s", got)
	}
	for i, x := range a {
		if x == rt || x == filepath.Join(rt, "aurora") {
			t.Fatalf("argv binds the runtime directory (arg %d): %s", i, got)
		}
	}
}

func TestWaylandSocketMustBeASocket(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("AURORA_WAYLAND", "aurora") // a directory in real life: holds approval.key
	if _, err := WaylandSocket(); err == nil {
		t.Fatal("a missing path was accepted")
	}
	t.Setenv("AURORA_WAYLAND", ".")
	if _, err := WaylandSocket(); err == nil || !strings.Contains(err.Error(), "not a unix socket") {
		t.Fatalf("a directory was accepted as the socket: %v", err)
	}
}

// The host's WAYLAND_DISPLAY must never become the domain's socket.
func TestHostWaylandDisplayIsIgnored(t *testing.T) {
	rt := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", rt)
	t.Setenv("AURORA_WAYLAND", "")
	l, err := net.Listen("unix", filepath.Join(rt, "wayland-0"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if p, err := WaylandSocket(); err == nil {
		t.Fatalf("resolved %s from the host's WAYLAND_DISPLAY", p)
	}
}

func TestUnknownDeviceRefused(t *testing.T) {
	s := Store{Root: t.TempDir()}
	if _, _, err := s.Create("d3"); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantDevice("d3", "card0"); err == nil {
		t.Fatal("unknown class granted")
	}
	if err := s.GrantDevice("d3", "gpu"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice("d3", "gpu"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeDevice("d3", "gpu"); err == nil {
		t.Fatal("second revoke should say there is no such grant")
	}
}
