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
	"github.com/antiartificial/aurora/bus/shm"
)

const marker = "REPLAY-SEGMENT-MARKER-91c2"

// recordSegments records a session in which an event and a request each carry
// a segment, and returns the log dir.
func recordSegments(t *testing.T) string {
	t.Helper()
	if err := shm.Supported(); err != nil {
		t.Skip(err)
	}
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
	svc.Register(ctx, "thumb")
	go func() {
		for r := range svc.Requests() {
			svc.Reply(r, json.RawMessage(`{"ok":true}`))
			r.CloseAttachments()
		}
	}()
	sub, _ := client.Dial(sock)
	sub.Subscribe(ctx, "img.*")
	go func() {
		for e := range sub.Events() {
			e.CloseAttachments()
		}
	}()
	b, _ := client.Dial(sock)
	data1 := []byte(strings.Repeat("p", 1<<20) + marker)
	ev := proto.Envelope{Kind: proto.KindEvent, Topic: "img.ready", Payload: json.RawMessage(`{"n":1}`)}
	client.AttachBytes(&ev, "frame", "image/png", data1)
	b.Send(ev)
	req := proto.Envelope{To: "thumb", Verb: "make@v1"}
	client.AttachBytes(&req, "src", "image/png", data1[:4096])
	if _, err := b.Call(ctx, req); err != nil {
		t.Fatal(err)
	}
	b.Publish("img.plain", json.RawMessage(`{"n":2}`), "", false)
	b.Flush(ctx)
	sub.Flush(ctx)
	for _, c := range []*client.Client{svc, sub, b} {
		c.Close()
	}
	srv.Close()
	return data
}

func TestReplayWithSegmentsPassesAndComparesHashes(t *testing.T) {
	data := recordSegments(t)
	msgs, err := buslog.ReadFile(data)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(msgs)
	if strings.Contains(string(raw), marker) {
		t.Fatal("log contains segment contents")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := replay.RunCheck(ctx, msgs, 0)
	if err != nil {
		t.Fatal(err)
	}
	var seg *replay.CheckResult
	for i, c := range res.Report.Checks {
		t.Logf("%s ok=%v %s", c.Name, c.OK, c.Detail)
		if c.Name == "segment descriptors" {
			seg = &res.Report.Checks[i]
		}
	}
	if !res.Report.OK || seg == nil || !seg.OK || !strings.Contains(seg.Detail, "2 segments") {
		t.Fatalf("untampered replay with segments must pass: %+v", res.Report)
	}
}

func TestReplayCheckFailsWhenSegmentHashOrDescriptorChanges(t *testing.T) {
	msgs, err := buslog.ReadFile(recordSegments(t))
	if err != nil {
		t.Fatal(err)
	}
	if r := replay.Check(msgs, replayedOf(t, msgs), deliveredOf(msgs)); !r.OK {
		t.Fatalf("baseline must pass: %+v", r)
	}
	for name, mut := range map[string]func(s *proto.Segment){
		"hash":  func(s *proto.Segment) { s.SHA256 = "sha256:" + strings.Repeat("0", 64) },
		"size":  func(s *proto.Segment) { s.Bytes++ },
		"mime":  func(s *proto.Segment) { s.Mime = "text/plain" },
		"drop":  func(s *proto.Segment) { s.ID = "other" },
		"seals": func(s *proto.Segment) { s.Sealed = false },
	} {
		// The original (reference) log is tampered; what the replay daemon
		// recorded and delivered stays faithful to the true log.
		tampered := append([]proto.Envelope(nil), msgs...)
		for i := range tampered {
			if len(tampered[i].Segments) > 0 {
				tampered[i].Segments = append([]proto.Segment(nil), tampered[i].Segments...)
				mut(&tampered[i].Segments[0])
				break
			}
		}
		rep := replayedOf(t, msgs)
		r := replay.Check(tampered, rep, deliveredOf(msgs))
		if r.OK {
			t.Errorf("tampering with segment %s was not detected", name)
		}
	}
}

// replayedOf builds what a faithful replay daemon would have logged.
func replayedOf(t *testing.T, msgs []proto.Envelope) []proto.Envelope {
	var out []proto.Envelope
	for _, m := range msgs {
		c := m
		c.Origin = &proto.Origin{ID: m.ID, From: m.From, TS: m.TS}
		c.Segments = append([]proto.Segment(nil), m.Segments...)
		for i := range c.Segments {
			c.Segments[i].Absent = true
		}
		out = append(out, c)
	}
	return out
}

func deliveredOf(msgs []proto.Envelope) []proto.Envelope {
	var out []proto.Envelope
	for _, m := range msgs {
		if m.Kind == proto.KindEvent {
			out = append(out, m)
		}
	}
	return out
}
