// aurora-gateway is an MCP server on stdio. It offers the verbs a policy file
// allows as tools and runs calls through the controller. See gateway/README.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/antiartificial/aurora/gateway"
	"github.com/antiartificial/aurora/shell/launcher"
)

func main() {
	fs := flag.NewFlagSet("aurora-gateway", flag.ExitOnError)
	sock := fs.String("socket", launcher.DefaultSocket(), "bus socket (env AURORA_BUS_SOCK)")
	policy := fs.String("policy", "", "policy file (JSON): apps, effects, roots (required)")
	timeout := fs.Duration("timeout", 30*time.Second, "timeout for each registry call")
	wait := fs.Duration("approval-wait", gateway.DefaultApprovalWait, "how long a destructive/external call waits for the controller to raise its approval before reporting 'running'")
	fs.Parse(os.Args[1:])
	if *policy == "" {
		fmt.Fprintln(os.Stderr, "aurora-gateway: --policy is required (there is no default allow-all)")
		os.Exit(2)
	}
	p, err := gateway.LoadPolicy(*policy)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aurora-gateway:", err)
		os.Exit(2)
	}
	conn, err := gateway.Dial(*sock, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aurora-gateway:", err)
		os.Exit(1)
	}
	defer conn.Close()
	g := gateway.New(conn, conn, p)
	g.ApprovalWait = *wait
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := g.Start(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "aurora-gateway:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "aurora-gateway: serving MCP on stdio for policy %q (apps %v, effects %v, roots %v)\n", p.Label, p.Apps, p.Effects, p.Roots)
	s := &gateway.Server{G: g}
	if err := s.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "aurora-gateway:", err)
		os.Exit(1)
	}
}
