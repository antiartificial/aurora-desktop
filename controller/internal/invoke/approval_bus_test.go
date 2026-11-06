package invoke

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antiartificial/aurora/controller/internal/domain"
)

// logBus is a DomainBus whose log holds the approval.preflight events the
// helper "published": it reads the helper's argv from the fake domain runner
// and stamps from, as busd would.
type logBus struct {
	okBus
	mu    sync.Mutex
	argv  []string
	from  string
	age   time.Duration
	extra []string // more senders for the same request id
	none  bool     // the log holds no event at all
}

func (b *logBus) Log(topic, since string, limit int) (json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var ms []map[string]any
	if len(b.argv) >= 5 && !b.none {
		for _, from := range append([]string{b.from}, b.extra...) {
			ns := time.Now().Add(-b.age).UnixNano()
			ms = append(ms, map[string]any{"kind": "event", "topic": topic, "from": from, "ts": fmt.Sprintf("%019d-00000000000000000001", ns),
				"payload": map[string]any{"request_id": b.argv[1], "app": b.argv[2], "verb": b.argv[3], "effect": b.argv[4], "grants": b.argv[5:]}})
		}
	}
	return json.Marshal(map[string]any{"messages": ms})
}

// busRoute sets up an invoker with NO compositor but a pre-flight helper, and
// returns the approval.requested it publishes after deciding allow.
func busRoute(t *testing.T, bus *logBus, helperExit int) (ApprovalRequest, *Result, *Error, string) {
	t.Helper()
	inv, dir, _, reqs, _ := withApprover(t)
	fk := inv.Runner.(*domain.Fake)
	fk.OnRun = func(argv []string, stdin []byte) domain.Result {
		if strings.Contains(argv[0], "preflight") {
			bus.mu.Lock()
			bus.argv = argv
			bus.mu.Unlock()
			return domain.Result{Exit: helperExit}
		}
		return writeOut(argv, stdin)
	}
	inv.Bus = bus
	inv.Approvals.Preflight = &Preflight{Bin: "/host/bin/aurora-preflight"}
	var logs []string
	inv.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	done := invokeAsync(inv, gesture(t, dir))
	ar := waitReq(t, reqs)
	if err := inv.Approvals.Decide(ar.RequestID, "allow", "uid1000:aurora"); err != nil {
		t.Fatal(err)
	}
	o := <-done
	return ar, o.res, o.err, strings.Join(logs, "\n")
}

func ownIdentity(t *testing.T) string {
	id, _, _ := (&domain.Fake{}).Identity(DomainName("org.aurora.demo"))
	return id
}

func TestBusRouteCarriesTheBusAttestedIdentityAndRecordsDomainSource(t *testing.T) {
	id := ownIdentity(t)
	ar, res, e, _ := busRoute(t, &logBus{from: id}, 0)
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if ar.DomainSource != "bus" || !ar.DomainAttested || ar.Domain != id || ar.DomainMismatch {
		t.Fatalf("approval.requested does not carry the attested identity: %+v", ar)
	}
	a := res.Approval
	if a.Route != "interactive" || a.DomainSource != "bus" || a.DomainShown != id || a.DomainVerified != "true" || a.DomainMismatch {
		t.Fatalf("provenance: %+v", a)
	}
}

func TestBusRouteShowsTheBusDomainAndFlagsAControllerMismatch(t *testing.T) {
	ar, res, e, logs := busRoute(t, &logBus{from: "domain:app-other:99aa"}, 0)
	if e != nil {
		t.Fatal(e)
	}
	if ar.Domain != "domain:app-other:99aa" || !ar.DomainMismatch || ar.DomainClaim != ownIdentity(t) || ar.DomainName != "app-other" || ar.DomainSource != "bus" {
		t.Fatalf("%+v", ar)
	}
	if !res.Approval.DomainMismatch || res.Approval.DomainShown != "domain:app-other:99aa" || !strings.Contains(logs, "the bus attests") {
		t.Fatalf("%+v\n%s", res.Approval, logs)
	}
}

func TestBusRouteWithNoPreflightEventShowsAndRecordsAClaim(t *testing.T) {
	for name, bus := range map[string]*logBus{
		"helper ran but no event is in the log": {none: true},
		"event from a session socket":           {from: "uid1000:aurora"},
		"stale event":                           {from: ownIdentity(t), age: 5 * time.Minute},
		"two domains for one id":                {from: ownIdentity(t), extra: []string{"domain:app-b:2"}},
	} {
		ar, res, e, logs := busRoute(t, bus, 0)
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		if ar.DomainSource != "claim" || ar.DomainAttested || ar.DomainMismatch {
			t.Fatalf("%s: must be a claim: %+v", name, ar)
		}
		a := res.Approval
		if a.DomainSource != "claim" || a.DomainVerified != "unknown" || !strings.Contains(a.DomainVerifyDetail, "not attested") || a.DomainShown != ownIdentity(t) {
			t.Fatalf("%s: provenance %+v", name, a)
		}
		if !strings.Contains(logs, "unverified CLAIM") {
			t.Fatalf("%s: not logged: %s", name, logs)
		}
	}
}

func TestBusRouteHelperFailureAndNoHelperAreClaims(t *testing.T) {
	ar, res, e, logs := busRoute(t, &logBus{from: ownIdentity(t)}, 1)
	if e != nil || ar.DomainSource != "claim" || res.Approval.DomainSource != "claim" || !strings.Contains(logs, "pre-flight in") {
		t.Fatalf("%v %+v %s", e, ar, logs)
	}
	inv, dir, _, reqs, _ := withApprover(t) // no Preflight configured
	done := invokeAsync(inv, gesture(t, dir))
	ar2 := waitReq(t, reqs)
	inv.Approvals.Decide(ar2.RequestID, "allow", "uid1000:aurora")
	o := <-done
	if o.err != nil || ar2.DomainSource != "claim" || ar2.DomainAttested || o.res.Approval.DomainSource != "claim" {
		t.Fatalf("%+v %+v", ar2, o)
	}
}
