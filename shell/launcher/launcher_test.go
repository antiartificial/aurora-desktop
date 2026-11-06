package launcher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	verbs   []Verb
	desc    map[string]Description
	invokes []InvokeRequest
	err     error // returned by every call
	fail    string
	events  []ProvEvent
}

func (f *fakeBackend) Apps(context.Context) ([]App, error) { return nil, f.err }
func (f *fakeBackend) Verbs(_ context.Context, accepts string) ([]Verb, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.verbs, nil // deliberately unfiltered: the launcher must filter too
}
func (f *fakeBackend) Describe(_ context.Context, app, verb string) (Description, error) {
	return f.desc[verb], f.err
}
func (f *fakeBackend) Invoke(_ context.Context, r InvokeRequest) (InvokeResult, error) {
	f.invokes = append(f.invokes, r)
	if f.fail != "" {
		return InvokeResult{Error: f.fail, Detail: "boom"}, nil
	}
	return InvokeResult{OK: true, Output: r.Output}, nil
}
func (f *fakeBackend) Watch(_ context.Context, fn func(ProvEvent)) error {
	for _, e := range f.events {
		fn(e)
	}
	return f.err
}
func (f *fakeBackend) Close() error { return nil }

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func newFake() *fakeBackend {
	return &fakeBackend{
		verbs: []Verb{
			{Name: "resize@v1", App: "org.aurora.lumen", Effect: "write", Accepts: []string{"image/png", "image/jpeg"}, Summary: "Resize"},
			{Name: "export@v1", App: "org.aurora.lumen", Effect: "write", Accepts: []string{"image/png"}, Summary: "Export"},
			{Name: "wipe@v1", App: "org.aurora.shred", Effect: "destructive", Accepts: []string{"image/*"}, Summary: "Shred"},
			{Name: "post@v1", App: "org.aurora.net", Effect: "external", Accepts: []string{"image/png"}, Summary: "Upload"},
			{Name: "wc@v1", App: "org.aurora.text", Effect: "read", Accepts: []string{"text/plain"}, Summary: "Count"},
		},
		desc: map[string]Description{
			"resize@v1": {Input: map[string]json.RawMessage{"width": raw(`"int"`), "height": raw(`"int?"`)}},
			"export@v1": {Input: map[string]json.RawMessage{"format": raw(`"string"`), "quality": raw(`{"type":"int","default":90}`)}},
		},
	}
}

func harness(be *fakeBackend, stdin string) (*Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &Env{
		Backend: func(string, time.Duration) Backend { return be },
		In:      bufio.NewReader(strings.NewReader(stdin)),
		Out:     &out, Err: &errb,
	}, &out, &errb
}

func writeImages(t *testing.T) (pngP, jpgP, txtP string) {
	t.Helper()
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var b bytes.Buffer
	png.Encode(&b, img)
	pngP = filepath.Join(dir, "a.png")
	os.WriteFile(pngP, b.Bytes(), 0o644)
	b.Reset()
	jpeg.Encode(&b, img, nil)
	jpgP = filepath.Join(dir, "b.jpg")
	os.WriteFile(jpgP, b.Bytes(), 0o644)
	txtP = filepath.Join(dir, "c.txt")
	os.WriteFile(txtP, []byte("hello world\n"), 0o644)
	return
}

func TestDetectMIME(t *testing.T) {
	p, j, x := writeImages(t)
	for path, want := range map[string]string{p: "image/png", j: "image/jpeg", x: "text/plain"} {
		got, err := DetectMIME(path)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", filepath.Base(path), got, err, want)
		}
	}
	// Content beats a lying extension.
	lie := filepath.Join(filepath.Dir(p), "lie.jpg")
	os.Rename(p, lie)
	if got, _ := DetectMIME(lie); got != "image/png" {
		t.Errorf("png named .jpg: got %q, want image/png", got)
	}
	// Extension decides only when sniffing is generic.
	md := filepath.Join(filepath.Dir(p), "n.md")
	os.WriteFile(md, []byte("# hi"), 0o644)
	if got, _ := DetectMIME(md); got != "text/markdown" {
		t.Errorf("md: got %q", got)
	}
}

