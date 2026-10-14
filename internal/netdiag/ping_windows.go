//go:build windows

package netdiag

import (
	"context"
	"os/exec"
	"strconv"
	"time"
)

// platformPing returns the Windows ping invocation.
//
// -n is the count on Windows and -w is the per probe timeout in milliseconds.
// The hostname is passed as a separate argument and never interpolated into a
// shell string, so a target cannot inject a command.
func platformPing() (pingRunner, bool) {
	return func(ctx context.Context, host string, count int, perProbe, total time.Duration) (string, error) {
		wait := int(perProbe.Milliseconds())
		if wait < 1 {
			wait = 1
		}
		cmd := exec.CommandContext(ctx, "ping",
			"-n", strconv.Itoa(count),
			"-w", strconv.Itoa(wait),
			host)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}, true
}
