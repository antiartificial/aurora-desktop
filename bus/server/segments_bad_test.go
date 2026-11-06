package server_test

import (
	"encoding/json"
	"net"
	"os"
	"testing"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/shm"
	"github.com/antiartificial/aurora/bus/wire"
)

// rawPeer speaks the wire protocol by hand so it can lie about fds.
type rawPeer struct {
	t  *testing.T
	c  *net.UnixConn
	rd *wire.Reader
}

func newRaw(t *testing.T, sock string) *rawPeer {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	p := &rawPeer{t: t, c: c, rd: wire.NewReader(c, 1<<20)}
	t.Cleanup(func() { p.rd.Close(); c.Close() })
	return p
}

func (p *rawPeer) send(declared int, files []*os.File) {
	e := proto.Envelope{Kind: proto.KindEvent, Topic: "x.y"}
	for i := 0; i < declared; i++ {
		e.Segments = append(e.Segments, proto.Segment{ID: string(rune('a' + i)), Bytes: 4, Sealed: true})
	}
	b, _ := json.Marshal(e)
	if err := wire.Write(p.c, append(b, '\n'), files); err != nil {
		p.t.Fatal(err)
	}
}

// errCode reads one reply and returns the error code in it.
func (p *rawPeer) errCode() string {
	var code string
	_, _, err := p.rd.Next(func(l []byte) (int, error) {
		var e proto.Envelope
		json.Unmarshal(l, &e)
		var pl struct{ Code string }
		json.Unmarshal(e.Payload, &pl)
		p.t.Logf("daemon replied: %s", l)
		code = pl.Code
		return 0, nil
	})
	if err != nil {
		p.t.Fatal(err)
	}
	return code
}

func memfds(t *testing.T, n int, seal bool) []*os.File {
	var fs []*os.File
	for i := 0; i < n; i++ {
		var s *shm.Segment
		var err error
		if seal {
			s, err = shm.FromBytes("t", []byte("data"))
		} else {
			s, err = shm.Create("t", 4)
		}
		if err != nil {
			t.Fatal(err)
		}
		fs = append(fs, s.File())
	}
	return fs
}

func closeFs(fs []*os.File) {
	for _, f := range fs {
		f.Close()
	}
}

func TestMoreFDsThanDeclaredRejectedAndClosed(t *testing.T) {
	needSHM(t)
	sock, _, _ := start(t)
	p := newRaw(t, sock)
	warm := memfds(t, 1, true)
	p.send(1, warm) // a valid message warms lazily opened fds (log segment)
	closeFs(warm)
	p.send(1, nil)
	p.errCode() // the daemon has now processed the valid one too
	base := fdCount(t)
	for i := 0; i < 50; i++ { // a resource attack: 4 fds per message, 1 declared
		fs := memfds(t, 4, true)
		p.send(1, fs)
		closeFs(fs)
		if c := p.errCode(); c != "bad_segments" {
			t.Fatalf("round %d: want bad_segments, got %q", i, c)
		}
	}
	settleFDs(t, base)
	// the connection is still good afterwards
	fs := memfds(t, 1, true)
	p.send(1, fs)
	closeFs(fs)
	p.send(1, nil)
	p.errCode() // in-order: the valid message before it has been handled
	settleFDs(t, base)
}

func TestFewerFDsThanDeclaredIsCleanError(t *testing.T) {
	needSHM(t)
	sock, _, _ := start(t)
	p := newRaw(t, sock)
	p.send(1, nil) // warm up lazily opened fds (poller, log segment) before counting
	p.errCode()
	base := fdCount(t)
	fs := memfds(t, 1, true)
	p.send(3, fs)
	closeFs(fs)
	if c := p.errCode(); c != "bad_segments" {
		t.Fatalf("one fd for three declared: got %q", c)
	}
	p.send(2, nil)
	if c := p.errCode(); c != "bad_segments" {
		t.Fatalf("no fds for two declared: got %q", c)
	}
	settleFDs(t, base)
}

func TestUnsealedAndMislabelledSegmentsRejected(t *testing.T) {
	needSHM(t)
	sock, _, _ := start(t)
	p := newRaw(t, sock)
	p.send(1, nil) // warm up lazily opened fds (poller, log segment) before counting
	p.errCode()
	base := fdCount(t)
	fs := memfds(t, 1, false) // memfd, never sealed
	p.send(1, fs)
	closeFs(fs)
	if c := p.errCode(); c != "bad_segments" {
		t.Fatalf("unsealed: got %q", c)
	}
	f, _ := os.CreateTemp(t.TempDir(), "plain") // not a memfd at all
	f.WriteString("data")
	p.send(1, []*os.File{f})
	f.Close()
	if c := p.errCode(); c != "bad_segments" {
		t.Fatalf("regular file: got %q", c)
	}
	fs = memfds(t, 1, true) // sealed, but declares 4 bytes in send(); lie about size
	e := proto.Envelope{Kind: proto.KindEvent, Topic: "x.y", Segments: []proto.Segment{{ID: "a", Bytes: 999, Sealed: true}}}
	b, _ := json.Marshal(e)
	wire.Write(p.c, append(b, '\n'), fs)
	closeFs(fs)
	if c := p.errCode(); c != "bad_segments" {
		t.Fatalf("size lie: got %q", c)
	}
	settleFDs(t, base)
}
