package proto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGlob(t *testing.T) {
	cases := []struct {
		pat, topic string
		want       bool
	}{
		{"window.*", "window.opened", true},
		{"window.*", "window.opened.late", false},
		{"window.*", "audio.opened", false},
		{"window.*", "window", false},
		{"**", "a.b.c", true},
		{"window.**", "window.a.b", true},
		{"a.*.c", "a.b.c", true},
		{"a.*.c", "a.b.d", false},
		{"win*.opened", "window.opened", true},
		{"a.b", "a.b", true},
		{"a.b", "a.bc", false},
	}
	for _, c := range cases {
		if got := Match(c.pat, c.topic); got != c.want {
			t.Errorf("Match(%q,%q) = %v, want %v", c.pat, c.topic, got, c.want)
		}
	}
}

func TestUnknownFieldsPreserved(t *testing.T) {
	in := `{"kind":"event","topic":"a.b","future_field":{"x":1},"shm":["h1"]}`
	var e Envelope
	if err := json.Unmarshal([]byte(in), &e); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(e)
	for _, want := range []string{`"future_field":{"x":1}`, `"shm":["h1"]`, `"topic":"a.b"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("lost %s in %s", want, out)
		}
	}
}

func TestValidate(t *testing.T) {
	bad := []Envelope{
		{Kind: KindRequest, To: "x", Verb: "noversion"},
		{Kind: KindRequest, Verb: "a@v1"},
		{Kind: KindEvent},
		{Kind: KindResponse},
		{Kind: "weird"},
	}
	for _, e := range bad {
		if e.Validate() == nil {
			t.Errorf("accepted %+v", e)
		}
	}
	ok := Envelope{Kind: KindRequest, To: "x", Verb: "resize.image@v2"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSegmentSigDmabufDiffersAndMemfdUnchanged(t *testing.T) {
	m := Segment{ID: "a", Bytes: 4, Sealed: true, SHA256: "sha256:x"}
	if got := m.Sig(); got != "a|4|true||sha256:x" {
		t.Errorf("memfd signature changed (old logs would no longer verify): %q", got)
	}
	d := Segment{ID: "a", Bytes: 4, Kind: "dmabuf", Ino: 7}
	if d.Sig() == m.Sig() || d.Sig() == (Segment{ID: "a", Bytes: 4, Kind: "dmabuf", Ino: 8}).Sig() {
		t.Error("dma-buf kind and inode must be in the signature")
	}
	e := Envelope{Segments: []Segment{{ID: "a", Kind: "weird"}}}
	if e.ValidateSegments() == nil {
		t.Error("unknown kind accepted")
	}
}
