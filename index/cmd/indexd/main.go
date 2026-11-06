// Command indexd keeps an index of files, attributes and provenance, built
// from the bus's provenance.created events. See index/README.md.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/index"
)

func main() {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = os.TempDir()
	}
	sock := flag.String("socket", envOr("AURORA_BUS_SOCK", filepath.Join(rt, "aurora", "bus.sock")), "busd socket (env AURORA_BUS_SOCK)")
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(os.Getenv("HOME"), ".local", "share")
	}
	dir := flag.String("data-dir", filepath.Join(data, "aurora", "index"), "where index.jsonl lives")
	qdir := flag.String("queries", "", "saved queries directory (default <data-dir>/queries)")
	flag.Parse()
	if *qdir == "" {
		*qdir = filepath.Join(*dir, "queries")
	}
	if err := os.MkdirAll(*qdir, 0o700); err != nil {
		log.Fatal(err)
	}
	ix, err := index.Open(*dir)
	if err != nil {
		log.Fatalf("open index: %v", err)
	}
	defer ix.Close()
	c, err := client.Dial(*sock)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	svc := &index.Service{Idx: ix, QueriesDir: *qdir, C: c, Logf: log.Printf}
	log.Printf("indexd: index only knows what provenance.created events it sees; see index/README.md for who sees what")
	if err := svc.Run(ctx); err != nil {
		log.Fatalf("indexd: %v", err)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
