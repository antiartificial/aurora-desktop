package server_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/server"
)

// domainWorld starts a daemon with one real domain socket and returns the
// session socket and the domain socket.
func domainWorld(t *testing.T) (sess, dom string) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "bt")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sess = filepath.Join(dir, "s")
	srv, err := server.New(server.Config{SocketPath: sess, DataDir: filepath.Join(dir, "log")})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	dom = filepath.Join(dir, "d")
	if err := srv.AddDomain("domain:alpha:00112233aabbccdd", dom); err != nil {
		t.Fatal(err)
	}
	return
}

// collect drains events for d and returns the topics seen.
func collect(ch <-chan proto.Envelope, d time.Duration) (topics []string) {
	end := time.After(d)
	for {
		select {
		case e := <-ch:
			topics = append(topics, e.Topic)
		case <-end:
			return
		}
	}
}

// A domain subscribed to ** gets app topics and never the system ones; the
// session subscribed to ** gets both. The canary proves the subscription lives.
func TestDomainStarSubscriptionGetsAppTopicsNotSystemOnes(t *testing.T) {
	sess, dom := domainWorld(t)
	d, s, pub := dial(t, dom), dial(t, sess), dial(t, sess)
	for _, c := range []*client.Client{d, s} {
		if err := c.Subscribe(ctx(t), "**"); err != nil {
			t.Fatalf("a wildcard must be accepted: %v", err)
		}
	}
	for _, topic := range []string{"provenance.created", "approval.requested", "agent.call", "index.match.x", "demo.canary"} {
		if err := pub.Publish(topic, json.RawMessage(`{"file":"/other/domain/secret.png"}`), proto.QoSInteractive, false); err != nil {
			t.Fatal(err)
		}
	}
	pub.Flush(ctx(t))
	got := collect(d.Events(), 500*time.Millisecond)
	if len(got) != 1 || got[0] != "demo.canary" {
		t.Fatalf("domain saw %v, want only [demo.canary]", got)
	}
	if all := collect(s.Events(), 300*time.Millisecond); len(all) != 5 {
		t.Fatalf("the session must keep receiving everything, saw %v", all)
	}
}

// Explicit subscriptions to a system namespace are refused with reserved_topic,
// the code publishing bus.* uses; app topics and wildcards are accepted, and the
// session may subscribe to anything.
func TestDomainCannotSubscribeToSystemNamespaces(t *testing.T) {
	sess, dom := domainWorld(t)
	d, s := dial(t, dom), dial(t, sess)
	for _, topic := range []string{"provenance.created", "provenance.*", "approval.requested", "bus.cap.revoked", "agent.x", "index.match.q"} {
		err := d.Subscribe(ctx(t), topic)
		var re *client.RemoteError
		if !errors.As(err, &re) || re.Code != "reserved_topic" {
			t.Errorf("domain subscribe %q: got %v, want reserved_topic", topic, err)
		}
		if err := s.Subscribe(ctx(t), topic); err != nil {
			t.Errorf("session subscribe %q: %v", topic, err)
		}
	}
	for _, topic := range []string{"demo.canary", "**", "*.created", "provenancex.y"} {
		if err := d.Subscribe(ctx(t), topic); err != nil {
			t.Errorf("domain subscribe %q: %v", topic, err)
		}
	}
}
