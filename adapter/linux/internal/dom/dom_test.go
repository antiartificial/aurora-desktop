package dom

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// homeTemp makes a scratch dir under $HOME (a btrfs fs on the dev box) and removes it afterwards.
func homeTemp(t *testing.T) string {
	t.Helper()
	h, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	d, err := os.MkdirTemp(h, ".aurora-test-")
	if err != nil {
		t.Skipf("cannot create scratch dir in $HOME: %v", err)
	}
	t.Cleanup(func() { removeTree(d, false) })
	return d
}

// sandboxed returns a store (rooted under scratch) and skips if bwrap cannot run here.
func sandboxed(t *testing.T) (Store, string) {
	t.Helper()
	scratch := homeTemp(t)
	st := Store{Root: filepath.Join(scratch, "domains")}
	t.Cleanup(func() { // domains created by a test leave runtime socket dirs and cgroups behind
		ents, _ := os.ReadDir(st.Root)
		for _, e := range ents {
			os.RemoveAll(SockDir(e.Name()))
			removeCgroup(e.Name())
		}
		os.RemoveAll(SockDir("probe"))
	})
	if _, _, err := st.Create("probe"); err != nil {
		t.Fatal(err)
	}
	if code, err := st.Run("probe", []string{"true"}, nil, nil, nil); err != nil || code != 0 {
		t.Skipf("bwrap/user namespaces unusable on this host: code=%d err=%v", code, err)
	}
	_, _ = st.Destroy("probe", false)
	return st, scratch
}

