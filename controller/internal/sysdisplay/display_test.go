package sysdisplay

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// edid builds a 128-byte EDID base block from the numbers that matter here.
//
// It is synthetic on purpose. The obvious fixture is this machine's real DP-1 block, and it is
// not used, because an EDID carries the monitor's serial number and committing one would put a
// stable identifier for the user's hardware in the repository -- which is the very field `Read`
// goes out of its way not to return. So the TIMINGS are the real ones (pixel clock 53325 in
// 10 kHz units, 3840 active + 160 blank by 2160 + 62, 699x395 mm, which is this display's own
// preferred mode at 59.997 Hz), and the identity is invented. The real block was read live
// through the verb and agreed: DP-1, 3840x2160, 59997 mHz, 699x395 mm, scale 1.5.
type dtd struct {
	clock10kHz, hActive, hBlank, vActive, vBlank, mmW, mmH int
}

func edid(mfr string, product, serial int, cmW, cmH int, t0 *dtd, name string) []byte {
	b := make([]byte, 128)
	copy(b, []byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0})
	if len(mfr) == 3 {
		v := 0
		for i := 0; i < 3; i++ {
			v |= int(mfr[i]-'A'+1) << (10 - 5*i)
		}
		b[8], b[9] = byte(v>>8), byte(v)
	}
	b[10], b[11] = byte(product), byte(product>>8)
	b[12], b[13], b[14], b[15] = byte(serial), byte(serial>>8), byte(serial>>16), byte(serial>>24)
	b[18], b[19] = 1, 4
	b[21], b[22] = byte(cmW), byte(cmH)
	if t0 != nil {
		d := b[54:72]
		d[0], d[1] = byte(t0.clock10kHz), byte(t0.clock10kHz>>8)
		d[2] = byte(t0.hActive)
		d[3] = byte(t0.hBlank)
		d[4] = byte(t0.hActive>>8)<<4 | byte(t0.hBlank>>8)&0x0f
		d[5] = byte(t0.vActive)
		d[6] = byte(t0.vBlank)
		d[7] = byte(t0.vActive>>8)<<4 | byte(t0.vBlank>>8)&0x0f
		d[12] = byte(t0.mmW)
		d[13] = byte(t0.mmH)
		d[14] = byte(t0.mmW>>8)<<4 | byte(t0.mmH>>8)&0x0f
	}
	// A monitor-name descriptor in the second slot: zero timing bytes, tag 0xFC.
	d := b[72:90]
	d[3] = 0xFC
	copy(d[5:18], []byte(name+"\n            "))
	return b
}

// realPanel is this machine's display, with an invented identity.
func realPanel() []byte {
	return edid("ZZZ", 0x1234, 0xDEADBEEF, 70, 40,
		&dtd{clock10kHz: 53325, hActive: 3840, hBlank: 160, vActive: 2160, vBlank: 62, mmW: 699, mmH: 395},
		"TESTPANEL")
}

