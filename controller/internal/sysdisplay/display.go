// Package sysdisplay reads the machine's display connectors from sysfs and their EDID.
//
// WHY THE CONTROLLER READS /sys RATHER THAN ASKING THE COMPOSITOR
//
// The compositor knows more than this package does: it holds the mode it actually set, the scale
// it actually applied and which output it treats as the one the dock sits on. Asking it would be
// the better answer to "what is on screen right now". It is not asked, for three reasons, and
// the third is the one that decides it:
//
//  1. There is no channel. The compositor serves no bus verb; it is a bus *client* (it calls
//     registry.apps@v1, system.audio@v1, bus.log@v1). Its only server socket is the trusted-path
//     socket, which carries signed approval prompts and nothing else. Adding a query to it is
//     compositor work and a protocol change, not a controller change.
//  2. The compositor need not be running. `system.display@v1` is a question about the machine's
//     displays, and a desktop that has not started yet, or has crashed, still has them. A verb
//     that answers only while the thing it describes is up is a verb a diagnostic cannot use.
//  3. What sysfs and the EDID give is the machine's own account of the hardware, which is the
//     authority the compositor itself defers to: `compositor/src/drm.rs` picks the connector's
//     PREFERRED mode and derives scale from the EDID's millimetre size. Reading the same two
//     facts here produces the same numbers from the same source, rather than a copy of the
//     compositor's state that can be stale.
//
// What this costs, stated rather than papered over: sysfs does not carry the mode that is
// CURRENTLY set, only the list the connector offers, so "current resolution", "current refresh"
// and "which output is primary" are reported as unavailable with the reason. They are the
// compositor's, and when it serves a verb of its own they should come from there.
//
// It deliberately drops one field it reads. The EDID carries a serial number, which is a stable
// per-device identifier and the single most useful thing in the block for recognising a machine
// again. Nothing a settings page or an agent does with display state needs it, so it is parsed
// past and never returned.
package sysdisplay

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Roots is where to read. Replaced in tests; Real() is the machine.
type Roots struct {
	// DRM is /sys/class/drm.
	DRM string
}

func Real() Roots { return Roots{DRM: "/sys/class/drm"} }

// Mode is one timing.
type Mode struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	// RefreshMHz is milli-hertz, as the compositor's output mode carries it (59950 is 59.95 Hz).
	// Integer Hz would report 59.95 and 60.00 as the same number, and on this machine's display
	// those are different modes.
	RefreshMHz int `json:"refresh_mhz"`
}

// Output is one display connector.
type Output struct {
	// Name is the connector as everything else names it ("DP-1"); Connector is the sysfs
	// directory it came from ("card1-DP-1"), because a machine with two GPUs can have two
	// connectors called DP-1 and the short name alone would not say which.
	Name      string `json:"name"`
	Connector string `json:"connector"`
	// Status is the kernel's word: "connected", "disconnected" or "unknown". Not a guess from
	// whether a mode list came back.
	Status  string `json:"status"`
	Enabled bool   `json:"enabled"`
	DPMS    string `json:"dpms,omitempty"`
	// Modes the connector offers, distinct, in the order sysfs lists them (preferred first).
	// Strings, not Modes, because the sysfs file carries no refresh rate; and DISTINCT because
	// it repeats a resolution once per rate without saying which rate each line is. This
	// machine's display produces "3840x2160" eleven times, and ten of those lines carry no
	// information the first does not. `ModeLines` keeps the count that was collapsed.
	Modes     []string `json:"modes"`
	ModeLines int      `json:"mode_lines,omitempty"`
	// Native is the EDID's first detailed timing descriptor: the preferred mode, with its
	// refresh computed from the pixel clock and the blanking. This is what the kernel marks
	// PREFERRED and what drm.rs picks.
	Native *Mode `json:"native,omitempty"`
	// PhysicalMM is width and height in millimetres, from the EDID.
	PhysicalMM *[2]int `json:"physical_mm,omitempty"`
	// SuggestedScale is what compositor/src/drm.rs auto_scale would decide from Native and
	// PhysicalMM. It is NOT the scale in force: the compositor's `--scale` and the theme's
	// [output] scale both override it, and neither is readable here.
	SuggestedScale float64 `json:"suggested_scale,omitempty"`
	Make           string  `json:"make,omitempty"`
	Model          string  `json:"model,omitempty"`
	// Unavailable names each figure this verb cannot answer and why, instead of a plausible
	// default. Keys are the field names a caller would have looked for.
	Unavailable map[string]string `json:"unavailable,omitempty"`
}

