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
	"github.com/antiartificial/aurora/contracts/object"
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
	// rights is what this handle permits over the object, kept apart from
	// resource (decision 0018) so one object can be held at several strengths.
	// The daemon records and reports it; no use site refuses on it yet.
	rights object.Rights
	// parent is the id of the handle this was derived from, "" for a minted
	// root. The id and not a pointer so the tree survives only as table rows:
	// revocation walks the table, and there is no second structure to keep in
	// step with it.
	parent string
	// access is what revoking this handle can actually withdraw (decision
	// 0019). It is the daemon's own label, decided at mint from the object's
	// class and inherited by every derivation, so no holder can raise it.
	access Access
	// lease marks a handle minted by bus.lease@v1. leaseReq is the daemon id of
	// the one request it was presented on ("" until then); ended is set when
	// that request's reply passes, and is terminal.
	lease    bool
	leaseReq string
	ended    bool
}

// Access says what ending a handle does to the access its holder already has.
// It is the tier of decision 0019, named for the mechanism and not for the
// outcome: "mounted" and "mediated" say where the object lives, which is the
// fact the guarantee follows from. "leased" was the record's wording for the
// enforceable side and is not used, because a mediated handle that is not
// ended at reply is not a lease, and a label that promised one would be wrong
// for every long-lived handle over a service. Whether a handle IS a lease is a
// separate fact (handle.lease), which is only ever true of a mediated one.
type Access string

const (
	// AccessMounted: the holder has the object in its own namespace (a bind
	// mount, a device node) and keeps it past revocation: an open fd cannot be
	// taken back, and it reads bytes written after the revoke. Revoking
	// removes the daemon's row and nothing else.
	AccessMounted Access = "mounted"
	// AccessMediated: the holder has a connection, not the object, and
	// something it does not control checks the daemon's table per request.
	// Revocation is enforced.
	AccessMediated Access = "mediated"
	// AccessUnclassified: the daemon cannot say. Reported as such rather than
	// guessed, because a guess in the strong direction is the failure 0019
	// exists to prevent, and a guess in the weak direction would mislabel
	// things nobody has examined.
	AccessUnclassified Access = "unclassified"
)

// classifyAccess decides a handle's Access from its resource and what the
// minter claimed. The class decides: a minter may weaken the label (say
// "mounted" of anything) but cannot strengthen it, so the only way to hold a
// "mediated" handle is to name a service or session object. The daemon does
// not otherwise parse resource names; this reads only the class prefix, and a
// legacy spelling that does not parse is unclassified, not mounted-by-default,
// since "ui:" and "host:" are neither.
func classifyAccess(resource, claimed string) (Access, error) {
	byClass := AccessUnclassified
	if ref, err := object.Parse(resource); err == nil {
		switch ref.Class {
		case object.File, object.Device:
			byClass = AccessMounted
		case object.Service, object.Session:
			byClass = AccessMediated
		}
	}
	switch Access(claimed) {
	case "":
		return byClass, nil
	case AccessMounted:
		return AccessMounted, nil
	case AccessMediated:
		if byClass != AccessMediated {
			return "", cerr("access_not_mediated", "%q is not a service or session object: access to it is %s, and a minter cannot declare it enforceable", resource, byClass)
		}
		return AccessMediated, nil
	}
	return "", cerr("bad_access", `access must be "mounted" or "mediated"`)
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
	case h.ended:
		return nil, cerr("cap_ended", "lease %s ended with its request's reply", h.id)
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
	TopicCapEnded       = "bus.cap.ended"
)

