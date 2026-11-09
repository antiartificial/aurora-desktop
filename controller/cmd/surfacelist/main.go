// Command surfacelist prints what the session's apps offer and what they must
// be granted to offer it: `aurora surface list`.
//
// It is a thin reader of registry.describe@v1 -- the descriptor the shell draws
// chrome from -- so what it prints is the same claim the compositor renders,
// not a second derivation from manifests.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	contract "github.com/antiartificial/aurora/contracts/surface"
	"github.com/antiartificial/aurora/controller/internal/config"
	"github.com/antiartificial/aurora/controller/internal/surface"
)

const usage = `usage: surfacelist [--socket S] [--json]

  List each app's surface: the app, each action's id and effect, and the grants
  it declares. An app with no verbs has no descriptor to fetch and is skipped.
  --json prints the descriptors as the wire bytes, in app order, canonically
  encoded (actions by id, grants sorted).

Exit status: 0 listed, 1 the controller could not answer.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the CLI body with its streams passed in, so the output shape is
// testable without a bus.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("surfacelist", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	sock := fs.String("socket", config.BusSocket(), "bus socket")
	asJSON := fs.Bool("json", false, "print the canonical descriptors as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	c, err := client.Dial(*sock)
	if err != nil {
		fmt.Fprintf(stderr, "surfacelist: %v\n", err)
		return 1
	}
	defer c.Close()
	// Generous: describe is a registry lookup, but a controller behind a slow
	// invoke is not a reason to print half a list.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ds, err := surface.List(ctx, c)
	if err != nil {
		fmt.Fprintf(stderr, "surfacelist: %v\n", err)
		return 1
	}
	if err := writeList(stdout, ds, *asJSON); err != nil {
		fmt.Fprintf(stderr, "surfacelist: %v\n", err)
		return 1
	}
	return 0
}

// writeList writes the list. App order is the caller's (List sorts by app id);
// within a block, actions are sorted by id and grants by name: a surface that
// reorders between runs makes a diff useless, and this output exists to be
// diffed when a manifest changes.
func writeList(w io.Writer, ds []contract.Descriptor, asJSON bool) error {
	if asJSON {
		out := make([]json.RawMessage, 0, len(ds))
		for _, d := range ds {
			b, err := d.Marshal() // canonical and validated, not json.Marshal
			if err != nil {
				return fmt.Errorf("%s: %w", d.App, err)
			}
			out = append(out, b)
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i, d := range ds {
		if i > 0 {
			fmt.Fprintln(tw)
		}
		fmt.Fprintf(tw, "%s\t%s\n", d.App, d.Title)
		actions := append([]contract.Action(nil), d.Actions...)
		sort.Slice(actions, func(i, j int) bool { return actions[i].ID < actions[j].ID })
		for _, a := range actions {
			// The effect is printed as the contract spells it. read/write/nominal
			// is the vocabulary the shell and the approvals use; abbreviating it
			// here would be a second vocabulary for the same claim.
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", a.ID, a.Effect, grants(a.Grants))
		}
		if len(d.Grants) > 0 {
			fmt.Fprintf(tw, "  (window)\t\t%s\n", grants(d.Grants))
		}
	}
	return tw.Flush()
}

// grants prints a sorted copy, "-" when there are none, so a column never reads
// as if a missing grant were an empty string in the middle of a list.
func grants(gs []string) string {
	if len(gs) == 0 {
		return "-"
	}
	cp := append([]string(nil), gs...)
	sort.Strings(cp)
	return strings.Join(cp, " ")
}
