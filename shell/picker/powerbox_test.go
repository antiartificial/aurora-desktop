package picker

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// script writes an executable shell script and returns its path.
func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "aurora-chooser")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// chooserSaying is a chooser that prints out and exits with code.
func chooserSaying(t *testing.T, out string, code int) string {
	return script(t, fmt.Sprintf("printf '%%s' '%s'\nexit %d", out, code))
}

func grantLine(path string) string { return fmt.Sprintf(`{"path":%q}`+"\n", path) }

type rig struct {
	pb      Powerbox
	log     *bytes.Buffer
	ttyUsed *bool
}

// newRig wires a Powerbox the way main does, with a chooser binary, a
// terminal that answers ttyInput, and the confirmer given.
func newRig(t *testing.T, o Options, chooser, ttyInput string, cf Confirmer) rig {
	t.Helper()
	used := false
	log := &bytes.Buffer{}
	e := OSEnv()
	e.Getenv = func(k string) string {
		return map[string]string{"WAYLAND_DISPLAY": "wayland-9", "AURORA_CHOOSER": chooser}[k]
	}
	route, bin := PlanRoute(e, false)
	return rig{log: log, ttyUsed: &used, pb: Powerbox{Opts: o, Route: route, Chooser: bin, Confirm: cf, Log: log,
		TTY: func() (*bufio.Reader, io.Writer, func(), error) {
			used = true
			return bufio.NewReader(strings.NewReader(ttyInput)), io.Discard, func() {}, nil
		}}}
}

// compositorFor serves answer on a fake trusted socket and returns a
// Compositor that verifies with approvalKey.
func compositorFor(t *testing.T, answer func(wireRequest) any) (*Compositor, <-chan wireRequest) {
	sock, got := startCompositor(t, answer)
	return &Compositor{Socket: sock, Key: approvalKey}, got
}

func allowAll(q wireRequest) any { return signed(approvalKey, verdict(q.RequestID, "allow")) }

func TestChooserRouteIsTakenWhenDisplayAndBinaryExist(t *testing.T) {
	d := tree(t)
	want := filepath.Join(d, "b.txt")
	args := filepath.Join(t.TempDir(), "args")
	bin := script(t, fmt.Sprintf("echo \"$@\" > %s\nprintf '%%s\\n' '{\"path\":\"%s\"}'", args, want))
	cf, got := compositorFor(t, allowAll)
	o := opts(KindFile, "ro", d)
	o.Start, o.Suggest = d, "ignored.txt"
	r := newRig(t, o, bin, "", cf)
	if r.pb.Route != RouteChooser {
		t.Fatalf("route %v", r.pb.Route)
	}
	res, err := r.pb.Grant(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c := claims(t, res); res.Path != want || c.Path != want || c.Mode != "ro" || c.App != "org.x" || c.Verb != "copy@v1" {
		t.Fatalf("%+v %+v", res, c)
	}
	if *r.ttyUsed {
		t.Fatal("the terminal was used although the chooser answered")
	}
	b, _ := os.ReadFile(args)
	if a := strings.TrimSpace(string(b)); a != "--app org.x --verb copy@v1 --mode ro --kind file --start "+d+" --suggest ignored.txt" {
		t.Fatalf("chooser argv: %q", a)
	}
	if q := <-got; q.Grants[0].Path != want {
		t.Fatalf("compositor was shown %+v", q.Grants)
	}
}

func TestRouteSelection(t *testing.T) {
	bin := script(t, "exit 0")
	cases := []struct {
		name     string
		env      map[string]string
		forceTTY bool
		want     Route
	}{
		{"display and chooser", map[string]string{"WAYLAND_DISPLAY": "w", "AURORA_CHOOSER": bin}, false, RouteChooser},
		{"no display", map[string]string{"AURORA_CHOOSER": bin}, false, RouteTTY},
		{"display but no chooser", map[string]string{"WAYLAND_DISPLAY": "w"}, false, RouteTTY},
		{"--tty forces the terminal", map[string]string{"WAYLAND_DISPLAY": "w", "AURORA_CHOOSER": bin}, true, RouteForcedTTY},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := OSEnv()
			e.Getenv = func(k string) string { return c.env[k] }
			e.Self = ""
			e.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
			if got, _ := PlanRoute(e, c.forceTTY); got != c.want {
				t.Fatalf("%v, want %v", got, c.want)
			}
		})
	}
}

