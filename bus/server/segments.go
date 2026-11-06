package server

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/shm"
)

const defaultMaxSegmentBytes = 256 << 20

func (s *Server) isReplay(e proto.Envelope) bool { return s.cfg.AllowReplay && e.Origin != nil }

// prepareSegments verifies every fd that arrived with e against its declared
// descriptor and rewrites the descriptor from what the daemon itself saw. A
// sender's size, seal claim and hash are never believed: the daemon fstats
// the fd, checks the seals, and hashes the sealed contents (immutable, so the
// hash cannot be invalidated after the fact). Runs outside s.mu.
//
// In replay mode (and only then) descriptors arrive with no fds: contents
// are not replayable, so they are kept as given and marked Absent.
func (s *Server) prepareSegments(e *proto.Envelope) error {
	if len(e.Segments) == 0 {
		return nil
	}
	if s.isReplay(*e) {
		for i := range e.Segments {
			e.Segments[i].Absent = true
		}
		return nil
	}
	max := s.cfg.MaxSegmentBytes
	if max <= 0 {
		max = defaultMaxSegmentBytes
	}
	for i := range e.Segments {
		sg := &e.Segments[i]
		f := e.Attach[i]
		kind, err := shm.DetectKind(f) // from the kernel, not from the descriptor
		if err != nil {
			return fmt.Errorf("segment %q: %w", sg.ID, err)
		}
		declared := sg.Kind
		if declared == "" {
			declared = "memfd"
		}
		if (declared == "dmabuf") != (kind == shm.KindDmabuf) {
			return fmt.Errorf("segment %q is declared kind %q but the fd is a %s", sg.ID, declared, kind)
		}
		if kind == shm.KindDmabuf {
			if err := s.prepareDmabuf(sg, f, max); err != nil {
				return err
			}
			continue
		}
		seg, err := shm.Adopt(f) // does not take ownership on error
		if err != nil {
			return fmt.Errorf("segment %q: %w", sg.ID, err)
		}
		if seg.Size() != sg.Bytes {
			return fmt.Errorf("segment %q declares %d bytes but the fd holds %d", sg.ID, sg.Bytes, seg.Size())
		}
		if seg.Size() > max {
			return fmt.Errorf("segment %q is %d bytes, over the %d limit", sg.ID, seg.Size(), max)
		}
		sum, err := seg.SHA256()
		if err != nil {
			return fmt.Errorf("segment %q: hash: %w", sg.ID, err)
		}
		sg.Sealed, sg.SHA256, sg.Absent, sg.Kind, sg.Ino = true, sum, false, "", 0
	}
	return nil
}

// dupFiles gives one recipient its own fds onto the same memory.
func dupFiles(fs []*os.File) ([]*os.File, error) {
	out := make([]*os.File, 0, len(fs))
	for _, f := range fs {
		fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			closeFiles(out)
			return nil, err
		}
		out = append(out, os.NewFile(uintptr(fd), f.Name()))
	}
	return out, nil
}

// prepareDmabuf records a dma-buf's kind, size (lseek SEEK_END) and inode. It
// does NOT hash and cannot seal: the contents stay mutable by every fd holder,
// so Sealed is false and SHA256 empty, whatever the sender declared.
func (s *Server) prepareDmabuf(sg *proto.Segment, f *os.File, max int64) error {
	d, err := shm.AdoptDmabuf(f)
	if err != nil {
		return fmt.Errorf("segment %q: %w", sg.ID, err)
	}
	if d.Size() != sg.Bytes {
		return fmt.Errorf("segment %q declares %d bytes but the dma-buf is %d", sg.ID, sg.Bytes, d.Size())
	}
	if d.Size() > max {
		return fmt.Errorf("segment %q is %d bytes, over the %d limit", sg.ID, d.Size(), max)
	}
	sg.Kind, sg.Ino, sg.Sealed, sg.SHA256, sg.Absent = "dmabuf", d.Inode(), false, "", false
	return nil
}
