package dom

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func needSubIDs(t *testing.T, st Store) {
	t.Helper()
	if err := subTools(); err != nil {
		t.Skip(err.Error())
	}
	if _, err := subRange("/etc/subuid"); err != nil {
		t.Skip(err.Error())
	}
	if _, err := subRange("/etc/subgid"); err != nil {
		t.Skip(err.Error())
	}
}

// peerUID connects a domain process to a listener in the domain's socket dir
// and returns the SO_PEERCRED uid the listener sees.
func peerUID(t *testing.T, st Store, name string) uint32 {
	t.Helper()
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("python3 missing")
	}
	if err := os.MkdirAll(SockDir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(SockDir(name), "bus.sock"), Net: "unix"})
	if err != nil {
		t.Skip("cannot listen: ", err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		st.Run(name, []string{"python3", "-c", "import socket;s=socket.socket(socket.AF_UNIX);s.connect('/run/aurora/bus.sock')"}, nil, nil, nil)
	}()
	c, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	raw, _ := c.SyscallConn()
	var cred *syscall.Ucred
	raw.Control(func(fd uintptr) { cred, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) })
	if cred == nil {
		t.Fatal("no SO_PEERCRED")
	}
	<-done
	return cred.Uid
}

func TestDomainProcessPeerCredIsItsSubuidAndDomainsDiffer(t *testing.T) {
	st, _ := sandboxed(t)
	needSubIDs(t, st)
	for _, n := range []string{"ua", "ub"} {
		if _, _, err := st.Create(n); err != nil {
			t.Fatal(err)
		}
	}
	ua, err := st.UID("ua")
	if err != nil {
		t.Fatal(err)
	}
	ub, _ := st.UID("ub")
	if ua == ub {
		t.Fatalf("two domains share uid %d", ua)
	}
	r, _ := subRange("/etc/subuid")
	for _, u := range []uint32{ua, ub} {
		if u < r.Start || u >= r.Start+r.Count || u == uint32(os.Getuid()) {
			t.Fatalf("uid %d is not a subuid (range %v, real uid %d)", u, r, os.Getuid())
		}
	}
	if got := peerUID(t, st, "ua"); got != ua {
		t.Fatalf("SO_PEERCRED uid of a domain process = %d, want its subuid %d (real uid %d)", got, ua, os.Getuid())
	}
	if got := peerUID(t, st, "ub"); got != ub {
		t.Fatalf("SO_PEERCRED uid of domain ub = %d, want %d", got, ub)
	}
	if again, _ := st.UID("ua"); again != ua {
		t.Fatalf("uid changed between calls: %d then %d", ua, again)
	}
}

func TestUIDCollisionProbesToNextFreeID(t *testing.T) {
	st, _ := sandboxed(t)
	needSubIDs(t, st)
	st.Create("c1")
	st.Create("c2")
	m1, _ := st.Load("c1")
	m2, _ := st.Load("c2")
	want := m1.UID
	m1.UID, m1.GID = 0, 0
	m2.UID = want // c2 now holds the id c1 hashes to
	if err := st.Save(m2); err != nil {
		t.Fatal(err)
	}
	if err := st.AssignUID(m1); err != nil {
		t.Fatal(err)
	}
	if m1.UID == want {
		t.Fatalf("c1 took %d which c2 holds", want)
	}
	if m1.UID != want+1 {
		t.Fatalf("probe did not step to the next id: %d", m1.UID)
	}
	if got, _ := st.Load("c1"); got.UID != m1.UID {
		t.Fatal("assignment not recorded in domain.json")
	}
}

// The domain keeps working with its files under its own uid: it writes its
// subvolumes and a rw grant; what it creates belongs to the subuid on the
// host; capabilities are only the two it was given; and the host user can
// still delete the domain.
func TestDomainFilesAndCapsUnderSubuid(t *testing.T) {
	st, scratch := sandboxed(t)
	needSubIDs(t, st)
	st.Create("f")
	uid, _ := st.UID("f")
	shared := filepath.Join(scratch, "shared")
	os.Mkdir(shared, 0o755)
	if _, err := st.GrantPath("f", shared, "rw"); err != nil {
		t.Fatal(err)
	}
	code, out := run(t, st, "f", "sh", "-c", "id -u; echo hi > /domain/data/a; mkdir /domain/config/d; echo g > "+shared+"/g; grep -E '^Cap(Eff|Bnd)' /proc/self/status")
	if code != 0 {
		t.Fatalf("domain could not use its files: %s", out)
	}
	m, _ := st.Load("f")
	if in, _, _ := innerIDs(m); !strings.HasPrefix(out, fmt.Sprintf("%d\n", in)) || in == 0 {
		t.Fatalf("id -u inside = %q, want namespace uid %d (not root)", out, in)
	}
	for _, f := range []string{filepath.Join(st.Dir("f"), "data", "a"), filepath.Join(shared, "g")} {
		var s syscall.Stat_t
		if err := syscall.Stat(f, &s); err != nil || s.Uid != uid {
			t.Fatalf("%s owner = %d (%v), want subuid %d", f, s.Uid, err, uid)
		}
	}
	if strings.Count(out, "000000000000000a") != 2 {
		t.Fatalf("capabilities are not exactly dac_override+fowner:\n%s", out)
	}
	if _, err := st.Destroy("f", false); err != nil {
		t.Fatalf("host cannot destroy a domain holding subuid-owned files: %v", err)
	}
	if _, err := os.Stat(st.Dir("f")); !os.IsNotExist(err) {
		t.Fatal("domain dir remains")
	}
}
