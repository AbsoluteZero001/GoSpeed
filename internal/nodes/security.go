package nodes

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ErrUnsafeTarget reports a node address that the address policy refuses to
// contact. GoSpeed never proxies or forwards URLs, so a node definition is the
// only place where an address can enter the tool; this policy keeps a shared or
// untrusted node list from turning the client into a probe for sensitive hosts.
var ErrUnsafeTarget = errors.New("nodes: target address is not allowed")

// blockedHostNames are cloud metadata service names. They must never be
// reachable through a node definition, because a metadata endpoint can hand
// out credentials to whoever asks.
var blockedHostNames = map[string]bool{
	"metadata":                 true,
	"metadata.goog":            true,
	"metadata.google.internal": true,
}

// blockedAddresses are well known cloud metadata addresses.
var blockedAddresses = []net.IP{
	net.ParseIP("169.254.169.254"), // AWS / Azure / GCP / OpenStack metadata
	net.ParseIP("100.100.100.200"), // Alibaba Cloud metadata
	net.ParseIP("fd00:ec2::254"),   // AWS IPv6 metadata
}

// ValidateTarget applies the GoSpeed address policy to a node:
//
//   - loopback addresses are allowed only for nodes explicitly marked local,
//     so a node list cannot silently point a "remote" node at the user's own
//     machine;
//   - link-local and cloud metadata addresses are always rejected;
//   - private LAN addresses are allowed because testing a LAN server is a
//     legitimate use case;
//   - host names are allowed and resolved by net/http at connection time
//     (GoSpeed does not pin or rewrite DNS results).
func (n Node) ValidateTarget() error {
	baseURL := strings.TrimRight(strings.TrimSpace(n.BaseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("node %q: parse base_url: %w", n.ID, err)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return fmt.Errorf("%w: node %q has no host", ErrUnsafeTarget, n.ID)
	}
	if blockedHostNames[host] {
		return fmt.Errorf("%w: node %q points at a cloud metadata host (%s)", ErrUnsafeTarget, n.ID, host)
	}
	address := net.ParseIP(host)
	if address == nil {
		// A host name: the policy cannot classify it without resolving, and
		// resolving would itself contact DNS. net/http validates the
		// certificate chain on HTTPS, and the operator chose this name.
		return nil
	}
	for _, blocked := range blockedAddresses {
		if blocked != nil && blocked.Equal(address) {
			return fmt.Errorf("%w: node %q points at a cloud metadata address (%s)", ErrUnsafeTarget, n.ID, address)
		}
	}
	if address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return fmt.Errorf("%w: node %q points at a link-local address (%s)", ErrUnsafeTarget, n.ID, address)
	}
	if address.IsLoopback() && !n.Local {
		return fmt.Errorf("%w: node %q points at a loopback address (%s); mark it with \"local\": true if this is intentional",
			ErrUnsafeTarget, n.ID, address)
	}
	return nil
}

// ValidateStoredNode is the validation applied whenever a node is loaded from
// or written to a configuration file.
func ValidateStoredNode(node Node) error {
	if err := node.Validate(); err != nil {
		return err
	}
	return node.ValidateTarget()
}
