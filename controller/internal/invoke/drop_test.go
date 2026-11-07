package invoke

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/antiartificial/aurora/controller/internal/domain"
)

// runningWindow launches the windowed test app and returns its domain and the handle that ends it.
func runningWindow(t *testing.T) (iv *Invoker, fk *domain.Fake, win string, end func()) {
	t.Helper()
	iv, fk, _ = winInvoker(t, true)
	iv.Tickets = &Tickets{Key: []byte(strings.Repeat("k", 32))}
	var proc *domain.FakeProc
	var mu sync.Mutex
	fk.StartHook = func(p *domain.FakeProc) *domain.FakeProc { mu.Lock(); proc = p; mu.Unlock(); return p }
	res, e := iv.Launch(t.Context(), LaunchRequest{App: "org.example.win"})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	return iv, fk, res.Domain, func() { mu.Lock(); proc.Exit(0); mu.Unlock(); iv.Close() }
}

func photo(t *testing.T) string {
	t.Helper()
	d, _ := filepath.EvalSymlinks(t.TempDir())
	p := filepath.Join(d, "photo.png")
	os.WriteFile(p, []byte("PNG"), 0o600)
	return p
}

func mounts(fk *domain.Fake) []string {
	var m []string
	for _, c := range fk.Log() {
		if strings.HasPrefix(c, "drop ") || strings.HasPrefix(c, "undrop ") {
			m = append(m, c)
		}
	}
	return m
}

func TestDropOfARegularFileReadOnlyNeedsNoApprovalAndMountsExactlyThatFile(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	defer end()
	if iv.Approvals != nil {
		t.Fatal("this test needs a controller with NO approval channel: any prompt would be refused")
	}
	p := photo(t)
	res, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{p}})
	if e != nil {
		t.Fatal(e, e.Detail)
	}
	if !res.OK || res.App != "org.example.win" || len(res.Grants) != 1 || res.Approval != nil {
		t.Fatalf("%+v", res)
	}
	g := res.Grants[0]
	if g.Host != p || g.Mode != "ro" || g.Source != "powerbox" || g.Ticket == "" || g.Dir || !strings.HasPrefix(g.Guest, "/run/aurora/drops/") || !strings.HasSuffix(g.Guest, "/photo.png") {
		t.Fatalf("grant %+v", g)
	}
	if strings.Contains(g.Guest, filepath.Dir(p)) {
		t.Fatalf("the guest path leaks the host directory: %s", g.Guest)
	}
	if got := mounts(fk); len(got) != 1 || got[0] != "drop "+win+" "+p+" ro" {
		t.Fatalf("mounts: %v", got)
	}
	if d := iv.Drops(win); len(d) != 1 || d[0].ID != g.ID {
		t.Fatalf("drops: %v", d)
	}
}

func TestEveryDropGetsItsOwnMountAndTicket(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	defer end()
	a, b := photo(t), photo(t)
	r1, e1 := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{a, b}})
	r2, e2 := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{a}})
	if e1 != nil || e2 != nil {
		t.Fatal(e1, e2)
	}
	ids := map[string]bool{}
	nonces := map[string]bool{}
	for _, g := range append(r1.Grants, r2.Grants...) {
		ids[g.ID], nonces[g.Ticket] = true, true
	}
	if len(ids) != 3 || len(nonces) != 3 || len(mounts(fk)) != 3 {
		t.Fatalf("ids %v nonces %v mounts %v", ids, nonces, mounts(fk))
	}
}

func TestADirectoryOrReadWriteIsRefusedWithoutAnApprovalChannel(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	defer end()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	for name, req := range map[string]DropRequest{
		"a directory": {Domain: win, Paths: []string{dir}},
		"read-write":  {Domain: win, Paths: []string{photo(t)}, Mode: "rw"},
	} {
		if _, e := iv.Drop(t.Context(), req); e == nil || e.Code != "approval_unavailable" {
			t.Errorf("%s: %v", name, e)
		}
	}
	if m := mounts(fk); len(m) != 0 {
		t.Fatalf("something was mounted without approval: %v", m)
	}
}

