package plumb

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultRulesValid(t *testing.T) {
	if Default().Len() == 0 {
		t.Fatal("no default rules")
	}
}

// Each case: the selection and the rule that must win, or "" for no match.
// The near-misses matter as much as the hits: a plumber that over-matches
// offers the wrong verb with confidence.
func TestRulesMatchAndNearMiss(t *testing.T) {
	rs := Default()
	cases := []struct {
		kind, value string
		rule        string
		object      string
		line, col   int
	}{
		// paths
		{KindPath, "/tmp/a.png", "path", "/tmp/a.png", 0, 0},
		{KindPath, "/", "", "", 0, 0}, // needs at least one more byte
		{KindPath, "a.png", "", "", 0, 0},
		{KindPath, "./a.png", "", "", 0, 0},
		{KindPath, "~/a.png", "", "", 0, 0},
		{KindPath, "", "", "", 0, 0},
		{KindPath, "/tmp/a\x00b", "", "", 0, 0},
		{KindPath, " /tmp/a", "", "", 0, 0},
		// a path with a space IS a path when the caller says path
		{KindPath, "/tmp/my file.png", "path", "/tmp/my file.png", 0, 0},

		// mime
		{KindMIME, "image/png", "mime", "image/png", 0, 0},
		{KindMIME, "IMAGE/PNG", "mime", "IMAGE/PNG", 0, 0},
		{KindMIME, "application/vnd.api+json", "mime", "application/vnd.api+json", 0, 0},
		{KindMIME, "image/*", "", "", 0, 0}, // a pattern is not a type
		{KindMIME, "*/*", "", "", 0, 0},
		{KindMIME, "image", "", "", 0, 0},
		{KindMIME, "image/png; charset=utf-8", "", "", 0, 0},
		{KindMIME, "image/png/extra", "", "", 0, 0},
		{KindMIME, "/png", "", "", 0, 0},
		{KindMIME, "image/ png", "", "", 0, 0},

		// text: absolute paths
		{KindText, "/home/a/b.go", "abs-path", "/home/a/b.go", 0, 0},
		{KindText, "/home/a/b.go:42", "path-line", "/home/a/b.go", 42, 0},
		{KindText, "/home/a/b.go:42:7", "path-line-col", "/home/a/b.go", 42, 7},
		{KindText, "a/b.go", "", "", 0, 0},
		{KindText, "./b.go", "", "", 0, 0},
		{KindText, "~/b.go", "", "", 0, 0},
		{KindText, "b.go", "", "", 0, 0},
		{KindText, "/", "", "", 0, 0},
		{KindText, "/home/a b.go", "", "", 0, 0}, // prose, not a path
		{KindText, " /home/a/b.go", "", "", 0, 0},
		{KindText, "/home/a/b.go ", "", "", 0, 0},
		{KindText, "/home/a/b.go\n", "", "", 0, 0},
		{KindText, "see /home/a/b.go", "", "", 0, 0},
		{KindText, "/home/a/b.go:", "", "", 0, 0},
		{KindText, "/home/a/b.go:x", "", "", 0, 0},
		{KindText, "/home/a/b.go:12:", "", "", 0, 0},
		{KindText, "/home/a/b.go:12:7:3", "", "", 0, 0},
		{KindText, "/home/a/b.go:-1", "", "", 0, 0},
		{KindText, "/home/a/b.go:1234567890", "", "", 0, 0}, // beyond 9 digits

		// text: urls
		{KindText, "https://example.com", "url-https", "", 0, 0},
		{KindText, "https://example.com/a/b?c=d#e", "url-https", "", 0, 0},
		{KindText, "HTTPS://Example.com:8443/x", "url-https", "", 0, 0},
		{KindText, "http://localhost:8080/", "url-http", "", 0, 0},
		{KindText, "http://", "", "", 0, 0},
		{KindText, "https://", "", "", 0, 0},
		{KindText, "https:/example.com", "", "", 0, 0},
		{KindText, "https//example.com", "", "", 0, 0},
		{KindText, "httpx://example.com", "", "", 0, 0},
		{KindText, "ftp://example.com", "", "", 0, 0},
		{KindText, "file:///etc/passwd", "", "", 0, 0},
		{KindText, "javascript:alert(1)", "", "", 0, 0},
		{KindText, "data:text/html,<b>x</b>", "", "", 0, 0},
		{KindText, "https://exa mple.com", "", "", 0, 0},
		{KindText, "https://example.com/a b", "", "", 0, 0},
		{KindText, "https://-example.com", "", "", 0, 0},
		{KindText, "https://example.com:99999999", "", "", 0, 0},
		{KindText, "go to https://example.com", "", "", 0, 0},

		// text: hostnames
		{KindText, "example.com", "hostname", "", 0, 0},
		{KindText, "docs.example.org", "hostname", "", 0, 0},
		{KindText, "Example.IO:3000", "hostname", "", 0, 0},
		{KindText, "example", "", "", 0, 0},
		{KindText, "localhost", "", "", 0, 0},
		{KindText, "example.", "", "", 0, 0},
		{KindText, ".com", "", "", 0, 0},
		{KindText, "-a.example.com", "", "", 0, 0},
		{KindText, "a_b.example.com", "", "", 0, 0},
		{KindText, "example.com/path", "", "", 0, 0},
		{KindText, "user@example.com", "", "", 0, 0},
		{KindText, "example.com:", "", "", 0, 0},
		{KindText, "example.com:123456", "", "", 0, 0},
		// file names that are also valid DNS names must stay files
		{KindText, "main.go", "", "", 0, 0},
		{KindText, "notes.md", "", "", 0, 0},
		{KindText, "build.sh", "", "", 0, 0},
		{KindText, "a.py", "", "", 0, 0},
		{KindText, "photo.png", "", "", 0, 0},
		{KindText, "archive.zip", "", "", 0, 0},
		{KindText, "1.2.3.4", "", "", 0, 0},
		{KindText, "v1.2", "", "", 0, 0},

		// kinds do not leak into each other
		{KindText, "image/png", "", "", 0, 0},
		{KindMIME, "https://example.com", "", "", 0, 0},
		{KindMIME, "/tmp/a.png", "", "", 0, 0},
		{KindPath, "https://example.com", "", "", 0, 0},
		{"", "/tmp/a.png", "", "", 0, 0},
		{"bogus", "/tmp/a.png", "", "", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.kind+" "+fmt.Sprintf("%q", c.value), func(t *testing.T) {
			m, ok := rs.Match(Selection{c.kind, c.value})
			if c.rule == "" {
				if ok {
					t.Fatalf("matched rule %q (object %q); want no match", m.Rule, m.Object)
				}
				return
			}
			if !ok || m.Rule != c.rule {
				t.Fatalf("got rule %q ok=%v, want %q", m.Rule, ok, c.rule)
			}
			if c.object != "" && m.Object != c.object {
				t.Errorf("object %q, want %q", m.Object, c.object)
			}
			if m.Line != c.line || m.Col != c.col {
				t.Errorf("line:col %d:%d, want %d:%d", m.Line, m.Col, c.line, c.col)
			}
		})
	}
}

