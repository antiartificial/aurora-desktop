package hlc

import (
	"sort"
	"sync"
	"testing"
)

func TestMonotonicAndSortable(t *testing.T) {
	c := New()
	var prev Timestamp
	var strs []string
	for i := 0; i < 10000; i++ {
		ts := c.Now()
		if i > 0 && !prev.Less(ts) {
			t.Fatalf("not strictly increasing: %v then %v", prev, ts)
		}
		prev = ts
		strs = append(strs, ts.String())
	}
	if !sort.StringsAreSorted(strs) {
		t.Fatal("string form does not sort in timestamp order")
	}
}

func TestSameWallTickUsesCounterAndClockGoingBackwardsStaysMonotonic(t *testing.T) {
	wall := int64(1000)
	c := NewWithSource(func() int64 { return wall })
	a, b := c.Now(), c.Now()
	wall = 500 // wall clock stepped backwards
	d := c.Now()
	if !(a.Less(b) && b.Less(d)) || d.Wall != 1000 {
		t.Fatalf("a=%v b=%v d=%v", a, b, d)
	}
	if a.String() >= b.String() || b.String() >= d.String() {
		t.Fatal("strings must sort with the counter")
	}
}

func TestParseRoundTripAndUpdate(t *testing.T) {
	ts := Timestamp{Wall: 1762400000123456789, Counter: 7}
	got, err := Parse(ts.String())
	if err != nil || got != ts {
		t.Fatalf("got %v err %v", got, err)
	}
	if _, err := Parse("garbage"); err == nil {
		t.Fatal("expected error")
	}
	c := NewWithSource(func() int64 { return 10 })
	if u := c.Update(Timestamp{Wall: 99, Counter: 3}); u != (Timestamp{99, 4}) {
		t.Fatalf("update = %v", u)
	}
	if n := c.Now(); !(Timestamp{99, 4}).Less(n) {
		t.Fatalf("Now after Update went backwards: %v", n)
	}
}

func TestConcurrentUnique(t *testing.T) {
	c := New()
	var mu sync.Mutex
	seen := map[string]bool{}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				s := c.Now().String()
				mu.Lock()
				if seen[s] {
					t.Errorf("duplicate %s", s)
				}
				seen[s] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
}

func TestCounterPastFormer32BitWrapStaysMonotonic(t *testing.T) {
	c := NewWithSource(func() int64 { return 1000 }) // wall stuck
	for _, start := range []uint64{1<<32 - 2, 1<<64 - 3} {
		c.last = Timestamp{Wall: 1000, Counter: start}
		prev := c.last
		for i := 0; i < 4 && prev.Counter < 1<<64-1; i++ {
			ts := c.Now()
			if !prev.Less(ts) || prev.String() >= ts.String() {
				t.Fatalf("not monotonic across %d: %v then %v (%s, %s)", start, prev, ts, prev, ts)
			}
			back, err := Parse(ts.String())
			if err != nil || back != ts {
				t.Fatalf("round trip %v: %v %v", ts, back, err)
			}
			prev = ts
		}
	}
	c.last = Timestamp{Wall: 1000, Counter: 1<<32 - 1}
	if u := c.Update(Timestamp{Wall: 1000, Counter: 1<<32 - 1}); u.Counter != 1<<32 {
		t.Fatalf("Update across 2^32: %v", u)
	}
}
