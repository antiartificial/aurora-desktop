package invoke

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/controller/internal/domain"
)

type okBus struct{}

func (okBus) AddDomain(string, string, string) error { return nil }
func (okBus) RemoveDomain(string) error              { return nil }

func preflightSetup(t *testing.T, reply string) (*Invoker, *domain.Fake, string, chan map[string]any) {
	t.Helper()
	inv, dir, _, _, _ := withApprover(t)
	fk := inv.Runner.(*domain.Fake)
	inv.Bus = okBus{}
	sock, got := fakeCompositor(t, reply)
	inv.Approvals.Compositor = &CompositorPrompt{Key: testKey, Socket: sock}
	inv.Approvals.Preflight = &Preflight{Bin: "/host/bin/aurora-preflight"}
	return inv, fk, dir, got
}

const busAnswer = `{"v":1,"decision":"allow","key":"a","reason":"key","domain_verified":"true","domain_source":"bus","domain_identity":"domain:app-org-aurora-x:00112233aabbccdd","domain_mismatch":"true","domain_verify_detail":"d"}`

func TestPreflightRunsInsideTheDomainBeforeThePromptAndProvenanceRecordsTheBusSource(t *testing.T) {
	inv, fk, dir, got := preflightSetup(t, busAnswer)
	res, e := inv.Invoke(testCtx(), gesture(t, dir))
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	m := <-got
	d := DomainName("org.aurora.demo")
	var pf string
	for _, c := range fk.Calls {
		if strings.HasPrefix(c, "run "+d+" /domain/data/helpers/aurora-preflight ") {
			pf = c
		}
	}
	if pf == "" || !strings.Contains(pf, m["request_id"].(string)+" org.aurora.demo delete@v1 destructive") {
		t.Fatalf("helper was not run in the domain with the request id and verb: %q", fk.Calls)
	}
	a := res.Approval
	if a.DomainSource != "bus" || a.DomainShown != "domain:app-org-aurora-x:00112233aabbccdd" || !a.DomainMismatch || a.DomainVerified != "true" {
		t.Fatalf("provenance did not record the bus source, shown identity and mismatch: %+v", a)
	}
}

func TestPreflightFailureStillPromptsAndRecordsAClaim(t *testing.T) {
	inv, fk, dir, got := preflightSetup(t, `{"v":1,"decision":"allow","key":"a","reason":"key","domain_verified":"true","domain_source":"claim"}`)
	fk.OnRun = func(argv []string, stdin []byte) domain.Result {
		if strings.Contains(argv[0], "preflight") {
			return domain.Result{Exit: 1, Stderr: []byte("bus refused")}
		}
		return writeOut(argv, stdin)
	}
	req := gesture(t, dir)
	res, e := inv.Invoke(testCtx(), req)
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	<-got // the prompt was still sent
	if res.Approval.DomainSource != "claim" || res.Approval.DomainMismatch {
		t.Fatalf("a failed helper must record a claim: %+v", res.Approval)
	}
	if _, err := os.Stat(req.Output); err != nil {
		t.Fatal("verb did not run after an allowed prompt")
	}
}

func TestNoPreflightHelperInstallableStillPrompts(t *testing.T) {
	inv, fk, dir, got := preflightSetup(t, `{"v":1,"decision":"allow","key":"a","reason":"key"}`)
	inv.Runner = onlyRunner{fk} // a runner without InstallHelper
	res, e := inv.Invoke(testCtx(), gesture(t, dir))
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	<-got
	if res.Approval.DomainSource != "claim" {
		t.Fatalf("%+v", res.Approval)
	}
}

// onlyRunner hides every method but the Runner interface's.
type onlyRunner struct{ r domain.Runner }

func (o onlyRunner) Available() error                            { return o.r.Available() }
func (o onlyRunner) Ensure(n string) error                       { return o.r.Ensure(n) }
func (o onlyRunner) InstallImage(n, s, e string) (string, error) { return o.r.InstallImage(n, s, e) }
func (o onlyRunner) Identity(n string) (string, string, error)   { return o.r.Identity(n) }
func (o onlyRunner) Cgroup(n string) (string, error)             { return o.r.Cgroup(n) }
func (o onlyRunner) Grant(n, p, m string) error                  { return o.r.Grant(n, p, m) }
func (o onlyRunner) Revoke(n, p string) error                    { return o.r.Revoke(n, p) }
func (o onlyRunner) GrantDevice(n, c string) error               { return o.r.GrantDevice(n, c) }
func (o onlyRunner) RevokeDevice(n, c string) error              { return o.r.RevokeDevice(n, c) }
func (o onlyRunner) Start(c context.Context, n string, a []string) (domain.Proc, error) {
	return o.r.Start(c, n, a)
}
func (o onlyRunner) Destroy(n string) error { return o.r.Destroy(n) }
func (o onlyRunner) Run(c context.Context, n string, a []string, s []byte) (domain.Result, error) {
	return o.r.Run(c, n, a, s)
}
