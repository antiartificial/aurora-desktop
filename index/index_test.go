package index

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/server"
)

func TestParseQueryStrict(t *testing.T) {
	good := "# c\ntitle: T\nmime: image/*\nmime: image/png\nsince: this-week\napp: a.b\n"
	q, err := ParseQuery("imgs", good)
	if err != nil || len(q.MIME) != 2 || q.Since != "this-week" || q.Title != "T" {
		t.Fatalf("%+v %v", q, err)
	}
	for _, c := range [][2]string{
		{"Bad.Name", "ext: png"}, {"typo", "mymetype: image/png"}, {"empty", "title: nothing"},
		{"since2", "since: today\nsince: 7d"}, {"bsince", "since: fortnight"},
		{"relpath", "under: pics"}, {"nocolon", "ext png"}, {"badmime", "mime: image"},
	} {
		if _, err := ParseQuery(c[0], c[1]); err == nil {
			t.Errorf("%s: parsed, want error", c[0])
		}
	}
}

func TestMatch(t *testing.T) {
	now := time.Date(2026, 11, 6, 15, 0, 0, 0, time.Local) // a Friday
	e := &Entry{File: "/home/u/Pictures/a.PNG", App: "org.aurora.magick", Verb: "convert@v1",
		Inputs: []string{"/home/u/Pictures/photo.png"}, ObservedAt: now.Add(-time.Hour)}
	cases := []struct {
		q    string
		want bool
	}{
		{"mime: image/*", true}, {"mime: image/jpeg", false}, {"ext: .png", true},
		{"app: org.aurora.magick\nverb: convert", true}, {"verb: convert@v2", false},
		{"verb: resize\nverb: convert", true}, {"app: other", false},
		{"under: /home/u/Pictures", true}, {"under: /home/u/Pic", false},
		{"derived-from: /home/u/Pictures/photo.png", true}, {"derived-from: /nope", false},
		{"since: today", true}, {"since: this-week", true}, {"since: 30m", false}, {"since: 2h", true},
		{"mime: image/*\nsince: this-week\napp: org.aurora.magick", true},
		{"mime: audio/*\nsince: this-week", false}, // AND across keys
	}
	for _, c := range cases {
		q, err := ParseQuery("q", c.q)
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		if got := q.Match(e, now); got != c.want {
			t.Errorf("%q: got %v want %v", c.q, got, c.want)
		}
	}
	// Last week's file is outside this-week (Monday 2026-11-02) but inside 7d.
	old := *e
	old.ObservedAt = time.Date(2026, 11, 1, 12, 0, 0, 0, time.Local)
	qw, _ := ParseQuery("q", "since: this-week")
	q7, _ := ParseQuery("q", "since: 7d")
	if qw.Match(&old, now) || !q7.Match(&old, now) {
		t.Error("week boundary wrong")
	}
	if MIMEOf("/x/y.png.txt") != "text/plain" || MIMEOf("/x/noext") != "application/octet-stream" {
		t.Error("MIMEOf")
	}
}

func prov(file string, inputs ...string) []byte {
	b, _ := json.Marshal(map[string]any{"file": file, "app": "org.test", "verb": "v@v1", "inputs": inputs, "ts": "T"})
	return b
}

func TestStorePersistsAndRelates(t *testing.T) {
	dir := t.TempDir()
	ix, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	add := func(id, file string, in ...string) {
		e, err := FromEvent("uid1:controllerd", id, "ts", prov(file, in...), now)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := ix.Add(e); !ok || err != nil {
			t.Fatalf("add %s: %v %v", id, ok, err)
		}
	}
	add("1", "/a/out.png", "/a/in.png")
	add("2", "/a/out2.png", "/a/in.png")
	add("3", "/a/out.png", "/a/other.png") // out.png rewritten from a different input
	if ok, _ := ix.Add(&Entry{File: "/a/out.png", BusID: "3"}); ok {
		t.Error("duplicate bus id was indexed twice")
	}
	if got := ix.UsedIn("/a/in.png"); len(got) != 1 || got[0] != "/a/out2.png" {
		t.Errorf("used_in uses a stale edge: %v", got)
	}
	if n := len(ix.Versions("/a/out.png")); n != 2 {
		t.Errorf("versions %d", n)
	}
	ix.Close()
	os.WriteFile(filepath.Join(dir, "index.jsonl"), append(mustRead(t, filepath.Join(dir, "index.jsonl")), []byte(`{"torn`)...), 0o600)
	ix2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ix2.Close()
	st := ix2.Stats()
	if st.Entries != 3 || st.Files != 2 || st.SkippedLines != 1 {
		t.Errorf("reload: %+v", st)
	}
	if _, err := FromEvent("uid1:controllerd", "9", "t", prov("relative.png"), now); err == nil {
		t.Error("relative path accepted")
	}
	if IsController("domain:app-x:11") || IsController("uid1:bash") || !IsController("uid1000:controllerd") {
		t.Error("IsController")
	}
}

