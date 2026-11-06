package launcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/contracts/plumb"
)

type plumbFake struct {
	*fakeBackend
	got plumb.Request
	rep plumb.Reply
}

func (p *plumbFake) Plumb(_ context.Context, r plumb.Request) (plumb.Reply, error) {
	p.got = r
	return p.rep, p.err
}

func runPlumb(t *testing.T, p *plumbFake, args ...string) (int, string, string) {
	t.Helper()
	env, out, errb := harness(p.fakeBackend, "")
	env.Backend = func(string, time.Duration) Backend { return p }
	code := Run(env, append([]string{"plumb"}, args...))
	return code, out.String(), errb.String()
}

func cannedReply() plumb.Reply {
	return plumb.Reply{
		Selection: plumb.Resolved{Kind: "path", Value: "/x.png", Rule: "path", MIME: "image/png"},
		Candidates: []plumb.Candidate{
			{App: "a", Verb: "view", Summary: "look", Effect: "read", Default: true},
			{App: "b", Verb: "wipe", Summary: "shred", Effect: "destructive", Group: "Danger"},
			{App: "c", Verb: "post", Summary: "send", Effect: "external"},
		},
	}
}

func TestPlumbJSONKeepsEffectOnEveryCandidate(t *testing.T) {
	p := &plumbFake{fakeBackend: newFake(), rep: cannedReply()}
	code, out, _ := runPlumb(t, p, "--json", "/x.png")
	if code != 0 {
		t.Fatal(code)
	}
	var back struct {
		Candidates []map[string]any `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	want := []string{"read", "destructive", "external"}
	for i, c := range back.Candidates {
		if c["effect"] != want[i] {
			t.Errorf("candidate %d effect %v, want %s", i, c["effect"], want[i])
		}
	}
	if back.Candidates[0]["default"] != true {
		t.Error("default flag lost")
	}
}

func TestPlumbHumanFlagsGatedEffects(t *testing.T) {
	p := &plumbFake{fakeBackend: newFake(), rep: cannedReply()}
	_, out, _ := runPlumb(t, p, "/x.png")
	for _, w := range []string{"!! DESTRUCTIVE", "!! EXTERNAL", "[default]", "group: Danger"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in\n%s", w, out)
		}
	}
	if strings.Contains(out, "!! READ") {
		t.Error("read flagged")
	}
}

func TestPlumbEmptyIsNotAnError(t *testing.T) {
	p := &plumbFake{fakeBackend: newFake(), rep: plumb.Reply{Selection: plumb.Resolved{Kind: "text", Value: "hi"}, Candidates: []plumb.Candidate{}, Reason: "no plumb rule matches this text selection"}}
	code, out, _ := runPlumb(t, p, "hi")
	if code != 0 || !strings.Contains(out, "nothing to offer: no plumb rule") {
		t.Fatalf("%d %s", code, out)
	}
	_, js, _ := runPlumb(t, p, "--json", "hi")
	if !strings.Contains(js, `"candidates":[]`) {
		t.Fatalf("empty list must be [] not null: %s", js)
	}
}

func TestSelectionFor(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.png")
	os.WriteFile(f, nil, 0o600)
	for _, c := range []struct {
		arg, forced string
		want        plumb.Selection
	}{
		{f, "", plumb.Selection{Kind: "path", Value: f}},
		{f + ":3", "", plumb.Selection{Kind: "text", Value: f + ":3"}},
		{"example.com", "", plumb.Selection{Kind: "text", Value: "example.com"}},
		{"image/png", "mime", plumb.Selection{Kind: "mime", Value: "image/png"}},
		{f, "text", plumb.Selection{Kind: "text", Value: f}},
	} {
		if got := SelectionFor(c.arg, c.forced); got != c.want {
			t.Errorf("%q/%q: %+v want %+v", c.arg, c.forced, got, c.want)
		}
	}
	wd, _ := os.Getwd()
	if got := SelectionFor("plumb_test.go", ""); got.Value != filepath.Join(wd, "plumb_test.go") || got.Kind != "path" {
		t.Errorf("relative existing: %+v", got)
	}
}

func TestPlumbUsageErrors(t *testing.T) {
	p := &plumbFake{fakeBackend: newFake()}
	for _, args := range [][]string{{}, {"a", "b"}, {"--path", "--mime", "x"}} {
		if code, _, _ := runPlumb(t, p, args...); code != 2 {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}
