package manifest

import (
	"strings"
	"testing"
)

// variant returns baseTOML with a replacement applied, plus optional extras.
func variant(t *testing.T, repl [][2]string, extra string) *Manifest {
	t.Helper()
	src := baseTOML
	for _, r := range repl {
		if !strings.Contains(src, r[0]) {
			t.Fatalf("base lacks %q", r[0])
		}
		src = strings.Replace(src, r[0], r[1], 1)
	}
	return mustLoad(t, src+extra)
}

func consent(t *testing.T, old, new *Manifest) bool {
	t.Helper()
	return Diff(old, new).NeedsConsent()
}

func TestConsentAddCapability(t *testing.T) {
	base := variant(t, nil, "")
	cases := map[string]*Manifest{
		"new network host": variant(t, [][2]string{{`["api.example.com"]`, `["api.example.com", "other.example.org"]`}}, ""),
		"new path":         variant(t, nil, "\n[[capabilities.paths]]\npath = \"~/Documents\"\naccess = \"read\"\n"),
		"new device":       variant(t, [][2]string{{"[capabilities]", "[capabilities]\ndevices = [\"camera\"]"}}, ""),
		"path read->write": variant(t, [][2]string{{`access = "read"`, `access = "write"`}}, ""),
	}
	for name, nw := range cases {
		if !consent(t, base, nw) {
			t.Errorf("%s: should require consent", name)
		}
	}
}

func TestConsentCoverage(t *testing.T) {
	base := variant(t, nil, "")
	// Narrower things already covered by existing grants: no consent.
	sub := variant(t, nil, "\n[[capabilities.paths]]\npath = \"~/Pictures/holiday\"\naccess = \"read\"\n")
	if consent(t, base, sub) {
		t.Error("sub-path with same access is covered by the parent grant")
	}
	port := variant(t, [][2]string{{`["api.example.com"]`, `["api.example.com:8443"]`}}, "")
	if consent(t, base, port) {
		t.Error("host with port is covered by old host without port")
	}
	// But a sibling prefix is not a sub-path.
	sib := variant(t, nil, "\n[[capabilities.paths]]\npath = \"~/Pictures2\"\naccess = \"read\"\n")
	if !consent(t, base, sib) {
		t.Error("~/Pictures2 is not under ~/Pictures")
	}
	// Dropping a port restriction widens.
	oldP := variant(t, [][2]string{{`["api.example.com"]`, `["api.example.com:8443"]`}}, "")
	if !consent(t, oldP, base) {
		t.Error("removing a port restriction widens access")
	}
	// Wildcards.
	wild := variant(t, [][2]string{{`["api.example.com"]`, `["*.example.com"]`}}, "")
	if consent(t, wild, base) {
		t.Error("api.example.com is covered by *.example.com")
	}
	if !consent(t, base, wild) {
		t.Error("widening a host to a wildcard needs consent")
	}
	apex := variant(t, [][2]string{{`["api.example.com"]`, `["example.com"]`}}, "")
	if !consent(t, wild, apex) {
		t.Error("*.example.com does not cover the apex example.com")
	}
}

func TestConsentEffectWidening(t *testing.T) {
	caps := "\ncapabilities = [\"net:api.example.com\"]\n"
	withEffect := func(e string) *Manifest {
		extra := ""
		if e == "destructive" || e == "external" {
			extra = caps
		}
		return variant(t, [][2]string{{`effect = "read"`, `effect = "` + e + `"`}}, extra)
	}
	cases := []struct {
		from, to string
		want     bool
	}{
		{"read", "write", true},
		{"write", "destructive", true},
		{"write", "external", true},
		{"read", "destructive", true},
		{"read", "external", true},
		{"destructive", "external", true}, // incomparable: treated as a change
		{"external", "destructive", true},
		{"write", "read", false},
		{"destructive", "write", false},
		{"external", "read", false},
		{"write", "write", false},
	}
	for _, c := range cases {
		if got := consent(t, withEffect(c.from), withEffect(c.to)); got != c.want {
			t.Errorf("%s -> %s: consent=%v want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestNoConsentVersionBump(t *testing.T) {
	old := variant(t, nil, "")
	nw := variant(t, [][2]string{{`version = "1.0.0"`, `version = "1.1.0"`}, {"sha256:0", "sha256:1"}}, "")
	r := Diff(old, nw)
	if r.NeedsConsent() {
		t.Errorf("pure version bump needs no consent: %+v", r.Changes)
	}
	if len(r.Changes) != 1 || !strings.Contains(r.Changes[0].Detail, "1.0.0 -> 1.1.0") {
		t.Errorf("version change should be reported as info: %+v", r.Changes)
	}
	if Diff(old, old).NeedsConsent() || len(Diff(old, old).Changes) != 0 {
		t.Error("identical manifests produce no changes")
	}
}

func TestNoConsentRemovals(t *testing.T) {
	rich := variant(t, nil, "\n[[capabilities.paths]]\npath = \"~/Documents\"\naccess = \"read\"\n")
	lean := variant(t, nil, "")
	if consent(t, rich, lean) {
		t.Error("removing a path needs no consent")
	}
	noNet := variant(t, [][2]string{{"network = [\"api.example.com\"]\n", ""}}, "")
	if consent(t, lean, noNet) {
		t.Error("removing a host needs no consent")
	}
	// write -> read narrowing of a path.
	w := variant(t, [][2]string{{`access = "read"`, `access = "write"`}}, "")
	if consent(t, w, lean) {
		t.Error("narrowing path access needs no consent")
	}
	// Removing a verb.
	two := variant(t, nil, "\n[[verb]]\nname = \"zap@v1\"\neffect = \"write\"\n")
	if consent(t, two, lean) {
		t.Error("removing a verb needs no consent")
	}
}

func TestConsentNewVerbs(t *testing.T) {
	old := variant(t, nil, "")
	if consent(t, old, variant(t, nil, "\n[[verb]]\nname = \"peek@v1\"\neffect = \"read\"\n")) {
		t.Error("a new read verb needs no consent")
	}
	if !consent(t, old, variant(t, nil, "\n[[verb]]\nname = \"zap@v1\"\neffect = \"write\"\n")) {
		t.Error("a new write verb needs consent")
	}
	// Renaming a verb's major version is a new verb.
	if !consent(t, old, variant(t, [][2]string{{"look@v1", "look@v2"}, {`effect = "read"`, `effect = "write"`}}, "")) {
		t.Error("look@v2 as write is a new, wider verb")
	}
	// A different app id is never an update.
	if !consent(t, old, variant(t, [][2]string{{"org.example.app", "org.example.other"}}, "")) {
		t.Error("changed app id needs consent")
	}
}

func TestConsentReasonsNameTheChange(t *testing.T) {
	old := variant(t, nil, "")
	nw := variant(t, [][2]string{{`effect = "read"`, `effect = "write"`}}, "\n[[capabilities.paths]]\npath = \"~/Documents\"\naccess = \"write\"\n")
	var details []string
	for _, c := range Diff(old, nw).Changes {
		if c.Consent {
			details = append(details, c.Detail)
		}
	}
	joined := strings.Join(details, "|")
	if len(details) != 2 || !strings.Contains(joined, "~/Documents") || !strings.Contains(joined, "look@v1 effect class widened read -> write") {
		t.Errorf("unexpected reasons: %v", details)
	}
}
