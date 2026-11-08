package manifest

import (
	"strings"
	"testing"
)

func TestCategoryTableIsUniqueAndNeverSystem(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Categories {
		if c.ID == "" || c.ID != strings.ToLower(c.ID) || strings.TrimSpace(c.ID) != c.ID || c.Label == "" {
			t.Errorf("malformed category %+v", c)
		}
		if seen[c.ID] {
			t.Errorf("duplicate category %q", c.ID)
		}
		seen[c.ID] = true
	}
	if seen["system"] {
		t.Error(`"system" is the compositor's section; a manifest cannot claim it`)
	}
}

func TestCategoryAcceptedOnWindowedAndVerbOnlyApps(t *testing.T) {
	for _, c := range Categories {
		verbOnly := strings.Replace(baseTOML, `name = "Example"`, "name = \"Example\"\ncategory = \""+c.ID+"\"", 1)
		if got := mustLoad(t, verbOnly).App.Category; got != c.ID {
			t.Errorf("verb-only: category %q round-tripped as %q", c.ID, got)
		}
		windowed := strings.Replace(windowedApp, `name = "Win"`, "name = \"Win\"\ncategory = \""+c.ID+"\"", 1)
		if got := mustLoad(t, windowed).App.Category; got != c.ID {
			t.Errorf("windowed: category %q round-tripped as %q", c.ID, got)
		}
	}
	if got := mustLoad(t, baseTOML).App.Category; got != "" {
		t.Errorf("unset category parsed as %q", got)
	}
}

func TestUnknownCategoryNamesTheKnownOnes(t *testing.T) {
	for _, bad := range []string{"system", "Files", "games", ""} {
		src := strings.Replace(baseTOML, `name = "Example"`, "name = \"Example\"\ncategory = \""+bad+"\"", 1)
		wantErr(t, src, "app.category", "files, media, development, utilities, haiku")
	}
}
