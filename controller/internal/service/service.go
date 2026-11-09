// Package service serves the registry.* verbs on the bus.
package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/contracts/plumb"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/prov"
	ctlsurface "github.com/antiartificial/aurora/controller/internal/surface"
	"github.com/antiartificial/aurora/controller/internal/sysaudio"
	"github.com/antiartificial/aurora/controller/internal/sysdisplay"
	"github.com/antiartificial/aurora/controller/internal/sysnet"
)

// Service answers registry requests.
type Service struct {
	Reg *apps.Registry
	Inv *invoke.Invoker
	// Rules is the plumb rule set for registry.plumb@v1; nil means the
	// embedded default.
	Rules *plumb.RuleSet
	// How the audio verbs run their tool. nil means really run it; the tests
	// replace it so the parsing can be checked without an audio server.
	Audio sysaudio.Runner
	// ThemeDir holds aurora-glass.toml, for system.pointer@v1 and
	// system.keyboard@v1. Empty means those verbs answer "unavailable" and name
	// the flag: it is NOT guessed, because the compositor's own default is the
	// repo's contracts/theme and a controller that assumed it would answer
	// about a tree it may not be running from.
	ThemeDir string
	// Where the display and network verbs read. Zero value means the real /sys;
	// the tests point them at a fixture tree.
	DisplayRoots sysdisplay.Roots
	NetRoots     sysnet.Roots
	// NetLister is the kernel interface list; nil means the real netlink call.
	NetLister sysnet.Lister
}

// audio is Audio, or the real exec.
func (s *Service) audio(name string, args ...string) (string, error) {
	if s.Audio != nil {
		return s.Audio(name, args...)
	}
	return sysaudio.Exec(name, args...)
}

// Serve handles requests from c until it closes. Each runs in its own
// goroutine so a slow invoke does not block the registry queries.
func (s *Service) Serve(c *client.Client) {
	for req := range c.Requests() {
		go s.handle(c, req)
	}
}

func fail(c *client.Client, req proto.Envelope, e *invoke.Error) {
	// Error-kind envelope. The payload carries the bus's code/message and the
	// registry's {"ok":false,"error","detail"} so one reply serves both readers.
	p, _ := json.Marshal(map[string]any{
		"code": e.Code, "message": e.Msg,
		"ok": false, "error": e.Msg, "detail": e.Detail,
	})
	c.Send(proto.Envelope{Kind: proto.KindError, Verb: req.Verb, Corr: req.ID, Payload: p, QoS: req.QoS})
}

func (s *Service) handle(c *client.Client, req proto.Envelope) {
	out, e := s.dispatch(req)
	if e != nil {
		fail(c, req, e)
		return
	}
	b, _ := json.Marshal(out)
	c.Reply(req, b)
}

// dispatch answers one request. req.From is set by busd from SO_PEERCRED, so
// it is what the kernel says the caller is, not what the caller claims.
func (s *Service) dispatch(req proto.Envelope) (out any, e *invoke.Error) {
	// The system.* family. Its effect class and caller rule are declared in one table
	// (system.go) and applied there, so the policy is not nine `if` statements in nine arms
	// where a forgotten one is a verb silently open to every domain.
	if out, e, handled := s.systemDispatch(req); handled {
		return out, e
	}
	switch req.Verb {
	case "approval.decide@v1":
		return s.decide(req)
	case "registry.verify@v1":
		out, e = s.verify(req)
	case "registry.apps@v1":
		out = s.listApps()
	case "registry.verbs@v1":
		var q struct {
			Accepts string `json:"accepts"`
		}
		if e = decode(req, &q); e == nil {
			out = s.listVerbs(q.Accepts)
		}
	case "registry.plumb@v1":
		var q plumb.Request
		if e = decode(req, &q); e == nil {
			out = s.plumb(q, req.From)
		}
	case "registry.describe@v1":
		var q struct{ App, Verb string }
		if e = decode(req, &q); e == nil {
			// A descriptor names what an app can do and which grants it needs, so reading one is
			// already a disclosure. This is the same gate ui.act uses, not a second, looser one.
			if e = s.surfaceCaller(req.From, q.App); e == nil {
				out, e = s.describe(q.App, q.Verb)
			}
		}
	case "registry.launch@v1", "registry.stop@v1":
		// A window is the user's to open and close. A domain's app asking the
		// controller to start another app (inside another domain, with a
		// display grant) is not something any manifest has asked for.
		if !mayDecide(req.From) {
			return nil, &invoke.Error{Code: "not_permitted", Msg: req.Verb + " may only be called from the session socket", Detail: "caller " + req.From + " is not the session user"}
		}
		if req.Verb == "registry.launch@v1" {
			var q invoke.LaunchRequest
			if e = decode(req, &q); e == nil {
				out, e = launchResult(s.Inv.Launch(context.Background(), q))
			}
		} else {
			var q invoke.StopRequest
			if e = decode(req, &q); e == nil {
				var names []string
				if names, e = s.Inv.Stop(q); e == nil {
					out = map[string]any{"ok": true, "stopped": names}
				}
			}
		}
	case "ui.act@v1":
		// One surface action, run only when the app holds every grant it
		// declares. The gate and its reasoning are in act.go.
		return s.act(req)
	case "registry.invoke@v1":
		var q invoke.Request
		if e = decode(req, &q); e == nil {
			out, e = invokeResult(s.Inv.Invoke(context.Background(), q))
		}
	default:
		e = &invoke.Error{Code: "no_such_verb", Msg: "registry has no verb " + req.Verb}
	}
	return out, e
}

