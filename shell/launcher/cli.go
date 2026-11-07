package launcher

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
)

// Env carries the process-level dependencies, injected by main or by tests.
type Env struct {
	Backend func(socket string, timeout time.Duration) Backend
	In      *bufio.Reader
	Out     io.Writer
	Err     io.Writer
	// Pick asks the powerbox for a grant (default ExecPicker); tests inject one.
	Pick func(PickRequest) (PickResult, error)
	// Tty opens the terminal approval prompts are drawn on and read from
	// (default none; main sets OpenTTY). Tests inject a scripted one.
	Tty func() (io.ReadWriter, error)
	// Yes auto-allows approval requests raised by this command's own invokes
	// (--yes). A test affordance that defeats approval.
	Yes bool
}

const usage = `aurora - the Aurora launcher

Usage:
  aurora open <file>            pick a verb for a file and run it
  aurora open --pick            choose the input in the powerbox instead (the
                                controller can then tell your pick from a typed path)
  aurora chain <file> --verb A --verb B
                                run A, then feed its output to B
  aurora plumb SELECTION [--path|--mime|--text] [--limit N] [--json]
                                what could be done with a selection: a path, a MIME type
                                (--mime), or text that looks like a path, path:line:col,
                                URL or hostname. Ranked verbs with their effect classes;
                                lists only, runs nothing
  aurora verbs [--accepts MIME] list verbs the registry knows
  aurora apps                   list installed apps
  aurora watch [--yes]          live log of provenance.created events, and
                                answers approval requests on /dev/tty
  aurora approvals [--yes]      only answer approval requests on /dev/tty
                                (a destructive or external verb blocks until you do)
  aurora activity [--log DIR] [--since D]
                                timeline of everything the bus log recorded: each invoke
                                with its grants, approval (route, decider, domain trust),
                                handle ids and output provenance; unmatched entries and
                                hidden state (timeouts, claims, unlogged revokes) are listed
  aurora query                  list saved queries (NAME.query files kept by indexd)
  aurora query run NAME [--watch] [--count N] [--json]
                                the files a saved query matches; --watch then streams new
                                matches live. NOT filtered by capability, and it only knows
                                files whose provenance.created event indexd saw
  aurora query show FILE        one file's indexed writes, provenance and derived-from edges
  aurora ui tree APP [--window N] [--root TOKEN] [--depth N] [--no-text] [--json]
                                the accessibility tree of a running application: its windows, views
                                (name, role, frame, state) and the text each drew. APP is the service it
                                was launched to serve ("kitchensink" for ui.kitchensink). The application
                                decides, from the identity busd stamped on you, whether you may read it
  aurora ui node APP WINDOW TOKEN [--json]     one view
  aurora ui text APP WINDOW TOKEN [--deep]     the text one view drew (--deep: and its descendants)
  --yes (open, chain, watch, approvals) allows every request WITHOUT asking.
                                A test affordance that defeats the point.

Every command takes -h, --socket PATH (default $AURORA_BUS_SOCK or
$XDG_RUNTIME_DIR/aurora/bus.sock) and --timeout DURATION (default 10s).
Needs "busd" and then "controllerd" running.
`

// listFlag is a repeatable string flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error { *l = append(*l, s); return nil }

type common struct {
	socket  string
	timeout time.Duration
}

func newFlags(env *Env, name, syn string, c *common) *flag.FlagSet {
	fs := flag.NewFlagSet("aurora "+name, flag.ContinueOnError)
	fs.SetOutput(env.Err)
	fs.Usage = func() { fmt.Fprintf(env.Err, "Usage: aurora %s\n\n", syn); fs.PrintDefaults() }
	fs.StringVar(&c.socket, "socket", DefaultSocket(), "bus socket path")
	fs.DurationVar(&c.timeout, "timeout", 10*time.Second, "timeout for each bus call")
	return fs
}

// parseInterleaved lets flags follow positional arguments
// (`aurora chain f.png --verb A`), which the flag package does not.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// Run executes one aurora invocation and returns the exit code:
// 0 success, 1 failure, 2 usage error.
func Run(env *Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(env.Err, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(env.Out, usage)
		return 0
	case "open":
		return cmdOpen(env, rest)
	case "chain":
		return cmdChain(env, rest)
	case "plumb":
		return cmdPlumb(env, rest)
	case "verbs":
		return cmdVerbs(env, rest)
	case "apps":
		return cmdApps(env, rest)
	case "approvals":
		return cmdApprovals(env, rest)
	case "watch":
		return cmdWatch(env, rest)
	case "activity":
		return cmdActivity(env, rest)
	case "query":
		return cmdQuery(env, rest)
	case "ui":
		return cmdUI(env, rest)
	}
	fmt.Fprintf(env.Err, "aurora: unknown command %q\n\n%s", cmd, usage)
	return 2
}

