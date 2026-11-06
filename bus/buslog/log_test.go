package buslog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/hlc"
	"github.com/antiartificial/aurora/bus/proto"
)

func at(wall time.Time, topic string) proto.Envelope {
	return proto.Envelope{
		ID: topic, TS: hlc.Timestamp{Wall: wall.UnixNano()}.String(), Kind: proto.KindEvent,
		Topic: topic, Payload: json.RawMessage(`{}`),
	}
}

func TestQueryByTopicAndTime(t *testing.T) {
	l, err := Open(t.TempDir(), 300) // tiny segments force rotation
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	now := time.Now()
	for _, e := range []proto.Envelope{
		at(now.Add(-2*time.Hour), "window.old"),
		at(now.Add(-30*time.Minute), "window.mid"),
		at(now.Add(-30*time.Minute), "audio.mid"),
		at(now.Add(-1*time.Minute), "window.new"),
		at(now, "audio.new"),
	} {
		if err := l.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if segs, _ := filepath.Glob(filepath.Join(l.dir, "seg-*")); len(segs) < 2 {
		t.Fatalf("expected rotation, got %d segment(s)", len(segs))
	}
	ids := func(q Query) string {
		m, err := l.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, e := range m {
			s = append(s, e.ID)
		}
		return strings.Join(s, ",")
	}
	if got := ids(Query{Topic: "window.*"}); got != "window.old,window.mid,window.new" {
		t.Errorf("topic: %s", got)
	}
	if got := ids(Query{SinceWall: now.Add(-10 * time.Minute).UnixNano()}); got != "window.new,audio.new" {
		t.Errorf("since: %s", got)
	}
	if got := ids(Query{SinceWall: now.Add(-time.Hour).UnixNano(), Topic: "audio.*"}); got != "audio.mid,audio.new" {
		t.Errorf("both: %s", got)
	}
	if got := ids(Query{Limit: 2}); got != "window.new,audio.new" {
		t.Errorf("limit keeps most recent: %s", got)
	}
	if got := ids(Query{Topic: "nothing.*"}); got != "" {
		t.Errorf("non-match: %s", got)
	}
}

func TestEphemeralSkippedAndCapsHashed(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir, 0)
	eph := at(time.Now(), "ptr.move")
	eph.Ephemeral = true
	l.Append(eph)
	g := at(time.Now(), "grant.x")
	g.Caps = []string{"TOP-SECRET"}
	l.Append(g)
	l.Close()

	l2, err := Open(dir, 0) // reopen: index rebuilt, appends continue
	if err != nil {
		t.Fatal(err)
	}
	l2.Append(at(time.Now(), "after.reopen"))
	l2.Close()

	msgs, _ := ReadFile(dir)
	if len(msgs) != 2 || msgs[0].Topic != "grant.x" || msgs[1].Topic != "after.reopen" {
		t.Fatalf("msgs = %+v", msgs)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "seg-000001.jsonl"))
	if strings.Contains(string(raw), "TOP-SECRET") || !strings.Contains(string(raw), CapID("TOP-SECRET")) {
		t.Fatalf("log must hold the hash, not the value: %s", raw)
	}
	if CapID("TOP-SECRET") != CapID("TOP-SECRET") || CapID("a") == CapID("b") {
		t.Fatal("CapID must be stable and distinguishing")
	}
}
