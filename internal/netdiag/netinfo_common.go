package netdiag

import (
	"net"
	"strings"
)

// appendUnique appends value unless it is already present, preserving the order
// the operating system reported.
func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// defaultRouteProbeTarget is a documentation address from TEST-NET-1
// (RFC 5737). Asking the operating system which source address it would use
// for it is a pure routing lookup: the UDP socket that answers the question
// sends nothing, and the address is guaranteed never to be a real host, so no
// traffic can reach a third party.
const defaultRouteProbeTarget = "192.0.2.1"

// defaultInterface reports the interface the operating system would use for a
// destination that is off link.
//
// It is derived from the OS routing decision rather than from a hardcoded
// interface list, so it is correct on a machine with several uplinks, a VPN or
// policy routing. When the lookup yields nothing the interface is Unknown; it
// is never guessed from the first entry of the interface list.
func defaultInterface() string {
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP(defaultRouteProbeTarget),
		Port: 9,
	})
	if err != nil {
		return Unknown
	}
	defer conn.Close()
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil || local.IP.IsUnspecified() {
		return Unknown
	}
	if name := interfaceForIP(local.IP); name != "" {
		return name
	}
	return Unknown
}

// splitLines normalises line endings so one parser serves every platform's
// output.
func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.Split(text, "\n")
}

// knownInterfaceIndexes returns the adapter indexes the standard library can
// see.
//
// The Windows readers use it as a cross check: a record that names an adapter
// this function has never heard of is not published. That turns a possible
// structure layout mistake into an Unknown instead of a wrong address.
func knownInterfaceIndexes() map[uint32]bool {
	indexes := make(map[uint32]bool)
	interfaces, err := net.Interfaces()
	if err != nil {
		return indexes
	}
	for _, iface := range interfaces {
		indexes[uint32(iface.Index)] = true
	}
	return indexes
}

// knownInterfaceIndexesFn is a seam over knownInterfaceIndexes, matching the
// readForwardTableFn seam on the Windows routing reader.
//
// Both halves of the gateway decision have to be replaceable together. The
// routing reader and the adapter list are two independent inputs to the same
// question, and the validator cross checks one against the other: a record that
// names an adapter the standard library has never heard of is discarded. A test
// that replaced only the routing table left the adapter list reading the real
// machine, so a record carrying an interface index the test invented survived on
// a developer laptop that happened to own that index and was then discarded
// everywhere else. That made the same test pass locally and fail on CI without
// any code change.
//
// Production code never assigns this. Tests must restore it.
var knownInterfaceIndexesFn = knownInterfaceIndexes

// maxInt returns the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// platformNetwork is what a platform reader can say about the local side of the
// path.
//
// The gateway and the interface it belongs to travel together because they come
// from the same route record. Reporting them apart would let a reader pair a
// gateway with the wrong adapter, which on a machine with a VPN is exactly the
// mistake that misleads.
type platformNetwork struct {
	Gateway    string
	Interface  string
	DNSServers []string
	Note       string
	// Candidates is how many usable default routes were available. A value
	// above one means the choice among them was a preference rather than a
	// fact, and the reader is told so in Note.
	Candidates int
	// RunnersUp lists the default routes that were available but not chosen.
	RunnersUp []string
}

// interfaceNameForIndex maps an operating system interface index onto the name
// the standard library reports for it.
func interfaceNameForIndex(index uint32) string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range interfaces {
		if uint32(iface.Index) == index {
			return iface.Name
		}
	}
	return ""
}
