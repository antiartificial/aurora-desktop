package launcher

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// PrintVerbs writes the verb listing. Destructive and external verbs are
// flagged with "!!" because the plan will require approval for them.
// numbered adds a 1-based index for the picker.
func PrintVerbs(w io.Writer, verbs []Verb, numbered bool) {
	for i, v := range verbs {
		prefix := ""
		if numbered {
			prefix = fmt.Sprintf("%2d. ", i+1)
		}
		effect := v.Effect
		if v.NeedsApproval() {
			effect = "!! " + strings.ToUpper(v.Effect)
		}
		fmt.Fprintf(w, "%s%-14s %-20s %-14s %s\n", prefix, v.Name, v.App, effect, v.Summary)
		if v.NeedsApproval() {
			fmt.Fprintf(w, "%s   ^ %s effect: will require approval (approval UI not built yet; running anyway on your say-so)\n", strings.Repeat(" ", len(prefix)), v.Effect)
		}
	}
}

// FilterByMIME keeps verbs that accept the type. A verb with no accepts list
// accepts anything. Matching understands wildcards such as image/*.
func FilterByMIME(verbs []Verb, mime string) []Verb {
	var out []Verb
	for _, v := range verbs {
		if len(v.Accepts) == 0 {
			out = append(out, v)
			continue
		}
		for _, a := range v.Accepts {
			if a == mime || a == "*/*" || (strings.HasSuffix(a, "/*") && strings.HasPrefix(mime, strings.TrimSuffix(a, "*"))) {
				out = append(out, v)
				break
			}
		}
	}
	return out
}

