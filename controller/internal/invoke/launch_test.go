package invoke

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/domain"
)

const winManifest = `[app]
id = "org.example.win"
name = "Win"
version = "1.0.0"
image_hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
windowed = true
exec = "bin/win"

[capabilities]
devices = ["gpu"]
paths = [{ path = "%s", access = "read" }]
`

// winInvoker builds an Invoker over one windowed app whose binary exists.
// declared is the one path its manifest declares.
func winInvoker(t *testing.T, built bool) (*Invoker, *domain.Fake, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "win")
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	declared := t.TempDir()
	os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(strings.Replace(winManifest, "%s", declared, 1)), 0o644)
	if built {
		os.WriteFile(filepath.Join(dir, "bin", "win"), []byte("#!/bin/sh\n"), 0o755)
	}
	reg, skipped := apps.Load(root)
	if len(skipped) != 0 {
		t.Fatalf("skipped: %v", skipped)
	}
	fk := &domain.Fake{}
	return &Invoker{Reg: reg, Runner: fk, Logf: t.Logf, LaunchGrace: 100 * time.Millisecond}, fk, declared
}

func calls(fk *domain.Fake) string { return strings.Join(fk.Log(), "\n") }

// The grants are the display plus exactly what the manifest declares, in the
// order they are composed, and the whole domain is gone when the app ends.
func TestLaunchGrantsAreDisplayPlusDeclaredAndGoWithTheApp(t *testing.T) {
	iv, fk, declared := winInvoker(t, true)
	var proc *domain.FakeProc
	var mu sync.Mutex
	fk.StartHook = func(p *domain.FakeProc) *domain.FakeProc { mu.Lock(); proc = p; mu.Unlock(); return p }
	res, e := iv.Launch(t.Context(), LaunchRequest{App: "org.example.win"})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if !res.Isolated || !strings.HasPrefix(res.Domain, "win-") || len(res.Domain) > 32 {
		t.Fatalf("result %+v", res)
	}
	var got []string
	for _, g := range res.Grants {
		got = append(got, g.Resource())
	}
	want := []string{"device:wayland", "device:gpu"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || !strings.Contains(got[2], declared) {
		t.Fatalf("grants %v, want %v then the declared path %s", got, want, declared)
	}
	c := calls(fk)
	if strings.Contains(c, "grant "+res.Domain+" /") && !strings.Contains(c, "grant "+res.Domain+" "+declared+" ro") {
		t.Fatalf("a path that is not the declared one was granted:\n%s", c)
	}
	if strings.Contains(c, "destroy") {
		t.Fatalf("domain destroyed while the app runs:\n%s", c)
	}
	mu.Lock()
	proc.Exit(0)
	mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(calls(fk), "destroy "+res.Domain) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	c = calls(fk)
	ig, ir, id := strings.Index(c, "grant-device "+res.Domain+" wayland"), strings.Index(c, "revoke-device "+res.Domain+" wayland"), strings.Index(c, "destroy "+res.Domain)
	st := strings.Index(c, "start "+res.Domain)
	if ig < 0 || st < ig || ir < st || id < ir {
		t.Fatalf("want grant, start, revoke, destroy in order:\n%s", c)
	}
}

// An app that dies at once is an error that says why, and the failure is what
// registry.apps will report as its launch_problem; nothing is left behind.
func TestLaunchFailureSaysWhyAndLeavesNothing(t *testing.T) {
	iv, fk, _ := winInvoker(t, true)
	fk.StartHook = func(p *domain.FakeProc) *domain.FakeProc {
		p.ErrText = "cannot connect to display"
		p.Exit(1)
		return p
	}
	_, e := iv.Launch(t.Context(), LaunchRequest{App: "org.example.win"})
	if e == nil || e.Code != "app_failed" || !strings.Contains(e.Detail, "cannot connect to display") {
		t.Fatalf("want app_failed naming the stderr, got %+v", e)
	}
	if p := iv.LaunchProblem("org.example.win"); !strings.Contains(p, "cannot connect to display") {
		t.Fatalf("launch problem %q does not carry the reason", p)
	}
	c := calls(fk)
	if !strings.Contains(c, "revoke-device") || !strings.Contains(c, "destroy win-") {
		t.Fatalf("grants or domain left behind after a failed launch:\n%s", c)
	}
}

// A domain whose sandbox degraded is stopped, not offered.
func TestLaunchStopsAWindowWhoseSandboxDegraded(t *testing.T) {
	iv, fk, _ := winInvoker(t, true)
	fk.StartHook = func(p *domain.FakeProc) *domain.FakeProc {
		p.ErrText = "domain: warning: running WITHOUT its own uid (shares the host user's): no subuid range\n"
		return p
	}
	_, e := iv.Launch(t.Context(), LaunchRequest{App: "org.example.win"})
	if e == nil || e.Code != "isolation_degraded" {
		t.Fatalf("want isolation_degraded, got %+v", e)
	}
	if !strings.Contains(calls(fk), "destroy win-") {
		t.Fatalf("domain left behind:\n%s", calls(fk))
	}
}

// No domain tooling: refuse, do not fall back to a host process.
func TestLaunchRefusesWithoutDomainToolingUnlessTheHatchIsSet(t *testing.T) {
	iv, fk, _ := winInvoker(t, true)
	fk.Unusable = os.ErrNotExist
	_, e := iv.Launch(t.Context(), LaunchRequest{App: "org.example.win"})
	if e == nil || e.Code != "isolation_unavailable" {
		t.Fatalf("want isolation_unavailable, got %+v", e)
	}
	if strings.Contains(calls(fk), "start") {
		t.Fatalf("started something anyway:\n%s", calls(fk))
	}
}

// The escape hatch is the only way out of a domain: it launches a host process,
// says so in the reply, and touches no domain machinery.
func TestUnsandboxedHatchIsLoudAndOutsideTheDomainMachinery(t *testing.T) {
	iv, fk, _ := winInvoker(t, true)
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "win", "bin"), 0o755)
	os.WriteFile(filepath.Join(root, "win", "manifest.toml"), []byte(strings.Replace(winManifest, "%s", t.TempDir(), 1)), 0o644)
	os.WriteFile(filepath.Join(root, "win", "bin", "win"), []byte("#!/bin/sh\nsleep 5\n"), 0o755)
	reg, _ := apps.Load(root)
	iv.Reg, iv.UnsandboxedWindows = reg, true
	t.Setenv("AURORA_WAYLAND", "wayland-9")
	res, e := iv.Launch(t.Context(), LaunchRequest{App: "org.example.win"})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if res.Isolated || !strings.Contains(res.Warning, "UNSANDBOXED") || res.Domain != "" {
		t.Fatalf("result %+v", res)
	}
	if len(fk.Log()) != 0 {
		t.Fatalf("the hatch touched the domain tooling:\n%s", calls(fk))
	}
}

