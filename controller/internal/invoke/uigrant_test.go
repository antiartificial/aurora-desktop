package invoke

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/antiartificial/aurora/bus/server"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// A verb that lists ui:read gets exactly one handle, minted by the controller for "ui:read:<the domain's identity>",
// while the app runs, and the handle is gone after. This is against a real in-process busd that enforces capabilities,
// and what the run sees is the bus's own table, which is what the application's tree service reads.
func TestUIReadMintedForTheDomainAndRevokedOnExit(t *testing.T) {
	iv, fk, _, d, cb := capSetup(t)
	iv.Bus = &fakeBus{}
	probe := d.dial()
	var during []server.InspectHandle
	var mu sync.Mutex
	fk.OnRun = func(argv []string, stdin []byte) domain.Result {
		mu.Lock()
		during = d.handles(probe)
		mu.Unlock()
		return writeOut(argv, stdin)
	}
	in := devInput(t)
	res, e := iv.Invoke(t.Context(), Request{App: "org.aurora.uiprobe", Verb: "peek@v1", Input: in, Output: in + ".out"})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	id, _, _ := fk.Identity(DomainName("org.aurora.uiprobe"))
	want := "ui:read:" + id

	mu.Lock()
	var found *server.InspectHandle
	for i := range during {
		if during[i].Resource == want {
			found = &during[i]
		}
	}
	mu.Unlock()
	if found == nil {
		t.Fatalf("while the app ran the bus held no handle %q: %+v", want, during)
	}
	if !strings.HasPrefix(found.Minter, "uid") {
		t.Errorf("the handle's minter is %q: it must be the controller's session identity, which a domain cannot take", found.Minter)
	}

	var got []prov.Grant
	for _, g := range res.Grants {
		if g.UI != "" {
			got = append(got, g)
		}
	}
	if len(got) != 1 || got[0].UI != "read" || got[0].UIFor != id || got[0].Source != prov.SourceAmbient || got[0].Cap == "" {
		t.Fatalf("ui grants in the record: %+v", got)
	}
	if left := d.handles(probe); len(left) != 0 {
		t.Fatalf("handles outstanding after the run (should be revoked): %+v", left)
	}
	// And the record verifies against the daemon: the controller minted it, for that resource.
	rec := prov.Record{Domain: id, Grants: res.Grants, CapEpoch: ""}
	rep := cb.Verify(rec)
	for _, v := range rep.Caps {
		if v.Cap == got[0].Cap && v.Status == prov.StatusNotVerified {
			t.Errorf("the ui grant does not verify: %+v", v)
		}
	}
	// Nothing was put in the domain's namespace for it.
	if c := strings.Join(fk.Calls, "\n"); strings.Contains(c, "device") || strings.Contains(c, "grant "+DomainName("org.aurora.uiprobe")+" ui:") {
		t.Errorf("a ui grant reached the adapter:\n%s", c)
	}
}

// Declared but not listed by the verb: no grant, no handle.
func TestUIReadNotGrantedUnlessTheVerbAsks(t *testing.T) {
	iv, fk, _, d, _ := capSetup(t)
	iv.Bus = &fakeBus{}
	probe := d.dial()
	var during []server.InspectHandle
	fk.OnRun = func(argv []string, stdin []byte) domain.Result {
		during = d.handles(probe)
		return writeOut(argv, stdin)
	}
	in := devInput(t)
	res, e := iv.Invoke(t.Context(), Request{App: "org.aurora.uiprobe", Verb: "plain@v1", Input: in, Output: filepath.Join(t.TempDir(), "o.png")})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	for _, g := range res.Grants {
		if g.UI != "" {
			t.Fatalf("an unrequested ui grant: %+v", g)
		}
	}
	for _, h := range during {
		if strings.HasPrefix(h.Resource, "ui:") {
			t.Fatalf("a ui handle exists although no verb asked: %+v", h)
		}
	}
}

// Fail closed: if the bus does not attest the domain, or there is no way to mint, the invoke does not run.
func TestUIReadRefusedWhenItCannotBeEnforced(t *testing.T) {
	// no bus registration: the identity is not attested
	iv, fk, _, _, _ := capSetup(t)
	in := devInput(t)
	_, e := iv.Invoke(t.Context(), Request{App: "org.aurora.uiprobe", Verb: "peek@v1", Input: in, Output: in + ".out"})
	if e == nil || e.Code != "ui_grant_unavailable" {
		t.Fatalf("want ui_grant_unavailable for an unattested domain, got %+v", e)
	}
	if c := strings.Join(fk.Calls, "\n"); strings.Contains(c, "run ") {
		t.Fatalf("ran despite the refusal:\n%s", c)
	}
	// no capability client at all
	iv2, fk2, _, _, _ := setup(t)
	iv2.Bus = &fakeBus{}
	_, e = iv2.Invoke(t.Context(), Request{App: "org.aurora.uiprobe", Verb: "peek@v1", Input: in, Output: in + ".2"})
	if e == nil || e.Code != "ui_grant_unavailable" {
		t.Fatalf("want ui_grant_unavailable without a cap client, got %+v", e)
	}
	if c := strings.Join(fk2.Calls, "\n"); strings.Contains(c, "run ") {
		t.Fatalf("ran despite the refusal:\n%s", c)
	}
}

func TestUIGrantResourceName(t *testing.T) {
	g := prov.Grant{Path: "ui:read", Mode: prov.ModeUse, UI: "read", UIFor: "domain:app-x-1:00ff"}
	if g.Resource() != "ui:read:domain:app-x-1:00ff" {
		t.Fatal(g.Resource())
	}
	b, _ := json.Marshal(g)
	if !strings.Contains(string(b), `"ui_for"`) {
		t.Fatalf("the identity must be in the record so the verifier can recompute the name: %s", b)
	}
}
