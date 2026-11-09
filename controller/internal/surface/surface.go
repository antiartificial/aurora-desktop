// Package surface fetches window descriptors from the registry over the bus.
//
// It exists so there is ONE client-side copy of "ask the controller for a
// window's surface" and one copy of the isolation rule that goes with it: a
// domain may learn its own surface and no one else's. The descriptor itself is
// contracts/surface's; nothing here redeclares its shape, because a second
// declaration would be a second thing to keep in step with the wire.
package surface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	contract "github.com/antiartificial/aurora/contracts/surface"
)

// Verbs this package speaks. Both are @v1 and stay @v1: the surface arrived
// as an additive key on describe's reply, so old readers ignore it.
const (
	DescribeVerb = "registry.describe@v1"
	AppsVerb     = "registry.apps@v1"
)

// Error codes. They are the codes the controller's bus errors use, so a caller
// switches on Code rather than parsing prose.
const (
	CodeNotPermitted = "not_permitted"
	CodeNoSurface    = "no_surface"
	CodeBadSurface   = "bad_surface"
)

// Error is a structured refusal or an unusable reply.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return e.Code + ": " + e.Message + " (" + e.Detail + ")"
	}
	return e.Code + ": " + e.Message
}

// Caller is *client.Client's Call, narrowed to the one method used here so a
// test can answer without a socket. The identity the bus stamps into From is
// the connection's, not anything this package can set.
type Caller interface {
	Call(ctx context.Context, e proto.Envelope) (proto.Envelope, error)
}

// DomainOf returns the domain name carried by a bus identity, and whether from
// is a domain at all. busd stamps "domain:<name>:<hex>" for a domain socket and
// "uid<N>:<comm>" for the session socket, so the prefix is the discriminator.
// A domain-shaped identity whose name cannot be read comes back as ("", true)
// rather than as a session: see Permit, which must not fail open on it.
func DomainOf(from string) (name string, isDomain bool) {
	rest, ok := strings.CutPrefix(from, "domain:")
	if !ok {
		return "", false
	}
	// The hex token is the last field; a domain name may not contain ':'.
	i := strings.LastIndexByte(rest, ':')
	if i <= 0 {
		return "", true
	}
	return rest[:i], true
}

// Permit returns nil when the identity from may be handed app's descriptor.
//
// from must be the bus-stamped identity (SO_PEERCRED), never a payload claim:
// that is the whole reason the rule can be enforced at all. The session user
// gets every surface, because the shell draws chrome for windows it did not
// start, and its limit is the one decision 0007 already states -- any
// unconfined process of the same uid is also "uid<N>:<comm>". A domain gets
// only its own app: another app's actions and declared grants are a statement
// about what that app can do, which no manifest entitles a neighbour to read.
//
// ownedBy answers whether domainName is the domain of appID. The controller
// supplies it from its launch table (a window's domain is minted per launch,
// so the name alone does not say whose it is) and, for verb domains, by
// recomputing invoke.DomainName.
func Permit(from, app string, ownedBy func(domainName, appID string) bool) *Error {
	name, isDomain := DomainOf(from)
	if !isDomain {
		return nil
	}
	if name == "" {
		return &Error{Code: CodeNotPermitted, Message: "caller " + from + " may not describe app " + app, Detail: "a domain-shaped identity must be domain:<name>:<token>, so the caller's app cannot be established"}
	}
	if ownedBy != nil && ownedBy(name, app) {
		return nil
	}
	return &Error{
		Code:    CodeNotPermitted,
		Message: "domain " + name + " may not describe app " + app,
		Detail:  "a domain sees only its own surface; the descriptor names another app's actions and grants",
	}
}

// Describe asks the registry for the surface of app, naming verb only because
// describe is per-verb: the reply's descriptor covers the whole window, so any
// one of the app's verbs answers for all of them.
func Describe(ctx context.Context, c Caller, app, verb string) (contract.Descriptor, error) {
	p, err := json.Marshal(map[string]string{"app": app, "verb": verb})
	if err != nil {
		return contract.Descriptor{}, err
	}
	r, err := c.Call(ctx, proto.Envelope{Kind: proto.KindRequest, To: "registry", Verb: DescribeVerb, Payload: p})
	if err != nil {
		return contract.Descriptor{}, fromWire(DescribeVerb, err)
	}
	var reply struct {
		Surface json.RawMessage `json:"surface"`
	}
	if err := json.Unmarshal(r.Payload, &reply); err != nil {
		return contract.Descriptor{}, &Error{Code: CodeNoSurface, Message: "describe reply is not decodable", Detail: err.Error()}
	}
	if len(reply.Surface) == 0 {
		// An older controller answers describe without a surface. That is not a
		// drawable window, and inventing one from the per-verb fields would be a
		// descriptor nobody validated.
		return contract.Descriptor{}, &Error{Code: CodeNoSurface, Message: "registry did not carry a surface for " + app, Detail: "the controller predates registry.describe@v1's surface key"}
	}
	d, err := contract.Unmarshal(reply.Surface)
	if err != nil {
		return contract.Descriptor{}, &Error{Code: CodeBadSurface, Message: "surface for " + app + " does not validate", Detail: err.Error()}
	}
	if d.App != app {
		// A descriptor for another app would have the shell draw the wrong chrome
		// with the right-looking grants, so a mismatch is refused, not repaired.
		return contract.Descriptor{}, &Error{Code: CodeBadSurface, Message: "asked for " + app + " but the surface says " + d.App}
	}
	return d, nil
}

// List returns the descriptor of every app the registry can describe, ordered
// by app id.
//
// The selection is "has at least one verb", not "windowed", because describe is
// per-verb: a manifest with no [[verb]] has no descriptor to fetch, and today
// every windowed manifest is one of those (the Haiku interface apps are launch
// targets whose surface the toolkit has not published yet). The bus has no verb
// that enumerates launched windows either, so this is the set of surfaces a
// session CAN open, which is what `aurora surface list` is asked for. An app
// with no verbs has no action to draw, so it is skipped rather than failing the
// whole listing.
func List(ctx context.Context, c Caller) ([]contract.Descriptor, error) {
	r, err := c.Call(ctx, proto.Envelope{Kind: proto.KindRequest, To: "registry", Verb: AppsVerb, Payload: json.RawMessage(`{}`)})
	if err != nil {
		return nil, fromWire(AppsVerb, err)
	}
	var reply struct {
		Apps []struct {
			ID    string   `json:"id"`
			Verbs []string `json:"verbs"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(r.Payload, &reply); err != nil {
		return nil, &Error{Code: "bad_reply", Message: AppsVerb + " reply is not decodable", Detail: err.Error()}
	}
	var out []contract.Descriptor
	for _, a := range reply.Apps {
		if len(a.Verbs) == 0 {
			continue
		}
		d, err := Describe(ctx, c, a.ID, a.Verbs[0])
		if err != nil {
			return nil, fmt.Errorf("surface: %s: %w", a.ID, err)
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].App < out[j].App })
	return out, nil
}

// fromWire turns a bus error into this package's structured error. client.Call
// keeps only the remote code and message, so the service's detail line is lost
// on this path; the code is the part a caller is meant to branch on.
func fromWire(verb string, err error) error {
	var re *client.RemoteError
	if errors.As(err, &re) {
		return &Error{Code: re.Code, Message: re.Message}
	}
	return fmt.Errorf("surface: %s: %w", verb, err)
}
