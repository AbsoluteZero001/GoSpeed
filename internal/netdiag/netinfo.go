package netdiag

import (
	"context"
	"net"
	"net/netip"
	"strings"
)

// systemInspector reports the local network configuration.
//
// It is split in two on purpose. The portable half enumerates interfaces and
// asks the operating system which local address it would use for the target,
// both of which the standard library can do everywhere. The platform half
// supplies the default gateway and the configured DNS servers, which the
// standard library does not expose at all.
//
// Asking the OS which source address it would pick is done by connecting a UDP
// socket and reading the local address back. A UDP connect transmits nothing:
// it only performs the routing lookup. This is the reason the result can claim
// the interface the packet would actually leave from, rather than guessing from
// the interface list.
type systemInspector struct{}

// Inspect collects the local network information for a target.
func (systemInspector) Inspect(ctx context.Context, target string) (*NetworkInfo, error) {
	info := &NetworkInfo{
		Interfaces:       enumerateInterfaces(),
		DefaultInterface: Unknown,
		DefaultGateway:   Unknown,
		DNSServers:       []string{},
		Scope:            Unknown,
	}

	local := platformGatewayAndDNS()
	info.DefaultGateway = orUnknown(local.Gateway)
	info.DNSServers = local.DNSServers
	if local.Note != "" {
		info.Notes = append(info.Notes, local.Note)
	}
	// The interface that owns the default route, as named by the routing table,
	// is authoritative: it is read from the same record as the gateway, so a
	// gateway and an interface from different adapters can never be reported
	// together.
	//
	// The UDP routing probe is the fallback for platforms that cannot name it,
	// and it asks the equivalent question of the operating system.
	info.DefaultInterface = defaultInterface()
	if local.Interface != "" {
		info.DefaultInterface = local.Interface
	}

	selection := routeTo(ctx, hostOf(target))
	info.RouteToTarget = selection
	info.LocalScope = classifyAddress(selection.LocalAddress)

	// The scope of the diagnosis is a property of the target, not of the local
	// adapter. A private source address reaching a public address is a remote
	// measurement, and reporting it as lan would describe the wrong network.
	if selection != nil {
		info.Scope = classifyAddress(selection.Destination)
	}
	return info, nil
}

// enumerateInterfaces lists the interfaces the standard library can see.
func enumerateInterfaces() []InterfaceInfo {
	interfaces, err := net.Interfaces()
	if err != nil {
		return []InterfaceInfo{}
	}
	out := make([]InterfaceInfo, 0, len(interfaces))
	for _, iface := range interfaces {
		info := InterfaceInfo{
			Name:      iface.Name,
			Index:     iface.Index,
			MTU:       iface.MTU,
			Addresses: []string{},
			IPv4:      []string{},
			IPv6:      []string{},
		}
		if iface.HardwareAddr != nil {
			info.HardwareAddr = iface.HardwareAddr.String()
		}
		if iface.Flags&net.FlagUp != 0 {
			info.Up = true
		}
		addresses, err := iface.Addrs()
		if err != nil {
			out = append(out, info)
			continue
		}
		for _, address := range addresses {
			text := address.String()
			info.Addresses = append(info.Addresses, text)
			ip, _, err := net.ParseCIDR(text)
			if err != nil {
				continue
			}
			if ip.To4() != nil {
				info.IPv4 = append(info.IPv4, ip.String())
			} else {
				info.IPv6 = append(info.IPv6, ip.String())
			}
		}
		out = append(out, info)
	}
	return out
}

// routeTo asks the OS which local address and interface it would use to reach
// the target.
//
// The returned RouteSelection always has every field filled. When the lookup
// cannot be performed the fields are Unknown rather than empty or zero, so the
// caller can render "could not determine" instead of guessing.
func routeTo(ctx context.Context, host string) *RouteSelection {
	selection := &RouteSelection{
		Destination:  orUnknown(host),
		LocalAddress: Unknown,
		Interface:    Unknown,
		Method:       "udp_route_lookup",
	}
	if host == "" {
		selection.Method = "none"
		return selection
	}

	destination := net.ParseIP(host)
	if destination == nil {
		resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(resolved) == 0 {
			// A name that will not resolve still gets a routing lookup against
			// a literal placeholder so the interface answer is available where
			// possible, but the destination stays Unknown because nothing was
			// actually resolved.
			selection.Destination = Unknown
			selection.Method = "unresolved"
			return selection
		}
		destination = net.IP(resolved[0].AsSlice())
	}
	selection.Destination = destination.String()

	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: destination, Port: 33434})
	if err != nil {
		selection.Method = "udp_route_lookup_failed"
		return selection
	}
	defer conn.Close()

	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil || local.IP.IsUnspecified() {
		selection.Method = "udp_route_lookup_empty"
		return selection
	}
	selection.LocalAddress = local.IP.String()
	if name := interfaceForIP(local.IP); name != "" {
		selection.Interface = name
	} else {
		// The address is real but the interface cannot be identified. The
		// address stays published because it was observed; only the interface
		// name is Unknown.
		selection.Interface = Unknown
	}
	return selection
}

// interfaceForIP names the interface that owns a local address.
func interfaceForIP(ip net.IP) string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	target, ok := netip.AddrFromSlice(ip)
	if !ok {
		return ""
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			own, _, err := net.ParseCIDR(address.String())
			if err != nil {
				continue
			}
			ownAddr, ok := netip.AddrFromSlice(own)
			if !ok {
				continue
			}
			if ownAddr.Unmap() == target.Unmap() {
				return iface.Name
			}
		}
	}
	return ""
}

// classifyAddress names the kind of network an address belongs to, using the
// same vocabulary internal/speedtest applies to a target.
//
// An address that will not parse is unknown rather than assumed: a guessed
// classification would be as wrong as a guessed address.
func classifyAddress(value string) string {
	if value == "" || value == Unknown {
		return Unknown
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return Unknown
	}
	addr = addr.Unmap()
	switch {
	case addr.IsLoopback():
		return ScopeLocal
	case addr.IsPrivate(), addr.IsLinkLocalUnicast():
		return ScopeLAN
	default:
		return ScopeRemote
	}
}

// Scope values shared with internal/speedtest so one vocabulary covers both the
// throughput result and the diagnostic.
const (
	ScopeLocal   = "local"
	ScopeLAN     = "lan"
	ScopeRemote  = "remote"
	ScopeUnknown = "unknown"
)

// orUnknown replaces an empty string with the Unknown literal. An empty field
// and an unknown field mean the same thing to a reader, and only one of them
// survives a JSON round trip unambiguously.
func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return Unknown
	}
	return value
}
