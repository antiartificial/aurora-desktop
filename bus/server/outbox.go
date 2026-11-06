package server

import (
	"sync"
	"time"

	"github.com/antiartificial/aurora/bus/proto"
)

// maxQueuedPerClass bounds memory for a client that stops reading; beyond it
// new messages of that class are dropped and counted.
const maxQueuedPerClass = 100000

// outbox is a per-connection priority queue: four FIFO queues, one per QoS
// class. pop always serves the most urgent non-empty class, so
// user-interactive traffic never waits behind background traffic that is
// still queued. There is no aging: under sustained load in a higher class a
// lower class makes no progress at all (see README "Known design consequence").
// stats exposes how long the oldest message of each class has been waiting.
type outbox struct {
	mu      sync.Mutex
	cond    *sync.Cond
	q       [proto.NumQoS][]queued
	closed  bool
	dropped int
}

type queued struct {
	line []byte
	at   time.Time
}

// ClassStats is the backlog of one QoS class on one connection.
type ClassStats struct {
	Queued      int   `json:"queued"`
	OldestAgeMS int64 `json:"oldest_age_ms"` // 0 when empty
}

func newOutbox() *outbox {
	o := &outbox{}
	o.cond = sync.NewCond(&o.mu)
	return o
}

func (o *outbox) push(rank int, line []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	if len(o.q[rank]) >= maxQueuedPerClass {
		o.dropped++
		return
	}
	o.q[rank] = append(o.q[rank], queued{line, time.Now()})
	o.cond.Signal()
}

// pop blocks until a line is available; ok is false once closed.
func (o *outbox) pop() (line []byte, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for {
		if o.closed {
			return nil, false
		}
		for r := range o.q {
			if len(o.q[r]) > 0 {
				line = o.q[r][0].line
				o.q[r][0] = queued{}
				o.q[r] = o.q[r][1:]
				return line, true
			}
		}
		o.cond.Wait()
	}
}

// stats reports, per class (rank order), queue depth and the age of the
// oldest undelivered message: the starvation signal.
func (o *outbox) stats() (cs [proto.NumQoS]ClassStats, dropped int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	for r := range o.q {
		cs[r].Queued = len(o.q[r])
		if len(o.q[r]) > 0 {
			cs[r].OldestAgeMS = now.Sub(o.q[r][0].at).Milliseconds()
		}
	}
	return cs, o.dropped
}

func (o *outbox) close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.cond.Broadcast()
}
