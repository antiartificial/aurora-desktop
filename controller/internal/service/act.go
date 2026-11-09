package service

import (
	"context"
	"encoding/json"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/contracts/surface"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/invoke"
)

// ActRequest is the ui.act@v1 payload: one action of one app's surface, and
// whatever the action needs to run.
//
// App names the surface, and with it the window: a launched window's domain
// name is minted per launch and the launch table is the only thing that maps
// one back to an app, so a request that named only a window could not be
// resolved here without inventing a second ownership table.
//
// Surface is the descriptor the window published (the toolkit's SurfaceJson).
// It is optional: without it the controller derives the descriptor from the
// app's manifest, which is what registry.describe@v1 already relays. WITH it,
// the descriptor is an app's CLAIM about its own window, and the whole point
// of ui.act is to police that claim rather than believe it -- see act.
type ActRequest struct {
	App    string                     `json:"app"`
	Action string                     `json:"action"`
	Input  string                     `json:"input,omitempty"`
	Output string                     `json:"output,omitempty"`
	Args   map[string]json.RawMessage `json:"args,omitempty"`
	// Tickets are passed through to the invoke path unchanged. ui.act adds a
	// gate; it must not become a way to skip one.
	Ticket       string          `json:"ticket,omitempty"`
	OutputTicket string          `json:"output_ticket,omitempty"`
	Surface      json.RawMessage `json:"surface,omitempty"`
}

// actReply is the success reply: the receipt is the answer, because a receipt
// is the evidence that an action ran. The invoke's own result travels beside
// it under "result" for a caller that needs the app's output, but a caller
// that only wants to know what happened reads the receipt.
type actReply struct {
	OK      bool            `json:"ok"`
	Receipt surface.Receipt `json:"receipt"`
	Result  any             `json:"result,omitempty"`
}

// act serves ui.act@v1 {app, action, ...}.
//
// # WHY A VERB OF ITS OWN, NOT A MODE OF registry.invoke@v1
//
// registry.invoke@v1 names an app and a VERB and is the path every other
// caller uses to run one. ui.act names a WINDOW's surface and an ACTION, and
// the action may be labelled and classed by the toolkit rather than by the
// manifest. That difference is the security-relevant one: the request can
// carry a descriptor the app published, which the controller must not trust.
// Folding that into invoke@v1 would put a second payload shape and a second
// gate on a verb existing clients decode, and would let a caller choose which
// gate applies by choosing a field. A separate verb keeps invoke@v1's
// contract intact and gives the surface gate one door.
//
// # THE GATE
//
// An action runs only if every grant it declares -- and every capability the
// verb behind it declares -- is HELD by the app. Held means declared at app
// level in the manifest: that is the consent the user gave at install, and it
// is what the launch and invoke paths actually compose (controller/internal/
// invoke's launchGrants and planGrants derive from the same section). Network
// hosts are the exception and are deliberately not held: nothing in this tree
// grants a verb a host (decision 0015), so a surface that declares a net grant
// is asking for something the app does not have.
//
// The effect in the receipt is the MANIFEST's, not the descriptor's. A surface
// that calls a write "nominal" is refused rather than run: the shell draws
// danger styling from that field and a receipt that repeated the claim would
// launder it.
//
// # THE CALLER
//
// A domain may act only on its own app's surface (surface.Permit, the same
// rule that governs registry.describe@v1), and a non-domain caller must be the
// session user. The surface's grants are the user's consent, so acting on
// another app's window -- or from a domain that cannot be shown to own the one
// it names -- is refused rather than inferred from the payload.
//
// A launched window's domain name is minted per launch, so "does this domain
// belong to this app" cannot be recomputed for a window; such a caller is
// refused (fail closed) until the launch table exposes ownership. Verbs, whose
// domains are named from the app id, are covered.
func (s *Service) act(req proto.Envelope) (any, *invoke.Error) {
	var q ActRequest
	if e := decode(req, &q); e != nil {
		return nil, e
	}
	if q.App == "" || q.Action == "" {
		return nil, &invoke.Error{Code: "bad_request", Msg: "ui.act@v1 needs {\"app\": ..., \"action\": ...}",
			Detail: "the app names whose surface, the action names what to run; neither is guessed"}
	}
	app, ok := s.Reg.App(q.App)
	if !ok {
		return nil, &invoke.Error{Code: "no_such_app", Msg: "no such app: " + q.App}
	}
	if e := s.surfaceCaller(req.From, q.App); e != nil {
		return nil, e
	}
	d, e := surfaceFor(app, q.Surface)
	if e != nil {
		return nil, e
	}
	action, ok := actionByID(d, q.Action)
	if !ok {
		return nil, &invoke.Error{Code: "no_such_action", Msg: "app " + q.App + " offers no action " + q.Action,
			Detail: "the surface lists the actions it offers; an action it does not list is not run"}
	}
	// The invoke path runs verbs, so the action must be one. A surface action
	// with no verb behind it has no existing path to run on, and inventing one
	// here would be a second execution policy.
	v, ok := app.Verb(action.ID)
	if !ok {
		return nil, &invoke.Error{Code: "no_such_action",
			Msg:    "action " + q.Action + " of app " + q.App + " names no verb, so there is nothing to run",
			Detail: "ui.act runs an action through registry.invoke's path; an action with no manifest verb has no such path"}
	}
	// The manifest's effect is the authority signal (decision 0012). Refusing
	// a disagreement keeps the shell's chrome and the receipt honest: neither
	// may describe a write as anything else.
	if want := surfaceEffect(v.Effect); action.Effect != want {
		return nil, violation(q.App, action.ID, action.Effect, want)
	}
	// Both lists are checked: the descriptor's (a claim) and the verb's (the
	// authority). Checking only the descriptor would let a surface under-declare
	// to dodge the gate; checking only the verb would accept a surface that
	// promises more than it will get.
	want := append(append([]string(nil), action.Grants...), v.Capabilities...)
	held := heldCapabilities(app)
	for _, g := range dedupe(want) {
		if !held[g] {
			return nil, &invoke.Error{Code: "grant_not_held",
				Msg:    "action " + action.ID + " needs grant " + g + ", which app " + q.App + " does not hold",
				Detail: "grant not declared in the app's manifest capabilities: the surface asked for access the user never consented to, so nothing ran"}
		}
	}
	res, e := s.Inv.Invoke(context.Background(), invoke.Request{
		App: q.App, Verb: action.ID, Input: q.Input, Output: q.Output, Args: q.Args,
		Ticket: q.Ticket, OutputTicket: q.OutputTicket,
	})
	if e != nil {
		return nil, e
	}
	return actReply{OK: true, Receipt: receiptFor(action, v.Effect, res), Result: res}, nil
}

