package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/antiartificial/aurora/bus/buslog"
	"github.com/antiartificial/aurora/bus/hlc"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/replay"
	"github.com/antiartificial/aurora/bus/server"
)

func cmdLog(args []string) int {
	fs, sock := cmdFlags("log", "[flags]", "Query the append-only log (ephemeral messages are never in it).")
	since := fs.String("since", "", "only messages newer than this duration, e.g. 10m")
	topic := fs.String("topic", "", "only events whose topic matches this glob, e.g. 'window.*'")
	limit := fs.Int("limit", 100, "most recent N messages (0 = all)")
	asJSON := fs.Bool("json", false, "print raw envelopes, one per line")
	_, code, done := parse(fs, args, 0, 0)
	if done {
		return code
	}
	c, code := dial(*sock)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := timeoutCtx(30 * time.Second)
	defer cancel()
	raw, err := c.Control(ctx, "bus.log@v1", map[string]any{"since": *since, "topic": *topic, "limit": *limit})
	if err != nil {
		return fail("%v", err)
	}
	var reply struct{ Messages []proto.Envelope }
	if err := json.Unmarshal(raw, &reply); err != nil {
		return fail("bad reply: %v", err)
	}
	for _, m := range reply.Messages {
		if *asJSON {
			b, _ := json.Marshal(m)
			fmt.Println(string(b))
			continue
		}
		fmt.Println(formatMsg(m))
	}
	fmt.Fprintf(os.Stderr, "%d message(s)\n", len(reply.Messages))
	return 0
}

