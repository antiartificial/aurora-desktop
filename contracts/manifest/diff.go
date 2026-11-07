package manifest

import (
	"fmt"
	"path"
	"strings"
)

// Change is one difference between two manifest versions.
type Change struct {
	Detail  string
	Consent bool // true if this change alone requires fresh consent
}

// DiffResult lists the consent-relevant and informational changes.
type DiffResult struct{ Changes []Change }

// NeedsConsent applies 0002's consent rule.
func (r DiffResult) NeedsConsent() bool {
	for _, c := range r.Changes {
		if c.Consent {
			return true
		}
	}
	return false
}

func (r *DiffResult) add(consent bool, format string, a ...any) {
	r.Changes = append(r.Changes, Change{Detail: fmt.Sprintf(format, a...), Consent: consent})
}

// effectRank orders effect classes by reach. destructive and external share a
// rank: neither is narrower than the other. Unknown classes rank highest, so a
// bad value can never be read as a narrowing.
func effectRank(e Effect) int {
	switch e {
	case EffectRead:
		return 0
	case EffectWrite:
		return 1
	case EffectDestructive, EffectExternal:
		return 2
	}
	return 3
}

// widens reports whether moving from old to new is not a strict narrowing.
func widens(old, new Effect) bool {
	return old != new && effectRank(new) >= effectRank(old)
}

func accessRank(a string) int {
	if a == "read" {
		return 0
	}
	return 1 // write, and anything unknown, is the wider grant
}

// Diff compares an installed manifest with an update.
//
// Consent is required when the update grants more than the user already
// agreed to: a capability not covered by an old one, a wider path access, or a
// verb whose effect class widens (or a new verb above read). Removals and
// narrowings never require consent.
func Diff(old, new *Manifest) DiffResult {
	var r DiffResult
	if old.App.ID != new.App.ID {
		r.add(true, "app id changed from %q to %q (a different app)", old.App.ID, new.App.ID)
	}
	if old.App.Version != new.App.Version {
		r.add(false, "version %s -> %s", old.App.Version, new.App.Version)
	}
	if old.App.Windowed != new.App.Windowed || old.App.Exec != new.App.Exec {
		// What the system launches changed, and a windowed app is launched with a display grant (0012, 0017).
		r.add(true, "launch target changed: windowed %v -> %v, exec %q -> %q", old.App.Windowed, new.App.Windowed, old.App.Exec, new.App.Exec)
	}
	diffPaths(&r, old.Capabilities.Paths, new.Capabilities.Paths)
	diffHosts(&r, old.Capabilities.Network, new.Capabilities.Network)
	diffDevices(&r, old.Capabilities.Devices, new.Capabilities.Devices)
	diffVerbs(&r, old.Verbs, new.Verbs)
	return r
}

func under(parent, child string) bool {
	parent, child = path.Clean(parent), path.Clean(child)
	return child == parent || strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}

func diffPaths(r *DiffResult, old, new []PathCap) {
	for _, n := range new {
		covered := false
		for _, o := range old {
			if under(o.Path, n.Path) && accessRank(o.Access) >= accessRank(n.Access) {
				covered = true
				break
			}
		}
		if !covered {
			r.add(true, "path %s (%s) is not covered by any previously granted path", n.Path, n.Access)
		}
	}
	for _, o := range old {
		if !containsPath(new, o) {
			r.add(false, "path %s (%s) removed or narrowed", o.Path, o.Access)
		}
	}
}

func containsPath(s []PathCap, p PathCap) bool {
	for _, x := range s {
		if x == p {
			return true
		}
	}
	return false
}

// hostCovers: old "example.com" covers any port; "*.example.com" covers its
// subdomains but not the apex; an old port, if given, must match.
func hostCovers(old, new string) bool {
	oh, op := splitHost(old)
	nh, np := splitHost(new)
	if op != "" && op != np {
		return false
	}
	if oh == nh {
		return true
	}
	return strings.HasPrefix(oh, "*.") && strings.HasSuffix(nh, oh[1:])
}

func diffHosts(r *DiffResult, old, new []string) {
	for _, n := range new {
		covered := false
		for _, o := range old {
			if hostCovers(o, n) {
				covered = true
				break
			}
		}
		if !covered {
			r.add(true, "network host %s is not covered by any previously granted host", n)
		}
	}
	for _, o := range old {
		if !contains(new, o) {
			r.add(false, "network host %s removed", o)
		}
	}
}

func diffDevices(r *DiffResult, old, new []string) {
	for _, n := range new {
		if !contains(old, n) {
			r.add(true, "device %s newly requested", n)
		}
	}
	for _, o := range old {
		if !contains(new, o) {
			r.add(false, "device %s removed", o)
		}
	}
}

func contains(s []string, x string) bool {
	for _, y := range s {
		if y == x {
			return true
		}
	}
	return false
}

func diffVerbs(r *DiffResult, old, new []Verb) {
	oldBy := map[string]Verb{}
	for _, v := range old {
		oldBy[v.Name] = v
	}
	newBy := map[string]bool{}
	for _, n := range new {
		newBy[n.Name] = true
		o, existed := oldBy[n.Name]
		switch {
		case !existed && n.Effect == EffectRead:
			r.add(false, "verb %s added (read)", n.Name)
		case !existed:
			r.add(true, "verb %s added with effect class %s", n.Name, n.Effect)
		case o.Effect == n.Effect:
		case widens(o.Effect, n.Effect):
			r.add(true, "verb %s effect class widened %s -> %s", n.Name, o.Effect, n.Effect)
		default:
			r.add(false, "verb %s effect class narrowed %s -> %s", n.Name, o.Effect, n.Effect)
		}
	}
	for _, o := range old {
		if !newBy[o.Name] {
			r.add(false, "verb %s removed", o.Name)
		}
	}
}
