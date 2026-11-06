// Package supervisor starts, watches and restarts the Aurora stack's services
// from a declarative list. Adding a service is a new entry in the JSON file,
// not new code. See README-less design notes in policy.go and health.go.
package supervisor

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Dur is a time.Duration that reads as "500ms" / "30s" in JSON.
type Dur time.Duration

func (d *Dur) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"5s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Dur(v)
	return nil
}
func (d Dur) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
func (d Dur) D() time.Duration             { return time.Duration(d) }

// Policy is everything that says how a service is restarted. A service entry
// inherits the file's "defaults" and overrides what it names ("0s" is a real
// value: unhealthy_after "0s" turns the hang-kill off).
type Policy struct {
	BackoffInitial Dur  `json:"backoff_initial"` // first delay before a restart
	BackoffMax     Dur  `json:"backoff_max"`     // the delay doubles up to this
	StableAfter    Dur  `json:"stable_after"`    // healthy this long = the run was not a crash loop; resets backoff and the crash window
	CrashLimit     int  `json:"crash_limit"`     // this many unstable exits inside CrashWindow = give up
	CrashWindow    Dur  `json:"crash_window"`
	UnhealthyAfter Dur  `json:"unhealthy_after"` // kill and restart a process that has not been healthy this long (0 = report only)
	StopGrace      Dur  `json:"stop_grace"`      // SIGTERM, then SIGKILL after this
	RestartOnDep   bool `json:"restart_on_dep_restart"`
}

// DefaultPolicy is what an entry gets when neither it nor "defaults" says.
func DefaultPolicy() Policy {
	return Policy{
		BackoffInitial: Dur(500 * time.Millisecond), BackoffMax: Dur(30 * time.Second),
		StableAfter: Dur(10 * time.Second), CrashLimit: 5, CrashWindow: Dur(60 * time.Second),
		UnhealthyAfter: Dur(30 * time.Second), StopGrace: Dur(5 * time.Second), RestartOnDep: true,
	}
}

// Health says how "healthy" is judged. type:
//
//	bus      the process answers as the bus daemon: the socket's SO_PEERCRED pid is
//	         the supervised pid (a busd that failed to bind and kept running is NOT
//	         healthy) and bus.ping@v1 is answered.
//	service  Name is registered on the bus BY the supervised pid (bus.inspect@v1)
//	         and a request to it gets a reply (any reply, an error included).
//	none     only "the process exists"; reported as "unchecked", never "healthy".
type Health struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// Service is one entry in the file.
type Service struct {
	Policy
	Name      string            `json:"name"`
	Command   []string          `json:"command"` // ${VAR} is expanded from the supervisor's environment
	Env       map[string]string `json:"env,omitempty"`
	Needs     []string          `json:"needs,omitempty"`
	Health    Health            `json:"health"`
	Log       string            `json:"log,omitempty"`
	NoAutoRun bool              `json:"no_autostart,omitempty"` // wait for `auroractl start`
}

// File is the declarative service list.
type File struct {
	Services []*Service
}

// Load reads and validates a service file.
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse decodes and validates a service file.
func Parse(b []byte) (*File, error) {
	var raw struct {
		Defaults json.RawMessage   `json:"defaults"`
		Services []json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	def := DefaultPolicy()
	if len(raw.Defaults) > 0 {
		if err := strictUnmarshal(raw.Defaults, &def); err != nil {
			return nil, fmt.Errorf("defaults: %w", err)
		}
	}
	f := &File{}
	seen := map[string]bool{}
	for i, r := range raw.Services {
		s := &Service{Policy: def}
		if err := strictUnmarshal(r, s); err != nil {
			return nil, fmt.Errorf("service %d: %w", i, err)
		}
		if s.Name == "" || strings.ContainsAny(s.Name, " \t\n/") {
			return nil, fmt.Errorf("service %d: needs a name without spaces", i)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("service %q declared twice", s.Name)
		}
		seen[s.Name] = true
		if len(s.Command) == 0 {
			return nil, fmt.Errorf("service %q: no command", s.Name)
		}
		for j, a := range s.Command {
			s.Command[j] = os.ExpandEnv(a)
		}
		switch s.Health.Type {
		case "":
			s.Health.Type = "none"
		case "bus", "none":
		case "service":
			if s.Health.Name == "" {
				return nil, fmt.Errorf("service %q: health type service needs a name", s.Name)
			}
		default:
			return nil, fmt.Errorf("service %q: unknown health type %q", s.Name, s.Health.Type)
		}
		if s.CrashLimit < 1 || s.BackoffInitial <= 0 || s.BackoffMax < s.BackoffInitial || s.StopGrace <= 0 {
			return nil, fmt.Errorf("service %q: crash_limit>=1, 0<backoff_initial<=backoff_max, stop_grace>0 required", s.Name)
		}
		s.Log = os.ExpandEnv(s.Log)
		f.Services = append(f.Services, s)
	}
	if len(f.Services) == 0 {
		return nil, fmt.Errorf("no services declared")
	}
	if _, err := f.Order(); err != nil {
		return nil, err
	}
	return f, nil
}

func strictUnmarshal(b []byte, v any) error {
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// Order returns the services dependencies-first, or an error naming an unknown
// dependency or a cycle.
func (f *File) Order() ([]*Service, error) {
	by := map[string]*Service{}
	for _, s := range f.Services {
		by[s.Name] = s
	}
	var out []*Service
	state := map[string]int{}
	var visit func(s *Service, path []string) error
	visit = func(s *Service, path []string) error {
		switch state[s.Name] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("dependency cycle: %s -> %s", strings.Join(path, " -> "), s.Name)
		}
		state[s.Name] = 1
		for _, n := range s.Needs {
			d, ok := by[n]
			if !ok {
				return fmt.Errorf("service %q needs unknown service %q", s.Name, n)
			}
			if err := visit(d, append(path, s.Name)); err != nil {
				return err
			}
		}
		state[s.Name] = 2
		out = append(out, s)
		return nil
	}
	for _, s := range f.Services {
		if err := visit(s, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}
