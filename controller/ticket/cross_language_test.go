package ticket

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The ticket format crosses a language boundary. The compositor mints in Rust
// (compositor/src/trusted/ticket.rs) under a key only it holds, and we verify here. A
// divergence of a single byte does not present as a protocol break: Verify returns
// ticket_bad_signature, which reads like the two sides were handed different keys.
//
// So the vector is pinned on both sides rather than described. This file is the authority,
// because Verify and json.Marshal are what the Rust emitter has to agree WITH. The Rust
// assertions are cross_language::a_minted_ticket_is_byte_identical_to_gos and
// cross_language::the_claim_order_is_gos_declaration_order.
//
// The path carries a quote, a backslash and the three characters encoding/json HTML-escapes by
// default, because each is a place a hand-written emitter diverges without saying so.
const (
	vecKey   = "aurora-ticket-test-key-012345678"
	vecPath  = `/tmp/a"b\c&d<e>f.png`
	vecNonce = "AAECAwQFBgcICQoLDA0ODw"
	vecExp   = 1762500000
	vecToken = "eyJ2IjoxLCJwYXRoIjoiL3RtcC9hXCJiXFxjXHUwMDI2ZFx1MDAzY2VcdTAwM2VmLnBuZyIsIm1vZGUiOiJybyIsImFwcCI6Im9yZy5hdXJvcmEubHVtZW4iLCJ2ZXJiIjoicmVzaXplQHYxIiwibm9uY2UiOiJBQUVDQXdRRkJnY0lDUW9MREEwT0R3IiwiZXhwIjoxNzYyNTAwMDAwfQ.Umq0y6qWMSG12rlsO3XzHZQSDInNAfO2pqRR9NYVE20"
)

func vecClaims() Claims {
	return Claims{
		V: 1, Path: vecPath, Mode: "ro",
		App: "org.aurora.lumen", Verb: "resize@v1",
		Nonce: vecNonce, Exp: vecExp,
	}
}

// What the compositor mints is a ticket this controller accepts, and the claims it reads back
// are the ones that were signed. Byte identity of the body is asserted separately; this is the
// end the rest of the system depends on.
func TestCompositorVectorVerifies(t *testing.T) {
	now := time.Unix(vecExp-1, 0)
	c, err := Verify([]byte(vecKey), vecToken, now)
	if err != nil {
		t.Fatalf("the compositor's ticket does not verify here: %v", err)
	}
	if c != vecClaims() {
		t.Fatalf("claims differ:\n got %+v\nwant %+v", c, vecClaims())
	}
	if err := c.Check(Want{Path: vecPath, Mode: "ro", App: "org.aurora.lumen", Verb: "resize@v1"}); err != nil {
		t.Fatalf("verified claims fail their own binding check: %v", err)
	}
}

// The body half of the vector is exactly json.Marshal of these claims. This is what pins field
// ORDER, which is the failure nobody would guess at: the MAC covers the serialised bytes and
// encoding/json emits struct fields in declaration order, so reordering the Rust emitter keeps
// every claim and every value and still invalidates every ticket.
func TestClaimsMarshalMatchesTheCompositorVector(t *testing.T) {
	body, err := json.Marshal(vecClaims())
	if err != nil {
		t.Fatal(err)
	}
	wantBody, _, _ := strings.Cut(vecToken, ".")
	if got := base64.RawURLEncoding.EncodeToString(body); got != wantBody {
		decoded, _ := base64.RawURLEncoding.DecodeString(wantBody)
		t.Fatalf("serialised claims differ from the compositor's:\n go   %s\n rust %s", body, decoded)
	}
}

// A ticket the compositor minted is still subject to expiry here. Without this the vector
// above could be satisfied by a Verify that had stopped checking anything.
func TestCompositorVectorStillExpires(t *testing.T) {
	if _, err := Verify([]byte(vecKey), vecToken, time.Unix(vecExp, 0)); err == nil {
		t.Fatal("a ticket at its own expiry was accepted")
	} else if e, ok := err.(*Error); !ok || e.Code != CodeExpired {
		t.Fatalf("want %s, got %v", CodeExpired, err)
	}
}

// And a ticket minted under any other key is refused, which is the whole reason the compositor
// holding the key alone is worth something.
func TestCompositorVectorUnderAnotherKeyIsRefused(t *testing.T) {
	other := "aurora-ticket-test-key-012345679"
	if _, err := Verify([]byte(other), vecToken, time.Unix(vecExp-1, 0)); err == nil {
		t.Fatal("a ticket verified under a key that did not mint it")
	} else if e, ok := err.(*Error); !ok || e.Code != CodeBadSignature {
		t.Fatalf("want %s, got %v", CodeBadSignature, err)
	}
}
