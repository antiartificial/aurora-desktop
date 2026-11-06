package invoke

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/antiartificial/aurora/controller/internal/domain"
)

type fakeBus struct {
	mu      sync.Mutex
	adds    [][2]string
	cgroups []string
	removes []string
	addErr  error
}

func (b *fakeBus) AddDomain(id, path, cgroup string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.adds = append(b.adds, [2]string{id, path})
	b.cgroups = append(b.cgroups, cgroup)
	return b.addErr
}
func (b *fakeBus) RemoveDomain(id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.removes = append(b.removes, id)
	return nil
}

func invokeDemo(t *testing.T, iv *Invoker, dir string) (*Result, *Error) {
	t.Helper()
	in := filepath.Join(dir, "in.png")
	os.WriteFile(in, png, 0o644)
	return iv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: filepath.Join(dir, "out.png")})
}

func TestRegistersDomainWithAdapterIdentityAndSocket(t *testing.T) {
	iv, fk, dir, _, recs := setup(t)
	fb := &fakeBus{}
	iv.Bus = fb
	if _, e := invokeDemo(t, iv, dir); e != nil {
		t.Fatal(e)
	}
	name := DomainName("org.aurora.demo")
	wantID, wantSock, _ := fk.Identity(name)
	if len(fb.adds) != 1 || fb.adds[0] != [2]string{wantID, wantSock} {
		t.Fatalf("adds = %v, want [%s %s]", fb.adds, wantID, wantSock)
	}
	r := (*recs)[0]
	if r.Domain != wantID || !r.DomainAttested {
		t.Fatalf("provenance domain = %q attested=%v, want %q true", r.Domain, r.DomainAttested, wantID)
	}
	// second invoke does not register again
	if _, e := invokeDemo(t, iv, dir); e != nil {
		t.Fatal(e)
	}
	if len(fb.adds) != 1 {
		t.Fatalf("re-registered: %v", fb.adds)
	}
}

func TestTeardownUnregisters(t *testing.T) {
	iv, fk, dir, _, _ := setup(t)
	fb := &fakeBus{}
	iv.Bus = fb
	invokeDemo(t, iv, dir)
	id, _, _ := fk.Identity(DomainName("org.aurora.demo"))
	iv.Close()
	if len(fb.removes) != 1 || fb.removes[0] != id {
		t.Fatalf("removes = %v, want [%s]", fb.removes, id)
	}
	iv.Close() // idempotent
	if len(fb.removes) != 1 {
		t.Fatalf("removed twice: %v", fb.removes)
	}
	// after teardown the next invoke registers again
	invokeDemo(t, iv, dir)
	if len(fb.adds) != 2 {
		t.Fatalf("adds = %v", fb.adds)
	}
}

func TestRegistrationFailureDoesNotBreakInvoke(t *testing.T) {
	iv, _, dir, _, recs := setup(t)
	fb := &fakeBus{addErr: errors.New("forbidden: refused")}
	iv.Bus = fb
	res, e := invokeDemo(t, iv, dir)
	if e != nil || res == nil || !res.OK {
		t.Fatalf("invoke failed: %v", e)
	}
	r := (*recs)[0]
	if r.Domain == "" || r.DomainAttested {
		t.Fatalf("want identity named but not attested, got %q %v", r.Domain, r.DomainAttested)
	}
	// a refused registration is retried next time, not cached
	invokeDemo(t, iv, dir)
	if len(fb.adds) != 2 {
		t.Fatalf("adds = %v", fb.adds)
	}
}

func TestIdentityLookupFailureDoesNotBreakInvoke(t *testing.T) {
	iv, fk, dir, _, recs := setup(t)
	fk.IdentityErr = errors.New("domain exec-id: boom")
	iv.Bus = &fakeBus{}
	if _, e := invokeDemo(t, iv, dir); e != nil {
		t.Fatal(e)
	}
	if r := (*recs)[0]; r.Domain != "" || r.DomainAttested {
		t.Fatalf("got %+v", r)
	}
}

var _ domain.Runner = (*domain.Fake)(nil)

func TestRegistersDomainWithItsCgroupSoBusdCanAttestIt(t *testing.T) {
	iv, fk, dir, _, _ := setup(t)
	fk.CgroupPath = "/user.slice/aurora-domains/d1"
	fb := &fakeBus{}
	iv.Bus = fb
	if _, e := invokeDemo(t, iv, dir); e != nil {
		t.Fatal(e)
	}
	if len(fb.cgroups) != 1 || fb.cgroups[0] != "/user.slice/aurora-domains/d1" {
		t.Fatalf("registered with cgroups %q", fb.cgroups)
	}
}
