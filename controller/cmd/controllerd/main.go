// controllerd loads app manifests, registers the `registry` service on the
// bus and runs verbs inside isolated domains.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/contracts/plumb"
	"github.com/antiartificial/aurora/controller/internal/apps"
	"github.com/antiartificial/aurora/controller/internal/busreg"
	"github.com/antiartificial/aurora/controller/internal/capbus"
	"github.com/antiartificial/aurora/controller/internal/config"
	"github.com/antiartificial/aurora/controller/internal/domain"
	"github.com/antiartificial/aurora/controller/internal/invoke"
	"github.com/antiartificial/aurora/controller/internal/service"
	"github.com/antiartificial/aurora/controller/ticket"
)

func main() {
	fs := flag.NewFlagSet("controllerd", flag.ExitOnError)
	appsDirs := fs.String("apps", "apps", "comma-separated apps directories (each <dir>/<app>/manifest.toml)")
	sock := fs.String("socket", config.BusSocket(), "bus socket (env AURORA_BUS_SOCK)")
	dbin := fs.String("domain-bin", "", "path to the `domain` binary (env AURORA_DOMAIN_BIN; else PATH; else adapter/linux/out/domain)")
	require := fs.Bool("require-isolation", false, "refuse to run verbs when domain tooling is unavailable instead of falling back")
	noGesture := fs.Bool("no-gesture-grants", false, "refuse any grant not covered by the app manifest's capabilities.paths (default: gesture grants allowed)")
	reqPB := fs.Bool("require-powerbox", false, "refuse any grant that is neither ambient nor backed by a valid powerbox ticket (bare gesture grants are rejected)")
	keyPath := fs.String("powerbox-key", ticket.KeyPath(), "shared key file for powerbox tickets (env AURORA_POWERBOX_KEY), created 0600 if missing")
	autoApprove := fs.Bool("auto-approve", false, "TEST AFFORDANCE, defeats approval: allow every destructive/external invoke on a non-ambient grant without asking anyone (logged loudly each time)")
	compSock := fs.String("compositor-socket", config.TrustedSocket(), "compositor trusted-path socket (env AURORA_TRUSTED_SOCK): approvals are drawn by the compositor when it answers here, else they fall back to the bus route")
	preflightBin := fs.String("preflight-bin", "", "aurora-preflight helper run inside the app's domain before a compositor prompt so the bus attests which domain asked (default: next to controllerd, or in PATH); none found means prompts show the domain as an unverified claim")
	dropSock := fs.String("drop-socket", config.DropSocket(), "socket the compositor reports file drops on (env AURORA_DROP_SOCK; decision 0020): a file dropped on a window is mounted read-only into that window's running domain; \"none\" turns file drops off")
	noComp := fs.Bool("no-compositor-approval", false, "never ask the compositor; use only the bus route (approval.requested / approval.decide@v1)")
	compUID := fs.Int("compositor-uid", -1, "uid the compositor must run as; a peer on the compositor socket with another uid, or inside a domain cgroup, is refused and approvals fall back to the bus route (default: this process's own uid)")
	apprTimeout := fs.Duration("approval-timeout", invoke.DefaultApprovalTimeout, "how long an approval request waits for a decision before the answer is deny")
	plumbRules := fs.String("plumb-rules", "", "plumb rules file (default: the rules embedded from contracts/plumb/rules.toml); a file that does not validate stops startup")
	unsandboxed := fs.Bool("unsandboxed-windows", false, "ESCAPE HATCH: launch windowed apps as plain host processes instead of in a domain (HAIKU_SRC builds, debugging). Default off: a dock click puts the app in a domain with device:wayland and what its manifest declares. Logged on every launch")
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

	if *noGesture {
		log.Printf("--no-gesture-grants: invokes whose paths fall outside the manifest's declared capabilities will be refused")
	} else {
		log.Printf("gesture grants allowed: paths outside a manifest's declared capabilities are granted on request and recorded as source=gesture")
	}

	var tickets *invoke.Tickets
	if key, err := ticket.LoadKey(*keyPath, true); err != nil {
		log.Printf("WARNING: powerbox key unusable (%v): every ticket will be refused", err)
	} else {
		tickets = &invoke.Tickets{Key: key}
		log.Printf("powerbox tickets verified with key %s (anything that can read it can forge tickets)", *keyPath)
	}
	if *reqPB {
		log.Printf("--require-powerbox: grants must be ambient or backed by a powerbox ticket; bare gesture grants are refused")
	}

	approver := &invoke.Approver{Timeout: *apprTimeout, AutoApprove: *autoApprove}
	if !*noComp {
		approver.Compositor = &invoke.CompositorPrompt{Socket: *compSock, Logf: log.Printf}
		log.Printf("compositor replies must be signed with the key %s (shared with the compositor; anything that can read it can forge a reply)", ticket.ApprovalKeyPath())
		if *compUID >= 0 {
			u := uint32(*compUID)
			approver.Compositor.ExpectedUID = &u
			log.Printf("compositor peer must be uid %d and outside any %s cgroup", u, invoke.DefaultDenyCgroupComponent)
		} else {
			log.Printf("compositor peer must be uid %d (this process) and outside any %s cgroup", os.Geteuid(), invoke.DefaultDenyCgroupComponent)
		}
		if _, err := os.Stat(*compSock); err != nil {
			log.Printf("compositor trusted socket %s not present: approvals will use the bus route until it appears (each request tries the compositor first)", *compSock)
		} else {
			log.Printf("approvals are drawn by the compositor via %s when it answers; otherwise the bus route", *compSock)
		}
	}
	if pb := locateHelper(*preflightBin, "aurora-preflight"); pb != "" {
		approver.Preflight = &invoke.Preflight{Bin: pb}
		log.Printf("approval pre-flight: %s runs inside the domain before each prompt, on both routes", pb)
	} else {
		log.Printf("WARNING: no aurora-preflight helper found: prompts will show the domain as an unverified claim (build ./cmd/aurora-preflight, pass --preflight-bin)")
	}
	if *autoApprove {
		log.Printf("WARNING: --auto-approve: destructive and external verbs on non-ambient grants will be APPROVED WITHOUT ASKING. This is a test affordance and defeats the point of approval.")
	} else {
		log.Printf("approval enforced: destructive/external verbs on non-ambient grants wait %s on approval.requested for approval.decide@v1 from the session socket, then are denied", *apprTimeout)
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

	if *unsandboxed {
		log.Printf("WARNING: --unsandboxed-windows: windowed apps launch as HOST PROCESSES with your files, network and environment, in no domain, with no grant and no indicator (decision 0012). This is a debugging escape hatch.")
	} else {
		log.Printf("windowed apps launch in a domain with device:wayland plus their manifest's declared capabilities (registry.launch@v1)")
	}

	c, err := client.Dial(*sock)
	if err != nil {
		log.Fatalf("%v", err)
	}
	defer c.Close()
	inv := &invoke.Invoker{
		Reg: reg, Runner: runner, Logf: log.Printf, RequireIsolation: *require, NoGestureGrants: *noGesture, RequirePowerbox: *reqPB, Tickets: tickets, Execs: execs, Timeout: *timeout, Approvals: approver,
		Bus: busreg.Client{C: c}, Caps: &capbus.Client{C: c}, UnsandboxedWindows: *unsandboxed,
		Publish: func(topic string, payload []byte) error {
			return c.Publish(topic, payload, proto.QoSInitiated, false)
		},
	}
	inv.DropDeny = []string{*keyPath, ticket.ApprovalKeyPath()}
	if *dropSock != "none" && *dropSock != "" {
		ds := &invoke.DropServer{Inv: inv, Socket: *dropSock, Logf: log.Printf}
		if *compUID >= 0 {
			u := uint32(*compUID)
			ds.ExpectedUID = &u
		}
		if err := ds.Listen(); err != nil {
			log.Printf("WARNING: file drops are off: cannot listen on %s: %v", *dropSock, err)
		} else {
			defer ds.Close()
			go ds.Serve(context.Background())
			log.Printf("file drops: the compositor may ask for a read-only mount of a dropped file into a running window's domain on %s; directories and read-write go through the approval prompt", *dropSock)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = c.Register(ctx, config.Service)
	cancel()
	if err != nil {
		log.Fatalf("register %s: %v", config.Service, err)
	}
	log.Printf("registered service %q on %s", config.Service, *sock)

	svc := &service.Service{Reg: reg, Inv: inv}
	if *plumbRules != "" {
		if svc.Rules, err = plumb.Load(*plumbRules); err != nil {
			log.Fatalf("--plumb-rules: %v", err)
		}
		log.Printf("plumb rules: %d from %s", svc.Rules.Len(), *plumbRules)
	}
	go svc.Serve(c)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Printf("%v: stopping", s)
		inv.Close()
	case <-c.Done():
		log.Fatalf("bus connection lost")
	}
}

// locateHelper finds a helper binary: explicit path, next to this executable, then PATH.
func locateHelper(explicit, name string) string {
	if explicit != "" {
		return explicit
	}
	if exe, err := os.Executable(); err == nil {
		c := filepath.Join(filepath.Dir(exe), name)
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}
