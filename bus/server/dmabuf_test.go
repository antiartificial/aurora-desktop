package server_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/shm"
	"github.com/antiartificial/aurora/bus/wire"
)

// realDmabuf is a genuine dma-buf (udmabuf, no GPU) or skips the test.
func realDmabuf(t *testing.T, size int64) *os.File {
	t.Helper()
	f, err := shm.CreateUdmabuf(size)
	if err != nil {
		t.Skipf("no real dma-buf available on this host: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func rawSeg(t *testing.T, p *rawPeer, sg proto.Segment, f *os.File) string {
	t.Helper()
	b, _ := json.Marshal(proto.Envelope{Kind: proto.KindEvent, Topic: "x.y", Segments: []proto.Segment{sg}})
	if err := wire.Write(p.c, append(b, '\n'), []*os.File{f}); err != nil {
		t.Fatal(err)
	}
	return p.errCode()
}

// Real dma-buf end to end: kind, size from lseek and inode are recorded by the
// daemon; there is no seal and no hash; the contents stay mutable to the sender
// after delivery (the difference from a sealed memfd, shown, not just claimed).
func TestDmabufRoundTripDescriptorAndMutability(t *testing.T) {
	buf := realDmabuf(t, 8192)
	sock, dir, _ := start(t)
	sub, pub := dial(t, sock), dial(t, sock)
	sub.Subscribe(ctx(t), "gpu.*")
	e := proto.Envelope{Kind: proto.KindEvent, Topic: "gpu.frame"}
	if err := client.AttachDmabuf(&e, "fb", "image/x-raw", buf); err != nil {
		t.Fatal(err)
	}
	e.Segments[0].Sealed, e.Segments[0].SHA256 = true, "sha256:forged" // must be overwritten
	if err := pub.Send(e); err != nil {
		t.Fatal(err)
	}
	got := recv(t, sub.Events())
	defer got.CloseAttachments()
	d := got.Segments[0]
	if d.Kind != "dmabuf" || d.Bytes != 8192 || d.Sealed || d.SHA256 != "" || d.Ino == 0 {
		t.Fatalf("descriptor wrong: %+v", d)
	}
	f, desc, err := client.OpenDmabuf(got, "fb")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if desc.Ino != d.Ino {
		t.Errorf("inode differs")
	}
	if _, err := client.OpenSegment(got, "fb"); err == nil {
		t.Error("OpenSegment accepted a dma-buf as a sealed memfd")
	}
	// Mutability: the sender writes after the bus delivered; the receiver sees it.
	wm, err := unix.Mmap(int(buf.Fd()), 0, 8192, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		t.Skipf("cannot map the dma-buf: %v", err)
	}
	defer unix.Munmap(wm)
	rm, err := unix.Mmap(int(f.Fd()), 0, 8192, unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Munmap(rm)
	copy(wm, marker)
	if !bytes.HasPrefix(rm, []byte(marker)) {
		t.Fatal("receiver did not observe the sender's later write")
	}
	// Log: descriptor with kind and ino; no hash, no contents.
	files, _ := filepath.Glob(filepath.Join(dir, "seg-*.jsonl"))
	var lb []byte
	for _, fn := range files {
		b, _ := os.ReadFile(fn)
		lb = append(lb, b...)
	}
	if !bytes.Contains(lb, []byte(`"kind":"dmabuf"`)) || !bytes.Contains(lb, []byte(`"ino":`)) || bytes.Contains(lb, []byte("sha256")) || bytes.Contains(lb, []byte(marker)) {
		t.Fatalf("log entry wrong for a dma-buf: %s", lb)
	}
}

// The daemon classifies the fd itself: declared and actual kind must agree, and
// nothing that is not a memfd or a dma-buf is accepted. No fds leak.
func TestDmabufKindMismatchAndImpostorsRejected(t *testing.T) {
	needSHM(t)
	sock, _, _ := start(t)
	p := newRaw(t, sock)
	p.send(1, nil)
	p.errCode()
	base := fdCount(t)
	sealed := memfds(t, 1, true)
	if c := rawSeg(t, p, proto.Segment{ID: "a", Bytes: 4, Kind: "dmabuf"}, sealed[0]); c != "bad_segments" {
		t.Errorf("sealed memfd declared dmabuf: %q", c)
	}
	closeFs(sealed)
	r, w, _ := os.Pipe()
	if c := rawSeg(t, p, proto.Segment{ID: "a", Bytes: 0, Kind: "dmabuf"}, r); c != "bad_segments" {
		t.Errorf("pipe declared dmabuf: %q", c)
	}
	null, _ := os.Open("/dev/null")
	if c := rawSeg(t, p, proto.Segment{ID: "a", Bytes: 0, Kind: "dmabuf"}, null); c != "bad_segments" {
		t.Errorf("/dev/null declared dmabuf: %q", c)
	}
	r.Close()
	w.Close()
	null.Close()
	if _, err := os.Stat("/dev/udmabuf"); err == nil {
		if buf, err := shm.CreateUdmabuf(4096); err == nil {
			if c := rawSeg(t, p, proto.Segment{ID: "a", Bytes: 4096, Sealed: true}, buf); c != "bad_segments" {
				t.Errorf("dma-buf declared as a (sealed) memfd: %q", c)
			}
			if c := rawSeg(t, p, proto.Segment{ID: "a", Bytes: 1, Kind: "dmabuf"}, buf); c != "bad_segments" {
				t.Errorf("dma-buf size lie accepted: %q", c)
			}
			buf.Close()
		}
	}
	settleFDs(t, base+0)
}