func TestADirectoryAndReadWriteGoThroughTheApprovalPromptAndDenialMountsNothing(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	defer end()
	reqs := make(chan ApprovalRequest, 4)
	iv.Publish = func(topic string, p []byte) error {
		if topic == TopicRequested {
			var r ApprovalRequest
			json.Unmarshal(p, &r)
			reqs <- r
		}
		return nil
	}
	iv.Approvals = &Approver{Timeout: 5 * time.Second}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	decide := func(want string) ApprovalRequest {
		select {
		case r := <-reqs:
			if err := iv.Approvals.Decide(r.RequestID, want, "uid"+"test"); err != nil {
				t.Fatal(err)
			}
			return r
		case <-time.After(3 * time.Second):
			t.Fatal("no approval request")
			return ApprovalRequest{}
		}
	}
	// a directory, denied
	done := make(chan *Error, 1)
	go func() { _, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{dir}}); done <- e }()
	r := decide(Deny)
	if e := <-done; e == nil || e.Code != "approval_denied" {
		t.Fatalf("denied directory: %v", e)
	}
	if r.App != "org.example.win" || r.DomainName != win || r.Verb != DropVerb || len(r.Grants) != 1 || r.Grants[0].Path != dir || r.Grants[0].Source != "powerbox" {
		t.Fatalf("the prompt shows %+v", r)
	}
	if len(mounts(fk)) != 0 {
		t.Fatalf("mounted after a denial: %v", mounts(fk))
	}
	// read-write, allowed: the prompt says destructive and the mount is rw
	p := photo(t)
	resc := make(chan *DropResult, 1)
	go func() {
		res, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{p}, Mode: "rw"})
		_ = e
		resc <- res
	}()
	r = decide(Allow)
	if r.Effect != "destructive" || r.Grants[0].Mode != "rw" {
		t.Fatalf("rw prompt %+v", r)
	}
	res := <-resc
	if res == nil || !res.OK || res.Approval == nil || !res.Approval.Required || res.Grants[0].Mode != "rw" {
		t.Fatalf("%+v", res)
	}
	if got := mounts(fk); len(got) != 1 || got[0] != "drop "+win+" "+p+" rw" {
		t.Fatalf("%v", got)
	}
	// a directory, allowed
	go func() { res, _ := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{dir}}); resc <- res }()
	r = decide(Allow)
	if r.Effect != "external" {
		t.Fatalf("directory prompt effect %q", r.Effect)
	}
	if res := <-resc; res == nil || !res.Grants[0].Dir || res.Grants[0].Mode != "ro" {
		t.Fatalf("%+v", res)
	}
}

func TestADropNamesOnlyWhatTheUserCouldRead(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	defer end()
	d, _ := filepath.EvalSymlinks(t.TempDir())
	real := filepath.Join(d, "real.txt")
	os.WriteFile(real, []byte("x"), 0o600)
	link := filepath.Join(d, "link.txt")
	os.Symlink(real, link)
	fifo := filepath.Join(d, "pipe")
	syscall.Mkfifo(fifo, 0o600)
	noread := filepath.Join(d, "noread.txt")
	os.WriteFile(noread, []byte("x"), 0o600)
	os.Chmod(noread, 0)
	key := filepath.Join(d, "powerbox.key")
	os.WriteFile(key, []byte("secret"), 0o600)
	iv.DropDeny = []string{key}
	cases := map[string]string{
		"a relative path":            "real.txt",
		"a dirty path":               d + "/./real.txt",
		"a symlink":                  link,
		"a fifo":                     fifo,
		"a missing file":             filepath.Join(d, "nope"),
		"a device":                   "/dev/null",
		"/proc":                      "/proc/self/status",
		"the root":                   "/",
		"a key the controller holds": key,
	}
	if os.Geteuid() != 0 {
		cases["an unreadable file"] = noread
	}
	for name, p := range cases {
		_, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{p}})
		if e == nil || e.Code != "drop_refused" {
			t.Errorf("%s: %v", name, e)
		}
	}
	if m := mounts(fk); len(m) != 0 {
		t.Fatalf("mounted: %v", m)
	}
	// one bad path does not spoil a good one, and says which was refused
	res, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{fifo, real}})
	if e != nil || len(res.Grants) != 1 || res.Grants[0].Host != real || len(res.Refused) != 1 || res.Refused[0].Path != fifo {
		t.Fatalf("%+v %v", res, e)
	}
}

