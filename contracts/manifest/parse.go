package manifest

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// Parse decodes a manifest. It rejects unknown keys and reports syntax errors
// with a line number. It does not validate; call Validate for that.
func Parse(src []byte) (*Manifest, error) {
	var m Manifest
	md, err := toml.Decode(string(src), &m)
	if err != nil {
		if pe, ok := err.(toml.ParseError); ok {
			return nil, Error{Field: "toml", Line: pe.Position.Line, Msg: pe.Message}
		}
		return nil, Error{Field: "toml", Msg: err.Error()}
	}
	m.lines = buildLineIndex(src)
	if un := md.Undecoded(); len(un) > 0 {
		var errs Errors
		for _, k := range un {
			parts := []string(k)
			table, key := strings.Join(parts[:len(parts)-1], "."), parts[len(parts)-1]
			errs = append(errs, Error{Field: k.String(), Line: m.lines.findKey(table, key),
				Msg: "unknown key"})
		}
		return nil, errs
	}
	return &m, nil
}

// Encode serialises a manifest to TOML.
func Encode(m *Manifest) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return buf.Bytes(), nil
}

// Load parses and validates in one step.
func Load(src []byte) (*Manifest, error) {
	m, err := Parse(src)
	if err != nil {
		return nil, err
	}
	if errs := m.Validate(); len(errs) > 0 {
		return nil, errs
	}
	return m, nil
}
