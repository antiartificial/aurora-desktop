package surface_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/server"
	contract "github.com/antiartificial/aurora/contracts/surface"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/surface"
)

const (
	appA = "org.example.a"
	appB = "org.example.b"
)

// domains starts a real busd with one socket per app's domain, registered under
// the identity the controller mints (invoke.DomainName), and a session-socket
// "registry" that answers describe exactly as the controller does: gate on the
// bus-stamped From, then marshal the contract.
//
// The gate lives in the responder because that is where the boundary is. A
// client-side check cannot be one: a domain can put any envelope on its own
// socket, and only the daemon decides what From says.
func domains(t *testing.T) (sockA, sockB string) {
	t.Helper()
	dir := t.TempDir()
	session := filepath.Join(dir, "session.sock")
	sockA = filepath.Join(dir, "a.sock")
	sockB = filepath.Join(dir, "b.sock")
	srv, err := server.New(server.Config{SocketPath: session, DataDir: filepath.Join(dir, "log")})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	for id, sock := range map[string]string{
		"domain:" + invoke.DomainName(appA) + ":00112233aabbccdd": sockA,
		"domain:" + invoke.DomainName(appB) + ":ffeeddcc99887766": sockB,
	} {
		if err := srv.AddDomain(id, sock); err != nil {
			t.Fatal(err)
		}
	}

	svc := dial(t, session)
	if err := svc.Register(context.Background(), "registry"); err != nil {
		t.Fatal(err)
	}
	ownedBy := func(name, app string) bool { return name == invoke.DomainName(app) }
	go func() {
		for req := range svc.Requests() {
			var q struct{ App, Verb string }
			if err := json.Unmarshal(req.Payload, &q); err != nil {
				_ = svc.ReplyError(req, "bad_request", err.Error())
				continue
			}
			if e := surface.Permit(req.From, q.App, ownedBy); e != nil {
				p, _ := json.Marshal(e)
				_ = svc.Send(proto.Envelope{Kind: proto.KindError, Verb: req.Verb, Corr: req.ID, Payload: p})
				continue
			}
			d := contract.Descriptor{App: q.App, Title: strings.ToUpper(q.App), Actions: []contract.Action{
				{ID: q.Verb, Label: "Open", Effect: contract.EffectRead},
				{ID: "save@v1", Label: "Save", Effect: contract.EffectWrite, Grants: []string{"path:/one"}},
			}}
			b, err := d.Marshal()
			if err != nil {
				_ = svc.ReplyError(req, "bad_surface", err.Error())
				continue
			}
			_ = svc.Reply(req, json.RawMessage(`{"app":"`+q.App+`","verb":"`+q.Verb+`","surface":`+string(b)+`}`))
		}
	}()
	return sockA, sockB
}

func dial(t *testing.T, sock string) *client.Client {
	t.Helper()
	var (
		c   *client.Client
		err error
	)
	for i := 0; i < 100; i++ {
		if c, err = client.Dial(sock); err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(err)
	return nil
}

// TestDomainCannotDescribeAnotherDomainsSurface is the isolation proof 8.8's
// check asks for, over a real daemon: the identity is the socket's, so domain A
// cannot present itself as anything else, and the registry refuses the request
// with a code instead of a descriptor. A's own request succeeds, and so does the
// session user's for either app -- the shell has to draw both windows.
func TestDomainCannotDescribeAnotherDomainsSurface(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sockA, _ := domains(t)
	a := dial(t, sockA)

	own, err := surface.Describe(ctx, a, appA, "open@v1")
	if err != nil {
		t.Fatalf("domain refused its own surface: %v", err)
	}
	if own.App != appA || len(own.Actions) != 2 {
		t.Fatalf("own surface: %+v", own)
	}

	foreign, err := surface.Describe(ctx, a, appB, "save@v1")
	if err == nil {
		t.Fatalf("domain A was given domain B's surface: %+v", foreign)
	}
	var e *surface.Error
	if !errors.As(err, &e) {
		t.Fatalf("refusal is not structured: %T %v", err, err)
	}
	if e.Code != surface.CodeNotPermitted {
		t.Fatalf("code = %q, want %q (%v)", e.Code, surface.CodeNotPermitted, err)
	}
	if foreign.App != "" || len(foreign.Actions) != 0 {
		t.Fatalf("a refused request still yielded a descriptor: %+v", foreign)
	}
	// The domain in the refusal is the SOCKET's, not a claim: the payload never
	// carried one, and the name is the one the test registered for A.
	if !strings.Contains(e.Message, appB) || !strings.Contains(e.Message, invoke.DomainName(appA)) {
		t.Fatalf("refusal names neither caller nor target: %v", err)
	}

	// The session socket is the shell: it may describe every window, otherwise
	// it could not draw B's chrome.
	sess := dial(t, filepath.Join(filepath.Dir(sockA), "session.sock"))
	for _, app := range []string{appA, appB} {
		d, err := surface.Describe(ctx, sess, app, "open@v1")
		if err != nil {
			t.Fatalf("session refused %s: %v", app, err)
		}
		if d.App != app {
			t.Fatalf("session got %+v for %s", d, app)
		}
	}
}

// TestDomainSocketIdentityIsNotTheCallersClaim guards the premise above: the
// daemon overwrites From, so a domain cannot make itself the session user to
// pass the gate. The payload claims uid0 and domain B; the reply must be about A.
func TestDomainSocketIdentityIsNotTheCallersClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sockA, _ := domains(t)
	a := dial(t, sockA)

	// Describe's payload has no identity field at all, which is the point; this
	// sends the extra claim through a raw call so the daemon, not the helper, is
	// what refuses it.
	payload, _ := json.Marshal(map[string]string{"app": appB, "verb": "save@v1", "from": "uid0:root"})
	r, err := a.Call(ctx, proto.Envelope{To: "registry", Verb: surface.DescribeVerb, Payload: payload})
	var re *client.RemoteError
	if !errors.As(err, &re) || re.Code != surface.CodeNotPermitted {
		t.Fatalf("claimed identity was believed: %v %s", err, r.Payload)
	}
	if strings.Contains(string(r.Payload), "save@v1") {
		t.Fatalf("the refusal carries the descriptor: %s", r.Payload)
	}
	// Still A's identity, even though the request said uid0.
	if !strings.Contains(string(r.Payload), invoke.DomainName(appA)) {
		t.Fatalf("refusal is not attributed to the socket's domain: %s", r.Payload)
	}
}
