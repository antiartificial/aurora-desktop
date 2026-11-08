// Package sysaudio reads and sets the default sink's volume and mute state, and lists the sinks.
//
// Volume is the one system figure that is not in sysfs. Network and battery can be read from
// /sys by whoever wants them; the audio graph lives in PipeWire or PulseAudio and is reached
// through a client library or a tool. That is why this is a controller verb and not something
// the compositor does for itself: the compositor draws the trusted path and should not be
// linking an audio client or spawning processes on its frame path.
//
// It is also an EFFECT. Reading the volume is harmless, but setting it -- or muting -- changes the
// machine for every application at once, so `system.audio.set@v1` and `system.audio.mute@v1` are
// refused to callers inside a domain by the same rule that refuses them registry.launch@v1. The
// declaration of that rule, for every `system.*` verb, is the table in
// controller/internal/service/system.go.
//
// The tool is injectable so the parsing can be tested without an audio server, which is still
// worth having: every CI machine is one. The note that used to stand here said nobody had watched
// these commands answer for real, because the machine this was first written on had 32-bit i386
// SPA plugins in /usr/lib/spa-0.2 and no /usr/lib64/spa-0.2, so no 64-bit audio server could run.
// That is no longer true. Get, Set, Mute and Sinks have all been run against PipeWire 1.6.9 on a
// real machine, over a real bus, through controllerd: the volume read back 40, a mute round-tripped
// to true and back to false, and five sinks came back with the default marked. What the code does
// on a machine with no audio server is still only tested, not watched; that is what Available is
// for.
package sysaudio

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Runner runs a command and returns its combined output. Replaced in tests.
type Runner func(name string, args ...string) (string, error)

// Exec is the real one.
func Exec(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

type State struct {
	// Volume 0-100. Meaningless unless Available.
	Volume int  `json:"volume"`
	Muted  bool `json:"muted"`
	// False when no audio server could be reached. The reason is for a person, not a parser.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// parseWpctl reads `wpctl get-volume @DEFAULT_AUDIO_SINK@` output.
//
// It prints "Volume: 0.45", with " [MUTED]" appended when muted. A volume may exceed 1.0:
// PipeWire allows over-amplification, and reporting 150% as 150% is more honest than clamping
// it to a full bar that cannot explain why it is loud.
func parseWpctl(s string) (State, bool) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "Volume:")
		if !ok {
			continue
		}
		muted := strings.Contains(rest, "[MUTED]")
		rest = strings.TrimSpace(strings.ReplaceAll(rest, "[MUTED]", ""))
		f, err := strconv.ParseFloat(strings.Fields(rest + " ")[0], 64)
		if err != nil {
			return State{}, false
		}
		return State{Volume: int(f*100 + 0.5), Muted: muted, Available: true}, true
	}
	return State{}, false
}

// parsePactl reads `pactl get-sink-volume @DEFAULT_SINK@`, whose first line carries several
// percentages (per channel). The first is taken: a desktop readout showing one number for a
// stereo sink is right, and the channels are equal in every case a volume control produces.
func parsePactl(s string) (State, bool) {
	for _, line := range strings.Split(s, "\n") {
		i := strings.Index(line, "/")
		if !strings.Contains(line, "Volume:") || i < 0 {
			continue
		}
		for _, f := range strings.Fields(line[i:]) {
			if v, ok := strings.CutSuffix(f, "%"); ok {
				n, err := strconv.Atoi(strings.TrimSpace(v))
				if err != nil {
					continue
				}
				return State{Volume: n, Available: true}, true
			}
		}
	}
	return State{}, false
}

// Get asks whichever tool answers. wpctl first: PipeWire is what a 2026 desktop runs, and on a
// machine with the pipewire-pulse shim both work and wpctl is the one talking to the real graph.
func Get(run Runner) State {
	if out, err := run("wpctl", "get-volume", "@DEFAULT_AUDIO_SINK@"); err == nil {
		if st, ok := parseWpctl(out); ok {
			return st
		}
	}
	if out, err := run("pactl", "get-sink-volume", "@DEFAULT_SINK@"); err == nil {
		if st, ok := parsePactl(out); ok {
			// pactl reports mute separately; one more call rather than a wrong answer.
			if m, err := run("pactl", "get-sink-mute", "@DEFAULT_SINK@"); err == nil {
				st.Muted = strings.Contains(m, "yes")
			}
			return st
		}
	}
	return State{Reason: "no audio server answered (tried wpctl, then pactl)"}
}