func TestMatchIsTooLongSafe(t *testing.T) {
	long := "/" + strings.Repeat("a", MaxValue)
	if _, ok := Default().Match(Selection{KindPath, long}); ok {
		t.Fatal("a value over MaxValue matched")
	}
	rep := Resolve(Default(), nil, Request{Selection: Selection{KindPath, long}}, nil)
	if rep.Reason == "" || len(rep.Selection.Value) > 100 {
		t.Fatalf("long value echoed or no reason: %d %q", len(rep.Selection.Value), rep.Reason)
	}
}

func TestMatchIsPure(t *testing.T) {
	rs := Default()
	a, _ := rs.Match(Selection{KindText, "/x/y.go:3:4"})
	for i := 0; i < 5; i++ {
		if b, _ := rs.Match(Selection{KindText, "/x/y.go:3:4"}); !reflect.DeepEqual(a, b) {
			t.Fatal("match is not repeatable")
		}
	}
}

func TestFirstMatchWinsInFileOrder(t *testing.T) {
	rs, err := Parse([]byte(`
[plumb]
version = 1
[[rule]]
name = "first"
kind = "text"
match = 'x.*'
resolve = "mime"
mime = "a/b"
[[rule]]
name = "second"
kind = "text"
match = 'xy'
resolve = "mime"
mime = "c/d"
`))
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := rs.Match(Selection{KindText, "xy"}); m.Rule != "first" {
		t.Fatalf("got %q", m.Rule)
	}
}

