// supervisord runs the stack's services from a declarative file: starts them in
// dependency order, restarts them under a backoff / crash-loop policy, checks
// their health on the bus and publishes supervisor.service.* events. Drive it
// with auroractl. SIGTERM / SIGINT stop every service (dependents first) and
// exit only once none of them is left.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/antiartificial/aurora/supervisor"
)

func main() {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = os.TempDir()
	}
	conf := flag.String("config", "", "service list (JSON); ${VAR} in command and log is expanded from the environment")
	ctl := flag.String("control", envOr("AURORA_SUPERVISOR_SOCK", filepath.Join(rt, "aurora", "supervisor.sock")), "control socket (env AURORA_SUPERVISOR_SOCK)")
	busSock := flag.String("bus-socket", envOr("AURORA_BUS_SOCK", filepath.Join(rt, "aurora", "bus.sock")), "busd socket, for events and health (env AURORA_BUS_SOCK)")
	children := flag.String("children-file", "", "write 'name pid' lines here, so a wrapper can reap strays if this process is SIGKILLed")
	every := flag.Duration("health-interval", time.Second, "how often health is checked")
	flag.Parse()
	log.SetFlags(log.Ltime)
	log.SetPrefix("supervisord: ")
	if *conf == "" {
		fmt.Fprintln(os.Stderr, "supervisord: --config is required")
		os.Exit(2)
	}
	f, err := supervisor.Load(*conf)
	if err != nil {
		log.Fatalf("%s: %v", *conf, err)
	}
	s, err := supervisor.New(f, supervisor.Config{BusSocket: *busSock, ChildrenFile: *children, HealthEvery: *every, Logf: log.Printf})
	if err != nil {
		log.Fatal(err)
	}
	ln, err := s.Serve(*ctl)
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(*ctl)
	log.Printf("%d services from %s; control socket %s", len(f.Services), *conf, *ctl)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		log.Printf("signal: stopping services")
		s.Shutdown() // ends Run
	}()
	s.Run(context.Background())
	ln.Close()
	log.Printf("all services stopped")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
