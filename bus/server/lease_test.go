package server_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

const clip = "session:clipboard"

type leaseRow struct {
	grantRow
	Access  string
	Lease   bool
	Expires string
}

func leaseOf(t *testing.T, c *client.Client, parent, rights string) (leaseRow, error) {
	t.Helper()
	raw, err := c.Control(ctx(t), "bus.lease@v1", map[string]any{"handle": parent, "rights": rights})
	var r leaseRow
	json.Unmarshal(raw, &r)
	return r, err
}

// heldService is a service that does not reply until the test says so, so a
// test can look at the daemon while a request, and the lease on it, is live.
type heldService struct {
	c *client.Client
}

func startHeld(t *testing.T, ce capEnv) heldService {
	t.Helper()
	s := dial(t, ce.session)
	if err := s.Register(ctx(t), "held"); err != nil {
		t.Fatal(err)
	}
	return heldService{s}
}

func (h heldService) call(t *testing.T, c *client.Client, tok string) (<-chan error, proto.Envelope) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := c.Call(ctx(t), proto.Envelope{To: "held", Verb: "read@v1", Caps: []string{tok}})
		done <- err
	}()
	return done, recv(t, h.c.Requests())
}

func mediatedRoot(t *testing.T, ce capEnv) grantRow {
	t.Helper()
	return mintR(t, ce.a, clip, "read,write,grant")
}

func TestLeaseIsADerivedHandleUnderDeriveRules(t *testing.T) {
	ce := startCaps(t)
	root := mediatedRoot(t, ce)

	l, err := leaseOf(t, ce.a, root.Handle, "read")
	if err != nil {
		t.Fatal(err)
	}
	if l.Rights != "read" || l.Parent != root.ID || l.Resource != clip || !l.Lease || l.Access != "mediated" || l.Handle == root.Handle {
		t.Fatalf("lease = %+v", l)
	}
	row := inspectByID(t, ce.a)[l.ID]
	if !row.Lease || row.Access != "mediated" || row.Parent != root.ID || row.Expires == nil {
		t.Fatalf("inspect lease = %+v", row)
	}

	if _, err := leaseOf(t, ce.a, root.Handle, "read,destroy"); code(err) != "rights_exceed_parent" {
		t.Fatalf("widening: %v", err)
	}
	if _, err := leaseOf(t, ce.b, root.Handle, "read"); code(err) != "cap_not_held" {
		t.Fatalf("non-holder: %v", err)
	}
	if _, err := leaseOf(t, ce.a, root.Handle, "read,grant"); code(err) != "lease_no_grant" {
		t.Fatalf("grant on a lease: %v", err)
	}
	if _, err := leaseOf(t, ce.a, root.Handle, "bogus"); code(err) != "bad_rights" {
		t.Fatalf("unknown right: %v", err)
	}
	leaf := mintR(t, ce.a, clip, "read")
	if _, err := leaseOf(t, ce.a, leaf.Handle, "read"); code(err) != "cap_no_grant" {
		t.Fatalf("parent without grant: %v", err)
	}
	if _, err := leaseOf(t, ce.a, mintR(t, ce.a, clip, "").Handle, "none"); code(err) != "cap_no_grant" {
		t.Fatalf("parent with no rights: %v", err)
	}

	// The request has no field that names an object.
	raw, err := ce.a.Control(ctx(t), "bus.lease@v1", map[string]any{
		"handle": root.Handle, "rights": "read", "resource": "session:seat", "object": "session:seat",
	})
	if err != nil {
		t.Fatal(err)
	}
	var r leaseRow
	json.Unmarshal(raw, &r)
	if r.Resource != clip {
		t.Fatalf("lease over %q, want the parent's %s", r.Resource, clip)
	}
}

