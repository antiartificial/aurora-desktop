package manifest

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var validEffects = map[Effect]bool{EffectRead: true, EffectWrite: true, EffectDestructive: true, EffectExternal: true}

var scalarTypes = map[string]bool{"string": true, "int": true, "float": true, "bool": true, "path": true, "bytes": true}

type validator struct {
	m    *Manifest
	errs Errors
}

func (v *validator) add(field, table string, idx int, key, format string, a ...any) {
	v.errs = append(v.errs, Error{Field: field, Line: v.m.lines.line(table, idx, key), Msg: fmt.Sprintf(format, a...)})
}

// Validate applies the schema v0 rules and returns every problem found.
func (m *Manifest) Validate() Errors {
	v := &validator{m: m}
	v.app()
	v.runtimes()
	v.capabilities()
	v.types()
	v.verbs()
	v.events()
	v.settings()
	v.replicants()
	v.migrations()
	return v.errs
}

func (v *validator) app() {
	a := v.m.App
	if !idRe.MatchString(a.ID) {
		v.add("app.id", "app", 0, "id", "%q must be a dotted lowercase identifier like org.example.app", a.ID)
	}
	if strings.TrimSpace(a.Name) == "" {
		v.add("app.name", "app", 0, "name", "is required")
	}
	if !semverRe.MatchString(a.Version) {
		v.add("app.version", "app", 0, "version", "%q is not a semantic version (MAJOR.MINOR.PATCH)", a.Version)
	}
	if !hashRe.MatchString(a.ImageHash) {
		v.add("app.image_hash", "app", 0, "image_hash", "%q must be sha256: followed by 64 lowercase hex digits", a.ImageHash)
	}
	switch {
	case a.Windowed && strings.TrimSpace(a.Exec) == "":
		v.add("app.exec", "app", 0, "windowed", "is required when windowed = true: a windowed app is launched directly, so the manifest must say what to run")
	case !a.Windowed && a.Exec != "":
		v.add("app.exec", "app", 0, "exec", "%q is only valid with windowed = true; a non-windowed app is invoked through its verbs", a.Exec)
	case strings.ContainsRune(a.Exec, 0):
		v.add("app.exec", "app", 0, "exec", "contains a NUL byte")
	}
}

func (v *validator) runtimes() {
	seen := map[string]bool{}
	for i, r := range v.m.Runtimes {
		f := fmt.Sprintf("runtime[%d]", i)
		if !keyNameRe.MatchString(r.Name) {
			v.add(f+".name", "runtime", i, "name", "%q must be lowercase letters, digits and underscores", r.Name)
		}
		if seen[r.Name] {
			v.add(f+".name", "runtime", i, "name", "duplicate runtime %q", r.Name)
		}
		seen[r.Name] = true
		if !semverRe.MatchString(r.Version) {
			v.add(f+".version", "runtime", i, "version", "%q is not an exact pinned version (MAJOR.MINOR.PATCH)", r.Version)
		}
	}
}

func (v *validator) capabilities() {
	c := v.m.Capabilities
	for i, p := range c.Paths {
		f := fmt.Sprintf("capabilities.paths[%d]", i)
		if msg := validPath(p.Path); msg != "" {
			v.add(f+".path", "capabilities.paths", i, "path", "%q %s", p.Path, msg)
		}
		if p.Access != "read" && p.Access != "write" {
			v.add(f+".access", "capabilities.paths", i, "access", `%q must be "read" or "write"`, p.Access)
		}
	}
	for i, h := range c.Network {
		if msg := validHost(h); msg != "" {
			v.add(fmt.Sprintf("capabilities.network[%d]", i), "capabilities", 0, "network", "%q %s", h, msg)
		}
	}
	for i, d := range c.Devices {
		if !deviceRe.MatchString(d) {
			v.add(fmt.Sprintf("capabilities.devices[%d]", i), "capabilities", 0, "devices", "%q must be a lowercase device class name", d)
		}
	}
	for i, u := range c.UI {
		if !uiClasses[u] {
			v.add(fmt.Sprintf("capabilities.ui[%d]", i), "capabilities", 0, "ui", `%q is not a ui class (want "read")`, u)
		}
	}
}

