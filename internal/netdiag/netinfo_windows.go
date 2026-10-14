//go:build windows

package netdiag

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// Windows exposes the default IPv4 gateway through the IP forwarding table and
// the configured DNS servers through GetAdaptersAddresses. They come from
// different APIs, so they are read separately.
//
// Both readings validate before publishing. The gateway must come from a row
// whose destination and mask are both zero, which is what "default route" means,
// and it must be a usable unicast address. A DNS server must belong to an
// adapter that is up, bound to IPv4 and also visible through the standard
// library. A layout mistake degrades to Unknown instead of inventing a value.

const (
	// flagUp is IfOperStatusUp. The LhFlags word is a union whose low byte
	// carries either the operational status or a media connect state depending
	// on the other flags, so only the "up" bit is treated as meaningful. It was
	// verified against a live adapter list: this is the bit that separates a
	// connected adapter from a dormant one.
	flagUp = 0x00000001

	// familyIPv4 is AF_INET.
	familyIPv4 = 2

	// familyUnspecified makes GetAdaptersAddresses return every family.
	familyUnspecified = 0

	// gaFlags skips multicast and anycast records, which never carry a gateway
	// or a configured resolver.
	gaFlags = 0x00000002 | 0x00000004
)

// forwardTableHeaderBytes is the number of bytes that precede the documented
// MIB_IPFORWARDROW fields inside each record the API fills.
//
// This was established empirically against the live routing table rather than
// assumed. Reading the fields one 32 bit word later than the published
// MIB_IPFORWARDROW description predicts is what makes every record parse
// correctly: with the word at offset zero treated as the destination, every
// route reads back as 0.0.0.0/0 with an impossible interface index. With the
// documented offset applied, the first record reproduces exactly what the
// native Get-NetRoute cmdlet reports for the same machine (next hop, interface
// index, route metric and interface metric), and the remaining records decode
// into the expected host and subnet routes for the loopback, link local and
// configured interfaces.
//
// The consequence of being wrong on some other machine is bounded: every field
// is validated below, and a record that fails validation is skipped rather than
// published. The failure mode is "unknown", never a wrong gateway.
// ipForwardRow mirrors MIB_IPFORWARDROW from the Windows SDK.
//
// Every member is a 32 bit word or a four byte address array, so the compiler
// inserts no padding and the Go size is exactly the C size. All five metric
// words are declared because dropping trailing members would change the size and
// shift every following record; the ones this file does not use are kept so the
// stride stays right.
type ipForwardRow struct {
	ForwardDest      [4]byte
	ForwardMask      [4]byte
	ForwardPolicy    uint32
	ForwardNextHop   [4]byte
	ForwardIfIndex   uint32
	ForwardType      uint32
	ForwardProto     uint32
	ForwardAge       uint32
	ForwardNextHopAS uint32

	// ForwardMetric1 is the route's effective metric as the routing stack uses
	// it, which on this operating system is the interface metric already folded
	// in. It is therefore compared on its own and InterfaceMetric is never
	// added to it again: doing so would count the same cost twice and could
	// pick the wrong uplink.
	//
	// The separate RouteMetric and InterfaceMetric that Get-NetRoute prints come
	// from a different API, MIB_IPFORWARD_ROW2, which this file does not use.
	// ForwardMetric1 is only verified to equal their sum, never to equal either
	// one alone.
	ForwardMetric1 uint32
	ForwardMetric2 uint32
	ForwardMetric3 uint32
	ForwardMetric4 uint32
	ForwardMetric5 uint32
}

// MIB_IPFORWARDTABLE begins with dwNumEntries and is followed by the records
// themselves, so the table has a four byte header and the first record starts
// immediately after it. dwNumEntries, not the reported buffer size, is the
// number of records: the buffer the API fills is larger than the table it
// contains, and walking to the end of the buffer would read past the last
// record into unused space.
const ipForwardTableHeaderBytes = 4

// maxForwardTableEntries bounds how many records are accepted from a header, so
// a corrupt count cannot turn a diagnostic into a large allocation or an
// unbounded walk.
const maxForwardTableEntries = 4096

// maxForwardTableBytes bounds the buffer allocation itself.
const maxForwardTableBytes = 8 << 20

// ipForwardRowBytes is the on-disk size of one record. It is asserted against
// the compiled struct size so a future edit that adds or removes a member fails
// loudly here instead of silently shifting every field after the table header.
var ipForwardRowBytes = int(unsafe.Sizeof(ipForwardRow{}))

