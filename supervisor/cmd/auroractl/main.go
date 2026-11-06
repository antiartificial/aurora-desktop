// auroractl talks to supervisord: what is running, is it healthy, and start /
// stop / restart one service.
//
//	auroractl status [--json] [-q]     table of services; -q prints nothing and exits 3 if any gave up
//	auroractl start|stop NAME [--no-wait]
//	auroractl restart NAME [--force] [--no-wait]
//	auroractl pid NAME                 the service's pid (exit 1 if none)
//
// The socket is --socket, else $AURORA_SUPERVISOR_SOCK, else
// $XDG_RUNTIME_DIR/aurora/supervisor.sock.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/antiartificial/aurora/supervisor"
)

var sock string

func main() {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = os.TempDir()
	}
	def := os.Getenv("AURORA_SUPERVISOR_SOCK")
	if def == "" {
		def = filepath.Join(rt, "aurora", "supervisor.sock")
	}
	args := os.Args[1:]
	// --socket may appear anywhere
	for i := 0; i < len(args); i++ {
		if args[i] == "--socket" && i+1 < len(args) {
			def = args[i+1]
			args = append(args[:i:i], args[i+2:]...)
			break
		}
	}
	sock = def
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(os.Stderr, "usage: auroractl [--socket PATH] status [--json] [-q] | start NAME | stop NAME | restart [--force] NAME | pid NAME")
		os.Exit(2)
	}
	switch args[0] {
	case "status":
		os.Exit(cmdStatus(args[1:]))
	case "pid":
		os.Exit(cmdPID(args[1:]))
	case "start", "stop", "restart":
		os.Exit(cmdAct(args[0], args[1:]))
	}
	fmt.Fprintf(os.Stderr, "auroractl: unknown command %q\n", args[0])
	os.Exit(2)
}

func call(r supervisor.Request) (supervisor.Response, error) {
	var resp supervisor.Response
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return resp, fmt.Errorf("no supervisor at %s (%v)", sock, err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(20 * time.Second))
	if err := json.NewEncoder(c).Encode(r); err != nil {
		return resp, err
	}
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return resp, err
	}
	if !resp.OK {
		return resp, fmt.Errorf("%s", resp.Error)
	}
	return resp, nil
}

func fail(f string, a ...any) int { fmt.Fprintf(os.Stderr, "auroractl: "+f+"\n", a...); return 1 }

func uptime(sec float64) string {
	d := time.Duration(sec) * time.Second
	switch {
	case d < time.Minute:
		return d.String()
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	quiet := fs.Bool("q", false, "print nothing; exit 3 if any service gave up")
	fs.Parse(args)
	r, err := call(supervisor.Request{Op: "status"})
	if err != nil {
		return fail("%v", err)
	}
	if *quiet {
		bad := 0
		for _, s := range r.Services {
			if s.State == supervisor.GaveUp {
				fmt.Fprintln(os.Stderr, s.Name)
				bad = 3
			}
		}
		return bad
	}
	if *asJSON {
		b, _ := json.Marshal(r)
		fmt.Println(string(b))
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVICE\tSTATE\tHEALTH\tPID\tUPTIME\tRESTARTS\tLAST EXIT")
	for _, s := range r.Services {
		pid, up := "-", "-"
		if s.PID != 0 {
			pid, up = fmt.Sprint(s.PID), uptime(s.UptimeSec)
		}
		last := s.LastExit
		if last == "" {
			last = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", c(s.Name), s.State, c(s.Health), pid, up, s.Restarts, c(last))
	}
	tw.Flush()
	for _, s := range r.Services {
		if s.Detail != "" && s.State != supervisor.Running {
			fmt.Printf("  %s: %s\n", c(s.Name), c(s.Detail))
		}
		if s.Health == "unhealthy" || (s.Health == "pending" && s.HealthReason != "") {
			fmt.Printf("  %s: %s\n", c(s.Name), c(s.HealthReason))
		}
	}
	return 0
}

// c scrubs a string for the terminal; everything here originated in the
// supervisor, but some of it (names, reasons) was once bus-supplied.
func c(s string) string { return supervisor.Clean(strings.TrimSpace(s)) }

func cmdPID(args []string) int {
	if len(args) != 1 {
		return fail("usage: pid NAME")
	}
	r, err := call(supervisor.Request{Op: "status"})
	if err != nil {
		return fail("%v", err)
	}
	for _, s := range r.Services {
		if s.Name == args[0] && s.PID != 0 {
			fmt.Println(s.PID)
			return 0
		}
	}
	return 1
}

func cmdAct(op string, args []string) int {
	fs := flag.NewFlagSet(op, flag.ExitOnError)
	force := fs.Bool("force", false, "restart the bus even though clients the supervisor did not start are connected to it")
	nowait := fs.Bool("no-wait", false, "return as soon as the supervisor accepts the request")
	// allow flags after the name
	var names []string
	rest := args
	for len(rest) > 0 {
		fs.Parse(rest)
		rest = fs.Args()
		if len(rest) > 0 {
			names = append(names, rest[0])
			rest = rest[1:]
		}
	}
	if len(names) != 1 {
		return fail("usage: %s [flags] NAME", op)
	}
	name := names[0]
	before, err := call(supervisor.Request{Op: "status"})
	if err != nil {
		return fail("%v", err)
	}
	oldPID := 0
	for _, s := range before.Services {
		if s.Name == name {
			oldPID = s.PID
		}
	}
	r, err := call(supervisor.Request{Op: op, Name: name, Force: *force})
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(r.Message)
	if *nowait {
		return 0
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
		st, err := call(supervisor.Request{Op: "status"})
		if err != nil {
			return fail("%v", err)
		}
		for _, s := range st.Services {
			if s.Name != name {
				continue
			}
			switch {
			case op == "stop" && s.PID == 0 && s.State == supervisor.Stopped:
				fmt.Printf("%s stopped\n", name)
				return 0
			case op != "stop" && s.PID != 0 && (op == "start" || s.PID != oldPID) && (s.Health == "healthy" || s.Health == "unchecked"):
				fmt.Printf("%s is up: pid %d, %s\n", name, s.PID, s.Health)
				return 0
			case op != "stop" && s.State == supervisor.GaveUp:
				return fail("%s gave up: %s", name, c(s.Detail))
			}
		}
	}
	return fail("timed out waiting for %s (see: auroractl status)", name)
}
