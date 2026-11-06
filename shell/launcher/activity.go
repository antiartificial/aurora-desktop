package launcher

// `aurora activity`: rebuild a readable timeline from the bus log.
//
// What it keys on, exactly (see correlate below):
//   - an invoke is a registry.invoke@v1 request; its reply is the response or
//     error whose corr is that request's id (the log keeps the daemon's ids, so
//     corr matches id). That is the only hard key between a request and its reply.
//   - an approval is tied to an invoke by the approval request_id found in the
//     invoke's reply (response.approval.request_id) or, when the invoke failed,
//     in the error detail ("request <id>; nothing was run"). approval.requested,
//     approval.preflight, approval.decided and approval.decide@v1 all carry that id.
//   - a provenance.created event is tied to an invoke by the output file and the
//     record's own ts, which the reply's embedded record repeats.
//
//   - a capability handle's lifecycle is the set of bus.cap.minted / transferred /
//     revoked events that name the same handle id. Only an event whose sender is
//     "bus" counts: busd publishes those itself and refuses clients the bus.*
//     topics, so the "by" inside is the identity of the connection that asked.
//     A handle is tied to an invoke when the id in an invoke's grants[].cap is
//     the id in a mint event.
//
// Nothing else is joined by guesswork. Whatever does not match is printed in
// its own sections, never dropped. A handle minted and not revoked in the log
// is reported as such; a log with no lifecycle events at all (older than the
// events) is reported as unable to say, not as "fine".

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/hlc"
	"github.com/antiartificial/aurora/bus/proto"
)

const (
	verbInvoke = "registry.invoke@v1"
	verbDecide = "approval.decide@v1"
	maxField   = 200 // display cap for any one log-supplied field
)

// LogReader is the optional half of a Backend that can return the bus log.
type LogReader interface {
	ReadLog(ctx context.Context) ([]proto.Envelope, error)
}

// ReadLog asks busd for the whole log (bus.log@v1, no limit).
func (b *busBackend) ReadLog(ctx context.Context) ([]proto.Envelope, error) {
	c, err := b.conn()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 6*b.timeout)
	defer cancel()
	raw, err := c.Control(ctx, "bus.log@v1", map[string]any{"limit": 0})
	if err != nil {
		var re *client.RemoteError
		if errors.As(err, &re) {
			return nil, fmt.Errorf("bus.log@v1 refused: %s", re.Message)
		}
		return nil, err
	}
	var reply struct{ Messages []proto.Envelope }
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("bus.log@v1: malformed reply: %w", err)
	}
	return reply.Messages, nil
}