// surfaceFor loads the descriptor ui.act will police: the one the request
// carries when it carries one, otherwise the manifest-derived one that
// registry.describe@v1 relays. A carried descriptor is validated exactly as
// the contract validates it and must name the app the request names, so a
// window cannot answer for another app.
func surfaceFor(app *apps.App, raw json.RawMessage) (surface.Descriptor, *invoke.Error) {
	if len(raw) == 0 {
		return windowSurface(app.ID(), app.Manifest.App.Name, app.Manifest.Verbs), nil
	}
	d, err := surface.Unmarshal(raw)
	if err != nil {
		return surface.Descriptor{}, &invoke.Error{Code: "bad_surface", Msg: "app " + app.ID() + " sent no drawable surface", Detail: err.Error()}
	}
	if d.App != app.ID() {
		return surface.Descriptor{}, &invoke.Error{Code: "bad_surface",
			Msg:    "the surface describes " + d.App + ", but the request names " + app.ID(),
			Detail: "a window may describe its own surface and no other app's"}
	}
	return d, nil
}

// actionByID finds one action in a descriptor.
func actionByID(d surface.Descriptor, id string) (surface.Action, bool) {
	for _, a := range d.Actions {
		if a.ID == id {
			return a, true
		}
	}
	return surface.Action{}, false
}

// heldCapabilities is the set of capability refs the app holds, in the
// descriptor's own vocabulary ("path:<p>", "device:<d>", "ui:<class>"). It is
// the app-level [capabilities] section, which is what the manifest validator
// already holds every verb capability to (validate.go's capDeclared) and what
// the launch and invoke paths compose. Network hosts are left out on purpose:
// nothing here grants a verb a host, so listing one would say an app holds
// something the controller never composed.
func heldCapabilities(app *apps.App) map[string]bool {
	c := app.Manifest.Capabilities
	held := make(map[string]bool, len(c.Paths)+len(c.Devices)+len(c.UI))
	for _, p := range c.Paths {
		held["path:"+p.Path] = true
	}
	for _, d := range c.Devices {
		held["device:"+d] = true
	}
	for _, u := range c.UI {
		held["ui:"+u] = true
	}
	return held
}

// receiptFor is the evidence an action ran: the manifest's effect, the file
// the invoke made when it made one, and the action's id as the receipt names
// it. A window's domain travels in the invoke's result, not in the receipt:
// the receipt is about the action, and the provenance record is where the
// domain is attested.
func receiptFor(a surface.Action, effect manifest.Effect, res *invoke.Result) surface.Receipt {
	r := surface.Receipt{Action: a.ID, Effect: surfaceEffect(effect)}
	if res != nil {
		r.Artifact = res.Output
		if r.Artifact == "" && res.Provenance != nil {
			r.Artifact = res.Provenance.File
		}
	}
	return r
}

// violation builds the refusal for a surface whose effect disagrees with the
// manifest's.
func violation(app, action, claimed, want string) *invoke.Error {
	return &invoke.Error{Code: "bad_surface",
		Msg:    "action " + action + " of app " + app + " is declared " + claimed + ", but its verb is " + want,
		Detail: "the manifest's effect is the authority signal; a surface may not relabel what it does"}
}

// dedupe drops repeats so a refusal names one grant once. Order is kept: the
// descriptor's grants are read first, then the verb's.
func dedupe(gs []string) []string {
	seen := make(map[string]bool, len(gs))
	out := gs[:0]
	for _, g := range gs {
		if !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}
