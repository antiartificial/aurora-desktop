// Package client is the Go client for busd.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/antiartificial/aurora/bus/proto"
)

// RemoteError is an error envelope returned by the daemon or a service.
type RemoteError struct {
	Code    string
	Message string
	From    string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("%s: %s (from %s)", e.Code, e.Message, e.From)
}

// ErrClosed is returned once the connection is gone.
var ErrClosed = errors.New("bus connection closed")

// Client is one connection to the bus. Safe for concurrent use.
type Client struct {
	nc       net.Conn
	wmu      sync.Mutex
	next     atomic.Uint64
	mu       sync.Mutex
	pending  map[string]chan proto.Envelope
	events   chan proto.Envelope
	requests chan proto.Envelope
	done     chan struct{}
}

// Dial connects to the daemon's Unix socket.
func Dial(path string) (*Client, error) {
	nc, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("cannot reach busd at %s (is it running? set --socket or AURORA_BUS_SOCK): %w", path, err)
	}
	c := &Client{
		nc: nc, pending: map[string]chan proto.Envelope{},
		events: make(chan proto.Envelope, 1024), requests: make(chan proto.Envelope, 1024),
		done: make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// Events delivers events matching this client's subscriptions.
func (c *Client) Events() <-chan proto.Envelope { return c.events }

// Requests delivers requests addressed to services this client registered.
func (c *Client) Requests() <-chan proto.Envelope { return c.requests }

// Done is closed when the connection ends.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close disconnects.
func (c *Client) Close() error { return c.nc.Close() }

func (c *Client) readLoop() {
	defer close(c.done)
	defer close(c.events)
	defer close(c.requests)
	sc := bufio.NewScanner(c.nc)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		var e proto.Envelope
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Kind {
		case proto.KindResponse, proto.KindError:
			c.mu.Lock()
			ch := c.pending[e.Corr]
			delete(c.pending, e.Corr)
			c.mu.Unlock()
			if ch != nil {
				ch <- e
			}
		case proto.KindEvent:
			c.events <- e
		case proto.KindRequest:
			c.requests <- e
		}
	}
}

// Send writes an envelope without waiting for anything.
func (c *Client) Send(e proto.Envelope) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.nc.Write(append(b, '\n'))
	return err
}

// Call sends a request (or control message) and waits for the matching
// response. An error envelope comes back as *RemoteError.
func (c *Client) Call(ctx context.Context, e proto.Envelope) (proto.Envelope, error) {
	e.ID = fmt.Sprintf("c%d", c.next.Add(1))
	if e.Kind == "" {
		e.Kind = proto.KindRequest
	}
	ch := make(chan proto.Envelope, 1)
	c.mu.Lock()
	c.pending[e.ID] = ch
	c.mu.Unlock()
	forget := func() { c.mu.Lock(); delete(c.pending, e.ID); c.mu.Unlock() }
	if err := c.Send(e); err != nil {
		forget()
		return proto.Envelope{}, err
	}
	select {
	case r := <-ch:
		if r.Kind == proto.KindError {
			re := &RemoteError{From: r.From}
			var p struct{ Code, Message string }
			json.Unmarshal(r.Payload, &p)
			re.Code, re.Message = p.Code, p.Message
			if re.Code == "" {
				re.Message = string(r.Payload)
			}
			return r, re
		}
		return r, nil
	case <-ctx.Done():
		forget()
		return proto.Envelope{}, ctx.Err()
	case <-c.done:
		return proto.Envelope{}, ErrClosed
	}
}

// Control calls a daemon verb (bus.*) and returns the reply payload.
func (c *Client) Control(ctx context.Context, verb string, payload any) (json.RawMessage, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	r, err := c.Call(ctx, proto.Envelope{To: proto.BusTarget, Verb: verb, Payload: raw})
	return r.Payload, err
}

// Register claims a service name; requests arrive on Requests().
func (c *Client) Register(ctx context.Context, name string) error {
	_, err := c.Control(ctx, "bus.register@v1", map[string]string{"name": name})
	return err
}

// Subscribe adds a topic glob; events arrive on Events().
func (c *Client) Subscribe(ctx context.Context, topic string) error {
	_, err := c.Control(ctx, "bus.subscribe@v1", map[string]string{"topic": topic})
	return err
}

// Publish sends an event.
func (c *Client) Publish(topic string, payload json.RawMessage, qos proto.QoS, ephemeral bool) error {
	return c.Send(proto.Envelope{Kind: proto.KindEvent, Topic: topic, Payload: payload, QoS: qos, Ephemeral: ephemeral})
}

// Reply answers a request received on Requests().
func (c *Client) Reply(req proto.Envelope, payload json.RawMessage) error {
	return c.Send(proto.Envelope{Kind: proto.KindResponse, Verb: req.Verb, Corr: req.ID, Payload: payload, QoS: req.QoS})
}

// ReplyError answers a request with an error.
func (c *Client) ReplyError(req proto.Envelope, code, msg string) error {
	p, _ := json.Marshal(map[string]string{"code": code, "message": msg})
	return c.Send(proto.Envelope{Kind: proto.KindError, Verb: req.Verb, Corr: req.ID, Payload: p, QoS: req.QoS})
}

// Flush returns once the daemon has processed everything this client sent
// before it (messages on one connection are handled in order).
func (c *Client) Flush(ctx context.Context) error {
	_, err := c.Control(ctx, "bus.ping@v1", nil)
	return err
}
