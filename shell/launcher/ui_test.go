package launcher

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type fakeUI struct {
	*fakeBackend
	reply json.RawMessage
	from  string
	err   error
	asked []string
	reqs  []map[string]any
}

func (f *fakeUI) UICall(_ context.Context, service, verb string, req any) (json.RawMessage, string, error) {
	f.asked = append(f.asked, service+" "+verb)
	b, _ := json.Marshal(req)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	f.reqs = append(f.reqs, m)
	return f.reply, f.from, f.err
}

const uiSample = `{"schema":"aurora.ui.tree/1","viewer":{"identity":"uid1000:bus","kind":"session","rule":"you are the user's own session"},
"service":"ui.ks","owner":"uid1000:ks","scale":1,"windows":[{"index":0,"title":"Sink","kind":"window","size":{"w":536,"h":386},"active":true,
"root":{"token":3,"parent":null,"name":"top","role":"window-root","role_confidence":"protocol","frame":{"l":0,"t":0,"r":535,"b":385},"abs":{"l":0,"t":0,"r":535,"b":385},
"state":{"navigable":false},"text":[],"children":[
{"token":9,"parent":3,"name":"press\u001b]0;PWNED\u0007","role":"control","role_confidence":"inferred","frame":{"l":10,"t":10,"r":85,"b":40},"abs":{"l":18,"t":40,"r":93,"b":70},
 "state":{"navigable":true},"text":[{"text":"Press me\u001b[2J","x":1,"y":2,"width":3,"size":12,"box":{"l":1,"t":2,"r":3,"b":4}}],"children":[]}]}}]}`

func uiHarness(f *fakeUI) (*Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return &Env{
		Backend: func(string, time.Duration) Backend { return f },
		In:      bufio.NewReader(strings.NewReader("")),
		Out:     &out, Err: &errb,
	}, &out, &errb
}

func TestUITreePrintsNamesRolesAndText(t *testing.T) {
	f := &fakeUI{fakeBackend: newFake(), reply: json.RawMessage(uiSample), from: "uid1000:ks"}
	env, out, _ := uiHarness(f)
	if rc := Run(env, []string{"ui", "tree", "ks"}); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	mustContain(t, out.String(), "ui.ks", "window 0", "top  [window-root/protocol]", "[control/inferred]", "focusable", "\"Press me", "VIEW: the service says you are a session")
	if strings.Contains(out.String(), "\x1b") {
		t.Errorf("an escape sequence the application drew reached the terminal: %q", out.String())
	}
	if f.asked[0] != "ui.ks tree@v1" {
		t.Errorf("asked %v: a bare name gets the ui. prefix", f.asked)
	}
}

func TestUIRefusalIsPrintedAndFails(t *testing.T) {
	f := &fakeUI{fakeBackend: newFake(), err: &UIError{Service: "ui.ks", Verb: "tree@v1", Code: "no_grant", Message: "no live ui:read grant"}}
	env, out, errb := uiHarness(f)
	if rc := Run(env, []string{"ui", "tree", "ui.ks"}); rc != 1 {
		t.Fatalf("rc %d, want 1", rc)
	}
	mustContain(t, errb.String(), "REFUSED", "no_grant", "ui:read")
	if out.Len() != 0 {
		t.Errorf("a refusal printed a tree: %q", out.String())
	}
}

func TestUITextAndNodeSendTheRightRequest(t *testing.T) {
	f := &fakeUI{fakeBackend: newFake(), reply: json.RawMessage(`{"text":"a\nb"}`)}
	env, out, _ := uiHarness(f)
	if rc := Run(env, []string{"ui", "text", "ks", "0", "12", "--deep"}); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	r := f.reqs[0]
	if r["window"] != float64(0) || r["token"] != float64(12) || r["deep"] != true {
		t.Errorf("request %v", r)
	}
	mustContain(t, out.String(), "a⏎b")
	if rc := Run(env, []string{"ui", "text", "ks", "x", "1"}); rc != 2 {
		t.Errorf("a non-numeric window should be a usage error, rc %d", rc)
	}
	if rc := Run(env, []string{"ui", "tree", "bad name!"}); rc != 2 {
		t.Errorf("a malformed service name should be a usage error, rc %d", rc)
	}
}

func TestUIJSONIsTheServicesReplyUntouched(t *testing.T) {
	f := &fakeUI{fakeBackend: newFake(), reply: json.RawMessage(uiSample)}
	env, out, _ := uiHarness(f)
	if rc := Run(env, []string{"ui", "tree", "ks", "--json"}); rc != 0 {
		t.Fatal(rc)
	}
	var v map[string]any
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || v["schema"] != "aurora.ui.tree/1" {
		t.Fatalf("not the reply: %v %q", err, out.String())
	}
}
