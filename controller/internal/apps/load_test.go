package apps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const good = `[app]
id = "org.example.%s"
name = "X"
version = "1.0.0"
image_hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
`

func put(t *testing.T, root, name, content string) {
	t.Helper()
	d := filepath.Join(root, name)
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "manifest.toml"), []byte(content), 0o644)
}

func TestBadManifestsSkippedWithReason(t *testing.T) {
	root := t.TempDir()
	put(t, root, "ok", strings.Replace(good, "%s", "ok", 1))
	put(t, root, "dup", strings.Replace(good, "%s", "ok", 1))
	put(t, root, "badid", strings.Replace(good, `org.example.%s`, "NotValid", 1))
	put(t, root, "garbage", "this is [not toml")
	put(t, root, "unknownkey", strings.Replace(good, "%s", "uk", 1)+"bogus = 1\n")
	os.MkdirAll(filepath.Join(root, "empty"), 0o755) // no manifest: not an app, not an error
	reg, skipped := Load(root, filepath.Join(root, "missing-dir"))
	if len(reg.Apps()) != 1 || reg.Apps()[0].ID() != "org.example.ok" {
		t.Fatalf("loaded %d apps", len(reg.Apps()))
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[filepath.Base(filepath.Dir(s.Path))+"|"+filepath.Base(s.Path)] = s.Reason
		if s.Reason == "" {
			t.Errorf("%s skipped without a reason", s.Path)
		}
	}
	if len(skipped) != 5 {
		t.Fatalf("want 5 skipped, got %d: %v", len(skipped), skipped)
	}
	for _, want := range []string{"duplicate app id", "app.id", "toml", "unknown key"} {
		found := false
		for _, r := range reasons {
			found = found || strings.Contains(r, want)
		}
		if !found {
			t.Errorf("no skip reason mentions %q: %v", want, reasons)
		}
	}
}

func TestRealAppsLoad(t *testing.T) {
	reg, skipped := Load("../../../apps")
	if len(skipped) != 0 {
		t.Fatalf("repo manifests skipped: %v", skipped)
	}
	if _, ok := reg.App("org.aurora.lumen"); !ok {
		t.Fatal("lumen not loaded")
	}
}
