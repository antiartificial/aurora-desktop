// Package wire is the bus transport: newline-delimited JSON lines over a
// Unix stream socket, with file descriptors attached out of band by
// SCM_RIGHTS on the same sendmsg as the line that declares them.
//
// Stream sockets do not preserve message boundaries, so the receiver
// associates fds with lines by byte position. The Linux kernel attaches the
// fds of a sendmsg to the first byte of its data and delivers them with the
// first read that touches that byte, then ends that read. The sender here
// always sends a fd-carrying line as one sendmsg starting at the line's
// first byte, so the fds that arrive in a read belong to a line that STARTS
// inside that read. See Reader.Next for the rules and what is rejected.
package wire

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// MaxFDsPerRead is the kernel's SCM_MAX_FD: one sendmsg carries at most 253 fds.
const MaxFDsPerRead = 253

// maxPending bounds fds held by a reader that no line has claimed yet.
const maxPending = 64

// ErrLineTooLong ends a connection whose line exceeds the limit.
var ErrLineTooLong = errors.New("wire: line too long")

// ErrFDFlood ends a connection that keeps sending fds nobody declared.
var ErrFDFlood = errors.New("wire: too many unclaimed fds from peer")

// Reject is a non-fatal per-message failure: the line was read and any fds
// it came with have been closed. The connection stays usable.
type Reject struct {
	Line []byte
	Err  error
}

func (r *Reject) Error() string { return r.Err.Error() }
func (r *Reject) Unwrap() error { return r.Err }

// Write sends line, attaching files with SCM_RIGHTS on the same sendmsg. It
// does not close files. The caller must serialise writers on one conn.
// Partial writes are finished with plain writes: fds ride with the first byte.
func Write(c *net.UnixConn, line []byte, files []*os.File) error {
	if len(files) == 0 {
		_, err := c.Write(line)
		return err
	}
	if len(files) > MaxFDsPerRead {
		return fmt.Errorf("wire: %d fds exceeds the SCM_RIGHTS limit of %d", len(files), MaxFDsPerRead)
	}
	fds := make([]int, len(files))
	for i, f := range files {
		fds[i] = int(f.Fd())
	}
	n, _, err := c.WriteMsgUnix(line, unix.UnixRights(fds...), nil)
	runtime.KeepAlive(files)
	if err != nil {
		return err
	}
	if n < len(line) {
		_, err = c.Write(line[n:])
	}
	return err
}

type pfd struct {
	f    *os.File
	a, b int64 // absolute stream offsets of the read that delivered it: [a,b)
}

// Reader reads lines and their fds from one connection. Not concurrent.
type Reader struct {
	c       *net.UnixConn
	maxLine int
	buf     []byte
	start   int // buf[start:end] is unconsumed
	end     int
	base    int64 // absolute stream offset of buf[0]
	scan    int   // buf[start:scan] known to hold no newline
	q       []pfd
	oob     []byte
}

// NewReader returns a Reader that fails with ErrLineTooLong past maxLine bytes.
func NewReader(c *net.UnixConn, maxLine int) *Reader {
	return &Reader{c: c, maxLine: maxLine, buf: make([]byte, 64<<10), oob: make([]byte, unix.CmsgSpace(4*MaxFDsPerRead))}
}

// Close closes every fd still held (unclaimed). Call when the conn ends.
func (r *Reader) Close() {
	for _, p := range r.q {
		p.f.Close()
	}
	r.q = nil
}

