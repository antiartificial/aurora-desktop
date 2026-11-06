package index

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Grant is one filesystem grant, as the provenance record states it.
type Grant struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Source string `json:"source"`
	Cap    string `json:"cap,omitempty"`
}

// Observed is what indexd itself saw when it lstat'ed the file, at ObservedAt.
// It is indexd's own observation, not part of the provenance claim, and goes
// stale the moment it is taken.
type Observed struct {
	Exists  bool   `json:"exists"`
	Size    int64  `json:"size,omitempty"`
	ModTime string `json:"mtime,omitempty"`
	Err     string `json:"err,omitempty"`
}

// Entry is one provenance.created event, indexed. Three kinds of field:
//
//	Sender, BusID, BusTS   stamped by busd on the event (Sender comes from SO_PEERCRED)
//	ObservedAt, Seq, Obs   indexd's own clock, counter and lstat
//	everything else        the text of the record the controller published; the
//	                       app chose its output path, and DomainAttested is the
//	                       controller's claim, not something indexd checked
type Entry struct {
	Seq        int       `json:"seq"`
	ObservedAt time.Time `json:"observed_at"`
	Sender     string    `json:"sender"`
	BusID      string    `json:"bus_id"`
	BusTS      string    `json:"bus_ts"`
	Obs        *Observed `json:"observed,omitempty"`

	File           string   `json:"file"`
	App            string   `json:"app"`
	Verb           string   `json:"verb"`
	Domain         string   `json:"domain,omitempty"`
	DomainAttested bool     `json:"domain_attested,omitempty"`
	Inputs         []string `json:"inputs"`
	Grants         []Grant  `json:"grants,omitempty"`
	CapEpoch       string   `json:"cap_epoch,omitempty"`
	RecordTS       string   `json:"record_ts"`
}

// Stats says how much the index can claim to know.
type Stats struct {
	Entries      int       `json:"entries"`
	Files        int       `json:"files"`
	FirstSeen    time.Time `json:"first_seen,omitzero"`
	StartedAt    time.Time `json:"started_at"`
	Rejected     int       `json:"rejected"` // events refused since start (wrong sender, bad path)
	SkippedLines int       `json:"skipped_lines"`
}

// Index is the in-memory index: every entry in arrival order, the versions of
// each file, and the edge map input -> outputs. The append-only file is its
// only persistence; it is replayed on Open.
type Index struct {
	mu       sync.RWMutex
	f        *os.File
	entries  []*Entry
	byFile   map[string][]*Entry
	usedIn   map[string][]string // input path -> output files (latest versions only are consulted)
	seenBus  map[string]bool
	started  time.Time
	rejected int
	skipped  int
}

// Open loads (or creates) the index file in dir.
func Open(dir string) (*Index, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "index.jsonl")
	ix := &Index{byFile: map[string][]*Entry{}, usedIn: map[string][]string{}, seenBus: map[string]bool{}, started: time.Now()}
	if f, err := os.Open(p); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var e Entry
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.File == "" {
				ix.skipped++ // a torn last line after a crash, or hand edits
				continue
			}
			ix.add(&e)
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	ix.f = f
	return ix, nil
}

func (ix *Index) Close() error { return ix.f.Close() }

func (ix *Index) add(e *Entry) {
	ix.entries = append(ix.entries, e)
	ix.byFile[e.File] = append(ix.byFile[e.File], e)
	ix.seenBus[e.BusID] = true
	for _, in := range e.Inputs {
		ix.usedIn[in] = append(ix.usedIn[in], e.File)
	}
}

// Add records e, appending it to the file first. It returns false (and does
// nothing) for a bus event id already indexed. It fills Seq.
func (ix *Index) Add(e *Entry) (bool, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if e.BusID != "" && ix.seenBus[e.BusID] {
		return false, nil
	}
	e.Seq = ix.lastSeq() + 1 // not len+1: skipped lines on load would repeat a Seq
	b, err := json.Marshal(e)
	if err != nil {
		return false, err
	}
	if _, err := ix.f.Write(append(b, '\n')); err != nil {
		return false, err
	}
	if err := ix.f.Sync(); err != nil {
		return false, err
	}
	ix.add(e)
	return true, nil
}

// NoteRejected counts an event indexd refused.
func (ix *Index) NoteRejected() { ix.mu.Lock(); ix.rejected++; ix.mu.Unlock() }

// Latest returns the newest entry of every file that vis accepts (nil: all),
// oldest first. vis judges the NEWEST entry only: a file another domain has
// since overwritten is not its earlier writer's any more.
func (ix *Index) Latest(vis func(*Entry) bool) []*Entry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []*Entry
	for _, vs := range ix.byFile {
		if e := vs[len(vs)-1]; vis == nil || vis(e) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Versions returns every indexed write of file that vis accepts (nil: all),
// oldest first, and whether the file's NEWEST write is among them.
func (ix *Index) Versions(file string, vis func(*Entry) bool) (vs []*Entry, latestVisible bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	all := ix.byFile[file]
	for _, e := range all {
		if vis == nil || vis(e) {
			vs = append(vs, e)
		}
	}
	return vs, len(all) > 0 && (vis == nil || vis(all[len(all)-1]))
}

// LastSeq is the Seq of the newest entry (0 for an empty index).
func (ix *Index) LastSeq() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.lastSeq()
}

