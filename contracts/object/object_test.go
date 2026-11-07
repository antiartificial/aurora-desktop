package object

import (
	"errors"
	"strings"
	"testing"
)

// The property the old scheme could not express: rights are not part of
// identity, so the same file under different authority is ONE object.
func TestSameObjectUnderDifferentRights(t *testing.T) {
	f, err := NewFile("/home/arti/notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	ro, rw := Cap{f, Read}, Cap{f, Read | Write}
	if ro.Ref != rw.Ref {
		t.Fatalf("different rights produced different objects: %v vs %v", ro.Ref, rw.Ref)
	}
	if ro.Ref.Wire() != "file:/home/arti/notes.txt" {
		t.Fatalf("wire form carries authority: %q", ro.Ref.Wire())
	}
	// and the two are distinguishable where it matters, in the rights
	if ro.Rights.Has(Write) || !rw.Rights.Has(Write) {
		t.Fatalf("%v %v", ro, rw)
	}
}

// Reduce is the routing invariant. A derived handle is never stronger than its
// source, and only a holder of Grant derives at all.
func TestReduceNeverStrengthens(t *testing.T) {
	parent := Read | Write | Grant
	for _, want := range []Rights{Read, Write, Read | Write, 0} {
		got, err := parent.Reduce(want)
		if err != nil {
			t.Fatalf("%s should be derivable from %s: %v", want, parent, err)
		}
		if !parent.Has(got) {
			t.Fatalf("derived %s exceeds parent %s", got, parent)
		}
	}
	// Destroy was never held, so it cannot be handed out.
	if _, err := parent.Reduce(Read | Destroy); !errors.Is(err, ErrNotSubset) {
		t.Fatalf("a right the parent lacks was derivable: %v", err)
	}
	// Grant itself is a right, so the chain can be cut: a child given
	// read-without-grant is a leaf and routes nothing further.
	child, err := parent.Reduce(Read)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Reduce(Read); !errors.Is(err, ErrNotSubset) {
		t.Fatalf("a leaf handle passed its object on anyway: %v", err)
	}
	// A handle that holds everything but Grant is still a leaf.
	leaf := All & ^Grant
	if _, err := leaf.Reduce(Read); !errors.Is(err, ErrNotSubset) {
		t.Fatalf("all rights but grant still routed: %v", err)
	}
}

// Every right must survive the round trip through the wire spelling, and the
// spelling must be stable regardless of the order bits were set.
func TestRightsRoundTripAndStableSpelling(t *testing.T) {
	for r := Rights(0); r <= All; r++ {
		if r & ^All != 0 {
			continue
		}
		s := r.String()
		back, err := ParseRights(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if back != r {
			t.Fatalf("%s parsed back as %s", s, back)
		}
	}
	if got := (Write | Read).String(); got != (Read | Write).String() {
		t.Fatalf("spelling depends on bit order: %q", got)
	}
	if got := Rights(0).String(); got != "none" {
		t.Fatalf("empty rights spell %q", got)
	}
	if _, err := ParseRights("read,teleport"); err == nil {
		t.Fatal("an unknown right was dropped silently")
	}
}

// A wire form must always split into exactly a class and a name. This is what
// "file:/x:rw" got wrong: it had two colons and no rule about which one ended
// the name.
func TestWireFormIsUnambiguous(t *testing.T) {
	for _, s := range []string{
		"file:/x", "device:gpu", "host:example.com", "host:example.com.443",
		"service:org.aurora.files", "service:org.aurora.files/open@v1", "session:clipboard",
	} {
		r, err := Parse(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if r.Wire() != s {
			t.Fatalf("%q round-tripped as %q", s, r.Wire())
		}
		if n := strings.Count(s, ":"); n != 1 {
			t.Fatalf("%q has %d colons: the name is ambiguous", s, n)
		}
	}
}

func TestParseRefusesRatherThanPassingThrough(t *testing.T) {
	bad := []string{
		"",                           // nothing
		"file",                       // no class separator
		"filehash:/x",                // the old second file namespace is gone
		"net:example.com",            // renamed to host, and the old spelling must not work
		"clip:clipboard",             // never a class
		"file:relative",              // a file is absolute
		"file:/x:rw",                 // the old authority-in-the-name form
		"session:printer",            // not a singleton this names
		"device:/dev/dri/renderD128", // a node, not a class
		"file:#abcd",                 // short digest
	}
	for _, s := range bad {
		if r, err := Parse(s); err == nil {
			t.Errorf("%q was accepted as %v", s, r)
		}
	}
}

// A name that cannot be spelled in the wire charset is hashed, and the hashed
// form still round-trips to the same object, so a verifier recomputing the
// name from a provenance record reaches the handle that was minted.
func TestUnspellableNamesHashAndStillMatch(t *testing.T) {
	p := "/home/arti/Ümlaut and spaces/file (1).txt"
	f, err := NewFile(p)
	if err != nil {
		t.Fatal(err)
	}
	w := f.Wire()
	if !strings.HasPrefix(w, "file:#") {
		t.Fatalf("unspellable name went on the wire literally: %q", w)
	}
	if len(w) > 128 {
		t.Fatalf("wire form %d bytes, over busd's resource limit", len(w))
	}
	back, err := Parse(w)
	if err != nil {
		t.Fatal(err)
	}
	if back.Wire() != w {
		t.Fatalf("hashed form did not round-trip: %q vs %q", back.Wire(), w)
	}
	// A verifier starting from the same path recomputes the same wire form.
	again, _ := NewFile(p)
	if again.Wire() != w {
		t.Fatal("hashing is not stable")
	}
	// A different unspellable path is a different object.
	other, _ := NewFile(p + "x")
	if other.Wire() == w {
		t.Fatal("two paths collided")
	}
	// A long but spellable path is hashed too rather than overflowing.
	long, _ := NewFile("/" + strings.Repeat("a", 200))
	if !strings.HasPrefix(long.Wire(), "file:#") || len(long.Wire()) > 128 {
		t.Fatalf("long path: %q (%d bytes)", long.Wire(), len(long.Wire()))
	}
}

// Every wire form this package can produce must satisfy busd's resource regex,
// or the handle cannot be minted at all. The constructors are the only way in,
// so checking them covers the namespace.
func TestEveryWireFormCanBeMinted(t *testing.T) {
	mk := func(r Ref, err error) Ref {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	refs := []Ref{
		mk(NewFile("/etc/hosts")),
		mk(NewFile("/home/arti/Ümlaut.txt")),
		mk(NewDevice("gpu")),
		mk(NewDevice("wayland")),
		mk(NewHost("example.com", 0)),
		mk(NewHost("example.com", 443)),
		mk(NewService("org.aurora.files", "")),
		mk(NewService("org.aurora.files", "open@v1")),
		mk(NewSession(Clipboard)),
		mk(NewSession(Seat)),
		mk(NewSession(Epoch)),
	}
	// Mirrors bus/server/caps.go's resourceRE. Kept as a literal on purpose:
	// if the daemon's charset ever changes, this test fails and names the
	// coupling rather than letting mints start failing at runtime.
	const allowed = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.:/-#@"
	for _, r := range refs {
		w := r.Wire()
		if w == "" || len(w) > 128 {
			t.Errorf("%q is %d bytes", w, len(w))
		}
		if i := strings.IndexFunc(w, func(c rune) bool { return !strings.ContainsRune(allowed, c) }); i >= 0 {
			t.Errorf("%q has a character busd will not accept at %d", w, i)
		}
		if _, err := Parse(w); err != nil {
			t.Errorf("%q does not parse back: %v", w, err)
		}
	}
}

func TestConstructorsRefuseMalformedInput(t *testing.T) {
	if _, err := NewFile("relative/path"); err == nil {
		t.Error("relative file accepted")
	}
	if _, err := NewDevice("/dev/dri/renderD128"); err == nil {
		t.Error("a device node was accepted as a class")
	}
	if _, err := NewHost("example.com", 70000); err == nil {
		t.Error("out-of-range port accepted")
	}
	if _, err := NewHost("exa mple.com", 0); err == nil {
		t.Error("unspellable host accepted")
	}
	if _, err := NewService("files/open", ""); err == nil {
		t.Error("a service name containing the verb separator was accepted")
	}
	if _, err := NewSession("printer"); err == nil {
		t.Error("an invented singleton was accepted")
	}
}
