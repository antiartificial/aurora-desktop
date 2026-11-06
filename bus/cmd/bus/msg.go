package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

func cmdSend(args []string) int {
	fs, sock := cmdFlags("send", "[flags] <target> <verb> [json]", "Call a verb on a registered service and print the typed reply payload.\n")
	qos := fs.String("qos", "", "QoS class (user-interactive|user-initiated|utility|background)")
	timeout := fs.Duration("timeout", 10*time.Second, "how long to wait for the reply")
	full := fs.Bool("envelope", false, "print the whole reply envelope instead of the payload")
	var segs segFlag
	fs.Var(&segs, "segment", segHelp)
	var caps segFlag
	fs.Var(&caps, "cap", "present a capability handle (from bus.mint@v1 / bus.cap.granted); repeatable")
	pos, code, done := parse(fs, args, 2, 3)
	if done {
		return code
	}
	payload, err := jsonArg(pos, 2)
	if err != nil {
		return fail("%v", err)
	}
	q, err := proto.ParseQoS(*qos)
	if err != nil {
		return fail("%v", err)
	}
	c, code := dial(*sock)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := timeoutCtx(*timeout)
	defer cancel()
	req := proto.Envelope{To: pos[0], Verb: pos[1], Payload: payload, QoS: q, Caps: caps}
	if err := attachFiles(&req, segs); err != nil {
		return fail("%v", err)
	}
	r, err := c.Call(ctx, req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fail("no reply from %q within %v", pos[0], *timeout)
		}
		return fail("%v", err)
	}
	if *full {
		b, _ := json.Marshal(r)
		fmt.Println(string(b))
	} else {
		fmt.Println(string(r.Payload))
	}
	return 0
}

func cmdSub(args []string) int {
	fs, sock := cmdFlags("sub", "[flags] <topic-glob>", "Stream events whose topic matches the glob (e.g. 'window.*', '**') as JSON envelopes, one per line, until interrupted.")
	payloadOnly := fs.Bool("payload-only", false, "print only each event's payload")
	segDir := fs.String("segment-dir", "", "write each received segment into this directory, named by segment id")
	pos, code, done := parse(fs, args, 1, 1)
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
	if err := c.Subscribe(ctx, pos[0]); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(os.Stderr, "subscribed %s\n", pos[0])
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				return fail("connection to busd lost")
			}
			if *segDir != "" {
				if err := saveSegments(e, *segDir); err != nil {
					fmt.Fprintf(os.Stderr, "bus: save segment: %v\n", err)
				}
			}
			e.CloseAttachments()
			if *payloadOnly {
				fmt.Println(string(e.Payload))
			} else {
				b, _ := json.Marshal(e)
				fmt.Println(string(b))
			}
		case <-sig:
			return 0
		}
	}
}

func cmdPub(args []string) int {
	fs, sock := cmdFlags("pub", "[flags] <topic> [json]", "Publish an event. ")
	qos := fs.String("qos", "", "QoS class (user-interactive|user-initiated|utility|background)")
	eph := fs.Bool("ephemeral", false, "deliver but do not log")
	var segs segFlag
	fs.Var(&segs, "segment", segHelp)
	pos, code, done := parse(fs, args, 1, 2)
	if done {
		return code
	}
	payload, err := jsonArg(pos, 1)
	if err != nil {
		return fail("%v", err)
	}
	q, err := proto.ParseQoS(*qos)
	if err != nil {
		return fail("%v", err)
	}
	c, code := dial(*sock)
	if c == nil {
		return code
	}
	defer c.Close()
	ctx, cancel := timeoutCtx(10 * time.Second)
	defer cancel()
	ev := proto.Envelope{Kind: proto.KindEvent, Topic: pos[0], Payload: payload, QoS: q, Ephemeral: *eph}
	if err := attachFiles(&ev, segs); err != nil {
		return fail("%v", err)
	}
	if err := c.Send(ev); err != nil {
		return fail("%v", err)
	}
	if err := c.Flush(ctx); err != nil {
		return fail("%v", err)
	}
	return 0
}

func cmdServe(args []string) int {
	fs, sock := cmdFlags("serve", "[flags] <name> <command>",
		"Register as service <name>. For each request, run <command> with `sh -c`,\nthe payload on stdin, and reply with its stdout (JSON, or a JSON string if\nit is not valid JSON). A non-zero exit becomes an error reply.\nEnvironment: BUS_FROM (caller domain), BUS_VERB. A request that carries\nsegments gets each as a read-only fd 3, 4, ... and BUS_SEGMENTS (descriptor\nJSON) and BUS_SEGMENT_FDS; read them with e.g. `sha256sum <&3`.")
	pos, code, done := parse(fs, args, 2, 2)
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
	if err := c.Register(ctx, pos[0]); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(os.Stderr, "serving %s\n", pos[0])
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case req, ok := <-c.Requests():
			if !ok {
				return fail("connection to busd lost")
			}
			wg.Add(1)
			go func() { defer wg.Done(); handle(c, req, pos[1]) }()
		case <-sig:
			return 0
		}
	}
}

func handle(c *client.Client, req proto.Envelope, command string) {
	defer req.CloseAttachments()
	cmd := exec.Command("sh", "-c", command)
	files, env, err := segmentEnv(req)
	defer func() {
		for _, f := range files {
			f.Close()
		}
	}()
	if err != nil {
		c.ReplyError(req, "bad_segments", err.Error())
		return
	}
	cmd.ExtraFiles = files
	cmd.Stdin = bytes.NewReader(req.Payload)
	cmd.Env = append(append(os.Environ(), "BUS_FROM="+req.From, "BUS_VERB="+req.Verb), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		c.ReplyError(req, "command_failed", fmt.Sprintf("%v: %s", err, bytes.TrimSpace(stderr.Bytes())))
		return
	}
	out = bytes.TrimSpace(out)
	if !json.Valid(out) {
		out, _ = json.Marshal(string(out))
	}
	c.Reply(req, out)
}