func TestFindChooserOrder(t *testing.T) {
	beside := t.TempDir()
	os.WriteFile(filepath.Join(beside, "aurora-chooser"), []byte("#!/bin/sh\n"), 0o755)
	onPath := "/on/path/aurora-chooser"
	look := func(string) (string, error) { return onPath, nil }
	env := func(m map[string]string) Env {
		return Env{Getenv: func(k string) string { return m[k] }, Self: filepath.Join(beside, "powerbox"), LookPath: look}
	}
	if got := FindChooser(env(map[string]string{"AURORA_CHOOSER": "/override"})); got != "/override" {
		t.Fatalf("override: %q", got)
	}
	if got := FindChooser(env(nil)); got != filepath.Join(beside, "aurora-chooser") {
		t.Fatalf("beside the executable: %q", got)
	}
	e := env(nil)
	e.Self = filepath.Join(t.TempDir(), "powerbox")
	if got := FindChooser(e); got != onPath {
		t.Fatalf("PATH: %q", got)
	}
	e.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if got := FindChooser(e); got != "" {
		t.Fatalf("none: %q", got)
	}
}

func TestForcedTTYNeverRunsTheChooser(t *testing.T) {
	d := tree(t)
	marker := filepath.Join(t.TempDir(), "ran")
	bin := script(t, "touch "+marker)
	e := OSEnv()
	e.Getenv = func(k string) string { return map[string]string{"WAYLAND_DISPLAY": "w", "AURORA_CHOOSER": bin}[k] }
	route, chooser := PlanRoute(e, true)
	pb := Powerbox{Opts: opts(KindFile, "ro", d), Route: route, Chooser: chooser, Confirm: Unattested{Why: "test"}, Log: io.Discard,
		TTY: func() (*bufio.Reader, io.Writer, func(), error) {
			return bufio.NewReader(strings.NewReader("2\ny\n")), io.Discard, func() {}, nil
		}}
	res, err := pb.Grant(context.Background())
	if err != nil || res.Path != filepath.Join(d, "b.txt") {
		t.Fatalf("%v %+v", err, res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("chooser ran under --tty")
	}
}

func TestChooserThatCannotServeFallsBackToTheTerminal(t *testing.T) {
	d := tree(t)
	notExec := filepath.Join(t.TempDir(), "chooser")
	os.WriteFile(notExec, []byte("#!/bin/sh\n"), 0o644)
	cases := []struct{ name, bin string }{
		{"binary is missing", filepath.Join(t.TempDir(), "nope")},
		{"binary is not executable", notExec},
		{"exit 2: no display reachable", chooserSaying(t, "", 2)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, opts(KindFile, "ro", d), c.bin, "2\ny\n", Unattested{Why: "test"})
			res, err := r.pb.Grant(context.Background())
			if err != nil || res.Path != filepath.Join(d, "b.txt") || claims(t, res).Path != res.Path {
				t.Fatalf("%v %+v", err, res)
			}
			if !*r.ttyUsed {
				t.Fatal("no fallback to the terminal")
			}
			if !strings.Contains(r.log.String(), "using the terminal") {
				t.Fatalf("fallback was silent: %q", r.log)
			}
		})
	}
}

func TestChooserCancelDoesNotFallBack(t *testing.T) {
	d := tree(t)
	// a terminal that would happily grant b.txt if asked
	r := newRig(t, opts(KindFile, "ro", d), chooserSaying(t, "", 3), "2\ny\n", Unattested{Why: "test"})
	res, err := r.pb.Grant(context.Background())
	if !errors.Is(err, ErrCancelled) || res.Ticket != "" {
		t.Fatalf("%v %+v", err, res)
	}
	if *r.ttyUsed {
		t.Fatal("asked again in the terminal after the user cancelled")
	}
}

