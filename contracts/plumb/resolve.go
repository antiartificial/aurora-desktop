package plumb

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Limits on the candidate list.
const (
	DefaultLimit = 20
	MaxLimit     = 50
)

// Verb is what the registry knows about one verb. The caller builds the list
// from registry state; this package never reads it itself.
type Verb struct {
	App     string
	Name    string
	Summary string
	Effect  string // read | write | destructive | external; the only authority signal
	Group   string // advisory, presentation only
	Accepts []string
	Default bool
}

// Request is the registry.plumb@v1 payload.
type Request struct {
	Selection Selection `json:"selection"`
	Limit     int       `json:"limit,omitempty"` // 0 = DefaultLimit, capped at MaxLimit
}

// Resolved says how the selection was understood.
type Resolved struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Rule   string `json:"rule,omitempty"`   // name of the rule that matched
	Object string `json:"object,omitempty"` // the extracted path (path rules)
	Line   int    `json:"line,omitempty"`
	Col    int    `json:"col,omitempty"`
	MIME   string `json:"mime,omitempty"` // the type verbs were looked up by
}

// Candidate is one verb the selection could be given to. Effect is always
// present and is copied from the manifest unchanged; a consumer decides what
// to show from it, and a destructive or external candidate says so here.
type Candidate struct {
	App     string `json:"app"`
	Verb    string `json:"verb"`
	Summary string `json:"summary"`
	Effect  string `json:"effect"`
	Group   string `json:"group,omitempty"` // advisory, presentation only
	Default bool   `json:"default"`         // the app's default verb for this type
}

// Reply is the registry.plumb@v1 answer. It is never an error for a
// selection that matches nothing: Candidates is then empty and Reason says why.
type Reply struct {
	Selection  Resolved    `json:"selection"`
	Candidates []Candidate `json:"candidates"`
	Truncated  bool        `json:"truncated"`
	Reason     string      `json:"reason,omitempty"`
	// Viewer is how the caller was treated: "session", "domain" or "unknown".
	// Set by the service, not by Resolve.
	Viewer string `json:"viewer,omitempty"`
}

// Probe turns an existing path into a MIME type. It is the only place
// resolution can touch the filesystem, and a nil Probe means it must not:
// the type then comes from the name alone.
type Probe func(path string) (mime string, err error)

// Resolve answers for one selection. It never runs a verb and never returns
// an error: whatever goes wrong becomes Reason on an empty list.
func Resolve(rs *RuleSet, verbs []Verb, req Request, probe Probe) Reply {
	rep := Reply{
		Selection:  Resolved{Kind: req.Selection.Kind, Value: req.Selection.Value},
		Candidates: []Candidate{},
	}
	if len(rep.Selection.Value) > MaxValue {
		rep.Selection.Value = rep.Selection.Value[:64] + "..."
	}
	switch req.Selection.Kind {
	case KindPath, KindMIME, KindText:
	default:
		rep.Reason = fmt.Sprintf("unknown selection kind %q; use path, mime or text", req.Selection.Kind)
		return rep
	}
	m, ok := rs.Match(req.Selection)
	if !ok {
		rep.Reason = fmt.Sprintf("no plumb rule matches this %s selection", req.Selection.Kind)
		return rep
	}
	rep.Selection.Rule, rep.Selection.Line, rep.Selection.Col = m.Rule, m.Line, m.Col

	var pool []Verb
	switch m.Resolve {
	case ResolvePath:
		if !strings.HasPrefix(m.Object, "/") {
			rep.Reason = "the selection is not an absolute path"
			return rep
		}
		p := path.Clean(m.Object)
		rep.Selection.Object = p
		var err error
		rep.Selection.MIME, err = pathMIME(p, probe)
		if err != nil {
			rep.Reason = err.Error()
			return rep
		}
	case ResolveMIME:
		rep.Selection.MIME = m.MIME
		if m.MIME == "" {
			rep.Selection.MIME = strings.ToLower(m.Object)
		}
	case ResolveVerb:
		app, name, _ := strings.Cut(m.Verb, ":")
		for _, v := range verbs {
			if v.App == app && v.Name == name {
				pool = []Verb{v}
			}
		}
		if pool == nil {
			rep.Reason = fmt.Sprintf("rule %s names verb %s, which no installed app has", m.Rule, m.Verb)
			return rep
		}
	}
	if m.Resolve != ResolveVerb {
		for _, v := range verbs {
			if specificity(v.Accepts, rep.Selection.MIME) >= 0 {
				pool = append(pool, v)
			}
		}
		if len(pool) == 0 {
			rep.Reason = fmt.Sprintf("no installed app has a verb that accepts %s", rep.Selection.MIME)
			return rep
		}
	}
	rep.Candidates, rep.Truncated = rank(pool, rep.Selection.MIME, req.Limit)
	return rep
}

