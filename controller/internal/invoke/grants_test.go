package invoke

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// homeWith returns a fake home with a Pictures dir (the demo app's declared
// write capability) and sets it as the invoker's home.
func homeWith(t *testing.T, inv *Invoker) (home, pics string) {
	t.Helper()
	home, _ = filepath.EvalSymlinks(t.TempDir())
	pics = filepath.Join(home, "Pictures")
	os.MkdirAll(pics, 0o755)
	inv.Home = home
	return home, pics
}

func putPNG(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.WriteFile(p, png, 0o644)
	return p
}

func TestAmbientGrantInsideDeclaredCapability(t *testing.T) {
	inv, _, _, _, recs := setup(t)
	_, pics := homeWith(t, inv)
	in := putPNG(t, pics, "in.png")
	sub := filepath.Join(pics, "sub")
	os.MkdirAll(sub, 0o755)
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: filepath.Join(sub, "o.png")})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	want := []prov.Grant{{Path: in, Mode: "ro", Source: "ambient"}, {Path: sub, Mode: "rw", Source: "ambient"}}
	if !reflect.DeepEqual(res.Grants, want) || res.NeedsApproval {
		t.Fatalf("grants %+v", res.Grants)
	}
	if !reflect.DeepEqual((*recs)[0].Grants, want) {
		t.Fatalf("provenance event grants %+v", (*recs)[0].Grants)
	}
}

func TestGestureGrantOutsideDeclaredCapability(t *testing.T) {
	inv, _, _, _, _ := setup(t)
	homeWith(t, inv)
	dir := t.TempDir() // not under the fake home
	in := putPNG(t, dir, "in.png")
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: filepath.Join(dir, "o.png")})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if len(res.Grants) != 2 || res.Grants[0].Source != "gesture" || res.Grants[1].Source != "gesture" {
		t.Fatalf("grants %+v", res.Grants)
	}
}

func TestSiblingPrefixIsNotCovered(t *testing.T) {
	inv, _, _, _, _ := setup(t)
	home, _ := homeWith(t, inv)
	other := filepath.Join(home, "Pictures-private")
	os.MkdirAll(other, 0o755)
	if got := inv.classify(inv.Reg.Apps()[0].Manifest, other, "ro"); got != "gesture" {
		t.Fatalf("Pictures-private classified %s", got)
	}
}

func TestReadCapabilityDoesNotCoverWriteGrant(t *testing.T) {
	inv, _, _, _, _ := setup(t)
	_, pics := homeWith(t, inv)
	m := inv.Reg.Apps()[0].Manifest
	if got := inv.classify(m, pics, "rw"); got != "ambient" {
		t.Fatalf("write cap, rw grant: %s", got)
	}
	m.Capabilities.Paths[0].Access = "read" // test-local copy of the loaded manifest
	if got := inv.classify(m, pics, "ro"); got != "ambient" {
		t.Fatalf("read cap, ro grant: %s", got)
	}
	if got := inv.classify(m, pics, "rw"); got != "gesture" {
		t.Fatalf("read cap, rw grant: %s", got)
	}
}

func TestNoGestureGrantsRefusesThenAllows(t *testing.T) {
	inv, fk, _, topics, _ := setup(t)
	homeWith(t, inv)
	dir := t.TempDir()
	in := putPNG(t, dir, "in.png")
	req := Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: filepath.Join(dir, "o.png")}

	inv.NoGestureGrants = true
	res, e := inv.Invoke(t.Context(), req)
	if res != nil || e == nil || e.Code != "gesture_grant_refused" || !strings.Contains(e.Detail, in) || !strings.Contains(e.Detail, "--no-gesture-grants") {
		t.Fatalf("want refusal, got %+v %+v", res, e)
	}
	if len(fk.Calls) != 0 || len(*topics) != 0 {
		t.Fatalf("refused invoke touched domain or published: %q %v", fk.Calls, *topics)
	}

	inv.NoGestureGrants = false
	res, e = inv.Invoke(t.Context(), req)
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if res.Grants[0].Source != "gesture" {
		t.Fatalf("grants %+v", res.Grants)
	}
}

func TestNoGestureGrantsAllowsDeclaredPaths(t *testing.T) {
	inv, _, _, _, _ := setup(t)
	_, pics := homeWith(t, inv)
	inv.NoGestureGrants = true
	in := putPNG(t, pics, "in.png")
	if _, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: filepath.Join(pics, "o.png")}); e != nil {
		t.Fatal(e, e.Detail)
	}
}

func TestReadVerbNeverGetsRWGrant(t *testing.T) {
	inv, fk, dir, _, _ := setup(t)
	fk.OnRun = func([]string, []byte) domain.Result { return domain.Result{} }
	in := putPNG(t, dir, "in.png")
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "size@v1", Input: in})
	if e != nil {
		t.Fatal(e)
	}
	if len(res.Grants) != 1 || res.Grants[0].Mode != "ro" {
		t.Fatalf("grants %+v", res.Grants)
	}
	// Asking for an output is refused before any grant exists.
	_, e = inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "size@v1", Input: in, Output: filepath.Join(dir, "x.png")})
	if e == nil || e.Code != "bad_request" {
		t.Fatalf("read verb with output: %+v", e)
	}
	for _, c := range fk.Calls {
		if strings.Contains(c, " rw") {
			t.Fatalf("rw grant for read verb: %q", fk.Calls)
		}
	}
}

func TestDestructiveGestureNeedsApproval(t *testing.T) {
	inv, _, dir, _, _ := setup(t)
	homeWith(t, inv)
	app := inv.Reg.Apps()[0]
	for i := range app.Manifest.Verbs {
		if app.Manifest.Verbs[i].Name == "copy@v1" {
			app.Manifest.Verbs[i].Effect = "destructive" // test-local mutation of the loaded manifest
		}
	}
	var logs []string
	inv.Logf = func(f string, a ...any) { logs = append(logs, f) }
	in := putPNG(t, dir, "in.png")
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: filepath.Join(dir, "o.png")})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if !res.NeedsApproval || len(logs) == 0 || !strings.Contains(logs[0], "NEEDS APPROVAL") {
		t.Fatalf("needs_approval=%v logs=%q", res.NeedsApproval, logs)
	}
}

func TestProvenanceXattrRecordCarriesGrants(t *testing.T) {
	inv, _, dir, _, _ := setup(t)
	homeWith(t, inv)
	var written prov.Record
	inv.SetProvenance = func(_ string, r prov.Record) error { written = r; return nil }
	in := putPNG(t, dir, "in.png")
	if _, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: filepath.Join(dir, "o.png")}); e != nil {
		t.Fatal(e)
	}
	if len(written.Grants) != 2 || written.Grants[0].Source != "gesture" || written.Grants[1].Mode != "rw" {
		t.Fatalf("xattr record grants %+v", written.Grants)
	}
}
