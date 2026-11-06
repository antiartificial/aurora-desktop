package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// approvingBackend is fakeBackend plus the Approver half. Its Invoke blocks
// like the controller does: it returns only after a decision was delivered.
type approvingBackend struct {
	*fakeBackend
	reqs      chan ApprovalRequest
	mu        sync.Mutex
	decisions []string
	decided   chan string
}

func newApproving(reqs ...ApprovalRequest) *approvingBackend {
	b := &approvingBackend{fakeBackend: newFake(), reqs: make(chan ApprovalRequest, 4), decided: make(chan string, 4)}
	for _, r := range reqs {
		b.reqs <- r
	}
	return b
}

func (b *approvingBackend) WatchApprovals(ctx context.Context) (<-chan ApprovalRequest, error) {
	out := make(chan ApprovalRequest)
	go func() {
		defer close(out)
		for {
			select {
			case r := <-b.reqs:
				select {
				case out <- r:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (b *approvingBackend) Decide(_ context.Context, id, d string) error {
	b.mu.Lock()
	b.decisions = append(b.decisions, id+"="+d)
	b.mu.Unlock()
	b.decided <- d
	return nil
}

func (b *approvingBackend) Invoke(ctx context.Context, r InvokeRequest) (InvokeResult, error) {
	b.invokes = append(b.invokes, r)
	if !r.MayBlock {
		return InvokeResult{OK: true, Output: r.Output}, nil
	}
	switch <-b.decided {
	case "allow":
		return InvokeResult{OK: true, Output: r.Output, Approval: raw(`{"required":true,"route":"interactive"}`)}, nil
	}
	return InvokeResult{}, errors.New("approval_denied: wipe@v1 was denied")
}

type scriptTTY struct {
	in  io.Reader
	out bytes.Buffer
}

func (t *scriptTTY) Read(p []byte) (int, error)  { return t.in.Read(p) }
func (t *scriptTTY) Write(p []byte) (int, error) { return t.out.Write(p) }
func (t *scriptTTY) String() string              { return t.out.String() }
func (t *scriptTTY) Len() int                    { return t.out.Len() }

var sample = ApprovalRequest{
	RequestID: "req1", App: "org.aurora.shred", Domain: "domain:app-x:00", Verb: "wipe@v1", Effect: "destructive",
	Grants: []ApprovalGrant{{Path: "/home/me/a.png", Mode: "ro", Source: "gesture"}, {Path: "/home/me/a.wipe.png", Mode: "rw", Source: "powerbox"}},
	Args:   json.RawMessage(`{"passes":3}`), TimeoutSeconds: 60,
}

func ttyEnv(be Backend, answer string) (*Env, *scriptTTY, *bytes.Buffer) {
	env, _, errb := harness(nil, "")
	env.Backend = func(string, time.Duration) Backend { return be }
	tty := &scriptTTY{in: strings.NewReader(answer)}
	env.Tty = func() (io.ReadWriter, error) { return tty, nil }
	return env, tty, errb
}

func TestFormatApprovalShowsEverythingTheUserDecidesOn(t *testing.T) {
	s := FormatApproval(sample)
	for _, want := range []string{"org.aurora.shred", "wipe@v1", "DESTRUCTIVE", "ro", "gesture", "/home/me/a.png", "rw", "powerbox", "/home/me/a.wipe.png", `{"passes":3}`, "60s", "domain:app-x:00"} {
		if !strings.Contains(s, want) {
			t.Errorf("prompt lacks %q:\n%s", want, s)
		}
	}
}

func TestPromptNeutralisesTerminalEscapes(t *testing.T) {
	r := sample
	r.App = "evil\x1b[2J\x1b[Hfake"
	r.Grants = []ApprovalGrant{{Path: "/a\x1b]0;x\x07", Mode: "ro", Source: "gesture"}}
	if s := FormatApproval(r); strings.ContainsAny(s, "\x1b\x07") {
		t.Fatalf("escape characters reach the terminal: %q", s)
	}
}

func TestParseAnswerDeniesByDefault(t *testing.T) {
	for in, want := range map[string]string{"y\n": "allow", "YES": "allow", " allow ": "allow", "": "deny", "n": "deny", "maybe": "deny", "yy": "deny"} {
		if got := parseAnswer(in); got != want {
			t.Errorf("%q -> %s, want %s", in, got, want)
		}
	}
}

func TestOpenDestructivePromptsInSameFlowAndAllows(t *testing.T) {
	be := newApproving(sample)
	env, tty, _ := ttyEnv(be, "y\n")
	var out bytes.Buffer
	env.Out = &out
	png, _, _ := writeImages(t)
	if code := Run(env, []string{"open", png, "--verb", "wipe"}); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(tty.String(), "APPROVAL REQUESTED") || !strings.Contains(tty.String(), "Allow? [y/N]") {
		t.Fatalf("no prompt on the tty: %q", tty.String())
	}
	if len(be.invokes) != 1 || !be.invokes[0].MayBlock || be.decisions[0] != "req1=allow" {
		t.Fatalf("invokes %+v decisions %v", be.invokes, be.decisions)
	}
	if !strings.Contains(out.String(), `"route":"interactive"`) {
		t.Fatalf("approval outcome not printed:\n%s", out.String())
	}
}

func TestOpenDestructiveDeniedOnNoOrEOF(t *testing.T) {
	for _, ans := range []string{"n\n", "", "\n"} {
		be := newApproving(sample)
		env, _, errb := ttyEnv(be, ans)
		png, _, _ := writeImages(t)
		if code := Run(env, []string{"open", png, "--verb", "wipe"}); code != 1 {
			t.Fatalf("answer %q: exit %d", ans, code)
		}
		if be.decisions[0] != "req1=deny" || !strings.Contains(errb.String(), "approval_denied") {
			t.Fatalf("answer %q: decisions %v err %s", ans, be.decisions, errb.String())
		}
	}
}

func TestYesFlagAllowsWithoutAskingAndSaysSo(t *testing.T) {
	be := newApproving(sample)
	env, tty, errb := ttyEnv(be, "")
	png, _, _ := writeImages(t)
	if code := Run(env, []string{"open", png, "--verb", "wipe", "--yes"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if tty.Len() != 0 || be.decisions[0] != "req1=allow" {
		t.Fatalf("tty %q decisions %v", tty.String(), be.decisions)
	}
	if !strings.Contains(errb.String(), "AUTO-ALLOWING") || !strings.Contains(errb.String(), "defeats the point") {
		t.Fatalf("--yes was not loud: %s", errb.String())
	}
}

func TestWriteVerbDoesNotStartAResponder(t *testing.T) {
	be := newApproving(sample)
	env, tty, _ := ttyEnv(be, "y\n")
	png, _, _ := writeImages(t)
	if code := Run(env, []string{"open", png, "--verb", "export", "--arg", "format=png"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if be.invokes[0].MayBlock || tty.Len() != 0 || len(be.decisions) != 0 {
		t.Fatalf("write verb touched approvals: %+v %q", be.invokes[0], tty.String())
	}
}

func TestApprovalsCommandAnswersAndWatchRunsResponder(t *testing.T) {
	be := newApproving(sample)
	env, tty, _ := ttyEnv(be, "yes\n")
	if code := Run(env, []string{"approvals", "--count", "1"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if be.decisions[0] != "req1=allow" || !strings.Contains(tty.String(), "wipe@v1") {
		t.Fatalf("decisions %v tty %q", be.decisions, tty.String())
	}

	// watch starts the responder too (the fake's Watch returns at once, so
	// only the announcement is checked here; the prompting path is shared).
	env, _, _ = ttyEnv(newApproving(), "")
	var out bytes.Buffer
	env.Out = &out
	if code := Run(env, []string{"watch", "--count", "1"}); code != 0 || !strings.Contains(out.String(), "also answering approval.requested") {
		t.Fatalf("watch exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := Run(env, []string{"watch", "--no-approvals"}); code != 0 || strings.Contains(out.String(), "answering approval") {
		t.Fatalf("--no-approvals: %s", out.String())
	}
}

// The controller publishes domain_source, domain_mismatch and domain_claim in
// approval.requested. The launcher used to drop all three and print only
// "bus attested: false", which looks the same whether the bus disagreed with
// the controller or was simply never asked. These tests pin the distinction.

func TestFormatApprovalDomainTrust(t *testing.T) {
	for _, c := range []struct {
		name             string
		req              ApprovalRequest
		want, wantNoneOf []string
	}{
		{
			name:       "attested by the bus",
			req:        ApprovalRequest{Domain: "app-org-aurora-demo-0011", DomainAttested: true, DomainSource: "bus"},
			want:       []string{"app-org-aurora-demo-0011", "[attested by the bus]"},
			wantNoneOf: []string{"UNVERIFIED", "MISMATCH"},
		},
		{
			name:       "only the controller's claim",
			req:        ApprovalRequest{Domain: "app-org-aurora-demo-0011", DomainSource: "claim"},
			want:       []string{"UNVERIFIED", "not attested"},
			wantNoneOf: []string{"[attested by the bus]"},
		},
		{
			name:       "no source at all is not trusted",
			req:        ApprovalRequest{Domain: "d"},
			want:       []string{"UNVERIFIED"},
			wantNoneOf: []string{"[attested by the bus]"},
		},
		{
			name:       "an unrecognised source is never shown as trusted",
			req:        ApprovalRequest{Domain: "d", DomainSource: "totally-fine-trust-me"},
			want:       []string{"UNVERIFIED"},
			wantNoneOf: []string{"[attested by the bus]"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := FormatApproval(c.req)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("want %q in:\n%s", w, got)
				}
			}
			for _, w := range c.wantNoneOf {
				if strings.Contains(got, w) {
					t.Errorf("did not want %q in:\n%s", w, got)
				}
			}
		})
	}
}

// A mismatch is the case to deny: the bus attested a domain the controller did
// not expect. Both names must be on screen, and it must be unmissable.
func TestFormatApprovalMismatchShowsBothDomains(t *testing.T) {
	got := FormatApproval(ApprovalRequest{
		Domain: "app-evil-9999", DomainClaim: "app-org-aurora-demo-0011",
		DomainAttested: true, DomainSource: "bus", DomainMismatch: true,
	})
	for _, w := range []string{"MISMATCH", "app-org-aurora-demo-0011", "app-evil-9999", "Deny unless"} {
		if !strings.Contains(got, w) {
			t.Fatalf("want %q in:\n%s", w, got)
		}
	}
	// The attested domain, not the claim, is the one presented as the domain.
	if !strings.Contains(got, "domain:  app-evil-9999") {
		t.Errorf("the domain line must show the bus's domain:\n%s", got)
	}
}

// An app controls its own id, so it reaches these strings. It must not be able
// to repaint the prompt that judges it.
func TestFormatApprovalMismatchCleansControlChars(t *testing.T) {
	got := FormatApproval(ApprovalRequest{
		Domain: "d", DomainClaim: "a\x1b[2Jb\nc", DomainMismatch: true, DomainSource: "bus", DomainAttested: true,
	})
	if strings.Contains(got, "\x1b") {
		t.Fatalf("escape survived into the prompt:\n%q", got)
	}
	if !strings.Contains(got, "a?[2Jb?c") {
		t.Errorf("want the cleaned claim, got:\n%s", got)
	}
}

// Cross-module wiring: these exact bytes were produced by marshalling the
// controller's own invoke.ApprovalRequest (controller/internal/invoke/
// approval.go). If a json tag drifts on either side, every unit test above
// still passes while the field silently arrives as the zero value, which is
// the failure mode that hid the mismatch warning in the first place.
func TestFormatApprovalDecodesTheControllersRealPayload(t *testing.T) {
	const fromController = `{"request_id":"r1","app":"org.aurora.demo","domain":"app-evil-9999","domain_name":"","domain_attested":true,"domain_source":"bus","domain_mismatch":true,"domain_claim":"app-org-aurora-demo-0011","verb":"delete@v1","effect":"destructive","grants":null,"args":null,"timeout_seconds":60,"expires_at":""}`
	var r ApprovalRequest
	if err := json.Unmarshal([]byte(fromController), &r); err != nil {
		t.Fatal(err)
	}
	if r.DomainSource != "bus" || !r.DomainMismatch || r.DomainClaim != "app-org-aurora-demo-0011" {
		t.Fatalf("a json tag has drifted from the controller: source=%q mismatch=%v claim=%q", r.DomainSource, r.DomainMismatch, r.DomainClaim)
	}
	if got := FormatApproval(r); !strings.Contains(got, "MISMATCH") {
		t.Fatalf("the controller said mismatch and the prompt did not:\n%s", got)
	}
}
