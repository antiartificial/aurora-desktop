// controller is a thin CLI over the registry service.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/controller/internal/config"
	"github.com/antiartificial/aurora/controller/internal/prov"
)

const usage = `usage: controller [--socket S] <command>

  apps                                       list loaded apps
  verbs [--accepts MIME]                     list verbs, optionally those accepting MIME
  describe <app> <verb>                      a verb's input/output schema and effect
  invoke <app> <verb> --input F [--output F] [--ticket T] [--output-ticket T] [--arg k=v ...]
                                             run a verb (values: JSON if valid, else string)
  decide <request_id> allow|deny             answer an approval.requested (from the session socket)
  provenance <file> [--verify]               read back the file's provenance xattr; --verify asks the
                                             daemon about the record's handle ids (exit 0 verified,
                                             3 not verified, 4 inconclusive)
`

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "controller: "+f+"\n", a...)
	os.Exit(1)
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(s string) error { *m = append(*m, s); return nil }

// split lets flags appear before or after positional arguments.
func split(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			os.Exit(2)
		}
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func main() {
	args := os.Args[1:]
	sock := config.BusSocket()
	if len(args) >= 2 && args[0] == "--socket" {
		sock, args = args[1], args[2:]
	}
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "apps":
		call(sock, "registry.apps@v1", map[string]any{})
	case "verbs":
		acc := fs.String("accepts", "", "MIME type")
		split(fs, rest)
		p := map[string]any{}
		if *acc != "" {
			p["accepts"] = *acc
		}
		call(sock, "registry.verbs@v1", p)
	case "describe":
		pos := split(fs, rest)
		if len(pos) != 2 {
			die("usage: controller describe <app> <verb>")
		}
		call(sock, "registry.describe@v1", map[string]any{"app": pos[0], "verb": pos[1]})
	case "invoke":
		in, out := fs.String("input", "", "input file (absolute)"), fs.String("output", "", "output file (absolute)")
		tk, otk := fs.String("ticket", "", "powerbox ticket for the input"), fs.String("output-ticket", "", "powerbox ticket for the output")
		var kv multi
		fs.Var(&kv, "arg", "k=v (repeatable)")
		pos := split(fs, rest)
		if len(pos) != 2 {
			die("usage: controller invoke <app> <verb> --input F [--output F] [--ticket T] [--output-ticket T] [--arg k=v ...]")
		}
		a := map[string]any{}
		for _, s := range kv {
			k, v, ok := strings.Cut(s, "=")
			if !ok {
				die("--arg wants k=v, got %q", s)
			}
			var j any
			if json.Unmarshal([]byte(v), &j) != nil {
				j = v
			}
			a[k] = j
		}
		p := map[string]any{"app": pos[0], "verb": pos[1], "args": a}
		if *in != "" {
			p["input"] = *in
		}
		if *out != "" {
			p["output"] = *out
		}
		if *tk != "" {
			p["ticket"] = *tk
		}
		if *otk != "" {
			p["output_ticket"] = *otk
		}
		call(sock, "registry.invoke@v1", p)
	case "decide":
		pos := split(fs, rest)
		if len(pos) != 2 {
			die("usage: controller decide <request_id> allow|deny")
		}
		call(sock, "approval.decide@v1", map[string]any{"request_id": pos[0], "decision": pos[1]})
	case "provenance":
		verify := fs.Bool("verify", false, "check the record's handle ids against the daemon")
		pos := split(fs, rest)
		if len(pos) != 1 {
			die("usage: controller provenance <file> [--verify]")
		}
		r, err := prov.Read(pos[0])
		if err != nil {
			die("%v", err)
		}
		show(r)
		if *verify {
			os.Exit(verifyRecord(sock, r))
		}
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func show(v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(b))
}

func call(sock, verb string, payload any) {
	c, err := client.Dial(sock)
	if err != nil {
		die("%v", err)
	}
	defer c.Close()
	b, _ := json.Marshal(payload)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, err := c.Call(ctx, proto.Envelope{To: config.Service, Verb: verb, Payload: b})
	if err != nil {
		if len(r.Payload) > 0 {
			fmt.Println(string(r.Payload))
		}
		die("%v", err)
	}
	var v any
	if json.Unmarshal(r.Payload, &v) != nil {
		fmt.Println(string(r.Payload))
		return
	}
	show(v)
}

// verifyRecord asks controllerd to check the record's handles and prints the
// report. Exit status: 0 verified (or the record names no handles), 3 not
// verified, 4 inconclusive.
func verifyRecord(sock string, r prov.Record) int {
	c, err := client.Dial(sock)
	if err != nil {
		die("%v", err)
	}
	defer c.Close()
	b, _ := json.Marshal(map[string]any{"record": r})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := c.Call(ctx, proto.Envelope{To: config.Service, Verb: "registry.verify@v1", Payload: b})
	if err != nil {
		die("verify: %v", err)
	}
	var rep prov.CapReport
	if err := json.Unmarshal(resp.Payload, &rep); err != nil {
		die("verify: unreadable reply: %v", err)
	}
	fmt.Println("-- handle verification")
	show(rep)
	switch rep.Status {
	case prov.StatusNotVerified:
		fmt.Println("RESULT: NOT VERIFIED")
		return 3
	case prov.StatusInconclusive:
		fmt.Println("RESULT: INCONCLUSIVE (not a negative: see detail)")
		return 4
	case prov.StatusNoHandles:
		fmt.Println("RESULT: no handles in this record; nothing to verify")
		return 0
	}
	fmt.Println("RESULT: VERIFIED (what this proves is narrow: see controller/README.md)")
	return 0
}
