package gateway

import (
	"encoding/json"
	"strings"
)

// clean makes caller- or app-supplied text safe to print on a terminal:
// control characters (including escape) become '?', so nothing an agent sends
// can repaint a prompt or a log a human is reading. It is a copy of
// shell/launcher/approvals.go's clean(), which is unexported; the rule is the
// same, and clean_test.go pins this copy against terminal-escape inputs.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || invisibleFormat(r) {
			return '?'
		}
		return r
	}, s)
}

func invisibleFormat(r rune) bool {
	switch {
	case r >= 0x200b && r <= 0x200f, r >= 0x2028 && r <= 0x202e, r >= 0x2060 && r <= 0x206f:
		return true
	case r == 0xfeff, r == 0x061c, r >= 0xfff9 && r <= 0xfffb:
		return true
	}
	return false
}

// cleanJSON re-encodes raw with every string value (and key) cleaned, for
// payloads that came from an app or from the agent. Invalid JSON is returned
// as a cleaned JSON string, never passed through.
func cleanJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		b, _ := json.Marshal(clean(string(raw)))
		return b
	}
	b, _ := json.Marshal(cleanValue(v))
	return b
}

func cleanValue(v any) any {
	switch t := v.(type) {
	case string:
		return clean(t)
	case []any:
		for i := range t {
			t[i] = cleanValue(t[i])
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[clean(k)] = cleanValue(x)
		}
		return out
	}
	return v
}
