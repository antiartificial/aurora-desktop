package server

import (
	"fmt"
	"testing"

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
