package wire

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pair(t *testing.T) (w, r *net.UnixConn) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	w, err = net.DialUnix("unix", nil, &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	r, err = ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close(); r.Close() })
	return
}

func tmpfile(t *testing.T, content string) *os.File {
	f, err := os.CreateTemp(t.TempDir(), "f")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(content)
	return f
}

func nfds() int { d, _ := os.ReadDir("/proc/self/fd"); return len(d) }

// declared parses "N:rest" lines: N is the declared fd count.
func declared(line []byte) (int, error) {
	var n int
	_, err := fmt.Sscanf(string(line), "%d:", &n)
	return n, err
}

func content(f *os.File) string {
	b := make([]byte, 64)
	n, _ := f.ReadAt(b, 0)
	return string(b[:n])
}

// Lines with and without fds, back to back, with a line larger than the read
// buffer in the middle: fds stay attached to the right line.
func TestFDsFollowTheirLinesAcrossReadsAndBoundaries(t *testing.T) {
	w, r := pair(t)
	rd := NewReader(r, 4<<20)
	defer rd.Close()
	big := "1:" + strings.Repeat("z", 300<<10) + "\n" // forces many partial reads
	go func() {
		f1, f2, f3 := tmpfile(t, "one"), tmpfile(t, "two"), tmpfile(t, "three")
		defer f1.Close()
		defer f2.Close()
		defer f3.Close()
		Write(w, []byte("1:a\n"), []*os.File{f1})
		Write(w, []byte("0:b\n"), nil)
		Write(w, []byte("0:c\n0:d\n"), nil) // two lines, one write
		Write(w, []byte(big), []*os.File{f2})
		Write(w, []byte("2:e\n"), []*os.File{f1, f3})
	}()
	want := []struct {
		prefix string
		fds    []string
	}{{"1:a", []string{"one"}}, {"0:b", nil}, {"0:c", nil}, {"0:d", nil}, {"1:zz", []string{"two"}}, {"2:e", []string{"one", "three"}}}
	for i, w := range want {
		line, files, err := rd.Next(declared)
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if !bytes.HasPrefix(line, []byte(w.prefix)) || len(files) != len(w.fds) {
			t.Fatalf("line %d: %.10q with %d fds, want %q with %d", i, line, len(files), w.prefix, len(w.fds))
		}
		for j, f := range files {
			if got := content(f); got != w.fds[j] {
				t.Fatalf("line %d fd %d holds %q, want %q", i, j, got, w.fds[j])
			}
			f.Close()
		}
	}
}

func TestSurplusAndMissingFDsRejectedAndClosed(t *testing.T) {
	w, r := pair(t)
	rd := NewReader(r, 1<<20)
	defer rd.Close()
	before := nfds()
	mk := func(n int) []*os.File {
		var fs []*os.File
		for i := 0; i < n; i++ {
			fs = append(fs, tmpfile(t, "x"))
		}
		return fs
	}
	fs := mk(5)
	Write(w, []byte("2:surplus\n"), fs)
	for _, f := range fs {
		f.Close()
	}
	if _, files, err := rd.Next(declared); err == nil || len(files) != 0 || !strings.Contains(err.Error(), "more fd") {
		t.Fatalf("surplus: %v %v", files, err)
	}
	fs = mk(1)
	Write(w, []byte("3:missing\n"), fs)
	fs[0].Close()
	if _, _, err := rd.Next(declared); err == nil || !strings.Contains(err.Error(), "only 1") {
		t.Fatalf("missing: %v", err)
	}
	Write(w, []byte("0:fine\n"), nil)
	if line, _, err := rd.Next(declared); err != nil || string(line) != "0:fine" {
		t.Fatalf("stream did not recover: %q %v", line, err)
	}
	for i := 0; i < 100 && nfds() > before; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if nfds() > before {
		t.Fatalf("fd leak: %d before, %d after", before, nfds())
	}
}

func TestFDFloodEndsConnection(t *testing.T) {
	w, r := pair(t)
	rd := NewReader(r, 1<<20)
	defer rd.Close()
	go func() {
		f := tmpfile(t, "x")
		defer f.Close()
		for i := 0; i < 200; i++ {
			// fds with a line that never completes: nothing can claim them
			Write(w, []byte("x"), []*os.File{f, f, f, f, f, f, f, f})
		}
	}()
	_, _, err := rd.Next(declared)
	if err != ErrFDFlood {
		t.Fatalf("want ErrFDFlood, got %v", err)
	}
}

func TestLineTooLong(t *testing.T) {
	w, r := pair(t)
	rd := NewReader(r, 1000)
	go Write(w, []byte(strings.Repeat("a", 5000)), nil)
	if _, _, err := rd.Next(declared); err != ErrLineTooLong {
		t.Fatalf("got %v", err)
	}
}
