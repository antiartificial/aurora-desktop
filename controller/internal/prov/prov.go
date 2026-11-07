// Package prov is provenance v0: the record and its extended attribute.
package prov

import (
	"encoding/json"
	"errors"
	"syscall"
)

// Attr is the xattr that carries the record on the output file.
const Attr = "user.aurora.provenance"

// Topic is the bus topic a record is published on.
const Topic = "provenance.created"

// TopicHeld is published, by the connection that minted a device grant's handle, once the grant is composed into
// a domain's namespace and before the payload runs: {"cap","resource","domain"}. The compositor's capability
// indicator (decision 0016) believes it only from the handle's own minter. "provenance." is a system namespace,
// so no domain connection hears it.
const TopicHeld = "provenance.grant.held"

// How a grant was authorised.
const (
	// SourceAmbient: covered by a capability the app's manifest declares
	// (consent at install time).
	SourceAmbient = "ambient"
	// SourceGesture: not covered by the manifest; justified only by the
	// caller's request for this one invocation (the powerbox model).
	SourceGesture = "gesture"
	// SourcePowerbox: not covered by the manifest, but backed by a valid
	// single-use grant ticket minted by the picker for exactly this path,
	// mode, app and verb. See controller/ticket for what that does and does
	// not prove.
	SourcePowerbox = "powerbox"
)

// How an approval was obtained (Approval.Route).
const (
	// RouteAmbient: a destructive/external verb whose grants are all ambient;
	// the manifest's declared capabilities were consented to at install time,
	// so no per-invoke approval was required.
	RouteAmbient = "ambient"
	// RouteInteractive: a decision arrived on approval.decide@v1 from the
	// session socket (the launcher's prompt, or anything else of the user's
	// that can reach the socket).
	RouteInteractive = "interactive"
	// RouteAutoApprove: controllerd --auto-approve. A test affordance that
	// defeats the point of approval.
	RouteAutoApprove = "auto-approve"
	// RouteCompositor: the compositor drew the prompt itself and read the
	// keystroke (Approval.Key). Preferred when the compositor is reachable;
	// the others are fallbacks. See compositor/README.md for what that does
	// and does not authenticate.
	RouteCompositor = "compositor"
)

// Approval records whether a destructive or external verb needed approval and
// how it got it. Only written for those effect classes. A denied or timed-out
// invoke produces no record at all (it errors).
type Approval struct {
	Required  bool   `json:"required"`
	Route     string `json:"route"`
	Decision  string `json:"decision,omitempty"` // "allow" when Required
	RequestID string `json:"request_id,omitempty"`
	By        string `json:"by,omitempty"`  // bus identity that decided; "controllerd" for auto-approve; "compositor" for route compositor
	Key       string `json:"key,omitempty"` // route compositor: the keystroke the compositor read
	At        string `json:"at"`            // RFC3339Nano, when decided (or when the invoke was checked)
	// DomainVerified, routes compositor and interactive: what the compositor itself found (compositor route) or
	// "true" when the controller read the bus's own pre-flight event (interactive route), else "unknown". On the
	// compositor route: what the compositor itself found
	// when it checked the domain identity it was shown against busd, at the
	// time it drew the prompt. "true": busd has that domain registered with a
	// cgroup it attests. "false": busd answered and does not vouch for it.
	// "unknown": busd could not be asked. "true" proves the named domain
	// exists and is attested. It does NOT prove this invoke ran in it.
	DomainVerified     string `json:"domain_verified,omitempty"`
	DomainVerifyDetail string `json:"domain_verify_detail,omitempty"`
	// DomainSource, routes compositor and interactive: "bus" when the domain the user was
	// shown came from an event busd stamped from inside that domain (the
	// requester did not choose it), "claim" when it was only the controller's
	// word. DomainShown is the identity displayed. DomainMismatch is true when
	// the controller claimed a different domain than the bus attested (the
	// compositor displayed the bus's). A same-uid process with its own domain
	// socket access, or one that joined the domain, can still publish as it.
	DomainSource   string `json:"domain_source,omitempty"`
	DomainShown    string `json:"domain_shown,omitempty"`
	DomainMismatch bool   `json:"domain_mismatch,omitempty"`
}

// Grant is one grant given to the app's domain for one invoke: a filesystem
// path (Mode "ro" or "rw") or a device (Device set, Path "device:<class>",
// Mode ModeUse; decision 0014).
type Grant struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"` // "ro" or "rw"; ModeUse for a device
	Source string `json:"source"`
	// Device is the device class ("gpu", "wayland") when this grant is a device grant.
	Device string `json:"device,omitempty"`
	// UI is the accessibility class ("read") when this grant is a ui grant (decision 0017), and UIFor is the domain
	// identity that holds it. The handle is minted for "ui:<class>:<identity>", which is what the application's tree
	// service looks for in the bus's own table.
	UI    string `json:"ui,omitempty"`
	UIFor string `json:"ui_for,omitempty"`
	// Cap is the id (cap:...) of the daemon-minted handle that named this
	// grant for this invoke; empty when none was issued (see Record.CapNote).
	Cap string `json:"cap,omitempty"`
	// Access is what revoking Cap can withdraw (decision 0019): AccessMounted
	// for everything the controller composes into a namespace. It is recorded
	// with the grant, and not left to be inferred from Path, because a reader
	// of this record is a verifier asking "is there still access", and the
	// answer for a mounted grant is not the one a revoked handle suggests.
	// Empty on records written before 0019, which say nothing either way.
	Access string `json:"access,omitempty"`
}

