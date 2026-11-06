package launcher

// `aurora query`: saved queries over the index that indexd (index/) keeps.
//
//	aurora query                      list saved queries
//	aurora query run NAME [--watch]   run one; --watch then streams new matches
//	aurora query show FILE            one file's indexed history and edges
//
// Conventions kept from activity.go: a string that reaches the terminal and
// was not typed by the user (paths, app ids, verbs, titles, error text) goes
// through short(), which is clean(); an identity is only labelled "stamped by
// busd" when it is the bus's own From; nothing here says "attested" about a
// claim, only that the controller said so.
//
// What this command does NOT do, and says every time it runs (queryCaveats):
// the plan's "results honor capabilities" is not implemented. indexd answers
// every caller with every indexed file and this command applies no filter of
// its own. And the index is built only from provenance.created events, so it
// knows nothing of files made outside Aurora, before indexd ran, or deleted
// since.
//
// The live path: subscribe to index.match.NAME first, then ask for the
// current matches, print those, then print live events, skipping any the
// snapshot already contained. A live event is believed only if its bus-stamped
// sender is indexd's connection (name "indexd", not a domain); the name is the
// process name, which a same-user process can choose.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

const (
	indexService     = "index"
	indexMatchPrefix = "index.match."
)

// queryNameRE mirrors indexd's rule for query names (they become topics).
var queryNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

const queryCaveats = `  NOT FILTERED BY CAPABILITY: every caller gets every indexed file; the plan's per-domain visibility is not implemented.
  Only files whose provenance.created event indexd saw are here. Files made outside Aurora, before indexd first ran, or while it was down are
  invisible; deletions and renames are not tracked (the present/MISSING mark is an lstat taken when the answer was made).
  Type comes from the file extension (indexd never opens files). App, verb, inputs and domain are the controller's published text, not verified here.`

// ---- wire types (decoded from indexd; all text is app- or controller-supplied) ----

type QueryObserved struct {
	Exists  bool   `json:"exists"`
	Size    int64  `json:"size"`
	ModTime string `json:"mtime"`
	Err     string `json:"err"`
}

type QueryHit struct {
	Seq            int            `json:"seq"`
	ObservedAt     string         `json:"observed_at"`
	Sender         string         `json:"sender"`
	BusID          string         `json:"bus_id"`
	BusTS          string         `json:"bus_ts"`
	Obs            *QueryObserved `json:"observed"`
	Now            *QueryObserved `json:"exists_now"`
	File           string         `json:"file"`
	App            string         `json:"app"`
	Verb           string         `json:"verb"`
	Domain         string         `json:"domain"`
	DomainAttested bool           `json:"domain_attested"`
	Inputs         []string       `json:"inputs"`
	Grants         []actGrant     `json:"grants"`
	CapEpoch       string         `json:"cap_epoch"`
	RecordTS       string         `json:"record_ts"`
	MIME           string         `json:"mime"`
}

type QueryStats struct {
	Entries      int    `json:"entries"`
	Files        int    `json:"files"`
	FirstSeen    string `json:"first_seen"`
	StartedAt    string `json:"started_at"`
	Rejected     int    `json:"rejected"`
	SkippedLines int    `json:"skipped_lines"`
}

type QueryInfo struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Error string `json:"error"`
}

type QueryList struct {
	Queries []QueryInfo `json:"queries"`
	Stats   QueryStats  `json:"stats"`
}

type QueryRun struct {
	Query     QueryInfo  `json:"query"`
	Hits      []QueryHit `json:"hits"`
	Total     int        `json:"total"`
	Truncated bool       `json:"truncated"`
	Stats     QueryStats `json:"stats"`
}

type QueryShow struct {
	Versions []QueryHit `json:"versions"`
	UsedIn   []string   `json:"used_in"`
	Stats    QueryStats `json:"stats"`
}

// QueryBackend is the optional half of a Backend that can talk to indexd.
type QueryBackend interface {
	QueryList(ctx context.Context) (QueryList, error)
	QueryRun(ctx context.Context, name string) (QueryRun, error)
	QueryShow(ctx context.Context, file string) (QueryShow, error)
	// QueryWatch subscribes, takes the snapshot (passed to snap), then calls
	// live for each new match event, with the event's bus-stamped sender, until
	// ctx ends. Hits already in the snapshot are not repeated.
	QueryWatch(ctx context.Context, name string, snap func(QueryRun), live func(sender string, h QueryHit)) error
}

