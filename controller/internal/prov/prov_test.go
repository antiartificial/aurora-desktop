package prov

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestXattrRoundTrip(t *testing.T) {
	f := filepath.Join(t.TempDir(), "o.png")
	os.WriteFile(f, []byte("x"), 0o644)
	r := Record{File: f, App: "a", Verb: "v@v1", Inputs: []string{"/in"}, Args: []byte(`{"w":1}`), Isolated: true, TS: "t"}
	if err := Write(f, r); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EPERM) {
			t.Skipf("filesystem has no user xattrs: %v", err)
		}
		t.Fatal(err)
	}
	got, err := Read(f)
	if err != nil || !reflect.DeepEqual(got, r) {
		t.Fatalf("got %+v err %v", got, err)
	}
	if _, err := Read(filepath.Join(filepath.Dir(f), "none")); err == nil {
		t.Fatal("expected error")
	}
}

func TestResourceNamesFitTheDaemonSyntaxAndAreStable(t *testing.T) {
	if got := Resource("/tmp/a.png", "rw"); got != "file:/tmp/a.png:rw" {
		t.Fatal(got)
	}
	odd := Resource("/tmp/my pics/\u00e9.png", "ro")
	if !resourceOK.MatchString(odd) || odd != Resource("/tmp/my pics/\u00e9.png", "ro") || odd == Resource("/tmp/my pics/\u00e9.png", "rw") {
		t.Fatal(odd)
	}
}

func TestReportedFrom(t *testing.T) {
	if r, big := ReportedFrom([]byte(`{"ok":true,"reported":{"cc":"x"}}`)); string(r) != `{"cc":"x"}` || big {
		t.Fatal(string(r), big)
	}
	for _, in := range []string{`{"ok":true}`, `not json`, `{"reported":"str"}`, `{"reported":null}`} {
		if r, big := ReportedFrom([]byte(in)); r != nil || big {
			t.Fatalf("%s: %s %v", in, r, big)
		}
	}
	huge := `{"reported":{"k":"` + strings.Repeat("a", MaxReported) + `"}}`
	if r, big := ReportedFrom([]byte(huge)); r != nil || !big {
		t.Fatal("oversize not refused")
	}
}
