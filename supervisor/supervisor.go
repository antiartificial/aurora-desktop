package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type State string

const (
	Stopped  State = "stopped"
	Waiting  State = "waiting"  // wanted, but a dependency is not ready
	Backoff  State = "backoff"  // wanted, sleeping before a restart
	Starting State = "starting" // process exists, never been healthy yet
	Running  State = "running"
	Stopping State = "stopping"
	GaveUp   State = "gave_up"
)

// Config is the supervisor's own wiring.
type Config struct {
	BusSocket    string
	ChildrenFile string // "name pid" lines, so a wrapper can reap strays if we are SIGKILLed
	HealthEvery  time.Duration
	Logf         func(string, ...any)
}

type killReq struct {
	pid    int
	reason string
	counts bool // a crash for the restart policy (a hang), vs. something we did on purpose
}

type svc struct {
	spec       *Service
	deps       []*svc
	dependents []*svc

	desired bool
	gaveUp  bool
	state   State
	detail  string
	pid     int

	startedAt   time.Time
	spawns      int
	lastExit    string
	lastExitAt  time.Time
	nextReason  string
	health      string // pending | healthy | unhealthy | unchecked | -
	healthWhy   string
	healthSince time.Time
	unhealthy   time.Time // since when this process has not been healthy (start counts)
	bestStreak  time.Duration

	window      crashWindow
	consecutive int
	notBefore   time.Time

	killCh chan killReq
	wake   chan struct{}
}

type Supervisor struct {
	cfg   Config
	bus   *Bus
	mu    sync.Mutex
	svcs  map[string]*svc
	order []*svc
	ctx   context.Context
	stop  context.CancelFunc
	wg    sync.WaitGroup
	pings map[string]*pingState
}

// New builds a supervisor for f. Nothing runs until Run.
func New(f *File, cfg Config) (*Supervisor, error) {
	order, err := f.Order()
	if err != nil {
		return nil, err
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.HealthEvery <= 0 {
		cfg.HealthEvery = time.Second
	}
	s := &Supervisor{cfg: cfg, svcs: map[string]*svc{}, pings: map[string]*pingState{}}
	s.bus = NewBus(cfg.BusSocket, cfg.Logf)
	for _, sp := range order {
		v := &svc{spec: sp, desired: !sp.NoAutoRun, state: Stopped, health: "-", killCh: make(chan killReq, 1), wake: make(chan struct{}, 1)}
		for _, n := range sp.Needs {
			d := s.svcs[n]
			v.deps = append(v.deps, d)
			d.dependents = append(d.dependents, v)
		}
		s.svcs[sp.Name] = v
		s.order = append(s.order, v)
	}
	return s, nil
}

// Run starts every wanted service (dependencies first, each waiting for its
// dependencies to be healthy) and blocks until Shutdown.
func (s *Supervisor) Run(ctx context.Context) {
	s.ctx, s.stop = context.WithCancel(ctx)
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.bus.Pump(s.ctx) }()
	go func() { defer s.wg.Done(); s.healthLoop() }()
	for _, v := range s.order {
		v := v
		s.wg.Add(1)
		go func() { defer s.wg.Done(); s.run(v) }()
	}
	<-s.ctx.Done()
	s.wg.Wait()
}

