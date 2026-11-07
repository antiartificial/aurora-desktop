package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/proto"
)

// resourceRE is the charset a handle's resource name may use. The daemon does
// not parse the name -- contracts/object owns the grammar -- but it does bound
// it, so a name is always loggable, greppable and of fixed maximum width.
//
// '@' and '#' are here because the object namespace needs them: a verb is
// spelled "service:files/open@v1", and a name too long or too strange to spell
// literally is carried as "file:#<sha256>". Without them those objects could
// not be minted at all.
var resourceRE = regexp.MustCompile(`^[A-Za-z0-9_.:/@#-]{1,128}$`)

// handle is one row of the daemon's authoritative capability table. The
// token is the unguessable value; holder is the domain identity that may
// present it right now. The table is the only source of truth: a token a
// client holds in its memory means nothing unless this table agrees.
type handle struct {
	token    string
	id       string // buslog.CapID(token): what the log and inspect show
	resource string
	minter   string
	holder   string
	expires  time.Time // zero: never
	revoked  bool
}

// codedErr is an error with a typed code for the client.
type codedErr struct{ code, msg string }

func (e *codedErr) Error() string { return e.msg }

func cerr(code, format string, a ...any) error {
	return &codedErr{code, fmt.Sprintf(format, a...)}
}

func newToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // no entropy: nothing safe to do
	}
	return "cap_" + hex.EncodeToString(b[:])
}

// check says whether domain may present token now. Order matters: a domain
// that does not hold a handle learns nothing about its state beyond "not held".
func (s *Server) check(domain, token string) (*handle, error) {
	h := s.caps[token]
	switch {
	case h == nil:
		return nil, cerr("cap_unknown", "handle is not in the daemon's table (forged or never minted)")
	case h.holder != domain:
		return nil, cerr("cap_not_held", "handle %s is not held by %s", h.id, domain)
	case h.revoked:
		return nil, cerr("cap_revoked", "handle %s was revoked", h.id)
	case !h.expires.IsZero() && !time.Now().Before(h.expires):
		return nil, cerr("cap_expired", "handle %s expired", h.id)
	}
	return h, nil
}

// checkCaps validates every handle a message presents. Callers hold s.mu.
func (s *Server) checkCaps(c *conn, e proto.Envelope) error {
	for _, t := range e.Caps {
		if _, err := s.check(c.domain, t); err != nil {
			return err
		}
	}
	return nil
}

// Topics the daemon publishes (and logs) for the capability lifecycle. The
// payload names handles by their stable id, never the token. "by" is the
// domain whose connection made the request, from the connection's identity;
// the envelope's own From is "bus", so the log shows the daemon said it.
const (
	TopicCapMinted      = "bus.cap.minted"
	TopicCapTransferred = "bus.cap.transferred"
	TopicCapRevoked     = "bus.cap.revoked"
)

// emitCap records and delivers one lifecycle event. Callers hold s.mu.
func (s *Server) emitCap(topic string, by string, h *handle, extra map[string]any) {
	m := map[string]any{"id": h.id, "resource": h.resource, "by": by, "minter": h.minter}
	for k, v := range extra {
		m[k] = v
	}
	payload, _ := json.Marshal(m)
	s.publish(proto.Envelope{
		ID: s.newID(), TS: s.clock.Now().String(), From: proto.BusTarget, Kind: proto.KindEvent,
		Topic: topic, QoS: proto.QoSInteractive, Payload: payload,
	})
}

