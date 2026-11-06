package server_test

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/server"
)

// TestMain doubles as the helper process the attestation tests start inside a
// real kernel cgroup. Commands on stdin: "send TOPIC", "move CGROUP_DIR".
func TestMain(m *testing.M) {
	if sock := os.Getenv("AURORA_ATTEST_HELPER"); sock != "" {
		os.Exit(helper(sock))
	}
	os.Exit(m.Run())
}

func helper(sock string) int {
	c, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Println("dial:", err)
		return 1
	}
	defer c.Close()
	in := bufio.NewScanner(os.Stdin)
	fmt.Println("ready")
	for in.Scan() {
		f := strings.Fields(in.Text())
		switch f[0] {
		case "send":
			fmt.Fprintf(c, `{"kind":"event","topic":%q}`+"\n", f[1])
		case "move":
			if err := os.WriteFile(filepath.Join(f[1], "cgroup.procs"), []byte("0"), 0o644); err != nil {
				fmt.Println("move:", err)
				return 1
			}
		}
		fmt.Println("ok")
	}
	return 0
}

// cgroupBase is the user's systemd-delegated cgroup dir; tests skip without it.
func cgroupBase(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || !strings.Contains(string(b), "0::") {
		t.Skip("no cgroup v2")
	}
	path := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "0::"))
	want := fmt.Sprintf("user@%d.service", os.Getuid())
	acc := "/sys/fs/cgroup"
	for _, p := range strings.Split(strings.Trim(path, "/"), "/") {
		acc = filepath.Join(acc, p)
		if p == want {
			return acc
		}
	}
	t.Skip("no delegated cgroup subtree")
	return ""
}

// mkCgroup makes a real cgroup and returns its dir and /proc-form path.
func mkCgroup(t *testing.T, base, name string) (dir, proc string) {
	t.Helper()
	dir = filepath.Join(base, fmt.Sprintf("%s-%d", name, os.Getpid()))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Skipf("cannot create cgroup under %s: %v", base, err)
	}
	t.Cleanup(func() {
		for i := 0; i < 50 && os.Remove(dir) != nil; i++ {
			time.Sleep(20 * time.Millisecond)
		}
	})
	return dir, strings.TrimPrefix(dir, "/sys/fs/cgroup")
}

type helperProc struct {
	cmd *exec.Cmd
	in  *bufio.Writer
	out *bufio.Scanner
	t   *testing.T
}

// startIn runs the helper inside cgroup dir from its first instruction
// (clone3 CLONE_INTO_CGROUP), connected to sock.
func startIn(t *testing.T, dir, sock string) *helperProc {
	t.Helper()
	fd, err := syscall.Open(dir, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		t.Skip(err)
	}
	defer syscall.Close(fd)
	cmd := exec.Command(os.Args[0])
	cmd.Env = []string{"AURORA_ATTEST_HELPER=" + sock}
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
	w, _ := cmd.StdinPipe()
	r, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a process in the cgroup: %v", err)
	}
	h := &helperProc{cmd, bufio.NewWriter(w), bufio.NewScanner(r), t}
	t.Cleanup(func() { w.Close(); cmd.Process.Kill(); cmd.Wait() })
	if h.read() != "ready" {
		t.Fatal("helper did not connect")
	}
	return h
}

func (h *helperProc) read() string { h.out.Scan(); return h.out.Text() }
func (h *helperProc) do(cmd string) string {
	fmt.Fprintln(h.in, cmd)
	h.in.Flush()
	return h.read()
}

type logs struct {
	mu sync.Mutex
	b  []string
}

func (l *logs) f(format string, a ...any) {
	l.mu.Lock()
	l.b = append(l.b, fmt.Sprintf(format, a...))
	l.mu.Unlock()
}
func (l *logs) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Contains(strings.Join(l.b, "\n"), sub)
}
func (l *logs) String() string { l.mu.Lock(); defer l.mu.Unlock(); return strings.Join(l.b, "\n") }

func attestDaemon(t *testing.T, mode server.AttestMode, cgroup string) (*server.Server, *logs, string, string) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "ba")
	lg := &logs{}
	srv, err := server.New(server.Config{SocketPath: filepath.Join(dir, "s"), DataDir: filepath.Join(dir, "l"), EnforceCaps: true, AttestDomains: mode, Logf: lg.f})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close(); os.RemoveAll(dir) })
	dsock := filepath.Join(dir, "d")
	if err := srv.AddDomainCgroup(idA, dsock, cgroup); err != nil {
		t.Fatal(err)
	}
	return srv, lg, filepath.Join(dir, "s"), dsock
}

