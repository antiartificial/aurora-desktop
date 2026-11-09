package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/server"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/service"
)

// TestRunAgainstTheRealRegistry is the end-to-end proof: a real busd, the real
// registry service and the real manifests, read through the CLI's own run().
// It is what catches a wire change the unit tests' hand-written JSON would not:
// the descriptors here are the ones the controller actually builds.
func TestRunAgainstTheRealRegistry(t *testing.T) {
	reg, skipped := apps.Load("../../../apps")
	if len(skipped) != 0 {
		t.Fatalf("skipped manifests: %+v", skipped)
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "bus.sock")
	srv, err := server.New(server.Config{SocketPath: sock, DataDir: filepath.Join(dir, "log")})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	svc, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := svc.Register(ctx, "registry"); err != nil {
		t.Fatal(err)
	}
	// describe reads only the registry; the invoker is the verb path, not this one.
	go (&service.Service{Reg: reg}).Serve(svc)

	var out, errb bytes.Buffer
	if code := run([]string{"--socket", sock}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{"org.aurora.lumen", "resize@v1", "write", "path:~/Pictures"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "org.aurora.haiku.text") {
		t.Fatalf("an app with no verbs was listed:\n%s", got)
	}

	// --json must be the contract's own bytes, so a descriptor read here is the
	// one a toolkit client would compare against.
	out.Reset()
	errb.Reset()
	if code := run([]string{"--socket", sock, "--json"}, &out, &errb); code != 0 {
		t.Fatalf("json exit %d: %s", code, errb.String())
	}
	var ds []json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &ds); err != nil {
		t.Fatalf("--json is not a JSON array: %v\n%s", err, out.String())
	}
	var sawLumen bool
	for _, raw := range ds {
		var d struct{ App string }
		if err := json.Unmarshal(raw, &d); err != nil {
			t.Fatal(err)
		}
		if d.App == "org.aurora.lumen" {
			sawLumen = true
		}
	}
	if !sawLumen || len(ds) == 0 {
		t.Fatalf("--json did not carry lumen: %s", out.String())
	}
}
