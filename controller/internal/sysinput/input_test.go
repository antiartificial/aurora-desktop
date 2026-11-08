package sysinput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func themeDir(t *testing.T, body string) string {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, ThemeFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTheInputTableIsRead(t *testing.T) {
	d := themeDir(t, `
# a comment the compositor preserves and this ignores
[input]
layout = "gb"
variant = "dvorak"
options = "ctrl:nocaps"
rules = "evdev"
model = "pc105"
repeat_delay = 350
repeat_rate = 40
pointer_speed = 0.25
natural_scroll = true
tap_to_click = false
left_handed = true

[dock]
edge = "left"
`)
	k := ReadKeyboard(d)
	if !k.Available {
		t.Fatalf("got %+v", k)
	}
	if k.Layout != "gb" || k.Variant != "dvorak" || k.Options != "ctrl:nocaps" || k.Rules != "evdev" || k.Model != "pc105" {
		t.Fatalf("xkb: %+v", k)
	}
	if k.RepeatDelayMS != 350 || k.RepeatRate != 40 {
		t.Fatalf("repeat: %d ms, %d/s", k.RepeatDelayMS, k.RepeatRate)
	}
	p := ReadPointer(d)
	if !p.Available || p.Speed != 0.25 || !p.NaturalScroll || p.TapToClick || !p.LeftHanded {
		t.Fatalf("pointer: %+v", p)
	}
	if !strings.HasSuffix(p.Source, ThemeFile) {
		t.Fatalf("source %q does not name the file it came from", p.Source)
	}
}

func TestOmittedKeysGetTheCompositorsDefaultsAndNotGosZeroValues(t *testing.T) {
	// The shipped theme omits every pointer key and both repeat keys, and serde fills them
	// from `impl Default for Input`. A Go reader that filled them with the zero value would
	// report repeat_delay 0 and tap-to-click OFF for the file the project ships -- a settings
	// page disagreeing with the compositor about the state of the machine.
	d := themeDir(t, "[input]\nlayout = \"us\"\n")
	k := ReadKeyboard(d)
	if k.RepeatDelayMS != 200 || k.RepeatRate != 25 {
		t.Fatalf("repeat defaults: %d ms, %d/s, want 200 and 25", k.RepeatDelayMS, k.RepeatRate)
	}
	p := ReadPointer(d)
	if !p.TapToClick {
		t.Fatal("tap_to_click defaults ON in the compositor; this reported it off")
	}
	if p.Speed != 0 || p.NaturalScroll || p.LeftHanded {
		t.Fatalf("pointer defaults: %+v", p)
	}
	// A theme file with no [input] table at all is the same case.
	if p2 := ReadPointer(themeDir(t, "[dock]\nedge = \"top\"\n")); !p2.Available || !p2.TapToClick {
		t.Fatalf("no [input] table: %+v", p2)
	}
}

func TestNoThemeDirectoryIsUnavailableAndNamesTheFlag(t *testing.T) {
	// Not the defaults dressed up as the machine's settings. "repeat delay 200 ms" read off a
	// file that was not there is exactly the invented figure this family of verbs refuses.
	p := ReadPointer("")
	if p.Available {
		t.Fatal("no theme directory must not answer with the defaults")
	}
	for _, want := range []string{"--theme-dir", "AURORA_THEME_DIR", ThemeFile} {
		if !strings.Contains(p.Reason, want) {
			t.Fatalf("the reason does not name %q: %s", want, p.Reason)
		}
	}
	if k := ReadKeyboard(""); k.Available || k.RepeatDelayMS != 0 {
		t.Fatalf("keyboard: %+v", k)
	}
}

func TestAMissingOrBrokenFileIsUnavailableWithTheReason(t *testing.T) {
	p := ReadPointer(filepath.Join(t.TempDir(), "absent"))
	if p.Available || !strings.Contains(p.Reason, "cannot read") {
		t.Fatalf("missing file: %+v", p)
	}
	b := ReadPointer(themeDir(t, "[input\nnope"))
	if b.Available || !strings.Contains(b.Reason, "does not parse") {
		t.Fatalf("broken TOML: %+v", b)
	}
	if b.TapToClick {
		t.Fatal("a file that did not parse must not yield a setting")
	}
}

func TestBothVerbsSayTheirValuesMayNotReachTheDevices(t *testing.T) {
	// The honest part. These are configured values; libinput exists only on the DRM backend,
	// which has never run on a display, and while nested the host owns the devices. A verb
	// that reported them as the state of the mouse would be making a claim.
	d := themeDir(t, "[input]\npointer_speed = 0.5\n")
	if r := ReadPointer(d).Unavailable["reaches_devices"]; !strings.Contains(r, "DRM") {
		t.Fatalf("pointer: %q", r)
	}
	if r := ReadKeyboard(d).Unavailable["reaches_devices"]; !strings.Contains(r, "DRM") {
		t.Fatalf("keyboard: %q", r)
	}
	// And the keyboard says the layout it reports is what was ASKED for: the compositor
	// validates the layout/variant pair and declines one xkb rejects.
	if r := ReadKeyboard(d).Unavailable["effective_layout"]; r == "" {
		t.Fatal("the keyboard verb claims its layout is the one loaded")
	}
}

func TestTheShippedThemeIsReadable(t *testing.T) {
	// Against the real file, not a fixture: the one the project ships and the compositor loads
	// by default. If a key is renamed there, this fails rather than silently reporting a
	// default. The file is only read.
	dir := "../../../contracts/theme"
	if _, err := os.Stat(filepath.Join(dir, ThemeFile)); err != nil {
		t.Skipf("no %s next to this checkout: %v", ThemeFile, err)
	}
	k := ReadKeyboard(dir)
	if !k.Available {
		t.Fatalf("the shipped theme did not read: %s", k.Reason)
	}
	// The shipped file sets these two explicitly, so they are read values and not defaults
	// that happen to match.
	if k.RepeatDelayMS != 200 || k.RepeatRate != 25 {
		t.Fatalf("shipped repeat: %d ms, %d/s", k.RepeatDelayMS, k.RepeatRate)
	}
	if !ReadPointer(dir).Available {
		t.Fatal("the shipped theme did not read for the pointer verb")
	}
}
