package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// The event busd delivers carries the identity from `domain exec-id` and says
// the bus attested it (registration really succeeded on a real busd).
func TestProvenanceEventNamesAttestedDomain(t *testing.T) {
	fk := &domain.Fake{OnRun: func(_ []string, stdin []byte) domain.Result {
		var rq struct{ Output string }
		json.Unmarshal(stdin, &rq)
		os.WriteFile(rq.Output, []byte("x"), 0o644)
		return domain.Result{}
	}}
	sf := &sockFake{Fake: fk, dir: t.TempDir()}
	e := start(t, sf, "../../testdata/apps")
	in, out := filepath.Join(e.dir, "in.png"), filepath.Join(e.dir, "out.png")
	os.WriteFile(in, png, 0o644)
	if raw, err := e.call(t, "registry.invoke@v1", map[string]any{"app": "org.aurora.demo", "verb": "copy@v1", "input": in, "output": out}); err != nil {
		t.Fatalf("%v %s", err, raw)
	}
	id, _, _ := sf.Identity(invoke.DomainName("org.aurora.demo"))
	select {
	case ev := <-e.events.Events():
		var r prov.Record
		json.Unmarshal(ev.Payload, &r)
		if r.Domain != id || !r.DomainAttested || r.App != "org.aurora.demo" {
			t.Fatalf("event record %+v, want domain %q attested", r, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no provenance event")
	}
}

func TestRegisteredDomainVisibleInBusInspect(t *testing.T) {
	d := t.TempDir()
	fk := &domain.Fake{OnRun: func(_ []string, stdin []byte) domain.Result {
		var rq struct{ Output string }
		json.Unmarshal(stdin, &rq)
		os.WriteFile(rq.Output, []byte("x"), 0o644)
		return domain.Result{}
	}}
	e := start(t, &sockFake{Fake: fk, dir: d}, "../../testdata/apps")
	in, out := filepath.Join(e.dir, "in.png"), filepath.Join(e.dir, "out.png")
	os.WriteFile(in, png, 0o644)
	if raw, err := e.call(t, "registry.invoke@v1", map[string]any{"app": "org.aurora.demo", "verb": "copy@v1", "input": in, "output": out}); err != nil {
		t.Fatalf("%v %s", err, raw)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := e.cli.Call(ctx, proto.Envelope{To: proto.BusTarget, Verb: "bus.inspect@v1"})
	if err != nil {
		t.Fatal(err)
	}
	id, sock, _ := (&sockFake{Fake: fk, dir: d}).Identity(invoke.DomainName("org.aurora.demo"))
	if !strings.Contains(string(r.Payload), id) || !strings.Contains(string(r.Payload), sock) {
		t.Fatalf("bus does not list %s at %s: %s", id, sock, r.Payload)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("busd is not listening on the registered socket: %v", err)
	}
}

// sockFake puts the fake domain's socket somewhere busd can really listen.
type sockFake struct {
	*domain.Fake
	dir string
}

func (s *sockFake) Identity(n string) (string, string, error) {
	id, _, err := s.Fake.Identity(n)
	return id, filepath.Join(s.dir, n, "bus.sock"), err
}
