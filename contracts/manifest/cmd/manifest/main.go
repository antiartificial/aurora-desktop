// Command manifest validates and inspects aurora app manifests.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/antiartificial/aurora/contracts/manifest"
)

const usage = `usage:
  manifest validate <file.toml>
  manifest show <file.toml>
  manifest verbs <file.toml> --accepts <mime>
  manifest diff <old.toml> <new.toml>

exit status: 0 ok; 1 error or invalid manifest; 2 usage; 3 (diff) fresh consent required`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func load(file string, stderr io.Writer) *manifest.Manifest {
	src, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil
	}
	m, err := manifest.Load(src)
	if err != nil {
		if es, ok := err.(manifest.Errors); ok {
			for _, e := range es {
				fmt.Fprintf(stderr, "%s: %s\n", file, e)
			}
			fmt.Fprintf(stderr, "%s: %d error(s)\n", file, len(es))
		} else {
			fmt.Fprintf(stderr, "%s: %s\n", file, err)
		}
		return nil
	}
	return m
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "validate", "show":
		if len(rest) != 1 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		m := load(rest[0], stderr)
		if m == nil {
			return 1
		}
		if cmd == "validate" {
			fmt.Fprintf(stdout, "%s: ok (%s %s, %d verbs)\n", rest[0], m.App.ID, m.App.Version, len(m.Verbs))
		} else {
			show(stdout, m)
		}
		return 0
	case "verbs":
		var file, mime string
		for i := 0; i < len(rest); i++ {
			switch a := rest[i]; {
			case a == "--accepts" && i+1 < len(rest):
				i++
				mime = rest[i]
			case strings.HasPrefix(a, "--accepts="):
				mime = strings.TrimPrefix(a, "--accepts=")
			case file == "" && !strings.HasPrefix(a, "-"):
				file = a
			default:
				fmt.Fprintln(stderr, usage)
				return 2
			}
		}
		if file == "" || mime == "" {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		m := load(file, stderr)
		if m == nil {
			return 1
		}
		for _, v := range m.VerbsAccepting(strings.ToLower(mime)) {
			fmt.Fprintf(stdout, "%s\t%s\n", v.Name, v.Effect)
		}
		return 0
	case "diff":
		if len(rest) != 2 {
			fmt.Fprintln(stderr, usage)
			return 2
		}
		old, nw := load(rest[0], stderr), load(rest[1], stderr)
		if old == nil || nw == nil {
			return 1
		}
		res := manifest.Diff(old, nw)
		for _, c := range res.Changes {
			tag := "info   "
			if c.Consent {
				tag = "CONSENT"
			}
			fmt.Fprintf(stdout, "  [%s] %s\n", tag, c.Detail)
		}
		if res.NeedsConsent() {
			fmt.Fprintln(stdout, "fresh consent REQUIRED")
			return 3
		}
		fmt.Fprintln(stdout, "no fresh consent required")
		return 0
	}
	fmt.Fprintf(stderr, "unknown command %q\n%s\n", cmd, usage)
	return 2
}

func show(w io.Writer, m *manifest.Manifest) {
	fmt.Fprintf(w, "%s (%s) %s\n  image %s\n", m.App.Name, m.App.ID, m.App.Version, m.App.ImageHash)
	for _, r := range m.Runtimes {
		fmt.Fprintf(w, "runtime  %s %s\n", r.Name, r.Version)
	}
	fmt.Fprintln(w, "capabilities")
	c := m.Capabilities
	for _, p := range c.Paths {
		fmt.Fprintf(w, "  path    %s (%s)\n", p.Path, p.Access)
	}
	for _, h := range c.Network {
		fmt.Fprintf(w, "  network %s\n", h)
	}
	for _, d := range c.Devices {
		fmt.Fprintf(w, "  device  %s\n", d)
	}
	if len(c.Paths)+len(c.Network)+len(c.Devices) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	fmt.Fprintf(w, "types\n  opens   %s\n  creates %s\n", strings.Join(m.Types.Opens, ", "), strings.Join(m.Types.Creates, ", "))
	fmt.Fprintln(w, "verbs")
	for _, v := range m.Verbs {
		fmt.Fprintf(w, "  %-16s %-11s accepts: %s", v.Name, v.Effect, strings.Join(v.Accepts, ", "))
		if len(v.Capabilities) > 0 {
			fmt.Fprintf(w, "  needs: %s", strings.Join(v.Capabilities, ", "))
		}
		fmt.Fprintln(w)
	}
	for _, e := range m.Events {
		fmt.Fprintf(w, "event    %s\n", e.Name)
	}
	for _, s := range m.Settings {
		fmt.Fprintf(w, "setting  %s (%s)\n", s.Key, s.Type)
	}
	for _, r := range m.Replicants {
		fmt.Fprintf(w, "replicant %s\n", r.Name)
	}
	for _, mg := range m.Migrations {
		fmt.Fprintf(w, "migration %s -> %s\n", mg.From, mg.To)
	}
}
