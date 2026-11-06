package invoke

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// planGrants lists the grants an invoke needs, each classified. A read-effect
// verb has no outDir, so it can only ever get the ro input grant.
func (iv *Invoker) planGrants(m *manifest.Manifest, input, outDir string) []prov.Grant {
	var gs []prov.Grant
	for _, c := range []struct{ path, mode string }{{input, "ro"}, {outDir, "rw"}} {
		if c.path != "" {
			gs = append(gs, prov.Grant{Path: c.path, Mode: c.mode, Source: iv.classify(m, c.path, c.mode)})
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
