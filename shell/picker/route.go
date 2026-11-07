package picker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Route is which user interface chooses the path. It is decided once, from
// the environment, before anything is shown.
type Route int

const (
	// RouteTTY: the terminal picker, because there is no chooser to use.
	RouteTTY Route = iota
	// RouteForcedTTY: the terminal picker, because the caller said --tty. The
	// chooser is not even looked at.
	RouteForcedTTY
	// RouteChooser: the graphical chooser, with the terminal as the fallback
	// for a chooser that cannot run here.
	RouteChooser
)

func (r Route) String() string {
	switch r {
	case RouteTTY:
		return "tty"
	case RouteForcedTTY:
		return "tty (forced)"
	case RouteChooser:
		return "chooser"
	}
	return fmt.Sprintf("Route(%d)", int(r))
}

// SelectRoute is the whole decision: the chooser when a Wayland display is
// set and the binary was found, unless the terminal was forced.
func SelectRoute(forceTTY, display bool, chooser string) Route {
	switch {
	case forceTTY:
		return RouteForcedTTY
	case display && chooser != "":
		return RouteChooser
	}
	return RouteTTY
}

// FindChooser mirrors launcher.findPowerbox (shell/launcher/pick.go): the
// $AURORA_CHOOSER override, an "aurora-chooser" next to this executable, then
// PATH. It returns "" when there is none. Like the powerbox lookup, the
// override is taken as given and not checked, so a wrong value shows up as a
// chooser that cannot be executed rather than as a silent fallback to PATH.
func FindChooser(e Env) string {
	if p := e.Getenv("AURORA_CHOOSER"); p != "" {
		return p
	}
	if e.Self != "" {
		p := filepath.Join(filepath.Dir(e.Self), "aurora-chooser")
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if p, err := e.LookPath("aurora-chooser"); err == nil {
		return p
	}
	return ""
}

// PlanRoute reads the environment once and says which route to take and,
// for the chooser route, which binary.
func PlanRoute(e Env, forceTTY bool) (Route, string) {
	if forceTTY {
		return RouteForcedTTY, ""
	}
	bin := FindChooser(e)
	return SelectRoute(false, e.Getenv("WAYLAND_DISPLAY") != "", bin), bin
}

// The chooser's exit codes: the contract with the graphical chooser.
const (
	chooserExitNoDisplay  = 2
	chooserExitCancelled  = 3
	chooserStdoutMaxBytes = 64 << 10
)

// chooserOutcome is everything running the chooser can come to.
type chooserOutcome int

const (
	chooserGranted     chooserOutcome = iota // exit 0 with a usable path
	chooserCancelled                         // exit 3: the user said no
	chooserNoDisplay                         // exit 2: it could not reach a display
	chooserUnavailable                       // it could not be executed at all
	chooserBroken                            // it ran and gave an answer that cannot be used
)

type chooserResult struct {
	outcome chooserOutcome
	path    string
	err     error
}

// runChooser executes the chooser with the contract's argv and reads its one
// line. The chooser inherits the environment (it needs WAYLAND_DISPLAY) and
// stderr; its stdout is a pipe, so what it prints is the only answer.
func runChooser(ctx context.Context, bin string, o Options, stderr io.Writer) chooserResult {
	args := []string{"--app", o.App, "--verb", o.Verb, "--mode", o.Mode, "--kind", o.Kind}
	if o.Start != "" {
		args = append(args, "--start", o.Start)
	}
	if o.Suggest != "" {
		args = append(args, "--suggest", o.Suggest)
	}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = &limitWriter{w: &out, left: chooserStdoutMaxBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return chooserResult{outcome: chooserUnavailable, err: err}
	}
	err := cmd.Wait()
	var ee *exec.ExitError
	switch {
	case err == nil:
		path, perr := parseChooserLine(out.Bytes())
		if perr != nil {
			return chooserResult{outcome: chooserBroken, err: perr}
		}
		return chooserResult{outcome: chooserGranted, path: path}
	case errors.As(err, &ee) && ee.ExitCode() == chooserExitCancelled:
		return chooserResult{outcome: chooserCancelled}
	case errors.As(err, &ee) && ee.ExitCode() == chooserExitNoDisplay:
		return chooserResult{outcome: chooserNoDisplay}
	}
	return chooserResult{outcome: chooserBroken, err: fmt.Errorf("chooser failed: %v", err)}
}

// parseChooserLine accepts exactly {"path":"/absolute/path"}, optionally
// followed by a newline, and nothing else.
func parseChooserLine(b []byte) (string, error) {
	var a struct {
		Path string `json:"path"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&a); err != nil {
		return "", fmt.Errorf("chooser gave an unusable answer %q: %v", clip(b), err)
	}
	if dec.More() || strings.TrimSpace(string(b[dec.InputOffset():])) != "" {
		return "", fmt.Errorf("chooser gave more than one answer: %q", clip(b))
	}
	if !filepath.IsAbs(a.Path) {
		return "", fmt.Errorf("chooser gave no absolute path: %q", clip(b))
	}
	return a.Path, nil
}

func clip(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

// limitWriter keeps the first left bytes and silently drops the rest, so a
// runaway chooser cannot grow the buffer; a truncated line then fails to parse.
type limitWriter struct {
	w    io.Writer
	left int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > l.left {
		p = p[:l.left]
	}
	l.left -= len(p)
	l.w.Write(p)
	return n, nil
}