func TestFilterByMIME(t *testing.T) {
	names := func(vs []Verb) string {
		var n []string
		for _, v := range vs {
			n = append(n, v.Name)
		}
		return strings.Join(n, ",")
	}
	f := newFake()
	if got := names(FilterByMIME(f.verbs, "image/jpeg")); got != "resize@v1,wipe@v1" {
		t.Errorf("jpeg: %s", got)
	}
	if got := names(FilterByMIME(f.verbs, "image/png")); got != "resize@v1,export@v1,wipe@v1,post@v1" {
		t.Errorf("png: %s", got)
	}
	if got := names(FilterByMIME(f.verbs, "text/plain")); got != "wc@v1" {
		t.Errorf("text: %s", got)
	}
}

func TestOpenNonInteractive(t *testing.T) {
	p, _, _ := writeImages(t)
	be := newFake()
	env, out, errb := harness(be, "") // empty stdin: any prompt would fail
	code := Run(env, []string{"open", p, "--verb", "resize", "--arg", "width=800", "--output", "/tmp/o.png"})
	if code != 0 {
		t.Fatalf("exit %d\nout:%s\nerr:%s", code, out, errb)
	}
	if len(be.invokes) != 1 {
		t.Fatalf("invokes: %+v", be.invokes)
	}
	r := be.invokes[0]
	if r.Verb != "resize@v1" || r.Input != p || r.Output != "/tmp/o.png" || r.Args["width"] != int64(800) {
		t.Errorf("bad request %+v", r)
	}
	if strings.Contains(out.String(), "Pick a verb") || strings.Contains(out.String(), "(int):") {
		t.Errorf("prompted in non-interactive mode:\n%s", out)
	}
	if !strings.Contains(out.String(), "output: /tmp/o.png") {
		t.Errorf("output not reported:\n%s", out)
	}
}

func TestOpenInteractivePick(t *testing.T) {
	p, _, _ := writeImages(t)
	be := newFake()
	env, out, errb := harness(be, "1\n640\n\n") // verb 1, width 640, height skipped
	if code := Run(env, []string{"open", p}); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	r := be.invokes[0]
	if r.Verb != "resize@v1" || r.Args["width"] != int64(640) {
		t.Errorf("bad request %+v", r)
	}
	if _, has := r.Args["height"]; has {
		t.Errorf("optional height should be absent: %v", r.Args)
	}
	if r.Output != strings.TrimSuffix(p, ".png")+".resize.png" {
		t.Errorf("derived output %q", r.Output)
	}
}

func TestBuildArgsPrompting(t *testing.T) {
	specs := ParseSchema(map[string]json.RawMessage{
		"format":  raw(`"string"`),
		"quality": raw(`{"type":"int","default":90}`),
		"strip":   raw(`"bool?"`),
		"scale":   raw(`"float"`),
	})
	// non-optional args by name (a default does not make one optional), then optionals.
	if specs[0].Name != "format" || specs[1].Name != "quality" || specs[2].Name != "scale" || specs[3].Name != "strip" {
		t.Fatalf("order: %+v", specs)
	}
	// format=avif; quality empty -> default; scale: "x" is rejected then 1.5; strip=true
	in := bufio.NewReader(strings.NewReader("avif\n\nx\n1.5\ntrue\n"))
	var out bytes.Buffer
	args, err := BuildArgs(specs, nil, true, in, &out)
	if err != nil {
		t.Fatal(err)
	}
	if args["format"] != "avif" || args["scale"] != 1.5 || args["strip"] != true || args["quality"] != float64(90) {
		t.Errorf("args %v", args)
	}
	if !strings.Contains(out.String(), "not a number") || !strings.Contains(out.String(), "[default 90]") {
		t.Errorf("prompt text:\n%s", out.String())
	}
	// Non-interactive: missing required is an error naming it.
	_, err = BuildArgs(specs, map[string]string{"format": "png"}, false, nil, &out)
	if err == nil || !strings.Contains(err.Error(), `"scale"`) {
		t.Errorf("want missing scale error, got %v", err)
	}
	// Typo'd argument name is rejected.
	_, err = BuildArgs(specs, map[string]string{"formt": "png"}, false, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "unknown argument") {
		t.Errorf("got %v", err)
	}
}

