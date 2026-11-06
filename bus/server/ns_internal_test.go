package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func nsServer(t *testing.T) *Server {
	t.Helper()
	dir, _ := os.MkdirTemp("", "bn")
	s, err := New(Config{SocketPath: filepath.Join(dir, "s"), DataDir: filepath.Join(dir, "l"), EnforceCaps: true, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close(); os.RemoveAll(dir) })
	if err := s.AddDomainNS("domain:a:00112233", filepath.Join(dir, "a"), ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDomain("domain:b:00112233", filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	return s
}

func sandboxSleep(t *testing.T, secs string) (*exec.Cmd, int32) {
	t.Helper()
	bw, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("no bwrap")
	}
	r, w, _ := os.Pipe()
	defer w.Close()
	cmd := exec.Command(bw, "--info-fd", "3", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent",
		"--ro-bind", "/", "/", "--proc", "/proc", "--", "/usr/bin/sleep", secs)
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	buf := make([]byte, 64)
	n, _ := r.Read(buf)
	var pid int32
	if i := strings.Index(string(buf[:n]), `"child-pid": `); i >= 0 {
		for _, c := range string(buf[i+13 : n]) {
			if c < '0' || c > '9' {
				break
			}
			pid = pid*10 + int32(c-'0')
		}
	}
	if pid == 0 {
		t.Skip("bwrap gave no child pid")
	}
	t.Cleanup(func() { syscall.Kill(int(pid), syscall.SIGKILL) })
	return cmd, pid
}

func TestNSRegistrationRules(t *testing.T) {
	s := nsServer(t)
	const a, b = "domain:a:00112233", "domain:b:00112233"
	_, init := sandboxSleep(t, "30")
	// the daemon's own pid: it is in the daemon's namespaces, not a sandbox
	if err := s.AddNSRun(a, int32(os.Getpid()), nil); err == nil || !strings.Contains(err.Error(), "daemon's own") {
		t.Errorf("daemon-namespace pid accepted: %v", err)
	}
	if err := s.AddNSRun(a, 0x7ffffff0, nil); err == nil {
		t.Error("nonexistent pid accepted")
	}
	if err := s.AddNSRun(b, init, nil); err == nil || !strings.Contains(err.Error(), "not registered for namespace") {
		t.Errorf("domain without ns attestation took a run: %v", err)
	}
	if err := s.AddNSRun(a, init, map[string]uint64{"pid": 1, "mnt": 2, "ipc": 3, "uts": 4}); err == nil || !strings.Contains(err.Error(), "do not match") {
		t.Errorf("forged claimed ids accepted: %v", err)
	}
	ids, _ := readNS(init)
	if err := s.AddNSRun(a, init, map[string]uint64(ids)); err != nil {
		t.Fatalf("honest claim refused: %v", err)
	}
	if err := s.RemoveNSRun(a, init); err != nil {
		t.Fatal(err)
	}
	if got := s.doms[a].liveRunPIDs(); len(got) != 0 {
		t.Errorf("run not removed: %v", got)
	}
}

// A run is exactly as long-lived as its init pid: when the sandbox is gone the
// registration is pruned, so a later namespace that reuses the inode numbers
// finds nothing to match.
func TestNSDeadRunIsPruned(t *testing.T) {
	s := nsServer(t)
	const a = "domain:a:00112233"
	cmd, init := sandboxSleep(t, "30")
	if err := s.AddNSRun(a, init, nil); err != nil {
		t.Fatal(err)
	}
	if got := s.doms[a].liveRunPIDs(); len(got) != 1 {
		t.Fatalf("live runs %v", got)
	}
	syscall.Kill(int(init), syscall.SIGKILL) // the sandbox's init; killing bwrap's launcher would not end it
	cmd.Wait()
	for i := 0; i < 50 && len(s.doms[a].liveRunPIDs()) != 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.doms[a].liveRunPIDs(); len(got) != 0 {
		t.Fatalf("dead run still registered: %v", got)
	}
}

// An unreadable peer on a namespace-attested domain is refused in both modes.
func TestNSUnreadablePeerRefused(t *testing.T) {
	for _, mode := range []AttestMode{AttestPrefer, AttestRequire} {
		s := &Server{cfg: Config{AttestDomains: mode, Logf: func(string, ...any) {}}}
		d := &domainSock{identity: "domain:a:00112233", cgroup: "/some/cg", nsOn: true}
		a, err := s.admitDomain(nil, Cred{PID: 0x7ffffff0, UID: 1}, d)
		if err == nil || a != nil || !strings.Contains(err.Error(), "cannot read pid") {
			t.Fatalf("mode %s: a=%v err=%v", mode, a, err)
		}
	}
}
