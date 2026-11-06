// Package hlc is a small hybrid logical clock: wall clock plus a logical
// counter, monotonic per clock, serialised as a lexicographically sortable
// string.
package hlc

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Timestamp is a wall time (unix ns) and a logical counter.
type Timestamp struct {
	Wall    int64
	Counter uint64
}

// String is fixed width (uint64 needs at most 20 digits), so string order
// equals timestamp order.
func (t Timestamp) String() string { return fmt.Sprintf("%019d-%020d", t.Wall, t.Counter) }

// Less orders timestamps.
func (t Timestamp) Less(o Timestamp) bool {
	return t.Wall < o.Wall || (t.Wall == o.Wall && t.Counter < o.Counter)
}

// Parse reads the String form.
func Parse(s string) (Timestamp, error) {
	w, c, ok := strings.Cut(s, "-")
	if !ok {
		return Timestamp{}, fmt.Errorf("bad hlc %q", s)
	}
	wall, err := strconv.ParseInt(w, 10, 64)
	if err != nil {
		return Timestamp{}, fmt.Errorf("bad hlc %q: %w", s, err)
	}
	ctr, err := strconv.ParseUint(c, 10, 64)
	if err != nil {
		return Timestamp{}, fmt.Errorf("bad hlc %q: %w", s, err)
	}
	return Timestamp{wall, ctr}, nil
}

// Clock issues monotonic timestamps.
type Clock struct {
	mu   sync.Mutex
	last Timestamp
	now  func() int64
}

// New returns a clock on the system wall clock.
func New() *Clock { return NewWithSource(func() int64 { return time.Now().UnixNano() }) }

// NewWithSource lets tests control the wall clock.
func NewWithSource(now func() int64) *Clock { return &Clock{now: now} }

// Now returns a timestamp strictly greater than any previously issued.
func (c *Clock) Now() Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w := c.now(); w > c.last.Wall {
		c.last = Timestamp{Wall: w}
	} else {
		c.last.Counter++
	}
	return c.last
}

// Update merges a remote timestamp (for federation) and returns a new one
// greater than both the local clock and the remote.
func (c *Clock) Update(remote Timestamp) Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.now()
	switch {
	case w > c.last.Wall && w > remote.Wall:
		c.last = Timestamp{Wall: w}
	case remote.Wall > c.last.Wall:
		c.last = Timestamp{remote.Wall, remote.Counter + 1}
	case remote.Wall == c.last.Wall && remote.Counter > c.last.Counter:
		c.last.Counter = remote.Counter + 1
	default:
		c.last.Counter++
	}
	return c.last
}
