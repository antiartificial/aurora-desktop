// Command powerbox is the file picker the user drives to grant an app access
// to one file. It prints a JSON {"path","mode","ticket"} line on stdout.
//
// The user chooses in the graphical chooser (aurora-chooser) when a Wayland
// display is reachable, else on /dev/tty, never on stdout or stdin, so a
// script that pipes into or reads from it does not supply the gesture. The
// chosen path is then confirmed on the compositor's trusted socket, and a
// ticket is minted only on a signed allow (--attest).
//
// Not a trusted path: see contracts/decisions/0007-ambient-and-gesture-grants.md.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/antiartificial/aurora/controller/ticket"
	"github.com/antiartificial/aurora/shell/picker"
)

func main() {
	fs := flag.NewFlagSet("powerbox", flag.ExitOnError)
	app := fs.String("app", "", "app id the grant is for (required)")
	verb := fs.String("verb", "", "verb the grant is for, e.g. copy@v1 (required)")
	mode := fs.String("mode", "ro", "ro or rw")
	kind := fs.String("kind", "", "file, dir or save (default: file for ro, save for rw)")
	start := fs.String("start", "", "directory to open in")
	suggest := fs.String("suggest", "", "save: suggested file name")
	ttl := fs.Duration("ttl", ticket.DefaultTTL, "ticket lifetime")
	keyPath := fs.String("key", ticket.KeyPath(), "shared key file (env AURORA_POWERBOX_KEY), created 0600 if missing")
	forceTTY := fs.Bool("tty", false, "choose in the terminal even when a chooser and a display are available")
	sock := fs.String("approval-socket", "", "compositor trusted socket to confirm on (default: env AURORA_TRUSTED_SOCK, else $XDG_RUNTIME_DIR/aurora-trusted.sock)")
	attest := fs.String("attest", "auto", "auto: confirm on the compositor when its socket is configured or present, and fail if it does not answer; required: also fail when there is no socket; off: never confirm (mints a ticket that proves no trusted-path gesture)")
	approvalTimeout := fs.Duration("approval-timeout", picker.DefaultApprovalTimeout, "how long to wait for the compositor's answer")
	testPath := fs.String("path", "", "TEST AFFORDANCE: mint for this path with NO user gesture. Defeats the purpose of the powerbox; never use outside tests.")
	fs.Parse(os.Args[1:])

	if *app == "" || *verb == "" || (*mode != "ro" && *mode != "rw") {
		die("need --app, --verb and --mode ro|rw")
	}
	if *kind == "" {
		*kind = picker.KindFile
		if *mode == "rw" {
			*kind = picker.KindSave
		}
	}
	key, err := ticket.LoadKey(*keyPath, true)
	if err != nil {
		die("key %s: %v", *keyPath, err)
	}
	o := picker.Options{App: *app, Verb: *verb, Mode: *mode, Kind: *kind, Start: *start, Suggest: *suggest, TTL: *ttl, Key: key}

	var res picker.Result
	if *testPath != "" {
		if os.Getenv("AURORA_POWERBOX_ALLOW_PATH") != "1" {
			die("--path is a test affordance that skips the user gesture; set AURORA_POWERBOX_ALLOW_PATH=1 to use it (a speed bump, not a security boundary: any same-user process can set it)")
		}
		fmt.Fprintln(os.Stderr, "powerbox: WARNING: --path is a test affordance; no user chose this, the ticket proves nothing about a gesture")
		res, err = picker.Mint(o, *testPath)
	} else {
		var pb picker.Powerbox
		if pb, err = wire(o, *forceTTY, *attest, *sock, *approvalTimeout); err == nil {
			res, err = pb.Grant(context.Background())
		}
	}
	if err != nil {
		die("%v", err)
	}
	json.NewEncoder(os.Stdout).Encode(res)
}

func wire(o picker.Options, forceTTY bool, attest, sock string, timeout time.Duration) (picker.Powerbox, error) {
	policy, err := picker.ParseAttestPolicy(attest)
	if err != nil {
		return picker.Powerbox{}, err
	}
	env := picker.OSEnv()
	confirmer, err := picker.NewConfirmer(policy, sock, env, timeout)
	if err != nil {
		return picker.Powerbox{}, err
	}
	route, chooser := picker.PlanRoute(env, forceTTY)
	return picker.Powerbox{Opts: o, Route: route, Chooser: chooser, TTY: picker.OpenTTY, Confirm: confirmer, Log: os.Stderr}, nil
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "powerbox: "+f+"\n", a...)
	os.Exit(1)
}
