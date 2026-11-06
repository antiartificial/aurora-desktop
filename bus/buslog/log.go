// Package buslog is the append-only, segmented message log.
//
// Layout: <dir>/seg-000001.jsonl, seg-000002.jsonl, ... one JSON envelope per
// line. Each segment's min/max wall time is kept in memory (rebuilt by a scan
// on open) so time queries skip old segments. Ephemeral messages are never
// written. Capability handles are never written: only a stable hash.
package buslog

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/antiartificial/aurora/bus/hlc"
	"github.com/antiartificial/aurora/bus/proto"
)

// DefaultSegmentBytes is the rotation threshold.
const DefaultSegmentBytes = 4 << 20

type segment struct {
	path             string
	minWall, maxWall int64
}

// Log is safe for concurrent use.
type Log struct {
	dir      string
	maxBytes int64

	mu   sync.RWMutex
	segs []*segment
	cur  *os.File
	size int64
}

// CapID is the stable identity of a capability handle: a hash, not the value.
func CapID(handle string) string {
	h := sha256.Sum256([]byte(handle))
	return "cap:" + hex.EncodeToString(h[:8])
}

// LogForm returns the envelope as it is stored: handles replaced by hashes.
func LogForm(e proto.Envelope) proto.Envelope {
	if len(e.Caps) > 0 {
		ids := make([]string, len(e.Caps))
		for i, c := range e.Caps {
			ids[i] = CapID(c)
		}
		e.CapIDs = ids
	}
	e.Caps = nil
	e.Attach = nil // never a live fd; Segments keeps only descriptor + hash
	return e
}

// Open opens (or creates) a log directory.
func Open(dir string, maxBytes int64) (*Log, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultSegmentBytes
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	l := &Log{dir: dir, maxBytes: maxBytes}
	paths, err := segmentPaths(dir)
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		s := &segment{path: p}
		err := scanFile(p, func(e proto.Envelope) bool {
			if ts, err := hlc.Parse(e.TS); err == nil {
				if s.minWall == 0 || ts.Wall < s.minWall {
					s.minWall = ts.Wall
				}
				if ts.Wall > s.maxWall {
					s.maxWall = ts.Wall
				}
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		l.segs = append(l.segs, s)
	}
	return l, nil
}

func segmentPaths(dir string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "seg-*.jsonl"))
	sort.Strings(paths)
	return paths, err
}

func (l *Log) rotate() error {
	if l.cur != nil {
		l.cur.Close()
	}
	p := filepath.Join(l.dir, fmt.Sprintf("seg-%06d.jsonl", len(l.segs)+1))
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	l.cur, l.size = f, 0
	l.segs = append(l.segs, &segment{path: p})
	return nil
}

// Append writes one message. Ephemeral messages are ignored; handles are
// replaced by hashes whatever the caller passes.
func (l *Log) Append(e proto.Envelope) error {
	if e.Ephemeral {
		return nil
	}
	b, err := json.Marshal(LogForm(e))
	if err != nil {
		return err
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur == nil || l.size >= l.maxBytes {
		if l.cur == nil && len(l.segs) > 0 {
			// Reopening: append to the last segment if it has room.
			last := l.segs[len(l.segs)-1]
			if st, err := os.Stat(last.path); err == nil && st.Size() < l.maxBytes {
				f, err := os.OpenFile(last.path, os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil {
					return err
				}
				l.cur, l.size = f, st.Size()
			}
		}
		if l.cur == nil || l.size >= l.maxBytes {
			if err := l.rotate(); err != nil {
				return err
			}
		}
	}
	n, err := l.cur.Write(b)
	l.size += int64(n)
	if err != nil {
		return err
	}
	if ts, err := hlc.Parse(e.TS); err == nil {
		s := l.segs[len(l.segs)-1]
		if s.minWall == 0 || ts.Wall < s.minWall {
			s.minWall = ts.Wall
		}
		if ts.Wall > s.maxWall {
			s.maxWall = ts.Wall
		}
	}
	return nil
}

// Query selects messages. Zero fields mean "no constraint".
type Query struct {
	SinceWall int64  // unix ns; messages at or after
	Topic     string // glob; when set only events with a matching topic
	Limit     int    // keep the most recent N (0 = all)
}

// Query returns matching messages in log order (the most recent Limit).
func (l *Log) Query(q Query) ([]proto.Envelope, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []proto.Envelope
	for _, s := range l.segs {
		if q.SinceWall > 0 && s.maxWall > 0 && s.maxWall < q.SinceWall {
			continue
		}
		err := scanFile(s.path, func(e proto.Envelope) bool {
			if q.SinceWall > 0 {
				ts, err := hlc.Parse(e.TS)
				if err != nil || ts.Wall < q.SinceWall {
					return true
				}
			}
			if q.Topic != "" && !(e.Topic != "" && proto.Match(q.Topic, e.Topic)) {
				return true
			}
			out = append(out, e)
			if q.Limit > 0 && len(out) > q.Limit {
				out = out[1:]
			}
			return true
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Close closes the active segment.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur != nil {
		err := l.cur.Close()
		l.cur = nil
		return err
	}
	return nil
}

// ReadFile reads every message from a segment file, or from all segments in
// a directory, in order.
func ReadFile(path string) ([]proto.Envelope, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	paths := []string{path}
	if st.IsDir() {
		if paths, err = segmentPaths(path); err != nil {
			return nil, err
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("no seg-*.jsonl files in %s", path)
		}
	}
	var out []proto.Envelope
	for _, p := range paths {
		if err := scanFile(p, func(e proto.Envelope) bool { out = append(out, e); return true }); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// scanFile calls fn per decodable line; a torn final line is skipped.
func scanFile(path string, fn func(proto.Envelope) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && strings.HasSuffix(string(line), "\n") {
			var e proto.Envelope
			if json.Unmarshal(line, &e) == nil && !fn(e) {
				return nil
			}
		}
		if err != nil {
			return nil
		}
	}
}