func TestChainFeedsOutputToInput(t *testing.T) {
	p, _, _ := writeImages(t)
	be := newFake()
	env, out, errb := harness(be, "")
	code := Run(env, []string{"chain", p, "--verb", "resize@v1", "--verb", "export@v1",
		"--arg", "width=800", "--arg", "format=avif", "--arg", "2:quality=70"})
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errb)
	}
	if len(be.invokes) != 2 {
		t.Fatalf("invokes %+v", be.invokes)
	}
	a, b := be.invokes[0], be.invokes[1]
	if b.Input != a.Output || a.Output == "" {
		t.Errorf("step 2 input %q != step 1 output %q", b.Input, a.Output)
	}
	if a.Input != p || b.Verb != "export@v1" || b.Args["format"] != "avif" || b.Args["quality"] != int64(70) {
		t.Errorf("bad chain %+v %+v", a, b)
	}
	if !strings.HasSuffix(b.Output, ".avif") {
		t.Errorf("final output %q should be .avif", b.Output)
	}
	if !strings.Contains(out.String(), "step 1/2") || !strings.Contains(out.String(), "step 2/2") {
		t.Errorf("both steps should be reported:\n%s", out)
	}
	if _, bad := a.Args["format"]; bad {
		t.Errorf("format leaked into resize: %v", a.Args)
	}
}

func TestChainStopsWhenStepOneFails(t *testing.T) {
	p, _, _ := writeImages(t)
	be := newFake()
	be.fail = "no space"
	env, _, errb := harness(be, "")
	code := Run(env, []string{"chain", p, "--verb", "resize", "--verb", "export", "--arg", "width=1", "--arg", "format=png"})
	if code == 0 || len(be.invokes) != 1 || !strings.Contains(errb.String(), "step 1") || !strings.Contains(errb.String(), "no space") {
		t.Errorf("code %d invokes %d err %s", code, len(be.invokes), errb)
	}
}

func TestRegistryAbsent(t *testing.T) {
	p, _, _ := writeImages(t)
	for _, tc := range []struct {
		err  error
		want []string
	}{
		{ErrRegistryAbsent, []string{"registry", "controllerd"}},
		{ErrBusDown, []string{"busd", "controllerd"}},
	} {
		be := newFake()
		be.err = tc.err
		for _, args := range [][]string{{"open", p, "--verb", "resize"}, {"verbs"}, {"apps"}, {"watch"}} {
			env, _, errb := harness(be, "")
			if code := Run(env, args); code != 1 {
				t.Errorf("%v: exit %d, want 1", args, code)
			}
			for _, w := range tc.want {
				if !strings.Contains(errb.String(), w) {
					t.Errorf("%v: stderr %q lacks %q", args, errb, w)
				}
			}
		}
	}
	if !errors.Is(ErrBusDown, ErrBusDown) {
		t.Fatal("sentinel")
	}
}

func TestDestructiveMarked(t *testing.T) {
	var out bytes.Buffer
	PrintVerbs(&out, newFake().verbs, true)
	marked := map[string]bool{}
	for _, line := range strings.Split(out.String(), "\n") {
		for _, name := range []string{"resize@v1", "wipe@v1", "post@v1", "wc@v1"} {
			if strings.Contains(line, name+" ") && !strings.Contains(line, "^") {
				marked[name] = strings.Contains(line, "!!")
			}
		}
	}
	if !marked["wipe@v1"] || !marked["post@v1"] {
		t.Errorf("destructive/external must be marked:\n%s", out.String())
	}
	if marked["resize@v1"] || marked["wc@v1"] {
		t.Errorf("read/write must not be marked:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "DESTRUCTIVE") || !strings.Contains(out.String(), "EXTERNAL") {
		t.Errorf("effect class names missing:\n%s", out.String())
	}
}

func TestWatchFormat(t *testing.T) {
	be := newFake()
	be.events = []ProvEvent{{Time: "2026-01-02T03:04:05Z", App: "org.aurora.lumen", Verb: "resize@v1", Output: "/o.png", Inputs: []string{"/i.png"}}}
	env, out, _ := harness(be, "")
	if code := Run(env, []string{"watch", "--count", "1"}); code != 0 {
		t.Fatal(code)
	}
	for _, w := range []string{"org.aurora.lumen", "resize@v1", "-> /o.png", "<- /i.png"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("missing %q in %q", w, out)
		}
	}
}

func TestHelpExitsZero(t *testing.T) {
	for _, c := range []string{"open", "chain", "verbs", "apps", "watch"} {
		env, _, _ := harness(newFake(), "")
		if code := Run(env, []string{c, "-h"}); code != 0 {
			t.Errorf("%s -h: exit %d", c, code)
		}
	}
}