func TestUnusableChooserAnswerMintsNothing(t *testing.T) {
	d := tree(t)
	cases := []struct {
		name string
		bin  string
	}{
		{"empty", chooserSaying(t, "", 0)},
		{"not json", chooserSaying(t, "granted", 0)},
		{"empty path", chooserSaying(t, `{"path":""}`, 0)},
		{"relative path", chooserSaying(t, `{"path":"b.txt"}`, 0)},
		{"no path field", chooserSaying(t, `{"file":"/etc/passwd"}`, 0)},
		{"two answers", chooserSaying(t, `{"path":"/a"} {"path":"/b"}`, 0)},
		{"trailing garbage", chooserSaying(t, `{"path":"`+filepath.Join(d, "b.txt")+`"} x`, 0)},
		{"a path on stdout but a failing exit", chooserSaying(t, grantLine(filepath.Join(d, "b.txt")), 1)},
		{"a path that does not exist", chooserSaying(t, grantLine(filepath.Join(d, "gone")), 0)},
		{"a directory for a file", chooserSaying(t, grantLine(d), 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cf, got := compositorFor(t, allowAll)
			r := newRig(t, opts(KindFile, "ro", d), c.bin, "2\ny\n", cf)
			res, err := r.pb.Grant(context.Background())
			if err == nil || res.Ticket != "" {
				t.Fatalf("minted from an unusable answer: %v %+v", err, res)
			}
			if *r.ttyUsed {
				t.Fatal("fell back to the terminal after the chooser ran")
			}
			select {
			case q := <-got:
				t.Fatalf("the compositor was asked about %+v", q)
			default:
			}
		})
	}
}

func TestOnlyAVerifiedAllowMints(t *testing.T) {
	d := tree(t)
	pick := chooserSaying(t, grantLine(filepath.Join(d, "b.txt")), 0)
	other := []byte("some-other-key-some-other-key-12")
	cases := []struct {
		name    string
		answer  func(wireRequest) any
		wantErr error // nil: must mint
		anyErr  bool
	}{
		{"allow", allowAll, nil, false},
		{"deny", func(q wireRequest) any { return signed(approvalKey, verdict(q.RequestID, "deny")) }, ErrDenied, false},
		{"allow with the wrong signature", func(q wireRequest) any { return signed(other, verdict(q.RequestID, "allow")) }, nil, true},
		{"allow for another request id", func(q wireRequest) any { return signed(approvalKey, verdict("pb-old", "allow")) }, nil, true},
		{"allow with no signature", func(q wireRequest) any { return verdict(q.RequestID, "allow") }, nil, true},
		{"no answer", nil, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cf, _ := compositorFor(t, c.answer)
			r := newRig(t, opts(KindFile, "ro", d), pick, "", cf)
			res, err := r.pb.Grant(context.Background())
			if c.wantErr == nil && !c.anyErr {
				if err != nil || claims(t, res).Path != filepath.Join(d, "b.txt") {
					t.Fatalf("%v %+v", err, res)
				}
				return
			}
			if err == nil || res.Ticket != "" || res.Path != "" {
				t.Fatalf("minted without a verified allow: %v %+v", err, res)
			}
			if c.wantErr != nil && !errors.Is(err, c.wantErr) {
				t.Fatalf("%v, want %v", err, c.wantErr)
			}
		})
	}
	t.Run("compositor socket set but absent", func(t *testing.T) {
		cf := &Compositor{Socket: filepath.Join(t.TempDir(), "none"), Key: approvalKey}
		res, err := newRig(t, opts(KindFile, "ro", d), pick, "", cf).pb.Grant(context.Background())
		if err == nil || res.Ticket != "" {
			t.Fatalf("%v %+v", err, res)
		}
	})
}

