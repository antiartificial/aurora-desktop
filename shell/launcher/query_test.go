package launcher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type fakeIndex struct {
	*fakeBackend
	list QueryList
	run  QueryRun
	show QueryShow
	err  error
	live []struct {
		from string
		h    QueryHit
	}
	asked []string
}

func (f *fakeIndex) QueryList(context.Context) (QueryList, error) { return f.list, f.err }
func (f *fakeIndex) QueryRun(_ context.Context, n string) (QueryRun, error) {
	f.asked = append(f.asked, "run "+n)
	return f.run, f.err
}
func (f *fakeIndex) QueryShow(_ context.Context, p string) (QueryShow, error) {
	f.asked = append(f.asked, "show "+p)
	return f.show, f.err
}
func (f *fakeIndex) QueryWatch(_ context.Context, n string, snap func(QueryRun), live func(string, QueryHit)) error {
	f.asked = append(f.asked, "watch "+n)
	if f.err != nil {
		return f.err
	}
	snap(f.run)
	for _, l := range f.live {
		live(l.from, l.h)
	}
	return nil
}

func queryHarness(f *fakeIndex) (*Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &Env{
		Backend: func(string, time.Duration) Backend { return f },
		In:      bufio.NewReader(strings.NewReader("")),
		Out:     &out, Err: &errb,
	}, &out, &errb
}

const esc = "\x1b]0;PWNED\x07"

func sampleHit() QueryHit {
	return QueryHit{Seq: 3, ObservedAt: "2026-11-06T15:04:05.123Z", Sender: "uid1000:controllerd", BusID: "id-9",
		File: "/pics/out" + esc + ".png", App: "org.evil" + esc, Verb: "convert@v1\x1b[2J", MIME: "image/png",
		Inputs: []string{"/pics/in.png" + esc}, Domain: "domain:x:1", DomainAttested: true, RecordTS: "T1",
		Now:    &QueryObserved{Exists: true},
		Grants: []actGrant{{Path: "/pics/in.png", Mode: "ro", Source: "gesture", Cap: "cap:aa"}}}
}

func TestQueryListMarksBrokenQueries(t *testing.T) {
	f := &fakeIndex{fakeBackend: newFake(), list: QueryList{
		Queries: []QueryInfo{{Name: "imgs", Title: "Images" + esc}, {Name: "bad", Error: "line 1: unknown key \"x\"" + esc}},
		Stats:   QueryStats{Entries: 2, Files: 2, StartedAt: "2026-11-06T10:00:00Z"}}}
	env, out, _ := queryHarness(f)
	if rc := Run(env, []string{"query"}); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	mustContain(t, out.String(), "imgs", "Images", "bad", "!! does not parse", "index: 2 entries for 2 files")
	if strings.Contains(out.String(), "\x1b") {
		t.Errorf("escape survived: %q", out.String())
	}
}

func TestQueryRunSaysWhatItDoesNotKnow(t *testing.T) {
	f := &fakeIndex{fakeBackend: newFake(), run: QueryRun{Query: QueryInfo{Name: "imgs", Title: "T"}, Total: 1,
		Hits: []QueryHit{sampleHit()}, Stats: QueryStats{Entries: 1, Files: 1, FirstSeen: "2026-11-06T10:00:00Z"}}}
	env, out, _ := queryHarness(f)
	if rc := Run(env, []string{"query", "run", "imgs"}); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	s := out.String()
	mustContain(t, s, "1 match(es)", "image/png", "<- /pics/in.png", "[present now]",
		"THE FILTER COVERS THE INDEX ONLY", "provenance.created", "invisible", "extension")
	if strings.Contains(s, "NOT FILTERED") {
		t.Errorf("the old no-filter warning is still printed: %q", s)
	}
	if strings.Contains(s, "\x1b") || strings.Contains(strings.ToLower(s), "attested") {
		t.Errorf("escape survived or a claim was called attested: %q", s)
	}
	if len(f.asked) != 1 || f.asked[0] != "run imgs" {
		t.Errorf("asked %v", f.asked)
	}
}

func TestQueryWatchDropsForgedEvents(t *testing.T) {
	h := sampleHit()
	forged := h
	forged.File = "/forged.png"
	f := &fakeIndex{fakeBackend: newFake(), run: QueryRun{Query: QueryInfo{Name: "imgs"}, Total: 0},
		live: []struct {
			from string
			h    QueryHit
		}{
			{"uid1000:bash", forged}, {"domain:app-x:11", forged},
			{"uid1000:indexd", h},
		}}
	env, out, errb := queryHarness(f)
	if rc := Run(env, []string{"query", "run", "imgs", "--watch", "--count", "1"}); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	if strings.Contains(out.String(), "forged") || strings.Count(out.String(), "\n+  ") != 1 {
		t.Errorf("out: %q", out.String())
	}
	mustContain(t, errb.String(), "ignored an index.next reply from uid1000:bash (stamped by busd)", "domain:app-x:11")
}

