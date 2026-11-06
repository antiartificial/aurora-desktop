package manifest

import (
	"regexp"
	"strings"
)

var (
	idRe      = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$`)
	semverRe  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	hashRe    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	verbRe    = regexp.MustCompile(`^[a-z][a-z0-9_]*@v[1-9][0-9]*$`)
	topicRe   = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z][a-z0-9_-]*)*$`)
	keyNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	deviceRe  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	hostRe    = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	portRe    = regexp.MustCompile(`^[0-9]{1,5}$`)
	tokenRe   = `[a-z0-9][a-z0-9!#$&^_.+-]{0,126}`
	mimeRe    = regexp.MustCompile(`^` + tokenRe + `/` + tokenRe + `$`)
	mimeWild  = regexp.MustCompile(`^(` + tokenRe + `/\*|\*/\*)$`)
)

func validMIME(s string) bool       { return mimeRe.MatchString(s) }
func validMIMEOrWild(s string) bool { return mimeRe.MatchString(s) || mimeWild.MatchString(s) }

// matchMIME reports whether pattern (exact, "type/*" or "*/*") covers mime.
func matchMIME(pattern, mime string) bool {
	if pattern == mime || pattern == "*/*" {
		return true
	}
	if t, ok := strings.CutSuffix(pattern, "/*"); ok {
		return strings.HasPrefix(mime, t+"/")
	}
	return false
}

// validPath: absolute or "~/"-rooted, no empty, "." or ".." segments, no NUL.
func validPath(p string) string {
	switch {
	case p == "":
		return "is empty"
	case strings.ContainsRune(p, 0):
		return "contains a NUL byte"
	case !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/"):
		return `must be absolute ("/...") or home-relative ("~/...")`
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/")
	if rest == "" {
		return "must name a directory below / or ~/ (granting all of it is not allowed)"
	}
	for _, seg := range strings.Split(strings.TrimSuffix(rest, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return `has an empty, "." or ".." segment`
		}
	}
	return ""
}

// splitHost splits "host[:port]".
func splitHost(s string) (host, port string) {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func validHost(s string) string {
	if strings.Contains(s, "://") || strings.Contains(s, "/") {
		return "must be a bare host, not a URL"
	}
	host, port := splitHost(s)
	if strings.Contains(s, ":") && !portRe.MatchString(port) {
		return "has an invalid port"
	}
	if !hostRe.MatchString(host) || len(host) > 253 {
		return "is not a valid host name"
	}
	if strings.HasPrefix(host, "*.") && !strings.Contains(host[2:], ".") {
		return "wildcard must be below a registrable domain (*.example.com)"
	}
	return ""
}
