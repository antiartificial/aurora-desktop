// bus is the command-line client for busd.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/antiartificial/aurora/bus/client"
)

const usage = `usage: bus <command> [flags] [args]

commands:
  send <target> <verb> [json]    call a verb and print the reply
  sub <topic>                    stream events matching a glob
  pub <topic> [json]             publish an event
  serve <name> <command>         register a service; run command per request
  log                            query the log
  inspect                        list clients, services, subscriptions
  replay <logfile|dir>           re-deliver a recorded session
  verify <logdir>                check the log's hash chain (tamper evidence)

Every command takes -h. Socket: --socket or AURORA_BUS_SOCK
(default $XDG_RUNTIME_DIR/aurora/bus.sock).
`

func defaultSocket() string {
	if s := os.Getenv("AURORA_BUS_SOCK"); s != "" {
		return s
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = filepath.Join(os.TempDir(), fmt.Sprintf("aurora-%d", os.Getuid()))
	}
	return filepath.Join(rt, "aurora", "bus.sock")
}

// cmdFlags builds a flag set with the shared --socket flag and a usage line.
func cmdFlags(name, synopsis, help string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet("bus "+name, flag.ContinueOnError)
	sock := fs.String("socket", defaultSocket(), "busd socket (env AURORA_BUS_SOCK)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: bus %s %s\n\n%s\n\nflags:\n", name, synopsis, help)
		fs.PrintDefaults()
	}
	return fs, sock
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "bus: "+format+"\n", args...)
	return 1
}

// parse accepts flags before, between or after positional arguments and
// stores the positionals in the returned slice. done means the caller should
// return code.
func parse(fs *flag.FlagSet, args []string, minArgs, maxArgs int) (pos []string, code int, done bool) {
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0, true
			}
			return nil, 2, true
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if n := len(pos); n < minArgs || (maxArgs >= 0 && n > maxArgs) {
		fmt.Fprintf(os.Stderr, "bus %s: wrong number of arguments\n\n", fs.Name()[4:])
		fs.Usage()
		return nil, 2, true
	}
	return pos, 0, false
}

func dial(sock string) (*client.Client, int) {
	c, err := client.Dial(sock)
	if err != nil {
		return nil, fail("%v", err)
	}
	return c, 0
}

// jsonArg validates an optional JSON argument.
func jsonArg(pos []string, i int) (json.RawMessage, error) {
	if len(pos) <= i {
		return nil, nil
	}
	s := pos[i]
	if !json.Valid([]byte(s)) {
		return nil, fmt.Errorf("argument %q is not valid JSON", s)
	}
	return json.RawMessage(s), nil
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	rest := args[1:]
	switch args[0] {
	case "send":
		return cmdSend(rest)
	case "sub":
		return cmdSub(rest)
	case "pub":
		return cmdPub(rest)
	case "serve":
		return cmdServe(rest)
	case "log":
		return cmdLog(rest)
	case "inspect":
		return cmdInspect(rest)
	case "replay":
		return cmdReplay(rest)
	case "verify":
		return cmdVerify(rest)
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
		return 0
	}
	fmt.Fprintf(os.Stderr, "bus: unknown command %q\n\n%s", args[0], usage)
	return 2
}

func timeoutCtx(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
