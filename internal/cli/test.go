package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"

	"github.com/AbsoluteZero001/GoSpeed/internal/config"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

func (a *App) runTest(ctx context.Context, args []string) int {
	defaults := config.Default()
	if err := defaults.Validate(); err != nil {
		return a.fail("%v", err)
	}

	set := a.newFlagSet("gospeed test")
	serverRef := set.String("server", "", "node ID or absolute base URL, for example http://127.0.0.1:8080")
	configPath := set.String("config", "", "node configuration file (defaults to configs/nodes.json, then configs/nodes.example.json)")
	duration := set.Duration("duration", defaults.Test.Duration, "transfer window per phase")
	timeout := set.Duration("timeout", defaults.Test.Timeout, "timeout per phase; must be longer than the duration")
	connections := set.Int("connections", defaults.Test.Connections, "parallel connections per transfer phase (v0.1.0 supports exactly 1)")
	latencySamples := set.Int("latency-samples", defaults.Test.LatencySamples, "number of HTTP RTT samples")
	latencyInterval := set.Duration("latency-interval", defaults.Test.LatencyInterval, "pause between HTTP RTT samples")
	maxBytes := set.Int64("max-bytes", defaults.Test.MaxBytes, "stop each transfer after this many bytes (0 = use the duration only)")
	warmup := set.Bool("warmup", defaults.Test.Warmup, "send one unmeasured /health request before the first phase")
	jsonOut := set.Bool("json", false, "write the result as JSON to stdout")
	if err := set.Parse(args); err != nil {
		return 2
	}

	target, err := a.resolveTarget(*serverRef, *configPath)
	if err != nil {
		return a.fail("%v", err)
	}
	if !*jsonOut {
		a.renderHeader(target)
	}

	options := speedtest.Options{
		Target:          target,
		Duration:        *duration,
		Timeout:         *timeout,
		Connections:     *connections,
		LatencySamples:  *latencySamples,
		LatencyInterval: *latencyInterval,
		MaxBytes:        *maxBytes,
		Warmup:          *warmup,
		Progress:        (&progressPrinter{out: a.Out, err: a.Err, jsonMode: *jsonOut}).handle,
	}
	engine := speedtest.NewEngine(nil)
	result, runErr := engine.Run(ctx, options)

	if *jsonOut {
		if result.TestID != "" {
			encoder := json.NewEncoder(a.Out)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(result); err != nil {
				return a.fail("encode result: %v", err)
			}
		}
	} else if result.TestID != "" {
		// A configuration error happens before a test id exists; printing an
		// empty result in that case would only add noise.
		a.renderResult(result)
	}
	if runErr != nil {
		fmt.Fprintf(a.Err, "Error: %v\n", runErr)
		if isConnectionRefused(runErr) && target.Local {
			fmt.Fprintln(a.Err, "Hint: the local test server is not running yet. Start it with: gospeed server")
		}
		if errors.Is(runErr, context.Canceled) {
			return 130
		}
		return 1
	}
	return 0
}

// renderHeader prints the banner and the target identity before any
// measurement starts, so a local loopback run is labelled from the first line.
func (a *App) renderHeader(target speedtest.Target) {
	fmt.Fprintln(a.Out, "====================================")
	fmt.Fprintln(a.Out, "              GoSpeed")
	fmt.Fprintln(a.Out, "         Network Speed Test")
	fmt.Fprintln(a.Out, "====================================")
	fmt.Fprintf(a.Out, "Server:   %s (%s)\n", target.Name, target.ID)
	fmt.Fprintf(a.Out, "Address:  %s\n", target.BaseURL)
	fmt.Fprintf(a.Out, "Protocol: %s\n", target.Protocol)
	if target.Local {
		fmt.Fprintln(a.Out, "Mode:     Local Loopback Test - does not represent internet bandwidth")
	}
}

// progressPrinter renders engine events. State is kept per test run, and the
// mutex makes the printer safe if a future phase reports from its own
// goroutine.
type progressPrinter struct {
	out      io.Writer
	err      io.Writer
	jsonMode bool

	mu   sync.Mutex
	line bool
}