func mustRead(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Over a real busd: an event from a trusted sender is indexed and published to
// the matching query's topic; an untrusted sender is counted and ignored; run
// and show answer; a query that does not parse is reported, not dropped.
func TestServiceOverBus(t *testing.T) {
	d, _ := os.MkdirTemp("", "ix")
	t.Cleanup(func() { os.RemoveAll(d) })
	sock := filepath.Join(d, "b.sock")
	srv, err := server.New(server.Config{SocketPath: sock, DataDir: filepath.Join(d, "log")})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	dial := func() *client.Client {
		for i := 0; i < 100; i++ {
			if c, err := client.Dial(sock); err == nil {
				t.Cleanup(func() { c.Close() })
				return c
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("dial")
		return nil
	}
	qd := filepath.Join(d, "q")
	os.MkdirAll(qd, 0o700)
	os.WriteFile(filepath.Join(qd, "pngs.query"), []byte("title: PNGs\next: png\nsince: today\n"), 0o600)
	os.WriteFile(filepath.Join(qd, "broken.query"), []byte("colour: red\n"), 0o600)
	ix, err := Open(filepath.Join(d, "idx"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	trusted := ""
	svc := &Service{Idx: ix, QueriesDir: qd, C: dial(), Logf: t.Logf, Controller: func(s string) bool { return trusted != "" && s == trusted }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)

	pub, cli := dial(), dial()
	if err := cli.Subscribe(ctx, MatchTopicPrefix+"pngs"); err != nil {
		t.Fatal(err)
	}
	call := func(verb string, p any) (map[string]json.RawMessage, error) {
		b, _ := json.Marshal(p)
		var r proto.Envelope
		var err error
		for i := 0; i < 100; i++ { // the service registers asynchronously
			r, err = cli.Call(ctx, proto.Envelope{To: ServiceName, Verb: verb, Payload: b})
			if re, ok := err.(*client.RemoteError); ok && re.Code == "no_such_service" {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			break
		}
		var m map[string]json.RawMessage
		if err == nil {
			json.Unmarshal(r.Payload, &m)
		}
		return m, err
	}
	if _, err := call("index.queries@v1", struct{}{}); err != nil {
		t.Fatal(err)
	}
	// Untrusted first: nothing indexed, nothing published.
	pub.Publish("provenance.created", prov("/x/a.png", "/x/in.png"), proto.QoSInitiated, false)
	time.Sleep(200 * time.Millisecond)
	if ix.Stats().Entries != 0 || ix.Stats().Rejected != 1 {
		t.Fatalf("untrusted sender indexed: %+v", ix.Stats())
	}
	// Learn our own bus-stamped name, trust it, publish again.
	probe := dial()
	probe.Subscribe(ctx, "provenance.created")
	pub.Publish("provenance.created", prov("/x/b.txt"), proto.QoSInitiated, false)
	ev := <-probe.Events()
	trusted = ev.From
	pub.Publish("provenance.created", prov("/x/c.png", "/x/in.png"), proto.QoSInitiated, false)
	select {
	case m := <-cli.Events():
		var me MatchEvent
		json.Unmarshal(m.Payload, &me)
		if m.Topic != "index.match.pngs" || me.Hit.File != "/x/c.png" || me.Hit.Sender != trusted || me.Hit.MIME != "image/png" {
			t.Fatalf("%s %+v", m.Topic, me)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no live match")
	}
	m, err := call("index.run@v1", map[string]string{"name": "pngs"})
	if err != nil {
		t.Fatal(err)
	}
	var total int
	json.Unmarshal(m["total"], &total)
	if total != 1 || len(m["notes"]) < 10 {
		t.Errorf("run: total %d notes %s", total, m["notes"])
	}
	if _, err := call("index.run@v1", map[string]string{"name": "broken"}); err == nil {
		t.Error("run of a broken query succeeded")
	}
	if _, err := call("index.run@v1", map[string]string{"name": "nope"}); err == nil {
		t.Error("run of a missing query succeeded")
	}
	m, _ = call("index.show@v1", map[string]string{"file": "/x/in.png"})
	var used []string
	json.Unmarshal(m["used_in"], &used)
	if len(used) != 1 || used[0] != "/x/c.png" {
		t.Errorf("used_in %v", used)
	}
	m, _ = call("index.queries@v1", struct{}{})
	if !jsonHas(m["queries"], `"error":"line 1: unknown key`) {
		t.Errorf("broken query not reported: %s", m["queries"])
	}
}

func jsonHas(b json.RawMessage, sub string) bool { return strings.Contains(string(b), sub) }
