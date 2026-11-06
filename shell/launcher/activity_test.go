package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/proto"
)

// logBuilder crafts bus-log entries with stable ids and a rising clock.
type logBuilder struct {
	n   int
	out []proto.Envelope
}

func (b *logBuilder) add(e proto.Envelope, payload string) string {
	b.n++
	e.ID = fmt.Sprintf("id-%d", b.n)
	e.TS = fmt.Sprintf("%019d-%020d", int64(1_700_000_000_000_000_000)+int64(b.n)*1_000_000, 0)
	if payload != "" {
		e.Payload = json.RawMessage(payload)
	}
	b.out = append(b.out, e)
	return e.ID
}

const ctl = "uid1000:controllerd"

func (b *logBuilder) invoke(from, app, verb, output string) string {
	p, _ := json.Marshal(map[string]any{"app": app, "verb": verb, "input": "/in.png", "output": output, "args": map[string]any{"w": 1}})
	return b.add(proto.Envelope{From: from, To: "registry", Kind: proto.KindRequest, Verb: verbInvoke}, string(p))
}

func (b *logBuilder) reply(id, payload string) {
	b.add(proto.Envelope{From: ctl, Kind: proto.KindResponse, Verb: verbInvoke, Corr: id}, payload)
}

func (b *logBuilder) fail(id, code, detail string) {
	b.add(proto.Envelope{From: ctl, Kind: proto.KindError, Verb: verbInvoke, Corr: id},
		fmt.Sprintf(`{"code":%q,"message":"nope","detail":%q,"ok":false}`, code, detail))
}

func (b *logBuilder) event(from, topic, payload string) {
	b.add(proto.Envelope{From: from, Kind: proto.KindEvent, Topic: topic}, payload)
}

const dom = "domain:app-org-aurora-demo-aa:11"

func approvalRecord(route, source, shown, decision, rid string) string {
	return fmt.Sprintf(`{"required":true,"route":%q,"decision":%q,"request_id":%q,"by":"compositor","domain_source":%q,"domain_shown":%q}`, route, decision, rid, source, shown)
}

func okReply(out, approval string) string {
	a := ""
	if approval != "" {
		a = `,"approval":` + approval
	}
	return fmt.Sprintf(`{"ok":true,"output":%q,"isolated":true,"grants":[{"path":"/in.png","mode":"ro","source":"gesture","cap":"cap:aa"},{"path":%q,"mode":"rw","source":"gesture","cap":"cap:bb"}]%s,"provenance":{"file":%q,"app":"org.aurora.demo","domain":%q,"domain_attested":true,"verb":"delete@v1","ts":"T1"%s}}`,
		out, out, a, out, dom, a)
}

func provEvent(out, approval string) string {
	a := ""
	if approval != "" {
		a = `,"approval":` + approval
	}
	return fmt.Sprintf(`{"file":%q,"app":"org.aurora.demo","domain":%q,"domain_attested":true,"verb":"delete@v1","inputs":["/in.png"],"isolated":true,"grants":[{"path":"/in.png","mode":"ro","source":"gesture","cap":"cap:aa"}],"ts":"T1"%s}`, out, dom, a)
}

func mustContain(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
}

func mustNotContain(t *testing.T, got string, bad ...string) {
	t.Helper()
	for _, w := range bad {
		if strings.Contains(got, w) {
			t.Errorf("unexpected %q in:\n%s", w, got)
		}
	}
}

func idx(t *testing.T, s, sub string) int {
	t.Helper()
	i := strings.Index(s, sub)
	if i < 0 {
		t.Fatalf("missing %q in:\n%s", sub, s)
	}
	return i
}