func (b *busBackend) callIndex(ctx context.Context, verb string, req, resp any) error {
	c, err := b.conn()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	r, err := c.Call(ctx, proto.Envelope{To: indexService, Verb: verb, Payload: payload})
	if err != nil {
		var re *client.RemoteError
		if errors.As(err, &re) {
			if re.Code == "no_such_service" {
				return errors.New("the index service is not running: start `indexd`")
			}
			return fmt.Errorf("%s: %s: %s", verb, re.Code, re.Message)
		}
		if errors.Is(err, client.ErrClosed) {
			return fmt.Errorf("%w (connection closed)", ErrBusDown)
		}
		return fmt.Errorf("%s: %w", verb, err)
	}
	if err := json.Unmarshal(r.Payload, resp); err != nil {
		return fmt.Errorf("%s: malformed reply: %w", verb, err)
	}
	return nil
}

func (b *busBackend) QueryList(ctx context.Context) (out QueryList, err error) {
	err = b.callIndex(ctx, "index.queries@v1", struct{}{}, &out)
	return
}

func (b *busBackend) QueryRun(ctx context.Context, name string) (out QueryRun, err error) {
	err = b.callIndex(ctx, "index.run@v1", map[string]string{"name": name}, &out)
	return
}

func (b *busBackend) QueryShow(ctx context.Context, file string) (out QueryShow, err error) {
	err = b.callIndex(ctx, "index.show@v1", map[string]string{"file": file}, &out)
	return
}

func (b *busBackend) QueryWatch(ctx context.Context, name string, snap func(QueryRun), live func(string, QueryHit)) error {
	c, err := b.conn()
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, b.timeout)
	err = c.Subscribe(sctx, indexMatchPrefix+name)
	cancel()
	if err != nil {
		return fmt.Errorf("subscribe %s%s: %w", indexMatchPrefix, name, err)
	}
	run, err := b.QueryRun(ctx, name) // after the subscribe: no gap
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, h := range run.Hits {
		seen[fmt.Sprintf("%s\x00%d", h.File, h.Seq)] = true
	}
	snap(run)
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-c.Events():
			if !ok {
				return fmt.Errorf("%w (connection closed)", ErrBusDown)
			}
			var m struct {
				Hit QueryHit `json:"hit"`
			}
			if json.Unmarshal(e.Payload, &m) != nil {
				continue
			}
			k := fmt.Sprintf("%s\x00%d", m.Hit.File, m.Hit.Seq)
			if seen[k] {
				continue
			}
			seen[k] = true
			live(e.From, m.Hit)
		}
	}
}

// trustedIndexer says whether a bus-stamped sender may speak for indexd.
func trustedIndexer(from string) bool {
	return strings.HasSuffix(from, ":indexd") && !strings.HasPrefix(from, "domain:")
}

// ---- command ----

func queryBackend(env *Env, c *common) (QueryBackend, func(), error) {
	be := env.Backend(c.socket, c.timeout)
	qb, ok := be.(QueryBackend)
	if !ok {
		be.Close()
		return nil, nil, errors.New("this backend cannot reach the index")
	}
	return qb, func() { be.Close() }, nil
}

