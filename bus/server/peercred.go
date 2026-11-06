package server

import (
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
)

// Cred is what the kernel says about the process on the other end.
type Cred struct {
	PID int32
	UID uint32
	GID uint32
}

// peerCred reads SO_PEERCRED. This is the ONLY source of sender identity.
func peerCred(c net.Conn) (Cred, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return Cred{}, fmt.Errorf("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return Cred{}, err
	}
	var cred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return Cred{}, err
	}
	if serr != nil {
		return Cred{}, serr
	}
	return Cred{PID: cred.Pid, UID: cred.Uid, GID: cred.Gid}, nil
}

// DefaultDomain is the MVP stand-in for Phase 2's attested domain id:
// "uid<uid>:<process name>", both read from the kernel (SO_PEERCRED and
// /proc/<pid>/comm). Real domains replace this resolver later.
func DefaultDomain(c Cred) string {
	comm := fmt.Sprintf("pid%d", c.PID)
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", c.PID)); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			comm = s
		}
	}
	return fmt.Sprintf("uid%d:%s", c.UID, comm)
}
