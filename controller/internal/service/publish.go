package service

import (
	"encoding/json"
	"sort"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/surface"
	"github.com/antiartificial/aurora/controller/internal/invoke"
)

// publish serves surface.publish@v1 {"surface": {...}}.
//
// # WHY THE BUS CARRIES THIS AND THE MANIFEST CANNOT
//
// registry.describe@v1 builds a descriptor from a manifest, so an app without
// one -- Atlas is the case that made this necessary -- has no surface for the
// compositor to relay at all. Publishing lets such an app announce its own
// descriptor rather than have the controller invent one from a manifest it
// does not have.
//
// # THE GATE
//
// A descriptor is a claim about what an app can do and which grants it needs,
// so writing one for another app would forge that claim in every compositor's
// listing. The rule is therefore the one registry.describe@v1 and ui.act@v1
// already apply -- surfaceCaller -- and it is that function, not a copy: a
// domain may publish only the surface of the app whose domain name it holds
// (invoke.DomainName, the same mapping the launch path uses), and the session,
// which is the person, may publish for any app it names. Reusing the function
// is what keeps publish from becoming the looser door.
//
// # WHAT IS KEPT
//
// surface.Unmarshal validates in the same step as parsing, so an invalid
// descriptor is refused with the validator's reason instead of being stored
// for the shell to choke on at draw time. The canonical bytes (Marshal) are
// re-read into the stored value, so the in-memory set and what list puts on
// the wire are the same sorted shape rather than two descriptions that drift.
//
// The app id is the key: republishing replaces. A window that redraws its
// chrome is describing the same surface again, not adding a second one, and
// two descriptors for one app would leave the compositor choosing between
// claims the app never meant to make together.
func (s *Service) publish(req proto.Envelope) (any, *invoke.Error) {
	var q struct {
		Surface json.RawMessage `json:"surface"`
	}
	if e := decode(req, &q); e != nil {
		return nil, e
	}
	if len(q.Surface) == 0 {
		return nil, &invoke.Error{Code: "bad_surface",
			Msg:    "surface.publish@v1 needs {\"surface\": {...}}",
			Detail: "the descriptor travels under \"surface\"; nothing was published"}
	}
	d, err := surface.Unmarshal(q.Surface)
	if err != nil {
		return nil, &invoke.Error{Code: "bad_surface",
			Msg:    "the published surface does not validate",
			Detail: err.Error()}
	}
	if e := s.surfaceCaller(req.From, d.App); e != nil {
		return nil, e
	}
	// Unmarshal already validated, so Marshal cannot fail here; it is still
	// checked rather than ignored, because a silent store of unvalidated bytes
	// would be exactly the failure this path exists to prevent.
	b, err := d.Marshal()
	if err != nil {
		return nil, &invoke.Error{Code: "bad_surface",
			Msg:    "the published surface does not validate",
			Detail: err.Error()}
	}
	canon, err := surface.Unmarshal(b)
	if err != nil {
		return nil, &invoke.Error{Code: "bad_surface",
			Msg:    "the published surface does not survive canonicalisation",
			Detail: err.Error()}
	}
	s.setPublished(canon)
	return map[string]any{"ok": true}, nil
}

// setPublished records d under its app id, replacing any earlier descriptor.
func (s *Service) setPublished(d surface.Descriptor) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.published == nil {
		s.published = make(map[string]surface.Descriptor)
	}
	s.published[d.App] = cloneDescriptor(d)
}

// Published returns every surface published over the bus, ordered by app id
// and already canonical (actions and grants sorted).
//
// It is the set surface.list@v1 answers with, exported so an in-process reader
// sees exactly what the wire would carry instead of rebuilding the rule.
// Manifest-derived descriptors are deliberately absent: registry.describe@v1
// covers those under its own gate, and mixing them in would let a manifest
// claim the authority of a person's grant merely by sharing an array.
func (s *Service) Published() []surface.Descriptor {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	out := make([]surface.Descriptor, 0, len(s.published))
	for _, d := range s.published {
		out = append(out, cloneDescriptor(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].App < out[j].App })
	return out
}

// cloneDescriptor copies the slices a Descriptor owns, so a caller holding the
// returned value cannot mutate what another request is about to read.
func cloneDescriptor(d surface.Descriptor) surface.Descriptor {
	cp := d
	cp.Actions = make([]surface.Action, len(d.Actions))
	for i, a := range d.Actions {
		a.Grants = append([]string(nil), a.Grants...)
		cp.Actions[i] = a
	}
	cp.Grants = append([]string(nil), d.Grants...)
	return cp
}
