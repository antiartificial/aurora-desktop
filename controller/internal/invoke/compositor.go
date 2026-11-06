package invoke

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/antiartificial/aurora/controller/internal/prov"
)

// CompositorPrompt asks the compositor to draw an approval prompt on its
// trusted path (compositor/README.md, "Trusted path"). The compositor, not a
// client, draws it and reads the keystroke.
//
// What the connection proves to the compositor: this process's uid and
// cgroup (SO_PEERCRED). What the answer proves to the controller: it was made
// by a process that held the approval key (compositor_sig.go). The reply is
// HMAC-signed over the request id, the decision, the key pressed, the domain
// shown, its source and whether it differed from our claim; an unsigned, badly
// signed or replayed reply is refused (a *SignatureError) and the caller falls
// back to the bus route. This is key possession, like the powerbox ticket: any
// process that can read the key file can sign. It is not kernel authentication
// of the compositor.
//
// The controller also checks the peer (compositor_peer.go): SO_PEERCRED uid
// must be the expected one and the peer must not be inside a domain cgroup,
// else nothing is sent and Ask returns a *PeerError.
type CompositorPrompt struct {
	Socket      string
	DialTimeout time.Duration // default 1s
	// ExpectedUID is the uid the compositor must run as; nil means this
	// process's own effective uid.
	ExpectedUID *uint32
	// DenyCgroupComponents refuses a peer whose cgroup path has one of these
	// as a path component; nil means DefaultDenyCgroupComponent.
	DenyCgroupComponents []string
	// DenyCgroupPrefixes refuses a peer at or below one of these cgroup paths.
	DenyCgroupPrefixes []string
	// Key verifies replies. If nil, the file at KeyPath is loaded (and created
	// if absent) on each Ask; an unusable key refuses the compositor route.
	Key []byte
	// KeyPath defaults to ticket.ApprovalKeyPath().
	KeyPath string
	// Logf, if set, records the credentials of each accepted peer.
	Logf func(format string, args ...any)
}

// CompositorAnswer is the compositor's reply.
type CompositorAnswer struct {
	RequestID string `json:"request_id"`
	// Sig is the hex HMAC-SHA256 over the fields in replyFields.
	Sig      string `json:"sig"`
	Decision string `json:"decision"`
	Key      string `json:"key"`
	Reason   string `json:"reason"` // "key" or "timeout"
	// DomainVerified is the compositor's own check of the claimed domain
	// against busd: "true", "false" or "unknown" (empty from a compositor
	// that predates the check, which is recorded as unknown).
	DomainVerified     string `json:"domain_verified"`
	DomainVerifyDetail string `json:"domain_verify_detail"`
	// DomainSource: "bus" when the compositor learned the domain from an
	// approval.preflight event busd stamped (not from our request), "claim"
	// when it shows our word. DomainIdentity is the identity it displayed.
	// DomainMismatch is "true" when the bus's domain differs from the one we
	// claimed; the compositor shows the bus's.
	DomainSource   string `json:"domain_source"`
	DomainIdentity string `json:"domain_identity"`
	DomainMismatch string `json:"domain_mismatch"`
	Error          string `json:"error"`
}

type wireGrant struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Source string `json:"source"`
}

type wireRequest struct {
	V         int    `json:"v"`
	Op        string `json:"op"`
	RequestID string `json:"request_id"`
	App       string `json:"app"`
	Domain    string `json:"domain"`
	// DomainIdentity is the bus identity the controller says the domain has;
	// the compositor checks this against busd.
	DomainIdentity string          `json:"domain_identity"`
	Verb           string          `json:"verb"`
	Effect         string          `json:"effect"`
	Grants         []wireGrant     `json:"grants"`
	Args           json.RawMessage `json:"args"`
	TimeoutMS      int64           `json:"timeout_ms"`
}

// Ask shows req and blocks for the answer. Any error means "the compositor
// did not answer" (not running, refused us, hung up): the caller falls back
// to another route. It never means deny.
func (c *CompositorPrompt) Ask(ctx context.Context, req ApprovalRequest, wait time.Duration) (*CompositorAnswer, error) {
	dt := c.DialTimeout
	if dt <= 0 {
		dt = time.Second
	}
	key, err := c.approvalKey()
	if err != nil {
		return nil, &SignatureError{c.Socket, err.Error() + "; nothing was sent"}
	}
	conn, err := (&net.Dialer{Timeout: dt}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	peer, err := peerOf(conn)
	if err != nil {
		return nil, &PeerError{c.Socket, "cannot read peer credentials: " + err.Error()}
	}
	if err := c.checkPeer(peer); err != nil {
		return nil, err
	}
	if c.Logf != nil {
		c.Logf("compositor peer accepted: %s", peer)
	}
	w := wireRequest{V: 1, Op: "approve", RequestID: req.RequestID, App: req.App, Domain: req.DomainName, DomainIdentity: req.Domain, Verb: req.Verb,
		Effect: string(req.Effect), Args: req.Args, TimeoutMS: wait.Milliseconds()}
	for _, g := range req.Grants {
		w.Grants = append(w.Grants, wireGrant{g.Path, g.Mode, g.Source})
	}
	b, _ := json.Marshal(w)
	conn.SetDeadline(time.Now().Add(wait + 5*time.Second))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	type res struct {
		a   *CompositorAnswer
		err error
	}
	ch := make(chan res, 1)
	go func() {
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			ch <- res{nil, fmt.Errorf("compositor hung up without an answer: %w", err)}
			return
		}
		var a CompositorAnswer
		if err := json.Unmarshal(line, &a); err != nil {
			ch <- res{nil, fmt.Errorf("unreadable compositor answer: %w", err)}
			return
		}
		ch <- res{&a, nil}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if r.a.Error != "" {
			return nil, fmt.Errorf("compositor refused the request: %s", r.a.Error)
		}
		if err := verifyReply(key, r.a, req.RequestID, c.Socket); err != nil {
			return nil, err
		}
		if r.a.Decision != Allow && r.a.Decision != Deny {
			return nil, fmt.Errorf("compositor answered %q", r.a.Decision)
		}
		return r.a, nil
	case <-ctx.Done():
		conn.Close() // the compositor withdraws the overlay when we hang up
		return nil, ctx.Err()
	}
}

// compositorApproval turns an answer into the provenance record.
func compositorApproval(id string, a *CompositorAnswer, at string) *prov.Approval {
	v := a.DomainVerified
	if v != "true" && v != "false" {
		v = "unknown"
	}
	src := a.DomainSource
	if src != "bus" {
		src = "claim"
	}
	return &prov.Approval{Required: true, Route: prov.RouteCompositor, Decision: Allow, RequestID: id, By: "compositor", Key: a.Key, At: at,
		DomainVerified: v, DomainVerifyDetail: a.DomainVerifyDetail, DomainSource: src, DomainShown: a.DomainIdentity, DomainMismatch: a.DomainMismatch == "true"}
}
