// Package shm is sealed shared memory over memfd_create(2) (decision 0001:
// "data off the bus"). A sender creates a segment, writes it, seals it, and
// passes the fd; a receiver can then trust the size and that the contents can
// never change under it, and maps it read-only.
//
// Writers use WriteAt/Write (pwrite), not a writable mmap: F_SEAL_WRITE
// cannot be applied while a writable shared mapping exists.
package shm

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// SealAll is what makes a segment trustworthy: its size cannot change, its
// contents cannot be written, and the seal set itself is frozen.
const SealAll = unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE | unix.F_SEAL_SEAL

// ErrUnsupported means the kernel lacks memfd_create or file sealing.
var ErrUnsupported = errors.New("shm: memfd with sealing is not supported by this kernel (needs Linux 3.17 or later)")

// ErrNotSealed is returned for a segment whose seals do not pin size and contents.
var ErrNotSealed = errors.New("shm: segment is not sealed against shrink, grow and write")

// Supported probes memfd_create and F_ADD_SEALS once and reports clearly.
func Supported() error {
	s, err := Create("probe", 1)
	if err != nil {
		return err
	}
	defer s.Close()
	return s.Seal()
}

// Segment is an open memfd.
type Segment struct {
	f    *os.File
	size int64
}

// Create makes a zero-filled, unsealed segment of size bytes.
func Create(name string, size int64) (*Segment, error) {
	if size < 0 {
		return nil, fmt.Errorf("shm: negative size %d", size)
	}
	fd, err := unix.MemfdCreate("aurora-"+name, unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
			return nil, fmt.Errorf("%w: memfd_create: %v", ErrUnsupported, err)
		}
		return nil, fmt.Errorf("shm: memfd_create: %w", err)
	}
	f := os.NewFile(uintptr(fd), "memfd:"+name)
	if err := f.Truncate(size); err != nil {
		f.Close()
		return nil, fmt.Errorf("shm: size %d: %w", size, err)
	}
	return &Segment{f: f, size: size}, nil
}

// FromBytes creates, fills and seals a segment.
func FromBytes(name string, data []byte) (*Segment, error) {
	s, err := Create(name, int64(len(data)))
	if err != nil {
		return nil, err
	}
	if _, err := s.WriteAt(data, 0); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.Seal(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// FromReader copies exactly size bytes from r into a new sealed segment.
func FromReader(name string, r io.Reader, size int64) (*Segment, error) {
	s, err := Create(name, size)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(io.NewOffsetWriter(s.f, 0), io.LimitReader(r, size)); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.Seal(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// FromFile copies an open regular file (from its start) into a sealed
// segment. A regular file cannot be sealed, so it is copied, never shared.
func FromFile(name string, f *os.File) (*Segment, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("shm: %s is not a regular file", f.Name())
	}
	return FromReader(name, io.NewSectionReader(f, 0, st.Size()), st.Size())
}

// Adopt wraps an fd received from a peer. It never trusts the sender: size
// comes from fstat, and the segment must be sealed (see ErrNotSealed). It
// takes ownership of f on success only.
func Adopt(f *os.File) (*Segment, error) {
	seals, err := unix.FcntlInt(f.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		if errors.Is(err, unix.EINVAL) {
			return nil, fmt.Errorf("shm: fd is not a memfd with sealing support: %w", err)
		}
		return nil, fmt.Errorf("shm: F_GET_SEALS: %w", err)
	}
	if seals&SealAll != SealAll {
		return nil, ErrNotSealed
	}
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return &Segment{f: f, size: st.Size()}, nil
}

// Seal pins size and contents and freezes the seal set.
func (s *Segment) Seal() error {
	if _, err := unix.FcntlInt(s.f.Fd(), unix.F_ADD_SEALS, SealAll); err != nil {
		if errors.Is(err, unix.EINVAL) {
			return fmt.Errorf("%w: F_ADD_SEALS: %v", ErrUnsupported, err)
		}
		if errors.Is(err, unix.EBUSY) {
			return fmt.Errorf("shm: cannot seal while a writable mapping exists: %w", err)
		}
		return fmt.Errorf("shm: seal: %w", err)
	}
	return nil
}

// Sealed reports whether the seals needed for trust are all set.
func (s *Segment) Sealed() bool {
	seals, err := unix.FcntlInt(s.f.Fd(), unix.F_GET_SEALS, 0)
	return err == nil && seals&SealAll == SealAll
}

// Size is the segment length in bytes.
func (s *Segment) Size() int64 { return s.size }

// File is the underlying fd, for passing over a socket. The Segment still owns it.
func (s *Segment) File() *os.File { return s.f }

// WriteAt writes before sealing; after sealing it fails with EPERM.
func (s *Segment) WriteAt(p []byte, off int64) (int, error) { return s.f.WriteAt(p, off) }

// ReadAt reads without touching any shared file offset (the open file
// description is shared with every process holding the fd).
func (s *Segment) ReadAt(p []byte, off int64) (int, error) {
	if off >= s.size {
		return 0, io.EOF
	}
	if int64(len(p)) > s.size-off {
		n, err := s.f.ReadAt(p[:s.size-off], off)
		if err == nil {
			err = io.EOF
		}
		return n, err
	}
	return s.f.ReadAt(p, off)
}

// Reader returns an independent sequential reader (pread based).
func (s *Segment) Reader() io.Reader { return io.NewSectionReader(s, 0, s.size) }

// SHA256 returns "sha256:<hex>" of the contents.
func (s *Segment) SHA256() (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, s.Reader()); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Dup returns a new Segment on a duplicate fd (same memory, independent Close).
func (s *Segment) Dup() (*Segment, error) {
	fd, err := unix.FcntlInt(s.f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("shm: dup: %w", err)
	}
	return &Segment{f: os.NewFile(uintptr(fd), s.f.Name()), size: s.size}, nil
}

// OpenReadOnly reopens the segment as a new read-only open file description
// with its own file offset, for handing to code that uses read(2).
func (s *Segment) OpenReadOnly() (*os.File, error) {
	return os.Open(fmt.Sprintf("/proc/self/fd/%d", s.f.Fd()))
}

// Close releases the fd. It is safe to call twice.
func (s *Segment) Close() error { return s.f.Close() }

// Mapping is a read-only view of a segment.
type Mapping struct{ data []byte }

// Map maps the whole segment PROT_READ, MAP_SHARED. The result stays valid
// after the Segment is closed, until Unmap.
func (s *Segment) Map() (*Mapping, error) {
	if s.size == 0 {
		return &Mapping{}, nil
	}
	b, err := unix.Mmap(int(s.f.Fd()), 0, int(s.size), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("shm: mmap: %w", err)
	}
	return &Mapping{data: b}, nil
}

// Bytes is the mapped memory. Writing to it faults (the mapping is read-only).
func (m *Mapping) Bytes() []byte { return m.data }

// Unmap releases the mapping. Bytes must not be used afterwards.
func (m *Mapping) Unmap() error {
	if m.data == nil {
		return nil
	}
	b := m.data
	m.data = nil
	return unix.Munmap(b)
}
