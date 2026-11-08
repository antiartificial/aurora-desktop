package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/sysdisplay"
	"github.com/antiartificial/aurora/controller/internal/sysnet"
)

// session is an identity mayDecide accepts; dom is what busd stamps on a domain's connection.
func session() string { return fmt.Sprintf("uid%d:test", os.Getuid()) }

const dom = "domain:app-org-aurora-x:00112233"

// sysSvc is a Service wired to fixtures for everything the system verbs read, so these tests
// exercise the dispatch and the caller rule rather than this machine's hardware. The packages
// have their own tests against real-shaped trees.
func sysSvc(t *testing.T) *Service {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "aurora-glass.toml"), []byte("[input]\nlayout = \"gb\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drm := t.TempDir()
	if err := os.MkdirAll(filepath.Join(drm, "card0-DP-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drm, "card0-DP-1", "status"), []byte("connected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Service{
		ThemeDir:     d,
		DisplayRoots: sysdisplay.Roots{DRM: drm},
		NetRoots:     sysnet.Roots{Net: t.TempDir()},
		NetLister:    func() ([]sysnet.Iface, error) { return nil, nil },
		Audio:        func(string, ...string) (string, error) { return "Volume: 0.40\n", nil },
	}
}

func call(t *testing.T, s *Service, from, verb, payload string) (any, *invoke.Error) {
	t.Helper()
	return s.dispatch(proto.Envelope{From: from, Verb: verb, Payload: json.RawMessage(payload)})
}

// TestEveryDeclaredSystemVerbIsServedAndEnforcedAsDeclared is the structural test, and the one
// that justifies the table existing at all.
//
// It walks `systemVerbs` and, for each, calls from the session socket and from a domain. A verb
// in the table with no arm answers no_such_verb from the session; a verb whose arm forgot the
// caller check answers a domain. Either is a defect the table was introduced to make impossible,
// and before it existed the check was an `if` inside each arm with nothing holding them to the
// declaration.
func TestEveryDeclaredSystemVerbIsServedAndEnforcedAsDeclared(t *testing.T) {
	s := sysSvc(t)
	if len(systemVerbs) < 9 {
		t.Fatalf("the table has shrunk to %d; this test is only as good as the table", len(systemVerbs))
	}
	for _, v := range systemVerbs {
		// A payload each write verb accepts, so a refusal here is the caller rule and not a
		// bad request.
		payload := "{}"
		switch v.Name {
		case "system.audio.mute@v1":
			payload = `{"muted":false}`
		case "system.audio.set@v1":
			payload = `{"volume":40}`
		}
		out, e := call(t, s, session(), v.Name, payload)
		if e != nil {
			t.Errorf("%s from the session socket: %s: %s", v.Name, e.Code, e.Msg)
		} else if out == nil {
			t.Errorf("%s answered null from the session socket", v.Name)
		}

		_, e = call(t, s, dom, v.Name, payload)
		switch {
		case v.SessionOnly && e == nil:
			t.Errorf("%s is declared session-only and answered a domain", v.Name)
		case v.SessionOnly && e.Code != "not_permitted":
			t.Errorf("%s refused a domain with %q, want not_permitted", v.Name, e.Code)
		case !v.SessionOnly && e != nil:
			t.Errorf("%s is declared open and refused a domain: %s", v.Name, e.Msg)
		}
		if v.SessionOnly {
			// The refusal has to say which caller and what it was after; a bare
			// "not_permitted" is not a thing a person can act on.
			if !strings.Contains(e.Detail, dom) || v.Why == "" || !strings.Contains(e.Detail, v.Why) {
				t.Errorf("%s refusal does not name the caller and the reason: %q", v.Name, e.Detail)
			}
		} else if v.Why != "" {
			t.Errorf("%s is open to everything, so its Why is never shown; it reads as a rule that is not enforced", v.Name)
		}
	}
}

func TestAudioReadIsTheOnlySystemVerbADomainMayCall(t *testing.T) {
	// The access rule, asserted rather than only written down. Volume and mute of the default
	// sink are one user-visible figure and not an identifier, and that verb shipped open. Every
	// other system verb reports the MACHINE -- its displays and their make and model, its audio
	// hardware, its interfaces and addresses, the user's input configuration -- and a confined
	// app gets what concerns its own window from Wayland instead.
	s := sysSvc(t)
	open := []string{}
	for _, v := range systemVerbs {
		if !v.SessionOnly {
			open = append(open, v.Name)
		}
	}
	want := []string{"system.verbs@v1", "system.audio@v1"}
	if len(open) != len(want) {
		t.Fatalf("verbs open to a domain: %v, want exactly %v", open, want)
	}
	for i := range want {
		if open[i] != want[i] {
			t.Fatalf("verbs open to a domain: %v, want exactly %v", open, want)
		}
	}
	// And it really answers one.
	out, e := call(t, s, dom, "system.audio@v1", "{}")
	if e != nil {
		t.Fatalf("a domain was refused the volume: %s", e.Msg)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"volume":40`) {
		t.Fatalf("got %s", b)
	}
}

func TestSystemVerbsPublishesTheRuleThatIsEnforced(t *testing.T) {
	// One source for what a caller is told and what is applied. A catalogue that disagreed
	// with the enforcement would be worse than no catalogue: a client would plan around it.
	s := sysSvc(t)
	out, e := call(t, s, dom, "system.verbs@v1", "{}")
	if e != nil {
		t.Fatal(e.Msg)
	}
	b, _ := json.Marshal(out)
	var rep struct {
		Verbs []struct {
			Name           string          `json:"name"`
			Effect         manifest.Effect `json:"effect"`
			Summary        string          `json:"summary"`
			SessionOnly    bool            `json:"session_only"`
			ConfinedReason string          `json:"confined_reason"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Verbs) != len(systemVerbs) {
		t.Fatalf("the catalogue lists %d verbs, the table has %d", len(rep.Verbs), len(systemVerbs))
	}
	for _, r := range rep.Verbs {
		v, ok := systemVerbByName(r.Name)
		if !ok {
			t.Fatalf("the catalogue lists %q, which is not in the table", r.Name)
		}
		if r.SessionOnly != v.SessionOnly {
			t.Errorf("%s: the catalogue says session_only=%v, the table enforces %v", r.Name, r.SessionOnly, v.SessionOnly)
		}
		if r.Effect != v.Effect || r.Summary != v.Summary || r.ConfinedReason != v.Why {
			t.Errorf("%s: the catalogue and the table disagree: %+v vs %+v", r.Name, r, v)
		}
		// Every verb declares an effect class, which is the thing these verbs did not have
		// before: the gateway's policy and the approval prompt both key off it.
		switch r.Effect {
		case manifest.EffectRead, manifest.EffectWrite, manifest.EffectDestructive, manifest.EffectExternal:
		default:
			t.Errorf("%s declares effect %q, which is not a class the manifest knows", r.Name, r.Effect)
		}
		if r.Summary == "" {
			t.Errorf("%s has no summary", r.Name)
		}
	}
}

func TestReadsAreReadAndWritesAreWrite(t *testing.T) {
	// The effect class is the signal the gateway's policy and the approval prompt both read,
	// so the two verbs that change the machine must not be filed as reads. Neither is
	// `destructive`: nothing is lost and the previous value can be put back, which is what
	// separates a volume change from a delete.
	writes := map[string]bool{"system.audio.set@v1": true, "system.audio.mute@v1": true}
	for _, v := range systemVerbs {
		want := manifest.EffectRead
		if writes[v.Name] {
			want = manifest.EffectWrite
		}
		if v.Effect != want {
			t.Errorf("%s is %q, want %q", v.Name, v.Effect, want)
		}
	}
}

func TestMuteNeedsAnExplicitStateSoAnEmptyPayloadCannotUnmute(t *testing.T) {
	// `muted` has no default. A plain `bool` would decode a missing field to false, so
	// `system.audio.mute@v1 {}` would silently unmute the machine -- a change nobody asked
	// for, from a request that looks like a query.
	s := sysSvc(t)
	_, e := call(t, s, session(), "system.audio.mute@v1", "{}")
	if e == nil {
		t.Fatal("an empty payload was accepted; it would have unmuted the machine")
	}
	if e.Code != "bad_request" || !strings.Contains(e.Msg, "muted") {
		t.Fatalf("got %s: %s", e.Code, e.Msg)
	}
	if _, e := call(t, s, session(), "system.audio.mute@v1", `{"muted":true}`); e != nil {
		t.Fatalf("an explicit state was refused: %s", e.Msg)
	}
}

func TestAnUnknownSystemVerbFallsThroughToNoSuchVerb(t *testing.T) {
	// systemDispatch must report "not handled" for a name it does not own, or it would
	// swallow every other verb's request.
	s := sysSvc(t)
	if _, e := call(t, s, session(), "system.nonsense@v1", "{}"); e == nil || e.Code != "no_such_verb" {
		t.Fatalf("got %+v", e)
	}
	// And a registry verb still reaches its own arm rather than being eaten.
	if _, _, handled := s.systemDispatch(proto.Envelope{From: session(), Verb: "registry.apps@v1"}); handled {
		t.Fatal("systemDispatch claimed a registry verb")
	}
}
