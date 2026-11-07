package picker

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// swapOnRead returns `answer` for the confirm's y/N read, and runs `swap` just
// before doing so. That is the attacker's window: after the prompt has been
// printed, while the user is deciding.
type swapOnRead struct {
	lines []string
	swap  func()
	n     int
}

func (s *swapOnRead) Read(p []byte) (int, error) {
	if s.n >= len(s.lines) {
		return 0, io.EOF
	}
	l := s.lines[s.n]
	s.n++
	if s.n == len(s.lines) && s.swap != nil {
		s.swap() // the last line is the confirm answer
	}
	return copy(p, l), nil
}

var shownPath = regexp.MustCompile(`access to (\S+)`)

// The path the user is shown must be the path that gets bound. confirm used to
// print the path as typed and let Mint resolve a second time afterwards, so a
// symlink swapped while the user was reading the prompt changed which file the
// ticket bound. The user approved docs/report.pdf and the app got
// secret/report.pdf.
func TestTheShownPathIsThePathThatBinds(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"docs", "secret"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "report.pdf"), []byte(d), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "tree")
	if err := os.Symlink(filepath.Join(root, "docs"), link); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	in := &swapOnRead{
		lines: []string{"report.pdf\n", "y\n"},
		swap: func() {
			// The attacker repoints the component the chosen path goes through.
			os.Remove(link)
			os.Symlink(filepath.Join(root, "secret"), link)
		},
	}
	got, err := Choose(Options{App: "com.example.leak", Verb: "read@v1", Mode: "ro", Kind: KindFile, Start: link}, bufio.NewReader(in), &out)
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}

	m := shownPath.FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no path in the prompt: %q", out.String())
	}
	shown := m[1]
	if got != shown {
		t.Fatalf("the user was shown %q and the grant binds %q", shown, got)
	}
	// And it must be the file that was actually chosen, not the swapped one.
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "docs" {
		t.Fatalf("the grant binds the attacker's file: %q contains %q", got, body)
	}
}