func TestADropNeedsARunningWindowTheControllerLaunched(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	defer end()
	p := photo(t)
	for name, req := range map[string]DropRequest{
		"an unknown domain":       {Domain: "win-nothing-00000000-000000", Paths: []string{p}},
		"the app's verb domain":   {Domain: DomainName("org.example.win"), Paths: []string{p}},
		"no domain":               {Paths: []string{p}},
		"a window of another app": {Domain: win, App: "org.evil.app", Paths: []string{p}},
	} {
		_, e := iv.Drop(t.Context(), req)
		if e == nil || (e.Code != "no_such_window" && e.Code != "app_mismatch") {
			t.Errorf("%s: %v", name, e)
		}
	}
	if _, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: nil}); e == nil || e.Code != "bad_request" {
		t.Errorf("no paths: %v", e)
	}
	if _, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{p}, Mode: "rwx"}); e == nil || e.Code != "bad_request" {
		t.Errorf("bad mode: %v", e)
	}
	many := make([]string, MaxDropPaths+1)
	for i := range many {
		many[i] = p
	}
	if _, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: many}); e == nil || e.Code != "bad_request" {
		t.Errorf("too many: %v", e)
	}
	if m := mounts(fk); len(m) != 0 {
		t.Fatalf("mounted: %v", m)
	}
}

func TestWithoutAPowerboxKeyNothingIsMounted(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	defer end()
	iv.Tickets = nil
	_, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{photo(t)}})
	if e == nil || e.Code != "drop_refused" || !strings.Contains(e.Detail, "powerbox key") {
		t.Fatalf("%v", e)
	}
	if len(mounts(fk)) != 0 {
		t.Fatal("mounted without a ticket")
	}
}

func TestAMountThatFailsIsReportedAndNotRemembered(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	defer end()
	fk.DropErr = os.ErrPermission
	_, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{photo(t)}})
	if e == nil || e.Code != "drop_refused" || len(iv.Drops(win)) != 0 {
		t.Fatalf("%v %v", e, iv.Drops(win))
	}
}

func TestReleaseUnmountsAndWindowEndUnmountsEverything(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	res, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{photo(t), photo(t)}})
	if e != nil {
		t.Fatal(e)
	}
	if e := iv.ReleaseDrop(win, res.Grants[0].ID); e != nil {
		t.Fatal(e)
	}
	if e := iv.ReleaseDrop(win, res.Grants[0].ID); e == nil || e.Code != "no_such_drop" {
		t.Fatalf("released twice: %v", e)
	}
	if len(iv.Drops(win)) != 1 {
		t.Fatalf("%v", iv.Drops(win))
	}
	end() // the window ends
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(calls(fk), "undrop "+win+" "+res.Grants[1].ID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	c := calls(fk)
	if !strings.Contains(c, "undrop "+win+" "+res.Grants[0].ID) || !strings.Contains(c, "undrop "+win+" "+res.Grants[1].ID) {
		t.Fatalf("not all drops were unmounted when the window ended:\n%s", c)
	}
	if _, e := iv.Drop(t.Context(), DropRequest{Domain: win, Paths: []string{photo(t)}}); e == nil || e.Code != "no_such_window" {
		t.Fatalf("a drop on an ended window: %v", e)
	}
}

