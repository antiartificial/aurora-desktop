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
  invoke <app> <verb> --input F [--output F] [--arg k=v ...]
                                             run a verb (values: JSON if valid, else string)
  provenance <file>                          read back the file's provenance xattr
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
		var kv multi
		fs.Var(&kv, "arg", "k=v (repeatable)")
		pos := split(fs, rest)
		if len(pos) != 2 {
			die("usage: controller invoke <app> <verb> --input F [--output F] [--arg k=v ...]")
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
		call(sock, "registry.invoke@v1", p)
	case "provenance":
		pos := split(fs, rest)
		if len(pos) != 1 {
			die("usage: controller provenance <file>")
		}
		r, err := prov.Read(pos[0])
		if err != nil {
			die("%v", err)
		}
		show(r)
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
