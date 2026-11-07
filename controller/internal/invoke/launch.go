package invoke

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// A window is launched, not invoked (decision 0012): there is no request JSON,
// no result and no deadline, because the user decides when it ends. What it
// shares with a verb is everything that makes a domain a domain: the grants
// are minted as bus handles, composed into the domain and announced so the
// capability indicator can show them (decision 0016), and they are revoked
// when the process is gone. What it does not share is the invoke path itself,
// which would turn a window into a verb whose "result" arrives minutes later.

// LaunchRequest is the registry.launch@v1 payload.
type LaunchRequest struct {
	App string `json:"app"`
}

// LaunchResult is the success reply. It is sent once the app has survived
// LaunchGrace, so an app that cannot start is an error and not a result.
type LaunchResult struct {
	OK       bool   `json:"ok"`
	App      string `json:"app"`
	Domain   string `json:"domain,omitempty"`
	Identity string `json:"identity,omitempty"`
	// Isolated is false only for the explicit escape hatch (UnsandboxedWindows).
	Isolated bool         `json:"isolated"`
	Grants   []prov.Grant `json:"grants"`
	CapNote  string       `json:"cap_note,omitempty"`
	// Warning is set when the app was launched without a sandbox.
	Warning string `json:"warning,omitempty"`
}

// StopRequest is the registry.stop@v1 payload: one launch by domain name, or
// every launch of an app.
type StopRequest struct {
	App    string `json:"app,omitempty"`
	Domain string `json:"domain,omitempty"`
}

// DefaultLaunchGrace is how long a launch waits to see the app stay up.
const DefaultLaunchGrace = 1500 * time.Millisecond

// LaunchFailureTTL is how long a failed launch is reported as the app's
// launch_problem in registry.apps@v1. It expires so a fixed app can be tried
// again from the dock, which does not offer a launch for an app that is
// reported as not launchable.
const LaunchFailureTTL = 30 * time.Second

type launched struct {
	app, name string
	cancel    context.CancelFunc
	done      chan struct{}
	exit      int
	stderr    string
}

type launchFailure struct {
	msg string
	at  time.Time
}

type launchState struct {
	mu       sync.Mutex
	ctx      context.Context
	stop     context.CancelFunc
	running  map[string]*launched // domain name -> launch
	failures map[string]launchFailure
	wg       sync.WaitGroup
}

func (iv *Invoker) launches() *launchState {
	iv.launchOnce.Do(func() {
		s := &launchState{running: map[string]*launched{}, failures: map[string]launchFailure{}}
		s.ctx, s.stop = context.WithCancel(context.Background())
		iv.lstate = s
	})
	return iv.lstate
}

// LaunchDomainName names the domain of one launch: "win-", up to eight
// characters of the sanitised app id, eight hex digits of its hash, and six
// random ones so two windows of one app are two domains. At most 28 characters.
func LaunchDomainName(appID string) string {
	pre := badName.ReplaceAllString(strings.ToLower(appID[strings.LastIndex(appID, ".")+1:]), "-")
	if len(pre) > 8 {
		pre = strings.TrimRight(pre[:8], "-")
	}
	sum := sha256.Sum256([]byte(appID))
	var r [3]byte
	_, _ = rand.Read(r[:])
	return fmt.Sprintf("win-%s-%x-%x", pre, sum[:4], r[:])
}

// LaunchProblem reports why an app that would otherwise launch should not be
// offered right now: its last launch failed, recently. "" means nothing known.
func (iv *Invoker) LaunchProblem(appID string) string {
	s := iv.launches()
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.failures[appID]
	if !ok {
		return ""
	}
	if time.Since(f.at) > LaunchFailureTTL {
		delete(s.failures, appID)
		return ""
	}
	return "last launch failed: " + f.msg
}

