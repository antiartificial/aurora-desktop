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

// Grant is one filesystem grant given to the app's domain for one invoke.
type Grant struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"` // "ro" or "rw"
	Source string `json:"source"`
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
	TS             string          `json:"ts"`
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
