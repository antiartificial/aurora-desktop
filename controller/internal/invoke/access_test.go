package invoke

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/bus/server"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// Decision 0019's one non-optional item, end to end against a real daemon: a
// bind-mounted grant is labelled mounted in the daemon's table while the app
// runs, in the provenance record the invoke writes, and in what a verifier
// prints after the handle is revoked, which is the moment a reader is most
// likely to conclude there is no access.
func TestMountedGrantIsNeverLabelledALease(t *testing.T) {
	iv, fk, dir, d, cb := capSetup(t)
	probe := d.dial()
	var during []server.InspectHandle
	fk.OnRun = func(a []string, in []byte) domain.Result {
		during = d.handles(probe)
		return writeOut(a, in)
	}
	res, e := invokeDemo(t, iv, dir)
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if len(during) != 2 {
		t.Fatalf("during the run: %+v", during)
	}
	for _, h := range during {
		if h.Access != "mounted" || h.Lease {
			t.Errorf("daemon row for %s: access %q lease %v, want mounted and not a lease", h.Resource, h.Access, h.Lease)
		}
	}

	r := res.Provenance
	if r == nil || len(r.Grants) != 2 {
		t.Fatalf("record %+v", r)
	}
	for _, g := range r.Grants {
		if g.Access != prov.AccessMounted {
			t.Errorf("recorded grant %+v: access %q, want mounted", g, g.Access)
		}
	}

	// The handles are revoked now: the daemon lists none of them.
	if left := d.handles(probe); len(left) != 0 {
		t.Fatalf("handles still outstanding: %+v", left)
	}
	rep := cb.Verify(*r)
	if len(rep.Caps) != 2 {
		t.Fatalf("report %+v", rep)
	}
	for _, v := range rep.Caps {
		if v.Access != prov.AccessMounted || !strings.Contains(v.Effect, "does not withdraw access") {
			t.Errorf("verdict %+v does not say the mount outlives the handle", v)
		}
		// Detail embeds the path, and the test's own temp dir is named after the test.
		if s := strings.ToLower(v.Access + v.Effect); strings.Contains(s, "lease") {
			t.Errorf("verdict for a mounted grant calls itself a lease: %s", s)
		}
	}
}

// A record that claims more than the daemon holds is overstating its own
// authority; the verifier must not pass it.
func TestVerifyRejectsARecordThatClaimsADifferentAccessThanTheDaemonHolds(t *testing.T) {
	iv, fk, dir, _, cb := capSetup(t)
	var rep prov.CapReport
	fk.OnRun = func(a []string, in []byte) domain.Result {
		var rq struct{ Input, Output string }
		json.Unmarshal(in, &rq)
		gs := liveGrants(iv, rq.Input, rq.Output)
		for i := range gs {
			gs[i].Access = prov.AccessMounted
		}
		honest := cb.Verify(prov.Record{CapEpoch: mustEpoch(cb), Grants: gs})
		if honest.Status != prov.StatusVerified {
			t.Errorf("honest label: %+v", honest)
		}
		gs[0].Access = "mediated"
		rep = cb.Verify(prov.Record{CapEpoch: mustEpoch(cb), Grants: gs})
		return writeOut(a, in)
	}
	if _, e := invokeDemo(t, iv, dir); e != nil {
		t.Fatal(e)
	}
	if rep.Status != prov.StatusNotVerified || !strings.Contains(rep.Caps[0].Detail, `daemon labels it "mounted"`) {
		t.Fatalf("claiming mediated over a mounted grant: %+v", rep)
	}
}
