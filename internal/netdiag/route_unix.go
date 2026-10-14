//go:build !windows

package netdiag

import (
	"math"
	"strconv"
	"time"
)

// platformTraceCommand returns the Unix traceroute invocation, shared by Linux
// and macOS.
//
// -n suppresses name resolution, -4 forces IPv4, -m bounds the TTL and -w sets
// the per probe wait in whole seconds. The wait is rounded up because rounding
// down would abandon probes that would still have answered inside the caller's
// timeout and report them as lost.
func platformTraceCommand() (routeCommand, bool) {
	return routeCommand{
		program: "traceroute",
		method:  "unix_traceroute",
		args: func(target string, maxHops int, timeout time.Duration) []string {
			seconds := int(math.Ceil(timeout.Seconds()))
			if seconds < 1 {
				seconds = 1
			}
			return []string{
				"-n",
				"-4",
				"-m", strconv.Itoa(maxHops),
				"-w", strconv.Itoa(seconds),
				target,
			}
		},
	}, true
}
