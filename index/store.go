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
	e.Seq = len(ix.entries) + 1
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

// Latest returns the newest entry of every file, oldest first.
func (ix *Index) Latest() []*Entry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var out []*Entry
	for _, vs := range ix.byFile {
		out = append(out, vs[len(vs)-1])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Versions returns every indexed write of file, oldest first.
func (ix *Index) Versions(file string) []*Entry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return append([]*Entry(nil), ix.byFile[file]...)
}

// UsedIn lists the files whose latest recorded inputs include file: the
// reverse of "derived from". Sorted, deduplicated.
func (ix *Index) UsedIn(file string) []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	set := map[string]bool{}
	for _, out := range ix.usedIn[file] {
		vs := ix.byFile[out]
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

func (ix *Index) Stats() Stats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s := Stats{Entries: len(ix.entries), Files: len(ix.byFile), StartedAt: ix.started, Rejected: ix.rejected, SkippedLines: ix.skipped}
	if len(ix.entries) > 0 {
		s.FirstSeen = ix.entries[0].ObservedAt
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
