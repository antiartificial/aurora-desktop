package dom

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const btrfsMagic = 0x9123683E

// OnBtrfs reports whether path lives on a btrfs filesystem.
func OnBtrfs(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	return int64(st.Type) == btrfsMagic
}

// btrfsBin finds the btrfs tool; /usr/sbin is often missing from an unprivileged PATH.
func btrfsBin() (string, error) {
	if p, err := exec.LookPath("btrfs"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/usr/sbin/btrfs", "/sbin/btrfs", "/usr/bin/btrfs"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("btrfs tool not found (install btrfs-progs)")
}

func btrfs(args ...string) error {
	bin, err := btrfsBin()
	if err != nil {
		return err
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("btrfs %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func subvolCreate(path string) error { return btrfs("subvolume", "create", path) }

// subvolSnapshot makes a writable snapshot of src at dst.
func subvolSnapshot(src, dst string) error { return btrfs("subvolume", "snapshot", src, dst) }

// removeTree deletes a directory or subvolume. Unprivileged users cannot
// `btrfs subvolume delete` a non-empty subvolume (EPERM without the
// user_subvol_rm_allowed mount option), but they can empty it and rmdir it,
// so we try the proper delete first and fall back to emptying.
func removeTree(path string, subvol bool) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	if subvol {
		_ = btrfs("subvolume", "delete", path) // best effort; may be EPERM
	}
	// make directories traversable/removable (a domain may have chmod'd them)
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	err := os.RemoveAll(path)
	if err != nil && rmInUserns(path) == nil {
		return os.RemoveAll(path) // the domain's own files are gone; this removes what the host user owns
	}
	return err
}
