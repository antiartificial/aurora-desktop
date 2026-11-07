package picker

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/antiartificial/aurora/controller/ticket"
)

// Verdict is how the attested confirm ended. Nothing else decides whether a
// ticket is minted.
type Verdict int

const (
	// VerdictAllowed: the compositor answered allow and the signature verified
	// for this request.
	VerdictAllowed Verdict = iota
	// VerdictDenied: the compositor answered deny (a key, or its timeout) and
	// the signature verified.
	VerdictDenied
	// VerdictUnattested: nobody was asked, because the caller chose that
	// (--attest=off) or because no compositor exists in this session and the
	// caller did not demand one. Always announced on stderr.
	VerdictUnattested
	// VerdictFailed: the compositor could not be asked, or its reply cannot be
	// believed (unreachable, refused us, wrong peer, bad or replayed
	// signature, no answer in time).
	VerdictFailed
)

func (v Verdict) String() string {
	switch v {
	case VerdictAllowed:
		return "allowed"
	case VerdictDenied:
		return "denied"
	case VerdictUnattested:
		return "unattested"
	case VerdictFailed:
		return "failed"
	}
	return fmt.Sprintf("Verdict(%d)", int(v))
}

// Confirmation is a Verdict and what it needs to be explained. Why is set for
// VerdictUnattested; Err for VerdictDenied and VerdictFailed.
type Confirmation struct {
	Verdict Verdict
	Why     string
	Err     error
}

// ErrDenied is the error behind VerdictDenied.
var ErrDenied = errors.New("denied at the compositor: nothing was granted")

// Ask is what the compositor is shown: the grant about to be made. Path is
// the resolved path, the one the ticket will bind.
type Ask struct {
	App, Verb, Mode, Kind, Path string
	Overwrites                  bool // KindSave onto a file that exists
}

// Confirmer asks for the second yes. Implementations: Compositor (the real
// one) and Unattested (an explicit, announced skip).
type Confirmer interface {
	Confirm(ctx context.Context, a Ask) Confirmation
}

// Unattested is the skip. Confirm always returns VerdictUnattested.
type Unattested struct{ Why string }

func (u Unattested) Confirm(context.Context, Ask) Confirmation {
	return Confirmation{Verdict: VerdictUnattested, Why: u.Why}
}

// AttestPolicy is what the caller demanded of attestation.
type AttestPolicy int

const (
	// AttestAuto: attest when a trusted socket is configured (--approval-socket,
	// $AURORA_TRUSTED_SOCK) or sits at the default path. A configured or
	// discovered socket that does not work is a failure. With no socket at all
	// there is no compositor to ask, so the grant is minted unattested, loudly.
	AttestAuto AttestPolicy = iota
	// AttestRequired: as auto, and no socket at all is a failure too.
	AttestRequired
	// AttestOff: never ask. The explicit opt-out.
	AttestOff
)

// ParseAttestPolicy reads the --attest flag.
func ParseAttestPolicy(s string) (AttestPolicy, error) {
	switch s {
	case "auto", "":
		return AttestAuto, nil
	case "required":
		return AttestRequired, nil
	case "off":
		return AttestOff, nil
	}
	return 0, fmt.Errorf("--attest %q: want auto, required or off", s)
}

// DefaultApprovalTimeout is how long the compositor is given to get an answer.
const DefaultApprovalTimeout = 60 * time.Second

// NewConfirmer applies the policy. socket is the --approval-socket flag.
//
// The socket path follows the controller's convention (controller/internal/
// config.TrustedSocket, internal and so not importable here): the flag, then
// $AURORA_TRUSTED_SOCK, then $XDG_RUNTIME_DIR/aurora-trusted.sock. The first
// two are "configured"; the last counts only if a socket is there.
func NewConfirmer(p AttestPolicy, socket string, e Env, timeout time.Duration) (Confirmer, error) {
	if p == AttestOff {
		return Unattested{Why: "--attest=off"}, nil
	}
	path, configured := socket, socket != ""
	if !configured {
		path = e.Getenv("AURORA_TRUSTED_SOCK")
		configured = path != ""
	}
	if !configured {
		rt := e.Getenv("XDG_RUNTIME_DIR")
		if rt == "" {
			rt = os.TempDir()
		}
		path = filepath.Join(rt, "aurora-trusted.sock")
		if !e.IsSocket(path) {
			if p == AttestRequired {
				return nil, fmt.Errorf("--attest=required but there is no compositor trusted socket (none configured, none at %s)", path)
			}
			return Unattested{Why: "no compositor trusted socket is configured or at " + path}, nil
		}
	}
	return &Compositor{Socket: path, Timeout: timeout}, nil
}

