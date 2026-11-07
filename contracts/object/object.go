// Package object is Aurora's object namespace: one grammar for every thing a
// capability can name, and the rights a handle carries over it.
//
// # Why one namespace
//
// Before this package, a handle named its object with a free string that busd
// checked against a charset regex and nothing ever parsed
// (bus/server/caps.go). Five families grew up by convention -- file:,
// filehash:, device:, net:, controller: -- and each subsystem spelled its own.
// A typo minted a handle for an object that did not exist, and the mint
// succeeded, because there was nothing to be wrong against.
//
// # Why rights are not in the name
//
// The old naming put the mode inside the object: prov.Resource gave
// "file:/x:ro" and "file:/x:rw". Those are two unrelated strings, so the
// system could not say that one is a weaker form of the other, could not
// derive the weak one from the strong one, and could not answer "revoke
// everything anyone holds on /x". Identity and authority are split here: a
// Ref says WHICH object, a Rights says WHAT may be done to it.
//
// Rights are deliberately not the verb effect classes (read | write |
// destructive | external, decision 0009). An effect describes what a verb
// intends to do and drives approval; rights describe what a handle permits and
// drive enforcement. A verb with effect "read" invoked with a handle lacking
// Read is a bug in the caller; a verb with effect "destructive" holding only
// Read is an attempt the daemon must refuse. Keeping them apart is what lets
// the second check mean anything (decision 0013's two-axes argument).
package object

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Class is the kind of object a Ref names. The set is closed: a new class is a
// decision, recorded, not a string a caller invents. Everything a capability
// can name in Aurora is one of these.
type Class string

const (
	// File is a path in the session's filesystem. Name is the absolute path.
	File Class = "file"
	// Device is a device class as decision 0014 defines it ("gpu", "wayland").
	// Name is the class, never a device node path: the node a class resolves
	// to is the adapter's business and changes between hosts.
	Device Class = "device"
	// Host is a network peer as decision 0015 enforces it. Name is the
	// hostname, port optional ("example.com", "example.com:443"). Named Host
	// and not Net because what Customs decides on is a host, not a subnet.
	Host Class = "host"
	// Service is a name on the bus (a kit, a daemon). Name is the service
	// name; "svc/verb" names one verb of it.
	Service Class = "service"
	// Window is a surface the compositor owns. Name is its stable id.
	Window Class = "window"
	// Session names the session's own singletons: the clipboard, the seat, the
	// capability epoch. Name is which one.
	Session Class = "session"
)

var classes = map[Class]bool{File: true, Device: true, Host: true, Service: true, Window: true, Session: true}

// Ref is the identity of one object, and nothing about authority over it.
//
// Two handles with different rights over the same file have equal Refs. That
// is the point: it makes "every handle on /x" a question with an answer.
type Ref struct {
	Class Class
	Name  string
	// Hashed is set when Name could not be spelled in the wire charset and the
	// wire form carries its digest instead. The Ref still compares equal to
	// another Ref parsed from the same wire form, so a verifier recomputing a
	// name from a provenance record reaches the same object. It does NOT
	// compare equal to the unhashed Ref for the same path -- a verifier that
	// needs that must hash its own candidate and compare the wire forms.
	Hashed bool
}

// nameOK is the charset a name may use literally on the wire. It excludes ':'
// so that a wire form always splits into exactly two parts, which is what the
// old scheme got wrong: "file:/x:rw" was ambiguous about where the name ended.
const nameOK = `abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.@/-`

// maxName keeps a wire form inside busd's 128-byte resource limit with room
// for the longest class prefix and the "#" marker.
const maxName = 100

func spellable(s string) bool {
	if s == "" || len(s) > maxName {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool { return !strings.ContainsRune(nameOK, r) }) < 0
}

// Wire is the string a handle is minted for: "class:name", or
// "class:#<digest>" when the name cannot be spelled literally.
//
// The form is stable: the same Ref always produces the same string, so a
// provenance record's handle id can be matched against a recomputed name years
// later. Callers must not build this string themselves -- that is how five
// namespaces happened.
func (r Ref) Wire() string {
	if r.Hashed {
		// Name IS the digest already (this Ref came from Parse). Hashing it
		// again would make the form fail to round-trip, so that a verifier
		// recomputing a name from a provenance record would miss the handle.
		return string(r.Class) + ":#" + r.Name
	}
	if !spellable(r.Name) {
		return string(r.Class) + ":#" + fmt.Sprintf("%x", sha256.Sum256([]byte(r.Name)))
	}
	return string(r.Class) + ":" + r.Name
}

