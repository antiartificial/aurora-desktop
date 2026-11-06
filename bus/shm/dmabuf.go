package shm

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// dma-buf segments. A dma-buf (GPU or camera buffer, or a udmabuf) is a
// kernel buffer object passed as an fd. It differs from a sealed memfd in
// every property the bus relies on:
//
//   - it cannot be sealed: the exporter and every fd holder can keep changing
//     the contents (and the GPU can) after the bus has seen it;
//   - there is no cheap, safe content hash: reading it needs a CPU mapping
//     bracketed by DMA_BUF_IOCTL_SYNC, may stall on fences, may fault on
//     device-local memory, and the result would be stale at once;
//   - its size is lseek(fd, 0, SEEK_END), not a seal-pinned st_size.
//
// So the bus records what it can establish about a dma-buf (kind, size, the
// kernel's inode number for correlation) and NOT a content hash.

// Kind is the kind of fd carried as a segment.
type Kind string

const (
	KindMemfd  Kind = "memfd"
	KindDmabuf Kind = "dmabuf"
)

const (
	dmabufMagic         = 0x444d4142 // DMA_BUF_MAGIC, statfs f_type of the dma-buf pseudo filesystem
	dmaBufIoctlSync     = 0x40086200 // _IOW('b', 0, struct dma_buf_sync)
	udmabufCreate       = 0x40187542 // UDMABUF_CREATE
	udmabufFlagsCloexec = 1          // UDMABUF_FLAGS_CLOEXEC
)

// ErrNotDmabuf means the fd is not a dma-buf.
var ErrNotDmabuf = errors.New("shm: fd is not a dma-buf")

// IsDmabuf decides from the kernel, never from a sender's claim: the fd must
// live on the dma-buf filesystem (statfs magic) AND answer DMA_BUF_IOCTL_SYNC
// the way a dma-buf does (flags 0 is invalid: EINVAL; any other file: ENOTTY).
func IsDmabuf(f *os.File) bool {
	var sfs unix.Statfs_t
	if unix.Fstatfs(int(f.Fd()), &sfs) != nil || uint64(sfs.Type) != dmabufMagic {
		return false
	}
	var flags uint64
	_, _, e := unix.Syscall(unix.SYS_IOCTL, f.Fd(), dmaBufIoctlSync, uintptr(unsafe.Pointer(&flags)))
	return e == unix.EINVAL
}

// DetectKind classifies a received fd as a dma-buf or a memfd with sealing
// support. Anything else is an error.
func DetectKind(f *os.File) (Kind, error) {
	if IsDmabuf(f) {
		return KindDmabuf, nil
	}
	if _, err := unix.FcntlInt(f.Fd(), unix.F_GET_SEALS, 0); err == nil {
		return KindMemfd, nil
	}
	return "", errors.New("shm: fd is neither a memfd nor a dma-buf")
}

// Dmabuf is what the bus knows about a dma-buf fd. It owns nothing: the
// *os.File stays with whoever passed it.
type Dmabuf struct {
	f    *os.File
	size int64
	ino  uint64
}

// AdoptDmabuf verifies f is a dma-buf and measures it. Size is
// lseek(SEEK_END) (st_size is 0 for a dma-buf); the offset is put back.
func AdoptDmabuf(f *os.File) (*Dmabuf, error) {
	if !IsDmabuf(f) {
		return nil, ErrNotDmabuf
	}
	size, err := unix.Seek(int(f.Fd()), 0, unix.SEEK_END)
	if err != nil {
		return nil, fmt.Errorf("shm: dma-buf size: %w", err)
	}
	unix.Seek(int(f.Fd()), 0, unix.SEEK_SET)
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return nil, err
	}
	return &Dmabuf{f: f, size: size, ino: st.Ino}, nil
}

func (d *Dmabuf) Size() int64    { return d.size }
func (d *Dmabuf) Inode() uint64  { return d.ino }
func (d *Dmabuf) File() *os.File { return d.f }