// Set moves the default sink to `vol` percent, clamped to 0-150.
//
// The ceiling is 150 rather than 100 because PipeWire permits over-amplification and refusing it
// here would make the verb unable to express a state the system can already be in. It is not
// 1000: a slip that made every sound ten times louder is not a mistake a volume control should
// be able to make.
func Set(run Runner, vol int) error {
	if vol < 0 {
		vol = 0
	}
	if vol > 150 {
		vol = 150
	}
	pct := fmt.Sprintf("%d%%", vol)
	if _, err := run("wpctl", "set-volume", "@DEFAULT_AUDIO_SINK@", pct); err == nil {
		return nil
	}
	if _, err := run("pactl", "set-sink-volume", "@DEFAULT_SINK@", pct); err == nil {
		return nil
	}
	return fmt.Errorf("no audio server answered (tried wpctl, then pactl)")
}

// ---- mute -------------------------------------------------------------------

// Mute sets the default sink's mute flag. `on` is the state wanted, not a toggle: a verb that
// toggled would make two callers racing each other produce whichever answer arrived last, and a
// keyboard key that is pressed twice would be indistinguishable from one that was pressed once
// and lost. A caller that wants a toggle reads `Get` and asks for the opposite.
//
// The same risk class as Set and refused in the same place: it silences or unsilences the machine
// for every application at once.
func Mute(run Runner, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	if _, err := run("wpctl", "set-mute", "@DEFAULT_AUDIO_SINK@", v); err == nil {
		return nil
	}
	if _, err := run("pactl", "set-sink-mute", "@DEFAULT_SINK@", v); err == nil {
		return nil
	}
	return fmt.Errorf("no audio server answered (tried wpctl, then pactl)")
}

// ---- sinks ------------------------------------------------------------------

// Sink is one output device as the audio server reports it.
type Sink struct {
	// Name is the server's stable id ("alsa_output.pci-0000_11_00.6.analog-stereo"), which is
	// what a future `system.audio.default.set@v1` would have to name. Description is the
	// human one.
	Name        string `json:"name"`
	Description string `json:"description"`
	// Volume 0-150 and Muted, per sink. Default marks the one the volume verbs act on.
	Volume  int  `json:"volume"`
	Muted   bool `json:"muted"`
	Default bool `json:"default"`
}

// SinkList is the reply of system.audio.sinks@v1. Same Available/Reason contract as State: a
// machine with no audio server reports an empty list and says why, rather than "no sinks",
// which is a claim about the hardware.
type SinkList struct {
	Sinks     []Sink `json:"sinks"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// pactlSink is the subset of `pactl -f json list sinks` this needs. Unknown fields are ignored,
// which is the point of using the JSON form: the human form's field order and indentation are
// presentation and have changed between releases.
type pactlSink struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Mute        bool   `json:"mute"`
	Volume      map[string]struct {
		Percent string `json:"value_percent"`
	} `json:"volume"`
}

// parseSinks reads `pactl -f json list sinks` and marks `def` as the default.
//
// Per-sink volume is the loudest channel, not the first: the channel map keys are "front-left"
// on an analog stereo sink and "aux0".."aux7" on an HDMI one, so "the first" depends on Go's map
// iteration order and would report a different number on each call. The loudest is stable, and
// for the balanced case every control produces it is the same number.
func parseSinks(s, def string) (SinkList, bool) {
	var raw []pactlSink
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return SinkList{}, false
	}
	out := SinkList{Sinks: []Sink{}, Available: true}
	for _, r := range raw {
		sk := Sink{Name: r.Name, Description: r.Description, Muted: r.Mute, Default: r.Name == def && def != ""}
		for _, ch := range r.Volume {
			if n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(ch.Percent), "%")); err == nil && n > sk.Volume {
				sk.Volume = n
			}
		}
		out.Sinks = append(out.Sinks, sk)
	}
	return out, true
}

// Sinks lists the output devices.
//
// This one is pactl-only, and the reason is worth stating rather than hiding behind a fallback
// that returns something: wpctl has no machine-readable listing. `wpctl status` prints a tree
// drawn with box characters, with Sinks, Sources, Filters and Streams in one indented block and
// the default marked by a `*` in a column; a parser for it would be guessing at a layout that is
// presentation and has no stability promise. `pactl -f json` is a declared output format. On a
// PipeWire machine it is served by the pipewire-pulse shim, which is what a desktop runs; on one
// without it, this verb says it cannot answer instead of inventing a list.
func Sinks(run Runner) SinkList {
	out, err := run("pactl", "-f", "json", "list", "sinks")
	if err != nil {
		return SinkList{Sinks: []Sink{}, Reason: "sink enumeration needs pactl -f json (pipewire-pulse or pulseaudio); wpctl has no machine-readable listing"}
	}
	// The default's name, if it can be had. A list with nothing marked default is still a true
	// list, so this failing is not fatal.
	def, _ := run("pactl", "get-default-sink")
	l, ok := parseSinks(out, strings.TrimSpace(def))
	if !ok {
		return SinkList{Sinks: []Sink{}, Reason: "pactl answered, but not with JSON this understands"}
	}
	return l
}
