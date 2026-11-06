package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/antiartificial/aurora/contracts/plumb"
)

// Plumber is the optional half of a Backend that can ask the registry what a
// selection could be given to. It lists; it never runs anything.
//
// The JSON a non-Go consumer (the compositor) reads is plumb.Request going
// out and plumb.Reply coming back on registry.plumb@v1; see the plumb
// package's json tags, and contracts/plumb/README.md for an example.
type Plumber interface {
	Plumb(ctx context.Context, req plumb.Request) (plumb.Reply, error)
}

// Plumb asks registry.plumb@v1.
func (b *busBackend) Plumb(ctx context.Context, req plumb.Request) (plumb.Reply, error) {
	var rep plumb.Reply
	err := b.call(ctx, "registry.plumb@v1", req, &rep)
	return rep, err
}

// SelectionFor decides what kind of selection a command-line argument is,
// unless the caller forced one. An argument that names something that exists
// is a path (made absolute); anything else is text, and the rules decide
// whether the text looks like something.
func SelectionFor(arg, forced string) plumb.Selection {
	if forced != "" {
		if forced == plumb.KindPath {
			if abs, err := filepath.Abs(arg); err == nil {
				arg = abs
			}
		}
		return plumb.Selection{Kind: forced, Value: arg}
	}
	if _, err := os.Stat(arg); err == nil {
		if abs, err := filepath.Abs(arg); err == nil {
			return plumb.Selection{Kind: plumb.KindPath, Value: abs}
		}
	}
	return plumb.Selection{Kind: plumb.KindText, Value: arg}
}

// PrintPlumb writes the human listing. Destructive and external candidates
// carry the same "!!" flag as `aurora verbs`; the effect class is always
// printed.
func PrintPlumb(w io.Writer, rep plumb.Reply) {
	s := rep.Selection
	line := fmt.Sprintf("%s %s", s.Kind, s.Value)
	if s.Rule != "" {
		line += "  (rule " + s.Rule
		if s.MIME != "" {
			line += ", " + s.MIME
		}
		if s.Line > 0 {
			line += fmt.Sprintf(", line %d", s.Line)
			if s.Col > 0 {
				line += fmt.Sprintf(" col %d", s.Col)
			}
		}
		line += ")"
	}
	fmt.Fprintln(w, line)
	if len(rep.Candidates) == 0 {
		fmt.Fprintf(w, "nothing to offer: %s\n", rep.Reason)
		return
	}
	for i, c := range rep.Candidates {
		effect := c.Effect
		if (Verb{Effect: c.Effect}).NeedsApproval() {
			effect = "!! " + strings.ToUpper(c.Effect)
		}
		mark := ""
		if c.Default {
			mark = "  [default]"
		}
		fmt.Fprintf(w, "%2d. %-14s %-20s %-14s %s%s\n", i+1, c.Verb, c.App, effect, c.Summary, mark)
		if c.Group != "" {
			fmt.Fprintf(w, "      group: %s\n", c.Group)
		}
	}
	if rep.Truncated {
		fmt.Fprintln(w, "(list truncated; --limit N shows more, up to 50)")
	}
}

func cmdPlumb(env *Env, args []string) int {
	var c common
	fs := newFlags(env, "plumb", "plumb [--path|--mime|--text] [--limit N] [--json] SELECTION", &c)
	asPath := fs.Bool("path", false, "SELECTION is a path")
	asMIME := fs.Bool("mime", false, "SELECTION is a MIME type")
	asText := fs.Bool("text", false, "SELECTION is text (a path, path:line:col, URL or hostname if it looks like one)")
	limit := fs.Int("limit", 0, "most candidates to list (default 20, at most 50)")
	asJSON := fs.Bool("json", false, "print the registry.plumb@v1 reply as JSON")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return parseFailed(err)
	}
	forced, n := "", 0
	for kind, on := range map[string]bool{plumb.KindPath: *asPath, plumb.KindMIME: *asMIME, plumb.KindText: *asText} {
		if on {
			forced, n = kind, n+1
		}
	}
	if len(pos) != 1 || n > 1 {
		fmt.Fprintln(env.Err, "usage: aurora plumb [--path|--mime|--text] [--limit N] [--json] SELECTION (one selection, at most one kind flag)")
		return 2
	}
	be := env.Backend(c.socket, c.timeout)
	defer be.Close()
	pl, ok := be.(Plumber)
	if !ok {
		return fail(env, fmt.Errorf("this backend cannot plumb"))
	}
	rep, err := pl.Plumb(context.Background(), plumb.Request{Selection: SelectionFor(pos[0], forced), Limit: *limit})
	if err != nil {
		return fail(env, err)
	}
	if *asJSON {
		b, _ := json.Marshal(rep)
		fmt.Fprintln(env.Out, string(b))
		return 0
	}
	PrintPlumb(env.Out, rep)
	return 0
}