// An interleaved log: two invokes overlap, each gets its own approval, and a
// stray domain event belongs to neither.
func TestActivityGroupsInterleavedInvokesByCorrAndApprovalID(t *testing.T) {
	var b logBuilder
	i1 := b.invoke("uid1000:aurora", "org.aurora.demo", "delete@v1", "/out/one.png")
	i2 := b.invoke("uid1000:aurora", "org.aurora.demo", "delete@v1", "/out/two.png")
	b.event(dom, "approval.preflight", `{"app":"org.aurora.demo","verb":"delete@v1","effect":"destructive","request_id":"rA","grants":[]}`)
	b.event(ctl, "approval.requested", `{"request_id":"rB","app":"org.aurora.demo","domain":"`+dom+`","domain_attested":false,"domain_source":"claim","verb":"delete@v1","effect":"destructive","grants":[{"path":"/out/two.png","mode":"rw","source":"gesture"}],"timeout_seconds":4}`)
	b.event(ctl, "approval.decided", `{"request_id":"rA","decision":"allow","by":"compositor","route":"compositor"}`)
	b.event("domain:stray:1", "app.probe", `{"hello":"STRAYMARK"}`)
	b.event(ctl, "provenance.created", provEvent("/out/one.png", approvalRecord("compositor", "bus", dom, "allow", "rA")))
	b.reply(i1, okReply("/out/one.png", approvalRecord("compositor", "bus", dom, "allow", "rA")))
	b.event(ctl, "approval.decided", `{"request_id":"rB","decision":"deny","by":"","reason":"timeout","route":"interactive"}`)
	b.fail(i2, "approval_timeout", "request rB; nothing was run")

	got := FormatActivity(b.out)
	g1 := got[idx(t, got, "[#1]"):idx(t, got, "[#2]")]
	g2 := got[idx(t, got, "[#2]"):idx(t, got, "== Not tied to any invoke")]
	mustContain(t, g1, "/out/one.png", "approval rA", "joined to this invoke via the invoke reply's approval record", "route compositor")
	mustNotContain(t, g1, "rB", "/out/two.png")
	mustContain(t, g2, "TIMED OUT", "rB", "joined to this invoke via the invoke error's detail text", "NOBODY")
	mustNotContain(t, g2, "rA")
	// in order: asked, pre-flight, decided, provenance, reply
	a, p, d, pv, r := idx(t, g1, "ASKED"), idx(t, g1, "PRE-FLIGHT"), idx(t, g1, "APPROVAL DECIDED"), idx(t, g1, "PROVENANCE"), idx(t, g1, "REPLY OK")
	if !(a < p && p < d && d < pv && pv < r) {
		t.Errorf("steps out of order: %d %d %d %d %d\n%s", a, p, d, pv, r, g1)
	}
	// the stray event is visible, not folded into either group
	rest := got[idx(t, got, "== Not tied to any invoke"):]
	mustContain(t, rest, "STRAYMARK")
	mustNotContain(t, g1+g2, "STRAYMARK")
	mustContain(t, got, "!! [#2] approval rB TIMED OUT")
}

func TestActivityDistinguishesAttestedFromClaimed(t *testing.T) {
	cases := []struct {
		name, source, preflightFrom string
		want, notWant               string
	}{
		{"bus and corroborated", "bus", dom, "[attested by the bus]", "UNVERIFIED"},
		{"bus but no pre-flight in the log", "bus", "", "UNVERIFIED: recorded as bus-attested, but this log holds no pre-flight", "[attested by the bus]"},
		{"bus but pre-flight from another domain", "bus", "domain:other:2", "UNVERIFIED", "[attested by the bus]"},
		{"claim", "claim", "", "[UNVERIFIED: the controller's claim, not attested]", "[attested"},
		{"nothing recorded", "", "", "UNVERIFIED", "[attested"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b logBuilder
			id := b.invoke("uid1000:aurora", "org.aurora.demo", "delete@v1", "/out/x.png")
			if c.preflightFrom != "" {
				b.event(c.preflightFrom, "approval.preflight", `{"verb":"delete@v1","effect":"destructive","request_id":"r1"}`)
			}
			b.reply(id, okReply("/out/x.png", approvalRecord("interactive", c.source, dom, "allow", "r1")))
			got := FormatActivity(b.out)
			line := got[idx(t, got, "domain "+dom):]
			line = line[:strings.Index(line, "\n")]
			mustContain(t, line, c.want)
			mustNotContain(t, line, c.notWant)
		})
	}
}

