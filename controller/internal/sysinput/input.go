// Package sysinput reads the keyboard and pointer settings out of the theme file's [input] table.
//
// # WHAT THESE VERBS DESCRIBE, PRECISELY
//
// Not the devices. The configured values. `[input]` in the theme file is where the settings panel
// writes a choice and where the compositor reads one; xkb takes `rules/model/layout/variant/
// options` and `repeat_delay/repeat_rate`, and libinput takes `pointer_speed`, `natural_scroll`,
// `tap_to_click` and `left_handed`. A verb that said "the pointer acceleration is 0.25" when the
// configuration says 0.25 and the running compositor is a winit client whose host owns the mouse
// would be making a claim about the machine that is not true, so `reaches_devices` is reported as
// unavailable with the reason. libinput exists only on the DRM backend, and `--drm` has never run
// on a display.
//
// # WHY THERE IS NO WRITE VERB, AND WHY THAT IS NOT A HALF-BUILD
//
// The brief asked for a set path. It is not here, and the reason is a property of the system
// rather than a shortage of time.
//
// The theme file has exactly one writer: the compositor, through `Fx::apply_edits`, which
// rewrites the keys it is given while preserving every comment and every line it was not asked
// to touch, and then picks up its own change through the normal hot reload. The settings panel
// does not hold a second copy of any setting; it is rebuilt from the file after each change.
// That single-writer property is what makes the panel correct.
//
// A controller write verb breaks it twice. It would need a second comment-preserving TOML
// rewriter, in Go, for a file whose format is defined by a Rust implementation -- which is the
// exact shape of the defect `demos/theme-drift/run.sh` exists to demonstrate, one file with two
// programs' understanding of it. And two processes rewriting the same file have no ordering: a
// controller write between the panel's read and its write silently loses, with no error for
// anyone to see.
//
// The write path for pointer and keyboard therefore belongs to the component that owns the file.
// What it needs is a verb the COMPOSITOR serves, reaching the same `Fx::apply_edits` the panel
// already calls, which means a request channel into the compositor that does not exist yet (its
// only server socket carries signed approval prompts). Until that exists, `system.pointer@v1`
// and `system.keyboard@v1` are first-class reads and nothing claims otherwise.
package sysinput

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// ThemeFile is the file name the compositor reads, from compositor/src/fx/theme.rs THEME_FILE.
const ThemeFile = "aurora-glass.toml"

// Input is the [input] table. Zero value is NOT the default: the defaults are the Rust
// `impl Default for Input`'s, and `defaults()` is the only thing that should construct one,
// because serde fills a missing key with that default and a Go reader that filled it with 0
// would report repeat_delay 0 and tap_to_click off for the shipped theme, which omits both.
type Input struct {
	Rules       string  `toml:"rules"`
	Model       string  `toml:"model"`
	Layout      string  `toml:"layout"`
	Variant     string  `toml:"variant"`
	Options     string  `toml:"options"`
	RepeatDelay int     `toml:"repeat_delay"`
	RepeatRate  int     `toml:"repeat_rate"`
	TapToClick  bool    `toml:"tap_to_click"`
	NaturalScr  bool    `toml:"natural_scroll"`
	Speed       float64 `toml:"pointer_speed"`
	LeftHanded  bool    `toml:"left_handed"`
}

// defaults mirrors compositor/src/fx/theme.rs `impl Default for Input`. Tap-to-click is ON,
// which is the one default that is not the zero value and the one a Go reader would get wrong.
func defaults() Input {
	return Input{RepeatDelay: 200, RepeatRate: 25, TapToClick: true}
}

// Keyboard is the reply of system.keyboard@v1.
type Keyboard struct {
	// Empty means "the xkb server default", which is a US layout. It is reported as empty
	// rather than as "us", because the file says nothing and guessing which default xkb will
	// pick is how a settings page comes to disagree with the keys under the user's fingers.
	Rules   string `json:"rules"`
	Model   string `json:"model"`
	Layout  string `json:"layout"`
	Variant string `json:"variant"`
	Options string `json:"options"`
	// Milliseconds before a held key repeats, and repeats per second.
	RepeatDelayMS int `json:"repeat_delay_ms"`
	RepeatRate    int `json:"repeat_rate"`

	Source      string            `json:"source"`
	Available   bool              `json:"available"`
	Reason      string            `json:"reason,omitempty"`
	Unavailable map[string]string `json:"unavailable,omitempty"`
}

