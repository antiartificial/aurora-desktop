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

// Minted handles, presented and transferred under enforcement, must pass the
// replay invariant check (grants are compared by identity), and the log must
// never hold a token.
func TestMintedHandlesSurviveReplayCheck(t *testing.T) {
	dir, _ := os.MkdirTemp("", "bt")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sess, data := filepath.Join(dir, "s"), filepath.Join(dir, "log")
	const idA, idB = "domain:alpha:00112233aabbccdd", "domain:beta:ffeeddcc99887766"
	srv, err := server.New(server.Config{SocketPath: sess, DataDir: data, EnforceCaps: true})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	srv.AddDomain(idA, filepath.Join(dir, "a"))
	srv.AddDomain(idB, filepath.Join(dir, "b"))
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
	raw, err := a.Control(ctx, "bus.mint@v1", map[string]any{"resource": "doc"})
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ Handle string }
	json.Unmarshal(raw, &m)
	use := func(c *client.Client) {
		if _, err := c.Call(ctx, proto.Envelope{To: "svc", Verb: "use@v1", Caps: []string{m.Handle}}); err != nil {
			t.Fatal(err)
		}
	}
	use(a)
	a.Control(ctx, "bus.transfer@v1", map[string]string{"handle": m.Handle, "to": idB})
	use(b)
	b.Flush(ctx)
	for _, c := range []*client.Client{svc, a, b} {
		c.Close()
	}
	srv.Close()

	files, _ := filepath.Glob(filepath.Join(data, "*"))
	for _, f := range files {
		if bs, _ := os.ReadFile(f); strings.Contains(string(bs), m.Handle) {
			t.Fatal("log contains the token")
		}
	}
	msgs, err := buslog.ReadFile(data)
	if err != nil {
		t.Fatal(err)
	}
	grants := 0
	for _, e := range msgs {
		if len(e.CapIDs) == 1 && e.CapIDs[0] == buslog.CapID(m.Handle) {
			grants++
		}
	}
	if grants != 2 {
		t.Fatalf("log has %d messages carrying the handle id, want 2", grants)
	}
	res, err := replay.RunCheck(ctx, msgs, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Report.Checks {
		t.Logf("%s ok=%v %s", c.Name, c.OK, c.Detail)
	}
	if !res.Report.OK {
		t.Fatal("replay check failed")
	}
}
