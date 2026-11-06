// Package domain drives the adapter/linux `domain` CLI.
package domain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Result is what a command run inside a domain produced.
type Result struct {
	Stdout []byte
	Stderr []byte
	Exit   int
}

// Runner is the controller's view of domain tooling. The real one shells out
// to `domain`; tests inject a fake that records calls.
type Runner interface {
	// Available returns nil if domains can be used, else the reason not.
	Available() error
	// Ensure creates the domain if it does not exist.
	Ensure(name string) error
	// InstallImage copies the app directory, and the executable if it lives
	// elsewhere, into the domain's own storage and returns the executable's
	// path as seen inside the domain.
	InstallImage(name, srcDir, exe string) (string, error)
	// Identity returns the domain's attested bus identity (`domain exec-id`)
	// and the host path of its bus socket (`domain sockpath`).
	Identity(name string) (id, sock string, err error)
	// Cgroup returns the domain's cgroup in /proc/<pid>/cgroup form (`domain cgroup`).
	Cgroup(name string) (string, error)
	Grant(name, path, mode string) error // mode "ro" or "rw"
	Revoke(name, path string) error
	// GrantDevice puts a device class ("gpu", "wayland") into the domain's
	// next run; RevokeDevice takes it out. See decision 0014.
	GrantDevice(name, class string) error
	RevokeDevice(name, class string) error
	Run(ctx context.Context, name string, argv []string, stdin []byte) (Result, error)
}

// CLI shells out to the `domain` binary.
type CLI struct {
	Bin string // path to the domain binary
}

// Locate finds the domain binary: explicit path, $AURORA_DOMAIN_BIN, PATH,
// then adapter/linux/out/domain next to the source tree (found from repoHint
// or the working directory upwards). Returns "" if none.
func Locate(explicit, repoHint string) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("AURORA_DOMAIN_BIN"); p != "" {
		return p
	}
	if p, err := exec.LookPath("domain"); err == nil {
		return p
	}
	for _, start := range []string{repoHint, "."} {
		if start == "" {
			continue
		}
		dir, err := filepath.Abs(start)
		if err != nil {
			continue
		}
		for ; ; dir = filepath.Dir(dir) {
			c := filepath.Join(dir, "adapter", "linux", "out", "domain")
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c
			}
			if dir == filepath.Dir(dir) {
				break
			}
		}
	}
	return ""
}

func (c CLI) Available() error {
	if c.Bin == "" {
		return errors.New("domain binary not found (set --domain-bin or AURORA_DOMAIN_BIN, or build adapter/linux: go build -o out/domain ./cmd/domain)")
	}
	if _, err := os.Stat(c.Bin); err != nil {
		return fmt.Errorf("domain binary: %w", err)
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		return errors.New("bwrap not found in PATH")
	}
	return nil
}

func (c CLI) do(ctx context.Context, stdin []byte, args ...string) (Result, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	res := Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.Exit = ee.ExitCode()
		return res, nil
	}
	return res, err
}

func (c CLI) must(args ...string) error {
	res, err := c.do(context.Background(), nil, args...)
	if err != nil {
		return err
	}
	if res.Exit != 0 {
		return fmt.Errorf("domain %s: %s", strings.Join(args, " "), strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

func (c CLI) Ensure(name string) error {
	res, err := c.do(context.Background(), nil, "create", name)
	if err != nil {
		return err
	}
	if res.Exit != 0 && !strings.Contains(string(res.Stderr), "already exists") {
		return fmt.Errorf("domain create %s: %s", name, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

func (c CLI) Identity(name string) (string, string, error) {
	out := func(verb string) (string, error) {
		res, err := c.do(context.Background(), nil, verb, name)
		if err != nil {
			return "", err
		}
		if res.Exit != 0 {
			return "", fmt.Errorf("domain %s %s: %s", verb, name, strings.TrimSpace(string(res.Stderr)))
		}
		return strings.TrimSpace(string(res.Stdout)), nil
	}
	id, err := out("exec-id")
	if err != nil {
		return "", "", err
	}
	sock, err := out("sockpath")
	if err != nil {
		return "", "", err
	}
	return id, sock, nil
}

func (c CLI) Cgroup(name string) (string, error) {
	res, err := c.do(context.Background(), nil, "cgroup", name)
	if err != nil {
		return "", err
	}
	if res.Exit != 0 {
		return "", fmt.Errorf("domain cgroup %s: %s", name, strings.TrimSpace(string(res.Stderr)))
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

func (c CLI) Grant(name, path, mode string) error {
	// --exact: the controller resolved and authorised this very path; the
	// domain must bind it as given, not resolve it a second time.
	return c.must("grant", name, path, "--"+mode, "--exact")
}

func (c CLI) Revoke(name, path string) error { return c.must("revoke", name, path) }

func (c CLI) GrantDevice(name, class string) error {
	return c.must("grant", name, "--device", class)
}

func (c CLI) RevokeDevice(name, class string) error {
	return c.must("revoke", name, "--device", class)
}

func (c CLI) Run(ctx context.Context, name string, argv []string, stdin []byte) (Result, error) {
	return c.do(ctx, stdin, append([]string{"run", name, "--"}, argv...)...)
}

// Root mirrors the domain CLI's data dir choice.
func Root() string {
	if d := os.Getenv("AURORA_DOMAINS_DIR"); d != "" {
		return d
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "aurora", "domains")
}

// InstallImage puts the app's files in <domain>/data/app, which the domain
// sees as /domain/data/app. The app's code is part of the domain, not a grant.
func (c CLI) InstallImage(name, srcDir, exe string) (string, error) {
	dst := filepath.Join(Root(), name, "data", "app")
	if err := os.RemoveAll(dst); err != nil {
		return "", err
	}
	if err := CopyTree(srcDir, dst); err != nil {
		return "", err
	}
	base := filepath.Base(exe)
	if err := copyFile(exe, filepath.Join(dst, base), 0o755); err != nil {
		return "", err
	}
	return "/domain/data/app/" + base, nil
}

// InstallHelper copies the helper binary src into <domain>/data/helpers and
// returns its path inside the domain. It is not an app grant: it is code the
// controller runs in the domain to publish on the domain's own bus socket.
func (c CLI) InstallHelper(name, src string) (string, error) {
	dst := filepath.Join(Root(), name, "data", "helpers")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	base := filepath.Base(src)
	if err := copyFile(src, filepath.Join(dst, base), 0o755); err != nil {
		return "", err
	}
	return "/domain/data/helpers/" + base, nil
}
