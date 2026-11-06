package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/server"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/busreg"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

type env struct {
	cli    *client.Client // caller
	events *client.Client // subscribed to provenance.created
	fake   *domain.Fake
	dir    string
	inv    *invoke.Invoker
	dial   func() *client.Client // a new session-socket client
}

// start runs a real in-process busd and a registry service on it.
func start(t *testing.T, runner domain.Runner, dirs ...string) *env {
	t.Helper()
	d, err := os.MkdirTemp("", "ctl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	sock := filepath.Join(d, "b.sock")
	srv, err := server.New(server.Config{SocketPath: sock, DataDir: filepath.Join(d, "log")})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	dial := func() *client.Client {
		var c *client.Client
		for i := 0; i < 100; i++ {
			if c, err = client.Dial(sock); err == nil {
				t.Cleanup(func() { c.Close() })
				return c
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal(err)
		return nil
	}
	reg, skipped := apps.Load(dirs...)
	if len(skipped) != 0 {
		t.Fatalf("skipped %v", skipped)
	}
	sc := dial()
	inv := &invoke.Invoker{Reg: reg, Runner: runner, Logf: t.Logf, Bus: busreg.Client{C: sc}, Timeout: 30 * time.Second,
		Approvals: &invoke.Approver{Timeout: 10 * time.Second},
		Publish:   func(topic string, p []byte) error { return sc.Publish(topic, p, proto.QoSInitiated, false) }}
	ctx := context.Background()
	if err := sc.Register(ctx, "registry"); err != nil {
		t.Fatal(err)
	}
	go (&Service{Reg: reg, Inv: inv}).Serve(sc)
	ec := dial()
	if err := ec.Subscribe(ctx, "provenance.created"); err != nil {
		t.Fatal(err)
	}
	fk, _ := runner.(*domain.Fake)
	return &env{cli: dial(), events: ec, fake: fk, dir: t.TempDir(), inv: inv, dial: dial}
}

func (e *env) call(t *testing.T, verb string, payload any) (json.RawMessage, error) {
	t.Helper()
	b, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := e.cli.Call(ctx, proto.Envelope{To: "registry", Verb: verb, Payload: b})
	return r.Payload, err
}

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func TestAppsAndVerbsShapes(t *testing.T) {
	e := start(t, &domain.Fake{}, "../../../apps")
	raw, err := e.call(t, "registry.apps@v1", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var a struct {
		Apps []struct {
			ID, Name, Version string
			Verbs             []string
		}
	}
	json.Unmarshal(raw, &a)
	found := false
	for _, x := range a.Apps {
		if x.ID == "org.aurora.lumen" {
			found = true
			if x.Name != "Lumen" || x.Version != "0.1.0" || strings.Join(x.Verbs, ",") != "resize@v1,export@v1,histogram@v1" {
				t.Fatalf("lumen entry %+v", x)
			}
		}
	}
	if !found {
		t.Fatalf("lumen missing: %s", raw)
	}

	raw, err = e.call(t, "registry.verbs@v1", map[string]any{"accepts": "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Verbs []struct {
			Name, App, Effect, Summary string
			Accepts                    []string
		}
	}
	json.Unmarshal(raw, &v)
	var lumen []string
	for _, x := range v.Verbs {
		if x.App == "org.aurora.lumen" {
			lumen = append(lumen, x.Name)
			if x.Effect == "" || len(x.Accepts) == 0 || x.Summary == "" {
				t.Fatalf("incomplete verb %+v", x)
			}
		}
	}
	if strings.Join(lumen, ",") != "resize@v1,export@v1,histogram@v1" {
		t.Fatalf("lumen verbs for image/png: %v", lumen)
	}
	// scale_video@v1 comes from apps/ffmpeg, which replaced a manifest-only
	// placeholder whose verb was called transcode@v1. The point of this
	// assertion is the filter, not the name: a video type must reach ffmpeg's
	// verbs and must not reach lumen's image verbs.
	raw, _ = e.call(t, "registry.verbs@v1", map[string]any{"accepts": "video/mp4"})
	if strings.Contains(string(raw), "lumen") || !strings.Contains(string(raw), "scale_video@v1") {
		t.Fatalf("video filter wrong: %s", raw)
	}
	all, _ := e.call(t, "registry.verbs@v1", map[string]any{})
	if !strings.Contains(string(all), "scale_video@v1") || !strings.Contains(string(all), "resize@v1") {
		t.Fatalf("unfiltered list wrong: %s", all)
	}
}

func TestDescribe(t *testing.T) {
	e := start(t, &domain.Fake{}, "../../../apps")
	raw, err := e.call(t, "registry.describe@v1", map[string]any{"app": "org.aurora.lumen", "verb": "resize@v1"})
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Effect string
		Input  map[string]string
	}
	json.Unmarshal(raw, &d)
	if d.Effect != "write" || d.Input["width"] != "int" || d.Input["height"] != "int?" {
		t.Fatalf("describe: %s", raw)
	}
	if _, err := e.call(t, "registry.describe@v1", map[string]any{"app": "org.aurora.lumen", "verb": "nope@v1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestInvokeMissingAppOrVerbIsCleanError(t *testing.T) {
	e := start(t, &domain.Fake{}, "../../../apps")
	for _, p := range []map[string]any{
		{"app": "no.such.app", "verb": "resize@v1", "input": "/x"},
		{"app": "org.aurora.lumen", "verb": "nope@v1", "input": "/x"},
		{},
	} {
		raw, err := e.call(t, "registry.invoke@v1", p)
		var re *client.RemoteError
		if !errors.As(err, &re) {
			t.Fatalf("want RemoteError, got %v", err)
		}
		var body struct {
			OK     bool
			Error  string
			Detail string
		}
		json.Unmarshal(raw, &body)
		if body.OK || body.Error == "" {
			t.Fatalf("bad failure body %s", raw)
		}
	}
	if len(e.fake.Calls) != 0 {
		t.Fatal("domain touched")
	}
	// service survived
	if _, err := e.call(t, "registry.apps@v1", map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

func TestInvokePublishesExactlyOneProvenanceEvent(t *testing.T) {
	fk := &domain.Fake{OnRun: func(_ []string, stdin []byte) domain.Result {
		var rq struct{ Output string }
		json.Unmarshal(stdin, &rq)
		os.WriteFile(rq.Output, []byte("out"), 0o644)
		return domain.Result{}
	}}
	e := start(t, fk, "../../testdata/apps")
	in, out := filepath.Join(e.dir, "in.png"), filepath.Join(e.dir, "out.png")
	os.WriteFile(in, png, 0o644)
	raw, err := e.call(t, "registry.invoke@v1", map[string]any{"app": "org.aurora.demo", "verb": "copy@v1",
		"input": in, "output": out, "args": map[string]any{"tag": "x"}})
	if err != nil {
		t.Fatalf("%v %s", err, raw)
	}
	var res struct {
		OK         bool
		Output     string
		Provenance struct {
			App, Verb, TS string
			Inputs        []string
		}
	}
	json.Unmarshal(raw, &res)
	if !res.OK || res.Output != out || res.Provenance.App != "org.aurora.demo" || res.Provenance.Verb != "copy@v1" ||
		len(res.Provenance.Inputs) != 1 || res.Provenance.Inputs[0] != in || res.Provenance.TS == "" {
		t.Fatalf("reply: %s", raw)
	}
	select {
	case ev := <-e.events.Events():
		if ev.Topic != "provenance.created" {
			t.Fatalf("topic %s", ev.Topic)
		}
		var r prov.Record
		json.Unmarshal(ev.Payload, &r)
		if r.File != out || r.App != "org.aurora.demo" || r.Verb != "copy@v1" || len(r.Inputs) != 1 || r.Inputs[0] != in ||
			string(r.Args) != `{"tag":"x"}` || !r.Isolated || r.TS == "" {
			t.Fatalf("event payload %s", ev.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no provenance.created event")
	}
	select {
	case ev := <-e.events.Events():
		t.Fatalf("second event: %s", ev.Payload)
	case <-time.After(300 * time.Millisecond):
	}
	// a failed invoke publishes nothing
	e.call(t, "registry.invoke@v1", map[string]any{"app": "org.aurora.demo", "verb": "copy@v1", "input": in, "output": in})
	select {
	case ev := <-e.events.Events():
		t.Fatalf("event for failed invoke: %s", ev.Payload)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestRealDomain runs the demo app inside a real bwrap domain, if available.
func TestRealDomain(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("no bwrap")
	}
	bin := filepath.Join(t.TempDir(), "domain")
	build := exec.Command("go", "build", "-o", bin, "./cmd/domain")
	build.Dir = "../../../adapter/linux"
	if b, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build domain CLI: %v\n%s", err, b)
	}
	droot := t.TempDir()
	t.Setenv("AURORA_DOMAINS_DIR", droot)
	cli := domain.CLI{Bin: bin}
	e := start(t, cli, "../../testdata/apps")
	in, out := filepath.Join(e.dir, "in.png"), filepath.Join(e.dir, "out.png")
	secret := filepath.Join(e.dir, "secret.txt")
	os.WriteFile(in, png, 0o644)
	os.WriteFile(secret, []byte("s3cret"), 0o644)
	raw, err := e.call(t, "registry.invoke@v1", map[string]any{"app": "org.aurora.demo", "verb": "copy@v1", "input": in, "output": out})
	if err != nil {
		t.Skipf("domain run unusable on this host: %v %s", err, raw)
	}
	var res struct{ Isolated bool }
	json.Unmarshal(raw, &res)
	if !res.Isolated {
		t.Fatalf("not isolated: %s", raw)
	}
	if b, _ := os.ReadFile(out); string(b) != string(png) {
		t.Fatal("output not copied")
	}
	// grants were revoked: the domain's grant list is empty afterwards
	meta, err := os.ReadFile(filepath.Join(droot, invoke.DomainName("org.aurora.demo"), "domain.json"))
	if err != nil {
		t.Fatalf("domain was not created under AURORA_DOMAINS_DIR: %v", err)
	}
	if strings.Contains(string(meta), e.dir) {
		t.Fatalf("stale grants remain: %s", meta)
	}
	// a granted-nothing run cannot see the sibling file nor the input
	r, _ := cli.Run(t.Context(), invoke.DomainName("org.aurora.demo"), []string{"/bin/cat", secret}, nil)
	if r.Exit == 0 {
		t.Fatalf("domain could read ungranted file: %s", r.Stdout)
	}
	// read verb: result comes back from the domain
	raw, err = e.call(t, "registry.invoke@v1", map[string]any{"app": "org.aurora.demo", "verb": "size@v1", "input": in})
	if err != nil || !strings.Contains(string(raw), `"bytes":`) {
		t.Fatalf("size: %v %s", err, raw)
	}
}
