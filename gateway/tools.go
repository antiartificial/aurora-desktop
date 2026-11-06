package gateway

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/antiartificial/aurora/shell/launcher"
)

// ResultTool is the one tool that is not a verb: it reports on an earlier call
// by its call id. It is how an agent learns the outcome of a call that stopped
// at an approval. It reads gateway state only and decides nothing.
const ResultTool = "aurora_result"

// Tool is an MCP tool definition generated from one manifest verb.
type Tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`

	app, verb, effect string
	accepts           []string
	takesInput        bool
	pathCaps          []string // "path:" capabilities the verb declares, for the approval pre-check
}

var badToolChar = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// toolName maps (app, verb) to an MCP tool name: the app id and the verb with
// '@' replaced by '_' ("org.aurora.magick.resize_v1"). MCP names are
// [A-Za-z0-9_.-], at most 128 characters. Names are looked up in a table, not
// parsed back, so a verb whose name sanitises to the same string as another
// is disambiguated rather than conflated.
func toolName(app, verb string) string {
	n := badToolChar.ReplaceAllString(app+"."+strings.ReplaceAll(verb, "@", "_"), "_")
	if len(n) > 128 {
		n = n[:128]
	}
	return n
}

// jsonType maps a manifest argument type to JSON Schema. MIME-typed and bytes
// fields have no scalar type, so they are left unconstrained (the controller
// does not check them either).
func jsonType(t string) map[string]any {
	switch t {
	case "int", "integer":
		return map[string]any{"type": "integer"}
	case "float", "number":
		return map[string]any{"type": "number"}
	case "bool", "boolean":
		return map[string]any{"type": "boolean"}
	case "string", "path":
		return map[string]any{"type": "string"}
	}
	return map[string]any{"description": "manifest type " + t}
}

// buildTool turns a verb and its description into a tool definition.
func buildTool(v launcher.Verb, d describeReply) Tool {
	props := map[string]any{}
	var required []string
	t := Tool{app: v.App, verb: v.Name, effect: v.Effect, accepts: v.Accepts}
	for _, c := range d.Capabilities {
		if strings.HasPrefix(c, "path:") {
			t.pathCaps = append(t.pathCaps, strings.TrimPrefix(c, "path:"))
		}
	}
	// A verb that declares accepted types takes an input file. The controller
	// treats a missing input as no grant; a verb without accepts may be
	// generative, so input is optional there.
	t.takesInput = len(v.Accepts) > 0
	if t.takesInput {
		props["input"] = map[string]any{
			"type":        "string",
			"description": "Absolute path of the input file. Accepted types: " + strings.Join(v.Accepts, ", ") + ".",
		}
		required = append(required, "input")
	}
	if v.Effect != launcher.EffectRead {
		props["output"] = map[string]any{
			"type":        "string",
			"description": "Absolute path of the file to create. Its directory must already exist.",
		}
		required = append(required, "output")
	}
	for _, s := range launcher.ParseSchema(d.Input) {
		if s.Name == "input" || s.Name == "output" {
			continue // would shadow the path fields; the manifest cannot rename them
		}
		p := jsonType(s.Type)
		if s.Doc != "" {
			p["description"] = s.Doc
		}
		props[s.Name] = p
		if !s.Optional {
			required = append(required, s.Name)
		}
	}
	sort.Strings(required)
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	t.InputSchema = schema
	t.Name = toolName(v.App, v.Name)
	t.Title = v.Name + " (" + v.App + ")"
	t.Description = fmt.Sprintf("%s [effect: %s]", v.Summary, v.Effect)
	switch v.Effect {
	case launcher.EffectRead:
		t.Description += ". Runs without asking."
	case launcher.EffectWrite:
		t.Description += ". Writes the output file. It is NOT staged in a snapshot: the file is written for real."
	default:
		t.Description += ". Stops at a user approval before anything runs; this tool cannot approve itself and the call may be denied."
	}
	// MCP annotations are hints a client may show; they enforce nothing.
	t.Annotations = map[string]any{
		"readOnlyHint":    v.Effect == launcher.EffectRead,
		"destructiveHint": v.Effect == launcher.EffectDestructive,
		"openWorldHint":   v.Effect == launcher.EffectExternal,
	}
	t.Meta = map[string]any{
		"aurora/app":    v.App,
		"aurora/verb":   v.Name,
		"aurora/effect": v.Effect,
		"aurora/accepts": func() []string {
			if v.Accepts == nil {
				return []string{}
			}
			return v.Accepts
		}(),
	}
	return t
}

func resultTool() Tool {
	return Tool{
		Name:        ResultTool,
		Title:       "Result of an earlier call",
		Description: "Reports the state of an earlier tool call by call_id: running, awaiting_approval, ok, denied, approval_timeout or error. It cannot approve anything.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"call_id": map[string]any{"type": "string"}},
			"required":             []string{"call_id"},
			"additionalProperties": false,
		},
		Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
		Meta:        map[string]any{"aurora/effect": "read", "aurora/builtin": true},
	}
}

// Tools generates the tool list for this policy, fresh from the registry on
// every call: one tool per allowed verb, plus aurora_result.
func (g *Gateway) Tools(ctx context.Context) ([]Tool, error) {
	verbs, err := g.Ctl.Verbs(ctx)
	if err != nil {
		return nil, err
	}
	var out []Tool
	seen := map[string]bool{}
	for _, v := range verbs {
		if !g.Policy.Allows(v.App, v.Effect) {
			continue
		}
		d, err := g.Ctl.Describe(ctx, v.App, v.Name)
		if err != nil {
			return nil, fmt.Errorf("describe %s %s: %w", v.App, v.Name, err)
		}
		t := buildTool(v, d)
		for seen[t.Name] { // distinct verbs that sanitise alike
			t.Name += "_"
		}
		seen[t.Name] = true
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	out = append(out, resultTool())
	g.mu.Lock()
	g.cache, g.cacheAt = out, time.Now()
	g.mu.Unlock()
	return out, nil
}
