package netdiag

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// hostOf strips brackets from an IPv6 literal and returns the host part of a
// host:port pair. A bare address is returned unchanged.
func hostOf(target string) string {
	host := strings.TrimSpace(target)
	if strings.HasPrefix(host, "[") {
		if end := strings.LastIndex(host, "]"); end > 0 {
			return host[1:end]
		}
	}
	if host, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return host
}

// isIPv4 reports whether s is an IPv4 literal.
//
// An IPv4-mapped IPv6 literal counts as IPv4 because it is the same address;
// anything that does not parse as an address literal is not IPv4 rather than
// being assumed to be.
func isIPv4(s string) bool {
	addr, err := netip.ParseAddr(s)
	return err == nil && addr.Unmap().Is4()
}

// netAddress builds the dial string for the TCP connect measurement.
func netAddress(target string, port int) string {
	return net.JoinHostPort(hostOf(target), strconv.Itoa(port))
}

// privateBlocks are the ranges that are never globally routable. An address in
// one of them is not sent to a third party enrichment service.
var privateBlocks = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// publicAddresses filters a list down to globally routable unicast addresses,
// deduplicated and in first-seen order.
//
// It exists so an enrichment service only ever sees addresses that are already
// public. Sending it a user's LAN layout would leak internal addressing and
// would not produce an ASN anyway.
func publicAddresses(all []string) []string {
	seen := make(map[string]bool, len(all))
	var out []string
	for _, candidate := range all {
		addr, err := netip.ParseAddr(candidate)
		if err != nil {
			continue
		}
		addr = addr.Unmap()
		if !addr.IsGlobalUnicast() {
			continue
		}
		private := false
		for _, block := range privateBlocks {
			if block.Contains(addr) {
				private = true
				break
			}
		}
		if private || seen[addr.String()] {
			continue
		}
		seen[addr.String()] = true
		out = append(out, addr.String())
	}
	return out
}
