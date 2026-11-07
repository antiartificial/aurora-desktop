package picker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// TTYUI opens the terminal picker's input and output. The returned func
// releases them.
type TTYUI func() (*bufio.Reader, io.Writer, func(), error)

// OpenTTY is the real /dev/tty. The user interface is on the controlling
// terminal, never on stdin or stdout, so a script that pipes into or reads
// from the powerbox does not supply the gesture.
func OpenTTY() (*bufio.Reader, io.Writer, func(), error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, nil, errors.New("no controlling terminal (/dev/tty): the picker needs a user to drive it")
	}
	return bufio.NewReader(tty), tty, func() { tty.Close() }, nil
}

// Powerbox is one grant, fully wired: which UI chooses, who confirms, where
// warnings go. Grant is the only thing that mints a ticket for a user.
type Powerbox struct {
	Opts    Options
	Route   Route
	Chooser string // binary for RouteChooser
	TTY     TTYUI
	Confirm Confirmer
	Log     io.Writer // warnings; stderr
}

// Grant chooses a path, resolves it, gets the second yes, and only then
// mints. Every way it can end short of a ticket is an error.
func (pb Powerbox) Grant(ctx context.Context) (Result, error) {
	path, err := pb.choose(ctx)
	if err != nil {
		return Result{}, err
	}
	resolved, err := Resolve(pb.Opts.Kind, path)
	if err != nil {
		return Result{}, err
	}
	c := pb.Confirm.Confirm(ctx, Ask{App: pb.Opts.App, Verb: pb.Opts.Verb, Mode: pb.Opts.Mode, Kind: pb.Opts.Kind, Path: resolved, Overwrites: overwrites(pb.Opts.Kind, resolved)})
	switch c.Verdict {
	case VerdictAllowed:
	case VerdictUnattested:
		fmt.Fprintf(pb.Log, "powerbox: WARNING: minting WITHOUT compositor attestation (%s); this ticket proves no trusted-path gesture\n", c.Why)
	case VerdictDenied, VerdictFailed:
		return Result{}, c.Err
	default:
		return Result{}, fmt.Errorf("unknown confirm verdict %v", c.Verdict)
	}
	// The user approved resolved. If something under it changed since, the
	// ticket would bind a path nobody was shown.
	if again, err := Resolve(pb.Opts.Kind, resolved); err != nil || again != resolved {
		return Result{}, fmt.Errorf("%s changed while the grant was being confirmed: nothing was granted", resolved)
	}
	return Mint(pb.Opts, resolved)
}

func overwrites(kind, path string) bool {
	if kind != KindSave {
		return false
	}
	_, err := os.Lstat(path)
	return err == nil
}

// choose returns the path the user picked, by the route's UI.
func (pb Powerbox) choose(ctx context.Context) (string, error) {
	switch pb.Route {
	case RouteForcedTTY, RouteTTY:
		return pb.chooseTTY()
	case RouteChooser:
		r := runChooser(ctx, pb.Chooser, pb.Opts, pb.Log)
		switch r.outcome {
		case chooserGranted:
			return r.path, nil
		case chooserCancelled:
			// The user said no. Asking again in another UI is the wrong answer.
			return "", ErrCancelled
		case chooserNoDisplay:
			fmt.Fprintf(pb.Log, "powerbox: chooser %s could not reach a display; using the terminal\n", pb.Chooser)
			return pb.chooseTTY()
		case chooserUnavailable:
			fmt.Fprintf(pb.Log, "powerbox: chooser %s cannot be run (%v); using the terminal\n", pb.Chooser, r.err)
			return pb.chooseTTY()
		case chooserBroken:
			return "", r.err
		}
		return "", fmt.Errorf("unknown chooser outcome %d", r.outcome)
	}
	return "", fmt.Errorf("unknown route %v", pb.Route)
}

func (pb Powerbox) chooseTTY() (string, error) {
	in, out, done, err := pb.TTY()
	if err != nil {
		return "", err
	}
	defer done()
	return Choose(pb.Opts, in, out)
}
