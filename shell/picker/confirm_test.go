package picker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// startCompositor serves one connection at a time on a fresh socket. answer
// turns the request into the reply line to send; nil hangs up without one and
// a nil-returning hold blocks until the test ends. Every request it received
// is on the returned channel.
func startCompositor(t *testing.T, answer func(wireRequest) any) (string, <-chan wireRequest) {
	t.Helper()
	dir, err := os.MkdirTemp("", "pb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "t.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	got := make(chan wireRequest, 8)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadBytes('\n')
				if err != nil {
					return
				}
				var req wireRequest
				if json.Unmarshal(line, &req) != nil {
					return
				}
				got <- req
				if answer == nil {
					return
				}
				if a := answer(req); a != nil {
					b, _ := json.Marshal(a)
					c.Write(append(b, '\n'))
				} else {
					time.Sleep(time.Minute)
				}
			}()
		}
	}()
	return sock, got
}

var approvalKey = []byte("approval-key-approval-key-123456")

func signed(key []byte, r reply) reply {
	r.Sig = hexOf(replyMAC(key, r.fields()))
	return r
}

func hexOf(b []byte) string {
	const d = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, d[c>>4], d[c&15])
	}
	return string(out)
}

func verdict(id, decision string) reply {
	return reply{RequestID: id, Decision: decision, Key: "a", Reason: "key", DomainVerified: "unknown", DomainSource: "claim", DomainMismatch: "false"}
}

func TestReplyMACMatchesInvokeVector(t *testing.T) {
	// Computed by controller/internal/invoke.SignReply for this key and answer.
	// If this changes, the powerbox and the controller no longer agree on what
	// the compositor signs.
	r := reply{RequestID: "req-1", Decision: "allow", Key: "a", Reason: "key", DomainVerified: "unknown", DomainVerifyDetail: "d", DomainSource: "claim", DomainIdentity: "i", DomainMismatch: "false"}
	got := hexOf(replyMAC([]byte("0123456789abcdef0123456789abcdef"), r.fields()))
	if want := "5d02248c0cb3376db4180e79534f9eb3e26333c9ed506f32eb52d59f25aae3cb"; got != want {
		t.Fatalf("MAC %s, invoke makes %s", got, want)
	}
}

func TestConfirmVerdicts(t *testing.T) {
	other := []byte("some-other-key-some-other-key-12")
	cases := []struct {
		name   string
		answer func(wireRequest) any
		want   Verdict
	}{
		{"signed allow", func(q wireRequest) any { return signed(approvalKey, verdict(q.RequestID, "allow")) }, VerdictAllowed},
		{"signed deny", func(q wireRequest) any { return signed(approvalKey, verdict(q.RequestID, "deny")) }, VerdictDenied},
		{"allow signed with another key", func(q wireRequest) any { return signed(other, verdict(q.RequestID, "allow")) }, VerdictFailed},
		{"allow with no signature", func(q wireRequest) any { return verdict(q.RequestID, "allow") }, VerdictFailed},
		{"deny with no signature", func(q wireRequest) any { return verdict(q.RequestID, "deny") }, VerdictFailed},
		{"allow signed for another request", func(q wireRequest) any { return signed(approvalKey, verdict("pb-replayed", "allow")) }, VerdictFailed},
		{"deny edited to allow after signing", func(q wireRequest) any {
			r := signed(approvalKey, verdict(q.RequestID, "deny"))
			r.Decision = "allow"
			return r
		}, VerdictFailed},
		{"signature that is not hex", func(q wireRequest) any {
			r := verdict(q.RequestID, "allow")
			r.Sig = "zz"
			return r
		}, VerdictFailed},
		{"signed answer that is neither allow nor deny", func(q wireRequest) any { return signed(approvalKey, verdict(q.RequestID, "maybe")) }, VerdictFailed},
		{"compositor refuses the request", func(wireRequest) any { return reply{Error: "uid 0 is not allowed to request approvals"} }, VerdictFailed},
		{"compositor hangs up", nil, VerdictFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sock, _ := startCompositor(t, c.answer)
			got := (&Compositor{Socket: sock, Key: approvalKey}).Confirm(context.Background(), Ask{App: "a", Verb: "v", Mode: "ro", Kind: KindFile, Path: "/x"})
			if got.Verdict != c.want {
				t.Fatalf("%v (%v), want %v", got.Verdict, got.Err, c.want)
			}
			if (got.Verdict == VerdictFailed || got.Verdict == VerdictDenied) && got.Err == nil {
				t.Fatal("a refusal with no error to report")
			}
			if got.Verdict == VerdictDenied && !errors.Is(got.Err, ErrDenied) {
				t.Fatalf("deny error is %v", got.Err)
			}
		})
	}
}

func TestConfirmSendsTheResolvedGrant(t *testing.T) {
	sock, got := startCompositor(t, func(q wireRequest) any { return signed(approvalKey, verdict(q.RequestID, "allow")) })
	c := &Compositor{Socket: sock, Key: approvalKey}
	a := Ask{App: "org.x", Verb: "copy@v1", Mode: "rw", Kind: KindSave, Path: "/real/out.png", Overwrites: true}
	if v := c.Confirm(context.Background(), a); v.Verdict != VerdictAllowed {
		t.Fatal(v.Err)
	}
	q := <-got
	if q.V != 1 || q.Op != "approve" || q.App != "org.x" || q.Verb != "copy@v1" || q.TimeoutMS <= 0 ||
		len(q.Grants) != 1 || q.Grants[0] != (wireGrant{"/real/out.png", "rw", "powerbox"}) || !q.Args.Overwrites || q.Args.Kind != KindSave {
		t.Fatalf("%+v", q)
	}
	first := q.RequestID
	c.Confirm(context.Background(), a)
	if second := (<-got).RequestID; first == "" || second == first {
		t.Fatalf("request ids %q then %q: must be fresh", first, second)
	}
}

