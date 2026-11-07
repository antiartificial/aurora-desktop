package dom

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// liveDomain starts a sandbox shaped like a per-domain-uid run (a user namespace made by `unshare`,
// bwrap as its root, the domain's socket directory bound at /run/aurora) and records it as the domain's
// live run. The only difference from `domain run` is the id map, which needs an /etc/subuid range this
// host may not have: the namespace relationships the mount code depends on are the same.
// nested=true makes the OTHER shape, bwrap's own nested user namespace (the no-subuid fallback).
func liveDomain(t *testing.T, st Store, name string, nested bool) (pid int32) {
	t.Helper()
	for _, b := range []string{"unshare", "bwrap", "nsenter"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not found", b)
		}
	}
	if _, _, err := st.Create(name); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(SockDir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(SockDir(name)) })
	infoR, infoW, _ := os.Pipe()
	bw := []string{"bwrap", "--unshare-pid", "--unshare-ipc", "--die-with-parent", "--ro-bind", "/usr", "/usr",
		"--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--bind", SockDir(name), "/run/aurora", "--info-fd", "3", "sleep", "60"}
	argv := append([]string{"unshare", "--user", "--map-root-user", "--"}, bw...)
	if nested {
		argv = append([]string{"bwrap", "--unshare-user"}, bw[1:]...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.ExtraFiles = []*os.File{infoW}
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a sandbox: %v", err)
	}
	infoW.Close()
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait(); infoR.Close() })
	done := make(chan struct{})
	var cp int32
	var perr error
	go func() { cp, perr = childPID(infoR); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Skip("sandbox never reported a pid")
	}
	if perr != nil {
		t.Skipf("sandbox did not start (user namespaces unusable here?): %v", perr)
	}
	ids, err1 := readNSIDs(cp)
	start, err2 := procStart(cp)
	if err1 != nil || err2 != nil {
		t.Fatalf("ns of %d: %v %v", cp, err1, err2)
	}
	if nested { // bwrap moves the payload into a second user namespace a moment after it starts
		for i := 0; i < 100 && ownsMountNS(cp) == nil; i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}
	m, _ := st.Load(name)
	m.NS = &NSRecord{PID: cp, Start: start, IDs: ids}
	if err := st.Save(m); err != nil {
		t.Fatal(err)
	}
	return cp
}

