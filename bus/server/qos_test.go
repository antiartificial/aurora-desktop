package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/antiartificial/aurora/bus/proto"
)

// Unit level: with 1000 background messages already queued, every
// user-interactive message pushed afterwards pops first.
func TestOutboxPriority(t *testing.T) {
	o := newOutbox()
	for i := 0; i < 1000; i++ {
		o.push(proto.QoSBackground.Rank(), []byte(fmt.Sprintf("bg%d", i)))
	}
	for i := 0; i < 5; i++ {
		o.push(proto.QoSUtility.Rank(), []byte(fmt.Sprintf("ut%d", i)))
		o.push(proto.QoSInitiated.Rank(), []byte(fmt.Sprintf("ui%d", i)))
		o.push(proto.QoSInteractive.Rank(), []byte(fmt.Sprintf("ix%d", i)))
	}
	var order []string
	for i := 0; i < 15; i++ {
		l, _ := o.pop()
		order = append(order, string(l)[:2])
	}
	for i, want := range []string{"ix", "ui", "ut"} {
		for j := 0; j < 5; j++ {
			if got := order[i*5+j]; got != want {
				t.Fatalf("pop %d = %s, want %s (order %v)", i*5+j, got, want, order)
			}
		}
	}
	if l, _ := o.pop(); string(l) != "bg0" {
		t.Fatalf("background must stay FIFO, got %s", l)
	}
}

// Strict priority starves background while higher classes keep arriving; the
// stats must show it (queued count and a growing oldest age), without
// changing the policy.
func TestOutboxStatsExposeStarvation(t *testing.T) {
	o := newOutbox()
	o.push(proto.QoSBackground.Rank(), []byte("bg\n"))
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < 50; i++ {
		o.push(proto.QoSInteractive.Rank(), []byte("ui\n"))
		if line, _ := o.pop(); string(line) != "ui\n" {
			t.Fatalf("policy changed: got %q before interactive", line)
		}
	}
	cs, _ := o.stats()
	bg := cs[proto.QoSBackground.Rank()]
	if bg.Queued != 1 || bg.OldestAgeMS < 20 {
		t.Fatalf("background stats = %+v, want 1 queued aged >= 20ms", bg)
	}
	if cs[0].Queued != 0 || cs[0].OldestAgeMS != 0 {
		t.Fatalf("interactive stats = %+v, want empty", cs[0])
	}
}
