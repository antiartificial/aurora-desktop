package dom

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NetProxy is the only way out of a domain that holds a net grant (decision
// 0015). The domain keeps an empty network namespace (--unshare-net, always);
// the proxy runs on the host, listens on a unix socket the domain sees at
// /run/aurora/net.sock, and decides by NAME:
//
//   - CONNECT host:port  -> the host in the request line
//   - GET http://host/.. -> the host in the request URI
//   - after a CONNECT, a TLS ClientHello's SNI must equal the CONNECT host
//
// A host not in the domain's net grants is answered 403 at once and never
// resolved: the name does not leave the machine, so a lookup cannot carry data.
// Only an allowed name is resolved, here, by the host's resolver; the domain
// has no resolver and no route.
type NetProxy struct {
	Domain   string
	Policy   NetPolicy
	Report   *NetReporter
	Resolve  func(ctx context.Context, host string) ([]net.IP, error)
	Dial     func(ctx context.Context, ip net.IP, port int) (net.Conn, error)
	Timeout  time.Duration // dial and request-head deadline
	wg       sync.WaitGroup
	mu       sync.Mutex
	listener net.Listener
}

// StaticResolutions parses AURORA_NET_RESOLVE ("name=ip,name=ip"): names the
// proxy resolves itself, before the system resolver. It lets a test point two
// hostnames at loopback addresses without touching /etc/hosts. It is read from
// the environment of the `domain run` process, which the domain cannot set.
func StaticResolutions(spec string) map[string]net.IP {
	out := map[string]net.IP{}
	for _, kv := range strings.Split(spec, ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
			if ip := net.ParseIP(v); ip != nil {
				out[strings.ToLower(k)] = ip
			}
		}
	}
	return out
}

// NewNetProxy builds the proxy for a domain.
func NewNetProxy(m *Meta, rep *NetReporter) *NetProxy {
	static := StaticResolutions(os.Getenv("AURORA_NET_RESOLVE"))
	return &NetProxy{
		Domain: m.Name, Policy: NewNetPolicy(m), Report: rep, Timeout: 10 * time.Second,
		Resolve: func(ctx context.Context, host string) ([]net.IP, error) {
			if ip, ok := static[host]; ok {
				return []net.IP{ip}, nil
			}
			if ip := net.ParseIP(host); ip != nil {
				return []net.IP{ip}, nil
			}
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
	}
}

// Serve accepts connections until ln is closed.
func (p *NetProxy) Serve(ln net.Listener) {
	p.mu.Lock()
	p.listener = ln
	p.mu.Unlock()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer c.Close()
			p.handle(c)
		}()
	}
}

// Close stops accepting; established tunnels end when their peers do.
func (p *NetProxy) Close() {
	p.mu.Lock()
	if p.listener != nil {
		p.listener.Close()
	}
	p.mu.Unlock()
}

func (p *NetProxy) record(host string, port int, verdict, reason string) {
	if p.Report != nil {
		p.Report.Record(host, port, verdict, reason)
	}
}

// refuse answers a refused request. The body says which domain, which host and
// why, so the app's error names the grant it lacks instead of a timeout.
func (p *NetProxy) refuse(c net.Conn, host string, port int, reason string) {
	p.record(host, port, "refused", reason)
	body := fmt.Sprintf("aurora: domain %q has no network grant for %s (%s)\n", p.Domain, host, reason)
	fmt.Fprintf(c, "HTTP/1.1 403 Forbidden\r\nX-Aurora-Verdict: refused\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
}

func (p *NetProxy) fail(c net.Conn, host string, port int, status int, why string) {
	p.record(host, port, "failed", why)
	body := fmt.Sprintf("aurora: %s\n", why)
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nX-Aurora-Verdict: failed\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, http.StatusText(status), len(body), body)
}

func (p *NetProxy) handle(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(p.Timeout))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	var host string
	var port int
	connect := req.Method == http.MethodConnect
	if connect {
		host, port, err = splitHostPort(req.Host, 0)
	} else if req.URL.IsAbs() && req.URL.Scheme == "http" {
		host, port, err = splitHostPort(req.URL.Host, 80)
	} else {
		p.fail(c, "", 0, 400, "this is a proxy: send CONNECT host:port or an absolute http:// URL")
		return
	}
	if err != nil {
		p.fail(c, "", 0, 400, "bad target: "+err.Error())
		return
	}
	if !p.Policy.Allows(host) {
		p.refuse(c, host, port, "no net grant for this host")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.Timeout)
	defer cancel()
	up, err := p.dialHost(ctx, host, port)
	if err != nil {
		p.fail(c, host, port, 502, fmt.Sprintf("granted host %s:%d is unreachable: %v", host, port, err))
		return
	}
	defer up.Close()
	if connect {
		if _, err := io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			return
		}
		if net.ParseIP(host) == nil {
			if why := sniffMismatch(c, br, host); why != "" {
				p.record(host, port, "refused", why)
				return // the tunnel is already open; a TLS client sees EOF, the event names the reason
			}
		}
	} else {
		req.Host = req.URL.Host
		req.Header.Del("Proxy-Connection")
		req.Header.Del("Proxy-Authorization")
		req.Close = true
		if err := req.Write(up); err != nil {
			p.fail(c, host, port, 502, "write to "+host+": "+err.Error())
			return
		}
	}
	p.record(host, port, "allowed", "net grant")
	splice(c, br, up)
}

