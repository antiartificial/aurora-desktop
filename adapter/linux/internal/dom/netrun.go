package dom

import (
	"fmt"
	"io"
	"net"
	"os"
)

// startNetProxy brings up the host side for one run: the proxy on
// <SockDir>/net.sock and a reporter that publishes to the bus named by
// AURORA_BUS_SOCK (without one, decisions are enforced and not reported). The
// returned func stops both and sends the final summary.
func (s Store) startNetProxy(m *Meta, warn io.Writer) (func(), error) {
	path := SockDir(m.Name) + "/net.sock"
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("network proxy: %w", err)
	}
	// the dir is host-private (0700) and is the only thing bound into the
	// domain; the socket's own mode must let the domain's subordinate uid in
	os.Chmod(path, 0o666)
	var send func(map[string]any) error
	if sock := os.Getenv("AURORA_BUS_SOCK"); sock != "" {
		send = func(p map[string]any) error { return busCall(sock, "bus.net.report@v1", p) }
	}
	rep := NewNetReporter(ExecID(m), send, DefaultSummary, warn)
	px := NewNetProxy(m, rep)
	go px.Serve(ln)
	return func() { px.Close(); rep.Close(); os.Remove(path) }, nil
}