func TestLeaseEndsAtReplyAndIsRefusedAfter(t *testing.T) {
	ce := startCaps(t)
	svc := startHeld(t, ce)
	w := dial(t, ce.session)
	w.Subscribe(ctx(t), "bus.cap.ended")
	root := mediatedRoot(t, ce)
	l, err := leaseOf(t, ce.a, root.Handle, "read")
	if err != nil {
		t.Fatal(err)
	}

	done, req := svc.call(t, ce.a, l.Handle)
	if !inspectByID(t, ce.a)[l.ID].Lease {
		t.Fatal("lease not outstanding while its request is in flight")
	}
	// One request only: the lease is lent to the request in flight.
	if _, err := ce.a.Call(ctx(t), proto.Envelope{To: "held", Verb: "read@v1", Caps: []string{l.Handle}}); code(err) != "cap_lease_bound" {
		t.Fatalf("second request while bound: %v", err)
	}

	if err := svc.c.Reply(req, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	ev := recv(t, w.Events())
	var p map[string]any
	json.Unmarshal(ev.Payload, &p)
	if p["id"] != l.ID || p["rights"] != "none" || p["cause"] != "reply" || p["lease"] != true || p["access"] != "mediated" {
		t.Fatalf("ended event = %v", p)
	}
	if _, ok := inspectByID(t, ce.a)[l.ID]; ok {
		t.Fatal("ended lease still listed as outstanding")
	}
	_, err = ce.a.Call(ctx(t), proto.Envelope{To: "held", Verb: "read@v1", Caps: []string{l.Handle}})
	if code(err) != "cap_ended" {
		t.Fatalf("presenting an ended lease: %v", err)
	}
	if _, err := derive(t, ce.a, l.Handle, "read"); code(err) != "cap_ended" {
		t.Fatalf("derive from ended lease: %v", err)
	}
	if _, err := leaseOf(t, ce.a, l.Handle, "read"); code(err) != "cap_ended" {
		t.Fatalf("lease from ended lease: %v", err)
	}
	// The parent is untouched: a lease ends, the authority it was cut from does not.
	if _, err := leaseOf(t, ce.a, root.Handle, "read"); err != nil {
		t.Fatalf("parent after a lease ended: %v", err)
	}
}

func TestLeaseEndsWhenTheServiceDisappears(t *testing.T) {
	ce := startCaps(t)
	svc := startHeld(t, ce)
	root := mediatedRoot(t, ce)
	l, _ := leaseOf(t, ce.a, root.Handle, "read")
	done, _ := svc.call(t, ce.a, l.Handle)
	svc.c.Close()
	if err := <-done; code(err) != "service_gone" {
		t.Fatalf("call: %v", err)
	}
	if _, err := ce.a.Call(ctx(t), proto.Envelope{To: "svc", Verb: "u@v1", Caps: []string{l.Handle}}); code(err) != "cap_ended" {
		t.Fatalf("lease whose callee vanished: %v", err)
	}
}

func TestLeaseIsForRequestsOnly(t *testing.T) {
	ce := startCaps(t)
	root := mediatedRoot(t, ce)
	l, _ := leaseOf(t, ce.a, root.Handle, "read")
	// An event is bounded by no reply, so a lease on one would never end.
	_, err := ce.a.Call(ctx(t), proto.Envelope{Kind: proto.KindEvent, Topic: "app.x", QoS: proto.QoSInteractive, Caps: []string{l.Handle}})
	if code(err) != "lease_request_only" {
		t.Fatalf("lease on an event: %v", err)
	}
	if _, err := ce.a.Call(ctx(t), proto.Envelope{To: proto.BusTarget, Verb: "bus.ping@v1", Caps: []string{l.Handle}}); code(err) != "lease_request_only" {
		t.Fatalf("lease on a control verb: %v", err)
	}
	if _, err := ce.a.Control(ctx(t), "bus.transfer@v1", map[string]any{"handle": l.Handle, "to": idB}); code(err) != "cap_is_lease" {
		t.Fatalf("transfer of a lease: %v", err)
	}
	if _, err := derive(t, ce.a, l.Handle, "read"); code(err) != "cap_is_lease" {
		t.Fatalf("derive from a live lease: %v", err)
	}
}

func TestRevokingTheParentKillsALease(t *testing.T) {
	ce := startCaps(t)
	svc := startHeld(t, ce)
	root := mediatedRoot(t, ce)

	unused, _ := leaseOf(t, ce.a, root.Handle, "read")
	inflight, _ := leaseOf(t, ce.a, root.Handle, "read")
	done, req := svc.call(t, ce.a, inflight.Handle)

	raw, err := ce.a.Control(ctx(t), "bus.revoke@v1", map[string]string{"id": root.ID})
	if err != nil {
		t.Fatal(err)
	}
	var rv grantRow
	json.Unmarshal(raw, &rv)
	if len(rv.Descendants) != 2 {
		t.Fatalf("revoke reported %v, want both leases", rv.Descendants)
	}
	if _, err := ce.a.Call(ctx(t), proto.Envelope{To: "svc", Verb: "u@v1", Caps: []string{unused.Handle}}); code(err) != "cap_revoked" {
		t.Fatalf("unused lease after parent revoke: %v", err)
	}
	if _, ok := inspectByID(t, ce.a)[inflight.ID]; ok {
		t.Fatal("in-flight lease still outstanding after its parent was revoked")
	}
	svc.c.Reply(req, json.RawMessage(`{}`))
	<-done
}

func TestLeaseCannotOutliveItsParentOrTheCap(t *testing.T) {
	ce := startCaps(t)
	raw, err := ce.a.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": clip, "rights": "read,grant", "ttl_ms": 2000})
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Handle  string
		Expires time.Time
	}
	json.Unmarshal(raw, &root)

	// Asking for far longer than the parent has is clamped to the parent's end.
	raw, err = ce.a.Control(ctx(t), "bus.lease@v1", map[string]any{"handle": root.Handle, "rights": "read", "ttl_ms": 3600000})
	if err != nil {
		t.Fatal(err)
	}
	var l struct{ Expires time.Time }
	json.Unmarshal(raw, &l)
	if l.Expires.IsZero() || l.Expires.After(root.Expires) {
		t.Fatalf("lease expires %v, parent %v", l.Expires, root.Expires)
	}

	// With an immortal parent a lease still has an end: a default, and a cap.
	immortal := mediatedRoot(t, ce)
	raw, _ = ce.a.Control(ctx(t), "bus.lease@v1", map[string]any{"handle": immortal.Handle, "rights": "read"})
	json.Unmarshal(raw, &l)
	if d := time.Until(l.Expires); d <= 0 || d > time.Minute {
		t.Fatalf("default lease lifetime %v", d)
	}
	raw, _ = ce.a.Control(ctx(t), "bus.lease@v1", map[string]any{"handle": immortal.Handle, "rights": "read", "ttl_ms": 3600000})
	json.Unmarshal(raw, &l)
	if d := time.Until(l.Expires); d > 6*time.Minute {
		t.Fatalf("a lease was granted %v", d)
	}

	// And once the parent has expired, the lease is refused like any descendant.
	short, _ := ce.a.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": clip, "rights": "read,grant", "ttl_ms": 1})
	var sr struct{ Handle string }
	json.Unmarshal(short, &sr)
	time.Sleep(20 * time.Millisecond)
	if _, err := leaseOf(t, ce.a, sr.Handle, "read"); code(err) != "cap_expired" {
		t.Fatalf("lease from expired parent: %v", err)
	}
}

