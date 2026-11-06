package manifest

import (
	"fmt"
	"strings"
)

// Error is one problem, naming the field and, where known, the source line.
type Error struct {
	Field string
	Line  int
	Msg   string
}

func (e Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s: %s", e.Line, e.Field, e.Msg)
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Msg)
}

// Errors is a list of problems that is itself an error.
type Errors []Error

func (es Errors) Error() string {
	s := make([]string, len(es))
	for i, e := range es {
		s[i] = e.Error()
	}
	return strings.Join(s, "\n")
}
