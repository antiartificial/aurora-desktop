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
// Out, live updates: when an indexed event matches a saved query, indexd
// publishes index.match.<query-name> with a MatchEvent. A client subscribes to
// that topic FIRST, then asks index.run@v1 for the current matches, and drops
// live events it already got from the snapshot (same file and seq); that
// ordering leaves no gap. The saved queries directory is re-read on every
// event and request, so saving a query is writing a file and it takes effect on
// the next event; there is no registration call.
//
// WHAT THIS DOES NOT DO: the plan says "results honor capabilities: a query
// from an app or agent returns only files that domain can see". This slice
// does NOT implement that. Every caller of the index service sees every
// indexed file, and index.match.* is an ordinary bus topic that any
// subscriber receives. The replies say so in Notes. (The paths and apps in the
// index are the same ones provenance.created already broadcasts to any
// subscriber; what the index adds is aggregation and a record that outlives
// the log window, and it does not make that safer.)

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

const (
	// ServiceName is the bus service name.
	ServiceName = "index"
	// MatchTopicPrefix + query name is the live-update topic.
	MatchTopicPrefix = "index.match."
	maxHits          = 500
)

// Notes are repeated in every reply so no client has to remember them.
var Notes = []string{
	"NOT FILTERED BY CAPABILITY: every caller sees every indexed file; the plan's per-domain visibility is not implemented.",
	"Built only from provenance.created events seen by indexd: files made outside Aurora, before indexd first ran, or while it was down are not here; deletions and renames are not tracked (exists_now is an lstat at reply time).",
	"mime is derived from the file extension; indexd never opens the files. App, verb, inputs and grants are the record's text as published by the controller, not verified here.",
}

// Hit is an entry as returned to clients.
type Hit struct {
	Entry
	MIME string    `json:"mime"`
	Now  *Observed `json:"exists_now,omitempty"`
}

// MatchEvent is the payload of index.match.<name>.
type MatchEvent struct {
	Query string `json:"query"`
	Hit   Hit    `json:"hit"`
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
	for {
		select {
		case <-ctx.Done():
			return nil
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

// Ingest indexes one provenance.created event and publishes every saved query
// it matches.
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
	qs, err := LoadQueries(s.QueriesDir)
	if err != nil {
		s.Logf("indexd: cannot read queries dir: %v", err)
		return
	}
	for _, qf := range qs {
		if qf.Q == nil || !qf.Q.Match(e, s.now()) {
			continue
		}
		b, _ := json.Marshal(MatchEvent{Query: qf.Name, Hit: Hit{Entry: *e, MIME: MIMEOf(e.File)}})
		if err := s.C.Publish(MatchTopicPrefix+qf.Name, b, proto.QoSInteractive, false); err != nil {
			s.Logf("indexd: could not publish match for %s: %v", qf.Name, err)
		}
	}
}

type queryInfo struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
	Error string `json:"error,omitempty"`
}

func (s *Service) handle(r proto.Envelope) {
	reply := func(v any) {
		b, _ := json.Marshal(v)
		s.C.Reply(r, b)
	}
	bad := func(code, msg string) { s.C.ReplyError(r, code, msg) }
	base := map[string]any{"stats": s.Idx.Stats(), "notes": Notes}
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
		qs, err := LoadQueries(s.QueriesDir)
		if err != nil {
			bad("internal", err.Error())
			return
		}
		for _, qf := range qs {
			if qf.Name != p.Name {
				continue
			}
			if qf.Err != nil {
				bad("bad_query", fmt.Sprintf("query %s does not parse: %v", p.Name, qf.Err))
				return
			}
			var hits []Hit
			total := 0
			now := s.now()
			for _, e := range s.Idx.Latest() {
				if qf.Q.Match(e, now) {
					total++
					if len(hits) < maxHits {
						hits = append(hits, Hit{Entry: *e, MIME: MIMEOf(e.File), Now: Observe(e.File)})
					}
				}
			}
			base["query"] = queryInfo{Name: qf.Name, Title: qf.Q.Title}
			base["hits"], base["total"], base["truncated"] = hits, total, total > len(hits)
			reply(base)
			return
		}
		bad("no_such_query", fmt.Sprintf("no saved query %q in %s", p.Name, s.QueriesDir))
	case "index.show@v1":
		var p struct{ File string }
		if json.Unmarshal(r.Payload, &p) != nil || !strings.HasPrefix(p.File, "/") {
			bad("bad_request", "want {\"file\": <absolute path>}")
			return
		}
		vs := s.Idx.Versions(p.File)
		hits := []Hit{}
		for _, e := range vs {
			hits = append(hits, Hit{Entry: *e, MIME: MIMEOf(e.File), Now: Observe(e.File)})
		}
		base["versions"] = hits
		base["used_in"] = s.Idx.UsedIn(p.File)
		reply(base)
	default:
		bad("unknown_verb", "unknown verb "+r.Verb)
	}
}
