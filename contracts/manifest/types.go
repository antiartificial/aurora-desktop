// Package manifest defines app manifest schema v0 (decision 0002): types,
// a TOML parser, a validator, MIME queries, and the consent diff.
package manifest

import "path/filepath"

// Effect is a verb's effect class.
type Effect string

const (
	EffectRead        Effect = "read"
	EffectWrite       Effect = "write"
	EffectDestructive Effect = "destructive"
	EffectExternal    Effect = "external"
)

// Manifest is the whole file. Sections follow 0002's numbered list.
type Manifest struct {
	App          App          `toml:"app"`                 // 1. identity + image hash
	Runtimes     []Runtime    `toml:"runtime,omitempty"`   // 2. pinned runtimes
	Capabilities Capabilities `toml:"capabilities"`        // 3. requested capabilities
	Types        Types        `toml:"types"`               // 4. types opened and created
	Verbs        []Verb       `toml:"verb,omitempty"`      // 5. verbs
	Events       []Event      `toml:"event,omitempty"`     // 6. emitted events
	Settings     []Setting    `toml:"setting,omitempty"`   // 7. typed settings
	Replicants   []Replicant  `toml:"replicant,omitempty"` // 8. replicants
	Migrations   []Migration  `toml:"migration,omitempty"` // 9. config migrations

	lines *lineIndex // source positions; nil for hand-built manifests
}

type App struct {
	ID        string `toml:"id"`
	Name      string `toml:"name"`
	Version   string `toml:"version"`
	ImageHash string `toml:"image_hash"`

	// Windowed marks an app that opens its own window and is launched
	// directly, not invoked as a verb (decision 0012). Exec is the
	// executable to launch; a relative path is resolved against the app's
	// directory. Exec is required when Windowed is true and refused otherwise.
	Windowed bool   `toml:"windowed,omitempty"`
	Exec     string `toml:"exec,omitempty"`

	// Category is ADVISORY, PRESENTATION ONLY, like Verb.Group: the section of
	// the application menu this app is listed under. It must be one of
	// Categories. Nothing may make an access decision, an approval decision or
	// any other policy choice from it (decision 0025).
	Category string `toml:"category,omitempty"`
}

// ExecPath resolves App.Exec: an absolute path is cleaned, a relative one is
// joined to appDir. It returns "" when Exec is unset.
func (a App) ExecPath(appDir string) string {
	switch {
	case a.Exec == "":
		return ""
	case filepath.IsAbs(a.Exec):
		return filepath.Clean(a.Exec)
	}
	return filepath.Join(appDir, a.Exec)
}

type Runtime struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

type Capabilities struct {
	Paths   []PathCap `toml:"paths,omitempty"`
	Network []string  `toml:"network,omitempty"`
	Devices []string  `toml:"devices,omitempty"`
	// UI lists the accessibility classes requested (decision 0017): "read" is the right to read the tree of the app's own windows.
	UI []string `toml:"ui,omitempty"`
}

// PathCap grants a path (and everything under it) with read or write access.
type PathCap struct {
	Path   string `toml:"path"`
	Access string `toml:"access"`
}

type Types struct {
	Opens   []string `toml:"opens,omitempty"`
	Creates []string `toml:"creates,omitempty"`
}

// Verb is a named, versioned operation. Capabilities lists the app-level
// capabilities it uses as "path:<p>", "net:<host>", "device:<d>" or "ui:<class>".
// Accepts is the MIME patterns of objects the verb can take.
type Verb struct {
	Name         string   `toml:"name"`
	Summary      string   `toml:"summary,omitempty"`
	Effect       Effect   `toml:"effect"`
	Accepts      []string `toml:"accepts,omitempty"`
	Capabilities []string `toml:"capabilities,omitempty"`
	// Default marks this as the verb run when the user simply opens a file
	// of one of its Accepts types. At most one default per accepted type per
	// app. It is a convenience, not a grant: the verb's Effect and
	// capabilities still apply unchanged.
	Default bool `toml:"default,omitempty"`
	// Group is ADVISORY, PRESENTATION ONLY: a heading for organising a long
	// verb menu. It is not semantic and not security-relevant. Nothing may
	// make an access decision, an approval decision or any other policy
	// choice from it. Effect is the only authority signal (decision 0012).
	Group string `toml:"group,omitempty"`

	Input  map[string]string `toml:"input,omitempty"`
	Output map[string]string `toml:"output,omitempty"`
}

type Event struct {
	Name    string            `toml:"name"`
	Payload map[string]string `toml:"payload,omitempty"`
}

type Setting struct {
	Key         string `toml:"key"`
	Type        string `toml:"type"`
	Default     any    `toml:"default,omitempty"`
	Description string `toml:"description,omitempty"`
}

type Replicant struct {
	Name  string `toml:"name"`
	Title string `toml:"title,omitempty"`
}

type Migration struct {
	From        string `toml:"from"`
	To          string `toml:"to"`
	Description string `toml:"description,omitempty"`
}
