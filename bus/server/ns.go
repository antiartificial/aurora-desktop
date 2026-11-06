package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Namespace attestation. Cgroup membership is forgeable by any same-uid host
// process (it can write its pid into cgroup.procs). Namespace membership is
// harder: a process started by `domain run` is in bwrap's new pid, mnt,
// ipc and uts namespaces (and a new user namespace, see nsTypes), and a process that only joined the cgroup is still in
// the host's. The kernel names a namespace by the inode of /proc/<pid>/ns/<type>.
//
// Identity is PER RUN: every `domain run` makes fresh namespaces (they cannot
// be pinned without CAP_SYS_ADMIN in the host mount namespace), so a domain
// has a set of live runs, each identified by the namespaces of its sandbox init
// pid. A registered run lives exactly as long as that init pid: once it is
// gone the run is pruned, so a recycled namespace inode number can never match.

// nsTypes are all compared; pid and mnt must also differ from the daemon's.
// pid and mnt are the sandbox's view of processes and files; ipc and uts are
// cheap extras. The USER namespace is deliberately not used: bwrap makes the
// payload's user namespace AFTER the point where the adapter can hold it (when
// it sets up /dev; observed: init's user ns inode changes between --block-fd
// and exec), so it is not known before the payload can connect. The other four
// are fixed at that point. A process outside the sandbox cannot have a
// sandbox's pid namespace as its OWN unless it forked after setns (see the
// README residuals).
var nsTypes = []string{"pid", "mnt", "ipc", "uts"}

type nsIDs map[string]uint64

func readNS(pid int32) (nsIDs, error) {
	ids := nsIDs{}
	for _, t := range nsTypes {
		st, err := os.Stat("/proc/" + strconv.Itoa(int(pid)) + "/ns/" + t)
		if err != nil {
			return nil, fmt.Errorf("cannot read /proc/%d/ns/%s: %w", pid, t, err)
		}
		ids[t] = st.Sys().(*syscall.Stat_t).Ino
	}
	return ids, nil
}

func (a nsIDs) equal(b nsIDs) bool {
	if len(a) != len(b) {
		return false
	}
	for t, v := range a {
		if b[t] != v {
			return false
		}
	}
	return true
}

func (a nsIDs) String() string {
	var out []string
	for _, t := range nsTypes {
		out = append(out, t+":"+strconv.FormatUint(a[t], 10))
	}
	return strings.Join(out, " ")
}

// nsRun is one sandbox run: init is its pid 1 (as seen from the host) and
// start that pid's start time, which proves the run is still the same process.
type nsRun struct {
	pid   int32
	start uint64
	ids   nsIDs
}

func (r *nsRun) live() bool {
	st, err := procStart(r.pid)
	return err == nil && st == r.start
}

// liveRuns prunes dead runs and returns the rest.
func (d *domainSock) liveRuns() []*nsRun {
	d.nsMu.Lock()
	defer d.nsMu.Unlock()
	keep := d.runs[:0]
	for _, r := range d.runs {
		if r.live() {
			keep = append(keep, r)
		}
	}
	d.runs = keep
	return append([]*nsRun(nil), keep...)
}

