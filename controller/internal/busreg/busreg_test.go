package busreg

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/server"
)

func TestAgainstRealBus(t *testing.T) {
	d := t.TempDir()
	sock := filepath.Join(d, "b.sock")
	srv, err := server.New(server.Config{SocketPath: sock, DataDir: filepath.Join(d, "log")})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()
	var c *client.Client
	for i := 0; i < 100; i++ {
		if c, err = client.Dial(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	b := Client{C: c}
	id, path := "domain:x:00ff00ff00ff00ff", filepath.Join(d, "x", "bus.sock")
	if err := b.AddDomain(id, path, "/x/cg"); err != nil {
		t.Fatal(err)
	}
	if err := b.AddDomain(id, path, "/x/cg"); err != nil {
		t.Fatalf("already registered must be success: %v", err)
	}
	if err := b.AddDomain("uid1000:evil", filepath.Join(d, "y", "bus.sock"), ""); err == nil {
		t.Fatal("bad identity accepted")
	}
	if err := b.RemoveDomain(id); err != nil {
		t.Fatal(err)
	}
	if err := b.RemoveDomain(id); err != nil {
		t.Fatalf("unknown domain on remove must be success: %v", err)
	}
}