func launchResult(r *invoke.LaunchResult, e *invoke.Error) (any, *invoke.Error) {
	if e != nil {
		return nil, e
	}
	return r, nil
}

func invokeResult(r *invoke.Result, e *invoke.Error) (any, *invoke.Error) {
	if e != nil {
		return nil, e
	}
	return r, nil
}

func decode(req proto.Envelope, into any) *invoke.Error {
	if len(req.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Payload, into); err != nil {
		return &invoke.Error{Code: "bad_request", Msg: "payload is not valid for " + req.Verb, Detail: err.Error()}
	}
	return nil
}

type appInfo struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Verbs   []string `json:"verbs"`
	// Windowed apps are launched directly (decision 0012). Exec is the resolved
	// path; ExecProblem is non-empty when it cannot be launched (e.g. not built).
	Windowed bool `json:"windowed,omitempty"`
	// Whether a dock click can do anything, and why not. Decided in
	// apps.App.Launchable, next to the filesystem.
	Launchable    bool   `json:"launchable"`
	LaunchProblem string `json:"launch_problem,omitempty"`
	Exec          string `json:"exec,omitempty"`
	ExecProblem   string `json:"exec_problem,omitempty"`
	// Category is the manifest's, unchanged, and presentation only. Empty
	// means the app files itself under no section.
	Category string `json:"category,omitempty"`
}

func (s *Service) listApps() any {
	list := []appInfo{}
	for _, a := range s.Reg.Apps() {
		m := a.Manifest
		info := appInfo{ID: m.App.ID, Name: m.App.Name, Version: m.App.Version, Verbs: []string{}}
		info.Windowed = m.App.Windowed
		info.Category = m.App.Category
		info.Exec, info.ExecProblem = a.ExecStatus()
		info.Launchable, info.LaunchProblem = a.Launchable()
		// A launch that just failed is reported on the same channel, for a while.
		if info.Launchable && info.Windowed {
			if p := s.Inv.LaunchProblem(a.ID()); p != "" {
				info.Launchable, info.LaunchProblem = false, p
			}
		}
		for _, v := range m.Verbs {
			info.Verbs = append(info.Verbs, v.Name)
		}
		list = append(list, info)
	}
	// categories is the vocabulary in display order; an app's section sorts by
	// its index here (decision 0025).
	return map[string]any{"apps": list, "categories": manifest.Categories}
}

type verbInfo struct {
	Name    string          `json:"name"`
	App     string          `json:"app"`
	Effect  manifest.Effect `json:"effect"`
	Accepts []string        `json:"accepts"`
	Summary string          `json:"summary"`
	// Default and Group are the manifest's, passed on unchanged. Group is
	// presentation only; Effect is the authority signal.
	Default bool   `json:"default,omitempty"`
	Group   string `json:"group,omitempty"`
}

func (s *Service) listVerbs(accepts string) any {
	list := []verbInfo{}
	for _, a := range s.Reg.Apps() {
		verbs := a.Manifest.Verbs
		if accepts != "" {
			verbs = a.Manifest.VerbsAccepting(accepts)
		}
		for _, v := range verbs {
			acc := v.Accepts
			if acc == nil {
				acc = []string{}
			}
			list = append(list, verbInfo{v.Name, a.ID(), v.Effect, acc, v.Summary, v.Default, v.Group})
		}
	}
	return map[string]any{"verbs": list}
}

