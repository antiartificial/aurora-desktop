package client

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/antiartificial/aurora/bus/proto"
	"github.com/antiartificial/aurora/bus/shm"
)

// Sending. Each helper adds a sealed segment to e. Client.Send then passes
// the fd with the line and closes it. The daemon computes the hash.
//
//	e := proto.Envelope{Kind: proto.KindEvent, Topic: "image.ready"}
//	client.AttachBytes(&e, "frame", "image/png", pngBytes)
//	c.Send(e)

// AttachBytes copies data into a new sealed memfd and attaches it.
func AttachBytes(e *proto.Envelope, id, mime string, data []byte) error {
	s, err := shm.FromBytes(id, data)
	if err != nil {
		return err
	}
	return attach(e, id, mime, s)
}

// AttachFile copies an open regular file into a sealed memfd and attaches it
// (a regular file cannot be sealed, so it is copied, not shared).
func AttachFile(e *proto.Envelope, id, mime string, f *os.File) error {
	s, err := shm.FromFile(id, f)
	if err != nil {
		return err
	}
	return attach(e, id, mime, s)
}

// AttachSegment attaches a sealed segment you already hold; the caller keeps
// and still owns s (a dup of its fd is sent).
func AttachSegment(e *proto.Envelope, id, mime string, s *shm.Segment) error {
	if !s.Sealed() {
		return shm.ErrNotSealed
	}
	d, err := s.Dup()
	if err != nil {
		return err
	}
	return attach(e, id, mime, d)
}

// attach takes ownership of s's fd (into e.Attach).
func attach(e *proto.Envelope, id, mime string, s *shm.Segment) error {
	if len(e.Segments) >= proto.MaxSegments {
		s.Close()
		return fmt.Errorf("at most %d segments per message", proto.MaxSegments)
	}
	for _, d := range e.Segments {
		if d.ID == id {
			s.Close()
			return fmt.Errorf("segment id %q already used", id)
		}
	}
	e.Segments = append(e.Segments, proto.Segment{ID: id, Bytes: s.Size(), Sealed: true, Mime: mime})
	e.Attach = append(e.Attach, s.File())
	return nil
}

// OpenSegment returns the received segment id, verified: it must be a
// memfd sealed against shrink, grow and write, with the size the descriptor
// declares. The returned Segment owns a dup, so it outlives
// e.CloseAttachments. Read it with Map (read-only mmap) or Reader (io.Reader).
// A replayed message has descriptors but no contents: that is an error here.
func OpenSegment(e proto.Envelope, id string) (*shm.Segment, error) {
	i := e.AttachIndex(id)
	if i < 0 {
		return nil, fmt.Errorf("message has no segment %q with contents", id)
	}
	held, err := shm.Adopt(e.Attach[i]) // validates; e still owns the fd
	if err != nil {
		return nil, err
	}
	s, err := held.Dup()
	if err != nil {
		return nil, err
	}
	for _, desc := range e.Segments {
		if desc.ID == id && desc.Bytes != s.Size() {
			s.Close()
			return nil, fmt.Errorf("segment %q declared %d bytes, fd holds %d", id, desc.Bytes, s.Size())
		}
	}
	return s, nil
}

// AttachDmabuf attaches a dma-buf fd you hold (a dup is sent; you keep f). The
// descriptor is unsealed and unhashed: receivers must treat the contents as
// mutable by anyone holding the buffer. Fails if f is not a dma-buf.
func AttachDmabuf(e *proto.Envelope, id, mime string, f *os.File) error {
	d, err := shm.AdoptDmabuf(f)
	if err != nil {
		return err
	}
	if len(e.Segments) >= proto.MaxSegments {
		return fmt.Errorf("at most %d segments per message", proto.MaxSegments)
	}
	for _, o := range e.Segments {
		if o.ID == id {
			return fmt.Errorf("segment id %q already used", id)
		}
	}
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return err
	}
	e.Segments = append(e.Segments, proto.Segment{ID: id, Bytes: d.Size(), Kind: "dmabuf", Ino: d.Inode(), Mime: mime})
	e.Attach = append(e.Attach, os.NewFile(uintptr(fd), f.Name()))
	return nil
}

// OpenDmabuf returns a received dma-buf segment (a dup the caller closes) and
// its descriptor. It checks the fd is a dma-buf of the declared size. Unlike
// OpenSegment there is no seal and no hash to rely on.
func OpenDmabuf(e proto.Envelope, id string) (*os.File, proto.Segment, error) {
	i := e.AttachIndex(id)
	if i < 0 {
		return nil, proto.Segment{}, fmt.Errorf("message has no segment %q with contents", id)
	}
	d, err := shm.AdoptDmabuf(e.Attach[i])
	if err != nil {
		return nil, proto.Segment{}, err
	}
	for _, desc := range e.Segments {
		if desc.ID == id {
			if desc.Kind != "dmabuf" || desc.Bytes != d.Size() {
				return nil, proto.Segment{}, fmt.Errorf("segment %q: descriptor does not match the dma-buf (%d bytes)", id, d.Size())
			}
			fd, err := unix.FcntlInt(e.Attach[i].Fd(), unix.F_DUPFD_CLOEXEC, 0)
			if err != nil {
				return nil, proto.Segment{}, err
			}
			return os.NewFile(uintptr(fd), e.Attach[i].Name()), desc, nil
		}
	}
	return nil, proto.Segment{}, fmt.Errorf("no descriptor for %q", id)
}
