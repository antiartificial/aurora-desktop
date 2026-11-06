// controllerd loads app manifests, registers the `registry` service on the
// bus and runs verbs inside isolated domains.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/config"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/service"
)

func main() {
	fs := flag.NewFlagSet("controllerd", flag.ExitOnError)
	appsDirs := fs.String("apps", "apps", "comma-separated apps directories (each <dir>/<app>/manifest.toml)")
	sock := fs.String("socket", config.BusSocket(), "bus socket (env AURORA_BUS_SOCK)")
	dbin := fs.String("domain-bin", "", "path to the `domain` binary (env AURORA_DOMAIN_BIN; else PATH; else adapter/linux/out/domain)")
	require := fs.Bool("require-isolation", false, "refuse to run verbs when domain tooling is unavailable instead of falling back")
	timeout := fs.Duration("timeout", time.Minute, "per-invoke time limit")
	execs := map[string]string{}
	fs.Func("exec", "app executable override, id=path (repeatable); for apps whose binary is built outside the app dir", func(v string) error {
		id, p, ok := strings.Cut(v, "=")
		if !ok {
			return fmt.Errorf("want id=path")
		}
		execs[id] = p
		return nil
	})
	fs.Parse(os.Args[1:])

	log.SetFlags(log.Ltime)
	log.SetPrefix("controllerd: ")
	reg, skipped := apps.Load(strings.Split(*appsDirs, ",")...)
	for _, s := range skipped {
		log.Printf("SKIPPED %s: %s", s.Path, s.Reason)
	}
	for _, a := range reg.Apps() {
		log.Printf("loaded %s %s (%d verbs) from %s", a.ID(), a.Manifest.App.Version, len(a.Manifest.Verbs), a.Dir)
	}

	runner := domain.CLI{Bin: domain.Locate(*dbin, "")}
	if err := runner.Available(); err != nil {
		log.Printf("WARNING: domain tooling unavailable: %v", err)
		if *require {
			log.Fatalf("--require-isolation set; refusing to start")
		}
		log.Printf("WARNING: verbs will run UNISOLATED on the host and provenance will say isolated=false")
	} else {
		log.Printf("domain tooling: %s", runner.Bin)
	}

	c, err := client.Dial(*sock)
	if err != nil {
		log.Fatalf("%v", err)
	}
	defer c.Close()
	inv := &invoke.Invoker{
		Reg: reg, Runner: runner, Logf: log.Printf, RequireIsolation: *require, Execs: execs, Timeout: *timeout,
		Publish: func(topic string, payload []byte) error {
			return c.Publish(topic, payload, proto.QoSInitiated, false)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = c.Register(ctx, config.Service)
	cancel()
	if err != nil {
		log.Fatalf("register %s: %v", config.Service, err)
	}
	log.Printf("registered service %q on %s", config.Service, *sock)

	svc := &service.Service{Reg: reg, Inv: inv}
	go svc.Serve(c)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Printf("%v: stopping", s)
	case <-c.Done():
		log.Fatalf("bus connection lost")
	}
}
