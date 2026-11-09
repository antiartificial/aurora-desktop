package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	contract "github.com/antiartificial/aurora/contracts/surface"
)

// windows is deliberately unsorted: the writer, not the registry, is what makes
// the output stable, because a surface that reorders between runs makes a diff
// useless.
func windows() []contract.Descriptor {
	return []contract.Descriptor{
		{App: "org.example.b", Title: "B", Actions: []contract.Action{
			{ID: "save@v1", Label: "Save", Effect: contract.EffectWrite, Grants: []string{"path:/z", "device:gpu"}},
			{ID: "open@v1", Label: "Open", Effect: contract.EffectRead},
		}},
		{App: "org.example.a", Title: "A", Grants: []string{"device:wayland"}, Actions: []contract.Action{
			{ID: "resize@v1", Label: "Resize", Effect: contract.EffectWrite, Grants: []string{"path:/p"}},
		}},
	}
}

var spaces = regexp.MustCompile(`\s+`)

// TestWriteListShowsAppActionEffectGrants pins the output shape: one block per
// window, one line per action in id order, the effect as the contract spells it,
// and the action's grants on the same line. Spacing is compared collapsed, so
// the test holds the columns' content and order rather than tabwriter's padding.
func TestWriteListShowsAppActionEffectGrants(t *testing.T) {
	var out bytes.Buffer
	if err := writeList(&out, windows(), false); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		got = append(got, strings.TrimSpace(spaces.ReplaceAllString(l, " ")))
	}
	want := []string{
		"org.example.b B",
		"open@v1 read -",
		"save@v1 write device:gpu path:/z",
		"",
		"org.example.a A",
		"resize@v1 write path:/p",
		"(window) device:wayland",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("output:\n%s", strings.Join(got, "\n"))
	}
}

// TestWriteListJSONIsCanonical checks --json against the contract's own
// encoding: sorted actions and sorted grants, because those are the bytes the
// toolkit and the compositor compare.
func TestWriteListJSONIsCanonical(t *testing.T) {
	var out bytes.Buffer
	if err := writeList(&out, windows(), true); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, `"app": "org.example.b"`) {
		t.Fatalf("json: %s", got)
	}
	if i, j := strings.Index(got, `"open@v1"`), strings.Index(got, `"save@v1"`); i < 0 || j < 0 || i > j {
		t.Fatalf("actions not sorted by id: %s", got)
	}
	if i, j := strings.Index(got, `"device:gpu"`), strings.Index(got, `"path:/z"`); i < 0 || j < 0 || i > j {
		t.Fatalf("grants not sorted: %s", got)
	}
}

// TestWriteListRefusesAnInvalidDescriptor: a descriptor the shell could not
// police must not be printed as if it were fine, in either form.
func TestWriteListRefusesAnInvalidDescriptor(t *testing.T) {
	bad := []contract.Descriptor{{App: "org.example.bad", Title: "Bad",
		Actions: []contract.Action{{ID: "x@v1", Label: "X", Effect: "external"}}}}
	var out bytes.Buffer
	if err := writeList(&out, bad, true); err == nil {
		t.Fatal("--json encoded a descriptor with an unknown effect")
	}
}
