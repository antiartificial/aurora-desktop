// Package gateway is the Phase 10 agent gateway, MVP slice. It generates MCP
// tool definitions from the verbs the registry serves and runs a tool call by
// sending registry.invoke@v1 to the controller. Isolation, grants, approval,
// capability handles and provenance are all the controller's; the gateway adds
// a policy filter, an effect-class check, a call table and audit events. It
// does not run anything itself and has no parallel execution path.
package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/antiartificial/aurora/shell/launcher"
)

// Audit topics. Both are published non-ephemeral, so they are in the bus log.
const (
	TopicCall   = "agent.tool.call"
	TopicResult = "agent.tool.result"
)

// Call statuses.
const (
	StatusOK              = "ok"
	StatusAwaiting        = "awaiting_approval"
	StatusRunning         = "running"
	StatusDenied          = "denied"
	StatusApprovalTimeout = "approval_timeout"
	StatusRefused         = "refused"
	StatusError           = "error"
	DefaultApprovalWait   = 10 * time.Second
	maxRecordedCalls      = 1000
)

// Gateway serves one agent policy.
type Gateway struct {
	Ctl    Controller
	Aud    Auditor
	Policy *Policy
	// ApprovalWait is how long a destructive or external call waits for the
	// controller to raise its approval before reporting "running".
	ApprovalWait time.Duration
	// Home expands "~/" in declared capability paths (default: user's home).
	Home string

	session string
	mu      sync.Mutex
	calls   map[string]*callState
	order   []string
	watch   bool
	cache   []Tool
	cacheAt time.Time
}

// toolCacheTTL bounds how stale a call's view of the catalogue may be. A call
// resolves its tool from the list built for the last tools/list or call, so a
// burst of calls does not put a verbs and describe round trip per call into
// the bus log. The policy filter is applied when the list is built.
const toolCacheTTL = 5 * time.Second

func (g *Gateway) cachedTools(ctx context.Context) ([]Tool, error) {
	g.mu.Lock()
	if g.cache != nil && time.Since(g.cacheAt) < toolCacheTTL {
		t := g.cache
		g.mu.Unlock()
		return t, nil
	}
	g.mu.Unlock()
	return g.Tools(ctx)
}

type callState struct {
	id, agent, tool, app, verb, effect, args string
	approval                                 chan launcher.ApprovalRequest
	done                                     chan struct{}
	final                                    *CallResult
	req                                      *launcher.ApprovalRequest
}

// CallResult is what a tool call returns. Strings that came from the agent or
// from an app have been cleaned.
type CallResult struct {
	Status  string `json:"status"`
	CallID  string `json:"call_id,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Effect  string `json:"effect,omitempty"`
	Message string `json:"message,omitempty"`
	Output  string `json:"output,omitempty"`
	Code    string `json:"code,omitempty"`
	// Warning is set when the effect ran without the interactive approval the
	// gateway expected for its class.
	Warning string `json:"warning,omitempty"`
	// Approval holds what the controller reported about the approval; every
	// field is the controller's, none is the agent's.
	Approval   json.RawMessage `json:"approval,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Isolated   *bool           `json:"isolated,omitempty"`
	Grants     json.RawMessage `json:"grants,omitempty"`
	Provenance json.RawMessage `json:"provenance,omitempty"`
}

// IsError is the MCP isError flag: set for every outcome that is not a
// success or a pending approval.
func (r CallResult) IsError() bool {
	switch r.Status {
	case StatusOK, StatusAwaiting, StatusRunning:
		return false
	}
	return true
}

func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// New builds a gateway. Call Start before the first Call.
func New(c Controller, a Auditor, p *Policy) *Gateway {
	return &Gateway{Ctl: c, Aud: a, Policy: p, ApprovalWait: DefaultApprovalWait, session: newID(), calls: map[string]*callState{}}
}

// Start subscribes to approval.requested so a gated call can tell the agent
// it is waiting. Without it gated calls are refused (fail closed).
func (g *Gateway) Start(ctx context.Context) error {
	ch, err := g.Ctl.WatchApprovals(ctx)
	if err != nil {
		return fmt.Errorf("cannot watch approval requests: %w", err)
	}
	g.mu.Lock()
	g.watch = true
	g.mu.Unlock()
	go func() {
		for r := range ch {
			g.offer(r)
		}
		g.mu.Lock()
		g.watch = false
		g.mu.Unlock()
	}()
	return nil
}

