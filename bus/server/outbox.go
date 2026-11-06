package server

import (
	"sync"

	"github.com/antiartificial/aurora/bus/proto"
)

// maxQueuedPerClass bounds memory for a client that stops reading; beyond it
// new messages of that class are dropped and counted.
const maxQueuedPerClass = 100000

// outbox is a per-connection priority queue: four FIFO queues, one per QoS
// class. pop always serves the most urgent non-empty class, so
// user-interactive traffic never waits behind background traffic that is
// still queued.
type outbox struct {
	mu      sync.Mutex
	cond    *sync.Cond
	q       [proto.NumQoS][][]byte
	closed  bool
	dropped int
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
	o.q[rank] = append(o.q[rank], line)
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
				line = o.q[r][0]
				o.q[r][0] = nil
				o.q[r] = o.q[r][1:]
				return line, true
			}
		}
		o.cond.Wait()
	}
}

func (o *outbox) close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
	o.cond.Broadcast()
}
