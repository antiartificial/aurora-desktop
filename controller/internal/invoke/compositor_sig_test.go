package invoke

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/antiartificial/aurora/controller/internal/prov"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const allowReply = `{"v":1,"decision":"allow","key":"a","reason":"key","domain_verified":"true","domain_verify_detail":"d","domain_source":"bus","domain_identity":"domain:app-x:0011","domain_mismatch":"false"}`

// refused runs an invoke whose compositor answers with answer. The reply must
// be refused: the bus route is what asks (we deny there), nothing runs, and
// the log says why. It returns the log.
func refused(t *testing.T, name string, answer func(m map[string]any, raw []byte) string) string {
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
	l := refused(t, "unsigned", func(m map[string]any, raw []byte) string {
		return strings.Replace(allowReply, `{"v":1,`, fmt.Sprintf(`{"v":1,"request_id":%q,`, m["request_id"]), 1)
	})
	if !strings.Contains(l, "has no signature") {
		t.Fatal(l)
	}
	t.Log(l)
}

func TestReplySignedWithTheWrongKeyIsRefused(t *testing.T) {
	l := refused(t, "wrong key", func(m map[string]any, raw []byte) string {
		return signedAs([]byte("ffffffffffffffffffffffffffffffff"), allowReply, m, raw)
	})
	if !strings.Contains(l, "does not verify") {
		t.Fatal(l)
	}
}

func TestSignatureReplayedFromAnotherRequestIsRefused(t *testing.T) {
	// A genuine signature, made with the right key, for a different request id.
	old := signedAs(testKey, allowReply, map[string]any{"request_id": "an-earlier-request"}, []byte("{}"))
	l := refused(t, "replay", func(map[string]any, []byte) string { return old })
	if !strings.Contains(l, "replayed") || !strings.Contains(l, "an-earlier-request") {
		t.Fatal(l)
	}
	// Even if the attacker rewrites the id to the current request, the signature no longer covers it.
	l = refused(t, "replay with id rewritten", func(m map[string]any, _ []byte) string {
		var obj map[string]any
		json.Unmarshal([]byte(old), &obj)
		obj["request_id"] = m["request_id"]
		b, _ := json.Marshal(obj)
		return string(b)
	})
	if !strings.Contains(l, "does not verify") {
		t.Fatal(l)
	}
}

func TestSignatureThatDoesNotCoverTheDecisionShownIsRefused(t *testing.T) {
	flip := func(field, to string) func(m map[string]any, raw []byte) string {
		return func(m map[string]any, raw []byte) string {
			deny := `{"v":1,"decision":"deny","key":"d","reason":"key","domain_verified":"true","domain_source":"bus","domain_identity":"domain:app-x:0011","domain_mismatch":"false"}`
			var obj map[string]any
			json.Unmarshal([]byte(signedAs(testKey, deny, m, raw)), &obj)
			obj[field] = to
			b, _ := json.Marshal(obj)
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
	l := refused(t, "squatter, no sig", func(m map[string]any, raw []byte) string {
		return `{"v":1,"decision":"allow","key":"a","reason":"key"}`
	})
	if !strings.Contains(l, "no signature") {
		t.Fatal(l)
	}
	l = refused(t, "squatter, made-up sig", func(m map[string]any, raw []byte) string {
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
	sock, _ := fakeCompositorFn(t, func(m map[string]any, raw []byte) string { return signedAs(stolen, allowReply, m, raw) })
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

// The signature covers the digest of the request we SENT, so a reply signed against a
// different request body is refused even when its id matches. Without the digest the
// signature says only "the key holder answered this id" and nothing about the verb, the
// effect or the grants the user was actually shown.
func TestSignatureOverADifferentRequestBodyIsRefused(t *testing.T) {
	l := refused(t, "right id, wrong body", func(m map[string]any, _ []byte) string {
		// Everything genuine except the bytes hashed: this stands for a compositor that
		// showed the user something other than what we asked for.
		other := []byte(`{"op":"approve","verb":"something-else@v1"}`)
		return signedAs(testKey, allowReply, m, other)
	})
	if !strings.Contains(l, "does not verify") {
		t.Fatalf("a reply signed over another request body was accepted: %s", l)
	}
}

// And the control: the same reply, signed over the real bytes, IS accepted. Without this the
// test above would pass even if the digest were ignored and something else were broken.
func TestSignatureOverTheRealRequestBodyIsAccepted(t *testing.T) {
	inv, dir, _, _, _ := withApprover(t)
	sock, _ := fakeCompositorFn(t, func(m map[string]any, raw []byte) string {
		return signedAs(testKey, allowReply, m, raw)
	})
	inv.Approvals.Compositor = &CompositorPrompt{Key: testKey, Socket: sock}
	res, e := inv.Invoke(testCtx(), gesture(t, dir))
	if e != nil {
		t.Fatalf("a correctly signed reply was refused: %v", e)
	}
	if res.Approval == nil || res.Approval.Route != prov.RouteCompositor || res.Approval.Decision != "allow" {
		t.Fatalf("approval %+v", res.Approval)
	}
}

// The request digest crosses a language boundary: the compositor computes it in Rust
// (compositor/src/trusted/sign.rs) and we recompute it here from the bytes we sent. If the
// two ever disagree every signed reply is refused and the compositor route falls back to the
// bus, which presents as a configuration problem rather than as a protocol break.
//
// The vector is pinned on both sides. The Rust assertion is
// cross_language_digest::the_request_digest_is_lowercase_hex_sha256_of_the_line.
func TestRequestDigestMatchesTheCompositorVector(t *testing.T) {
	line := []byte(`{"v":1,"op":"approve","request_id":"r1"}`)
	sum := sha256.Sum256(line)
	const want = "aab51d14477500a94b09cb5d34f6c71fe9506e5be51139c424de2ca5ce8f7ec0"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("digest differs from the compositor's: got %s want %s", got, want)
	}
}