func (v *validator) types() {
	for i, t := range v.m.Types.Opens {
		if !validMIME(t) {
			v.add(fmt.Sprintf("types.opens[%d]", i), "types", 0, "opens", "%q is not a valid MIME type", t)
		}
	}
	for i, t := range v.m.Types.Creates {
		if !validMIME(t) {
			v.add(fmt.Sprintf("types.creates[%d]", i), "types", 0, "creates", "%q is not a valid MIME type", t)
		}
	}
}

// capDeclared reports whether a verb capability ref names a requested capability.
func (m *Manifest) capDeclared(ref string) (known bool, msg string) {
	kind, val, ok := strings.Cut(ref, ":")
	if !ok || val == "" {
		return false, `must look like "path:<path>", "net:<host>", "device:<name>" or "ui:<class>"`
	}
	switch kind {
	case "path":
		for _, p := range m.Capabilities.Paths {
			if p.Path == val {
				return true, ""
			}
		}
	case "net":
		for _, h := range m.Capabilities.Network {
			if h == val {
				return true, ""
			}
		}
	case "device":
		for _, d := range m.Capabilities.Devices {
			if d == val {
				return true, ""
			}
		}
	case "ui":
		for _, u := range m.Capabilities.UI {
			if u == val {
				return true, ""
			}
		}
	default:
		return false, fmt.Sprintf("unknown capability kind %q (want path, net, device or ui)", kind)
	}
	return false, "is not among the app's requested capabilities"
}

// uiClasses are the accessibility classes (decision 0017). "read" is the only one composed.
var uiClasses = map[string]bool{"read": true}

const maxGroupLen = 40

// validGroup checks the advisory display group. It reaches the UI, so it gets
// the standard other untrusted display text gets: no control characters,
// bounded length.
func validGroup(g string) string {
	switch {
	case strings.TrimSpace(g) == "":
		return "must not be empty or blank"
	case utf8.RuneCountInString(g) > maxGroupLen:
		return fmt.Sprintf("is longer than %d characters", maxGroupLen)
	}
	for _, r := range g {
		if unicode.IsControl(r) {
			return "contains a control character"
		}
	}
	return ""
}

func (v *validator) verbs() {
	seen := map[string]bool{}
	defaultFor := map[string]string{} // accepted MIME type -> default verb name
	for i, vb := range v.m.Verbs {
		f := fmt.Sprintf("verb[%d]", i)
		if vb.Name != "" {
			f = fmt.Sprintf("verb[%d] (%s)", i, vb.Name)
		}
		switch {
		case !verbRe.MatchString(vb.Name):
			hint := ""
			if !strings.Contains(vb.Name, "@") {
				hint = `; verb names carry a major version, e.g. "resize@v1"`
			}
			v.add(f+".name", "verb", i, "name", "%q is not a valid verb name%s", vb.Name, hint)
		case seen[vb.Name]:
			v.add(f+".name", "verb", i, "name", "duplicate verb name %q", vb.Name)
		}
		seen[vb.Name] = true
		if !validEffects[vb.Effect] {
			v.add(f+".effect", "verb", i, "effect", `unknown effect class %q (want read, write, destructive or external)`, vb.Effect)
		}
		if (vb.Effect == EffectExternal || vb.Effect == EffectDestructive) && len(vb.Capabilities) == 0 {
			v.add(f+".capabilities", "verb", i, "effect", "a %s verb must declare the capabilities it needs", vb.Effect)
		}
		for j, ref := range vb.Capabilities {
			if ok, msg := v.m.capDeclared(ref); !ok {
				v.add(fmt.Sprintf("%s.capabilities[%d]", f, j), "verb", i, "capabilities", "%q %s", ref, msg)
			}
		}
		for j, a := range vb.Accepts {
			if !validMIMEOrWild(a) {
				v.add(fmt.Sprintf("%s.accepts[%d]", f, j), "verb", i, "accepts", "%q is not a valid MIME type or pattern", a)
			}
		}
		if vb.Default {
			if len(vb.Accepts) == 0 {
				v.add(f+".default", "verb", i, "default", "a default verb must declare accepts: a verb that takes no document is the default for nothing")
			}
			for _, a := range vb.Accepts {
				if prev, dup := defaultFor[a]; dup {
					v.add(f+".default", "verb", i, "default", "%s and %s are both default for %s; an app has at most one default per accepted type", prev, vb.Name, a)
				} else {
					defaultFor[a] = vb.Name
				}
			}
		}
		if vb.Group != "" || v.m.lines.has("verb", i, "group") {
			if msg := validGroup(vb.Group); msg != "" {
				v.add(f+".group", "verb", i, "group", "%q %s", vb.Group, msg)
			}
		}
		v.schema(f+".input", "verb", i, "input", vb.Input)
		v.schema(f+".output", "verb", i, "output", vb.Output)
	}
}

