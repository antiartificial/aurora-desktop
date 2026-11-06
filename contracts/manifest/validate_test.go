package manifest

import (
	"reflect"
	"strings"
	"testing"
)

func TestExamplesValidate(t *testing.T) {
	for _, app := range []string{"lumen", "ffmpeg", "imagemagick"} {
		if _, err := Load(example(t, app)); err != nil {
			t.Errorf("%s: %v", app, err)
		}
	}
}

func TestBaseIsValid(t *testing.T) { mustLoad(t, baseTOML) }

func TestVerbWithoutVersionRejected(t *testing.T) {
	src := strings.Replace(baseTOML, "look@v1", "look", 1)
	wantErr(t, src, "verb[0]", "name", "major version")
	// Line number points at the name line.
	if got := errFor(src); !strings.Contains(got, "line 16:") {
		t.Errorf("want line 16 in %q", got)
	}
	for _, bad := range []string{"look@1", "look@v0", "look@v", "Look@v1", "look@v1.2"} {
		wantErr(t, strings.Replace(baseTOML, "look@v1", bad, 1), "verb[0]")
	}
}

func TestUnknownEffectRejected(t *testing.T) {
	wantErr(t, strings.Replace(baseTOML, `effect = "read"`, `effect = "delete"`, 1), `unknown effect class "delete"`)
	wantErr(t, strings.Replace(baseTOML, `effect = "read"`, `effect = ""`, 1), "unknown effect class")
}

func TestExternalWithoutCapabilitiesRejected(t *testing.T) {
	for _, e := range []string{"external", "destructive"} {
		src := strings.Replace(baseTOML, `effect = "read"`, `effect = "`+e+`"`, 1)
		wantErr(t, src, "must declare the capabilities it needs")
		ok := src + "capabilities = [\"net:api.example.com\"]\n"
		mustLoad(t, ok)
	}
}

func TestVerbCapabilityMustBeRequested(t *testing.T) {
	src := baseTOML + "capabilities = [\"net:evil.example.org\"]\n"
	wantErr(t, src, "not among the app's requested capabilities")
	wantErr(t, baseTOML+"capabilities = [\"bogus\"]\n", "path:<path>")
	wantErr(t, baseTOML+"capabilities = [\"fs:/x\"]\n", "unknown capability kind")
}

func TestDuplicateNamesRejected(t *testing.T) {
	dupVerb := baseTOML + "\n[[verb]]\nname = \"look@v1\"\neffect = \"read\"\n"
	wantErr(t, dupVerb, "duplicate verb name")
	// A different major version is a different verb.
	mustLoad(t, baseTOML+"\n[[verb]]\nname = \"look@v2\"\neffect = \"read\"\n")
	ev := baseTOML + "\n[[event]]\nname = \"a.done\"\n\n[[event]]\nname = \"a.done\"\n"
	wantErr(t, ev, "duplicate event name")
}

func TestCapabilityForms(t *testing.T) {
	cases := map[string]string{
		`path = "relative/x"`: "absolute",
		`path = "~/a/../b"`:   "..",
		`path = "/"`:          "below",
		`path = "~/"`:         "below",
		`path = "/a//b"`:      "empty",
		`access = "rw"`:       `"read" or "write"`,
	}
	for repl, want := range cases {
		src := baseTOML
		if strings.HasPrefix(repl, "path") {
			src = strings.Replace(src, `path = "~/Pictures"`, repl, 1)
		} else {
			src = strings.Replace(src, `access = "read"`, repl, 1)
		}
		wantErr(t, src, want)
	}
	for _, h := range []string{"https://x.com", "x.com/path", "bad host", "x.com:99999x", "*.com", "-x.com"} {
		wantErr(t, strings.Replace(baseTOML, `"api.example.com"`, `"`+h+`"`, 1), "network[0]")
	}
	for _, h := range []string{"api.example.com:8443", "*.example.com", "localhost"} {
		mustLoad(t, strings.Replace(baseTOML, `"api.example.com"`, `"`+h+`"`, 1))
	}
	wantErr(t, strings.Replace(baseTOML, "[capabilities]", "[capabilities]\ndevices = [\"GPU!\"]", 1), "devices[0]")
}

