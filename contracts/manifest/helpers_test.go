package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseTOML = `
[app]
id = "org.example.app"
name = "Example"
version = "1.0.0"
image_hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

[capabilities]
network = ["api.example.com"]

[[capabilities.paths]]
path = "~/Pictures"
access = "read"

[[verb]]
name = "look@v1"
effect = "read"
accepts = ["image/png"]
`

func mustParse(t *testing.T, src string) *Manifest {
	t.Helper()
	m, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

func mustLoad(t *testing.T, src string) *Manifest {
	t.Helper()
	m, err := Load([]byte(src))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return m
}

func example(t *testing.T, app string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "apps", app, "manifest.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// errFor loads src and returns the combined error text ("" if valid).
func errFor(src string) string {
	_, err := Load([]byte(src))
	if err == nil {
		return ""
	}
	return err.Error()
}

func wantErr(t *testing.T, src string, substrs ...string) {
	t.Helper()
	got := errFor(src)
	if got == "" {
		t.Fatalf("expected error containing %q, manifest was valid", substrs)
	}
	for _, s := range substrs {
		if !strings.Contains(got, s) {
			t.Errorf("error %q missing %q", got, s)
		}
	}
}
