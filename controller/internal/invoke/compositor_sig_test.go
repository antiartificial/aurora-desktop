package invoke

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const allowReply = `{"v":1,"decision":"allow","key":"a","reason":"key","domain_verified":"true","domain_verify_detail":"d","domain_source":"bus","domain_identity":"domain:app-x:0011","domain_mismatch":"false"}`

// refused runs an invoke whose compositor answers with answer. The reply must
// be refused: the bus route is what asks (we deny there), nothing runs, and
// the log says why. It returns the log.
func refused(t *testing.T, name string, answer func(m map[string]any) string) string {
	t.Helper()
	inv, dir, _, reqs, recs := withApprover(t)
	sock, _ := fakeCompositorFn(t, answer)
	inv.Approvals.Compositor = &CompositorPrompt{Key: testKey, Socket: sock}
	var logs []string
	inv.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	req := gesture(t, dir)
	done := invokeAsync(inv, req)
	ar := waitReq(t, reqs)
	inv.Approvals.Decide(ar.RequestID, "deny", "uid1000:aurora")
	if o := <-done; o.err == nil || o.err.Code != "approval_denied" {
		t.Fatalf("%s: an allow that failed verification was acted on: %+v", name, o)
	}
	if _, err := os.Stat(req.Output); err == nil {
		t.Fatalf("%s: the verb ran", name)
	}
	if len(*recs) != 0 {
		t.Fatalf("%s: provenance was written: %+v", name, *recs)
	}
	l := strings.Join(logs, "\n")
	if !strings.Contains(l, "REFUSING THE COMPOSITOR'S REPLY") || !strings.Contains(l, "falling back to the bus route") {
		t.Fatalf("%s: reason not logged loudly: %q", name, l)
	}
	return l[strings.Index(l, "WARNING"):]
}

func TestValidSignedReplyIsAcceptedAndRecorded(t *testing.T) {
	inv, dir, _, _, _ := withApprover(t)
	sock, _ := fakeCompositor(t, allowReply)
	inv.Approvals.Compositor = &CompositorPrompt{Key: testKey, Socket: sock}
	res, e := inv.Invoke(testCtx(), gesture(t, dir))
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	a := res.Approval
	if a.Route != "compositor" || a.Decision != Allow || a.Key != "a" || a.DomainSource != "bus" || a.DomainShown != "domain:app-x:0011" || a.DomainVerified != "true" {
		t.Fatalf("%+v", a)
	}
}

func TestUnsignedReplyIsRefusedAndTheControllerFallsBack(t *testing.T) {
	l := refused(t, "unsigned", func(m map[string]any) string {
		return strings.Replace(allowReply, `{"v":1,`, fmt.Sprintf(`{"v":1,"request_id":%q,`, m["request_id"]), 1)
	})
	if !strings.Contains(l, "has no signature") {
		t.Fatal(l)
	}
	t.Log(l)
}

func TestReplySignedWithTheWrongKeyIsRefused(t *testing.T) {
	l := refused(t, "wrong key", func(m map[string]any) string {
		return signedAs([]byte("ffffffffffffffffffffffffffffffff"), allowReply, m)
	})
	if !strings.Contains(l, "does not verify") {
		t.Fatal(l)
	}
}

func TestSignatureReplayedFromAnotherRequestIsRefused(t *testing.T) {
	// A genuine signature, made with the right key, for a different request id.
	old := signedAs(testKey, allowReply, map[string]any{"request_id": "an-earlier-request"})
	l := refused(t, "replay", func(map[string]any) string { return old })
	if !strings.Contains(l, "replayed") || !strings.Contains(l, "an-earlier-request") {
		t.Fatal(l)
	}
	// Even if the attacker rewrites the id to the current request, the signature no longer covers it.
	l = refused(t, "replay with id rewritten", func(m map[string]any) string {
		var raw map[string]any
		json.Unmarshal([]byte(old), &raw)
		raw["request_id"] = m["request_id"]
		b, _ := json.Marshal(raw)
		return string(b)
	})
	if !strings.Contains(l, "does not verify") {
		t.Fatal(l)
	}
}

func TestSignatureThatDoesNotCoverTheDecisionShownIsRefused(t *testing.T) {
	flip := func(field, to string) func(m map[string]any) string {
		return func(m map[string]any) string {
			deny := `{"v":1,"decision":"deny","key":"d","reason":"key","domain_verified":"true","domain_source":"bus","domain_identity":"domain:app-x:0011","domain_mismatch":"false"}`
			var raw map[string]any
			json.Unmarshal([]byte(signedAs(testKey, deny, m)), &raw)
			raw[field] = to
			b, _ := json.Marshal(raw)
			return string(b)
		}
	}
	for field, to := range map[string]string{"decision": "allow", "key": "a", "reason": "key2", "domain_source": "claim", "domain_identity": "domain:evil:9", "domain_mismatch": "true", "domain_verified": "false"} {
		l := refused(t, "flipped "+field, flip(field, to))
		if !strings.Contains(l, "does not verify") {
			t.Fatalf("%s: %s", field, l)
		}
	}
}

func TestSquatterThatAnswersAllowWithoutTheKeyIsRefusedAndNothingRuns(t *testing.T) {
	// It listens first, passes the uid and cgroup check (same process here), and says allow.
	l := refused(t, "squatter, no sig", func(m map[string]any) string { return `{"v":1,"decision":"allow","key":"a","reason":"key"}` })
	if !strings.Contains(l, "no signature") {
		t.Fatal(l)
	}
	l = refused(t, "squatter, made-up sig", func(m map[string]any) string {
		return fmt.Sprintf(`{"v":1,"request_id":%q,"decision":"allow","key":"a","reason":"key","sig":"%064x"}`, m["request_id"], 1)
	})
	if !strings.Contains(l, "does not verify") {
		t.Fatal(l)
	}
}

// The residual, stated as a test: a same-uid squatter that can READ the key
// file signs correctly and is accepted. The key is what is protected.
func TestResidualSquatterThatCanReadTheKeyFileIsAccepted(t *testing.T) {
	inv, dir, _, _, _ := withApprover(t)
	kp := filepath.Join(t.TempDir(), "approval.key")
	cp := &CompositorPrompt{KeyPath: kp}
	k, err := cp.approvalKey() // the controller made it, as whichever starts first does
	if err != nil {
		t.Fatal(err)
	}
	stolen, _ := os.ReadFile(kp) // what any same-uid process can do
	sock, _ := fakeCompositorFn(t, func(m map[string]any) string { return signedAs(stolen, allowReply, m) })
	cp.Socket = sock
	inv.Approvals.Compositor = cp
	res, e := inv.Invoke(testCtx(), gesture(t, dir))
	if e != nil || string(stolen) != string(k) || res.Approval.Route != "compositor" {
		t.Fatalf("expected the key holder to be accepted (this is the documented residual): %v", e)
	}
}

func TestKeyFileIsCreatedPrivateAndALooseOneRefusedBeforeAnythingIsSent(t *testing.T) {
	kp := filepath.Join(t.TempDir(), "sub", "approval.key")
	cp := &CompositorPrompt{KeyPath: kp}
	if _, err := cp.approvalKey(); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(kp); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	os.Chmod(kp, 0o644)
	sock, got := fakeCompositor(t, allowReply)
	cp.Socket = sock
	_, err := cp.Ask(testCtx(), ApprovalRequest{RequestID: "r"}, 1e9)
	if !isSigErr(err) || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("a group-readable key was used: %v", err)
	}
	select {
	case m := <-got:
		t.Fatalf("the socket was dialled with no usable key: %v", m)
	default: // never dialled
	}
}
