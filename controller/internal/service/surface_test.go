package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/contracts/manifest"
	"github.com/antiartificial/aurora/contracts/surface"
	"github.com/antiartificial/aurora/controller/internal/domain"
)

// TestWindowSurface checks the translation the controller performs between a
// manifest's effects and the surface contract, and that a descriptor the
// contract refuses is refused here rather than encoded for someone else to
// trip over. The bad cases cannot arrive from apps.Load, which validates
// manifests on the way in; they are pinned anyway because windowSurface is
// where the second, stricter contract is applied, and a future caller that
// builds verbs by hand must not get a silently unpoliced surface.
func TestWindowSurface(t *testing.T) {
	v := func(name string, effect manifest.Effect, grants ...string) manifest.Verb {
		return manifest.Verb{Name: name, Summary: name + " summary", Effect: effect, Capabilities: grants}
	}
	cases := []struct {
		name    string
		app     string
		verbs   []manifest.Verb
		wantErr string // "" means it must validate and marshal
	}{
		{
			name:  "valid",
			app:   "org.example.atlas",
			verbs: []manifest.Verb{v("open@v1", manifest.EffectRead), v("save@v1", manifest.EffectWrite, "path:/tmp", "device:gpu")},
		},
		{
			name: "all effects map up",
			app:  "org.example.atlas",
			verbs: []manifest.Verb{
				v("look@v1", manifest.EffectRead),
				v("delete@v1", manifest.EffectDestructive, "path:/tmp"),
				v("send@v1", manifest.EffectExternal, "net:example.com"),
			},
		},
		{
			name:    "duplicate action id",
			app:     "org.example.atlas",
			verbs:   []manifest.Verb{v("open@v1", manifest.EffectRead), v("open@v1", manifest.EffectWrite)},
			wantErr: "duplicate action id",
		},
		{
			name:    "unknown effect",
			app:     "org.example.atlas",
			verbs:   []manifest.Verb{v("open@v1", manifest.Effect("maybe"))},
			wantErr: "not read/write/nominal",
		},
		{
			name:    "no app",
			verbs:   []manifest.Verb{v("open@v1", manifest.EffectRead)},
			wantErr: "app is empty",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, err := windowSurface(c.app, "Atlas", c.verbs).Marshal()
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want error containing %q, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Decoding back through the contract proves the bytes the controller
			// sends are the bytes a shell can read.
			d, err := surface.Unmarshal(raw)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if d.App != c.app || d.Title != "Atlas" {
				t.Fatalf("identity: %+v", d)
			}
			// Marshal sorts actions by id, so the reply is stable across runs
			// even though the manifest need not list verbs in order.
			for i := 1; i < len(d.Actions); i++ {
				if d.Actions[i-1].ID >= d.Actions[i].ID {
					t.Fatalf("actions not canonical: %v", d.Actions)
				}
			}
			// The exact effect mapping is the part a shell would draw wrongly.
			for _, a := range d.Actions {
				switch a.ID {
				case "look@v1":
					if a.Effect != surface.EffectRead {
						t.Fatalf("%s effect %q", a.ID, a.Effect)
					}
				case "delete@v1", "send@v1":
					if a.Effect != surface.EffectWrite {
						t.Fatalf("%s effect %q", a.ID, a.Effect)
					}
				}
			}
			if c.name == "valid" {
				var save surface.Action
				for _, a := range d.Actions {
					if a.ID == "save@v1" {
						save = a
					}
				}
				if strings.Join(save.Grants, ",") != "device:gpu,path:/tmp" {
					t.Fatalf("grants not sorted/preserved: %v", save.Grants)
				}
			}
		})
	}
}

// TestDescribeCarriesSurface is the wire test: the reply keeps every field an
// older client reads and gains the canonical descriptor under "surface".
func TestDescribeCarriesSurface(t *testing.T) {
	e := start(t, &domain.Fake{}, "../../../apps")
	raw, err := e.call(t, "registry.describe@v1", map[string]any{"app": "org.aurora.lumen", "verb": "resize@v1"})
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		App, Verb, Effect, Summary string
		Input                      map[string]string
		Capabilities               []string
		Surface                    json.RawMessage
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("reply not decodable by an old reader: %v: %s", err, raw)
	}
	// The pre-surface shape, unchanged.
	if d.App != "org.aurora.lumen" || d.Verb != "resize@v1" || d.Effect != "write" || d.Input["width"] != "int" {
		t.Fatalf("old fields changed: %s", raw)
	}
	if strings.Join(d.Capabilities, ",") != "path:~/Pictures" {
		t.Fatalf("capabilities: %s", raw)
	}
	s, err := surface.Unmarshal(d.Surface)
	if err != nil {
		t.Fatalf("surface does not validate: %v: %s", err, d.Surface)
	}
	if s.App != "org.aurora.lumen" || s.Title != "Lumen" {
		t.Fatalf("surface identity: %+v", s)
	}
	var ids []string
	for _, a := range s.Actions {
		ids = append(ids, a.ID)
	}
	if strings.Join(ids, ",") != "export@v1,histogram@v1,resize@v1" {
		t.Fatalf("surface actions: %v", ids)
	}
	// The action for the verb asked about carries the verb's effect and grants.
	if a := s.Actions[2]; a.Effect != surface.EffectWrite || strings.Join(a.Grants, ",") != "path:~/Pictures" {
		t.Fatalf("resize action: %+v", a)
	}
}
