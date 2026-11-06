package index

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	if got := ix.UsedIn("/a/in.png", nil); len(got) != 1 || got[0] != "/a/out2.png" {
		t.Errorf("used_in uses a stale edge: %v", got)
	}
	if vs, _ := ix.Versions("/a/out.png", nil); len(vs) != 2 {
		t.Errorf("versions %d", len(vs))
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

// startBus runs a real busd in a short temp dir and returns a dialler.
func startBus(t *testing.T) (*server.Server, string, func(path string) *client.Client) {
	d, _ := os.MkdirTemp("", "ix")
	t.Cleanup(func() { os.RemoveAll(d) })
	sock := filepath.Join(d, "b.sock")
	srv, err := server.New(server.Config{SocketPath: sock, DataDir: filepath.Join(d, "log")})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	dial := func(path string) *client.Client {
		for i := 0; i < 100; i++ {
			if c, err := client.Dial(path); err == nil {
				t.Cleanup(func() { c.Close() })
				return c
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("dial", path)
		return nil
	}
	return srv, d, dial
}

func callIndex(ctx context.Context, c *client.Client, verb string, p any) (map[string]json.RawMessage, error) {
	b, _ := json.Marshal(p)
	var r proto.Envelope
	var err error
	for i := 0; i < 100; i++ { // the service registers asynchronously
		r, err = c.Call(ctx, proto.Envelope{To: ServiceName, Verb: verb, Payload: b})
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

func hitFiles(t *testing.T, m map[string]json.RawMessage, key string) []string {
	var hs []Hit
	if err := json.Unmarshal(m[key], &hs); err != nil {
		t.Fatalf("%s: %v in %s", key, err, m[key])
	}
	var out []string
	for _, h := range hs {
		out = append(out, h.File)
	}
	return out
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			return false
		}
	}
	return true
}

// Over a real busd: an event from a trusted sender is indexed; an untrusted
// sender is counted and ignored; run, next and show answer; a query that does
// not parse is reported, not dropped; and no index.match.* event is published.
func TestServiceOverBus(t *testing.T) {
	_, d, dial := startBus(t)
	sock := filepath.Join(d, "b.sock")
	qd := filepath.Join(d, "q")
	os.MkdirAll(qd, 0o700)
	os.WriteFile(filepath.Join(qd, "pngs.query"), []byte("title: PNGs\next: png\nsince: today\n"), 0o600)
	os.WriteFile(filepath.Join(qd, "broken.query"), []byte("colour: red\n"), 0o600)
	ix, err := Open(filepath.Join(d, "idx"))
	if err != nil {
		t.Fatal(err)
	}
	defer ix.Close()
	var trusted atomic.Value
	trusted.Store("")
	svc := &Service{Idx: ix, QueriesDir: qd, C: dial(sock), Logf: t.Logf, Controller: func(s string) bool { t, _ := trusted.Load().(string); return t != "" && s == t }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)

	pub, cli, snoop := dial(sock), dial(sock), dial(sock)
	snoop.Subscribe(ctx, "**") // everything: a stand-in for any subscriber at all
	call := func(verb string, p any) (map[string]json.RawMessage, error) { return callIndex(ctx, cli, verb, p) }
	if _, err := call("index.queries@v1", struct{}{}); err != nil {
		t.Fatal(err)
	}
	// Untrusted first: nothing indexed.
	pub.Publish("provenance.created", prov("/x/a.png", "/x/in.png"), proto.QoSInitiated, false)
	time.Sleep(200 * time.Millisecond)
	if ix.Stats().Entries != 0 || ix.Stats().Rejected != 1 {
		t.Fatalf("untrusted sender indexed: %+v", ix.Stats())
	}
	// Learn our own bus-stamped name, trust it, publish again.
	probe := dial(sock)
	probe.Subscribe(ctx, "provenance.created")
	pub.Publish("provenance.created", prov("/x/b.txt"), proto.QoSInitiated, false)
	ev := <-probe.Events()
	trusted.Store(ev.From)
	m, err := call("index.run@v1", map[string]string{"name": "pngs"})
	if err != nil {
		t.Fatal(err)
	}
	var asOf int
	json.Unmarshal(m["as_of_seq"], &asOf)
	pub.Publish("provenance.created", prov("/x/c.png", "/x/in.png"), proto.QoSInitiated, false)
	m, err = call("index.next@v1", map[string]any{"name": "pngs", "after": asOf, "wait_ms": 3000})
	if err != nil {
		t.Fatal(err)
	}
	if got := hitFiles(t, m, "hits"); !sameSet(got, "/x/c.png") {
		t.Fatalf("live match: %v", got)
	}
	var cur int
	json.Unmarshal(m["as_of_seq"], &cur)
	if cur <= asOf {
		t.Errorf("cursor did not advance: %d -> %d", asOf, cur)
	}
	m, _ = call("index.next@v1", map[string]any{"name": "pngs", "after": cur, "wait_ms": 200})
	if got := hitFiles(t, m, "hits"); len(got) != 0 {
		t.Errorf("a repeat poll returned %v again", got)
	}
	m, err = call("index.run@v1", map[string]string{"name": "pngs"})
	if err != nil {
		t.Fatal(err)
	}
	var total int
	json.Unmarshal(m["total"], &total)
	if total != 1 || len(m["notes"]) < 10 || !jsonHas(m["viewer"], `"kind":"session"`) {
		t.Errorf("run: total %d viewer %s", total, m["viewer"])
	}
	if _, err := call("index.run@v1", map[string]string{"name": "broken"}); err == nil {
		t.Error("run of a broken query succeeded")
	}
	if _, err := call("index.next@v1", map[string]any{"name": "nope", "after": 0}); err == nil {
		t.Error("next of a missing query succeeded")
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
	// No index.match.* topic exists for a subscriber to hear.
	for {
		select {
		case e := <-snoop.Events():
			if strings.HasPrefix(e.Topic, "index.") {
				t.Fatalf("indexd published %s to a ** subscriber", e.Topic)
			}
			continue
		case <-time.After(200 * time.Millisecond):
		}
		break
	}
}

func provDomain(file, dom string, inputs ...string) []byte {
	b, _ := json.Marshal(map[string]any{"file": file, "app": "org.test", "verb": "v@v1", "inputs": inputs, "ts": "T", "domain": dom})
	return b
}

const (
	domA = "domain:app-alpha-1111111111111111:aaaaaaaa"
	domB = "domain:app-beta-2222222222222222:bbbbbbbb"
)

func TestViewerKinds(t *testing.T) {
	for who, want := range map[string]string{
		domA: "domain", "uid1000:bash": "session", "": "unknown", "domain:": "unknown", "bus": "unknown",
		"alice": "unknown", "uid:x": "unknown",
	} {
		if got := Viewer(who).Kind(); got != want {
			t.Errorf("%q: %s, want %s", who, got, want)
		}
	}
	e := &Entry{Domain: domA}
	if !Viewer(domA).Sees(e) || Viewer(domB).Sees(e) || !Viewer("uid1000:x").Sees(e) || Viewer("").Sees(e) || Viewer("bus").Sees(e) {
		t.Error("Sees")
	}
	if Viewer(domA).Sees(&Entry{}) || Viewer("domain:").Sees(&Entry{}) {
		t.Error("an entry with no domain is visible to a domain, or the empty domain identity sees it")
	}
}

// Adversarial: two real domain sockets on a real busd. Each domain sees only
// its own entries through run, next (parked and immediate) and show; naming the
// other domain in the payload changes nothing; the session sees everything; a
// file overwritten by the other domain stops being yours and its on-disk size
// is not reported to you.
func TestDomainsSeeOnlyTheirOwn(t *testing.T) {
	srv, d, dial := startBus(t)
	sockA, sockB := filepath.Join(d, "a.sock"), filepath.Join(d, "b2.sock")
	if err := srv.AddDomain(domA, sockA); err != nil {
		t.Fatal(err)
	}
	if err := srv.AddDomain(domB, sockB); err != nil {
		t.Fatal(err)
	}
	qd := filepath.Join(d, "q")
	os.MkdirAll(qd, 0o700)
	os.WriteFile(filepath.Join(qd, "pngs.query"), []byte("ext: png\n"), 0o600)
	ix, _ := Open(filepath.Join(d, "idx"))
	defer ix.Close()
	svc := &Service{Idx: ix, QueriesDir: qd, C: dial(filepath.Join(d, "b.sock")), Logf: t.Logf, Controller: func(string) bool { return true }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)
	user, a, b, pub := dial(filepath.Join(d, "b.sock")), dial(sockA), dial(sockB), dial(filepath.Join(d, "b.sock"))
	snoopA := dial(sockA)
	snoopA.Subscribe(ctx, "index.**")
	snoopA.Subscribe(ctx, "**")
	// A wrote /a/secret.png from /a/in.png; B wrote /b/theirs.png; a record with no domain.
	for _, p := range [][]byte{provDomain("/a/own.png", domA, "/a/in.png"), provDomain("/b/theirs.png", domB, "/a/in.png"), provDomain("/n/nodomain.png", "")} {
		pub.Publish("provenance.created", p, proto.QoSInitiated, false)
	}
	for ix.Stats().Entries < 3 {
		time.Sleep(10 * time.Millisecond)
	}
	run := func(c *client.Client, extra map[string]any) map[string]json.RawMessage {
		p := map[string]any{"name": "pngs"}
		for k, v := range extra {
			p[k] = v
		}
		m, err := callIndex(ctx, c, "index.run@v1", p)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if got := hitFiles(t, run(user, nil), "hits"); !sameSet(got, "/a/own.png", "/b/theirs.png", "/n/nodomain.png") {
		t.Errorf("the session should see all three: %v", got)
	}
	ma, mb := run(a, nil), run(b, nil)
	if got := hitFiles(t, ma, "hits"); !sameSet(got, "/a/own.png") {
		t.Errorf("domain A saw %v", got)
	}
	if got := hitFiles(t, mb, "hits"); !sameSet(got, "/b/theirs.png") {
		t.Errorf("domain B saw %v", got)
	}
	if !jsonHas(ma["viewer"], `"identity":"`+domA+`"`) || !jsonHas(ma["stats"], `"entries":1,`) {
		t.Errorf("viewer %s stats %s", ma["viewer"], ma["stats"])
	}
	for _, leak := range []string{"/b/theirs.png", "nodomain", domB} {
		if jsonHas(ma["hits"], leak) || jsonHas(ma["stats"], leak) {
			t.Errorf("domain A's reply mentions %q", leak)
		}
	}
	// Asking for the other domain's view by name does nothing.
	forge := map[string]any{"domain": domB, "as": domB, "from": domB, "viewer": domB, "identity": domB, "caller": domB, "kind": "session"}
	if got := hitFiles(t, run(a, forge), "hits"); !sameSet(got, "/a/own.png") {
		t.Errorf("a payload naming domain B changed domain A's view: %v", got)
	}
	// show: B's file looks exactly like a file nobody indexed; and used_in is filtered.
	show := func(c *client.Client, f string) map[string]json.RawMessage {
		m, err := callIndex(ctx, c, "index.show@v1", map[string]string{"file": f})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if got := hitFiles(t, show(a, "/b/theirs.png"), "versions"); len(got) != 0 {
		t.Errorf("A shown B's file: %v", got)
	}
	if got := hitFiles(t, show(user, "/b/theirs.png"), "versions"); len(got) != 1 {
		t.Errorf("the session cannot see it either: %v", got)
	}
	var used []string
	json.Unmarshal(show(a, "/a/in.png")["used_in"], &used)
	if len(used) != 1 || used[0] != "/a/own.png" {
		t.Errorf("A's used_in for the shared input lists %v (B's output must not appear)", used)
	}
	// next: immediate, and parked. B writes first: A's parked poll must not be answered by it.
	after := 0
	json.Unmarshal(ma["as_of_seq"], &after)
	type res struct {
		files []string
		err   error
	}
	park := func(c *client.Client) chan res {
		ch := make(chan res, 1)
		go func() {
			m, err := callIndex(ctx, c, "index.next@v1", map[string]any{"name": "pngs", "after": after, "wait_ms": 3000})
			var r res
			if err == nil {
				r.files = hitFiles(t, m, "hits")
			}
			r.err = err
			ch <- r
		}()
		return ch
	}
	if m, _ := callIndex(ctx, a, "index.next@v1", map[string]any{"name": "pngs", "after": 0}); !sameSet(hitFiles(t, m, "hits"), "/a/own.png") {
		t.Errorf("A's immediate next replay: %s", m["hits"])
	}
	pa, pb := park(a), park(b)
	time.Sleep(150 * time.Millisecond)
	pub.Publish("provenance.created", provDomain("/b/new.png", domB), proto.QoSInitiated, false)
	if r := <-pb; r.err != nil || !sameSet(r.files, "/b/new.png") {
		t.Errorf("B's parked poll: %+v", r)
	}
	select {
	case r := <-pa:
		t.Fatalf("A's parked poll was answered by B's file: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
	pub.Publish("provenance.created", provDomain("/a/new.png", domA), proto.QoSInitiated, false)
	if r := <-pa; r.err != nil || !sameSet(r.files, "/a/new.png") {
		t.Errorf("A's parked poll: %+v", r)
	}
	// A parked poll that times out is answered empty, with the cursor moved past what it scanned.
	m, err := callIndex(ctx, a, "index.next@v1", map[string]any{"name": "pngs", "after": 0, "wait_ms": 100})
	if err != nil || len(hitFiles(t, m, "hits")) != 2 { // immediate: own.png and new.png
		t.Errorf("A replay: %v %s", err, m["hits"])
	}
	var cur int
	json.Unmarshal(m["as_of_seq"], &cur)
	if cur != ix.LastSeq() {
		t.Errorf("cursor %d, index at %d: A's poll would rescan other domains' entries forever", cur, ix.LastSeq())
	}
	// B overwrites A's file: it stops being A's, and A is not told its size.
	os.MkdirAll(filepath.Join(d, "f"), 0o700)
	shared := filepath.Join(d, "f", "shared.png")
	pub.Publish("provenance.created", provDomain(shared, domA), proto.QoSInitiated, false)
	for ix.Stats().Entries < 6 {
		time.Sleep(10 * time.Millisecond)
	}
	os.WriteFile(shared, []byte("A"), 0o600)
	if m := show(a, shared); len(hitFiles(t, m, "versions")) != 1 || !jsonHas(m["versions"], `"exists_now"`) {
		t.Errorf("A should see its own live file with a presence mark: %s", m["versions"])
	}
	pub.Publish("provenance.created", provDomain(shared, domB), proto.QoSInitiated, false)
	for ix.Stats().Entries < 7 {
		time.Sleep(10 * time.Millisecond)
	}
	if m := show(a, shared); len(hitFiles(t, m, "versions")) != 1 || jsonHas(m["versions"], `"exists_now"`) {
		t.Errorf("after B overwrote it, A may see its own old write but not the file's present state: %s", m["versions"])
	}
	for _, f := range hitFiles(t, run(a, nil), "hits") {
		if f == shared {
			t.Errorf("a file B overwrote is still in A's query")
		}
	}
	// Nothing about any of this reached a domain subscriber of ** except the broadcast
	// provenance.created, which this slice does not (cannot) filter.
	sawProv := false
	for {
		select {
		case e := <-snoopA.Events():
			if strings.HasPrefix(e.Topic, "index.") {
				t.Fatalf("domain A's ** subscription received %s", e.Topic)
			}
			sawProv = sawProv || (e.Topic == "provenance.created" && strings.Contains(string(e.Payload), "/b/theirs.png"))
			continue
		case <-time.After(200 * time.Millisecond):
		}
		break
	}
	if !sawProv {
		t.Log("provenance.created no longer reaches a domain subscriber: update the README and the replies, the upstream leak closed")
	}
}

func jsonHas(b json.RawMessage, sub string) bool { return strings.Contains(string(b), sub) }
