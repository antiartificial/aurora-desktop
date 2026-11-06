package launcher

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// extMIME is the fallback table. It is deliberately small and does not
// consult the system mime database, so results are the same on every host.
var extMIME = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif",
	".txt": "text/plain", ".md": "text/markdown", ".json": "application/json",
	".pdf": "application/pdf", ".svg": "image/svg+xml",
}

// DetectMIME sniffs the first 512 bytes with http.DetectContentType. That
// wins whenever it says something specific. It answers text/plain for any
// text-looking bytes and application/octet-stream for unknown binary, so for
// those two generic answers the file extension wins if it is in extMIME
// (that is how .md and .json files get a real type). Content beats extension
// otherwise: a PNG named .jpg is image/png.
func DetectMIME(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a directory; only files are supported objects", path)
	}
	buf := make([]byte, 512)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	sniffed := http.DetectContentType(buf[:n])
	base, _, perr := mime.ParseMediaType(sniffed)
	if perr != nil {
		base = sniffed
	}
	generic := base == "text/plain" || base == "application/octet-stream"
	if !generic {
		return base, nil
	}
	if byExt, ok := extMIME[strings.ToLower(filepath.Ext(path))]; ok {
		return byExt, nil
	}
	return base, nil
}
