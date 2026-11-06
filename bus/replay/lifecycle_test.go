package replay_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/replay"
	"github.com/antiartificial/aurora/bus/server"
)

const (
	domA = "domain:alpha:00112233aabbccdd"
	domB = "domain:beta:ffeeddcc99887766"
)

// recordLifecycle runs a real daemon: A mints h1, uses it, transfers it to B,
// B uses it, A revokes it; A mints h2 and never revokes it. Returns the log.
func recordLifecycle(t *testing.T) (msgs []proto.Envelope, id1, id2 string) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "bt")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sess, data := filepath.Join(dir, "s"), filepath.Join(dir, "log")
	srv, err := server.New(server.Config{SocketPath: sess, DataDir: data, EnforceCaps: true})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	srv.AddDomain(domA, filepath.Join(dir, "a"))
	srv.AddDomain(domB, filepath.Join(dir, "b"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	svc, _ := client.Dial(sess)
	svc.Register(ctx, "svc")
	go func() {
		for r := range svc.Requests() {
			svc.Reply(r, json.RawMessage(`{"ok":true}`))
		}
	}()
	a, _ := client.Dial(filepath.Join(dir, "a"))
	b, _ := client.Dial(filepath.Join(dir, "b"))
	mint := func() (string, string) {
		raw, err := a.Control(ctx, "bus.mint@v1", map[string]any{"resource": "doc"})
		if err != nil {
			t.Fatal(err)
		}
		var m struct{ Handle, ID string }
		json.Unmarshal(raw, &m)
		return m.Handle, m.ID
	}
	use := func(c *client.Client, h string) {
		if _, err := c.Call(ctx, proto.Envelope{To: "svc", Verb: "use@v1", Caps: []string{h}}); err != nil {
			t.Fatal(err)
		}
	}
	h1, id1 := mint()
	use(a, h1)
	if _, err := a.Control(ctx, "bus.transfer@v1", map[string]string{"handle": h1, "to": domB}); err != nil {
		t.Fatal(err)
	}
	use(b, h1)
	if _, err := a.Control(ctx, "bus.revoke@v1", map[string]string{"id": id1}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Control(ctx, "bus.revoke@v1", map[string]string{"id": id1}); err != nil { // idempotent, says nothing
		t.Fatal(err)
	}
	_, id2 = mint()
	b.Flush(ctx)
	for _, c := range []*client.Client{svc, a, b} {
		c.Close()
	}
	srv.Close()
	msgs, err = buslog.ReadFile(data)
	if err != nil {
		t.Fatal(err)
	}
	return
}

func lifecycleCheck(t *testing.T, msgs []proto.Envelope) replay.CheckResult {
	t.Helper()
	r := replay.Check(msgs, faithful(msgs), nil)
	return r.Checks[len(r.Checks)-1]
}

func faithful(msgs []proto.Envelope) []proto.Envelope {
	var out []proto.Envelope
	for _, m := range msgs {
		c := m
		c.Origin = &proto.Origin{ID: m.ID, From: m.From, TS: m.TS}
		out = append(out, c)
	}
	return out
}

func TestLifecycleIsLoggedAndPassesReplay(t *testing.T) {
	msgs, id1, id2 := recordLifecycle(t)
	var topics []string
	for _, e := range msgs {
		if strings.HasPrefix(e.Topic, "bus.cap.") {
			topics = append(topics, e.Topic+" "+e.From)
			if strings.Contains(string(e.Payload), "cap_") {
				t.Fatalf("a token reached the log: %s", e.Payload)
			}
		}
		if e.Kind == proto.KindRequest && e.To == proto.BusTarget {
			t.Fatalf("control verb %s was logged: replay would re-run it", e.Verb)
		}
	}
	want := "bus.cap.minted bus,bus.cap.transferred bus,bus.cap.revoked bus,bus.cap.minted bus"
	if got := strings.Join(topics, ","); got != want {
		t.Fatalf("lifecycle events = %s, want %s (second revoke must not log)", got, want)
	}
	// The recorded identity is the connection's, not a claim.
	for _, e := range msgs {
		if e.Topic == "bus.cap.revoked" {
			var p struct{ ID, By string }
			json.Unmarshal(e.Payload, &p)
			if p.ID != id1 || p.By != domA {
				t.Fatalf("revoke payload = %s", e.Payload)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := replay.RunCheck(ctx, msgs, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Report.Checks {
		t.Logf("%s ok=%v %s", c.Name, c.OK, c.Detail)
	}
	if !res.Report.OK {
		t.Fatal("replay check failed on a real session")
	}
	last := res.Report.Checks[len(res.Report.Checks)-1]
	if last.Name != "capability lifecycle" || !strings.Contains(last.Detail, "2 minted, 1 transferred, 1 revoked, 1 never revoked") {
		t.Fatalf("lifecycle check = %+v", last)
	}
	_ = id2
}

// Every tamper is applied to a consistent copy of a real log, so the replay
// reproduces it faithfully and only the lifecycle check can object.
func TestLifecycleCheckDetectsTampering(t *testing.T) {
	good, id1, _ := recordLifecycle(t)
	if c := lifecycleCheck(t, good); !c.OK {
		t.Fatalf("good log must pass: %+v", c)
	}
	clone := func() []proto.Envelope { return append([]proto.Envelope(nil), good...) }
	idx := func(topic string, n int) int {
		for i, e := range good {
			if e.Topic == topic {
				if n == 0 {
					return i
				}
				n--
			}
		}
		t.Fatalf("no %s #%d", topic, n)
		return -1
	}
	cases := map[string]struct {
		mutate func([]proto.Envelope) []proto.Envelope
		want   string
	}{
		"revoke removed then used after": {func(m []proto.Envelope) []proto.Envelope {
			// move the revoke before B's use of the handle
			r := m[idx("bus.cap.revoked", 0)]
			m = append(m[:idx("bus.cap.revoked", 0)], m[idx("bus.cap.revoked", 0)+1:]...)
			at := idx("bus.cap.transferred", 0) + 1
			return append(m[:at], append([]proto.Envelope{r}, m[at:]...)...)
		}, "after it was revoked"},
		"mint deleted": {func(m []proto.Envelope) []proto.Envelope {
			i := idx("bus.cap.minted", 0)
			return append(m[:i], m[i+1:]...)
		}, "never minted"},
		"revoke by a stranger": {func(m []proto.Envelope) []proto.Envelope {
			i := idx("bus.cap.revoked", 0)
			m[i].Payload = json.RawMessage(strings.Replace(string(m[i].Payload), domA, domB, 1))
			return m
		}, "but " + domA + " minted it"},
		"event forged by a client": {func(m []proto.Envelope) []proto.Envelope {
			i := idx("bus.cap.revoked", 0)
			m[i].From = domB
			return m
		}, "not by the daemon"},
		"transfer after revoke": {func(m []proto.Envelope) []proto.Envelope {
			tr := m[idx("bus.cap.transferred", 0)]
			return append(m, tr)
		}, "after it was revoked"},
		"use by someone who never held it": {func(m []proto.Envelope) []proto.Envelope {
			e := proto.Envelope{ID: "x1", From: "domain:evil:1", Kind: proto.KindEvent, Topic: "t", CapIDs: []string{id1}}
			at := idx("bus.cap.minted", 0) + 1
			return append(m[:at], append([]proto.Envelope{e}, m[at:]...)...)
		}, "held by " + domA},
		"revoke twice": {func(m []proto.Envelope) []proto.Envelope {
			return append(m, m[idx("bus.cap.revoked", 0)])
		}, "revoked twice"},
	}
	for name, c := range cases {
		got := lifecycleCheck(t, c.mutate(clone()))
		if got.OK || !strings.Contains(got.Detail, c.want) {
			t.Errorf("%s: want failure containing %q, got %+v", name, c.want, got)
		}
	}
}
