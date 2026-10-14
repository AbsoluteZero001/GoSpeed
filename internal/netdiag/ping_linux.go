//go:build linux

package netdiag

import (
	"context"
	"math"
	"os/exec"
	"strconv"
	"time"
)

// platformPing returns the Linux ping invocation.
//
// Linux spells -W in whole seconds, unlike Windows where the same flag is
// milliseconds, so the per probe timeout is rounded up. Rounding up can only
// make a probe wait longer than asked; rounding down would time out probes that
// would have answered and report them as lost.
func platformPing() (pingRunner, bool) {
	return func(ctx context.Context, host string, count int, perProbe, total time.Duration) (string, error) {
		seconds := int(math.Ceil(perProbe.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		cmd := exec.CommandContext(ctx, "ping",
			"-n",
			"-c", strconv.Itoa(count),
			"-W", strconv.Itoa(seconds),
			host)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}, true
}
