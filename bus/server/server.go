// Package server is the busd daemon: one broker, one connection per client.
package server

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/hlc"
	"github.com/antiartificial/aurora/bus/proto"
)

const maxLine = 4 << 20

// Config configures a daemon.
type Config struct {
	SocketPath   string
	DataDir      string
	SegmentBytes int64
	// AllowReplay lets clients attach an "origin" and cap_ids to messages so
	// a recorded session can be re-delivered. Sender identity is STILL taken
	// from peer credentials; origin is informational. For fresh daemons only.
	AllowReplay bool
	// Resolver maps peer credentials to a domain name (default DefaultDomain).
	Resolver func(Cred) string
	Logf     func(format string, args ...any)
}

type conn struct {
	id       int64
	nc       net.Conn
	cred     Cred
	domain   string
	since    time.Time
	out      *outbox
	subs     []string // guarded by Server.mu
	services []string // guarded by Server.mu
}

type pendingReq struct {
	requester *conn
	clientID  string
	service   *conn
}

// Server is a running daemon.
type Server struct {
	cfg   Config
	ln    net.Listener
	log   *buslog.Log
	clock *hlc.Clock
	nonce string
	seq   atomic.Uint64

	// mu serialises routing so log order equals delivery order.
	mu       sync.Mutex
	conns    map[int64]*conn
	services map[string]*conn
	pending  map[string]pendingReq
	nextConn int64
	closed   bool
	wg       sync.WaitGroup
}

// New opens the log and starts listening. Call Serve to accept clients.
func New(cfg Config) (*Server, error) {
	if cfg.Resolver == nil {
		cfg.Resolver = DefaultDomain
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	lg, err := buslog.Open(cfg.DataDir, cfg.SegmentBytes)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	ln, err := listen(cfg.SocketPath)
	if err != nil {
		lg.Close()
		return nil, err
	}
	var nb [4]byte
	rand.Read(nb[:])
	return &Server{
		cfg: cfg, ln: ln, log: lg, clock: hlc.New(), nonce: hex.EncodeToString(nb[:]),
		conns: map[int64]*conn{}, services: map[string]*conn{}, pending: map[string]pendingReq{},
	}, nil
}

func listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("another busd is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve accepts clients until Close is called.
func (s *Server) Serve() error {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			return err
		}
		cred, err := peerCred(nc)
		if err != nil {
			s.cfg.Logf("reject connection: %v", err)
			nc.Close()
			continue
		}
		s.mu.Lock()
		s.nextConn++
		c := &conn{id: s.nextConn, nc: nc, cred: cred, domain: s.cfg.Resolver(cred), since: time.Now(), out: newOutbox()}
		s.conns[c.id] = c
		s.wg.Add(2)
		s.mu.Unlock()
		s.cfg.Logf("client %d connected: %s (pid %d)", c.id, c.domain, cred.PID)
		go s.writeLoop(c)
		go s.readLoop(c)
	}
}

// Close stops the daemon, disconnects clients and removes the socket.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]*conn, 0, len(s.conns))
	for _, c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	s.ln.Close()
	for _, c := range conns {
		c.out.close()
		c.nc.Close()
	}
	s.wg.Wait()
	os.Remove(s.cfg.SocketPath)
	return s.log.Close()
}

func (s *Server) writeLoop(c *conn) {
	defer s.wg.Done()
	for {
		line, ok := c.out.pop()
		if !ok {
			return
		}
		if _, err := c.nc.Write(line); err != nil {
			c.nc.Close()
			c.out.close()
			return
		}
	}
}

func (s *Server) readLoop(c *conn) {
	defer s.wg.Done()
	defer s.drop(c)
	sc := bufio.NewScanner(c.nc)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		var e proto.Envelope
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			s.mu.Lock()
			s.sendError(c, "", "bad_json", err.Error())
			s.mu.Unlock()
			continue
		}
		s.dispatch(c, e)
	}
}

