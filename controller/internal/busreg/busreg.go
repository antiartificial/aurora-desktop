// Package busreg registers domains with busd so that connections arriving on a
// domain's socket are attributed to that domain.
package busreg

import (
	"context"
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

func (b Client) call(verb string, identity, path string) error {
	t := b.Timeout
	if t == 0 {
		t = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()
	_, err := b.C.Control(ctx, verb, map[string]string{"identity": identity, "path": path})
	return err
}

// AddDomain registers identity at the socket path. A domain busd already has
// under that identity counts as success (a controller restart, or a second
// controller); every other refusal is returned.
func (b Client) AddDomain(identity, path string) error {
	err := b.call("bus.domain.add@v1", identity, path)
	var re *client.RemoteError
	if errors.As(err, &re) && strings.Contains(re.Message, "is already registered") {
		return nil
	}
	return err
}

// RemoveDomain unregisters identity. A domain busd does not know is success.
func (b Client) RemoveDomain(identity string) error {
	err := b.call("bus.domain.remove@v1", identity, "")
	var re *client.RemoteError
	if errors.As(err, &re) && strings.Contains(re.Message, "is not registered") {
		return nil
	}
	return err
}
