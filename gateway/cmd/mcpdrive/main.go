// mcpdrive is a scripted MCP client for tests and demos. It starts an MCP
// server (the command after "--"), performs the initialize handshake, then
// reads one command per line on stdin and prints the server's raw JSON-RPC
// response, one line each:
//
//	list                  tools/list
//	call NAME [JSON]      tools/call with that arguments object
//	raw JSON              send this JSON-RPC message as is
//
// It is not an LLM and decides nothing; the caller scripts every step.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	name := "mcpdrive"
	args := os.Args[1:]
	if len(args) >= 2 && args[0] == "--name" {
		name, args = args[1], args[2:]
	}
	if len(args) < 2 || args[0] != "--" {
		fmt.Fprintln(os.Stderr, "usage: mcpdrive [--name CLIENT] -- server-command [args]")
		os.Exit(2)
	}
	cmd := exec.Command(args[1], args[2:]...)
	cmd.Stderr = os.Stderr
	in, _ := cmd.StdinPipe()
	outp, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "mcpdrive:", err)
		os.Exit(1)
	}
	rd := bufio.NewReaderSize(outp, 1<<20)
	id := 0
	rpc := func(method string, params any) string {
		id++
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		return exchange(in, rd, b)
	}
	resp := rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": name, "version": "0"}})
	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(resp), &init) != nil || init.Result.ProtocolVersion == "" {
		fmt.Fprintln(os.Stderr, "mcpdrive: initialize failed:", resp)
		os.Exit(1)
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	in.Write(append(b, '\n'))
	fmt.Println(resp)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		f := strings.SplitN(strings.TrimSpace(sc.Text()), " ", 3)
		switch f[0] {
		case "":
		case "list":
			fmt.Println(rpc("tools/list", map[string]any{}))
		case "call":
			if len(f) < 2 {
				fmt.Println(`{"error":"call needs a tool name"}`)
				continue
			}
			var a any = map[string]any{}
			if len(f) == 3 {
				if err := json.Unmarshal([]byte(f[2]), &a); err != nil {
					fmt.Println(`{"error":"bad JSON arguments"}`)
					continue
				}
			}
			fmt.Println(rpc("tools/call", map[string]any{"name": f[1], "arguments": a}))
		case "raw":
			if len(f) < 2 {
				continue
			}
			fmt.Println(exchange(in, rd, []byte(strings.TrimSpace(sc.Text()[3:]))))
		default:
			fmt.Println(`{"error":"unknown command"}`)
		}
	}
	in.Close()
	cmd.Wait()
}

func exchange(in interface{ Write([]byte) (int, error) }, rd *bufio.Reader, msg []byte) string {
	in.Write(append(msg, '\n'))
	line, err := rd.ReadString('\n')
	if err != nil && line == "" {
		return fmt.Sprintf(`{"error":"server closed: %v"}`, err)
	}
	return strings.TrimRight(line, "\n")
}
