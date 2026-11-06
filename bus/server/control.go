package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/proto"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// control handles requests addressed to the daemon itself. They are not
// logged. Callers hold s.mu.
func (s *Server) control(c *conn, e proto.Envelope, clientID string) {
	var out any
	var err error
	switch e.Verb {
	case "bus.ping@v1":
		out = map[string]string{"pong": s.clock.Now().String()}
	case "bus.register@v1":
		out, err = s.ctlRegister(c, e.Payload)
	case "bus.subscribe@v1":
		out, err = s.ctlSubscribe(c, e.Payload, true)
	case "bus.unsubscribe@v1":
		out, err = s.ctlSubscribe(c, e.Payload, false)
	case "bus.inspect@v1":
		out = s.inspect()
	case "bus.log@v1":
		out, err = s.ctlLog(e.Payload)
	default:
		err = fmt.Errorf("unknown control verb %q", e.Verb)
	}
	if err != nil {
		s.sendError(c, clientID, "control_failed", err.Error())
		return
	}
	payload, _ := json.Marshal(out)
	s.enqueue(c, proto.Envelope{
		ID: s.newID(), TS: s.clock.Now().String(), From: "bus", To: c.domain,
		Kind: proto.KindResponse, Verb: e.Verb, QoS: proto.QoSInteractive, Corr: clientID, Payload: payload,
	})
}

func (s *Server) ctlRegister(c *conn, raw json.RawMessage) (any, error) {
	var p struct{ Name string }
	if err := json.Unmarshal(raw, &p); err != nil || !nameRE.MatchString(p.Name) || p.Name == proto.BusTarget {
		return nil, fmt.Errorf("register needs {\"name\":\"...\"} using [A-Za-z0-9_.-], not %q", proto.BusTarget)
	}
	if owner := s.services[p.Name]; owner != nil {
		return nil, fmt.Errorf("service %q is already registered by %s", p.Name, owner.domain)
	}
	s.services[p.Name] = c
	c.services = append(c.services, p.Name)
	return map[string]string{"registered": p.Name, "domain": c.domain}, nil
}

func (s *Server) ctlSubscribe(c *conn, raw json.RawMessage, add bool) (any, error) {
	var p struct{ Topic string }
	if err := json.Unmarshal(raw, &p); err != nil || p.Topic == "" {
		return nil, fmt.Errorf("needs {\"topic\":\"glob\"}")
	}
	if add {
		c.subs = append(c.subs, p.Topic)
		return map[string]string{"subscribed": p.Topic}, nil
	}
	for i, t := range c.subs {
		if t == p.Topic {
			c.subs = append(c.subs[:i], c.subs[i+1:]...)
			break
		}
	}
	return map[string]string{"unsubscribed": p.Topic}, nil
}

func (s *Server) ctlLog(raw json.RawMessage) (any, error) {
	var p struct {
		Since string `json:"since"`
		Topic string `json:"topic"`
		Limit int    `json:"limit"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	q := buslog.Query{Topic: p.Topic, Limit: p.Limit}
	if p.Since != "" {
		d, err := time.ParseDuration(p.Since)
		if err != nil {
			return nil, fmt.Errorf("bad since: %w", err)
		}
		q.SinceWall = time.Now().Add(-d).UnixNano()
	}
	msgs, err := s.log.Query(q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"messages": msgs}, nil
}

// InspectClient, InspectService and InspectSub are the bus.inspect@v1 reply.
type InspectClient struct {
	ID            int64     `json:"id"`
	Domain        string    `json:"domain"`
	PID           int32     `json:"pid"`
	UID           uint32    `json:"uid"`
	Connected     time.Time `json:"connected"`
	Services      []string  `json:"services"`
	Subscriptions []string  `json:"subscriptions"`
	// Outbox is the per-class backlog, indexed by QoS rank (0 user-interactive
	// ... 3 background). A growing OldestAgeMS in a low class beside traffic
	// in a higher one is starvation (no aging; see README).
	Outbox  [proto.NumQoS]ClassStats `json:"outbox"`
	Dropped int                      `json:"dropped"`
}
type InspectService struct {
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Client int64  `json:"client"`
}
type InspectSub struct {
	Topic  string `json:"topic"`
	Domain string `json:"domain"`
	Client int64  `json:"client"`
}
type InspectReply struct {
	Clients       []InspectClient  `json:"clients"`
	Services      []InspectService `json:"services"`
	Subscriptions []InspectSub     `json:"subscriptions"`
	Clock         string           `json:"clock"`
}

func (s *Server) inspect() InspectReply {
	r := InspectReply{Clients: []InspectClient{}, Services: []InspectService{}, Subscriptions: []InspectSub{}, Clock: s.clock.Now().String()}
	for _, c := range s.conns {
		cs, dropped := c.out.stats()
		r.Clients = append(r.Clients, InspectClient{
			Outbox: cs, Dropped: dropped,
			ID: c.id, Domain: c.domain, PID: c.cred.PID, UID: c.cred.UID, Connected: c.since,
			Services: append([]string{}, c.services...), Subscriptions: append([]string{}, c.subs...),
		})
		for _, t := range c.subs {
			r.Subscriptions = append(r.Subscriptions, InspectSub{t, c.domain, c.id})
		}
	}
	for n, c := range s.services {
		r.Services = append(r.Services, InspectService{n, c.domain, c.id})
	}
	sort.Slice(r.Clients, func(i, j int) bool { return r.Clients[i].ID < r.Clients[j].ID })
	sort.Slice(r.Services, func(i, j int) bool { return r.Services[i].Name < r.Services[j].Name })
	sort.Slice(r.Subscriptions, func(i, j int) bool {
		a, b := r.Subscriptions[i], r.Subscriptions[j]
		return a.Client < b.Client || (a.Client == b.Client && a.Topic < b.Topic)
	})
	return r
}