func TestMIMEValidation(t *testing.T) {
	good := baseTOML + "\n[types]\nopens = [\"image/png\", \"application/vnd.api+json\"]\n"
	mustLoad(t, good)
	for _, bad := range []string{"png", "image/", "/png", "image/png; q=1", "Image/PNG", "image/*", "a b/c"} {
		wantErr(t, baseTOML+"\n[types]\ncreates = [\""+bad+"\"]\n", "types.creates[0]", "MIME")
	}
	wantErr(t, strings.Replace(baseTOML, `["image/png"]`, `["image"]`, 1), "accepts[0]")
	mustLoad(t, strings.Replace(baseTOML, `["image/png"]`, `["image/*", "*/*"]`, 1))
}

func TestIdentityRules(t *testing.T) {
	wantErr(t, strings.Replace(baseTOML, "org.example.app", "App", 1), "app.id")
	wantErr(t, strings.Replace(baseTOML, `"1.0.0"`, `"1.0"`, 1), "app.version")
	wantErr(t, strings.Replace(baseTOML, "sha256:0", "md5:0", 1), "app.image_hash")
}

func TestOtherSections(t *testing.T) {
	wantErr(t, baseTOML+"\n[[runtime]]\nname=\"go\"\nversion=\"1.27\"\n", "runtime[0].version")
	wantErr(t, baseTOML+"\n[[setting]]\nkey=\"q\"\ntype=\"int\"\ndefault=\"x\"\n", "does not match type int")
	wantErr(t, baseTOML+"\n[[setting]]\nkey=\"q\"\ntype=\"blob\"\n", "setting[0].type")
	wantErr(t, baseTOML+"\n[[migration]]\nfrom=\"1.0.0\"\nto=\"1.0.0\"\n", "must differ")
	wantErr(t, baseTOML+"\n[[replicant]]\nname=\"W\"\n", "replicant[0].name")
	wantErr(t, baseTOML+"\n[[event]]\nname=\"x\"\npayload = { n = \"integer\" }\n", "event[0].payload.n")
}

func TestParseErrors(t *testing.T) {
	_, err := Parse([]byte("[app]\nid = \n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("syntax error should name line 2: %v", err)
	}
	_, err = Parse([]byte(strings.Replace(baseTOML, `effect = "read"`, "effect = \"read\"\nefect = \"x\"", 1)))
	if err == nil || !strings.Contains(err.Error(), "unknown key") || !strings.Contains(err.Error(), "line 18") {
		t.Errorf("unknown key should be reported with its line: %v", err)
	}
}

func TestNestedTableLineNumbers(t *testing.T) {
	src := strings.Replace(baseTOML, `accepts = ["image/png"]`, "accepts = [\"image/png\"]\n[verb.input]\nw = \"integer\"\n", 1)
	if got := errFor(src); !strings.Contains(got, "line 19:") {
		t.Errorf("want [verb.input] header line 19, got %q", got)
	}
}

func TestMultipleErrorsReported(t *testing.T) {
	src := strings.Replace(baseTOML, `effect = "read"`, `effect = "zap"`, 1)
	src = strings.Replace(src, "org.example.app", "X", 1)
	_, err := Load([]byte(src))
	if es, ok := err.(Errors); !ok || len(es) != 2 {
		t.Errorf("want 2 errors, got %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, app := range []string{"lumen", "ffmpeg", "imagemagick"} {
		m1 := mustLoad(t, string(example(t, app)))
		out, err := Encode(m1)
		if err != nil {
			t.Fatal(err)
		}
		m2, err := Load(out)
		if err != nil {
			t.Fatalf("%s: re-parse: %v\n%s", app, err, out)
		}
		m1.lines, m2.lines = nil, nil
		if !reflect.DeepEqual(m1, m2) {
			t.Errorf("%s: round trip changed the manifest\nbefore: %+v\nafter:  %+v", app, m1, m2)
		}
	}
}

func TestRoundTripCoversEverySection(t *testing.T) {
	m := mustLoad(t, string(example(t, "lumen")))
	if len(m.Runtimes) == 0 || len(m.Capabilities.Paths) == 0 || len(m.Capabilities.Devices) == 0 ||
		len(m.Types.Opens) == 0 || len(m.Verbs) == 0 || len(m.Events) == 0 ||
		len(m.Settings) == 0 || len(m.Replicants) == 0 || len(m.Migrations) == 0 {
		t.Fatalf("lumen should exercise every section: %+v", m)
	}
}
