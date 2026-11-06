package index

// The bus face of indexd.
//
// In: provenance.created events (the same events `aurora watch` shows), which
// are the only thing indexd learns from.
//
// Out, requests to the service "index":
//
//	index.queries@v1  {}                -> {queries:[{name,title,error}], stats, notes}
//	index.run@v1      {name}            -> {query, hits:[Hit], total, truncated, stats, notes}
//	index.show@v1     {file}            -> {versions:[Hit], derived_from, used_in, stats, notes}
//
//	index.next@v1     {name, after, wait_ms} -> {hits:[Hit], as_of_seq, viewer, stats, notes}
//
// Live updates are a long poll, not a topic. index.run@v1 returns as_of_seq;
// the client then calls index.next@v1 with after = that number. indexd answers
// at once with every matching WRITE after that sequence number (not only the
// newest write of each file), or, if there is none, holds the request and
// answers the moment one arrives or wait_ms (at most maxWait) passes. The
// client repeats with after = the as_of_seq it was given. That leaves no gap
// and no duplicate: the cursor is indexd's own sequence number, and an entry
// the caller may not see still advances it. The saved queries directory is
// re-read on every event and request, so saving a query is writing a file and
// it takes effect on the next call; there is no registration call.
//
// WHO SEES WHAT: see access.go. The caller is the From busd stamped on the
// request, never anything in the payload. Every reply says whose view it is
// ("viewer") and the rule, and both reply and live paths are filtered the same
// way: a match is only ever put into a reply addressed to the caller that
// asked, and indexd publishes NO index.match.* event, because a bus topic
// reaches every subscriber and the bus has no per-subscriber filter. A domain
// that subscribes to index.match.** now receives nothing.
//
// WHAT THIS DOES NOT CLOSE: provenance.created, the event indexd learns from,
// is still an ordinary broadcast topic. Any connection, a domain included, may
// subscribe to it and read every record in full (file, app, domain, inputs,
// grants). The index filter does not and cannot change that; closing it needs
// busd to refuse domain-socket subscriptions to that topic (and to the
// wildcards that match it). The replies say so.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

const (
	// ServiceName is the bus service name.
	ServiceName = "index"
	maxHits     = 500
	// maxWait caps how long index.next@v1 holds a request, and maxWaiters how
	// many it holds; the caller's own timeout must be longer than wait_ms.
	maxWait    = 25 * time.Second
	maxWaiters = 256
)

// notesFor is repeated in every reply so no client has to remember it.
func notesFor(v Viewer) []string {
	return []string{
		"VIEW: " + v.Rule() + " The identity is the From busd stamped on your request; nothing you send can change it.",
		"THE FILTER COVERS THIS SERVICE ONLY: provenance.created, which indexd learns from, is still an ordinary bus topic that any subscriber, a domain included, receives in full. Closing that needs a topic restriction in busd; this index cannot do it.",
		"Visibility rests on the Domain text of the controller's provenance record (the sender is checked to be the controller; the text is not verified). Records with no domain (unisolated runs) are visible to the session only. seq and as_of_seq are global, so gaps in them show that entries you cannot see exist, not what they are. Saved-query names and titles are the user's and are listed to every caller.",
		"Built only from provenance.created events seen by indexd: files made outside Aurora, before indexd first ran, or while it was down are not here; deletions and renames are not tracked (exists_now is an lstat at reply time).",
		"mime is derived from the file extension; indexd never opens the files. App, verb, inputs and grants are the record's text as published by the controller, not verified here.",
	}
}

// Hit is an entry as returned to clients.
type Hit struct {
	Entry
	MIME string    `json:"mime"`
	Now  *Observed `json:"exists_now,omitempty"`
}

type Service struct {
	Idx        *Index
	QueriesDir string
	C          *client.Client
	Logf       func(string, ...any)
	Now        func() time.Time
	// Controller decides whether a bus-stamped sender may publish provenance
	// (default IsController); tests replace it.
	Controller func(sender string) bool

	mu      sync.Mutex
	waiters []waiter // parked index.next@v1 requests
}