// Compositor asks the compositor's trusted-path socket to approve a grant.
//
// This is a small client of the same protocol as controller/internal/invoke
// (CompositorPrompt.Ask, compositor_sig.go, compositor_peer.go). That package
// is under controller/internal, which Go does not let this module import, so
// the request, the reply MAC and the peer check are written out again here.
// The MAC input must stay identical to invoke.replyMAC and to the signing side
// in compositor/src/trusted/sign.rs; TestReplyMACMatchesInvokeVector pins the
// bytes against a value computed by invoke.SignReply. The way to end the
// duplication is a public controller/approval package that both import.
//
// What a verified allow proves: something that held the approval key answered
// this request id with allow. It is key possession, not kernel authentication
// of the compositor, and the MAC does not cover the grant that was shown. See
// the report on the attestation design.
type Compositor struct {
	Socket  string
	Timeout time.Duration // default DefaultApprovalTimeout
	// Key verifies replies. If nil, ticket.ApprovalKeyPath() is loaded (and
	// created if absent, as the controller does) on each Confirm.
	Key []byte
	// ExpectedUID is the uid the compositor must run as; nil means our own.
	ExpectedUID *uint32
	// NewID makes the request id; nil means 16 random bytes in hex.
	NewID func() string
}

func (c *Compositor) Confirm(ctx context.Context, a Ask) Confirmation {
	fail := func(f string, args ...any) Confirmation {
		return Confirmation{Verdict: VerdictFailed, Err: fmt.Errorf("attestation at %s: "+f, append([]any{c.Socket}, args...)...)}
	}
	key := c.Key
	if len(key) == 0 {
		p := ticket.ApprovalKeyPath()
		k, err := ticket.LoadKey(p, true)
		if err != nil {
			return fail("approval key %s unusable: %v; nothing was sent", p, err)
		}
		key = k
	}
	wait := c.Timeout
	if wait <= 0 {
		wait = DefaultApprovalTimeout
	}
	id := newRequestID()
	if c.NewID != nil {
		id = c.NewID()
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return fail("%v", err)
	}
	defer conn.Close()
	if err := checkPeer(conn, c.ExpectedUID); err != nil {
		return fail("%v; nothing was sent", err)
	}
	req, _ := json.Marshal(wireRequest{V: 1, Op: "approve", RequestID: id, App: a.App, Verb: a.Verb,
		Effect: "powerbox grant", Grants: []wireGrant{{Path: a.Path, Mode: a.Mode, Source: "powerbox"}},
		Args: wireArgs{Kind: a.Kind, Overwrites: a.Overwrites}, TimeoutMS: wait.Milliseconds()})
	conn.SetDeadline(time.Now().Add(wait + 5*time.Second))
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return fail("%v", err)
	}
	type read struct {
		r   reply
		err error
	}
	ch := make(chan read, 1)
	go func() {
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			ch <- read{err: fmt.Errorf("the compositor hung up without an answer: %w", err)}
			return
		}
		var r reply
		if err := json.Unmarshal(line, &r); err != nil {
			ch <- read{err: fmt.Errorf("unreadable answer: %w", err)}
			return
		}
		ch <- read{r: r}
	}()
	select {
	case <-ctx.Done():
		return fail("%v", ctx.Err())
	case rd := <-ch:
		if rd.err != nil {
			return fail("%v", rd.err)
		}
		return judge(key, id, rd.r, c.Socket)
	}
}

