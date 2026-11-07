// Package picker is the terminal file picker of the powerbox v0. The user
// drives it: list, navigate, select, confirm. Confirming mints a grant ticket
// (controller/ticket) bound to the exact resolved path, the mode, the app and
// the verb the grant is for.
//
// This is NOT yet a trusted path. It runs as an ordinary process of the same
// user, so it proves "this program, which holds the ticket key, was told yes",
// not "a human at the compositor said yes". See contracts/decisions/0007.
package picker

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/antiartificial/aurora/controller/ticket"
)

// Kinds of selection.
const (
	KindFile = "file" // an existing regular file
	KindDir  = "dir"  // an existing directory
	KindSave = "save" // a file name in an existing directory (may not exist yet)
)

// Options says what the grant is for. App, Verb, Mode and Kind are fixed by
// the caller; the user only chooses the path.
type Options struct {
	App, Verb, Mode, Kind string
	Start                 string // directory to open in (default: the working directory)
	Suggest               string // KindSave: suggested file name
	TTL                   time.Duration
	Key                   []byte
	Now                   func() time.Time
}

// Result is a minted ticket and the path it is for.
type Result struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Ticket string `json:"ticket"`
}

// ErrCancelled is returned when the user quits or declines.
var ErrCancelled = errors.New("cancelled: nothing was granted")

// Mint makes a ticket for an already chosen path. The path is resolved here,
// so the ticket binds the canonical path; a save path resolves its directory
// and keeps its base name, the same rule the controller applies to outputs.
func Mint(o Options, path string) (Result, error) {
	p, err := Resolve(o.Kind, path)
	if err != nil {
		return Result{}, err
	}
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	ttl := o.TTL
	if ttl <= 0 {
		ttl = ticket.DefaultTTL
	}
	tok, err := ticket.Mint(o.Key, ticket.Claims{Path: p, Mode: o.Mode, App: o.App, Verb: o.Verb}, now(), ttl)
	return Result{Path: p, Mode: o.Mode, Ticket: tok}, err
}

// Resolve canonicalises a selection and checks it is of the right kind.
func Resolve(kind, path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s is not absolute", path)
	}
	if kind == KindSave {
		dir, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			return "", err
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return "", fmt.Errorf("%s is not a directory", dir)
		}
		p := filepath.Join(dir, filepath.Base(path))
		if st, err := os.Lstat(p); err == nil && !st.Mode().IsRegular() {
			return "", fmt.Errorf("%s exists and is not a regular file", p)
		}
		return p, nil
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	switch {
	case err != nil:
		return "", err
	case kind == KindDir && !st.IsDir():
		return "", fmt.Errorf("%s is not a directory", p)
	case kind == KindFile && !st.Mode().IsRegular():
		return "", fmt.Errorf("%s is not a regular file", p)
	}
	return p, nil
}

type entry struct {
	name  string
	isDir bool
}

func list(dir string) ([]entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var es []entry
	for _, d := range des {
		isDir := d.IsDir()
		if d.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, d.Name())); err == nil {
				isDir = st.IsDir()
			}
		}
		es = append(es, entry{d.Name(), isDir})
	}
	sort.Slice(es, func(i, j int) bool {
		if es[i].isDir != es[j].isDir {
			return es[i].isDir
		}
		return es[i].name < es[j].name
	})
	return es, nil
}

const maxShown = 200

// Run drives the picker on in/out until the user confirms or quits, then
// mints. Powerbox.Grant uses Choose instead, so that something else gets to
// say yes between the choice and the ticket.
func Run(o Options, in *bufio.Reader, out io.Writer) (Result, error) {
	path, err := Choose(o, in, out)
	if err != nil {
		return Result{}, err
	}
	return Mint(o, path)
}

