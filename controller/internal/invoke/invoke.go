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
	// Ticket authorises the input grant, OutputTicket the output grant: grant
	// tickets minted by the powerbox picker (see controller/ticket).
	Ticket       string `json:"ticket,omitempty"`
	OutputTicket string `json:"output_ticket,omitempty"`
}

// Result is the success reply. Provenance is nil for verbs that write no file.
type Result struct {
	OK       bool            `json:"ok"`
	Output   string          `json:"output,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Isolated bool            `json:"isolated"`
	Grants   []prov.Grant    `json:"grants"`
	// NeedsApproval is set when approval was required and given (the verb is
	// external/destructive and a grant was not ambient). Approval says how.
	NeedsApproval bool `json:"needs_approval,omitempty"`
	// Approval is set for external and destructive verbs only: whether it was
	// required, the route (interactive, auto-approve, ambient) and when.
	Approval   *prov.Approval `json:"approval,omitempty"`
	Log        string         `json:"log,omitempty"` // tail of the app's stderr
	Provenance *prov.Record   `json:"provenance,omitempty"`
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
	// RequirePowerbox refuses any grant that is neither ambient nor backed by
	// a valid powerbox ticket: bare gesture grants are rejected.
	RequirePowerbox bool
	// Approvals gates external/destructive verbs on non-ambient grants. nil
	// means no approval channel exists, and such verbs are refused.
	Approvals *Approver
	// Tickets verifies grant tickets; nil means every ticket is refused.
	Tickets *Tickets
	// Home expands "~/" in manifest paths (default: the user's home).
	Home    string
	Timeout time.Duration
	// SetProvenance writes the record onto the output file (default xattr).
	SetProvenance func(path string, r prov.Record) error
	// Bus, if set, gets each app domain registered when it is ensured so the
	// bus attributes the domain's connections to it; nil disables that.
	Bus DomainBus
	// Execs overrides executable lookup: app id to host path.
	Execs map[string]string

	clock      *hlc.Clock
	mu         sync.Mutex
	regMu      sync.Mutex
	registered map[string]string // domain name -> identity registered with the bus
	locks      map[string]*sync.Mutex
}

func (iv *Invoker) logf(f string, a ...any) {
	if iv.Logf != nil {
		iv.Logf(f, a...)
	}
}

// domainLock serialises invokes per domain (not per app id): invokes that
// share a domain share its grant list and its data/app image, so the lock key
// is the domain name itself and the invariant holds by construction.
func (iv *Invoker) domainLock(name string) *sync.Mutex {
	iv.mu.Lock()
	defer iv.mu.Unlock()
	if iv.locks == nil {
		iv.locks = map[string]*sync.Mutex{}
		iv.clock = hlc.New()
	}
	if iv.locks[name] == nil {
		iv.locks[name] = &sync.Mutex{}
	}
	return iv.locks[name]
}

var badName = regexp.MustCompile(`[^a-z0-9_-]`)

// DomainName maps an app id to a domain name (a-z 0-9 _ -, max 32): "app-",
// up to 11 characters of the sanitised id for readability, and always the
// first 64 bits of sha256 of the FULL id. The readable part is lossy ('.' and
// '-' both become '-'), the hash is what keeps distinct ids in distinct
// domains. Injective up to a 64-bit hash collision, which a 32-character
// name cannot do better than.
func DomainName(appID string) string {
	pre := badName.ReplaceAllString(strings.ToLower(appID), "-")
	if len(pre) > 11 {
		pre = strings.TrimRight(pre[:11], "-")
	}
	sum := sha256.Sum256([]byte(appID))
	return fmt.Sprintf("app-%s-%x", pre, sum[:8])
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
	succeeded := false
	input, output, e := checkPaths(req, verb, writes)
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

	grants := iv.planGrants(app.Manifest, input, output)
	redeem, e := iv.checkTickets(req, app.ID(), grants)
	if e != nil {
		return nil, e
	}
	if g, ok := firstGesture(grants); ok && iv.NoGestureGrants {
		return nil, fail("gesture_grant_refused", "%s would need a grant outside its manifest's declared capabilities", fmt.Sprintf("%s (%s) is not covered by any capabilities.paths of %s; --no-gesture-grants is set", g.Path, g.Mode, app.ID()), req.Verb)
	}

	if iv.RequirePowerbox {
		for _, g := range grants {
			if g.Source == prov.SourceGesture {
				return nil, fail("powerbox_required", "%s needs a grant that was neither declared by its manifest nor picked in the powerbox", fmt.Sprintf("%s (%s) is a bare gesture grant; --require-powerbox is set", g.Path, g.Mode), req.Verb)
			}
		}
	}
	// Blocks until a human (or --auto-approve) allows it; before any domain
	// lock, output file, ticket spend or grant, so a denial leaves nothing.
	approval, e := iv.approve(ctx, app, verb, req.Verb, argsJSON, grants)
	if e != nil {
		return nil, e
	}
	if e := redeem(); e != nil {
		return nil, e
	}

	l := iv.domainLock(DomainName(app.ID()))
	l.Lock()
	defer l.Unlock()
	if iv.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, iv.Timeout)
		defer cancel()
	}

	if writes {
		created, e := prepareOutput(output)
		if e != nil {
			return nil, e
		}
		if created {
			defer func() {
				if !succeeded {
					os.Remove(output)
				}
			}()
		}
	}
	var ident domIdent
	res, isolated, e := iv.execute(ctx, app, exe, argv, stdin, grants, &ident)
	if e != nil {
		return nil, e
	}
	if res.Exit != 0 {
		return nil, fail("app_failed", "%s exited with status %d", tail(res.Stderr), req.Verb, res.Exit)
	}
	out := &Result{OK: true, Isolated: isolated, Grants: grants, NeedsApproval: approval != nil && approval.Required, Approval: approval, Log: tail(res.Stderr)}
	if t := bytes.TrimSpace(res.Stdout); len(t) > 0 && json.Valid(t) {
		out.Result = json.RawMessage(t)
	}
	if !writes {
		return out, nil
	}
	if st, err := os.Stat(output); err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return nil, fail("no_output", "%s finished but wrote no file at %s", tail(res.Stderr), req.Verb, output)
	}
	rec := prov.Record{File: output, App: app.ID(), Verb: req.Verb, Inputs: []string{input},
		Domain: ident.ID, DomainAttested: ident.Attested, Args: argsJSON, Isolated: isolated, Grants: grants, Approval: approval, TS: iv.clock.Now().String()}
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
	succeeded = true
	return out, nil
}

// checkPaths canonicalises input and output and checks they are usable. The
// output is its resolved directory plus its base name; a base name that is
// itself a symlink or a directory is refused, so the one granted file is the
// file the caller named and not wherever a link points.

func checkPaths(req Request, verb manifest.Verb, writes bool) (input, output string, e *Error) {
	if req.Input != "" || len(verb.Accepts) > 0 {
		if !filepath.IsAbs(req.Input) {
			return "", "", fail("bad_request", "input must be an absolute path", "got "+req.Input)
		}
		p, err := filepath.EvalSymlinks(req.Input)
		if err != nil {
			return "", "", fail("bad_input", "cannot read input", err.Error())
		}
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			return "", "", fail("bad_input", "input is not a regular file", p)
		}
		input = p
	}
	if !writes {
		if req.Output != "" {
			return "", "", fail("bad_request", "%s is a read verb and takes no output", "", req.Verb)
		}
		return input, "", nil
	}
	if !filepath.IsAbs(req.Output) {
		return "", "", fail("bad_request", "%s writes a file: output must be an absolute path", "got "+req.Output, req.Verb)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(req.Output))
	if err != nil {
		return "", "", fail("bad_output", "output directory unusable", err.Error())
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", "", fail("bad_output", "output directory is not a directory", dir)
	}
	output = filepath.Join(dir, filepath.Base(req.Output))
	if st, err := os.Lstat(output); err == nil && !st.Mode().IsRegular() {
		return "", "", fail("bad_output", "output exists and is not a regular file (symlinks are refused)", output)
	}
	if output == input {
		return "", "", fail("bad_request", "output must differ from input", "")
	}
	return input, output, nil
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
