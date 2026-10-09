package nodes

import (
	"net"
	"net/url"
	"strings"
)

// Network scope labels. They are presentation labels for a configured
// address; they never describe the internet path in use at measurement time.
const (
	ScopeLocal   = "local"
	ScopeLAN     = "lan"
	ScopeRemote  = "remote"
	ScopeUnknown = "unknown"
)

// NetworkScope classifies the host of a node base URL without contacting DNS.
// It matches the scope the measurement engine records in results: loopback and
// localhost are local, private and link-local literals are lan, other literals
// are remote, and host names stay unknown because DNS could resolve anywhere.
func NetworkScope(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ScopeUnknown
	}
	return ScopeForHost(parsed.Hostname())
}

// ScopeForHost classifies a host name or literal address without resolving it.
func ScopeForHost(host string) string {
	trimmed := strings.ToLower(strings.TrimSpace(host))
	if trimmed == "" {
		return ScopeUnknown
	}
	if trimmed == "localhost" {
		return ScopeLocal
	}
	address := net.ParseIP(trimmed)
	if address == nil {
		return ScopeUnknown
	}
	switch {
	case address.IsLoopback():
		return ScopeLocal
	case address.IsPrivate(), address.IsLinkLocalUnicast(), address.IsLinkLocalMulticast():
		return ScopeLAN
	default:
		return ScopeRemote
	}
}
