package ticket

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var (
	key  = []byte("0123456789abcdef0123456789abcdef")
	t0   = time.Unix(1_000_000, 0)
	base = Claims{Path: "/tmp/a.png", Mode: "ro", App: "org.x", Verb: "copy@v1"}
)

func code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestRoundTrip(t *testing.T) {
	tok, err := Mint(key, base, t0, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verify(key, tok, t0.Add(59*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Check(Want{base.Path, base.Mode, base.App, base.Verb}); err != nil {
		t.Fatal(err)
	}
	if c.Path != base.Path || c.Nonce == "" {
		t.Fatalf("claims %+v", c)
	}
}

func TestRefusals(t *testing.T) {
	tok, _ := Mint(key, base, t0, time.Minute)
	if _, err := Verify(key, tok, t0.Add(time.Minute)); code(err) != CodeExpired {
		t.Fatalf("expiry: %v", err)
	}
	body, sig, _ := strings.Cut(tok, ".")
	forged := body + "." + strings.Repeat("A", len(sig))
	for name, bad := range map[string]string{"sig": forged, "other key": "", "junk": "nope", "bits": "!!.!!"} {
		k := key
		if name == "other key" {
			k = []byte("ffffffffffffffffffffffffffffffff")
			bad = tok
		}
		_, err := Verify(k, bad, t0)
		want := CodeBadSignature
		if name == "junk" || name == "bits" {
			want = CodeMalformed
		}
		if code(err) != want {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A different body with the original signature is a tamper.
	other, _ := Mint(key, Claims{Path: "/etc/shadow", Mode: "ro", App: "org.x", Verb: "copy@v1"}, t0, time.Minute)
	ob, _, _ := strings.Cut(other, ".")
	if _, err := Verify(key, ob+"."+sig, t0); code(err) != CodeBadSignature {
		t.Fatalf("spliced: %v", err)
	}
	c, _ := Verify(key, tok, t0)
	for _, w := range []struct {
		want Want
		code string
	}{
		{Want{"/tmp/b.png", "ro", "org.x", "copy@v1"}, CodeWrongPath},
		{Want{"/tmp/a.png", "rw", "org.x", "copy@v1"}, CodeWrongMode},
		{Want{"/tmp/a.png", "ro", "org.y", "copy@v1"}, CodeWrongApp},
		{Want{"/tmp/a.png", "ro", "org.x", "del@v1"}, CodeWrongVerb},
	} {
		if err := c.Check(w.want); code(err) != w.code {
			t.Fatalf("%+v: %v", w.want, err)
		}
	}
}

func TestMintRefusesIncomplete(t *testing.T) {
	if _, err := Mint(key, Claims{Path: "/x", Mode: "rx", App: "a", Verb: "v"}, t0, time.Minute); err == nil {
		t.Fatal("bad mode accepted")
	}
	if _, err := Mint([]byte("short"), base, t0, time.Minute); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestLedgerSingleUseAndAtomic(t *testing.T) {
	var l Ledger
	a, _ := Mint(key, base, t0, time.Minute)
	b, _ := Mint(key, base, t0, time.Minute)
	ca, _ := Verify(key, a, t0)
	cb, _ := Verify(key, b, t0)
	if err := l.Redeem(t0, ca); err != nil {
		t.Fatal(err)
	}
	if err := l.Redeem(t0, cb, ca); code(err) != CodeReused {
		t.Fatalf("reuse: %v", err)
	}
	if err := l.Redeem(t0, cb); err != nil {
		t.Fatalf("a failed redeem must spend nothing: %v", err)
	}
	if err := l.Redeem(t0, cb); code(err) != CodeReused {
		t.Fatal("second redeem of cb")
	}
}

func TestKeyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "k")
	k1, err := LoadKey(p, true)
	if err != nil || len(k1) != 32 {
		t.Fatal(err)
	}
	k2, _ := LoadKey(p, false)
	if string(k1) != string(k2) {
		t.Fatal("key changed")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
	os.Chmod(p, 0o644)
	if _, err := LoadKey(p, false); err == nil {
		t.Fatal("loose key file accepted")
	}
	if _, err := LoadKey(filepath.Join(t.TempDir(), "none"), false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
}
