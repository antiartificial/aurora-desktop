package manifest

import (
	"strings"
	"testing"
)

const windowedApp = `
[app]
id = "org.example.win"
name = "Win"
version = "1.0.0"
image_hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
windowed = true
exec = "bin/win"
`

func TestWindowedParseAndResolve(t *testing.T) {
	m := mustLoad(t, windowedApp)
	if !m.App.Windowed || m.App.Exec != "bin/win" {
		t.Fatalf("parsed %+v", m.App)
	}
	if got := m.App.ExecPath("/apps/win"); got != "/apps/win/bin/win" {
		t.Errorf("relative exec resolved to %q", got)
	}
	abs := strings.Replace(windowedApp, `"bin/win"`, `"/opt/win"`, 1)
	if got := mustLoad(t, abs).App.ExecPath("/apps/win"); got != "/opt/win" {
		t.Errorf("absolute exec resolved to %q", got)
	}
	if got := mustLoad(t, baseTOML).App.ExecPath("/x"); got != "" {
		t.Errorf("unset exec resolved to %q", got)
	}
}

func TestWindowedRequiresExec(t *testing.T) {
	src := strings.Replace(windowedApp, `exec = "bin/win"`+"\n", "", 1)
	wantErr(t, src, "app.exec", "required when windowed = true")
	if got := errFor(src); !strings.Contains(got, "line 7:") {
		t.Errorf("want line 7 (the windowed key) in %q", got)
	}
	wantErr(t, strings.Replace(windowedApp, `"bin/win"`, `"  "`, 1), "required when windowed = true")
}

func TestExecWithoutWindowedRejected(t *testing.T) {
	wantErr(t, strings.Replace(baseTOML, `name = "Example"`, "name = \"Example\"\nexec = \"bin/x\"", 1), "app.exec", "only valid with windowed = true")
	wantErr(t, strings.Replace(windowedApp, "windowed = true", "windowed = false", 1), "only valid with windowed = true")
}

func TestWindowedMayAlsoHaveVerbs(t *testing.T) {
	mustLoad(t, windowedApp+"\n[[verb]]\nname = \"look@v1\"\neffect = \"read\"\n")
}

func TestConsentLaunchTargetChange(t *testing.T) {
	old := mustLoad(t, windowedApp)
	if consent(t, old, mustLoad(t, windowedApp)) {
		t.Error("identical windowed manifests need no consent")
	}
	if !consent(t, old, mustLoad(t, strings.Replace(windowedApp, "bin/win", "bin/other", 1))) {
		t.Error("changing exec must require consent")
	}
}

func TestDefaultVerb(t *testing.T) {
	other := "\n[[verb]]\nname = \"other@v1\"\neffect = \"read\"\naccepts = [\"image/png\"]\n"
	if !mustLoad(t, baseTOML+"default = true\n").Verbs[0].Default {
		t.Error("default not parsed")
	}
	// A different type is a different slot.
	mustLoad(t, baseTOML+"default = true\n\n[[verb]]\nname = \"o@v1\"\neffect = \"read\"\naccepts = [\"image/jpeg\"]\ndefault = true\n")
	wantErr(t, baseTOML+"default = true\n"+other+"default = true\n", "look@v1 and other@v1 are both default for image/png")
	mustLoad(t, baseTOML+"default = true\n"+other)
	// No accepts: nothing to be the default for.
	wantErr(t, windowedApp+"\n[[verb]]\nname = \"v@v1\"\neffect = \"read\"\ndefault = true\n", "default verb must declare accepts")
}

func TestVerbGroup(t *testing.T) {
	if got := mustLoad(t, baseTOML+"group = \"Convert\"\n").Verbs[0].Group; got != "Convert" {
		t.Errorf("group = %q", got)
	}
	wantErr(t, baseTOML+"group = \"\"\n", "verb[0] (look@v1).group", "empty")
	wantErr(t, baseTOML+"group = \"   \"\n", "empty or blank")
	wantErr(t, baseTOML+"group = \"a\\u001b[31mred\"\n", "control character")
	wantErr(t, baseTOML+"group = \"a\\nb\"\n", "control character")
	wantErr(t, baseTOML+"group = \""+strings.Repeat("x", 41)+"\"\n", "longer than 40")
	mustLoad(t, baseTOML+"group = \""+strings.Repeat("é", 40)+"\"\n")
}
