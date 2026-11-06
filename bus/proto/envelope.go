// Package proto defines the bus envelope (decision 0001) and topic matching.
//
// MVP wire format: one JSON object per line. See bus/README.md.
package proto

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// Kind is the message kind.
type Kind string

const (
	KindRequest  Kind = "request"
	KindResponse Kind = "response"
	KindEvent    Kind = "event"
	KindError    Kind = "error"
)

// QoS is the delivery class. Rank 0 is delivered first.
type QoS string

const (
	QoSInteractive QoS = "user-interactive"
	QoSInitiated   QoS = "user-initiated"
	QoSUtility     QoS = "utility"
	QoSBackground  QoS = "background"
)

// NumQoS is the number of QoS classes.
const NumQoS = 4

// Rank returns 0 (most urgent) to 3 (least urgent).
func (q QoS) Rank() int {
	switch q {
	case QoSInteractive:
		return 0
	case QoSInitiated:
		return 1
	case QoSUtility:
		return 2
	}
	return 3
}

// ParseQoS validates a class name. The empty string means user-initiated.
func ParseQoS(s string) (QoS, error) {
	switch q := QoS(s); q {
	case "":
		return QoSInitiated, nil
	case QoSInteractive, QoSInitiated, QoSUtility, QoSBackground:
		return q, nil
	}
	return "", fmt.Errorf("unknown qos %q (want user-interactive|user-initiated|utility|background)", s)
}

// BusTarget is the reserved target of the daemon's own control verbs.
const BusTarget = "bus"

// Origin records where a replayed message came from. It is informational
// and only honoured by a daemon started in replay mode.
type Origin struct {
	ID   string `json:"id"`
	From string `json:"from"`
	TS   string `json:"ts"`
}

// Envelope is the unit on the wire. Unknown fields survive a decode/encode
// round trip in Extra (0001: "Unknown fields are preserved").
type Envelope struct {
	ID        string          `json:"id,omitempty"`
	TS        string          `json:"ts,omitempty"`
	From      string          `json:"from,omitempty"` // overwritten by the daemon from SO_PEERCRED
	To        string          `json:"to,omitempty"`
	Kind      Kind            `json:"kind"`
	Verb      string          `json:"verb,omitempty"`
	Topic     string          `json:"topic,omitempty"`
	QoS       QoS             `json:"qos,omitempty"`
	Corr      string          `json:"corr,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Caps      []string        `json:"caps,omitempty"`    // opaque handles; never logged
	CapIDs    []string        `json:"cap_ids,omitempty"` // stable hashes; what the log stores
	Ephemeral bool            `json:"ephemeral,omitempty"`
	Origin    *Origin         `json:"origin,omitempty"`
	Segments  []Segment       `json:"segments,omitempty"` // descriptors; fds travel out of band

	// Seq and Prev are the log's hash chain. Only buslog.Append sets them
	// (the daemon clears whatever a client sent); see buslog/chain.go.
	Seq  uint64 `json:"seq,omitempty"`
	Prev string `json:"prev,omitempty"`

	// Attach holds the open fds that accompany Segments (SCM_RIGHTS). They
	// exist only in memory: never marshalled, never logged.
	Attach []*os.File `json:"-"`

	Extra map[string]json.RawMessage `json:"-"`
}

var knownFields = map[string]bool{
	"id": true, "ts": true, "from": true, "to": true, "kind": true, "verb": true,
	"topic": true, "qos": true, "corr": true, "payload": true, "caps": true,
	"cap_ids": true, "ephemeral": true, "origin": true, "segments": true,
	"seq": true, "prev": true,
}

type plain Envelope // no methods: avoids recursion

// MarshalJSON writes known fields plus preserved unknown ones.
func (e Envelope) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(plain(e))
	if err != nil || len(e.Extra) == 0 {
		return b, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range e.Extra {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// UnmarshalJSON reads known fields and keeps the rest in Extra.
func (e *Envelope) UnmarshalJSON(b []byte) error {
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k := range m {
		if knownFields[k] {
			delete(m, k)
		}
	}
	if len(m) > 0 {
		p.Extra = m
	}
	*e = Envelope(p)
	return nil
}

var verbRE = regexp.MustCompile(`^[a-z0-9_.-]+@v[0-9]+$`)

// Validate checks a client-sent envelope.
func (e *Envelope) Validate() error {
	switch e.Kind {
	case KindRequest:
		if e.To == "" {
			return fmt.Errorf("request needs a target in \"to\"")
		}
		if !verbRE.MatchString(e.Verb) {
			return fmt.Errorf("verb %q must look like name@v1 (major version is part of the name)", e.Verb)
		}
	case KindResponse, KindError:
		if e.Corr == "" {
			return fmt.Errorf("%s needs \"corr\" (the request id)", e.Kind)
		}
	case KindEvent:
		if e.Topic == "" {
			return fmt.Errorf("event needs a \"topic\"")
		}
	default:
		return fmt.Errorf("unknown kind %q", e.Kind)
	}
	return nil
}
