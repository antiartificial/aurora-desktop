package dom

import (
	"fmt"
	"net"
	"strings"
)

// NormalizeHost validates and canonicalises the host in a net grant. The
// grammar is deliberately tiny and closed (decision 0015): one hostname or IP
// literal, compared whole. Anything that would widen the grant past what a user
// reading "net a.example.com" would expect is refused rather than guessed at:
//
//   - wildcards ("*.example.com"): a wildcard lets the domain resolve
//     <anything>.example.com, and that lookup is a channel to the zone's
//     authoritative server. It is also the one thing "a.example.com" must not mean.
//   - ports, schemes and paths: per-port rules are not in v1, and a grant that
//     looked like it carried a port would read as a restriction it is not.
func NormalizeHost(h string) (string, error) {
	h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
	if net.ParseIP(h) != nil {
		return h, nil
	}
	switch {
	case h == "":
		return "", fmt.Errorf("empty host")
	case strings.ContainsAny(h, "*?[]"):
		return "", fmt.Errorf("net grant %q: wildcards are not supported (a wildcard would let the domain look up any name under it)", h)
	case strings.ContainsAny(h, ":/@ \t"):
		return "", fmt.Errorf("net grant %q: give a bare host name or IP (no scheme, port or path: per-port rules are not in v1)", h)
	}
	if len(h) > 253 {
		return "", fmt.Errorf("net grant: host name too long")
	}
	for _, l := range strings.Split(h, ".") {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", fmt.Errorf("net grant %q: not a valid host name", h)
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return "", fmt.Errorf("net grant %q: not a valid host name (use the ASCII/punycode form)", h)
			}
		}
	}
	return h, nil
}

// NetPolicy is the set of hosts a domain may reach: exactly its net grants.
type NetPolicy struct{ hosts map[string]bool }

// NewNetPolicy reads the policy from the domain's recorded grants. A grant that
// does not normalise is skipped, never widened: it allows nothing.
func NewNetPolicy(m *Meta) NetPolicy {
	p := NetPolicy{hosts: map[string]bool{}}
	for _, g := range m.Grants {
		if g.Kind != "net" {
			continue
		}
		if h, err := NormalizeHost(g.Host); err == nil {
			p.hosts[h] = true
		}
	}
	return p
}

// Allows reports whether the (already extracted) host name is granted. The
// comparison is whole-string equality after lower-casing: "evil-a.example.com"
// and "a.example.com.evil.net" do not match "a.example.com".
func (p NetPolicy) Allows(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h != "" && p.hosts[h]
}

// Hosts lists the granted hosts.
func (p NetPolicy) Hosts() []string {
	var o []string
	for h := range p.hosts {
		o = append(o, h)
	}
	return o
}
