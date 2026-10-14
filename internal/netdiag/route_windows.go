//go:build windows

package netdiag

import (
	"strconv"
	"time"
)

// platformTraceCommand returns the Windows tracert invocation.
//
// -d suppresses reverse DNS so the measurement contains no resolver latency,
// and -4 forces IPv4 because this package models an IPv4 route. The TTL ceiling
// comes from the caller, and -w sets the per probe wait in milliseconds, which
// is what tracert expects on Windows.
func platformTraceCommand() (routeCommand, bool) {
	return routeCommand{
		program: "tracert",
		method:  "windows_tracert",
		args: func(target string, maxHops int, timeout time.Duration) []string {
			wait := int(timeout.Milliseconds())
			if wait < 1 {
				wait = 1
			}
			return []string{
				"-d",
				"-4",
				"-h", strconv.Itoa(maxHops),
				"-w", strconv.Itoa(wait),
				target,
			}
		},
	}, true
}
