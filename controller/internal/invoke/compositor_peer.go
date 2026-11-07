package invoke

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// DefaultDenyCgroupComponent is the parent of every domain cgroup the adapter
// makes (adapter/linux/internal/dom/cgroup.go). A compositor is a host
// process; a peer inside this subtree is a sandboxed app.
const DefaultDenyCgroupComponent = "aurora-domains"

// PeerError means the process on the compositor socket failed the credential
// check. The request was NOT sent to it. Callers fall back to the bus route
// and log it loudly: it is either a misconfiguration or an impostor.
type PeerError struct {
	Socket string
	Reason string
}

func (e *PeerError) Error() string {
	return fmt.Sprintf("peer on %s failed the credential check: %s", e.Socket, e.Reason)
}

// Peer is what SO_PEERCRED and /proc say about the process that listen()ed
// on the socket we dialled.
type Peer struct {
	UID, PID int
	Cgroup   string // cgroup v2 path, "" if unreadable
	Exe      string // /proc/<pid>/exe target, informational
	Comm     string // informational
}

func (p Peer) String() string {
	return fmt.Sprintf("uid %d pid %d exe %q comm %q cgroup %q", p.UID, p.PID, p.Exe, p.Comm, p.Cgroup)
}

// peerOf reads the connection's peer credentials. SO_PEERCRED on a connected
// stream socket reports the credentials of the process that called listen().
func peerOf(conn net.Conn) (Peer, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return Peer{}, fmt.Errorf("not a unix connection")
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var cred *syscall.Ucred
	var serr error
	if err := rc.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if serr != nil {
		return Peer{}, serr
	}
	p := Peer{UID: int(cred.Uid), PID: int(cred.Pid)}
	root := "/proc/" + strconv.Itoa(p.PID)
	if b, err := os.ReadFile(root + "/cgroup"); err == nil {
		p.Cgroup = cgroupV2Path(string(b))
	}
	p.Exe, _ = os.Readlink(root + "/exe")
	if b, err := os.ReadFile(root + "/comm"); err == nil {
		p.Comm = strings.TrimSpace(string(b))
	}
	return p, nil
}

func cgroupV2Path(file string) string {
	for _, l := range strings.Split(file, "\n") {
		if p, ok := strings.CutPrefix(l, "0::"); ok {
			return p
		}
	}
	return ""
}

// checkPeer applies the policy. Fails closed: an unreadable cgroup refuses.
//
// What it rules out: another user's process, and any process confined in a
// domain (a sandboxed app cannot stand in for the compositor). What it does
// NOT rule out: another host process of the same user that bound the socket
// first. Same uid, same cgroup tree, and /proc/<pid>/exe is whatever that
// process chooses to exec. Exe and comm are logged, not trusted.
func (c *CompositorPrompt) checkPeer(p Peer) error {
	return checkPeerPolicy(c.Socket, p, c.ExpectedUID, c.DenyCgroupComponents, c.DenyCgroupPrefixes)
}

// checkPeerPolicy is checkPeer's rule for any socket whose other end must be a host process of one user:
// uid must match (nil: this process's own), the cgroup must be readable and not inside a domain.
func checkPeerPolicy(socket string, p Peer, expected *uint32, denyComp, denyPrefix []string) error {
	want := os.Geteuid()
	if expected != nil {
		want = int(*expected)
	}
	if p.UID != want {
		return &PeerError{socket, fmt.Sprintf("peer uid is %d, expected %d (pid %d)", p.UID, want, p.PID)}
	}
	if p.Cgroup == "" {
		return &PeerError{socket, fmt.Sprintf("cannot read the cgroup of pid %d, so cannot show it is outside a domain", p.PID)}
	}
	deny := denyComp
	if deny == nil {
		deny = []string{DefaultDenyCgroupComponent}
	}
	for _, comp := range strings.Split(p.Cgroup, "/") {
		for _, d := range deny {
			if comp == d {
				return &PeerError{socket, fmt.Sprintf("peer pid %d is inside a domain cgroup (%s)", p.PID, p.Cgroup)}
			}
		}
	}
	for _, pre := range denyPrefix {
		if p.Cgroup == pre || strings.HasPrefix(p.Cgroup, strings.TrimRight(pre, "/")+"/") {
			return &PeerError{socket, fmt.Sprintf("peer pid %d is inside a denied cgroup (%s)", p.PID, p.Cgroup)}
		}
	}
	return nil
}
