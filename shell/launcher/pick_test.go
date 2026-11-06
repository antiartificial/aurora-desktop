package launcher

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenPickCarriesTicketsOpaquely(t *testing.T) {
	p, _, _ := writeImages(t)
	be := newFake()
	env, out, errb := harness(be, "")
	var reqs []PickRequest
	env.Pick = func(r PickRequest) (PickResult, error) {
		reqs = append(reqs, r)
		if r.Mode == "ro" {
			return PickResult{Path: p, Ticket: "IN-TICKET"}, nil
		}
		return PickResult{Path: filepath.Join(filepath.Dir(p), "chosen.png"), Ticket: "OUT-TICKET"}, nil
	}
	code := Run(env, []string{"open", "--pick", "--verb", "resize", "--arg", "width=8"})
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errb)
	}
	if len(be.invokes) != 1 {
		t.Fatalf("invokes %+v", be.invokes)
	}
	iv := be.invokes[0]
	if iv.Input != p || iv.Ticket != "IN-TICKET" || iv.OutputTicket != "OUT-TICKET" || !strings.HasSuffix(iv.Output, "chosen.png") {
		t.Fatalf("invoke %+v", iv)
	}
	// The picker was asked for exactly this app and verb, ro input then rw save.
	if len(reqs) != 2 || reqs[0].Mode != "ro" || reqs[1].Mode != "rw" || reqs[1].Kind != "save" ||
		reqs[0].App != "org.aurora.lumen" || reqs[0].Verb != "resize@v1" || reqs[1].Suggest != "a.resize.png" {
		t.Fatalf("pick requests %+v", reqs)
	}
}

func TestOpenPickReadVerbAsksForNoOutput(t *testing.T) {
	_, _, txt := writeImages(t)
	be := newFake()
	env, _, _ := harness(be, "")
	n := 0
	env.Pick = func(r PickRequest) (PickResult, error) { n++; return PickResult{Path: txt, Ticket: "T"}, nil }
	if code := Run(env, []string{"open", "--pick", "--verb", "wc"}); code != 0 || n != 1 {
		t.Fatalf("exit %d, picks %d", code, n)
	}
	if be.invokes[0].OutputTicket != "" || be.invokes[0].Ticket != "T" {
		t.Fatalf("%+v", be.invokes[0])
	}
}

func TestOpenPickCancelledInvokesNothing(t *testing.T) {
	be := newFake()
	env, _, errb := harness(be, "")
	env.Pick = func(PickRequest) (PickResult, error) {
		return PickResult{}, errors.New("cancelled: nothing was granted")
	}
	if code := Run(env, []string{"open", "--pick", "--verb", "resize"}); code != 1 || len(be.invokes) != 0 {
		t.Fatalf("exit %d invokes %d", code, len(be.invokes))
	}
	if !strings.Contains(errb.String(), "cancelled") {
		t.Fatal(errb.String())
	}
}

func TestOpenPickRejectsFileAndWrongType(t *testing.T) {
	p, _, txt := writeImages(t)
	be := newFake()
	env, _, errb := harness(be, "")
	env.Pick = func(PickRequest) (PickResult, error) { return PickResult{Path: txt, Ticket: "T"}, nil }
	if code := Run(env, []string{"open", "--pick", "--verb", "resize"}); code != 1 || len(be.invokes) != 0 || !strings.Contains(errb.String(), "does not accept") {
		t.Fatalf("wrong type: %d %s", code, errb)
	}
	if code := Run(env, []string{"open", "--pick", p}); code != 2 {
		t.Fatalf("--pick with a file: exit %d", code)
	}
}

func TestOpenWithoutPickSendsNoTicket(t *testing.T) {
	p, _, _ := writeImages(t)
	be := newFake()
	env, _, _ := harness(be, "")
	env.Pick = func(PickRequest) (PickResult, error) { t.Fatal("picker used without --pick"); return PickResult{}, nil }
	if code := Run(env, []string{"open", p, "--verb", "resize", "--arg", "width=8"}); code != 0 {
		t.Fatal(code)
	}
	if be.invokes[0].Ticket != "" || be.invokes[0].OutputTicket != "" {
		t.Fatalf("%+v", be.invokes[0])
	}
}
