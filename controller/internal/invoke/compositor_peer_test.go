package invoke

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/controller/internal/prov"
)

func ownCgroup(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Skip("no /proc/self/cgroup")
	}
	p := cgroupV2Path(string(b))
	if p == "" {
		t.Skip("no cgroup v2 path")
	}
	return p
}

func TestControllerAcceptsALegitimateCompositorAndLogsItsCredentials(t *testing.T) {
	inv, dir, _, _, recs := withApprover(t)
	sock, got := fakeCompositor(t, `{"v":1,"decision":"allow","key":"a","reason":"key","domain_verified":"true","domain_verify_detail":"registered and cgroup-attested by busd; 1 live connection(s) now"}`)
	var logs []string
	cp := &CompositorPrompt{Key: testKey, Socket: sock, Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }}
	inv.Approvals.Compositor = cp
	if _, e := inv.Invoke(testCtx(), gesture(t, dir)); e != nil {
		t.Fatal(e, e.Detail)
	}
	m := <-got
	if m["domain_identity"] == nil || !strings.HasPrefix(m["domain_identity"].(string), "domain:") || strings.Contains(m["domain"].(string), "attested") {
		t.Fatalf("wire domain fields: domain=%v identity=%v", m["domain"], m["domain_identity"])
	}
	if l := strings.Join(logs, "\n"); !strings.Contains(l, "compositor peer accepted") || !strings.Contains(l, fmt.Sprintf("uid %d", os.Geteuid())) {
		t.Fatalf("accepted peer not logged: %q", l)
	}
	// provenance carries the verification result, on the result and in the record
	if a := (*recs)[0].Approval; a == nil || a.DomainVerified != "true" || !strings.Contains(a.DomainVerifyDetail, "cgroup-attested") {
		t.Fatalf("provenance %+v", a)
	}
}

func TestProvenanceCarriesFalseAndUnknownVerification(t *testing.T) {
	for reply, want := range map[string]string{
		`{"v":1,"decision":"allow","key":"a","reason":"key","domain_verified":"false","domain_verify_detail":"busd has no domain registered under this identity"}`: "false",
		`{"v":1,"decision":"allow","key":"a","reason":"key","domain_verified":"unknown","domain_verify_detail":"bus unreachable"}`:                                 "unknown",
		`{"v":1,"decision":"allow","key":"a","reason":"key"}`:                                "unknown", // a compositor that predates the check
		`{"v":1,"decision":"allow","key":"a","reason":"key","domain_verified":"yes please"}`: "unknown", // never trust junk as verified
	} {
		inv, dir, _, _, recs := withApprover(t)
		sock, _ := fakeCompositor(t, reply)
		inv.Approvals.Compositor = &CompositorPrompt{Key: testKey, Socket: sock}
		if _, e := inv.Invoke(testCtx(), gesture(t, dir)); e != nil {
			t.Fatal(e, e.Detail)
		}
		if a := (*recs)[0].Approval; a == nil || a.DomainVerified != want {
			t.Fatalf("%s: provenance %+v, want %s", reply, a, want)
		}
	}
}

func TestControllerRefusesAnImpostorAndFallsBackWithTheReasonLogged(t *testing.T) {
	own := uint32(os.Geteuid())
	other := own + 1
	cg := ownCgroup(t)
	cases := map[string]*CompositorPrompt{
		"wrong uid":            {ExpectedUID: &other},
		"inside a domain":      {DenyCgroupComponents: []string{filepath.Base(cg)}}, // this process stands in for a domain-confined peer
		"inside denied cgroup": {DenyCgroupPrefixes: []string{cg}},
	}
	for name, cp := range cases {
		inv, dir, _, reqs, recs := withApprover(t)
		// An impostor that would say "allow" if it were ever asked.
		sock, got := fakeCompositor(t, `{"v":1,"decision":"allow","key":"a","reason":"key","domain_verified":"true"}`)
		cp.Socket, cp.Key = sock, testKey
		inv.Approvals.Compositor = cp
		var logs []string
		inv.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
		done := invokeAsync(inv, gesture(t, dir))
		ar := waitReq(t, reqs) // the bus route is what asks now
		if err := inv.Approvals.Decide(ar.RequestID, "deny", "uid1000:aurora"); err != nil {
			t.Fatal(err)
		}
		if o := <-done; o.err == nil || o.err.Code != "approval_denied" || !strings.Contains(o.err.Msg+o.err.Detail, "uid1000:aurora") {
			t.Fatalf("%s: %+v", name, o.err)
		}
		// The impostor sees a connection and then a hang-up; never a request.
		if m := <-got; len(m) != 0 {
			t.Fatalf("%s: the request reached the impostor: %+v", name, m)
		}
		l := strings.Join(logs, "\n")
		if !strings.Contains(l, "REFUSING THE COMPOSITOR ROUTE") || !strings.Contains(l, "failed the credential check") || !strings.Contains(l, "falling back to the bus route") {
			t.Fatalf("%s: reason not logged loudly: %q", name, l)
		}
		if len(*recs) != 0 {
			t.Fatalf("%s: published %+v", name, *recs)
		}
		t.Logf("%s: %s", name, l[strings.Index(l, "WARNING"):])
	}
}

func TestPeerErrorIsTypedAndNothingIsSent(t *testing.T) {
	sock, got := fakeCompositor(t, `{"v":1,"decision":"allow"}`)
	other := uint32(os.Geteuid()) + 1
	_, err := (&CompositorPrompt{Key: testKey, Socket: sock, ExpectedUID: &other}).Ask(testCtx(), ApprovalRequest{RequestID: "r"}, 1e9)
	if !isPeerErr(err) || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("%v", err)
	}
	if m := <-got; len(m) != 0 {
		t.Fatalf("request sent to a refused peer: %+v", m)
	}
}

func TestUnreadableCgroupFailsClosed(t *testing.T) {
	cp := &CompositorPrompt{Socket: "s"}
	if err := cp.checkPeer(Peer{UID: os.Geteuid(), PID: 1}); err == nil {
		t.Fatal("accepted a peer whose cgroup is unknown")
	}
	if err := cp.checkPeer(Peer{UID: os.Geteuid(), PID: 1, Cgroup: "/user.slice/aurora-domains/x"}); err == nil {
		t.Fatal("accepted a peer in aurora-domains")
	}
	if err := cp.checkPeer(Peer{UID: os.Geteuid(), PID: 1, Cgroup: "/user.slice/app.slice/aurora-compositor.service"}); err != nil {
		t.Fatal(err)
	}
	var _ = prov.RouteCompositor
}
