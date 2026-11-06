package invoke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// Bus topics of the approval flow.
const (
	TopicRequested = "approval.requested"
	TopicDecided   = "approval.decided"
	// DefaultApprovalTimeout is how long an undecided request waits before the
	// answer is deny.
	DefaultApprovalTimeout = 60 * time.Second
)

// Decision values of approval.decide@v1.
const (
	Allow = "allow"
	Deny  = "deny"
)

// ApprovalRequest is the approval.requested payload: everything a prompt
// needs to show what is about to happen, exactly as the controller will run it.
type ApprovalRequest struct {
	RequestID      string          `json:"request_id"`
	App            string          `json:"app"`
	Domain         string          `json:"domain,omitempty"` // bus identity of the app's domain, if the adapter reported one
	DomainName     string          `json:"domain_name"`
	DomainAttested bool            `json:"domain_attested"`
	Verb           string          `json:"verb"`
	Effect         manifest.Effect `json:"effect"`
	Grants         []prov.Grant    `json:"grants"`
	Args           json.RawMessage `json:"args"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	ExpiresAt      string          `json:"expires_at"`
}

type verdict struct{ decision, by string }

// Approver holds the requests awaiting a decision. The zero value is usable.
type Approver struct {
	// Timeout is the wait before an undecided request is denied (default 60s).
	Timeout time.Duration
	// AutoApprove allows every request immediately. A test and scripting
	// affordance that defeats the point of approval: it is logged loudly.
	AutoApprove bool

	mu      sync.Mutex
	pending map[string]chan verdict
}

// ErrNoSuchRequest is returned by Decide for an id that is unknown, already
// decided, or expired.
var ErrNoSuchRequest = errors.New("no such pending approval request (unknown, already decided, or timed out)")

// Decide delivers a decision. by is the bus identity of the decider; the
// caller (the service) is responsible for having checked it may decide.
func (a *Approver) Decide(id, decision, by string) error {
	if decision != Allow && decision != Deny {
		return fmt.Errorf("decision must be %q or %q, got %q", Allow, Deny, decision)
	}
	a.mu.Lock()
	ch, ok := a.pending[id]
	delete(a.pending, id)
	a.mu.Unlock()
	if !ok {
		return ErrNoSuchRequest
	}
	ch <- verdict{decision, by} // buffered 1, and only one deleter wins
	return nil
}

func newID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (a *Approver) open() (string, chan verdict) {
	id := newID()
	ch := make(chan verdict, 1)
	a.mu.Lock()
	if a.pending == nil {
		a.pending = map[string]chan verdict{}
	}
	a.pending[id] = ch
	a.mu.Unlock()
	return id, ch
}

func (a *Approver) forget(id string) {
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
}

func (a *Approver) timeout() time.Duration {
	if a.Timeout > 0 {
		return a.Timeout
	}
	return DefaultApprovalTimeout
}

// needsApproval: an external or destructive verb that needs any grant not
// covered by the manifest's declared capabilities (gesture or powerbox).
func needsApproval(effect manifest.Effect, grants []prov.Grant) bool {
	if effect != manifest.EffectExternal && effect != manifest.EffectDestructive {
		return false
	}
	for _, g := range grants {
		if g.Source != prov.SourceAmbient {
			return true
		}
	}
	return false
}

// approve gates one invoke. It returns nil, nil for verbs that are not
// external/destructive. Otherwise it records the route: ambient when no
// approval was needed, else it blocks until allow, deny, timeout, or ctx end.
// Nothing has been granted, created or spent when it returns an error.
func (iv *Invoker) approve(ctx context.Context, app *apps.App, verb manifest.Verb, name string, argsJSON json.RawMessage, grants []prov.Grant) (*prov.Approval, *Error) {
	if verb.Effect != manifest.EffectExternal && verb.Effect != manifest.EffectDestructive {
		return nil, nil
	}
	now := func() string { return time.Now().UTC().Format(time.RFC3339Nano) }
	if !needsApproval(verb.Effect, grants) {
		return &prov.Approval{Required: false, Route: prov.RouteAmbient, At: now()}, nil
	}
	ap := iv.Approvals
	if ap == nil {
		return nil, fail("approval_unavailable", "%s is %s and needs approval, but this controller has no approval channel", "refusing to run unapproved; nothing was run", name, verb.Effect)
	}
	dname := DomainName(app.ID())
	req := ApprovalRequest{App: app.ID(), DomainName: dname, Verb: name, Effect: verb.Effect, Grants: grants, Args: argsJSON}
	if id, _, err := iv.Runner.Identity(dname); err == nil {
		req.Domain = id
		iv.regMu.Lock()
		req.DomainAttested = iv.registered[dname] == id
		iv.regMu.Unlock()
	}
	if ap.AutoApprove {
		id := newID()
		iv.logf("WARNING: AUTO-APPROVED (--auto-approve): %s verb %s of %s on %v, args %s; no human was asked", verb.Effect, name, app.ID(), grants, argsJSON)
		return &prov.Approval{Required: true, Route: prov.RouteAutoApprove, Decision: Allow, RequestID: id, By: "controllerd", At: now()}, nil
	}
	id, ch := ap.open()
	defer ap.forget(id)
	wait := ap.timeout()
	req.RequestID, req.TimeoutSeconds = id, int(wait.Round(time.Second)/time.Second)
	req.ExpiresAt = time.Now().Add(wait).UTC().Format(time.RFC3339)
	b, _ := json.Marshal(req)
	if iv.Publish == nil {
		return nil, fail("approval_unavailable", "%s needs approval but the controller has no bus to ask on", "nothing was run", name)
	}
	if err := iv.Publish(TopicRequested, b); err != nil {
		return nil, fail("approval_unavailable", "%s needs approval but the request could not be published", err.Error(), name)
	}
	iv.logf("approval requested %s: %s verb %s of %s on %v; waiting up to %s", id, verb.Effect, name, app.ID(), grants, wait)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	var v verdict
	var reason string
	select {
	case v = <-ch:
	case <-timer.C:
		v.decision, reason = Deny, "timeout"
	case <-ctx.Done():
		v.decision, reason = Deny, "canceled"
	}
	iv.publishDecided(id, v, reason)
	switch {
	case reason == "timeout":
		return nil, fail("approval_timeout", "%s was not approved: no decision within %s, so the answer is deny", "request "+id+"; nothing was run", name, wait)
	case reason == "canceled":
		return nil, fail("approval_denied", "%s was not approved: the invoke was canceled while waiting", "request "+id+"; nothing was run", name)
	case v.decision != Allow:
		return nil, fail("approval_denied", "%s was denied by %s", "request "+id+"; nothing was run", name, v.by)
	}
	iv.logf("approval %s: allowed by %s", id, v.by)
	return &prov.Approval{Required: true, Route: prov.RouteInteractive, Decision: Allow, RequestID: id, By: v.by, At: now()}, nil
}

func (iv *Invoker) publishDecided(id string, v verdict, reason string) {
	if iv.Publish == nil {
		return
	}
	b, _ := json.Marshal(map[string]string{"request_id": id, "decision": v.decision, "by": v.by, "reason": reason})
	if err := iv.Publish(TopicDecided, b); err != nil {
		iv.logf("WARNING: could not publish %s: %v", TopicDecided, err)
	}
}
