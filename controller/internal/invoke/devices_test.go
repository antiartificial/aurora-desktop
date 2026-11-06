package invoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
