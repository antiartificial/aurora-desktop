package proto

import (
	"fmt"
)

// MaxSegments is the most segments one message may declare (and so the most
// fds one message may carry; the kernel's own SCM_MAX_FD is 253).
const MaxSegments = 16

// Segment describes one shared-memory segment travelling with a message
// (0001: "data off the bus"). The fd itself travels out of band over
// SCM_RIGHTS; this descriptor is all the JSON line and the log ever hold.
// SHA256 is set by the daemon from the sealed contents; a sender's value is
// overwritten. Absent is set on a replayed descriptor: contents are not
// replayable, only the descriptor and hash are.
type Segment struct {
	ID     string `json:"id"`
	Bytes  int64  `json:"bytes"`
	Sealed bool   `json:"sealed"`
	Mime   string `json:"mime,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Absent bool   `json:"absent,omitempty"`
}

// Sig is the canonical form compared by the replay checker. Absent is left
// out on purpose: a replayed descriptor is Absent by construction.
func (s Segment) Sig() string {
	return fmt.Sprintf("%s|%d|%t|%s|%s", s.ID, s.Bytes, s.Sealed, s.Mime, s.SHA256)
}

// ValidateSegments checks declared descriptors (not their fds).
func (e *Envelope) ValidateSegments() error {
	if len(e.Segments) > MaxSegments {
		return fmt.Errorf("%d segments declared, at most %d allowed", len(e.Segments), MaxSegments)
	}
	seen := map[string]bool{}
	for _, s := range e.Segments {
		if s.ID == "" || seen[s.ID] {
			return fmt.Errorf("segment id %q is empty or duplicated", s.ID)
		}
		if s.Bytes < 0 {
			return fmt.Errorf("segment %q has negative size", s.ID)
		}
		seen[s.ID] = true
	}
	return nil
}

// Present is how many fds must accompany this envelope: every declared
// segment except replayed (Absent) ones.
func (e *Envelope) Present() int {
	n := 0
	for _, s := range e.Segments {
		if !s.Absent {
			n++
		}
	}
	return n
}

// CloseAttachments closes and forgets any fds attached to the envelope.
func (e *Envelope) CloseAttachments() {
	for _, f := range e.Attach {
		f.Close()
	}
	e.Attach = nil
}

// AttachIndex returns the index into Attach of segment id, or -1.
func (e *Envelope) AttachIndex(id string) int {
	j := 0
	for _, s := range e.Segments {
		if s.Absent {
			if s.ID == id {
				return -1
			}
			continue
		}
		if s.ID == id {
			if j < len(e.Attach) {
				return j
			}
			return -1
		}
		j++
	}
	return -1
}
