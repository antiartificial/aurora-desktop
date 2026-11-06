// Package plumb answers "I have this selected: what can the system do with
// it?". It is Aurora's plumber: a small, ordered list of rules (rules.toml)
// maps a selection to a MIME type or a verb, and Resolve ranks the verbs the
// registry offers for it.
//
// Plumbing never runs anything. It lists what could happen; running a verb
// stays behind registry.invoke@v1, its effect class, approval and grants.
package plumb

import (
	_ "embed"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Selection kinds.
const (
	KindPath = "path" // an absolute path the caller says is selected
	KindMIME = "mime" // a MIME type, directly
	KindText = "text" // text that may look like a path, a URL, a host
)

// Resolve modes of a rule.
const (
	ResolvePath = "path"
	ResolveMIME = "mime"
	ResolveVerb = "verb"
)

// MaxValue bounds a selection value. A longer one matches no rule.
const MaxValue = 4096

//go:embed rules.toml
var defaultSrc []byte

// Rule is one entry of rules.toml.
type Rule struct {
	Name    string `toml:"name"`
	Kind    string `toml:"kind"`
	Match   string `toml:"match"`
	Resolve string `toml:"resolve"`
	Object  string `toml:"object,omitempty"` // group holding the path or MIME; "object" if empty
	MIME    string `toml:"mime,omitempty"`
	Verb    string `toml:"verb,omitempty"`
}

type file struct {
	Plumb struct {
		Version int `toml:"version"`
	} `toml:"plumb"`
	Rule []Rule `toml:"rule"`
}

type compiled struct {
	Rule
	re                *regexp.Regexp
	obj, line, col    int // group indexes, -1 if absent
	verbApp, verbName string
}

// RuleSet is a validated, ordered rule list. It is immutable and safe for
// concurrent use.
type RuleSet struct{ rules []compiled }

// Len is the number of rules.
func (rs *RuleSet) Len() int { return len(rs.rules) }

// Default returns the embedded rules.toml. It panics if the shipped file is
// invalid; a test keeps it valid.
func Default() *RuleSet {
	rs, err := Parse(defaultSrc)
	if err != nil {
		panic("plumb: embedded rules.toml: " + err.Error())
	}
	return rs
}

// Load reads and validates a rules file.
func Load(path string) (*RuleSet, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rs, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rs, nil
}

// Parse decodes and validates rules. Unknown keys, a bad regexp, a missing
// group, a duplicate name or a rule that resolves nowhere is an error: the
// whole file is rejected.
func Parse(src []byte) (*RuleSet, error) {
	var f file
	md, err := toml.Decode(string(src), &f)
	if err != nil {
		return nil, err
	}
	if un := md.Undecoded(); len(un) > 0 {
		return nil, fmt.Errorf("unknown key %s", un[0])
	}
	if f.Plumb.Version != 1 {
		return nil, fmt.Errorf("[plumb] version must be 1, got %d", f.Plumb.Version)
	}
	rs := &RuleSet{}
	seen := map[string]bool{}
	for i, r := range f.Rule {
		c, err := compile(r)
		if err != nil {
			return nil, fmt.Errorf("rule %d (%q): %w", i+1, r.Name, err)
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("rule %d: duplicate name %q", i+1, r.Name)
		}
		seen[r.Name] = true
		rs.rules = append(rs.rules, c)
	}
	return rs, nil
}

func compile(r Rule) (compiled, error) {
	c := compiled{Rule: r, obj: -1, line: -1, col: -1}
	if r.Name == "" {
		return c, fmt.Errorf("name is required")
	}
	switch r.Kind {
	case KindPath, KindMIME, KindText:
	default:
		return c, fmt.Errorf("kind must be path, mime or text, got %q", r.Kind)
	}
	if r.Match == "" {
		return c, fmt.Errorf("match is required")
	}
	re, err := regexp.Compile(`\A(?:` + r.Match + `)\z`)
	if err != nil {
		return c, fmt.Errorf("match: %w", err)
	}
	c.re = re
	for i, n := range re.SubexpNames() {
		switch n {
		case "line":
			c.line = i
		case "col":
			c.col = i
		}
	}
	group := r.Object
	if group == "" {
		group = "object"
	}
	c.obj = re.SubexpIndex(group)
	needObj := r.Resolve == ResolvePath || (r.Resolve == ResolveMIME && r.MIME == "")
	if needObj && c.obj < 0 {
		return c, fmt.Errorf("match has no group (?P<%s>...) to resolve from", group)
	}
	switch r.Resolve {
	case ResolvePath:
		if r.MIME != "" || r.Verb != "" {
			return c, fmt.Errorf("resolve=path takes neither mime nor verb")
		}
	case ResolveMIME:
		if r.Verb != "" {
			return c, fmt.Errorf("resolve=mime takes no verb")
		}
		if r.MIME != "" && !strings.Contains(r.MIME, "/") {
			return c, fmt.Errorf("mime %q is not type/subtype", r.MIME)
		}
		if r.MIME != "" && r.Object != "" {
			return c, fmt.Errorf("resolve=mime takes mime or object, not both")
		}
	case ResolveVerb:
		app, name, ok := strings.Cut(r.Verb, ":")
		if !ok || app == "" || name == "" {
			return c, fmt.Errorf("verb must be \"<app id>:<verb name>\"")
		}
		if r.MIME != "" {
			return c, fmt.Errorf("resolve=verb takes no mime")
		}
		c.verbApp, c.verbName = app, name
	default:
		return c, fmt.Errorf("resolve must be path, mime or verb, got %q", r.Resolve)
	}
	return c, nil
}

// Selection is what the caller has selected.
type Selection struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Match is what the first matching rule extracted. It is pure data.
type Match struct {
	Rule    string
	Resolve string
	Object  string // the extracted path, or MIME for resolve=mime from a group
	MIME    string // fixed MIME of a resolve=mime rule
	Verb    string // "<app>:<verb>" of a resolve=verb rule
	Line    int
	Col     int
}

// Match tries the rules in order and returns the first that matches. It is
// pure: no I/O, same answer every time. ok is false when no rule matches.
func (rs *RuleSet) Match(sel Selection) (m Match, ok bool) {
	if len(sel.Value) == 0 || len(sel.Value) > MaxValue || strings.ContainsRune(sel.Value, 0) {
		return m, false
	}
	for _, c := range rs.rules {
		if c.Kind != sel.Kind {
			continue
		}
		sm := c.re.FindStringSubmatch(sel.Value)
		if sm == nil {
			continue
		}
		m = Match{Rule: c.Name, Resolve: c.Resolve, MIME: c.MIME, Verb: c.Verb}
		if c.obj >= 0 {
			m.Object = sm[c.obj]
		}
		m.Line, m.Col = num(sm, c.line), num(sm, c.col)
		return m, true
	}
	return m, false
}

func num(sm []string, i int) int {
	if i < 0 || sm[i] == "" {
		return 0
	}
	n, err := strconv.Atoi(sm[i])
	if err != nil {
		return 0
	}
	return n
}
