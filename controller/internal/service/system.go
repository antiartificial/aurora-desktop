package service

import (
	"fmt"
	"sort"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/sysaudio"
	"github.com/antiartificial/aurora/controller/internal/sysdisplay"
	"github.com/antiartificial/aurora/controller/internal/sysinput"
	"github.com/antiartificial/aurora/controller/internal/sysnet"
)

// systemVerb declares one `system.*` verb: its effect class and who may call it.
//
// # WHY A TABLE AND NOT MORE `case` ARMS
//
// The two audio verbs that shipped first put the caller rule inline, as an `if !mayDecide` at the
// top of the arm that needs it. With two verbs that is readable. With nine it is a policy spread
// across nine places, where the mistake is silent: an arm that forgets the check is a verb open
// to every domain and nothing says so. Here the rule is data, applied once in `systemDispatch`
// before any arm runs, and the same data answers `system.verbs@v1` -- so what a caller is told
// about a verb and what is enforced for it come from one line.
//
// These verbs are also NOT in the registry. `registry.verbs@v1` walks app manifests, and a system
// verb has no manifest and no app, so until now `system.audio@v1` had no declared effect anywhere
// in the system: a thing you could call, with a risk class that existed only as a comment. That
// is the half of "a thing you can do should be a verb" that was still missing, and the table plus
// `system.verbs@v1` is what closes it.
type systemVerb struct {
	Name   string
	Effect manifest.Effect
	// Summary is one line, as a manifest verb's is.
	Summary string
	// SessionOnly refuses any caller that is not the session socket. A domain connects as
	// "domain:<name>:<hex>" and is refused; see mayDecide for the limit of that test.
	SessionOnly bool
	// Why says, in the refusal and in the catalogue, what the caller may not do and why. It is
	// written for a person reading a denial, not for a parser.
	Why string
}

// systemVerbs is every `system.*` verb, with its effect class and caller rule.
//
// # THE ACCESS RULE, STATED ONCE
//
// A confined app gets system state through the protocol that already serves it, not through a bus
// verb that reports the machine. Its output size, scale and refresh arrive on `wl_output`; its
// keymap and repeat rate arrive on `wl_keyboard`; its pointer behaviour is the compositor's to
// apply and not its to read. Those channels give an app what concerns its own window. A
// `system.*` read gives it the machine: every connector including the dark ones, the monitor's
// make and model, every interface and address.
//
// So the rule is neither "everything" nor "nothing". It is: the only system verb a domain may
// call is the one whose answer is a single user-visible figure that is not an identifier --
// `system.audio@v1`, volume and mute, which shipped open and stays open because an in-window
// volume readout is a thing a person might reasonably want. Everything else is session-only, and
// each line below says what the refused caller was after.
//
// `system.network@v1` is the strongest case and the one to notice. Decision 0015 is built so that
// a domain cannot see the network: no resolv.conf, no route, `lo` and nothing else in
// /proc/net/dev. Handing it the host's interfaces, link states and addresses would return that
// whole picture by another door, and would do it without appearing in the `net.` events 0015 added
// so a person can see what is connected to what.
var systemVerbs = []systemVerb{
	{"system.verbs@v1", manifest.EffectRead,
		"list the system verbs with their effect class and caller rule", false,
		"", // open: a catalogue of verb names carries no state about the machine.
	},
	{"system.audio@v1", manifest.EffectRead,
		"the default sink's volume and mute state", false, ""},
	{"system.audio.set@v1", manifest.EffectWrite,
		"set the default sink's volume, 0-150 percent", true,
		"change the machine's volume"},
	{"system.audio.mute@v1", manifest.EffectWrite,
		"mute or unmute the default sink", true,
		"silence the machine for every application at once"},
	{"system.audio.sinks@v1", manifest.EffectRead,
		"list the output devices with their volume, mute state and which is default", true,
		"enumerate the machine's audio hardware: sink descriptions name the devices (\"Turtle Beach Stealth 700 G2\") and are a fingerprint, where the default sink's volume alone is not"},
	{"system.display@v1", manifest.EffectRead,
		"the display connectors: modes, the EDID's preferred mode and millimetre size, and the scale that implies", true,
		"enumerate the machine's displays; an app learns its own output's size and scale from wl_output, which is about its window, where this is about the hardware and names the monitor's make and model"},
	{"system.pointer@v1", manifest.EffectRead,
		"the configured pointer settings: acceleration, natural scroll, tap to click, left handed", true,
		"read the user's input configuration, which the compositor applies and an app has no part in"},
	{"system.keyboard@v1", manifest.EffectRead,
		"the configured keyboard settings: xkb layout, variant, options, repeat delay and rate", true,
		"read the user's keyboard configuration; an app gets its keymap on wl_keyboard, and the layout name is a locale fingerprint it was not given"},
	{"system.network@v1", manifest.EffectRead,
		"the hardware-backed network interfaces: link state, speed and addresses (read only; see decision 0015)", true,
		"see the machine's network, which decision 0015 is built to keep from a domain: inside one there is no resolv.conf, no route and no interface but lo"},
}

// systemVerbByName finds a verb in the table.
func systemVerbByName(name string) (systemVerb, bool) {
	for _, v := range systemVerbs {
		if v.Name == name {
			return v, true
		}
	}
	return systemVerb{}, false
}

