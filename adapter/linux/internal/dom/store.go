// Package dom implements the Linux backend of the kernel adapter's domain
// operations (contract 3): create/destroy, run, grant, snapshot, limits.
package dom

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Grant is one capability recorded for a domain. Kind is "path" or "net".
type Grant struct {
	Kind string `json:"kind"`
	Path string `json:"path,omitempty"`
	Host string `json:"host,omitempty"`
	Mode string `json:"mode,omitempty"` // "ro" or "rw" for path grants
}

// Limits are cgroup v2 resource budgets. Zero means unlimited.
type Limits struct {
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
	Pids        int   `json:"pids,omitempty"`
	CPUPercent  int   `json:"cpu_percent,omitempty"`
}

// Meta is the persisted record of a domain (domain.json).
type Meta struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Created    time.Time `json:"created"`
	Subvolumes bool      `json:"subvolumes"` // data/config/cache are btrfs subvolumes
	Grants     []Grant   `json:"grants"`
	Limits     Limits    `json:"limits"`
	NS         *NSRecord `json:"ns,omitempty"`  // namespaces of the latest run (see nsrun.go)
	UID        uint32    `json:"uid,omitempty"` // host-visible uid of the domain's processes (a subuid, see uid.go)
	GID        uint32    `json:"gid,omitempty"`
}

// Subdirs are the three trees every domain owns.
var Subdirs = []string{"config", "data", "cache"}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ValidName reports whether s is usable as a domain name or snapshot tag.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Store is a directory holding all domains.
type Store struct{ Root string }

// DefaultRoot returns $AURORA_DOMAINS_DIR or ${XDG_DATA_HOME:-~/.local/share}/aurora/domains.
func DefaultRoot() string {
	if d := os.Getenv("AURORA_DOMAINS_DIR"); d != "" {
		return d
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "aurora", "domains")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "aurora", "domains")
}

func (s Store) Dir(name string) string      { return filepath.Join(s.Root, name) }
func (s Store) SnapRoot(name string) string { return filepath.Join(s.Dir(name), "snapshots") }
func (s Store) metaPath(name string) string { return filepath.Join(s.Dir(name), "domain.json") }

// Load reads a domain's metadata.
func (s Store) Load(name string) (*Meta, error) {
	if !ValidName(name) {
		return nil, fmt.Errorf("invalid domain name %q (use a-z 0-9 _ -, max 32)", name)
	}
	b, err := os.ReadFile(s.metaPath(name))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("no such domain %q", name)
	}
	if err != nil {
		return nil, err
	}
	m := &Meta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("corrupt %s: %w", s.metaPath(name), err)
	}
	return m, nil
}

// Save atomically writes a domain's metadata.
func (s Store) Save(m *Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.metaPath(m.Name) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.metaPath(m.Name))
}

// List returns all domains sorted by name. Kept (destroyed --keep-data) dirs are skipped.
func (s Store) List() ([]*Meta, error) {
	ents, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Meta
	for _, e := range ents {
		if !e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		if m, err := s.Load(e.Name()); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ExecID is the attested identity string a server should associate with the domain.
func ExecID(m *Meta) string { return "domain:" + m.Name + ":" + m.ID }
