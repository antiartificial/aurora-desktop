package server

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func sandboxSh(t *testing.T, script string) int32 {
	t.Helper()
	bw, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("no bwrap")
	}
	r, w, _ := os.Pipe()
	defer w.Close()
	cmd := exec.Command(bw, "--info-fd", "3", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--die-with-parent",
		"--ro-bind", "/", "/", "--proc", "/proc", "--", "/bin/sh", "-c", script)
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	buf := make([]byte, 64)
	n, _ := r.Read(buf)
	var pid int32
	if i := strings.Index(string(buf[:n]), `"child-pid": `); i >= 0 {
		for _, c := range string(buf[i+13 : n]) {
			if c < '0' || c > '9' {
				break
			}
			pid = pid*10 + int32(c-'0')
		}
	}
	if pid == 0 {
		t.Skip("bwrap gave no child pid")
	}
	t.Cleanup(func() { syscall.Kill(int(pid), syscall.SIGKILL) })
	return pid
}

// findChild waits for a process with comm sleep and the given arg whose
// cmdline ends in arg, and returns its pid.
func findSleep(t *testing.T, arg string) int32 {
	t.Helper()
	for i := 0; i < 100; i++ {
		ents, _ := os.ReadDir("/proc")
		for _, e := range ents {
			n, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			b, _ := os.ReadFile("/proc/" + e.Name() + "/cmdline")
			if string(b) == "sleep\x00"+arg+"\x00" {
				return int32(n)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Skip("sleep " + arg + " not found")
	return 0
}

func runOf(t *testing.T, init int32) *nsRun {
	st, err := procStart(init)
	if err != nil {
		t.Fatal(err)
	}
	return &nsRun{pid: init, start: st}
}

// A legitimate process whose parent exited is reparented to the sandbox init
// (pid 1 of its pid namespace): it must still descend from the registered init.
func TestAncestryOrphanInsideSandboxStillDescends(t *testing.T) {
	init := sandboxSh(t, "(sleep 41 &); sleep 40")
	orphan := findSleep(t, "41")
	for i := 0; i < 100; i++ { // wait for the subshell to exit and the reparent
		if p, err := readProc(orphan); err == nil && p.ppid == init {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p, _ := readProc(orphan); p.ppid != init {
		t.Fatalf("orphan's ppid is %d, expected sandbox init %d", p.ppid, init)
	}
	if err := descendsFrom(orphan, runOf(t, init)); err != nil {
		t.Fatalf("reparented legitimate process refused: %v", err)
	}
	// and a normal descendant (the payload shell's child)
	if err := descendsFrom(findSleep(t, "40"), runOf(t, init)); err != nil {
		t.Fatalf("direct descendant refused: %v", err)
	}
}

func TestAncestryHostProcessAndPidReuseRefused(t *testing.T) {
	init := sandboxSh(t, "sleep 42")
	run := runOf(t, init)
	if err := descendsFrom(int32(os.Getpid()), run); err == nil || !strings.Contains(err.Error(), "without passing") {
		t.Fatalf("host process accepted or wrong error: %v", err)
	}
	c := exec.Command("sleep", "43")
	c.Start()
	defer func() { c.Process.Kill(); c.Wait() }()
	if err := descendsFrom(int32(c.Process.Pid), run); err == nil {
		t.Fatal("a child of the test process was accepted")
	}
	bad := *run
	bad.start++
	if err := descendsFrom(findSleep(t, "42"), &bad); err == nil || !strings.Contains(err.Error(), "reuse") {
		t.Fatalf("wrong start time on the init pid accepted: %v", err)
	}
	if err := descendsFrom(0x7ffffff0, run); err == nil {
		t.Fatal("nonexistent pid accepted")
	}
}
