package dom

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

// Volume (decision 0015). A bus event per connection would let one chatty app
// fill the append-only log, so this is the only way a connection becomes an
// event:
//
//   - the FIRST connection of each (host, verdict, reason) in a run is a net.conn
//     event, at once. A refusal of a new host is never delayed or coalesced away
//     with the allowed traffic around it, because a new refused host is the thing
//     a person wants to see.
//   - every later one only increments a counter. Counters leave as one net.summary
//     per Interval (and one at the end of the run), however many connections there
//     were.
//   - a token bucket bounds even the first-seen events (a domain asking for a
//     million different names would otherwise be a million events): burst 20, then
//     2 a second. What the bucket withholds is counted in the summary, not lost.
//   - the table of distinct keys is capped; past the cap new keys share "(other)".
//
// The worst case is therefore a bounded number of events per run plus one
// summary per Interval, independent of how many connections the app makes.
const (
	reportBurst    = 20
	reportRate     = 2.0 // tokens a second
	reportMaxKeys  = 512
	DefaultSummary = 30 * time.Second
)

type connKey struct{ host, verdict, reason string }

// NetReporter turns the proxy's decisions into bus events with bounded volume.
type NetReporter struct {
	send     func(map[string]any) error
	identity string
	warn     io.Writer
	interval time.Duration

	mu      sync.Mutex
	seen    map[connKey]bool
	tally   map[connKey]int
	dropped int
	tokens  float64
	last    time.Time
	warned  bool
	stop    chan struct{}
	done    chan struct{}
	now     func() time.Time
}

// NewNetReporter starts a reporter. send publishes one bus.net.report@v1
// payload; nil send makes decisions countable (Stats) but publishes nothing.
func NewNetReporter(identity string, send func(map[string]any) error, interval time.Duration, warn io.Writer) *NetReporter {
	if interval <= 0 {
		interval = DefaultSummary
	}
	if warn == nil {
		warn = io.Discard
	}
	r := &NetReporter{send: send, identity: identity, warn: warn, interval: interval,
		seen: map[connKey]bool{}, tally: map[connKey]int{}, tokens: reportBurst,
		stop: make(chan struct{}), done: make(chan struct{}), now: time.Now}
	r.last = r.now()
	go r.loop()
	return r
}

func (r *NetReporter) loop() {
	defer close(r.done)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			r.Flush()
		case <-r.stop:
			return
		}
	}
}

// Record notes one connection decision.
func (r *NetReporter) Record(host string, port int, verdict, reason string) {
	k := connKey{host, verdict, reason}
	r.mu.Lock()
	if !r.seen[k] && len(r.seen) >= reportMaxKeys {
		k = connKey{"(other)", verdict, reason}
	}
	r.tally[k]++
	first := !r.seen[k]
	emit := false
	if first {
		r.seen[k] = true
		n := r.now()
		r.tokens += n.Sub(r.last).Seconds() * reportRate
		if r.tokens > reportBurst {
			r.tokens = reportBurst
		}
		r.last = n
		if r.tokens >= 1 {
			r.tokens--
			emit = true
		} else {
			r.dropped++
		}
	}
	r.mu.Unlock()
	if emit && r.send != nil {
		r.deliver(map[string]any{"identity": r.identity, "kind": "conn", "host": host, "port": port,
			"verdict": verdict, "reason": reason, "first": true})
	}
}

// Flush sends the counters accumulated since the last flush as one summary.
func (r *NetReporter) Flush() {
	r.mu.Lock()
	if len(r.tally) == 0 && r.dropped == 0 {
		r.mu.Unlock()
		return
	}
	var es []map[string]any
	keys := make([]connKey, 0, len(r.tally))
	for k := range r.tally {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return a.host < b.host || (a.host == b.host && (a.verdict < b.verdict || (a.verdict == b.verdict && a.reason < b.reason)))
	})
	for _, k := range keys {
		es = append(es, map[string]any{"host": k.host, "verdict": k.verdict, "reason": k.reason, "count": r.tally[k]})
	}
	dropped := r.dropped
	r.tally, r.dropped = map[connKey]int{}, 0
	r.mu.Unlock()
	if r.send != nil {
		r.deliver(map[string]any{"identity": r.identity, "kind": "summary", "entries": es, "dropped": dropped})
	}
}

func (r *NetReporter) deliver(p map[string]any) {
	if err := r.send(p); err != nil {
		r.mu.Lock()
		w := !r.warned
		r.warned = true
		r.mu.Unlock()
		if w {
			fmt.Fprintf(r.warn, "domain: warning: network events are not reaching the bus (%v); connections are still enforced, just not reported\n", err)
		}
	}
}

// Close stops the ticker and sends the final summary.
func (r *NetReporter) Close() {
	close(r.stop)
	<-r.done
	r.Flush()
}
