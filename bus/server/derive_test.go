package server_test

import (
	"encoding/json"
	"testing"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/server"
)

type grantRow struct {
	Handle, ID, Rights, Parent, Resource string
	Descendants                          []string
}

func mintR(t *testing.T, c *client.Client, res, rights string) grantRow {
	t.Helper()
	p := map[string]any{"resource": res}
	if rights != "" {
		p["rights"] = rights
	}
	raw, err := c.Control(ctx(t), "bus.mint@v1", p)
	if err != nil {
		t.Fatal(err)
	}
	var r grantRow
	json.Unmarshal(raw, &r)
	return r
}

func derive(t *testing.T, c *client.Client, parent, rights string) (grantRow, error) {
	t.Helper()
	raw, err := c.Control(ctx(t), "bus.derive@v1", map[string]any{"handle": parent, "rights": rights})
	var r grantRow
	json.Unmarshal(raw, &r)
	return r, err
}

func inspectByID(t *testing.T, c *client.Client) map[string]server.InspectHandle {
	t.Helper()
	raw, err := c.Control(ctx(t), "bus.inspect@v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ins server.InspectReply
	json.Unmarshal(raw, &ins)
	m := map[string]server.InspectHandle{}
	for _, h := range ins.Handles {
		m[h.ID] = h
	}
	return m
}

func revokedEvents(t *testing.T, w *client.Client, n int) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for i := 0; i < n; i++ {
		e := recv(t, w.Events())
		if e.Topic != "bus.cap.revoked" {
			t.Fatalf("event %s, want bus.cap.revoked", e.Topic)
		}
		var p map[string]any
		json.Unmarshal(e.Payload, &p)
		out[p["id"].(string)] = p
	}
	return out
}

func TestMintRecordsRightsAndOmissionIsNone(t *testing.T) {
	ce := startCaps(t)
	w := dial(t, ce.session)
	w.Subscribe(ctx(t), "bus.cap.minted")

	rw := mintR(t, ce.a, "file:/x", "write,read")
	if rw.Rights != "read,write" {
		t.Fatalf("mint reply rights = %q, want the canonical sorted spelling", rw.Rights)
	}
	bare := mintR(t, ce.a, "file:/y", "")
	if bare.Rights != "none" {
		t.Fatalf("a mint with no rights field holds %q, want none (fail-closed)", bare.Rights)
	}
	ins := inspectByID(t, ce.a)
	if ins[rw.ID].Rights != "read,write" || ins[bare.ID].Rights != "none" {
		t.Fatalf("inspect rights = %+v", ins)
	}
	var p struct{ ID, Rights string }
	json.Unmarshal(recv(t, w.Events()).Payload, &p)
	if p.ID != rw.ID || p.Rights != "read,write" {
		t.Fatalf("minted event payload %+v", p)
	}
	if _, err := ce.a.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": "file:/z", "rights": "read,fly"}); code(err) != "bad_rights" {
		t.Fatalf("unknown right: %v", err)
	}
}

func TestDeriveRules(t *testing.T) {
	ce := startCaps(t)
	root := mintR(t, ce.a, "file:/x", "read,write,grant")

	// Only the holder derives, even a domain that knows the token.
	if _, err := derive(t, ce.b, root.Handle, "read"); code(err) != "cap_not_held" {
		t.Fatalf("non-holder derive: %v", err)
	}
	child, err := derive(t, ce.a, root.Handle, "read")
	if err != nil {
		t.Fatal(err)
	}
	if child.Rights != "read" || child.Parent != root.ID || child.Resource != "file:/x" || child.Handle == root.Handle {
		t.Fatalf("child = %+v", child)
	}
	ins := inspectByID(t, ce.a)
	if ins[child.ID].Parent != root.ID || ins[child.ID].Resource != "file:/x" || ins[child.ID].Rights != "read" {
		t.Fatalf("inspect child = %+v", ins[child.ID])
	}

	// Cannot exceed the parent.
	if _, err := derive(t, ce.a, root.Handle, "read,destroy"); code(err) != "rights_exceed_parent" {
		t.Fatalf("widening: %v", err)
	}
	// The child has no grant, so it is a leaf.
	if _, err := derive(t, ce.a, child.Handle, "read"); code(err) != "cap_no_grant" {
		t.Fatalf("leaf derive: %v", err)
	}
	// A parent without grant cannot derive even an equal set.
	nogrant := mintR(t, ce.a, "file:/y", "read")
	if _, err := derive(t, ce.a, nogrant.Handle, "read"); code(err) != "cap_no_grant" {
		t.Fatalf("no-grant parent: %v", err)
	}
	// A handle minted with no rights is a leaf too.
	if _, err := derive(t, ce.a, mintR(t, ce.a, "file:/z", "").Handle, "none"); code(err) != "cap_no_grant" {
		t.Fatalf("none parent: %v", err)
	}
	if _, err := derive(t, ce.a, root.Handle, "bogus"); code(err) != "bad_rights" {
		t.Fatalf("unknown right: %v", err)
	}

	// A child that keeps grant can derive further.
	mid, err := derive(t, ce.a, root.Handle, "read,grant")
	if err != nil {
		t.Fatal(err)
	}
	if g, err := derive(t, ce.a, mid.Handle, "read"); err != nil || g.Parent != mid.ID {
		t.Fatalf("grandchild: %+v %v", g, err)
	}
}