// offer hands an approval request to the oldest unmatched call with the same
// app, verb and arguments. The match is by those three only: the controller's
// request carries no caller id, so two simultaneous identical gated calls
// cannot be told apart, and a request someone else raised for the same app,
// verb and arguments would match too.
func (g *Gateway) offer(r launcher.ApprovalRequest) {
	g.mu.Lock()
	defer g.mu.Unlock()
	want := canonArgs(r.Args)
	for _, id := range g.order {
		cs := g.calls[id]
		if cs.final != nil || cs.req != nil || cs.app != r.App || cs.verb != r.Verb || cs.args != want {
			continue
		}
		rr := r
		cs.req = &rr
		select {
		case cs.approval <- r:
		default:
		}
		return
	}
}

func canonArgs(raw json.RawMessage) string {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil || v == nil {
		return "{}"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (g *Gateway) record(cs *callState) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls[cs.id] = cs
	g.order = append(g.order, cs.id)
	if len(g.order) > maxRecordedCalls {
		delete(g.calls, g.order[0])
		g.order = g.order[1:]
	}
}

func (g *Gateway) audit(topic string, v map[string]any) error {
	v["session"] = g.session
	b, _ := json.Marshal(v)
	return g.Aud.Publish(topic, b)
}

func refuse(tool, format string, a ...any) CallResult {
	return CallResult{Status: StatusRefused, Tool: clean(tool), Message: clean(fmt.Sprintf(format, a...))}
}

func gated(effect string) bool {
	return effect != launcher.EffectRead && effect != launcher.EffectWrite
}

func (g *Gateway) home() string {
	if g.Home != "" {
		return g.Home
	}
	h, _ := os.UserHomeDir()
	return h
}

// declaredCover reports whether the controller would classify path as
// ambient because of one of the verb's declared path capabilities. The test is
// deliberately broader than the controller's (it ignores read/write access),
// so it can only err toward refusing.
func (g *Gateway) declaredCover(t Tool, path string) bool {
	for _, c := range t.pathCaps {
		if rest, ok := strings.CutPrefix(c, "~/"); ok {
			c = filepath.Join(g.home(), rest)
		}
		c = filepath.Clean(c)
		if r, err := filepath.EvalSymlinks(c); err == nil {
			c = r
		}
		if within(c, path) {
			return true
		}
	}
	return false
}

// Call runs one tool call for the agent named by agent (the name the MCP
// client gave in initialize: a claim, not an authenticated identity).
func (g *Gateway) Call(ctx context.Context, agent, name string, raw json.RawMessage) CallResult {
	if name == ResultTool {
		return g.lookup(raw)
	}
	tools, err := g.cachedTools(ctx)
	if err != nil {
		return CallResult{Status: StatusError, Tool: clean(name), Message: clean("cannot read the verb catalogue: " + err.Error())}
	}
	var tool *Tool
	for i := range tools {
		if tools[i].Name == name {
			tool = &tools[i]
		}
	}
	args := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			g.audit(TopicCall, map[string]any{"call_id": "", "agent": g.agentField(agent), "tool": name, "refused": "arguments are not a JSON object"})
			return refuse(name, "arguments must be a JSON object")
		}
	}
	id := newID()
	if tool == nil {
		// Not offered to this policy: the same answer whether the verb does not
		// exist or exists and is not allowed, so the call is not a probe of the catalogue.
		g.audit(TopicCall, map[string]any{"call_id": id, "agent": g.agentField(agent), "tool": name, "refused": "no such tool for this policy", "args": args})
		return refuse(name, "no such tool: %s", name)
	}
	in, _ := args["input"].(string)
	out, _ := args["output"].(string)
	delete(args, "input")
	delete(args, "output")
	var inPath, outPath string
	if tool.takesInput || in != "" {
		if in == "" {
			return g.refused(id, agent, tool, args, in, out, "input is required")
		}
		if inPath, err = g.Policy.Confine(in, true); err != nil {
			return g.refused(id, agent, tool, args, in, out, "input: "+err.Error())
		}
	}
	if tool.effect == launcher.EffectRead {
		if out != "" {
			return g.refused(id, agent, tool, args, in, out, "a read tool takes no output")
		}
	} else {
		if out == "" {
			return g.refused(id, agent, tool, args, in, out, "output is required")
		}
		if outPath, err = g.Policy.Confine(out, false); err != nil {
			return g.refused(id, agent, tool, args, in, out, "output: "+err.Error())
		}
	}
	if gated(tool.effect) {
		g.mu.Lock()
		watching := g.watch
		g.mu.Unlock()
		if !watching {
			return g.refused(id, agent, tool, args, in, out, "cannot watch approval requests, so this call could not be reported as awaiting approval; refusing rather than running it blind")
		}
		// The controller asks only when some grant is not declared by the app's
		// manifest. If every path this call names is declared (or it names none),
		// the controller would run it with no prompt. Refuse that here.
		nonAmbient := false
		for _, p := range []string{inPath, outPath} {
			if p != "" && !g.declaredCover(*tool, p) {
				nonAmbient = true
			}
		}
		if !nonAmbient {
			return g.refused(id, agent, tool, args, in, out, "this "+tool.effect+" call would not reach an approval: the controller treats a path the app's manifest declares as consented at install time, and a call with no path has no grant to ask about. The gateway refuses it rather than run it unapproved")
		}
	}
	argsCanon, _ := json.Marshal(args)
	cs := &callState{id: id, agent: agent, tool: tool.Name, app: tool.app, verb: tool.verb, effect: tool.effect,
		args: canonArgs(argsCanon), approval: make(chan launcher.ApprovalRequest, 1), done: make(chan struct{})}
	// Audit first and fail closed: a call that cannot be recorded is not made.
	if err := g.audit(TopicCall, map[string]any{
		"call_id": id, "agent": g.agentField(agent), "tool": tool.Name, "app": tool.app, "verb": tool.verb, "effect": tool.effect,
		"input": inPath, "output": outPath, "args": args, "gated": gated(tool.effect),
	}); err != nil {
		return CallResult{Status: StatusError, CallID: id, Tool: clean(tool.Name), Message: "audit event could not be published, so the call was not made: " + clean(err.Error())}
	}
	g.record(cs)
	req := launcher.InvokeRequest{App: tool.app, Verb: tool.verb, Input: inPath, Output: outPath, Args: args, MayBlock: gated(tool.effect)}
	if !gated(tool.effect) {
		g.run(ctx, cs, req)
		return *g.finalOf(cs)
	}
	// The invoke outlives this MCP request: it blocks in the controller until
	// a human decides, and the agent is told so instead of being held.
	go g.run(context.Background(), cs, req)
	wait := time.NewTimer(g.ApprovalWait)
	defer wait.Stop()
	select {
	case <-cs.done:
		return *g.finalOf(cs)
	case ar := <-cs.approval:
		return awaitingResult(cs, ar)
	case <-wait.C:
		return CallResult{Status: StatusRunning, CallID: id, Tool: clean(tool.Name), Effect: tool.effect,
			Message: "the controller has not raised an approval yet; poll " + ResultTool + " with this call_id"}
	case <-ctx.Done():
		return CallResult{Status: StatusRunning, CallID: id, Tool: clean(tool.Name), Effect: tool.effect, Message: "request canceled; the call continues; poll " + ResultTool}
	}
}