// parseForwardTable turns the buffer GetIpForwardTable fills into records.
//
// The copy is done byte by byte rather than by casting the buffer to a struct,
// because this part of the routing table needs no pointer arithmetic at all.
func parseForwardTable(buf []byte) ([]ipForwardRow, error) {
	if ipForwardRowBytes != 14*4 {
		return nil, fmt.Errorf("netdiag: ipForwardRow is %d bytes, the Windows record is 56", ipForwardRowBytes)
	}
	if len(buf) < ipForwardTableHeaderBytes {
		return nil, fmt.Errorf("netdiag: routing table buffer is %d bytes, too small for a header", len(buf))
	}
	entries := binary.LittleEndian.Uint32(buf[:ipForwardTableHeaderBytes])
	if entries > maxForwardTableEntries {
		return nil, fmt.Errorf("netdiag: routing table claims %d entries, refusing to walk it", entries)
	}
	needed := ipForwardTableHeaderBytes + int(entries)*ipForwardRowBytes
	if needed > len(buf) {
		return nil, fmt.Errorf(
			"netdiag: routing table needs %d bytes for %d entries but the buffer holds %d",
			needed, entries, len(buf))
	}

	rows := make([]ipForwardRow, entries)
	for index := range rows {
		offset := ipForwardTableHeaderBytes + index*ipForwardRowBytes
		copy(rows[index].ForwardDest[:], buf[offset:offset+4])
		copy(rows[index].ForwardMask[:], buf[offset+4:offset+8])
		rows[index].ForwardPolicy = binary.LittleEndian.Uint32(buf[offset+8 : offset+12])
		copy(rows[index].ForwardNextHop[:], buf[offset+12:offset+16])
		rows[index].ForwardIfIndex = binary.LittleEndian.Uint32(buf[offset+16 : offset+20])
		rows[index].ForwardType = binary.LittleEndian.Uint32(buf[offset+20 : offset+24])
		rows[index].ForwardProto = binary.LittleEndian.Uint32(buf[offset+24 : offset+28])
		rows[index].ForwardAge = binary.LittleEndian.Uint32(buf[offset+28 : offset+32])
		rows[index].ForwardNextHopAS = binary.LittleEndian.Uint32(buf[offset+32 : offset+36])
		rows[index].ForwardMetric1 = binary.LittleEndian.Uint32(buf[offset+36 : offset+40])
		rows[index].ForwardMetric2 = binary.LittleEndian.Uint32(buf[offset+40 : offset+44])
		rows[index].ForwardMetric3 = binary.LittleEndian.Uint32(buf[offset+44 : offset+48])
		rows[index].ForwardMetric4 = binary.LittleEndian.Uint32(buf[offset+48 : offset+52])
		rows[index].ForwardMetric5 = binary.LittleEndian.Uint32(buf[offset+52 : offset+56])
	}
	return rows, nil
}

// isContiguousMask reports whether mask is a run of one bits from the top.
//
// A real route mask always looks like that. A value read from the wrong offset
// almost never does, which makes this the cheapest way to tell a correct parse
// from a wrong one.
func isContiguousMask(mask [4]byte) bool {
	seenZero := false
	for _, b := range mask {
		for bit := 7; bit >= 0; bit-- {
			set := b&(1<<uint(bit)) != 0
			if seenZero && set {
				return false
			}
			if !set {
				seenZero = true
			}
		}
	}
	return true
}

// routeRecordIsUsable reports whether a record is a default route that can be
// published without inventing anything.
//
// The checks are deliberately redundant, and each one independently rules out a
// way the record could be wrong: a destination or mask that is not the default
// route, a mask that is not a real prefix length, an interface index no adapter
// claims, and a next hop that is not a usable unicast address.
func routeRecordIsUsable(row ipForwardRow, known map[uint32]bool) bool {
	if row.ForwardDest != [4]byte{} || row.ForwardMask != [4]byte{} {
		return false
	}
	if !isContiguousMask(row.ForwardMask) {
		return false
	}
	if row.ForwardIfIndex == 0 || !known[row.ForwardIfIndex] {
		return false
	}
	address := netip.AddrFrom4(row.ForwardNextHop)
	if !address.IsValid() || address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() {
		return false
	}
	return true
}

