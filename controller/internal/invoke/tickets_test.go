package invoke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/controller/internal/prov"
	"github.com/antiartificial/aurora/controller/ticket"
)

var (
	tkey = []byte("0123456789abcdef0123456789abcdef")
	tnow = time.Unix(2_000_000, 0)
)

type pbEnv struct {
	inv      *Invoker
	in, out  string
	now      *time.Time
	runs     func() int
	recs     *[]prov.Record
	clockNow time.Time
}

// pbSetup returns an invoker with tickets enabled, and a file outside the
// manifest's declared paths (so its grants would be bare gestures).
func pbSetup(t *testing.T) *pbEnv {
	inv, fk, _, _, recs := setup(t)
	homeWith(t, inv)
	e := &pbEnv{inv: inv, recs: recs, clockNow: tnow}
	inv.Tickets = &Tickets{Key: tkey, Now: func() time.Time { return e.clockNow }}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	e.in = putPNG(t, dir, "in.png")
	e.out = filepath.Join(dir, "o.png")
	e.runs = func() int {
		n := 0
		for _, c := range fk.Calls {
			if strings.HasPrefix(c, "run ") {
				n++
			}
		}
		return n
	}
	return e
}

func (e *pbEnv) mint(t *testing.T, c ticket.Claims) string {
	t.Helper()
	tok, err := ticket.Mint(tkey, c, e.clockNow, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *pbEnv) inTicket(t *testing.T) string {
	return e.mint(t, ticket.Claims{Path: e.in, Mode: "ro", App: "org.aurora.demo", Verb: "copy@v1"})
}

func (e *pbEnv) req(tok string) Request {
	return Request{App: "org.aurora.demo", Verb: "copy@v1", Input: e.in, Output: e.out, Ticket: tok}
}

func code(e *Error) string {
	if e == nil {
		return ""
	}
	return e.Code
}

func TestValidTicketYieldsPowerboxSource(t *testing.T) {
	e := pbSetup(t)
	res, err := e.inv.Invoke(t.Context(), e.req(e.inTicket(t)))
	if err != nil {
		t.Fatal(err, err.Detail)
	}
	if res.Grants[0].Source != "powerbox" || res.Grants[0].Path != e.in {
		t.Fatalf("input grant %+v", res.Grants[0])
	}
	if res.Grants[1].Source != "gesture" { // the output had no ticket
		t.Fatalf("output grant %+v", res.Grants[1])
	}
	if got := (*e.recs)[0].Grants[0].Source; got != "powerbox" {
		t.Fatalf("provenance.created grant source %q", got)
	}
}

func TestOutputTicketAuthorisesOutputGrant(t *testing.T) {
	e := pbSetup(t)
	r := e.req(e.inTicket(t))
	r.OutputTicket = e.mint(t, ticket.Claims{Path: e.out, Mode: "rw", App: "org.aurora.demo", Verb: "copy@v1"})
	res, err := e.inv.Invoke(t.Context(), r)
	if err != nil {
		t.Fatal(err, err.Detail)
	}
	if res.Grants[0].Source != "powerbox" || res.Grants[1].Source != "powerbox" {
		t.Fatalf("grants %+v", res.Grants)
	}
}

func TestProvenanceXattrCarriesPowerbox(t *testing.T) {
	e := pbSetup(t)
	var got prov.Record
	e.inv.SetProvenance = func(_ string, r prov.Record) error { got = r; return nil }
	if _, err := e.inv.Invoke(t.Context(), e.req(e.inTicket(t))); err != nil {
		t.Fatal(err)
	}
	if got.Grants[0].Source != prov.SourcePowerbox {
		t.Fatalf("xattr record %+v", got.Grants)
	}
	// And the real xattr round-trips the value.
	f := filepath.Join(t.TempDir(), "x")
	os.WriteFile(f, []byte("x"), 0o644)
	if prov.Write(f, got) == nil {
		if back, err := prov.Read(f); err != nil || back.Grants[0].Source != "powerbox" {
			t.Fatalf("read back %+v %v", back, err)
		}
	}
}

func TestTicketRefusals(t *testing.T) {
	other := ticket.Claims{Path: "", Mode: "ro", App: "org.aurora.demo", Verb: "copy@v1"}
	for name, c := range map[string]struct {
		mutate func(e *pbEnv, t *testing.T) string
		want   string
	}{
		"expired": {func(e *pbEnv, t *testing.T) string {
			tok := e.inTicket(t)
			e.clockNow = e.clockNow.Add(2 * time.Minute)
			return tok
		}, ticket.CodeExpired},
		"tampered": {func(e *pbEnv, t *testing.T) string {
			tok := e.inTicket(t)
			b := []byte(tok)
			b[3] ^= 1
			return string(b)
		}, ticket.CodeBadSignature},
		"garbage": {func(e *pbEnv, t *testing.T) string { return "hello" }, ticket.CodeMalformed},
		"other key": {func(e *pbEnv, t *testing.T) string {
			tok, _ := ticket.Mint([]byte("ffffffffffffffffffffffffffffffff"), ticket.Claims{Path: e.in, Mode: "ro", App: "org.aurora.demo", Verb: "copy@v1"}, e.clockNow, time.Minute)
			return tok
		}, ticket.CodeBadSignature},
		"different path": {func(e *pbEnv, t *testing.T) string {
			o := other
			o.Path = e.in + ".other"
			return e.mint(t, o)
		}, ticket.CodeWrongPath},
		"different app": {func(e *pbEnv, t *testing.T) string {
			o := other
			o.Path, o.App = e.in, "org.aurora.lumen"
			return e.mint(t, o)
		}, ticket.CodeWrongApp},
		"different verb": {func(e *pbEnv, t *testing.T) string {
			o := other
			o.Path, o.Verb = e.in, "export@v1"
			return e.mint(t, o)
		}, ticket.CodeWrongVerb},
		"different mode": {func(e *pbEnv, t *testing.T) string {
			o := other
			o.Path, o.Mode = e.in, "rw"
			return e.mint(t, o)
		}, ticket.CodeWrongMode},
	} {
		t.Run(name, func(t *testing.T) {
			e := pbSetup(t)
			tok := c.mutate(e, t)
			_, err := e.inv.Invoke(t.Context(), e.req(tok))
			if code(err) != c.want {
				t.Fatalf("got %v, want %s", err, c.want)
			}
			if e.runs() != 0 {
				t.Fatal("app ran despite refused ticket")
			}
			if _, statErr := os.Stat(e.out); statErr == nil {
				t.Fatal("output created despite refused ticket")
			}
		})
	}
}

func TestReusedTicketRefused(t *testing.T) {
	e := pbSetup(t)
	tok := e.inTicket(t)
	if _, err := e.inv.Invoke(t.Context(), e.req(tok)); err != nil {
		t.Fatal(err)
	}
	os.Remove(e.out)
	_, err := e.inv.Invoke(t.Context(), e.req(tok))
	if code(err) != ticket.CodeReused {
		t.Fatalf("got %v", err)
	}
}

func TestRefusedRequestDoesNotBurnTicket(t *testing.T) {
	e := pbSetup(t)
	e.inv.RequirePowerbox = true // output has no ticket, so this is refused
	tok := e.inTicket(t)
	if _, err := e.inv.Invoke(t.Context(), e.req(tok)); code(err) != "powerbox_required" {
		t.Fatalf("got %v", err)
	}
	e.inv.RequirePowerbox = false
	if _, err := e.inv.Invoke(t.Context(), e.req(tok)); err != nil {
		t.Fatalf("ticket was burned by a refused request: %v", err)
	}
}

func TestNoTicketVerifierRefusesTickets(t *testing.T) {
	e := pbSetup(t)
	e.inv.Tickets = nil
	if _, err := e.inv.Invoke(t.Context(), e.req(e.inTicket(t))); code(err) != "ticket_unavailable" {
		t.Fatalf("got %v", err)
	}
}

func TestRequirePowerbox(t *testing.T) {
	// bare gesture: refused.
	e := pbSetup(t)
	e.inv.RequirePowerbox = true
	_, err := e.inv.Invoke(t.Context(), e.req(""))
	if code(err) != "powerbox_required" || e.runs() != 0 {
		t.Fatalf("gesture: %v", err)
	}
	// ambient: allowed.
	_, pics := homeWith(t, e.inv)
	in := putPNG(t, pics, "in.png")
	res, err := e.inv.Invoke(t.Context(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: in, Output: filepath.Join(pics, "o.png")})
	if err != nil || res.Grants[0].Source != "ambient" {
		t.Fatalf("ambient: %v", err)
	}
	// powerbox (both grants ticketed): allowed.
	r := e.req(e.inTicket(t))
	r.OutputTicket = e.mint(t, ticket.Claims{Path: e.out, Mode: "rw", App: "org.aurora.demo", Verb: "copy@v1"})
	res, err = e.inv.Invoke(t.Context(), r)
	if err != nil || res.Grants[0].Source != "powerbox" || res.Grants[1].Source != "powerbox" {
		t.Fatalf("powerbox: %v", err)
	}
}
