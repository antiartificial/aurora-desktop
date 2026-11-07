package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/server"
)

// What "healthy" means, per Health.Type (see spec.go): never just "the pid
// exists". A process that is up but not on the bus, registered by someone else,
// or registered but silent is reported unhealthy with the reason.
//
// The bus-registration half is checked every tick from bus.inspect@v1 (control
// traffic, not written to the bus log). The "answers a request" half sends the
// service a request, and requests ARE logged, so it runs every pingEvery once a
// service has answered, and every tick while it has not. The probe verb is
// supervisor.ping@v1; services answer unknown verbs with a typed error and that
// counts as answering: it proves the request loop is alive, which is the point.
const (
	pingEvery   = 10 * time.Second
	pingTimeout = 2 * time.Second
	// A healthy service is pinged less and less often, up to this.
	pingMax = 5 * time.Minute
)

// pingInterval is how long to wait before pinging again after `streak`
// consecutive answers: the floor, doubled per answer, capped.
//
// Why back off at all: a ping is a request, and requests are written to the
// append-only, hash-chained log. At a flat 10s, a measurement of a running
// desktop found 1463 of 2642 records in that log were pings -- 55% of the
// thing `bus verify` chains and audit tools read was liveness noise nobody
// subscribed to.
//
// Why not exempt pings from the log instead: a message class that skips the
// audit log is precisely what an attacker would want to send. The log's worth
// comes from having no exceptions.
//
// Backing off costs detection latency for a service that has been answering
// for a long time, which is the case where something going wrong is least
// likely and a late notice matters least. Any failure, restart or pid change
// resets the streak, so a flapping service is still probed at the floor.
func pingInterval(streak int) time.Duration {
	d := pingEvery
	for i := 0; i < streak && d < pingMax; i++ {
		d *= 2
	}
	if d > pingMax {
		d = pingMax
	}
	return d
}

type pingState struct {
	pid int
	ok  bool
	// Consecutive answers; drives the backoff above.
	streak int
	last   time.Time
}

type probeInput struct {
	v   *svc
	pid int
}

type verdict struct {
	ok     bool
	reason string
}

func (s *Supervisor) probeAll(in []probeInput, pings map[string]*pingState) map[string]verdict {
	out := map[string]verdict{}
	needBus := false
	for _, p := range in {
		if p.v.spec.Health.Type != "none" {
			needBus = true
		}
	}
	if !needBus {
		return out
	}
	var cli *client.Client
	var busErr error
	cli, busErr = s.bus.Client()
	var ins *server.InspectReply
	if busErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
		raw, err := cli.Control(ctx, "bus.inspect@v1", nil)
		cancel()
		if err != nil {
			busErr = fmt.Errorf("bus.inspect: %v", err)
		} else {
			ins = &server.InspectReply{}
			if err := json.Unmarshal(raw, ins); err != nil {
				busErr, ins = fmt.Errorf("bus.inspect: malformed reply"), nil
			}
		}
	}
	for _, p := range in {
		switch p.v.spec.Health.Type {
		case "bus":
			out[p.v.spec.Name] = s.probeBus(p.pid, cli, busErr)
		case "service":
			if busErr != nil {
				out[p.v.spec.Name] = verdict{false, "bus unavailable: " + Clean(busErr.Error())}
				continue
			}
			out[p.v.spec.Name] = probeService(p, cli, ins, pings)
		}
	}
	return out
}

func (s *Supervisor) probeBus(pid int, cli *client.Client, busErr error) verdict {
	got, err := peerPID(s.cfg.BusSocket)
	if err != nil {
		return verdict{false, "bus socket not accepting: " + Clean(err.Error())}
	}
	if got != pid {
		// busd only warns when it cannot bind and keeps running; the socket then
		// belongs to somebody else's daemon and this one is useless.
		return verdict{false, fmt.Sprintf("socket is served by pid %d, not the supervised pid %d", got, pid)}
	}
	if busErr != nil {
		return verdict{false, "bus not answering: " + Clean(busErr.Error())}
	}
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if _, err := cli.Control(ctx, "bus.ping@v1", nil); err != nil {
		return verdict{false, "bus.ping@v1 failed: " + Clean(err.Error())}
	}
	return verdict{true, "bus.ping@v1 answered by the supervised pid"}
}

func probeService(p probeInput, cli *client.Client, ins *server.InspectReply, pings map[string]*pingState) verdict {
	name := p.v.spec.Health.Name
	var cid int64 = -1
	for _, sv := range ins.Services {
		if sv.Name == name {
			cid = sv.Client
		}
	}
	if cid < 0 {
		return verdict{false, fmt.Sprintf("not registered on the bus as %q", Clean(name))}
	}
	regPID := int32(-1)
	var lastHeard time.Time
	for _, c := range ins.Clients {
		if c.ID == cid {
			regPID, lastHeard = c.PID, c.LastRecv
		}
	}
	if int(regPID) != p.pid {
		return verdict{false, fmt.Sprintf("%q is registered by pid %d, not the supervised pid %d", Clean(name), regPID, p.pid)}
	}
	ps := pings[p.v.spec.Name]
	if ps == nil || ps.pid != p.pid {
		ps = &pingState{pid: p.pid}
		pings[p.v.spec.Name] = ps
	}
	if ps.ok && time.Since(ps.last) < pingInterval(ps.streak) {
		return verdict{true, "registered by the supervised pid; last answered a request " + time.Since(ps.last).Truncate(time.Second).String() + " ago"}
	}
	// A service that has been handling real traffic has already proved its
	// request loop is not wedged -- that is the only thing the ping exists to
	// show. So observe first and manufacture nothing: a working system never
	// pings at all, and only silence produces a synthetic request.
	//
	// The evidence is the broker's own record of when it last read a frame
	// from the service (conn.lastRecvNS), fetched over bus.inspect@v1, which
	// is control traffic and is NOT written to the append-only log. A wedged
	// loop sends nothing, so recent activity cannot be forged by being stuck.
	if !lastHeard.IsZero() && time.Since(lastHeard) < pingEvery {
		ps.ok, ps.last = true, lastHeard
		return verdict{true, "registered by the supervised pid; the bus saw it send " + time.Since(lastHeard).Truncate(time.Second).String() + " ago (observed, not probed)"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	_, err := cli.Call(ctx, proto.Envelope{To: name, Verb: "supervisor.ping@v1", QoS: proto.QoSBackground})
	var re *client.RemoteError
	if err != nil && !errors.As(err, &re) {
		ps.ok, ps.streak = false, 0
		return verdict{false, fmt.Sprintf("registered as %q but not answering requests (%s)", Clean(name), Clean(err.Error()))}
	}
	ps.ok, ps.last, ps.streak = true, time.Now(), ps.streak+1
	return verdict{true, "registered by the supervised pid and answering requests"}
}

func listForeignClients(cli *client.Client, mine map[int]bool) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	raw, err := cli.Control(ctx, "bus.inspect@v1", nil)
	if err != nil {
		return nil, err
	}
	var ins server.InspectReply
	if err := json.Unmarshal(raw, &ins); err != nil {
		return nil, err
	}
	var out []string
	for _, c := range ins.Clients {
		if !mine[int(c.PID)] {
			d := Clean(c.Domain)
			if d == "" {
				d = "session"
			}
			out = append(out, fmt.Sprintf("pid %d (%s)", c.PID, d))
		}
	}
	return out, nil
}
