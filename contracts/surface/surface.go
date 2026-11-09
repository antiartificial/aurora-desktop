// Package surface is the presentation contract: what a window offers, what it is allowed to do, and
// what it did. It is the wire form of docs/03-ergonomics.md's SurfaceDescriptor, and it is a
// CONTRACT rather than a convenience type because three sides must agree on it: the toolkit emits
// it, the controller relays it, and the shell draws the chrome from it.
//
// The rule the shape encodes: grants are DECLARED on the surface and never inferred from use, so a
// descriptor is a claim about access and a receipt is the evidence of what happened.
package surface

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// Effects an action can have. "nominal" means it changes nothing a person would have to be told
// about; the other two are the ones that need a grant.
const (
	EffectRead    = "read"
	EffectWrite   = "write"
	EffectNominal = "nominal"
)

// Action is one thing a surface offers.
type Action struct {
	ID     string   `json:"id"`               // the verb: "open", "rename"
	Label  string   `json:"label"`            // what the header shows
	Effect string   `json:"effect"`           // read | write | nominal
	Grants []string `json:"grants,omitempty"` // capabilities this action requires
}

// Receipt is the evidence that an action ran.
type Receipt struct {
	Action   string `json:"action"`
	Artifact string `json:"artifact,omitempty"` // domain-qualified id, when one was made
	Effect   string `json:"effect"`
	Detail   string `json:"detail,omitempty"`
}

// Descriptor is one window's surface.
type Descriptor struct {
	App     string   `json:"app"`
	Title   string   `json:"title"`
	Actions []Action `json:"actions"`
	Grants  []string `json:"grants,omitempty"`
	// ShellChrome says the shell draws this surface's chrome, so the app should not draw its own.
	// It belongs on the surface rather than in a process flag: it is a property of what is being
	// presented, and the shell is the side that has to honour it.
	ShellChrome bool `json:"shell_chrome,omitempty"`
}

// Validate rejects a descriptor the shell cannot draw or the controller cannot police. It is
// deliberately strict about the effect: an unknown effect is an unpoliced action.
func (d Descriptor) Validate() error {
	if d.App == "" {
		return errors.New("surface: app is empty")
	}
	seen := make(map[string]bool, len(d.Actions))
	for i, a := range d.Actions {
		if a.ID == "" {
			return fmt.Errorf("surface: action %d has no id", i)
		}
		if seen[a.ID] {
			return fmt.Errorf("surface: duplicate action id %q", a.ID)
		}
		seen[a.ID] = true
		switch a.Effect {
		case EffectRead, EffectWrite, EffectNominal:
		default:
			return fmt.Errorf("surface: action %q has effect %q, which is not read/write/nominal", a.ID, a.Effect)
		}
	}
	return nil
}

// Marshal is the canonical encoding: actions sorted by id so the same tree produces the same bytes.
// A surface that reorders between runs makes a golden diff useless.
func (d Descriptor) Marshal() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	cp := d
	// A surface with no actions must encode as [] rather than null: Rust's Vec<Action> rejects null,
	// so a nil slice would make the same surface readable by two of the three sides and not the third.
	cp.Actions = append([]Action{}, d.Actions...)
	for i := range cp.Actions {
		cp.Actions[i].Grants = append([]string(nil), cp.Actions[i].Grants...)
		sort.Strings(cp.Actions[i].Grants)
	}
	sort.Slice(cp.Actions, func(i, j int) bool { return cp.Actions[i].ID < cp.Actions[j].ID })
	cp.Grants = append([]string(nil), d.Grants...)
	sort.Strings(cp.Grants)
	return json.Marshal(cp)
}

// Unmarshal parses and validates in one step, so a caller cannot forget the second half.
func Unmarshal(b []byte) (Descriptor, error) {
	var d Descriptor
	if err := json.Unmarshal(b, &d); err != nil {
		return Descriptor{}, err
	}
	return d, d.Validate()
}