// Launch starts a windowed app. See the package comment above.
func (iv *Invoker) Launch(ctx context.Context, req LaunchRequest) (*LaunchResult, *Error) {
	app, ok := iv.Reg.App(req.App)
	if !ok {
		return nil, fail("no_such_app", "no such app: %q", "", req.App)
	}
	if !app.Manifest.App.Windowed {
		return nil, fail("not_windowed", "app %s is not windowed: it is invoked through its verbs, not launched", "decision 0012", app.ID())
	}
	res, e := iv.launch(ctx, app)
	if e != nil {
		s := iv.launches()
		s.mu.Lock()
		msg := e.Msg
		if e.Detail != "" {
			msg += ": " + oneLine(e.Detail)
		}
		s.failures[app.ID()] = launchFailure{msg: msg, at: time.Now()}
		s.mu.Unlock()
		iv.logf("launch of %s failed: %s", app.ID(), msg)
	}
	return res, e
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[i+1:])
	}
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// launchGrants lists a window's grants: the display, which is what makes it a
// window, then what its manifest declares and nothing else. Declared paths are
// ambient, as they are for a verb. Declared network hosts are NOT granted here:
// nothing in this tree grants a verb a host either (decision 0015 is a
// `domain grant --net` the controller has not been wired to), so a windowed
// app has no network, and the launch says nothing different.
func (iv *Invoker) launchGrants(app *apps.App) []prov.Grant {
	gs := []prov.Grant{{Path: "device:wayland", Mode: prov.ModeUse, Device: "wayland", Source: prov.SourceAmbient}}
	for _, d := range app.Manifest.Capabilities.Devices {
		if d != "wayland" {
			gs = append(gs, prov.Grant{Path: "device:" + d, Mode: prov.ModeUse, Device: d, Source: prov.SourceAmbient})
		}
	}
	for _, c := range app.Manifest.Capabilities.Paths {
		p := iv.expand(c.Path)
		if _, err := os.Stat(p); err != nil {
			iv.logf("declared path %s of %s does not exist; not granted", p, app.ID())
			continue
		}
		mode := "ro"
		if c.Access == "write" {
			mode = "rw"
		}
		gs = append(gs, prov.Grant{Path: p, Mode: mode, Source: prov.SourceAmbient})
	}
	return gs
}

func (iv *Invoker) launch(ctx context.Context, app *apps.App) (*LaunchResult, *Error) {
	if ok, why := app.Launchable(); !ok {
		return nil, fail("not_launchable", "app %s cannot be launched", why, app.ID())
	}
	exe, _ := app.ExecStatus()
	if iv.UnsandboxedWindows {
		return iv.launchDirect(app, exe)
	}
	if err := iv.Runner.Available(); err != nil {
		return nil, fail("isolation_unavailable", "domain tooling unavailable, and a window is not launched outside a domain unless --unsandboxed-windows is set", err.Error())
	}
	s := iv.launches()
	name := LaunchDomainName(app.ID())
	if err := iv.Runner.Ensure(name); err != nil {
		return nil, fail("domain_error", "cannot create domain %s", err.Error(), name)
	}
	l := &launched{app: app.ID(), name: name, done: make(chan struct{})}
	var undo []func()
	teardown := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		if err := iv.Teardown(name); err != nil {
			iv.logf("WARNING: %v", err)
		}
		if err := iv.Runner.Destroy(name); err != nil {
			iv.logf("WARNING: destroy %s failed (stale domain): %v", name, err)
		}
	}
	imgExe, err := iv.Runner.InstallImage(name, app.Dir, exe)
	if err != nil {
		teardown()
		return nil, fail("domain_error", "cannot install app image into %s", err.Error(), name)
	}
	grants := iv.launchGrants(app)
	cs := iv.mintCapsTTL(grants, 0)
	undo = append(undo, func() { cs.release(iv.logf) })
	ident := domIdent{}
	ident.ID, ident.Attested = iv.domainIdentity(name)
	for _, g := range grants {
		g := g
		if g.Device != "" {
			if err := iv.Runner.GrantDevice(name, g.Device); err != nil {
				teardown()
				return nil, fail("domain_error", "grant device %s to %s failed", err.Error(), g.Device, name)
			}
			undo = append(undo, func() {
				if err := iv.Runner.RevokeDevice(name, g.Device); err != nil {
					iv.logf("WARNING: revoke device %s from %s failed (stale grant): %v", g.Device, name, err)
				}
			})
			iv.announceHeld(g, ident.ID, app.ID())
			continue
		}
		if err := iv.Runner.Grant(name, g.Path, g.Mode); err != nil {
			teardown()
			return nil, fail("domain_error", "grant %s (%s) to %s failed", err.Error(), g.Path, g.Mode, name)
		}
		undo = append(undo, func() {
			if err := iv.Runner.Revoke(name, g.Path); err != nil {
				iv.logf("WARNING: revoke %s from %s failed (stale grant): %v", g.Path, name, err)
			}
		})
	}
	lctx, cancel := context.WithCancel(s.ctx)
	l.cancel = cancel
	proc, err := iv.Runner.Start(lctx, name, []string{imgExe})
	if err != nil {
		cancel()
		teardown()
		return nil, fail("domain_error", "cannot start %s in %s", err.Error(), app.ID(), name)
	}
	s.mu.Lock()
	s.running[name] = l
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		exit, werr := proc.Wait()
		cancel()
		l.exit, l.stderr = exit, proc.Stderr()
		if werr != nil {
			l.stderr += "\n" + werr.Error()
		}
		teardown()
		s.mu.Lock()
		delete(s.running, name)
		s.mu.Unlock()
		iv.logf("window %s (domain %s) ended with status %d; its domain and grants are gone", app.ID(), name, exit)
		close(l.done)
	}()

	grace := iv.LaunchGrace
	if grace <= 0 {
		grace = DefaultLaunchGrace
	}
	select {
	case <-l.done:
		why := fmt.Sprintf("exited with status %d within %s", l.exit, grace)
		return nil, fail("app_failed", "%s did not stay up", tail([]byte(why+"\n"+l.stderr)), app.ID())
	case <-time.After(grace):
	case <-ctx.Done():
	}
	// The domain tooling says "running WITHOUT its own uid" or "WITHOUT a
	// domain cgroup" on stderr when it could not enforce what a domain is for.
	// A window whose sandbox degraded is not offered as a sandboxed window.
	if e := proc.Stderr(); strings.Contains(e, "running WITHOUT") {
		l.cancel()
		<-l.done
		return nil, fail("isolation_degraded", "%s was stopped: its domain could not be fully enforced", oneLineMatching(e, "running WITHOUT"), app.ID())
	}
	iv.logf("launched %s in domain %s as %s (isolated; grants: %s)", app.ID(), name, ident.ID, grantNames(grants))
	return &LaunchResult{OK: true, App: app.ID(), Domain: name, Identity: ident.ID, Isolated: true, Grants: grants, CapNote: cs.note}, nil
}

