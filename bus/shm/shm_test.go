package shm

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"golang.org/x/sys/unix"
)

func need(t *testing.T) {
	if err := Supported(); err != nil {
		t.Skip(err)
	}
}

func TestCreateWriteSealMapClose(t *testing.T) {
	need(t)
	s, err := Create("t", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Sealed() {
		t.Fatal("fresh segment reports sealed")
	}
	if _, err := s.WriteAt([]byte("abcdefgh"), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Seal(); err != nil {
		t.Fatal(err)
	}
	if !s.Sealed() {
		t.Fatal("not sealed after Seal")
	}
	if _, err := s.WriteAt([]byte("X"), 0); !errors.Is(err, unix.EPERM) {
		t.Fatalf("write after seal: %v, want EPERM", err)
	}
	m, err := s.Map()
	if err != nil {
		t.Fatal(err)
	}
	if string(m.Bytes()) != "abcdefgh" {
		t.Fatalf("map: %q", m.Bytes())
	}
	if err := m.Unmap(); err != nil || m.Unmap() != nil {
		t.Fatal("unmap should succeed and be idempotent")
	}
	b, _ := io.ReadAll(s.Reader())
	if !bytes.Equal(b, []byte("abcdefgh")) {
		t.Fatalf("reader: %q", b)
	}
	ro, err := s.OpenReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Write([]byte("x")); err == nil {
		t.Fatal("read-only reopen is writable")
	}
}

func TestSealFailsWithWritableMapping(t *testing.T) {
	need(t)
	s, _ := Create("t", 4096)
	defer s.Close()
	b, err := unix.Mmap(int(s.f.Fd()), 0, 4096, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Seal(); err == nil {
		t.Fatal("sealed with a live writable mapping")
	}
	unix.Munmap(b)
	if err := s.Seal(); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptRefusesUnsealedAndNonMemfd(t *testing.T) {
	need(t)
	s, _ := Create("t", 4)
	defer s.Close()
	if _, err := Adopt(s.File()); !errors.Is(err, ErrNotSealed) {
		t.Fatalf("unsealed: %v", err)
	}
	r, w, _ := osPipe()
	defer r.Close()
	defer w.Close()
	if _, err := Adopt(r); err == nil {
		t.Fatal("adopted a pipe")
	}
}

func TestEmptySegment(t *testing.T) {
	need(t)
	s, err := FromBytes("e", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.Map()
	if err != nil || len(m.Bytes()) != 0 || m.Unmap() != nil {
		t.Fatalf("empty map: %v", err)
	}
}

func TestSHA256(t *testing.T) {
	need(t)
	s, _ := FromBytes("h", []byte("abc"))
	defer s.Close()
	got, _ := s.SHA256()
	if got != "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal(got)
	}
}
