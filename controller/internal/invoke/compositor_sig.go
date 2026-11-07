package invoke

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/antiartificial/aurora/controller/ticket"
)

// SignatureError means the compositor's reply did not carry a valid signature
// for the request just made. The reply is discarded whatever it said: an
// unsigned "allow" is the same as no answer. Callers fall back to the bus
// route and log it loudly.
type SignatureError struct {
	Socket string
	Reason string
}

func (e *SignatureError) Error() string {
	return fmt.Sprintf("reply on %s is not signed by the compositor: %s", e.Socket, e.Reason)
}

// replyLabel is part of the MAC input, so an approval signature cannot be
// confused with any other HMAC made with a key that happens to be the same.
const replyLabel = "aurora-approval-reply-v1\x00"

// replyFields is what the signature covers, in order: the request id, the
// decision, the key pressed, why, what the compositor found about the domain,
// where the domain shown came from, the identity shown, whether it differed
// from what we claimed, and the SHA-256 of the request line we sent. Each is
// length prefixed, so no two different decisions have the same encoding.
// compositor/src/trusted/sign.rs is the signing side.
//
// The digest is last because it was added last, and the order is the wire
// format. It is the field that makes a verified answer say WHICH request was
// answered: without it the signature covers an id we chose and nothing about
// the verb, the effect or the grants the user was actually shown.
func (a *CompositorAnswer) replyFields() []string {
	return []string{a.RequestID, a.Decision, a.Key, a.Reason, a.DomainVerified, a.DomainVerifyDetail, a.DomainSource, a.DomainIdentity, a.DomainMismatch, a.requestSHA256}
}

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

// SignReply is what a compositor does; the controller has it so tests (and any
// stand-in compositor) sign exactly as the real one does.
func SignReply(key []byte, a CompositorAnswer) string {
	return hex.EncodeToString(replyMAC(key, a.replyFields()))
}

// verifyReply checks that a carries a valid signature, and that it is for
// requestID (a signature replayed from another request names another id).
func verifyReply(key []byte, a *CompositorAnswer, requestID, socket string) error {
	if a.Sig == "" {
		return &SignatureError{socket, "the reply has no signature"}
	}
	sig, err := hex.DecodeString(a.Sig)
	if err != nil {
		return &SignatureError{socket, "the signature is not hex"}
	}
	// The id is compared BEFORE the MAC, for the error message and nothing else. Both
	// refuse, and the MAC alone would now catch a replay anyway, since the signed fields
	// include the digest of the request we sent. But "signed for request X, not Y" names
	// what happened and "the signature does not verify" does not, and a replay is the case
	// worth naming.
	if a.RequestID != requestID {
		return &SignatureError{socket, fmt.Sprintf("the reply is signed for request %q, not %q (replayed)", a.RequestID, requestID)}
	}
	if !hmac.Equal(sig, replyMAC(key, a.replyFields())) {
		return &SignatureError{socket, "the signature does not verify (wrong key, a field was changed after signing, or it answers a different request)"}
	}
	return nil
}

// approvalKey is the key replies are verified with: Key if set, else the file
// at KeyPath (default ticket.ApprovalKeyPath), created if neither process has
// made it yet and refused unless it is a 0600 regular file we own.
func (c *CompositorPrompt) approvalKey() ([]byte, error) {
	if len(c.Key) > 0 {
		return c.Key, nil
	}
	p := c.KeyPath
	if p == "" {
		p = ticket.ApprovalKeyPath()
	}
	k, err := ticket.LoadKey(p, true)
	if err != nil {
		return nil, fmt.Errorf("approval key %s unusable: %w", p, err)
	}
	return k, nil
}
