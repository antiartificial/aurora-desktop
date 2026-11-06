package server_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/server"
)

// start launches a daemon on a short temp path and returns its socket and log dir.
func start(t *testing.T) (sock, dataDir string, srv *server.Server) {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatal(err)
	}
	sock, dataDir = filepath.Join(dir, "bus.sock"), filepath.Join(dir, "log")
	srv, err = server.New(server.Config{SocketPath: sock, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close(); os.RemoveAll(dir) })
	return
}

func dial(t *testing.T, sock string) *client.Client {
	t.Helper()
	c, err := client.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func recv(t *testing.T, ch <-chan proto.Envelope) proto.Envelope {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for message")
	}
	return proto.Envelope{}
}

func expectedDomain() string {
	return server.DefaultDomain(server.Cred{PID: int32(os.Getpid()), UID: uint32(os.Getuid())})
}

// A client-supplied "from" must be overwritten with the peer-credential identity.
func TestPeerCredIdentityCannotBeSpoofed(t *testing.T) {
	sock, dataDir, _ := start(t)
	sub := dial(t, sock)
	if err := sub.Subscribe(ctx(t), "t.*"); err != nil {
		t.Fatal(err)
	}

	raw, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.Write([]byte(`{"id":"x","from":"uid0:evil-root","kind":"event","topic":"t.spoof","payload":{"n":1}}` + "\n"))

	got := recv(t, sub.Events())
	want := expectedDomain()
	if got.From == "uid0:evil-root" || got.From != want {
		t.Fatalf("from = %q, want %q (kernel-attested)", got.From, want)
	}

	// The log must carry the attested identity too.
	if err := sub.Flush(ctx(t)); err != nil {
		t.Fatal(err)
	}
	msgs, err := buslog.ReadFile(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].From != want {
		t.Fatalf("log = %+v, want one message from %q", msgs, want)
	}
	// Same for a spoofed origin outside replay mode: it is stripped.
	raw.Write([]byte(`{"kind":"event","topic":"t.o","origin":{"id":"a","from":"evil","ts":"0"}}` + "\n"))
	if got := recv(t, sub.Events()); got.Origin != nil {
		t.Fatalf("origin survived outside replay mode: %+v", got.Origin)
	}
}