// hostConnect dials as this (host, non-domain) process and says whether the
// daemon kept the connection, plus any error line it was sent.
func hostConnect(t *testing.T, sock string) (kept bool, reply string) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	line, err := bufio.NewReader(c).ReadString('\n')
	return err != nil && os.IsTimeout(err), line
}

func TestAttestedDomainProcessAccepted(t *testing.T) {
	base := cgroupBase(t)
	cdir, cproc := mkCgroup(t, base, "attest-ok")
	_, lg, session, dsock := attestDaemon(t, server.AttestRequire, cproc)
	sub := dial(t, session)
	sub.Subscribe(ctx(t), "ok.*")
	h := startIn(t, cdir, dsock)
	h.do("send ok.one")
	if got := recv(t, sub.Events()); got.From != idA || got.Topic != "ok.one" {
		t.Fatalf("got %+v", got)
	}
	if !lg.has("attested pid") {
		t.Fatalf("no attestation logged:\n%s", lg)
	}
}

func TestHostProcessOnDomainSocketRefused(t *testing.T) {
	base := cgroupBase(t)
	_, cproc := mkCgroup(t, base, "attest-no")
	for _, mode := range []server.AttestMode{server.AttestPrefer, server.AttestRequire} {
		_, lg, _, dsock := attestDaemon(t, mode, cproc)
		kept, reply := hostConnect(t, dsock)
		if kept || !strings.Contains(reply, "attestation_failed") || !strings.Contains(reply, "REFUSED") {
			t.Fatalf("mode %s: kept=%v reply=%q", mode, kept, reply)
		}
		// the log says who, where they were, and where they should have been
		for _, want := range []string{fmt.Sprintf("pid %d", os.Getpid()), "uid", "comm", "is in cgroup", cproc, idA} {
			if !lg.has(want) {
				t.Errorf("mode %s: log lacks %q:\n%s", mode, want, lg)
			}
		}
	}
}

func TestAttestOffRestoresSocketIdentity(t *testing.T) {
	base := cgroupBase(t)
	_, cproc := mkCgroup(t, base, "attest-off")
	_, lg, session, dsock := attestDaemon(t, server.AttestOff, cproc)
	sub := dial(t, session)
	sub.Subscribe(ctx(t), "off.*")
	dial(t, dsock).Publish("off.one", nil, "", false)
	if got := recv(t, sub.Events()); got.From != idA {
		t.Fatalf("from = %q", got.From)
	}
	if !lg.has("attestation is OFF") || !lg.has("mode: off") {
		t.Fatalf("off mode not logged loudly:\n%s", lg)
	}
}

func TestRequireRefusesWithoutCgroup(t *testing.T) {
	_, lg, _, dsock := attestDaemon(t, server.AttestRequire, "")
	kept, reply := hostConnect(t, dsock)
	if kept || !strings.Contains(reply, "REQUIRED") {
		t.Fatalf("kept=%v reply=%q", kept, reply)
	}
	if !lg.has("no cgroup registered") {
		t.Fatalf("log:\n%s", lg)
	}
	// prefer degrades, loudly
	_, lg, _, dsock = attestDaemon(t, server.AttestPrefer, "")
	dial(t, dsock)
	time.Sleep(100 * time.Millisecond)
	if !lg.has("UNATTESTED") {
		t.Fatalf("prefer did not log the degrade:\n%s", lg)
	}
}

func TestLeavingTheCgroupDropsTheConnection(t *testing.T) {
	base := cgroupBase(t)
	cdir, cproc := mkCgroup(t, base, "attest-leave")
	odir, _ := mkCgroup(t, base, "attest-other")
	_, lg, session, dsock := attestDaemon(t, server.AttestRequire, cproc)
	sub := dial(t, session)
	sub.Subscribe(ctx(t), "leave.*")
	h := startIn(t, cdir, dsock)
	h.do("send leave.before")
	recv(t, sub.Events())
	if r := h.do("move " + odir); r != "ok" {
		t.Skipf("cannot migrate a process between cgroups here: %s", r)
	}
	time.Sleep(1200 * time.Millisecond) // recheck interval is 1s
	h.do("send leave.after")
	sub.Flush(ctx(t))
	select {
	case e := <-sub.Events():
		t.Fatalf("message from a process that left the cgroup was delivered: %+v", e)
	case <-time.After(300 * time.Millisecond):
	}
	if !lg.has("attestation LOST") {
		t.Fatalf("log:\n%s", lg)
	}
}
