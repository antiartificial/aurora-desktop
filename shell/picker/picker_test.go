package picker

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/controller/ticket"
)

var key = []byte("0123456789abcdef0123456789abcdef")

func opts(kind, mode, start string) Options {
	return Options{App: "org.x", Verb: "copy@v1", Mode: mode, Kind: kind, Start: start, Key: key}
}

func drive(t *testing.T, o Options, input string) (Result, error, string) {
	t.Helper()
	var out bytes.Buffer
	r, err := Run(o, bufio.NewReader(strings.NewReader(input)), &out)
	return r, err, out.String()
}

func tree(t *testing.T) string {
	d, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(d, "sub"), 0o755)
	os.WriteFile(filepath.Join(d, "sub", "a.png"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(d, "b.txt"), []byte("x"), 0o644)
	return d
}

func claims(t *testing.T, r Result) ticket.Claims {
	t.Helper()
	c, err := ticket.Verify(key, r.Ticket, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNavigateSelectConfirmMintsTicket(t *testing.T) {
	d := tree(t)
	// listing is dirs first: 1. sub/  2. b.txt ; enter sub, choose a.png, yes
	r, err, ui := drive(t, opts(KindFile, "ro", d), "1\n1\ny\n")
	if err != nil {
		t.Fatal(err, ui)
	}
	want := filepath.Join(d, "sub", "a.png")
	c := claims(t, r)
	if r.Path != want || c.Path != want || c.Mode != "ro" || c.App != "org.x" || c.Verb != "copy@v1" {
		t.Fatalf("%+v %+v", r, c)
	}
}

func TestDeclineAndQuitGrantNothing(t *testing.T) {
	d := tree(t)
	if r, err, _ := drive(t, opts(KindFile, "ro", d), "2\nn\nq\n"); !errors.Is(err, ErrCancelled) || r.Ticket != "" {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err, _ := drive(t, opts(KindFile, "ro", d), ""); !errors.Is(err, ErrCancelled) {
		t.Fatalf("EOF must cancel, got %v", err)
	}
}

func TestFolderIsNotAFileSelection(t *testing.T) {
	d := tree(t)
	// choosing the dir "sub" opens it; only the file can be confirmed
	r, err, _ := drive(t, opts(KindFile, "ro", d), "sub\nq\n")
	if err == nil || r.Ticket != "" {
		t.Fatalf("a folder yielded a file ticket: %+v", r)
	}
}

func TestDirKind(t *testing.T) {
	d := tree(t)
	r, err, _ := drive(t, opts(KindDir, "ro", d), "1\ns\ny\n")
	if err != nil || r.Path != filepath.Join(d, "sub") {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestSaveKind(t *testing.T) {
	d := tree(t)
	o := opts(KindSave, "rw", d)
	o.Suggest = "out.png"
	r, err, _ := drive(t, o, "w\ny\n")
	if err != nil || r.Path != filepath.Join(d, "out.png") || claims(t, r).Mode != "rw" {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := os.Stat(r.Path); err == nil {
		t.Fatal("picker created the file; only the controller may")
	}
	// a name with a slash is refused
	if _, err, _ := drive(t, o, "w ../x\nq\n"); err == nil {
		t.Fatal("path traversal in name accepted")
	}
}

func TestTicketBindsResolvedPath(t *testing.T) {
	d := tree(t)
	link := filepath.Join(t.TempDir(), "link.txt")
	os.Symlink(filepath.Join(d, "b.txt"), link)
	r, err := Mint(opts(KindFile, "ro", ""), link)
	if err != nil || r.Path != filepath.Join(d, "b.txt") {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := Mint(opts(KindFile, "ro", ""), d); err == nil {
		t.Fatal("directory accepted as a file")
	}
	if _, err := Mint(opts(KindFile, "ro", ""), "rel/path"); err == nil {
		t.Fatal("relative path accepted")
	}
}
