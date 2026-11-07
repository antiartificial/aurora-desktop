package domain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CLI runner speaks `domain drop` / `domain undrop`: the arguments it passes and the one JSON line it reads back.
func TestCLIDropSpeaksTheDomainCommand(t *testing.T) {
	d := t.TempDir()
	log := filepath.Join(d, "args")
	bin := filepath.Join(d, "domain")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\ncase \"$1\" in drop) echo '{\"id\":\"3\",\"host\":\"'$3'\",\"guest\":\"/run/aurora/drops/3/a.png\",\"mode\":\"ro\",\"dir\":false}';; undrop) exit 0;; esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := CLI{Bin: bin}
	m, err := c.MountDrop("win-x", "/home/u/a.png", "ro")
	if err != nil || m.ID != "3" || m.Guest != "/run/aurora/drops/3/a.png" || m.Host != "/home/u/a.png" {
		t.Fatalf("%+v %v", m, err)
	}
	if err := c.UnmountDrop("win-x", "3"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(b)); got != "drop win-x /home/u/a.png --ro\nundrop win-x 3" {
		t.Fatalf("args: %q", got)
	}
	// a failing domain command, and an answer that is not the documented line
	os.WriteFile(bin, []byte("#!/bin/sh\necho no live run >&2; exit 1\n"), 0o755)
	if _, err := c.MountDrop("win-x", "/a", "ro"); err == nil || !strings.Contains(err.Error(), "no live run") {
		t.Fatalf("%v", err)
	}
	os.WriteFile(bin, []byte("#!/bin/sh\necho hello\n"), 0o755)
	if _, err := c.MountDrop("win-x", "/a", "ro"); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("%v", err)
	}
}