// 0019's reason for existing: a caller must not come away holding something
// called a lease over a bind-mounted file. The daemon refuses, for every object
// whose access is not mediated, and refuses to let a minter say otherwise.
func TestNoLeaseOverWhatIsNotMediated(t *testing.T) {
	ce := startCaps(t)
	for _, res := range []string{"file:/etc/passwd", "file:#" + hash64, "device:gpu", "host:example.com", "file:/x:ro", "ui:read:domain:app-x-1:00ff"} {
		root := mintR(t, ce.a, res, "read,write,grant")
		_, err := leaseOf(t, ce.a, root.Handle, "read")
		if code(err) != "lease_not_enforceable" {
			t.Errorf("lease over %s: %v", res, err)
		}
		// ... but ordinary derivation is still available, labelled for what it is.
		d, err := derive(t, ce.a, root.Handle, "read")
		if err != nil {
			t.Errorf("derive over %s: %v", res, err)
			continue
		}
		row := inspectByID(t, ce.a)[d.ID]
		if row.Lease || row.Access == "mediated" {
			t.Errorf("derived handle over %s presents as %+v", res, row)
		}
	}
	// A minter cannot declare a file mediated.
	for _, res := range []string{"file:/x", "device:gpu", "file:/x:ro"} {
		_, err := ce.a.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": res, "rights": "read", "access": "mediated"})
		if code(err) != "access_not_mediated" {
			t.Errorf("mint of %s claiming mediated: %v", res, err)
		}
	}
	if _, err := ce.a.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": clip, "access": "leased"}); code(err) != "bad_access" {
		t.Errorf("unknown access word: %v", err)
	}
}

const hash64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAccessLabelFollowsTheClassAndIsInherited(t *testing.T) {
	ce := startCaps(t)
	want := map[string]string{
		"file:/x": "mounted", "device:gpu": "mounted", "service:files/open@v1": "mediated", clip: "mediated",
		"host:example.com": "unclassified", "file:/x:ro": "unclassified", "ui:read:x": "unclassified",
	}
	for res, access := range want {
		r := mintR(t, ce.a, res, "read,grant")
		if got := inspectByID(t, ce.a)[r.ID].Access; got != access {
			t.Errorf("%s: access %q, want %q", res, got, access)
		}
		d, err := derive(t, ce.a, r.Handle, "read")
		if err != nil {
			t.Fatal(err)
		}
		if got := inspectByID(t, ce.a)[d.ID].Access; got != access {
			t.Errorf("derived from %s: access %q, want %q", res, got, access)
		}
	}
	// A minter may weaken the label but it is never strengthened.
	raw, err := ce.a.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": clip, "rights": "read", "access": "mounted"})
	if err != nil {
		t.Fatal(err)
	}
	var r struct{ ID, Access string }
	json.Unmarshal(raw, &r)
	if r.Access != "mounted" || inspectByID(t, ce.a)[r.ID].Access != "mounted" {
		t.Fatalf("weakened mint = %+v", r)
	}
}

func TestAccessAndLeaseAreInTheCapEvents(t *testing.T) {
	ce := startCaps(t)
	w := dial(t, ce.session)
	w.Subscribe(ctx(t), "bus.cap.minted")
	file := mintR(t, ce.a, "file:/x", "read,grant")
	root := mediatedRoot(t, ce)
	leaseOf(t, ce.a, root.Handle, "read")

	var got []map[string]any
	for i := 0; i < 3; i++ {
		var p map[string]any
		json.Unmarshal(recv(t, w.Events()).Payload, &p)
		got = append(got, p)
	}
	if got[0]["id"] != file.ID || got[0]["access"] != "mounted" || got[0]["lease"] != nil {
		t.Fatalf("file mint event = %v", got[0])
	}
	if got[1]["access"] != "mediated" || got[1]["lease"] != nil {
		t.Fatalf("service mint event = %v", got[1])
	}
	if got[2]["access"] != "mediated" || got[2]["lease"] != true {
		t.Fatalf("lease mint event = %v", got[2])
	}
}