// State is the reply of system.display@v1.
type State struct {
	Outputs []Output `json:"outputs"`
	// Available is false when there is no DRM at all (a container, a machine with no KMS
	// driver). An empty Outputs list with Available true means the directory was there and
	// held no connectors, which is a different fact.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func readTrim(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// AutoScale is compositor/src/drm.rs auto_scale, in Go: pixel density over 96 dpi, snapped to a
// quarter, with anything under 1.25 left at 1.0 and a ceiling of 3.
//
// It is a second copy of a rule that already exists in Rust, which is the thing the theme-drift
// demo exists to warn about. It is copied anyway because the alternative is worse: this verb
// either reports the number the compositor would choose or it reports nothing, and "nothing" is
// the figure a settings page most wants. The copy is bounded (eight lines, no state) and
// TestAutoScaleMatchesTheCompositor holds it to the Rust test's own cases.
func AutoScale(px, mm int) float64 {
	if px <= 0 || mm <= 0 {
		return 1.0
	}
	dpi := float64(px) / (float64(mm) / 25.4)
	s := dpi / 96.0
	if s < 1.25 {
		return 1.0
	}
	// Round half away from zero, as Rust's f64::round does; Go's math.Round agrees, but the
	// arithmetic is written out so the snapping is visible.
	q := float64(int(s*4.0+0.5)) / 4.0
	if q > 3.0 {
		return 3.0
	}
	return q
}

// Read lists the connectors under r.DRM.
func Read(r Roots) State {
	ents, err := os.ReadDir(r.DRM)
	if err != nil {
		return State{Outputs: []Output{}, Reason: fmt.Sprintf("cannot read %s: %v (no DRM: a container, or a kernel with no KMS driver)", r.DRM, err)}
	}
	st := State{Outputs: []Output{}, Available: true}
	for _, e := range ents {
		n := e.Name()
		// A connector directory is "card<N>-<NAME>". card1, renderD128 and version are not
		// connectors; the `status` file is what distinguishes one, so it is required.
		short, ok := connectorName(n)
		if !ok {
			continue
		}
		dir := filepath.Join(r.DRM, n)
		status, ok := readTrim(filepath.Join(dir, "status"))
		if !ok {
			continue
		}
		o := Output{Name: short, Connector: n, Status: status, Modes: []string{}, Unavailable: map[string]string{}}
		if v, ok := readTrim(filepath.Join(dir, "enabled")); ok {
			o.Enabled = v == "enabled"
		}
		if v, ok := readTrim(filepath.Join(dir, "dpms")); ok {
			o.DPMS = v
		}
		if v, ok := readTrim(filepath.Join(dir, "modes")); ok && v != "" {
			lines := strings.Split(v, "\n")
			o.ModeLines = len(lines)
			seen := map[string]bool{}
			for _, m := range lines {
				if m = strings.TrimSpace(m); m != "" && !seen[m] {
					seen[m], o.Modes = true, append(o.Modes, m)
				}
			}
		}
		// A sysfs binary attribute reports st_size 0, so the EDID has to be read to find out
		// whether there is one. os.ReadFile does the right thing; a size check does not.
		if b, err := os.ReadFile(filepath.Join(dir, "edid")); err == nil && len(b) > 0 {
			applyEDID(&o, b)
		}
		if o.Native == nil {
			o.Unavailable["native"] = "no EDID, or one this does not parse; the connector's mode list is still in `modes`"
		}
		if o.PhysicalMM == nil {
			o.Unavailable["physical_mm"] = "the EDID reports no millimetre size (common on VMs and some projectors)"
		}
		if o.SuggestedScale == 0 {
			o.Unavailable["suggested_scale"] = "needs the EDID's preferred mode and millimetre size"
		}
		// The three the compositor owns. Stated on every output, connected or not, because a
		// caller that reads `modes[0]` as "the current resolution" is the bug this prevents.
		o.Unavailable["current_mode"] = "sysfs carries the connector's mode LIST, not the mode that is set; the mode in force is the compositor's and it serves no verb yet"
		o.Unavailable["current_refresh"] = "same: `modes` has no refresh rate at all. `native.refresh_mhz` is the EDID's preferred rate, not necessarily the one running"
		o.Unavailable["scale"] = "scale is the compositor's, from --scale or the theme's [output] scale; `suggested_scale` is what it would derive from this EDID absent an override"
		o.Unavailable["primary"] = "there is no primary output in DRM or in Wayland; which display is primary is a compositor choice and Aurora drives one connector today"
		st.Outputs = append(st.Outputs, o)
	}
	sort.Slice(st.Outputs, func(i, j int) bool { return st.Outputs[i].Connector < st.Outputs[j].Connector })
	return st
}

// connectorName splits "card1-DP-1" into "DP-1". "card1", "renderD128" and "version" are not
// connectors and return false.
func connectorName(dir string) (string, bool) {
	if !strings.HasPrefix(dir, "card") {
		return "", false
	}
	_, rest, ok := strings.Cut(dir, "-")
	if !ok || rest == "" {
		return "", false
	}
	return rest, true
}

// applyEDID fills Make, Model, PhysicalMM, Native and SuggestedScale from an EDID base block.
//
// Only the 128-byte base block is read. CEA extension blocks carry more modes and are skipped:
// the preferred timing is in the base block by definition, and an extension's modes are already
// in the kernel's `modes` list.
func applyEDID(o *Output, b []byte) {
	if len(b) < 128 {
		return
	}
	// Header. A connector with no monitor returns zeros or a short read; without this check a
	// zero block would parse into a 0x0 mode and a manufacturer of "@@@".
	if string(b[0:8]) != "\x00\xff\xff\xff\xff\xff\xff\x00" {
		return
	}
	o.Make = manufacturer(b[8], b[9])
	if w, h := int(b[21]), int(b[22]); w > 0 && h > 0 {
		mm := [2]int{w * 10, h * 10}
		o.PhysicalMM = &mm
	}
	// The four 18-byte descriptors at 54. The first is the preferred timing; the others may
	// carry the monitor's name (tag 0xFC) and a more precise millimetre size.
	for i := 0; i < 4; i++ {
		d := b[54+i*18 : 54+i*18+18]
		// A descriptor whose first two bytes are zero is not a timing; byte 3 is then a tag.
		if d[0] == 0 && d[1] == 0 {
			if d[3] == 0xFC {
				o.Model = strings.TrimSpace(strings.ReplaceAll(string(d[5:18]), "\n", " "))
			}
			continue
		}
		if i != 0 {
			continue
		}
		o.Native, o.PhysicalMM = detailedTiming(d, o.PhysicalMM)
	}
	if o.Native != nil && o.PhysicalMM != nil {
		// auto_scale over the WIDTH only, which is what drm.rs add_output does. drm.rs probe
		// takes max(width, height) instead; the two disagree on a display whose EDID
		// millimetre size is anisotropic. add_output is the one that runs, so it is the one
		// copied here.
		o.SuggestedScale = AutoScale(o.Native.Width, o.PhysicalMM[0])
	}
}

// manufacturer decodes the EDID's two-byte PnP id: three 5-bit letters, big-endian, 1 = 'A'.
func manufacturer(hi, lo byte) string {
	v := binary.BigEndian.Uint16([]byte{hi, lo})
	out := make([]byte, 3)
	for i := 0; i < 3; i++ {
		c := byte(v>>(10-5*i)) & 0x1f
		if c < 1 || c > 26 {
			return ""
		}
		out[i] = 'A' + c - 1
	}
	return string(out)
}

// detailedTiming reads one 18-byte detailed timing descriptor: the active and blanking counts,
// split across low bytes and a shared high nibble, and the pixel clock in units of 10 kHz.
//
// Refresh is pixel clock over total pixels, which is the definition, not a lookup in a table of
// known modes. A descriptor whose clock or totals are zero yields no mode rather than a division
// by zero or a 0 Hz answer.
func detailedTiming(d []byte, mm *[2]int) (*Mode, *[2]int) {
	clock10kHz := int(binary.LittleEndian.Uint16(d[0:2]))
	hActive := int(d[2]) | int(d[4]>>4)<<8
	hBlank := int(d[3]) | int(d[4]&0x0f)<<8
	vActive := int(d[5]) | int(d[7]>>4)<<8
	vBlank := int(d[6]) | int(d[7]&0x0f)<<8
	hTotal, vTotal := hActive+hBlank, vActive+vBlank
	if clock10kHz == 0 || hActive == 0 || vActive == 0 || hTotal == 0 || vTotal == 0 {
		return nil, mm
	}
	// Hz = clock / (hTotal*vTotal); in mHz, with the rounding done once at the end so a
	// 59.95 Hz mode does not come back as 59.
	hz := float64(clock10kHz) * 10000.0 / float64(hTotal*vTotal)
	m := &Mode{Width: hActive, Height: vActive, RefreshMHz: int(hz*1000 + 0.5)}
	// Bytes 12-14 are the image size in millimetres, which is finer than the base block's
	// centimetres (700x400 rather than 70x40 cm). Taken when both are non-zero.
	if w, h := int(d[12])|int(d[14]>>4)<<8, int(d[13])|int(d[14]&0x0f)<<8; w > 0 && h > 0 {
		s := [2]int{w, h}
		return m, &s
	}
	return m, mm
}
