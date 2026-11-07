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
	"github.com/antiartificial/aurora/bus/server"
)

// A log in which a lease is presented after its request was answered is a log
// the daemon could not have written; the check must say so, and must pass the
// log that really was written.
func TestLifecycleCheckKnowsALeaseEndsAtReply(t *testing.T) {
	dir, _ := os.MkdirTemp("", "bt")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sess, data := filepath.Join(dir, "s"), filepath.Join(dir, "log")
	srv, err := server.New(server.Config{SocketPath: sess, DataDir: data, EnforceCaps: true})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	srv.AddDomain(domA, filepath.Join(dir, "a"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	svc, _ := client.Dial(sess)
	svc.Register(ctx, "svc")
	go func() {
		for r := range svc.Requests() {
			svc.Reply(r, json.RawMessage(`{}`))
		}
	}()
	a, _ := client.Dial(filepath.Join(dir, "a"))
	raw, err := a.Control(ctx, "bus.mint@v1", map[string]any{"resource": "session:clipboard", "rights": "read,grant"})
	if err != nil {
		t.Fatal(err)
	}
	var root struct{ Handle string }
	json.Unmarshal(raw, &root)
	raw, err = a.Control(ctx, "bus.lease@v1", map[string]any{"handle": root.Handle, "rights": "read"})
	if err != nil {
		t.Fatal(err)
	}
	var l struct{ Handle, ID string }
	json.Unmarshal(raw, &l)
	if _, err := a.Call(ctx, proto.Envelope{To: "svc", Verb: "read@v1", Caps: []string{l.Handle}}); err != nil {
		t.Fatal(err)
	}
	a.Flush(ctx)
	svc.Close()
	a.Close()
	srv.Close()
	good, err := buslog.ReadFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if c := lifecycleCheck(t, good); !c.OK {
		t.Fatalf("a real lease lifecycle must pass: %+v", c)
	}

	bad := append([]proto.Envelope(nil), good...)
	bad = append(bad, proto.Envelope{ID: "x1", From: domA, Kind: proto.KindRequest, To: "svc", CapIDs: []string{l.ID}})
	if c := lifecycleCheck(t, bad); c.OK || !strings.Contains(c.Detail, "after its request was answered") {
		t.Fatalf("reuse of an ended lease must fail: %+v", c)
	}
}
