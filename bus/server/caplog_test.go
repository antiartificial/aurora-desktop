package server_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/proto"
)

// Mint, transfer-less revoke: the daemon logs and delivers bus.cap.* events
// stamped From "bus", with the acting domain taken from the connection, and
// control verbs themselves stay out of the log.
func TestCapLifecycleIsLogged(t *testing.T) {
	sock, dataDir, _ := start(t)
	watch := dial(t, sock)
	watch.Subscribe(ctx(t), "bus.cap.*")
	c := dial(t, sock)
	raw, err := c.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": "file:/x:r"})
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ Handle, ID string }
	json.Unmarshal(raw, &m)
	if _, err := c.Control(ctx(t), "bus.revoke@v1", map[string]string{"handle": m.Handle}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bus.cap.minted", "bus.cap.revoked"} {
		e := recv(t, watch.Events())
		var p struct{ ID, By, Resource string }
		json.Unmarshal(e.Payload, &p)
		if e.Topic != want || e.From != "bus" || p.ID != m.ID || p.By != expectedDomain() || p.Resource != "file:/x:r" {
			t.Fatalf("event %s from %q payload %s", e.Topic, e.From, e.Payload)
		}
	}
	msgs, _ := buslog.ReadFile(dataDir)
	if len(msgs) != 2 {
		t.Fatalf("log has %d entries, want exactly the 2 lifecycle events (no control verbs): %+v", len(msgs), msgs)
	}
}

// A client cannot write the daemon's voice into the log.
func TestClientCannotForgeBusTopics(t *testing.T) {
	sock, dataDir, _ := start(t)
	watch := dial(t, sock)
	watch.Subscribe(ctx(t), "**")
	c := dial(t, sock)
	c.Publish("bus.cap.revoked", json.RawMessage(`{"id":"cap:00","by":"x"}`), "", false)
	c.Publish("after.forgery", nil, "", false)
	c.Flush(ctx(t))
	if e := recv(t, watch.Events()); e.Topic != "after.forgery" {
		t.Fatalf("first delivered event is %s, the forged one got through", e.Topic)
	}
	msgs, _ := buslog.ReadFile(dataDir)
	if len(msgs) != 1 || msgs[0].Kind != proto.KindEvent || msgs[0].Topic != "after.forgery" {
		t.Fatalf("log = %+v", msgs)
	}
}

// The daemon's resource charset must admit every name the object namespace can
// build (contracts/object). It is widened to include '@' and '#' for exactly
// two forms: a verb object, "service:files/open@v1", and a name too long or
// too strange to spell literally, "file:#<sha256>". Without them those objects
// cannot be minted, so a capability over them cannot exist.
//
// This lives here rather than only in contracts/object because that package
// can only mirror the regex as a literal; this mints against the real daemon.
func TestResourceCharsetAdmitsTheObjectNamespace(t *testing.T) {
	sock, _, _ := start(t)
	c := dial(t, sock)
	ok := []string{
		"file:/etc/hosts",
		"file:#" + strings.Repeat("ab", 32),
		"device:gpu",
		"host:example.com.443",
		"service:org.aurora.files",
		"service:org.aurora.files/open@v1",
		"session:clipboard",
	}
	for _, res := range ok {
		if _, err := c.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": res}); err != nil {
			t.Errorf("mint %q: %v", res, err)
		}
	}
	// Still bounded: the charset is not a free-for-all, and the limit holds.
	bad := []string{
		"file:/path with spaces",
		"file:/Ümlaut",
		"service:a;rm -rf /",
		"file:/" + strings.Repeat("a", 200),
		"",
	}
	for _, res := range bad {
		if _, err := c.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": res}); err == nil {
			t.Errorf("mint %q was accepted", res)
		}
	}
}