func TestConfirmFailsWhenTheCompositorIsUnusable(t *testing.T) {
	t.Run("socket missing", func(t *testing.T) {
		c := &Compositor{Socket: filepath.Join(t.TempDir(), "none.sock"), Key: approvalKey}
		if v := c.Confirm(context.Background(), Ask{}); v.Verdict != VerdictFailed {
			t.Fatalf("%v", v.Verdict)
		}
	})
	t.Run("no answer in time", func(t *testing.T) {
		sock, _ := startCompositor(t, func(wireRequest) any { return nil })
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		if v := (&Compositor{Socket: sock, Key: approvalKey}).Confirm(ctx, Ask{}); v.Verdict != VerdictFailed {
			t.Fatalf("%v", v.Verdict)
		}
	})
	t.Run("peer is another uid: nothing is sent", func(t *testing.T) {
		sock, got := startCompositor(t, func(q wireRequest) any { return signed(approvalKey, verdict(q.RequestID, "allow")) })
		uid := uint32(os.Geteuid()) + 1
		v := (&Compositor{Socket: sock, Key: approvalKey, ExpectedUID: &uid}).Confirm(context.Background(), Ask{})
		if v.Verdict != VerdictFailed {
			t.Fatalf("%v", v.Verdict)
		}
		select {
		case q := <-got:
			t.Fatalf("request reached an impostor: %+v", q)
		case <-time.After(100 * time.Millisecond):
		}
	})
	t.Run("approval key unusable: nothing is sent", func(t *testing.T) {
		sock, got := startCompositor(t, func(q wireRequest) any { return signed(approvalKey, verdict(q.RequestID, "allow")) })
		loose := filepath.Join(t.TempDir(), "approval.key")
		os.WriteFile(loose, approvalKey, 0o644)
		t.Setenv("AURORA_APPROVAL_KEY", loose)
		if v := (&Compositor{Socket: sock}).Confirm(context.Background(), Ask{}); v.Verdict != VerdictFailed {
			t.Fatalf("%v", v.Verdict)
		}
		select {
		case q := <-got:
			t.Fatalf("request sent although replies could not be verified: %+v", q)
		case <-time.After(100 * time.Millisecond):
		}
	})
}

func TestAttestPolicy(t *testing.T) {
	good, _ := startCompositor(t, func(q wireRequest) any { return signed(approvalKey, verdict(q.RequestID, "allow")) })
	rtWith := func(sock string) string { // an XDG_RUNTIME_DIR whose default socket is sock
		rt, _ := os.MkdirTemp("", "rt")
		t.Cleanup(func() { os.RemoveAll(rt) })
		if sock != "" {
			os.Symlink(sock, filepath.Join(rt, "aurora-trusted.sock"))
		}
		return rt
	}
	missing := filepath.Join(t.TempDir(), "gone.sock")
	cases := []struct {
		name        string
		policy      AttestPolicy
		flag        string
		env         map[string]string
		wantBuildOK bool
		want        Verdict // of Confirm, when it builds
	}{
		{"off skips even with a working socket", AttestOff, good, nil, true, VerdictUnattested},
		{"flag socket is asked", AttestAuto, good, nil, true, VerdictAllowed},
		{"env socket is asked", AttestAuto, "", map[string]string{"AURORA_TRUSTED_SOCK": good}, true, VerdictAllowed},
		{"flag wins over env", AttestAuto, good, map[string]string{"AURORA_TRUSTED_SOCK": missing}, true, VerdictAllowed},
		{"default path socket is asked", AttestAuto, "", map[string]string{"XDG_RUNTIME_DIR": rtWith(good)}, true, VerdictAllowed},
		{"configured but missing is a failure, not a skip", AttestAuto, missing, nil, true, VerdictFailed},
		{"env configured but missing is a failure, not a skip", AttestAuto, "", map[string]string{"AURORA_TRUSTED_SOCK": missing}, true, VerdictFailed},
		{"nothing configured or present: auto skips", AttestAuto, "", map[string]string{"XDG_RUNTIME_DIR": rtWith("")}, true, VerdictUnattested},
		{"nothing configured or present: required refuses to start", AttestRequired, "", map[string]string{"XDG_RUNTIME_DIR": rtWith("")}, false, 0},
		{"required with a socket asks", AttestRequired, good, nil, true, VerdictAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := OSEnv()
			e.Getenv = func(k string) string { return c.env[k] }
			cf, err := NewConfirmer(c.policy, c.flag, e, time.Second)
			if (err == nil) != c.wantBuildOK {
				t.Fatalf("NewConfirmer error %v", err)
			}
			if err != nil {
				return
			}
			if c, ok := cf.(*Compositor); ok {
				c.Key = approvalKey
			}
			if v := cf.Confirm(context.Background(), Ask{Path: "/x"}); v.Verdict != c.want {
				t.Fatalf("%v (%v), want %v", v.Verdict, v.Err, c.want)
			}
		})
	}
	if _, err := ParseAttestPolicy("sometimes"); err == nil {
		t.Fatal("unknown policy accepted")
	}
}
