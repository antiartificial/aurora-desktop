// Package service serves the registry.* verbs on the bus.
package service

import (
	"context"
	"encoding/json"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/invoke"
)

// Service answers registry requests.
type Service struct {
	Reg *apps.Registry
	Inv *invoke.Invoker
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
	var out any
	var e *invoke.Error
	switch req.Verb {
	case "registry.apps@v1":
		out = s.listApps()
	case "registry.verbs@v1":
		var q struct {
			Accepts string `json:"accepts"`
		}
		if e = decode(req, &q); e == nil {
			out = s.listVerbs(q.Accepts)
		}
	case "registry.describe@v1":
		var q struct{ App, Verb string }
		if e = decode(req, &q); e == nil {
			out, e = s.describe(q.App, q.Verb)
		}
	case "registry.invoke@v1":
		var q invoke.Request
		if e = decode(req, &q); e == nil {
			out, e = invokeResult(s.Inv.Invoke(context.Background(), q))
		}
	default:
		e = &invoke.Error{Code: "no_such_verb", Msg: "registry has no verb " + req.Verb}
	}
	if e != nil {
		fail(c, req, e)
		return
	}
	b, _ := json.Marshal(out)
	c.Reply(req, b)
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
}

func (s *Service) listApps() any {
	list := []appInfo{}
	for _, a := range s.Reg.Apps() {
		m := a.Manifest
		info := appInfo{ID: m.App.ID, Name: m.App.Name, Version: m.App.Version, Verbs: []string{}}
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
			list = append(list, verbInfo{v.Name, a.ID(), v.Effect, acc, v.Summary})
		}
	}
	return map[string]any{"verbs": list}
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
