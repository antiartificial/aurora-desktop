// Package server is the busd daemon: one broker, one connection per client.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/hlc"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/wire"
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
	// EnforceCaps makes the daemon reject any message whose caps the sender's
	// domain does not hold in the minting table. busd turns it on by default.
	// Off, caps are the legacy opaque strings (kept for old tests/clients).
	EnforceCaps bool
	// Resolver maps peer credentials to a domain name (default DefaultDomain).
	Resolver func(Cred) string
	// MaxSegmentBytes caps one segment (default 256 MiB). The daemon hashes
	// every segment it accepts, so this bounds the work a sender can cause.
	MaxSegmentBytes int64
	// AttestDomains is the domain-socket attestation mode (zero: prefer).
	AttestDomains AttestMode
	Logf          func(format string, args ...any)
}

type conn struct {
	id        int64
	nc        net.Conn
	cred      Cred
	domain    string
	viaDomain bool // arrived on a per-domain socket (identity from the socket)
	since     time.Time
	out       *outbox
	att       *attestState // nil unless attested at accept
	subs      []string     // guarded by Server.mu
	services  []string     // guarded by Server.mu

	segsIn, segBytesIn int64 // accepted from this client; guarded by Server.mu
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
	caps     map[string]*handle     // token -> authoritative row
	doms     map[string]*domainSock // identity -> per-domain listener
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
	cfg.Logf("domain attestation mode: %s", cfg.AttestDomains)
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
		caps: map[string]*handle{}, doms: map[string]*domainSock{},
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

// Serve accepts clients on the session socket until Close is called.
func (s *Server) Serve() error {
	err := s.acceptLoop(s.ln, "", "")
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil
	}
	return err
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
	s.closeDomains()
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
		line, files, ok := c.out.popFiles()
		if !ok {
			return
		}
		err := wire.Write(c.nc.(*net.UnixConn), line, files)
		closeFiles(files) // the kernel holds its own reference once sent
		if err != nil {
			c.nc.Close()
			c.out.close()
			return
		}
	}
}

func (s *Server) readLoop(c *conn) {
	defer s.wg.Done()
	defer s.drop(c)
	rd := wire.NewReader(c.nc.(*net.UnixConn), maxLine)
	defer rd.Close()
	for {
		var e proto.Envelope
		var perr error
		_, files, err := rd.Next(func(line []byte) (int, error) {
			if perr = json.Unmarshal(line, &e); perr != nil {
				return 0, perr
			}
			if err := e.ValidateSegments(); err != nil {
				return 0, err
			}
			if s.isReplay(e) {
				return 0, nil // replayed descriptors have no contents
			}
			for i := range e.Segments {
				e.Segments[i].Absent = false // only the daemon sets this
			}
			return e.Present(), nil
		})
		var rej *wire.Reject
		switch {
		case errors.As(err, &rej):
			code := "bad_segments"
			if perr != nil {
				code = "bad_json"
			}
			s.mu.Lock()
			s.sendError(c, e.ID, code, rej.Error())
			s.mu.Unlock()
			continue
		case err != nil:
			if !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
				s.cfg.Logf("client %d: read: %v", c.id, err)
			}
			return
		}
		e.Attach = files
		if err := s.prepareSegments(&e); err != nil {
			s.mu.Lock()
			s.sendError(c, e.ID, "bad_segments", err.Error())
			s.mu.Unlock()
			e.CloseAttachments()
			continue
		}
		if !s.stillAttested(c) {
			e.CloseAttachments()
			return
		}
		s.dispatch(c, e)
		e.CloseAttachments() // enqueue gave every recipient its own dup
	}
}

// drop removes a disconnected client and fails requests it was serving.
func (s *Server) drop(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, c.id)
	c.att.close()
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
	files, err := dupFiles(e.Attach)
	if err != nil {
		s.cfg.Logf("dup segment fds for client %d: %v", c.id, err)
		return
	}
	var segBytes int64
	for _, sg := range e.Segments {
		if !sg.Absent {
			segBytes += sg.Bytes
		}
	}
	c.out.pushFiles(e.QoS.Rank(), append(b, '\n'), files, segBytes)
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
	replay := s.isReplay(e)
	c.segsIn += int64(len(e.Segments))
	for _, sg := range e.Segments {
		c.segBytesIn += sg.Bytes
	}

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

	if s.cfg.EnforceCaps && !replay {
		if err := s.checkCaps(c, e); err != nil {
			var ce *codedErr
			errors.As(err, &ce)
			s.sendError(c, clientID, ce.code, ce.msg)
			return
		}
	}

	if e.Kind == proto.KindRequest && e.To == proto.BusTarget {
		if len(e.Segments) > 0 {
			s.sendError(c, clientID, "bad_message", "control verbs take no segments")
			return
		}
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
