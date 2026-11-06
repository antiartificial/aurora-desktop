// Package prov is provenance v0: the record and its extended attribute.
package prov

import (
	"encoding/json"
	"errors"
	"syscall"
)

// Attr is the xattr that carries the record on the output file.
const Attr = "user.aurora.provenance"

// Topic is the bus topic a record is published on.
const Topic = "provenance.created"

// Record says where a file came from.
type Record struct {
	File     string          `json:"file"`
	App      string          `json:"app"`
	Verb     string          `json:"verb"`
	Inputs   []string        `json:"inputs"`
	Args     json.RawMessage `json:"args"`
	Isolated bool            `json:"isolated"`
	TS       string          `json:"ts"`
}

// Write stores the record on path as an extended attribute.
func Write(path string, r Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return syscall.Setxattr(path, Attr, b, 0)
}

// Read returns the record stored on path.
func Read(path string) (Record, error) {
	var r Record
	sz, err := syscall.Getxattr(path, Attr, nil)
	if err != nil {
		if errors.Is(err, syscall.ENODATA) {
			return r, errors.New("no provenance record on " + path)
		}
		return r, err
	}
	buf := make([]byte, sz)
	n, err := syscall.Getxattr(path, Attr, buf)
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(buf[:n], &r)
}
