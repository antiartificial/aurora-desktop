package launcher

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// splitStepArgs separates "1:k=v" / "2:k=v" (bound to a step) from plain
// "k=v" (applies to whichever step declares k).
func splitStepArgs(items []string) (step [3]map[string]string, plain map[string]string, err error) {
	step[1], step[2], plain = map[string]string{}, map[string]string{}, map[string]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok || k == "" {
			return step, plain, fmt.Errorf("bad --arg %q (want key=value or STEP:key=value)", it)
		}
		if p, name, has := strings.Cut(k, ":"); has && (p == "1" || p == "2") {
			step[p[0]-'0'][name] = v
		} else {
			plain[k] = v
		}
	}
	return
}

func cmdChain(env *Env, args []string) int {
	var c common
	fs := newFlags(env, "chain", "chain <file> --verb A --verb B [--arg [1:|2:]k=v]... [--output PATH]", &c)
	var verbs, kv listFlag
	fs.Var(&verbs, "verb", "verb to run (give exactly twice: first, then second)")
	fs.Var(&kv, "arg", "argument key=value; prefix 1: or 2: to target a step (repeatable)")
	app := fs.String("app", "", "disambiguate verbs when several apps offer one")
	yes := fs.Bool("yes", false, "TEST AFFORDANCE: allow approval requests without asking (defeats approval)")
	output := fs.String("output", "", "final output path (default: next to the input)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return parseFailed(err)
	}
	env.Yes = *yes
	if len(pos) != 1 || len(verbs) != 2 {
		fmt.Fprintln(env.Err, "aurora chain: need one file and exactly two --verb flags")
		return 2
	}
	stepArgs, plain, err := splitStepArgs(kv)
	if err != nil {
		fmt.Fprintf(env.Err, "aurora chain: %v\n", err)
		return 2
	}
	file, err := filepath.Abs(pos[0])
	if err != nil {
		return fail(env, err)
	}
	mime, err := DetectMIME(file)
	if err != nil {
		return fail(env, err)
	}
	be := env.Backend(c.socket, c.timeout)
	defer be.Close()
	ctx := context.Background()
	interactive := len(kv) == 0
	claimed := map[string]bool{}

	input, inMIME := file, mime
	last := ""
	for i := 1; i <= 2; i++ {
		all, err := be.Verbs(ctx, inMIME)
		if err != nil {
			return fail(env, err)
		}
		v, err := ResolveVerb(FilterByMIME(all, inMIME), verbs[i-1], *app)
		if err != nil {
			return fail(env, fmt.Errorf("step %d: %w", i, err))
		}
		if v.NeedsApproval() {
			fmt.Fprintf(env.Out, "!! step %d: %s is %s (will require approval; not enforced yet)\n", i, v.Name, v.Effect)
		}
		fmt.Fprintf(env.Out, "[step %d/2] %s on %s (%s)\n", i, v.Name, input, inMIME)
		o := runOpts{interactive: interactive, pick: func(specs []ArgSpec) (map[string]string, error) {
			m := map[string]string{}
			for k, val := range stepArgs[i] {
				m[k] = val
			}
			for _, s := range specs {
				if val, ok := plain[s.Name]; ok {
					m[s.Name], claimed[s.Name] = val, true
				}
			}
			if i == 2 {
				for k := range plain {
					if !claimed[k] {
						return nil, fmt.Errorf("--arg %q is not an argument of either verb", k)
					}
				}
			}
			return m, nil
		}}
		if i == 2 {
			o.output = *output
		}
		out, err := runVerb(ctx, env, be, input, v, o)
		if err != nil {
			return fail(env, fmt.Errorf("step %d: %w", i, err))
		}
		last = out
		if i == 1 {
			if out == "" {
				return fail(env, fmt.Errorf("step 1 (%s) produced no output file to feed into step 2", v.Name))
			}
			input = out
			// Re-sniff the real file; if it is not readable (registry on
			// another filesystem view) keep the previous type.
			if m, err := DetectMIME(out); err == nil {
				inMIME = m
			}
		}
	}
	if last == "" {
		last = "(no output file)"
	}
	fmt.Fprintf(env.Out, "Chain complete: %s -> %s\n", file, last)
	return 0
}
