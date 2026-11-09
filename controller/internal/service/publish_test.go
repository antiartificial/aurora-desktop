package service

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/surface"
	"github.com/antiartificial/aurora/controller/internal/invoke"
)

// sessionCaller is the identity busd stamps for this uid: the session socket,
// which surfaceCaller treats as the person.
func sessionCaller() string { return fmt.Sprintf("uid%d:test", os.Getuid()) }

// domainCaller is the bus identity of app's verb domain, derived exactly as
// the controller derives it everywhere else.
func domainCaller(app string) string {
	return "domain:" + invoke.DomainName(app) + ":00112233aabbccdd"
}

func publishPayload(t *testing.T, d surface.Descriptor) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"surface": d})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func publish(t *testing.T, s *Service, from string, d surface.Descriptor) *invoke.Error {
	t.Helper()
	_, e := s.dispatch(proto.Envelope{From: from, Verb: "surface.publish@v1", Payload: publishPayload(t, d)})
	return e
}

// listRaw returns the descriptors surface.list@v1 served, as raw bytes, so a
// test can check the encoding and not only the decoded fields.
func listRaw(t *testing.T, s *Service, from string) []json.RawMessage {
	t.Helper()
	out, e := s.dispatch(proto.Envelope{From: from, Verb: "surface.list@v1"})
	if e != nil {
		t.Fatalf("list from %q: %+v", from, e)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		Surfaces []json.RawMessage `json:"surfaces"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r.Surfaces
}

// TestPublishThenListRoundTrip is the gap this verb pair closes: an app with no
// manifest announces a descriptor and the listing hands it back unchanged. The
// app id is one the test registry has never heard of, which is the point.
func TestPublishThenListRoundTrip(t *testing.T) {
	s := &Service{}
	d := surface.Descriptor{
		App:    "org.atlas",
		Title:  "Atlas",
		Grants: []string{"path:/tmp/atlas"},
		Actions: []surface.Action{
			{ID: "open", Label: "Open", Effect: surface.EffectRead},
			{ID: "place", Label: "Place", Effect: surface.EffectWrite, Grants: []string{"path:/tmp/atlas"}},
		},
	}
	if e := publish(t, s, sessionCaller(), d); e != nil {
		t.Fatalf("publish: %+v", e)
	}
	got := listRaw(t, s, sessionCaller())
	if len(got) != 1 {
		t.Fatalf("want 1 surface, got %d: %s", len(got), got)
	}
	var back surface.Descriptor
	if err := json.Unmarshal(got[0], &back); err != nil {
		t.Fatal(err)
	}
	if back.App != d.App || back.Title != d.Title || len(back.Actions) != 2 {
		t.Fatalf("round trip lost the descriptor: %s", got[0])
	}
	if back.Actions[1].Grants[0] != "path:/tmp/atlas" {
		t.Fatalf("action grants lost: %s", got[0])
	}
}

// TestPublishRefusesInvalidDescriptor pins that validation happens at publish
// and the reason reaches the caller: a surface the shell cannot draw must not
// sit in the set waiting for a draw to fail.
func TestPublishRefusesInvalidDescriptor(t *testing.T) {
	s := &Service{}
	for name, d := range map[string]surface.Descriptor{
		"unknown effect": {
			App: "org.example.bad", Title: "Bad",
			Actions: []surface.Action{{ID: "run", Label: "Run", Effect: "everything"}},
		},
		"no app": {
			Title:   "Nameless",
			Actions: []surface.Action{{ID: "run", Label: "Run", Effect: surface.EffectNominal}},
		},
		"duplicate action": {
			App: "org.example.dup", Title: "Dup",
			Actions: []surface.Action{
				{ID: "run", Label: "Run", Effect: surface.EffectRead},
				{ID: "run", Label: "Run again", Effect: surface.EffectRead},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := publish(t, s, sessionCaller(), d)
			if e == nil || e.Code != "bad_surface" || e.Detail == "" {
				t.Fatalf("want a bad_surface refusal with a reason, got %+v", e)
			}
		})
	}
	if got := listRaw(t, s, sessionCaller()); len(got) != 0 {
		t.Fatalf("a refused descriptor was stored: %s", got)
	}

	// A request that carries no descriptor at all is refused by name, not
	// interpreted as an empty surface.
	_, e := s.dispatch(proto.Envelope{From: sessionCaller(), Verb: "surface.publish@v1", Payload: json.RawMessage(`{}`)})
	if e == nil || e.Code != "bad_surface" {
		t.Fatalf("empty publish: %+v", e)
	}
}

// TestPublishRefusesAnotherAppsSurface pins the caller rule: a domain may not
// write a descriptor for an app it does not own, because that descriptor is a
// claim about another app's actions and grants. The session -- the person --
// still may, for any app it names.
func TestPublishRefusesAnotherAppsSurface(t *testing.T) {
	s := &Service{}
	d := surface.Descriptor{
		App:     "org.aurora.demo",
		Title:   "Demo",
		Actions: []surface.Action{{ID: "copy", Label: "Copy", Effect: surface.EffectWrite}},
	}
	for _, from := range []string{domainCaller("org.aurora.other"), "domain:"} {
		e := publish(t, s, from, d)
		if e == nil || e.Code != "not_permitted" || e.Msg == "" {
			t.Fatalf("publish for another app from %q was not refused: %+v", from, e)
		}
	}
	if got := listRaw(t, s, sessionCaller()); len(got) != 0 {
		t.Fatalf("a refused publish was stored: %s", got)
	}

	// Its own domain may publish its own surface.
	if e := publish(t, s, domainCaller("org.aurora.demo"), d); e != nil {
		t.Fatalf("own domain refused: %+v", e)
	}
	// And the session may publish for an app that has no manifest, naming it.
	if e := publish(t, s, sessionCaller(), surface.Descriptor{
		App: "org.atlas", Title: "Atlas", Actions: []surface.Action{{ID: "open", Label: "Open", Effect: surface.EffectRead}},
	}); e != nil {
		t.Fatalf("session publish refused: %+v", e)
	}
	if got := listRaw(t, s, sessionCaller()); len(got) != 2 {
		t.Fatalf("want 2 stored surfaces, got %d", len(got))
	}
}

// TestRepublishReplaces pins that the app id is the key: a window that
// redraws its chrome replaces its descriptor rather than adding a second one.
func TestRepublishReplaces(t *testing.T) {
	s := &Service{}
	first := surface.Descriptor{
		App: "org.atlas", Title: "One",
		Actions: []surface.Action{{ID: "old", Label: "Old", Effect: surface.EffectRead}},
	}
	second := surface.Descriptor{
		App: "org.atlas", Title: "Two",
		Actions: []surface.Action{{ID: "new", Label: "New", Effect: surface.EffectRead}},
	}
	if e := publish(t, s, sessionCaller(), first); e != nil {
		t.Fatalf("first publish: %+v", e)
	}
	if e := publish(t, s, sessionCaller(), second); e != nil {
		t.Fatalf("second publish: %+v", e)
	}
	got := listRaw(t, s, sessionCaller())
	if len(got) != 1 {
		t.Fatalf("republish added a second surface: %s", got)
	}
	var back surface.Descriptor
	if err := json.Unmarshal(got[0], &back); err != nil {
		t.Fatal(err)
	}
	if back.Title != "Two" || len(back.Actions) != 1 || back.Actions[0].ID != "new" {
		t.Fatalf("the stale descriptor survived: %s", got[0])
	}
}

// TestListIsCanonical pins the encoding: the list is sorted by app id and each
// descriptor is exactly Descriptor.Marshal's bytes, so a golden diff across
// runs is stable and the same tree cannot produce two orderings.
func TestListIsCanonical(t *testing.T) {
	s := &Service{}
	z := surface.Descriptor{
		App: "z.example", Title: "Zed",
		Grants: []string{"path:/b", "path:/a"},
		Actions: []surface.Action{
			{ID: "zeta", Label: "Zeta", Effect: surface.EffectRead, Grants: []string{"ui:y", "ui:x"}},
			{ID: "alpha", Label: "Alpha", Effect: surface.EffectWrite, Grants: []string{"path:/b", "path:/a"}},
		},
	}
	a := surface.Descriptor{
		App: "a.example", Title: "Aye",
		Actions: []surface.Action{{ID: "one", Label: "One", Effect: surface.EffectNominal}},
	}
	// Published out of order on purpose: the list, not the caller, sorts.
	for _, d := range []surface.Descriptor{z, a} {
		if e := publish(t, s, sessionCaller(), d); e != nil {
			t.Fatalf("publish %s: %+v", d.App, e)
		}
	}
	got := listRaw(t, s, sessionCaller())
	if len(got) != 2 {
		t.Fatalf("want 2 surfaces, got %d", len(got))
	}
	var apps []string
	for _, raw := range got {
		var d surface.Descriptor
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		apps = append(apps, d.App)
	}
	if strings.Join(apps, ",") != "a.example,z.example" {
		t.Fatalf("list order %v", apps)
	}
	// Each entry must be byte-identical to the canonical encoding of what was
	// published, not merely equal after decoding.
	for i, d := range []surface.Descriptor{a, z} {
		want, err := d.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if string(got[i]) != string(want) {
			t.Fatalf("%s is not canonical:\n got %s\nwant %s", d.App, got[i], want)
		}
	}
	var zed surface.Descriptor
	if err := json.Unmarshal(got[1], &zed); err != nil {
		t.Fatal(err)
	}
	if zed.Actions[0].ID != "alpha" || zed.Actions[1].ID != "zeta" ||
		strings.Join(zed.Grants, ",") != "path:/a,path:/b" ||
		strings.Join(zed.Actions[0].Grants, ",") != "path:/a,path:/b" {
		t.Fatalf("actions or grants not sorted: %s", got[1])
	}
}

// TestListCallerRule pins who may read the published set: the session and any
// named domain, and no one else. A foreign uid and an unnameable domain are
// refused because neither can be placed in a class the rule allows.
func TestListCallerRule(t *testing.T) {
	s := &Service{}
	if e := publish(t, s, sessionCaller(), surface.Descriptor{
		App: "org.atlas", Title: "Atlas",
		Actions: []surface.Action{{ID: "open", Label: "Open", Effect: surface.EffectRead}},
	}); e != nil {
		t.Fatalf("publish: %+v", e)
	}
	for _, from := range []string{sessionCaller(), domainCaller("org.atlas"), domainCaller("org.other")} {
		if got := listRaw(t, s, from); len(got) != 1 {
			t.Fatalf("list from %q returned %d surfaces", from, len(got))
		}
	}
	for _, from := range []string{"uid999999:probe", "domain:"} {
		_, e := s.dispatch(proto.Envelope{From: from, Verb: "surface.list@v1"})
		if e == nil || e.Code != "not_permitted" {
			t.Fatalf("list from %q was not refused: %+v", from, e)
		}
	}
}
