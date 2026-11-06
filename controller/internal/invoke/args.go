package invoke

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// checkArgs validates args against a verb's input schema: no unknown keys,
// no missing required keys, and scalar types match. MIME-typed or `bytes`
// fields are not checked.
func checkArgs(schema map[string]string, args map[string]json.RawMessage) error {
	var problems []string
	for k, raw := range args {
		t, ok := schema[k]
		if !ok {
			problems = append(problems, fmt.Sprintf("unknown argument %q", k))
			continue
		}
		if err := checkType(strings.TrimSuffix(t, "?"), raw); err != nil {
			problems = append(problems, fmt.Sprintf("argument %q: %v", k, err))
		}
	}
	for k, t := range schema {
		if _, ok := args[k]; !ok && !strings.HasSuffix(t, "?") {
			problems = append(problems, fmt.Sprintf("missing required argument %q (%s)", k, t))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("%s", strings.Join(problems, "; "))
}

func checkType(t string, raw json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return err
	}
	switch t {
	case "string", "path":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("want %s", t)
		}
	case "int":
		n, ok := v.(json.Number)
		if !ok {
			return fmt.Errorf("want int")
		}
		if _, err := n.Int64(); err != nil {
			return fmt.Errorf("want int")
		}
	case "float":
		if _, ok := v.(json.Number); !ok {
			return fmt.Errorf("want float")
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("want bool")
		}
	}
	return nil
}

// sniffMIME guesses a file's type from its bytes, then its extension.
func sniffMIME(path string) string {
	f, err := os.Open(path)
	if err == nil {
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		f.Close()
		t := http.DetectContentType(buf[:n])
		t, _, _ = mime.ParseMediaType(t)
		if t != "application/octet-stream" && t != "text/plain" {
			return t
		}
	}
	t, _, _ := mime.ParseMediaType(mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
	return t
}

func mimeMatch(pattern, t string) bool {
	if pattern == "*/*" || pattern == t {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(t, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

func acceptsMIME(patterns []string, t string) bool {
	for _, p := range patterns {
		if mimeMatch(p, t) {
			return true
		}
	}
	return false
}