func TestActivityShowsMismatchInTheBusWording(t *testing.T) {
	var b logBuilder
	id := b.invoke("uid1000:aurora", "org.aurora.demo", "delete@v1", "/out/x.png")
	b.event(dom, "approval.preflight", `{"request_id":"r1"}`)
	b.event(ctl, "approval.requested", `{"request_id":"r1","app":"a","domain":"`+dom+`","domain_attested":true,"domain_source":"bus","domain_mismatch":true,"domain_claim":"domain:claimed:9","verb":"delete@v1","effect":"destructive"}`)
	b.event(ctl, "approval.decided", `{"request_id":"r1","decision":"allow","by":"x","route":"interactive"}`)
	b.reply(id, okReply("/out/x.png", approvalRecord("interactive", "bus", dom, "allow", "r1")))
	mustContain(t, FormatActivity(b.out), "!! MISMATCH: the controller expected domain:claimed:9, the bus attested "+dom)
}

func TestActivityNeverRendersLogTextWithEscapes(t *testing.T) {
	const esc = "\x1b[2J\x1b]0;pwned\x07"
	const bidi = "evil\u202egnp.exe\u200b"
	var b logBuilder
	hostile, _ := json.Marshal(map[string]any{"app": "org.evil" + esc, "verb": "v\nFAKE LINE [#99] OK" + bidi, "input": "/in" + esc, "output": "/o\r\n  15:00:00.000  REPLY OK", "args": map[string]any{"k": esc}})
	id := b.add(proto.Envelope{From: "uid1000:evil" + esc, To: "registry", Kind: proto.KindRequest, Verb: verbInvoke}, string(hostile))
	b.event("domain:x"+esc, "approval.preflight", `{"request_id":"r\u001b9","verb":"`+esc+`","effect":"d\u202e"}`)
	b.event("uid1000:evil", "app.note", `{"x":"`+strings.ReplaceAll(esc, "\x1b", `\u001b`)+`"}`)
	b.add(proto.Envelope{From: "uid1000:evil", Kind: proto.KindEvent, Topic: "t" + esc, Segments: []proto.Segment{{ID: "s" + esc, Kind: "memfd", Bytes: 1, SHA256: "h" + esc}}}, `{}`)
	b.add(proto.Envelope{From: ctl, Kind: proto.KindResponse, Verb: verbInvoke, Corr: id},
		`{"ok":true,"output":"/o`+`\u001b[31m","log":"WARNING `+`\u001b]0;x\u0007 \u202e","grants":[{"path":"/p\u001b[1m","mode":"rw","source":"gesture","cap":"c\u001b"}],"provenance":{"domain":"d\u001b","domain_attested":false,"cap_note":"n\u001b[0m"}}`)
	got := FormatActivity(b.out)
	for _, bad := range []string{"\x1b", "\x07", "\r", "\u202e", "\u200b"} {
		if strings.Contains(got, bad) {
			t.Errorf("output contains %q:\n%q", bad, got)
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "[#99]") || strings.HasPrefix(l, "  15:00:00.000  REPLY OK") {
			t.Errorf("an app forged a line of the timeline: %q", l)
		}
	}
	mustContain(t, got, "org.evil?[2J")
}

func TestCleanStripsBidiAndInvisibles(t *testing.T) {
	if got := clean("a\u202eb\u200bc\u2028d\ufeffe\u2066f"); got != "a?b?c?d?e?f" {
		t.Fatalf("got %q", got)
	}
	if got := clean("ünïcode ✓ 日本"); got != "ünïcode ✓ 日本" {
		t.Fatalf("clean damaged ordinary text: %q", got)
	}
}

