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

// BwrapArgs builds the bubblewrap argument vector (without the command). With
// ownUID, bwrap is already running as root of the domain's user namespace (see
// uid.go): it makes no user namespace of its own, and the payload drops to the
// domain's subuid via setpriv instead of bwrap's --cap-drop.
func (s Store) BwrapArgs(m *Meta, ownUID bool) []string {
	a := []string{"--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
		"--die-with-parent", "--new-session", "--hostname", m.Name}
	if !ownUID {
		a = append(a, "--unshare-user", "--cap-drop", "ALL")
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
	devArgs, devEnv := DeviceArgs(m)
	a = append(a, devArgs...)
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
	return append(a, devEnv...)
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
	if stderr == nil {
		stderr = io.Discard
	}
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
	for _, w := range DeviceWarnings(m) {
		fmt.Fprintf(stderr, "domain: warning: %s\n", w)
	}
	// --info-fd reports the sandbox init pid as soon as its namespaces exist;
	// --block-fd holds the payload until we have recorded (and, with a bus,
	// registered) those namespaces. fds 3 and 4 in the child.
	infoR, infoW, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	blockR, blockW, err := os.Pipe()
	if err != nil {
		return 0, err
	}
	defer infoR.Close()
	defer blockW.Close()
	own, why := false, s.AssignUID(m)
	if why == nil {
		why = subTools()
	}
	if why != nil {
		fmt.Fprintf(stderr, "domain: warning: running WITHOUT its own uid (shares the host user's): %v\n", why)
	} else {
		own = true
	}
	args := append([]string{"--info-fd", "3", "--block-fd", "4"}, s.BwrapArgs(m, own)...)
	full := append(append([]string{bw}, args...), "--")
	if own {
		pre, err1 := usernsPrefix(m)
		drop, err2 := privDrop(m)
		if err1 != nil || err2 != nil {
			return 0, fmt.Errorf("per-domain uid: %v %v", err1, err2)
		}
		// unshare maps the ids and execs bwrap as namespace root; bwrap execs
		// setpriv in the sandbox, which becomes the subuid and execs the payload.
		full = append(append(pre, full...), drop...)
	}
	full = append(full, argv...)
	cmd := exec.Command(full[0], full[1:]...)
	cmd.ExtraFiles = []*os.File{infoW, blockR}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.Env = []string{"PATH=/usr/bin:/bin"} // unshare finds newuidmap; the domain env is set via --setenv
	if m.Limits != (Limits{}) {
		for _, r := range ApplyLimits(name, m.Limits) {
			if !r.Applied {
				fmt.Fprintf(stderr, "domain: warning: %s limit NOT enforced: %s\n", r.Controller, r.Detail)
			}
		}
	}
	// Every run goes into the domain's own cgroup, limits or not: this is what
	// lets a bus daemon attest that a connecting pid is inside the domain.
	if d, err := EnsureCgroup(name); err != nil {
		fmt.Fprintf(stderr, "domain: warning: running WITHOUT a domain cgroup, the bus cannot attest this process: %v\n", err)
	} else if fd, err := syscall.Open(d, syscall.O_RDONLY|syscall.O_DIRECTORY, 0); err != nil {
		fmt.Fprintf(stderr, "domain: warning: running WITHOUT a domain cgroup, the bus cannot attest this process: %v\n", err)
	} else {
		defer syscall.Close(fd)
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
	}
	err = cmd.Start()
	infoW.Close()
	blockR.Close()
	if err != nil {
		return 0, err
	}
	unregister := s.recordRun(name, infoR, stderr)
	blockW.Close() // release the payload
	err = cmd.Wait()
	unregister()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}
