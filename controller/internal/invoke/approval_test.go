package invoke

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
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

// testKey is the approval key the fakes sign with and the prompts verify with.
var testKey = []byte("0123456789abcdef0123456789abcdef")

// fakeCompositorFn listens like the compositor's trusted socket; each request
// line is decoded into got and answered with whatever answer returns for it.
func fakeCompositorFn(t *testing.T, answer func(m map[string]any, raw []byte) string) (string, chan map[string]any) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trusted.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan map[string]any, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadBytes('\n')
				var m map[string]any
				json.Unmarshal(line, &m)
				got <- m
				// The digest is over the line as sent, newline excluded, which is what the
				// real compositor hashes in trusted/socket.rs.
				fmt.Fprintln(c, answer(m, bytes.TrimRight(line, "\n")))
			}()
		}
	}()
	return path, got
}

// signedAs returns reply with the request id of m and a signature made with
// key, as the real compositor does. `raw` is the request line as it arrived:
// the signature covers its digest, so a fake that does not hash the same bytes
// produces a signature the controller rightly refuses.
func signedAs(key []byte, reply string, m map[string]any, raw []byte) string {
	var a CompositorAnswer
	json.Unmarshal([]byte(reply), &a)
	if a.RequestID == "" {
		a.RequestID, _ = m["request_id"].(string)
	}
	sum := sha256.Sum256(raw)
	a.requestSHA256 = hex.EncodeToString(sum[:])
	a.Sig = SignReply(key, a)
	var obj map[string]any
	json.Unmarshal([]byte(reply), &obj)
	obj["request_id"], obj["sig"] = a.RequestID, a.Sig
	b, _ := json.Marshal(obj)
	return string(b)
}

// fakeCompositor answers every request with reply (a JSON line), signed with testKey.
func fakeCompositor(t *testing.T, reply string) (string, chan map[string]any) {
	return fakeCompositorFn(t, func(m map[string]any, raw []byte) string { return signedAs(testKey, reply, m, raw) })
}

func TestCompositorRouteAllowRecordsRouteAndKey(t *testing.T) {
	inv, dir, _, reqs, recs := withApprover(t)
	sock, got := fakeCompositor(t, `{"v":1,"decision":"allow","key":"a","reason":"key"}`)
	inv.Approvals.Compositor = &CompositorPrompt{Key: testKey, Socket: sock}
	req := gesture(t, dir)
	res, e := inv.Invoke(testCtx(), req)
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	m := <-got
	if m["op"] != "approve" || m["verb"] != "delete@v1" || m["effect"] != "destructive" || m["app"] != "org.aurora.demo" || m["request_id"] == "" {
		t.Fatalf("compositor was shown %+v", m)
	}
	if gs, _ := m["grants"].([]any); len(gs) != 2 {
		t.Fatalf("grants shown: %+v", m["grants"])
	}
	a := res.Approval
	if a == nil || a.Route != prov.RouteCompositor || a.Decision != "allow" || a.By != "compositor" || a.Key != "a" || a.RequestID != m["request_id"] {
		t.Fatalf("approval %+v", a)
	}
	if got := (*recs)[0].Approval; got == nil || got.Route != "compositor" || got.Key != "a" {
		t.Fatalf("provenance %+v", got)
	}
	if _, err := os.Stat(req.Output); err != nil {
		t.Fatalf("allowed verb produced no output: %v", err)
	}
	select {
	case r := <-reqs:
		t.Fatalf("the bus route was used too: %+v", r)
	default:
	}
}

func TestCompositorRouteDenyAndTimeoutRunNothing(t *testing.T) {
	for reply, code := range map[string]string{
		`{"v":1,"decision":"deny","key":"Escape","reason":"key"}`: "approval_denied",
		`{"v":1,"decision":"deny","key":"","reason":"timeout"}`:   "approval_timeout",
	} {
		inv, dir, _, reqs, recs := withApprover(t)
		sock, _ := fakeCompositor(t, reply)
		inv.Approvals.Compositor = &CompositorPrompt{Key: testKey, Socket: sock}
		req := gesture(t, dir)
		_, e := inv.Invoke(testCtx(), req)
		if e == nil || e.Code != code {
			t.Fatalf("%s: got %+v", reply, e)
		}
		if _, err := os.Stat(req.Output); err == nil {
			t.Fatal("denied invoke left an output file")
		}
		if len(*recs) != 0 || len(reqs) != 0 {
			t.Fatalf("denied invoke published: %+v, bus route used: %d", *recs, len(reqs))
		}
	}
}

func TestFallsBackToBusRouteWhenCompositorAbsentOrRefuses(t *testing.T) {
	refuse, _ := fakeCompositor(t, `{"v":1,"error":"uid 1000 is not allowed to request approvals"}`)
	for name, sock := range map[string]string{
		"absent":  filepath.Join(t.TempDir(), "nothing-here.sock"),
		"refused": refuse,
	} {
		inv, dir, _, reqs, recs := withApprover(t)
		inv.Approvals.Compositor = &CompositorPrompt{Key: testKey, Socket: sock}
		var logs []string
		inv.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
		done := invokeAsync(inv, gesture(t, dir))
		ar := waitReq(t, reqs)
		if err := inv.Approvals.Decide(ar.RequestID, "allow", "uid1000:aurora"); err != nil {
			t.Fatal(err)
		}
		o := <-done
		if o.err != nil {
			t.Fatalf("%s: %v %v", name, o.err, o.err.Detail)
		}
		if a := o.res.Approval; a == nil || a.Route != prov.RouteInteractive || a.Key != "" {
			t.Fatalf("%s: approval %+v", name, a)
		}
		if (*recs)[0].Approval.Route != prov.RouteInteractive || !strings.Contains(strings.Join(logs, "\n"), "falling back to the bus route") {
			t.Fatalf("%s: record %+v, logs %q", name, (*recs)[0].Approval, logs)
		}
	}
}

func TestCompositorRouteDoesNotBypassAmbientOrAutoApprove(t *testing.T) {
	inv, _, pics, _, _ := withApprover(t)
	sock, got := fakeCompositor(t, `{"v":1,"decision":"deny","key":"d","reason":"key"}`)
	inv.Approvals.Compositor = &CompositorPrompt{Key: testKey, Socket: sock}
	res, e := inv.Invoke(testCtx(), Request{App: "org.aurora.demo", Verb: "delete@v1", Input: putPNG(t, pics, "in.png"), Output: filepath.Join(pics, "gone.png")})
	if e != nil || res.Approval.Route != prov.RouteAmbient || len(got) != 0 {
		t.Fatalf("ambient grants must not prompt: %+v %+v", e, res)
	}
}
