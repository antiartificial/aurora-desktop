// Package invoke runs a verb: validates the request, grants a domain exactly
// the requested input and output (classified ambient or gesture), runs the app, revokes, records provenance.
package invoke

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/antiartificial/aurora/bus/hlc"
	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// Request is the registry.invoke@v1 payload.
type Request struct {
	App    string                     `json:"app"`
	Verb   string                     `json:"verb"`
	Input  string                     `json:"input"`
	Args   map[string]json.RawMessage `json:"args,omitempty"`
	Output string                     `json:"output,omitempty"`
}

// Result is the success reply. Provenance is nil for verbs that write no file.
type Result struct {
	OK       bool            `json:"ok"`
	Output   string          `json:"output,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Isolated bool            `json:"isolated"`
	Grants   []prov.Grant    `json:"grants"`
	// NeedsApproval is set when an external or destructive verb ran on a
	// gesture grant. There is no approval UI yet: this is a flag, not a gate.
	NeedsApproval bool         `json:"needs_approval,omitempty"`
	Log           string       `json:"log,omitempty"` // tail of the app's stderr
	Provenance    *prov.Record `json:"provenance,omitempty"`
}

// Error is the failure reply: {"ok":false,"error":...,"detail":...}.
type Error struct {
	Code   string `json:"code"`
	Msg    string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func fail(code, msg, detail string, a ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(msg, a...), Detail: detail}
}

// Invoker executes verbs.
type Invoker struct {
	Reg              *apps.Registry
	Runner           domain.Runner
	Publish          func(topic string, payload []byte) error
	Logf             func(format string, a ...any)
	RequireIsolation bool
	// NoGestureGrants refuses any grant not covered by the manifest.
	NoGestureGrants bool
	// Home expands "~/" in manifest paths (default: the user's home).
	Home    string
	Timeout time.Duration
	// SetProvenance writes the record onto the output file (default xattr).
	SetProvenance func(path string, r prov.Record) error
	// Execs overrides executable lookup: app id to host path.
	Execs map[string]string

	clock *hlc.Clock
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (iv *Invoker) logf(f string, a ...any) {
	if iv.Logf != nil {
		iv.Logf(f, a...)
	}
}

// appLock serialises invokes per app: they share one domain, and grants are
// per-domain state.
func (iv *Invoker) appLock(id string) *sync.Mutex {
	iv.mu.Lock()
	defer iv.mu.Unlock()
	if iv.locks == nil {
		iv.locks = map[string]*sync.Mutex{}
		iv.clock = hlc.New()
	}
	if iv.locks[id] == nil {
		iv.locks[id] = &sync.Mutex{}
	}
	return iv.locks[id]
}

var badName = regexp.MustCompile(`[^a-z0-9_-]`)

// DomainName maps an app id to a domain name (a-z 0-9 _ -, max 32).
func DomainName(appID string) string {
	n := "app-" + badName.ReplaceAllString(strings.ToLower(appID), "-")
	if len(n) > 32 {
		sum := sha256.Sum256([]byte(appID))
		n = n[:25] + fmt.Sprintf("-%x", sum[:3])
	}
	return n
}

// Invoke runs one verb.
func (iv *Invoker) Invoke(ctx context.Context, req Request) (*Result, *Error) {
	app, ok := iv.Reg.App(req.App)
	if !ok {
		return nil, fail("no_such_app", "no such app: %q", "", req.App)
	}
	verb, ok := app.Verb(req.Verb)
	if !ok {
		return nil, fail("no_such_verb", "app %s has no verb %q", "", req.App, req.Verb)
	}
	writes := verb.Effect != manifest.EffectRead
	input, outDir, output, e := checkPaths(req, verb, writes)
	if e != nil {
		return nil, e
	}
	if len(verb.Accepts) > 0 {
		if t := sniffMIME(input); t != "" && !acceptsMIME(verb.Accepts, t) {
			return nil, fail("unsupported_type", "%s does not accept %s", fmt.Sprintf("input %s; accepts %v", input, verb.Accepts), req.Verb, t)
		}
	}
	if err := checkArgs(verb.Input, req.Args); err != nil {
		return nil, fail("bad_args", "invalid arguments for %s", err.Error(), req.Verb)
	}
	exe, err := iv.findExe(app)
	if err != nil {
		return nil, fail("not_implemented", "app %s has no runnable implementation", err.Error(), app.ID())
	}
	args := req.Args
	if args == nil {
		args = map[string]json.RawMessage{}
	}
	argsJSON, _ := json.Marshal(args)
	argv := []string{"--verb", req.Verb}
	stdin, _ := json.Marshal(struct {
		Input  string                     `json:"input,omitempty"`
		Output string                     `json:"output,omitempty"`
		Args   map[string]json.RawMessage `json:"args"`
	}{input, output, args})

	grants := iv.planGrants(app.Manifest, input, outDir)
	needsApproval := false
	if g, ok := firstGesture(grants); ok {
		if iv.NoGestureGrants {
			return nil, fail("gesture_grant_refused", "%s would need a grant outside its manifest's declared capabilities", fmt.Sprintf("%s (%s) is not covered by any capabilities.paths of %s; --no-gesture-grants is set", g.Path, g.Mode, app.ID()), req.Verb)
		}
		if verb.Effect == manifest.EffectExternal || verb.Effect == manifest.EffectDestructive {
			needsApproval = true
			iv.logf("WARNING: NEEDS APPROVAL: %s verb %s of %s runs on gesture grant %s (%s), not covered by its manifest; no approval UI exists yet, proceeding", verb.Effect, req.Verb, app.ID(), g.Path, g.Mode)
		}
	}

	l := iv.appLock(app.ID())
	l.Lock()
	defer l.Unlock()
	if iv.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, iv.Timeout)
		defer cancel()
	}

	res, isolated, e := iv.execute(ctx, app, exe, argv, stdin, grants)
	if e != nil {
		return nil, e
	}
	if res.Exit != 0 {
		return nil, fail("app_failed", "%s exited with status %d", tail(res.Stderr), req.Verb, res.Exit)
	}
	out := &Result{OK: true, Isolated: isolated, Grants: grants, NeedsApproval: needsApproval, Log: tail(res.Stderr)}
	if t := bytes.TrimSpace(res.Stdout); len(t) > 0 && json.Valid(t) {
		out.Result = json.RawMessage(t)
	}
	if !writes {
		return out, nil
	}
	if st, err := os.Stat(output); err != nil || !st.Mode().IsRegular() {
		return nil, fail("no_output", "%s finished but wrote no file at %s", tail(res.Stderr), req.Verb, output)
	}
	rec := prov.Record{File: output, App: app.ID(), Verb: req.Verb, Inputs: []string{input},
		Args: argsJSON, Isolated: isolated, Grants: grants, TS: iv.clock.Now().String()}
	set := iv.SetProvenance
	if set == nil {
		set = prov.Write
	}
	if err := set(output, rec); err != nil {
		iv.logf("WARNING: could not write provenance xattr on %s: %v (event still published)", output, err)
	}
	if iv.Publish != nil {
		b, _ := json.Marshal(rec)
		if err := iv.Publish(prov.Topic, b); err != nil {
			iv.logf("WARNING: could not publish %s: %v", prov.Topic, err)
		}
	}
	out.Output, out.Provenance = output, &rec
	return out, nil
}

// checkPaths canonicalises input and output and checks they are usable.
func checkPaths(req Request, verb manifest.Verb, writes bool) (input, outDir, output string, e *Error) {
	if req.Input != "" || len(verb.Accepts) > 0 {
		if !filepath.IsAbs(req.Input) {
			return "", "", "", fail("bad_request", "input must be an absolute path", "got "+req.Input)
		}
		p, err := filepath.EvalSymlinks(req.Input)
		if err != nil {
			return "", "", "", fail("bad_input", "cannot read input", err.Error())
		}
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			return "", "", "", fail("bad_input", "input is not a regular file", p)
		}
		input = p
	}
	if !writes {
		if req.Output != "" {
			return "", "", "", fail("bad_request", "%s is a read verb and takes no output", "", req.Verb)
		}
		return input, "", "", nil
	}
	if !filepath.IsAbs(req.Output) {
		return "", "", "", fail("bad_request", "%s writes a file: output must be an absolute path", "got "+req.Output, req.Verb)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(req.Output))
	if err != nil {
		return "", "", "", fail("bad_output", "output directory unusable", err.Error())
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", "", "", fail("bad_output", "output directory is not a directory", dir)
	}
	output = filepath.Join(dir, filepath.Base(req.Output))
	if output == input {
		return "", "", "", fail("bad_request", "output must differ from input", "")
	}
	return input, dir, output, nil
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 2000 {
		s = "..." + s[len(s)-2000:]
	}
	return s
}

// findExe locates an app's executable: an explicit override, else in the app
// directory `run`, `<last id segment>` or `bin/<last id segment>`.
func (iv *Invoker) findExe(app *apps.App) (string, error) {
	if p, ok := iv.Execs[app.ID()]; ok {
		p, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			return "", fmt.Errorf("override executable %s is not a file", p)
		}
		return p, nil
	}
	seg := app.ID()[strings.LastIndex(app.ID(), ".")+1:]
	for _, rel := range []string{"run", seg, filepath.Join("bin", seg)} {
		p := filepath.Join(app.Dir, rel)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode().Perm()&0o100 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("no executable run, %s or bin/%s in %s (the app's code has not been built or installed)", seg, seg, app.Dir)
}