func cmdQuery(env *Env, args []string) int {
	var c common
	fs := newFlags(env, "query", "query [list | run NAME [--watch] [--count N] [--json] | show FILE [--json]]", &c)
	watch := fs.Bool("watch", false, "run: after the current matches, stream new ones as they are indexed")
	count := fs.Int("count", 0, "run --watch: exit after N new matches (0 = until interrupted)")
	asJSON := fs.Bool("json", false, "run/show: one JSON object per line instead of text")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return parseFailed(err)
	}
	sub := "list"
	if len(pos) > 0 {
		sub, pos = pos[0], pos[1:]
	}
	need := func(n int, what string) bool {
		if len(pos) != n {
			fmt.Fprintf(env.Err, "aurora query %s: want %s\n", sub, what)
			return false
		}
		return true
	}
	switch sub {
	case "list":
		if !need(0, "no arguments") {
			return 2
		}
	case "run":
		if !need(1, "a query name") {
			return 2
		}
		if !queryNameRE.MatchString(pos[0]) {
			fmt.Fprintf(env.Err, "aurora query run: %q is not a query name (lowercase letters, digits, - and _)\n", clean(pos[0]))
			return 2
		}
	case "show":
		if !need(1, "an absolute file path") {
			return 2
		}
		if !strings.HasPrefix(pos[0], "/") {
			fmt.Fprintf(env.Err, "aurora query show: %q is not an absolute path\n", clean(pos[0]))
			return 2
		}
	default:
		fmt.Fprintf(env.Err, "aurora query: unknown subcommand %q (list, run, show)\n", clean(sub))
		return 2
	}
	if *watch && sub != "run" {
		fmt.Fprintln(env.Err, "aurora query: --watch only applies to `run`")
		return 2
	}
	qb, closeBE, err := queryBackend(env, &c)
	if err != nil {
		return fail(env, err)
	}
	defer closeBE()
	ctx, stop := signalCtx()
	defer stop()
	switch sub {
	case "list":
		return queryList(ctx, env, qb)
	case "show":
		return queryShow(ctx, env, qb, pos[0], *asJSON)
	}
	return queryRun(ctx, env, qb, pos[0], *watch, *count, *asJSON)
}

func queryList(ctx context.Context, env *Env, qb QueryBackend) int {
	l, err := qb.QueryList(ctx)
	if err != nil {
		return fail(env, err)
	}
	if len(l.Queries) == 0 {
		fmt.Fprintln(env.Out, "no saved queries (write NAME.query into indexd's queries directory)")
	}
	for _, q := range l.Queries {
		if q.Error != "" {
			fmt.Fprintf(env.Out, "%-24s !! does not parse: %s\n", short(q.Name), short(q.Error))
			continue
		}
		fmt.Fprintf(env.Out, "%-24s %s\n", short(q.Name), dash(q.Title))
	}
	fmt.Fprintf(env.Out, "\n%s\n", statsLine(l.Stats))
	return 0
}

func statsLine(s QueryStats) string {
	out := fmt.Sprintf("index: %d entries for %d files", s.Entries, s.Files)
	if s.Entries > 0 {
		out += ", first seen " + wall(s.FirstSeen)
	}
	out += ", indexd running since " + wall(s.StartedAt)
	if s.SkippedLines > 0 {
		out += fmt.Sprintf("; %d unreadable line(s) skipped on load", s.SkippedLines)
	}
	return out
}

func wall(ts string) string {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.Local().Format("2006-01-02 15:04:05")
	}
	return short(ts)
}

func clock(ts string) string {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.Local().Format("15:04:05.000")
	}
	return "??:??:??.???"
}

// presence is a lstat result, labelled with when it was taken.
func presence(h QueryHit) string {
	o, when := h.Now, "now"
	if o == nil {
		o, when = h.Obs, "when indexed"
	}
	switch {
	case o == nil:
		return "[not checked]"
	case o.Err != "":
		return "[unreadable " + when + ": " + short(o.Err) + "]"
	case o.Exists:
		return "[present " + when + "]"
	}
	return "[MISSING " + when + "]"
}

func hitLine(h QueryHit, tag string) string {
	s := fmt.Sprintf("%s%s  %s  %s  %s %s", tag, clock(h.ObservedAt), short(h.File), short(h.MIME), dash(h.App), dash(h.Verb))
	if len(h.Inputs) > 0 {
		in := make([]string, len(h.Inputs))
		for i, p := range h.Inputs {
			in[i] = short(p)
		}
		s += "  <- " + strings.Join(in, ", ")
	}
	return s + "  " + presence(h)
}

