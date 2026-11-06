package supervisor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Request is one line of JSON on the control socket; Response is the answer.
type Request struct {
	Op    string `json:"op"` // status | start | stop | restart
	Name  string `json:"name,omitempty"`
	Force bool   `json:"force,omitempty"`
}
type Response struct {
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
	Message  string   `json:"message,omitempty"`
	Services []Status `json:"services,omitempty"`
	PID      int      `json:"supervisor_pid,omitempty"`
}

// Serve answers control requests on a unix socket (0600, same uid only) until
// the listener is closed.
func (s *Supervisor) Serve(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
		c.Close()
		return nil, fmt.Errorf("another supervisor is already listening on %s", path)
	}
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serveConn(c.(*net.UnixConn))
		}
	}()
	return ln, nil
}

func (s *Supervisor) serveConn(c *net.UnixConn) {
	defer c.Close()
	if raw, err := c.SyscallConn(); err == nil {
		var uc *syscall.Ucred
		raw.Control(func(fd uintptr) { uc, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) })
		if uc == nil || int(uc.Uid) != os.Getuid() {
			json.NewEncoder(c).Encode(Response{Error: "refused: peer is not this user"})
			return
		}
	}
	c.SetDeadline(time.Now().Add(15 * time.Second))
	var req Request
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&req); err != nil {
		json.NewEncoder(c).Encode(Response{Error: "bad request: " + err.Error()})
		return
	}
	json.NewEncoder(c).Encode(s.Handle(req))
}

// Handle executes one control request.
func (s *Supervisor) Handle(r Request) Response {
	var msg string
	var err error
	switch r.Op {
	case "status":
		return Response{OK: true, Services: s.Status(), PID: os.Getpid()}
	case "start":
		msg, err = s.Start(r.Name)
	case "stop":
		msg, err = s.Stop(r.Name)
	case "restart":
		msg, err = s.Restart(r.Name, r.Force)
	default:
		err = fmt.Errorf("unknown op %q", Clean(r.Op))
	}
	if err != nil {
		return Response{Error: err.Error()}
	}
	return Response{OK: true, Message: msg}
}
