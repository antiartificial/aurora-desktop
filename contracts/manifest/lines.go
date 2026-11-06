package manifest

import (
	"regexp"
	"strings"
)

// lineIndex maps (table, index, key) to a 1-based source line. It is a
// best-effort text scan used only to put line numbers in error messages.
type lineIndex struct {
	at map[lineKey]int
}

type lineKey struct {
	table string
	idx   int
	key   string // "" is the table header itself
}

var (
	headerRe = regexp.MustCompile(`^\s*(\[\[?)\s*([A-Za-z0-9_.\-]+)\s*\]\]?`)
	keyRe    = regexp.MustCompile(`^\s*([A-Za-z0-9_\-]+)\s*=`)
)

func buildLineIndex(src []byte) *lineIndex {
	li := &lineIndex{at: map[lineKey]int{}}
	counts := map[string]int{}
	arrays := map[string]bool{}
	table, idx := "", 0
	for n, line := range strings.Split(string(src), "\n") {
		n++
		if m := headerRe.FindStringSubmatch(line); m != nil {
			name := m[2]
			first, rest, dotted := strings.Cut(name, ".")
			switch {
			case dotted && arrays[first]:
				// [verb.input] under [[verb]]: record as key of the current element.
				li.at[lineKey{first, counts[first] - 1, rest}] = n
				table, idx = "", 0
			default:
				if m[1] == "[[" {
					arrays[name] = true
				}
				idx = counts[name]
				counts[name]++
				table = name
				li.at[lineKey{table, idx, ""}] = n
			}
			continue
		}
		if m := keyRe.FindStringSubmatch(line); m != nil && table != "" {
			k := lineKey{table, idx, m[1]}
			if _, seen := li.at[k]; !seen {
				li.at[k] = n
			}
		}
	}
	return li
}

// line returns the line of a key, falling back to the table header, else 0.
func (li *lineIndex) line(table string, idx int, key string) int {
	if li == nil {
		return 0
	}
	if n, ok := li.at[lineKey{table, idx, key}]; ok {
		return n
	}
	return li.at[lineKey{table, idx, ""}]
}

// findKey returns the first line where key appears in any element of table.
func (li *lineIndex) findKey(table, key string) int {
	if li == nil {
		return 0
	}
	best := 0
	for k, n := range li.at {
		if k.table == table && k.key == key && (best == 0 || n < best) {
			best = n
		}
	}
	return best
}
