package server_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/server"
	"github.com/antiartificial/aurora/bus/shm"
	"github.com/antiartificial/aurora/bus/wire"
)

const marker = "AURORA-SEGMENT-SECRET-MARKER-7f3a"

func fdCount(t *testing.T) int {
	t.Helper()
	d, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd")
	}
	return len(d)
}

// settleFDs waits for the open fd count to come back to want (the daemon
// closes fds on its own goroutines).
func settleFDs(t *testing.T, want int) {
	t.Helper()
	var got int
	for i := 0; i < 200; i++ {
		if got = fdCount(t); got <= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fd leak: %d open, want %d", got, want)
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>8)
	}
	copy(b[n/2:], marker)
	return b
}

func needSHM(t *testing.T) {
	if err := shm.Supported(); err != nil {
		t.Skip(err)
	}
}

func publishSeg(t *testing.T, c *client.Client, topic string, data []byte) {
	t.Helper()
	e := proto.Envelope{Kind: proto.KindEvent, Topic: topic, Payload: json.RawMessage(`{"n":1}`)}
	if err := client.AttachBytes(&e, "frame", "image/png", data); err != nil {
		t.Fatal(err)
	}
	e.Segments[0].SHA256 = "sha256:forged" // the daemon must overwrite this
	if err := c.Send(e); err != nil {
		t.Fatal(err)
	}
}

func TestSegmentRoundTrip16MiB(t *testing.T) {
	needSHM(t)
	sock, _, _ := start(t)
	sub, pub := dial(t, sock), dial(t, sock)
	if err := sub.Subscribe(ctx(t), "img.*"); err != nil {
		t.Fatal(err)
	}
	data := pattern(16 << 20)
	publishSeg(t, pub, "img.ready", data)
	e := recv(t, sub.Events())
	defer e.CloseAttachments()

	seg, err := client.OpenSegment(e, "frame")
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()
	m, err := seg.Map()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Unmap()
	if !bytes.Equal(m.Bytes(), data) {
		t.Fatal("mapped segment differs from what was sent")
	}
	var rd bytes.Buffer
	if _, err := rd.ReadFrom(seg.Reader()); err != nil || !bytes.Equal(rd.Bytes(), data) {
		t.Fatalf("io.Reader path differs: %v", err)
	}
	sum := sha256.Sum256(data)
	d := e.Segments[0]
	if d.Bytes != 16<<20 || !d.Sealed || d.Mime != "image/png" || d.SHA256 != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("descriptor wrong (forged hash must be replaced): %+v", d)
	}
	if string(e.Payload) != `{"n":1}` {
		t.Fatalf("small JSON payload path changed: %s", e.Payload)
	}
}

