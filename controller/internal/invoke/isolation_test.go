package invoke

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/controller/internal/domain"
)

// Pairs of valid app ids that the old mapping sent to the same domain.
var collidingPairs = [][2]string{
	{"org.aurora.lumen", "org.aurora-lumen"},
	{"org.aurora.lumen", "org-aurora.lumen"},
	{"org.aurora.lumen", "org-aurora-lumen"},
	{"a.b", "a-b"},
	{"com.x.app", "com.x-app"},
	{"org.verylongvendor.extremelylongapplicationname1", "org.verylongvendor.extremelylongapplicationname2"},
}

func TestCollidingIDsGetDistinctDomains(t *testing.T) {
	for _, p := range collidingPairs {
		a, b := DomainName(p[0]), DomainName(p[1])
		if a == b {
			t.Errorf("%q and %q both map to %s", p[0], p[1], a)
		}
	}
}

func TestDomainNameInjectiveAndValid(t *testing.T) {
	var ids []string
	segs := []string{"org", "com", "aurora", "lumen", "x", "a1", "a-1", "a_1", "long-vendor-name", "Lumen"}
	for _, a := range segs {
		for _, b := range segs {
			for _, sep1 := range []string{".", "-", "_"} {
				for _, sep2 := range []string{".", "-"} {
					ids = append(ids, a+sep1+b, a+sep1+b+sep2+"app", a+sep1+b+sep2+a)
				}
			}
		}
	}
	seen := map[string]string{}
	for _, id := range ids {
		n := DomainName(id)
		if len(n) > 32 || strings.Trim(n, "abcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
			t.Fatalf("%q -> invalid domain name %q", id, n)
		}
		if n != DomainName(id) {
			t.Fatalf("%q: not deterministic", id)
		}
		if prev, ok := seen[n]; ok && prev != id {
			t.Fatalf("%q and %q both map to %s", prev, id, n)
		}
		seen[n] = id
	}
	if len(seen) < 100 {
		t.Fatalf("only %d distinct ids exercised", len(seen))
	}
}

func TestIDsDifferingOnlyByDotVsDashGetDifferentDomainDirs(t *testing.T) {
	root := t.TempDir()
	d1, d2 := filepath.Join(root, DomainName("org.aurora.lumen")), filepath.Join(root, DomainName("org.aurora-lumen"))
	if d1 == d2 {
		t.Fatalf("same domain directory %s", d1)
	}
	if err := os.MkdirAll(filepath.Join(d1, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d2); err == nil {
		t.Fatal("second id's domain directory exists after creating the first's")
	}
}

func TestLockIsKeyedOnDomainName(t *testing.T) {
	inv := &Invoker{}
	for _, id := range []string{"org.aurora.lumen", "org.aurora-lumen", "org.aurora.demo"} {
		if inv.domainLock(DomainName(id)) != inv.domainLock(DomainName(id)) {
			t.Fatalf("%s: lock not stable for one domain", id)
		}
	}
	if inv.domainLock(DomainName("org.aurora.lumen")) == inv.domainLock(DomainName("org.aurora-lumen")) {
		t.Fatal("distinct domains share a lock")
	}
	// The key is the domain name: the same name always yields the same lock,
	// whatever produced it.
	if inv.domainLock("app-x") != inv.domainLock("app-x") {
		t.Fatal("lock not keyed on the name")
	}
	if len(inv.locks) != 4 {
		t.Fatalf("locks keyed by %v, want one per domain name", inv.locks)
	}
	for k := range inv.locks {
		if !strings.HasPrefix(k, "app-") {
			t.Fatalf("lock key %q is not a domain name", k)
		}
	}
}

// The path classified is byte-identical to the path handed to the grant call.
func TestClassifiedPathIsTheGrantedPath(t *testing.T) {
	inv, fk, dir, _, _ := setup(t)
	real, _ := filepath.EvalSymlinks(dir)
	os.WriteFile(filepath.Join(real, "in.png"), png, 0o644)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// both input and output are named through a symlinked directory
	res, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: link + "/in.png", Output: link + "/out.png"})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	d := DomainName("org.aurora.demo")
	for _, g := range res.Grants {
		want := fmt.Sprintf("grant %s %s %s", d, g.Path, g.Mode)
		found := false
		for _, c := range fk.Calls {
			found = found || c == want
		}
		if !found {
			t.Errorf("classified %q not handed to grant byte-for-byte; calls %q", g.Path, fk.Calls)
		}
		if strings.Contains(g.Path, "link") || g.Path != filepath.Clean(g.Path) {
			t.Errorf("grant path %q is not canonical", g.Path)
		}
	}
	if len(res.Grants) != 2 {
		t.Fatalf("grants %+v", res.Grants)
	}
}

func TestOutputFilePreparedAndSymlinkRefused(t *testing.T) {
	inv, fk, dir, _, _ := setup(t)
	in := filepath.Join(dir, "in.png")
	os.WriteFile(in, png, 0o644)
	victim := filepath.Join(dir, "victim.txt")
	os.WriteFile(victim, []byte("keep"), 0o644)
	out := filepath.Join(dir, "out.png")
	os.Symlink(victim, out)
	_, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: out})
	if e == nil || e.Code != "bad_output" {
		t.Fatalf("symlinked output accepted: %+v", e)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" || len(fk.Calls) != 0 {
		t.Fatalf("victim touched or domain used: %q %v", b, fk.Calls)
	}
	// a failed invoke removes an output file it created, but keeps a pre-existing one
	fk.OnRun = func([]string, []byte) domain.Result { return domain.Result{Exit: 1} }
	fresh := filepath.Join(dir, "fresh.png")
	if _, e := inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: fresh}); e == nil {
		t.Fatal("want failure")
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Fatal("failed invoke left the file it created")
	}
	old := filepath.Join(dir, "old.png")
	os.WriteFile(old, []byte("old"), 0o644)
	inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: old})
	if _, err := os.Stat(old); err != nil {
		t.Fatal("pre-existing output removed")
	}
}
