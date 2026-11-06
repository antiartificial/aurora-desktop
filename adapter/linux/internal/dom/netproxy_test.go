package dom

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeHostRefusesAnythingWiderThanAName(t *testing.T) {
	for _, bad := range []string{"", "*.example.com", "*", "a.example.com:443", "https://a.example.com", "a.example.com/x", "a b", "-a.example.com", "a..example.com", "user@a.example.com", "[::1]"} {
		if h, err := NormalizeHost(bad); err == nil {
			t.Errorf("NormalizeHost(%q) = %q, want a refusal", bad, h)
		}
	}
	for in, want := range map[string]string{"A.Example.COM": "a.example.com", "a.example.com.": "a.example.com", "192.0.2.7": "192.0.2.7", "::1": "::1"} {
		if got, err := NormalizeHost(in); err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestPolicyIsWholeNameEquality(t *testing.T) {
	m := &Meta{Grants: []Grant{{Kind: "net", Host: "a.example.com"}, {Kind: "net", Host: "*.wild.com"}, {Kind: "path", Path: "b.example.com"}}}
	p := NewNetPolicy(m)
	for h, want := range map[string]bool{
		"a.example.com": true, "A.EXAMPLE.COM": true, "a.example.com.": true,
		"b.example.com": false, "example.com": false, "xa.example.com": false,
		"a.example.com.evil.net": false, "evil-a.example.com": false, "x.wild.com": false, "*.wild.com": false, "": false,
	} {
		if p.Allows(h) != want {
			t.Errorf("Allows(%q) = %v, want %v", h, !want, want)
		}
	}
}

// helloFor makes a real ClientHello for the given server name.
func helloFor(t *testing.T, name string) []byte {
	t.Helper()
	a, b := net.Pipe()
	go func() {
		tls.Client(a, &tls.Config{ServerName: name, InsecureSkipVerify: true}).Handshake()
		a.Close()
	}()
	buf := make([]byte, 16384)
	b.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := b.Read(buf)
	b.Close()
	return buf[:n]
}

func TestParseSNI(t *testing.T) {
	h := helloFor(t, "a.example.com")
	if got, ok := parseSNI(h[5:]); !ok || got != "a.example.com" {
		t.Errorf("parseSNI = %q, %v", got, ok)
	}
	if _, ok := parseSNI(h[5 : len(h)/2]); ok {
		t.Error("a truncated hello parsed")
	}
	if _, ok := parseSNI([]byte{0x02, 0, 0, 0}); ok {
		t.Error("a ServerHello parsed as a ClientHello")
	}
	for i := 0; i < len(h)-5; i++ { // never panic, whatever the cut
		parseSNI(h[5 : 5+i])
	}
}

type recorder struct {
	mu sync.Mutex
	ev []string
}

func (r *recorder) send(p map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p["kind"] == "conn" {
		r.ev = append(r.ev, fmt.Sprintf("%v/%v/%v", p["host"], p["verdict"], p["reason"]))
	} else {
		r.ev = append(r.ev, "summary")
	}
	return nil
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ev...)
}

// proxyFor serves a proxy for a domain granted `hosts`; every name resolves to
// the loopback listener at `target`, so nothing leaves the test.
func proxyFor(t *testing.T, target string, hosts ...string) (addr string, rec *recorder) {
	t.Helper()
	m := &Meta{Name: "t"}
	for _, h := range hosts {
		m.Grants = append(m.Grants, Grant{Kind: "net", Host: h})
	}
	rec = &recorder{}
	rep := NewNetReporter("domain:t:00112233", rec.send, time.Hour, nil)
	px := NewNetProxy(m, rep)
	tcp, _ := net.ResolveTCPAddr("tcp", target)
	px.Resolve = func(ctx context.Context, host string) ([]net.IP, error) { return []net.IP{tcp.IP}, nil }
	px.Dial = func(ctx context.Context, ip net.IP, port int) (net.Conn, error) { return net.Dial("tcp", target) }
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback")
	}
	go px.Serve(ln)
	t.Cleanup(func() { px.Close(); rep.Close() })
	return ln.Addr().String(), rec
}

