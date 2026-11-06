// busd is the Aurora message bus daemon.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/antiartificial/aurora/bus/server"
)

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

func defaultDataDir() string {
	if d := os.Getenv("AURORA_BUS_DATA"); d != "" {
		return d
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "aurora", "bus", "log")
}

func main() {
	fs := flag.NewFlagSet("busd", flag.ExitOnError)
	sock := fs.String("socket", defaultSocket(), "Unix socket path (env AURORA_BUS_SOCK)")
	data := fs.String("data-dir", defaultDataDir(), "log segment directory (env AURORA_BUS_DATA)")
	segBytes := fs.Int64("segment-bytes", 0, "rotate log segments at this size (default 4 MiB)")
	allowReplay := fs.Bool("allow-replay", false, "accept replayed messages' origin/cap_ids (for fresh daemons used with `bus replay`)")
	legacy := fs.Bool("legacy-caps", false, "do NOT enforce the capability table: caps stay opaque, forgeable strings (old behaviour)")
	var doms domFlags
	fs.Var(&doms, "domain", "listen for a domain: IDENTITY=SOCKET (identity as printed by `domain exec-id`, socket by `domain sockpath`); repeatable")
	var cgs domFlags
	fs.Var(&cgs, "domain-cgroup", "cgroup of a --domain: IDENTITY=CGROUP (as printed by `domain cgroup`); repeatable. Connections are accepted only from processes in it")
	attest := fs.String("attest-domains", "prefer", "domain socket attestation: require (refuse anything unattestable), prefer (attest when a cgroup is registered and the host can; otherwise accept loudly), off (socket is the identity)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: busd [flags]\n\nThe Aurora message bus daemon. Sender identity comes from SO_PEERCRED.")
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])

	log.SetFlags(log.Ltime)
	log.SetPrefix("busd: ")
	mode, err := server.ParseAttestMode(*attest)
	if err != nil {
		log.Fatalf("--attest-domains: %v", err)
	}
	srv, err := server.New(server.Config{
		AttestDomains: mode,
		SocketPath:    *sock, DataDir: *data, SegmentBytes: *segBytes, AllowReplay: *allowReplay, EnforceCaps: !*legacy,
		Logf: log.Printf,
	})
	if err != nil {
		log.Fatalf("%v", err)
	}
	cgroups := map[string]string{}
	for _, c := range cgs {
		id, cg, _ := strings.Cut(c, "=")
		cgroups[id] = cg
	}
	for id := range cgroups {
		if !doms.has(id) {
			srv.Close()
			log.Fatalf("--domain-cgroup %s: no --domain with that identity", id)
		}
	}
	for _, d := range doms {
		id, path, _ := strings.Cut(d, "=")
		if err := srv.AddDomainCgroup(id, path, cgroups[id]); err != nil {
			srv.Close()
			log.Fatalf("--domain %s: %v", d, err)
		}
		log.Printf("domain socket %s -> %s (cgroup %q)", path, id, cgroups[id])
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Printf("%v: shutting down", s)
		if err := srv.Close(); err != nil {
			log.Printf("close: %v", err)
		}
	}()
	log.Printf("listening on %s, log in %s", *sock, *data)
	if err := srv.Serve(); err != nil {
		log.Fatalf("serve: %v", err)
	}
	log.Printf("stopped")
}

type domFlags []string

func (d *domFlags) String() string { return strings.Join(*d, ",") }
func (d *domFlags) Set(v string) error {
	if !strings.Contains(v, "=") {
		return fmt.Errorf("want IDENTITY=VALUE, got %q", v)
	}
	*d = append(*d, v)
	return nil
}

func (d domFlags) has(id string) bool {
	for _, v := range d {
		if strings.HasPrefix(v, id+"=") {
			return true
		}
	}
	return false
}
