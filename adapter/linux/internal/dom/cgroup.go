package dom

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LimitResult reports whether one controller's limit is actually enforced.
type LimitResult struct {
	Controller string
	Applied    bool
	Detail     string
}

// ParseSize parses "512M", "2G", "100K" or plain bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := int64(1)
	for suf, m := range map[string]int64{"K": 1 << 10, "M": 1 << 20, "G": 1 << 30, "T": 1 << 40} {
		if strings.HasSuffix(s, suf) {
			mult = m
			s = strings.TrimSuffix(s, suf)
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("bad size")
	}
	return n * mult, nil
}

// delegatedBase finds the cgroup systemd delegated to this user (user@UID.service).
func delegatedBase() (string, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	var path string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "0::") {
			path = strings.TrimPrefix(l, "0::")
		}
	}
	if path == "" {
		return "", fmt.Errorf("cgroup v2 not in use")
	}
	want := fmt.Sprintf("user@%d.service", os.Getuid())
	acc := "/sys/fs/cgroup"
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		acc = filepath.Join(acc, part)
		if part == want {
			return acc, nil
		}
	}
	return "", fmt.Errorf("no delegated cgroup (%s not in %s); not in a systemd user session", want, path)
}

// CgroupDir is where the domain's cgroup lives (it may not exist yet).
func CgroupDir(name string) (string, error) {
	base, err := delegatedBase()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "aurora-domains", name), nil
}

func removeCgroup(name string) {
	if d, err := CgroupDir(name); err == nil {
		_ = os.Remove(d)
		_ = os.Remove(filepath.Dir(d)) // only succeeds when no domain cgroups remain
	}
}

func enable(dir, ctrl string) error {
	cur, _ := os.ReadFile(filepath.Join(dir, "cgroup.subtree_control"))
	for _, c := range strings.Fields(string(cur)) {
		if c == ctrl {
			return nil
		}
	}
	return os.WriteFile(filepath.Join(dir, "cgroup.subtree_control"), []byte("+"+ctrl), 0o644)
}

// ApplyLimits creates the domain's cgroup and writes whatever limits the host
// lets this user enforce. Each requested limit gets an honest result.
func ApplyLimits(name string, l Limits) []LimitResult {
	type want struct {
		ctrl, file, val string
		set             bool
	}
	cpu := "max"
	if l.CPUPercent > 0 {
		cpu = fmt.Sprintf("%d 100000", l.CPUPercent*1000)
	}
	mem, pids := "max", "max"
	if l.MemoryBytes > 0 {
		mem = strconv.FormatInt(l.MemoryBytes, 10)
	}
	if l.Pids > 0 {
		pids = strconv.Itoa(l.Pids)
	}
	wants := []want{
		{"memory", "memory.max", mem, l.MemoryBytes > 0},
		{"pids", "pids.max", pids, l.Pids > 0},
		{"cpu", "cpu.max", cpu, l.CPUPercent > 0},
	}
	base, err := delegatedBase()
	var res []LimitResult
	fail := func(w want, why string) {
		res = append(res, LimitResult{w.ctrl, false, why})
	}
	if err != nil {
		for _, w := range wants {
			if w.set {
				fail(w, err.Error())
			}
		}
		return res
	}
	parent := filepath.Join(base, "aurora-domains")
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		for _, w := range wants {
			if w.set {
				fail(w, "cannot create cgroup under delegated "+base+": "+err.Error())
			}
		}
		return res
	}
	for _, w := range wants {
		if !w.set {
			if _, err := os.Stat(filepath.Join(dir, w.file)); err == nil {
				_ = os.WriteFile(filepath.Join(dir, w.file), []byte(w.val), 0o644)
			}
			continue
		}
		if err := enable(base, w.ctrl); err != nil {
			fail(w, fmt.Sprintf("controller %q is not delegated to this user by systemd (needs Delegate=%s on user@%d.service): %v", w.ctrl, w.ctrl, os.Getuid(), err))
			continue
		}
		if err := enable(parent, w.ctrl); err != nil {
			fail(w, "cannot enable controller: "+err.Error())
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, w.file), []byte(w.val), 0o644); err != nil {
			fail(w, "cannot write "+w.file+": "+err.Error())
			continue
		}
		res = append(res, LimitResult{w.ctrl, true, w.file + " = " + w.val + " in " + dir})
	}
	return res
}
