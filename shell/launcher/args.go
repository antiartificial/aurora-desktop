package launcher

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// ArgSpec is one declared argument of a verb.
type ArgSpec struct {
	Name     string
	Type     string // int, float, bool, string (anything else is treated as string)
	Optional bool   // declared with a trailing "?"
	Default  any
	Doc      string
}

// ParseSchema reads Description.Input. A value is either a type string such
// as "int" or "int?" (the manifest's notation), or an object with type,
// default, description and optional. Required arguments sort first, then by
// name, so prompts are in a stable order.
func ParseSchema(in map[string]json.RawMessage) []ArgSpec {
	var specs []ArgSpec
	for name, raw := range in {
		s := ArgSpec{Name: name, Type: "string"}
		var t string
		if json.Unmarshal(raw, &t) == nil {
			s.Type = t
		} else {
			var o struct {
				Type     string `json:"type"`
				Default  any    `json:"default"`
				Doc      string `json:"description"`
				Optional bool   `json:"optional"`
			}
			if json.Unmarshal(raw, &o) == nil {
				s.Type, s.Default, s.Doc, s.Optional = o.Type, o.Default, o.Doc, o.Optional
			}
		}
		if strings.HasSuffix(s.Type, "?") {
			s.Type, s.Optional = strings.TrimSuffix(s.Type, "?"), true
		}
		if s.Type == "" {
			s.Type = "string"
		}
		specs = append(specs, s)
	}
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Optional != specs[j].Optional {
			return !specs[i].Optional
		}
		return specs[i].Name < specs[j].Name
	})
	return specs
}

// Coerce converts text to the Go value for a declared type.
func Coerce(typ, text string) (any, error) {
	switch typ {
	case "int", "integer":
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", text)
		}
		return n, nil
	case "float", "number":
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", text)
		}
		return f, nil
	case "bool", "boolean":
		b, err := strconv.ParseBool(text)
		if err != nil {
			return nil, fmt.Errorf("%q is not true/false", text)
		}
		return b, nil
	}
	return text, nil
}

// BuildArgs resolves every declared argument. Order of precedence: value from
// --arg, then (if interactive) the user's answer, then the default. A
// required argument with none of these is an error. Supplied names the
// schema does not declare are rejected, so a typo does not silently vanish.
func BuildArgs(specs []ArgSpec, supplied map[string]string, interactive bool, in *bufio.Reader, out io.Writer) (map[string]any, error) {
	known := map[string]bool{}
	for _, s := range specs {
		known[s.Name] = true
	}
	for k := range supplied {
		if !known[k] {
			return nil, fmt.Errorf("unknown argument %q (verb takes: %s)", k, names(specs))
		}
	}
	args := map[string]any{}
	for _, s := range specs {
		if text, ok := supplied[s.Name]; ok {
			v, err := Coerce(s.Type, text)
			if err != nil {
				return nil, fmt.Errorf("argument %s: %w", s.Name, err)
			}
			args[s.Name] = v
			continue
		}
		if !interactive {
			if s.Default != nil {
				args[s.Name] = s.Default
			} else if !s.Optional {
				return nil, fmt.Errorf("missing required argument %q (%s); pass --arg %s=VALUE", s.Name, s.Type, s.Name)
			}
			continue
		}
		v, err := promptOne(s, in, out)
		if err != nil {
			return nil, err
		}
		if v != nil {
			args[s.Name] = v
		}
	}
	return args, nil
}

func names(specs []ArgSpec) string {
	if len(specs) == 0 {
		return "no arguments"
	}
	var n []string
	for _, s := range specs {
		n = append(n, s.Name)
	}
	return strings.Join(n, ", ")
}

func promptOne(s ArgSpec, in *bufio.Reader, out io.Writer) (any, error) {
	label := fmt.Sprintf("  %s (%s", s.Name, s.Type)
	if s.Optional {
		label += ", optional"
	}
	label += ")"
	if s.Default != nil {
		label += fmt.Sprintf(" [default %v]", s.Default)
	}
	if s.Doc != "" {
		label += " - " + s.Doc
	}
	for {
		fmt.Fprintf(out, "%s: ", label)
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if err != nil && line == "" {
			return nil, fmt.Errorf("input ended while asking for %q", s.Name)
		}
		if line == "" {
			if s.Default != nil {
				return s.Default, nil
			}
			if s.Optional {
				return nil, nil
			}
			fmt.Fprintf(out, "  %s is required\n", s.Name)
			continue
		}
		v, cerr := Coerce(s.Type, line)
		if cerr != nil {
			fmt.Fprintf(out, "  %v\n", cerr)
			continue
		}
		return v, nil
	}
}
