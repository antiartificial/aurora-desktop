package dom

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// SockDir is the per-domain host directory bound into the domain at /run/aurora.
// A bus daemon listens on <SockDir>/bus.sock; whoever connects there is, by
// construction, a process that was given this directory, i.e. this domain.
func SockDir(name string) string {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = fmt.Sprintf("/tmp/aurora-%d", os.Getuid())
	}
	return filepath.Join(rt, "aurora", "domains", name)
}

// HasNet reports whether any net grant exists.
func HasNet(m *Meta) bool {
	for _, g := range m.Grants {
		if g.Kind == "net" {
			return true
		}
	}
	return false
}

// BwrapArgs builds the bubblewrap argument vector (without the command).
func (s Store) BwrapArgs(m *Meta) []string {
	a := []string{
		"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
		"--die-with-parent", "--new-session", "--cap-drop", "ALL", "--hostname", m.Name,
	}
	net := HasNet(m)
	if !net {
		a = append(a, "--unshare-net")
	}
	// minimal read-only system: /usr plus the merged-usr links
	a = append(a, "--ro-bind", "/usr", "/usr")
	for _, d := range []string{"bin", "sbin", "lib", "lib64"} {
		host := "/" + d
		if t, err := os.Readlink(host); err == nil {
			a = append(a, "--symlink", t, host)
		} else if st, err := os.Stat(host); err == nil && st.IsDir() {
			a = append(a, "--ro-bind", host, host)
		}
	}
	a = append(a, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp")
	etc := []string{"passwd", "group", "ld.so.cache", "nsswitch.conf"}
	if net {
		etc = append(etc, "resolv.conf", "hosts", "ssl", "ca-certificates", "pki")
	}
	for _, f := range etc {
		a = append(a, "--ro-bind-try", "/etc/"+f, "/etc/"+f)
	}
	for _, g := range m.Grants {
		if g.Kind != "path" {
			continue
		}
		flag := "--ro-bind"
		if g.Mode == "rw" {
			flag = "--bind"
		}
		a = append(a, flag, g.Path, g.Path)
	}
	for _, sd := range Subdirs {
		a = append(a, "--bind", filepath.Join(s.Dir(m.Name), sd), "/domain/"+sd)
	}
	a = append(a, "--bind", SockDir(m.Name), "/run/aurora")
	a = append(a, "--clearenv",
		"--setenv", "PATH", "/usr/bin:/bin",
		"--setenv", "HOME", "/domain/data",
		"--setenv", "XDG_DATA_HOME", "/domain/data",
		"--setenv", "XDG_CONFIG_HOME", "/domain/config",
		"--setenv", "XDG_CACHE_HOME", "/domain/cache",
		"--setenv", "AURORA_DOMAIN", m.Name,
		"--setenv", "AURORA_DOMAIN_ID", ExecID(m),
		"--setenv", "AURORA_BUS_SOCKET", "/run/aurora/bus.sock",
		"--setenv", "TERM", termOr(),
		"--chdir", "/domain/data")
	return a
}

func termOr() string {
	if t := os.Getenv("TERM"); t != "" {
		return t
	}
	return "dumb"
}

// Run executes argv inside the domain and returns its exit code.
// Warnings (e.g. unenforced limits) are written to stderr.
func (s Store) Run(name string, argv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	m, err := s.Load(name)
	if err != nil {
		return 0, err
	}
	if len(argv) == 0 {
		return 0, errors.New("no command given")
	}
	bw, err := exec.LookPath("bwrap")
	if err != nil {
		return 0, errors.New("bwrap not found in PATH")
	}
	if err := os.MkdirAll(SockDir(name), 0o700); err != nil {
		return 0, err
	}
	for _, g := range m.Grants {
		if g.Kind == "path" {
			if _, err := os.Stat(g.Path); err != nil {
				fmt.Fprintf(stderr, "domain: warning: granted path %s is not accessible: %v\n", g.Path, err)
			}
		}
	}
	cmd := exec.Command(bw, append(append(s.BwrapArgs(m), "--"), argv...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = []string{} // bwrap itself needs nothing; the domain env is set via --setenv
	if m.Limits != (Limits{}) {
		results := ApplyLimits(name, m.Limits)
		for _, r := range results {
			if !r.Applied {
				fmt.Fprintf(stderr, "domain: warning: %s limit NOT enforced: %s\n", r.Controller, r.Detail)
			}
		}
		if d, err := CgroupDir(name); err == nil {
			if fd, err := syscall.Open(d, syscall.O_RDONLY|syscall.O_DIRECTORY, 0); err == nil {
				defer syscall.Close(fd)
				cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
			}
		}
	}
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}
