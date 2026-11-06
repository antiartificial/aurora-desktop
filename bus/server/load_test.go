package server_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/antiartificial/aurora/bus/proto"
)

// End to end under load: a subscriber stops reading while 8000 background
// messages (about 8 MB, far more than socket buffers hold) and then 100
// user-interactive ones are published. In FIFO order the interactive messages
// would arrive last (positions 8000+). With per-subscriber priority queues
// they overtake the queued background backlog.
func TestQoSOrderingUnderLoad(t *testing.T) {
	const nBG, nUI = 8000, 100
	sock, _, _ := start(t)
	sub := dial(t, sock)
	if err := sub.Subscribe(ctx(t), "**"); err != nil {
		t.Fatal(err)
	}
	pub := dial(t, sock)
	pad := strings.Repeat("x", 1000)
	for i := 0; i < nBG; i++ {
		pub.Publish("noise.bg", json.RawMessage(fmt.Sprintf(`{"i":%d,"pad":%q}`, i, pad)), proto.QoSBackground, true)
	}
	for i := 0; i < nUI; i++ {
		pub.Publish("ui.click", json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)), proto.QoSInteractive, true)
	}
	// The daemon handles one connection's messages in order, so once this
	// returns every message above is queued for the (non-reading) subscriber.
	if err := pub.Flush(ctx(t)); err != nil {
		t.Fatal(err)
	}

	lastUI, uiSeen, uiOrdered, prev := -1, 0, true, -1
	for pos := 0; pos < nBG+nUI; pos++ {
		e := recv(t, sub.Events())
		if e.QoS == proto.QoSInteractive {
			var p struct{ I int }
			json.Unmarshal(e.Payload, &p)
			if p.I != prev+1 {
				uiOrdered = false
			}
			prev = p.I
			lastUI, uiSeen = pos, uiSeen+1
		}
	}
	if uiSeen != nUI {
		t.Fatalf("saw %d interactive messages, want %d", uiSeen, nUI)
	}
	if !uiOrdered {
		t.Fatal("interactive messages must stay FIFO among themselves")
	}
	// Anything that fit in socket and client buffers could not be overtaken.
	// Require that the last interactive message beat at least half the backlog.
	if lastUI >= nBG/2 {
		t.Fatalf("last interactive message at position %d of %d: it queued behind background traffic", lastUI, nBG+nUI)
	}
	t.Logf("last of %d interactive messages arrived at position %d of %d (FIFO would be %d)", nUI, lastUI, nBG+nUI, nBG+nUI-1)
}