// inDomain runs sh -c script inside the live domain's namespaces and returns its output and exit code.
func inDomain(t *testing.T, pid int32, script string) (string, int) {
	t.Helper()
	out, err := exec.Command("nsenter", "-t", strconv.Itoa(int(pid)), "-U", "-m", "-p", "--preserve-credentials", "--", "sh", "-c", script).CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func testHelperExe(t *testing.T) {
	t.Helper()
	old := helperExe
	helperExe = os.Executable // the test binary answers DropCmd in TestMain
	t.Cleanup(func() { helperExe = old })
}

func TestMountDropGivesTheRunningDomainExactlyOneReadOnlyFile(t *testing.T) {
	testHelperExe(t)
	st, scratch := sandboxed(t)
	pid := liveDomain(t, st, "dropro", false)
	secretDir := filepath.Join(scratch, "pics")
	os.MkdirAll(secretDir, 0o700)
	photo := filepath.Join(secretDir, "photo one.png")
	write(t, photo, "PNGDATA")
	write(t, filepath.Join(secretDir, "neighbour.txt"), "NOT GRANTED")

	m, err := st.MountDrop("dropro", photo, "ro")
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	if m.Guest != "/run/aurora/drops/1/photo_one.png" || m.Mode != "ro" || m.Host != photo {
		t.Fatalf("mount %+v", m)
	}
	// readable, with the right bytes
	if out, code := inDomain(t, pid, "cat "+m.Guest); code != 0 || out != "PNGDATA" {
		t.Fatalf("cat: %d %q", code, out)
	}
	// read-only: a write is refused by the kernel and the host file is untouched
	if out, code := inDomain(t, pid, "echo pwned > "+m.Guest); code == 0 || !strings.Contains(out, "Read-only") {
		t.Fatalf("write was not refused: %d %q", code, out)
	}
	if b, _ := os.ReadFile(photo); string(b) != "PNGDATA" {
		t.Fatalf("host file changed: %q", b)
	}
	// nothing else of the directory, and no host path, is visible
	if out, _ := inDomain(t, pid, "ls /run/aurora/drops/1; ls "+secretDir+" 2>&1; cat "+filepath.Join(secretDir, "neighbour.txt")+" 2>&1"); strings.Contains(out, "NOT GRANTED") || strings.Count(out, "No such file") != 2 || !strings.HasPrefix(out, "photo_one.png\n") {
		t.Fatalf("what the domain sees: %q", out)
	}
	// revoke: unmounting removes it at once, and the placeholder goes
	if err := st.UnmountDrop("dropro", m.ID); err != nil {
		t.Fatal(err)
	}
	if out, code := inDomain(t, pid, "cat "+m.Guest); code == 0 {
		t.Fatalf("still readable after unmount: %q", out)
	}
	if _, err := os.Stat(filepath.Join(DropsDir("dropro"), "1")); !os.IsNotExist(err) {
		t.Fatalf("placeholder left behind: %v", err)
	}
	// a second drop gets a new number
	m2, err := st.MountDrop("dropro", photo, "ro")
	if err != nil || m2.ID != "1" {
		t.Fatalf("second mount %+v %v (ids are reused once the placeholder is gone)", m2, err)
	}
}

func TestMountDropReadWriteReachesTheHostFile(t *testing.T) {
	testHelperExe(t)
	st, scratch := sandboxed(t)
	pid := liveDomain(t, st, "droprw", false)
	f := filepath.Join(scratch, "note.txt")
	write(t, f, "old")
	m, err := st.MountDrop("droprw", f, "rw")
	if err != nil {
		t.Fatal(err)
	}
	if out, code := inDomain(t, pid, "echo new > "+m.Guest); code != 0 {
		t.Fatalf("write refused: %q", out)
	}
	if b, _ := os.ReadFile(f); string(b) != "new\n" {
		t.Fatalf("host file %q", b)
	}
}

func TestMountDropADirectory(t *testing.T) {
	testHelperExe(t)
	st, scratch := sandboxed(t)
	pid := liveDomain(t, st, "dropdir", false)
	d := filepath.Join(scratch, "album")
	os.MkdirAll(d, 0o700)
	write(t, filepath.Join(d, "a.txt"), "A")
	write(t, filepath.Join(scratch, "outside.txt"), "OUT")
	m, err := st.MountDrop("dropdir", d, "ro")
	if err != nil || !m.Dir {
		t.Fatalf("%+v %v", m, err)
	}
	if out, code := inDomain(t, pid, "cat "+m.Guest+"/a.txt; ls "+m.Guest+"/.. "); code != 0 || !strings.HasPrefix(out, "A") || strings.Contains(out, "outside") {
		t.Fatalf("%d %q", code, out)
	}
	if out, code := inDomain(t, pid, "touch "+m.Guest+"/b.txt"); code == 0 {
		t.Fatalf("directory was writable: %q", out)
	}
}

func TestMountDropRefusesWhatIsNotAnExactRegularFileOrDirectory(t *testing.T) {
	testHelperExe(t)
	st, scratch := sandboxed(t)
	liveDomain(t, st, "dropbad", false)
	real := filepath.Join(scratch, "real.txt")
	write(t, real, "R")
	link := filepath.Join(scratch, "link.txt")
	os.Symlink(real, link)
	fifo := filepath.Join(scratch, "pipe")
	syscall.Mkfifo(fifo, 0o600)
	unreadable := filepath.Join(scratch, "nope.txt")
	write(t, unreadable, "x")
	os.Chmod(unreadable, 0)
	cases := []struct{ name, path, mode, want string }{
		{"a symlink is refused, not followed", link, "ro", "resolves to"},
		{"a relative path", "real.txt", "ro", "absolute"},
		{"a dirty path", scratch + "/./real.txt", "ro", "clean"},
		{"a missing file", filepath.Join(scratch, "missing"), "ro", "exist"},
		{"a fifo", fifo, "ro", "not a regular file"},
		{"a device", "/dev/null", "ro", "not a regular file"},
		{"a bad mode", real, "ro,rw", "mode"},
	}
	if os.Geteuid() != 0 {
		cases = append(cases, struct{ name, path, mode, want string }{"a file the user cannot read", unreadable, "ro", "permission"})
	}
	for _, c := range cases {
		if _, err := st.MountDrop("dropbad", c.path, c.mode); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (want %q)", c.name, err, c.want)
		}
	}
	if ents, _ := os.ReadDir(DropsDir("dropbad")); len(ents) != 0 {
		t.Errorf("refused drops left placeholders: %v", ents)
	}
}

