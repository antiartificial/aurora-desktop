package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/antiartificial/aurora/bus/proto"
)

// CheckResult is one invariant's verdict.
type CheckResult struct {
	Name   string
	OK     bool
	Detail string
}

// Report is the full verdict. OK only if every check passed.
type Report struct {
	OK     bool
	Checks []CheckResult
}

// Check compares an original log with the log of a replay daemon (replayed;
// each message carries Origin with its original id and sender) and with the
// events a probe subscriber on that daemon received (delivered).
func Check(orig, replayed, delivered []proto.Envelope) Report {
	r := Report{Checks: []CheckResult{
		causalOrder(orig, replayed),
		finalState(orig, replayed, delivered),
		capGrants(orig, replayed),
	}}
	r.OK = true
	for _, c := range r.Checks {
		r.OK = r.OK && c.OK
	}
	return r
}

func originID(e proto.Envelope) string {
	if e.Origin != nil {
		return e.Origin.ID
	}
	return ""
}

func originFrom(e proto.Envelope) string {
	if e.Origin != nil {
		return e.Origin.From
	}
	return e.From
}

// causalOrder: every original message was replayed exactly once, and the
// happens-before edges of the original (per-sender order, request before its
// response) hold in the replay.
func causalOrder(orig, replayed []proto.Envelope) CheckResult {
	res := CheckResult{Name: "causal order"}
	pos := map[string]int{}
	var problems []string
	for i, e := range replayed {
		id := originID(e)
		if _, dup := pos[id]; dup {
			problems = append(problems, "duplicate "+id)
		}
		pos[id] = i
	}
	for _, e := range orig {
		if _, ok := pos[e.ID]; !ok {
			problems = append(problems, "missing "+e.ID)
		}
	}
	if len(replayed) != len(orig) {
		problems = append(problems, fmt.Sprintf("message count %d != %d", len(replayed), len(orig)))
	}
	edges := 0
	lastBySender := map[string]string{}
	known := map[string]bool{}
	for _, e := range orig {
		known[e.ID] = true
		if prev, ok := lastBySender[e.From]; ok {
			edges++
			if pos[prev] >= pos[e.ID] {
				problems = append(problems, fmt.Sprintf("sender order %s before %s violated", prev, e.ID))
			}
		}
		lastBySender[e.From] = e.ID
		if e.Corr != "" && known[e.Corr] {
			edges++
			if pos[e.Corr] >= pos[e.ID] {
				problems = append(problems, fmt.Sprintf("request %s must precede response %s", e.Corr, e.ID))
			}
		}
	}
	res.OK = len(problems) == 0
	res.Detail = fmt.Sprintf("%d messages, %d happens-before edges", len(orig), edges)
	if !res.OK {
		res.Detail += "; " + summarize(problems)
	}
	return res
}

type state struct {
	count int
	last  string
}

func (s state) String() string { return fmt.Sprintf("n=%d last=%s", s.count, s.last) }

func hash(p []byte) string {
	h := sha256.Sum256(p)
	return hex.EncodeToString(h[:4])
}

// finalState: a subscriber of a topic sees the same events, so the final
// state of every topic (event count, last payload) must match; likewise for
// every service (requests handled, last request). Original state comes from
// the original log; replayed topic state from what the probe subscriber
// actually received, service state from the replay daemon's log.
func finalState(orig, replayed, delivered []proto.Envelope) CheckResult {
	res := CheckResult{Name: "final state per subscriber"}
	want, got := map[string]state{}, map[string]state{}
	add := func(m map[string]state, key string, e proto.Envelope) {
		s := m[key]
		s.count++
		s.last = hash(e.Payload)
		m[key] = s
	}
	for _, e := range orig {
		switch e.Kind {
		case proto.KindEvent:
			add(want, "topic "+e.Topic, e)
		case proto.KindRequest:
			add(want, "service "+e.To, e)
		}
	}
	for _, e := range delivered {
		add(got, "topic "+e.Topic, e)
	}
	for _, e := range replayed {
		if e.Kind == proto.KindRequest {
			add(got, "service "+e.To, e)
		}
	}
	var problems []string
	for k, w := range want {
		if g, ok := got[k]; !ok || g != w {
			problems = append(problems, fmt.Sprintf("%s: want %v, got %v", k, w, got[k]))
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			problems = append(problems, "unexpected "+k)
		}
	}
	res.OK = len(problems) == 0
	res.Detail = fmt.Sprintf("%d topics/services compared", len(want))
	if !res.OK {
		res.Detail += "; " + summarize(problems)
	}
	return res
}

// capGrants: the set of (sender, target, capability identity) is the same.
func capGrants(orig, replayed []proto.Envelope) CheckResult {
	res := CheckResult{Name: "capability grants"}
	set := func(msgs []proto.Envelope) map[string]bool {
		m := map[string]bool{}
		for _, e := range msgs {
			target := e.To
			if e.Kind == proto.KindEvent {
				target = "topic:" + e.Topic
			}
			for _, id := range e.CapIDs {
				m[fmt.Sprintf("%s -> %s : %s", originFrom(e), target, id)] = true
			}
		}
		return m
	}
	want, got := set(orig), set(replayed)
	var problems []string
	for k := range want {
		if !got[k] {
			problems = append(problems, "missing grant "+k)
		}
	}
	for k := range got {
		if !want[k] {
			problems = append(problems, "extra grant "+k)
		}
	}
	res.OK = len(problems) == 0
	res.Detail = fmt.Sprintf("%d grants", len(want))
	if !res.OK {
		res.Detail += "; " + summarize(problems)
	}
	return res
}

func summarize(p []string) string {
	sort.Strings(p)
	if len(p) > 3 {
		return strings.Join(p[:3], "; ") + fmt.Sprintf("; ... %d more", len(p)-3)
	}
	return strings.Join(p, "; ")
}
