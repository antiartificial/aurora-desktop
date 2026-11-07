// Package capbus is the controller's use of the daemon's capability table
// (bus.mint@v1, bus.revoke@v1, bus.inspect@v1): it mints a handle per grant,
// presents them so the daemon's holder check runs, revokes them, and later
// checks what the daemon says about a record's handle ids.
package capbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

// Handle is a minted capability. Token is the secret; ID is what the log,
// `bus inspect` and the provenance record show.
type Handle struct{ Token, ID string }

// Client talks to busd over the controller's session-socket connection, so
// the controller is the minter and holder of everything it mints.
type Client struct {
	C       *client.Client
	Timeout time.Duration // per call, default 5s

	mu     sync.Mutex
	self   string            // our bus identity, learned from the epoch mint
	epoch  Handle            // minted once; its survival marks the daemon's life
	ledger map[string]string // handle id -> resource we minted it for (our memory, not the daemon's)
}

// EpochResource names the epoch handle.
const EpochResource = "controller:epoch"

func (b *Client) ctx() (context.Context, context.CancelFunc) {
	t := b.Timeout
	if t == 0 {
		t = 5 * time.Second
	}
	return context.WithTimeout(context.Background(), t)
}

func (b *Client) control(verb string, payload any) (json.RawMessage, error) {
	ctx, cancel := b.ctx()
	defer cancel()
	return b.C.Control(ctx, verb, payload)
}

// Mint asks the daemon for a handle naming resource. ttl 0 means no expiry.
// access is the label the caller claims for the grant ("mounted"; "" lets the
// daemon classify). It sends no rights: 0018 item 5 says to add them in the
// change that first enforces rights at a use site, and nothing here does.
func (b *Client) Mint(resource string, ttl time.Duration, access string) (Handle, error) {
	req := map[string]any{"resource": resource, "ttl_ms": ttl.Milliseconds()}
	if access != "" {
		req["access"] = access
	}
	raw, err := b.control("bus.mint@v1", req)
	if err != nil {
		return Handle{}, err
	}
	var r struct{ Handle, ID, Holder string }
	if err := json.Unmarshal(raw, &r); err != nil || r.Handle == "" || r.ID == "" {
		return Handle{}, fmt.Errorf("unreadable mint reply %q", raw)
	}
	b.mu.Lock()
	if b.ledger == nil {
		b.ledger = map[string]string{}
	}
	b.ledger[r.ID] = resource
	if b.self == "" {
		b.self = r.Holder // minter and first holder are the same identity
	}
	b.mu.Unlock()
	return Handle{r.Handle, r.ID}, nil
}

// Revoke kills a handle by id. Only the minter may.
func (b *Client) Revoke(id string) error {
	_, err := b.control("bus.revoke@v1", map[string]string{"id": id})
	return err
}

// Present has the daemon run its holder check on tokens: every control
// message is checked before routing, so a bus.ping carrying them is rejected
// with cap_unknown / cap_not_held / cap_revoked / cap_expired unless this
// connection's identity currently holds each one.
func (b *Client) Present(tokens []string) error {
	ctx, cancel := b.ctx()
	defer cancel()
	_, err := b.C.Call(ctx, proto.Envelope{To: proto.BusTarget, Verb: "bus.ping@v1", Caps: tokens})
	return err
}

// Publish sends an event carrying tokens. Delivery to subscribers includes
// them, but they are useless to anyone but the holder; the daemon logs only
// their ids, so the bus log independently names the handles.
func (b *Client) Publish(topic string, payload []byte, tokens []string) error {
	return b.C.Send(proto.Envelope{Kind: proto.KindEvent, Topic: topic, Payload: payload, QoS: proto.QoSInitiated, Caps: tokens})
}

// Epoch returns the id of the epoch handle, minting it on first use. The
// daemon keeps handle rows (revoked ones too) until it restarts, so a record
// that names an epoch the daemon still knows was written in this daemon's
// current life.
func (b *Client) Epoch() (string, error) {
	b.mu.Lock()
	id := b.epoch.ID
	b.mu.Unlock()
	if id != "" {
		return id, nil
	}
	h, err := b.Mint(EpochResource, 0, "")
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.epoch.ID == "" {
		b.epoch = h
	} else {
		go b.Revoke(h.ID) // lost a race; do not leave a second one outstanding
	}
	return b.epoch.ID, nil
}

// Close revokes the epoch handle. The daemon still remembers the row.
func (b *Client) Close() {
	b.mu.Lock()
	id := b.epoch.ID
	b.mu.Unlock()
	if id != "" {
		b.Revoke(id)
	}
}

// Code extracts the daemon's error code from err ("" if it is not one).
func Code(err error) string {
	var re *client.RemoteError
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}