func TestTheTerminalRouteIsAttestedToo(t *testing.T) {
	d := tree(t)
	deny := func(q wireRequest) any { return signed(approvalKey, verdict(q.RequestID, "deny")) }
	cf, got := compositorFor(t, deny)
	r := newRig(t, opts(KindFile, "ro", d), "", "2\ny\n", cf)
	r.pb.Route = RouteForcedTTY
	res, err := r.pb.Grant(context.Background())
	if !errors.Is(err, ErrDenied) || res.Ticket != "" {
		t.Fatalf("%v %+v", err, res)
	}
	if q := <-got; q.Grants[0].Path != filepath.Join(d, "b.txt") {
		t.Fatalf("%+v", q.Grants)
	}
	// declining at the terminal never reaches the compositor
	cf2, got2 := compositorFor(t, allowAll)
	r = newRig(t, opts(KindFile, "ro", d), "", "2\nn\nq\n", cf2)
	r.pb.Route = RouteForcedTTY
	if _, err := r.pb.Grant(context.Background()); !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
	select {
	case q := <-got2:
		t.Fatalf("compositor asked after the user declined: %+v", q)
	default:
	}
}

func TestMintedTicketBindsTheResolvedPathThatTheCompositorWasShown(t *testing.T) {
	d := tree(t)
	link := filepath.Join(t.TempDir(), "link.txt")
	os.Symlink(filepath.Join(d, "b.txt"), link)
	cf, got := compositorFor(t, allowAll)
	r := newRig(t, opts(KindFile, "ro", ""), chooserSaying(t, grantLine(link), 0), "", cf)
	res, err := r.pb.Grant(context.Background())
	want := filepath.Join(d, "b.txt")
	if err != nil || res.Path != want || claims(t, res).Path != want {
		t.Fatalf("%v %+v", err, res)
	}
	if q := <-got; q.Grants[0].Path != want {
		t.Fatalf("the compositor was shown %q, the ticket binds %q", q.Grants[0].Path, want)
	}
}

func TestPathChangingDuringConfirmMintsNothing(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(root, "a"), 0o755)
	os.MkdirAll(filepath.Join(root, "evil"), 0o755)
	os.WriteFile(filepath.Join(root, "a", "f"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "evil", "f"), []byte("x"), 0o644)
	swap := confirmerFunc(func(context.Context, Ask) Confirmation {
		os.Rename(filepath.Join(root, "a"), filepath.Join(root, "a.old"))
		os.Symlink(filepath.Join(root, "evil"), filepath.Join(root, "a"))
		return Confirmation{Verdict: VerdictAllowed}
	})
	r := newRig(t, opts(KindFile, "ro", ""), chooserSaying(t, grantLine(filepath.Join(root, "a", "f")), 0), "", swap)
	res, err := r.pb.Grant(context.Background())
	if err == nil || res.Ticket != "" {
		t.Fatalf("the ticket binds a path nobody was shown: %v %+v", err, res)
	}
}

type confirmerFunc func(context.Context, Ask) Confirmation

func (f confirmerFunc) Confirm(ctx context.Context, a Ask) Confirmation { return f(ctx, a) }

func TestUnattestedGrantIsLoud(t *testing.T) {
	d := tree(t)
	r := newRig(t, opts(KindFile, "ro", d), chooserSaying(t, grantLine(filepath.Join(d, "b.txt")), 0), "", Unattested{Why: "--attest=off"})
	res, err := r.pb.Grant(context.Background())
	if err != nil || res.Ticket == "" {
		t.Fatalf("%v %+v", err, res)
	}
	if l := r.log.String(); !strings.Contains(l, "WARNING") || !strings.Contains(l, "WITHOUT compositor attestation") || !strings.Contains(l, "--attest=off") {
		t.Fatalf("a skipped attestation was not announced: %q", l)
	}
}

func TestSaveThroughTheChooserReportsOverwrite(t *testing.T) {
	d := tree(t)
	cf, got := compositorFor(t, allowAll)
	o := opts(KindSave, "rw", d)
	r := newRig(t, o, chooserSaying(t, grantLine(filepath.Join(d, "b.txt")), 0), "", cf)
	res, err := r.pb.Grant(context.Background())
	if err != nil || claims(t, res).Mode != "rw" {
		t.Fatalf("%v %+v", err, res)
	}
	if q := <-got; !q.Args.Overwrites || q.Grants[0].Mode != "rw" {
		t.Fatalf("%+v", q)
	}
}
