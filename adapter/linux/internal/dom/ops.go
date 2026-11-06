package dom

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Create makes a new domain. The returned note says which storage backend was used.
func (s Store) Create(name string) (*Meta, string, error) {
	if !ValidName(name) {
		return nil, "", fmt.Errorf("invalid domain name %q (use a-z 0-9 _ -, max 32)", name)
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, "", err
	}
	dir := s.Dir(name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		if os.IsExist(err) {
			return nil, "", fmt.Errorf("domain %q already exists", name)
		}
		return nil, "", err
	}
	m := &Meta{ID: newID(), Name: name, Created: time.Now().UTC(), Grants: []Grant{}}
	note := "storage: plain directories (data dir is not on btrfs; snapshots unavailable)"
	if OnBtrfs(dir) {
		if _, err := btrfsBin(); err == nil {
			m.Subvolumes = true
			note = "storage: btrfs subvolumes (config, data, cache); snapshots available"
		} else {
			note = "storage: plain directories (btrfs filesystem but btrfs tool missing; snapshots unavailable)"
		}
	}
	for _, sd := range Subdirs {
		p := filepath.Join(dir, sd)
		var err error
		if m.Subvolumes {
			err = subvolCreate(p)
		} else {
			err = os.Mkdir(p, 0o700)
		}
		if err == nil {
			err = os.Chmod(p, 0o700)
		}
		if err != nil {
			_ = s.removeAll(m, dir)
			return nil, "", err
		}
	}
	if err := s.Save(m); err != nil {
		_ = s.removeAll(m, dir)
		return nil, "", err
	}
	return m, note, nil
}

func (s Store) removeAll(m *Meta, dir string) error {
	if tags, _ := os.ReadDir(filepath.Join(dir, "snapshots")); tags != nil {
		for _, t := range tags {
			for _, sd := range Subdirs {
				if err := removeTree(filepath.Join(dir, "snapshots", t.Name(), sd), m.Subvolumes); err != nil {
					return err
				}
			}
		}
	}
	for _, sd := range Subdirs {
		if err := removeTree(filepath.Join(dir, sd), m.Subvolumes); err != nil {
			return err
		}
	}
	return os.RemoveAll(dir)
}

// Destroy removes a domain. With keepData the tree is renamed to <name>.kept-<time> instead of deleted.
// It returns the kept path (empty if deleted).
func (s Store) Destroy(name string, keepData bool) (string, error) {
	m, err := s.Load(name)
	if err != nil {
		return "", err
	}
	removeCgroup(name)
	_ = os.RemoveAll(SockDir(name))
	_ = os.Remove(filepath.Dir(SockDir(name))) // empty parents, if no other domain
	_ = os.Remove(filepath.Dir(filepath.Dir(SockDir(name))))
	if keepData {
		kept := s.Dir(name) + ".kept-" + time.Now().UTC().Format("20060102T150405")
		return kept, os.Rename(s.Dir(name), kept)
	}
	return "", s.removeAll(m, s.Dir(name))
}

// Snapshot snapshots all three subvolumes under the given tag (default: timestamp).
func (s Store) Snapshot(name, tag string) (string, error) {
	m, err := s.Load(name)
	if err != nil {
		return "", err
	}
	if !m.Subvolumes {
		return "", fmt.Errorf("domain %q is on plain directories; snapshots require btrfs (create the domain under a btrfs data dir)", name)
	}
	if tag == "" {
		tag = time.Now().UTC().Format("20060102t150405")
	}
	if !ValidName(tag) {
		return "", fmt.Errorf("invalid snapshot tag %q", tag)
	}
	td := filepath.Join(s.SnapRoot(name), tag)
	if err := os.MkdirAll(s.SnapRoot(name), 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(td, 0o700); err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("snapshot %q already exists", tag)
		}
		return "", err
	}
	for _, sd := range Subdirs {
		if err := subvolSnapshot(filepath.Join(s.Dir(name), sd), filepath.Join(td, sd)); err != nil {
			for _, d := range Subdirs {
				_ = removeTree(filepath.Join(td, d), true)
			}
			_ = os.Remove(td)
			return "", err
		}
	}
	return tag, nil
}