// What revoking a grant's handle can take back (decision 0019). The names say
// where the object lives, which is the fact the guarantee follows from.
// "leased" is deliberately absent: a lease is a handle that ends at a reply
// (busd's bus.lease@v1), and nothing the controller composes into a domain is
// one. A grant must not be describable by a word that promises it.
const (
	// AccessMounted: the object is in the domain's namespace (a bind mount or
	// device node) for the life of the domain. The handle's expiry and
	// revocation are bookkeeping; a holder's open fd survives both.
	AccessMounted = "mounted"
	// AccessUnclassified: the controller does not claim either way.
	AccessUnclassified = "unclassified"
)

// AccessKind is the label this grant is recorded with. Files and devices are
// mounted. A ui grant is a statement in the bus's own table for the tree
// service to find, not a mount, but nothing has measured whether revoking it
// is enforced, so it claims nothing.
func (g Grant) AccessKind() string {
	if g.UI != "" {
		return AccessUnclassified
	}
	return AccessMounted
}

// AccessNote says in words what an Access label means for revocation, for
// output a person reads. A label alone is easy to skim past.
func AccessNote(access string) string {
	switch access {
	case AccessMounted:
		return "mounted: revoking the handle does not withdraw access the app already holds (decision 0019)"
	case "":
		return "access kind not recorded (written before decision 0019): revocation may not have withdrawn access"
	}
	return access + ": revocation is not claimed to withdraw access already held"
}

// Record says where a file came from.
type Record struct {
	File string `json:"file"`
	App  string `json:"app"`
	// Domain is the app's domain identity (`domain exec-id`), the name the bus
	// attributes the domain's connections to. DomainAttested says busd had
	// the domain registered when the verb ran; if false the name is only
	// what the adapter reported, not something the bus could vouch for.
	Domain         string          `json:"domain,omitempty"`
	DomainAttested bool            `json:"domain_attested,omitempty"`
	Verb           string          `json:"verb"`
	Inputs         []string        `json:"inputs"`
	Args           json.RawMessage `json:"args"`
	Isolated       bool            `json:"isolated"`
	Grants         []Grant         `json:"grants"`
	Approval       *Approval       `json:"approval,omitempty"`
	// CapEpoch is the id of a handle the controller minted when it connected
	// to the daemon. If the daemon still knows it, the daemon has not
	// restarted since this record was written (see verify.go).
	CapEpoch string `json:"cap_epoch,omitempty"`
	// CapNote says why a grant has no Cap, or that presenting them failed.
	CapNote string `json:"cap_note,omitempty"`
	// Reported is what the app said, in its result's "reported" object,
	// about what determined the output (a compiler's identity and flags, for
	// instance). Every other field of the record is the controller's
	// observation; this one is the app's claim, made from inside its domain,
	// and is recorded so the claim travels with the file under the same
	// xattr. Capped at MaxReported bytes: an xattr is small and a record
	// that cannot be written is no record at all.
	Reported json.RawMessage `json:"reported,omitempty"`
	TS       string          `json:"ts"`
}

// MaxReported bounds Record.Reported.
const MaxReported = 2048

// ReportedFrom extracts the "reported" object from an app's result JSON. It
// returns nil when the result has none or it is not an object; tooBig is true
// when the object exceeds MaxReported (and nil is returned).
func ReportedFrom(result []byte) (rep json.RawMessage, tooBig bool) {
	var m struct {
		Reported json.RawMessage `json:"reported"`
	}
	if json.Unmarshal(result, &m) != nil || len(m.Reported) == 0 || m.Reported[0] != '{' {
		return nil, false
	}
	if len(m.Reported) > MaxReported {
		return nil, true
	}
	return m.Reported, false
}

// Write stores the record on path as an extended attribute.
func Write(path string, r Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return syscall.Setxattr(path, Attr, b, 0)
}

// Read returns the record stored on path.
func Read(path string) (Record, error) {
	var r Record
	sz, err := syscall.Getxattr(path, Attr, nil)
	if err != nil {
		if errors.Is(err, syscall.ENODATA) {
			return r, errors.New("no provenance record on " + path)
		}
		return r, err
	}
	buf := make([]byte, sz)
	n, err := syscall.Getxattr(path, Attr, buf)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(buf[:n], &r)
}