func dropServer(t *testing.T, iv *Invoker) *DropServer {
	t.Helper()
	d, err := os.MkdirTemp("", "drop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	s := &DropServer{Inv: iv, Socket: filepath.Join(d, "drop.sock"), Logf: t.Logf}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); s.Close() })
	go s.Serve(ctx)
	return s
}

func ask(t *testing.T, s *DropServer, line string) map[string]any {
	t.Helper()
	c, err := net.Dial("unix", s.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte(line + "\n"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("no reply: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("reply %q: %v", b, err)
	}
	return m
}

func TestTheDropSocketSpeaksTheDocumentedWireFormat(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	defer end()
	s := dropServer(t, iv)
	var fix struct{ Request, Reply map[string]any }
	b, err := os.ReadFile("../../testdata/drop-wire.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &fix); err != nil {
		t.Fatal(err)
	}
	p := photo(t)
	fix.Request["domain"], fix.Request["paths"] = win, []string{p}
	line, _ := json.Marshal(fix.Request)
	got := ask(t, s, string(line))
	// every key the fixture documents is present, with the documented type
	for k, want := range fix.Reply {
		if _, ok := got[k]; !ok {
			t.Errorf("reply lacks %q: %v", k, got)
		}
		if k == "grants" {
			g0 := got["grants"].([]any)[0].(map[string]any)
			for gk := range want.([]any)[0].(map[string]any) {
				if _, ok := g0[gk]; !ok {
					t.Errorf("grant lacks %q: %v", gk, g0)
				}
			}
		}
	}
	if got["ok"] != true || got["domain"] != win || len(mounts(fk)) != 1 {
		t.Fatalf("%v %v", got, mounts(fk))
	}
	id := got["grants"].([]any)[0].(map[string]any)["id"].(string)
	// release over the wire
	if r := ask(t, s, `{"v":1,"op":"release","domain":"`+win+`","id":"`+id+`"}`); r["ok"] != true {
		t.Fatalf("%v", r)
	}
	if r := ask(t, s, `{"v":1,"op":"frobnicate"}`); r["ok"] != false || r["code"] != "bad_request" {
		t.Fatalf("%v", r)
	}
	if r := ask(t, s, `not json`); r["ok"] != false || r["code"] != "bad_request" {
		t.Fatalf("%v", r)
	}
	if r := ask(t, s, `{"v":1,"op":"drop","domain":"`+win+`","paths":["relative"]}`); r["ok"] != false || r["code"] != "drop_refused" {
		t.Fatalf("%v", r)
	}
}

func TestTheDropSocketRefusesAPeerOfAnotherUserBeforeReadingAnything(t *testing.T) {
	iv, fk, win, end := runningWindow(t)
	defer end()
	s := dropServer(t, iv)
	other := uint32(os.Geteuid() + 1)
	s.ExpectedUID = &other
	r := ask(t, s, `{"v":1,"op":"drop","domain":"`+win+`","paths":["`+photo(t)+`"]}`)
	if r["ok"] != false || r["code"] != "peer_refused" || len(mounts(fk)) != 0 {
		t.Fatalf("%v %v", r, mounts(fk))
	}
	// and one inside a domain cgroup is refused: pretend our own cgroup is one
	s.ExpectedUID = nil
	cg, _ := os.ReadFile("/proc/self/cgroup")
	if p := cgroupV2Path(string(cg)); p != "" {
		s.DenyCgroupPrefixes = []string{p}
		r := ask(t, s, `{"v":1,"op":"drop","domain":"`+win+`","paths":["`+photo(t)+`"]}`)
		if r["code"] != "peer_refused" || len(mounts(fk)) != 0 {
			t.Fatalf("%v", r)
		}
	}
}

func TestTheDropSocketIsPrivate(t *testing.T) {
	iv, _, _, end := runningWindow(t)
	defer end()
	s := dropServer(t, iv)
	st, err := os.Stat(s.Socket)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", st, err)
	}
	if err := (&DropServer{Inv: iv, Socket: s.Socket}).Listen(); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("a live socket was replaced: %v", err)
	}
}