func oneLineMatching(s, sub string) string {
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, sub) {
			return strings.TrimSpace(ln)
		}
	}
	return ""
}

func grantNames(gs []prov.Grant) string {
	var n []string
	for _, g := range gs {
		n = append(n, g.Resource())
	}
	return strings.Join(n, ", ")
}

// launchDirect is the escape hatch: the app runs as a plain host process, as
// every windowed app did before this existed. It is only reached when the
// controller was started with --unsandboxed-windows, and it says so on every
// launch, in the log and in the reply.
func (iv *Invoker) launchDirect(app *apps.App, exe string) (*LaunchResult, *Error) {
	disp := hostDisplay()
	if disp == "" {
		return nil, fail("no_display", "cannot find the compositor's socket for an unsandboxed launch", "set AURORA_WAYLAND, or start the stack with aurora-up")
	}
	warn := "UNSANDBOXED: " + app.ID() + " runs as a host process with the user's files, network and environment (controllerd --unsandboxed-windows)"
	iv.logf("WARNING: %s", warn)
	cmd := exec.Command(exe)
	cmd.Dir = app.Dir
	cmd.Env = append(os.Environ(), "WAYLAND_DISPLAY="+disp)
	var eb tailWriter
	cmd.Stderr = &eb
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fail("exec_error", "cannot start %s", err.Error(), exe)
	}
	done := make(chan struct{})
	go func() {
		err := cmd.Wait()
		iv.logf("unsandboxed window %s ended: %v", app.ID(), err)
		close(done)
	}()
	grace := iv.LaunchGrace
	if grace <= 0 {
		grace = DefaultLaunchGrace
	}
	select {
	case <-done:
		return nil, fail("app_failed", "%s did not stay up", tail([]byte(eb.String())), app.ID())
	case <-time.After(grace):
	}
	return &LaunchResult{OK: true, App: app.ID(), Isolated: false, Grants: []prov.Grant{}, Warning: warn}, nil
}

type tailWriter struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 8<<10 {
		t.b = append([]byte(nil), t.b[len(t.b)-8<<10:]...)
	}
	return len(p), nil
}

func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

// hostDisplay is the compositor's socket name for a host process: AURORA_WAYLAND,
// else the name aurora-up wrote. WAYLAND_DISPLAY is not consulted, as in the
// adapter: in a user's shell it names the host's compositor.
func hostDisplay() string {
	if d := os.Getenv("AURORA_WAYLAND"); d != "" {
		return d
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		if b, err := os.ReadFile(filepath.Join(rt, "aurora", "display")); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// Stop ends launches: one domain, or every launch of an app. It returns the
// domains asked to stop; each one's teardown completes before Stop returns, or
// after five seconds.
func (iv *Invoker) Stop(req StopRequest) ([]string, *Error) {
	if req.App == "" && req.Domain == "" {
		return nil, fail("bad_request", "stop needs an app or a domain", "")
	}
	s := iv.launches()
	s.mu.Lock()
	var ls []*launched
	for n, l := range s.running {
		if (req.Domain != "" && n == req.Domain) || (req.Domain == "" && l.app == req.App) {
			ls = append(ls, l)
		}
	}
	s.mu.Unlock()
	names := []string{}
	for _, l := range ls {
		l.cancel()
		names = append(names, l.name)
	}
	for _, l := range ls {
		select {
		case <-l.done:
		case <-time.After(5 * time.Second):
		}
	}
	return names, nil
}

// stopLaunches ends every running window and waits for its teardown. Called by Close.
func (iv *Invoker) stopLaunches() {
	s := iv.launches()
	s.stop()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		iv.logf("WARNING: some windows did not end within 10s of shutdown; their domains may remain (domain list)")
	}
}
