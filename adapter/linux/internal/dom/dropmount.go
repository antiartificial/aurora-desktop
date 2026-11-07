package dom

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

// Mounting one file or directory into a domain that is ALREADY RUNNING.
//
// Every other grant is a bwrap argument, so it exists only for the next `domain run`. A drag and drop
// (decision 0020) cannot wait for the next run: the user drops a file on a window that is open now.
// The mechanism, in the order the helper does it:
//
//  1. Enter the domain's USER namespace (nsenter), as the owner of it. Only the user who started the
//     domain has capabilities there; the host's namespace is untouched.
//  2. Make a private copy of the host's mount namespace (still owned by the domain's user namespace),
//     so the file can be named by its host path, and clone ONLY that file's mount from it
//     (open_tree OPEN_TREE_CLONE, a detached mount). Binding straight from the host's own namespace is
//     refused by the kernel; a clone made inside a namespace the domain's user owns is allowed.
//  3. Make that detached mount read-only (and nosuid, nodev) BEFORE it is attached, so there is no
//     moment at which the domain holds it writable.
//  4. setns into the domain's MOUNT namespace and move_mount the detached mount onto a placeholder
//     under /run/aurora/drops. That directory is the domain's own bus-socket directory (SockDir),
//     bound into every domain, which is how the placeholder gets there without entering anything.
//
// It only works when the domain's mount namespace is owned by its user namespace: that is the
// per-domain-uid mode (uid.go), where `unshare` makes the user namespace and bwrap runs as its root.
// In the fallback (no /etc/subuid range) bwrap makes a second, nested user namespace and the mount
// namespace belongs to the OUTER one, which the helper cannot enter without also leaving the
// payload's. That mode is refused with a reason rather than papered over.
//
// Revoking is unmounting; the mount also dies with the domain's run, since it lives in its namespace.

// DropsGuest is where drops appear inside a domain.
const DropsGuest = "/run/aurora/drops"

// DropCmd is the argv[0] of the in-process helper (this binary re-run by nsenter, see RunDropHelper).
const DropCmd = "__dropmount"

// Mount is one drop as the domain sees it.
type Mount struct {
	ID    string // "1", "2", ... per domain, never reused while the placeholder exists
	Host  string // the host path that was mounted (canonical)
	Guest string // where the domain sees it: /run/aurora/drops/<id>/<name>
	Mode  string // "ro" or "rw"
	Dir   bool
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// DropName is the file name a dropped path gets inside the domain: the base name with anything outside
// [A-Za-z0-9._-] replaced, never empty, never starting with a dot, at most 100 bytes. It never carries
// the host directory.
func DropName(p string) string {
	n := unsafeName.ReplaceAllString(filepath.Base(p), "_")
	n = strings.TrimLeft(n, ".")
	if len(n) > 100 {
		n = n[:100]
	}
	if n == "" || n == "_" {
		n = "dropped"
	}
	return n
}

// DropsDir is the host side of DropsGuest for one domain.
func DropsDir(name string) string { return filepath.Join(SockDir(name), "drops") }

// helperExe is the binary nsenter runs as the helper. The domain command is its own helper; tests
// that link this package point it at the test binary (which answers DropCmd in TestMain).
var helperExe = os.Executable

// liveRun returns the init pid of the domain's live run, refusing a stale record (as NSID does).
func (s Store) liveRun(name string) (pid int32, mntIno uint64, err error) {
	m, err := s.Load(name)
	if err != nil {
		return 0, 0, err
	}
	r := m.NS
	if r == nil {
		return 0, 0, fmt.Errorf("domain %s has not been run: there is nothing to mount into", name)
	}
	st, err := procStart(r.PID)
	if err != nil || st != r.Start {
		return 0, 0, fmt.Errorf("domain %s has no live run (last recorded init pid %d is gone)", name, r.PID)
	}
	now, err := readNSIDs(r.PID)
	if err != nil || now["mnt"] != r.IDs["mnt"] {
		return 0, 0, fmt.Errorf("domain %s has no live run (init pid %d no longer has the recorded mount namespace)", name, r.PID)
	}
	return r.PID, now["mnt"], nil
}

const nsGetUserns = 0xb701 // NS_GET_USERNS

func ino(fd int) (uint64, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return 0, err
	}
	return st.Ino, nil
}