func (d *domainSock) liveRunPIDs() []int32 {
	var out []int32
	for _, r := range d.liveRuns() {
		out = append(out, r.pid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// AddDomainNS is AddDomainCgroup with namespace attestation on: a connection
// must also come from the namespaces of a run registered with AddNSRun.
func (s *Server) AddDomainNS(identity, path, cgroup string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addDomainLocked(identity, path, cgroup, true)
}

// AddNSRun registers the sandbox whose init process is pid as a live run of
// the domain. The daemon reads the namespace ids from /proc itself; claimed,
// if non-nil, must equal what it reads (a registrar that sends ids is checked,
// not trusted). Run it before the sandbox's payload can connect.
func (s *Server) AddNSRun(identity string, pid int32, claimed map[string]uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addNSRunLocked(identity, pid, claimed)
}

// RemoveNSRun forgets a run (idempotent).
func (s *Server) RemoveNSRun(identity string, pid int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.doms[identity]
	if d == nil {
		return fmt.Errorf("domain %s is not registered", identity)
	}
	d.dropRun(pid)
	return nil
}

func (d *domainSock) dropRun(pid int32) {
	d.nsMu.Lock()
	defer d.nsMu.Unlock()
	keep := d.runs[:0]
	for _, r := range d.runs {
		if r.pid != pid {
			keep = append(keep, r)
		}
	}
	d.runs = keep
}

func (s *Server) addNSRunLocked(identity string, pid int32, claimed map[string]uint64) error {
	d := s.doms[identity]
	switch {
	case d == nil:
		return fmt.Errorf("domain %s is not registered", identity)
	case !d.nsOn:
		return fmt.Errorf("domain %s was not registered for namespace attestation", identity)
	}
	start, err := procStart(pid)
	if err != nil {
		return fmt.Errorf("run init pid %d: %w (gone, or not visible in the daemon's pid namespace)", pid, err)
	}
	ids, err := readNS(pid)
	if err != nil {
		return err
	}
	if st2, err := procStart(pid); err != nil || st2 != start {
		return fmt.Errorf("run init pid %d changed while being read", pid)
	}
	if claimed != nil && !nsIDs(claimed).equal(ids) {
		return fmt.Errorf("claimed namespaces %s do not match what /proc shows for pid %d: %s", nsIDs(claimed), pid, ids)
	}
	self, err := readNS(int32(os.Getpid()))
	if err != nil {
		return fmt.Errorf("daemon cannot read its own namespaces: %w", err)
	}
	for _, t := range []string{"pid", "mnt"} {
		if ids[t] == self[t] {
			return fmt.Errorf("pid %d is in the daemon's own %s namespace: that is not a sandbox, every host process would match", pid, t)
		}
	}
	for _, o := range s.doms {
		if o == d {
			continue
		}
		for _, r := range o.liveRuns() {
			if r.ids["pid"] == ids["pid"] || r.ids["mnt"] == ids["mnt"] {
				return fmt.Errorf("pid %d shares a pid or mount namespace with a live run of %s: one process would be both", pid, o.identity)
			}
		}
	}
	d.dropRun(pid)
	d.nsMu.Lock()
	defer d.nsMu.Unlock()
	d.runs = append(d.runs, &nsRun{pid, start, ids})
	s.cfg.Logf("domain %s: registered run init pid %d, namespaces %s", identity, pid, ids)
	return nil
}

// attestNS reads the peer's namespaces and requires them to equal a live
// registered run's. The pid is pinned by the same pidfd liveness check as the
// cgroup read. It returns the matched ids and run.
func attestNS(a *attestState, pid int32, d *domainSock) (nsIDs, *nsRun, error) {
	ids, err := readNS(pid)
	if err != nil {
		return nil, nil, fmt.Errorf("%w (peer exited, not visible in the daemon's pid namespace, or not readable by the daemon)", err)
	}
	if !a.alive() {
		return nil, nil, fmt.Errorf("pid %d went away during namespace attestation", pid)
	}
	runs := d.liveRuns()
	if len(runs) == 0 {
		return nil, nil, errors.New("namespace ids unknown: no live sandbox run of this domain is registered")
	}
	for _, r := range runs {
		if ids.equal(r.ids) {
			return ids, r, nil
		}
	}
	return nil, nil, fmt.Errorf("peer namespaces (%s) match none of the %d live runs of this domain (e.g. %s)", ids, len(runs), runs[0].ids)
}

// ctlDomainNS registers or removes a run: {"identity","pid","ns":{type:inode},"remove":bool}.
func (s *Server) ctlDomainNS(c *conn, raw json.RawMessage) (any, error) {
	if c.viaDomain {
		return nil, cerr("forbidden", "domain sockets are managed from the session socket only")
	}
	var p struct {
		Identity string
		PID      int32
		NS       map[string]uint64
		Remove   bool
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Identity == "" || p.PID <= 0 {
		return nil, errors.New(`needs {"identity":"domain:<name>:<id>","pid":<sandbox init pid>,"ns":{"user":<inode>,...} (optional, checked),"remove":<bool>}`)
	}
	if p.Remove {
		d := s.doms[p.Identity]
		if d == nil {
			return nil, fmt.Errorf("domain %s is not registered", p.Identity)
		}
		d.dropRun(p.PID)
		return map[string]int32{"removed": p.PID}, nil
	}
	return map[string]int32{"added": p.PID}, s.addNSRunLocked(p.Identity, p.PID, p.NS)
}
