package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/antiartificial/aurora/bus/proto"
)

// Topics a domain's network proxy reports on (decision 0015). Both live under
// net., a system namespace: no domain connection is ever delivered them.
const (
	TopicNetConn    = "net.conn"    // one connection's verdict (first of its kind, see 0015 on volume)
	TopicNetSummary = "net.summary" // counts of what was coalesced since the last summary
)

// netEntry is one (host, verdict, reason) tally in a summary.
type netEntry struct {
	Host    string `json:"host"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	Count   int    `json:"count"`
}

var netVerdicts = map[string]bool{"allowed": true, "refused": true, "failed": true}

const maxNetEntries = 600

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ctlNetReport publishes one connection event for a registered domain.
//
// The domain on the event is not a field the reporter writes: From is the
// registered identity the report names, the payload's domain is derived from
// it, and the daemon refuses an identity it has no live per-domain socket for,
// so a report can only ever speak for a domain busd itself is hosting. Only the
// session socket may report; a domain is refused (it must not narrate its own
// connections or anyone else's).
func (s *Server) ctlNetReport(c *conn, raw json.RawMessage) (any, error) {
	if c.viaDomain {
		return nil, cerr("forbidden", "network reports come from the session socket only")
	}
	var p struct {
		Identity string     `json:"identity"`
		Kind     string     `json:"kind"` // conn | summary
		Host     string     `json:"host"`
		Port     int        `json:"port"`
		Verdict  string     `json:"verdict"`
		Reason   string     `json:"reason"`
		First    bool       `json:"first"`
		Dropped  int        `json:"dropped"`
		Entries  []netEntry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Identity == "" {
		return nil, fmt.Errorf(`needs {"identity":"domain:<name>:<id>","kind":"conn|summary",...}`)
	}
	if s.doms[p.Identity] == nil {
		return nil, fmt.Errorf("domain %s is not registered: no event is published for a domain busd is not hosting", p.Identity)
	}
	name := strings.Split(p.Identity, ":")[1]
	var topic string
	var body any
	switch p.Kind {
	case "conn":
		if !netVerdicts[p.Verdict] {
			return nil, fmt.Errorf("verdict must be allowed, refused or failed")
		}
		topic = TopicNetConn
		body = map[string]any{"domain": name, "host": clip(p.Host, 253), "port": p.Port,
			"verdict": p.Verdict, "reason": clip(p.Reason, 200), "first": p.First}
	case "summary":
		if len(p.Entries) > maxNetEntries {
			p.Entries = p.Entries[:maxNetEntries]
		}
		for i := range p.Entries {
			p.Entries[i].Host = clip(p.Entries[i].Host, 253)
			p.Entries[i].Reason = clip(p.Entries[i].Reason, 200)
			if !netVerdicts[p.Entries[i].Verdict] {
				return nil, fmt.Errorf("verdict must be allowed, refused or failed")
			}
		}
		topic = TopicNetSummary
		body = map[string]any{"domain": name, "entries": p.Entries, "dropped": p.Dropped}
	default:
		return nil, fmt.Errorf("kind must be conn or summary")
	}
	pl, _ := json.Marshal(body)
	s.publish(proto.Envelope{
		ID: s.newID(), TS: s.clock.Now().String(), From: p.Identity,
		Kind: proto.KindEvent, Topic: topic, QoS: proto.QoSBackground, Payload: pl,
	})
	return map[string]string{"published": topic}, nil
}