func TestProxyAllowsGrantedAndRefusesOthersByName(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, "host=%s", r.Host) }))
	defer up.Close()
	addr, rec := proxyFor(t, strings.TrimPrefix(up.URL, "http://"), "a.test")
	get := func(host string) (int, string) {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprintf(c, "GET http://%s/ HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("a.test:8080"); code != 200 || body != "host=a.test:8080" {
		t.Errorf("granted host: %d %q", code, body)
	}
	for _, h := range []string{"b.test:8080", "a.test.evil.net:8080", "127.0.0.1:8080"} {
		code, body := get(h)
		if code != 403 || !strings.Contains(body, "no network grant") || !strings.Contains(body, strings.Split(h, ":")[0]) {
			t.Errorf("%s: want a 403 that names the host, got %d %q", h, code, body)
		}
	}
	if got := rec.all(); len(got) != 4 {
		t.Errorf("events = %v, want one first-seen per (host, verdict)", got)
	}
}

func TestProxyConnectChecksSNIAgainstTheHost(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer ts.Close()
	addr, rec := proxyFor(t, strings.TrimPrefix(ts.URL, "https://"), "a.test")
	via := func(connectHost, sni string) error {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprintf(c, "CONNECT %s:443 HTTP/1.1\r\nHost: %s:443\r\n\r\n", connectHost, connectHost)
		br := bufio.NewReader(c)
		resp, err := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
		if err != nil || resp.StatusCode != 200 {
			return fmt.Errorf("connect: %v %v", err, resp)
		}
		tc := tls.Client(&bufConn{c, br}, &tls.Config{ServerName: sni, InsecureSkipVerify: true})
		return tc.Handshake()
	}
	if err := via("a.test", "a.test"); err != nil {
		t.Errorf("matching SNI: %v", err)
	}
	if err := via("a.test", "b.test"); err == nil {
		t.Error("a TLS hello for b.test through a CONNECT to a.test was tunnelled")
	}
	if err := via("b.test", "b.test"); err == nil {
		t.Error("a CONNECT to an ungranted host succeeded")
	}
	got := strings.Join(rec.all(), " ")
	for _, want := range []string{"a.test/allowed/net grant", `a.test/refused/TLS SNI "b.test" does not match the CONNECT host`, "b.test/refused/no net grant for this host"} {
		if !strings.Contains(got, want) {
			t.Errorf("events %q lack %q", got, want)
		}
	}
}

type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func TestReporterVolumeIsBoundedWhateverTheTraffic(t *testing.T) {
	rec := &recorder{}
	now := time.Unix(0, 0)
	r := NewNetReporter("domain:t:00112233", rec.send, time.Hour, nil)
	r.now = func() time.Time { return now } // no refill: the burst is all there is
	r.last, r.tokens = now, reportBurst
	for i := 0; i < 100000; i++ { // a chatty app: one host, many connections
		r.Record("chatty.test", 443, "allowed", "net grant")
	}
	if got := len(rec.all()); got != 1 {
		t.Errorf("100000 connections to one host sent %d events, want 1", got)
	}
	for i := 0; i < 5000; i++ { // a hostile one: a different name every time
		r.Record(fmt.Sprintf("n%d.test", i), 443, "refused", "no net grant for this host")
	}
	if got := len(rec.all()); got > 1+reportBurst {
		t.Errorf("5000 distinct hosts sent %d events, want at most %d", got-1, reportBurst)
	}
	r.Close()
	all := rec.all()
	if all[len(all)-1] != "summary" {
		t.Errorf("no final summary: %v", all)
	}
	r.mu.Lock()
	keys := len(r.seen)
	r.mu.Unlock()
	if keys > reportMaxKeys+1 {
		t.Errorf("tracked %d distinct keys, cap is %d", keys, reportMaxKeys)
	}
}
