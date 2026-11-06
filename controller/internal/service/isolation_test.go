package service

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/controller/internal/domain"
)

// probeScript runs inside the domain DURING the invoke and reports what it
// can see around the one output file it was granted.
const probeScript = `#!/bin/sh
req=$(cat)
out=$(printf '%s' "$req" | sed -n 's/.*"output":"\([^"]*\)".*/\1/p')
dir=$(dirname "$out")
sib=$(cat "$dir/secret.txt" 2>/dev/null || echo DENIED)
ls=$(ls "$dir" 2>/dev/null | tr '\n' ' ')
echo hello > "$dir/planted.txt" 2>/dev/null
echo pwned > "$dir/secret.txt" 2>/dev/null
printf 'png-bytes' > "$out" || exit 1
echo "{\"sibling\":\"$sib\",\"ls\":\"$ls\"}"
`

func probeApp(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "probe")
	os.MkdirAll(root, 0o755)
	m, err := os.ReadFile("../../testdata/apps/demo/manifest.toml")
	if err != nil {
		t.Fatal(err)
	}
	m = []byte(strings.Replace(string(m), "org.aurora.demo", "org.aurora.probe", 1))
	os.WriteFile(filepath.Join(root, "manifest.toml"), m, 0o644)
	os.WriteFile(filepath.Join(root, "run"), []byte(probeScript), 0o755)
	return filepath.Dir(root)
}

// TestOutputGrantIsOneFileDuringInvoke: while the app runs, a sibling of the
// output file is unreadable and unwritable, the directory listing shows only
// the granted files, and the output file itself is writable.
func TestOutputGrantIsOneFileDuringInvoke(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("no bwrap")
	}
	bin := filepath.Join(t.TempDir(), "domain")
	build := exec.Command("go", "build", "-o", bin, "./cmd/domain")
	build.Dir = "../../../adapter/linux"
	if b, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build domain CLI: %v\n%s", err, b)
	}
	t.Setenv("AURORA_DOMAINS_DIR", t.TempDir())
	e := start(t, domain.CLI{Bin: bin}, probeApp(t))
	dir, _ := filepath.EvalSymlinks(e.dir)
	in, out, secret := filepath.Join(dir, "in.png"), filepath.Join(dir, "out.png"), filepath.Join(dir, "secret.txt")
	os.WriteFile(in, png, 0o644)
	os.WriteFile(secret, []byte("s3cret"), 0o644)
	raw, err := e.call(t, "registry.invoke@v1", map[string]any{"app": "org.aurora.probe", "verb": "copy@v1", "input": in, "output": out})
	if err != nil {
		t.Skipf("domain run unusable on this host: %v %s", err, raw)
	}
	var res struct {
		Isolated bool
		Result   struct{ Sibling, Ls string }
	}
	if json.Unmarshal(raw, &res); !res.Isolated {
		t.Fatalf("not isolated: %s", raw)
	}
	if res.Result.Sibling != "DENIED" || strings.Contains(string(raw), "s3cret") {
		t.Fatalf("sibling readable during invoke: %s", raw)
	}
	for _, f := range strings.Fields(res.Result.Ls) {
		if f != "in.png" && f != "out.png" && f != "planted.txt" {
			t.Fatalf("directory listing shows %q inside the domain: %s", f, res.Result.Ls)
		}
	}
	if b, _ := os.ReadFile(out); string(b) != "png-bytes" {
		t.Fatalf("output file not writable from inside: %q", b)
	}
	if b, _ := os.ReadFile(secret); string(b) != "s3cret" {
		t.Fatalf("sibling overwritten from inside: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "planted.txt")); err == nil {
		t.Fatal("app planted a file in the host directory")
	}
}