// plumb serves registry.plumb@v1 {"selection":{"kind","value"},"limit"}: what
// could be done with a selection. It lists verbs and runs none of them.
// req.From, stamped by busd, decides how much of the filesystem the answer
// may look at (see plumb.ViewerKind); nothing in the payload can widen that.
func (s *Service) plumb(q plumb.Request, from string) plumb.Reply {
	rules := s.Rules
	if rules == nil {
		rules = plumb.Default()
	}
	var verbs []plumb.Verb
	for _, a := range s.Reg.Apps() {
		for _, v := range a.Manifest.Verbs {
			verbs = append(verbs, plumb.Verb{App: a.ID(), Name: v.Name, Summary: v.Summary,
				Effect: string(v.Effect), Group: v.Group, Accepts: v.Accepts, Default: v.Default})
		}
	}
	return plumb.ForViewer(rules, verbs, q, from)
}

// surfaceCaller is the one rule for who may read or act on an app's surface: the app's own domain,
// or the session (which is the person). Both registry.describe@v1 and ui.act@v1 call it, so a caller
// cannot find the looser of the two paths.
func (s *Service) surfaceCaller(from, app string) *invoke.Error {
	if _, isDomain := ctlsurface.DomainOf(from); isDomain {
		if e := ctlsurface.Permit(from, app, func(name, appID string) bool { return name == invoke.DomainName(appID) }); e != nil {
			return &invoke.Error{Code: e.Code, Msg: e.Message, Detail: e.Detail}
		}
		return nil
	}
	if !mayDecide(from) {
		return &invoke.Error{Code: "not_permitted",
			Msg:    "a surface is readable from the session socket or its own domain",
			Detail: fmt.Sprintf("caller %q is neither the session user nor the domain of %s", from, app)}
	}
	return nil
}

func (s *Service) describe(appID, verb string) (any, *invoke.Error) {
	a, ok := s.Reg.App(appID)
	if !ok {
		return nil, &invoke.Error{Code: "no_such_app", Msg: "no such app: " + appID}
	}
	v, ok := a.Verb(verb)
	if !ok {
		return nil, &invoke.Error{Code: "no_such_verb", Msg: "app " + appID + " has no verb " + verb}
	}
	nz := func(m map[string]string) map[string]string {
		if m == nil {
			return map[string]string{}
		}
		return m
	}
	acc, caps := v.Accepts, v.Capabilities
	if acc == nil {
		acc = []string{}
	}
	if caps == nil {
		caps = []string{}
	}
	// The surface contract's descriptor travels alongside the per-verb fields,
	// under its own key, so a reader that knows only the old shape keeps
	// working: this verb stays @v1 and old clients decode into a struct that
	// simply has no field for "surface". Marshal (not json.Marshal) is what
	// makes the bytes canonical, and it validates first: a descriptor the shell
	// could not draw or the controller could not police is refused here, as a
	// structured error, rather than sent on for the shell to choke on.
	sb, err := windowSurface(appID, a.Manifest.App.Name, a.Manifest.Verbs).Marshal()
	if err != nil {
		return nil, &invoke.Error{Code: "bad_surface", Msg: "app " + appID + " has no drawable surface", Detail: err.Error()}
	}
	return map[string]any{
		"app": appID, "verb": v.Name, "effect": v.Effect, "summary": v.Summary,
		"accepts": acc, "input": nz(v.Input), "output": nz(v.Output), "capabilities": caps,
		"surface": json.RawMessage(sb),
	}, nil
}

// verify serves registry.verify@v1 {"record": <provenance record>}: it asks
// the daemon about the record's handle ids. Same caller rule as approvals,
// because the check probes with bus.revoke@v1 and that is the controller's
// own power to use.
func (s *Service) verify(req proto.Envelope) (any, *invoke.Error) {
	if !mayDecide(req.From) {
		return nil, &invoke.Error{Code: "not_permitted", Msg: "registry.verify@v1 may only be called from the session socket"}
	}
	var q struct {
		Record prov.Record `json:"record"`
	}
	if e := decode(req, &q); e != nil {
		return nil, e
	}
	rep, err := s.Inv.VerifyCaps(q.Record)
	if err != nil {
		return nil, &invoke.Error{Code: "caps_unavailable", Msg: err.Error()}
	}
	return rep, nil
}