// The request has no way to name an object: a resource field is ignored, and
// the child is over the parent's object.
func TestDeriveCopiesTheObjectFromTheParent(t *testing.T) {
	ce := startCaps(t)
	root := mintR(t, ce.a, "file:/x", "read,grant")
	raw, err := ce.a.Control(ctx(t), "bus.derive@v1", map[string]any{
		"handle": root.Handle, "rights": "read", "resource": "file:/etc/shadow", "object": "file:/etc/shadow",
	})
	if err != nil {
		t.Fatal(err)
	}
	var r grantRow
	json.Unmarshal(raw, &r)
	if r.Resource != "file:/x" || inspectByID(t, ce.a)[r.ID].Resource != "file:/x" {
		t.Fatalf("derived over %q, want the parent's file:/x", r.Resource)
	}
}

func TestDerivedHandleCannotOutliveParentExpiry(t *testing.T) {
	ce := startCaps(t)
	raw, err := ce.a.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": "file:/x", "rights": "read,grant", "ttl_ms": 60000})
	if err != nil {
		t.Fatal(err)
	}
	var root struct{ Handle, Expires string }
	json.Unmarshal(raw, &root)
	raw, err = ce.a.Control(ctx(t), "bus.derive@v1", map[string]any{"handle": root.Handle, "rights": "read"})
	if err != nil {
		t.Fatal(err)
	}
	var child struct{ Expires string }
	json.Unmarshal(raw, &child)
	if root.Expires == "" || child.Expires != root.Expires {
		t.Fatalf("child expires %q, parent %q", child.Expires, root.Expires)
	}
}

func TestRevokeKillsDescendantsTransitively(t *testing.T) {
	ce := startCaps(t)
	w := dial(t, ce.session)
	root := mintR(t, ce.a, "file:/x", "read,write,grant")
	a, _ := derive(t, ce.a, root.Handle, "read,grant")
	b, _ := derive(t, ce.a, a.Handle, "read,grant")
	c, err := derive(t, ce.a, b.Handle, "read")
	if err != nil {
		t.Fatal(err)
	}
	w.Subscribe(ctx(t), "bus.cap.revoked")

	raw, err := ce.a.Control(ctx(t), "bus.revoke@v1", map[string]string{"id": root.ID})
	if err != nil {
		t.Fatal(err)
	}
	var reply grantRow
	json.Unmarshal(raw, &reply)
	if len(reply.Descendants) != 3 {
		t.Fatalf("revoke reply %s", raw)
	}
	ev := revokedEvents(t, w, 4)
	rootEv := ev[root.ID]
	if rootEv == nil || len(rootEv["descendants"].([]any)) != 3 {
		t.Fatalf("root event must list its descendants: %+v", rootEv)
	}
	for _, d := range []grantRow{a, b, c} {
		e := ev[d.ID]
		if e == nil || e["cause"] != "descendant" || e["root"] != root.ID {
			t.Fatalf("no descendant event for %s: %+v", d.ID, e)
		}
		if err := ce.present(ce.a, d.Handle); code(err) != "cap_revoked" {
			t.Fatalf("descendant %s still usable: %v", d.ID, err)
		}
	}
	if ins := inspectByID(t, ce.a); len(ins) != 0 {
		t.Fatalf("handles still listed: %+v", ins)
	}
	// Revoking again is silent: the tree is already dead.
	if _, err := ce.a.Control(ctx(t), "bus.revoke@v1", map[string]string{"id": root.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeMiddleSparesParentAndSiblings(t *testing.T) {
	ce := startCaps(t)
	root := mintR(t, ce.a, "file:/x", "read,grant")
	mid, _ := derive(t, ce.a, root.Handle, "read,grant")
	sib, _ := derive(t, ce.a, root.Handle, "read,grant")
	kid, _ := derive(t, ce.a, mid.Handle, "read")
	sibKid, _ := derive(t, ce.a, sib.Handle, "read")

	if _, err := ce.a.Control(ctx(t), "bus.revoke@v1", map[string]string{"id": mid.ID}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		tok  string
		dead bool
	}{{"mid", mid.Handle, true}, {"kid", kid.Handle, true}, {"parent", root.Handle, false}, {"sibling", sib.Handle, false}, {"sibling's child", sibKid.Handle, false}} {
		err := ce.present(ce.a, tc.tok)
		if tc.dead && code(err) != "cap_revoked" || !tc.dead && err != nil {
			t.Errorf("%s: dead=%v err=%v", tc.name, tc.dead, err)
		}
	}
	if _, err := derive(t, ce.a, root.Handle, "read"); err != nil {
		t.Fatalf("parent after child revoked: %v", err)
	}
	if _, err := derive(t, ce.a, mid.Handle, "read"); code(err) != "cap_revoked" {
		t.Fatalf("derive from revoked: %v", err)
	}
}
