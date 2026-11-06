package launcher

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// ApprovalRequest is an approval.requested payload from the controller: the
// real verb, grants and arguments of an invoke that is blocked waiting.
type ApprovalRequest struct {
	RequestID      string          `json:"request_id"`
	App            string          `json:"app"`
	Domain         string          `json:"domain"`
	DomainName     string          `json:"domain_name"`
	DomainAttested bool            `json:"domain_attested"`
	Verb           string          `json:"verb"`
	Effect         string          `json:"effect"`
	Grants         []ApprovalGrant `json:"grants"`
	Args           json.RawMessage `json:"args"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	ExpiresAt      string          `json:"expires_at"`
}

// ApprovalGrant is one filesystem grant the request asks for.
type ApprovalGrant struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Source string `json:"source"`
}

// Approver is the optional half of a Backend that can see approval requests
// and answer them. The bus backend implements it; plain fakes need not.
type Approver interface {
	// WatchApprovals subscribes (before returning) and delivers requests until
	// ctx ends, then closes the channel.
	WatchApprovals(ctx context.Context) (<-chan ApprovalRequest, error)
	// Decide answers a request: "allow" or "deny".
	Decide(ctx context.Context, id, decision string) error
}

// clean makes app-supplied text safe to print on a terminal: control
// characters (including escape) become '?', so an app cannot repaint the
// prompt it is being judged by.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return '?'
		}
		return r
	}, s)
}

// FormatApproval renders the request for a human: app, verb, effect class,
// every grant with its source, and the arguments.
func FormatApproval(r ApprovalRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== APPROVAL REQUESTED (%s) ===\n", clean(r.RequestID))
	fmt.Fprintf(&b, "  app:     %s\n", clean(r.App))
	if r.Domain != "" {
		fmt.Fprintf(&b, "  domain:  %s (bus attested: %v)\n", clean(r.Domain), r.DomainAttested)
	}
	fmt.Fprintf(&b, "  verb:    %s\n  effect:  %s\n", clean(r.Verb), strings.ToUpper(clean(r.Effect)))
	for _, g := range r.Grants {
		fmt.Fprintf(&b, "  grant:   %-2s %-8s %s\n", clean(g.Mode), clean(g.Source), clean(g.Path))
	}
	args := "{}"
	if len(r.Args) > 0 {
		args = clean(string(r.Args))
	}
	fmt.Fprintf(&b, "  args:    %s\n", args)
	if r.TimeoutSeconds > 0 {
		fmt.Fprintf(&b, "  (no answer within %ds means deny)\n", r.TimeoutSeconds)
	}
	return b.String()
}

// parseAnswer: only an explicit yes allows; anything else, EOF included, denies.
func parseAnswer(line string) string {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "allow":
		return "allow"
	}
	return "deny"
}

// responder prompts for requests one at a time on a terminal. yes answers
// allow to everything without asking.
type responder struct {
	env *Env
	be  Approver
	yes bool
	mu  sync.Mutex
	tty *bufio.Reader
	out io.Writer
}

func (r *responder) answer(ctx context.Context, req ApprovalRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, err := time.Parse(time.RFC3339, req.ExpiresAt); err == nil && time.Now().After(t) {
		return
	}
	decision := "allow"
	if r.yes {
		fmt.Fprintf(r.env.Err, "aurora: --yes: AUTO-ALLOWING %s %s (%s) without asking. This is a test affordance and defeats the point of approval.\n", clean(req.App), clean(req.Verb), req.Effect)
	} else {
		if err := r.openTTY(); err != nil {
			fmt.Fprintf(r.env.Err, "aurora: cannot ask about %s %s: %v (it will be denied when it times out)\n", clean(req.App), clean(req.Verb), err)
			return
		}
		fmt.Fprint(r.out, FormatApproval(req))
		fmt.Fprint(r.out, "Allow? [y/N]: ")
		line, _ := r.tty.ReadString('\n')
		decision = parseAnswer(line)
		fmt.Fprintf(r.out, "-> %s\n", decision)
	}
	if err := r.be.Decide(ctx, req.RequestID, decision); err != nil {
		fmt.Fprintf(r.env.Err, "aurora: decision for %s not accepted: %v\n", req.RequestID, err)
	}
}

func (r *responder) openTTY() error {
	if r.tty != nil {
		return nil
	}
	if r.env.Tty == nil {
		return fmt.Errorf("no terminal available")
	}
	rw, err := r.env.Tty()
	if err != nil {
		return err
	}
	r.tty, r.out = bufio.NewReader(rw), rw
	return nil
}

// OpenTTY opens /dev/tty for prompting, so a pipe on stdin or stdout cannot
// answer for the user. (Any process that can write to that terminal still can
// draw a convincing fake; see decision 0007.)
func OpenTTY() (io.ReadWriter, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/tty: %w", err)
	}
	return f, nil
}

// startResponder subscribes and answers requests in the background until the
// returned stop is called. It subscribes before returning, so a request
// published right after cannot be missed.
func startResponder(ctx context.Context, env *Env, be Backend, yes bool) (stop func(), err error) {
	ap, ok := be.(Approver)
	if !ok {
		return func() {}, fmt.Errorf("this backend cannot answer approvals")
	}
	ctx, cancel := context.WithCancel(ctx)
	ch, err := ap.WatchApprovals(ctx)
	if err != nil {
		cancel()
		return func() {}, err
	}
	r := &responder{env: env, be: ap, yes: yes}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for req := range ch {
			r.answer(ctx, req)
		}
	}()
	return func() { cancel(); <-done }, nil
}

func cmdApprovals(env *Env, args []string) int {
	var c common
	fs := newFlags(env, "approvals", "approvals [--yes] [--count N]", &c)
	yes := fs.Bool("yes", false, "TEST AFFORDANCE: allow every request without asking (defeats approval)")
	count := fs.Int("count", 0, "exit after answering N requests (0 = until interrupted)")
	if _, err := parseInterleaved(fs, args); err != nil {
		return parseFailed(err)
	}
	be := env.Backend(c.socket, c.timeout)
	defer be.Close()
	ap, ok := be.(Approver)
	if !ok {
		return fail(env, fmt.Errorf("this backend cannot answer approvals"))
	}
	ctx, stop := signalCtx()
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := ap.WatchApprovals(ctx)
	if err != nil {
		return fail(env, err)
	}
	fmt.Fprintln(env.Out, "answering approval.requested on /dev/tty (Ctrl-C to stop)")
	r := &responder{env: env, be: ap, yes: *yes}
	for n := 0; ; {
		select {
		case <-ctx.Done():
			return 0
		case req, ok := <-ch:
			if !ok {
				return 0
			}
			r.answer(ctx, req)
			if n++; *count > 0 && n >= *count {
				return 0
			}
		}
	}
}
