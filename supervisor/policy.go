package supervisor

import "time"

// The restart policy, in one place so it can be read and tested.
//
//   - A process that exits is restarted whatever its exit code: these services
//     are meant to run forever, so even exit 0 is a failure.
//   - The delay before restart n is BackoffInitial * 2^n, capped at BackoffMax,
//     where n counts consecutive unstable runs. Doubling stops a fast crasher
//     from burning CPU and filling the log; the cap bounds how long a transient
//     fault (a port held for a minute) can keep a service down.
//   - A run is STABLE if the service was healthy for StableAfter (or, for
//     health type none, simply stayed up that long). A stable run wipes the
//     backoff and the crash window: it died of something that started later, not
//     of how it starts, so it earns a fast first retry.
//   - CrashLimit unstable exits inside CrashWindow = give up: state gave_up, an
//     event, no more spawns until a person runs `auroractl start/restart`. A
//     loop that cannot be fixed by retrying is a report, not a workload.
//   - Exits the supervisor caused (stop, restart, a dependency bouncing) are not
//     crashes: no backoff, no window entry.
//   - A process that is up but not healthy for UnhealthyAfter is killed and
//     counts as a crash: a hung indexd is as dead as an exited one.
func (p Policy) delay(consecutive int) time.Duration {
	d := p.BackoffInitial.D()
	for i := 0; i < consecutive && d < p.BackoffMax.D(); i++ {
		d *= 2
	}
	if d > p.BackoffMax.D() {
		d = p.BackoffMax.D()
	}
	return d
}

// crashWindow decides give-up. exits holds the unstable exits seen so far.
type crashWindow struct{ exits []time.Time }

// record adds an exit at now and reports how many fall inside the window.
func (w *crashWindow) record(now time.Time, window time.Duration) int {
	keep := w.exits[:0]
	for _, t := range w.exits {
		if now.Sub(t) < window {
			keep = append(keep, t)
		}
	}
	w.exits = append(keep, now)
	return len(w.exits)
}
func (w *crashWindow) reset() { w.exits = nil }
