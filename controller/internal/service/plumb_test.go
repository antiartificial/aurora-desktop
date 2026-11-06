package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/plumb"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/domain"
)

func plumbVia(t *testing.T, e *env, kind, value string) plumb.Reply {
	t.Helper()
	raw, err := e.call(t, "registry.plumb@v1", plumb.Request{Selection: plumb.Selection{Kind: kind, Value: value}})
	if err != nil {
		t.Fatalf("%v %s", err, raw)
	}
	var rep plumb.Reply
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

// Over a real busd, as the user's own session: a real PNG offers the demo
// app's verbs with their effect classes, the destructive one marked, and the
// runner is never touched.
func TestPlumbOverBusOffersVerbsWithEffects(t *testing.T) {
	fk := &domain.Fake{}
	e := start(t, fk, "../../testdata/apps")
	f := filepath.Join(e.dir, "pic")
	os.WriteFile(f, png, 0o644)
	rep := plumbVia(t, e, plumb.KindPath, f)
	if rep.Viewer != "session" || rep.Selection.MIME != "image/png" {
		t.Fatalf("%+v", rep)
	}
	got := map[string]string{}
	for _, c := range rep.Candidates {
		got[c.Verb] = c.Effect
	}
	want := map[string]string{"copy@v1": "write", "size@v1": "read", "delete@v1": "destructive"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if rep.Candidates[len(rep.Candidates)-1].Verb != "delete@v1" {
		t.Errorf("destructive should rank last among equals: %+v", rep.Candidates)
	}
	if len(fk.Calls) != 0 {
		t.Fatalf("plumbing touched the domain runner: %v", fk.Calls)
	}
	// text that looks like a path:line, and one that matches nothing
	if r := plumbVia(t, e, plumb.KindText, f+":3"); r.Selection.Line != 3 || len(r.Candidates) != 3 {
		t.Fatalf("%+v", r)
	}
	if r := plumbVia(t, e, plumb.KindText, "hello there"); len(r.Candidates) != 0 || r.Reason == "" {
		t.Fatalf("%+v", r)
	}
}

func TestPlumbDomainCallerLearnsNothingFromDisk(t *testing.T) {
	reg, skipped := apps.Load("../../testdata/apps")
	if len(skipped) != 0 {
		t.Fatal(skipped)
	}
	svc := &Service{Reg: reg} // no Invoker: plumbing must not need one
	dir := t.TempDir()
	real := filepath.Join(dir, "other-domains-file")
	os.WriteFile(real, png, 0o600)
	missing := filepath.Join(dir, "never-existed")

	ask := func(from, p string) plumb.Reply {
		b, _ := json.Marshal(plumb.Request{Selection: plumb.Selection{Kind: plumb.KindPath, Value: p}})
		out, e := svc.dispatch(proto.Envelope{From: from, Verb: "registry.plumb@v1", Payload: b})
		if e != nil {
			t.Fatal(e)
		}
		return out.(plumb.Reply)
	}
	const dom = "domain:app-org-aurora-x:00112233"
	a, b := ask(dom, real), ask(dom, missing)
	a.Selection.Value, a.Selection.Object, b.Selection.Value, b.Selection.Object = "", "", "", ""
	if !reflect.DeepEqual(a, b) || len(a.Candidates) != 0 || a.Selection.MIME != "" {
		t.Fatalf("a domain can tell a real file from a missing one:\n%+v\n%+v", a, b)
	}
	// the same question from the session does see the file
	if s := ask("uid1000:aurora", real); s.Selection.MIME != "image/png" || len(s.Candidates) != 3 {
		t.Fatalf("session: %+v", s)
	}
	// and from an identity nobody vouches for: nothing
	if u := ask("", real); len(u.Candidates) != 0 || u.Viewer != "unknown" {
		t.Fatalf("unknown: %+v", u)
	}
}

func TestVerbsCarryDefaultAndGroup(t *testing.T) {
	e := start(t, &domain.Fake{}, "../../../apps")
	raw, err := e.call(t, "registry.verbs@v1", map[string]any{"accepts": "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Verbs []struct {
			Name    string
			Default bool
			Group   string
		}
	}
	json.Unmarshal(raw, &v)
	var n int
	for _, x := range v.Verbs {
		if x.Default || x.Group != "" {
			n++
		}
	}
	if n == 0 {
		t.Fatalf("no default or group in registry.verbs@v1 for image/png: %s", raw)
	}
}