func TestActivitySurfacesHiddenState(t *testing.T) {
	var b logBuilder
	noReply := b.invoke("uid1000:aurora", "org.aurora.demo", "delete@v1", "/out/hang.png")
	_ = noReply
	// requested, never decided; the invoke is still open
	b.event(ctl, "approval.requested", `{"request_id":"rN","app":"org.aurora.demo","verb":"delete@v1","effect":"destructive","domain_source":"claim"}`)
	// a domain forging a controller-only topic
	b.event("domain:mal:1", "provenance.created", `{"file":"/home/me/tax.pdf","app":"org.aurora.demo","verb":"delete@v1"}`)
	// auto-approved run, and an unisolated one
	ia := b.invoke("uid1000:aurora", "org.aurora.demo", "delete@v1", "/out/auto.png")
	b.reply(ia, strings.Replace(okReply("/out/auto.png", `{"required":true,"route":"auto-approve","decision":"allow","request_id":"zz","by":"controllerd"}`), `"isolated":true`, `"isolated":false`, 1))
	// an orphan approval left undecided
	b.event(ctl, "approval.requested", `{"request_id":"rLost","app":"org.aurora.demo","verb":"delete@v1","effect":"destructive"}`)

	got := FormatActivity(b.out)
	mustContain(t, got,
		"NO REPLY IN LOG",
		"!! the request has no reply in the log",
		"!! approval rLost was requested and never decided in this log",
		"UNTRUSTED (only the controller publishes this topic)",
		"!! [#2] AUTO-APPROVED",
		"!! [#2] RAN UNISOLATED",
		"/home/me/tax.pdf", // the forged event is shown, flagged, not dropped
		"mint, transfer and revoke are bus control verbs and are NOT logged",
	)
	mustNotContain(t, got[idx(t, got, "[#1]"):idx(t, got, "[#2]")], "/home/me/tax.pdf")
}

func TestActivityRunWithoutDecisionIsFlagged(t *testing.T) {
	var b logBuilder
	id := b.invoke("uid1000:aurora", "org.aurora.demo", "delete@v1", "/out/x.png")
	b.event(ctl, "approval.requested", `{"request_id":"r1","verb":"delete@v1","effect":"destructive"}`)
	b.reply(id, okReply("/out/x.png", approvalRecord("interactive", "claim", dom, "allow", "r1")))
	mustContain(t, FormatActivity(b.out), "the invoke RAN with approval r1 but the log holds no decision for it")
}

func TestActivityNoEntrySilentlyDropped(t *testing.T) {
	var b logBuilder
	id := b.invoke("uid1000:aurora", "org.aurora.demo", "copy@v1", "/out/a.png")
	b.reply(id, okReply("/out/a.png", ""))
	b.add(proto.Envelope{From: "uid1000:q", To: "registry", Kind: proto.KindRequest, Verb: "registry.verbs@v1"}, `{"M1":1}`)
	b.event("uid1000:p", "ev.one", `{"M2":1}`)
	b.event("domain:d:1", "approval.preflight", `{"request_id":"","M3":1}`) // no id: cannot be joined
	b.add(proto.Envelope{From: ctl, Kind: proto.KindResponse, Verb: verbInvoke, Corr: "no-such-request"}, `{"M4":1}`)
	b.add(proto.Envelope{From: "uid1000:q", Kind: proto.KindEvent, Topic: "provenance.created"}, `not json M5`)
	got := FormatActivity(b.out)
	for _, m := range []string{"M1", "M2", "M3", "M4", "M5"} {
		mustContain(t, got, m)
	}
	mustContain(t, got, "no reply in the log") // M1's request
}