// Choose drives the terminal picker until the user confirms a path or quits.
// It returns the path the user confirmed, unresolved, and mints nothing.
func Choose(o Options, in *bufio.Reader, out io.Writer) (string, error) {
	cur := o.Start
	if cur == "" {
		cur, _ = os.Getwd()
	}
	cur, err := filepath.Abs(cur)
	if err != nil {
		return "", err
	}
	access := map[string]string{"ro": "read-only", "rw": "read-write"}[o.Mode]
	fmt.Fprintf(out, "\n== Powerbox: choose %s for %s (verb %s), %s ==\n", map[string]string{KindFile: "a file", KindDir: "a folder", KindSave: "where to save"}[o.Kind], o.App, o.Verb, access)
	for {
		es, err := list(cur)
		if err != nil {
			fmt.Fprintf(out, "cannot list %s: %v\n", cur, err)
			cur = filepath.Dir(cur)
			continue
		}
		fmt.Fprintf(out, "\n%s\n   0. ../\n", cur)
		for i, e := range es {
			if i == maxShown {
				fmt.Fprintf(out, "   ... %d more not shown; type a name\n", len(es)-maxShown)
				break
			}
			sfx := ""
			if e.isDir {
				sfx = "/"
			}
			fmt.Fprintf(out, "%4d. %s%s\n", i+1, e.name, sfx)
		}
		fmt.Fprint(out, help(o.Kind))
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if err != nil && line == "" {
			return "", ErrCancelled
		}
		next, chosen := step(o, cur, es, line)
		switch {
		case line == "q":
			return "", ErrCancelled
		case chosen != "":
			ok, err := confirm(o, chosen, access, in, out)
			if err != nil {
				fmt.Fprintf(out, "  %v\n", err)
			} else if ok {
				return chosen, nil
			} else {
				fmt.Fprintln(out, "  not granted")
			}
		default:
			cur = next
		}
	}
}

func help(kind string) string {
	switch kind {
	case KindDir:
		return "number/name: open   0: up   s: choose this folder   q: quit > "
	case KindSave:
		return "number/name: open folder   0: up   w [NAME]: save here   q: quit > "
	}
	return "number/name: open or choose   0: up   q: quit > "
}

// step interprets one command: it returns the next directory, or a chosen path.
func step(o Options, cur string, es []entry, line string) (next, chosen string) {
	next = cur
	if line == "0" || line == ".." {
		return filepath.Dir(cur), ""
	}
	if o.Kind == KindDir && line == "s" {
		return cur, cur
	}
	if o.Kind == KindSave && (line == "w" || strings.HasPrefix(line, "w ")) {
		name := strings.TrimSpace(strings.TrimPrefix(line, "w"))
		if name == "" {
			name = o.Suggest
		}
		if name == "" || strings.ContainsRune(name, '/') || name == "." || name == ".." {
			return cur, ""
		}
		return cur, filepath.Join(cur, name)
	}
	var e *entry
	if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(es) {
		e = &es[n-1]
	} else {
		for i := range es {
			if es[i].name == line {
				e = &es[i]
			}
		}
	}
	if e == nil {
		return cur, ""
	}
	p := filepath.Join(cur, e.name)
	if e.isDir {
		return p, ""
	}
	if o.Kind == KindFile {
		return cur, p
	}
	return cur, ""
}

func confirm(o Options, path, access string, in *bufio.Reader, out io.Writer) (bool, error) {
	if _, err := Resolve(o.Kind, path); err != nil {
		return false, err
	}
	extra := ""
	if o.Kind == KindSave {
		if _, err := os.Lstat(path); err == nil {
			extra = " (EXISTS, will be overwritten)"
		}
	}
	fmt.Fprintf(out, "Grant %s access to %s%s\nto %s for %s, for one use within %s? [y/N] ", access, path, extra, o.App, o.Verb, ttlOf(o))
	line, _ := in.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(line), "y"), nil
}

func ttlOf(o Options) time.Duration {
	if o.TTL > 0 {
		return o.TTL
	}
	return ticket.DefaultTTL
}