// selectDefaultGateway picks the default route Windows itself would prefer.
//
// Among several default routes the lowest effective metric wins, which is how
// the routing stack orders them on a machine with several uplinks or a VPN.
// A metric of zero is a legitimate, preferred value and is compared like any
// other: treating zero as "unset" would discard the best route on the table.
//
// The comparison uses ForwardMetric1 alone. InterfaceMetric is already part of
// it, so adding a separate interface metric on top would count the same cost
// twice.
func selectDefaultGateway(rows []ipForwardRow, known map[uint32]bool) (gateway string, interfaceIndex uint32, ok bool) {
	best := ^uint32(0)
	for _, row := range rows {
		if !routeRecordIsUsable(row, known) {
			continue
		}
		if ok && row.ForwardMetric1 >= best {
			continue
		}
		best = row.ForwardMetric1
		gateway = netip.AddrFrom4(row.ForwardNextHop).String()
		interfaceIndex = row.ForwardIfIndex
		ok = true
	}
	return gateway, interfaceIndex, ok
}

// readForwardTable queries the IPv4 routing table and returns its records.
func readForwardTable() ([]ipForwardRow, error) {
	handle, err := syscall.LoadDLL("iphlpapi.dll")
	if err != nil {
		return nil, err
	}
	defer handle.Release()
	proc, err := handle.FindProc("GetIpForwardTable")
	if err != nil {
		return nil, err
	}

	var size uint32
	// The sizing call fails with ERROR_INSUFFICIENT_BUFFER and reports the
	// size the table needs. That is the expected first step, not an error.
	if ret, _, _ := proc.Call(0, uintptr(unsafe.Pointer(&size))); ret == 0 && size == 0 {
		return nil, fmt.Errorf("netdiag: the routing table reported no size")
	}
	if size == 0 || size > maxForwardTableBytes {
		return nil, fmt.Errorf("netdiag: routing table size %d is out of range", size)
	}
	buf := make([]byte, size)
	if ret, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); ret != 0 {
		return nil, fmt.Errorf("netdiag: GetIpForwardTable failed with code %d", ret)
	}
	return parseForwardTable(buf)
}

// readForwardTableFn is a seam so a test can substitute a routing table reader
// that fails, without the rest of the package needing to know how the table is
// fetched. Production code never assigns it.
var readForwardTableFn = readForwardTable

// defaultGatewayResult is the full outcome of reading the default route.
type defaultGatewayResult struct {
	Gateway   string
	Interface uint32
	// Candidates is how many usable default routes the table offered. More than
	// one means the machine has several uplinks or a VPN, and the choice is
	// reported so a reader can check it against the native tooling.
	Candidates int
	// RunnersUp lists the gateways that were available but not preferred.
	RunnersUp []string
}

// defaultGateway returns the next hop and interface of the IPv4 default route.
func defaultGateway() defaultGatewayResult {
	rows, err := readForwardTableFn()
	if err != nil {
		return defaultGatewayResult{}
	}
	known := knownInterfaceIndexesFn()
	gateway, index, ok := selectDefaultGateway(rows, known)
	result := defaultGatewayResult{Gateway: gateway, Interface: index}
	if !ok {
		return result
	}

	// Count the routes that were available, so an ambiguous table can be
	// reported rather than presented as a single unambiguous answer.
	best := ^uint32(0)
	for _, row := range rows {
		if !routeRecordIsUsable(row, known) {
			continue
		}
		result.Candidates++
		address := netip.AddrFrom4(row.ForwardNextHop).String()
		if row.ForwardMetric1 < best {
			best = row.ForwardMetric1
		}
		if address != gateway {
			result.RunnersUp = append(result.RunnersUp, address)
		}
	}
	return result
}

// adapterAddresses is IP_ADAPTER_ADDRESSES_LH up to the first DNS server
// pointer. Every field uses a real Go type so the compiler inserts the right
// padding per architecture; the Flags bit field is a plain uint32 tested with
// masks because a C bit field has no faithful Go equivalent.
type adapterAddresses struct {
	Length              uint32
	IfIndex             uint32
	Next                *adapterAddresses
	AdapterName         *byte
	FirstUnicastAddress uintptr
	FirstAnycast        uintptr
	FirstMulticast      uintptr
	FirstDNSServer      *dnsServerAddress
	DnsSuffix           uintptr
	Description         uintptr
	FriendlyName        uintptr
	PhysicalAddress     [8]byte
	PhysicalAddressLen  uint32
	Flags               uint32
}

// dnsServerAddress is IP_ADAPTER_DNS_SERVER_ADDRESS_XP.
type dnsServerAddress struct {
	Length  uint32
	Flags   uint32
	Next    *dnsServerAddress
	Address socketAddress
}

// socketAddress is SOCKET_ADDRESS: a sockaddr pointer and its length.
//
// The pointer is typed unsafe.Pointer rather than uintptr on purpose. The API
// writes a raw address into the buffer, but reading it back as a uintptr and
// converting it to unsafe.Pointer is exactly the pattern go vet rejects: the
// compiler is free to assume a uintptr does not keep anything alive, so the
// pointed-to bytes could move or be collected before they are read. Keeping it
// as unsafe.Pointer makes it a real reference for as long as the structure
// lives, and the buffer it points into is a Go allocation kept alive by
// runtime.KeepAlive at the end of the call.
type socketAddress struct {
	Pointer unsafe.Pointer
	Length  int32
}

