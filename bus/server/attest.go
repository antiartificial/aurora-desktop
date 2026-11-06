package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// AttestMode says what the daemon does when a connection arrives on a domain
// socket. The zero value is AttestPrefer.
type AttestMode int

const (
	// AttestPrefer checks the peer's cgroup whenever the domain has a
	// registered cgroup and the host can report it; a mismatch is refused. If the
	// domain has no cgroup registered, or the host has no cgroup v2, the
	// connection is accepted on socket identity alone, with a loud log line.
	AttestPrefer AttestMode = iota
	// AttestRequire refuses every connection that cannot be attested.
	AttestRequire
	// AttestOff never checks: the socket alone is the identity (old behaviour).
	AttestOff
)

func (m AttestMode) String() string {
	return [...]string{"prefer", "require", "off"}[m]
}

// ParseAttestMode parses "require", "prefer" or "off".
func ParseAttestMode(s string) (AttestMode, error) {
	for _, m := range []AttestMode{AttestPrefer, AttestRequire, AttestOff} {
		if m.String() == s {
			return m, nil
		}
	}
	return 0, fmt.Errorf("attest mode %q: want require, prefer or off", s)
}

// errNoCgroup2 means the host (or this daemon's view of it) has no unified
// cgroup hierarchy line in /proc/<pid>/cgroup: attestation is impossible, as
// opposed to failed.
var errNoCgroup2 = errors.New("host has no cgroup v2 (no 0:: line in /proc/<pid>/cgroup)")

// recheckEvery bounds how often one connection's membership is re-read.
const recheckEvery = time.Second

// attestState is what the daemon keeps to re-verify a connection later.
type attestState struct {
	mu      sync.Mutex
	pid     int32
	pidfd   int // SO_PEERPIDFD, or -1 where the kernel lacks it
	start   uint64
	want    string
	ns      nsIDs // namespaces matched at admission; nil if not attested on them
	checked time.Time
}

func (a *attestState) close() {
	if a != nil && a.pidfd >= 0 {
		unix.Close(a.pidfd)
	}
}

// inCgroup: path is the cgroup or below it.
func inCgroup(path, want string) bool {
	return path == want || strings.HasPrefix(path, want+"/")
}

func readCgroup(pid int32) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/cgroup")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "0::") {
			return strings.TrimPrefix(l, "0::"), nil
		}
	}
	return "", errNoCgroup2
}

// procStart is field 22 of /proc/<pid>/stat (clock ticks since boot at which
// the process started). comm may hold spaces and ')', so count from the last ')'.
func procStart(pid int32) (uint64, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/stat")
	if err != nil {
		return 0, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, errors.New("malformed /proc stat")
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3
	if len(f) < 20 {
		return 0, errors.New("short /proc stat")
	}
	return strconv.ParseUint(f[19], 10, 64)
}

// peerPidfd returns SO_PEERPIDFD (Linux 6.5+), or -1 if unsupported.
func peerPidfd(nc net.Conn) int {
	uc, ok := nc.(*net.UnixConn)
	if !ok {
		return -1
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1
	}
	fd := -1
	raw.Control(func(f uintptr) {
		if v, err := unix.GetsockoptInt(int(f), unix.SOL_SOCKET, unix.SO_PEERPIDFD); err == nil {
			fd = v
		}
	})
	return fd
}

// alive reports whether the process behind the original SO_PEERPIDFD still
// exists (a zombie counts). Without a pidfd it compares start times.
func (a *attestState) alive() bool {
	if a.pidfd >= 0 {
		return unix.PidfdSendSignal(a.pidfd, 0, nil, 0) == nil
	}
	st, err := procStart(a.pid)
	return err == nil && st == a.start
}

// attest verifies that cred's process is inside want. It returns the state to
// re-verify later and the cgroup observed. PID reuse: the pidfd pins the exact
// process; the liveness check AFTER the /proc read proves the pid still named
// it during the read. Without SO_PEERPIDFD it falls back to comparing the
// process start time before and after (residual: a reuse inside that window
// that also reproduces start time to the tick, which needs a full pid wrap).
func attest(nc net.Conn, cred Cred, want string) (*attestState, string, error) {
	a := &attestState{pid: cred.PID, pidfd: peerPidfd(nc), want: want}
	fail := func(err error) (*attestState, string, error) { a.close(); return nil, "", err }
	st, err := procStart(cred.PID)
	if err != nil {
		return fail(fmt.Errorf("cannot read pid %d from /proc (exited, or not visible in this pid namespace): %w", cred.PID, err))
	}
	a.start = st
	got, err := readCgroup(cred.PID)
	if err != nil {
		return fail(err)
	}
	if !a.alive() {
		return fail(fmt.Errorf("pid %d went away during attestation; its identity cannot be pinned", cred.PID))
	}
	a.checked = time.Now()
	if !inCgroup(got, want) {
		return fail(&mismatch{got})
	}
	return a, got, nil
}

type mismatch struct{ got string }

func (m *mismatch) Error() string { return "peer is in cgroup " + strconv.Quote(m.got) }

// recheck re-reads the original connector's cgroup, at most once per
// recheckEvery. A connector that is still alive but outside the domain cgroup
// is a violation. A connector that has exited is not: its connection, accepted
// while attested, may legitimately live on in a child that inherited it.
func (a *attestState) recheck() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Since(a.checked) < recheckEvery {
		return nil
	}
	a.checked = time.Now()
	got, err := readCgroup(a.pid)
	if err != nil || !a.alive() {
		return nil
	}
	if !inCgroup(got, a.want) {
		return &mismatch{got}
	}
	if a.ns != nil {
		if now, err := readNS(a.pid); err == nil && !now.equal(a.ns) && a.alive() {
			return fmt.Errorf("peer namespaces changed: %s, were %s", now, a.ns)
		}
	}
	return nil
}

