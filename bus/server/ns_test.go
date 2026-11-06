package server_test

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/server"
)

// startProc starts the (lazy-dialling) helper, optionally inside a real bwrap
// sandbox (new user, pid, ipc, uts and mount namespaces, as `domain run` makes
// them) and optionally created inside cgroup dir. It returns the helper and,
// for a sandbox, the pid of its init as the host sees it.
func startProc(t *testing.T, sock, cgdir string, sandbox bool) (*helperProc, int32) {
	t.Helper()
	var cmd *exec.Cmd
	var info *os.File
	if sandbox {
		bw, err := exec.LookPath("bwrap")
		if err != nil {
			t.Skip("no bwrap")
		}
		r, w, _ := os.Pipe()
		info = r
		defer w.Close()
		cmd = exec.Command(bw, "--info-fd", "3", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
			"--die-with-parent", "--cap-drop", "ALL", "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--", os.Args[0])
		cmd.ExtraFiles = []*os.File{w}
	} else {
		cmd = exec.Command(os.Args[0])
	}
	cmd.Env = []string{"AURORA_ATTEST_HELPER=" + sock, "AURORA_ATTEST_LAZY=1"}
	if cgdir != "" {
		fd, err := syscall.Open(cgdir, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
		if err != nil {
			t.Skip(err)
		}
		defer syscall.Close(fd)
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
	}
	w, _ := cmd.StdinPipe()
	r, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start: %v", err)
	}
	h := &helperProc{cmd, bufio.NewWriter(w), bufio.NewScanner(r), t}
	t.Cleanup(func() { w.Close(); cmd.Process.Kill(); cmd.Wait() })
	var pid int32
	if sandbox {
		sc := bufio.NewScanner(info)
		for sc.Scan() {
			if f := strings.Fields(sc.Text()); len(f) == 2 && f[0] == `"child-pid":` {
				n := 0
				for _, c := range strings.TrimSuffix(f[1], ",") {
					n = n*10 + int(c-'0')
				}
				pid = int32(n)
				break
			}
		}
		if pid == 0 {
			t.Skip("bwrap gave no child pid (cannot run here?)")
		}
	}
	if h.read() != "ready" {
		t.Skip("helper did not start (sandbox unusable here?)")
	}
	return h, pid
}

// nsDaemon is attestDaemon for a domain registered with namespace attestation.
func nsDaemon(t *testing.T, mode server.AttestMode, cgroup string) (*server.Server, *logs, string, string) {
	t.Helper()
	srv, lg, session, dsock := attestDaemonWith(t, mode, cgroup, true)
	return srv, lg, session, dsock
}

// delivered says whether a message sent by h after dialling reaches sub.
func delivered(t *testing.T, h *helperProc, sub *client.Client, topic string) bool {
	t.Helper()
	h.do("dial")
	h.do("send " + topic)
	select {
	case e := <-sub.Events():
		return e.Topic == topic
	case <-time.After(500 * time.Millisecond):
		return false
	}
}

func TestNSRealBwrapDomainProcessAccepted(t *testing.T) {
	base := cgroupBase(t)
	cdir, cproc := mkCgroup(t, base, "ns-ok")
	srv, lg, session, dsock := nsDaemon(t, server.AttestRequire, cproc)
	sub := dial(t, session)
	sub.Subscribe(ctx(t), "ns.*")
	h, init := startProc(t, dsock, cdir, true)
	if err := srv.AddNSRun(idA, init, nil); err != nil {
		t.Fatal(err)
	}
	if !delivered(t, h, sub, "ns.ok") {
		t.Fatalf("a real sandboxed domain process was not attested:\n%s", lg)
	}
	if !lg.has("signals cgroup=ok") || !lg.has("namespaces=ok") {
		t.Fatalf("log does not name both signals:\n%s", lg)
	}
}