// finish maps a parse error to an exit code.
func parseFailed(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func fail(env *Env, err error) int {
	fmt.Fprintf(env.Err, "aurora: %s\n", hint(err))
	return 1
}

func cmdApps(env *Env, args []string) int {
	var c common
	fs := newFlags(env, "apps", "apps", &c)
	if _, err := parseInterleaved(fs, args); err != nil {
		return parseFailed(err)
	}
	be := env.Backend(c.socket, c.timeout)
	defer be.Close()
	apps, err := be.Apps(context.Background())
	if err != nil {
		return fail(env, err)
	}
	if len(apps) == 0 {
		fmt.Fprintln(env.Out, "no apps registered")
		return 0
	}
	for _, a := range apps {
		fmt.Fprintf(env.Out, "%s  %s %s\n", a.ID, a.Name, a.Version)
		if a.Windowed {
			if a.ExecProblem != "" {
				fmt.Fprintf(env.Out, "    windowed: UNAVAILABLE, %s\n", a.ExecProblem)
			} else {
				fmt.Fprintf(env.Out, "    windowed: %s\n", a.Exec)
			}
		}
		if len(a.Verbs) > 0 || !a.Windowed {
			fmt.Fprintf(env.Out, "    verbs: %s\n", strings.Join(a.Verbs, ", "))
		}
	}
	return 0
}

func cmdVerbs(env *Env, args []string) int {
	var c common
	fs := newFlags(env, "verbs", "verbs [--accepts MIME]", &c)
	accepts := fs.String("accepts", "", "only verbs accepting this MIME type")
	if _, err := parseInterleaved(fs, args); err != nil {
		return parseFailed(err)
	}
	be := env.Backend(c.socket, c.timeout)
	defer be.Close()
	verbs, err := be.Verbs(context.Background(), *accepts)
	if err != nil {
		return fail(env, err)
	}
	if len(verbs) == 0 {
		fmt.Fprintln(env.Out, "no verbs")
		return 0
	}
	PrintVerbs(env.Out, verbs, false)
	return 0
}

func cmdWatch(env *Env, args []string) int {
	var c common
	fs := newFlags(env, "watch", "watch [--count N] [--yes] [--no-approvals]", &c)
	count := fs.Int("count", 0, "exit after N events (0 = run until interrupted)")
	yes := fs.Bool("yes", false, "TEST AFFORDANCE: allow every approval request without asking (defeats approval)")
	noAppr := fs.Bool("no-approvals", false, "do not answer approval requests")
	if _, err := parseInterleaved(fs, args); err != nil {
		return parseFailed(err)
	}
	be := env.Backend(c.socket, c.timeout)
	defer be.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if _, can := be.(Approver); can && !*noAppr {
		if stopA, err := startResponder(ctx, env, be, *yes); err != nil {
			fmt.Fprintf(env.Err, "aurora: not answering approvals: %v\n", err)
		} else {
			defer stopA()
			fmt.Fprintln(env.Out, "also answering approval.requested on /dev/tty")
		}
	}
	fmt.Fprintln(env.Out, "watching provenance.created (Ctrl-C to stop)")
	n := 0
	err := be.Watch(ctx, func(e ProvEvent) {
		fmt.Fprintln(env.Out, FormatProv(e))
		if n++; *count > 0 && n >= *count {
			cancel()
		}
	})
	if err != nil {
		return fail(env, err)
	}
	return 0
}

// FormatProv renders one provenance event on a single line.
func FormatProv(e ProvEvent) string {
	t := e.Time
	if p, err := time.Parse(time.RFC3339Nano, t); err == nil {
		t = p.Local().Format("15:04:05")
	}
	s := fmt.Sprintf("%s  %s  %s  -> %s", t, e.App, e.Verb, e.Output)
	if len(e.Inputs) > 0 {
		s += "  <- " + strings.Join(e.Inputs, ", ")
	}
	return s
}

func signalCtx() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}
