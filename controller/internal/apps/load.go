// Package apps loads and indexes app manifests.
package apps

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/antiartificial/aurora/contracts/manifest"
)

// App is one loaded, validated manifest and the directory it came from.
type App struct {
	Dir      string
	Manifest *manifest.Manifest
}

// ID is the app's reverse-DNS id.
func (a *App) ID() string { return a.Manifest.App.ID }

// Verb finds a verb by exact versioned name.
func (a *App) Verb(name string) (manifest.Verb, bool) {
	for _, v := range a.Manifest.Verbs {
		if v.Name == name {
			return v, true
		}
	}
	return manifest.Verb{}, false
}

// Skipped is a manifest that was found but not loaded.
type Skipped struct {
	Path   string
	Reason string
}

// Registry is the set of loaded apps, ordered by id.
type Registry struct {
	apps []*App
	byID map[string]*App
}

// Load reads every <dir>/<app>/manifest.toml under each dir. Invalid
// manifests and duplicate ids are skipped and returned with a reason.
func Load(dirs ...string) (*Registry, []Skipped) {
	r := &Registry{byID: map[string]*App{}}
	var skipped []Skipped
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			skipped = append(skipped, Skipped{dir, err.Error()})
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			appDir, err := filepath.Abs(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			path := filepath.Join(appDir, "manifest.toml")
			src, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				skipped = append(skipped, Skipped{path, err.Error()})
				continue
			}
			m, err := manifest.Load(src)
			if err != nil {
				skipped = append(skipped, Skipped{path, err.Error()})
				continue
			}
			if prev, dup := r.byID[m.App.ID]; dup {
				skipped = append(skipped, Skipped{path, fmt.Sprintf("duplicate app id %s (already loaded from %s)", m.App.ID, prev.Dir)})
				continue
			}
			a := &App{Dir: appDir, Manifest: m}
			r.byID[a.ID()] = a
			r.apps = append(r.apps, a)
		}
	}
	sort.Slice(r.apps, func(i, j int) bool { return r.apps[i].ID() < r.apps[j].ID() })
	return r, skipped
}

// Apps returns all apps ordered by id.
func (r *Registry) Apps() []*App { return r.apps }

// App looks an app up by id.
func (r *Registry) App(id string) (*App, bool) { a, ok := r.byID[id]; return a, ok }

// ExecStatus resolves a windowed app's executable and says whether it can be
// launched. A windowed app whose binary was never built (the Haiku targets are
// skipped without HAIKU_SRC) must be reported as such, never listed as
// launchable. For a non-windowed app it returns "", "".
// Launchable reports whether a dock click on this app can do anything, and
// why not when it cannot. The decision belongs here, next to the filesystem,
// rather than in the compositor: the shell should not be stat-ing app
// directories to work out what it may offer.
//
// Three ways to be unlaunchable, all of them real cases in this tree:
// a windowed app whose binary was never built (the Haiku apps need HAIKU_SRC
// and cmake), an app declaring no verbs at all, and a verb-only app with no
// run script -- Haiku Ledger serves its verbs directly on the bus, so the
// controller cannot invoke them even though the manifest lists them.
func (a *App) Launchable() (ok bool, problem string) {
	if a.Manifest.App.Windowed {
		if _, p := a.ExecStatus(); p != "" {
			return false, p
		}
		return true, ""
	}
	if len(a.Manifest.Verbs) == 0 {
		return false, "declares no verbs and is not windowed"
	}
	run := filepath.Join(a.Dir, "run")
	fi, err := os.Stat(run)
	switch {
	case os.IsNotExist(err):
		return false, "no run script: its verbs are served on the bus, not invocable through the controller"
	case err != nil:
		return false, "cannot stat run: " + err.Error()
	case fi.IsDir() || fi.Mode()&0o111 == 0:
		return false, "run is not an executable file"
	}
	return true, ""
}

func (a *App) ExecStatus() (path, problem string) {
	if !a.Manifest.App.Windowed {
		return "", ""
	}
	path = a.Manifest.App.ExecPath(a.Dir)
	fi, err := os.Stat(path)
	switch {
	case os.IsNotExist(err):
		return path, "not built: " + path + " does not exist"
	case err != nil:
		return path, "cannot stat exec: " + err.Error()
	case fi.IsDir() || fi.Mode()&0o111 == 0:
		return path, "exec is not an executable file: " + path
	}
	return path, ""
}