func TestQueryRunJSON(t *testing.T) {
	f := &fakeIndex{fakeBackend: newFake(), run: QueryRun{Query: QueryInfo{Name: "imgs"}, Total: 1, Hits: []QueryHit{sampleHit()}},
		live: []struct {
			from string
			h    QueryHit
		}{{"uid1000:indexd", sampleHit()}}}
	env, out, _ := queryHarness(f)
	if rc := Run(env, []string{"query", "run", "imgs", "--watch", "--json"}); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a snapshot line and a live line, got %q", out.String())
	}
	var a, b struct{ Live bool }
	json.Unmarshal([]byte(lines[0]), &a)
	json.Unmarshal([]byte(lines[1]), &b)
	if a.Live || !b.Live {
		t.Errorf("live flags %v %v", a.Live, b.Live)
	}
}

func TestQueryShowLabelsClaims(t *testing.T) {
	f := &fakeIndex{fakeBackend: newFake(), show: QueryShow{Versions: []QueryHit{sampleHit()}, UsedIn: []string{"/pics/next.png"},
		Stats: QueryStats{Entries: 1, Files: 1, FirstSeen: "2026-11-06T10:00:00Z"}}}
	env, out, _ := queryHarness(f)
	if rc := Run(env, []string{"query", "show", "/pics/x.png"}); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	s := out.String()
	mustContain(t, s, "derived from /pics/in.png", "used in", "/pics/next.png", "uid1000:controllerd (stamped by busd)",
		"controller says the bus attested it; indexd did not check", "grant      ro /pics/in.png")
	if strings.Contains(s, "\x1b") {
		t.Errorf("escape survived: %q", s)
	}
	// An unknown file exits 1 and does not claim Aurora never made it.
	f.show = QueryShow{}
	env, out, _ = queryHarness(f)
	if rc := Run(env, []string{"query", "show", "/nope"}); rc != 1 {
		t.Errorf("rc %d", rc)
	}
	mustContain(t, out.String(), "not in the index", "does NOT mean")
}

func TestQueryUsageAndErrors(t *testing.T) {
	f := &fakeIndex{fakeBackend: newFake()}
	for _, args := range [][]string{{"query", "run"}, {"query", "run", "Bad.Name"}, {"query", "show", "rel"}, {"query", "frob"}, {"query", "list", "--watch"}, {"query", "list", "extra"}} {
		env, _, _ := queryHarness(f)
		if rc := Run(env, args); rc != 2 {
			t.Errorf("%v: rc %d, want 2", args, rc)
		}
	}
	f.err = ErrBusDown
	env, _, errb := queryHarness(f)
	if rc := Run(env, []string{"query"}); rc != 1 {
		t.Errorf("rc %d", rc)
	}
	mustContain(t, errb.String(), "Start it first")
	// A backend with no index support says so rather than panicking.
	var out, eb bytes.Buffer
	env = &Env{Backend: func(string, time.Duration) Backend { return newFake() }, Out: &out, Err: &eb}
	if rc := Run(env, []string{"query"}); rc != 1 || !strings.Contains(eb.String(), "cannot reach the index") {
		t.Errorf("rc %d %q", rc, eb.String())
	}
}

func TestTrustedIndexer(t *testing.T) {
	for from, want := range map[string]bool{"uid1000:indexd": true, "uid1000:bash": false, "domain:app-x:indexd": false, "": false} {
		if trustedIndexer(from) != want {
			t.Errorf("%q", from)
		}
	}
}

func TestQueryPrintsWhoseViewItIs(t *testing.T) {
	dom := &QueryViewer{Identity: "domain:app-x:aaaaaaaa" + esc, Kind: "domain", Rule: "only your own files" + esc}
	f := &fakeIndex{fakeBackend: newFake(),
		run:  QueryRun{Query: QueryInfo{Name: "imgs"}, Viewer: dom},
		show: QueryShow{Viewer: dom},
		list: QueryList{Viewer: dom}}
	for _, args := range [][]string{{"query", "run", "imgs"}, {"query", "show", "/pics/x.png"}, {"query"}} {
		env, out, _ := queryHarness(f)
		Run(env, args)
		mustContain(t, out.String(), "VIEW: indexd says it saw this request from domain:app-x:aaaaaaaa", "as indexd reports it", "as a domain", "only your own files")
		if strings.Contains(out.String(), "\x1b") || strings.Contains(strings.ToLower(out.String()), "attested") {
			t.Errorf("%v: escape survived or the identity was called attested: %q", args, out.String())
		}
	}
	// A reply with no viewer comes from an indexd that does not filter: say so.
	f.run.Viewer = nil
	env, out, _ := queryHarness(f)
	Run(env, []string{"query", "run", "imgs"})
	mustContain(t, out.String(), "did not say whose view this is", "does NOT filter")
	// A domain's "no such file" must not claim the file was never made, nor that it does not exist.
	f.show = QueryShow{Viewer: dom}
	env, out, _ = queryHarness(f)
	if rc := Run(env, []string{"query", "show", "/b/theirs.png"}); rc != 1 {
		t.Errorf("rc %d", rc)
	}
	mustContain(t, out.String(), "not in the index, or not visible to you", "another domain's file looks exactly like this", "does NOT mean")
}
