//go:build linux

package netdiag

import (
	"bufio"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// /proc/net/route column positions. The header the kernel prints is:
//
//	Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
//
// Only Destination, Gateway, Metric and Mask are read. A line with fewer
// columns is skipped, so a truncated or unexpectedly extended table cannot
// shift one column into another and produce a wrong gateway.
const (
	routeColumnInterface   = 0
	routeColumnDestination = 1
	routeColumnGateway     = 2
	routeColumnMetric      = 6
	routeColumnMask        = 7
	routeMinimumColumns    = 8
)

// platformGatewayAndDNS reads the IPv4 default gateway from /proc/net/route and
// the resolvers from /proc/resolv.conf.
//
// Both files are a stable part of the Linux proc filesystem and both are plain
// text, so this needs no dependency and no external program. A file that is
// missing or unreadable yields Unknown with a note rather than a guess.
//
// The gateway and the interface name come from the same route row, so the two
// can never end up describing different adapters.
func platformGatewayAndDNS() platformNetwork {
	result := platformNetwork{DNSServers: []string{}}

	gateway, iface, found := linuxDefaultGateway()
	if found {
		result.Gateway = gateway
		result.Interface = iface
	}
	result.DNSServers = linuxResolvers()

	var notes []string
	if !found {
		notes = append(notes,
			"no IPv4 default route (0.0.0.0/0) was found in /proc/net/route; "+
				"the gateway is reported as unknown rather than guessed")
	}
	if len(result.DNSServers) == 0 {
		notes = append(notes, "no nameserver line could be read from /proc/resolv.conf")
	}
	if len(notes) > 0 {
		result.Note = strings.Join(notes, "; ")
	}
	return result
}

// linuxDefaultGateway returns the next hop and interface of the IPv4 default
// route.
//
// The gateway column is a 32 bit word printed as eight hex digits in host byte
// order. Reading those digits straight out as an address would yield something
// that looks like a plausible address and points elsewhere, so the word is
// decoded and byte swapped back into network order.
//
// A host with several uplinks can carry more than one default route; the lowest
// Metric wins, which is how the kernel itself orders them.
func linuxDefaultGateway() (gateway string, iface string, found bool) {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return "", "", false
	}
	defer file.Close()

	bestMetric := ^uint32(0)
	scanner := bufio.NewScanner(file)
	for lineNumber := 0; scanner.Scan(); lineNumber++ {
		if lineNumber == 0 {
			continue // header
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < routeMinimumColumns {
			continue
		}
		if fields[routeColumnDestination] != "00000000" {
			continue
		}
		if fields[routeColumnMask] != "00000000" {
			continue
		}
		metric := routeMetric(fields[routeColumnMetric])
		if found && metric >= bestMetric {
			continue
		}
		address, ok := decodeRouteHexWord(fields[routeColumnGateway])
		if !ok {
			continue
		}
		addr := netip.AddrFrom4(address)
		if addr.IsUnspecified() || addr.IsMulticast() {
			continue
		}
		bestMetric = metric
		gateway = addr.String()
		iface = fields[routeColumnInterface]
		found = true
	}
	return gateway, iface, found
}

// routeMetric parses a decimal metric column.
func routeMetric(value string) uint32 {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(parsed)
}

// decodeRouteHexWord converts one eight digit /proc/net/route hex word into a
// network order address.
func decodeRouteHexWord(value string) ([4]byte, bool) {
	var out [4]byte
	word, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return out, false
	}
	out[0] = byte(word)
	out[1] = byte(word >> 8)
	out[2] = byte(word >> 16)
	out[3] = byte(word >> 24)
	return out, true
}

// linuxResolvers reads the nameserver entries from /proc/resolv.conf.
func linuxResolvers() []string {
	file, err := os.Open("/proc/resolv.conf")
	if err != nil {
		return nil
	}
	defer file.Close()

	servers := []string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		if ip := net.ParseIP(fields[1]); ip != nil {
			servers = appendUnique(servers, ip.String())
		}
	}
	return servers
}