func formatMsg(m proto.Envelope) string {
	ts := m.TS
	if t, err := hlc.Parse(m.TS); err == nil {
		ts = time.Unix(0, t.Wall).Format("15:04:05.000") + fmt.Sprintf("+%d", t.Counter)
	}
	what := m.Verb
	if m.Kind == proto.KindEvent {
		what = m.Topic
	}
	s := fmt.Sprintf("%s %-8s %s -> %s %s %s", ts, m.Kind, m.From, orDash(m.To), what, m.Payload)
	if m.Corr != "" {
		s += " corr=" + m.Corr
	}
	if len(m.CapIDs) > 0 {
		s += " caps=" + strings.Join(m.CapIDs, ",")
	}
	for _, sg := range m.Segments {
		if sg.Kind == "dmabuf" {
			s += fmt.Sprintf(" seg=%s(dmabuf %d bytes ino %d, unsealed, NO hash)", sg.ID, sg.Bytes, sg.Ino)
			continue
		}
		s += fmt.Sprintf(" seg=%s(%d bytes %s)", sg.ID, sg.Bytes, sg.SHA256[:min(len(sg.SHA256), 19)])
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

func cmdInspect(args []string) int {
	fs, sock := cmdFlags("inspect", "[flags]", "List connected clients, registered services and subscriptions.")
	asJSON := fs.Bool("json", false, "print JSON")
	_, code, done := parse(fs, args, 0, 0)
	if done {
		return code
	}
	c, code := dial(*sock)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := timeoutCtx(10 * time.Second)
	defer cancel()
	raw, err := c.Control(ctx, "bus.inspect@v1", nil)
	if err != nil {
		return fail("%v", err)
	}
	if *asJSON {
		fmt.Println(string(raw))
		return 0
	}
	var r server.InspectReply
	if err := json.Unmarshal(raw, &r); err != nil {
		return fail("bad reply: %v", err)
	}
	fmt.Printf("clients (%d):\n", len(r.Clients))
	for _, c := range r.Clients {
		fmt.Printf("  #%d %s pid=%d uid=%d since %s\n", c.ID, c.Domain, c.PID, c.UID, c.Connected.Format("15:04:05"))
		if sg := c.Segments; sg.Received+int64(sg.Delivered)+int64(sg.OpenQueued) > 0 {
			fmt.Printf("      segments: %d open (%d bytes, held by busd awaiting delivery), %d delivered, %d received (%d bytes)\n",
				sg.OpenQueued, sg.OpenBytes, sg.Delivered, sg.Received, sg.ReceivedByte)
		}
		for rank, cs := range c.Outbox {
			if cs.Queued > 0 {
				fmt.Printf("      backlog %s: %d queued, oldest %dms\n", qosNames[rank], cs.Queued, cs.OldestAgeMS)
			}
		}
		if c.Dropped > 0 {
			fmt.Printf("      dropped: %d\n", c.Dropped)
		}
	}
	fmt.Printf("services (%d):\n", len(r.Services))
	for _, s := range r.Services {
		fmt.Printf("  %s  (client #%d, %s)\n", s.Name, s.Client, s.Domain)
	}
	fmt.Printf("subscriptions (%d):\n", len(r.Subscriptions))
	for _, s := range r.Subscriptions {
		fmt.Printf("  %s  (client #%d, %s)\n", s.Topic, s.Client, s.Domain)
	}
	fmt.Printf("domain sockets (%d):\n", len(r.Domains))
	for _, d := range r.Domains {
		cg := d.Cgroup
		if cg == "" {
			cg = "(none: unattestable)"
		}
		uid := "-"
		if d.UID != nil {
			uid = fmt.Sprint(*d.UID)
		}
		fmt.Printf("  %s  %s  cgroup=%s uid=%s\n", d.Identity, d.Socket, cg, uid)
	}
	fmt.Printf("handles (%d outstanding):\n", len(r.Handles))
	for _, h := range r.Handles {
		exp := "never"
		if h.Expires != nil {
			exp = h.Expires.Format("15:04:05.000")
		}
		fmt.Printf("  %s  resource=%s  holder=%s  minter=%s  expires=%s\n", h.ID, h.Resource, h.Holder, h.Minter, exp)
	}
	return 0
}

func cmdReplay(args []string) int {
	fs, sock := cmdFlags("replay", "[flags] <logfile|logdir>",
		"Re-deliver a recorded session in its original order.\n\n"+
			"  --check   start a fresh in-process daemon, replay into it, and verify\n"+
			"            INVARIANTS (causal order, final state per subscriber, capability\n"+
			"            grants). Exit 0 on pass, 1 on fail. Bytes are never compared.\n"+
			"  default   re-deliver to the daemon at --socket (start it with\n"+
			"            `busd --allow-replay` so original ids/senders are kept as origin).")
	check := fs.Bool("check", false, "verify invariants against a fresh in-process daemon")
	speed := fs.Float64("speed", 0, "pace by recorded timestamps at this multiple (0 = as fast as possible)")
	pos, code, done := parse(fs, args, 1, 1)
	if done {
		return code
	}
	msgs, err := buslog.ReadFile(pos[0])
	if err != nil {
		return fail("%v", err)
	}
	if len(msgs) == 0 {
		return fail("%s contains no messages", pos[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if *check {
		res, err := replay.RunCheck(ctx, msgs, *speed)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Printf("replayed %d of %d messages into a fresh daemon\n", res.Delivered, len(msgs))
		if n := countSegments(msgs); n > 0 {
			fmt.Printf("note: %d segment(s) were carried; their CONTENTS are not in the log and are not replayed.\n"+
				"      replay re-sends descriptors (id, size, mime, sha256) marked absent, and the check compares those.\n", n)
		}
		for _, c := range res.Report.Checks {
			mark := "PASS"
			if !c.OK {
				mark = "FAIL"
			}
			fmt.Printf("  [%s] %-28s %s\n", mark, c.Name, c.Detail)
		}
		if !res.Report.OK {
			fmt.Println("replay check: FAIL")
			return 1
		}
		fmt.Println("replay check: PASS")
		return 0
	}
	c, code := dial(*sock)
	if c == nil {
		return code
	}
	defer c.Close()
	if err := replay.Deliver(ctx, c, msgs, *speed); err != nil {
		return fail("%v", err)
	}
	fmt.Printf("re-delivered %d messages to %s\n", len(msgs), *sock)
	return 0
}

var qosNames = [...]string{"user-interactive", "user-initiated", "utility", "background"}

func countSegments(msgs []proto.Envelope) (n int) {
	for _, m := range msgs {
		n += len(m.Segments)
	}
	return n
}