func awaitingResult(cs *callState, ar launcher.ApprovalRequest) CallResult {
	ap, _ := json.Marshal(map[string]any{
		"request_id": clean(ar.RequestID), "timeout_seconds": ar.TimeoutSeconds,
		// Reported by the controller in its approval.requested event. Not the agent's.
		"controller_reported_domain": clean(ar.Domain), "domain_attested": ar.DomainAttested, "domain_source": clean(ar.DomainSource),
	})
	return CallResult{Status: StatusAwaiting, CallID: cs.id, Tool: clean(cs.tool), Effect: cs.effect, Approval: ap,
		Message: fmt.Sprintf("awaiting approval: a person must allow this %s call on their own approval prompt. This tool cannot approve it. The controller denies it if nobody answers within %ds. Poll %s with call_id %s.", cs.effect, ar.TimeoutSeconds, ResultTool, cs.id)}
}

func (g *Gateway) refused(id, agent string, t *Tool, args map[string]any, in, out, why string) CallResult {
	g.audit(TopicCall, map[string]any{"call_id": id, "agent": g.agentField(agent), "tool": t.Name, "app": t.app, "verb": t.verb, "effect": t.effect,
		"input": in, "output": out, "args": args, "refused": why})
	r := refuse(t.Name, "%s", why)
	r.CallID, r.Effect = id, t.effect
	return r
}

