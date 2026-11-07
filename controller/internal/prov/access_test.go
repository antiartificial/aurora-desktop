package prov

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Every shape of grant the controller composes is labelled for what revoking
// it can do. None is "mediated", and no spelling of the label or its
// explanation says "lease": a bind-mounted file must not be describable by the
// word that promises what Hubris promises (decision 0019).
func TestNoGrantShapeIsLabelledEnforceable(t *testing.T) {
	for name, g := range map[string]Grant{
		"file ro": {Path: "/a", Mode: "ro"},
		"file rw": {Path: "/a", Mode: "rw"},
		"device":  {Path: "device:gpu", Mode: ModeUse, Device: "gpu"},
		"ui":      {Path: "ui:read", Mode: ModeUse, UI: "read", UIFor: "domain:x:1"},
	} {
		k := g.AccessKind()
		if k != AccessMounted && k != AccessUnclassified {
			t.Errorf("%s: access %q", name, k)
		}
		if strings.Contains(strings.ToLower(k+AccessNote(k)), "lease") {
			t.Errorf("%s: label or note mentions a lease: %q / %q", name, k, AccessNote(k))
		}
	}
	if g := (Grant{Path: "/a", Mode: "ro"}); g.AccessKind() != AccessMounted {
		t.Fatal("a file grant is a bind mount")
	}
}

// The label travels in the xattr a verifier reads, and an older record that
// has none says so instead of reading as a clean revocation.
func TestAccessLabelSurvivesIntoTheStoredRecord(t *testing.T) {
	f := filepath.Join(t.TempDir(), "o.png")
	os.WriteFile(f, []byte("x"), 0o644)
	r := Record{File: f, App: "a", Verb: "v@v1", Grants: []Grant{{Path: "/in", Mode: "ro", Cap: "cap:1", Access: AccessMounted}}, TS: "t"}
	if err := Write(f, r); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EPERM) {
			t.Skipf("filesystem has no user xattrs: %v", err)
		}
		t.Fatal(err)
	}
	got, err := Read(f)
	if err != nil || got.Grants[0].Access != AccessMounted {
		t.Fatalf("read back %+v err %v", got, err)
	}
	if !strings.Contains(AccessNote(""), "before decision 0019") {
		t.Fatal(AccessNote(""))
	}
}