func (r Ref) String() string { return r.Wire() }

// Parse reads a wire form back. It rejects an unknown class and a malformed
// name rather than passing them through: a handle for an object nobody can
// name is the failure this package exists to prevent.
func Parse(s string) (Ref, error) {
	c, n, ok := strings.Cut(s, ":")
	if !ok {
		return Ref{}, fmt.Errorf("object %q: want class:name", s)
	}
	cl := Class(c)
	if !classes[cl] {
		return Ref{}, fmt.Errorf("object %q: unknown class %q", s, c)
	}
	if rest, hashed := strings.CutPrefix(n, "#"); hashed {
		if len(rest) != 64 || !spellable(rest) {
			return Ref{}, fmt.Errorf("object %q: malformed digest", s)
		}
		return Ref{Class: cl, Name: rest, Hashed: true}, nil
	}
	if !spellable(n) {
		return Ref{}, fmt.Errorf("object %q: name is not spellable and was not hashed", s)
	}
	// Parse applies exactly the rules the constructors apply. When it did not,
	// "session:printer" and "device:/dev/dri/renderD128" parsed cleanly into
	// objects no constructor can build -- a second way in, with weaker rules,
	// which is how the first five namespaces drifted apart.
	r, err := build(cl, n)
	if err != nil {
		return Ref{}, fmt.Errorf("object %q: %w", s, err)
	}
	return r, nil
}

// build is the single per-class validator. Parse and every constructor go
// through it, so there is one definition of a well-formed name per class.
func build(cl Class, n string) (Ref, error) {
	if n == "" {
		return Ref{}, errors.New("empty name")
	}
	// Only a file may carry a name the wire charset cannot spell: a path is
	// whatever the filesystem holds, and Wire hashes it. Every other class
	// names something the system itself chose -- a device class, a hostname, a
	// bus service, a window id -- so an unspellable one is a caller's mistake
	// and hashing it would hide that behind an object nobody can read.
	if cl != File && !spellable(n) {
		return Ref{}, fmt.Errorf("%q cannot be spelled as a %s name", n, cl)
	}
	switch cl {
	case File:
		if !strings.HasPrefix(n, "/") {
			return Ref{}, errors.New("a file must be an absolute path")
		}
	case Device:
		if strings.ContainsAny(n, "/@") {
			return Ref{}, fmt.Errorf("want a bare device class, got %q", n)
		}
	case Host:
		if strings.ContainsAny(n, "/@") {
			return Ref{}, fmt.Errorf("want a hostname, got %q", n)
		}
	case Service:
		svc, verb, hasVerb := strings.Cut(n, "/")
		if svc == "" || strings.Contains(svc, "@") {
			return Ref{}, fmt.Errorf("malformed service name %q", svc)
		}
		if hasVerb && (verb == "" || strings.Contains(verb, "/")) {
			return Ref{}, fmt.Errorf("malformed verb %q", verb)
		}
	case Window:
		if strings.ContainsAny(n, "/@") {
			return Ref{}, fmt.Errorf("want a window id, got %q", n)
		}
	case Session:
		if !sessionNames[n] {
			return Ref{}, fmt.Errorf("%q is not one of clipboard, seat, epoch", n)
		}
	}
	return Ref{Class: cl, Name: n}, nil
}

// Constructors. Each one exists so that a caller cannot spell a class wrong,
// and so that grep finds every place a given class is named.

// NewFile names an absolute path. A path that cannot be spelled on the wire is
// still a valid object: Wire hashes it.
func NewFile(path string) (Ref, error) { return build(File, path) }

// NewDevice names a device class from decision 0014.
func NewDevice(class string) (Ref, error) { return build(Device, class) }

// NewWindow names a surface the compositor owns.
func NewWindow(id string) (Ref, error) { return build(Window, id) }

// NewHost names a network peer, port optional.
func NewHost(host string, port int) (Ref, error) {
	if port == 0 {
		return build(Host, host)
	}
	if port < 0 || port > 65535 {
		return Ref{}, fmt.Errorf("host object %q: port %d out of range", host, port)
	}
	// '.' is in the charset and ':' is not, so a port is spelled with '.'
	// rather than inventing a second separator.
	return build(Host, fmt.Sprintf("%s.%d", host, port))
}