// waiter is a parked index.next@v1: who asked (the bus-stamped From), for which
// query, after which sequence number, until when.
type waiter struct {
	req      proto.Envelope
	who      Viewer
	query    string
	after    int
	deadline time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Run subscribes, registers and serves until the connection ends or ctx does.
func (s *Service) Run(ctx context.Context) error {
	if err := s.C.Subscribe(ctx, "provenance.created"); err != nil {
		return fmt.Errorf("subscribe provenance.created: %w", err)
	}
	if err := s.C.Register(ctx, ServiceName); err != nil {
		return fmt.Errorf("register %s: %w", ServiceName, err)
	}
	s.Logf("indexd: serving %q; queries in %s; %d entries already indexed", ServiceName, s.QueriesDir, s.Idx.Stats().Entries)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			s.expire()
		case e, ok := <-s.C.Events():
			if !ok {
				return fmt.Errorf("bus connection closed")
			}
			if e.Topic == "provenance.created" {
				s.Ingest(e)
			}
		case r, ok := <-s.C.Requests():
			if !ok {
				return fmt.Errorf("bus connection closed")
			}
			s.handle(r)
		}
	}
}

// Ingest indexes one provenance.created event and answers every parked
// index.next@v1 it satisfies. It publishes nothing to the bus.
func (s *Service) Ingest(ev proto.Envelope) {
	trust := s.Controller
	if trust == nil {
		trust = IsController
	}
	if !trust(ev.From) {
		s.Idx.NoteRejected()
		s.Logf("indexd: ignored provenance.created from %q: not the controller", ev.From)
		return
	}
	e, err := FromEvent(ev.From, ev.ID, ev.TS, ev.Payload, s.now())
	if err != nil {
		s.Idx.NoteRejected()
		s.Logf("indexd: ignored provenance.created: %v", err)
		return
	}
	added, err := s.Idx.Add(e)
	if err != nil {
		s.Logf("indexd: WARNING: could not persist entry for %s: %v (not indexed)", e.File, err)
		return
	}
	if !added {
		return
	}
	s.wake()
}

type queryInfo struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
	Error string `json:"error,omitempty"`
}

// find returns the named saved query, or says why it cannot be run.
func (s *Service) find(name string) (*QueryFile, string, string) {
	qs, err := LoadQueries(s.QueriesDir)
	if err != nil {
		return nil, "internal", err.Error()
	}
	for i := range qs {
		if qs[i].Name != name {
			continue
		}
		if qs[i].Err != nil {
			return nil, "bad_query", fmt.Sprintf("query %s does not parse: %v", name, qs[i].Err)
		}
		return &qs[i], "", ""
	}
	return nil, "no_such_query", fmt.Sprintf("no saved query %q in %s", name, s.QueriesDir)
}

// base is the part of every reply: whose view it is, what that view can count,
// and the notes.
func (s *Service) base(who Viewer) map[string]any {
	return map[string]any{"viewer": who.Info(), "stats": s.Idx.StatsFor(who.Sees), "notes": notesFor(who)}
}

// next is the writes after cursor that q matches and who may see.
func (s *Service) next(who Viewer, q *Query, after int) ([]Hit, int) {
	now := s.now()
	es, cursor := s.Idx.After(after, maxHits, func(e *Entry) bool { return who.Sees(e) && q.Match(e, now) })
	hits := []Hit{}
	for _, e := range es {
		hits = append(hits, Hit{Entry: *e, MIME: MIMEOf(e.File)})
	}
	return hits, cursor
}

func (s *Service) replyNext(r proto.Envelope, who Viewer, hits []Hit, cursor int) {
	b := s.base(who)
	b["hits"], b["as_of_seq"] = hits, cursor
	j, _ := json.Marshal(b)
	s.C.Reply(r, j)
}

// wake answers the parked requests that now have something to say.
func (s *Service) wake() {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.waiters[:0]
	for _, w := range s.waiters {
		qf, code, msg := s.find(w.query)
		if qf == nil {
			s.C.ReplyError(w.req, code, msg)
			continue
		}
		if hits, cursor := s.next(w.who, qf.Q, w.after); len(hits) > 0 {
			s.replyNext(w.req, w.who, hits, cursor)
			continue
		}
		keep = append(keep, w)
	}
	s.waiters = keep
}

