package server_test

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/server"
)

// subIDs finds the invoking user's subuid range and the tools to use it, or skips.
func subIDs(t *testing.T) (start, count uint32) {
	t.Helper()
	for _, tool := range []string{"unshare", "newuidmap", "newgidmap", "setpriv", "bwrap", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not found")
		}
	}
	b, _ := os.ReadFile("/etc/subuid")
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Split(l, ":")
		if len(f) == 3 && (f[0] == strconv.Itoa(os.Getuid()) || f[0] == os.Getenv("USER")) {
			s, _ := strconv.ParseUint(f[1], 10, 32)
			c, _ := strconv.ParseUint(f[2], 10, 32)
			if c > 100 {
				return uint32(s), uint32(c)
			}
		}
	}
	t.Skip("no subuid range for this user")
	return
}

func mapFlags(start, count uint32) []string {
	return []string{"--user",
		fmt.Sprintf("--map-users=0:%d:1", os.Getuid()), fmt.Sprintf("--map-users=1:%d:%d", start, count),
		fmt.Sprintf("--map-groups=0:%d:1", os.Getgid()), fmt.Sprintf("--map-groups=1:%d:%d", start, count), "--"}
}

// startSub starts the helper the way `domain run` starts a payload: root of a
// fresh user namespace (unshare + newuidmap) execs bwrap, which execs setpriv,
// which becomes subuid `uid` (host uid) with only dac_override and fowner.
// It returns the helper, the sandbox init pid and the namespace's cgroup dir.
func startSub(t *testing.T, sock, cgdir string, uid uint32) (*helperProc, int32) {
	t.Helper()
	start, count := subIDs(t)
	in := uid - start + 1
	r, w, _ := os.Pipe()
	defer w.Close()
	args := append(mapFlags(start, count), "bwrap", "--info-fd", "3", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
		"--die-with-parent", "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev", "--",
		"setpriv", "--reuid", fmt.Sprint(in), "--regid", fmt.Sprint(in), "--clear-groups",
		"--bounding-set=-all,+dac_override,+fowner", "--inh-caps=-all,+dac_override,+fowner",
		"--ambient-caps=+dac_override,+fowner", "--no-new-privs", "--", os.Args[0])
	cmd := exec.Command("unshare", args...)
	cmd.ExtraFiles = []*os.File{w}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "AURORA_ATTEST_HELPER=" + sock, "AURORA_ATTEST_LAZY=1"}
	fd, err := syscall.Open(cgdir, syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		t.Skip(err)
	}
	defer syscall.Close(fd)
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: fd}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start: %v", err)
	}
	h := &helperProc{cmd, bufio.NewWriter(stdin), bufio.NewScanner(stdout), t}
	t.Cleanup(func() { stdin.Close(); cmd.Process.Kill(); cmd.Wait() })
	var pid int32
	for sc := bufio.NewScanner(r); sc.Scan(); {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[0] == `"child-pid":` {
			n, _ := strconv.Atoi(strings.TrimSuffix(f[1], ","))
			pid = int32(n)
			break
		}
	}
	if pid == 0 {
		t.Skip("no sandbox pid (user namespaces unusable here?)")
	}
	if h.read() != "ready" {
		t.Skip("helper did not start in the subuid sandbox")
	}
	return h, pid
}

// uidDomain: a daemon with the domain registered with cgroup, namespaces and uid.
func uidDomain(t *testing.T, mode server.AttestMode, cgroup string, ns bool, uid uint32) (*server.Server, *logs, *client.Client, string) {
	t.Helper()
	srv, lg, session, dsock := attestDaemonWith(t, mode, cgroup, ns)
	if err := srv.SetDomainUID(idA, uid); err != nil {
		t.Fatal(err)
	}
	sub := dial(t, session)
	sub.Subscribe(ctx(t), "demo.*")
	return srv, lg, sub, dsock
}

func sawSpoof(sub *client.Client) bool {
	select {
	case e := <-sub.Events():
		return e.Topic == "demo.spoof" && e.From == idA
	case <-time.After(700 * time.Millisecond):
		return false
	}
}

func atk(t *testing.T, pre []string, args ...string) string {
	t.Helper()
	argv := append(append(pre, "python3", "../demo-attacks.py"), args...)
	out, _ := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	return string(out)
}

func TestUIDRealSubuidDomainProcessAccepted(t *testing.T) {
	start, _ := subIDs(t)
	uid := start + 4242
	cdir, cproc := mkCgroup(t, cgroupBase(t), "uid-ok")
	srv, lg, sub, dsock := uidDomain(t, server.AttestRequire, cproc, true, uid)
	h, init := startSub(t, dsock, cdir, uid)
	if err := srv.AddNSRun(idA, init, nil); err != nil {
		t.Fatal(err)
	}
	sub.Subscribe(ctx(t), "ns.*")
	if !delivered(t, h, sub, "ns.ok") {
		t.Fatalf("a real subuid domain process was not attested:\n%s", lg)
	}
	if !lg.has(fmt.Sprintf(" uid %d comm", uid)) || !lg.has("uid=ok") {
		t.Fatalf("peer uid not the subuid %d or not named as a signal:\n%s", uid, lg)
	}
}

func TestUIDRefusesRealUIDHostProcessOnUIDAlone(t *testing.T) {
	start, _ := subIDs(t)
	// No cgroup, prefer mode: the only thing that can refuse is the uid.
	_, lg, _, dsock := uidDomain(t, server.AttestPrefer, "", false, start+7)
	kept, reply := hostConnect(t, dsock)
	if kept || !strings.Contains(reply, "uid=MISMATCH") || !strings.Contains(reply, "cgroup=not checked") {
		t.Fatalf("host process with the real uid was not refused on uid grounds: kept=%v reply=%q\n%s", kept, reply, lg)
	}
	// Control: the same daemon shape, registered with the real uid, accepts it
	// (loudly, unattested otherwise), so the refusal above was the uid.
	_, _, _, dsock2 := uidDomain(t, server.AttestPrefer, "", false, uint32(os.Getuid()))
	if kept, reply := hostConnect(t, dsock2); !kept {
		t.Fatalf("control: matching uid was refused: %q", reply)
	}
}