// A binary that was never built is not launchable, and says so.
func TestLaunchOfAnUnbuiltAppIsRefusedWithTheRegistrysReason(t *testing.T) {
	iv, fk, _ := winInvoker(t, false)
	_, e := iv.Launch(t.Context(), LaunchRequest{App: "org.example.win"})
	if e == nil || e.Code != "not_launchable" || !strings.Contains(e.Detail, "not built") {
		t.Fatalf("got %+v", e)
	}
	if len(fk.Log()) != 0 {
		t.Fatalf("domain calls for an unbuilt app:\n%s", calls(fk))
	}
}

// Stop ends a window and its teardown has run when Stop returns.
func TestStopTearsTheDomainDown(t *testing.T) {
	iv, fk, _ := winInvoker(t, true)
	res, e := iv.Launch(t.Context(), LaunchRequest{App: "org.example.win"})
	if e != nil {
		t.Fatal(e)
	}
	names, e := iv.Stop(StopRequest{App: "org.example.win"})
	if e != nil || len(names) != 1 || names[0] != res.Domain {
		t.Fatalf("stopped %v %v", names, e)
	}
	if !strings.Contains(calls(fk), "destroy "+res.Domain) {
		t.Fatalf("not destroyed after Stop:\n%s", calls(fk))
	}
}