// tree writes a /sys/class/drm-shaped directory. `files` maps a relative path to its contents;
// an "edid" entry is written as bytes from blobs.
func tree(t *testing.T, files map[string]string, blobs map[string][]byte) Roots {
	t.Helper()
	d := t.TempDir()
	for p, v := range files {
		full := filepath.Join(d, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for p, v := range blobs {
		full := filepath.Join(d, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, v, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Roots{DRM: d}
}

// thisMachine is the shape of this machine: one connected DP and three dark connectors, with the
// non-connector entries (card1, renderD128, version) sysfs really has alongside them.
func thisMachine(t *testing.T) Roots {
	t.Helper()
	f := map[string]string{
		"version":                "drm 1.1.0 20060810\n",
		"card1/dev":              "226:1\n",
		"card1-DP-1/status":      "connected\n",
		"card1-DP-1/enabled":     "enabled\n",
		"card1-DP-1/dpms":        "On\n",
		"card1-DP-1/modes":       "3840x2160\n3840x2160\n3840x2160\n2560x1440\n1920x1080\n1920x1080\n",
		"card1-DP-2/status":      "disconnected\n",
		"card1-DP-2/enabled":     "disabled\n",
		"card1-DP-2/modes":       "",
		"card1-HDMI-A-1/status":  "disconnected\n",
		"card1-HDMI-A-1/enabled": "disabled\n",
		"renderD128/dev":         "226:128\n",
	}
	return tree(t, f, map[string][]byte{"card1-DP-1/edid": realPanel(), "card1-DP-2/edid": {}})
}

func out(t *testing.T, st State, name string) Output {
	t.Helper()
	for _, o := range st.Outputs {
		if o.Name == name {
			return o
		}
	}
	t.Fatalf("no output %q in %+v", name, st.Outputs)
	return Output{}
}

func TestTheConnectedPanelIsReadFromItsEDID(t *testing.T) {
	st := Read(thisMachine(t))
	if !st.Available {
		t.Fatalf("got %+v", st)
	}
	o := out(t, st, "DP-1")
	if o.Connector != "card1-DP-1" || o.Status != "connected" || !o.Enabled || o.DPMS != "On" {
		t.Fatalf("got %+v", o)
	}
	if o.Native == nil || o.Native.Width != 3840 || o.Native.Height != 2160 {
		t.Fatalf("native: %+v", o.Native)
	}
	// 53325 * 10 kHz over 4000 x 2222 is 59.9966 Hz. Milli-hertz, because 59.997 and 60.000
	// are different modes and integer Hz reports them as the same number.
	if o.Native.RefreshMHz != 59997 {
		t.Fatalf("refresh %d mHz, want 59997", o.Native.RefreshMHz)
	}
	// The millimetre size comes from the detailed timing descriptor (699x395), not the base
	// block's centimetres (70x40 -> 700x400), because it is the finer of the two.
	if o.PhysicalMM == nil || *o.PhysicalMM != [2]int{699, 395} {
		t.Fatalf("physical_mm: %+v", o.PhysicalMM)
	}
	if o.SuggestedScale != 1.5 {
		t.Fatalf("suggested_scale %v, want 1.5", o.SuggestedScale)
	}
	if o.Make != "ZZZ" || o.Model != "TESTPANEL" {
		t.Fatalf("make/model %q/%q", o.Make, o.Model)
	}
}

func TestTheSerialNumberIsNeverReturned(t *testing.T) {
	// The EDID carries one and it is parsed past. It is a stable per-device identifier and
	// nothing a settings page or an agent does with display state needs it; a verb that
	// handed it out would make every caller a fingerprinting tool.
	b, err := json.Marshal(Read(thisMachine(t)))
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range []string{"3735928559", "DEADBEEF", "deadbeef", "0xdeadbeef"} {
		if strings.Contains(string(b), form) {
			t.Fatalf("the EDID serial leaked as %q: %s", form, b)
		}
	}
}

func TestDuplicateModeLinesAreCollapsedAndCounted(t *testing.T) {
	// sysfs repeats a resolution once per refresh rate without saying which rate each line is,
	// so the repeats carry nothing the first does not. Eleven identical strings is noise; the
	// count of what was collapsed is not.
	o := out(t, Read(thisMachine(t)), "DP-1")
	want := []string{"3840x2160", "2560x1440", "1920x1080"}
	if len(o.Modes) != len(want) {
		t.Fatalf("modes %v, want %v", o.Modes, want)
	}
	for i := range want {
		if o.Modes[i] != want[i] {
			t.Fatalf("modes %v, want %v (preferred first)", o.Modes, want)
		}
	}
	if o.ModeLines != 6 {
		t.Fatalf("mode_lines %d, want 6", o.ModeLines)
	}
}

func TestADarkConnectorIsReportedWithReasonsAndNoInventedMode(t *testing.T) {
	st := Read(thisMachine(t))
	o := out(t, st, "DP-2")
	if o.Status != "disconnected" || o.Enabled {
		t.Fatalf("got %+v", o)
	}
	// A zero-length edid file must not parse into a 0x0 mode at 0 Hz.
	if o.Native != nil || o.PhysicalMM != nil || o.SuggestedScale != 0 {
		t.Fatalf("a disconnected connector reported a mode: %+v", o)
	}
	for _, k := range []string{"native", "physical_mm", "suggested_scale"} {
		if o.Unavailable[k] == "" {
			t.Fatalf("%s is missing with no reason given: %+v", k, o.Unavailable)
		}
	}
	// A connector with no `dpms` file at all reports none, rather than a guess.
	if h := out(t, st, "HDMI-A-1"); h.DPMS != "" {
		t.Fatalf("dpms %q invented for a connector with no dpms file", h.DPMS)
	}
}

func TestTheCompositorsFiguresAreNamedAsMissingOnEveryOutput(t *testing.T) {
	// This is the whole point of the Unavailable map. A caller that read modes[0] as "the
	// current resolution" would be wrong on this machine, where the panel offers twelve and
	// sysfs does not say which is set.
	for _, o := range Read(thisMachine(t)).Outputs {
		for _, k := range []string{"current_mode", "current_refresh", "scale", "primary"} {
			if o.Unavailable[k] == "" {
				t.Fatalf("%s: %s is not declared unavailable: %+v", o.Name, k, o.Unavailable)
			}
		}
	}
}

func TestNonConnectorEntriesAreNotOutputs(t *testing.T) {
	// card1, renderD128 and version live in the same directory. The `status` file is what
	// makes a connector, and "card1" has no dash and so is not even a candidate.
	for _, o := range Read(thisMachine(t)).Outputs {
		switch o.Name {
		case "DP-1", "DP-2", "HDMI-A-1":
		default:
			t.Fatalf("%q (from %q) is not a connector", o.Name, o.Connector)
		}
	}
	if n := len(Read(thisMachine(t)).Outputs); n != 3 {
		t.Fatalf("%d outputs, want 3", n)
	}
}

func TestGarbageEDIDIsIgnoredRatherThanParsed(t *testing.T) {
	// A VALID block with a corrupted header, not a block of zeros: zeros parse into nothing
	// whether the header is checked or not, so they do not test the check. These bytes would
	// otherwise yield a 3840x2160 display -- which is what a connector with no monitor would
	// then be reported as, on whatever happened to be left in the buffer.
	bad := realPanel()
	bad[1] = 0x00
	r := tree(t, map[string]string{"card1-DP-1/status": "connected\n"}, map[string][]byte{"card1-DP-1/edid": bad})
	o := out(t, Read(r), "DP-1")
	if o.Native != nil || o.Make != "" || o.Model != "" {
		t.Fatalf("a block with a bad header became a display: %+v", o)
	}
	// And a block of zeros, which is what some drivers return for an empty connector.
	rz := tree(t, map[string]string{"card1-DP-1/status": "connected\n"}, map[string][]byte{"card1-DP-1/edid": make([]byte, 128)})
	if oz := out(t, Read(rz), "DP-1"); oz.Native != nil || oz.Make != "" {
		t.Fatalf("a zero block became a display: %+v", oz)
	}
	// And a block with a good header but a zero pixel clock yields no mode, not 0 Hz.
	z := edid("ZZZ", 1, 2, 70, 40, &dtd{clock10kHz: 0, hActive: 3840, vActive: 2160}, "X")
	r2 := tree(t, map[string]string{"card1-DP-1/status": "connected\n"}, map[string][]byte{"card1-DP-1/edid": z})
	if o2 := out(t, Read(r2), "DP-1"); o2.Native != nil {
		t.Fatalf("a zero pixel clock became a mode: %+v", o2.Native)
	}
}

func TestNoDRMAtAllSaysSoRatherThanReportingNoDisplays(t *testing.T) {
	// A container has no /sys/class/drm. "outputs: []" with available true would be a claim
	// that the machine has no displays, which is a different fact.
	st := Read(Roots{DRM: filepath.Join(t.TempDir(), "absent")})
	if st.Available || st.Reason == "" {
		t.Fatalf("got %+v", st)
	}
	if st.Outputs == nil {
		t.Fatal("outputs must marshal as [] rather than null")
	}
}

func TestAutoScaleMatchesTheCompositor(t *testing.T) {
	// The cases are compositor/src/drm.rs's own test, scale_follows_pixel_density, plus this
	// machine's display. This is the test that holds the Go copy of auto_scale to the Rust
	// original; if drm.rs changes its rule, this is what should fail.
	for _, c := range []struct {
		px, mm int
		want   float64
		why    string
	}{
		{1920, 0, 1.0, "unknown size (VM) is 1x"},
		{1920, 509, 1.0, "24in 1080p, ~96 dpi"},
		{3840, 597, 1.75, "27in 4K, ~163 dpi"},
		{3840, 699, 1.5, "this machine's 32in 4K, ~139 dpi"},
		// The case the 1.25 floor exists for, and the only kind that tests it: ~110 dpi
		// snaps to 1.25 on the quarter grid, and the floor sends it back to 1.0. A 27in
		// 1440p monitor is exactly here, and scaling it 1.25x would be wrong.
		{2560, 589, 1.0, "27in 1440p, ~110 dpi: under the 1.25 floor"},
		{-1, 100, 1.0, "nonsense is 1x, not a negative scale"},
		{3840, 1, 3.0, "a bogus EDID size is clamped to 3, not 97,536"},
	} {
		if got := AutoScale(c.px, c.mm); got != c.want {
			t.Errorf("AutoScale(%d, %d) = %v, want %v (%s)", c.px, c.mm, got, c.want, c.why)
		}
	}
}