// NewService names a bus service, or one verb of it when verb is not empty.
func NewService(name, verb string) (Ref, error) {
	// The separator is refused in name even though Parse must accept it in a
	// whole wire form: NewService("files/open", "") and NewService("files",
	// "open") would otherwise build the same object from two callers who meant
	// different things, and only one of them is right about its arguments.
	if strings.Contains(name, "/") {
		return Ref{}, fmt.Errorf("service object: name %q contains the verb separator; pass the verb as the second argument", name)
	}
	if verb == "" {
		return build(Service, name)
	}
	return build(Service, name+"/"+verb)
}

// Session singletons. These are the names; there are no others.
const (
	Clipboard = "clipboard"
	Seat      = "seat"
	Epoch     = "epoch"
)

var sessionNames = map[string]bool{Clipboard: true, Seat: true, Epoch: true}

// NewSession names one of the session's singletons.
func NewSession(what string) (Ref, error) { return build(Session, what) }

// Rights is what a handle permits over its object: a set, so that one handle
// can be strictly weaker than another and the daemon can check that.
type Rights uint8

const (
	// Read observes the object's contents or state.
	Read Rights = 1 << iota
	// Write changes them.
	Write
	// Use operates the object without reading or writing its contents: opening
	// a render node, connecting to a host, calling a service. A domain granted
	// device:gpu holds Use and nothing else, which is the honest description
	// of what it got.
	Use
	// Enumerate lists what is inside: a directory's entries, a service's
	// verbs. Separate from Read because knowing a file exists is a disclosure
	// of its own (the plumb tests already turn on this distinction).
	Enumerate
	// Destroy removes the object. Not implied by Write: an app that may
	// rewrite a file is not therefore allowed to unlink it, and the
	// destructive effect class exists precisely to mark that difference.
	Destroy
	// Grant passes a subset of these rights to someone else. Without it a
	// handle is a leaf and the holder cannot route it onward. This is the
	// right that makes capability routing expressible.
	Grant
)

// All is every right. Used by tests and by the session's own root handles;
// never mint this for an app.
const All = Read | Write | Use | Enumerate | Destroy | Grant

var rightNames = []struct {
	r Rights
	s string
}{{Read, "read"}, {Write, "write"}, {Use, "use"}, {Enumerate, "enumerate"}, {Destroy, "destroy"}, {Grant, "grant"}}

// Has reports whether every right in want is held. Zero want is true: it asks
// nothing.
func (r Rights) Has(want Rights) bool { return r&want == want }

// String is the stable wire spelling: names, sorted, comma separated, or
// "none". Sorted so a provenance record and a log line of the same rights
// compare as strings.
func (r Rights) String() string {
	var out []string
	for _, n := range rightNames {
		if r&n.r != 0 {
			out = append(out, n.s)
		}
	}
	if len(out) == 0 {
		return "none"
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// ParseRights reads that spelling back. An unknown name is an error, not a
// silently dropped right: dropping one quietly weakens a handle and the holder
// finds out at the worst moment.
func ParseRights(s string) (Rights, error) {
	if s == "" || s == "none" {
		return 0, nil
	}
	var out Rights
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		var ok bool
		for _, n := range rightNames {
			if n.s == f {
				out, ok = out|n.r, true
				break
			}
		}
		if !ok {
			return 0, fmt.Errorf("rights %q: unknown right %q", s, f)
		}
	}
	return out, nil
}

// ErrNotSubset is returned by Reduce when the asked-for rights exceed what the
// parent holds.
var ErrNotSubset = errors.New("derived rights must be a subset of the parent's")

// Reduce derives the rights of a handle passed on from one holding r.
//
// This is the whole invariant of a capability system that can route: a handle
// obtained from another handle is never stronger than its source, and only a
// holder of Grant may pass anything on at all. Everything else in routing is
// bookkeeping; this is the rule that makes the bookkeeping worth keeping.
func (r Rights) Reduce(want Rights) (Rights, error) {
	if !r.Has(Grant) {
		return 0, fmt.Errorf("%w: this handle does not carry the grant right", ErrNotSubset)
	}
	if !r.Has(want) {
		return 0, fmt.Errorf("%w: has %s, asked for %s", ErrNotSubset, r, want)
	}
	return want, nil
}

// Cap is an object and the rights over it: what a handle is minted for.
type Cap struct {
	Ref    Ref
	Rights Rights
}

// String is the human and log spelling: "file:/x [read,write]".
func (c Cap) String() string { return c.Ref.Wire() + " [" + c.Rights.String() + "]" }
