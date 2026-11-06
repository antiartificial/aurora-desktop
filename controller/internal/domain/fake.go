package domain

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// Fake records every call, for tests. OnRun may produce side effects.
type Fake struct {
	// CgroupPath is what Cgroup reports for every domain.
	CgroupPath  string
	mu          sync.Mutex
	Calls       []string
	Unusable    error                                    // if set, Available returns it
	OnRun       func(argv []string, stdin []byte) Result // default: exit 0, no output
	GrantErr    error
	IdentityErr error
}

func (f *Fake) rec(format string, a ...any) {
	f.mu.Lock()
	f.Calls = append(f.Calls, fmt.Sprintf(format, a...))
	f.mu.Unlock()
}

func (f *Fake) Available() error { return f.Unusable }
func (f *Fake) Ensure(n string) error {
	f.rec("ensure %s", n)
	return nil
}
func (f *Fake) InstallImage(n, src, exe string) (string, error) {
	f.rec("install %s", n)
	return "/domain/data/app/" + filepath.Base(exe), nil
}

// Identity mimics `domain exec-id` / `domain sockpath` with a fixed shape.
func (f *Fake) Identity(n string) (string, string, error) {
	if f.IdentityErr != nil {
		return "", "", f.IdentityErr
	}
	return "domain:" + n + ":00112233aabbccdd", "/fake/run/" + n + "/bus.sock", nil
}

// Cgroup returns Fake.CgroupPath ("" = none, the domain registers unattested).
func (f *Fake) Cgroup(n string) (string, error) { return f.CgroupPath, nil }
func (f *Fake) Grant(n, p, m string) error {
	f.rec("grant %s %s %s", n, p, m)
	return f.GrantErr
}
func (f *Fake) Revoke(n, p string) error {
	f.rec("revoke %s %s", n, p)
	return nil
}
func (f *Fake) Run(_ context.Context, n string, argv []string, stdin []byte) (Result, error) {
	f.rec("run %s %s", n, strings.Join(argv, " "))
	if f.OnRun != nil {
		return f.OnRun(argv, stdin), nil
	}
	return Result{}, nil
}