func (g *Gateway) agentField(claimed string) map[string]any {
	return map[string]any{"claimed_by_client": claimed, "policy_label": g.Policy.Label, "authenticated": false}
}

func (g *Gateway) finalOf(cs *callState) *CallResult {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cs.final
}

// run sends registry.invoke@v1 and records the outcome. It is the whole of
// execution: everything else is the controller.
func (g *Gateway) run(ctx context.Context, cs *callState, req launcher.InvokeRequest) {
	res, err := g.Ctl.Invoke(ctx, req)
	r := CallResult{CallID: cs.id, Tool: clean(cs.tool), Effect: cs.effect}
	switch {
	case err != nil:
		var ie *launcher.InvokeError
		msg := err.Error()
		r.Status = StatusError
		if errors.As(err, &ie) {
			msg = ie.Msg
			if ie.Detail != "" {
				msg += " (" + ie.Detail + ")"
			}
			switch {
			case strings.HasPrefix(ie.Msg, "approval_denied"):
				r.Status = StatusDenied
			case strings.HasPrefix(ie.Msg, "approval_timeout"):
				r.Status = StatusApprovalTimeout
			}
			r.Code, _, _ = strings.Cut(ie.Msg, ":")
		}
		r.Message = clean(msg)
	case !res.OK:
		r.Status, r.Message, r.Code = StatusError, clean(res.Error+" "+res.Detail), "not_ok"
	default:
		r.Status, r.Output = StatusOK, clean(res.Output)
		r.Result, r.Grants, r.Provenance, r.Approval = cleanJSON(res.Result), cleanJSON(res.Grants), cleanJSON(res.Provenance), cleanJSON(res.Approval)
		r.Isolated = res.Isolated
		if gated(cs.effect) && !approvedInteractively(res.Approval) {
			r.Warning = "this " + cs.effect + " call ran without an interactive approval on record; the controller treated it as consented. It is already done."
		}
	}
	g.mu.Lock()
	cs.final = &r
	g.mu.Unlock()
	close(cs.done)
	g.audit(TopicResult, map[string]any{"call_id": cs.id, "agent": g.agentField(cs.agent), "tool": cs.tool, "status": r.Status, "code": r.Code,
		"output": r.Output, "approval": json.RawMessage(orNull(r.Approval)), "warning": r.Warning})
}

func orNull(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}

func approvedInteractively(raw json.RawMessage) bool {
	var a struct {
		Required bool   `json:"required"`
		Route    string `json:"route"`
		Decision string `json:"decision"`
	}
	return json.Unmarshal(raw, &a) == nil && a.Required && a.Decision == "allow" && a.Route != "auto-approve" && a.Route != "ambient"
}

// lookup implements aurora_result.
func (g *Gateway) lookup(raw json.RawMessage) CallResult {
	var a struct {
		CallID string `json:"call_id"`
	}
	if json.Unmarshal(raw, &a) != nil || a.CallID == "" {
		return refuse(ResultTool, "call_id is required")
	}
	g.mu.Lock()
	cs := g.calls[a.CallID]
	g.mu.Unlock()
	if cs == nil {
		return refuse(ResultTool, "no call %s in this session", a.CallID)
	}
	if f := g.finalOf(cs); f != nil {
		return *f
	}
	g.mu.Lock()
	req := cs.req
	g.mu.Unlock()
	if req != nil {
		return awaitingResult(cs, *req)
	}
	return CallResult{Status: StatusRunning, CallID: cs.id, Tool: clean(cs.tool), Effect: cs.effect, Message: "still running"}
}