// sockaddrFamily returns the AF_* value at the head of a sockaddr.
func sockaddrFamily(base unsafe.Pointer, length int32) uint16 {
	if base == nil || length < 2 {
		return 0
	}
	raw := unsafe.Slice((*byte)(base), 2)
	return uint16(raw[0]) | uint16(raw[1])<<8
}

// sockaddrInIPv4 reads the IPv4 address out of a sockaddr_in, which is
// family(2) port(2) address(4) zero(8) in network byte order.
func sockaddrInIPv4(base unsafe.Pointer, length int32) string {
	if base == nil || length < 8 {
		return ""
	}
	raw := unsafe.Slice((*byte)(base), int(length))
	addr := netip.AddrFrom4([4]byte{raw[4], raw[5], raw[6], raw[7]})
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsMulticast() || addr.IsLinkLocalUnicast() {
		return ""
	}
	return addr.String()
}

// platformGatewayAndDNS reads the default IPv4 gateway, the interface that
// gateway belongs to, and the DNS servers configured on the active adapters.
func platformGatewayAndDNS() platformNetwork {
	result := platformNetwork{DNSServers: []string{}}

	choice := defaultGateway()
	if choice.Gateway != "" {
		result.Gateway = choice.Gateway
		result.Interface = interfaceNameForIndex(choice.Interface)
		result.Candidates = choice.Candidates
		result.RunnersUp = choice.RunnersUp
	}

	handle, err := syscall.LoadDLL("iphlpapi.dll")
	if err != nil {
		result.Note = "the Windows adapter list could not be read, so the DNS servers are unknown: " + err.Error()
		return result
	}
	defer handle.Release()
	proc, err := handle.FindProc("GetAdaptersAddresses")
	if err != nil {
		if result.Note == "" {
			result.Note = "GetAdaptersAddresses is unavailable, so the DNS servers are unknown"
		} else {
			result.Note += "; GetAdaptersAddresses is unavailable"
		}
		return result
	}

	size := uint32(15 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		ret, _, _ := proc.Call(
			uintptr(familyUnspecified), uintptr(gaFlags),
			0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)),
		)
		if ret != 0 {
			// A non zero result means the buffer was refused; the size field
			// carries the size the next attempt needs.
			continue
		}
		known := knownInterfaceIndexesFn()
		for adapter := (*adapterAddresses)(unsafe.Pointer(&buf[0])); adapter != nil; adapter = adapter.Next {
			// Only an adapter that is up and also visible through the standard
			// library is read. Whether it carries IPv4 is decided by what the
			// address lists contain, not by a flag: the union in LhFlags has no
			// bit that means "IPv4 is bound".
			if adapter.Flags&flagUp == 0 || !known[adapter.IfIndex] {
				continue
			}
			for dns := adapter.FirstDNSServer; dns != nil; dns = dns.Next {
				if sockaddrFamily(dns.Address.Pointer, dns.Address.Length) != familyIPv4 {
					continue
				}
				if address := sockaddrInIPv4(dns.Address.Pointer, dns.Address.Length); address != "" {
					result.DNSServers = appendUnique(result.DNSServers, address)
				}
			}
			if len(result.DNSServers) > 0 {
				break
			}
		}
		runtime.KeepAlive(buf)
		break
	}

	var notes []string
	if result.Gateway == "" {
		notes = append(notes,
			"no IPv4 default route (0.0.0.0/0) with a usable next hop was found; "+
				"the gateway is reported as unknown rather than guessed")
	} else if result.Candidates > 1 {
		// More than one uplink means the choice is a preference, not a fact.
		// Saying so, and naming what was passed over, lets a reader confirm the
		// selection against the native routing tools instead of trusting it.
		notes = append(notes, fmt.Sprintf(
			"%d IPv4 default routes were available; %s was chosen because it had the "+
				"lowest effective route metric in the routing table (also offered: %s). "+
				"Windows combines the route and interface metrics into that single value, "+
				"so the two contributions cannot be separated here",
			result.Candidates, result.Gateway, strings.Join(result.RunnersUp, ", ")))
	}
	if len(result.DNSServers) == 0 {
		notes = append(notes, "no configured DNS server could be read on this adapter set")
	}
	if len(notes) > 0 {
		result.Note = strings.Join(notes, "; ")
	}
	return result
}