func cmdActivity(env *Env, args []string) int {
	var c common
	fs := newFlags(env, "activity", "activity [--log DIR|FILE] [--since DURATION]", &c)
	dir := fs.String("log", "", "read this log directory (or seg-*.jsonl file) directly instead of asking busd")
	since := fs.Duration("since", 0, "only entries newer than this (an invoke that began earlier shows its later events as unattributed)")
	if _, err := parseInterleaved(fs, args); err != nil {
		return parseFailed(err)
	}
	var entries []proto.Envelope
	var err error
	if *dir != "" {
		entries, err = buslog.ReadFile(*dir)
	} else {
		be := env.Backend(c.socket, c.timeout)
		defer be.Close()
		lr, ok := be.(LogReader)
		if !ok {
			return fail(env, fmt.Errorf("this backend cannot read the bus log"))
		}
		entries, err = lr.ReadLog(context.Background())
	}
	if err != nil {
		return fail(env, err)
	}
	if *since > 0 {
		cut := time.Now().Add(-*since).UnixNano()
		kept := entries[:0:0]
		for _, e := range entries {
			if t, err := hlc.Parse(e.TS); err != nil || t.Wall >= cut {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	io.WriteString(env.Out, FormatActivity(entries))
	return 0
}

// ---- decoding of the payloads we care about (all of it app- or controller-
// supplied text: every string printed goes through clean) ----

type actGrant struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Source string `json:"source"`
	Cap    string `json:"cap"`
}

type actApproval struct {
	Required       bool   `json:"required"`
	Route          string `json:"route"`
	Decision       string `json:"decision"`
	RequestID      string `json:"request_id"`
	By             string `json:"by"`
	Key            string `json:"key"`
	At             string `json:"at"`
	DomainVerified string `json:"domain_verified"`
	DomainSource   string `json:"domain_source"`
	DomainShown    string `json:"domain_shown"`
	DomainMismatch bool   `json:"domain_mismatch"`
}

type actRecord struct {
	File           string          `json:"file"`
	App            string          `json:"app"`
	Domain         string          `json:"domain"`
	DomainAttested bool            `json:"domain_attested"`
	Verb           string          `json:"verb"`
	Inputs         []string        `json:"inputs"`
	Args           json.RawMessage `json:"args"`
	Isolated       *bool           `json:"isolated"`
	Grants         []actGrant      `json:"grants"`
	Approval       *actApproval    `json:"approval"`
	CapEpoch       string          `json:"cap_epoch"`
	CapNote        string          `json:"cap_note"`
	TS             string          `json:"ts"`
}

type actReply struct {
	OK            bool            `json:"ok"`
	Output        string          `json:"output"`
	Result        json.RawMessage `json:"result"`
	Isolated      *bool           `json:"isolated"`
	Grants        []actGrant      `json:"grants"`
	Approval      *actApproval    `json:"approval"`
	NeedsApproval bool            `json:"needs_approval"`
	Log           string          `json:"log"`
	Provenance    *actRecord      `json:"provenance"`
	Code          string          `json:"code"`
	Error         string          `json:"error"`
	Message       string          `json:"message"`
	Detail        string          `json:"detail"`
}

type actInvokeReq struct {
	App    string          `json:"app"`
	Verb   string          `json:"verb"`
	Input  string          `json:"input"`
	Output string          `json:"output"`
	Args   json.RawMessage `json:"args"`
}

type actDecided struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
	By        string `json:"by"`
	Reason    string `json:"reason"`
	Route     string `json:"route"`
}

type actPreflight struct {
	App       string   `json:"app"`
	Verb      string   `json:"verb"`
	Effect    string   `json:"effect"`
	RequestID string   `json:"request_id"`
	Grants    []string `json:"grants"`
}

func decode(e proto.Envelope, v any) bool {
	return len(e.Payload) > 0 && json.Unmarshal(e.Payload, v) == nil
}

// ---- the model ----

type ent struct {
	i int
	e proto.Envelope
}

// approvalState is everything the log says about one approval request id.
type approvalState struct {
	id         string
	requested  *ApprovalRequest
	reqEnt     ent
	preflights []ent // from anyone; only a domain: sender proves "from inside"
	decided    *actDecided
	decidedEnt ent
	decides    []ent // approval.decide@v1 requests
	record     *actApproval
	invoke     *invoke
}

type invoke struct {
	n        int
	req      ent
	rq       actInvokeReq
	reply    *ent
	rp       actReply
	prov     *ent // provenance.created event
	pv       actRecord
	approval *approvalState
	how      string // how the approval was linked
	warns    []string
	gestures int
	provHow  string
}

// capPayload is a bus.cap.* event payload. All of it was written by busd from
// its own table and the requesting connection, except Resource, which is the
// name the minter chose.
type capPayload struct {
	ID       string `json:"id"`
	Resource string `json:"resource"`
	By       string `json:"by"`
	Minter   string `json:"minter"`
	Holder   string `json:"holder"`
	From     string `json:"from"`
	To       string `json:"to"`
	Expires  string `json:"expires"`
}

type lifeEv struct {
	ent
	topic string
	p     capPayload
}

// handleLife is everything the log says about one handle id.
type handleLife struct {
	id       string
	evs      []lifeEv
	mint     *lifeEv
	revoke   *lifeEv
	uses     []ent // other messages presenting the id in cap_ids
	invokes  []*invoke
	problems []string
}

const (
	topicCapMinted      = "bus.cap.minted"
	topicCapTransferred = "bus.cap.transferred"
	topicCapRevoked     = "bus.cap.revoked"
)

func isCapTopic(s string) bool {
	return s == topicCapMinted || s == topicCapTransferred || s == topicCapRevoked
}

type timeline struct {
	handles    map[string]*handleLife
	horder     []string
	lifeEvents int // trusted lifecycle events in the log
	forgedLife []int

	entries     []proto.Envelope
	consumed    []bool
	controllers map[string]bool
	invokes     []*invoke
	approvals   map[string]*approvalState
	order       []string // approval ids in first-seen order
	unfiled     []int    // controller-only topics from a non-controller
}

var detailReq = regexp.MustCompile(`^request ([0-9A-Za-z_-]{1,64});`)

func (t *timeline) isController(from string) bool {
	if len(t.controllers) > 0 {
		return t.controllers[from]
	}
	return !strings.HasPrefix(from, "domain:")
}

func (t *timeline) appr(id string) *approvalState {
	a := t.approvals[id]
	if a == nil {
		a = &approvalState{id: id}
		t.approvals[id] = a
		t.order = append(t.order, id)
	}
	return a
}

func (t *timeline) take(i int) { t.consumed[i] = true }

func correlate(entries []proto.Envelope) *timeline {
	t := &timeline{entries: entries, consumed: make([]bool, len(entries)), controllers: map[string]bool{}, approvals: map[string]*approvalState{}, handles: map[string]*handleLife{}}
	byID := map[string]*invoke{}
	for i, e := range entries {
		if (e.Kind == proto.KindResponse || e.Kind == proto.KindError) && e.Verb == verbInvoke {
			t.controllers[e.From] = true
		}
		if e.Kind == proto.KindRequest && e.Verb == verbInvoke && e.ID != "" {
			iv := &invoke{n: len(t.invokes) + 1, req: ent{i, e}}
			decode(e, &iv.rq)
			t.invokes = append(t.invokes, iv)
			byID[e.ID] = iv
			t.take(i)
		}
	}
	// replies
	for i, e := range entries {
		if (e.Kind != proto.KindResponse && e.Kind != proto.KindError) || e.Verb != verbInvoke {
			continue
		}
		iv := byID[e.Corr]
		if iv == nil || iv.reply != nil {
			continue
		}
		iv.reply = &ent{i, e}
		decode(e, &iv.rp)
		t.take(i)
	}
	// approval traffic, by request id
	decideByID := map[string]*approvalState{}
	for i, e := range entries {
		switch {
		case e.Kind == proto.KindEvent && e.Topic == "approval.requested":
			var r ApprovalRequest
			if !t.isController(e.From) || !decode(e, &r) || r.RequestID == "" {
				continue
			}
			a := t.appr(r.RequestID)
			if a.requested == nil {
				a.requested, a.reqEnt = &r, ent{i, e}
				t.take(i)
			}
		case e.Kind == proto.KindEvent && e.Topic == "approval.decided":
			var d actDecided
			if !t.isController(e.From) || !decode(e, &d) || d.RequestID == "" {
				continue
			}
			a := t.appr(d.RequestID)
			if a.decided == nil {
				a.decided, a.decidedEnt = &d, ent{i, e}
				t.take(i)
			}
		case e.Kind == proto.KindEvent && e.Topic == "approval.preflight":
			var p actPreflight
			if !decode(e, &p) || p.RequestID == "" {
				continue
			}
			a := t.appr(p.RequestID)
			a.preflights = append(a.preflights, ent{i, e})
			t.take(i)
		case e.Kind == proto.KindRequest && e.Verb == verbDecide:
			var p struct {
				RequestID string `json:"request_id"`
			}
			if !decode(e, &p) || p.RequestID == "" {
				continue
			}
			a := t.appr(p.RequestID)
			a.decides = append(a.decides, ent{i, e})
			decideByID[e.ID] = a
			t.take(i)
		}
	}
	for i, e := range entries { // a decide's reply rides with its request
		if a := decideByID[e.Corr]; a != nil && e.Verb == verbDecide && (e.Kind == proto.KindResponse || e.Kind == proto.KindError) {
			a.decides = append(a.decides, ent{i, e})
			t.take(i)
		}
	}
	// invoke <-> approval
	for _, iv := range t.invokes {
		if iv.reply == nil {
			continue
		}
		id, how := "", ""
		switch {
		case iv.rp.Approval != nil && iv.rp.Approval.RequestID != "":
			id, how = iv.rp.Approval.RequestID, "the invoke reply's approval record"
		case iv.rp.Provenance != nil && iv.rp.Provenance.Approval != nil && iv.rp.Provenance.Approval.RequestID != "":
			id, how = iv.rp.Provenance.Approval.RequestID, "the invoke reply's provenance record"
		default:
			if m := detailReq.FindStringSubmatch(iv.rp.Detail); m != nil {
				id, how = m[1], "the invoke error's detail text"
			}
		}
		if id == "" {
			continue
		}
		if a := t.appr(id); a.invoke == nil {
			a.invoke, iv.approval, iv.how = iv, a, how
			if iv.rp.Approval != nil {
				a.record = iv.rp.Approval
			} else if iv.rp.Provenance != nil {
				a.record = iv.rp.Provenance.Approval
			}
		}
	}
	// provenance events
	for i, e := range entries {
		if e.Kind != proto.KindEvent || e.Topic != "provenance.created" {
			continue
		}
		var r actRecord
		if !t.isController(e.From) || !decode(e, &r) {
			continue
		}
		var cand []*invoke
		for _, iv := range t.invokes {
			if iv.prov == nil && iv.reply != nil && iv.rp.OK && iv.rp.Output == r.File {
				cand = append(cand, iv)
			}
		}
		var pick *invoke
		how := ""
		for _, iv := range cand {
			if iv.rp.Provenance != nil && iv.rp.Provenance.TS != "" && iv.rp.Provenance.TS == r.TS {
				pick, how = iv, "same output file and record ts as the reply"
				break
			}
		}
		if pick == nil && len(cand) == 1 {
			pick, how = cand[0], "same output file (no record ts to compare)"
		}
		if pick != nil {
			pick.prov, pick.pv, pick.provHow = &ent{i, e}, r, how
			t.take(i)
		}
	}
	t.correlateHandles()
	for _, iv := range t.invokes { // fall back to the reply's embedded record
		if iv.prov == nil && iv.rp.Provenance != nil {
			iv.pv = *iv.rp.Provenance
		}
	}
	return t
}

// correlateHandles joins bus.cap.* events by handle id, then ties handles to
// invokes by the id in grants[].cap. It also checks, from the log alone, that
// each handle's story is possible; the replay checker does the same strictly.
func (t *timeline) correlateHandles() {
	hl := func(id string) *handleLife {
		h := t.handles[id]
		if h == nil {
			h = &handleLife{id: id}
			t.handles[id] = h
			t.horder = append(t.horder, id)
		}
		return h
	}
	for i, e := range t.entries {
		if e.Kind != proto.KindEvent || !isCapTopic(e.Topic) {
			continue
		}
		var p capPayload
		if !decode(e, &p) || p.ID == "" {
			continue // stays unclaimed and prints under "Not tied to any invoke"
		}
		if e.From != proto.BusTarget {
			t.forgedLife = append(t.forgedLife, i) // not claimed: shown, tagged UNTRUSTED
			continue
		}
		t.lifeEvents++
		h := hl(p.ID)
		ev := lifeEv{ent{i, e}, e.Topic, p}
		h.evs = append(h.evs, ev)
		switch e.Topic {
		case topicCapMinted:
			if h.mint != nil {
				h.problems = append(h.problems, "minted twice")
			} else {
				m := ev
				h.mint = &m
			}
		case topicCapRevoked:
			if h.mint == nil {
				h.problems = append(h.problems, "revoked but no mint event for it in this log")
			}
			if h.revoke != nil {
				h.problems = append(h.problems, "revoked twice")
			} else {
				r := ev
				h.revoke = &r
			}
			if h.mint != nil && p.By != h.mint.p.By {
				h.problems = append(h.problems, fmt.Sprintf("revoked by %s but minted by %s", short(p.By), short(h.mint.p.By)))
			}
		case topicCapTransferred:
			if h.mint == nil {
				h.problems = append(h.problems, "transferred but no mint event for it in this log")
			}
			if h.revoke != nil {
				h.problems = append(h.problems, "transferred after it was revoked")
			}
		}
		t.take(i)
	}
	if t.lifeEvents == 0 {
		return
	}
	// presentations: any other message carrying the id
	for i, e := range t.entries {
		if e.Kind == proto.KindEvent && isCapTopic(e.Topic) {
			continue
		}
		for _, id := range e.CapIDs {
			h := t.handles[id]
			if h == nil {
				h = hl(id)
				h.problems = append(h.problems, "presented but never minted in this log")
			}
			h.uses = append(h.uses, ent{i, e})
			if h.revoke != nil && i > h.revoke.i {
				h.problems = append(h.problems, fmt.Sprintf("presented by %s at %s, after it was revoked", short(e.From), clk(e)))
			}
		}
	}
	for _, iv := range t.invokes {
		gs := iv.rp.Grants
		if len(gs) == 0 && iv.rp.Provenance != nil {
			gs = iv.rp.Provenance.Grants
		}
		for _, g := range gs {
			if h := t.handles[g.Cap]; h != nil && g.Cap != "" {
				h.invokes = append(h.invokes, iv)
			}
		}
	}
}

func (t *timeline) logEnd() int64 {
	if len(t.entries) == 0 {
		return 0
	}
	if ts, err := hlc.Parse(t.entries[len(t.entries)-1].TS); err == nil {
		return ts.Wall
	}
	return 0
}

// expiredBy reports the TTL expiry the mint event named, if the log's last
// entry is past it. Expiry is derived from the mint's own expires field; the
// daemon logs no event when a handle times out.
func (h *handleLife) expiredBy(end int64) (time.Time, bool) {
	if h.mint == nil || h.mint.p.Expires == "" || end == 0 {
		return time.Time{}, false
	}
	x, err := time.Parse(time.RFC3339Nano, h.mint.p.Expires)
	return x, err == nil && x.UnixNano() <= end
}

func (h *handleLife) stateWord(end int64) string {
	switch {
	case h.revoke != nil:
		return "REVOKED at " + clk(h.revoke.e)
	case h.mint == nil:
		return "no mint event in this log"
	}
	if x, ok := h.expiredBy(end); ok {
		return "EXPIRED by its TTL (" + x.UTC().Format("15:04:05.000") + "), never explicitly revoked"
	}
	return "NEVER REVOKED in this log"
}

// renderHandles prints one block per handle id the lifecycle events (or a
// presentation) named, and returns nothing: findings go to the summary.
func (t *timeline) renderHandles(b *strings.Builder, sm *summary) {
	if t.lifeEvents == 0 && len(t.forgedLife) == 0 {
		return
	}
	fmt.Fprintf(b, "\n== Capability handles (bus.cap.* events: sender 'bus', so busd wrote them; 'by' is the identity of the connection busd saw)\n")
	end := t.logEnd()
	var unrevoked []string
	for _, id := range t.horder {
		h := t.handles[id]
		res := ""
		if h.mint != nil {
			res = "  resource " + dash(h.mint.p.Resource) + " (named by the minter)"
		}
		ivs := ""
		var ns []string
		for _, iv := range h.invokes {
			ns = append(ns, fmt.Sprintf("#%d", iv.n))
		}
		if len(ns) > 0 {
			ivs = "  invoke " + strings.Join(ns, ", ")
		}
		fmt.Fprintf(b, "  %s%s%s\n", short(id), res, ivs)
		type line struct {
			i int
			e proto.Envelope
			s string
		}
		var ls []line
		for _, ev := range h.evs {
			switch ev.topic {
			case topicCapMinted:
				x := ""
				if ev.p.Expires != "" {
					x = ", expires " + short(ev.p.Expires)
					if tm, err := time.Parse(time.RFC3339Nano, ev.p.Expires); err == nil {
						x = ", expires " + tm.UTC().Format("15:04:05.000") + " UTC"
					}
				}
				ls = append(ls, line{ev.i, ev.e, fmt.Sprintf("minted by %s (stamped by busd), held by %s%s", dash(ev.p.By), dash(ev.p.Holder), x)})
			case topicCapTransferred:
				ls = append(ls, line{ev.i, ev.e, fmt.Sprintf("transferred %s -> %s, asked by %s (stamped by busd)", dash(ev.p.From), dash(ev.p.To), dash(ev.p.By))})
			case topicCapRevoked:
				ls = append(ls, line{ev.i, ev.e, fmt.Sprintf("revoked by %s (stamped by busd)", dash(ev.p.By))})
			}
		}
		const maxUses = 6
		for k, u := range h.uses {
			if k == maxUses {
				ls = append(ls, line{u.i, u.e, fmt.Sprintf("... and %d more presentations", len(h.uses)-maxUses)})
				break
			}
			what := dash(u.e.Verb)
			if u.e.Kind == proto.KindEvent {
				what = dash(u.e.Topic)
			}
			ls = append(ls, line{u.i, u.e, fmt.Sprintf("presented by %s (stamped by busd) on %s %s", dash(u.e.From), u.e.Kind, what)})
		}
		sort.SliceStable(ls, func(a, c int) bool { return ls[a].i < ls[c].i })
		for _, l := range ls {
			fmt.Fprintf(b, "    %s  %s\n", clk(l.e), l.s)
		}
		if len(h.invokes) > 0 {
			fmt.Fprintf(b, "    joined to invoke by: the id in the invoke reply's grants[].cap is this id\n")
		}
		fmt.Fprintf(b, "    state: %s\n", h.stateWord(end))
		for _, pr := range h.problems {
			fmt.Fprintf(b, "    !! %s\n", pr)
			sm.add("handle %s: %s", short(id), pr)
		}
		if h.mint != nil && h.revoke == nil {
			if _, exp := h.expiredBy(end); !exp {
				who := "not tied to an invoke"
				if len(ns) > 0 {
					who = "invoke " + strings.Join(ns, ", ")
				}
				unrevoked = append(unrevoked, fmt.Sprintf("%s (%s, %s)", short(id), dash(h.mint.p.Resource), who))
			}
		}
	}
	if len(unrevoked) > 0 {
		show := unrevoked
		more := ""
		if len(show) > 8 {
			more = fmt.Sprintf(" and %d more", len(show)-8)
			show = show[:8]
		}
		sm.add("%d handle(s) minted and NEVER REVOKED in this log (still live, or the holder or controller died before revoking): %s%s", len(unrevoked), strings.Join(show, "; "), more)
	}
}

// ---- rendering ----

func short(s string) string {
	s = clean(s)
	if r := []rune(s); len(r) > maxField+16 {
		return string(r[:maxField]) + fmt.Sprintf("...(+%d chars)", len(r)-maxField)
	}
	return s
}

func clk(e proto.Envelope) string {
	if t, err := hlc.Parse(e.TS); err == nil {
		return time.Unix(0, t.Wall).UTC().Format("15:04:05.000")
	}
	return "??:??:??.???"
}

func nobody(s string) string {
	if s == "" {
		return "NOBODY"
	}
	return short(s)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return short(s)
}

type step struct {
	i     int
	clock string
	lines []string
}

func (iv *invoke) status() string {
	switch {
	case iv.reply == nil:
		return "NO REPLY IN LOG"
	case iv.reply.e.Kind == proto.KindError || !iv.rp.OK:
		switch iv.rp.Code {
		case "approval_denied":
			return "DENIED"
		case "approval_timeout":
			return "TIMED OUT (no answer, so denied)"
		}
		return "FAILED"
	}
	return "OK"
}

func routeNote(route string) string {
	switch route {
	case "compositor":
		return "the compositor drew the prompt and read the keystroke"
	case "interactive":
		return "a decision sent on approval.decide@v1 by whatever of the user's reached the session socket"
	case "auto-approve":
		return "TEST AFFORDANCE: controllerd --auto-approve, NO HUMAN WAS ASKED"
	case "ambient":
		return "no per-invoke approval: every grant was declared in the app's manifest"
	}
	return ""
}

// domainTrustFor says how far the domain shown for an approval can be trusted,
// using the same words as the live prompt (domainTrust), but only calling it
// attested when the log itself holds the pre-flight busd stamped from inside
// that domain. A record that says "bus" with no such event in the log is
// reported as unverified.
func (a *approvalState) domainTrustFor() (shown, trust string, extra []string) {
	source, attested, mismatch, claim := "", false, false, ""
	if a.requested != nil {
		shown, source, attested, mismatch, claim = a.requested.Domain, a.requested.DomainSource, a.requested.DomainAttested, a.requested.DomainMismatch, a.requested.DomainClaim
	}
	if a.record != nil {
		if a.record.DomainShown != "" {
			shown = a.record.DomainShown
		}
		if source == "" {
			source = a.record.DomainSource
			attested = source == "bus"
		}
		mismatch = mismatch || a.record.DomainMismatch
	}
	fromInside := ""
	for _, p := range a.preflights {
		if strings.HasPrefix(p.e.From, "domain:") && p.e.From == shown {
			fromInside = p.e.From
		}
	}
	req := ApprovalRequest{Domain: shown, DomainAttested: attested, DomainSource: source}
	switch {
	case shown == "":
		trust = "[UNVERIFIED: no domain identity recorded]"
	case source == "bus" && fromInside == "":
		trust = "[UNVERIFIED: recorded as bus-attested, but this log holds no pre-flight event from that domain]"
	case source == "bus":
		trust = domainTrust(req) + " (pre-flight in this log was stamped by busd from inside it)"
	default:
		trust = domainTrust(req)
	}
	if mismatch {
		extra = append(extra, fmt.Sprintf("!! MISMATCH: the controller expected %s, the bus attested %s.", dash(claim), dash(shown)))
	}
	for _, p := range a.preflights {
		if shown != "" && p.e.From != shown {
			extra = append(extra, fmt.Sprintf("!! a pre-flight for this request came from %s, not from the domain shown (%s)", dash(p.e.From), short(shown)))
		}
		if !strings.HasPrefix(p.e.From, "domain:") {
			extra = append(extra, fmt.Sprintf("!! a pre-flight for this request was published by %s, which is not a domain", dash(p.e.From)))
		}
	}
	return shown, trust, extra
}

func grantLines(gs []actGrant) []string {
	var out []string
	for _, g := range gs {
		c := "NO HANDLE"
		if g.Cap != "" {
			c = short(g.Cap)
		}
		out = append(out, fmt.Sprintf("%-2s %-8s %s  %s", short(g.Mode), short(g.Source), short(g.Path), c))
	}
	return out
}

// summary collects "needs attention" items as the timeline is rendered.
type summary struct {
	items []string
}

func (s *summary) add(f string, a ...any) { s.items = append(s.items, fmt.Sprintf(f, a...)) }

func (t *timeline) renderInvoke(b *strings.Builder, iv *invoke, sm *summary) {
	req := iv.req.e
	fmt.Fprintf(b, "\n[#%d] %s %s  %s\n", iv.n, dash(iv.rq.App), dash(iv.rq.Verb), iv.status())
	var steps []step
	add := func(i int, e proto.Envelope, lines ...string) { steps = append(steps, step{i, clk(e), lines}) }
	args := ""
	if len(iv.rq.Args) > 0 && string(iv.rq.Args) != "null" {
		args = "  args " + short(string(iv.rq.Args))
	}
	add(iv.req.i, req, fmt.Sprintf("ASKED by %s (stamped by busd): %s %s%s", dash(req.From), dash(iv.rq.App), dash(iv.rq.Verb), args),
		fmt.Sprintf("input %s  output %s", dash(iv.rq.Input), dash(iv.rq.Output)))

	ap := iv.approval
	if ap != nil {
		shown, trust, extra := ap.domainTrustFor()
		for _, p := range ap.preflights {
			var pf actPreflight
			decode(p.e, &pf)
			who := "stamped by busd from inside"
			if !strings.HasPrefix(p.e.From, "domain:") {
				who = "NOT from a domain; published by"
			}
			add(p.i, p.e, fmt.Sprintf("PRE-FLIGHT %s %s: %s %s", who, dash(p.e.From), dash(pf.Verb), dash(pf.Effect)),
				"(the payload is the app's own text; the sender identity is the bus's)")
		}
		if ap.requested != nil {
			r := ap.requested
			ls := []string{fmt.Sprintf("APPROVAL REQUESTED %s: %s verb %s (%s)", short(ap.id), strings.ToUpper(dash(r.Effect)), dash(r.Verb), dash(r.App)),
				fmt.Sprintf("domain %s %s", dash(shown), trust)}
			ls = append(ls, extra...)
			for _, g := range r.Grants {
				ls = append(ls, fmt.Sprintf("asks for %-2s %-8s %s", short(g.Mode), short(g.Source), short(g.Path)))
			}
			if r.TimeoutSeconds > 0 {
				ls = append(ls, fmt.Sprintf("(no answer within %ds means deny)", r.TimeoutSeconds))
			}
			add(ap.reqEnt.i, ap.reqEnt.e, ls...)
		} else if ap.record != nil || len(ap.preflights) > 0 {
			why := "no approval.requested event in the log"
			if ap.record != nil && ap.record.Route == "compositor" {
				why += " (the compositor route prompts without one)"
			}
			ls := []string{fmt.Sprintf("APPROVAL %s: %s", short(ap.id), why),
				fmt.Sprintf("domain %s %s", dash(shown), trust)}
			ls = append(ls, extra...)
			at := iv.req
			if len(ap.preflights) > 0 {
				at = ap.preflights[len(ap.preflights)-1]
			} else if iv.reply != nil {
				at = *iv.reply
			}
			add(at.i, at.e, ls...)
		}
		for _, d := range ap.decides {
			if d.e.Kind == proto.KindRequest {
				var p struct {
					Decision string `json:"decision"`
				}
				decode(d.e, &p)
				add(d.i, d.e, fmt.Sprintf("DECISION %s sent on approval.decide@v1 by %s (stamped by busd)", strings.ToUpper(dash(p.Decision)), dash(d.e.From)))
			}
		}
		if ap.decided != nil {
			d := ap.decided
			l := fmt.Sprintf("APPROVAL DECIDED: %s by %s via route %s", strings.ToUpper(dash(d.Decision)), nobody(d.By), dash(d.Route))
			if d.Reason != "" {
				l += " (reason: " + short(d.Reason) + ")"
			}
			ls := []string{l}
			if n := routeNote(d.Route); n != "" {
				ls = append(ls, "route means: "+n)
			}
			add(ap.decidedEnt.i, ap.decidedEnt.e, ls...)
		}
	}

	var rec *actApproval
	if ap != nil {
		rec = ap.record
	} else if iv.rp.Approval != nil {
		rec = iv.rp.Approval
	} else if iv.pv.Approval != nil {
		rec = iv.pv.Approval
	}

	if iv.prov != nil {
		pv := iv.pv
		ls := []string{fmt.Sprintf("PROVENANCE: %s was produced by %s %s", dash(pv.File), dash(pv.App), dash(pv.Verb))}
		if len(pv.Inputs) > 0 {
			var ins []string
			for _, in := range pv.Inputs {
				ins = append(ins, short(in))
			}
			ls = append(ls, "from "+strings.Join(ins, ", "))
		}
		ls = append(ls, "joined to this invoke by: "+iv.provHow)
		if pv.CapEpoch != "" {
			ls = append(ls, "cap epoch "+short(pv.CapEpoch))
		}
		var ids []string
		for _, c := range iv.prov.e.CapIDs {
			ids = append(ids, short(c))
		}
		if len(ids) > 0 {
			ls = append(ls, "handles presented with this event (bus-logged ids): "+strings.Join(ids, ", "))
		}
		add(iv.prov.i, iv.prov.e, ls...)
	}

	if iv.reply != nil {
		e := iv.reply.e
		var ls []string
		switch iv.status() {
		case "OK":
			ls = append(ls, fmt.Sprintf("REPLY OK: output %s", dash(iv.rp.Output)))
			if iv.rp.Output == "" && len(iv.rp.Result) > 0 {
				ls = append(ls, "result "+short(string(iv.rp.Result)))
			}
		default:
			msg := iv.rp.Message
			if msg == "" {
				msg = iv.rp.Error
			}
			ls = append(ls, fmt.Sprintf("REPLY %s: %s: %s", iv.status(), dash(iv.rp.Code), dash(msg)))
			if iv.rp.Detail != "" {
				ls = append(ls, "detail: "+short(iv.rp.Detail))
			}
		}
		gs := iv.rp.Grants
		if len(gs) == 0 {
			gs = iv.pv.Grants
		}
		if len(gs) > 0 {
			ls = append(ls, "GRANTED to the domain for this run (and a capability handle id each):")
			for _, l := range grantLines(gs) {
				ls = append(ls, "  "+l)
			}
		}
		add(iv.reply.i, e, ls...)
	}
	sort.SliceStable(steps, func(a, b int) bool { return steps[a].i < steps[b].i })
	for _, s := range steps {
		for k, l := range s.lines {
			if k == 0 {
				fmt.Fprintf(b, "  %s  %s\n", s.clock, l)
			} else {
				fmt.Fprintf(b, "                %s\n", l)
			}
		}
	}

	// ---- state lines and findings for this invoke ----
	note := func(f string, a ...any) { fmt.Fprintf(b, "      state: "+f+"\n", a...) }
	flag := func(f string, a ...any) {
		m := fmt.Sprintf(f, a...)
		fmt.Fprintf(b, "      !! %s\n", m)
		sm.add("[#%d] %s", iv.n, m)
	}
	gs := iv.rp.Grants
	if len(gs) == 0 {
		gs = iv.pv.Grants
	}
	if iv.reply == nil {
		flag("the request has no reply in the log: still running, or the controller died; any grants it made are unaccounted for")
	}
	if iv.reply != nil && iv.rp.OK {
		iso := iv.rp.Isolated
		if iso == nil {
			iso = iv.pv.Isolated
		}
		switch {
		case iso != nil && !*iso:
			flag("RAN UNISOLATED: the controller reports no domain boundary for this run")
		case iso == nil:
			note("isolation not reported")
		}
		if iv.pv.Domain != "" {
			if iv.pv.DomainAttested {
				note("run domain %s [controller says busd had it registered]", short(iv.pv.Domain))
			} else {
				flag("run domain %s is UNVERIFIED: the name the adapter reported, which busd had not registered", short(iv.pv.Domain))
			}
		}
		for _, g := range gs {
			if g.Cap == "" {
				flag("grant %s %s has no capability handle id (cap note: %s)", short(g.Mode), short(g.Path), dash(iv.pv.CapNote))
			}
		}
		if iv.pv.CapNote != "" {
			flag("capability note on the record: %s", short(iv.pv.CapNote))
		}
		for _, l := range strings.Split(iv.rp.Log, "\n") {
			if strings.Contains(strings.ToLower(l), "warning") {
				iv.warns = append(iv.warns, l)
			}
		}
		if len(iv.warns) > 0 {
			note("run log has %d warning line(s) (unauthenticated text), first: %s", len(iv.warns), short(iv.warns[0]))
		}
	}
	if iv.reply != nil && ap == nil && (iv.rp.NeedsApproval || (rec != nil && rec.Required)) {
		flag("the reply says approval was needed but names no request id the log can join")
	}
	if ap != nil {
		d := ap.decided
		switch {
		case d == nil && rec != nil && (rec.Route == "auto-approve"):
		case d == nil:
			flag("approval %s has NO approval.decided event in the log", short(ap.id))
		case d.Reason == "timeout":
			flag("approval %s TIMED OUT: nobody answered, so it was denied", short(ap.id))
		case d.Reason == "canceled":
			flag("approval %s was CANCELED while waiting", short(ap.id))
		}
		if d != nil && rec != nil && rec.Decision != "" && rec.Decision != d.Decision {
			flag("record says %s but the decided event says %s", short(rec.Decision), short(d.Decision))
		}
		if d != nil && rec != nil && rec.By != "" && rec.By != d.By {
			flag("record says decided by %s but the decided event says %s", short(rec.By), short(d.By))
		}
		if iv.rp.OK && d == nil && (rec == nil || rec.Route != "auto-approve") {
			flag("the invoke RAN with approval %s but the log holds no decision for it", short(ap.id))
		}
		if iv.rp.OK && d != nil && d.Decision != "allow" {
			flag("the invoke RAN although the decided event says %s", short(d.Decision))
		}
		note("approval %s joined to this invoke via %s", short(ap.id), iv.how)
	}
	if rec != nil && rec.Route == "auto-approve" {
		flag("AUTO-APPROVED (controllerd --auto-approve): a test affordance, no human was asked")
	}
	if rec != nil && !rec.Required && rec.Route == "ambient" {
		note("approval: none needed, every grant was manifest-declared (ambient)")
	}
	for _, g := range gs {
		if h := t.handles[g.Cap]; h != nil && g.Cap != "" {
			note("handle %s: %s (details under Capability handles)", short(g.Cap), h.stateWord(t.logEnd()))
		}
	}
	for _, g := range gs {
		if g.Source != "ambient" && g.Source != "" {
			iv.gestures++
		}
	}
	if iv.gestures > 0 {
		note("%d of %d grants are gesture/powerbox: not declared in the app's manifest, justified only by this invoke", iv.gestures, len(gs))
	}
}

func (t *timeline) renderApprovalOrphan(b *strings.Builder, a *approvalState, sm *summary) {
	shown, trust, extra := a.domainTrustFor()
	fmt.Fprintf(b, "  approval %s: no invoke in this log carries this id\n", short(a.id))
	for _, p := range a.preflights {
		var pf actPreflight
		decode(p.e, &pf)
		who := "from inside"
		if !strings.HasPrefix(p.e.From, "domain:") {
			who = "NOT from a domain, from"
		}
		fmt.Fprintf(b, "    %s  pre-flight %s %s (stamped by busd) claims %s %s, grants %s\n", clk(p.e), who, dash(p.e.From), dash(pf.Verb), dash(pf.Effect), short(strings.Join(pf.Grants, "; ")))
	}
	if a.requested != nil {
		fmt.Fprintf(b, "    %s  requested: %s %s (%s), domain %s %s\n", clk(a.reqEnt.e), dash(a.requested.Verb), dash(a.requested.Effect), dash(a.requested.App), dash(shown), trust)
	}
	for _, d := range a.decides {
		if d.e.Kind == proto.KindRequest {
			fmt.Fprintf(b, "    %s  decide sent by %s\n", clk(d.e), dash(d.e.From))
		}
	}
	if a.decided != nil {
		fmt.Fprintf(b, "    %s  decided %s by %s via %s %s\n", clk(a.decidedEnt.e), strings.ToUpper(dash(a.decided.Decision)), nobody(a.decided.By), dash(a.decided.Route), short(a.decided.Reason))
	}
	for _, x := range extra {
		fmt.Fprintf(b, "    %s\n", x)
	}
	switch {
	case a.decided == nil && a.requested == nil:
		fmt.Fprintf(b, "    state: nothing asked and nothing decided\n")
		sm.add("approval %s has traffic (%d decide message(s), %d pre-flight(s)) but no request and no decision", short(a.id), len(a.decides), len(a.preflights))
	case a.decided == nil:
		fmt.Fprintf(b, "    !! never decided in this log\n")
		sm.add("approval %s was requested and never decided in this log", short(a.id))
	case a.decided.Reason == "timeout":
		fmt.Fprintf(b, "    !! TIMED OUT\n")
		sm.add("approval %s TIMED OUT (denied by default)", short(a.id))
	}
}

// controllerOnly are the topics only the controller may publish; the bus does
// not enforce that, so a copy from anyone else is shown as untrusted.
var controllerOnly = map[string]bool{"provenance.created": true, "approval.requested": true, "approval.decided": true}

// renderRest prints every entry the correlation did not claim. Request/reply
// pairs are folded onto one line; nothing is omitted.
func (t *timeline) renderRest(b *strings.Builder, sm *summary) {
	replyOf := map[string]int{}
	for i, e := range t.entries {
		if !t.consumed[i] && (e.Kind == proto.KindResponse || e.Kind == proto.KindError) && e.Corr != "" {
			replyOf[e.Corr] = i
		}
	}
	paired := map[int]bool{}
	n := 0
	for i, e := range t.entries {
		if t.consumed[i] || paired[i] {
			continue
		}
		n++
		if n == 1 {
			fmt.Fprintf(b, "\n== Not tied to any invoke (every entry the correlation could not claim)\n")
		}
		switch e.Kind {
		case proto.KindRequest:
			line := fmt.Sprintf("%s  %s -> %s  request %s %s", clk(e), dash(e.From), dash(e.To), dash(e.Verb), short(string(e.Payload)))
			if j, ok := replyOf[e.ID]; ok {
				r := t.entries[j]
				paired[j] = true
				line += fmt.Sprintf("\n              => %s from %s: %s", r.Kind, dash(r.From), short(string(r.Payload)))
			} else {
				line += "\n              => no reply in the log"
			}
			fmt.Fprintf(b, "  %s\n", line)
		case proto.KindEvent:
			tag := ""
			if controllerOnly[e.Topic] && !t.isController(e.From) {
				tag = "UNTRUSTED (only the controller publishes this topic): "
				sm.add("%s published %s, a controller-only topic, and it was not joined to anything", dash(e.From), dash(e.Topic))
			}
			if isCapTopic(e.Topic) && e.From != proto.BusTarget {
				tag = "UNTRUSTED (only the daemon, sender 'bus', publishes bus.cap.*; a client forged this): "
				sm.add("%s published %s, a daemon-only topic: not counted as a mint, transfer or revoke", dash(e.From), dash(e.Topic))
			}
			fmt.Fprintf(b, "  %s  %s%s event %s %s%s\n", clk(e), tag, dash(e.From), dash(e.Topic), short(string(e.Payload)), segText(e))
		default:
			fmt.Fprintf(b, "  %s  %s from %s verb %s corr %s %s\n", clk(e), e.Kind, dash(e.From), dash(e.Verb), dash(e.Corr), short(string(e.Payload)))
		}
	}
	if n == 0 {
		fmt.Fprintf(b, "\n== Not tied to any invoke: nothing, every entry was claimed above\n")
	}
}

func segText(e proto.Envelope) string {
	s := ""
	for _, sg := range e.Segments {
		if sg.Kind == "dmabuf" {
			s += fmt.Sprintf("  seg %s (dmabuf, %d bytes, UNSEALED, NO HASH)", short(sg.ID), sg.Bytes)
			continue
		}
		s += fmt.Sprintf("  seg %s (%d bytes, sha256 %s)", short(sg.ID), sg.Bytes, short(sg.SHA256))
	}
	return s
}

// FormatActivity renders the whole timeline for a log, oldest first.
func FormatActivity(entries []proto.Envelope) string {
	t := correlate(entries)
	var b strings.Builder
	var sm summary
	if len(entries) == 0 {
		return "no activity in the log\n"
	}
	first, last := clk(entries[0]), clk(entries[len(entries)-1])
	fmt.Fprintf(&b, "aurora activity: %d log entries, %s to %s UTC, %d invokes\n", len(entries), first, last, len(t.invokes))
	var ctl []string
	for c := range t.controllers {
		ctl = append(ctl, short(c))
	}
	sort.Strings(ctl)
	if len(ctl) > 0 {
		fmt.Fprintf(&b, "controller identity (sender of invoke replies, stamped by busd): %s\n", strings.Join(ctl, ", "))
	} else {
		fmt.Fprintf(&b, "no invoke reply in the log, so the controller's identity is unknown: any non-domain sender is treated as the controller\n")
	}
	fmt.Fprintf(&b, "Times are the bus's clock. Identities marked 'stamped by busd' come from the connection; all other text is whatever the sender wrote.\n")
	for _, iv := range t.invokes {
		t.renderInvoke(&b, iv, &sm)
	}
	t.renderHandles(&b, &sm)
	var orphans []*approvalState
	for _, id := range t.order {
		if a := t.approvals[id]; a.invoke == nil {
			orphans = append(orphans, a)
		}
	}
	if len(orphans) > 0 {
		fmt.Fprintf(&b, "\n== Approval traffic not tied to any invoke\n")
		type key struct{ from, claim string }
		groups := map[key][]*approvalState{}
		var gorder []key
		for _, a := range orphans {
			if a.requested == nil && a.decided == nil && len(a.decides) == 0 && len(a.preflights) > 0 && strings.HasPrefix(a.preflights[0].e.From, "domain:") {
				var pf actPreflight
				decode(a.preflights[0].e, &pf)
				k := key{a.preflights[0].e.From, strings.Join([]string{pf.App, pf.Verb, pf.Effect, strings.Join(pf.Grants, "; ")}, " | ")}
				if len(groups[k]) == 0 {
					gorder = append(gorder, k)
				}
				groups[k] = append(groups[k], a)
				continue
			}
			t.renderApprovalOrphan(&b, a, &sm)
		}
		for _, k := range gorder { // identical unprompted pre-flights fold into one entry
			g := groups[k]
			var ids []string
			for i, a := range g {
				if i < 12 {
					ids = append(ids, short(a.id))
				}
			}
			more := ""
			if len(g) > 12 {
				more = fmt.Sprintf(" and %d more", len(g)-12)
			}
			fmt.Fprintf(&b, "  %d pre-flight-only approval id(s) %s%s, at %s to %s: from inside %s (stamped by busd), claiming [app | verb | effect | grants] %s\n", len(g), strings.Join(ids, ", "), more, clk(g[0].preflights[0].e), clk(g[len(g)-1].preflights[0].e), short(k.from), short(k.claim))
			fmt.Fprintf(&b, "    state: nothing asked, nothing decided, no invoke; nothing in the log ties these to anything the user did\n")
			sm.add("%d unprompted pre-flight(s) from %s (ids %s): no request, decision or invoke", len(g), short(k.from), strings.Join(ids, ", "))
		}
	}
	t.renderRest(&b, &sm)

	// cross-invoke state
	epochs := []string{}
	seen := map[string]bool{}
	gestures, grants := 0, 0
	for _, iv := range t.invokes {
		if iv.reply != nil && iv.rp.OK {
			if e := iv.pv.CapEpoch; e != "" && !seen[e] {
				seen[e] = true
				epochs = append(epochs, short(e))
			}
			gs := iv.rp.Grants
			if len(gs) == 0 {
				gs = iv.pv.Grants
			}
			for _, g := range gs {
				grants++
				if g.Source != "ambient" {
					gestures++
				}
			}
		}
	}
	var warned, gest []string
	for _, iv := range t.invokes {
		if len(iv.warns) > 0 {
			warned = append(warned, fmt.Sprintf("#%d", iv.n))
		}
		if iv.gestures > 0 {
			gest = append(gest, fmt.Sprintf("#%d", iv.n))
		}
	}
	if len(warned) > 0 {
		sm.add("run logs of invokes %s carry warning lines (first one is printed under each); read them", strings.Join(warned, ", "))
	}
	if gestures > 0 {
		sm.add("%d grants (invokes %s) were not declared in any manifest: each rests on the caller's say-so for that one invoke", gestures, strings.Join(gest, ", "))
	}
	fmt.Fprintf(&b, "\n== Needs attention\n")
	if len(sm.items) == 0 {
		fmt.Fprintf(&b, "  (nothing flagged by the checks above)\n")
	}
	for _, s := range sm.items {
		fmt.Fprintf(&b, "  !! %s\n", s)
	}
	fmt.Fprintf(&b, "\n== What this log cannot tell you\n")
	if t.lifeEvents == 0 {
		fmt.Fprintf(&b, "  Capability handles: this log holds no bus.cap.* lifecycle event, so it cannot say whether any handle was minted, moved or revoked\n")
		fmt.Fprintf(&b, "  (it predates those events, or no handle was ever minted). Handle ids in grants are NOT evidence of revocation.\n")
	} else {
		fmt.Fprintf(&b, "  Capability handles: mint, transfer and revoke are in this log (above). Still not in it: the token values (only ids are logged);\n")
		fmt.Fprintf(&b, "  a TTL expiry (derived from the mint's expires field; busd logs no event for it); a mint, transfer or revoke busd refused;\n")
		fmt.Fprintf(&b, "  and anything from before this log began or from a busd run whose log is not here (a handle from then shows as 'never minted').\n")
		fmt.Fprintf(&b, "  'Never revoked' means no revoke event, not that the handle is live now: ask bus.inspect@v1 for the daemon's table.\n")
	}
	fmt.Fprintf(&b, "  Run domains: 'registered with busd' is the controller's record; nothing in this log shows a run happened inside the domain.\n")
	if len(t.invokes) > 0 {
		fmt.Fprintf(&b, "  Filesystem grants: %d across successful invokes. Their revocation is done by controllerd after the run;\n", grants)
		fmt.Fprintf(&b, "  only its own stderr records a failure, so a stale grant would not show here.\n")
	}
	if len(epochs) > 1 {
		fmt.Fprintf(&b, "  Cap epoch changed %d times (%s): controllerd reconnected or busd restarted in between; handles from earlier epochs cannot be verified from this log.\n", len(epochs)-1, strings.Join(epochs, " -> "))
	}
	return b.String()
}
