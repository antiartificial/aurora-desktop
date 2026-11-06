// Package replay re-delivers a recorded session and verifies it on
// invariants (0001): causal order, final state per subscriber, and the set of
// capability grants. It never compares bytes.
package replay

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/hlc"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/server"
)

// Deliver re-sends recorded messages to a daemon in their original order.
// speed > 0 paces delivery by the recorded timestamps divided by speed
// (each gap capped at one second); 0 means as fast as possible. Each message
// carries its original id, sender and timestamp as "origin"; a daemon not
// started with replay mode ignores that, and the sender is always the
// replaying client's own peer-credential identity.
func Deliver(ctx context.Context, c *client.Client, msgs []proto.Envelope, speed float64) error {
	var prev int64
	for _, m := range msgs {
		if speed > 0 {
			if ts, err := hlc.Parse(m.TS); err == nil {
				if prev > 0 && ts.Wall > prev {
					gap := time.Duration(float64(ts.Wall-prev) / speed)
					if gap > time.Second {
						gap = time.Second
					}
					select {
					case <-time.After(gap):
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				prev = ts.Wall
			}
		}
		out := proto.Envelope{
			ID: m.ID, To: m.To, Kind: m.Kind, Verb: m.Verb, Topic: m.Topic, QoS: m.QoS,
			Corr: m.Corr, Payload: m.Payload, CapIDs: m.CapIDs,
			// Descriptors and hashes only: segment CONTENTS are not in the log and
			// cannot be replayed. The daemon marks them absent.
			Segments: absent(m.Segments),
			Origin:   &proto.Origin{ID: m.ID, From: m.From, TS: m.TS},
		}
		if err := c.Send(out); err != nil {
			return err
		}
	}
	return c.Flush(ctx)
}

// Result is the outcome of a self-contained replay check.
type Result struct {
	Report    Report
	Delivered int
}

// RunCheck starts a fresh in-process daemon in replay mode, replays orig into
// it while a probe subscriber watches, and compares invariants.
func RunCheck(ctx context.Context, orig []proto.Envelope, speed float64) (Result, error) {
	tmp, err := os.MkdirTemp("", "busreplay")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)
	sock := filepath.Join(tmp, "bus.sock")
	dataDir := filepath.Join(tmp, "log")
	srv, err := server.New(server.Config{SocketPath: sock, DataDir: dataDir, AllowReplay: true})
	if err != nil {
		return Result{}, err
	}
	go srv.Serve()

	probe, err := client.Dial(sock)
	if err != nil {
		srv.Close()
		return Result{}, err
	}
	if err := probe.Subscribe(ctx, "**"); err != nil {
		srv.Close()
		return Result{}, err
	}
	rc, err := client.Dial(sock)
	if err != nil {
		srv.Close()
		return Result{}, err
	}
	if err := Deliver(ctx, rc, orig, speed); err != nil {
		srv.Close()
		return Result{}, err
	}

	// Collect what the probe subscriber saw. Wait for every expected event
	// (non-ephemeral events are the only ones in the original log).
	want := 0
	for _, m := range orig {
		if m.Kind == proto.KindEvent {
			want++
		}
	}
	var seen []proto.Envelope
	deadline := time.After(5 * time.Second)
	for len(seen) < want {
		select {
		case e, ok := <-probe.Events():
			if !ok {
				want = len(seen)
				break
			}
			seen = append(seen, e)
		case <-deadline:
			want = len(seen)
		}
	}
	rc.Close()
	probe.Close()
	if err := srv.Close(); err != nil {
		return Result{}, err
	}
	replayed, err := readOrEmpty(dataDir)
	if err != nil {
		return Result{}, err
	}
	return Result{Report: Check(orig, replayed, seen), Delivered: len(replayed)}, nil
}

func readOrEmpty(dir string) ([]proto.Envelope, error) {
	m, err := buslog.ReadFile(dir)
	if err != nil {
		if _, serr := os.Stat(dir); serr == nil {
			return nil, nil
		}
		return nil, err
	}
	return m, nil
}

// absent copies descriptors marked as having no contents attached.
func absent(in []proto.Segment) []proto.Segment {
	out := append([]proto.Segment(nil), in...)
	for i := range out {
		out[i].Absent = true
	}
	return out
}