// judge turns a reply into a verdict. The signature and the request id are
// checked before the decision is looked at, so an unsigned or replayed
// "allow" and an unsigned "deny" are both failures.
func judge(key []byte, id string, r reply, socket string) Confirmation {
	bad := func(f string, args ...any) Confirmation {
		return Confirmation{Verdict: VerdictFailed, Err: fmt.Errorf("attestation at %s: "+f, append([]any{socket}, args...)...)}
	}
	if r.Error != "" {
		return bad("the compositor refused the request: %s", r.Error)
	}
	if r.Sig == "" {
		return bad("the reply has no signature")
	}
	sig, err := hex.DecodeString(r.Sig)
	if err != nil {
		return bad("the signature is not hex")
	}
	if !hmac.Equal(sig, replyMAC(key, r.fields())) {
		return bad("the signature does not verify (wrong key, or a field was changed after signing)")
	}
	if r.RequestID != id {
		return bad("the reply is signed for request %q, not %q (replayed)", r.RequestID, id)
	}
	switch r.Decision {
	case "allow":
		return Confirmation{Verdict: VerdictAllowed}
	case "deny":
		return Confirmation{Verdict: VerdictDenied, Err: ErrDenied}
	}
	return bad("the compositor answered %q", r.Decision)
}

type wireGrant struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Source string `json:"source"`
}

type wireArgs struct {
	Kind       string `json:"kind"`
	Overwrites bool   `json:"overwrites,omitempty"`
}

type wireRequest struct {
	V         int         `json:"v"`
	Op        string      `json:"op"`
	RequestID string      `json:"request_id"`
	App       string      `json:"app"`
	Verb      string      `json:"verb"`
	Effect    string      `json:"effect"`
	Grants    []wireGrant `json:"grants"`
	Args      wireArgs    `json:"args"`
	TimeoutMS int64       `json:"timeout_ms"`
}

type reply struct {
	RequestID          string `json:"request_id"`
	Sig                string `json:"sig"`
	Decision           string `json:"decision"`
	Key                string `json:"key"`
	Reason             string `json:"reason"`
	DomainVerified     string `json:"domain_verified"`
	DomainVerifyDetail string `json:"domain_verify_detail"`
	DomainSource       string `json:"domain_source"`
	DomainIdentity     string `json:"domain_identity"`
	DomainMismatch     string `json:"domain_mismatch"`
	Error              string `json:"error"`
}

// fields is what the signature covers, in order. Same as invoke's
// CompositorAnswer.replyFields.
func (r reply) fields() []string {
	return []string{r.RequestID, r.Decision, r.Key, r.Reason, r.DomainVerified, r.DomainVerifyDetail, r.DomainSource, r.DomainIdentity, r.DomainMismatch}
}

// replyLabel and replyMAC are invoke.replyLabel and invoke.replyMAC, copied
// because they are unexported in a package this module cannot import.
const replyLabel = "aurora-approval-reply-v1\x00"

func replyMAC(key []byte, fields []string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(replyLabel))
	for _, f := range fields {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(f)))
		h.Write(n[:])
		h.Write([]byte(f))
	}
	return h.Sum(nil)
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "pb-" + hex.EncodeToString(b[:])
}

// defaultDenyCgroupComponent is the parent of every domain cgroup
// (invoke.DefaultDenyCgroupComponent). A compositor is a host process; a peer
// inside a domain is a sandboxed app.
const defaultDenyCgroupComponent = "aurora-domains"

// checkPeer is invoke's checkPeerPolicy with the default deny list: the
// socket's owner (SO_PEERCRED) must have the expected uid, a readable cgroup,
// and not be inside a domain. It fails closed. It does not rule out another
// host process of the same user that bound the socket first.
func checkPeer(conn net.Conn, expected *uint32) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("not a unix connection")
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var serr error
	if err := rc.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if serr != nil {
		return fmt.Errorf("cannot read peer credentials: %w", serr)
	}
	want := os.Geteuid()
	if expected != nil {
		want = int(*expected)
	}
	if int(cred.Uid) != want {
		return fmt.Errorf("peer uid is %d, expected %d (pid %d)", cred.Uid, want, cred.Pid)
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(int(cred.Pid)) + "/cgroup")
	if err != nil {
		return fmt.Errorf("cannot read the cgroup of pid %d, so cannot show it is outside a domain", cred.Pid)
	}
	cg := ""
	for _, l := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(l, "0::"); ok {
			cg = p
		}
	}
	if cg == "" {
		return fmt.Errorf("pid %d has no cgroup v2 path, so cannot show it is outside a domain", cred.Pid)
	}
	for _, comp := range strings.Split(cg, "/") {
		if comp == defaultDenyCgroupComponent {
			return fmt.Errorf("peer pid %d is inside a domain cgroup (%s)", cred.Pid, cg)
		}
	}
	return nil
}
