package invoke

import (
	"regexp"

	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

var uiResourceOK = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,128}$`)

// bindUIGrants says which domain each ui grant is for (decision 0017). Unlike a path or a device, a ui grant has no
// effect in the domain's namespace: it exists only as a bus handle, minted by the controller for
// "ui:<class>:<domain identity>", and the application's tree service honours a domain's request only while the bus's
// own table holds that handle. So it is worth nothing unless the bus attests the identity, the controller can mint,
// and the resource fits the daemon's syntax; each of those is refused rather than quietly dropped, because a grant
// the record names but the bus cannot enforce is worse than no grant.
func (iv *Invoker) bindUIGrants(app *apps.App, grants []prov.Grant) *Error {
	var any bool
	for _, g := range grants {
		any = any || g.UI != ""
	}
	if !any {
		return nil
	}
	if iv.Caps == nil {
		return fail("ui_grant_unavailable", "%s lists ui:read but this controller has no bus capability client, so the grant could not exist", "", app.ID())
	}
	if err := iv.Runner.Available(); err != nil {
		return fail("ui_grant_unavailable", "%s lists ui:read, a grant to a domain, but domain tooling is unavailable", err.Error(), app.ID())
	}
	name := DomainName(app.ID())
	if err := iv.Runner.Ensure(name); err != nil {
		return fail("domain_error", "cannot create domain %s", err.Error(), name)
	}
	id, attested := iv.domainIdentity(name)
	if id == "" || !attested {
		return fail("ui_grant_unavailable", "%s lists ui:read but the bus does not attest domain %s, so a handle for it would name an identity the bus cannot stand behind", "", app.ID(), name)
	}
	for i := range grants {
		if grants[i].UI == "" {
			continue
		}
		grants[i].UIFor = id
		if r := grants[i].Resource(); !uiResourceOK.MatchString(r) {
			return fail("ui_grant_unavailable", "the handle name %q does not fit the bus's resource syntax", "", r)
		}
	}
	return nil
}
