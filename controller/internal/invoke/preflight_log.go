package invoke

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EventLog reads busd's log. busreg.Client implements it; Invoker.Bus is
// asserted to it, so a Bus without it just means no attestation.
type EventLog interface {
	Log(topic, since string, limit int) (json.RawMessage, error)
}

const preflightFresh = 60 * time.Second

func grantArgs(req ApprovalRequest) []string {
	var out []string
	for _, g := range req.Grants {
		out = append(out, fmt.Sprintf("%s %s (%s)", g.Mode, g.Path, g.Source))
	}
	return out
}

// attestedDomain finds, in the bus log, the domain that published
// approval.preflight for req, the way the compositor does (attest.rs). An event
// counts only if: its `from` is a domain identity busd stamped (a session
// socket sender is not), the request id, app, verb, effect and grants are
// exactly those of req, it is fresh, and exactly one domain published for the
// id. Anything else is an error saying why, and the caller shows a claim.
//
// Not checked here, unlike the compositor: that busd still lists the domain
// with a cgroup (bus.inspect). The controller registered it moments earlier.
func attestedDomain(log EventLog, req ApprovalRequest, now time.Time) (string, error) {
	raw, err := log.Log("approval.preflight", "2m", 1000)
	if err != nil {
		return "", fmt.Errorf("bus log unreadable: %w", err)
	}
	var whole struct {
		Messages []struct {
			Kind    string          `json:"kind"`
			Topic   string          `json:"topic"`
			From    string          `json:"from"`
			TS      string          `json:"ts"`
			Payload json.RawMessage `json:"payload"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &whole); err != nil {
		return "", fmt.Errorf("bus log unreadable: %w", err)
	}
	want := grantArgs(req)
	var ids, ignored []string
	for _, m := range whole.Messages {
		var p struct {
			RequestID string   `json:"request_id"`
			App       string   `json:"app"`
			Verb      string   `json:"verb"`
			Effect    string   `json:"effect"`
			Grants    []string `json:"grants"`
		}
		if m.Kind != "event" || m.Topic != "approval.preflight" || json.Unmarshal(m.Payload, &p) != nil || p.RequestID != req.RequestID {
			continue
		}
		parts := strings.Split(m.From, ":")
		ns, nerr := strconv.ParseInt(m.TS[:min(19, len(m.TS))], 10, 64)
		age := now.Sub(time.Unix(0, ns))
		switch {
		case len(parts) != 3 || parts[0] != "domain" || parts[1] == "" || parts[2] == "":
			ignored = append(ignored, fmt.Sprintf("sender %q is not a domain identity", m.From))
		case nerr != nil || age > preflightFresh || age < -preflightFresh:
			ignored = append(ignored, "event is stale")
		case p.App != req.App || p.Verb != req.Verb || p.Effect != string(req.Effect) || !sameStrings(p.Grants, want):
			ignored = append(ignored, "event content differs from the approval request")
		default:
			dup := false
			for _, i := range ids {
				dup = dup || i == m.From
			}
			if !dup {
				ids = append(ids, m.From)
			}
		}
	}
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		if len(ignored) > 0 {
			return "", fmt.Errorf("no usable approval.preflight event (%s)", strings.Join(ignored, "; "))
		}
		return "", fmt.Errorf("no approval.preflight event for this request id in the bus log")
	}
	return "", fmt.Errorf("events for this request id came from several domains: %s", strings.Join(ids, ", "))
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
