// busd is the Aurora message bus daemon.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
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
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: busd [flags]\n\nThe Aurora message bus daemon. Sender identity comes from SO_PEERCRED.")
		fs.PrintDefaults()
	}
	fs.Parse(os.Args[1:])

	log.SetFlags(log.Ltime)
	log.SetPrefix("busd: ")
	srv, err := server.New(server.Config{
		SocketPath: *sock, DataDir: *data, SegmentBytes: *segBytes, AllowReplay: *allowReplay,
		Logf: log.Printf,
	})
	if err != nil {
		log.Fatalf("%v", err)
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
