package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/shell/launcher"
)

// describeReply is registry.describe@v1 including the capability references
// shell/launcher's Description drops.
type describeReply struct {
	launcher.Description
	Capabilities []string `json:"capabilities"`
}

// Controller is everything the gateway asks of the system: the registry's
// verb catalogue and registry.invoke@v1. There is deliberately no method that
// decides an approval. The gateway cannot approve anything because it has no
// way to ask for it.
type Controller interface {
	Verbs(ctx context.Context) ([]launcher.Verb, error)
	Describe(ctx context.Context, app, verb string) (describeReply, error)
	// Invoke is registry.invoke@v1. mayBlock allows it to wait on an approval.
	Invoke(ctx context.Context, r launcher.InvokeRequest) (launcher.InvokeResult, error)
	// WatchApprovals delivers approval.requested events from the controller.
	WatchApprovals(ctx context.Context) (<-chan launcher.ApprovalRequest, error)
}

// Auditor publishes an event on the bus. busd stamps the sender.
type Auditor interface {
	Publish(topic string, payload []byte) error
}

// BusConn is the production Controller and Auditor: busd over its socket,
// reusing shell/launcher's backend for everything it already does.
type BusConn struct {
	be      launcher.Backend
	apr     launcher.Approver
	c       *client.Client
	timeout time.Duration
}

// Dial connects to busd. timeout bounds each registry call except an invoke
// that may block on an approval, which the launcher backend extends.
func Dial(socket string, timeout time.Duration) (*BusConn, error) {
	c, err := client.Dial(socket)
	if err != nil {
		return nil, fmt.Errorf("cannot reach busd at %s: %w", socket, err)
	}
	be := launcher.DialBackend(socket, timeout)
	apr, ok := be.(launcher.Approver)
	if !ok {
		c.Close()
		return nil, errors.New("launcher backend cannot watch approvals")
	}
	return &BusConn{be: be, apr: apr, c: c, timeout: timeout}, nil
}

func (b *BusConn) Close() error { b.be.Close(); return b.c.Close() }

func (b *BusConn) Verbs(ctx context.Context) ([]launcher.Verb, error) { return b.be.Verbs(ctx, "") }

func (b *BusConn) Describe(ctx context.Context, app, verb string) (describeReply, error) {
	var d describeReply
	payload, _ := json.Marshal(map[string]string{"app": app, "verb": verb})
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	r, err := b.c.Call(ctx, proto.Envelope{To: "registry", Verb: "registry.describe@v1", Payload: payload})
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(r.Payload, &d)
}

func (b *BusConn) Invoke(ctx context.Context, r launcher.InvokeRequest) (launcher.InvokeResult, error) {
	return b.be.Invoke(ctx, r)
}

func (b *BusConn) WatchApprovals(ctx context.Context) (<-chan launcher.ApprovalRequest, error) {
	return b.apr.WatchApprovals(ctx)
}

// Publish sends a logged (non-ephemeral) event.
func (b *BusConn) Publish(topic string, payload []byte) error {
	return b.c.Publish(topic, payload, proto.QoSUtility, false)
}
