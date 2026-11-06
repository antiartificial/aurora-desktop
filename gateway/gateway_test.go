package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antiartificial/aurora/shell/launcher"
)

type fakeCtl struct {
	mu      sync.Mutex
	verbs   []launcher.Verb
	desc    map[string]describeReply
	invoke  func(launcher.InvokeRequest) (launcher.InvokeResult, error)
	appr    chan launcher.ApprovalRequest
	invoked []launcher.InvokeRequest
}

func (f *fakeCtl) Verbs(context.Context) ([]launcher.Verb, error) { return f.verbs, nil }
func (f *fakeCtl) Describe(_ context.Context, app, verb string) (describeReply, error) {
	return f.desc[app+"/"+verb], nil
}
func (f *fakeCtl) Invoke(_ context.Context, r launcher.InvokeRequest) (launcher.InvokeResult, error) {
	f.mu.Lock()
	f.invoked = append(f.invoked, r)
	f.mu.Unlock()
	return f.invoke(r)
}
func (f *fakeCtl) WatchApprovals(context.Context) (<-chan launcher.ApprovalRequest, error) {
	return f.appr, nil
}
func (f *fakeCtl) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.invoked) }

type fakeAud struct {
	mu   sync.Mutex
	ev   []string
	fail bool
}

func (a *fakeAud) Publish(topic string, p []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return errors.New("bus down")
	}
	a.ev = append(a.ev, topic+" "+string(p))
	return nil
}
func (a *fakeAud) all() string { a.mu.Lock(); defer a.mu.Unlock(); return strings.Join(a.ev, "\n") }

func isoTrue() *bool { b := true; return &b }