func (p *NetProxy) dialHost(ctx context.Context, host string, port int) (net.Conn, error) {
	ips, err := p.Resolve(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("cannot resolve: %v", err)
	}
	dial := p.Dial
	if dial == nil {
		dial = func(ctx context.Context, ip net.IP, port int) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		}
	}
	var last error
	for _, ip := range ips { // the address is chosen once, here: what we connect to is what we resolved
		c, err := dial(ctx, ip, port)
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

func splitHostPort(hp string, def int) (string, int, error) {
	h, ps, err := net.SplitHostPort(hp)
	if err != nil {
		if def == 0 || strings.Contains(hp, ":") && !strings.HasPrefix(hp, "[") && strings.Count(hp, ":") == 1 {
			return "", 0, err
		}
		h, ps = strings.Trim(hp, "[]"), strconv.Itoa(def)
	}
	n, err := strconv.Atoi(ps)
	if err != nil || n < 1 || n > 65535 {
		return "", 0, errors.New("bad port")
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "" {
		return "", 0, errors.New("empty host")
	}
	return h, n, nil
}

// splice copies both ways until either side ends. br may hold bytes the client
// sent along with its request head.
func splice(c net.Conn, br *bufio.Reader, up net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(up, br)
		if uc, ok := up.(interface{ CloseWrite() error }); ok {
			uc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		io.Copy(c, up)
		if cc, ok := c.(interface{ CloseWrite() error }); ok {
			cc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

// sniffMismatch peeks at what the client sends right after the 200. If it is
// a TLS ClientHello its SNI must be the CONNECT host; a hello the proxy cannot
// parse is refused rather than waved through (fail closed: a client that splits
// its hello across records to hide the name gets nothing). Non-TLS bytes, no
// SNI at all, and a client that waits for the server to speak first (nothing
// within the window) pass: there is no name to compare. The proxy sees only
// this first record, so it is a check on what the client claims to be
// connecting to, not an inspection of the stream (0015 says what that leaves).
func sniffMismatch(c net.Conn, br *bufio.Reader, host string) string {
	c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	defer c.SetReadDeadline(time.Time{})
	first, err := br.Peek(1)
	if err != nil || first[0] != 0x16 {
		return ""
	}
	hdr, err := br.Peek(5)
	if err != nil {
		return "unparseable TLS hello"
	}
	n := int(hdr[3])<<8 | int(hdr[4])
	if n > 16384 {
		return "unparseable TLS hello"
	}
	rec, err := br.Peek(5 + n)
	if err != nil {
		return "unparseable TLS hello"
	}
	sni, ok := parseSNI(rec[5:])
	if !ok {
		return "unparseable TLS hello"
	}
	if sni != "" && strings.ToLower(strings.TrimSuffix(sni, ".")) != host {
		return fmt.Sprintf("TLS SNI %q does not match the CONNECT host", sni)
	}
	return ""
}

// parseSNI reads server_name out of a ClientHello handshake message.
func parseSNI(b []byte) (sni string, ok bool) {
	// handshake header: type(1) len(3)
	if len(b) < 4 || b[0] != 0x01 {
		return "", false
	}
	hl := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	if hl > len(b)-4 {
		return "", false // spans records
	}
	b = b[4 : 4+hl]
	skip := func(n int) bool {
		if len(b) < n {
			return false
		}
		b = b[n:]
		return true
	}
	if !skip(2+32) || len(b) < 1 || !skip(1+int(b[0])) {
		return "", false
	}
	if len(b) < 2 || !skip(2+(int(b[0])<<8|int(b[1]))) {
		return "", false
	}
	if len(b) < 1 || !skip(1+int(b[0])) {
		return "", false
	}
	if len(b) == 0 {
		return "", true // no extensions
	}
	if len(b) < 2 {
		return "", false
	}
	el := int(b[0])<<8 | int(b[1])
	if el > len(b)-2 {
		return "", false
	}
	b = b[2 : 2+el]
	for len(b) >= 4 {
		t, l := int(b[0])<<8|int(b[1]), int(b[2])<<8|int(b[3])
		if l > len(b)-4 {
			return "", false
		}
		data := b[4 : 4+l]
		b = b[4+l:]
		if t != 0 {
			continue
		}
		if len(data) < 5 {
			return "", false
		}
		ll := int(data[0])<<8 | int(data[1])
		if ll > len(data)-2 || data[2] != 0 {
			return "", false
		}
		nl := int(data[3])<<8 | int(data[4])
		if nl > len(data)-5 {
			return "", false
		}
		return string(data[5 : 5+nl]), true
	}
	return "", true
}
