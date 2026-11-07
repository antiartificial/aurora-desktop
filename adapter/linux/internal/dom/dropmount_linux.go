package dom

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"
)

// The new mount API's syscall numbers are the same on every architecture (they come after the table
// was unified); setns is not, see setns_*.go.
const (
	sysOpenTree     = 428
	sysMoveMount    = 429
	sysOpenat2      = 437
	sysMountSetattr = 442

	cloneNewns          = 0x00020000
	openTreeClone       = 1
	openTreeCloexec     = 0x80000
	moveMountFEmptyPath = 4
	atEmptyPath         = 0x1000
	resolveNoSymlinks   = 0x04
	mountAttrRdonly     = 0x1
	mountAttrNosuid     = 0x2
	mountAttrNodev      = 0x4
	oPath               = 0x200000
	atFdcwd             = -100
)

type openHow struct{ flags, mode, resolve uint64 }
type mountAttr struct{ set, clr, prop, userns uint64 }

func cstr(s string) *byte {
	b, err := syscall.BytePtrFromString(s)
	if err != nil {
		return nil
	}
	return b
}

func cwd() uintptr { n := atFdcwd; return uintptr(n) } // a negative constant cannot be converted directly

func runDropHelper(args []string) error {
	if len(args) < 4 {
		return fmt.Errorf("usage: %s mount|umount ...", DropCmd)
	}
	// Namespace changes are per thread, and setns(mnt) refuses a thread that shares its fs with others.
	runtime.LockOSThread()
	if err := syscall.Unshare(syscall.CLONE_FS); err != nil {
		return fmt.Errorf("unshare fs: %v", err)
	}
	mntPath, wantIno := args[2], args[3]
	switch args[1] {
	case "mount":
		if len(args) != 9 {
			return fmt.Errorf("mount wants 9 arguments, got %d", len(args))
		}
		return doMount(mntPath, wantIno, args[4], args[5], args[6], args[7], args[8])
	case "umount":
		if len(args) != 5 {
			return fmt.Errorf("umount wants 5 arguments, got %d", len(args))
		}
		return doUmount(mntPath, wantIno, args[4])
	}
	return fmt.Errorf("unknown helper operation %q", args[1])
}

// enterMountNS joins the domain's mount namespace, after checking it is the one the caller named.
func enterMountNS(path, wantIno string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	want, _ := strconv.ParseUint(wantIno, 10, 64)
	got, err := ino(int(f.Fd()))
	if err != nil || got != want {
		return fmt.Errorf("the mount namespace at %s is not the one recorded for the domain (inode %d, want %s)", path, got, wantIno)
	}
	if _, _, en := syscall.Syscall(sysSetns, f.Fd(), cloneNewns, 0); en != 0 {
		return fmt.Errorf("setns mnt: %v", en)
	}
	return nil
}

func doMount(mntPath, wantIno, src, wantDev, wantSrcIno, guest, mode string) error {
	if err := ensureUnderDrops(guest); err != nil {
		return err
	}
	if mode != "ro" && mode != "rw" {
		return fmt.Errorf("mode %q", mode)
	}
	// A private copy of the host's mount namespace, owned by the domain's user namespace.
	if err := syscall.Unshare(cloneNewns); err != nil {
		return fmt.Errorf("unshare mnt: %v", err)
	}
	// Open the source with no symlink anywhere in its path, as an O_PATH handle, and insist it is the
	// inode the caller authorised.
	how := openHow{flags: oPath | syscall.O_CLOEXEC, resolve: resolveNoSymlinks}
	sp := cstr(src)
	if sp == nil {
		return fmt.Errorf("bad source path")
	}
	fd, _, en := syscall.Syscall6(sysOpenat2, cwd(), uintptr(unsafe.Pointer(sp)), uintptr(unsafe.Pointer(&how)), unsafe.Sizeof(how), 0, 0)
	if en != 0 {
		return fmt.Errorf("open %s without following symlinks: %v", src, en)
	}
	srcFd := int(fd)
	defer syscall.Close(srcFd)
	var st syscall.Stat_t
	if err := syscall.Fstat(srcFd, &st); err != nil {
		return err
	}
	wd, _ := strconv.ParseUint(wantDev, 10, 64)
	wi, _ := strconv.ParseUint(wantSrcIno, 10, 64)
	if uint64(st.Dev) != wd || st.Ino != wi {
		return fmt.Errorf("%s is no longer the file that was authorised (device/inode changed)", src)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG && st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return fmt.Errorf("%s is not a regular file or directory", src)
	}
	empty := cstr("")
	tree, _, en := syscall.Syscall6(sysOpenTree, uintptr(srcFd), uintptr(unsafe.Pointer(empty)), atEmptyPath|openTreeClone|openTreeCloexec, 0, 0, 0)
	if en != 0 {
		return fmt.Errorf("open_tree %s: %v", src, en)
	}
	defer syscall.Close(int(tree))
	attr := mountAttr{set: mountAttrNosuid | mountAttrNodev}
	if mode == "ro" {
		attr.set |= mountAttrRdonly
	}
	if _, _, en := syscall.Syscall6(sysMountSetattr, tree, uintptr(unsafe.Pointer(empty)), atEmptyPath, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0); en != 0 {
		return fmt.Errorf("mount_setattr: %v", en)
	}
	if err := enterMountNS(mntPath, wantIno); err != nil {
		return err
	}
	gp := cstr(guest)
	if gp == nil {
		return fmt.Errorf("bad target")
	}
	if _, _, en := syscall.Syscall6(sysMoveMount, tree, uintptr(unsafe.Pointer(empty)), cwd(), uintptr(unsafe.Pointer(gp)), moveMountFEmptyPath, 0); en != 0 {
		return fmt.Errorf("move_mount onto %s: %v", guest, en)
	}
	return nil
}

func doUmount(mntPath, wantIno, guest string) error {
	if err := ensureUnderDrops(guest); err != nil {
		return err
	}
	if err := enterMountNS(mntPath, wantIno); err != nil {
		return err
	}
	// Detach, so that a process holding the path open cannot keep the mount alive. A placeholder that
	// is not a mount point (already unmounted) is not an error.
	if err := syscall.Unmount(guest, syscall.MNT_DETACH); err != nil && err != syscall.EINVAL {
		return fmt.Errorf("umount %s: %v", guest, err)
	}
	return nil
}