// Pointer is the reply of system.pointer@v1.
type Pointer struct {
	// Speed is libinput's pointer acceleration, -1 slowest to 1 fastest; 0 is the device
	// default, not "no acceleration".
	Speed         float64           `json:"speed"`
	NaturalScroll bool              `json:"natural_scroll"`
	TapToClick    bool              `json:"tap_to_click"`
	LeftHanded    bool              `json:"left_handed"`
	Source        string            `json:"source"`
	Available     bool              `json:"available"`
	Reason        string            `json:"reason,omitempty"`
	Unavailable   map[string]string `json:"unavailable,omitempty"`
}

// noDevices is the same note on both verbs: what is read is configuration, not device state.
const noDevices = "these are the CONFIGURED values. They reach the devices through libinput, which exists only on the DRM backend; while the compositor is a nested winit client the host owns the devices and nothing here is in force. The controller cannot tell which backend the compositor is running, so it does not guess"

const noThemeDir = "no theme directory is configured: start controllerd with --theme-dir, or set AURORA_THEME_DIR, naming the directory that holds " + ThemeFile + ". It is not guessed, because the compositor's own default is the repo's contracts/theme and a controller that assumed it would answer about a tree it may not be running from"

// Read parses the [input] table out of <dir>/aurora-glass.toml. An empty dir, a missing file or
// one that does not parse all produce an unavailable answer with the reason, never the defaults
// dressed up as the machine's settings: "repeat delay 200 ms" read off a file that was not there
// is the kind of plausible number this whole family of verbs refuses to invent.
func Read(dir string) (Input, string, error) {
	if dir == "" {
		return Input{}, "", fmt.Errorf("%s", noThemeDir)
	}
	p := filepath.Join(dir, ThemeFile)
	b, err := os.ReadFile(p)
	if err != nil {
		return Input{}, p, fmt.Errorf("cannot read %s: %v", p, err)
	}
	// Only the [input] table is decoded. The rest of the theme is shaders, colours and dock
	// geometry; a strict decode of the whole file would make this verb fail whenever the
	// compositor gained a key, which is a coupling it has no reason to have.
	var doc struct {
		Input *Input `toml:"input"`
	}
	in := defaults()
	doc.Input = &in
	if _, err := toml.Decode(string(b), &doc); err != nil {
		return Input{}, p, fmt.Errorf("%s does not parse as TOML: %v", p, err)
	}
	return in, p, nil
}

// ReadKeyboard answers system.keyboard@v1.
func ReadKeyboard(dir string) Keyboard {
	in, p, err := Read(dir)
	if err != nil {
		return Keyboard{Reason: err.Error(), Unavailable: map[string]string{}}
	}
	return Keyboard{
		Rules: in.Rules, Model: in.Model, Layout: in.Layout, Variant: in.Variant, Options: in.Options,
		RepeatDelayMS: in.RepeatDelay, RepeatRate: in.RepeatRate,
		Source: p, Available: true,
		Unavailable: map[string]string{
			"reaches_devices":  noDevices,
			"effective_layout": "what xkb actually compiled is the compositor's; it validates the layout/variant pair and declines one it rejects, so a value here is what was asked for, not necessarily what is loaded",
		},
	}
}

// ReadPointer answers system.pointer@v1.
func ReadPointer(dir string) Pointer {
	in, p, err := Read(dir)
	if err != nil {
		return Pointer{Reason: err.Error(), Unavailable: map[string]string{}}
	}
	return Pointer{
		Speed: in.Speed, NaturalScroll: in.NaturalScr, TapToClick: in.TapToClick, LeftHanded: in.LeftHanded,
		Source: p, Available: true,
		Unavailable: map[string]string{"reaches_devices": noDevices},
	}
}