// Snapshots lists snapshot tags, oldest first.
func (s Store) Snapshots(name string) []string {
	ents, _ := os.ReadDir(s.SnapRoot(name))
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// Rollback replaces the live subvolumes with fresh snapshots of the named snapshot.
// The snapshot itself is kept, so you can roll back to it again.
func (s Store) Rollback(name, tag string) error {
	m, err := s.Load(name)
	if err != nil {
		return err
	}
	if !m.Subvolumes {
		return fmt.Errorf("domain %q is on plain directories; rollback requires btrfs", name)
	}
	if !ValidName(tag) {
		return fmt.Errorf("invalid snapshot tag %q", tag)
	}
	td := filepath.Join(s.SnapRoot(name), tag)
	for _, sd := range Subdirs {
		if _, err := os.Stat(filepath.Join(td, sd)); err != nil {
			return fmt.Errorf("no such snapshot %q for domain %q", tag, name)
		}
	}
	for _, sd := range Subdirs {
		live := filepath.Join(s.Dir(name), sd)
		if err := removeTree(live, true); err != nil {
			return err
		}
		if err := subvolSnapshot(filepath.Join(td, sd), live); err != nil {
			return err
		}
	}
	return nil
}

func canon(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	r, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return r, nil
}

// GrantPath grants exactly one file or directory. mode is "ro" or "rw".
// The path is made absolute and its symlinks are resolved here.
func (s Store) GrantPath(name, path, mode string) (string, error) {
	return s.grantPath(name, path, mode, false)
}

// GrantPathExact is GrantPath for a caller that already resolved the path and
// authorised that exact string. It binds the path as given, never re-resolves
// and so never silently substitutes another path: the path must be absolute,
// clean and already canonical, else the grant is refused (a symlink component
// changed since the caller resolved it).
func (s Store) GrantPathExact(name, path, mode string) (string, error) {
	return s.grantPath(name, path, mode, true)
}

func (s Store) grantPath(name, path, mode string, exact bool) (string, error) {
	m, err := s.Load(name)
	if err != nil {
		return "", err
	}
	if mode != "ro" && mode != "rw" {
		return "", fmt.Errorf("mode must be ro or rw")
	}
	var p string
	if exact {
		r, err := filepath.EvalSymlinks(path)
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || err != nil || r != path {
			return "", fmt.Errorf("cannot grant %s exactly: not an absolute canonical path that exists (resolves to %q, err %v)", path, r, err)
		}
		p = path
	} else if p, err = canon(path); err != nil {
		return "", fmt.Errorf("cannot grant %s: %w", path, err)
	}
	if p == "/" {
		return "", fmt.Errorf("refusing to grant /")
	}
	replaced := false
	for i, g := range m.Grants {
		if g.Kind == "path" && g.Path == p {
			m.Grants[i].Mode = mode
			replaced = true
		}
	}
	if !replaced {
		m.Grants = append(m.Grants, Grant{Kind: "path", Path: p, Mode: mode})
	}
	return p, s.Save(m)
}

// GrantNet records a network grant. Enforcement is coarse: any net grant
// shares the host network namespace; the host name is only recorded.
func (s Store) GrantNet(name, host string) error {
	m, err := s.Load(name)
	if err != nil {
		return err
	}
	if host == "" {
		return fmt.Errorf("empty host")
	}
	for _, g := range m.Grants {
		if g.Kind == "net" && g.Host == host {
			return nil
		}
	}
	m.Grants = append(m.Grants, Grant{Kind: "net", Host: host})
	return s.Save(m)
}

// Revoke removes a path grant (by path) or, with net, a net grant (by host).
func (s Store) Revoke(name, target string, net bool) error {
	m, err := s.Load(name)
	if err != nil {
		return err
	}
	key := target
	if !net {
		if p, err := canon(target); err == nil {
			key = p
		}
	}
	var keep []Grant
	found := false
	for _, g := range m.Grants {
		if (net && g.Kind == "net" && g.Host == key) || (!net && g.Kind == "path" && g.Path == key) {
			found = true
			continue
		}
		keep = append(keep, g)
	}
	if !found {
		return fmt.Errorf("no such grant: %s", target)
	}
	if keep == nil {
		keep = []Grant{}
	}
	m.Grants = keep
	return s.Save(m)
}

// SetLimits stores limits (non-negative fields replace; pass current values to keep).
func (s Store) SetLimits(name string, l Limits) error {
	m, err := s.Load(name)
	if err != nil {
		return err
	}
	m.Limits = l
	return s.Save(m)
}
