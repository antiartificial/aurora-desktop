// aurora-preflight runs INSIDE an app's domain, before the app's payload, and
// publishes one approval.preflight event on the domain's own bus socket. busd
// stamps `from` with the domain identity it derives from the socket and the
// connector's cgroup, so the compositor can learn which domain the request
// came from without trusting the controller's word for it.
//
// It needs no grants and reads nothing but its argv. It carries no secret and
// no authority: the event is only evidence of where it was published from.
//
//	aurora-preflight REQUEST_ID APP VERB EFFECT [GRANT ...]
//
// Exit 0 only when busd has processed the event. Bus socket: $AURORA_BUS_SOCKET
// (the domain sets /run/aurora/bus.sock).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "aurora-preflight:", err)
		os.Exit(1)
	}
}

func run(a []string) error {
	if len(a) < 4 {
		return fmt.Errorf("usage: aurora-preflight REQUEST_ID APP VERB EFFECT [GRANT ...]")
	}
	sock := os.Getenv("AURORA_BUS_SOCKET")
	if sock == "" {
		sock = "/run/aurora/bus.sock"
	}
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	payload, _ := json.Marshal(map[string]any{"request_id": a[0], "app": a[1], "verb": a[2], "effect": a[3], "grants": a[4:]})
	ev, _ := json.Marshal(map[string]any{"kind": "event", "topic": "approval.preflight", "payload": json.RawMessage(payload)})
	ping, _ := json.Marshal(map[string]any{"id": "pf", "kind": "request", "to": "bus", "verb": "bus.ping@v1"})
	if _, err := c.Write(append(append(ev, '\n'), append(ping, '\n')...)); err != nil {
		return err
	}
	// Messages on one connection are handled in order: the ping's answer
	// means the event was stamped and logged. A refused connection (failed
	// attestation) writes an error line and closes instead.
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("bus closed the connection before confirming: %w", err)
		}
		var e struct{ Kind, Corr string }
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		switch {
		case e.Kind == "error":
			return fmt.Errorf("bus refused: %s", line)
		case e.Corr == "pf" && e.Kind == "response":
			return nil
		}
	}
}
