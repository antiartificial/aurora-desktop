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
func (iv *Invoker) execute(ctx context.Context, app *apps.App, exe string, argv []string, stdin []byte, grants []prov.Grant, ident *domIdent) (domain.Result, bool, *Error) {
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
	ident.ID, ident.Attested = iv.domainIdentity(name)
	imgExe, err := iv.Runner.InstallImage(name, app.Dir, exe)
	if err != nil {
		return domain.Result{}, false, fail("domain_error", "cannot install app image into %s", err.Error(), name)
	}
	var undo []func()
	defer func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}()
	for _, g := range grants {
		g := g
		if g.UI != "" {
			// Nothing to put in the domain's namespace: the grant is the bus handle minted for it (mintCaps), which the
			// application's tree service reads from the bus's table. It is revoked with the rest, in cs.release.
			continue
		}
		if g.Device != "" {
			if err := iv.Runner.GrantDevice(name, g.Device); err != nil {
				return domain.Result{}, false, fail("domain_error", "grant device %s to %s failed", err.Error(), g.Device, name)
			}
			undo = append(undo, func() {
				if err := iv.Runner.RevokeDevice(name, g.Device); err != nil {
					iv.logf("WARNING: revoke device %s from %s failed (stale grant): %v", g.Device, name, err)
				}
			})
			continue
		}
		if err := iv.Runner.Grant(name, g.Path, g.Mode); err != nil {
			return domain.Result{}, false, fail("domain_error", "grant %s (%s) to %s failed", err.Error(), g.Path, g.Mode, name)
		}
		undo = append(undo, func() {
			if err := iv.Runner.Revoke(name, g.Path); err != nil {
				iv.logf("WARNING: revoke %s from %s failed (stale grant): %v", g.Path, name, err)
			}
		})
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
