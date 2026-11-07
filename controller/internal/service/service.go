// Package service serves the registry.* verbs on the bus.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/antiartificial/aurora/controller/internal/sysaudio"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/contracts/plumb"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/prov"
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
	case "system.audio@v1":
		// Reading the volume is harmless and anything may ask.
		out = sysaudio.Get(s.audio)
	case "system.audio.set@v1":
		// SETTING it is not. It changes the machine for every application at once, so it is
		// refused inside a domain by the same rule that refuses registry.launch@v1: an app the
		// user gave a file to has not been given the speakers.
		if !mayDecide(req.From) {
			return nil, &invoke.Error{Code: "not_permitted", Msg: "system.audio.set@v1 may only be called from the session socket", Detail: fmt.Sprintf("caller %q may not change the machine's volume", req.From)}
		}
		var q struct {
			Volume int `json:"volume"`
		}
		if e = decode(req, &q); e == nil {
			if err := sysaudio.Set(s.audio, q.Volume); err != nil {
				return nil, &invoke.Error{Code: "unavailable", Msg: "the volume could not be set", Detail: err.Error()}
			}
			out = sysaudio.Get(s.audio)
		}
	case "registry.describe@v1":
		var q struct{ App, Verb string }
		if e = decode(req, &q); e == nil {
			out, e = s.describe(q.App, q.Verb)
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
}

func (s *Service) listApps() any {
	list := []appInfo{}
	for _, a := range s.Reg.Apps() {
		m := a.Manifest
		info := appInfo{ID: m.App.ID, Name: m.App.Name, Version: m.App.Version, Verbs: []string{}}
		info.Windowed = m.App.Windowed
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
	return map[string]any{"apps": list}
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
	return map[string]any{
		"app": appID, "verb": v.Name, "effect": v.Effect, "summary": v.Summary,
		"accepts": acc, "input": nz(v.Input), "output": nz(v.Output), "capabilities": caps,
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