func TestActivityRealControllerLogs(t *testing.T) {
	entries, err := buslog.ReadFile("testdata/activity-demo.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got := FormatActivity(entries)
	mustContain(t, got, "9 invokes", "DENIED", "TIMED OUT", "[#7] org.aurora.demo delete@v1  OK",
		"[UNVERIFIED: the controller's claim, not attested]", "approval: none needed",
		"!! [#8] approval 1e849de4c2f456a9f8f80483a5a66aa5 TIMED OUT")
	if n := strings.Count(got, "\n[#"); n != 9 {
		t.Errorf("want 9 invoke groups, got %d", n)
	}
	// every approval id in the raw log appears
	for _, id := range []string{"509d2f587a5e50e5e326dc639c7fbb8c", "ae93c1a61ec30a041187d6b7051f334a", "1e849de4c2f456a9f8f80483a5a66aa5"} {
		mustContain(t, got, id)
	}
	// app.probe came from inside a domain and belongs to no invoke
	rest := got[idx(t, got, "== Not tied to any invoke"):]
	mustContain(t, rest, "app.probe", "from inside the domain")

	entries, err = buslog.ReadFile("testdata/activity-attested.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got = FormatActivity(entries)
	mustContain(t, got, "[attested by the bus] (pre-flight in this log was stamped by busd from inside it)",
		"10 pre-flight-only approval id(s) t1,", "forged-2", "domain:other:7befdca8c4b13df8")
}

type logBackend struct {
	fakeBackend
	entries []proto.Envelope
	err     error
}

func (l *logBackend) ReadLog(context.Context) ([]proto.Envelope, error) { return l.entries, l.err }

func TestActivityCommandOverTheBus(t *testing.T) {
	var b logBuilder
	id := b.invoke("uid1000:aurora", "org.aurora.demo", "copy@v1", "/out/a.png")
	b.reply(id, okReply("/out/a.png", ""))
	be := &logBackend{entries: b.out}
	var out, errb strings.Builder
	env := &Env{Backend: func(string, time.Duration) Backend { return be }, Out: &out, Err: &errb}
	if code := Run(env, []string{"activity"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	mustContain(t, out.String(), "[#1] org.aurora.demo copy@v1  OK")
	if errb.Len() != 0 {
		t.Errorf("stderr: %s", errb.String())
	}
}

func TestActivityCommandReadsALogDirectory(t *testing.T) {
	var lb logBuilder
	id := lb.invoke("uid1000:aurora", "org.aurora.demo", "copy@v1", "/out/a.png")
	lb.reply(id, okReply("/out/a.png", ""))
	var data []byte
	for _, e := range lb.out {
		line, _ := json.Marshal(e)
		data = append(data, append(line, '\n')...)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seg-000001.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	var out, errb strings.Builder
	env := &Env{Backend: func(string, time.Duration) Backend { called = true; return newFake() }, Out: &out, Err: &errb}
	if code := Run(env, []string{"activity", "--log", dir}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if called {
		t.Error("--log must not touch the bus")
	}
	mustContain(t, out.String(), "[#1] org.aurora.demo copy@v1  OK")
	// the recorded entries are from 2023, so --since 1h drops them all
	out.Reset()
	if code := Run(env, []string{"activity", "--log", dir, "--since", "1h"}); code != 0 {
		t.Fatal(errb.String())
	}
	mustContain(t, out.String(), "no activity in the log")
}

func TestActivityCommandErrors(t *testing.T) {
	var out, errb strings.Builder
	env := &Env{Backend: func(string, time.Duration) Backend { return newFake() }, Out: &out, Err: &errb}
	if code := Run(env, []string{"activity"}); code != 1 || !strings.Contains(errb.String(), "cannot read the bus log") {
		t.Fatalf("a backend with no log: code %d, %q", code, errb.String())
	}
	errb.Reset()
	env.Backend = func(string, time.Duration) Backend { return &logBackend{err: ErrBusDown} }
	if code := Run(env, []string{"activity"}); code != 1 || !strings.Contains(errb.String(), "busd") {
		t.Fatalf("bus down: code %d, %q", code, errb.String())
	}
	errb.Reset()
	if code := Run(env, []string{"activity", "--log", "/nonexistent/x"}); code != 1 {
		t.Fatalf("bad --log: code %d", code)
	}
	if code := Run(env, []string{"activity", "--bogus"}); code != 2 {
		t.Fatalf("bad flag: code %d", code)
	}
}