// The JSON line carries the descriptor only, however big the segment is.
func TestLineStaysSmall(t *testing.T) {
	needSHM(t)
	sock, _, _ := start(t)
	raw, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	rd := wire.NewReader(raw, 1<<20)
	defer rd.Close()
	wire.Write(raw, []byte(`{"kind":"request","to":"bus","verb":"bus.subscribe@v1","payload":{"topic":"img.*"}}`+"\n"), nil)
	if _, _, err := rd.Next(func([]byte) (int, error) { return 0, nil }); err != nil {
		t.Fatal(err)
	}
	pub := dial(t, sock)
	publishSeg(t, pub, "img.ready", pattern(16<<20))
	line, files, err := rd.Next(func(l []byte) (int, error) {
		var e proto.Envelope
		json.Unmarshal(l, &e)
		return e.Present(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	if len(line) > 600 || len(files) != 1 {
		t.Fatalf("line is %d bytes with %d fds; want a small line and 1 fd: %s", len(line), len(files), line)
	}
	if strings.Contains(string(line), marker) {
		t.Fatal("contents in line")
	}
}

func TestLogHasDescriptorAndHashNotContents(t *testing.T) {
	needSHM(t)
	sock, dir, _ := start(t)
	sub, pub := dial(t, sock), dial(t, sock)
	sub.Subscribe(ctx(t), "img.*")
	data := pattern(2 << 20)
	publishSeg(t, pub, "img.ready", data)
	e := recv(t, sub.Events())
	e.CloseAttachments()
	raw, _ := sub.Control(ctx(t), "bus.log@v1", nil) // the query path too
	sum := sha256.Sum256(data)
	hash := "sha256:" + hex.EncodeToString(sum[:])
	files, _ := filepath.Glob(filepath.Join(dir, "seg-*.jsonl"))
	var logBytes []byte
	for _, f := range files {
		b, _ := os.ReadFile(f)
		logBytes = append(logBytes, b...)
	}
	for name, b := range map[string][]byte{"log files": logBytes, "bus.log reply": raw} {
		if !bytes.Contains(b, []byte(`"id":"frame"`)) || !bytes.Contains(b, []byte(hash)) || !bytes.Contains(b, []byte(`"sealed":true`)) {
			t.Fatalf("%s lack descriptor+hash: %s", name, b)
		}
		if bytes.Contains(b, []byte(marker)) || bytes.Contains(b, data[:64]) {
			t.Fatalf("%s contain segment contents", name)
		}
		if len(b) > 4096 {
			t.Fatalf("%s is %d bytes for a 2 MiB segment", name, len(b))
		}
	}
}

func TestReceiverCannotWriteSealedSegment(t *testing.T) {
	needSHM(t)
	sock, _, _ := start(t)
	sub, pub := dial(t, sock), dial(t, sock)
	sub.Subscribe(ctx(t), "img.*")
	publishSeg(t, pub, "img.ready", pattern(1<<20))
	e := recv(t, sub.Events())
	defer e.CloseAttachments()
	seg, err := client.OpenSegment(e, "frame")
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()
	if _, err := seg.WriteAt([]byte("x"), 0); err == nil {
		t.Error("write(2) to a sealed segment succeeded")
	}
	if err := seg.File().Truncate(10); err == nil {
		t.Error("shrink succeeded")
	}
	if err := seg.File().Truncate(2 << 20); err == nil {
		t.Error("grow succeeded")
	}
	if b, err := unix.Mmap(int(seg.File().Fd()), 0, 4096, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED); err == nil {
		unix.Munmap(b)
		t.Error("writable shared mmap of a sealed segment succeeded")
	}
	if _, err := unix.FcntlInt(seg.File().Fd(), unix.F_ADD_SEALS, 0); err == nil {
		t.Error("seals were not frozen by F_SEAL_SEAL")
	}
}

func TestNoFDLeakAcrossCycles(t *testing.T) {
	needSHM(t)
	sock, _, _ := start(t)
	sub, pub := dial(t, sock), dial(t, sock)
	sub.Subscribe(ctx(t), "img.*")
	data := pattern(64 << 10)
	publishSeg(t, pub, "img.ready", data) // warm up lazy fds
	w := recv(t, sub.Events())
	w.CloseAttachments()
	base := fdCount(t)
	for i := 0; i < 400; i++ {
		publishSeg(t, pub, "img.ready", data)
		e := recv(t, sub.Events())
		seg, err := client.OpenSegment(e, "frame")
		if err != nil {
			t.Fatal(err)
		}
		m, err := seg.Map()
		if err != nil || m.Bytes()[len(data)/2] != marker[0] {
			t.Fatalf("cycle %d: map: %v", i, err)
		}
		m.Unmap()
		seg.Close()
		e.CloseAttachments()
	}
	settleFDs(t, base)
	t.Logf("fds before %d, after 400 cycles %d", base, fdCount(t))
}

// A receiver that never reads must not make the daemon hold unbounded fds,
// and everything is released when it goes away.
func TestReceiverThatNeverConsumes(t *testing.T) {
	needSHM(t)
	sock, _, _ := start(t)
	slow, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	wire.Write(slow, []byte(`{"kind":"request","to":"bus","verb":"bus.subscribe@v1","payload":{"topic":"img.*"}}`+"\n"), nil)
	pub, insp := dial(t, sock), dial(t, sock)
	pub.Flush(ctx(t))
	time.Sleep(50 * time.Millisecond) // subscription is in place before the load
	base := fdCount(t)
	big := json.RawMessage(`"` + strings.Repeat("x", 200<<10) + `"`)
	for i := 0; i < 600; i++ {
		e := proto.Envelope{Kind: proto.KindEvent, Topic: "img.ready", Payload: big}
		if err := client.AttachBytes(&e, "s", "", []byte("abc")); err != nil {
			t.Fatal(err)
		}
		pub.Send(e)
	}
	pub.Flush(ctx(t))
	raw, err := insp.Control(ctx(t), "bus.inspect@v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var r server.InspectReply
	json.Unmarshal(raw, &r)
	var queued, dropped int
	for _, c := range r.Clients {
		queued += c.Segments.OpenQueued
		dropped += c.Dropped
	}
	if queued == 0 || queued > 256 || dropped == 0 {
		t.Fatalf("daemon holds %d queued fds, dropped %d; want 0 < queued <= 256 and drops", queued, dropped)
	}
	if grew := fdCount(t) - base; grew > 260 {
		t.Fatalf("process fd count grew by %d", grew)
	}
	slow.Close()
	settleFDs(t, base)
}