func TestUIDCgroupJoinAttackerStillRefused(t *testing.T) {
	start, _ := subIDs(t)
	uid := start + 4243
	odir, oproc := mkCgroup(t, cgroupBase(t), "uid-join")
	srv, lg, sub, dsock := uidDomain(t, server.AttestRequire, oproc, true, uid)
	_, init := startSub(t, dsock, odir, uid)
	if err := srv.AddNSRun(idA, init, nil); err != nil {
		t.Fatal(err)
	}
	h, _ := startProc(t, dsock, "", false)
	if h.do("move "+odir) != "ok" {
		t.Skip("cannot join the cgroup here")
	}
	sub.Subscribe(ctx(t), "join.*")
	if delivered(t, h, sub, "join.attack") {
		t.Fatalf("a host process that joined the cgroup spoke as the domain:\n%s", lg)
	}
	if !lg.has("uid=MISMATCH") {
		t.Errorf("not refused on uid:\n%s", lg)
	}
}

// 8f/8g, pinned as measured. A same-user attacker that enters the domain's
// namespaces but keeps its own uid is refused (on uid). One that also takes the
// domain's uid (it OWNS the user namespace) passes cgroup, namespaces and uid,
// and is refused ONLY by process ancestry: it forked after setns, so its
// parent chain leads to the host, not to the sandbox init.
func TestUIDSetnsAttacker(t *testing.T) {
	start, _ := subIDs(t)
	uid := start + 4244
	cdir, cproc := mkCgroup(t, cgroupBase(t), "uid-setns")
	srv, lg, sub, dsock := uidDomain(t, server.AttestRequire, cproc, true, uid)
	_, init := startSub(t, dsock, cdir, uid)
	if err := srv.AddNSRun(idA, init, nil); err != nil {
		t.Fatal(err)
	}
	out := atk(t, nil, "setns", dsock, idB, cdir, strconv.Itoa(int(init)))
	if strings.Contains(out, "could not enter") {
		t.Skipf("attacker cannot setns here: %s", out)
	}
	if sawSpoof(sub) {
		t.Fatalf("setns attacker that kept its own uid was accepted:\n%s\n%s", out, lg)
	}
	if !lg.has("uid=MISMATCH") {
		t.Errorf("expected refusal on uid:\n%s", lg)
	}
	out = atk(t, nil, "setns", dsock, idB, cdir, strconv.Itoa(int(init)), strconv.Itoa(int(uid-start+1)))
	if sawSpoof(sub) {
		t.Fatalf("setns + domain uid + fork was ACCEPTED despite the ancestry check:\n%s\n%s", out, lg)
	}
	if !lg.has("namespaces=ok uid=ok ancestry=FAILED") {
		t.Errorf("refused, but not on ancestry alone:\n%s", lg)
	}
}

// A same-user attacker cannot setuid to the domain's uid from the host, but
// can forge it in a user namespace of its own over the user's subuid range:
// uid alone is forgeable, and the namespaces refuse that attacker.
func TestUIDCannotBecomeDomainUIDFromHostButCanForgeIt(t *testing.T) {
	start, count := subIDs(t)
	uid := start + 4245
	if out := atk(t, nil, "setuid", strconv.Itoa(int(uid))); !strings.Contains(out, "not permitted") {
		t.Fatalf("host setuid to the domain uid: %s", out)
	}
	pre := append([]string{"unshare"}, mapFlags(start, count)...)
	forge := func(dsock string, extra ...string) string {
		return atk(t, pre, append([]string{"forge", dsock, idB, strconv.Itoa(int(uid - start + 1))}, extra...)...)
	}
	// uid is the only signal: the forger is accepted.
	_, lg, sub, dsock := uidDomain(t, server.AttestPrefer, "", false, uid)
	out := forge(dsock)
	if !sawSpoof(sub) {
		t.Fatalf("forged uid alone was not accepted (the README says it is):\n%s\n%s", out, lg)
	}
	// Full stack: it joins the cgroup and has the uid, but not the namespaces.
	cdir, cproc := mkCgroup(t, cgroupBase(t), "uid-forge")
	srv, lg2, sub2, dsock2 := uidDomain(t, server.AttestRequire, cproc, true, uid)
	_, init := startSub(t, dsock2, cdir, uid)
	if err := srv.AddNSRun(idA, init, nil); err != nil {
		t.Fatal(err)
	}
	out = forge(dsock2, cdir)
	if sawSpoof(sub2) {
		t.Fatalf("forged uid + joined cgroup passed the full stack:\n%s\n%s", out, lg2)
	}
	if !lg2.has("cgroup=ok") || !lg2.has("namespaces=FAILED") || !lg2.has("uid=ok") {
		t.Errorf("refusal not on namespaces:\n%s", lg2)
	}
}

func TestUIDDuplicateAcrossDomainsRejected(t *testing.T) {
	start, _ := subIDs(t)
	srv, _, _, _ := uidDomain(t, server.AttestPrefer, "", false, start+1)
	if err := srv.AddDomainCgroup("domain:other:00112233aabbccee", t.TempDir()+"/o", ""); err != nil {
		t.Fatal(err)
	}
	if err := srv.SetDomainUID("domain:other:00112233aabbccee", start+1); err == nil {
		t.Fatal("two domains registered with one uid")
	}
}
