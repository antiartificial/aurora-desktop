package prov

import (
	"crypto/sha256"
	"fmt"
	"regexp"
)

// Verification statuses. Only "verified" is a positive claim, and it is
// narrow: see capbus.Verify for exactly what each one rests on.
const (
	StatusVerified     = "verified"
	StatusNotVerified  = "not_verified"
	StatusInconclusive = "inconclusive"
	StatusNoHandles    = "no_handles" // the record names no handle: nothing to check
)

// CapVerdict is the outcome for one grant's handle.
type CapVerdict struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Cap    string `json:"cap"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// CapReport is the outcome for one record. Status is the worst of its caps:
// not_verified beats inconclusive beats verified.
type CapReport struct {
	Status string       `json:"status"`
	Note   string       `json:"note,omitempty"`
	Caps   []CapVerdict `json:"caps"`
}

// Summarise sets Status from the per-cap verdicts.
func (r *CapReport) Summarise() {
	r.Status = StatusNoHandles
	for _, c := range r.Caps {
		switch {
		case c.Status == StatusNotVerified:
			r.Status = StatusNotVerified
			return
		case c.Status == StatusInconclusive:
			r.Status = StatusInconclusive
		case r.Status == StatusNoHandles:
			r.Status = StatusVerified
		}
	}
}

// ModeUse is the Mode of a device grant: there is no read or write to narrow.
const ModeUse = "use"

// Resource is the name this grant's handle is minted for.
func (g Grant) Resource() string {
	if g.Device != "" {
		return "device:" + g.Device
	}
	return Resource(g.Path, g.Mode)
}

var resourceOK = regexp.MustCompile(`^[A-Za-z0-9_.:/-]{1,128}$`)

// Resource is the name a grant's handle is minted for. It must fit the
// daemon's resource syntax; a path that does not (spaces, non-ASCII, long) is
// named by its hash instead, so every grant can be minted and the verifier
// can recompute the same name from the record.
func Resource(path, mode string) string {
	if r := "file:" + path + ":" + mode; resourceOK.MatchString(r) {
		return r
	}
	return fmt.Sprintf("filehash:%x:%s", sha256.Sum256([]byte(path)), mode)
}
