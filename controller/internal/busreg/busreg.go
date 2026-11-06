// Package busreg registers domains with busd so that connections arriving on a
// domain's socket are attributed to that domain.
package busreg

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/antiartificial/aurora/bus/client"
)

// Client registers domains over a session-socket connection (the only place
// busd accepts bus.domain.add@v1 and bus.domain.remove@v1 from).
type Client struct {
	C       *client.Client
	Timeout time.Duration // default 5s
}

func (b Client) call(verb string, identity, path, cgroup string) error {
	t := b.Timeout
	if t == 0 {
		t = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()
	_, err := b.C.Control(ctx, verb, map[string]string{"identity": identity, "path": path, "cgroup": cgroup})
	return err
}

// AddDomain registers identity at the socket path with the domain's cgroup
// (`domain cgroup <name>`), which is what lets busd attest connections to it
// and lets the compositor verify the domain. An empty cgroup registers it
// unattestable. A domain busd already has
// under that identity counts as success (a controller restart, or a second
// controller); every other refusal is returned.
func (b Client) AddDomain(identity, path, cgroup string) error {
	err := b.call("bus.domain.add@v1", identity, path, cgroup)
	var re *client.RemoteError
	if errors.As(err, &re) {
		// With a cgroup, re-adding the same domain is reported by busd as an
		// overlap with itself rather than as "already registered".
		if strings.Contains(re.Message, "is already registered") || strings.Contains(re.Message, " of "+identity+":") {
			return nil
		}
	}
	return err
}

// RemoveDomain unregisters identity. A domain busd does not know is success.
func (b Client) RemoveDomain(identity string) error {
	err := b.call("bus.domain.remove@v1", identity, "", "")
	var re *client.RemoteError
	if errors.As(err, &re) && strings.Contains(re.Message, "is not registered") {
		return nil
	}
	return err
}

// Log reads the bus log (bus.log@v1) for one topic since a duration like "2m".
// The reply is busd's own: each message's `from` was stamped by the daemon.
func (b Client) Log(topic, since string, limit int) (json.RawMessage, error) {
	t := b.Timeout
	if t == 0 {
		t = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()
	return b.C.Control(ctx, "bus.log@v1", map[string]any{"topic": topic, "since": since, "limit": limit})
}