func (ix *Index) lastSeq() int {
	if len(ix.entries) == 0 {
		return 0
	}
	return ix.entries[len(ix.entries)-1].Seq
}

// After returns the entries with Seq > after that keep accepts, oldest first,
// at most limit of them, and the highest Seq it examined: the cursor to pass
// next time. Entries it skipped (not keep) are examined, so a caller never
// re-scans them. If limit cut the list short the cursor is the last returned
// entry's Seq.
func (ix *Index) After(after, limit int, keep func(*Entry) bool) (out []*Entry, cursor int) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	cursor = after
	start := sort.Search(len(ix.entries), func(i int) bool { return ix.entries[i].Seq > after })
	for _, e := range ix.entries[start:] {
		if keep(e) {
			if len(out) == limit {
				return out, cursor
			}
			out = append(out, e)
		}
		cursor = e.Seq
	}
	return out, cursor
}

// UsedIn lists the files whose latest recorded inputs include file: the
// reverse of "derived from". Sorted, deduplicated. Only files whose latest
// entry vis accepts are listed (nil: all).
func (ix *Index) UsedIn(file string, vis func(*Entry) bool) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	set := map[string]bool{}
	for _, out := range ix.usedIn[file] {
		vs := ix.byFile[out]
		if vis != nil && !vis(vs[len(vs)-1]) {
			continue
		}
		for _, in := range vs[len(vs)-1].Inputs {
			if in == file {
				set[out] = true
			}
		}
	}
	var r []string
	for o := range set {
		r = append(r, o)
	}
	sort.Strings(r)
	return r
}

func (ix *Index) Stats() Stats { return ix.StatsFor(nil) }

// StatsFor counts only what vis accepts (nil: everything): entries, files whose
// newest entry is accepted, and the first accepted entry's time. The refused
// and skipped-line counters are about indexd's own input, not about files, and
// are kept.
func (ix *Index) StatsFor(vis func(*Entry) bool) Stats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s := Stats{StartedAt: ix.started, Rejected: ix.rejected, SkippedLines: ix.skipped}
	for _, e := range ix.entries {
		if vis == nil || vis(e) {
			if s.Entries == 0 {
				s.FirstSeen = e.ObservedAt
			}
			s.Entries++
		}
	}
	for _, vs := range ix.byFile {
		if vis == nil || vis(vs[len(vs)-1]) {
			s.Files++
		}
	}
	return s
}

// IsController is the default test for "this event came from the controller":
// the bus-stamped sender is "uid<N>:controllerd". Only the controller publishes
// provenance, so anything else (a domain, any other process) is refused. The
// name is the connection's process name, which a same-user process can choose,
// so this keeps honest mistakes out, not a determined local attacker.
func IsController(sender string) bool {
	return strings.HasSuffix(sender, ":controllerd") && !strings.HasPrefix(sender, "domain:")
}

// Observe lstat's file. It never opens it and never follows a symlink.
func Observe(file string) *Observed {
	st, err := os.Lstat(file)
	if err != nil {
		if os.IsNotExist(err) {
			return &Observed{}
		}
		return &Observed{Err: err.Error()}
	}
	return &Observed{Exists: true, Size: st.Size(), ModTime: st.ModTime().UTC().Format(time.RFC3339Nano)}
}

// ProvPayload is the provenance.created payload as indexd reads it. It is
// decoded here rather than imported: the controller keeps its record type
// internal, and indexd should depend on the wire shape, not the Go type.
type ProvPayload struct {
	File           string   `json:"file"`
	App            string   `json:"app"`
	Verb           string   `json:"verb"`
	Domain         string   `json:"domain"`
	DomainAttested bool     `json:"domain_attested"`
	Inputs         []string `json:"inputs"`
	Grants         []Grant  `json:"grants"`
	CapEpoch       string   `json:"cap_epoch"`
	TS             string   `json:"ts"`
}

// FromEvent builds an Entry from an event, or says why it will not be indexed.
// sender is the bus-stamped From; whether to trust it is the caller's call
// (see IsController).
func FromEvent(sender, busID, busTS string, payload []byte, now time.Time) (*Entry, error) {
	var p ProvPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("malformed payload: %w", err)
	}
	if !filepath.IsAbs(p.File) || filepath.Clean(p.File) != p.File {
		return nil, fmt.Errorf("output %q is not a clean absolute path", p.File)
	}
	e := &Entry{ObservedAt: now, Sender: sender, BusID: busID, BusTS: busTS,
		File: p.File, App: p.App, Verb: p.Verb, Domain: p.Domain, DomainAttested: p.DomainAttested,
		Inputs: p.Inputs, Grants: p.Grants, CapEpoch: p.CapEpoch, RecordTS: p.TS}
	e.Obs = Observe(e.File)
	return e, nil
}
