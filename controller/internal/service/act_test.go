package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/surface"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/invoke"
)

// TestUIActGrantGate is the gate, end to end over the bus: which surface
// actions run, and which are refused before the app's domain is touched.
//
// The cases are descriptors an app could publish about itself, which is the
// untrusted input ui.act exists to police. They are built by hand and passed
// through the real handler, the real registry and the real invoke path -- the
// policy is not mocked, only its input varies.
func TestUIActGrantGate(t *testing.T) {
	fk := &domain.Fake{OnRun: func(_ []string, stdin []byte) domain.Result {
		var rq struct{ Output string }
		json.Unmarshal(stdin, &rq)
		if rq.Output != "" {
			os.WriteFile(rq.Output, []byte("out"), 0o644)
		}
		return domain.Result{}
	}}
	e := start(t, fk, "../../testdata/apps")
	in, out := filepath.Join(e.dir, "in.png"), filepath.Join(e.dir, "out.png")
	os.WriteFile(in, png, 0o644)

	// A descriptor for one app. Every action below is built from a real demo
	// verb except where the case is about an id no verb backs.
	demoSurface := func(acts ...surface.Action) json.RawMessage {
		b, err := surface.Descriptor{App: "org.aurora.demo", Title: "Demo copier", Actions: acts}.Marshal()
		if err != nil {
			t.Fatalf("build descriptor: %v", err)
		}
		return b
	}
	cases := []struct {
		name string
		// app, action and the surface claim the request carries (nil: let the
		// controller derive it from the manifest, the trusted path).
		app     string
		action  string
		surface json.RawMessage
		wantErr string // "" means it must run
		want    string // receipt effect on success
	}{
		{
			name:   "granted action allowed and receipted",
			app:    "org.aurora.demo",
			action: "copy@v1",
			want:   surface.EffectWrite,
		},
		{
			name:   "granted action allowed when the surface repeats its held grant",
			app:    "org.aurora.demo",
			action: "copy@v1",
			surface: demoSurface(surface.Action{ID: "copy@v1", Label: "Copy", Effect: surface.EffectWrite,
				Grants: []string{"path:~/Pictures"}}),
			want: surface.EffectWrite,
		},
		{
			name:   "ungranted action refused",
			app:    "org.aurora.demo",
			action: "copy@v1",
			surface: demoSurface(surface.Action{ID: "copy@v1", Label: "Copy", Effect: surface.EffectWrite,
				Grants: []string{"path:/etc/shadow"}}),
			wantErr: "grant_not_held",
		},
		{
			// The fourth case, in its own shape: the effect the shell would
			// draw as a write, with a grant the app does not hold.
			name:   "write action with a missing grant refused",
			app:    "org.aurora.demo",
			action: "delete@v1",
			surface: demoSurface(surface.Action{ID: "delete@v1", Label: "Delete", Effect: surface.EffectWrite,
				Grants: []string{"device:gpu"}}),
			wantErr: "grant_not_held",
		},
		{
			name:   "unknown action refused",
			app:    "org.aurora.demo",
			action: "nope@v1",
			surface: demoSurface(surface.Action{ID: "copy@v1", Label: "Copy", Effect: surface.EffectWrite,
				Grants: []string{"path:~/Pictures"}}),
			wantErr: "no_such_action",
		},
		{
			name:    "a surface that lists no such action refuses it",
			app:     "org.aurora.demo",
			action:  "copy@v1",
			surface: demoSurface(),
			wantErr: "no_such_action",
		},
		{
			name:   "a write relabelled nominal refused",
			app:    "org.aurora.demo",
			action: "copy@v1",
			surface: demoSurface(surface.Action{ID: "copy@v1", Label: "Copy", Effect: surface.EffectNominal,
				Grants: []string{"path:~/Pictures"}}),
			wantErr: "bad_surface",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := len(fk.Calls)
			body := map[string]any{"app": c.app, "action": c.action, "input": in, "output": out}
			if c.surface != nil {
				body["surface"] = c.surface
			}
			raw, err := e.call(t, "ui.act@v1", body)
			if c.wantErr != "" {
				var re *client.RemoteError
				if !errors.As(err, &re) {
					t.Fatalf("want refusal, got err=%v body=%s", err, raw)
				}
				var f struct {
					Code, Message, Detail string
					OK                    bool
				}
				if err := json.Unmarshal(raw, &f); err != nil {
					t.Fatalf("refusal is not the error envelope: %s", raw)
				}
				if f.OK || f.Code != c.wantErr || f.Message == "" || f.Detail == "" {
					t.Fatalf("want code %q with message and detail, got %s", c.wantErr, raw)
				}
				// The property that matters: a refused action never reached
				// the app's domain, so nothing ran.
				if len(fk.Calls) != before {
					t.Fatalf("refused action touched the domain: %v", fk.Calls[before:])
				}
				return
			}
			if err != nil {
				t.Fatalf("%v %s", err, raw)
			}
			var r actReply
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatalf("reply: %s", raw)
			}
			if !r.OK || r.Receipt.Action != c.action || r.Receipt.Effect != c.want {
				t.Fatalf("receipt: %s", raw)
			}
			if r.Receipt.Artifact != out {
				t.Fatalf("receipt names %q as the artifact, the invoke made %q", r.Receipt.Artifact, out)
			}
		})
	}
}

// TestUIActCallerIsNotThePayloadsClaim pins the caller rule beside the grant
// rule: the identity comes from the socket (req.From), never the payload, and
// a domain may act only on its own app's surface. It is dispatched directly
// because busd stamps From from peer credentials, and the case under test is
// precisely a caller that is not the session.
func TestUIActCallerIsNotThePayloadsClaim(t *testing.T) {
	reg, skipped := apps.Load("../../testdata/apps")
	if len(skipped) != 0 {
		t.Fatalf("skipped %v", skipped)
	}
	s := &Service{Reg: reg, Inv: &invoke.Invoker{Reg: reg, Runner: &domain.Fake{}}}
	b, _ := json.Marshal(map[string]any{"app": "org.aurora.demo", "action": "copy@v1"})

	// Another app's domain, a fourth party's uid, and a domain-shaped identity
	// with no name are all refused. The last one must not read as a session.
	for _, from := range []string{
		"domain:" + invoke.DomainName("org.aurora.other") + ":00112233aabbccdd",
		"uid999999:probe",
		"domain:",
	} {
		_, e := s.dispatch(proto.Envelope{From: from, Verb: "ui.act@v1", Payload: b})
		if e == nil || e.Code != "not_permitted" || e.Msg == "" {
			t.Fatalf("caller %q: %+v", from, e)
		}
	}

	// The app's own domain is allowed past the caller gate. It may still fail
	// later (here there is no input file), but not as a stranger.
	_, e := s.dispatch(proto.Envelope{From: "domain:" + invoke.DomainName("org.aurora.demo") + ":00112233aabbccdd", Verb: "ui.act@v1", Payload: b})
	if e != nil && e.Code == "not_permitted" {
		t.Fatalf("the app's own domain was refused as a stranger: %+v", e)
	}
}
