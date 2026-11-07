package replay

import (
	"encoding/json"
	"fmt"

	"github.com/antiartificial/aurora/bus/proto"
)

// Lifecycle topics the daemon publishes (server.TopicCap*). Spelled out here
// because replay must not trust the server package's idea of them: this check
// reads what is in the log.
const (
	topicMinted      = "bus.cap.minted"
	topicTransferred = "bus.cap.transferred"
	topicRevoked     = "bus.cap.revoked"
	topicEnded       = "bus.cap.ended"
)

type capState struct {
	minter, holder string
	revoked        bool
	ended          bool // a lease whose request was answered
	uses           int
}

// capLifecycle walks the ORIGINAL log once, in order, and checks that the
// capability lifecycle in it is possible:
//   - lifecycle events come from the daemon ("bus"), never from a client;
//   - a handle is minted once, before anything else happens to it;
//   - a transfer is made by the current holder, names that holder as "from",
//     and happens before any revoke;
//   - a revoke is by the minting domain, of a minted handle, once;
//   - any message presenting a handle (cap_ids) comes from its current holder
//     and not after the revoke.
//
// Unlike the other checks this one does not compare against the replay: a log
// that was edited consistently (so replay reproduces it faithfully) still has
// to make sense on its own. A log with no lifecycle events at all (written
// before they existed, or by a daemon that does not enforce caps) has nothing
// to check; the detail says so rather than claiming a pass on handles.
func capLifecycle(orig []proto.Envelope) CheckResult {
	res := CheckResult{Name: "capability lifecycle"}
	caps := map[string]*capState{}
	var problems []string
	bad := func(e proto.Envelope, f string, a ...any) {
		problems = append(problems, fmt.Sprintf("%s: %s", e.ID, fmt.Sprintf(f, a...)))
	}
	events, minted, revoked, transfers := 0, 0, 0, 0
	for _, e := range orig {
		if e.Kind == proto.KindEvent && (e.Topic == topicMinted || e.Topic == topicTransferred || e.Topic == topicRevoked || e.Topic == topicEnded) {
			events++
			var p struct{ ID, By, Minter, Holder, From, To string }
			if e.From != proto.BusTarget {
				bad(e, "%s published by %q, not by the daemon", e.Topic, e.From)
				continue
			}
			if err := json.Unmarshal(e.Payload, &p); err != nil || p.ID == "" {
				bad(e, "%s has no readable handle id", e.Topic)
				continue
			}
			c := caps[p.ID]
			switch e.Topic {
			case topicMinted:
				minted++
				if c != nil {
					bad(e, "handle %s minted twice", p.ID)
					continue
				}
				if p.Holder != p.By || p.Minter != p.By {
					bad(e, "handle %s minted by %s but holder %s, minter %s", p.ID, p.By, p.Holder, p.Minter)
				}
				caps[p.ID] = &capState{minter: p.Minter, holder: p.Holder}
			case topicTransferred:
				transfers++
				switch {
				case c == nil:
					bad(e, "transfer of handle %s that was never minted", p.ID)
				case c.revoked:
					bad(e, "transfer of handle %s after it was revoked", p.ID)
				case p.From != c.holder || p.By != c.holder:
					bad(e, "handle %s transferred by %s from %s, but %s holds it", p.ID, p.By, p.From, c.holder)
				default:
					c.holder = p.To
				}
			case topicEnded:
				switch {
				case c == nil:
					bad(e, "end of lease %s that was never minted", p.ID)
				case c.ended:
					bad(e, "lease %s ended twice", p.ID)
				default:
					c.ended = true
				}
			case topicRevoked:
				revoked++
				switch {
				case c == nil:
					bad(e, "revoke of handle %s that was never minted", p.ID)
				case c.revoked:
					bad(e, "handle %s revoked twice", p.ID)
				case p.By != c.minter:
					bad(e, "handle %s revoked by %s, but %s minted it", p.ID, p.By, c.minter)
				default:
					c.revoked = true
				}
			}
			continue
		}
		if events == 0 {
			continue // no lifecycle yet: nothing says what a handle id means
		}
		for _, id := range e.CapIDs {
			c := caps[id]
			switch {
			case c == nil:
				bad(e, "presents handle %s that was never minted", id)
			case c.revoked:
				bad(e, "%s presents handle %s after it was revoked", e.From, id)
			case c.ended:
				bad(e, "%s presents lease %s after its request was answered", e.From, id)
			case e.From != c.holder:
				bad(e, "%s presents handle %s held by %s", e.From, id, c.holder)
			default:
				c.uses++
			}
		}
	}
	res.OK = len(problems) == 0
	if events == 0 {
		res.Detail = "no lifecycle events in this log: handle use cannot be checked"
	} else {
		res.Detail = fmt.Sprintf("%d minted, %d transferred, %d revoked, %d never revoked", minted, transfers, revoked, len(caps)-revoked)
	}
	if !res.OK {
		res.Detail += "; " + summarize(problems)
	}
	return res
}
