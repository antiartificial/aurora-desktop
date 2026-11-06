package capbus

import (
	"encoding/json"
	"fmt"

	"github.com/antiartificial/aurora/bus/server"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

// Verify asks the daemon what it knows about each handle id in rec.
//
// Evidence available: `bus inspect` lists handles that are neither revoked
// nor expired, with resource and minter. Revoked and expired handles are not
// listed, so for those the only probe is bus.revoke@v1 by id, which is
// idempotent for the minter: success means the row exists and we minted it
// (and it is now certainly dead), cap_not_minter means someone else did,
// cap_unknown means the daemon has no such row.
//
// An unknown id is "never minted" only if the daemon has been running
// since the record was written. That is what rec.CapEpoch is for: the daemon
// never prunes rows, so if it still knows the epoch handle, it never
// restarted, and an unknown id was never minted. If it does not know the
// epoch, the daemon may simply have forgotten (handles are in memory): the
// answer is inconclusive, never negative. A record whose epoch was removed
// or altered therefore degrades to inconclusive, not to verified.
func (b *Client) Verify(rec prov.Record) prov.CapReport {
	rep := prov.CapReport{Note: rec.CapNote}
	var claims []prov.Grant
	for _, g := range rec.Grants {
		if g.Cap != "" {
			claims = append(claims, g)
		}
	}
	if len(claims) == 0 {
		rep.Summarise()
		return rep
	}
	all := func(status, detail string) prov.CapReport {
		for _, g := range claims {
			rep.Caps = append(rep.Caps, prov.CapVerdict{Path: g.Path, Mode: g.Mode, Cap: g.Cap, Status: status, Detail: detail})
		}
		rep.Summarise()
		return rep
	}
	if _, err := b.Epoch(); err != nil { // also learns our own identity
		return all(prov.StatusInconclusive, "cannot reach the daemon: "+err.Error())
	}
	raw, err := b.control("bus.inspect@v1", nil)
	if err != nil {
		return all(prov.StatusInconclusive, "bus inspect failed: "+err.Error())
	}
	var ins server.InspectReply
	if err := json.Unmarshal(raw, &ins); err != nil {
		return all(prov.StatusInconclusive, "unreadable bus inspect reply")
	}
	live := map[string]server.InspectHandle{}
	for _, h := range ins.Handles {
		live[h.ID] = h
	}
	b.mu.Lock()
	self := b.self
	b.mu.Unlock()

	// known: the daemon has a row for id and we minted it.
	known := func(id string) (bool, string) {
		if h, ok := live[id]; ok {
			return h.Minter == self, code(h.Minter == self)
		}
		err := b.Revoke(id)
		return err == nil, Code(err)
	}
	epochKnown := false
	if rec.CapEpoch != "" {
		epochKnown, _ = known(rec.CapEpoch)
	}
	for _, g := range claims {
		v := prov.CapVerdict{Path: g.Path, Mode: g.Mode, Cap: g.Cap}
		want := g.Resource()
		if h, ok := live[g.Cap]; ok {
			switch {
			case h.Minter != self:
				v.Status, v.Detail = prov.StatusNotVerified, fmt.Sprintf("daemon says it was minted by %s, not by the controller (%s)", h.Minter, self)
			case h.Resource != want:
				v.Status, v.Detail = prov.StatusNotVerified, fmt.Sprintf("daemon says it names %q, the record claims %q", h.Resource, want)
			default:
				v.Status, v.Detail = prov.StatusVerified, fmt.Sprintf("outstanding: minted by the controller for %s, not revoked (held by %s)", want, h.Holder)
			}
			rep.Caps = append(rep.Caps, v)
			continue
		}
		ok, c := known(g.Cap)
		b.mu.Lock()
		res, have := b.ledger[g.Cap]
		b.mu.Unlock()
		switch {
		case ok && have && res != want:
			v.Status, v.Detail = prov.StatusNotVerified, fmt.Sprintf("the controller minted it for %q, the record claims %q", res, want)
		case ok && have:
			v.Status, v.Detail = prov.StatusVerified, fmt.Sprintf("minted by the controller (daemon knows the row; no longer outstanding: revoked or expired). Resource %s matches the controller's own ledger; the daemon no longer lists it, so that part is not independent", want)
		case ok:
			v.Status, v.Detail = prov.StatusInconclusive, "daemon knows the row and the controller minted it, but the resource it names cannot be checked any more (revoked handles are not listed and this controller process has no ledger entry)"
		case c == "cap_not_minter":
			v.Status, v.Detail = prov.StatusNotVerified, "the daemon has this handle but it was minted by a different identity than the controller"
		case c == "cap_unknown" && epochKnown:
			v.Status, v.Detail = prov.StatusNotVerified, "the daemon has been up since this record was written (it still knows the record's epoch handle) and has no such handle: it was never minted"
		case c == "cap_unknown":
			v.Status, v.Detail = prov.StatusInconclusive, "the daemon has no such handle, and cannot be shown to have been running when the record was written (epoch handle unknown): forged, or forgotten in a daemon restart"
		default:
			v.Status, v.Detail = prov.StatusInconclusive, "probe failed: error code "+c
		}
		rep.Caps = append(rep.Caps, v)
	}
	rep.Summarise()
	return rep
}

func code(ok bool) string {
	if ok {
		return ""
	}
	return "cap_not_minter"
}
