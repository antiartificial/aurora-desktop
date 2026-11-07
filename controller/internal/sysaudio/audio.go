// Package sysaudio reads and sets the default sink's volume.
//
// Volume is the one system figure that is not in sysfs. Network and battery can be read from
// /sys by whoever wants them; the audio graph lives in PipeWire or PulseAudio and is reached
// through a client library or a tool. That is why this is a controller verb and not something
// the compositor does for itself: the compositor draws the trusted path and should not be
// linking an audio client or spawning processes on its frame path.
//
// It is also an EFFECT. Reading the volume is harmless, but setting it changes the machine for
// every application at once, so `system.audio.set@v1` is refused to callers inside a domain by
// the same rule that refuses them registry.launch@v1.
//
// The tool is injectable so the parsing can be tested without an audio server. That matters here
// more than usual, and the reason is worth writing down rather than leaving as "it did not work":
// the machine this was written on has /usr/lib/spa-0.2 containing 32-bit i386 plugins and no
// /usr/lib64/spa-0.2 at all. The 64-bit SPA package is simply not installed, so a 64-bit wpctl
// cannot load `support.system` and no audio server can run. That is a missing package, not a
// sandbox and not a configuration mistake, and it is why nobody has watched these commands
// answer for real.
package sysaudio

import (
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
