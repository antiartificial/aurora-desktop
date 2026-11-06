package dom

import (
	"bufio"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"strings"
	"syscall"
)

// Per-domain host uids. A domain's processes run under their own SUBORDINATE
// uid (from the invoking user's /etc/subuid range), so SO_PEERCRED on the bus
// socket reports it, and no domain shares a uid with the host user or another
// domain. The user namespace is made by util-linux `unshare`, which calls
// newuidmap/newgidmap (setuid helpers) to install a map of two ranges:
//
//	inside 0        -> the real uid   (the files the domain may touch are mapped, see sandbox.go)
//	inside 1..N     -> subuid start..start+N-1
//
// The payload is then setuid to inside uid 1+off, i.e. host uid start+off.

// SubRange is a [Start, Start+Count) subordinate id range.
type SubRange struct{ Start, Count uint32 }

// subRange reads the first range of the invoking user from a subuid/subgid file.
func subRange(file string) (SubRange, error) {
	f, err := os.Open(file)
	if err != nil {
		return SubRange{}, err
	}
	defer f.Close()
	names := []string{strconv.Itoa(os.Getuid())}
	if u, err := user.Current(); err == nil {
		names = append(names, u.Username)
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(strings.TrimSpace(sc.Text()), ":")
		if len(p) != 3 {
			continue
		}
		for _, n := range names {
			if p[0] == n {
				st, e1 := strconv.ParseUint(p[1], 10, 32)
				ct, e2 := strconv.ParseUint(p[2], 10, 32)
				if e1 == nil && e2 == nil && ct >= 2 {
					return SubRange{uint32(st), uint32(ct)}, nil
				}
			}
		}
	}
	return SubRange{}, fmt.Errorf("no usable range for uid %d in %s", os.Getuid(), file)
}

var errNoSubIDs = errors.New("per-domain uids unavailable")

// subTools checks everything the user-namespace path needs.
func subTools() error {
	for _, t := range []string{"unshare", "newuidmap", "newgidmap", "setpriv"} {
		if _, err := exec.LookPath(t); err != nil {
			return fmt.Errorf("%w: %s not found", errNoSubIDs, t)
		}
	}
	return nil
}

// AssignUID gives m a host uid/gid if it has none (or its recorded one has
// left the user's subuid range). The choice is deterministic: offset
// fnv32a(name) mod range, then linear probing past ids other domains hold; the
// result is recorded in domain.json and never changes while it stays valid.
func (s Store) AssignUID(m *Meta) error {
	ur, err := subRange("/etc/subuid")
	if err != nil {
		return fmt.Errorf("%w: %v", errNoSubIDs, err)
	}
	gr, err := subRange("/etc/subgid")
	if err != nil {
		return fmt.Errorf("%w: %v", errNoSubIDs, err)
	}
	n := ur.Count
	if gr.Count < n {
		n = gr.Count
	}
	if m.UID >= ur.Start && m.UID-ur.Start < n && m.GID == gr.Start+(m.UID-ur.Start) {
		return nil
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	taken := map[uint32]bool{}
	ms, _ := s.List()
	for _, o := range ms {
		if o.Name != m.Name && o.UID != 0 {
			taken[o.UID] = true
		}
	}
	h := fnv.New32a()
	h.Write([]byte(m.Name))
	off := h.Sum32() % n
	for i := uint32(0); i < n; i++ {
		if c := ur.Start + (off+i)%n; !taken[c] {
			m.UID, m.GID = c, gr.Start+(off+i)%n
			return s.Save(m)
		}
	}
	return fmt.Errorf("%w: all %d ids in use", errNoSubIDs, n)
}

// UID returns the domain's host uid, assigning one if needed.
func (s Store) UID(name string) (uint32, error) {
	m, err := s.Load(name)
	if err != nil {
		return 0, err
	}
	if err := s.AssignUID(m); err != nil {
		return 0, err
	}
	return m.UID, nil
}

func (s Store) lock() (func(), error) {
	f, err := os.OpenFile(s.Root+"/.uid.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// usernsPrefix is the command line that runs what follows inside a fresh user
// namespace holding the two-range map for domain m, as namespace root (uid 0
// = the real user, with full capabilities IN THAT NAMESPACE ONLY).
func usernsPrefix(m *Meta) ([]string, error) {
	ur, err := subRange("/etc/subuid")
	if err != nil {
		return nil, err
	}
	gr, err := subRange("/etc/subgid")
	if err != nil {
		return nil, err
	}
	u := func(f string, id, start, n uint32) string { return fmt.Sprintf("--map-%s=%d:%d:%d", f, id, start, n) }
	return []string{"unshare", "--user",
		u("users", 0, uint32(os.Getuid()), 1), u("users", 1, ur.Start, ur.Count),
		u("groups", 0, uint32(os.Getgid()), 1), u("groups", 1, gr.Start, gr.Count), "--"}, nil
}

// innerIDs are the domain's ids inside the namespace.
func innerIDs(m *Meta) (uid, gid uint32, err error) {
	ur, err := subRange("/etc/subuid")
	if err != nil {
		return 0, 0, err
	}
	gr, err := subRange("/etc/subgid")
	if err != nil {
		return 0, 0, err
	}
	return 1 + m.UID - ur.Start, 1 + m.GID - gr.Start, nil
}

// privDrop is the setpriv invocation that turns namespace root into the
// domain's uid, keeping ONLY CAP_DAC_OVERRIDE and CAP_FOWNER (see sandbox.go).
func privDrop(m *Meta) ([]string, error) {
	u, g, err := innerIDs(m)
	if err != nil {
		return nil, err
	}
	return []string{"setpriv", "--reuid", strconv.Itoa(int(u)), "--regid", strconv.Itoa(int(g)), "--clear-groups",
		"--bounding-set=-all,+dac_override,+fowner", "--inh-caps=-all,+dac_override,+fowner",
		"--ambient-caps=+dac_override,+fowner", "--no-new-privs", "--"}, nil
}

// rmInUserns deletes path as namespace root: it can remove files the domain
// created (owned by its subuid), which the host user cannot.
func rmInUserns(path string) error {
	if subTools() != nil {
		return errNoSubIDs
	}
	m := &Meta{}
	pre, err := usernsPrefix(m)
	if err != nil {
		return err
	}
	argv := append(pre, "rm", "-rf", "--", path)
	if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
		return fmt.Errorf("remove %s in user namespace: %v: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}
