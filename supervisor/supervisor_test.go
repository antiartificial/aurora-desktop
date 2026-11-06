package supervisor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDelayDoublesAndCaps(t *testing.T) {
	p := DefaultPolicy()
	p.BackoffInitial, p.BackoffMax = Dur(100*time.Millisecond), Dur(500*time.Millisecond)
	var got []time.Duration
	for i := 0; i < 6; i++ {
		got = append(got, p.delay(i))
	}
	want := []time.Duration{100, 200, 400, 500, 500, 500}
	for i := range want {
		if got[i] != want[i]*time.Millisecond {
			t.Fatalf("delay(%d) = %s, want %s (all: %v)", i, got[i], want[i]*time.Millisecond, got)
		}
	}
}

func TestCrashWindowForgetsOldExits(t *testing.T) {
	var w crashWindow
	t0 := time.Now()
	for i := 0; i < 4; i++ {
		w.record(t0.Add(time.Duration(i)*time.Second), 10*time.Second)
	}
	if n := w.record(t0.Add(30*time.Second), 10*time.Second); n != 1 {
		t.Fatalf("exits older than the window must not count: got %d", n)
	}
}

func TestParseRejectsBadFiles(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"cycle":   {`{"services":[{"name":"a","command":["x"],"needs":["b"]},{"name":"b","command":["x"],"needs":["a"]}]}`, "cycle"},
		"unknown": {`{"services":[{"name":"a","command":["x"],"needs":["zz"]}]}`, "unknown service"},
		"dup":     {`{"services":[{"name":"a","command":["x"]},{"name":"a","command":["x"]}]}`, "twice"},
		"field":   {`{"services":[{"name":"a","command":["x"],"restart_foreber":true}]}`, "unknown field"},
		"health":  {`{"services":[{"name":"a","command":["x"],"health":{"type":"vibes"}}]}`, "health type"},
	} {
		if _, err := Parse([]byte(c.in)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want containing %q", name, err, c.want)
		}
	}
	f, err := Parse([]byte(`{"defaults":{"crash_limit":2},"services":[{"name":"b","command":["x"],"needs":["a"]},{"name":"a","command":["x"],"unhealthy_after":"0s"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	o, _ := f.Order()
	if o[0].Name != "a" || o[0].CrashLimit != 2 || o[0].UnhealthyAfter != 0 || f.Services[0].UnhealthyAfter == 0 {
		t.Fatalf("order/defaults wrong: %+v", o[0])
	}
}

// ---- real processes (health type none, so no bus is needed)

func start(t *testing.T, conf string) (*Supervisor, func()) {
	t.Helper()
	f, err := Parse([]byte(conf))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(f, Config{BusSocket: filepath.Join(t.TempDir(), "none.sock"), Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	go s.Run(context.Background())
	return s, s.Shutdown
}

func get(s *Supervisor, name string) Status {
	for _, st := range s.Status() {
		if st.Name == name {
			return st
		}
	}
	return Status{}
}

func until(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

const fast = `"backoff_initial":"20ms","backoff_max":"80ms","stable_after":"300ms","crash_window":"30s","stop_grace":"1s"`

func TestKillNineIsRestartedAndCounted(t *testing.T) {
	s, stop := start(t, fmt.Sprintf(`{"services":[{"name":"w","command":["sleep","60"],%s}]}`, fast))
	defer stop()
	until(t, "w running", 3*time.Second, func() bool { return get(s, "w").State == Running })
	pid := get(s, "w").PID
	syscall.Kill(pid, syscall.SIGKILL)
	until(t, "w back under a new pid", 3*time.Second, func() bool { g := get(s, "w"); return g.PID != 0 && g.PID != pid })
	g := get(s, "w")
	if g.Restarts != 1 || !strings.Contains(g.LastExit, "SIGKILL") {
		t.Fatalf("restart not recorded: %+v", g)
	}
}

func TestCrashLoopGivesUpThenStartRevives(t *testing.T) {
	s, stop := start(t, fmt.Sprintf(`{"services":[{"name":"c","command":["sh","-c","exit 3"],"crash_limit":4,%s}]}`, fast))
	defer stop()
	until(t, "c gave up", 5*time.Second, func() bool { return get(s, "c").State == GaveUp })
	g := get(s, "c")
	if g.Restarts != 3 || !strings.Contains(g.Detail, "4 unstable exits") || g.LastExit != "exit status 3" {
		t.Fatalf("give-up wrong (want 3 restarts = 4 runs): %+v", g)
	}
	time.Sleep(300 * time.Millisecond)
	if get(s, "c").Restarts != 3 {
		t.Fatal("kept spawning after giving up")
	}
	s.Start("c")
	until(t, "c spawned again by an explicit start", 3*time.Second, func() bool { return get(s, "c").Restarts > 3 })
}

func TestStableRunResetsTheCrashWindow(t *testing.T) {
	// lives 400ms (> stable_after 300ms) then dies, 6 times, with crash_limit 3: never gives up.
	s, stop := start(t, fmt.Sprintf(`{"services":[{"name":"s","command":["sleep","0.4"],"crash_limit":3,%s}]}`, fast))
	defer stop()
	until(t, "6 restarts", 10*time.Second, func() bool { return get(s, "s").Restarts >= 6 })
	if get(s, "s").State == GaveUp {
		t.Fatal("stable runs must not accumulate toward give-up")
	}
}

func TestDependencyBounceIsNotACrash(t *testing.T) {
	s, stop := start(t, fmt.Sprintf(`{"services":[{"name":"dep","command":["sleep","60"],%s},{"name":"user","command":["sleep","60"],"needs":["dep"],%s}]}`, fast, fast))
	defer stop()
	until(t, "both up", 3*time.Second, func() bool { return get(s, "dep").State == Running && get(s, "user").State == Running })
	dp, up := get(s, "dep").PID, get(s, "user").PID
	syscall.Kill(dp, syscall.SIGKILL)
	until(t, "both under new pids", 5*time.Second, func() bool {
		d, u := get(s, "dep"), get(s, "user")
		return d.PID != 0 && d.PID != dp && u.PID != 0 && u.PID != up
	})
	if _, err := s.Stop("dep"); err != nil {
		t.Fatal(err)
	}
	until(t, "stop dep stops its dependent too", 3*time.Second, func() bool { return get(s, "user").State == Stopped && get(s, "dep").State == Stopped })
	if _, err := s.Start("user"); err != nil {
		t.Fatal(err)
	}
	until(t, "start user brings dep up first", 3*time.Second, func() bool { return get(s, "user").State == Running && get(s, "dep").State == Running })
}

func TestShutdownLeavesNothing(t *testing.T) {
	s, stop := start(t, fmt.Sprintf(`{"services":[{"name":"a","command":["sleep","60"],%s},{"name":"b","command":["sleep","60"],"needs":["a"],%s}]}`, fast, fast))
	until(t, "up", 3*time.Second, func() bool { return get(s, "a").State == Running && get(s, "b").State == Running })
	pa, pb := get(s, "a").PID, get(s, "b").PID
	stop()
	for _, p := range []int{pa, pb} {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d", p)); err == nil {
			t.Fatalf("pid %d survived Shutdown", p)
		}
	}
}