// The regression test for the demonstrated hole: a same-uid host process that
// writes its pid into the domain's cgroup.procs and then connects.
func TestNSHostProcessThatJoinsTheCgroupRefused(t *testing.T) {
	base := cgroupBase(t)
	cdir, cproc := mkCgroup(t, base, "ns-join")

	// Before: a domain registered with a cgroup only accepts the joiner.
	_, _, session, dsock := attestDaemon(t, server.AttestRequire, cproc)
	sub := dial(t, session)
	sub.Subscribe(ctx(t), "join.*")
	h, _ := startProc(t, dsock, "", false)
	if h.do("move "+cdir) != "ok" {
		t.Skip("cannot join the cgroup here")
	}
	if !delivered(t, h, sub, "join.cgroup-only") {
		t.Fatal("control failed: the cgroup-only domain no longer accepts a process that joined the cgroup, so this test shows nothing")
	}

	// After: with namespace ids registered the same attack is refused, even
	// though a real run of the domain is live.
	odir, oproc := mkCgroup(t, base, "ns-join2")
	srv, lg, session2, dsock2 := nsDaemon(t, server.AttestRequire, oproc)
	sub2 := dial(t, session2)
	sub2.Subscribe(ctx(t), "join.*")
	_, init := startProc(t, dsock2, odir, true)
	if err := srv.AddNSRun(idA, init, nil); err != nil {
		t.Fatal(err)
	}
	h2, _ := startProc(t, dsock2, "", false)
	h2.do("move " + odir)
	if delivered(t, h2, sub2, "join.attack") {
		t.Fatalf("a host process that joined the cgroup spoke as the domain:\n%s", lg)
	}
	for _, want := range []string{"cgroup=ok", "namespaces=FAILED", "match none of the 1 live runs"} {
		if !lg.has(want) {
			t.Errorf("log lacks %q:\n%s", want, lg)
		}
	}
}

func TestNSOrdinaryHostProcessStillRefused(t *testing.T) {
	base := cgroupBase(t)
	odir, cproc := mkCgroup(t, base, "ns-host")
	srv, _, _, dsock := nsDaemon(t, server.AttestRequire, cproc)
	_, init := startProc(t, dsock, odir, true)
	if err := srv.AddNSRun(idA, init, nil); err != nil {
		t.Fatal(err)
	}
	kept, reply := hostConnect(t, dsock)
	if kept || !strings.Contains(reply, "attestation_failed") || !strings.Contains(reply, "cgroup=MISMATCH") {
		t.Fatalf("kept=%v reply=%q", kept, reply)
	}
}

func TestNSOffStillPermissive(t *testing.T) {
	_, lg, session, dsock := nsDaemon(t, server.AttestOff, "")
	sub := dial(t, session)
	sub.Subscribe(ctx(t), "off.*")
	dial(t, dsock).Publish("off.one", nil, "", false)
	if got := recv(t, sub.Events()); got.From != idA {
		t.Fatalf("from %q", got.From)
	}
	if !lg.has("attestation is OFF") {
		t.Fatalf("not loud:\n%s", lg)
	}
}

// A domain that wants namespace attestation but has no live run registered:
// even a process properly in the cgroup is refused (prefer and require).
func TestNSRequireRefusesWhenNamespaceIDsUnknown(t *testing.T) {
	base := cgroupBase(t)
	cdir, cproc := mkCgroup(t, base, "ns-unknown")
	for _, mode := range []server.AttestMode{server.AttestRequire, server.AttestPrefer} {
		_, lg, session, dsock := nsDaemon(t, mode, cproc)
		sub := dial(t, session)
		sub.Subscribe(ctx(t), "unk.*")
		h, _ := startProc(t, dsock, cdir, false)
		if delivered(t, h, sub, "unk.one") || !lg.has("namespace ids unknown") {
			t.Fatalf("mode %s: attested without namespace ids:\n%s", mode, lg)
		}
	}
}

// Destroy and recreate a domain: the new registration knows no runs, so the
// still-living old sandbox (same namespace inodes) is not accepted as it.
func TestNSRecreatedDomainDoesNotInheritRuns(t *testing.T) {
	base := cgroupBase(t)
	cdir, cproc := mkCgroup(t, base, "ns-recreate")
	srv, lg, session, dsock := nsDaemon(t, server.AttestRequire, cproc)
	sub := dial(t, session)
	sub.Subscribe(ctx(t), "re.*")
	h, init := startProc(t, dsock, cdir, true)
	if err := srv.AddNSRun(idA, init, nil); err != nil {
		t.Fatal(err)
	}
	if err := srv.RemoveDomain(idA); err != nil {
		t.Fatal(err)
	}
	if err := srv.AddDomainNS(idA, dsock, cproc); err != nil {
		t.Fatal(err)
	}
	if delivered(t, h, sub, "re.old") || !lg.has("namespace ids unknown") {
		t.Fatalf("recreated domain accepted the old sandbox:\n%s", lg)
	}
}
