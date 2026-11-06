package supervisor

import (
	"testing"
	"time"
)

func TestPingIntervalBacksOffAndIsCapped(t *testing.T) {
	for _, c := range []struct {
		streak int
		want   time.Duration
	}{
		{0, 10 * time.Second},
		{1, 20 * time.Second},
		{2, 40 * time.Second},
		{5, 5 * time.Minute}, // 320s would exceed the cap
		{99, 5 * time.Minute},
	} {
		if got := pingInterval(c.streak); got != c.want {
			t.Errorf("pingInterval(%d) = %v, want %v", c.streak, got, c.want)
		}
	}
}

// The saving is the point of the change, so measure it rather than assume it.
func TestBackoffCutsLogRecordsOverAnHour(t *testing.T) {
	count, streak := 0, 0
	for el := time.Duration(0); el < time.Hour; {
		el += pingInterval(streak)
		streak++
		count++
	}
	flat := int(time.Hour / pingEvery) // 360
	if count >= flat/10 {
		t.Errorf("an hour healthy: %d pings, flat 10s would be %d; wanted at least a 10x cut", count, flat)
	}
	t.Logf("one hour healthy: %d pings instead of %d", count, flat)
}