func pathMIME(p string, probe Probe) (string, error) {
	if probe == nil {
		if mt := ExtMIME(p); mt != "" {
			return mt, nil
		}
		return "", fmt.Errorf("the type of this path cannot be told from its name")
	}
	mt, err := probe(p)
	if err != nil {
		return "", err
	}
	return mt, nil
}

// specificity is how closely accepts covers mime: 0 exact, 1 "type/*", 2
// "*/*", -1 not at all. A verb with no accepts takes nothing.
func specificity(accepts []string, mime string) int {
	best := -1
	for _, a := range accepts {
		s := -1
		switch {
		case a == mime:
			s = 0
		case a == "*/*":
			s = 2
		case strings.HasSuffix(a, "/*") && strings.HasPrefix(mime, strings.TrimSuffix(a, "*")):
			s = 1
		}
		if s >= 0 && (best < 0 || s < best) {
			best = s
		}
	}
	return best
}

// effectRank orders the existing effect classes from least to most
// authority. It decides nothing but display order; an unknown class sorts
// last and is reported as it came.
func effectRank(e string) int {
	switch e {
	case "read":
		return 0
	case "write":
		return 1
	case "destructive":
		return 2
	case "external":
		return 3
	}
	return 4
}

// rank orders, dedupes and bounds the pool. The order is, first to last:
//
//  1. a verb its manifest marks default, when it accepts the type exactly or
//     by "type/*" (a "*/*" default is not the default FOR this type);
//  2. the more specific accepts pattern: exact, then "type/*", then "*/*";
//  3. the lower effect class: read, write, destructive, external, so the
//     least authority is offered first among equals;
//  4. app id, then verb name, so the order is total and repeatable.
func rank(pool []Verb, mime string, limit int) ([]Candidate, bool) {
	type scored struct {
		c            Candidate
		def, spec, e int
	}
	seen := map[[2]string]bool{}
	var all []scored
	for _, v := range pool {
		k := [2]string{v.App, v.Name}
		if seen[k] {
			continue
		}
		seen[k] = true
		spec := specificity(v.Accepts, mime)
		if spec < 0 { // a direct-verb rule: the rule, not accepts, chose it
			spec = 3
		}
		def := v.Default && spec < 2
		eff := v.Effect
		if eff == "" {
			eff = "unknown"
		}
		d := 1
		if def {
			d = 0
		}
		all = append(all, scored{Candidate{v.App, v.Name, v.Summary, eff, v.Group, def}, d, spec, effectRank(v.Effect)})
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		switch {
		case a.def != b.def:
			return a.def < b.def
		case a.spec != b.spec:
			return a.spec < b.spec
		case a.e != b.e:
			return a.e < b.e
		case a.c.App != b.c.App:
			return a.c.App < b.c.App
		}
		return a.c.Verb < b.c.Verb
	})
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	trunc := len(all) > limit
	if trunc {
		all = all[:limit]
	}
	out := make([]Candidate, len(all))
	for i, s := range all {
		out[i] = s.c
	}
	return out, trunc
}
