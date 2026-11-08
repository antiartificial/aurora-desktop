// Package launcher is the logic behind the aurora CLI. Everything that talks
// to the bus sits behind Backend so tests can inject a fake.
package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Effect classes. Destructive and external are the ones that need approval
// when they run on a grant their manifest did not declare: the controller
// blocks them until someone answers (see approvals.go).
const (
	EffectRead        = "read"
	EffectWrite       = "write"
	EffectDestructive = "destructive"
	EffectExternal    = "external"
)

// App is one entry of registry.apps@v1.
type App struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Verbs       []string `json:"verbs"`
	Windowed    bool     `json:"windowed,omitempty"`
	Exec        string   `json:"exec,omitempty"`
	ExecProblem string   `json:"exec_problem,omitempty"`
	// Category is presentation only; empty means no section.
	Category string `json:"category,omitempty"`
}

// Verb is one entry of registry.verbs@v1.
type Verb struct {
	Name    string   `json:"name"`
	App     string   `json:"app"`
	Effect  string   `json:"effect"`
	Accepts []string `json:"accepts"`
	Summary string   `json:"summary"`
	// Default and Group come from the manifest. Group is presentation only.
	Default bool   `json:"default,omitempty"`
	Group   string `json:"group,omitempty"`
}

// NeedsApproval reports whether the effect class is one the plan gates.
func (v Verb) NeedsApproval() bool {
	return v.Effect == EffectDestructive || v.Effect == EffectExternal
}

// Description is the reply of registry.describe@v1. The protocol only says
// "input/output schema and effect class", so Input is read tolerantly: a map
// of argument name to either a type string ("int", "int?") or an object
// {"type","default","description"}. See ParseSchema.
type Description struct {
	App     string                     `json:"app"`
	Verb    string                     `json:"verb"`
	Effect  string                     `json:"effect"`
	Summary string                     `json:"summary"`
	Input   map[string]json.RawMessage `json:"input"`
	Output  map[string]json.RawMessage `json:"output"`
}

// InvokeRequest is the request of registry.invoke@v1.
type InvokeRequest struct {
	App    string         `json:"app"`
	Verb   string         `json:"verb"`
	Input  string         `json:"input"`
	Args   map[string]any `json:"args,omitempty"`
	Output string         `json:"output,omitempty"`
	// Tickets are powerbox grant tickets for the input and output, carried
	// opaquely from the picker to the controller.
	Ticket       string `json:"ticket,omitempty"`
	OutputTicket string `json:"output_ticket,omitempty"`
	// MayBlock is set for verbs that can wait on an approval; the backend
	// then allows the call to outlast its usual timeout. Not sent.
	MayBlock bool `json:"-"`
}

// InvokeResult is the reply of registry.invoke@v1.
type InvokeResult struct {
	OK         bool            `json:"ok"`
	Output     string          `json:"output"`
	Result     json.RawMessage `json:"result,omitempty"` // read verbs: data instead of a file
	Isolated   *bool           `json:"isolated,omitempty"`
	Provenance json.RawMessage `json:"provenance,omitempty"`
	Grants     json.RawMessage `json:"grants,omitempty"`
	Approval   json.RawMessage `json:"approval,omitempty"`
	Error      string          `json:"error,omitempty"`
	Detail     string          `json:"detail,omitempty"`
}

// ProvEvent is a provenance.created payload as the launcher reads it.
type ProvEvent struct {
	Time   string
	App    string
	Verb   string
	Output string
	Inputs []string
}

// Backend is everything the launcher needs from the system.
type Backend interface {
	Apps(ctx context.Context) ([]App, error)
	Verbs(ctx context.Context, accepts string) ([]Verb, error)
	Describe(ctx context.Context, app, verb string) (Description, error)
	Invoke(ctx context.Context, r InvokeRequest) (InvokeResult, error)
	// Watch calls fn for each provenance.created event until ctx ends.
	Watch(ctx context.Context, fn func(ProvEvent)) error
	Close() error
}

// Sentinel errors a Backend returns so the CLI can say what to start.
var (
	ErrBusDown        = errors.New("bus is not running")
	ErrRegistryAbsent = errors.New("registry service is not running")
)

// InvokeError is a registry-reported failure (ok:false).
type InvokeError struct{ Verb, Msg, Detail string }

func (e *InvokeError) Error() string {
	s := fmt.Sprintf("%s failed: %s", e.Verb, e.Msg)
	if e.Detail != "" {
		s += " (" + e.Detail + ")"
	}
	return s
}

// hint turns a backend error into an operator-facing message.
func hint(err error) string {
	switch {
	case errors.Is(err, ErrBusDown):
		return err.Error() + ". Start it first: run `busd`, then `controllerd`."
	case errors.Is(err, ErrRegistryAbsent):
		return err.Error() + ". The bus is up but nothing serves `registry`: start `controllerd`."
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out waiting for the bus (is `controllerd` hung? raise --timeout)"
	}
	return err.Error()
}
