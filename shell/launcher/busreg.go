package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

// DefaultSocket mirrors busd's own default.
func DefaultSocket() string {
	if s := os.Getenv("AURORA_BUS_SOCK"); s != "" {
		return s
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = filepath.Join(os.TempDir(), fmt.Sprintf("aurora-%d", os.Getuid()))
	}
	return filepath.Join(rt, "aurora", "bus.sock")
}

type busBackend struct {
	socket  string
	timeout time.Duration
	mu      sync.Mutex
	c       *client.Client
}

// DialBackend returns a Backend that talks to busd. It connects lazily, on
// the first call, so `aurora -h` and argument errors never need a bus.
func DialBackend(socket string, timeout time.Duration) Backend {
	return &busBackend{socket: socket, timeout: timeout}
}

func (b *busBackend) conn() (*client.Client, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.c != nil {
		return b.c, nil
	}
	c, err := client.Dial(b.socket)
	if err != nil {
		return nil, fmt.Errorf("%w (socket %s)", ErrBusDown, b.socket)
	}
	b.c = c
	return c, nil
}

func (b *busBackend) Close() error {
	if b.c != nil {
		return b.c.Close()
	}
	return nil
}

// call sends one request to the registry service and decodes the reply.
func (b *busBackend) call(ctx context.Context, verb string, req, resp any) error {
	c, err := b.conn()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	r, err := c.Call(ctx, proto.Envelope{To: "registry", Verb: verb, Payload: payload})
	if err != nil {
		var re *client.RemoteError
		errors.As(err, &re)
		if re != nil && re.Code == "no_such_service" {
			return ErrRegistryAbsent
		}
		if re != nil {
			// The registry answers failures as an error envelope whose payload
			// also carries {"error","detail"}; the client drops those fields.
			var body struct{ Error, Detail string }
			_ = json.Unmarshal(r.Payload, &body)
			if body.Error == "" {
				body.Error = re.Message
			}
			return &InvokeError{Verb: verb, Msg: re.Code + ": " + body.Error, Detail: body.Detail}
		}
		if errors.Is(err, client.ErrClosed) {
			return fmt.Errorf("%w (connection closed)", ErrBusDown)
		}
		return fmt.Errorf("%s: %w", verb, err)
	}
	if err := json.Unmarshal(r.Payload, resp); err != nil {
		return fmt.Errorf("%s: malformed reply: %w", verb, err)
	}
	return nil
}

func (b *busBackend) Apps(ctx context.Context) ([]App, error) {
	var out struct {
		Apps []App `json:"apps"`
	}
	err := b.call(ctx, "registry.apps@v1", struct{}{}, &out)
	return out.Apps, err
}

func (b *busBackend) Verbs(ctx context.Context, accepts string) ([]Verb, error) {
	req := map[string]string{}
	if accepts != "" {
		req["accepts"] = accepts
	}
	var out struct {
		Verbs []Verb `json:"verbs"`
	}
	err := b.call(ctx, "registry.verbs@v1", req, &out)
	return out.Verbs, err
}

func (b *busBackend) Describe(ctx context.Context, app, verb string) (Description, error) {
	var d Description
	err := b.call(ctx, "registry.describe@v1", map[string]string{"app": app, "verb": verb}, &d)
	return d, err
}

func (b *busBackend) Invoke(ctx context.Context, r InvokeRequest) (InvokeResult, error) {
	var out InvokeResult
	err := b.call(ctx, "registry.invoke@v1", r, &out)
	return out, err
}

func (b *busBackend) Watch(ctx context.Context, fn func(ProvEvent)) error {
	c, err := b.conn()
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, b.timeout)
	err = c.Subscribe(sctx, "provenance.created")
	cancel()
	if err != nil {
		return fmt.Errorf("subscribe provenance.created: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-c.Events():
			if !ok {
				return fmt.Errorf("%w (connection closed)", ErrBusDown)
			}
			fn(parseProv(e))
		}
	}
}

// parseProv reads a provenance.created payload. The registry protocol does
// not pin its shape, so this accepts: app, verb, inputs (array) or
// input (string), and output or file (the controller uses file), and a time under time/ts/created (RFC3339). The envelope's
// hybrid-clock timestamp is the fallback for the time.
func parseProv(e proto.Envelope) ProvEvent {
	var p struct {
		App    string   `json:"app"`
		Verb   string   `json:"verb"`
		Output string   `json:"output"`
		File   string   `json:"file"` // controller's provenance record names the output "file"
		Inputs []string `json:"inputs"`
		Input  string   `json:"input"`
		Time   string   `json:"time"`
		TS     string   `json:"ts"`
		At     string   `json:"created"`
	}
	_ = json.Unmarshal(e.Payload, &p)
	ev := ProvEvent{App: p.App, Verb: p.Verb, Output: p.Output, Inputs: p.Inputs}
	if ev.Output == "" {
		ev.Output = p.File
	}
	if len(ev.Inputs) == 0 && p.Input != "" {
		ev.Inputs = []string{p.Input}
	}
	for _, t := range []string{p.Time, p.TS, p.At, e.TS} {
		if t != "" {
			ev.Time = normTime(t)
			break
		}
	}
	return ev
}

// normTime turns a bus hybrid-clock stamp (19-digit wall ns, "-", counter)
// into RFC3339; anything else (already RFC3339, say) passes through.
func normTime(t string) string {
	if len(t) >= 19 {
		if ns, err := strconv.ParseInt(t[:19], 10, 64); err == nil {
			return time.Unix(0, ns).Format(time.RFC3339Nano)
		}
	}
	return t
}
