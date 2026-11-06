package apps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const winManifest = `[app]
id = "org.example.win"
name = "Win"
version = "1.0.0"
image_hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
windowed = true
exec = "bin/win"
`

func loadWin(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "win")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.toml"), []byte(winManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, skipped := Load(root)
	if len(skipped) != 0 {
		t.Fatalf("skipped: %v", skipped)
	}
	a, ok := reg.App("org.example.win")
	if !ok {
		t.Fatal("app not loaded")
	}
	return a
}

func TestExecStatus(t *testing.T) {
	a := loadWin(t)
	path, problem := a.ExecStatus()
	if !strings.HasSuffix(path, "win/bin/win") || !strings.Contains(problem, "not built") {
		t.Errorf("missing binary: path %q problem %q", path, problem)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, problem := a.ExecStatus(); !strings.Contains(problem, "not an executable") {
		t.Errorf("non-executable file: %q", problem)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, problem := a.ExecStatus(); problem != "" {
		t.Errorf("executable file reported %q", problem)
	}
}

func TestExecStatusNotWindowed(t *testing.T) {
	a := loadWin(t)
	a.Manifest.App.Windowed = false
	if p, problem := a.ExecStatus(); p != "" || problem != "" {
		t.Errorf("non-windowed app: %q %q", p, problem)
	}
}