// Next returns the next line (without its newline) and the files it claims.
// count is called with the line and returns how many fds the line declares.
//
// Handled: partial reads (lines and fds are reassembled across reads);
// fds arriving with a read that also holds earlier lines (position based
// claim); fewer fds than declared (Reject, partial fds closed); more fds
// than declared (Reject, ALL of the message's fds closed: an unclaimed fd
// is a resource leak); fds from a read that ended before this line began
// (stale, closed); fd floods (ErrFDFlood past maxPending unclaimed);
// SCM_MAX_FD per message (control buffer sized for 253, kernel closes the
// excess and sets MSG_CTRUNC, which then shows as fewer-than-declared).
// Not handled: a sender that sends two fd-carrying lines in one sendmsg
// (fds ride with the first line; the second sees fewer than declared).
func (r *Reader) Next(count func(line []byte) (int, error)) ([]byte, []*os.File, error) {
	for {
		if i := bytes.IndexByte(r.buf[r.scan:r.end], '\n'); i >= 0 {
			nl := r.scan + i
			line := append([]byte(nil), r.buf[r.start:nl]...)
			s := r.base + int64(r.start)
			e := r.base + int64(nl) + 1
			r.start, r.scan = nl+1, nl+1
			n, err := count(line)
			if err != nil {
				n = 0
			}
			files, cerr := r.claim(s, e, n)
			if err == nil {
				err = cerr
			}
			if err != nil {
				return line, nil, &Reject{Line: line, Err: err}
			}
			return line, files, nil
		}
		r.scan = r.end
		if r.end-r.start > r.maxLine {
			return nil, nil, ErrLineTooLong
		}
		if err := r.fill(); err != nil {
			return nil, nil, err
		}
	}
}

// claim takes the n fds that belong to the line occupying [s,e).
func (r *Reader) claim(s, e int64, n int) ([]*os.File, error) {
	var mine, keep []pfd
	for _, p := range r.q {
		switch {
		case p.b <= s: // delivered before this line began: stale
			p.f.Close()
		case p.a <= s: // delivered by a read that contains this line's first byte
			mine = append(mine, p)
		default:
			keep = append(keep, p)
		}
	}
	take := mine
	if len(take) > n {
		take = mine[:n]
	}
	// Surplus whose read ended inside this line cannot belong to a later line.
	var surplus int
	for _, p := range mine[len(take):] {
		if p.b <= e {
			surplus++
			p.f.Close()
		} else {
			keep = append(keep, p)
		}
	}
	r.q = keep
	switch {
	case len(take) < n:
		closeAll(take)
		return nil, fmt.Errorf("line declares %d fd(s) but only %d arrived", n, len(take))
	case surplus > 0:
		closeAll(take)
		return nil, fmt.Errorf("%d more fd(s) arrived than the %d declared; all closed", surplus, n)
	}
	files := make([]*os.File, len(take))
	for i, p := range take {
		files[i] = p.f
	}
	return files, nil
}

func closeAll(ps []pfd) {
	for _, p := range ps {
		p.f.Close()
	}
}

func (r *Reader) fill() error {
	if r.start > 0 && r.start == r.end {
		r.base += int64(r.start)
		r.start, r.end, r.scan = 0, 0, 0
	} else if r.end == len(r.buf) {
		if r.start > 0 {
			copy(r.buf, r.buf[r.start:r.end])
			r.base += int64(r.start)
			r.end -= r.start
			r.scan -= r.start
			r.start = 0
		} else {
			nb := make([]byte, 2*len(r.buf))
			copy(nb, r.buf[:r.end])
			r.buf = nb
		}
	}
	a := r.base + int64(r.end)
	n, oobn, flags, _, err := r.c.ReadMsgUnix(r.buf[r.end:], r.oob)
	r.end += n
	if oobn > 0 {
		r.takeFDs(r.oob[:oobn], a, a+int64(n))
	}
	_ = flags // MSG_CTRUNC: the kernel closed what did not fit; claim() sees fewer fds
	if err == io.EOF && n > 0 {
		err = nil
	}
	if err != nil {
		return err
	}
	if len(r.q) > maxPending {
		r.Close()
		return ErrFDFlood
	}
	return nil
}

func (r *Reader) takeFDs(oob []byte, a, b int64) {
	msgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return
	}
	for _, m := range msgs {
		fds, err := unix.ParseUnixRights(&m)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			r.q = append(r.q, pfd{f: os.NewFile(uintptr(fd), "scm"), a: a, b: b})
		}
	}
}
