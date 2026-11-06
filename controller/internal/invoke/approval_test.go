package invoke

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/controller/internal/prov"
)

// withApprover is setup() plus an Approver; approval.requested publishes are
// captured on the returned channel. dir is outside ~/Pictures (gesture), pics inside (ambient).
func withApprover(t *testing.T) (*Invoker, string, string, chan ApprovalRequest, *[]prov.Record) {
	t.Helper()
	inv, _, dir, _, recs := setup(t)
	_, pics := homeWith(t, inv)
	reqs := make(chan ApprovalRequest, 4)
	inner := inv.Publish
	inv.Publish = func(topic string, p []byte) error {
		switch topic {
		case TopicRequested:
			var r ApprovalRequest
			json.Unmarshal(p, &r)
			reqs <- r
			return nil
		case TopicDecided:
			return nil
		}
		return inner(topic, p) // provenance.created: setup() collects these
	}
	inv.Approvals = &Approver{Timeout: 5 * time.Second}
	return inv, dir, pics, reqs, recs
}

type outcome struct {
	res *Result
	err *Error
}

func invokeAsync(inv *Invoker, req Request) chan outcome {
	ch := make(chan outcome, 1)
	go func() {
		r, e := inv.Invoke(testCtx(), req)
		ch <- outcome{r, e}
	}()
	return ch
}

func gesture(t *testing.T, dir string) Request {
	return Request{App: "org.aurora.demo", Verb: "delete@v1", Input: putPNG(t, dir, "in.png"), Output: filepath.Join(dir, "gone.png"), Args: map[string]json.RawMessage{}}
}

func waitReq(t *testing.T, reqs chan ApprovalRequest) ApprovalRequest {
	t.Helper()
	select {
	case r := <-reqs:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("no approval.requested published")
		return ApprovalRequest{}
	}
}

