package supervisor

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

// Topic namespace. Events are published as "supervisor.service.<what>".
// This is deliberately NOT a prefix busd treats as a system namespace
// (provenance. approval. bus. agent. index.): bus. is refused to clients, and
// the rest are not ours. Consequence, stated plainly: until busd's
// systemPrefixes (bus/server/server.go) gains "supervisor.", a confined domain
// that subscribes to "**" or "supervisor.**" DOES receive these events. They name
// services, restart counts and exit reasons, and no paths or capability ids. A
// domain has no business with them; the one-line fix is to add the prefix.
const (
	TopicStarted   = "supervisor.service.started"
	TopicRestarted = "supervisor.service.restarted"
	TopicExited    = "supervisor.service.exited"
	TopicGaveUp    = "supervisor.service.gave_up"
	TopicStopped   = "supervisor.service.stopped"
	TopicHealth    = "supervisor.service.health"
)

type queued struct {
	topic string
	body  []byte
}

// Bus is the supervisor's own connection to busd: used to publish its events
// and to ask busd about the services it supervises. It reconnects on its own;
// events raised while busd is down (the busd-exited event itself, always) wait
// in a bounded queue and go out, stamped with their real time, when it is back.
type Bus struct {
	sock string
	logf func(string, ...any)

	mu      sync.Mutex
	c       *client.Client
	q       []queued
	dropped int
	kick    chan struct{}
}

const maxQueue = 512

func NewBus(sock string, logf func(string, ...any)) *Bus {
	return &Bus{sock: sock, logf: logf, kick: make(chan struct{}, 1)}
}

// Client returns a live connection, dialing if the last one is gone.
func (b *Bus) Client() (*client.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.c != nil {
		select {
		case <-b.c.Done():
			b.c.Close()
			b.c = nil
		default:
			return b.c, nil
		}
	}
	c, err := client.Dial(b.sock)
	if err != nil {
		return nil, err
	}
	b.c = c
	return c, nil
}

// Emit queues an event. It never blocks and never fails the caller.
func (b *Bus) Emit(topic string, body map[string]any) {
	body["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	b.mu.Lock()
	if b.dropped > 0 {
		body["dropped_before"] = b.dropped
		b.dropped = 0
	}
	raw, _ := json.Marshal(body)
	if len(b.q) >= maxQueue {
		b.q = b.q[1:]
		b.dropped++
	}
	b.q = append(b.q, queued{topic, raw})
	b.mu.Unlock()
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// Pump delivers queued events until ctx ends.
func (b *Bus) Pump(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			b.flush()
			return
		case <-t.C:
		case <-b.kick:
		}
		b.flush()
	}
}

func (b *Bus) flush() {
	b.mu.Lock()
	n := len(b.q)
	b.mu.Unlock()
	if n == 0 {
		return
	}
	c, err := b.Client()
	if err != nil {
		return // busd is down; keep them
	}
	for {
		b.mu.Lock()
		if len(b.q) == 0 {
			b.mu.Unlock()
			return
		}
		e := b.q[0]
		b.mu.Unlock()
		if err := c.Publish(e.topic, e.body, proto.QoSUtility, false); err != nil {
			return
		}
		b.mu.Lock()
		if len(b.q) > 0 {
			b.q = b.q[1:]
		}
		b.mu.Unlock()
	}
}

// peerPID connects to the bus socket and returns the pid of whatever accepted:
// SO_PEERCRED on the client side of a unix socket names the listener's owner.
func peerPID(sock string) (int, error) {
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		return 0, err
	}
	var uc *syscall.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) { uc, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) }); err != nil {
		return 0, err
	}
	if cerr != nil {
		return 0, cerr
	}
	return int(uc.Pid), nil
}

// Clean strips anything that could move a terminal or forge a line from a
// string that came off the bus, and caps its length.
func Clean(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsPrint(r) {
			out = append(out, r)
		} else {
			out = append(out, '?')
		}
		if len(out) >= 120 {
			out = append(out, '…')
			break
		}
	}
	return string(out)
}