// ResolveVerb finds a verb by exact name or by name without its @version.
func ResolveVerb(verbs []Verb, name, app string) (Verb, error) {
	var hits []Verb
	for _, v := range verbs {
		if app != "" && v.App != app {
			continue
		}
		if v.Name == name || strings.SplitN(v.Name, "@", 2)[0] == name {
			hits = append(hits, v)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		var have []string
		for _, v := range verbs {
			have = append(have, v.Name)
		}
		return Verb{}, fmt.Errorf("no applicable verb %q (available: %s)", name, strings.Join(have, ", "))
	}
	var apps []string
	for _, v := range hits {
		apps = append(apps, v.App)
	}
	return Verb{}, fmt.Errorf("verb %q is offered by several apps (%s); pick one with --app", name, strings.Join(apps, ", "))
}

// parseKV splits repeated --arg k=v flags.
func parseKV(items []string) (map[string]string, error) {
	m := map[string]string{}
	for _, it := range items {
		k, v, ok := strings.Cut(it, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("bad --arg %q (want key=value)", it)
		}
		m[k] = v
	}
	return m, nil
}

// pickVerb asks the user to choose by number or name.
func pickVerb(verbs []Verb, in *bufio.Reader, out io.Writer) (Verb, error) {
	for {
		fmt.Fprintf(out, "Pick a verb [1-%d]: ", len(verbs))
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if err != nil && line == "" {
			return Verb{}, fmt.Errorf("input ended before a verb was chosen")
		}
		if n, e := strconv.Atoi(line); e == nil && n >= 1 && n <= len(verbs) {
			return verbs[n-1], nil
		}
		if v, e := ResolveVerb(verbs, line, ""); e == nil && line != "" {
			return v, nil
		}
		fmt.Fprintln(out, "  not a valid choice")
	}
}

// deriveOutput names the result next to the input: photo.png + resize@v1 ->
// photo.resize.png. A string "format" argument decides the extension, since
// export-style verbs change the type.
func deriveOutput(input, verb string, args map[string]any) string {
	ext := filepath.Ext(input)
	if f, ok := args["format"].(string); ok && f != "" {
		ext = "." + strings.TrimPrefix(f, ".")
	}
	base := strings.TrimSuffix(input, filepath.Ext(input))
	return base + "." + strings.SplitN(verb, "@", 2)[0] + ext
}

type runOpts struct {
	pick        func([]ArgSpec) (map[string]string, error) // which --arg values apply to this verb
	interactive bool
	output      string // explicit output path, else derived
}

// runVerb describes, collects arguments, invokes and prints one step. It
// returns the output path ("" for verbs that produce no file).
func runVerb(ctx context.Context, env *Env, be Backend, input string, v Verb, o runOpts) (string, error) {
	d, err := be.Describe(ctx, v.App, v.Name)
	if err != nil {
		return "", err
	}
	specs := ParseSchema(d.Input)
	if len(specs) > 0 && o.interactive {
		fmt.Fprintf(env.Out, "Arguments for %s:\n", v.Name)
	}
	supplied, err := o.pick(specs)
	if err != nil {
		return "", err
	}
	args, err := BuildArgs(specs, supplied, o.interactive, env.In, env.Out)
	if err != nil {
		return "", err
	}
	req := InvokeRequest{App: v.App, Verb: v.Name, Input: input, Args: args, Output: o.output}
	if req.Output == "" && v.Effect != EffectRead {
		req.Output = deriveOutput(input, v.Name, args)
	}
	fmt.Fprintf(env.Out, "Running %s (%s) on %s ...\n", v.Name, v.App, input)
	res, err := be.Invoke(ctx, req)
	if err != nil {
		return "", err
	}
	if !res.OK {
		return "", &InvokeError{Verb: v.Name, Msg: res.Error, Detail: res.Detail}
	}
	fmt.Fprintf(env.Out, "OK  %s\n", v.Name)
	if res.Output != "" {
		fmt.Fprintf(env.Out, "    output: %s\n", res.Output)
	} else {
		fmt.Fprintln(env.Out, "    (verb produced no output file)")
	}
	if len(res.Result) > 0 && string(res.Result) != "null" {
		fmt.Fprintf(env.Out, "    result: %s\n", res.Result)
	}
	if res.Isolated != nil {
		fmt.Fprintf(env.Out, "    isolated: %v\n", *res.Isolated)
	}
	if len(res.Provenance) > 0 && string(res.Provenance) != "null" {
		fmt.Fprintf(env.Out, "    provenance: %s\n", res.Provenance)
	}
	return res.Output, nil
}

func cmdOpen(env *Env, args []string) int {
	var c common
	fs := newFlags(env, "open", "open <file> [--verb NAME] [--app ID] [--arg k=v]... [--output PATH]", &c)
	verb := fs.String("verb", "", "run this verb without the picker")
	app := fs.String("app", "", "disambiguate --verb when several apps offer it")
	output := fs.String("output", "", "output path (default: next to the input)")
	var kv listFlag
	fs.Var(&kv, "arg", "verb argument key=value (repeatable); skips prompting")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return parseFailed(err)
	}
	if len(pos) != 1 {
		fmt.Fprintln(env.Err, "aurora open: need exactly one file")
		return 2
	}
	supplied, err := parseKV(kv)
	if err != nil {
		fmt.Fprintf(env.Err, "aurora open: %v\n", err)
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
	all, err := be.Verbs(ctx, mime)
	if err != nil {
		return fail(env, err)
	}
	// Filter again client-side: cheap, and it keeps a registry that ignores
	// "accepts" from offering verbs that cannot take this file.
	verbs := FilterByMIME(all, mime)
	fmt.Fprintf(env.Out, "%s  (%s)\n", file, mime)
	if len(verbs) == 0 {
		fmt.Fprintf(env.Err, "aurora: no verbs accept %s\n", mime)
		return 1
	}
	var chosen Verb
	if *verb != "" {
		chosen, err = ResolveVerb(verbs, *verb, *app)
	} else {
		PrintVerbs(env.Out, verbs, true)
		chosen, err = pickVerb(verbs, env.In, env.Out)
	}
	if err != nil {
		return fail(env, err)
	}
	_, err = runVerb(ctx, env, be, file, chosen, runOpts{
		pick:        func([]ArgSpec) (map[string]string, error) { return supplied, nil },
		interactive: len(supplied) == 0,
		output:      *output,
	})
	if err != nil {
		return fail(env, err)
	}
	return 0
}
