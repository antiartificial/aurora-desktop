package surface

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestValidateRejectsWhatTheShellCannotDraw(t *testing.T) {
	cases := []struct {
		name string
		d    Descriptor
	}{
		{"no app", Descriptor{Title: "t"}},
		{"no id", Descriptor{App: "a", Actions: []Action{{Label: "x", Effect: EffectRead}}}},
		{"duplicate id", Descriptor{App: "a", Actions: []Action{
			{ID: "open", Label: "x", Effect: EffectRead},
			{ID: "open", Label: "y", Effect: EffectRead},
		}}},
		{"unknown effect", Descriptor{App: "a", Actions: []Action{{ID: "open", Effect: "maybe"}}}},
	}
	for _, c := range cases {
		if err := c.d.Validate(); err == nil {
			t.Errorf("%s: expected an error", c.name)
		}
	}
}

func TestMarshalIsCanonical(t *testing.T) {
	a := Descriptor{App: "org.example.atlas", Title: "Atlas",
		Actions: []Action{
			{ID: "rename", Label: "Rename", Effect: EffectWrite, Grants: []string{"fs:write", "fs:read"}},
			{ID: "open", Label: "Open", Effect: EffectRead},
		},
		Grants: []string{"fs:write", "fs:read"}}
	b := Descriptor{App: "org.example.atlas", Title: "Atlas",
		Actions: []Action{
			{ID: "open", Label: "Open", Effect: EffectRead},
			{ID: "rename", Label: "Rename", Effect: EffectWrite, Grants: []string{"fs:read", "fs:write"}},
		},
		Grants: []string{"fs:read", "fs:write"}}

	ab, err := a.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	bb, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ab, bb) {
		t.Fatalf("the same surface produced two encodings:\n%s\n%s", ab, bb)
	}
}

func TestRoundTripKeepsTheContract(t *testing.T) {
	d := Descriptor{App: "a", Title: "t", Actions: []Action{{ID: "open", Label: "Open", Effect: EffectRead}}}
	b, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.App != d.App || len(got.Actions) != 1 || got.Actions[0].ID != "open" {
		t.Fatalf("round trip lost the surface: %+v", got)
	}
}

func TestUnmarshalValidates(t *testing.T) {
	bad := []byte(`{"app":"a","actions":[{"id":"x","effect":"nonsense"}]}`)
	if _, err := Unmarshal(bad); err == nil {
		t.Fatal("an unknown effect must be refused")
	}
	// and json.Unmarshal alone must not be the path anyone takes
	var raw map[string]any
	if err := json.Unmarshal(bad, &raw); err != nil {
		t.Fatal(err)
	}
}

func TestShellChromeSurvivesAndDefaultsOff(t *testing.T) {
	// A claim the shell has to honour, so it must not be dropped in the round trip -- an app that
	// opted out of drawing its own chrome would keep drawing it, and the user would see two headers.
	on := Descriptor{App: "a", Title: "t", ShellChrome: true}
	b, err := on.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"shell_chrome":true`)) {
		t.Fatalf("the claim was dropped: %s", b)
	}
	got, err := Unmarshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ShellChrome {
		t.Fatal("shell_chrome did not survive the round trip")
	}

	off := Descriptor{App: "a", Title: "t"}
	b2, err := off.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b2, []byte("shell_chrome")) {
		t.Fatalf("the default must not be emitted: %s", b2)
	}
}