// systemDispatch answers one system.* request, or reports that it is not one.
//
// The caller rule is applied here, once, from the table, before any arm runs.
func (s *Service) systemDispatch(req proto.Envelope) (out any, e *invoke.Error, handled bool) {
	v, ok := systemVerbByName(req.Verb)
	if !ok {
		return nil, nil, false
	}
	if v.SessionOnly && !mayDecide(req.From) {
		return nil, &invoke.Error{
			Code: "not_permitted",
			Msg:  v.Name + " may only be called from the session socket",
			// The same two-part shape the audio arm used: what the rule is, then which
			// caller tripped it and what it was after.
			Detail: fmt.Sprintf("caller %q may not %s", req.From, v.Why),
		}, true
	}
	switch req.Verb {
	case "system.verbs@v1":
		return s.listSystemVerbs(), nil, true
	case "system.audio@v1":
		return sysaudio.Get(s.audio), nil, true
	case "system.audio.sinks@v1":
		return sysaudio.Sinks(s.audio), nil, true
	case "system.audio.set@v1":
		var q struct {
			Volume int `json:"volume"`
		}
		if e := decode(req, &q); e != nil {
			return nil, e, true
		}
		if err := sysaudio.Set(s.audio, q.Volume); err != nil {
			return nil, &invoke.Error{Code: "unavailable", Msg: "the volume could not be set", Detail: err.Error()}, true
		}
		// The NEW state, not an empty ack: a volume control that has to ask again to find
		// out what it did would show the old number for one frame.
		return sysaudio.Get(s.audio), nil, true
	case "system.audio.mute@v1":
		// `muted` is required and has no default. A missing field would decode to false,
		// which is "unmute" -- so an empty payload would silently unmute the machine, and
		// that is not a thing a caller should be able to do by accident.
		var q struct {
			Muted *bool `json:"muted"`
		}
		if e := decode(req, &q); e != nil {
			return nil, e, true
		}
		if q.Muted == nil {
			return nil, &invoke.Error{Code: "bad_request",
				Msg:    "system.audio.mute@v1 needs {\"muted\": true|false}",
				Detail: "the state wanted, not a toggle: two callers toggling race each other, and a mute key pressed twice is indistinguishable from one press that was lost. Read system.audio@v1 and ask for the opposite"}, true
		}
		if err := sysaudio.Mute(s.audio, *q.Muted); err != nil {
			return nil, &invoke.Error{Code: "unavailable", Msg: "the mute state could not be set", Detail: err.Error()}, true
		}
		return sysaudio.Get(s.audio), nil, true
	case "system.display@v1":
		return sysdisplay.Read(s.displayRoots()), nil, true
	case "system.network@v1":
		return sysnet.Read(s.netRoots(), s.netLister()), nil, true
	case "system.pointer@v1":
		return sysinput.ReadPointer(s.ThemeDir), nil, true
	case "system.keyboard@v1":
		return sysinput.ReadKeyboard(s.ThemeDir), nil, true
	}
	// A name in the table with no arm. Reachable only by editing the table and forgetting the
	// arm, which is a build-time mistake this reports rather than answering null.
	return nil, &invoke.Error{Code: "no_such_verb", Msg: "registry declares " + req.Verb + " but serves no implementation for it"}, true
}

// systemVerbInfo is one row of system.verbs@v1. Deliberately not verbInfo: that one carries an
// `app` and an `accepts` list, and a system verb has neither. A reader that saw `"app": ""` would
// reasonably wonder which app that was.
type systemVerbInfo struct {
	Name    string          `json:"name"`
	Effect  manifest.Effect `json:"effect"`
	Summary string          `json:"summary"`
	// SessionOnly and ConfinedReason are the caller rule, published so a client can tell
	// before it calls, and so the policy is inspectable rather than only enforced.
	SessionOnly    bool   `json:"session_only"`
	ConfinedReason string `json:"confined_reason,omitempty"`
}

// listSystemVerbs answers system.verbs@v1.
//
// It is a separate verb rather than extra rows in registry.verbs@v1 on purpose: that verb's
// contract is "the verbs app manifests declare", every row has an app, and the shell and the
// gateway both read it that way. Folding appless rows in would change an answer two components
// already depend on, to save a verb name.
func (s *Service) listSystemVerbs() any {
	list := make([]systemVerbInfo, 0, len(systemVerbs))
	for _, v := range systemVerbs {
		list = append(list, systemVerbInfo{v.Name, v.Effect, v.Summary, v.SessionOnly, v.Why})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return map[string]any{"verbs": list}
}

// displayRoots is DisplayRoots, or the real /sys/class/drm.
func (s *Service) displayRoots() sysdisplay.Roots {
	if s.DisplayRoots.DRM != "" {
		return s.DisplayRoots
	}
	return sysdisplay.Real()
}

// netRoots is NetRoots, or the real /sys/class/net.
func (s *Service) netRoots() sysnet.Roots {
	if s.NetRoots.Net != "" {
		return s.NetRoots
	}
	return sysnet.Real()
}

// netLister is NetLister, or the real netlink call.
func (s *Service) netLister() sysnet.Lister {
	if s.NetLister != nil {
		return s.NetLister
	}
	return sysnet.RealLister
}
