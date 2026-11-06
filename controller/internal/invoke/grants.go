package invoke

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// planGrants lists the grants an invoke needs, each classified: the input file
// ro and the single output file rw (never its directory). A read-effect verb
// has no output, so it can only ever get the ro input grant. The paths are
// canonical and are resolved here exactly once: classification and the grant
// call use these very strings, and the domain binds them without re-resolving.
func (iv *Invoker) planGrants(m *manifest.Manifest, verb manifest.Verb, input, output string) []prov.Grant {
	var gs []prov.Grant
	for _, c := range []struct{ path, mode string }{{input, "ro"}, {output, "rw"}} {
		if c.path != "" {
			gs = append(gs, prov.Grant{Path: c.path, Mode: c.mode, Source: iv.classify(m, c.path, c.mode)})
		}
	}
	return append(gs, planDevices(m, verb)...)
}

// planDevices turns the device capabilities a verb lists ("device:gpu") into
// grants. A device is only ever granted because the verb asked for it, and the
// manifest validator has already refused a verb that names a device the
// manifest does not declare. A device a manifest declares but no verb lists
// is never granted: the declaration is a request, not a grant.
func planDevices(m *manifest.Manifest, verb manifest.Verb) []prov.Grant {
	var gs []prov.Grant
	for _, c := range verb.Capabilities {
		if class, ok := strings.CutPrefix(c, "device:"); ok && class != "" {
			gs = append(gs, prov.Grant{Path: "device:" + class, Mode: prov.ModeUse, Device: class, Source: prov.SourceAmbient})
		}
	}
	return gs
}

func firstGesture(gs []prov.Grant) (prov.Grant, bool) {
	for _, g := range gs {
		if g.Source == prov.SourceGesture {
			return g, true
		}
	}
	return prov.Grant{}, false
}

// classify says whether a grant is covered by a declared capabilities.paths
// entry: the grant must be the declared path or below it, and a "read"
// declaration covers only "ro" grants. Anything else is a gesture.
//
// contracts/manifest keeps its coverage rule unexported (diff.go), so this is
// the one place the controller applies it; grants_test.go pins it.
func (iv *Invoker) classify(m *manifest.Manifest, path, mode string) string {
	for _, c := range m.Capabilities.Paths {
		if mode == "rw" && c.Access != "write" {
			continue
		}
		if within(iv.expand(c.Path), path) {
			return prov.SourceAmbient
		}
	}
	return prov.SourceGesture
}

// expand turns a manifest path ("~/x" or absolute) into a canonical absolute
// path, resolving symlinks when the path exists (grant paths are canonical).
func (iv *Invoker) expand(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home := iv.Home
		if home == "" {
			home, _ = os.UserHomeDir()
		}
		p = filepath.Join(home, rest)
	}
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func within(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}