func TestParseRejects(t *testing.T) {
	hdr := "[plumb]\nversion = 1\n[[rule]]\n"
	bad := map[string]string{
		"no version":     "[[rule]]\nname='a'\nkind='text'\nmatch='a'\nresolve='mime'\nmime='a/b'\n",
		"unknown key":    hdr + "name='a'\nkind='text'\nmatch='a'\nresolve='mime'\nmime='a/b'\nscript='rm'\n",
		"bad regexp":     hdr + "name='a'\nkind='text'\nmatch='('\nresolve='mime'\nmime='a/b'\n",
		"no name":        hdr + "kind='text'\nmatch='a'\nresolve='mime'\nmime='a/b'\n",
		"bad kind":       hdr + "name='a'\nkind='file'\nmatch='a'\nresolve='mime'\nmime='a/b'\n",
		"bad resolve":    hdr + "name='a'\nkind='text'\nmatch='a'\nresolve='run'\n",
		"missing group":  hdr + "name='a'\nkind='text'\nmatch='a'\nresolve='path'\n",
		"mime and group": hdr + "name='a'\nkind='text'\nmatch='(?P<object>a)'\nresolve='mime'\nmime='a/b'\nobject='object'\n",
		"bad fixed mime": hdr + "name='a'\nkind='text'\nmatch='a'\nresolve='mime'\nmime='ab'\n",
		"bad verb":       hdr + "name='a'\nkind='text'\nmatch='a'\nresolve='verb'\nverb='nocolon'\n",
		"verb with mime": hdr + "name='a'\nkind='text'\nmatch='a'\nresolve='verb'\nverb='a:b'\nmime='a/b'\n",
		"duplicate": hdr + "name='a'\nkind='text'\nmatch='a'\nresolve='mime'\nmime='a/b'\n" +
			"[[rule]]\nname='a'\nkind='text'\nmatch='b'\nresolve='mime'\nmime='a/b'\n",
	}
	for name, src := range bad {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

var fixture = []Verb{
	{App: "z.app", Name: "zoom", Effect: "read", Accepts: []string{"image/*"}, Summary: "z"},
	{App: "a.app", Name: "wipe", Effect: "destructive", Accepts: []string{"image/png"}, Summary: "w"},
	{App: "b.app", Name: "info", Effect: "read", Accepts: []string{"*/*"}, Default: true, Summary: "i"},
	{App: "c.app", Name: "view", Effect: "read", Accepts: []string{"image/png", "image/jpeg"}, Default: true, Group: "View", Summary: "v"},
	{App: "c.app", Name: "view", Effect: "read", Accepts: []string{"image/png"}, Default: true}, // duplicate
	{App: "d.app", Name: "post", Effect: "external", Accepts: []string{"image/png"}},
	{App: "e.app", Name: "edit", Effect: "write", Accepts: []string{"image/png"}},
	{App: "f.app", Name: "play", Effect: "read", Accepts: []string{"audio/*"}, Default: true},
	{App: "g.app", Name: "noeffect", Effect: "", Accepts: []string{"image/png"}},
	{App: "h.app", Name: "none"}, // accepts nothing
}

func names(cs []Candidate) []string {
	var o []string
	for _, c := range cs {
		o = append(o, c.App+":"+c.Verb)
	}
	return o
}

func TestRanking(t *testing.T) {
	rep := Resolve(Default(), fixture, Request{Selection: Selection{KindMIME, "image/png"}}, nil)
	// default+exact first; then exact ordered read < write < destructive <
	// external < unknown; then type/*; then */* (a */* default is not the
	// default for this type).
	want := []string{"c.app:view", "e.app:edit", "a.app:wipe", "d.app:post", "g.app:noeffect", "z.app:zoom", "b.app:info"}
	if got := names(rep.Candidates); !reflect.DeepEqual(got, want) {
		t.Fatalf("order\n got %v\nwant %v", got, want)
	}
	if !rep.Candidates[0].Default || rep.Candidates[0].Group != "View" {
		t.Errorf("first: %+v", rep.Candidates[0])
	}
	if rep.Candidates[6].Default {
		t.Error("a */* default was flagged default for image/png")
	}
}

func TestEffectNeverDropped(t *testing.T) {
	rep := Resolve(Default(), fixture, Request{Selection: Selection{KindMIME, "image/png"}}, nil)
	byName := map[string]string{}
	for _, c := range rep.Candidates {
		if c.Effect == "" {
			t.Errorf("%s has no effect", c.Verb)
		}
		byName[c.Verb] = c.Effect
	}
	if byName["wipe"] != "destructive" || byName["post"] != "external" || byName["noeffect"] != "unknown" {
		t.Errorf("effects %v", byName)
	}
}

func TestBoundAndTruncate(t *testing.T) {
	var many []Verb
	for i := 0; i < 80; i++ {
		many = append(many, Verb{App: fmt.Sprintf("app%02d", i), Name: "v", Effect: "read", Accepts: []string{"image/png"}})
	}
	sel := Selection{KindMIME, "image/png"}
	for limit, want := range map[int]int{0: DefaultLimit, 3: 3, 1000: MaxLimit, -5: DefaultLimit} {
		rep := Resolve(Default(), many, Request{Selection: sel, Limit: limit}, nil)
		if len(rep.Candidates) != want || !rep.Truncated {
			t.Errorf("limit %d: %d candidates truncated=%v, want %d true", limit, len(rep.Candidates), rep.Truncated, want)
		}
	}
	if rep := Resolve(Default(), fixture, Request{Selection: sel}, nil); rep.Truncated {
		t.Error("truncated when the list fit")
	}
}

func TestUnmatchedIsEmptyWithReason(t *testing.T) {
	for _, s := range []Selection{
		{KindText, "just some words"},
		{KindMIME, "image/*"},
		{KindMIME, "chemical/x-pdb"}, // matches a rule, but nothing accepts it
		{KindText, "https://example.com"},
		{"weird", "x"},
		{KindPath, "relative"},
	} {
		rep := Resolve(Default(), fixture[:2], Request{Selection: s}, nil) // zoom and wipe: no */* verb
		if rep.Candidates == nil || len(rep.Candidates) != 0 || rep.Reason == "" {
			t.Errorf("%v: candidates=%v reason=%q", s, rep.Candidates, rep.Reason)
		}
	}
}

func TestDirectVerbRule(t *testing.T) {
	rs, err := Parse([]byte("[plumb]\nversion = 1\n[[rule]]\nname='n'\nkind='text'\nmatch='note: .*'\nresolve='verb'\nverb='e.app:edit'\n"))
	if err != nil {
		t.Fatal(err)
	}
	rep := Resolve(rs, fixture, Request{Selection: Selection{KindText, "note: hi"}}, nil)
	if got := names(rep.Candidates); !reflect.DeepEqual(got, []string{"e.app:edit"}) || rep.Candidates[0].Effect != "write" {
		t.Fatalf("%v %+v", got, rep)
	}
	rs2, _ := Parse([]byte("[plumb]\nversion = 1\n[[rule]]\nname='n'\nkind='text'\nmatch='x'\nresolve='verb'\nverb='no.such:verb'\n"))
	rep = Resolve(rs2, fixture, Request{Selection: Selection{KindText, "x"}}, nil)
	if len(rep.Candidates) != 0 || rep.Reason == "" {
		t.Fatalf("%+v", rep)
	}
}

func TestPathWithDiskProbe(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "noext")
	os.WriteFile(png, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o600)
	rep := Resolve(Default(), fixture, Request{Selection: Selection{KindText, png + ":9:2"}}, DiskProbe)
	if rep.Selection.MIME != "image/png" || rep.Selection.Line != 9 || rep.Selection.Col != 2 || len(rep.Candidates) == 0 {
		t.Fatalf("%+v", rep)
	}
	rep = Resolve(Default(), fixture, Request{Selection: Selection{KindPath, dir}}, DiskProbe)
	if rep.Selection.MIME != "inode/directory" {
		t.Fatalf("dir: %+v", rep)
	}
	rep = Resolve(Default(), fixture, Request{Selection: Selection{KindPath, filepath.Join(dir, "missing")}}, DiskProbe)
	if rep.Reason == "" || len(rep.Candidates) != 0 {
		t.Fatalf("missing: %+v", rep)
	}
}

// A PNG named .txt is a PNG for the session (content wins) and text for a
// domain (the name is all it gets).
func TestViewerDomainSeesNoDisk(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "secret.dat")
	os.WriteFile(real, []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), 0o600)
	missing := filepath.Join(dir, "absent.dat")
	named := filepath.Join(dir, "x.png")

	const session, dom = "uid1000:aurora", "domain:lumen:abc"
	req := func(p string) Request { return Request{Selection: Selection{KindPath, p}} }

	if rep := ForViewer(Default(), fixture, req(real), session); rep.Selection.MIME != "image/png" || len(rep.Candidates) == 0 {
		t.Fatalf("session did not sniff: %+v", rep)
	}
	// The domain gets a byte-identical answer whether the file exists or not.
	a := ForViewer(Default(), fixture, req(real), dom)
	b := ForViewer(Default(), fixture, req(missing), dom)
	a.Selection.Value, a.Selection.Object, b.Selection.Value, b.Selection.Object = "", "", "", ""
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("existence is observable to a domain:\n%+v\n%+v", a, b)
	}
	if len(a.Candidates) != 0 || a.Selection.MIME != "" || a.Viewer != "domain" {
		t.Fatalf("domain learned something from disk: %+v", a)
	}
	// A name with a known extension resolves for a domain with no disk access,
	// existing or not.
	if x := ForViewer(Default(), fixture, req(named), dom); x.Selection.MIME != "image/png" || len(x.Candidates) == 0 {
		t.Fatalf("by-name: %+v", x)
	}
	if _, err := os.Stat(named); err == nil {
		t.Fatal("fixture should not exist")
	}
	// Directories look like any extensionless name to a domain.
	d := ForViewer(Default(), fixture, req(dir), dom)
	if d.Selection.MIME != "" {
		t.Fatalf("domain learned %q for a directory", d.Selection.MIME)
	}
	// Unknown callers see nothing at all.
	for _, from := range []string{"", "nobody", "uid1000", "domain:", "root:x"} {
		rep := ForViewer(Default(), fixture, Request{Selection: Selection{KindMIME, "image/png"}}, from)
		if len(rep.Candidates) != 0 || rep.Viewer != "unknown" || rep.Reason == "" {
			t.Errorf("%q: %+v", from, rep)
		}
	}
}

// The same identities index/access.go classifies; keep the two in step.
func TestViewerKindTable(t *testing.T) {
	for from, want := range map[string]string{
		"uid1000:aurora":    "session",
		"uid0:x":            "session",
		"uid1000:":          "unknown",
		"uid:aurora":        "unknown",
		"domain:lumen:abc":  "domain",
		"domain:":           "unknown",
		"":                  "unknown",
		"xdomain:lumen:abc": "unknown",
		" uid1000:aurora":   "unknown",
		"DOMAIN:lumen:abc":  "unknown",
	} {
		if got := ViewerKind(from); got != want {
			t.Errorf("%q: %q, want %q", from, got, want)
		}
	}
}