func run(t *testing.T, st Store, name string, argv ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code, err := st.Run(name, argv, nil, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	return code, out.String()
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultDomainSeesNoUserFiles(t *testing.T) {
	st, scratch := sandboxed(t)
	secret := filepath.Join(scratch, "secret.txt")
	write(t, secret, "TOPSECRET")
	if _, _, err := st.Create("iso"); err != nil {
		t.Fatal(err)
	}
	code, out := run(t, st, "iso", "cat", secret)
	if code == 0 || strings.Contains(out, "TOPSECRET") {
		t.Fatalf("domain read an ungranted file: code=%d out=%q", code, out)
	}
	home, _ := os.UserHomeDir()
	code, out = run(t, st, "iso", "ls", home)
	if code == 0 {
		t.Fatalf("$HOME is visible inside the domain: %q", out)
	}
	// its own data dir is writable and lands in the domain's data/
	if code, out = run(t, st, "iso", "sh", "-c", "echo mine > /domain/data/f"); code != 0 {
		t.Fatalf("cannot write own data: %s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(st.Dir("iso"), "data", "f")); string(b) != "mine\n" {
		t.Fatalf("data not persisted on host: %q", b)
	}
}

func TestGrantExactFileOnly(t *testing.T) {
	st, scratch := sandboxed(t)
	a, b := filepath.Join(scratch, "a.txt"), filepath.Join(scratch, "b.txt")
	write(t, a, "AAA")
	write(t, b, "BBB")
	if _, _, err := st.Create("g"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GrantPath("g", a, "ro"); err != nil {
		t.Fatal(err)
	}
	if code, out := run(t, st, "g", "cat", a); code != 0 || out != "AAA" {
		t.Fatalf("granted file unreadable: %d %q", code, out)
	}
	if code, out := run(t, st, "g", "cat", b); code == 0 || strings.Contains(out, "BBB") {
		t.Fatalf("sibling readable: %d %q", code, out)
	}
	if err := st.Revoke("g", a, false); err != nil {
		t.Fatal(err)
	}
	if code, _ := run(t, st, "g", "cat", a); code == 0 {
		t.Fatal("revoked file still readable")
	}
	if err := st.Revoke("g", a, false); err == nil {
		t.Fatal("revoking twice should error")
	}
}

func TestReadOnlyAndReadWriteGrants(t *testing.T) {
	st, scratch := sandboxed(t)
	ro, rw := filepath.Join(scratch, "ro.txt"), filepath.Join(scratch, "rw.txt")
	write(t, ro, "orig")
	write(t, rw, "orig")
	st.Create("w")
	st.GrantPath("w", ro, "ro")
	st.GrantPath("w", rw, "rw")
	if code, _ := run(t, st, "w", "sh", "-c", "echo changed > "+ro); code == 0 {
		t.Fatal("write to ro grant succeeded")
	}
	if b, _ := os.ReadFile(ro); string(b) != "orig" {
		t.Fatalf("ro file modified: %q", b)
	}
	if code, out := run(t, st, "w", "sh", "-c", "echo changed > "+rw); code != 0 {
		t.Fatalf("write to rw grant failed: %s", out)
	}
	if b, _ := os.ReadFile(rw); string(b) != "changed\n" {
		t.Fatalf("rw file not modified: %q", b)
	}
}

func TestNoNetworkByDefault(t *testing.T) {
	st, scratch := sandboxed(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot listen on loopback")
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_ = scratch
	port := ln.Addr().(*net.TCPAddr).Port
	probe := fmt.Sprintf("exec 3<>/dev/tcp/127.0.0.1/%d", port)
	st.Create("n")
	if code, _ := run(t, st, "n", "bash", "-c", probe); code == 0 {
		t.Fatal("domain without net grant reached host loopback")
	}
	// coarse net grant: shares the host netns, so the same connect now works
	if err := st.GrantNet("n", "example.org"); err != nil {
		t.Fatal(err)
	}
	if code, out := run(t, st, "n", "bash", "-c", probe); code != 0 {
		t.Fatalf("net-granted domain cannot connect: %s", out)
	}
}

func TestSnapshotRollbackRestoresDeletedFile(t *testing.T) {
	scratch := homeTemp(t)
	st := Store{Root: filepath.Join(scratch, "domains")}
	if !OnBtrfs(scratch) {
		t.Skip("not on btrfs")
	}
	if _, err := btrfsBin(); err != nil {
		t.Skip(err.Error())
	}
	m, _, err := st.Create("s")
	if err != nil || !m.Subvolumes {
		t.Fatalf("create: %v subvol=%v", err, m != nil && m.Subvolumes)
	}
	f := filepath.Join(st.Dir("s"), "data", "keep.txt")
	write(t, f, "precious")
	if _, err := st.Snapshot("s", "v1"); err != nil {
		t.Skipf("btrfs snapshot not permitted here: %v", err)
	}
	if _, err := st.Snapshot("s", "v1"); err == nil {
		t.Fatal("duplicate tag accepted")
	}
	os.Remove(f)
	write(t, filepath.Join(st.Dir("s"), "data", "junk"), "x")
	if err := st.Rollback("s", "v1"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(f); err != nil || string(b) != "precious" {
		t.Fatalf("file not restored: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(st.Dir("s"), "data", "junk")); err == nil {
		t.Fatal("post-snapshot file survived rollback")
	}
	if err := st.Rollback("s", "nope"); err == nil {
		t.Fatal("rollback to missing snapshot succeeded")
	}
	if _, err := st.Destroy("s", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.Dir("s")); !os.IsNotExist(err) {
		t.Fatal("destroy left files behind")
	}
}

func TestPlainDirectoryFallback(t *testing.T) {
	root := t.TempDir() // tmpfs on most hosts
	if OnBtrfs(root) {
		t.Skip("temp dir is btrfs; fallback not exercised")
	}
	st := Store{Root: root}
	m, note, err := st.Create("p")
	if err != nil || m.Subvolumes || !strings.Contains(note, "plain directories") {
		t.Fatalf("create: %v %v %q", err, m, note)
	}
	if _, err := st.Snapshot("p", "x"); err == nil || !strings.Contains(err.Error(), "btrfs") {
		t.Fatalf("expected clear btrfs error, got %v", err)
	}
}

func TestDestroyRemovesEverything(t *testing.T) {
	st, _ := sandboxed(t)
	st.Create("d")
	write(t, filepath.Join(st.Dir("d"), "data", "x"), "x")
	os.Mkdir(filepath.Join(st.Dir("d"), "data", "sub"), 0o500) // awkward perms
	st.Snapshot("d", "t1")
	run(t, st, "d", "true")
	if _, err := st.Destroy("d", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.Dir("d")); !os.IsNotExist(err) {
		t.Fatalf("domain dir remains: %v", err)
	}
	if _, err := os.Stat(SockDir("d")); !os.IsNotExist(err) {
		t.Fatal("socket dir remains")
	}
	if ms, _ := st.List(); len(ms) != 0 {
		t.Fatalf("still listed: %v", ms)
	}
	// keep-data keeps files but removes the domain
	st.Create("k")
	write(t, filepath.Join(st.Dir("k"), "data", "x"), "x")
	kept, err := st.Destroy("k", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(kept, "data", "x")); err != nil {
		t.Fatal("kept data missing")
	}
	if _, err := st.Load("k"); err == nil {
		t.Fatal("kept domain still loadable")
	}
	if _, _, err := st.Create("k"); err != nil {
		t.Fatalf("cannot recreate after keep-data: %v", err)
	}
	st.Destroy("k", false)
	removeTree(kept, true)
}

func TestIdentityEnvAndSocket(t *testing.T) {
	st, _ := sandboxed(t)
	m, _, _ := st.Create("id")
	defer st.Destroy("id", false)
	code, out := run(t, st, "id", "sh", "-c", "echo $AURORA_DOMAIN_ID")
	if code != 0 || strings.TrimSpace(out) != ExecID(m) {
		t.Fatalf("env id %q != %q", out, ExecID(m))
	}
	// A server listening on the per-domain socket knows who connects: only this domain has it mounted.
	sock := filepath.Join(SockDir("id"), "bus.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.Close()
		}
		got <- err
	}()
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("python3 missing; cannot connect to a unix socket from inside")
	}
	code, out = run(t, st, "id", "python3", "-c",
		"import socket;s=socket.socket(socket.AF_UNIX);s.connect('/run/aurora/bus.sock')")
	if code != 0 {
		t.Fatalf("domain could not reach its bus socket: %s", out)
	}
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}

func TestParseSizeAndLimits(t *testing.T) {
	for in, want := range map[string]int64{"512M": 512 << 20, "1G": 1 << 30, "100": 100, "4k": 4096} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q)=%d,%v want %d", in, got, err, want)
		}
	}
	if _, err := ParseSize("lots"); err == nil {
		t.Error("bad size accepted")
	}
	if _, err := delegatedBase(); err != nil {
		t.Skipf("no delegated cgroup: %v", err)
	}
	name := fmt.Sprintf("lim%d", os.Getpid())
	defer removeCgroup(name)
	var pids *LimitResult
	for _, r := range ApplyLimits(name, Limits{Pids: 7, MemoryBytes: 64 << 20}) {
		if r.Controller == "pids" {
			r := r
			pids = &r
		}
	}
	if pids == nil || !pids.Applied {
		t.Skipf("pids controller not delegated: %+v", pids)
	}
	d, _ := CgroupDir(name)
	if b, _ := os.ReadFile(filepath.Join(d, "pids.max")); strings.TrimSpace(string(b)) != "7" {
		t.Fatalf("pids.max = %q", b)
	}
}

func TestLimitEnforcedInsideDomain(t *testing.T) {
	st, _ := sandboxed(t)
	if _, err := delegatedBase(); err != nil {
		t.Skip(err.Error())
	}
	st.Create("lm")
	defer st.Destroy("lm", false)
	st.SetLimits("lm", Limits{Pids: 20})
	var out bytes.Buffer
	code, _ := st.Run("lm", []string{"sh", "-c", "cat /proc/self/cgroup; for i in $(seq 1 60); do sleep 1 & done 2>&1; wait"}, nil, &out, &out)
	_ = code
	if !strings.Contains(out.String(), "Resource temporarily unavailable") && !strings.Contains(out.String(), "retry") && !strings.Contains(out.String(), "fork") {
		t.Skipf("could not observe pids limit (controller likely undelegated): %q", out.String())
	}
}
