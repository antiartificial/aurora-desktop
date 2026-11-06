// Package launcher is the logic behind the aurora CLI. Everything that talks
// to the bus sits behind Backend so tests can inject a fake.
package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Effect classes. Destructive and external are the ones the plan says will
// need approval (Phase 5/10); the launcher only marks them for now.
const (
	EffectRead        = "read"
	EffectWrite       = "write"
	EffectDestructive = "destructive"
	EffectExternal    = "external"
)

// App is one entry of registry.apps@v1.
type App struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Verbs   []string `json:"verbs"`
}

// Verb is one entry of registry.verbs@v1.
type Verb struct {
	Name    string   `json:"name"`
	App     string   `json:"app"`
	Effect  string   `json:"effect"`
	Accepts []string `json:"accepts"`
	Summary string   `json:"summary"`
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
}

// InvokeResult is the reply of registry.invoke@v1.
type InvokeResult struct {
	OK         bool            `json:"ok"`
	Output     string          `json:"output"`
	Result     json.RawMessage `json:"result,omitempty"` // read verbs: data instead of a file
	Isolated   *bool           `json:"isolated,omitempty"`
	Provenance json.RawMessage `json:"provenance,omitempty"`
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
