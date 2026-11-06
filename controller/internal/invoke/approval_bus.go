package invoke

import (
	"context"
	"strings"
	"time"

	"github.com/antiartificial/aurora/controller/internal/prov"
)

// busDomain is what the bus-route prompt shows about the domain and where that
// came from: "bus" when it was read from an approval.preflight event busd
// stamped from inside the domain, else "claim" (our word, unverified).
type busDomain struct {
	Source   string
	Detail   string
	Mismatch bool
}

// attestForBus runs the pre-flight for the bus route, under the request id the
// bus prompt will use, and rewrites req to say what the responder should show.
// registered: busd has the domain registered (so the helper's event will be
// stamped). It never fails the approval: no event means a claim, said so.
func (iv *Invoker) attestForBus(ctx context.Context, ap *Approver, dname string, req *ApprovalRequest, registered bool) busDomain {
	claim := func(why string) busDomain {
		iv.logf("WARNING: approval %s on the bus route: %s; the responder is shown the domain as an unverified CLAIM", req.RequestID, why)
		req.DomainSource, req.DomainAttested = "claim", false
		return busDomain{Source: "claim", Detail: "not attested: " + why}
	}
	log, _ := iv.Bus.(EventLog)
	switch {
	case ap.Preflight == nil:
		return claim("no pre-flight helper is configured")
	case req.Domain == "" || !registered:
		return claim("the domain has no attested bus identity")
	case log == nil:
		return claim("the bus client cannot read the bus log")
	}
	if _, err := ap.Preflight.run(ctx, iv.Runner, dname, *req); err != nil {
		return claim("pre-flight in " + dname + " failed: " + err.Error())
	}
	id, err := attestedDomain(log, *req, time.Now())
	if err != nil {
		return claim(err.Error())
	}
	b := busDomain{Source: "bus", Detail: "published from inside the domain over its own bus socket, stamped by busd"}
	if id != req.Domain {
		b.Mismatch, req.DomainClaim = true, req.Domain
		b.Detail += "; the controller's own claim was " + req.Domain
		iv.logf("WARNING: approval %s: the bus attests %s but this controller expected %s; the responder is shown the bus's", req.RequestID, id, req.Domain)
	}
	req.Domain, req.DomainSource, req.DomainAttested, req.DomainMismatch = id, "bus", true, b.Mismatch
	if parts := strings.Split(id, ":"); len(parts) == 3 {
		req.DomainName = parts[1]
	}
	return b
}

func interactiveApproval(id string, v verdict, req ApprovalRequest, b busDomain, at string) *prov.Approval {
	verified := "unknown"
	if b.Source == "bus" {
		verified = "true"
	}
	return &prov.Approval{Required: true, Route: prov.RouteInteractive, Decision: Allow, RequestID: id, By: v.by, At: at,
		DomainVerified: verified, DomainVerifyDetail: b.Detail, DomainSource: b.Source, DomainShown: req.Domain, DomainMismatch: b.Mismatch}
}