func peerComm(pid int32) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/comm")
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(b))
}

// admit is admitDomain for a domain known only by identity and cgroup (no
// namespace ids), kept for callers and tests that have no domainSock.
func (s *Server) admit(nc net.Conn, cred Cred, d string, cg string) (*attestState, error) {
	return s.admitDomain(nc, cred, &domainSock{identity: d, cgroup: cg})
}

// admitDomain decides one connection on domain socket d. A nil state with a nil
// error means "accepted without attestation" (already logged). Not for the
// session socket. Two signals are checked and both are named in the log:
// cgroup membership, then (if the domain registered namespace attestation)
// that the peer's namespaces are those of a live registered run.
func (s *Server) admitDomain(nc net.Conn, cred Cred, dom *domainSock) (*attestState, error) {
	d, cg := dom.identity, dom.cgroup
	who := fmt.Sprintf("pid %d uid %d comm %q", cred.PID, cred.UID, peerComm(cred.PID))
	degrade := func(why string) (*attestState, error) {
		if s.cfg.AttestDomains == AttestRequire {
			return nil, fmt.Errorf("attestation REQUIRED but impossible for %s on %s: %s", who, d, why)
		}
		s.cfg.Logf("WARNING: %s on %s accepted UNATTESTED (mode %s): %s", who, d, s.cfg.AttestDomains, why)
		return nil, nil
	}
	switch {
	case s.cfg.AttestDomains == AttestOff:
		return nil, nil
	case dom.uidOn && cred.UID != dom.uid:
		return nil, fmt.Errorf("attestation REFUSED for %s on %s: signals uid=MISMATCH (peer uid %d, domain uid is %d) cgroup=not checked namespaces=not checked; the socket is not the identity", who, d, cred.UID, dom.uid)
	case cg == "":
		return degrade("no cgroup registered for this domain")
	}
	a, got, err := attest(nc, cred, cg)
	var mm *mismatch
	switch {
	case errors.Is(err, errNoCgroup2):
		return degrade(err.Error())
	case errors.As(err, &mm):
		return nil, fmt.Errorf("attestation REFUSED for %s on %s: signals cgroup=MISMATCH (%v, domain cgroup is %q) namespaces=not checked uid=%s; the socket is not the identity", who, d, err, cg, uidSignal(dom))
	case err != nil:
		return nil, fmt.Errorf("attestation FAILED for %s on %s (mode %s): %w", who, d, s.cfg.AttestDomains, err)
	}
	if !dom.nsOn {
		s.cfg.Logf("WARNING: %s on %s: signals cgroup=ok namespaces=NOT CHECKED (no namespace ids registered for this domain): cgroup membership alone is forgeable by a same-uid process", who, d)
		s.cfg.Logf("attested %s on %s: cgroup %s (pidfd %v)", who, d, got, a.pidfd >= 0)
		return a, nil
	}
	ns, run, err := attestNS(a, cred.PID, dom)
	if err != nil {
		a.close()
		return nil, fmt.Errorf("attestation REFUSED for %s on %s (mode %s): signals cgroup=ok namespaces=FAILED uid=%s: %w", who, d, s.cfg.AttestDomains, uidSignal(dom), err)
	}
	a.ns = ns
	s.cfg.Logf("attested %s on %s: signals cgroup=ok (%s) namespaces=ok (%s, run init pid %d) uid=%s (pidfd %v)", who, d, got, ns, run.pid, uidSignal(dom), a.pidfd >= 0)
	return a, nil
}

func uidSignal(d *domainSock) string {
	if d.uidOn {
		return "ok"
	}
	return "not-registered"
}

// stillAttested re-verifies c (cgroup and, if attested on them, namespaces); on failure it logs and hangs up (no error line:
// the write loop may be mid-message).
func (s *Server) stillAttested(c *conn) bool {
	err := c.att.recheck()
	if err != nil {
		s.cfg.Logf("client %d (%s, pid %d): attestation LOST, disconnecting: %v; domain cgroup is %q", c.id, c.domain, c.cred.PID, err, c.att.want)
		c.nc.Close()
	}
	return err == nil
}
