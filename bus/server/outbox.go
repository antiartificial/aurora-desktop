package server

import (
	"os"
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
	fds     int   // fds currently queued
	fdBytes int64 // segment bytes currently queued
	sent    int   // segments handed to the socket
}

type queued struct {
	line  []byte
	files []*os.File // fds owned by this queue entry until written or dropped
	bytes int64      // total declared segment bytes
	at    time.Time
}

// maxQueuedFDs bounds the fds the daemon holds for one slow receiver.
const maxQueuedFDs = 256

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

func (o *outbox) push(rank int, line []byte) { o.pushFiles(rank, line, nil, 0) }

// pop blocks until a line is available; ok is false once closed.
func (o *outbox) pop() (line []byte, ok bool) {
	line, files, ok := o.popFiles()
	closeFiles(files)
	return line, ok
}

// pushFiles queues a line and takes ownership of files: they are closed if
// the message is dropped or the outbox closed.
func (o *outbox) pushFiles(rank int, line []byte, files []*os.File, segBytes int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		closeFiles(files)
		return
	}
	if len(o.q[rank]) >= maxQueuedPerClass || o.fds+len(files) > maxQueuedFDs {
		o.dropped++
		closeFiles(files)
		return
	}
	o.fds += len(files)
	o.fdBytes += segBytes
	o.q[rank] = append(o.q[rank], queued{line, files, segBytes, time.Now()})
	o.cond.Signal()
}

func closeFiles(fs []*os.File) {
	for _, f := range fs {
		f.Close()
	}
}

// popFiles is pop plus the fds to send with the line; the caller closes them.
func (o *outbox) popFiles() (line []byte, files []*os.File, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for {
		if o.closed {
			return nil, nil, false
		}
		for r := range o.q {
			if len(o.q[r]) > 0 {
				it := o.q[r][0]
				o.q[r][0] = queued{}
				o.q[r] = o.q[r][1:]
				o.fds -= len(it.files)
				o.fdBytes -= it.bytes
				o.sent += len(it.files)
				return it.line, it.files, true
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

func (o *outbox) segStats() (queued int, queuedBytes int64, sent int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.fds, o.fdBytes, o.sent
}

func (o *outbox) close() {
	o.mu.Lock()
	o.closed = true
	for r := range o.q {
		for _, it := range o.q[r] {
			closeFiles(it.files)
		}
		o.q[r] = nil
	}
	o.fds, o.fdBytes = 0, 0
	o.mu.Unlock()
	o.cond.Broadcast()
}