// emitCap records and delivers one lifecycle event. Callers hold s.mu.
func (s *Server) emitCap(topic string, by string, h *handle, extra map[string]any) {
	m := map[string]any{"id": h.id, "resource": h.resource, "by": by, "minter": h.minter, "rights": h.rights.String(), "access": string(h.access)}
	if h.lease {
		m["lease"] = true
	}
	if h.parent != "" {
		m["parent"] = h.parent
	}
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
		Rights   string `json:"rights"`
		Access   string `json:"access"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || !resourceRE.MatchString(p.Resource) || p.TTLMS < 0 {
		return nil, errors.New(`mint needs {"resource":"name","ttl_ms":optional>=0,"rights":"read,write"}`)
	}
	// A mint that names no rights holds NONE, not all. Fail-closed: a caller
	// that forgot the field (every minter written before 0018) gets a handle
	// that permits nothing and cannot derive, instead of silently getting
	// authority nobody chose. All was rejected because the omission is
	// indistinguishable from an oversight, and an oversight must not widen.
	// Nothing enforces rights at use sites yet, so today this costs nothing;
	// the day something does, minters must say what they mean.
	rights, err := object.ParseRights(p.Rights)
	if err != nil {
		return nil, cerr("bad_rights", "%v", err)
	}
	access, err := classifyAccess(p.Resource, p.Access)
	if err != nil {
		return nil, err
	}
	h := &handle{token: newToken(), resource: p.Resource, minter: c.domain, holder: c.domain, rights: rights, access: access}
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
	out := map[string]any{"handle": h.token, "id": h.id, "resource": h.resource, "holder": h.holder, "rights": h.rights.String(), "access": string(h.access)}
	if !h.expires.IsZero() {
		out["expires"] = h.expires
	}
	return out, nil
}

// ctlDerive mints a handle over the same object as one the caller holds, with
// rights no stronger than the parent's.
//
// Everything about the child comes from the parent row: the resource is copied
// verbatim and the request has no field to name one, so a caller cannot widen
// a derivation into a different object. The expiry is inherited too; a child
// that outlived its parent's TTL would be a way to extend a lease by routing
// it through yourself. The holder is the deriving domain, and it becomes the
// child's minter, so it can revoke what it derived without being able to
// touch the parent.
func (s *Server) ctlDerive(c *conn, raw json.RawMessage) (any, error) {
	var p struct{ Handle, Rights string }
	if err := json.Unmarshal(raw, &p); err != nil || p.Handle == "" || p.Rights == "" {
		return nil, errors.New(`derive needs {"handle":"...","rights":"read"}`)
	}
	// check is first so a domain that does not hold the parent learns nothing
	// about its rights.
	parent, err := s.check(c.domain, p.Handle)
	if err != nil {
		return nil, err
	}
	if parent.lease {
		return nil, cerr("cap_is_lease", "lease %s cannot be derived from: a child would outlive the request it was lent for", parent.id)
	}
	want, err := object.ParseRights(p.Rights)
	if err != nil {
		return nil, cerr("bad_rights", "%v", err)
	}
	// Reduce enforces both rules but reports them as one error; the code the
	// client sees should say which rule it hit.
	if !parent.rights.Has(object.Grant) {
		return nil, cerr("cap_no_grant", "handle %s does not carry the grant right", parent.id)
	}
	got, err := parent.rights.Reduce(want)
	if err != nil {
		return nil, cerr("rights_exceed_parent", "%v", err)
	}
	h := &handle{
		token: newToken(), resource: parent.resource, minter: c.domain, holder: c.domain,
		expires: parent.expires, rights: got, parent: parent.id, access: parent.access,
	}
	h.id = buslog.CapID(h.token)
	s.caps[h.token] = h
	ex := map[string]any{"holder": h.holder}
	if !h.expires.IsZero() {
		ex["expires"] = h.expires
	}
	s.emitCap(TopicCapMinted, c.domain, h, ex)
	out := map[string]any{"handle": h.token, "id": h.id, "resource": h.resource, "holder": h.holder, "rights": h.rights.String(), "access": string(h.access), "parent": h.parent}
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
	if h.lease {
		return nil, cerr("cap_is_lease", "lease %s is lent to its holder for one request and cannot be transferred", h.id)
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
	// Collect before mutating so the root's event can name everything that
	// dies with it. Each descendant also gets its own event: the replay and
	// lifecycle checks fold revocations per handle id, and a single event
	// listing ids would leave them believing the children were never revoked.
	// The sweep runs even if h was already revoked, so a child that somehow
	// survived is still caught; a handle already dead emits nothing.
	var dead []*handle
	for _, d := range s.descendants(h) {
		if !d.revoked {
			dead = append(dead, d)
		}
	}
	ids := make([]string, len(dead))
	for i, d := range dead {
		ids[i] = d.id
	}
	if !h.revoked { // revoking twice changes nothing and says nothing
		h.revoked = true
		ex := map[string]any{"holder": h.holder}
		if len(ids) > 0 {
			ex["descendants"] = ids
		}
		s.emitCap(TopicCapRevoked, c.domain, h, ex)
	}
	for _, d := range dead {
		d.revoked = true
		s.emitCap(TopicCapRevoked, c.domain, d, map[string]any{"holder": d.holder, "cause": "descendant", "root": h.id})
	}
	out := map[string]any{"revoked": h.id}
	if len(ids) > 0 {
		out["descendants"] = ids
	}
	return out, nil
}

// descendants returns every handle derived from h, transitively, ordered by id
// so events are deterministic. A walk over the table per level is quadratic,
// which is fine for a table of live grants and avoids a child index that
// would have to be kept consistent with s.caps.
func (s *Server) descendants(h *handle) []*handle {
	var out []*handle
	seen := map[string]bool{h.id: true}
	frontier := map[string]bool{h.id: true}
	for len(frontier) > 0 {
		next := map[string]bool{}
		for _, x := range s.caps {
			if frontier[x.parent] && !seen[x.id] {
				seen[x.id] = true
				out = append(out, x)
				next[x.id] = true
			}
		}
		frontier = next
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// InspectHandle is an outstanding handle in bus.inspect@v1. Never the token.
type InspectHandle struct {
	ID       string `json:"id"`
	Resource string `json:"resource"`
	Minter   string `json:"minter"`
	Holder   string `json:"holder"`
	Rights   string `json:"rights"`
	// Access is what revoking the handle can withdraw: "mounted" (nothing: the
	// holder keeps the object), "mediated" (everything) or "unclassified".
	// Lease is true only for a handle that ends at its request's reply.
	Access  string     `json:"access"`
	Lease   bool       `json:"lease,omitempty"`
	Parent  string     `json:"parent,omitempty"`
	Expires *time.Time `json:"expires,omitempty"`
}

func (s *Server) inspectHandles() []InspectHandle {
	out := []InspectHandle{}
	now := time.Now()
	for _, h := range s.caps {
		if h.revoked || h.ended || (!h.expires.IsZero() && !now.Before(h.expires)) {
			continue
		}
		ih := InspectHandle{ID: h.id, Resource: h.resource, Minter: h.minter, Holder: h.holder, Rights: h.rights.String(), Access: string(h.access), Lease: h.lease, Parent: h.parent}
		if !h.expires.IsZero() {
			x := h.expires
			ih.Expires = &x
		}
		out = append(out, ih)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Bounds on a lease's own lifetime. A lease normally ends at the reply, but a
// lease that is never presented, or whose request is never answered by a
// service that stays connected, would otherwise live to its parent's expiry,
// which may be never. The bound is what makes "short-lived" true of every
// lease and not only of well-behaved ones.
const (
	defaultLeaseTTL = 30 * time.Second
	maxLeaseTTL     = 5 * time.Minute
)

// ctlLease derives a handle for one request, beside ctlDerive and under the
// same rules, because a lease is a derived handle with a short life and a
// second mechanism would be a second place for those rules to drift.
//
// It is refused, not minted with a warning label, over anything that is not
// mediated. Hubris's lease is a promise that the callee cannot keep the thing;
// for a bind-mounted file that promise is false (0019, finding 4), and a
// handle that is called a lease and then says "not enforceable" in a field
// somebody has to read is the exact artifact that record exists to prevent:
// the word gets copied and the field does not. Refusing makes the sentence "a
// lease is enforceable" true of every lease that exists, with no field to
// consult. The caller who wants a plain short-lived handle over a file has
// bus.derive@v1, which is labelled mounted and never claims to end anything.
func (s *Server) ctlLease(c *conn, raw json.RawMessage) (any, error) {
	var p struct {
		Handle, Rights string
		TTLMS          int64 `json:"ttl_ms"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Handle == "" || p.Rights == "" || p.TTLMS < 0 {
		return nil, errors.New(`lease needs {"handle":"...","rights":"read","ttl_ms":optional>=0}`)
	}
	parent, err := s.check(c.domain, p.Handle)
	if err != nil {
		return nil, err
	}
	if parent.lease {
		return nil, cerr("cap_is_lease", "lease %s cannot be leased from", parent.id)
	}
	if parent.access != AccessMediated {
		return nil, cerr("lease_not_enforceable", "handle %s is %s: ending a lease over it would not take back what the holder already has (decision 0019), so none is issued", parent.id, parent.access)
	}
	want, err := object.ParseRights(p.Rights)
	if err != nil {
		return nil, cerr("bad_rights", "%v", err)
	}
	// A lease cannot be derived from, so grant on it would be a right nobody
	// can exercise; refusing says so instead of recording a dead right.
	if want.Has(object.Grant) {
		return nil, cerr("lease_no_grant", "a lease cannot carry the grant right: it cannot be derived from")
	}
	if !parent.rights.Has(object.Grant) {
		return nil, cerr("cap_no_grant", "handle %s does not carry the grant right", parent.id)
	}
	got, err := parent.rights.Reduce(want)
	if err != nil {
		return nil, cerr("rights_exceed_parent", "%v", err)
	}
	ttl := defaultLeaseTTL
	if p.TTLMS > 0 {
		ttl = min(time.Duration(p.TTLMS)*time.Millisecond, maxLeaseTTL)
	}
	exp := time.Now().Add(ttl)
	if !parent.expires.IsZero() && parent.expires.Before(exp) {
		exp = parent.expires
	}
	h := &handle{
		token: newToken(), resource: parent.resource, minter: c.domain, holder: c.domain,
		expires: exp, rights: got, parent: parent.id, access: parent.access, lease: true,
	}
	h.id = buslog.CapID(h.token)
	s.caps[h.token] = h
	s.emitCap(TopicCapMinted, c.domain, h, map[string]any{"holder": h.holder, "expires": h.expires})
	return map[string]any{
		"handle": h.token, "id": h.id, "resource": h.resource, "holder": h.holder, "rights": h.rights.String(),
		"access": string(h.access), "lease": true, "parent": h.parent, "expires": h.expires,
	}, nil
}