func TestDestructiveGestureBlocksUntilApprovedThenRuns(t *testing.T) {
	inv, dir, _, reqs, recs := withApprover(t)
	req := gesture(t, dir)
	done := invokeAsync(inv, req)
	ar := waitReq(t, reqs)
	// Blocked: nothing ran, no output exists, while the request is pending.
	select {
	case o := <-done:
		t.Fatalf("returned before any decision: %+v %+v", o.res, o.err)
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := os.Stat(req.Output); err == nil {
		t.Fatal("output exists while approval is pending")
	}
	if ar.App != "org.aurora.demo" || ar.Verb != "delete@v1" || ar.Effect != "destructive" || ar.RequestID == "" ||
		!strings.HasPrefix(ar.Domain, "domain:app-org-aurora-") || len(ar.Grants) != 2 || ar.Grants[0].Path != req.Input || ar.Grants[0].Source != "gesture" || ar.Grants[1].Mode != "rw" {
		t.Fatalf("request payload %+v", ar)
	}
	if err := inv.Approvals.Decide(ar.RequestID, "allow", "uid1000:aurora"); err != nil {
		t.Fatal(err)
	}
	o := <-done
	if o.err != nil {
		t.Fatal(o.err, o.err.Detail)
	}
	a := o.res.Approval
	if a == nil || !a.Required || a.Route != prov.RouteInteractive || a.Decision != "allow" || a.By != "uid1000:aurora" || a.RequestID != ar.RequestID || a.At == "" || !o.res.NeedsApproval {
		t.Fatalf("reply approval %+v", a)
	}
	if _, err := os.Stat(req.Output); err != nil {
		t.Fatalf("approved verb produced no output: %v", err)
	}
	if got := (*recs)[0].Approval; got == nil || got.Route != prov.RouteInteractive || got.By != "uid1000:aurora" {
		t.Fatalf("provenance approval %+v", got)
	}
	if err := inv.Approvals.Decide(ar.RequestID, "allow", "x"); err != ErrNoSuchRequest {
		t.Fatalf("a decided request must not be decidable again: %v", err)
	}
}

func TestDenialRefusesAndLeavesNoOutput(t *testing.T) {
	inv, dir, _, reqs, recs := withApprover(t)
	req := gesture(t, dir)
	done := invokeAsync(inv, req)
	ar := waitReq(t, reqs)
	inv.Approvals.Decide(ar.RequestID, "deny", "uid1000:aurora")
	o := <-done
	if o.err == nil || o.err.Code != "approval_denied" || !strings.Contains(o.err.Msg, "denied by uid1000:aurora") {
		t.Fatalf("want approval_denied, got %+v", o.err)
	}
	if _, err := os.Stat(req.Output); err == nil {
		t.Fatal("denied invoke left an output file")
	}
	if len(*recs) != 0 {
		t.Fatalf("denied invoke published provenance: %+v", *recs)
	}
}

func TestApprovalTimeoutDeniesByDefault(t *testing.T) {
	inv, dir, _, reqs, _ := withApprover(t)
	inv.Approvals.Timeout = 100 * time.Millisecond
	req := gesture(t, dir)
	done := invokeAsync(inv, req)
	waitReq(t, reqs)
	o := <-done
	if o.err == nil || o.err.Code != "approval_timeout" || !strings.Contains(o.err.Msg, "no decision within 100ms") || !strings.Contains(o.err.Msg, "deny") {
		t.Fatalf("want approval_timeout, got %+v", o.err)
	}
	if _, err := os.Stat(req.Output); err == nil {
		t.Fatal("timed-out invoke left an output file")
	}
}

func TestDefaultApprovalTimeoutIs60s(t *testing.T) {
	if (&Approver{}).timeout() != 60*time.Second {
		t.Fatal("default must be 60s")
	}
}

func TestDecideRejectsBadDecision(t *testing.T) {
	if err := (&Approver{}).Decide("x", "maybe", "u"); err == nil || err == ErrNoSuchRequest {
		t.Fatalf("got %v", err)
	}
}

func TestDestructiveAllAmbientNeedsNoApproval(t *testing.T) {
	inv, _, pics, reqs, _ := withApprover(t)
	req := Request{App: "org.aurora.demo", Verb: "delete@v1", Input: putPNG(t, pics, "in.png"), Output: filepath.Join(pics, "gone.png")}
	res, e := inv.Invoke(testCtx(), req)
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	select {
	case r := <-reqs:
		t.Fatalf("approval requested for ambient grants: %+v", r)
	default:
	}
	a := res.Approval
	if a == nil || a.Required || a.Route != prov.RouteAmbient || a.At == "" || res.NeedsApproval {
		t.Fatalf("approval %+v", a)
	}
}

func TestAutoApproveRunsAndLogsLoudly(t *testing.T) {
	inv, dir, _, reqs, recs := withApprover(t)
	inv.Approvals.AutoApprove = true
	var logs []string
	inv.Logf = func(f string, a ...any) { logs = append(logs, f) }
	res, e := inv.Invoke(testCtx(), gesture(t, dir))
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if a := res.Approval; a == nil || a.Route != prov.RouteAutoApprove || a.By != "controllerd" || a.Decision != "allow" {
		t.Fatalf("approval %+v", a)
	}
	if (*recs)[0].Approval.Route != prov.RouteAutoApprove {
		t.Fatalf("provenance %+v", (*recs)[0].Approval)
	}
	if len(logs) == 0 || !strings.Contains(strings.Join(logs, "\n"), "AUTO-APPROVED") {
		t.Fatalf("auto-approval was not logged: %q", logs)
	}
	select {
	case <-reqs:
		t.Fatal("auto-approve should not ask anyone")
	default:
	}
}

func TestNonDestructiveInvokeUnaffected(t *testing.T) {
	inv, dir, _, reqs, _ := withApprover(t)
	res, e := inv.Invoke(testCtx(), Request{App: "org.aurora.demo", Verb: "copy@v1", Input: putPNG(t, dir, "in.png"), Output: filepath.Join(dir, "c.png")})
	if e != nil {
		t.Fatal(e)
	}
	if res.Approval != nil || res.NeedsApproval || len(reqs) != 0 {
		t.Fatalf("write verb touched approval: %+v", res.Approval)
	}
}

func testCtx() context.Context { return context.Background() }
