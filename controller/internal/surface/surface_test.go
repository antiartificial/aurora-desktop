package surface_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/surface"
)

// callerFunc is a Caller that answers in-process, so the parsing and the
// identity rule are checked without a socket. The wire path -- a real daemon
// stamping From -- is isolation_test.go.
type callerFunc func(context.Context, proto.Envelope) (proto.Envelope, error)

func (f callerFunc) Call(ctx context.Context, e proto.Envelope) (proto.Envelope, error) {
	return f(ctx, e)
}

func reply(payload string) (proto.Envelope, error) {
	return proto.Envelope{Kind: proto.KindResponse, Payload: json.RawMessage(payload)}, nil
}

// TestDescribeSpeaksTheSurfaceContract is the parse test: the descriptor goes
// through contracts/surface, and what comes back is canonical even though the
// registry's bytes need not be (actions and grants arrive in any order).
func TestDescribeSpeaksTheSurfaceContract(t *testing.T) {
	var got proto.Envelope
	c := callerFunc(func(_ context.Context, e proto.Envelope) (proto.Envelope, error) {
		got = e
		return reply(`{"app":"org.example.a","verb":"open@v1","effect":"read",
			"surface":{"app":"org.example.a","title":"A","actions":[
				{"id":"save@v1","label":"Save","effect":"write","grants":["path:/z","path:/a"]},
				{"id":"open@v1","label":"Open","effect":"read"}]}}`)
	})
	d, err := surface.Describe(context.Background(), c, "org.example.a", "open@v1")
	if err != nil {
		t.Fatal(err)
	}
	if d.App != "org.example.a" || d.Title != "A" || len(d.Actions) != 2 {
		t.Fatalf("descriptor: %+v", d)
	}
	b, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(b), `"open@v1"`) > strings.Index(string(b), `"save@v1"`) {
		t.Fatalf("actions not in canonical order: %s", b)
	}
	if !strings.Contains(string(b), `["path:/a","path:/z"]`) {
		t.Fatalf("grants not sorted: %s", b)
	}
	// The request is describe@v1 on the registry service, naming the app and
	// the verb the caller passed -- not a new verb and not a surface-specific one.
	if got.Verb != surface.DescribeVerb || got.To != "registry" || got.Kind != proto.KindRequest {
		t.Fatalf("request envelope: %+v", got)
	}
	var q map[string]string
	if err := json.Unmarshal(got.Payload, &q); err != nil || q["app"] != "org.example.a" || q["verb"] != "open@v1" {
		t.Fatalf("request payload: %s", got.Payload)
	}
}

// TestDescribeRefusesUnusableReplies covers the three ways a reply is not a
// drawable surface. None of them may come back as an empty descriptor: an empty
// descriptor is a window the shell would draw with no chrome and no grants.
func TestDescribeRefusesUnusableReplies(t *testing.T) {
	for _, tc := range []struct {
		name, payload, code string
	}{
		{"older controller", `{"app":"org.example.a","verb":"open@v1","effect":"read"}`, surface.CodeNoSurface},
		{"other app", `{"surface":{"app":"org.example.b","title":"B","actions":[{"id":"x@v1","label":"x","effect":"read"}]}}`, surface.CodeBadSurface},
		{"unknown effect", `{"surface":{"app":"org.example.a","title":"A","actions":[{"id":"x@v1","label":"x","effect":"external"}]}}`, surface.CodeBadSurface},
		{"not an object", `["surface"]`, surface.CodeNoSurface},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := callerFunc(func(context.Context, proto.Envelope) (proto.Envelope, error) { return reply(tc.payload) })
			d, err := surface.Describe(context.Background(), c, "org.example.a", "open@v1")
			var e *surface.Error
			if !errors.As(err, &e) || e.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
			if d.App != "" || d.Actions != nil {
				t.Fatalf("a refused reply still produced a descriptor: %+v", d)
			}
		})
	}
}

// TestPermitIsTheIsolationRule is the unit half of the isolation proof: the
// bus-stamped identity decides, and the app it names is the only one it gets.
// ownedBy here recomputes the domain name, which is what the controller does for
// a verb domain; isolation_test.go supplies it from a launch table instead.
func TestPermitIsTheIsolationRule(t *testing.T) {
	const (
		appA = "org.example.a"
		appB = "org.example.b"
	)
	ownedBy := func(name, app string) bool { return name == invoke.DomainName(app) }
	a := "domain:" + invoke.DomainName(appA) + ":00112233aabbccdd"

	for _, tc := range []struct {
		name, from, app string
		code            string
	}{
		{"own surface", a, appA, ""},
		{"another domain's surface", a, appB, surface.CodeNotPermitted},
		{"session user draws every window", "uid1000:aurora", appB, ""},
		{"unreadable domain does not fail open", "domain:onlyonepart", appA, surface.CodeNotPermitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := surface.Permit(tc.from, tc.app, ownedBy)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || err.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
			// The refusal names both sides, so a log says who asked for what.
			if !strings.Contains(err.Message, tc.app) {
				t.Fatalf("refusal does not name the app: %v", err)
			}
		})
	}
}

func TestDomainOf(t *testing.T) {
	for _, tc := range []struct {
		from, name string
		isDomain   bool
	}{
		{"domain:app-x-0011:00112233aabbccdd", "app-x-0011", true},
		{"domain:", "", true},
		{"uid1000:bash", "", false},
		{"", "", false},
	} {
		name, isDomain := surface.DomainOf(tc.from)
		if name != tc.name || isDomain != tc.isDomain {
			t.Errorf("DomainOf(%q) = %q,%v want %q,%v", tc.from, name, isDomain, tc.name, tc.isDomain)
		}
	}
}

// TestListSkipsAppsWithoutVerbs checks that an app with no verbs is skipped
// (describe is per-verb, so it has no descriptor to fetch) and that the output
// is ordered by app id whatever order the registry listed them in.
func TestListSkipsAppsWithoutVerbs(t *testing.T) {
	c := callerFunc(func(_ context.Context, e proto.Envelope) (proto.Envelope, error) {
		switch e.Verb {
		case surface.AppsVerb:
			return reply(`{"apps":[
				{"id":"org.example.z","windowed":true,"verbs":["run@v1"]},
				{"id":"org.example.cli","windowed":false,"verbs":["run@v1"]},
				{"id":"org.example.empty","windowed":true,"verbs":[]},
				{"id":"org.example.a","windowed":true,"verbs":["open@v1"]}]}`)
		case surface.DescribeVerb:
			var q map[string]string
			if err := json.Unmarshal(e.Payload, &q); err != nil {
				t.Fatal(err)
			}
			return reply(`{"surface":{"app":"` + q["app"] + `","title":"` + q["app"] + `","actions":[{"id":"x@v1","label":"x","effect":"nominal"}]}}`)
		}
		return proto.Envelope{}, errors.New("unexpected verb " + e.Verb)
	})
	ds, err := surface.List(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 || ds[0].App != "org.example.a" || ds[1].App != "org.example.cli" || ds[2].App != "org.example.z" {
		t.Fatalf("windows: %+v", ds)
	}
}
