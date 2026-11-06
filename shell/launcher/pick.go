package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// PickRequest asks the powerbox for one grant. The launcher fixes everything
// except the choice of path, which only the user makes.
type PickRequest struct {
	App, Verb, Mode string // Mode: "ro" or "rw"
	Kind            string // "file" or "save"
	Start, Suggest  string
}

// PickResult is what the powerbox returns. Ticket is opaque to the launcher:
// it is carried to the controller unread and unmodified. Path is what the
// picker says the ticket is for; the controller checks the ticket against the
// request, so a picker that lied here could only get the invoke refused.
type PickResult struct {
	Path   string `json:"path"`
	Ticket string `json:"ticket"`
}

// ExecPicker runs the powerbox binary and reads its one JSON line. The
// powerbox talks to the user on /dev/tty; its stdout is a pipe so a script
// cannot answer for the user by feeding stdin.
func ExecPicker(r PickRequest) (PickResult, error) {
	bin, err := findPowerbox()
	if err != nil {
		return PickResult{}, err
	}
	args := []string{"--app", r.App, "--verb", r.Verb, "--mode", r.Mode, "--kind", r.Kind}
	if r.Start != "" {
		args = append(args, "--start", r.Start)
	}
	if r.Suggest != "" {
		args = append(args, "--suggest", r.Suggest)
	}
	var out bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &out, os.Stderr
	if err := cmd.Run(); err != nil {
		return PickResult{}, fmt.Errorf("powerbox: no grant made (%v)", err)
	}
	var pr PickResult
	if err := json.Unmarshal(out.Bytes(), &pr); err != nil || pr.Ticket == "" || !filepath.IsAbs(pr.Path) {
		return PickResult{}, fmt.Errorf("powerbox gave an unusable answer: %q", out.String())
	}
	return pr, nil
}

// findPowerbox: $AURORA_POWERBOX, a "powerbox" next to this executable, PATH.
func findPowerbox() (string, error) {
	if p := os.Getenv("AURORA_POWERBOX"); p != "" {
		return p, nil
	}
	if self, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(self), "powerbox")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath("powerbox"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("--pick needs the powerbox binary: build ./cmd/powerbox next to aurora, put it on PATH, or set AURORA_POWERBOX")
}

// pickOpen is `aurora open --pick`: the user picks the input in the powerbox
// instead of naming it on the command line, and for a verb that writes, picks
// where the output goes. The tickets ride along on the invoke request.
func pickOpen(ctx context.Context, env *Env, be Backend, verbName, app, output string, supplied map[string]string) int {
	pick := env.Pick
	if pick == nil {
		pick = ExecPicker
	}
	verbs, err := be.Verbs(ctx, "")
	if err != nil {
		return fail(env, err)
	}
	var chosen Verb
	if verbName != "" {
		chosen, err = ResolveVerb(verbs, verbName, app)
	} else if len(verbs) == 0 {
		err = fmt.Errorf("no verbs registered")
	} else {
		PrintVerbs(env.Out, verbs, true)
		chosen, err = pickVerb(verbs, env.In, env.Out)
	}
	if err != nil {
		return fail(env, err)
	}
	in, err := pick(PickRequest{App: chosen.App, Verb: chosen.Name, Mode: "ro", Kind: "file"})
	if err != nil {
		return fail(env, err)
	}
	mime, err := DetectMIME(in.Path)
	if err != nil {
		return fail(env, err)
	}
	fmt.Fprintf(env.Out, "%s  (%s)\n", in.Path, mime)
	if len(FilterByMIME([]Verb{chosen}, mime)) == 0 {
		fmt.Fprintf(env.Err, "aurora: %s does not accept %s\n", chosen.Name, mime)
		return 1
	}
	_, err = runVerb(ctx, env, be, in.Path, chosen, runOpts{
		pick:        func([]ArgSpec) (map[string]string, error) { return supplied, nil },
		interactive: len(supplied) == 0,
		output:      output,
		inTicket:    in.Ticket,
		pickOutput: func(suggest string) (PickResult, error) {
			return pick(PickRequest{App: chosen.App, Verb: chosen.Name, Mode: "rw", Kind: "save",
				Start: filepath.Dir(suggest), Suggest: filepath.Base(suggest)})
		},
	})
	if err != nil {
		return fail(env, err)
	}
	return 0
}