// handle prints live counters. In JSON mode the human progress moves to stderr
// so stdout stays a single JSON document.
func (p *progressPrinter) handle(event speedtest.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jsonMode {
		if event.Stage == speedtest.StageStart {
			fmt.Fprintf(p.err, "%s...\n", event.Phase)
		}
		return
	}
	switch event.Stage {
	case speedtest.StageStart:
		fmt.Fprintf(p.out, "\nTesting %s...\n", event.Phase)
	case speedtest.StageProgress:
		if event.Phase == speedtest.PhaseLatency {
			return
		}
		if event.Mbps <= 0 {
			// The platform clock had not advanced yet; printing 0.00 Mbps
			// would look like a measurement instead of a timing floor.
			return
		}
		fmt.Fprintf(p.out, "\r  %-8s %10.2f Mbps   %s transferred",
			event.Phase, event.Mbps, humanBytes(event.Bytes))
		p.line = true
	case speedtest.StageDone:
		if event.Phase == speedtest.PhaseLatency {
			fmt.Fprintf(p.out, "  HTTP RTT avg: %s\n", speedtest.FormatMillis(event.Elapsed))
			return
		}
		if p.line {
			fmt.Fprintln(p.out)
			p.line = false
		}
	}
}

func (a *App) renderResult(result speedtest.Result) {
	fmt.Fprintln(a.Out)
	fmt.Fprintln(a.Out, "====================================")
	fmt.Fprintf(a.Out, "Test ID:  %s\n", valueOrNA(result.TestID))
	fmt.Fprintf(a.Out, "Status:   %s\n", valueOrNA(string(result.Status)))
	if result.ErrorMessage != "" {
		fmt.Fprintf(a.Out, "Error:    %s\n", result.ErrorMessage)
	}
	fmt.Fprintf(a.Out, "Server:   %s (%s)\n", valueOrNA(result.Target.ServerName), valueOrNA(result.Target.ServerID))
	fmt.Fprintf(a.Out, "Address:  %s\n", valueOrNA(result.Target.ServerAddress))
	fmt.Fprintf(a.Out, "Protocol: %s\n", valueOrNA(string(result.Target.Protocol)))
	if result.Target.Local {
		fmt.Fprintln(a.Out, "Mode:     Local Loopback Test - does not represent internet bandwidth")
	}
	fmt.Fprintln(a.Out)

	if result.Latency != nil {
		latency := result.Latency
		fmt.Fprintf(a.Out, "Latency:  %s min / %s avg / %s max (%d/%d HTTP RTT samples)\n",
			speedtest.FormatMillis(latency.MinNs),
			speedtest.FormatMillis(latency.AverageNs),
			speedtest.FormatMillis(latency.MaxNs),
			latency.SuccessfulSamples,
			latency.Attempts)
		if latency.JitterNs != nil {
			fmt.Fprintf(a.Out, "Jitter:   %s (mean absolute difference of consecutive HTTP RTT samples)\n",
				speedtest.FormatMillis(*latency.JitterNs))
		} else {
			fmt.Fprintln(a.Out, "Jitter:   N/A (fewer than two samples)")
		}
	} else {
		fmt.Fprintln(a.Out, "Latency:  N/A (not measured)")
		fmt.Fprintln(a.Out, "Jitter:   N/A (not measured)")
	}
	fmt.Fprintln(a.Out, "Packet loss: N/A (not measured in v0.1.0)")

	if result.Download != nil {
		download := result.Download
		fmt.Fprintf(a.Out, "Download: %s (%.2f MB/s, %s in %.2f s, %d connection)\n",
			speedtest.FormatMbps(download.Mbps),
			download.MBPerSecond,
			humanBytes(download.Bytes),
			download.DurationNs.Seconds(),
			download.Connections)
	} else {
		fmt.Fprintln(a.Out, "Download: N/A (not measured)")
	}
	if result.Upload != nil {
		upload := result.Upload
		fmt.Fprintf(a.Out, "Upload:   %s (%.2f MB/s, %s in %.2f s, server confirmed %s)\n",
			speedtest.FormatMbps(upload.Mbps),
			upload.MBPerSecond,
			humanBytes(upload.Bytes),
			upload.DurationNs.Seconds(),
			humanBytes(upload.ServerConfirmedBytes))
	} else {
		fmt.Fprintln(a.Out, "Upload:   N/A (not measured)")
	}
	fmt.Fprintln(a.Out, "====================================")
}

func valueOrNA(value string) string {
	if strings.TrimSpace(value) == "" {
		return "N/A"
	}
	return value
}

// isConnectionRefused detects the common "server is not running" failures so
// the CLI can print an actionable hint instead of only the raw error.
func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection refused") || strings.Contains(message, "actively refused")
}
