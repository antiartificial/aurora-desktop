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
	"github.com/antiartificial/aurora/controller/ticket"
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
	RequestID  string `json:"request_id"`
	App        string `json:"app"`
	Domain     string `json:"domain,omitempty"` // bus identity of the app's domain, if the adapter reported one
	DomainName string `json:"domain_name"`
	// DomainAttested is true only when Domain was read from an
	// approval.preflight event busd stamped from inside the domain (bus route;
	// the compositor route does that check itself). Otherwise Domain is the
	// controller's unverified claim. DomainSource says the same in a word.
	DomainAttested bool   `json:"domain_attested"`
	DomainSource   string `json:"domain_source,omitempty"` // "bus" or "claim"
	// DomainMismatch: the bus attested a different domain than the one the
	// controller expected, which is DomainClaim. Domain is the bus's.
	DomainMismatch bool            `json:"domain_mismatch,omitempty"`
	DomainClaim    string          `json:"domain_claim,omitempty"`
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
	// Compositor, if set, is tried first: the compositor draws the prompt.
	// When it cannot be reached or refuses us, the bus route below is used.
	Compositor *CompositorPrompt
	// Preflight, if set, runs a helper inside the app's domain before the
	// prompt so the bus attests which domain asked. Both routes use it.
	Preflight *Preflight

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
	return iv.approveIn(ctx, app, DomainName(app.ID()), verb, name, argsJSON, grants)
}