// The helper itself re-checks what the parent authorised: a path whose inode is not the one named is
// refused (a swap between the parent's check and the helper's open).
func TestHelperRefusesAnInodeThatIsNotTheAuthorisedOne(t *testing.T) {
	testHelperExe(t)
	st, scratch := sandboxed(t)
	pid := liveDomain(t, st, "dropswap", false)
	f := filepath.Join(scratch, "a.txt")
	write(t, f, "A")
	var ds syscall.Stat_t
	syscall.Stat(f, &ds)
	_, mntIno, _ := st.liveRun("dropswap")
	id, hostDir, _ := newDropDir("dropswap")
	os.WriteFile(filepath.Join(hostDir, "a.txt"), nil, 0o600)
	err := runHelper(pid, []string{DropCmd, "mount", "/proc/" + strconv.Itoa(int(pid)) + "/ns/mnt", strconv.FormatUint(mntIno, 10),
		f, strconv.FormatUint(uint64(ds.Dev), 10), strconv.FormatUint(ds.Ino+1, 10), "/run/aurora/drops/" + id + "/a.txt", "ro"})
	if err == nil || !strings.Contains(err.Error(), "no longer the file") {
		t.Fatalf("a wrong inode was mounted: %v", err)
	}
	// a symlink in the middle of the path is refused by the helper even if the parent were fooled
	link := filepath.Join(scratch, "alias")
	os.Symlink(scratch, link)
	err = runHelper(pid, []string{DropCmd, "mount", "/proc/" + strconv.Itoa(int(pid)) + "/ns/mnt", strconv.FormatUint(mntIno, 10),
		filepath.Join(link, "a.txt"), strconv.FormatUint(uint64(ds.Dev), 10), strconv.FormatUint(ds.Ino, 10), "/run/aurora/drops/" + id + "/a.txt", "ro"})
	if err == nil || !strings.Contains(err.Error(), "without following symlinks") {
		t.Fatalf("a symlinked path was followed: %v", err)
	}
	// and it will not mount anywhere but under the drops directory
	err = runHelper(pid, []string{DropCmd, "mount", "/proc/" + strconv.Itoa(int(pid)) + "/ns/mnt", strconv.FormatUint(mntIno, 10),
		f, strconv.FormatUint(uint64(ds.Dev), 10), strconv.FormatUint(ds.Ino, 10), "/run/aurora/drops/../bus.sock", "ro"})
	if err == nil || !strings.Contains(err.Error(), "not under") {
		t.Fatalf("mounted outside drops: %v", err)
	}
}

func TestMountDropNeedsALiveRunWithItsOwnUserNamespace(t *testing.T) {
	testHelperExe(t)
	st, scratch := sandboxed(t)
	f := filepath.Join(scratch, "a.txt")
	write(t, f, "A")
	if _, _, err := st.Create("neverran"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MountDrop("neverran", f, "ro"); err == nil || !strings.Contains(err.Error(), "not been run") {
		t.Fatalf("never-run domain: %v", err)
	}
	liveDomain(t, st, "dropnest", true)
	if _, err := st.MountDrop("dropnest", f, "ro"); err == nil || !strings.Contains(err.Error(), "without its own uid") {
		t.Fatalf("nested-userns mode must be refused with its reason: %v", err)
	}
	pid := liveDomain(t, st, "dropgone", false)
	syscall.Kill(int(pid), syscall.SIGKILL)
	time.Sleep(300 * time.Millisecond)
	if _, err := st.MountDrop("dropgone", f, "ro"); err == nil || !strings.Contains(err.Error(), "no live run") {
		t.Fatalf("stale record accepted: %v", err)
	}
}

func TestDropNameNeverCarriesAHostPathOrAnOddByte(t *testing.T) {
	for in, want := range map[string]string{
		"/home/arti/photo.png": "photo.png", "/x/a b\x1b[2J.txt": "a_b__2J.txt", "/x/.hidden": "hidden", "/x/..": "dropped", "/": "dropped", "/x/" + strings.Repeat("a", 300): strings.Repeat("a", 100),
	} {
		if got := DropName(in); got != want {
			t.Errorf("DropName(%q) = %q, want %q", in, got, want)
		}
	}
}
