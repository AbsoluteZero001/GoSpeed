//go:build darwin

package netdiag

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

// platformPing returns the macOS ping invocation.
//
// macOS spells -W in milliseconds, like Windows, and needs -t as well to bound
// the overall run; without it a stuck probe can outlive the caller's context
// budget.
func platformPing() (pingRunner, bool) {
	return func(ctx context.Context, host string, count int, perProbe, total time.Duration) (string, error) {
		milliseconds := int(perProbe.Milliseconds())
		if milliseconds < 1 {
			milliseconds = 1
		}
		cmd := exec.CommandContext(ctx, "ping",
			"-n",
			"-c", strconv.Itoa(count),
			"-W", strconv.Itoa(milliseconds),
			"-t", strconv.Itoa(milliseconds),
			host)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}, true
}
