package invoke

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

const demoApps = "../../testdata/apps"

// writeOut is a fake app: it creates the file named by "output" in the request.
func writeOut(argv []string, stdin []byte) domain.Result {
	var rq struct{ Output string }
	json.Unmarshal(stdin, &rq)
	os.WriteFile(rq.Output, []byte("out"), 0o644)
	return domain.Result{}
}

func setup(t *testing.T) (*Invoker, *domain.Fake, string, *[]string, *[]prov.Record) {
	t.Helper()
	reg, skipped := apps.Load(demoApps)
	if len(skipped) != 0 {
		t.Fatalf("skipped: %v", skipped)
	}
	fk := &domain.Fake{OnRun: writeOut}
	var topics []string
	var recs []prov.Record
	var mu sync.Mutex
	inv := &Invoker{Reg: reg, Runner: fk, Logf: t.Logf,
		Publish: func(topic string, p []byte) error {
			var r prov.Record
			json.Unmarshal(p, &r)
			mu.Lock()
			topics, recs = append(topics, topic), append(recs, r)
			mu.Unlock()
			return nil
		},
		SetProvenance: func(string, prov.Record) error { return nil },
	}
	return inv, fk, t.TempDir(), &topics, &recs
}

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestGrantRevokeSequence(t *testing.T) {
	inv, fk, dir, topics, recs := setup(t)
	in := filepath.Join(dir, "in.png")
	os.WriteFile(in, png, 0o644)
	out := filepath.Join(dir, "out.png")
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: out})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	d := DomainName("org.aurora.demo")
	want := []string{
		"ensure " + d,
		"install " + d,
		"grant " + d + " " + in + " ro",
		"grant " + d + " " + out + " rw", // the one output file, not dir
		"run " + d + " /domain/data/app/run --verb copy@v1",
		"revoke " + d + " " + out,
		"revoke " + d + " " + in,
	}
	if !reflect.DeepEqual(fk.Calls, want) {
		t.Fatalf("calls:\n got %q\nwant %q", fk.Calls, want)
	}
	if !res.Isolated || res.Provenance == nil || res.Output != out {
		t.Fatalf("result %+v", res)
	}
	if len(*topics) != 1 || (*topics)[0] != "provenance.created" {
		t.Fatalf("topics %v", *topics)
	}
	r := (*recs)[0]
	if r.File != out || r.App != "org.aurora.demo" || r.Verb != "copy@v1" || !reflect.DeepEqual(r.Inputs, []string{in}) || !r.Isolated || r.TS == "" {
		t.Fatalf("record %+v", r)
	}
}

func TestRevokeOnFailure(t *testing.T) {
	inv, fk, dir, topics, _ := setup(t)
	fk.OnRun = func([]string, []byte) domain.Result { return domain.Result{Exit: 3, Stderr: []byte("boom")} }
	in := filepath.Join(dir, "in.png")
	os.WriteFile(in, png, 0o644)
	_, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: filepath.Join(dir, "o.png")})
	if e == nil || e.Code != "app_failed" || e.Detail != "boom" {
		t.Fatalf("err %+v", e)
	}
	if n := len(fk.Calls); n < 2 || !strings.HasPrefix(fk.Calls[n-1], "revoke") || !strings.HasPrefix(fk.Calls[n-2], "revoke") {
		t.Fatalf("grants not revoked after failure: %q", fk.Calls)
	}
	if len(*topics) != 0 {
		t.Fatal("event published for failed run")
	}
}

func TestReadVerbGrantsInputOnly(t *testing.T) {
	inv, fk, dir, topics, _ := setup(t)
	fk.OnRun = func([]string, []byte) domain.Result { return domain.Result{Stdout: []byte(`{"bytes":15}`)} }
	in := filepath.Join(dir, "in.png")
	os.WriteFile(in, png, 0o644)
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "size@v1", Input: in})
	if e != nil {
		t.Fatal(e)
	}
	if string(res.Result) != `{"bytes":15}` || res.Provenance != nil || len(*topics) != 0 {
		t.Fatalf("%+v", res)
	}
	for _, c := range fk.Calls {
		if strings.Contains(c, " rw") {
			t.Fatalf("read verb got rw grant: %q", fk.Calls)
		}
	}
}

