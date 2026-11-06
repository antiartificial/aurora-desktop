package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

func writeFake() *domain.Fake {
	return &domain.Fake{OnRun: func(_ []string, stdin []byte) domain.Result {
		var rq struct{ Output string }
		json.Unmarshal(stdin, &rq)
		os.WriteFile(rq.Output, []byte("x"), 0o644)
		return domain.Result{}
	}}
}

func decide(t *testing.T, c *client.Client, id, decision string) (json.RawMessage, error) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"request_id": id, "decision": decision})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := c.Call(ctx, proto.Envelope{To: "registry", Verb: "approval.decide@v1", Payload: b})
	return r.Payload, err
}

// Over a real busd: a destructive verb blocks, the app's own domain connection
// (identity "domain:...", attested by busd) is refused when it tries to approve
// it, and the session socket's decision lets it run with the route recorded.
func TestAppDomainCannotApproveItsOwnRequest(t *testing.T) {
	sf := &sockFake{Fake: writeFake(), dir: t.TempDir()}
	e := start(t, sf, "../../testdata/apps")
	ctx := context.Background()
	asub := e.dial()
	if err := asub.Subscribe(ctx, invoke.TopicRequested); err != nil {
		t.Fatal(err)
	}
	// A non-gated invoke first, so busd has the app's domain registered.
	in, out := filepath.Join(e.dir, "in.png"), filepath.Join(e.dir, "out.png")
	os.WriteFile(in, png, 0o644)
	if raw, err := e.call(t, "registry.invoke@v1", map[string]any{"app": "org.aurora.demo", "verb": "copy@v1", "input": in, "output": out}); err != nil {
		t.Fatalf("%v %s", err, raw)
	}
	_, sock, _ := sf.Identity(invoke.DomainName("org.aurora.demo"))
	var dom *client.Client
	for i := 0; i < 50 && dom == nil; i++ {
		if c, err := client.Dial(sock); err == nil {
			dom = c
		} else {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if dom == nil {
		t.Fatal("cannot connect to the domain socket")
	}
	defer dom.Close()

	gone := filepath.Join(e.dir, "gone.png")
	type res struct {
		raw json.RawMessage
		err error
	}
	done := make(chan res, 1)
	go func() {
		raw, err := e.call(t, "registry.invoke@v1", map[string]any{"app": "org.aurora.demo", "verb": "delete@v1", "input": in, "output": gone})
		done <- res{raw, err}
	}()
	var ar invoke.ApprovalRequest
	select {
	case ev := <-asub.Events():
		json.Unmarshal(ev.Payload, &ar)
	case <-time.After(5 * time.Second):
		t.Fatal("no approval.requested on the bus")
	}
	if ar.Verb != "delete@v1" || ar.Effect != "destructive" || len(ar.Grants) != 2 {
		t.Fatalf("request %+v", ar)
	}

	raw, err := decide(t, dom, ar.RequestID, "allow")
	if err == nil || !strings.Contains(string(raw), "not_permitted") || !strings.Contains(string(raw), "domain:") {
		t.Fatalf("domain approval must be refused: err=%v payload=%s", err, raw)
	}
	select {
	case r := <-done:
		t.Fatalf("invoke finished after a domain's 'approval': %v %s", r.err, r.raw)
	case <-time.After(200 * time.Millisecond):
	}
	if _, err := os.Stat(gone); err == nil {
		t.Fatal("output exists: the domain approved itself")
	}

	if raw, err := decide(t, e.cli, ar.RequestID, "allow"); err != nil {
		t.Fatalf("session decision refused: %v %s", err, raw)
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("%v %s", r.err, r.raw)
	}
	var reply struct {
		Approval *prov.Approval `json:"approval"`
	}
	json.Unmarshal(r.raw, &reply)
	if a := reply.Approval; a == nil || a.Route != "interactive" || !strings.HasPrefix(a.By, "uid") || a.Decision != "allow" {
		t.Fatalf("reply approval %+v", a)
	}
	if _, err := os.Stat(gone); err != nil {
		t.Fatal("approved verb did not run")
	}
}

func TestDecideUnknownRequestAndBadDecision(t *testing.T) {
	e := start(t, &domain.Fake{}, "../../testdata/apps")
	if raw, err := decide(t, e.cli, "nope", "allow"); err == nil || !strings.Contains(string(raw), "no_such_request") {
		t.Fatalf("unknown id: %v %s", err, raw)
	}
	if raw, err := decide(t, e.cli, "nope", "maybe"); err == nil || !strings.Contains(string(raw), "bad_request") {
		t.Fatalf("bad decision: %v %s", err, raw)
	}
}

func TestMayDecide(t *testing.T) {
	for from, want := range map[string]bool{
		"uid" + itoa(os.Getuid()) + ":aurora": true,
		"domain:app-org-aurora-x:00112233":    false,
		"uid0:evil":                           os.Getuid() == 0,
		"":                                    false,
		"bus":                                 false,
		"uid" + itoa(os.Getuid()) + "0:look-alike": false,
	} {
		if got := mayDecide(from); got != want {
			t.Errorf("mayDecide(%q) = %v, want %v", from, got, want)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
