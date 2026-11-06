package dom

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// NSTypes are the namespaces a run is identified by. bwrap creates new pid,
// mount, ipc and uts namespaces for every run; the kernel names each by the
// inode of /proc/<pid>/ns/<type>. The user namespace is NOT included: bwrap
// replaces the sandbox's user namespace after --block-fd releases it (when it
// sets up /dev), so its inode at the point we can register is not the
// payload's. pid, mnt, ipc and uts do not change at that step (verified).
var NSTypes = []string{"pid", "mnt", "ipc", "uts"}

// NSRecord is the namespace identity of the domain's most recent `domain run`
// (kept in domain.json). Identity is PER RUN: the namespaces die with the run
// and cannot be pinned without privilege, so the record is only meaningful
// while PID (the sandbox's init, as seen from the host) is alive with the same
// start time. `domain nsid` checks exactly that, so a stale record is never
// printed and a recycled namespace inode cannot masquerade as the domain.
type NSRecord struct {
	PID   int32             `json:"pid"`
	Start uint64            `json:"start"` // /proc/<pid>/stat field 22
	IDs   map[string]uint64 `json:"ids"`   // type -> namespace inode
}

func readNSIDs(pid int32) (map[string]uint64, error) {
	ids := map[string]uint64{}
	for _, t := range NSTypes {
		st, err := os.Stat("/proc/" + strconv.Itoa(int(pid)) + "/ns/" + t)
		if err != nil {
			return nil, err
		}
		ids[t] = st.Sys().(*syscall.Stat_t).Ino
	}
	return ids, nil
}

func procStart(pid int32) (uint64, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/stat")
	if err != nil {
		return 0, err
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(f) < 20 {
		return 0, errors.New("short /proc stat")
	}
	return strconv.ParseUint(f[19], 10, 64)
}

// childPID reads bwrap's --info-fd stream, which begins `{`, `"child-pid": N,`.
func childPID(r io.Reader) (int32, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":"); ok && strings.Trim(k, `"`) == "child-pid" {
			n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(v), ","))
			return int32(n), err
		}
	}
	return 0, errors.New("bwrap reported no child pid")
}

// NSID prints the namespace identity of the domain's live run as type:inode
// pairs, one per line.
func (s Store) NSID(name string) ([]string, error) {
	m, err := s.Load(name)
	if err != nil {
		return nil, err
	}
	r := m.NS
	if r == nil {
		return nil, fmt.Errorf("domain %s has not been run: no namespace identity recorded", name)
	}
	st, err := procStart(r.PID)
	if err != nil || st != r.Start {
		return nil, fmt.Errorf("domain %s has no live run (last recorded init pid %d is gone); namespaces are per run", name, r.PID)
	}
	now, err := readNSIDs(r.PID)
	if err != nil {
		return nil, fmt.Errorf("domain %s has no live run (init pid %d is exiting: %v)", name, r.PID, err)
	}
	var out []string
	for _, t := range NSTypes {
		if now[t] != r.IDs[t] {
			return nil, fmt.Errorf("pid %d no longer has the recorded %s namespace", r.PID, t)
		}
		out = append(out, fmt.Sprintf("%s:%d", t, now[t]))
	}
	return out, nil
}

// recordRun stores the run's namespace identity in domain.json and, when
// AURORA_BUS_SOCK names a bus daemon, registers it there BEFORE the caller
// releases the sandbox's payload, so the payload cannot connect first. The
// returned func unregisters. Failures are warnings: without registration a
// daemon that attests namespaces simply refuses the domain's connections.
func (s Store) recordRun(name string, info io.Reader, warn io.Writer) func() {
	if f, ok := info.(*os.File); ok {
		// A plain blocking read, not the netpoller: with the poller, a payload that
		// connects and exits within ~2ms made TestRunPlacesProcessInDomainCgroup
		// flaky (the test's accept ran late; 0/90 failures became ~7/30).
		f.Fd()
	}
	pid, err := childPID(info)
	if err != nil {
		fmt.Fprintf(warn, "domain: warning: no namespace identity recorded: %v\n", err)
		return func() {}
	}
	ids, err1 := readNSIDs(pid)
	start, err2 := procStart(pid)
	if err1 != nil || err2 != nil {
		fmt.Fprintf(warn, "domain: warning: cannot read namespaces of pid %d: %v %v\n", pid, err1, err2)
		return func() {}
	}
	if m, err := s.Load(name); err == nil {
		m.NS = &NSRecord{PID: pid, Start: start, IDs: ids}
		if err := s.Save(m); err != nil {
			fmt.Fprintf(warn, "domain: warning: cannot record namespaces: %v\n", err)
		}
		if sock := os.Getenv("AURORA_BUS_SOCK"); sock != "" {
			p := map[string]any{"identity": ExecID(m), "pid": pid, "ns": ids}
			if err := busCall(sock, "bus.domain.ns@v1", p); err != nil {
				fmt.Fprintf(warn, "domain: warning: bus %s did not register this run's namespaces: %v\n", sock, err)
				return func() {}
			}
			return func() {
				p["remove"] = true
				busCall(sock, "bus.domain.ns@v1", p)
			}
		}
	}
	return func() {}
}

// busCall sends one control request on the bus session socket and waits for
// its reply. It speaks the bus's line-JSON envelope directly so this module
// need not import the bus.
func busCall(sock, verb string, payload any) error {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	pl, _ := json.Marshal(payload)
	req, _ := json.Marshal(map[string]any{"id": "ns-" + strconv.Itoa(os.Getpid()), "kind": "request", "to": "bus", "verb": verb, "payload": json.RawMessage(pl)})
	if _, err := c.Write(append(req, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return err
	}
	var rep struct {
		Kind    string
		Payload struct{ Message string }
	}
	if json.Unmarshal(line, &rep) != nil {
		return errors.New("unreadable reply")
	}
	if rep.Kind == "error" {
		return errors.New(rep.Payload.Message)
	}
	return nil
}
