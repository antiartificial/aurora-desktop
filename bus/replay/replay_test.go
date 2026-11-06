package replay_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/replay"
	"github.com/antiartificial/aurora/bus/server"
)

// record runs a real session on a daemon and returns its log directory.
func record(t *testing.T) string {
	t.Helper()
	dir, _ := os.MkdirTemp("", "bt")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock, data := filepath.Join(dir, "s"), filepath.Join(dir, "log")
	srv, err := server.New(server.Config{SocketPath: sock, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	svc, _ := client.Dial(sock)
	svc.Register(ctx, "calc")
	go func() {
		for r := range svc.Requests() {
			svc.Reply(r, json.RawMessage(`{"sum":3}`))
		}
	}()
	a, _ := client.Dial(sock)
	a.Subscribe(ctx, "demo.*")
	b, _ := client.Dial(sock)
	b.Publish("demo.one", json.RawMessage(`{"n":1}`), "", false)
	b.Send(proto.Envelope{Kind: proto.KindEvent, Topic: "demo.grant", Caps: []string{"handle-A"}, Payload: json.RawMessage(`{"n":2}`)})
	if _, err := b.Call(ctx, proto.Envelope{To: "calc", Verb: "add@v1", Payload: json.RawMessage(`{"a":1,"b":2}`), Caps: []string{"handle-B"}}); err != nil {
		t.Fatal(err)
	}
	b.Publish("demo.two", json.RawMessage(`{"n":3}`), proto.QoSBackground, false)
	b.Publish("demo.ptr", json.RawMessage(`{"n":4}`), "", true) // ephemeral: not recorded
	b.Flush(ctx)
	a.Flush(ctx)
	for _, c := range []*client.Client{svc, a, b} {
		c.Close()
	}
	srv.Close()
	return data
}

func TestReplayInvariantsPassOnRecordedSession(t *testing.T) {
	msgs, err := buslog.ReadFile(record(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 { // 3 events + request + response; ephemeral excluded
		t.Fatalf("recorded %d messages, want 5", len(msgs))
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
	if !res.Report.OK || res.Delivered != 5 {
		t.Fatalf("report = %+v delivered %d", res.Report, res.Delivered)
	}
}

func TestCheckDetectsViolations(t *testing.T) {
	orig, _ := buslog.ReadFile(record(t))
	mk := func() []proto.Envelope { // a faithful "replayed" log
		var out []proto.Envelope
		for _, m := range orig {
			c := m
			c.Origin = &proto.Origin{ID: m.ID, From: m.From, TS: m.TS}
			out = append(out, c)
		}
		return out
	}
	var delivered []proto.Envelope
	for _, m := range mk() {
		if m.Kind == proto.KindEvent {
			delivered = append(delivered, m)
		}
	}
	if r := replay.Check(orig, mk(), delivered); !r.OK {
		t.Fatalf("faithful replay must pass: %+v", r)
	}
	// Request/response swapped: causal order must fail.
	swapped := mk()
	for i := range swapped {
		if swapped[i].Kind == proto.KindRequest {
			swapped[i], swapped[i+1] = swapped[i+1], swapped[i]
			break
		}
	}
	if r := replay.Check(orig, swapped, delivered); r.OK || r.Checks[0].OK {
		t.Fatal("swap must break causal order")
	}
	// A subscriber missing the last event: final state must fail.
	if r := replay.Check(orig, mk(), delivered[:len(delivered)-1]); r.OK || r.Checks[1].OK {
		t.Fatal("missing delivery must break final state")
	}
	// A dropped capability grant: grants must fail.
	dropped := mk()
	for i := range dropped {
		dropped[i].CapIDs = nil
	}
	if r := replay.Check(orig, dropped, delivered); r.OK || r.Checks[2].OK {
		t.Fatal("lost grants must be detected")
	}
}
