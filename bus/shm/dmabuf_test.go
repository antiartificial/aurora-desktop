package shm

import (
	"os"
	"testing"
)

func udmabuf(t *testing.T, size int64) *os.File {
	t.Helper()
	f, err := CreateUdmabuf(size)
	if err != nil {
		t.Skipf("no real dma-buf available on this host: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestDetectKindRealDmabuf(t *testing.T) {
	f := udmabuf(t, 10000)
	if k, err := DetectKind(f); err != nil || k != KindDmabuf {
		t.Fatalf("kind %q err %v", k, err)
	}
	d, err := AdoptDmabuf(f)
	if err != nil {
		t.Fatal(err)
	}
	if d.Size() != 12288 || d.Inode() == 0 {
		t.Errorf("size %d ino %d", d.Size(), d.Inode())
	}
	if off, _ := f.Seek(0, 1); off != 0 {
		t.Errorf("offset not restored: %d", off)
	}
	if _, err := Adopt(f); err == nil {
		t.Error("a dma-buf was adopted as a sealed memfd")
	}
}

func TestDetectKindRejectsOthers(t *testing.T) {
	m, _ := FromBytes("x", []byte("hi"))
	defer m.Close()
	if k, err := DetectKind(m.File()); err != nil || k != KindMemfd {
		t.Errorf("memfd detected as %q %v", k, err)
	}
	if IsDmabuf(m.File()) {
		t.Error("memfd claimed dma-buf")
	}
	reg, _ := os.CreateTemp(t.TempDir(), "r")
	defer reg.Close()
	null, _ := os.Open("/dev/null")
	defer null.Close()
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	for name, f := range map[string]*os.File{"regular": reg, "devnull": null, "pipe": r} {
		if IsDmabuf(f) {
			t.Errorf("%s detected as dma-buf", name)
		}
		// A tmpfs file answers F_GET_SEALS (kind memfd); Adopt then refuses it
		// because it is not sealed. Either way it is not a usable segment.
		if k, err := DetectKind(f); err == nil && k == KindMemfd {
			if _, err := Adopt(f); err == nil {
				t.Errorf("%s adopted as a sealed segment", name)
			}
		} else if err == nil {
			t.Errorf("%s detected as %q", name, k)
		}
		if _, err := AdoptDmabuf(f); err != ErrNotDmabuf {
			t.Errorf("%s: AdoptDmabuf err %v", name, err)
		}
	}
}
