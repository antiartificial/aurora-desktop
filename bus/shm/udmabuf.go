package shm

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// CreateUdmabuf makes a real dma-buf from a fresh memfd through /dev/udmabuf,
// the one allocator that needs no GPU. size is rounded up to a page. It is a
// producer for tests and CPU-only senders; a GPU client would pass the fd it
// got from its driver. It fails if /dev/udmabuf is missing or not accessible.
func CreateUdmabuf(size int64) (*os.File, error) {
	page := int64(os.Getpagesize())
	size = (size + page - 1) / page * page
	if size == 0 {
		size = page
	}
	dev, err := os.OpenFile("/dev/udmabuf", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("shm: udmabuf: %w", err)
	}
	defer dev.Close()
	mfd, err := unix.MemfdCreate("aurora-udmabuf", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	defer unix.Close(mfd)
	if err := unix.Ftruncate(mfd, size); err != nil {
		return nil, err
	}
	if _, err := unix.FcntlInt(uintptr(mfd), unix.F_ADD_SEALS, unix.F_SEAL_SHRINK); err != nil {
		return nil, err
	}
	req := struct {
		memfd, flags uint32
		offset, size uint64
	}{uint32(mfd), udmabufFlagsCloexec, 0, uint64(size)}
	fd, _, e := unix.Syscall(unix.SYS_IOCTL, dev.Fd(), udmabufCreate, uintptr(unsafe.Pointer(&req)))
	if e != 0 {
		return nil, fmt.Errorf("shm: UDMABUF_CREATE: %w", e)
	}
	return os.NewFile(fd, "dmabuf:udmabuf"), nil
}
