package launcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/server"
)

// QueryWatch over a real busd against a stand-in "index" service: it must take
// the snapshot's as_of_seq as its first cursor, send each reply's cursor back,
// never subscribe to any index.match.* topic, and stop when its context ends.
func TestQueryWatchLongPollsFromTheSnapshotCursor(t *testing.T) {
	d, _ := os.MkdirTemp("", "qw")
	t.Cleanup(func() { os.RemoveAll(d) })
	sock := filepath.Join(d, "b.sock")
	srv, err := server.New(server.Config{SocketPath: sock, DataDir: filepath.Join(d, "log")})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	var svc *client.Client
	for i := 0; i < 100 && svc == nil; i++ {
		if svc, err = client.Dial(sock); err != nil {
			svc = nil
			time.Sleep(20 * time.Millisecond)
		}
	}
	if svc == nil {
		t.Fatal("dial")
	}
	t.Cleanup(func() { svc.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := svc.Register(ctx, "index"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var afters []int
	var inspected string
	spy, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer spy.Close()
	go func() {
		for r := range svc.Requests() {
			var p struct {
				After  int
				WaitMS int `json:"wait_ms"`
			}
			json.Unmarshal(r.Payload, &p)
			switch r.Verb {
			case "index.run@v1":
				svc.Reply(r, []byte(`{"query":{"name":"q"},"hits":[],"total":0,"as_of_seq":7}`))
			case "index.next@v1":
				mu.Lock()
				afters = append(afters, p.After)
				mu.Unlock()
				switch p.After {
				case 7: // the snapshot's cursor: two new writes, the second beyond what was asked
					svc.Reply(r, []byte(`{"hits":[{"seq":9,"file":"/a.png"}],"as_of_seq":9}`))
				case 9:
					raw, _ := spy.Control(ctx, "bus.inspect@v1", nil)
					mu.Lock()
					inspected = string(raw)
					mu.Unlock()
					svc.Reply(r, []byte(`{"hits":[],"as_of_seq":12}`))
				default:
					svc.Reply(r, []byte(`{"hits":[{"seq":13,"file":"/b.png"}],"as_of_seq":13}`))
				}
			}
		}
	}()
	be := DialBackend(sock, 2*time.Second).(*busBackend)
	defer be.Close()
	var got []string
	wctx, wcancel := context.WithCancel(ctx)
	snapped := false
	err = be.QueryWatch(wctx, "q", func(QueryRun) { snapped = true }, func(from string, h QueryHit) {
		got = append(got, h.File)
		if from == "" {
			t.Error("live hit carried no bus-stamped sender")
		}
		if len(got) == 2 {
			wcancel()
		}
	})
	if err != nil || !snapped {
		t.Fatalf("watch: %v snapped=%v", err, snapped)
	}
	if len(got) != 2 || got[0] != "/a.png" || got[1] != "/b.png" {
		t.Errorf("hits %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(afters) < 3 || afters[0] != 7 || afters[1] != 9 || afters[2] != 12 {
		t.Errorf("cursors sent: %v, want 7, 9, 12", afters)
	}
	if !strings.Contains(inspected, `"name":"index"`) || strings.Contains(inspected, "index.match") {
		t.Errorf("while a watch was live, the bus's view was: %s", inspected)
	}
}
