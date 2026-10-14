//go:build darwin

package netdiag

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// platformCommandTimeout bounds a system tool invocation so a stuck command
// cannot hang a diagnostic.
const platformCommandTimeout = 5 * time.Second

// platformGatewayAndDNS reads the IPv4 default gateway from netstat and the
// resolvers from scutil.
//
// macOS exposes neither in a proc style file, so both come from system tools
// that ship with the operating system. When a tool is missing or fails, the
// affected field is reported as unknown with a note; it is never guessed.
func platformGatewayAndDNS() platformNetwork {
	result := platformNetwork{DNSServers: []string{}}
	var notes []string

	gateway, iface, err := darwinDefaultGateway()
	if err != nil {
		notes = append(notes, "the default gateway could not be read on macOS: "+err.Error())
	} else if gateway != "" {
		result.Gateway = gateway
		result.Interface = iface
	} else {
		notes = append(notes,
			"no IPv4 default route was found in the routing table; "+
				"the gateway is reported as unknown rather than guessed")
	}

	servers, err := darwinResolvers()
	if err != nil {
		notes = append(notes, "the DNS servers could not be read on macOS: "+err.Error())
		result.DNSServers = []string{}
	} else {
		result.DNSServers = servers
	}
	if len(notes) > 0 {
		result.Note = strings.Join(notes, "; ")
	}
	return result
}

// darwinDefaultGateway reads the default route out of "netstat -rn".
//
// The destination column is spelled "default" on current releases and "0/0" on
// older ones, so both are accepted. The gateway column is "link#" for a direct
// route, which is not an address and must not be reported as one.
//
// When several default routes exist the lowest metric wins, matching how the
// system itself orders them on a machine with more than one uplink.
func darwinDefaultGateway() (gateway string, iface string, err error) {
	out, err := exec.Command("netstat", "-rn").Output()
	if err != nil {
		return "", "", err
	}
	bestMetric := ^int64(1 << 62)
	for index, line := range splitLines(string(out)) {
		if index == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[0] != "default" && fields[0] != "0/0" {
			continue
		}
		candidate := fields[1]
		if candidate == "" || strings.HasPrefix(candidate, "link#") {
			continue
		}
		metric := darwinMetric(fields[len(fields)-1])
		if gateway != "" && metric >= bestMetric {
			continue
		}
		bestMetric = metric
		gateway = candidate
		iface = fields[0]
	}
	return gateway, iface, nil
}

// darwinMetric parses a decimal metric column, treating an unparsable value as
// the least preferred so a readable route is never discarded over it.
func darwinMetric(value string) int64 {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 1 << 62
	}
	return parsed
}

// darwinResolvers reads the configured resolvers out of scutil.
func darwinResolvers() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), platformCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--dns").Output()
	if err != nil {
		return []string{}, err
	}
	servers := []string{}
	for _, line := range splitLines(string(out)) {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "nameserver[") {
			continue
		}
		open := strings.Index(trimmed, ":")
		if open < 0 {
			continue
		}
		if value := strings.TrimSpace(trimmed[open+1:]); value != "" {
			servers = appendUnique(servers, value)
		}
	}
	return servers, nil
}