// rig: apps A (read identify, write resize, destructive redact with a declared
// path cap under home) and B (a verb the policy does not allow).
func rig(t *testing.T) (*Gateway, *fakeCtl, *fakeAud, string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	declared := filepath.Join(root, "declared")
	os.MkdirAll(declared, 0o755)
	raw := func(s string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		json.Unmarshal([]byte(s), &m)
		return m
	}
	f := &fakeCtl{
		verbs: []launcher.Verb{
			{Name: "identify@v1", App: "app.a", Effect: "read", Accepts: []string{"image/png"}, Summary: "id"},
			{Name: "resize@v1", App: "app.a", Effect: "write", Accepts: []string{"image/png"}, Summary: "rs"},
			{Name: "redact@v1", App: "app.a", Effect: "destructive", Accepts: []string{"text/plain"}, Summary: "rd"},
			{Name: "share@v1", App: "app.a", Effect: "external", Summary: "sh"},
			{Name: "other@v1", App: "app.b", Effect: "read", Accepts: []string{"image/png"}, Summary: "no"},
		},
		desc: map[string]describeReply{
			"app.a/resize@v1": {Description: launcher.Description{Input: raw(`{"width":"int?","format":"string"}`)}},
			"app.a/redact@v1": {Description: launcher.Description{Input: raw(`{"word":"string"}`)}, Capabilities: []string{"path:" + declared}},
		},
		appr: make(chan launcher.ApprovalRequest, 4),
	}
	f.invoke = func(r launcher.InvokeRequest) (launcher.InvokeResult, error) {
		return launcher.InvokeResult{OK: true, Output: r.Output, Isolated: isoTrue()}, nil
	}
	a := &fakeAud{}
	p := &Policy{Label: "t", Apps: []string{"app.a"}, Roots: []string{root}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	g := New(f, a, p)
	g.ApprovalWait = 2 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := g.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return g, f, a, root
}

func mk(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func args(kv map[string]any) json.RawMessage { b, _ := json.Marshal(kv); return b }

func TestToolListIsAFunctionOfPolicy(t *testing.T) {
	g, _, _, _ := rig(t)
	tools, err := g.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	got := strings.Join(names, ",")
	want := "app.a.identify_v1,app.a.redact_v1,app.a.resize_v1,app.a.share_v1,aurora_result"
	if got != want {
		t.Fatalf("tools %s, want %s (app.b must be absent)", got, want)
	}
	g.Policy.Effects = []string{"read"}
	tools, _ = g.Tools(context.Background())
	if len(tools) != 2 || tools[0].Name != "app.a.identify_v1" {
		t.Fatalf("read-only policy should offer identify and aurora_result, got %+v", tools)
	}
}

func TestToolDefinitionShape(t *testing.T) {
	g, _, _, _ := rig(t)
	tools, _ := g.Tools(context.Background())
	var rs, rd Tool
	for _, tl := range tools {
		switch tl.Name {
		case "app.a.resize_v1":
			rs = tl
		case "app.a.redact_v1":
			rd = tl
		}
	}
	b, _ := json.Marshal(rs.InputSchema)
	var s struct {
		Type       string                    `json:"type"`
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
		Additional bool                      `json:"additionalProperties"`
	}
	json.Unmarshal(b, &s)
	if s.Type != "object" || s.Additional || strings.Join(s.Required, ",") != "format,input,output" {
		t.Fatalf("schema %s", b)
	}
	if s.Properties["width"]["type"] != "integer" || s.Properties["format"]["type"] != "string" {
		t.Fatalf("manifest arg types not mapped: %s", b)
	}
	if !strings.Contains(s.Properties["input"]["description"].(string), "image/png") {
		t.Fatalf("accepted MIME types missing from input description")
	}
	if rs.Meta["aurora/effect"] != "write" || rs.Annotations["readOnlyHint"] != false || rs.Annotations["destructiveHint"] != false {
		t.Fatalf("write annotations %+v %+v", rs.Meta, rs.Annotations)
	}
	if rd.Annotations["destructiveHint"] != true || !strings.Contains(rd.Description, "approval") {
		t.Fatalf("destructive tool not marked: %+v", rd)
	}
}

func TestReadAndWriteRunThroughTheController(t *testing.T) {
	g, f, a, root := rig(t)
	png := mk(t, filepath.Join(root, "a.png"), "x")
	r := g.Call(context.Background(), "agent-x", "app.a.identify_v1", args(map[string]any{"input": png}))
	if r.Status != StatusOK || f.count() != 1 || f.invoked[0].Verb != "identify@v1" || f.invoked[0].MayBlock {
		t.Fatalf("read: %+v %+v", r, f.invoked)
	}
	r = g.Call(context.Background(), "agent-x", "app.a.resize_v1", args(map[string]any{"input": png, "output": filepath.Join(root, "b.png"), "width": 10, "format": "png"}))
	if r.Status != StatusOK || r.Output != filepath.Join(root, "b.png") || f.count() != 2 {
		t.Fatalf("write: %+v", r)
	}
	if f.invoked[1].Args["format"] != "png" || f.invoked[1].Args["input"] != nil {
		t.Fatalf("input/output must be lifted out of args: %+v", f.invoked[1].Args)
	}
	ev := a.all()
	if !strings.Contains(ev, `"claimed_by_client":"agent-x"`) || !strings.Contains(ev, `"authenticated":false`) ||
		strings.Count(ev, TopicCall) != 2 || strings.Count(ev, TopicResult) != 2 {
		t.Fatalf("audit events:\n%s", ev)
	}
}

func TestPolicyRefusalsNeverReachTheController(t *testing.T) {
	g, f, _, root := rig(t)
	png := mk(t, filepath.Join(root, "a.png"), "x")
	outside := mk(t, filepath.Join(t.TempDir(), "secret.png"), "x")
	link := filepath.Join(root, "escape.png")
	os.Symlink(outside, link)
	cases := map[string]struct {
		tool string
		a    map[string]any
	}{
		"unlisted tool":      {"app.b.other_v1", map[string]any{"input": png}},
		"input outside root": {"app.a.identify_v1", map[string]any{"input": outside}},
		"symlink escape":     {"app.a.identify_v1", map[string]any{"input": link}},
		"relative path":      {"app.a.identify_v1", map[string]any{"input": "a.png"}},
		"dotdot":             {"app.a.identify_v1", map[string]any{"input": root + "/../" + filepath.Base(filepath.Dir(outside)) + "/secret.png"}},
		"output outside":     {"app.a.resize_v1", map[string]any{"input": png, "output": filepath.Join(filepath.Dir(outside), "o.png")}},
		"read with output":   {"app.a.identify_v1", map[string]any{"input": png, "output": filepath.Join(root, "o.png")}},
		"write w/o output":   {"app.a.resize_v1", map[string]any{"input": png}},
	}
	for name, c := range cases {
		r := g.Call(context.Background(), "x", c.tool, args(c.a))
		if r.Status != StatusRefused || !r.IsError() {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if f.count() != 0 {
		t.Fatalf("a refused call reached the controller: %+v", f.invoked)
	}
}

func TestDestructiveStopsAtApprovalAndDenyLeavesNothing(t *testing.T) {
	g, f, a, root := rig(t)
	txt := mk(t, filepath.Join(root, "n.txt"), "secret")
	out := filepath.Join(root, "n.red.txt")
	release := make(chan struct{})
	f.invoke = func(r launcher.InvokeRequest) (launcher.InvokeResult, error) {
		if !r.MayBlock {
			t.Error("a destructive invoke must be allowed to block on approval")
		}
		f.appr <- launcher.ApprovalRequest{RequestID: "req1", App: r.App, Verb: r.Verb, Effect: "destructive", Args: json.RawMessage(`{"word":"secret"}`), TimeoutSeconds: 60, Domain: "domain:x", DomainSource: "bus", DomainAttested: true}
		<-release
		return launcher.InvokeResult{}, &launcher.InvokeError{Verb: r.Verb, Msg: "approval_denied: x was denied by uid1:y", Detail: "request req1; nothing was run"}
	}
	r := g.Call(context.Background(), "x", "app.a.redact_v1", args(map[string]any{"input": txt, "output": out, "word": "secret"}))
	if r.Status != StatusAwaiting || r.IsError() || !strings.Contains(string(r.Approval), `"request_id":"req1"`) || !strings.Contains(r.Message, "cannot approve") {
		t.Fatalf("want awaiting_approval, got %+v", r)
	}
	if got := g.Call(context.Background(), "x", ResultTool, args(map[string]any{"call_id": r.CallID})); got.Status != StatusAwaiting {
		t.Fatalf("poll while pending: %+v", got)
	}
	close(release)
	var final CallResult
	for i := 0; i < 100; i++ {
		final = g.Call(context.Background(), "x", ResultTool, args(map[string]any{"call_id": r.CallID}))
		if final.Status != StatusAwaiting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != StatusDenied || !final.IsError() {
		t.Fatalf("want denied, got %+v", final)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("output exists after a denial")
	}
	if !strings.Contains(a.all(), `"status":"denied"`) {
		t.Fatalf("denial not audited:\n%s", a.all())
	}
}

func TestDestructiveThatWouldSkipApprovalIsRefused(t *testing.T) {
	g, f, _, root := rig(t)
	declared := filepath.Join(root, "declared")
	in := mk(t, filepath.Join(declared, "n.txt"), "x")
	// every path under the verb's declared capability: the controller would not ask
	r := g.Call(context.Background(), "x", "app.a.redact_v1", args(map[string]any{"input": in, "output": filepath.Join(declared, "o.txt"), "word": "x"}))
	if r.Status != StatusRefused || !strings.Contains(r.Message, "would not reach an approval") || f.count() != 0 {
		t.Fatalf("%+v invoked=%d", r, f.count())
	}
}

func TestGatedCallRefusedWhenApprovalsCannotBeWatched(t *testing.T) {
	g, f, _, root := rig(t)
	g.mu.Lock()
	g.watch = false
	g.mu.Unlock()
	txt := mk(t, filepath.Join(root, "n.txt"), "x")
	r := g.Call(context.Background(), "x", "app.a.redact_v1", args(map[string]any{"input": txt, "output": filepath.Join(root, "o.txt"), "word": "x"}))
	if r.Status != StatusRefused || f.count() != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestAuditFailsClosed(t *testing.T) {
	g, f, a, root := rig(t)
	a.fail = true
	png := mk(t, filepath.Join(root, "a.png"), "x")
	r := g.Call(context.Background(), "x", "app.a.identify_v1", args(map[string]any{"input": png}))
	if r.Status != StatusError || f.count() != 0 {
		t.Fatalf("a call that cannot be audited must not run: %+v", r)
	}
}

func TestAutoApprovedGatedRunIsFlagged(t *testing.T) {
	g, f, _, root := rig(t)
	txt := mk(t, filepath.Join(root, "n.txt"), "x")
	f.invoke = func(r launcher.InvokeRequest) (launcher.InvokeResult, error) {
		return launcher.InvokeResult{OK: true, Output: r.Output, Approval: json.RawMessage(`{"required":true,"route":"auto-approve","decision":"allow"}`)}, nil
	}
	r := g.Call(context.Background(), "x", "app.a.redact_v1", args(map[string]any{"input": txt, "output": filepath.Join(root, "o.txt"), "word": "x"}))
	if r.Status != StatusOK || r.Warning == "" {
		t.Fatalf("an auto-approved destructive run must carry a warning: %+v", r)
	}
}

func TestTerminalEscapesDoNotSurvive(t *testing.T) {
	g, f, a, root := rig(t)
	esc := "\x1b[2J\x1b]0;pwn\x07‮"
	r := g.Call(context.Background(), esc+"agent", esc+"tool", args(map[string]any{"input": esc}))
	b, _ := json.Marshal(r)
	for _, bad := range []string{"\x1b", "\x07", "‮"} {
		if strings.Contains(r.Message, bad) || strings.Contains(r.Tool, bad) {
			t.Fatalf("raw %q survived in %+v", bad, r)
		}
	}
	_ = b
	png := mk(t, filepath.Join(root, "a.png"), "x")
	f.invoke = func(r launcher.InvokeRequest) (launcher.InvokeResult, error) {
		return launcher.InvokeResult{OK: true, Output: r.Output, Result: json.RawMessage(`{"format":"` + `\u001b[31mred"}`), Provenance: json.RawMessage(`{"app":"\u001b]0;x\u0007"}`)}, nil
	}
	res := g.Call(context.Background(), esc, "app.a.identify_v1", args(map[string]any{"input": png}))
	out := string(res.Result) + string(res.Provenance)
	if strings.ContainsAny(out, "\x1b\x07") || strings.Contains(out, `\u001b`) || strings.Contains(out, `\u0007`) {
		t.Fatalf("app-supplied escapes survived: %s", out)
	}
	if clean("a\x1b[31mb‮c\x7f") != "a?[31mb?c?" {
		t.Fatalf("clean: %q", clean("a\x1b[31mb‮c\x7f"))
	}
	_ = a
}

// ---- MCP, driven by a scripted client over pipes ----

type scripted struct {
	w  io.Writer
	rd *bufio.Reader
	id int
}

func (c *scripted) rpc(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	c.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	c.w.Write(append(b, '\n'))
	line, err := c.rd.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("not JSON: %q", line)
	}
	if m["jsonrpc"] != "2.0" || int(m["id"].(float64)) != c.id {
		t.Fatalf("bad envelope %v", m)
	}
	return m
}

func TestMCPProtocol(t *testing.T) {
	g, f, a, root := rig(t)
	png := mk(t, filepath.Join(root, "a.png"), "x")
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	s := &Server{G: g}
	done := make(chan struct{})
	go func() { s.Serve(context.Background(), sr, sw); close(done) }()
	c := &scripted{w: cw, rd: bufio.NewReader(cr)}

	if m := c.rpc(t, "tools/list", nil); m["error"] == nil {
		t.Fatalf("tools/list before initialize must fail: %v", m)
	}
	m := c.rpc(t, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "scripted"}})
	res := m["result"].(map[string]any)
	if res["protocolVersion"] != "2024-11-05" || res["serverInfo"].(map[string]any)["name"] != "aurora-gateway" {
		t.Fatalf("initialize: %v", res)
	}
	if res["capabilities"].(map[string]any)["tools"] == nil {
		t.Fatal("no tools capability")
	}
	cw.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	if m := c.rpc(t, "ping", nil); m["result"] == nil {
		t.Fatal("ping")
	}
	m = c.rpc(t, "tools/list", map[string]any{})
	tl := m["result"].(map[string]any)["tools"].([]any)
	if len(tl) != 5 {
		t.Fatalf("tools: %d", len(tl))
	}
	first := tl[0].(map[string]any)
	for _, k := range []string{"name", "description", "inputSchema", "annotations", "_meta"} {
		if first[k] == nil {
			t.Fatalf("tool lacks %s: %v", k, first)
		}
	}
	m = c.rpc(t, "tools/call", map[string]any{"name": "app.a.identify_v1", "arguments": map[string]any{"input": png}})
	r := m["result"].(map[string]any)
	if r["isError"] != false || r["structuredContent"].(map[string]any)["status"] != "ok" || r["content"].([]any)[0].(map[string]any)["type"] != "text" {
		t.Fatalf("call: %v", r)
	}
	if !strings.Contains(a.all(), `"claimed_by_client":"scripted"`) {
		t.Fatalf("agent name from initialize not audited: %s", a.all())
	}
	m = c.rpc(t, "tools/call", map[string]any{"name": "app.b.other_v1", "arguments": map[string]any{"input": png}})
	if m["result"].(map[string]any)["isError"] != true {
		t.Fatalf("unlisted tool must be an isError result: %v", m)
	}
	if m := c.rpc(t, "no/such", nil); m["error"].(map[string]any)["code"].(float64) != -32601 {
		t.Fatalf("unknown method: %v", m)
	}
	if m := c.rpc(t, "tools/call", map[string]any{"arguments": 1}); m["error"] == nil {
		t.Fatal("nameless call must be a protocol error")
	}
	if f.count() != 1 {
		t.Fatalf("exactly one call should have reached the controller, got %d", f.count())
	}
	cw.Close()
	<-done
}