// ownsMountNS reports whether pid's mount namespace is owned by the very user namespace pid is in.
// That is the one shape the helper can enter (see above).
func ownsMountNS(pid int32) error {
	p := "/proc/" + strconv.Itoa(int(pid)) + "/ns/"
	mnt, err := syscall.Open(p+"mnt", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(mnt)
	own, _, en := syscall.Syscall(syscall.SYS_IOCTL, uintptr(mnt), nsGetUserns, 0)
	if en != 0 {
		return fmt.Errorf("cannot find the owner of the mount namespace: %v", en)
	}
	defer syscall.Close(int(own))
	user, err := syscall.Open(p+"user", syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(user)
	a, err1 := ino(int(own))
	b, err2 := ino(user)
	if err1 != nil || err2 != nil {
		return errors.New("cannot compare user namespaces")
	}
	if a != b {
		return errors.New("this domain runs without its own uid (no usable /etc/subuid range), so its mount namespace belongs to an outer user namespace that cannot be entered; mounting into a running domain needs per-domain uids (adapter/linux/README.md, 'Per-domain uid')")
	}
	return nil
}

// MountDrop mounts the host path into the running domain, read-only unless mode is "rw".
//
// The path must be absolute, clean and already canonical (the caller resolved symlinks and authorised
// the result), and a regular file or directory the calling user can open. Nothing but that exact inode
// is mounted: the helper re-opens it with RESOLVE_NO_SYMLINKS and compares device and inode, so a
// symlink swapped in between is refused, not followed.
func (s Store) MountDrop(name, path, mode string) (Mount, error) {
	var zero Mount
	if mode != "ro" && mode != "rw" {
		return zero, errors.New("mode must be ro or rw")
	}
	pid, mntIno, err := s.liveRun(name)
	if err != nil {
		return zero, err
	}
	if err := ownsMountNS(pid); err != nil {
		return zero, err
	}
	host, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return zero, fmt.Errorf("cannot mount %s: not an absolute clean path that exists (%v)", path, err)
	}
	if host != path {
		return zero, fmt.Errorf("cannot mount %s exactly: it resolves to %s (the caller must resolve symlinks and authorise the result)", path, host)
	}
	// Look before opening: opening a FIFO for reading blocks forever, and the drop must not hang
	// whoever asked. O_NONBLOCK|O_NOFOLLOW covers a swap between this check and the open.
	if lst, err := os.Lstat(host); err != nil {
		return zero, fmt.Errorf("cannot mount %s: %v", host, err)
	} else if !lst.IsDir() && !lst.Mode().IsRegular() {
		return zero, fmt.Errorf("cannot mount %s: not a regular file or directory (%s)", host, lst.Mode().Type())
	}
	fd, err := syscall.Open(host, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return zero, fmt.Errorf("cannot mount %s: %v", host, err)
	}
	f := os.NewFile(uintptr(fd), host)
	st, err := f.Stat()
	f.Close()
	if err != nil {
		return zero, err
	}
	dir := st.IsDir()
	if !dir && !st.Mode().IsRegular() {
		return zero, fmt.Errorf("cannot mount %s: not a regular file or directory (%s)", host, st.Mode().Type())
	}
	sys := st.Sys().(*syscall.Stat_t)

	id, hostDir, err := newDropDir(name)
	if err != nil {
		return zero, err
	}
	leaf := filepath.Join(hostDir, DropName(host))
	if dir {
		err = os.Mkdir(leaf, 0o700)
	} else {
		var pf *os.File
		if pf, err = os.OpenFile(leaf, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
			pf.Close()
		}
	}
	if err != nil {
		os.RemoveAll(hostDir)
		return zero, err
	}
	guest := filepath.Join(DropsGuest, id, DropName(host))
	args := []string{DropCmd, "mount", "/proc/" + strconv.Itoa(int(pid)) + "/ns/mnt", strconv.FormatUint(mntIno, 10),
		host, strconv.FormatUint(uint64(sys.Dev), 10), strconv.FormatUint(sys.Ino, 10), guest, mode}
	if err := runHelper(pid, args); err != nil {
		os.RemoveAll(hostDir)
		return zero, err
	}
	return Mount{ID: id, Host: host, Guest: guest, Mode: mode, Dir: dir}, nil
}

// UnmountDrop removes one drop: the mount first (the domain loses it at once, even a process that still
// holds the path open sees an empty placeholder afterwards), then the placeholder. A domain that is no
// longer running has no mount left; the placeholder is still removed.
func (s Store) UnmountDrop(name, id string) error {
	if _, err := strconv.ParseUint(id, 10, 32); err != nil {
		return fmt.Errorf("bad drop id %q", id)
	}
	hostDir := filepath.Join(DropsDir(name), id)
	ents, err := os.ReadDir(hostDir)
	if err != nil {
		return fmt.Errorf("no drop %s in domain %s", id, name)
	}
	if pid, mntIno, err := s.liveRun(name); err == nil {
		for _, e := range ents {
			args := []string{DropCmd, "umount", "/proc/" + strconv.Itoa(int(pid)) + "/ns/mnt", strconv.FormatUint(mntIno, 10), filepath.Join(DropsGuest, id, e.Name())}
			if err := runHelper(pid, args); err != nil {
				return err // do not delete a placeholder that may still have a mount on it
			}
		}
	}
	return os.RemoveAll(hostDir)
}

// newDropDir creates drops/<n> for the next free n.
func newDropDir(name string) (id, dir string, err error) {
	root := DropsDir(name)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", "", err
	}
	for n := 1; n < 1<<20; n++ {
		d := filepath.Join(root, strconv.Itoa(n))
		switch err := os.Mkdir(d, 0o700); {
		case err == nil:
			return strconv.Itoa(n), d, nil
		case !os.IsExist(err):
			return "", "", err
		}
	}
	return "", "", errors.New("too many drops")
}

func runHelper(pid int32, args []string) error {
	exe, err := helperExe()
	if err != nil {
		return err
	}
	// --preserve-credentials: the host user is root inside the domain's user namespace (uid.go maps
	// 0 to the real uid), so keeping our credentials IS being its root, and capabilities survive exec.
	argv := append([]string{"-t", strconv.Itoa(int(pid)), "-U", "--preserve-credentials", "--", exe}, args...)
	cmd := exec.Command("nsenter", argv...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %v: %s", args[1], err, strings.TrimSpace(out.String()))
	}
	return nil
}

// RunDropHelper is the helper's main: `__dropmount mount|umount ...` (run inside the domain's user
// namespace by nsenter). It returns the exit status.
func RunDropHelper(args []string) int {
	if err := runDropHelper(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func ensureUnderDrops(guest string) error {
	if filepath.Clean(guest) != guest || !strings.HasPrefix(guest, DropsGuest+"/") || strings.Contains(guest, "..") {
		return fmt.Errorf("refusing target %q: not under %s", guest, DropsGuest)
	}
	return nil
}
