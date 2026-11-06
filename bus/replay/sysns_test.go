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

// System-namespace events are withheld from domain connections, but the replay
// probe is a session connection, so it still sees them and needs no exemption.
// A log holding provenance.created and approval.requested, recorded with a
// domain subscriber on ** present, must pass the check, and must still fail
// when one of those payloads is altered.
func TestReplayOfSystemTopicsPassesAndTamperingStillFails(t *testing.T) {
	dir, _ := os.MkdirTemp("", "bt")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock, data := filepath.Join(dir, "s"), filepath.Join(dir, "log")
	srv, err := server.New(server.Config{SocketPath: sock, DataDir: data})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	if err := srv.AddDomain("domain:alpha:00112233aabbccdd", filepath.Join(dir, "d")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d, _ := client.Dial(filepath.Join(dir, "d"))
	d.Subscribe(ctx, "**")
	p, _ := client.Dial(sock)
	p.Publish("provenance.created", json.RawMessage(`{"file":"/a.png"}`), "", false)
	p.Publish("approval.requested", json.RawMessage(`{"id":"x"}`), "", false)
	p.Publish("demo.one", json.RawMessage(`{"n":1}`), "", false)
	p.Flush(ctx)
	d.Close()
	p.Close()
	srv.Close()

	msgs, err := buslog.ReadFile(data)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("recorded %d messages (%v), want 3", len(msgs), err)
	}
	res, err := replay.RunCheck(ctx, msgs, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Report.OK || res.Delivered != 3 {
		t.Fatalf("good session must pass: %+v delivered %d", res.Report, res.Delivered)
	}

	// RunCheck compares a log with its own replay, so altering the log first
	// proves nothing. Tamper with what was delivered instead, as the existing
	// tamper tests do: the check must name the system topic that diverged.
	var replayed []proto.Envelope
	for _, m := range msgs {
		c := m
		c.Origin = &proto.Origin{ID: m.ID, From: m.From, TS: m.TS}
		replayed = append(replayed, c)
	}
	if r := replay.Check(msgs, replayed, replayed); !r.OK {
		t.Fatalf("untampered stream must pass: %+v", r)
	}
	delivered := append([]proto.Envelope(nil), replayed...)
	delivered[0].Payload = json.RawMessage(`{"file":"/tampered.png"}`)
	if r := replay.Check(msgs, replayed, delivered); r.OK {
		t.Fatalf("tampered system-topic payload passed the check: %+v", r)
	}
}
