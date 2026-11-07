package invoke

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// DropServer is the socket the compositor reports file drops on (decision 0020). Line JSON, one request
// and one reply per connection:
//
//	{"v":1,"op":"drop","domain":"win-text-1a2b3c4d-aabbcc","paths":["/home/u/photo.png"],"mode":"ro"}
//	{"v":1,"ok":true,"app":"org.aurora.text","domain":"win-...","grants":[{"id":"1","host":"/home/u/photo.png",
//	   "guest":"/run/aurora/drops/1/photo.png","mode":"ro","dir":false,"source":"powerbox","ticket":"..."}]}
//	{"v":1,"ok":false,"code":"no_such_window","error":"..."}
//	{"v":1,"op":"release","domain":"win-...","id":"1"}
//
// Who may connect is decided on the connection from SO_PEERCRED, before a byte is read: the same user,
// outside any domain cgroup, exactly the rule the controller applies to the compositor
// (compositor_peer.go) turned around. It cannot tell the compositor from another host process of the same
// user; neither can the compositor tell this socket's owner from a squatter. What a same-user process gains
// by speaking here is a mount of a file it names into a window it names, read-only for a regular file:
// something it can already do by running `domain` itself.
type DropServer struct {
	Inv    *Invoker
	Socket string
	// ExpectedUID is the uid that may connect; nil means this process's own.
	ExpectedUID *uint32
	// DenyCgroupComponents / DenyCgroupPrefixes as for CompositorPrompt; nil components means aurora-domains.
	DenyCgroupComponents []string
	DenyCgroupPrefixes   []string
	// Timeout bounds one request including an approval prompt (default: approval timeout + 30s).
	Timeout time.Duration
	Logf    func(format string, a ...any)

	ln net.Listener
}

const maxDropLine = 64 * 1024

func (s *DropServer) logf(f string, a ...any) {
	if s.Logf != nil {
		s.Logf(f, a...)
	}
}

// Listen binds the socket 0600, replacing only a stale one.
func (s *DropServer) Listen() error {
	if c, err := net.DialTimeout("unix", s.Socket, 500*time.Millisecond); err == nil {
		c.Close()
		return fmt.Errorf("%s is in use by a live process", s.Socket)
	}
	_ = os.Remove(s.Socket)
	old := syscallUmask(0o177)
	ln, err := net.Listen("unix", s.Socket)
	syscallUmask(old)
	if err != nil {
		return err
	}
	s.ln = ln
	return nil
}

// Serve accepts until ctx ends.
func (s *DropServer) Serve(ctx context.Context) error {
	if s.ln == nil {
		if err := s.Listen(); err != nil {
			return err
		}
	}
	go func() { <-ctx.Done(); s.ln.Close() }()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(ctx, c)
	}
}

// Close stops listening and removes the socket.
func (s *DropServer) Close() {
	if s.ln != nil {
		s.ln.Close()
		os.Remove(s.Socket)
	}
}

func reply(c net.Conn, v any) {
	b, _ := json.Marshal(v)
	c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	c.Write(append(b, '\n'))
}

func refuseConn(c net.Conn, code, msg string) {
	reply(c, map[string]any{"v": 1, "ok": false, "code": code, "error": msg})
}

func (s *DropServer) handle(ctx context.Context, c net.Conn) {
	defer c.Close()
	p, err := peerOf(c)
	if err == nil {
		err = checkPeerPolicy(s.Socket, p, s.ExpectedUID, s.DenyCgroupComponents, s.DenyCgroupPrefixes)
	}
	if err != nil {
		s.logf("drop: REFUSED a peer on %s: %v", s.Socket, err)
		refuseConn(c, "peer_refused", err.Error())
		return
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReaderSize(io.LimitReader(c, maxDropLine+1), 4096).ReadBytes('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		refuseConn(c, "bad_request", "no request line")
		return
	}
	if len(line) > maxDropLine {
		refuseConn(c, "bad_request", "request too large")
		return
	}
	c.SetReadDeadline(time.Time{})
	var req DropRequest
	if err := json.Unmarshal(line, &req); err != nil {
		refuseConn(c, "bad_request", "unreadable request: "+err.Error())
		return
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
		if s.Inv.Approvals != nil {
			timeout += s.Inv.Approvals.timeout()
		}
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// A compositor that hangs up while a prompt is open withdraws the request.
	go func() {
		var b [1]byte
		c.Read(b[:])
		cancel()
	}()
	switch req.Op {
	case "", "drop":
		res, e := s.Inv.Drop(rctx, req)
		if e != nil {
			s.logf("drop: refused for %s: %s (%s)", req.Domain, e.Msg, e.Detail)
			reply(c, map[string]any{"v": 1, "ok": false, "code": e.Code, "error": e.Msg, "detail": e.Detail})
			return
		}
		reply(c, struct {
			V int `json:"v"`
			*DropResult
		}{1, res})
	case "release":
		if e := s.Inv.ReleaseDrop(req.Domain, req.ID); e != nil {
			reply(c, map[string]any{"v": 1, "ok": false, "code": e.Code, "error": e.Msg, "detail": e.Detail})
			return
		}
		reply(c, map[string]any{"v": 1, "ok": true})
	default:
		refuseConn(c, "bad_request", fmt.Sprintf("unknown op %q", req.Op))
	}
}
