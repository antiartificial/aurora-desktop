package server

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"
)

// identityRE is the shape of `domain exec-id`: domain:<name>:<hex id>. The
// session socket's uid:comm identities cannot match it, so a domain socket
// can never be registered under a name a session client already has.
var identityRE = regexp.MustCompile(`^domain:[A-Za-z0-9_.-]+:[0-9a-f]{8,64}$`)

type domainSock struct {
	identity, path string
	ln             net.Listener
}

// AddDomain makes the daemon also listen on path. Every connection accepted
// there is attributed to identity, a fact about WHICH SOCKET it came in on,
// not about anything the client sends. SO_PEERCRED is still read (uid, pid).
func (s *Server) AddDomain(identity, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addDomainLocked(identity, path)
}

func (s *Server) addDomainLocked(identity, path string) error {
	if !identityRE.MatchString(identity) {
		return fmt.Errorf("identity %q must look like domain:<name>:<hex id> (see `domain exec-id`)", identity)
	}
	if path == s.cfg.SocketPath {
		return errors.New("a domain socket cannot be the session socket")
	}
	if s.closed {
		return errors.New("daemon is closed")
	}
	if s.doms[identity] != nil {
		return fmt.Errorf("domain %s is already registered", identity)
	}
	for _, d := range s.doms {
		if d.path == path {
			return fmt.Errorf("socket %s already belongs to %s", path, d.identity)
		}
	}
	ln, err := listen(path)
	if err != nil {
		return err
	}
	s.doms[identity] = &domainSock{identity, path, ln}
	go s.acceptLoop(ln, identity)
	return nil
}

// RemoveDomain stops listening for a domain, disconnects its clients and
// drops the handles it holds. Handles it minted but passed on stay valid.
func (s *Server) RemoveDomain(identity string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeDomainLocked(identity)
}

func (s *Server) removeDomainLocked(identity string) error {
	d := s.doms[identity]
	if d == nil {
		return fmt.Errorf("domain %s is not registered", identity)
	}
	delete(s.doms, identity)
	var victims []*conn
	for _, c := range s.conns {
		if c.viaDomain && c.domain == identity {
			victims = append(victims, c)
		}
	}
	for tok, h := range s.caps {
		if h.holder == identity {
			delete(s.caps, tok)
		}
	}
	d.ln.Close()
	for _, c := range victims {
		c.nc.Close()
	}
	return nil
}

// acceptLoop serves one listener. identity "" is the session socket, whose
// clients are named by Config.Resolver; otherwise by the listener's domain.
func (s *Server) acceptLoop(ln net.Listener, identity string) error {
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		cred, err := peerCred(nc)
		if err != nil {
			s.cfg.Logf("reject connection: %v", err)
			nc.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			nc.Close()
			return net.ErrClosed
		}
		name := identity
		if identity == "" {
			name = s.cfg.Resolver(cred)
		}
		s.nextConn++
		c := &conn{id: s.nextConn, nc: nc, cred: cred, domain: name, viaDomain: identity != "", since: time.Now(), out: newOutbox()}
		s.conns[c.id] = c
		s.wg.Add(2)
		s.mu.Unlock()
		s.cfg.Logf("client %d connected: %s (pid %d)", c.id, c.domain, cred.PID)
		go s.writeLoop(c)
		go s.readLoop(c)
	}
}

func (s *Server) closeDomains() {
	s.mu.Lock()
	ds := s.doms
	s.doms = map[string]*domainSock{}
	s.mu.Unlock()
	for _, d := range ds {
		d.ln.Close()
	}
}

// InspectDomain is a registered domain socket in bus.inspect@v1.
type InspectDomain struct {
	Identity string `json:"identity"`
	Socket   string `json:"socket"`
}

func (s *Server) inspectDomains() []InspectDomain {
	out := []InspectDomain{}
	for _, d := range s.doms {
		out = append(out, InspectDomain{d.identity, d.path})
	}
	return out
}
