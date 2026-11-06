// busprobe is a fixture app (demo.sh and tests, not shipped): given the
// controller's JSON request on stdin it copies input to output, then dials
// $AURORA_BUS_SOCKET, the socket `domain run` binds in at /run/aurora, and
// publishes app.probe. It sends no identity; the bus decides `from`.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

func main() {
	var rq struct{ Input, Output string }
	if err := json.NewDecoder(os.Stdin).Decode(&rq); err != nil {
		fmt.Fprintln(os.Stderr, "busprobe:", err)
		os.Exit(2)
	}
	if err := cp(rq.Input, rq.Output); err != nil {
		fmt.Fprintln(os.Stderr, "busprobe:", err)
		os.Exit(1)
	}
	sock := os.Getenv("AURORA_BUS_SOCKET")
	c, err := client.Dial(sock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "busprobe: cannot reach the bus at %q: %v\n", sock, err)
		os.Exit(1)
	}
	defer c.Close()
	p, _ := json.Marshal(map[string]string{"hello": "from inside the domain", "socket": sock})
	if err := c.Publish("app.probe", p, proto.QoSInitiated, false); err != nil {
		fmt.Fprintln(os.Stderr, "busprobe:", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Flush(ctx)
	fmt.Println(`{"ok":true}`)
}

func cp(in, out string) error {
	src, err := os.Open(in)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(out, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}