func (s *Server) ctlMint(c *conn, raw json.RawMessage) (any, error) {
	var p struct {
		Resource string `json:"resource"`
		TTLMS    int64  `json:"ttl_ms"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || !resourceRE.MatchString(p.Resource) || p.TTLMS < 0 {
		return nil, errors.New(`mint needs {"resource":"name","ttl_ms":optional>=0}`)
	}
	h := &handle{token: newToken(), resource: p.Resource, minter: c.domain, holder: c.domain}
	h.id = buslog.CapID(h.token)
	if p.TTLMS > 0 {
		h.expires = time.Now().Add(time.Duration(p.TTLMS) * time.Millisecond)
	}
	s.caps[h.token] = h
	ex := map[string]any{"holder": h.holder}
	if !h.expires.IsZero() {
		ex["expires"] = h.expires
	}
	s.emitCap(TopicCapMinted, c.domain, h, ex)
	out := map[string]any{"handle": h.token, "id": h.id, "resource": h.resource, "holder": h.holder}
	if !h.expires.IsZero() {
		out["expires"] = h.expires
	}
	return out, nil
}

// ctlTransfer moves a handle the caller holds to another connected domain.
// The daemon pushes the token to the new holder itself, so the old holder
// need not (and after the move cannot) present it.
func (s *Server) ctlTransfer(c *conn, raw json.RawMessage) (any, error) {
	var p struct{ Handle, To string }
	if err := json.Unmarshal(raw, &p); err != nil || p.Handle == "" || p.To == "" {
		return nil, errors.New(`transfer needs {"handle":"...","to":"<domain identity>"}`)
	}
	h, err := s.check(c.domain, p.Handle)
	if err != nil {
		return nil, err
	}
	var targets []*conn
	for _, t := range s.conns {
		if t.domain == p.To {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return nil, cerr("no_such_domain", "no connected client has identity %q to receive the handle", p.To)
	}
	from := h.holder
	h.holder = p.To
	s.emitCap(TopicCapTransferred, c.domain, h, map[string]any{"from": from, "to": p.To})
	note, _ := json.Marshal(map[string]string{"id": h.id, "resource": h.resource, "from": c.domain})
	for _, t := range targets {
		s.enqueue(t, proto.Envelope{
			ID: s.newID(), TS: s.clock.Now().String(), From: "bus", To: t.domain, Kind: proto.KindEvent,
			Topic: "bus.cap.granted", QoS: proto.QoSInteractive, Payload: note, Caps: []string{h.token},
		})
	}
	return map[string]string{"transferred": h.id, "to": p.To}, nil
}

// ctlRevoke lets the minter kill a handle wherever it is now. It takes the
// handle value or its id (the minter may no longer hold the value).
func (s *Server) ctlRevoke(c *conn, raw json.RawMessage) (any, error) {
	var p struct{ Handle, ID string }
	if err := json.Unmarshal(raw, &p); err != nil || (p.Handle == "" && p.ID == "") {
		return nil, errors.New(`revoke needs {"handle":"..."} or {"id":"cap:..."}`)
	}
	var h *handle
	for _, x := range s.caps {
		if (p.Handle != "" && x.token == p.Handle) || (p.Handle == "" && x.id == p.ID) {
			h = x
		}
	}
	if h == nil {
		return nil, cerr("cap_unknown", "no such handle")
	}
	if h.minter != c.domain {
		return nil, cerr("cap_not_minter", "only the minting domain may revoke %s", h.id)
	}
	if !h.revoked { // revoking twice changes nothing and says nothing
		h.revoked = true
		s.emitCap(TopicCapRevoked, c.domain, h, map[string]any{"holder": h.holder})
	}
	return map[string]string{"revoked": h.id}, nil
}

// InspectHandle is an outstanding handle in bus.inspect@v1. Never the token.
type InspectHandle struct {
	ID       string     `json:"id"`
	Resource string     `json:"resource"`
	Minter   string     `json:"minter"`
	Holder   string     `json:"holder"`
	Expires  *time.Time `json:"expires,omitempty"`
}

func (s *Server) inspectHandles() []InspectHandle {
	out := []InspectHandle{}
	now := time.Now()
	for _, h := range s.caps {
		if h.revoked || (!h.expires.IsZero() && !now.Before(h.expires)) {
			continue
		}
		ih := InspectHandle{ID: h.id, Resource: h.resource, Minter: h.minter, Holder: h.holder}
		if !h.expires.IsZero() {
			x := h.expires
			ih.Expires = &x
		}
		out = append(out, ih)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
