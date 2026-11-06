package invoke

import (
	"bytes"
	"context"
	"os/exec"

	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// execute runs argv for app, inside its domain when domain tooling works.
// Inside the domain the grants are exactly the planned ones (input ro, the
// output file rw, see planGrants), revoked afterwards. Without domain tooling it runs the app
// directly on the host, says so loudly, and reports isolated=false.
func (iv *Invoker) execute(ctx context.Context, app *apps.App, exe string, argv []string, stdin []byte, grants []prov.Grant) (domain.Result, bool, *Error) {
	if err := iv.Runner.Available(); err != nil {
		if iv.RequireIsolation {
			return domain.Result{}, false, fail("isolation_unavailable", "domain tooling unavailable and isolation is required", err.Error())
		}
		iv.logf("WARNING: running %s UNISOLATED on the host (no domain): %v", app.ID(), err)
		return iv.direct(ctx, exe, app.Dir, argv, stdin)
	}
	name := DomainName(app.ID())
	if err := iv.Runner.Ensure(name); err != nil {
		return domain.Result{}, false, fail("domain_error", "cannot create domain %s", err.Error(), name)
	}
	imgExe, err := iv.Runner.InstallImage(name, app.Dir, exe)
	if err != nil {
		return domain.Result{}, false, fail("domain_error", "cannot install app image into %s", err.Error(), name)
	}
	var granted []string
	defer func() {
		for i := len(granted) - 1; i >= 0; i-- {
			if err := iv.Runner.Revoke(name, granted[i]); err != nil {
				iv.logf("WARNING: revoke %s from %s failed (stale grant): %v", granted[i], name, err)
			}
		}
	}()
	for _, g := range grants {
		if err := iv.Runner.Grant(name, g.Path, g.Mode); err != nil {
			return domain.Result{}, false, fail("domain_error", "grant %s (%s) to %s failed", err.Error(), g.Path, g.Mode, name)
		}
		granted = append(granted, g.Path)
	}
	res, err := iv.Runner.Run(ctx, name, append([]string{imgExe}, argv...), stdin)
	if err != nil {
		return res, true, fail("domain_error", "running in domain %s failed", err.Error(), name)
	}
	return res, true, nil
}

func (iv *Invoker) direct(ctx context.Context, exe, dir string, argv []string, stdin []byte) (domain.Result, bool, *Error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, argv...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := domain.Result{Stdout: out.Bytes(), Stderr: errb.Bytes()}
	if ee, ok := err.(*exec.ExitError); ok {
		res.Exit = ee.ExitCode()
	} else if err != nil {
		return res, false, fail("exec_error", "cannot run app", err.Error())
	}
	return res, false, nil
}
