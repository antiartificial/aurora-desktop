package server

import (
	"strings"
	"testing"
)

// A peer whose pid /proc cannot show (gone, or another pid namespace) is
// unattestable: refused in prefer and require alike, never waved through.
func TestUnreadablePeerRefused(t *testing.T) {
	for _, mode := range []AttestMode{AttestPrefer, AttestRequire} {
		s := &Server{cfg: Config{AttestDomains: mode, Logf: func(string, ...any) {}}}
		a, err := s.admit(nil, Cred{PID: 0x7ffffff0, UID: 1}, "domain:a:00112233", "/some/cg")
		if err == nil || a != nil || !strings.Contains(err.Error(), "cannot read pid") {
			t.Fatalf("mode %s: a=%v err=%v", mode, a, err)
		}
	}
}

func TestParseAttestMode(t *testing.T) {
	for _, s := range []string{"require", "prefer", "off"} {
		if m, err := ParseAttestMode(s); err != nil || m.String() != s {
			t.Errorf("%s: %v %v", s, m, err)
		}
	}
	if _, err := ParseAttestMode("yes"); err == nil {
		t.Error("junk accepted")
	}
	if AttestMode(0) != AttestPrefer {
		t.Error("zero value must be prefer")
	}
}

func TestCgroupRegistrationRules(t *testing.T) {
	for _, bad := range []string{"/", "rel/x", "/a/../b", "/a/"} {
		if checkCgroup(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if !inCgroup("/a/b/c", "/a/b") || inCgroup("/a/bc", "/a/b") || !inCgroup("/a/b", "/a/b") {
		t.Error("inCgroup prefix logic wrong")
	}
}
