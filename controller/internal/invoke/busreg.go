package invoke

import "fmt"

// DomainBus registers domains with the bus (see internal/busreg).
type DomainBus interface {
	AddDomain(identity, path string) error
	RemoveDomain(identity string) error
}

type domIdent struct {
	ID       string
	Attested bool
}

// domainIdentity makes sure the domain is registered with busd and returns
// its identity and whether the bus is attesting it. Registration failures
// never fail the invoke: the app still runs, but the record says
// attested=false and the app is not reachable as a domain on the bus.
func (iv *Invoker) domainIdentity(name string) (id string, attested bool) {
	id, sock, err := iv.Runner.Identity(name)
	if err != nil {
		iv.logf("WARNING: cannot read identity of %s: %v", name, err)
		return "", false
	}
	if iv.Bus == nil {
		return id, false
	}
	iv.regMu.Lock()
	defer iv.regMu.Unlock()
	if iv.registered[name] == id {
		return id, true
	}
	if err := iv.Bus.AddDomain(id, sock); err != nil {
		iv.logf("WARNING: bus refused or could not register %s (%s): %v; the app will not appear as its domain on the bus", id, sock, err)
		return id, false
	}
	if iv.registered == nil {
		iv.registered = map[string]string{}
	}
	iv.registered[name] = id
	iv.logf("registered %s with the bus at %s", id, sock)
	return id, true
}

// Teardown unregisters one domain from the bus.
func (iv *Invoker) Teardown(name string) error {
	iv.regMu.Lock()
	defer iv.regMu.Unlock()
	id, ok := iv.registered[name]
	if !ok || iv.Bus == nil {
		return nil
	}
	if err := iv.Bus.RemoveDomain(id); err != nil {
		return fmt.Errorf("unregister %s: %w", id, err)
	}
	delete(iv.registered, name)
	return nil
}

// Close unregisters every domain this controller registered. Domains
// themselves are never destroyed by the controller, so this is the teardown.
func (iv *Invoker) Close() {
	iv.regMu.Lock()
	names := make([]string, 0, len(iv.registered))
	for n := range iv.registered {
		names = append(names, n)
	}
	iv.regMu.Unlock()
	for _, n := range names {
		if err := iv.Teardown(n); err != nil {
			iv.logf("WARNING: %v", err)
		}
	}
}
