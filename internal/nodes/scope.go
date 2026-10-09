package nodes

import (
	"net"
	"net/url"
	"strings"
)

// Network scope labels. They describe the configured address only; they never
// claim which path the traffic actually took at measurement time. A host name
// stays unknown because resolving it could point anywhere (LAN, VPN, proxy or
// the public internet), so "unknown" is the honest answer, not a fallback.
//
// Mapping to the presentation buckets used by the CLI and the desktop client:
//
//	LOOPBACK -> ScopeLocal   (127.0.0.0/8, ::1, localhost)
//	LAN      -> ScopeLAN     (RFC1918, ULA, link-local literals)
//	PUBLIC   -> ScopeRemote  (other literal addresses)
//	UNKNOWN  -> ScopeUnknown (host names and anything not classifiable)
const (
	ScopeLocal   = "local"
	ScopeLAN     = "lan"
	ScopeRemote  = "remote"
	ScopeUnknown = "unknown"
)

// IsLoopbackHost reports whether a host name or literal address is the local
// machine (127.0.0.0/8, ::1 or localhost). It never resolves names, so a host
// name other than "localhost" is not treated as loopback.
func IsLoopbackHost(host string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(host))
	if trimmed == "" {
		return false
	}
	if trimmed == "localhost" {
		return true
	}
	address := net.ParseIP(trimmed)
	return address != nil && address.IsLoopback()
}

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
