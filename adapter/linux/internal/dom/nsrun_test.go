package dom

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const printNS = `for t in pid mnt ipc uts; do echo $t:$(readlink /proc/self/ns/$t | sed 's/.*\[\(.*\)\]/\1/'); done; sleep 2`

func parseNS(out string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Fields(out) {
		k, v, _ := strings.Cut(l, ":")
		m[k] = v
	}
	return m
}

// While a run is alive `nsid` prints exactly the namespaces the payload sees
// from inside; once it has exited the stale record is refused.
func TestNSIDMatchesTheRunningPayload(t *testing.T) {
	st, _ := sandboxed(t)
	st.Create("nsd")
	defer st.Destroy("nsd", false)
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { st.Run("nsd", []string{"sh", "-c", printNS}, nil, &out, &out); close(done) }()
	var ids []string
	var err error
	for i := 0; i < 60; i++ {
		if ids, err = st.NSID("nsd"); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("nsid never worked: %v", err)
	}
	<-done
	inside := parseNS(out.String())
	if len(ids) != len(NSTypes) {
		t.Fatalf("ids %v", ids)
	}
	for _, l := range ids {
		k, v, _ := strings.Cut(l, ":")
		if inside[k] != v || v == "" {
			t.Errorf("%s: nsid %s, payload sees %q", k, v, inside[k])
		}
	}
	if _, err := st.NSID("nsd"); err == nil || !strings.Contains(err.Error(), "no live run") {
		t.Fatalf("stale record accepted: %v", err)
	}
}

// With AURORA_BUS_SOCK set, a run registers its namespaces BEFORE the payload
// starts, and unregisters when it ends.
func TestRunRegistersWithBus(t *testing.T) {
	st, _ := sandboxed(t)
	m, _, _ := st.Create("reg")
	defer st.Destroy("reg", false)
	sock := filepath.Join(t.TempDir(), "b")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("AURORA_BUS_SOCK", sock)
	var mu sync.Mutex
	var reqs []map[string]any
	var at []time.Time
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			var r struct {
				Verb    string
				To      string
				Payload map[string]any
			}
			json.Unmarshal(line, &r)
			mu.Lock()
			reqs = append(reqs, r.Payload)
			reqs[len(reqs)-1]["_verb"] = r.Verb + "/" + r.To
			at = append(at, time.Now())
			mu.Unlock()
			fmt.Fprintln(c, `{"kind":"response"}`)
			c.Close()
		}
	}()
	var out bytes.Buffer
	st.Run("reg", []string{"sh", "-c", "date +%s%N"}, nil, &out, &out)
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 2 {
		t.Fatalf("want add+remove, got %v", reqs)
	}
	add, rm := reqs[0], reqs[1]
	if add["_verb"] != "bus.domain.ns@v1/bus" || add["identity"] != ExecID(m) || add["pid"] == nil || add["remove"] != nil {
		t.Errorf("add: %v", add)
	}
	if ns, _ := add["ns"].(map[string]any); len(ns) != len(NSTypes) {
		t.Errorf("ns in add: %v", add["ns"])
	}
	if rm["remove"] != true || rm["pid"] != add["pid"] {
		t.Errorf("remove: %v", rm)
	}
	var payloadNs int64
	fmt.Sscan(strings.TrimSpace(out.String()), &payloadNs)
	if payloadNs == 0 || at[0].UnixNano() > payloadNs {
		t.Errorf("registration (%d) came after the payload started (%d)", at[0].UnixNano(), payloadNs)
	}
}
