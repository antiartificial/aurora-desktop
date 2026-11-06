package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/server"
	"github.com/antiartificial/aurora/controller/internal/capbus"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// capDaemon is a real in-process busd that ENFORCES capabilities.
type capDaemon struct {
	srv  *server.Server
	sock string
	t    *testing.T
}

func startDaemon(t *testing.T) *capDaemon {
	t.Helper()
	d, err := os.MkdirTemp("", "cap")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	sock := filepath.Join(d, "b.sock")
	srv, err := server.New(server.Config{SocketPath: sock, DataDir: filepath.Join(d, "log"), EnforceCaps: true})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return &capDaemon{srv, sock, t}
}

func (d *capDaemon) dial() *client.Client {
	d.t.Helper()
	var err error
	for i := 0; i < 100; i++ {
		var c *client.Client
		if c, err = client.Dial(d.sock); err == nil {
			d.t.Cleanup(func() { c.Close() })
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	d.t.Fatal(err)
	return nil
}

// handles lists outstanding handles, not counting the controller's epoch one.
func (d *capDaemon) handles(c *client.Client) []server.InspectHandle {
	d.t.Helper()
	raw, err := c.Control(context.Background(), "bus.inspect@v1", nil)
	if err != nil {
		d.t.Fatal(err)
	}
	var r server.InspectReply
	json.Unmarshal(raw, &r)
	var out []server.InspectHandle
	for _, h := range r.Handles {
		if h.Resource != capbus.EpochResource {
			out = append(out, h)
		}
	}
	return out
}

func capSetup(t *testing.T) (*Invoker, *domain.Fake, string, *capDaemon, *capbus.Client) {
	t.Helper()
	iv, fk, dir, _, _ := setup(t)
	d := startDaemon(t)
	cb := &capbus.Client{C: d.dial()}
	iv.Caps = cb
	iv.Timeout = 30 * time.Second
	return iv, fk, dir, d, cb
}

func TestMintsOneHandlePerGrantAndRecordsIds(t *testing.T) {
	iv, fk, dir, d, _ := capSetup(t)
	probe := d.dial()
	var during []server.InspectHandle
	fk.OnRun = func(a []string, in []byte) domain.Result {
		during = d.handles(probe) // the handles are live while the app runs
		return writeOut(a, in)
	}
	res, e := invokeDemo(t, iv, dir)
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if len(res.Grants) != 2 || res.CapNote != "" {
		t.Fatalf("grants %+v note %q", res.Grants, res.CapNote)
	}
	want := map[string]string{}
	for _, g := range res.Grants {
		if !strings.HasPrefix(g.Cap, "cap:") {
			t.Fatalf("grant %+v has no cap id", g)
		}
		want[g.Cap] = prov.Resource(g.Path, g.Mode)
	}
	if len(during) != 2 {
		t.Fatalf("during the run: %+v", during)
	}
	for _, h := range during {
		if want[h.ID] != h.Resource || !strings.HasPrefix(h.Resource, "file:/") || !strings.HasSuffix(h.Resource, ":ro") && !strings.HasSuffix(h.Resource, ":rw") || h.Expires == nil {
			t.Fatalf("daemon table row %+v, want resource %q with a TTL", h, want[h.ID])
		}
	}
	// the record (what goes in the event and xattr) carries the same ids
	if r := res.Provenance; r == nil || r.Grants[0].Cap != res.Grants[0].Cap || r.Grants[1].Cap != res.Grants[1].Cap || r.CapEpoch == "" {
		t.Fatalf("record %+v", r)
	}
}

func TestHandlesPresentedAndAcceptedByHolderCheck(t *testing.T) {
	iv, _, dir, d, cb := capSetup(t)
	sub := d.dial()
	if err := sub.Subscribe(context.Background(), "provenance.created"); err != nil {
		t.Fatal(err)
	}
	res, e := invokeDemo(t, iv, dir)
	if e != nil {
		t.Fatal(e)
	}
	if res.CapNote != "" {
		t.Fatalf("daemon rejected the handles: %s", res.CapNote)
	}
	// The daemon delivers a message only after its holder check passes, so
	// seeing the event with caps proves it accepted them.
	select {
	case ev := <-sub.Events():
		if len(ev.Caps) != 2 {
			t.Fatalf("event carried %d caps", len(ev.Caps))
		}
		for i, tok := range ev.Caps {
			if buslog.CapID(tok) != res.Grants[i].Cap {
				t.Fatalf("cap %d id %s != record %s", i, buslog.CapID(tok), res.Grants[i].Cap)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no provenance.created with caps was delivered")
	}
	// negative control: the same check does reject a handle the daemon never issued
	if err := cb.Present([]string{"cap_" + strings.Repeat("0", 64)}); capbus.Code(err) != "cap_unknown" {
		t.Fatalf("forged handle: %v", err)
	}
}

func TestHandlesRevokedAfterInvokeIncludingFailure(t *testing.T) {
	iv, fk, dir, d, cb := capSetup(t)
	probe := d.dial()
	res, e := invokeDemo(t, iv, dir)
	if e != nil {
		t.Fatal(e)
	}
	if h := d.handles(probe); len(h) != 0 {
		t.Fatalf("outstanding after success: %+v", h)
	}
	// the daemon still knows the row: re-revoking by id is idempotent for the minter
	if err := cb.Revoke(res.Grants[0].Cap); err != nil {
		t.Fatalf("daemon forgot a revoked handle: %v", err)
	}
	fk.OnRun = func([]string, []byte) domain.Result { return domain.Result{Exit: 3, Stderr: []byte("boom")} }
	if _, e := invokeDemo(t, iv, dir); e == nil || e.Code != "app_failed" {
		t.Fatalf("want app_failed, got %v", e)
	}
	if h := d.handles(probe); len(h) != 0 {
		t.Fatalf("outstanding after failure: %+v", h)
	}
	fk.GrantErr = errors.New("grant refused")
	if _, e := invokeDemo(t, iv, dir); e == nil || e.Code != "domain_error" {
		t.Fatalf("want domain_error, got %v", e)
	}
	if h := d.handles(probe); len(h) != 0 {
		t.Fatalf("outstanding after domain error: %+v", h)
	}
}

func TestVerifyVerifiedTamperedAndForgotten(t *testing.T) {
	iv, _, dir, _, cb := capSetup(t)
	res, e := invokeDemo(t, iv, dir)
	if e != nil {
		t.Fatal(e)
	}
	rec := *res.Provenance

	if rep := cb.Verify(rec); rep.Status != prov.StatusVerified {
		t.Fatalf("matching record: %+v", rep)
	}
	// tampered: a cap id the controller never minted, daemon still the same one
	bad := rec
	bad.Grants = append([]prov.Grant(nil), rec.Grants...)
	bad.Grants[1].Cap = "cap:0123456789abcdef"
	if rep := cb.Verify(bad); rep.Status != prov.StatusNotVerified || rep.Caps[1].Status != prov.StatusNotVerified {
		t.Fatalf("tampered cap id: %+v", rep)
	}
	// tampered: a real handle of the controller's, but for another grant
	swap := rec
	swap.Grants = append([]prov.Grant(nil), rec.Grants...)
	swap.Grants[0].Cap = rec.Grants[1].Cap
	if rep := cb.Verify(swap); rep.Status != prov.StatusNotVerified {
		t.Fatalf("swapped cap ids: %+v", rep)
	}
	// tampered: the epoch removed degrades to inconclusive, never to verified
	noEpoch := bad
	noEpoch.CapEpoch = ""
	if rep := cb.Verify(noEpoch); rep.Status != prov.StatusInconclusive {
		t.Fatalf("no epoch: %+v", rep)
	}
	// forgotten: a fresh daemon (a restart) knows none of it: inconclusive, not negative
	cb2 := &capbus.Client{C: startDaemon(t).dial()}
	if rep := cb2.Verify(rec); rep.Status != prov.StatusInconclusive {
		t.Fatalf("after daemon restart: %+v", rep)
	}
}

func TestVerifyWhileHandlesAreLive(t *testing.T) {
	iv, fk, dir, _, cb := capSetup(t)
	var rep prov.CapReport
	fk.OnRun = func(a []string, in []byte) domain.Result {
		var rq struct{ Input, Output string }
		json.Unmarshal(in, &rq)
		// the record does not exist yet; verify the grants as planned
		rep = cb.Verify(prov.Record{CapEpoch: mustEpoch(cb), Grants: liveGrants(iv, rq.Input, rq.Output)})
		return writeOut(a, in)
	}
	if _, e := invokeDemo(t, iv, dir); e != nil {
		t.Fatal(e)
	}
	if rep.Status != prov.StatusVerified || !strings.Contains(rep.Caps[0].Detail, "outstanding") {
		t.Fatalf("live verify: %+v", rep)
	}
}

func TestNoBusStillSucceedsAndSaysWhy(t *testing.T) {
	iv, _, dir, _, _ := capSetup(t)
	iv.Caps = nil
	res, e := invokeDemo(t, iv, dir)
	if e != nil {
		t.Fatal(e)
	}
	r := res.Provenance
	if !strings.Contains(r.CapNote, "no handle issued") || r.Grants[0].Cap != "" || r.CapEpoch != "" {
		t.Fatalf("record %+v", r)
	}
	if rep := (&capbus.Client{}).Verify(*r); rep.Status != prov.StatusNoHandles {
		t.Fatalf("verify of handle-less record: %+v", rep)
	}
}

func TestDaemonGoneOrMintFailureStillSucceeds(t *testing.T) {
	iv, _, dir, d, cb := capSetup(t)
	cb.Timeout = 500 * time.Millisecond
	d.srv.Close() // the daemon dies under the controller
	res, e := invokeDemo(t, iv, dir)
	if e != nil {
		t.Fatalf("invoke failed because capability bookkeeping did: %v", e)
	}
	if r := res.Provenance; !strings.Contains(r.CapNote, "no handle issued") || r.Grants[0].Cap != "" {
		t.Fatalf("record %+v", r)
	}
}

func mustEpoch(cb *capbus.Client) string {
	id, _ := cb.Epoch()
	return id
}

// liveGrants rebuilds the grants an invoke planned, with the ids the daemon
// currently lists for them.
func liveGrants(iv *Invoker, in, out string) []prov.Grant {
	raw, _ := iv.Caps.(*capbus.Client).C.Control(context.Background(), "bus.inspect@v1", nil)
	var r server.InspectReply
	json.Unmarshal(raw, &r)
	var gs []prov.Grant
	for _, c := range []struct{ p, m string }{{in, "ro"}, {out, "rw"}} {
		for _, h := range r.Handles {
			if h.Resource == prov.Resource(c.p, c.m) {
				gs = append(gs, prov.Grant{Path: c.p, Mode: c.m, Cap: h.ID})
			}
		}
	}
	return gs
}
