package server

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Process ancestry. A peer must descend, by real parent (/proc/<pid>/stat
// field 4), from the sandbox init pid of a live registered run. This stops a
// process that merely joined the domain's namespaces from outside (setns then
// fork): its parent chain leads to the host, never to the sandbox init. It does
// NOT stop a legitimate descendant that is made to act for someone else.
//
// PID reuse: along the chain each parent must have started no later than its
// child (a recycled pid starts after the process whose ppid names it), and the
// run's init pid must still have the registered start time.

// maxAncestry bounds the walk.
const maxAncestry = 64

type procInfo struct {
	ppid  int32
	start uint64
}

// readProc reads fields 4 (ppid) and 22 (start time) of /proc/<pid>/stat.
func readProc(pid int32) (procInfo, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/stat")
	if err != nil {
		return procInfo{}, err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return procInfo{}, errors.New("malformed /proc stat")
	}
	f := strings.Fields(s[i+1:]) // f[0] is field 3
	if len(f) < 20 {
		return procInfo{}, errors.New("short /proc stat")
	}
	pp, err := strconv.Atoi(f[1])
	if err != nil {
		return procInfo{}, err
	}
	st, err := strconv.ParseUint(f[19], 10, 64)
	return procInfo{int32(pp), st}, err
}

// descendsFrom reports whether pid is run.pid or has it as an ancestor.
func descendsFrom(pid int32, run *nsRun) error {
	cur, err := readProc(pid)
	if err != nil {
		return err
	}
	at := pid
	for depth := 0; depth < maxAncestry; depth++ {
		if at == run.pid {
			if cur.start != run.start {
				return fmt.Errorf("pid %d is the registered init pid but with a different start time (pid reuse)", at)
			}
			return nil
		}
		if cur.ppid <= 1 {
			return fmt.Errorf("parent chain of pid %d ends at pid %d without passing sandbox init pid %d", pid, at, run.pid)
		}
		par, err := readProc(cur.ppid)
		if err != nil {
			return fmt.Errorf("parent %d of %d unreadable: %w", cur.ppid, at, err)
		}
		if par.start > cur.start {
			return fmt.Errorf("parent %d of %d started after it: pid reuse in the chain", cur.ppid, at)
		}
		at, cur = cur.ppid, par
	}
	return fmt.Errorf("parent chain of pid %d deeper than %d", pid, maxAncestry)
}
