package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const lumen = "../../../../apps/lumen/manifest.toml"

func do(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	c := run(args, &o, &e)
	return c, o.String(), e.String()
}

func TestCLI(t *testing.T) {
	if c, out, _ := do("validate", lumen); c != 0 || !strings.Contains(out, "ok") {
		t.Errorf("validate: %d %q", c, out)
	}
	c, out, _ := do("verbs", lumen, "--accepts", "image/png")
	if c != 0 || out != "resize@v1\twrite\nexport@v1\twrite\nhistogram@v1\tread\n" {
		t.Errorf("verbs: %d %q", c, out)
	}
	if c, _, _ := do("verbs", lumen); c != 2 {
		t.Errorf("missing --accepts should be a usage error, got %d", c)
	}
	if c, _, _ := do("show", "nope.toml"); c != 1 {
		t.Errorf("missing file: %d", c)
	}
}

func TestCLIDiffExitCodes(t *testing.T) {
	src, _ := os.ReadFile(lumen)
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	bump := write("bump.toml", strings.Replace(string(src), `version = "0.1.0"`, `version = "0.1.1"`, 1))
	wide := write("wide.toml", strings.Replace(string(src), `devices = ["gpu"]`, `devices = ["gpu", "camera"]`, 1))
	if c, _, _ := do("diff", lumen, bump); c != 0 {
		t.Errorf("bump: %d", c)
	}
	if c, out, _ := do("diff", lumen, wide); c != 3 || !strings.Contains(out, "REQUIRED") {
		t.Errorf("wide: %d %q", c, out)
	}
	bad := write("bad.toml", "[app]\n")
	if c, _, _ := do("diff", lumen, bad); c != 1 {
		t.Errorf("invalid input to diff must be an error, got %d", c)
	}
}
