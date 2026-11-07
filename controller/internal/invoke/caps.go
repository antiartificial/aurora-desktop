package invoke

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/antiartificial/aurora/controller/internal/capbus"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// CapBus is the daemon's capability table as the controller uses it (see
// internal/capbus). nil on an Invoker means no handles are issued and the
// record says so. Capability bookkeeping never fails an invoke.
type CapBus interface {
	Mint(resource string, ttl time.Duration) (capbus.Handle, error)
	Revoke(id string) error
	Present(tokens []string) error
	Publish(topic string, payload []byte, tokens []string) error
	Epoch() (string, error)
	Verify(rec prov.Record) prov.CapReport
}

// capGrace is added to the invoke timeout for a handle's TTL, so a run that
// ends at the deadline can still publish its record carrying the handles.
const capGrace = 10 * time.Second

// capSet is the handles minted for one invoke.
type capSet struct {
	bus    CapBus
	tokens []string
	ids    []string
	epoch  string
	note   string
}

// mintCaps mints one handle per grant, naming the resource and mode, and
// writes each id into grants[i].Cap. It never returns an error: failure
// leaves the grant without a Cap and says why in the set's note. Call
// release (deferred) to revoke what was minted.
func (iv *Invoker) mintCaps(grants []prov.Grant) *capSet {
	cs := &capSet{bus: iv.Caps}
	if iv.Caps == nil {
		cs.note = "no handle issued: no bus capability client configured"
		return cs
	}
	var ttl time.Duration
	if iv.Timeout > 0 {
		ttl = iv.Timeout + capGrace
	}
	var errs []error
	for i := range grants {
		h, err := iv.Caps.Mint(grants[i].Resource(), ttl)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s (%s): %w", grants[i].Path, grants[i].Mode, err))
			continue
		}
		grants[i].Cap = h.ID
		cs.ids, cs.tokens = append(cs.ids, h.ID), append(cs.tokens, h.Token)
	}
	if len(errs) > 0 {
		cs.note = "no handle issued for some grants: " + errors.Join(errs...).Error()
		iv.logf("WARNING: %s", cs.note)
	}
	if len(cs.tokens) == 0 {
		return cs
	}
	if ep, err := iv.Caps.Epoch(); err != nil {
		iv.logf("WARNING: no capability epoch handle: %v (verification after a daemon restart will be inconclusive either way)", err)
	} else {
		cs.epoch = ep
	}
	// The controller holds these handles, so the daemon's holder check must
	// accept them. If it does not, say so in the record; the run proceeds.
	if err := iv.Caps.Present(cs.tokens); err != nil {
		cs.note = "handles minted but the daemon rejected presenting them: " + err.Error()
		cs.tokens = nil
		iv.logf("WARNING: %s", cs.note)
	}
	return cs
}

// release revokes every handle minted for the invoke. Failures are logged.
func (cs *capSet) release(logf func(string, ...any)) {
	for _, id := range cs.ids {
		if err := cs.bus.Revoke(id); err != nil && logf != nil {
			logf("WARNING: revoke of handle %s failed: %v", id, err)
		}
	}
}

// VerifyCaps checks a provenance record's handle ids against the daemon.
func (iv *Invoker) VerifyCaps(rec prov.Record) (prov.CapReport, error) {
	if iv.Caps == nil {
		return prov.CapReport{}, errors.New("this controller has no bus capability client")
	}
	return iv.Caps.Verify(rec), nil
}

// publishRecord sends the provenance event, carrying the handles when they
// were accepted so the bus log records their ids beside the message.
func (iv *Invoker) publishRecord(cs *capSet, payload []byte) error {
	if cs.bus != nil && len(cs.tokens) > 0 {
		return cs.bus.Publish(prov.Topic, payload, cs.tokens)
	}
	if iv.Publish == nil {
		return nil
	}
	return iv.Publish(prov.Topic, payload)
}

// announceHeld says which domain a device grant was just composed into, naming the grant by its handle id. It is
// published on the connection that minted the handle: busd stamps `from` from that connection, so the compositor can
// require the sender to be the handle's minter and ignore anyone else who speaks the topic. Never fails an invoke;
// without a handle (no bus capability client, or the mint failed) there is nothing to tie the statement to and
// nothing is sent, so such a grant is not shown by the indicator (decision 0016, "what this does not cover").
func (iv *Invoker) announceHeld(g prov.Grant, domainIdentity, app string) {
	if iv.Caps == nil || g.Cap == "" || domainIdentity == "" {
		return
	}
	b, _ := json.Marshal(map[string]string{"cap": g.Cap, "resource": g.Resource(), "domain": domainIdentity, "app": app})
	if err := iv.Caps.Publish(prov.TopicHeld, b, nil); err != nil {
		iv.logf("WARNING: could not announce %s held by %s: %v", g.Resource(), domainIdentity, err)
	}
}
