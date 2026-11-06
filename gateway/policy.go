package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Policy is what one agent session is allowed. It is an operator-written file,
// not something the agent can edit or the controller knows about.
//
//	{"label": "photo-agent",
//	 "apps": ["org.aurora.magick"],
//	 "effects": ["read", "write", "destructive"],
//	 "roots": ["/home/me/Pictures/agent"]}
//
// A verb is offered as a tool only if its app is in Apps and its effect class
// is in Effects. A call may only name files under Roots.
//
// ENFORCEMENT: this is enforced by the gateway process, for callers that go
// through it: a tool outside the policy is neither listed nor callable, and a
// path outside Roots is refused before anything is sent to the controller.
// It is NOT a security boundary against anything else. The controller accepts
// registry.invoke@v1 from every process of the same uid, and the gateway is a
// same-uid process like any other, so a caller that bypasses the gateway is
// not constrained by this file at all. "An agent sees only the verbs it is
// allowed" holds for a client that speaks only MCP to this gateway; it is
// advisory for everything else.
type Policy struct {
	// Label names the grant in audit events. It is the operator's text, not an
	// authenticated identity.
	Label   string   `json:"label"`
	Apps    []string `json:"apps"`
	Effects []string `json:"effects"`
	Roots   []string `json:"roots"`
}

var allEffects = []string{"read", "write", "destructive", "external"}

// LoadPolicy reads and validates a policy file.
func LoadPolicy(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Policy
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("policy %s: %w", path, err)
	}
	return &p, p.Validate()
}

// Validate normalises the policy and rejects one that would allow nothing
// sensible or everything by accident.
func (p *Policy) Validate() error {
	if len(p.Apps) == 0 {
		return fmt.Errorf("policy: apps is empty; list the app ids this agent may use (there is no allow-all)")
	}
	if len(p.Roots) == 0 {
		return fmt.Errorf("policy: roots is empty; list the directories this agent may name (there is no allow-all)")
	}
	if len(p.Effects) == 0 {
		p.Effects = slices.Clone(allEffects)
	}
	for _, e := range p.Effects {
		if !slices.Contains(allEffects, e) {
			return fmt.Errorf("policy: unknown effect class %q", e)
		}
	}
	for i, r := range p.Roots {
		if !filepath.IsAbs(r) {
			return fmt.Errorf("policy: root %q is not absolute", r)
		}
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			r = rr
		}
		p.Roots[i] = filepath.Clean(r)
	}
	if p.Label == "" {
		p.Label = "unlabelled"
	}
	return nil
}

// Allows says whether a verb is offered to this agent.
func (p *Policy) Allows(app, effect string) bool {
	return slices.Contains(p.Apps, app) && slices.Contains(p.Effects, effect)
}

// Confine resolves path (symlinks included) and returns it only if it lies
// under a root. An input must exist. An output need not, but its directory
// must, and is the part that is resolved.
func (p *Policy) Confine(path string, mustExist bool) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute")
	}
	path = filepath.Clean(path)
	var resolved string
	if mustExist {
		r, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("cannot resolve path: %v", err)
		}
		resolved = r
	} else {
		dir, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return "", fmt.Errorf("output directory does not resolve: %v", err)
		}
		resolved = filepath.Join(dir, filepath.Base(path))
	}
	for _, r := range p.Roots {
		if within(r, resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("path is outside every directory this agent may use")
}

func within(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}