// expire answers parked requests whose wait is over, with no hits and the
// cursor advanced past everything scanned.
func (s *Service) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	keep := s.waiters[:0]
	for _, w := range s.waiters {
		if now.Before(w.deadline) {
			keep = append(keep, w)
			continue
		}
		hits, cursor := []Hit{}, w.after
		if qf, _, _ := s.find(w.query); qf != nil {
			hits, cursor = s.next(w.who, qf.Q, w.after)
		}
		s.replyNext(w.req, w.who, hits, cursor)
	}
	s.waiters = keep
}

func (s *Service) handle(r proto.Envelope) {
	who := Viewer(r.From) // stamped by busd; never read from the payload
	reply := func(v any) {
		b, _ := json.Marshal(v)
		s.C.Reply(r, b)
	}
	bad := func(code, msg string) { s.C.ReplyError(r, code, msg) }
	base := s.base(who)
	switch r.Verb {
	case "index.queries@v1":
		qs, err := LoadQueries(s.QueriesDir)
		if err != nil {
			bad("internal", err.Error())
			return
		}
		infos := []queryInfo{}
		for _, qf := range qs {
			qi := queryInfo{Name: qf.Name}
			if qf.Q != nil {
				qi.Title = qf.Q.Title
			}
			if qf.Err != nil {
				qi.Error = qf.Err.Error()
			}
			infos = append(infos, qi)
		}
		base["queries"] = infos
		reply(base)
	case "index.run@v1":
		var p struct{ Name string }
		if json.Unmarshal(r.Payload, &p) != nil || !NameRE.MatchString(p.Name) {
			bad("bad_request", "want {\"name\": <query name>}")
			return
		}
		qf, code, msg := s.find(p.Name)
		if qf == nil {
			bad(code, msg)
			return
		}
		asOf := s.Idx.LastSeq() // before the scan; Run and Ingest share a goroutine, so nothing lands between
		var hits []Hit
		total := 0
		now := s.now()
		for _, e := range s.Idx.Latest(who.Sees) {
			if qf.Q.Match(e, now) {
				total++
				if len(hits) < maxHits {
					hits = append(hits, Hit{Entry: *e, MIME: MIMEOf(e.File), Now: Observe(e.File)})
				}
			}
		}
		base["query"] = queryInfo{Name: qf.Name, Title: qf.Q.Title}
		base["hits"], base["total"], base["truncated"], base["as_of_seq"] = hits, total, total > len(hits), asOf
		reply(base)
	case "index.next@v1":
		var p struct {
			Name   string
			After  int
			WaitMS int `json:"wait_ms"`
		}
		if json.Unmarshal(r.Payload, &p) != nil || !NameRE.MatchString(p.Name) || p.After < 0 || p.WaitMS < 0 {
			bad("bad_request", "want {\"name\": <query name>, \"after\": <as_of_seq>, \"wait_ms\": <0..25000>}")
			return
		}
		qf, code, msg := s.find(p.Name)
		if qf == nil {
			bad(code, msg)
			return
		}
		hits, cursor := s.next(who, qf.Q, p.After)
		if len(hits) > 0 || p.WaitMS == 0 {
			s.replyNext(r, who, hits, cursor)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.waiters) >= maxWaiters {
			bad("busy", "too many parked index.next@v1 requests")
			return
		}
		wait := min(time.Duration(p.WaitMS)*time.Millisecond, maxWait)
		s.waiters = append(s.waiters, waiter{req: r, who: who, query: p.Name, after: cursor, deadline: s.now().Add(wait)})
	case "index.show@v1":
		var p struct{ File string }
		if json.Unmarshal(r.Payload, &p) != nil || !strings.HasPrefix(p.File, "/") {
			bad("bad_request", "want {\"file\": <absolute path>}")
			return
		}
		vs, latestVisible := s.Idx.Versions(p.File, who.Sees)
		hits := []Hit{}
		for _, e := range vs {
			h := Hit{Entry: *e, MIME: MIMEOf(e.File)}
			if latestVisible { // the file on disk is the newest write; if that is not yours, its size and mtime are not yours to learn
				h.Now = Observe(e.File)
			}
			hits = append(hits, h)
		}
		base["versions"] = hits
		base["used_in"] = s.Idx.UsedIn(p.File, who.Sees)
		reply(base)
	default:
		bad("unknown_verb", "unknown verb "+r.Verb)
	}
}
