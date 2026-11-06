package plumb

import (
	"regexp"
	"strings"
)

// ViewerKind classifies the bus-stamped From of a request exactly as
// index/access.go's Viewer.Kind does (and a test pins the two to the same
// table): "domain", "session", or "unknown", which is treated as seeing
// nothing. The From is what busd stamped from the socket; the payload is
// never consulted.
//
// What each kind gets from the plumber:
//
//	session  the user's own session: paths are checked and sniffed on disk.
//	domain   a confined app: the filesystem is not touched at all. The type
//	         of a path comes from its name alone, so an existing file, a
//	         missing file and a file in someone else's tree all answer
//	         alike, and nothing about the disk can be learned by asking.
//	unknown  nothing: an empty list and a reason.
//
// The verbs listed are the registry's, which registry.verbs@v1 already
// shows every caller; plumbing adds no knowledge of apps.
func ViewerKind(from string) string {
	switch {
	case strings.HasPrefix(from, "domain:") && len(from) > len("domain:"):
		return "domain"
	case sessionRE.MatchString(from):
		return "session"
	}
	return "unknown"
}

var sessionRE = regexp.MustCompile(`^uid[0-9]+:.+`)

// ForViewer resolves a request for the caller identified by from.
func ForViewer(rs *RuleSet, verbs []Verb, req Request, from string) Reply {
	kind := ViewerKind(from)
	var rep Reply
	switch kind {
	case "session":
		rep = Resolve(rs, verbs, req, DiskProbe)
	case "domain":
		rep = Resolve(rs, verbs, req, nil)
	default:
		rep = Reply{
			Selection:  Resolved{Kind: req.Selection.Kind, Value: req.Selection.Value},
			Candidates: []Candidate{},
			Reason:     "your identity is not one the plumber recognises: nothing is shown",
		}
	}
	rep.Viewer = kind
	return rep
}
