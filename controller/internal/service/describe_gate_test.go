package service

import (
	"encoding/json"
	"testing"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/invoke"
)

// TestDescribeUsesTheSameCallerRule closes the gap ui.act had already closed: reading a descriptor
// discloses what an app can do and which grants it needs, so describe must not be the looser path.
// Both verbs go through surfaceCaller; this pins that they actually do.
func TestDescribeUsesTheSameCallerRule(t *testing.T) {
	reg, skipped := apps.Load("../../testdata/apps")
	if len(skipped) != 0 {
		t.Fatalf("skipped %v", skipped)
	}
	s := &Service{Reg: reg, Inv: &invoke.Invoker{Reg: reg, Runner: &domain.Fake{}}}
	b, _ := json.Marshal(map[string]any{"app": "org.aurora.demo", "verb": "copy@v1"})

	for _, from := range []string{
		"domain:" + invoke.DomainName("org.aurora.other") + ":00112233aabbccdd",
		"uid999999:probe",
		"domain:",
	} {
		_, e := s.dispatch(proto.Envelope{From: from, Verb: "registry.describe@v1", Payload: b})
		if e == nil || e.Code != "not_permitted" || e.Msg == "" {
			t.Fatalf("describe from %q was not refused: %+v", from, e)
		}
	}

	// The app's own domain may read its own surface.
	_, e := s.dispatch(proto.Envelope{From: "domain:" + invoke.DomainName("org.aurora.demo") + ":00112233aabbccdd",
		Verb: "registry.describe@v1", Payload: b})
	if e != nil && e.Code == "not_permitted" {
		t.Fatalf("the app's own domain was refused its own surface: %+v", e)
	}
}
