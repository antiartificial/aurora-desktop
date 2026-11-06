package server_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/server"
)

const (
	idAlpha = "domain:alpha:00112233aabbccdd"
	idBeta  = "domain:beta:00112233aabbccee"
)

// netWorld is two registered domains and the session socket.
func netWorld(t *testing.T) (sess, alpha, beta string) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "bn")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sess = filepath.Join(dir, "s")
	srv, err := server.New(server.Config{SocketPath: sess, DataDir: filepath.Join(dir, "log")})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	alpha, beta = filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := srv.AddDomain(idAlpha, alpha); err != nil {
		t.Fatal(err)
	}
	if err := srv.AddDomain(idBeta, beta); err != nil {
		t.Fatal(err)
	}
	return
}

func report(t *testing.T, c *client.Client, p map[string]any) error {
	t.Helper()
	_, err := c.Control(ctx(t), "bus.net.report@v1", p)
	return err
}

// The session hears a domain's connection events, stamped with the domain the
// daemon hosts; a domain subscribed to everything hears neither its own nor a
// neighbour's; and it cannot subscribe to them by name either.
func TestNetEventsAreStampedAndNeverReachDomains(t *testing.T) {
	sess, alpha, beta := netWorld(t)
	s, rep, da, db := dial(t, sess), dial(t, sess), dial(t, alpha), dial(t, beta)
	if err := s.Subscribe(ctx(t), "net.**"); err != nil {
		t.Fatalf("session subscribe: %v", err)
	}
	for _, c := range []*client.Client{da, db} {
		if err := c.Subscribe(ctx(t), "**"); err != nil {
			t.Fatal(err)
		}
	}
	for _, topic := range []string{"net.conn", "net.summary", "net.*", "net.conn.beta"} {
		err := db.Subscribe(ctx(t), topic)
		var re *client.RemoteError
		if !errors.As(err, &re) || re.Code != "reserved_topic" {
			t.Errorf("beta subscribe %q: got %v, want reserved_topic", topic, err)
		}
	}
	if err := report(t, rep, map[string]any{"identity": idAlpha, "kind": "conn", "host": "a.example.test", "port": 443, "verdict": "refused", "reason": "no net grant for this host", "first": true}); err != nil {
		t.Fatal(err)
	}
	if err := report(t, rep, map[string]any{"identity": idAlpha, "kind": "summary", "entries": []map[string]any{{"host": "a.example.test", "verdict": "refused", "reason": "x", "count": 9}}}); err != nil {
		t.Fatal(err)
	}
	if err := dialPub(t, dial(t, sess), "demo.canary"); err != nil {
		t.Fatal(err)
	}
	var got []proto.Envelope
	end := time.After(600 * time.Millisecond)
loop:
	for {
		select {
		case e := <-s.Events():
			got = append(got, e)
		case <-end:
			break loop
		}
	}
	if len(got) != 2 {
		t.Fatalf("session saw %d net events, want 2", len(got))
	}
	for _, e := range got {
		if e.From != idAlpha {
			t.Errorf("event From = %q, want the registered identity %q", e.From, idAlpha)
		}
		var b struct{ Domain string }
		json.Unmarshal(e.Payload, &b)
		if b.Domain != "alpha" {
			t.Errorf("payload domain = %q, want alpha (derived by the daemon)", b.Domain)
		}
	}
	for name, c := range map[string]*client.Client{"alpha": da, "beta": db} {
		if seen := collect(c.Events(), 300*time.Millisecond); len(seen) != 1 || seen[0] != "demo.canary" {
			t.Errorf("%s subscribed to ** saw %v, want only [demo.canary]", name, seen)
		}
	}
}

func dialPub(t *testing.T, c *client.Client, topic string) error {
	t.Helper()
	if err := c.Publish(topic, json.RawMessage(`{}`), proto.QoSInteractive, false); err != nil {
		return err
	}
	return c.Flush(ctx(t))
}

// Nothing a domain does can write into net.*: not by publishing, not by calling
// the report verb; and the report verb will not speak for a domain the daemon
// is not hosting or accept a verdict it does not know.
func TestNetEventsCannotBeForgedByADomain(t *testing.T) {
	sess, alpha, _ := netWorld(t)
	s, rep, da := dial(t, sess), dial(t, sess), dial(t, alpha)
	if err := s.Subscribe(ctx(t), "net.**"); err != nil {
		t.Fatal(err)
	}
	err := da.Publish("net.conn", json.RawMessage(`{"domain":"beta","host":"x","verdict":"allowed"}`), proto.QoSInteractive, false)
	if err == nil {
		err = da.Flush(ctx(t))
	}
	if forged := collect(s.Events(), 300*time.Millisecond); len(forged) != 0 {
		t.Fatalf("a domain's net.conn publish reached the session: %v (publish err %v)", forged, err)
	}
	var re *client.RemoteError
	if _, err := da.Control(ctx(t), "bus.net.report@v1", map[string]any{"identity": idAlpha, "kind": "conn", "host": "x", "verdict": "allowed"}); !errors.As(err, &re) || re.Code != "forbidden" {
		t.Errorf("a domain calling the report verb: got %v, want forbidden", err)
	}
	if err := report(t, rep, map[string]any{"identity": "domain:ghost:00112233aabbccdd", "kind": "conn", "host": "x", "verdict": "allowed"}); err == nil {
		t.Error("a report for an unregistered domain was accepted")
	}
	if err := report(t, rep, map[string]any{"identity": idAlpha, "kind": "conn", "host": "x", "verdict": "maybe"}); err == nil {
		t.Error("an unknown verdict was accepted")
	}
	if got := collect(s.Events(), 300*time.Millisecond); len(got) != 0 {
		t.Errorf("rejected reports still published: %v", got)
	}
	// control: a valid report from the same session works
	if err := report(t, rep, map[string]any{"identity": idAlpha, "kind": "conn", "host": "x", "verdict": "allowed"}); err != nil {
		t.Fatal(err)
	}
	if got := collect(s.Events(), 300*time.Millisecond); len(got) != 1 || got[0] != "net.conn" {
		t.Errorf("control report: saw %v, want [net.conn]", got)
	}
}
