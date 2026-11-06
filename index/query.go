package index

// The saved-query format.
//
// A saved query is one small text file, <name>.query, in the queries
// directory (the plan's /queries/). One `key: value` per line, `#` starts a
// comment, blank lines are ignored:
//
//	# Images exported this week
//	title: Images exported this week
//	mime: image/*
//	since: this-week
//	app: org.aurora.magick
//
// Different keys are ANDed; a repeated key is ORed (two `mime:` lines match
// either). Keys:
//
//	title         display text only (not a predicate)
//	mime          MIME type or type/* , derived from the file EXTENSION (see MIMEOf)
//	ext           file extension without the dot
//	app           producing app id (what the provenance record says)
//	verb          verb name; "convert" matches "convert@v1", "convert@v1" matches exactly
//	since         when indexd first saw the file: today | this-week | this-month
//	              (calendar, local time, week starts Monday) or a duration 90m | 24h | 7d
//	under         the file's path is under this directory
//	derived-from  one of the file's recorded inputs is exactly this path
//
// Why this and not JSON/TOML: it is written by hand and by shell heredocs (the
// demo does both), wants comments, and has no nesting. It is parsed strictly:
// an unknown key, a repeated `since`, a bad value or a query with no predicate
// is an error, never silently ignored, so a typo cannot turn into a query that
// matches everything (or nothing) without saying so. The name comes from the
// file name, not from inside the file, so two files cannot claim one name.

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// NameRE is what a query name may be. It is kept to a topic-safe shape
// (no dots, wildcards or control bytes) in case it is ever put in one.
var NameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Query is a parsed saved query.
type Query struct {
	Name, Title string
	MIME, Ext   []string
	App, Verb   []string
	Under       []string
	DerivedFrom []string
	Since       string // raw text, "" for none
}

// ParseQuery parses the text of one query file; name comes from the file name.
func ParseQuery(name, text string) (*Query, error) {
	if !NameRE.MatchString(name) {
		return nil, fmt.Errorf("query name %q: want %s", name, NameRE)
	}
	q := &Query{Name: name}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || v == "" {
			return nil, fmt.Errorf("line %d: want `key: value`", n+1)
		}
		switch k {
		case "title":
			q.Title = v
		case "mime":
			if !regexp.MustCompile(`^[a-z0-9.+-]+/([a-z0-9.+-]+|\*)$`).MatchString(strings.ToLower(v)) {
				return nil, fmt.Errorf("line %d: mime %q: want type/subtype or type/*", n+1, v)
			}
			q.MIME = append(q.MIME, strings.ToLower(v))
		case "ext":
			q.Ext = append(q.Ext, strings.ToLower(strings.TrimPrefix(v, ".")))
		case "app":
			q.App = append(q.App, v)
		case "verb":
			q.Verb = append(q.Verb, v)
		case "under", "derived-from":
			if !filepath.IsAbs(v) {
				return nil, fmt.Errorf("line %d: %s %q: want an absolute path", n+1, k, v)
			}
			if k == "under" {
				q.Under = append(q.Under, filepath.Clean(v))
			} else {
				q.DerivedFrom = append(q.DerivedFrom, filepath.Clean(v))
			}
		case "since":
			if q.Since != "" {
				return nil, fmt.Errorf("line %d: since given twice", n+1)
			}
			if _, err := sinceCutoff(v, time.Now()); err != nil {
				return nil, fmt.Errorf("line %d: %w", n+1, err)
			}
			q.Since = v
		default:
			return nil, fmt.Errorf("line %d: unknown key %q (known: title mime ext app verb since under derived-from)", n+1, k)
		}
	}
	if len(q.MIME)+len(q.Ext)+len(q.App)+len(q.Verb)+len(q.Under)+len(q.DerivedFrom) == 0 && q.Since == "" {
		return nil, fmt.Errorf("no predicate: a query that matches every file is almost always a mistake")
	}
	return q, nil
}

// sinceCutoff turns a `since` value into the earliest allowed time.
func sinceCutoff(v string, now time.Time) (time.Time, error) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch v {
	case "today":
		return day, nil
	case "this-week":
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)), nil
	case "this-month":
		return day.AddDate(0, 0, 1-day.Day()), nil
	}
	if n, ok := strings.CutSuffix(v, "d"); ok {
		if d, err := strconv.Atoi(n); err == nil && d > 0 {
			return now.Add(-time.Duration(d) * 24 * time.Hour), nil
		}
	} else if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("since %q: want today, this-week, this-month, or a duration like 90m, 24h, 7d", v)
}

// Match reports whether e satisfies q at time now.
func (q *Query) Match(e *Entry, now time.Time) bool {
	any := func(list []string, f func(string) bool) bool {
		if len(list) == 0 {
			return true
		}
		for _, s := range list {
			if f(s) {
				return true
			}
		}
		return false
	}
	mt := MIMEOf(e.File)
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(e.File), "."))
	ok := any(q.MIME, func(p string) bool {
		if t, ok := strings.CutSuffix(p, "/*"); ok {
			return strings.HasPrefix(mt, t+"/")
		}
		return mt == p
	}) &&
		any(q.Ext, func(x string) bool { return ext == x }) &&
		any(q.App, func(a string) bool { return e.App == a }) &&
		any(q.Verb, func(v string) bool {
			return e.Verb == v || (!strings.Contains(v, "@") && strings.HasPrefix(e.Verb, v+"@"))
		}) &&
		any(q.Under, func(d string) bool { return strings.HasPrefix(e.File, d+"/") }) &&
		any(q.DerivedFrom, func(p string) bool {
			for _, in := range e.Inputs {
				if in == p {
					return true
				}
			}
			return false
		})
	if !ok {
		return false
	}
	if q.Since != "" {
		cut, err := sinceCutoff(q.Since, now)
		return err == nil && !e.ObservedAt.Before(cut)
	}
	return true
}

var extMIME = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp", ".svg": "image/svg+xml",
	".txt": "text/plain", ".md": "text/markdown", ".json": "application/json", ".pdf": "application/pdf",
	".wav": "audio/wave", ".mp3": "audio/mpeg", ".flac": "audio/flac", ".ogg": "audio/ogg",
	".mp4": "video/mp4", ".webm": "video/webm",
}

// MIMEOf names a file's type from its extension ALONE. indexd never opens the
// files it indexes (they are app output, and the path is app-supplied), so
// unlike the launcher's DetectMIME this cannot sniff content: a PNG named
// .jpg is image/jpeg here. Unknown extensions are application/octet-stream.
func MIMEOf(file string) string {
	if t, ok := extMIME[strings.ToLower(path.Ext(file))]; ok {
		return t
	}
	return "application/octet-stream"
}

// LoadQueries reads every *.query file in dir. A file that does not parse is
// returned as a QueryFile with Err set, not dropped: a broken query must be
// visible. A missing dir is no queries, not an error.
type QueryFile struct {
	Name string
	Q    *Query
	Err  error
}

func LoadQueries(dir string) ([]QueryFile, error) {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []QueryFile
	for _, de := range ents {
		name, ok := strings.CutSuffix(de.Name(), ".query")
		if !ok || de.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			out = append(out, QueryFile{Name: name, Err: err})
			continue
		}
		q, err := ParseQuery(name, string(b))
		out = append(out, QueryFile{Name: name, Q: q, Err: err})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