// Shutdown stops everything, dependents before what they need, waits for each
// process to be gone, then ends Run. Safe to call once.
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	for _, v := range s.order {
		v.desired = false
	}
	s.mu.Unlock()
	for i := len(s.order) - 1; i >= 0; i-- {
		v := s.order[i]
		s.mu.Lock()
		s.requestKill(v, "supervisor shutting down", false)
		s.mu.Unlock()
		for {
			s.mu.Lock()
			gone := v.pid == 0
			s.mu.Unlock()
			if gone {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		s.bus.flush() // say so while the bus is still there
	}
	s.stop()
	s.wg.Wait()
}

func poke(v *svc) {
	select {
	case v.wake <- struct{}{}:
	default:
	}
}

// requestKill asks v's run loop to terminate its current process. s.mu held.
func (s *Supervisor) requestKill(v *svc, reason string, counts bool) {
	if v.pid == 0 {
		return
	}
	select {
	case v.killCh <- killReq{v.pid, reason, counts}:
	default: // one is already pending for this process
	}
}

func (s *Supervisor) blockedBy(v *svc) string {
	for _, d := range v.deps {
		switch {
		case d.gaveUp:
			return "needs " + d.spec.Name + ", which gave up"
		case !d.desired:
			return "needs " + d.spec.Name + ", which is stopped"
		case d.pid == 0 || (d.health != "healthy" && d.health != "unchecked"):
			return "waiting for " + d.spec.Name + " to be healthy"
		}
	}
	return ""
}

func (s *Supervisor) run(v *svc) {
	for {
		s.mu.Lock()
		var timer <-chan time.Time
		for {
			if s.ctx.Err() != nil {
				s.mu.Unlock()
				return
			}
			timer = nil
			if why := s.blockedBy(v); !v.desired {
				v.state, v.detail = Stopped, ""
			} else if v.gaveUp {
				v.state = GaveUp
			} else if why != "" {
				v.state, v.detail = Waiting, why
			} else if d := time.Until(v.notBefore); d > 0 {
				v.state, v.detail = Backoff, "restarting in "+d.Round(10*time.Millisecond).String()
				timer = time.After(d)
			} else {
				break
			}
			s.mu.Unlock()
			select {
			case <-v.wake:
			case <-timer:
			case <-s.ctx.Done():
				return
			}
			s.mu.Lock()
		}
		cmd, err := s.spawn(v)
		if err != nil {
			s.exited(v, "start failed: "+Clean(err.Error()), false, true)
			s.mu.Unlock()
			continue
		}
		pid := cmd.Process.Pid
		s.mu.Unlock()

		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var reason string
		kr := killReq{}
	wait:
		for {
			select {
			case err := <-done:
				reason = describeExit(err, cmd)
				break wait
			case k := <-v.killCh:
				if k.pid != pid {
					continue // aimed at a process that is already gone
				}
				kr = k
				s.mu.Lock()
				if v.state == Starting || v.state == Running {
					v.state, v.detail = Stopping, kr.reason
				}
				s.mu.Unlock()
				err := terminate(cmd, done, v.spec.StopGrace.D())
				reason = kr.reason
				if kr.counts {
					reason = kr.reason + " (" + describeExit(err, cmd) + ")"
				}
				break wait
			}
		}
		s.mu.Lock()
		s.exited(v, reason, kr.pid != 0, kr.counts)
		s.mu.Unlock()
	}
}

func describeExit(err error, cmd *exec.Cmd) string {
	ps := cmd.ProcessState
	if ps == nil {
		return fmt.Sprint(err)
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return "killed by " + sigName(ws.Signal())
	}
	return fmt.Sprintf("exit status %d", ps.ExitCode())
}

func terminate(cmd *exec.Cmd, done chan error, grace time.Duration) error {
	pid := cmd.Process.Pid
	syscall.Kill(pid, syscall.SIGTERM)
	select {
	case err := <-done:
		return err
	case <-time.After(grace):
		syscall.Kill(-pid, syscall.SIGKILL) // the whole group: it did not listen
		syscall.Kill(pid, syscall.SIGKILL)
		return <-done
	}
}

func (s *Supervisor) spawn(v *svc) (*exec.Cmd, error) {
	sp := v.spec
	cmd := exec.Command(sp.Command[0], sp.Command[1:]...)
	cmd.Env = os.Environ()
	for k, val := range sp.Env {
		cmd.Env = append(cmd.Env, k+"="+os.ExpandEnv(val))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if sp.Log != "" {
		f, err := os.OpenFile(sp.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		fmt.Fprintf(f, "--- supervisord: start #%d at %s ---\n", v.spawns+1, time.Now().Format(time.RFC3339))
		cmd.Stdout, cmd.Stderr = f, f
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	now := time.Now()
	v.spawns++
	v.pid, v.startedAt, v.detail = cmd.Process.Pid, now, ""
	v.healthWhy, v.bestStreak, v.healthSince = "", 0, now
	v.unhealthy = now
	if sp.Health.Type == "none" {
		v.state, v.health = Running, "unchecked"
	} else {
		v.state, v.health = Starting, "pending"
	}
	s.writeChildren()
	body := map[string]any{"service": sp.Name, "pid": v.pid, "restarts": v.spawns - 1}
	if v.spawns == 1 {
		s.bus.Emit(TopicStarted, body)
	} else {
		body["because"] = v.nextReason
		s.bus.Emit(TopicRestarted, body)
	}
	s.cfg.Logf("%s: started pid %d (start #%d)", sp.Name, v.pid, v.spawns)
	v.nextReason = ""
	if sp.Health.Type == "none" {
		s.wakeDependents(v)
	}
	return cmd, nil
}

func (s *Supervisor) wakeDependents(v *svc) {
	for _, d := range v.dependents {
		poke(d)
	}
}

// exited records the end of v's process. s.mu held. byUs: the supervisor asked
// for it. counts: it is nevertheless a crash for the restart policy (hang kill).
func (s *Supervisor) exited(v *svc, reason string, byUs, counts bool) {
	now := time.Now()
	ran := time.Duration(0)
	if v.pid != 0 {
		ran = now.Sub(v.startedAt)
	}
	if v.health == "healthy" {
		if st := now.Sub(v.healthSince); st > v.bestStreak {
			v.bestStreak = st
		}
	}
	if v.spec.Health.Type == "none" && ran > v.bestStreak {
		v.bestStreak = ran
	}
	stable := v.bestStreak >= v.spec.StableAfter.D()
	v.pid, v.health, v.healthWhy = 0, "-", ""
	v.lastExit, v.lastExitAt = reason, now
	s.writeChildren()
	s.cfg.Logf("%s: exited: %s (ran %s)", v.spec.Name, reason, ran.Round(time.Millisecond))
	s.bus.Emit(TopicExited, map[string]any{"service": v.spec.Name, "reason": reason, "expected": byUs && !counts, "ran_ms": ran.Milliseconds()})
	for _, d := range v.dependents {
		if d.spec.RestartOnDep && d.pid != 0 {
			s.requestKill(d, "restarted: needs "+v.spec.Name+", which exited", false)
		}
		poke(d)
	}
	poke(v)
	if byUs && !counts {
		v.nextReason = reason
		if !v.desired {
			v.state = Stopped
			s.bus.Emit(TopicStopped, map[string]any{"service": v.spec.Name, "reason": reason})
		}
		return
	}
	// Dying because a dependency just died is the dependency's story, not a crash.
	for _, d := range v.deps {
		if d.pid == 0 || !procAlive(d.pid) {
			v.nextReason = reason + " (needs " + d.spec.Name + ", which is down)"
			return
		}
	}
	if stable {
		v.window.reset()
		v.consecutive = 0
	}
	n := v.window.record(now, v.spec.CrashWindow.D())
	if n >= v.spec.CrashLimit {
		v.gaveUp, v.state = true, GaveUp
		v.detail = fmt.Sprintf("gave up: %d unstable exits in %s; last: %s", n, v.spec.CrashWindow.D(), reason)
		s.cfg.Logf("%s: %s", v.spec.Name, v.detail)
		s.bus.Emit(TopicGaveUp, map[string]any{"service": v.spec.Name, "exits": n, "window_ms": v.spec.CrashWindow.D().Milliseconds(), "last_reason": reason})
		return
	}
	d := v.spec.delay(v.consecutive)
	v.consecutive++
	v.notBefore = now.Add(d)
	v.nextReason = reason
}

func procAlive(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(b), ')')
	return i < 0 || i+2 >= len(b) || b[i+2] != 'Z'
}

func (s *Supervisor) writeChildren() {
	if s.cfg.ChildrenFile == "" {
		return
	}
	var b strings.Builder
	for _, v := range s.order {
		if v.pid != 0 {
			fmt.Fprintf(&b, "%s %d\n", v.spec.Name, v.pid)
		}
	}
	tmp := s.cfg.ChildrenFile + ".tmp"
	if os.WriteFile(tmp, []byte(b.String()), 0o600) == nil {
		os.Rename(tmp, s.cfg.ChildrenFile)
	}
}

// ---------------------------------------------------------------- health

func (s *Supervisor) healthLoop() {
	t := time.NewTicker(s.cfg.HealthEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		var in []probeInput
		s.mu.Lock()
		for _, v := range s.order {
			if v.pid != 0 && (v.state == Starting || v.state == Running) && v.spec.Health.Type != "none" {
				in = append(in, probeInput{v, v.pid})
			}
		}
		s.mu.Unlock()
		if len(in) == 0 {
			continue
		}
		res := s.probeAll(in, s.pings)
		for _, p := range in {
			if r, ok := res[p.v.spec.Name]; ok {
				s.applyHealth(p.v, p.pid, r)
			}
		}
	}
}

func (s *Supervisor) applyHealth(v *svc, pid int, r verdict) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.pid != pid || (v.state != Starting && v.state != Running) {
		return
	}
	now := time.Now()
	prev := v.health
	v.healthWhy = r.reason
	if r.ok {
		if prev != "healthy" {
			v.health, v.healthSince, v.unhealthy, v.state = "healthy", now, time.Time{}, Running
			s.cfg.Logf("%s: healthy (%s)", v.spec.Name, r.reason)
			s.bus.Emit(TopicHealth, map[string]any{"service": v.spec.Name, "healthy": true, "was": prev, "detail": r.reason})
			s.wakeDependents(v)
		}
		return
	}
	if prev == "healthy" {
		if st := now.Sub(v.healthSince); st > v.bestStreak {
			v.bestStreak = st
		}
		v.health, v.unhealthy = "unhealthy", now
		s.cfg.Logf("%s: UNHEALTHY: %s", v.spec.Name, r.reason)
		s.bus.Emit(TopicHealth, map[string]any{"service": v.spec.Name, "healthy": false, "was": prev, "detail": r.reason})
	} else if prev == "pending" {
		v.health = "unhealthy" // started but not yet answering; reason says why
	}
	if ua := v.spec.UnhealthyAfter.D(); ua > 0 && now.Sub(v.unhealthy) >= ua {
		s.requestKill(v, fmt.Sprintf("unhealthy for %s: %s", ua, r.reason), true)
	}
}

// ---------------------------------------------------------------- control

// Status is the read-only view of one service.
type Status struct {
	Name         string   `json:"name"`
	State        State    `json:"state"`
	Health       string   `json:"health"`
	HealthReason string   `json:"health_reason,omitempty"`
	PID          int      `json:"pid"`
	UptimeSec    float64  `json:"uptime_sec"`
	Restarts     int      `json:"restarts"`
	LastExit     string   `json:"last_exit,omitempty"`
	LastExitAt   string   `json:"last_exit_at,omitempty"`
	Detail       string   `json:"detail,omitempty"`
	Needs        []string `json:"needs,omitempty"`
}

func (s *Supervisor) Status() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Status
	for _, v := range s.order {
		st := Status{Name: v.spec.Name, State: v.state, Health: v.health, HealthReason: v.healthWhy, PID: v.pid, Detail: v.detail, Needs: v.spec.Needs, LastExit: v.lastExit}
		if v.spawns > 0 {
			st.Restarts = v.spawns - 1
		}
		if v.pid != 0 {
			st.UptimeSec = time.Since(v.startedAt).Seconds()
		}
		if !v.lastExitAt.IsZero() {
			st.LastExitAt = v.lastExitAt.UTC().Format(time.RFC3339)
		}
		out = append(out, st)
	}
	return out
}

func (s *Supervisor) lookup(name string) (*svc, error) {
	v := s.svcs[name]
	if v == nil {
		names := make([]string, 0, len(s.svcs))
		for n := range s.svcs {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("no service %q (have: %s)", Clean(name), strings.Join(names, ", "))
	}
	return v, nil
}

func (v *svc) clear() {
	v.gaveUp, v.consecutive, v.notBefore = false, 0, time.Time{}
	v.window.reset()
}

// Start wants name and everything it needs, and clears any give-up.
func (s *Supervisor) Start(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.lookup(name)
	if err != nil {
		return "", err
	}
	var started []string
	var want func(*svc)
	want = func(x *svc) {
		for _, d := range x.deps {
			want(d)
		}
		if !x.desired || x.gaveUp {
			started = append(started, x.spec.Name)
		}
		x.desired = true
		x.clear()
		poke(x)
	}
	want(v)
	if len(started) == 0 {
		return name + " is already wanted and running (or starting)", nil
	}
	return "starting " + strings.Join(started, ", "), nil
}

// Stop stops name and everything that needs it.
func (s *Supervisor) Stop(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.lookup(name)
	if err != nil {
		return "", err
	}
	var stopped []string
	var down func(*svc)
	down = func(x *svc) {
		for _, d := range x.dependents {
			down(d)
		}
		if x.desired {
			stopped = append(stopped, x.spec.Name)
		}
		x.desired = false
		s.requestKill(x, "stopped by request", false)
		poke(x)
	}
	down(v)
	if len(stopped) == 0 {
		return name + " is already stopped", nil
	}
	return "stopping " + strings.Join(stopped, ", "), nil
}

// BusDependents names the services that will bounce when v restarts.
func (s *Supervisor) restartWillBounce(v *svc) []string {
	var out []string
	var walk func(*svc)
	walk = func(x *svc) {
		for _, d := range x.dependents {
			if d.spec.RestartOnDep {
				out = append(out, d.spec.Name)
				walk(d)
			}
		}
	}
	walk(v)
	return out
}

// Restart bounces name now (no backoff) and clears any give-up. A restart of
// the bus also bounces everything that needs it; unless force is set it is
// refused while clients the supervisor did not start are connected to that bus,
// because they would be orphaned: busd forgets every connection and every
// domain socket, and only the supervised services get restarted.
func (s *Supervisor) Restart(name string, force bool) (string, error) {
	s.mu.Lock()
	v, err := s.lookup(name)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if v.spec.Health.Type == "bus" && !force {
		orphans, err := s.unsupervisedBusClients()
		if err != nil {
			return "", fmt.Errorf("refusing to restart %s: cannot tell who is connected to it (%v); --force to restart anyway", name, err)
		}
		if len(orphans) > 0 {
			return "", fmt.Errorf("refusing to restart %s: %d bus client(s) not started by this supervisor would lose their connection and stay disconnected: %s. Stop them, or use --force", name, len(orphans), strings.Join(orphans, ", "))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	was := v.pid != 0
	v.desired = true
	v.clear()
	msg := "starting " + name
	if was {
		s.requestKill(v, "restart requested", false)
		msg = "restarting " + name
		if b := s.restartWillBounce(v); len(b) > 0 {
			msg += " (and, because they need it: " + strings.Join(b, ", ") + ")"
		}
	}
	poke(v)
	return msg, nil
}

func (s *Supervisor) unsupervisedBusClients() ([]string, error) {
	cli, err := s.bus.Client()
	if err != nil {
		return nil, err
	}
	return listForeignClients(cli, s.childPIDs())
}

func (s *Supervisor) childPIDs() map[int]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := map[int]bool{os.Getpid(): true}
	for _, v := range s.order {
		if v.pid != 0 {
			m[v.pid] = true
		}
	}
	return m
}

func sigName(s syscall.Signal) string {
	names := map[syscall.Signal]string{syscall.SIGKILL: "SIGKILL", syscall.SIGTERM: "SIGTERM", syscall.SIGSEGV: "SIGSEGV",
		syscall.SIGABRT: "SIGABRT", syscall.SIGINT: "SIGINT", syscall.SIGHUP: "SIGHUP", syscall.SIGPIPE: "SIGPIPE", syscall.SIGBUS: "SIGBUS"}
	if n, ok := names[s]; ok {
		return n
	}
	return fmt.Sprintf("signal %d", int(s))
}