// checkLeases applies what makes a lease a lease to the handles a message
// presents. It runs beside checkCaps, which already refuses a revoked,
// expired or ended one. A lease is for one request to a service: presented on
// an event or a control verb it would not be bounded by any reply, and
// presented on a second request it would be a retained handle.
func (s *Server) checkLeases(e proto.Envelope) error {
	for _, t := range e.Caps {
		h := s.caps[t]
		if h == nil || !h.lease {
			continue
		}
		if e.Kind != proto.KindRequest || e.To == proto.BusTarget {
			return cerr("lease_request_only", "lease %s may be presented only on a request to a service", h.id)
		}
		if h.leaseReq != "" {
			return cerr("cap_lease_bound", "lease %s is already lent to a request", h.id)
		}
	}
	return nil
}

// bindLeases ties each lease on a delivered request to it. Callers hold s.mu.
func (s *Server) bindLeases(e proto.Envelope) {
	for _, t := range e.Caps {
		if h := s.caps[t]; h != nil && h.lease {
			h.leaseReq = e.ID
		}
	}
}

// endLeases ends every lease bound to request id and drops its rights to none.
// The rights are zeroed as well as the state set so the row, and the event,
// say what Hubris says: at reply the lease holds nothing. Callers hold s.mu.
func (s *Server) endLeases(reqID, cause string) {
	var done []*handle
	for _, h := range s.caps {
		if h.lease && h.leaseReq == reqID && !h.ended {
			done = append(done, h)
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].id < done[j].id })
	for _, h := range done {
		h.ended, h.rights = true, 0
		s.emitCap(TopicCapEnded, h.holder, h, map[string]any{"holder": h.holder, "cause": cause, "request": reqID})
	}
}
