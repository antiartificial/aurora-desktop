// Package ticket is the grant ticket of the powerbox v0: a short-lived,
// single-use, HMAC-authenticated statement "the user picked THIS path, in THIS
// mode, for THIS app and verb". The picker mints it, the controller verifies
// and redeems it, and the launcher in between treats it as an opaque string.
//
// What it guarantees and what it does not is in contracts/decisions/0007. In
// short: it is integrity and binding, not authentication of a human. Anything
// that can read the key file can mint a ticket for any path.
package ticket

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// DefaultTTL is how long a ticket lives unless the picker is told otherwise.
const DefaultTTL = 60 * time.Second

// Claims is what a ticket asserts.
type Claims struct {
	V     int    `json:"v"`
	Path  string `json:"path"` // exact resolved path
	Mode  string `json:"mode"` // "ro" or "rw"
	App   string `json:"app"`
	Verb  string `json:"verb"`
	Nonce string `json:"nonce"`
	Exp   int64  `json:"exp"` // unix seconds
}

// Error codes. The controller returns them as the registry error code.
const (
	CodeMalformed    = "ticket_malformed"
	CodeBadSignature = "ticket_bad_signature"
	CodeExpired      = "ticket_expired"
	CodeReused       = "ticket_reused"
	CodeWrongApp     = "ticket_wrong_app"
	CodeWrongVerb    = "ticket_wrong_verb"
	CodeWrongMode    = "ticket_wrong_mode"
	CodeWrongPath    = "ticket_wrong_path"
)

// Error is a refusal with a stable code.
type Error struct{ Code, Msg string }

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

func refuse(code, msg string) *Error { return &Error{code, msg} }

// Mint signs a ticket valid for ttl from now. The caller must already have
// resolved c.Path to its canonical form; Mint does not touch the filesystem.
func Mint(key []byte, c Claims, now time.Time, ttl time.Duration) (string, error) {
	if len(key) < 32 {
		return "", errors.New("ticket key too short")
	}
	if c.Path == "" || c.App == "" || c.Verb == "" || (c.Mode != "ro" && c.Mode != "rw") {
		return "", errors.New("ticket needs path, app, verb and mode ro|rw")
	}
	var n [16]byte
	if _, err := rand.Read(n[:]); err != nil {
		return "", err
	}
	c.V, c.Nonce, c.Exp = 1, base64.RawURLEncoding.EncodeToString(n[:]), now.Add(ttl).Unix()
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return enc(body) + "." + enc(mac(key, body)), nil
}

func enc(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func mac(key, body []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte("aurora-powerbox-ticket-v1\x00"))
	h.Write(body)
	return h.Sum(nil)
}

// Verify checks the signature and the expiry and returns the claims. It does
// not check bindings (see Claims.Check) and does not spend the ticket.
func Verify(key []byte, tok string, now time.Time) (Claims, error) {
	var c Claims
	b, s, ok := strings.Cut(tok, ".")
	if !ok {
		return c, refuse(CodeMalformed, "not a ticket")
	}
	body, err1 := base64.RawURLEncoding.DecodeString(b)
	sig, err2 := base64.RawURLEncoding.DecodeString(s)
	if err1 != nil || err2 != nil {
		return c, refuse(CodeMalformed, "not a ticket")
	}
	if !hmac.Equal(sig, mac(key, body)) {
		return c, refuse(CodeBadSignature, "signature does not verify: tampered, or minted with another key")
	}
	if err := json.Unmarshal(body, &c); err != nil || c.V != 1 || c.Nonce == "" {
		return Claims{}, refuse(CodeMalformed, "unreadable claims")
	}
	if !now.Before(time.Unix(c.Exp, 0)) {
		return c, refuse(CodeExpired, "expired at "+time.Unix(c.Exp, 0).UTC().Format(time.RFC3339))
	}
	return c, nil
}

// Want is what a request needs the ticket to cover.
type Want struct{ Path, Mode, App, Verb string }

// Check refuses unless the claims are exactly for w. Order: app, verb, mode,
// path.
func (c Claims) Check(w Want) error {
	switch {
	case c.App != w.App:
		return refuse(CodeWrongApp, "ticket is for app "+c.App+", not "+w.App)
	case c.Verb != w.Verb:
		return refuse(CodeWrongVerb, "ticket is for verb "+c.Verb+", not "+w.Verb)
	case c.Mode != w.Mode:
		return refuse(CodeWrongMode, "ticket is for mode "+c.Mode+", not "+w.Mode)
	case c.Path != w.Path:
		return refuse(CodeWrongPath, "ticket is for "+c.Path+", not "+w.Path)
	}
	return nil
}

// Ledger remembers redeemed nonces until they would have expired anyway. It
// is in memory: a controllerd restart forgets it, so a ticket spent before a
// restart is accepted again if still within its (short) lifetime.
type Ledger struct {
	mu    sync.Mutex
	spent map[string]int64
}

// Redeem spends all the tickets, or none: if any was already spent it spends
// nothing and returns ticket_reused.
func (l *Ledger) Redeem(now time.Time, cs ...Claims) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.spent == nil {
		l.spent = map[string]int64{}
	}
	for n, exp := range l.spent {
		if exp <= now.Unix() {
			delete(l.spent, n)
		}
	}
	seen := map[string]bool{}
	for _, c := range cs {
		if _, dup := l.spent[c.Nonce]; dup || seen[c.Nonce] {
			return refuse(CodeReused, "ticket was already used")
		}
		seen[c.Nonce] = true
	}
	for _, c := range cs {
		l.spent[c.Nonce] = c.Exp
	}
	return nil
}
