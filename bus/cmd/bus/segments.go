package main

import (
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/antiartificial/aurora/bus/client"
	"github.com/antiartificial/aurora/bus/proto"
)

// segFlag collects repeated --segment FILE[:mime] flags.
type segFlag []string

func (s *segFlag) String() string     { return strings.Join(*s, ",") }
func (s *segFlag) Set(v string) error { *s = append(*s, v); return nil }

const segHelp = "attach FILE as a sealed shared-memory segment, passed as an fd (repeatable; FILE or FILE=mime/type; id is the base name)"

// attachFiles adds each --segment file to e as a sealed memfd segment.
func attachFiles(e *proto.Envelope, specs []string) error {
	for _, spec := range specs {
		path, mt := spec, ""
		if i := strings.LastIndex(spec, "="); i > 0 {
			path, mt = spec[:i], spec[i+1:]
		}
		if mt == "" {
			mt = mime.TypeByExtension(filepath.Ext(path))
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = client.AttachFile(e, filepath.Base(path), mt, f)
		f.Close()
		if err != nil {
			e.CloseAttachments()
			return fmt.Errorf("segment %s: %w", path, err)
		}
	}
	return nil
}

// segmentEnv describes received segments to a handler: BUS_SEGMENTS is the
// descriptor JSON; each segment is a read-only fd 3, 4, ... (own offset).
// Returns the files to pass as ExtraFiles and the env entries.
func segmentEnv(req proto.Envelope) ([]*os.File, []string, error) {
	if len(req.Segments) == 0 {
		return nil, nil, nil
	}
	var files []*os.File
	var fds []string
	for _, d := range req.Segments {
		s, err := client.OpenSegment(req, d.ID)
		if err != nil {
			return files, nil, err
		}
		ro, err := s.OpenReadOnly()
		s.Close()
		if err != nil {
			return files, nil, err
		}
		fds = append(fds, fmt.Sprint(3+len(files)))
		files = append(files, ro)
	}
	desc, _ := json.Marshal(req.Segments)
	return files, []string{"BUS_SEGMENTS=" + string(desc), "BUS_SEGMENT_FDS=" + strings.Join(fds, " ")}, nil
}

// saveSegments writes each received segment of e into dir, named by id.
func saveSegments(e proto.Envelope, dir string) error {
	for _, d := range e.Segments {
		s, err := client.OpenSegment(e, d.ID)
		if err != nil {
			return err
		}
		m, err := s.Map()
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, filepath.Base(d.ID)), m.Bytes(), 0o600)
			m.Unmap()
		}
		s.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
