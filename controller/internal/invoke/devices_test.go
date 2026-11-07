package invoke

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/server"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

func devInput(t *testing.T) string {
	t.Helper()
	return putPNG(t, t.TempDir(), "in.png")
}

// A verb that lists device:gpu gets exactly that device grant, ambient, and it
// is taken away again after the run, in reverse order of granting.
func TestDeviceGrantMintedAndRevoked(t *testing.T) {
	inv, fk, _, _, _ := setup(t)
	in := devInput(t)
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.devprobe", Verb: "gpu@v1", Input: in, Output: in + ".out"})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	want := prov.Grant{Path: "device:gpu", Mode: prov.ModeUse, Device: "gpu", Source: prov.SourceAmbient}
	var got []prov.Grant
	for _, g := range res.Grants {
		if g.Device != "" {
			got = append(got, g)
		}
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("device grants %+v, want [%+v]", got, want)
	}
	d := DomainName("org.aurora.devprobe")
	calls := strings.Join(fk.Calls, "\n")
	gi, ri := strings.Index(calls, "grant-device "+d+" gpu"), strings.Index(calls, "revoke-device "+d+" gpu")
	run := strings.Index(calls, "run "+d)
	if gi < 0 || run < gi || ri < run {
		t.Fatalf("want grant-device, run, revoke-device in that order:\n%s", calls)
	}
}

// A verb that lists no device gets none, although the manifest declares two.
func TestDeclaredDeviceIsNotGrantedUnlessVerbAsks(t *testing.T) {
	inv, fk, _, _, _ := setup(t)
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.devprobe", Verb: "plain@v1", Input: devInput(t), Output: filepath.Join(t.TempDir(), "o.png")})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	for _, g := range res.Grants {
		if g.Device != "" {
			t.Fatalf("unrequested device grant %+v", g)
		}
	}
	if c := strings.Join(fk.Calls, "\n"); strings.Contains(c, "device") {
		t.Fatalf("device call without a request:\n%s", c)
	}
}

// The handle names the device, and a failure to grant leaves nothing to revoke.
func TestDeviceGrantFailureIsNotRun(t *testing.T) {
	inv, fk, _, _, _ := setup(t)
	fk.GrantErr = os.ErrPermission
	_, e := inv.Invoke(t.Context(), Request{App: "org.aurora.devprobe", Verb: "display@v1", Input: devInput(t), Output: filepath.Join(t.TempDir(), "o.png")})
	if e == nil {
		t.Fatal("expected the grant failure to fail the invoke")
	}
	if c := strings.Join(fk.Calls, "\n"); strings.Contains(c, "run ") {
		t.Fatalf("ran despite a failed grant:\n%s", c)
	}
}

func TestDeviceGrantResourceName(t *testing.T) {
	g := prov.Grant{Path: "device:gpu", Mode: prov.ModeUse, Device: "gpu"}
	if g.Resource() != "device:gpu" {
		t.Fatal(g.Resource())
	}
	f := prov.Grant{Path: "/a/b", Mode: "ro"}
	if f.Resource() != prov.Resource("/a/b", "ro") {
		t.Fatal(f.Resource())
	}
}

func busLog(t *testing.T, c *client.Client, topic string) []map[string]any {
	t.Helper()
	raw, err := c.Control(context.Background(), "bus.log@v1", map[string]any{"topic": topic})
	if err != nil {
		t.Fatal(err)
	}
	var r struct{ Messages []map[string]any }
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r.Messages
}

// The compositor's capability indicator (decision 0016) learns WHICH domain holds a device grant from this event,
// and believes it only when it comes from the connection that minted the handle. So: it is sent after the grant is
// composed and before the payload runs, by the minter, naming the handle id and the domain; a verb with no device
// grant sends nothing; and the handle is revoked when the run ends.
func TestDeviceGrantIsAnnouncedByItsMinterWhileTheDomainHoldsIt(t *testing.T) {
	iv, fk, _, d, _ := capSetup(t)
	probe := d.dial()
	var during []server.InspectHandle
	var announced []map[string]any
	fk.OnRun = func(a []string, in []byte) domain.Result {
		during = d.handles(probe)
		announced = busLog(t, probe, prov.TopicHeld)
		return writeOut(a, in)
	}
	res, e := iv.Invoke(t.Context(), Request{App: "org.aurora.devprobe", Verb: "display@v1", Input: devInput(t), Output: filepath.Join(t.TempDir(), "o.png")})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	var cap string
	for _, g := range res.Grants {
		if g.Device == "wayland" {
			cap = g.Cap
		}
	}
	if !strings.HasPrefix(cap, "cap:") {
		t.Fatalf("the wayland grant has no handle: %+v", res.Grants)
	}
	if len(announced) != 1 {
		t.Fatalf("while the payload ran, %d announcements: %v", len(announced), announced)
	}
	p, _ := announced[0]["payload"].(map[string]any)
	want := "domain:" + DomainName("org.aurora.devprobe") + ":00112233aabbccdd"
	if p["cap"] != cap || p["resource"] != "device:wayland" || p["domain"] != want {
		t.Fatalf("announcement %v, want cap %s device:wayland %s", p, cap, want)
	}
	var minter string
	for _, h := range during {
		if h.ID == cap {
			minter = h.Minter
		}
	}
	if minter == "" || announced[0]["from"] != minter {
		t.Fatalf("announced by %v, but the handle's minter is %q (during the run: %+v)", announced[0]["from"], minter, during)
	}
	if left := d.handles(probe); len(left) != 0 {
		t.Fatalf("handle outlived the run: %+v", left)
	}

	// no device grant, nothing said
	if _, e := iv.Invoke(t.Context(), Request{App: "org.aurora.devprobe", Verb: "plain@v1", Input: devInput(t), Output: filepath.Join(t.TempDir(), "o2.png")}); e != nil {
		t.Fatal(e, e.Detail)
	}
	if n := len(busLog(t, probe, prov.TopicHeld)); n != 1 {
		t.Fatalf("a verb with no device grant announced one: %d announcements in the log", n)
	}
}
