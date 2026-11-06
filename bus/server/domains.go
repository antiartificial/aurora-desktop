package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/antiartificial/aurora/bus/proto"
)

// identityRE is the shape of `domain exec-id`: domain:<name>:<hex id>. The
// session socket's uid:comm identities cannot match it, so a domain socket
// can never be registered under a name a session client already has.
var identityRE = regexp.MustCompile(`^domain:[A-Za-z0-9_.-]+:[0-9a-f]{8,64}$`)

type domainSock struct {
	identity, path string
	cgroup         string // "" = none registered, connections cannot be attested
	ln             net.Listener
	nsOn           bool // namespace attestation enabled (see ns.go)
	uidOn          bool // the peer's SO_PEERCRED uid must equal uid
	uid            uint32
	nsMu           sync.Mutex
	runs           []*nsRun // live sandbox runs the daemon was told of
}

// AddDomain makes the daemon also listen on path. Every connection accepted
// there is attributed to identity, a fact about WHICH SOCKET it came in on,
// not about anything the client sends. SO_PEERCRED is still read (uid, pid).
// With no cgroup the domain cannot be attested (see AddDomainCgroup).
func (s *Server) AddDomain(identity, path string) error {
	return s.AddDomainCgroup(identity, path, "")
}

// AddDomainCgroup is AddDomain plus the cgroup the domain's processes live in
// (`domain cgroup <name>`, in /proc/<pid>/cgroup form). Unless the daemon's
// AttestDomains is off, a connection is accepted only if the kernel says its
// peer process is in that cgroup or below it.
func (s *Server) AddDomainCgroup(identity, path, cgroup string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addDomainLocked(identity, path, cgroup, false)
}

func (s *Server) addDomainLocked(identity, path, cgroup string, ns bool) error {
	if !identityRE.MatchString(identity) {
		return fmt.Errorf("identity %q must look like domain:<name>:<hex id> (see `domain exec-id`)", identity)
	}
	if path == s.cfg.SocketPath {
		return errors.New("a domain socket cannot be the session socket")
	}
	if s.closed {
		return errors.New("daemon is closed")
	}
	if cgroup != "" {
		if err := checkCgroup(cgroup); err != nil {
			return err
		}
		for _, d := range s.doms {
			if d.cgroup != "" && (inCgroup(d.cgroup, cgroup) || inCgroup(cgroup, d.cgroup)) {
				return fmt.Errorf("cgroup %s overlaps %s of %s: one process would be both", cgroup, d.cgroup, d.identity)
			}
		}
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
	d := &domainSock{identity: identity, path: path, cgroup: cgroup, ln: ln, nsOn: ns}
	s.doms[identity] = d
	switch {
	case cgroup != "" && ns && s.cfg.AttestDomains != AttestOff:
		s.cfg.Logf("domain %s: connections must come from cgroup %s AND from the namespaces of a registered live run (attest mode %s)", identity, cgroup, s.cfg.AttestDomains)
	case cgroup != "" && s.cfg.AttestDomains != AttestOff:
		s.cfg.Logf("domain %s: connections must come from cgroup %s (attest mode %s); NO namespace ids registered, a same-uid process that joins the cgroup would pass", identity, cgroup, s.cfg.AttestDomains)
	case s.cfg.AttestDomains == AttestOff:
		s.cfg.Logf("WARNING: domain %s: attestation is OFF, any same-uid process that opens %s is this domain", identity, path)
	case s.cfg.AttestDomains == AttestRequire:
		s.cfg.Logf("WARNING: domain %s has no cgroup registered and mode is require: every connection will be refused", identity)
	default:
		s.cfg.Logf("WARNING: domain %s has no cgroup registered: its connections are UNATTESTED (mode prefer)", identity)
	}
	go s.acceptLoop(ln, identity, d)
	return nil
}

// SetDomainUID makes the domain require that a connecting peer's SO_PEERCRED
// uid is uid (`domain uid <name>`: the subordinate uid its processes run as).
// It is one more signal alongside cgroup and namespaces and is checked first.
func (s *Server) SetDomainUID(identity string, uid uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setDomainUIDLocked(identity, uid)
}

func (s *Server) setDomainUIDLocked(identity string, uid uint32) error {
	d := s.doms[identity]
	if d == nil {
		return fmt.Errorf("domain %s is not registered", identity)
	}
	for _, o := range s.doms {
		if o != d && o.uidOn && o.uid == uid {
			return fmt.Errorf("uid %d already belongs to %s: two domains cannot share a uid", uid, o.identity)
		}
	}
	d.uid, d.uidOn = uid, true
	s.cfg.Logf("domain %s: connections must come from uid %d", identity, uid)
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
func (s *Server) acceptLoop(ln net.Listener, identity string, d *domainSock) error {
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
		var att *attestState
		if identity != "" {
			if att, err = s.admitDomain(nc, cred, d); err != nil {
				s.cfg.Logf("%v", err)
				refuse(nc, err)
				continue
			}
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			att.close()
			nc.Close()
			return net.ErrClosed
		}
		name := identity
		if identity == "" {
			name = s.cfg.Resolver(cred)
		}
		s.nextConn++
		c := &conn{id: s.nextConn, nc: nc, cred: cred, domain: name, viaDomain: identity != "", since: time.Now(), out: newOutbox(), att: att}
		s.conns[c.id] = c
		s.wg.Add(2)
		s.mu.Unlock()
		s.cfg.Logf("client %d connected: %s (pid %d)", c.id, c.domain, cred.PID)
		go s.writeLoop(c)
		go s.readLoop(c)
	}
}

// refuse tells the peer why, best effort, then hangs up. The reason names no
// cgroup the peer could not already read about itself.
func refuse(nc net.Conn, why error) {
	pl, _ := json.Marshal(map[string]string{"code": "attestation_failed", "message": why.Error()})
	line, _ := json.Marshal(proto.Envelope{From: "bus", Kind: proto.KindError, Payload: pl})
	nc.SetWriteDeadline(time.Now().Add(time.Second))
	nc.Write(append(line, '\n'))
	nc.Close()
}

// checkCgroup rejects cgroup paths that would match too much or are not in
// the /proc/<pid>/cgroup form ("/" would match every process on the host).
func checkCgroup(cg string) error {
	if !strings.HasPrefix(cg, "/") || cg == "/" || path.Clean(cg) != cg {
		return fmt.Errorf("cgroup %q must be a clean absolute path below the root, as in /proc/<pid>/cgroup", cg)
	}
	return nil
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
	Identity string  `json:"identity"`
	Socket   string  `json:"socket"`
	Cgroup   string  `json:"cgroup,omitempty"`
	NS       bool    `json:"ns,omitempty"`   // namespace attestation on
	UID      *uint32 `json:"uid,omitempty"`  // required peer uid, if registered
	RunPIDs  []int32 `json:"runs,omitempty"` // live registered sandbox runs (init pids)
}

func (s *Server) inspectDomains() []InspectDomain {
	out := []InspectDomain{}
	for _, d := range s.doms {
		id := InspectDomain{Identity: d.identity, Socket: d.path, Cgroup: d.cgroup, NS: d.nsOn, RunPIDs: d.liveRunPIDs()}
		if d.uidOn {
			u := d.uid
			id.UID = &u
		}
		out = append(out, id)
	}
	return out
}
