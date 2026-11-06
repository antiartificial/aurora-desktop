package service

import (
	"fmt"
	"os"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"strings"
)

// mayDecide says whether a bus identity may answer approval requests: only
// the session socket's own user, "uid<our uid>:<process>". An app's domain
// connects as "domain:<name>:<hex>" and is refused, as is another uid. That
// is the whole rule, and its limit is stated plainly: any unconfined process
// of the same user is also "uid<N>:<comm>" and may decide (decision 0007).
func mayDecide(from string) bool {
	return strings.HasPrefix(from, fmt.Sprintf("uid%d:", os.Getuid()))
}

// decide serves approval.decide@v1 {request_id, decision: "allow"|"deny"}.
func (s *Service) decide(req proto.Envelope) (any, *invoke.Error) {
	if !mayDecide(req.From) {
		return nil, &invoke.Error{Code: "not_permitted", Msg: "approval.decide@v1 may only be called from the session socket", Detail: fmt.Sprintf("caller %q may not decide approvals; an app cannot approve its own request", req.From)}
	}
	if s.Inv.Approvals == nil {
		return nil, &invoke.Error{Code: "approval_unavailable", Msg: "this controller has no approval channel"}
	}
	var q struct {
		RequestID string `json:"request_id"`
		Decision  string `json:"decision"`
	}
	if e := decode(req, &q); e != nil {
		return nil, e
	}
	if err := s.Inv.Approvals.Decide(q.RequestID, q.Decision, req.From); err != nil {
		code := "bad_request"
		if err == invoke.ErrNoSuchRequest {
			code = "no_such_request"
		}
		return nil, &invoke.Error{Code: code, Msg: err.Error()}
	}
	return map[string]any{"ok": true, "request_id": q.RequestID, "decision": q.Decision, "by": req.From}, nil
}