func TestRequestResponseRoundTrip(t *testing.T) {
	sock, _, _ := start(t)
	svc := dial(t, sock)
	if err := svc.Register(ctx(t), "echo"); err != nil {
		t.Fatal(err)
	}
	go func() {
		for req := range svc.Requests() {
			svc.Reply(req, json.RawMessage(`{"echo":`+string(req.Payload)+`}`))
		}
	}()
	caller := dial(t, sock)
	r, err := caller.Call(ctx(t), proto.Envelope{To: "echo", Verb: "say@v1", Payload: json.RawMessage(`"hi"`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(r.Payload) != `{"echo":"hi"}` || r.Kind != proto.KindResponse {
		t.Fatalf("reply = %+v", r)
	}
	if r.From != expectedDomain() {
		t.Fatalf("response from = %q", r.From)
	}

	// Errors are typed: unknown service, bad verb, duplicate registration.
	_, err = caller.Call(ctx(t), proto.Envelope{To: "nobody", Verb: "x@v1"})
	if re, ok := err.(*client.RemoteError); !ok || re.Code != "no_such_service" {
		t.Fatalf("err = %v", err)
	}
	_, err = caller.Call(ctx(t), proto.Envelope{To: "echo", Verb: "unversioned"})
	if re, ok := err.(*client.RemoteError); !ok || re.Code != "bad_message" {
		t.Fatalf("err = %v", err)
	}
	if err := caller.Register(ctx(t), "echo"); err == nil {
		t.Fatal("duplicate registration accepted")
	}
}

func TestTopicGlobDelivery(t *testing.T) {
	sock, _, _ := start(t)
	sub := dial(t, sock)
	if err := sub.Subscribe(ctx(t), "window.*"); err != nil {
		t.Fatal(err)
	}
	pub := dial(t, sock)
	pub.Publish("audio.muted", nil, "", false)   // must NOT match
	pub.Publish("window.a.deep", nil, "", false) // * is one segment: must NOT match
	pub.Publish("window.opened", nil, "", false) // matches
	pub.Flush(ctx(t))
	got := recv(t, sub.Events())
	if got.Topic != "window.opened" {
		t.Fatalf("first delivered topic = %q, want window.opened", got.Topic)
	}
	select {
	case e := <-sub.Events():
		t.Fatalf("unexpected extra delivery: %s", e.Topic)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestEphemeralDeliveredButNotLogged(t *testing.T) {
	sock, dataDir, _ := start(t)
	sub := dial(t, sock)
	sub.Subscribe(ctx(t), "ptr.*")
	pub := dial(t, sock)
	pub.Publish("ptr.move", json.RawMessage(`{"x":1}`), "", true)
	pub.Publish("ptr.click", json.RawMessage(`{"x":1}`), "", false)
	pub.Flush(ctx(t))
	if recv(t, sub.Events()).Topic != "ptr.move" || recv(t, sub.Events()).Topic != "ptr.click" {
		t.Fatal("both messages must be delivered, in order")
	}
	msgs, err := buslog.ReadFile(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Topic != "ptr.click" {
		t.Fatalf("log = %+v, want only ptr.click", msgs)
	}
}

func TestLogNeverContainsCapabilityValues(t *testing.T) {
	sock, dataDir, _ := start(t)
	sub := dial(t, sock)
	sub.Subscribe(ctx(t), "grant.*")
	pub := dial(t, sock)
	pub.Send(proto.Envelope{Kind: proto.KindEvent, Topic: "grant.file", Caps: []string{"SECRET-HANDLE-123"}})
	pub.Flush(ctx(t))
	got := recv(t, sub.Events())
	if len(got.Caps) != 1 || got.Caps[0] != "SECRET-HANDLE-123" {
		t.Fatalf("recipient must receive the handle, got %v", got.Caps)
	}
	files, _ := filepath.Glob(filepath.Join(dataDir, "*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "SECRET-HANDLE-123") {
			t.Fatal("log contains a handle value")
		}
		if !strings.Contains(string(b), buslog.CapID("SECRET-HANDLE-123")) {
			t.Fatal("log lacks the handle's identity hash")
		}
	}
}

func TestLogQueryOverBus(t *testing.T) {
	sock, _, _ := start(t)
	c := dial(t, sock)
	c.Publish("a.one", nil, "", false)
	c.Publish("b.two", nil, "", false)
	c.Flush(ctx(t))
	raw, err := c.Control(ctx(t), "bus.log@v1", map[string]any{"topic": "a.*", "since": "1m"})
	if err != nil {
		t.Fatal(err)
	}
	var r struct{ Messages []proto.Envelope }
	json.Unmarshal(raw, &r)
	if len(r.Messages) != 1 || r.Messages[0].Topic != "a.one" {
		t.Fatalf("got %+v", r.Messages)
	}
}

func TestInspectAndServiceGone(t *testing.T) {
	sock, _, _ := start(t)
	svc := dial(t, sock)
	svc.Register(ctx(t), "slow")
	sub := dial(t, sock)
	sub.Subscribe(ctx(t), "x.*")
	raw, err := sub.Control(ctx(t), "bus.inspect@v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	var r server.InspectReply
	json.Unmarshal(raw, &r)
	if len(r.Clients) != 2 || len(r.Services) != 1 || r.Services[0].Name != "slow" || len(r.Subscriptions) != 1 {
		t.Fatalf("inspect = %+v", r)
	}
	// A request pending on a service that disconnects fails instead of hanging.
	caller := dial(t, sock)
	go func() { <-svc.Requests(); svc.Close() }()
	_, err = caller.Call(ctx(t), proto.Envelope{To: "slow", Verb: "work@v1"})
	if re, ok := err.(*client.RemoteError); !ok || re.Code != "service_gone" {
		t.Fatalf("err = %v", err)
	}
}

func TestStaleSocketRemovedAndDuplicateDaemonRefused(t *testing.T) {
	dir, _ := os.MkdirTemp("", "bt")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "sub", "bus.sock")
	os.MkdirAll(filepath.Dir(sock), 0o700)
	l, _ := net.Listen("unix", sock)
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close() // leaves a stale socket file behind
	s1, err := server.New(server.Config{SocketPath: sock, DataDir: filepath.Join(dir, "l")})
	if err != nil {
		t.Fatalf("stale socket not handled: %v", err)
	}
	go s1.Serve()
	if _, err := server.New(server.Config{SocketPath: sock, DataDir: filepath.Join(dir, "l2")}); err == nil {
		t.Fatal("second daemon on a live socket must fail")
	}
	s1.Close()
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("socket not removed on close")
	}
}
