package service

import (
	"encoding/json"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	ctlsurface "github.com/antiartificial/aurora/controller/internal/surface"
)

// listReply is surface.list@v1's answer. The descriptors travel as raw JSON
// because Descriptor.Marshal is what makes them canonical; re-marshalling a
// decoded struct would derive the bytes from field order instead, and the
// sorted shape would no longer be guaranteed by the contract.
type listReply struct {
	Surfaces []json.RawMessage `json:"surfaces"`
}

// listSurfaces serves surface.list@v1 {}.
//
// # WHY THE LIST IS PUBLISHED-ONLY
//
// It returns exactly what apps announced over surface.publish@v1 and nothing
// from a manifest. registry.describe@v1 already relays manifest-derived
// descriptors one app at a time under surfaceCaller; folding those into this
// list would hand a manifest the standing of a person's grant simply because
// the two arrived in the same array, which is the authority confusion the
// surface contract exists to prevent.
//
// # THE CALLER
//
// A list request names no app, so there is no natural argument to hand
// surfaceCaller. The rule is stated here instead: the session may read it, and
// any named domain may read it. The second half is deliberate, not a
// loosening. A published surface is an announcement its own app chose to put
// on the bus for the compositor to draw, so publishing is itself the
// disclosure; a read of the set tells a domain nothing the bus had not already
// been told. describe and ui.act stay behind surfaceCaller because they read
// what the app did NOT publish -- the registry's manifest view of another
// app's actions and consent -- and that asymmetry must not be erased by
// routing the same question through a list. Anything that is neither the
// session nor a domain with a readable name (a foreign uid, or "domain:" with
// no token to identify) is refused, because such a caller cannot be placed in
// either class.
func (s *Service) listSurfaces(req proto.Envelope) (any, *invoke.Error) {
	if e := surfaceListCaller(req.From); e != nil {
		return nil, e
	}
	ds := s.Published()
	out := listReply{Surfaces: make([]json.RawMessage, 0, len(ds))}
	for _, d := range ds {
		b, err := d.Marshal()
		if err != nil {
			// Only descriptors that already passed Marshal are stored, so this
			// is unreachable; a refusal still beats a silent gap in the list.
			return nil, &invoke.Error{Code: "bad_surface",
				Msg: "a stored surface does not validate", Detail: err.Error()}
		}
		out.Surfaces = append(out.Surfaces, json.RawMessage(b))
	}
	return out, nil
}

// surfaceListCaller is surface.list@v1's caller rule; listSurfaces says why it
// is not surfaceCaller.
func surfaceListCaller(from string) *invoke.Error {
	if mayDecide(from) {
		return nil
	}
	if name, isDomain := ctlsurface.DomainOf(from); isDomain && name != "" {
		return nil
	}
	return &invoke.Error{Code: "not_permitted",
		Msg:    "surface.list@v1 is readable from the session socket or a domain",
		Detail: "caller " + from + " is neither the session user nor a named domain"}
}
