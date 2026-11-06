package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
)

// MCP over stdio, the part that is implemented: newline-delimited JSON-RPC 2.0
// (the stdio transport), initialize with protocol version negotiation,
// notifications/initialized, ping, tools/list and tools/call, with tool
// definitions carrying inputSchema, annotations and _meta, and call results
// carrying content plus structuredContent and isError.
//
// Not implemented: resources, prompts, sampling, roots, elicitation, logging,
// completion, progress notifications, request cancellation
// (notifications/cancelled is ignored), tools/list pagination (the whole list
// is returned, no nextCursor), listChanged notifications (the list is rebuilt
// on every tools/list, but a client is not told when it changes), JSON-RPC
// batches, the streamable HTTP transport, and authorization. The server
// writes only JSON-RPC to stdout; nothing else may share it.

var supportedVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// ServerVersion is reported in serverInfo.
const ServerVersion = "0.1.0"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Server is one MCP session over a reader and a writer.
type Server struct {
	G *Gateway

	wmu         sync.Mutex
	out         io.Writer
	mu          sync.Mutex
	initialized bool
	agent       string // clientInfo.name: whatever the client said it is
}

// Serve reads requests until in closes or ctx ends. Each request is handled in
// its own goroutine so a call waiting on an approval does not block ping.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		if len(line) == 0 {
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); s.handle(ctx, line) }()
	}
	wg.Wait()
	return sc.Err()
}

func (s *Server) send(r rpcResponse) {
	r.JSONRPC = "2.0"
	b, _ := json.Marshal(r)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.out.Write(append(b, '\n'))
}

func (s *Server) fail(id json.RawMessage, code int, msg string) {
	s.send(rpcResponse{ID: id, Error: &rpcError{code, clean(msg)}})
}

func (s *Server) handle(ctx context.Context, line []byte) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		if len(line) > 0 && line[0] == '[' {
			s.fail(json.RawMessage("null"), -32600, "batches are not supported")
			return
		}
		s.fail(json.RawMessage("null"), -32700, "parse error")
		return
	}
	notification := len(req.ID) == 0
	if notification {
		return // notifications/initialized, notifications/cancelled and the rest need no reply
	}
	switch req.Method {
	case "initialize":
		s.initialize(req)
	case "ping":
		s.send(rpcResponse{ID: req.ID, Result: map[string]any{}})
	case "tools/list":
		if !s.ready(req) {
			return
		}
		tools, err := s.G.Tools(ctx)
		if err != nil {
			s.fail(req.ID, -32603, "cannot build the tool list: "+err.Error())
			return
		}
		s.send(rpcResponse{ID: req.ID, Result: map[string]any{"tools": tools}})
	case "tools/call":
		if !s.ready(req) {
			return
		}
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &p) != nil || p.Name == "" {
			s.fail(req.ID, -32602, "tools/call needs a name")
			return
		}
		s.mu.Lock()
		agent := s.agent
		s.mu.Unlock()
		res := s.G.Call(ctx, agent, p.Name, p.Arguments)
		b, _ := json.Marshal(res)
		s.send(rpcResponse{ID: req.ID, Result: map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(b)}},
			"structuredContent": json.RawMessage(b),
			"isError":           res.IsError(),
		}})
	default:
		s.fail(req.ID, -32601, "method not found: "+req.Method)
	}
}

func (s *Server) ready(req rpcRequest) bool {
	s.mu.Lock()
	ok := s.initialized
	s.mu.Unlock()
	if !ok {
		s.fail(req.ID, -32002, "server not initialized: send initialize first")
	}
	return ok
}

func (s *Server) initialize(req rpcRequest) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	json.Unmarshal(req.Params, &p)
	ver := supportedVersions[0]
	for _, v := range supportedVersions {
		if v == p.ProtocolVersion {
			ver = v
		}
	}
	s.mu.Lock()
	s.initialized, s.agent = true, p.ClientInfo.Name
	s.mu.Unlock()
	s.send(rpcResponse{ID: req.ID, Result: map[string]any{
		"protocolVersion": ver,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": "aurora-gateway", "version": ServerVersion},
		"instructions":    "Tools are Aurora verbs allowed to this session. A destructive or external tool stops at a user approval; you cannot approve it. Writes are not staged in a snapshot.",
	}})
}
