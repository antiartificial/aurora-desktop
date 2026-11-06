package buslog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// chained writes n records across several segments and returns the dir.
func chained(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	l, err := Open(dir, 400)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := l.Append(at(time.Now(), fmt.Sprintf("t.%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	l.Close()
	if ps, _ := segmentPaths(dir); len(ps) < 3 {
		t.Fatalf("want several segments, got %d", len(ps))
	}
	return dir
}

func rewrite(t *testing.T, path string, f func([]string) []string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ls := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	out := strings.Join(f(ls), "\n")
	if out != "" {
		out += "\n"
	}
	os.WriteFile(path, []byte(out), 0o600)
}

func TestChainVerifiesAcrossSegmentsAndReopen(t *testing.T) {
	dir := chained(t, 20)
	l, _ := Open(dir, 400) // reopening continues the chain
	l.Append(at(time.Now(), "t.more"))
	l.Close()
	r, err := Verify(dir, "")
	if err != nil || r.Status != ChainVerified || r.Chained != 21 || !r.Anchored {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestChainDetectsTampering(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"delete record": func(t *testing.T, d string) {
			rewrite(t, filepath.Join(d, "seg-000002.jsonl"), func(l []string) []string { return append(l[:1], l[2:]...) })
		},
		"delete last record": func(t *testing.T, d string) {
			ps, _ := segmentPaths(d)
			rewrite(t, ps[len(ps)-1], func(l []string) []string { return l[:len(l)-1] })
		},
		"delete last segment": func(t *testing.T, d string) {
			ps, _ := segmentPaths(d)
			os.Remove(ps[len(ps)-1])
		},
		"delete first segment":  func(t *testing.T, d string) { os.Remove(filepath.Join(d, "seg-000001.jsonl")) },
		"delete middle segment": func(t *testing.T, d string) { os.Remove(filepath.Join(d, "seg-000002.jsonl")) },
		"reorder": func(t *testing.T, d string) {
			rewrite(t, filepath.Join(d, "seg-000002.jsonl"), func(l []string) []string { l[0], l[1] = l[1], l[0]; return l })
		},
		"edit": func(t *testing.T, d string) {
			rewrite(t, filepath.Join(d, "seg-000002.jsonl"), func(l []string) []string {
				l[0] = strings.Replace(l[0], `"from"`, `"x":1,"from"`, 1)
				l[0] = strings.Replace(l[0], `"kind":"event"`, `"kind":"event","verb":"v"`, 1)
				return l
			})
		},
		"edit last record": func(t *testing.T, d string) {
			ps, _ := segmentPaths(d)
			rewrite(t, ps[len(ps)-1], func(l []string) []string {
				l[len(l)-1] = strings.Replace(l[len(l)-1], `"payload":{}`, `"payload":{"a":1}`, 1)
				return l
			})
		},
		"splice unchained": func(t *testing.T, d string) {
			rewrite(t, filepath.Join(d, "seg-000002.jsonl"), func(l []string) []string {
				return append([]string{l[0], `{"id":"forged","kind":"event","topic":"x"}`}, l[1:]...)
			})
		},
		"splice duplicate": func(t *testing.T, d string) {
			rewrite(t, filepath.Join(d, "seg-000002.jsonl"), func(l []string) []string {
				return append([]string{l[0], l[0]}, l[1:]...)
			})
		},
		"truncate mid-record": func(t *testing.T, d string) {
			ps, _ := segmentPaths(d)
			b, _ := os.ReadFile(ps[len(ps)-1])
			os.WriteFile(ps[len(ps)-1], b[:len(b)-10], 0o600)
		},
		"delete head": func(t *testing.T, d string) { os.Remove(filepath.Join(d, HeadFile)) },
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			dir := chained(t, 20)
			if r, _ := Verify(dir, ""); r.Status != ChainVerified {
				t.Fatalf("clean log must verify: %+v", r)
			}
			tamper(t, dir)
			r, err := Verify(dir, "")
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != ChainBroken {
				t.Fatalf("tampering not detected: %+v", r)
			}
		})
	}
}

func TestLegacyLogIsNotChained(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "seg-000001.jsonl"), []byte(`{"id":"a","kind":"event","topic":"x"}`+"\n"), 0o600)
	r, err := Verify(dir, "")
	if err != nil || r.Status != ChainUnchained {
		t.Fatalf("%+v %v", r, err)
	}
	// A daemon extending a legacy log starts a chain; the legacy prefix stays unverifiable.
	l, _ := Open(dir, 0)
	l.Append(at(time.Now(), "t.new"))
	l.Close()
	if r, _ := Verify(dir, ""); r.Status != ChainUnchained || r.Chained != 1 || r.Legacy != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestClientSuppliedChainIsOverwritten(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, 0)
	e := at(time.Now(), "t.x")
	e.Seq, e.Prev = 99, "deadbeef"
	l.Append(e)
	l.Close()
	if r, _ := Verify(dir, ""); r.Status != ChainVerified {
		t.Fatalf("%+v", r)
	}
}

func TestTornTailIsCutBeforeAppending(t *testing.T) {
	dir := chained(t, 10)
	ps, _ := segmentPaths(dir)
	f, _ := os.OpenFile(ps[len(ps)-1], os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"id":"half`)
	f.Close()
	l, _ := Open(dir, 400)
	l.Append(at(time.Now(), "t.after"))
	l.Close()
	if r, _ := Verify(dir, ""); r.Status != ChainVerified {
		t.Fatalf("%+v", r)
	}
}