func TestCleanErrors(t *testing.T) {
	inv, fk, dir, _, _ := setup(t)
	in := filepath.Join(dir, "in.png")
	os.WriteFile(in, png, 0o644)
	txt := filepath.Join(dir, "x.txt")
	os.WriteFile(txt, []byte("hello"), 0o644)
	out := filepath.Join(dir, "o.png")
	cases := []struct {
		name string
		req  Request
		code string
	}{
		{"missing app", Request{App: "no.such", Verb: "copy@v1", Input: in, Output: out}, "no_such_app"},
		{"missing verb", Request{App: "org.aurora.demo", Verb: "nope@v1", Input: in, Output: out}, "no_such_verb"},
		{"relative input", Request{App: "org.aurora.demo", Verb: "copy@v1", Input: "in.png", Output: out}, "bad_request"},
		{"missing input", Request{App: "org.aurora.demo", Verb: "copy@v1", Input: dir + "/gone.png", Output: out}, "bad_input"},
		{"no output", Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in}, "bad_request"},
		{"wrong type", Request{App: "org.aurora.demo", Verb: "copy@v1", Input: txt, Output: out}, "unsupported_type"},
		{"unknown arg", Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: out,
			Args: map[string]json.RawMessage{"zzz": json.RawMessage("1")}}, "bad_args"},
		{"wrong arg type", Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: out,
			Args: map[string]json.RawMessage{"tag": json.RawMessage("1")}}, "bad_args"},
		{"output==input", Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: in}, "bad_request"},
	}
	for _, c := range cases {
		res, e := inv.Invoke(t.Context(), c.req)
		if res != nil || e == nil || e.Code != c.code {
			t.Errorf("%s: got res=%v err=%+v want code %s", c.name, res, e, c.code)
		}
	}
	if len(fk.Calls) != 0 {
		t.Fatalf("domain touched on rejected requests: %q", fk.Calls)
	}
}

func TestFallbackIsLoudAndUnisolated(t *testing.T) {
	inv, fk, dir, _, recs := setup(t)
	fk.Unusable = os.ErrNotExist
	var logs []string
	inv.Logf = func(f string, a ...any) { logs = append(logs, f) }
	in := filepath.Join(dir, "in.png")
	os.WriteFile(in, png, 0o644)
	out := filepath.Join(dir, "o.png")
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: out})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if res.Isolated || (*recs)[0].Isolated || len(fk.Calls) != 0 {
		t.Fatalf("fallback claimed isolation: %+v calls=%q", res, fk.Calls)
	}
	if len(logs) == 0 || !strings.Contains(logs[0], "UNISOLATED") {
		t.Fatalf("no loud warning: %q", logs)
	}
	if b, _ := os.ReadFile(out); len(b) == 0 {
		t.Fatal("direct run wrote nothing")
	}
	inv.RequireIsolation = true
	if _, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: out}); e == nil || e.Code != "isolation_unavailable" {
		t.Fatalf("RequireIsolation: %+v", e)
	}
}

func TestDomainNameShape(t *testing.T) {
	n := DomainName("org.verylongvendor.extremelylongapplicationname")
	if len(n) > 32 || !strings.HasPrefix(n, "app-org-verylo") {
		t.Fatal(n)
	}
	if n := DomainName("org.aurora.lumen"); !strings.HasPrefix(n, "app-org-aurora-") || len(n) > 32 {
		t.Fatal(n)
	}
}

func TestReportedLandsInProvenance(t *testing.T) {
	inv, fk, dir, _, recs := setup(t)
	fk.OnRun = func(a []string, in []byte) domain.Result {
		writeOut(a, in)
		return domain.Result{Stdout: []byte(`{"ok":true,"secret":"not recorded","reported":{"cc":"gcc 1"}}`)}
	}
	in := filepath.Join(dir, "in.png")
	os.WriteFile(in, png, 0o644)
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: filepath.Join(dir, "out.png")})
	if e != nil {
		t.Fatal(e)
	}
	if string(res.Provenance.Reported) != `{"cc":"gcc 1"}` || len(*recs) != 1 || string((*recs)[0].Reported) != `{"cc":"gcc 1"}` {
		t.Fatalf("%s / %+v", res.Provenance.Reported, *recs)
	}
}
