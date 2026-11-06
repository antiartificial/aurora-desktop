package proto

import (
	"path"
	"strings"
)

// Match reports whether a dotted topic matches a glob pattern.
// Each segment is matched with path.Match rules ("*" matches within one
// segment, so "window.*" matches "window.opened" but not "window.a.b").
// A whole segment of "**" matches zero or more segments.
func Match(pattern, topic string) bool {
	return match(strings.Split(pattern, "."), strings.Split(topic, "."))
}

func match(p, t []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			if len(p) == 1 {
				return true
			}
			for i := 0; i <= len(t); i++ {
				if match(p[1:], t[i:]) {
					return true
				}
			}
			return false
		}
		if len(t) == 0 {
			return false
		}
		if ok, _ := path.Match(p[0], t[0]); !ok {
			return false
		}
		p, t = p[1:], t[1:]
	}
	return len(t) == 0
}
