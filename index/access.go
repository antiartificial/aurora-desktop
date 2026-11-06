package index

// Who may see what.
//
// THE RULE: a caller that is a domain sees only the entries whose recorded
// domain is exactly that domain's bus-stamped identity; the user's own session
// sees everything; anything else sees nothing.
//
// The caller is the From busd stamped on the request envelope. For a
// connection that arrived on a per-domain socket, From is "domain:<name>:<id>"
// (a fact about which socket, and, with attestation on, which cgroup,
// namespaces and uid, see bus/server/domains.go); for the session socket it is
// "uid<N>:<process name>". The request payload is never consulted: there is no
// field a caller can set to ask for someone else's results.
//
// Why not also "files under a path the domain holds a grant for": the
// controller revokes every grant when an invoke ends (execute.go), so between
// invokes no domain holds one and the index cannot know whether one is held
// now. A rule that read grants out of old records would be handing out access
// the domain no longer has. The cost of the simple rule: a domain does not see
// a file another domain made even if it was once granted that file as an input.
//
// What the rule trusts: the Domain text of the controller's provenance record
// (the sender is checked to be the controller, the text is not verified), and
// that the same-uid session is the user. A process that reaches the session
// socket is the session; confining it is the domain sandbox's job.

import (
	"regexp"
	"strings"
)

// Viewer is the bus-stamped From of a request.
type Viewer string

var sessionRE = regexp.MustCompile(`^uid[0-9]+:.+`)

// Kind is "domain", "session", or "unknown" (sees nothing: fail closed).
func (v Viewer) Kind() string {
	switch {
	case strings.HasPrefix(string(v), "domain:") && len(v) > len("domain:"):
		return "domain"
	case sessionRE.MatchString(string(v)):
		return "session"
	}
	return "unknown"
}

// Sees reports whether v may be told about e.
func (v Viewer) Sees(e *Entry) bool {
	switch v.Kind() {
	case "session":
		return true
	case "domain":
		return e.Domain == string(v)
	}
	return false
}

// Rule is what to tell the caller about the view it got.
func (v Viewer) Rule() string {
	switch v.Kind() {
	case "session":
		return "you are the user's own session: every indexed file is shown."
	case "domain":
		return "you are a domain: only files whose recorded producing domain is exactly your own identity are shown; other domains' files are neither listed nor counted, and look the same as files that were never indexed."
	}
	return "your identity is not one this index recognises: nothing is shown."
}

// ViewerInfo is the "viewer" object in every reply.
type ViewerInfo struct {
	Identity string `json:"identity"` // the request's bus-stamped From
	Kind     string `json:"kind"`
	Rule     string `json:"rule"`
}

func (v Viewer) Info() ViewerInfo { return ViewerInfo{string(v), v.Kind(), v.Rule()} }