func queryRun(ctx context.Context, env *Env, qb QueryBackend, name string, watch bool, count int, asJSON bool) int {
	emit := func(live bool, h QueryHit) {
		if asJSON {
			b, _ := json.Marshal(map[string]any{"live": live, "hit": h})
			fmt.Fprintln(env.Out, string(b))
			return
		}
		tag := "   "
		if live {
			tag = "+  "
		}
		fmt.Fprintln(env.Out, hitLine(h, tag))
	}
	snap := func(r QueryRun) {
		if !asJSON {
			fmt.Fprintf(env.Out, "query %s: %s\n", short(r.Query.Name), dash(r.Query.Title))
			fmt.Fprintf(env.Out, "%s\n", statsLine(r.Stats))
			fmt.Fprintf(env.Out, "%d match(es)%s; times are indexd's clock when it saw the event; app/verb are the record's text\n", r.Total,
				map[bool]string{true: fmt.Sprintf(" (first %d shown)", len(r.Hits)), false: ""}[r.Truncated])
		}
		for _, h := range r.Hits {
			emit(false, h)
		}
		if !asJSON {
			fmt.Fprintf(env.Out, "%s\n", queryCaveats)
			if watch {
				fmt.Fprintln(env.Out, "watching index.match."+short(r.Query.Name)+" (Ctrl-C to stop); '+' marks a new match")
			}
		}
	}
	if !watch {
		r, err := qb.QueryRun(ctx, name)
		if err != nil {
			return fail(env, err)
		}
		snap(r)
		return 0
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	n := 0
	err := qb.QueryWatch(ctx, name, snap, func(from string, h QueryHit) {
		if !trustedIndexer(from) {
			fmt.Fprintf(env.Err, "aurora: ignored an index.match event from %s (stamped by busd): not indexd\n", short(from))
			return
		}
		emit(true, h)
		if n++; count > 0 && n >= count {
			cancel()
		}
	})
	if err != nil {
		return fail(env, err)
	}
	return 0
}

func queryShow(ctx context.Context, env *Env, qb QueryBackend, file string, asJSON bool) int {
	s, err := qb.QueryShow(ctx, file)
	if err != nil {
		return fail(env, err)
	}
	if asJSON {
		b, _ := json.Marshal(s)
		fmt.Fprintln(env.Out, string(b))
		if len(s.Versions) == 0 {
			return 1
		}
		return 0
	}
	fmt.Fprintf(env.Out, "file %s\n", short(file))
	if len(s.Versions) == 0 {
		fmt.Fprintf(env.Out, "  not in the index. That does NOT mean Aurora did not make it: indexd only knows provenance.created events it saw.\n")
		if len(s.UsedIn) > 0 {
			fmt.Fprintf(env.Out, "  used in: %s\n", joinShort(s.UsedIn))
		}
		fmt.Fprintf(env.Out, "\n%s\n", statsLine(s.Stats))
		return 1
	}
	fmt.Fprintf(env.Out, "  %s, %d indexed write(s), oldest first; history reaches back only to %s\n",
		presence(s.Versions[len(s.Versions)-1]), len(s.Versions), wall(s.Stats.FirstSeen))
	for i, h := range s.Versions {
		fmt.Fprintf(env.Out, "\n  write %d of %d, index seq %d, seen by indexd at %s\n", i+1, len(s.Versions), h.Seq, wall(h.ObservedAt))
		fmt.Fprintf(env.Out, "    by         %s %s (the record's text; type %s from the extension)\n", dash(h.App), dash(h.Verb), dash(h.MIME))
		fmt.Fprintf(env.Out, "    published  by %s (stamped by busd), bus event %s\n", dash(h.Sender), dash(h.BusID))
		fmt.Fprintf(env.Out, "    record ts  %s (the controller's clock text)\n", dash(h.RecordTS))
		dom := dash(h.Domain)
		if h.Domain != "" {
			dom += map[bool]string{true: " (controller says the bus attested it; indexd did not check)", false: " (controller did NOT claim the bus attested it)"}[h.DomainAttested]
		}
		fmt.Fprintf(env.Out, "    domain     %s\n", dom)
		fmt.Fprintf(env.Out, "    derived from %s\n", joinShort(h.Inputs))
		for _, g := range h.Grants {
			fmt.Fprintf(env.Out, "    grant      %s %s, source %s, handle %s\n", dash(g.Mode), dash(g.Path), dash(g.Source), dash(g.Cap))
		}
		if h.CapEpoch != "" {
			fmt.Fprintf(env.Out, "    cap epoch  %s\n", short(h.CapEpoch))
		}
	}
	fmt.Fprintf(env.Out, "\n  used in (files whose latest write names this one as an input): %s\n", joinShort(s.UsedIn))
	fmt.Fprintf(env.Out, "\n%s\n%s\n", statsLine(s.Stats), queryCaveats)
	fmt.Fprintf(env.Out, "  Identities marked 'stamped by busd' come from the connection; every other value is text the sender wrote.\n")
	return 0
}

func joinShort(ss []string) string {
	if len(ss) == 0 {
		return "(none recorded)"
	}
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = short(s)
	}
	return strings.Join(out, ", ")
}
