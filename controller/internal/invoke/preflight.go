package invoke

import (
	"context"
	"fmt"
	"time"

	"github.com/antiartificial/aurora/controller/internal/domain"
)

// Preflight runs a tiny helper (cmd/aurora-preflight) inside the app's domain
// before an approval prompt is shown. The helper publishes approval.preflight
// on the domain's own bus socket; busd stamps the event's `from` with the
// domain identity it derives from that socket and the connector's cgroup. The
// compositor reads that event from the bus log by request id, so it learns
// which domain the request came from without taking the controller's word.
//
// The helper needs no grants and no secret. If it cannot run, the prompt is
// still shown, and the domain is shown as the controller's unverified claim.
// Both routes use it: the compositor reads the event itself; the controller
// reads it for the bus route (preflight_log.go).
type Preflight struct {
	Bin     string        // host path of the aurora-preflight binary (static)
	Timeout time.Duration // default 5s
}

// HelperInstaller is implemented by runners that can place a helper binary in
// a domain's own storage and return its path as seen inside the domain.
type HelperInstaller interface {
	InstallHelper(name, src string) (string, error)
}

// run executes the helper in domain dname for req. It returns "" on success,
// else why the bus will have no attested event; the caller only logs it.
func (p *Preflight) run(ctx context.Context, r domain.Runner, dname string, req ApprovalRequest) (took time.Duration, err error) {
	start := time.Now()
	defer func() { took = time.Since(start) }()
	hi, ok := r.(HelperInstaller)
	if !ok {
		return 0, fmt.Errorf("this domain runner cannot install a helper")
	}
	path, err := hi.InstallHelper(dname, p.Bin)
	if err != nil {
		return 0, fmt.Errorf("install helper: %w", err)
	}
	t := p.Timeout
	if t <= 0 {
		t = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	argv := append([]string{path, req.RequestID, req.App, req.Verb, string(req.Effect)}, grantArgs(req)...)
	res, err := r.Run(ctx, dname, argv, nil)
	if err != nil {
		return 0, err
	}
	if res.Exit != 0 {
		return 0, fmt.Errorf("helper exited %d: %s", res.Exit, string(res.Stderr))
	}
	return 0, nil
}