// drop removes a disconnected client and fails requests it was serving.
func (s *Server) drop(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c.id)
	for _, name := range c.services {
		if s.services[name] == c {
			delete(s.services, name)
		}
	}
	for id, p := range s.pending {
		switch {
		case p.requester == c:
			delete(s.pending, id)
		case p.service == c:
			delete(s.pending, id)
			s.sendError(p.requester, p.clientID, "service_gone", "service disconnected before replying")
		}
	}
	c.out.close()
	c.nc.Close()
	s.cfg.Logf("client %d disconnected", c.id)
}

func (s *Server) newID() string { return fmt.Sprintf("%s-%d", s.nonce, s.seq.Add(1)) }

// enqueue marshals and queues an envelope for a client. Callers hold s.mu.
func (s *Server) enqueue(c *conn, e proto.Envelope) {
	b, err := json.Marshal(e)
	if err != nil {
		s.cfg.Logf("marshal: %v", err)
		return
	}
	c.out.push(e.QoS.Rank(), append(b, '\n'))
}

// sendError replies with an error envelope from the daemon. Callers hold s.mu.
func (s *Server) sendError(c *conn, corr, code, msg string) {
	payload, _ := json.Marshal(map[string]string{"code": code, "message": msg})
	s.enqueue(c, proto.Envelope{
		ID: s.newID(), TS: s.clock.Now().String(), From: "bus", To: c.domain,
		Kind: proto.KindError, QoS: proto.QoSInteractive, Corr: corr, Payload: payload,
	})
}

func (s *Server) record(e proto.Envelope) {
	if err := s.log.Append(e); err != nil {
		s.cfg.Logf("log append: %v", err)
	}
}

// dispatch stamps, validates, logs and routes one client message.
func (s *Server) dispatch(c *conn, e proto.Envelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clientID := e.ID
	replay := s.cfg.AllowReplay && e.Origin != nil

	// Hard rule (0001): identity comes from SO_PEERCRED, never the message.
	e.From = c.domain
	e.ID = s.newID()
	e.TS = s.clock.Now().String()
	if !replay {
		e.Origin = nil
		e.CapIDs = nil
	}
	if len(e.Caps) > 0 {
		e.CapIDs = buslog.LogForm(e).CapIDs
	}
	q, err := proto.ParseQoS(string(e.QoS))
	if err != nil {
		s.sendError(c, clientID, "bad_qos", err.Error())
		return
	}
	e.QoS = q
	if err := e.Validate(); err != nil {
		s.sendError(c, clientID, "bad_message", err.Error())
		return
	}

	if e.Kind == proto.KindRequest && e.To == proto.BusTarget {
		s.control(c, e, clientID)
		return
	}
	switch e.Kind {
	case proto.KindRequest:
		svc := s.services[e.To]
		if svc == nil {
			if replay {
				s.record(e)
				return
			}
			s.sendError(c, clientID, "no_such_service", fmt.Sprintf("no service %q is registered", e.To))
			return
		}
		s.record(e)
		if !replay {
			s.pending[e.ID] = pendingReq{requester: c, clientID: clientID, service: svc}
		}
		s.enqueue(svc, e)
	case proto.KindResponse, proto.KindError:
		if replay {
			s.record(e)
			return
		}
		p, ok := s.pending[e.Corr]
		if !ok || p.service != c {
			s.sendError(c, clientID, "unknown_corr", fmt.Sprintf("no pending request %q for you to answer", e.Corr))
			return
		}
		delete(s.pending, e.Corr)
		e.To = p.requester.domain
		s.record(e) // the log keeps the daemon's ids
		e.Corr = p.clientID
		s.enqueue(p.requester, e)
	case proto.KindEvent:
		s.record(e)
		for _, sub := range s.conns {
			for _, pat := range sub.subs {
				if proto.Match(pat, e.Topic) {
					s.enqueue(sub, e)
					break
				}
			}
		}
	}
}
