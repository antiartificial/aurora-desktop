package server_test

import (
	"encoding/json"
	"errors"
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

const (
	idA = "domain:alpha:00112233aabbccdd"
	idB = "domain:beta:ffeeddcc99887766"
)

type env struct {
	session, sockA, sockB, data string
	srv                         *server.Server
}

// startDomains runs an enforcing daemon with two registered domain sockets.
func startDomains(t *testing.T) env {
	t.Helper()
	dir, err := os.MkdirTemp("", "bd")
	if err != nil {
		t.Fatal(err)
	}
	e := env{session: filepath.Join(dir, "s"), sockA: filepath.Join(dir, "a"), sockB: filepath.Join(dir, "b"), data: filepath.Join(dir, "log")}
	e.srv, err = server.New(server.Config{SocketPath: e.session, DataDir: e.data, EnforceCaps: true})
	if err != nil {
		t.Fatal(err)
	}
	go e.srv.Serve()
	t.Cleanup(func() { e.srv.Close(); os.RemoveAll(dir) })
	if err := e.srv.AddDomain(idA, e.sockA); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.AddDomain(idB, e.sockB); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestDomainSocketIdentityCannotBeSpoofed(t *testing.T) {
	e := startDomains(t)
	sub := dial(t, e.session)
	sub.Subscribe(ctx(t), "t.*")

	raw, err := net.Dial("unix", e.sockA)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	// The client claims to be domain beta, and root. Both are ignored.
	raw.Write([]byte(`{"from":"` + idB + `","kind":"event","topic":"t.one"}` + "\n"))
	raw.Write([]byte(`{"from":"uid0:evil-root","kind":"event","topic":"t.two"}` + "\n"))
	for _, topic := range []string{"t.one", "t.two"} {
		if got := recv(t, sub.Events()); got.Topic != topic || got.From != idA {
			t.Fatalf("%s: from = %q, want %q (socket-attributed)", topic, got.From, idA)
		}
	}
	sub.Flush(ctx(t))
	msgs, _ := buslog.ReadFile(e.data)
	if len(msgs) != 2 || msgs[0].From != idA || msgs[1].From != idA {
		t.Fatalf("log = %+v, want both from %q", msgs, idA)
	}
	// A session-socket client keeps the uid:comm identity, never a domain one.
	sess := dial(t, e.session)
	sess.Publish("t.three", nil, "", false)
	if got := recv(t, sub.Events()); got.From != expectedDomain() || strings.HasPrefix(got.From, "domain:") {
		t.Fatalf("session client from = %q", got.From)
	}
}

func TestDomainIdentitiesDistinctAndStable(t *testing.T) {
	e := startDomains(t)
	sub := dial(t, e.session)
	sub.Subscribe(ctx(t), "who.**")
	a1, a2, b := dial(t, e.sockA), dial(t, e.sockA), dial(t, e.sockB)
	from := func(c *client.Client) string {
		c.Publish("who.am.i", nil, "", false)
		return recv(t, sub.Events()).From
	}
	fa1, fa2, fb := from(a1), from(a2), from(b)
	if fa1 != idA || fa2 != idA || fb != idB || fa1 == fb {
		t.Fatalf("a1=%q a2=%q b=%q", fa1, fa2, fb)
	}
}

func TestDomainRegistrationRules(t *testing.T) {
	e := startDomains(t)
	dir := filepath.Dir(e.session)
	for _, bad := range []string{"uid1000:bash", "domain:x", "domain:x:ZZ", ""} {
		if e.srv.AddDomain(bad, filepath.Join(dir, "x")) == nil {
			t.Errorf("identity %q accepted", bad)
		}
	}
	if e.srv.AddDomain(idA, filepath.Join(dir, "x")) == nil {
		t.Error("duplicate identity accepted")
	}
	if e.srv.AddDomain("domain:c:0011223344", e.sockA) == nil {
		t.Error("socket shared by two identities accepted")
	}
	// A domain cannot register domains; the session socket can.
	d := dial(t, e.sockA)
	_, err := d.Control(ctx(t), "bus.domain.add@v1", map[string]string{"identity": "domain:c:0011223344", "path": filepath.Join(dir, "c")})
	if code(err) != "forbidden" {
		t.Fatalf("domain registering a domain: %v", err)
	}
	s := dial(t, e.session)
	if _, err := s.Control(ctx(t), "bus.domain.add@v1", map[string]string{"identity": "domain:c:0011223344", "path": filepath.Join(dir, "c")}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Dial(filepath.Join(dir, "c")); err != nil {
		t.Fatal(err)
	}
	// Removal disconnects the domain's clients and closes its socket.
	if _, err := s.Control(ctx(t), "bus.domain.remove@v1", map[string]string{"identity": idA}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-d.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("removed domain's client still connected")
	}
	if _, err := client.Dial(e.sockA); err == nil {
		t.Fatal("removed domain socket still accepts")
	}
}

func code(err error) string {
	var re *client.RemoteError
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// capEnv is two domains plus a service that accepts any call it is delivered.
type capEnv struct {
	env
	t    *testing.T
	a, b *client.Client
}

func startCaps(t *testing.T) capEnv {
	e := startDomains(t)
	svc := dial(t, e.session)
	if err := svc.Register(ctx(t), "svc"); err != nil {
		t.Fatal(err)
	}
	go func() {
		for r := range svc.Requests() {
			svc.Reply(r, json.RawMessage(`{"ok":true}`))
		}
	}()
	return capEnv{e, t, dial(t, e.sockA), dial(t, e.sockB)}
}

func (ce capEnv) present(c *client.Client, tok string) error {
	_, err := c.Call(ctx(ce.t), proto.Envelope{To: "svc", Verb: "use@v1", Caps: []string{tok}})
	return err
}

func mint(t *testing.T, c *client.Client, res string, ttlMS int64) (tok, id string) {
	t.Helper()
	raw, err := c.Control(ctx(t), "bus.mint@v1", map[string]any{"resource": res, "ttl_ms": ttlMS})
	if err != nil {
		t.Fatal(err)
	}
	var r struct{ Handle, ID string }
	json.Unmarshal(raw, &r)
	if !strings.HasPrefix(r.Handle, "cap_") || len(r.Handle) < 64 || r.ID != buslog.CapID(r.Handle) {
		t.Fatalf("bad mint reply %s", raw)
	}
	return r.Handle, r.ID
}

func TestCapabilityPolicy(t *testing.T) {
	ce := startCaps(t)
	tok, id := mint(t, ce.a, "file:/doc", 0)

	if err := ce.present(ce.a, "cap_invented"); code(err) != "cap_unknown" {
		t.Fatalf("invented handle: %v", err)
	}
	if err := ce.present(ce.a, tok); err != nil {
		t.Fatalf("holder rejected: %v", err)
	}
	if err := ce.present(ce.b, tok); code(err) != "cap_not_held" {
		t.Fatalf("non-holder (knows the token): %v", err)
	}
	// Session-socket clients are a different identity too.
	if err := ce.present(dial(t, ce.session), tok); code(err) != "cap_not_held" {
		t.Fatalf("session client: %v", err)
	}

	// Transfer a -> b. The daemon delivers the token to b.
	if _, err := ce.a.Control(ctx(t), "bus.transfer@v1", map[string]string{"handle": tok, "to": idB}); err != nil {
		t.Fatal(err)
	}
	g := recv(t, ce.b.Events())
	if g.Topic != "bus.cap.granted" || g.From != "bus" || len(g.Caps) != 1 || g.Caps[0] != tok {
		t.Fatalf("grant notice = %+v", g)
	}
	if err := ce.present(ce.a, tok); code(err) != "cap_not_held" {
		t.Fatalf("old holder after transfer: %v", err)
	}
	if err := ce.present(ce.b, tok); err != nil {
		t.Fatalf("new holder rejected: %v", err)
	}
	if _, err := ce.a.Control(ctx(t), "bus.transfer@v1", map[string]string{"handle": tok, "to": idA}); code(err) != "cap_not_held" {
		t.Fatalf("old holder transferring again: %v", err)
	}
	if _, err := ce.b.Control(ctx(t), "bus.transfer@v1", map[string]string{"handle": tok, "to": "domain:ghost:00112233"}); code(err) != "no_such_domain" {
		t.Fatalf("transfer to nobody: %v", err)
	}

	// Inspect shows id, resource, holder; never the token.
	raw, _ := ce.b.Control(ctx(t), "bus.inspect@v1", nil)
	var ins server.InspectReply
	json.Unmarshal(raw, &ins)
	if len(ins.Handles) != 1 || ins.Handles[0].ID != id || ins.Handles[0].Holder != idB || ins.Handles[0].Minter != idA || ins.Handles[0].Resource != "file:/doc" {
		t.Fatalf("inspect handles = %+v", ins.Handles)
	}
	if strings.Contains(string(raw), tok) {
		t.Fatal("inspect leaks the token")
	}

	// Only the minter revokes, even though it no longer holds the handle.
	if _, err := ce.b.Control(ctx(t), "bus.revoke@v1", map[string]string{"handle": tok}); code(err) != "cap_not_minter" {
		t.Fatalf("holder revoking: %v", err)
	}
	if _, err := ce.a.Control(ctx(t), "bus.revoke@v1", map[string]string{"id": id}); err != nil {
		t.Fatal(err)
	}
	if err := ce.present(ce.b, tok); code(err) != "cap_revoked" {
		t.Fatalf("after revoke: %v", err)
	}
	raw, _ = ce.b.Control(ctx(t), "bus.inspect@v1", nil)
	json.Unmarshal(raw, &ins)
	if len(ins.Handles) != 0 {
		t.Fatalf("revoked handle still listed: %+v", ins.Handles)
	}
}

func TestExpiredHandleRejected(t *testing.T) {
	ce := startCaps(t)
	tok, _ := mint(t, ce.a, "tmp", 80)
	if err := ce.present(ce.a, tok); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := ce.present(ce.a, tok); code(err) != "cap_expired" {
		t.Fatalf("after expiry: %v", err)
	}
}

func TestLogHoldsOnlyHandleIdentity(t *testing.T) {
	ce := startCaps(t)
	tok, id := mint(t, ce.a, "secret-res", 0)
	if err := ce.present(ce.a, tok); err != nil {
		t.Fatal(err)
	}
	ce.a.Control(ctx(t), "bus.transfer@v1", map[string]string{"handle": tok, "to": idB})
	ce.present(ce.b, tok)
	ce.present(ce.a, "cap_forged-xyz") // rejected: must not be logged either
	ce.b.Flush(ctx(t))
	files, _ := filepath.Glob(filepath.Join(ce.data, "*"))
	seen := false
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), tok) || strings.Contains(string(b), "cap_forged") {
			t.Fatal("log contains a handle value")
		}
		seen = seen || strings.Contains(string(b), id)
	}
	if !seen {
		t.Fatal("log lacks the handle identity")
	}
}