// approveIn is approve for the domain dname, which is not always the app's verb domain: a window runs in
// its own (LaunchDomainName) and a prompt about it must name, and be pre-flighted from, that one.
func (iv *Invoker) approveIn(ctx context.Context, app *apps.App, dname string, verb manifest.Verb, name string, argsJSON json.RawMessage, grants []prov.Grant) (*prov.Approval, *Error) {
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
	req := ApprovalRequest{App: app.ID(), DomainName: dname, Verb: name, Effect: verb.Effect, Grants: grants, Args: argsJSON}
	if ap.Preflight != nil && !ap.AutoApprove && iv.Runner.Available() == nil {
		// The helper runs in the domain, so the domain must exist (and be
		// registered with busd, below) before the prompt rather than at execute.
		if err := iv.Runner.Ensure(dname); err != nil {
			iv.logf("WARNING: cannot create domain %s for the approval pre-flight: %v", dname, err)
		}
	}
	registered := false
	if id, _, err := iv.Runner.Identity(dname); err == nil {
		req.Domain = id
		// The domain exists, so make sure busd has it registered: the
		// compositor will ask busd about this identity. (A domain that does
		// not exist yet has no identity; the prompt then names none and shows
		// as unverified, which is true.)
		_, registered = iv.domainIdentity(dname)
		req.DomainAttested = registered
	}
	if ap.AutoApprove {
		id := newID()
		iv.logf("WARNING: AUTO-APPROVED (--auto-approve): %s verb %s of %s on %v, args %s; no human was asked", verb.Effect, name, app.ID(), grants, argsJSON)
		return &prov.Approval{Required: true, Route: prov.RouteAutoApprove, Decision: Allow, RequestID: id, By: "controllerd", At: now()}, nil
	}
	wait := ap.timeout()
	if ap.Compositor != nil {
		req.RequestID, req.TimeoutSeconds = newID(), int(wait.Round(time.Second)/time.Second)
		if ap.Preflight != nil && req.Domain != "" && req.DomainAttested {
			if took, err := ap.Preflight.run(ctx, iv.Runner, dname, req); err != nil {
				iv.logf("WARNING: approval pre-flight in %s failed (%v); the compositor will show the domain as an unverified claim", dname, err)
			} else {
				iv.logf("approval %s: pre-flight published from inside %s in %s", req.RequestID, dname, took.Round(time.Millisecond))
			}
		} else if ap.Preflight != nil {
			iv.logf("WARNING: approval pre-flight skipped (domain has no attested bus identity); the compositor will show the domain as an unverified claim")
		}
		iv.logf("approval requested %s on the compositor: %s verb %s of %s on %v; waiting up to %s", req.RequestID, verb.Effect, name, app.ID(), grants, wait)
		ans, err := ap.Compositor.Ask(ctx, req, wait)
		switch {
		case ctx.Err() != nil:
			iv.publishDecided(req.RequestID, verdict{Deny, "compositor"}, "canceled", prov.RouteCompositor)
			return nil, fail("approval_denied", "%s was not approved: the invoke was canceled while waiting", "request "+req.RequestID+"; nothing was run", name)
		case isPeerErr(err):
			iv.logf("WARNING: REFUSING THE COMPOSITOR ROUTE: %v. Nothing was sent to it. A same-uid impostor may be squatting the socket %s, or --compositor-uid is wrong; falling back to the bus route (approval.requested / approval.decide@v1)", err, ap.Compositor.Socket)
		case isSigErr(err):
			iv.logf("WARNING: REFUSING THE COMPOSITOR'S REPLY: %v. Whatever it said (allow included) was discarded and nothing was run on it. Something other than the compositor may be answering on %s, or the approval key differs (%s); falling back to the bus route (approval.requested / approval.decide@v1)", err, ap.Compositor.Socket, ticket.ApprovalKeyPath())
		case err != nil:
			iv.logf("compositor prompt unavailable (%v); falling back to the bus route (approval.requested / approval.decide@v1)", err)
		case ans.Decision != Allow:
			reason := ans.Reason
			iv.publishDecided(req.RequestID, verdict{Deny, "compositor"}, reason, prov.RouteCompositor)
			if reason == "timeout" {
				return nil, fail("approval_timeout", "%s was not approved: no decision within %s, so the answer is deny", "request "+req.RequestID+"; nothing was run", name, wait)
			}
			return nil, fail("approval_denied", "%s was denied by the compositor prompt (key %q)", "request "+req.RequestID+"; nothing was run", name, ans.Key)
		default:
			iv.publishDecided(req.RequestID, verdict{Allow, "compositor"}, "", prov.RouteCompositor)
			iv.logf("approval %s: allowed at the compositor prompt (key %q; domain_verified=%s source=%s shown=%s mismatch=%s: %s)", req.RequestID, ans.Key, ans.DomainVerified, ans.DomainSource, ans.DomainIdentity, ans.DomainMismatch, ans.DomainVerifyDetail)
			return compositorApproval(req.RequestID, ans, now()), nil
		}
	}
	id, ch := ap.open()
	defer ap.forget(id)
	req.RequestID, req.TimeoutSeconds = id, int(wait.Round(time.Second)/time.Second)
	bd := iv.attestForBus(ctx, ap, dname, &req, registered)
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
	iv.publishDecided(id, v, reason, prov.RouteInteractive)
	switch {
	case reason == "timeout":
		return nil, fail("approval_timeout", "%s was not approved: no decision within %s, so the answer is deny", "request "+id+"; nothing was run", name, wait)
	case reason == "canceled":
		return nil, fail("approval_denied", "%s was not approved: the invoke was canceled while waiting", "request "+id+"; nothing was run", name)
	case v.decision != Allow:
		return nil, fail("approval_denied", "%s was denied by %s", "request "+id+"; nothing was run", name, v.by)
	}
	iv.logf("approval %s: allowed by %s", id, v.by)
	return interactiveApproval(id, v, req, bd, now()), nil
}

func isPeerErr(err error) bool {
	var pe *PeerError
	return errors.As(err, &pe)
}

func isSigErr(err error) bool {
	var se *SignatureError
	return errors.As(err, &se)
}

func (iv *Invoker) publishDecided(id string, v verdict, reason, route string) {
	if iv.Publish == nil {
		return
	}
	b, _ := json.Marshal(map[string]string{"request_id": id, "decision": v.decision, "by": v.by, "reason": reason, "route": route})
	if err := iv.Publish(TopicDecided, b); err != nil {
		iv.logf("WARNING: could not publish %s: %v", TopicDecided, err)
	}
}