// schema checks field types: a scalar name or a MIME type, optional "?" suffix.
func (v *validator) schema(field, table string, idx int, key string, s map[string]string) {
	for name, t := range s {
		if !keyNameRe.MatchString(name) {
			v.add(field+"."+name, table, idx, key, "field name must be lowercase letters, digits and underscores")
		}
		base := strings.TrimSuffix(t, "?")
		if !scalarTypes[base] && !validMIMEOrWild(base) {
			v.add(field+"."+name, table, idx, key, "%q is not a scalar type (string, int, float, bool, path, bytes) or MIME type", t)
		}
	}
}

func (v *validator) events() {
	seen := map[string]bool{}
	for i, e := range v.m.Events {
		f := fmt.Sprintf("event[%d]", i)
		if !topicRe.MatchString(e.Name) {
			v.add(f+".name", "event", i, "name", "%q is not a valid topic name (lowercase dotted segments)", e.Name)
		} else if seen[e.Name] {
			v.add(f+".name", "event", i, "name", "duplicate event name %q", e.Name)
		}
		seen[e.Name] = true
		v.schema(f+".payload", "event", i, "payload", e.Payload)
	}
}

func (v *validator) settings() {
	seen := map[string]bool{}
	for i, s := range v.m.Settings {
		f := fmt.Sprintf("setting[%d]", i)
		if !keyNameRe.MatchString(s.Key) {
			v.add(f+".key", "setting", i, "key", "%q must be lowercase letters, digits and underscores", s.Key)
		} else if seen[s.Key] {
			v.add(f+".key", "setting", i, "key", "duplicate setting key %q", s.Key)
		}
		seen[s.Key] = true
		ok := false
		switch s.Type {
		case "string", "int", "float", "bool":
			ok = true
		default:
			v.add(f+".type", "setting", i, "type", "%q must be string, int, float or bool", s.Type)
		}
		if ok && s.Default != nil {
			good := false
			switch d := s.Default.(type) {
			case string:
				good = s.Type == "string"
			case int64:
				good = s.Type == "int"
			case float64:
				good = s.Type == "float"
			case bool:
				good = s.Type == "bool"
			default:
				_ = d
			}
			if !good {
				v.add(f+".default", "setting", i, "default", "default %v does not match type %s", s.Default, s.Type)
			}
		}
	}
}

func (v *validator) replicants() {
	seen := map[string]bool{}
	for i, r := range v.m.Replicants {
		f := fmt.Sprintf("replicant[%d]", i)
		if !keyNameRe.MatchString(r.Name) {
			v.add(f+".name", "replicant", i, "name", "%q must be lowercase letters, digits and underscores", r.Name)
		} else if seen[r.Name] {
			v.add(f+".name", "replicant", i, "name", "duplicate replicant name %q", r.Name)
		}
		seen[r.Name] = true
	}
}

func (v *validator) migrations() {
	for i, mg := range v.m.Migrations {
		f := fmt.Sprintf("migration[%d]", i)
		if !semverRe.MatchString(mg.From) {
			v.add(f+".from", "migration", i, "from", "%q is not a semantic version", mg.From)
		}
		if !semverRe.MatchString(mg.To) {
			v.add(f+".to", "migration", i, "to", "%q is not a semantic version", mg.To)
		}
		if mg.From == mg.To && mg.From != "" {
			v.add(f+".to", "migration", i, "to", "must differ from from")
		}
	}
}
