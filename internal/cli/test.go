package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/config"
	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

// maxRepeat bounds --repeat so a typo cannot start an accidental multi-hour
// traffic bill.
const maxRepeat = 10

func (a *App) runTest(ctx context.Context, args []string) int {
	defaults := config.Default()
	if err := defaults.Validate(); err != nil {
		return a.fail("%v", err)
	}

	set := a.newFlagSet("gospeed test")
	serverRef := set.String("server", "", "node ID or absolute base URL, for example http://127.0.0.1:8080")
	nodeRef := set.String("node", "", "node ID to test (same as --server <id>)")
	autoSelect := set.Bool("auto", false, "probe the enabled nodes, select the best candidate and test it")
	configPath := set.String("config", "", "node configuration file (defaults to configs/nodes.json, then configs/nodes.example.json)")
	duration := set.Duration("duration", defaults.Test.Duration, "transfer window per phase")
	timeout := set.Duration("timeout", defaults.Test.Timeout, "timeout per phase; must be longer than the duration")
	connections := set.Int("connections", defaults.Test.Connections,
		"parallel connections per transfer phase (1..16; 1, 4, 8 and 16 are the end to end tested values)")
	sampleInterval := set.Duration("sample-interval", defaults.Test.SampleInterval, "real time rate sampling interval")
	latencySamples := set.Int("latency-samples", defaults.Test.LatencySamples, "number of HTTP RTT samples")
	latencyInterval := set.Duration("latency-interval", defaults.Test.LatencyInterval, "pause between HTTP RTT samples")
	maxBytes := set.Int64("max-bytes", defaults.Test.MaxBytes, "stop each transfer after this many bytes in total (0 = use the duration only)")
	warmup := set.Bool("warmup", defaults.Test.Warmup, "send one unmeasured /health request before the first phase")
	negotiate := set.Bool("capabilities", true, "negotiate GET /capabilities and honour the server limits (servers without the endpoint are accepted)")
	repeat := set.Int("repeat", 1, fmt.Sprintf("run the test N times sequentially and aggregate the completed runs (1..%d)", maxRepeat))
	jsonOut := set.Bool("json", false, "write the result as JSON to stdout")
	if err := set.Parse(args); err != nil {
		return 2
	}
	if *serverRef != "" && *nodeRef != "" {
		return a.fail("--server and --node are mutually exclusive")
	}
	if *autoSelect && (*serverRef != "" || *nodeRef != "") {
		return a.fail("--auto cannot be combined with --server or --node")
	}
	if *connections < 1 || *connections > speedtest.MaxConnections {
		return a.fail("--connections must be between 1 and %d, got %d", speedtest.MaxConnections, *connections)
	}
	if *sampleInterval <= 0 {
		return a.fail("--sample-interval must be positive, got %s", *sampleInterval)
	}
	if *timeout <= *duration {
		return a.fail("--timeout %s must be longer than --duration %s", *timeout, *duration)
	}
	if *repeat < 1 || *repeat > maxRepeat {
		return a.fail("--repeat must be between 1 and %d", maxRepeat)
	}

	var target speedtest.Target
	var err error
	switch {
	case *autoSelect:
		target, err = a.autoTarget(ctx, *configPath)
	case *nodeRef != "":
		target, err = a.resolveTarget(*nodeRef, *configPath)
	default:
		target, err = a.resolveTarget(*serverRef, *configPath)
	}
	if err != nil {
		return a.fail("%v", err)
	}
	if !*jsonOut {
		a.renderHeader(target, *connections)
	}

	options := speedtest.Options{
		Target:                target,
		Duration:              *duration,
		Timeout:               *timeout,
		Connections:           *connections,
		SampleInterval:        *sampleInterval,
		LatencySamples:        *latencySamples,
		LatencyInterval:       *latencyInterval,
		MaxBytes:              *maxBytes,
		Warmup:                *warmup,
		NegotiateCapabilities: *negotiate,
	}
	engine := speedtest.NewEngine(nil)
	results := make([]speedtest.Result, 0, *repeat)
	exitCode := 0

	for run := 1; run <= *repeat; run++ {
		printer := newProgressPrinter(a.Out, a.Err, *jsonOut)
		options.Progress = printer.handle
		if *repeat > 1 && !*jsonOut {
			fmt.Fprintf(a.Out, "\n----- Run %d/%d -----\n", run, *repeat)
		}
		result, runErr := engine.Run(ctx, options)
		if result.TestID != "" {
			results = append(results, result)
		}
		if runErr != nil {
			if *repeat == 1 {
				fmt.Fprintf(a.Err, "Error: %v\n", runErr)
			} else {
				fmt.Fprintf(a.Err, "Run %d/%d error: %v\n", run, *repeat, runErr)
			}
			if errors.Is(runErr, context.Canceled) {
				// Still print the cancelled result before exiting with the
				// conventional signal exit code.
				exitCode = 130
				break
			}
			exitCode = 1
		}
	}

	if len(results) == 0 {
		// The failure was already reported per run.
		return exitCode
	}
	if *repeat == 1 {
		result := results[0]
		if *jsonOut {
			if err := encodeResult(a, result); err != nil {
				return a.fail("encode result: %v", err)
			}
		} else {
			a.renderResult(result)
		}
		return exitCode
	}
	summary, err := speedtest.SummarizeRuns(results)
	if err != nil {
		return a.fail("summarize runs: %v", err)
	}
	if *jsonOut {
		return a.encodeJSON(speedtest.BatchResult{Summary: summary, Results: results})
	}
	a.renderSummary(summary)
	return exitCode
}

func encodeResult(a *App, result speedtest.Result) error {
	encoder := json.NewEncoder(a.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

// autoTarget probes the enabled nodes, ranks them with measured data and
// returns the selected target with the reason attached.
func (a *App) autoTarget(ctx context.Context, configPath string) (speedtest.Target, error) {
	manager, _, err := a.loadNodes(configPath)
	if err != nil {
		return speedtest.Target{}, err
	}
	list := enabledNodes(manager.List())
	if len(list) == 0 {
		return speedtest.Target{}, errors.New("no enabled node is configured; enable one with \"gospeed nodes enable <id>\"")
	}
	probes, err := manager.CheckAll(ctx, nil, nodes.CheckOptions{
		ProbeOptions: nodes.ProbeOptions{
			Attempts:       2,
			Timeout:        defaultProbeTimeout,
			LatencySamples: 3,
		},
		Parallel: defaultProbeParallel,
	})
	if err != nil {
		return speedtest.Target{}, err
	}
	probes = filterProbes(probes, list)
	selection, err := nodes.SelectAuto(list, probes)
	if err != nil {
		return speedtest.Target{}, err
	}
	target := nodeTarget(selection.Node)
	target.SelectionMethod = string(selection.Method)
	target.SelectionReason = selection.Reason
	target.HealthStatus = string(selection.Probe.Status)
	if latency, ok := selection.Probe.SelectionLatency(); ok {
		target.HealthLatency = latency
	}
	return target, nil
}

// renderHeader prints the banner and the target identity before any
// measurement starts, so a local loopback run is labelled from the first line.
func (a *App) renderHeader(target speedtest.Target, connections int) {
	fmt.Fprintln(a.Out, "====================================")
	fmt.Fprintf(a.Out, "           GoSpeed v%s\n", version.Version)
	fmt.Fprintln(a.Out, "         Network Speed Test")
	fmt.Fprintln(a.Out, "====================================")
	fmt.Fprintf(a.Out, "Server:      %s (%s)\n", target.Name, target.ID)
	fmt.Fprintf(a.Out, "Address:     %s\n", target.BaseURL)
	fmt.Fprintf(a.Out, "Protocol:    %s\n", target.Protocol)
	fmt.Fprintf(a.Out, "Scope:       %s\n", target.NetworkScope())
	fmt.Fprintf(a.Out, "Connections: %d\n", connections)
	if target.SelectionMethod != "" {
		fmt.Fprintf(a.Out, "Selection:   %s\n", target.SelectionMethod)
	}
	if target.SelectionReason != "" {
		fmt.Fprintf(a.Out, "Selected by: %s\n", target.SelectionReason)
	}
	if target.Local {
		fmt.Fprintln(a.Out, "Mode:        Local Loopback Test - does not represent internet bandwidth")
	}
}

// isTerminalWriter reports whether w is an interactive console. Dynamic
// progress refresh is only used there; redirected output gets plain lines so
// logs and pipes stay readable on every platform, including PowerShell.
func isTerminalWriter(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

const progressBarWidth = 20

// progressPrinter renders engine events. State is kept per test run, and the
// mutex keeps the printer safe when a phase reports from its own goroutine.
type progressPrinter struct {
	out      io.Writer
	err      io.Writer
	jsonMode bool
	dynamic  bool

	mu           sync.Mutex
	lastWidth    int
	plainPrinted bool
	lastPlainAt  time.Duration
}

func newProgressPrinter(out, err io.Writer, jsonMode bool) *progressPrinter {
	return &progressPrinter{
		out:      out,
		err:      err,
		jsonMode: jsonMode,
		dynamic:  !jsonMode && isTerminalWriter(out),
	}
}

// handle renders one engine event. In JSON mode stdout stays a single JSON
// document and the human readable progress moves to stderr.
func (p *progressPrinter) handle(event speedtest.Progress) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.jsonMode {
		if event.Stage == speedtest.StageStart && event.Phase != "" {
			fmt.Fprintf(p.err, "%s...\n", event.Phase)
		}
		return
	}
	switch event.Stage {
	case speedtest.StageStart:
		if event.Phase == "" {
			return
		}
		fmt.Fprintf(p.out, "\nTesting %s...\n", event.Phase)
	case speedtest.StageProgress:
		p.renderProgress(event)
	case speedtest.StageDone:
		if event.Phase == "" {
			// Terminal run state event; the result summary follows.
			return
		}
		if event.Phase == speedtest.PhaseLatency {
			fmt.Fprintf(p.out, "  HTTP RTT avg: %s\n", speedtest.FormatMillis(event.Elapsed))
			return
		}
		p.finishTransferLine(event)
	}
}

func (p *progressPrinter) renderProgress(event speedtest.Progress) {
	if event.Phase == speedtest.PhaseLatency || event.Mbps <= 0 {
		// A zero rate means the platform clock had not advanced yet; printing
		// 0.00 Mbps would look like a measurement instead of a timing floor.
		return
	}
	line := formatTransferProgress(event)
	if p.dynamic {
		p.writeLive(line)
		return
	}
	// Redirected output gets at most one line per second instead of a wall of
	// carriage return updates.
	if !p.plainPrinted || event.Elapsed-p.lastPlainAt >= time.Second {
		p.plainPrinted = true
		p.lastPlainAt = event.Elapsed
		fmt.Fprintln(p.out, line)
	}
}

func (p *progressPrinter) finishTransferLine(event speedtest.Progress) {
	line := fmt.Sprintf("%-8s %s   %s in %.2f s   %d connection(s)",
		event.Phase,
		speedtest.FormatMbps(event.Mbps),
		humanBytes(event.Bytes),
		event.Elapsed.Seconds(),
		event.ActiveConnections)
	if p.dynamic {
		p.writeLive(line)
		fmt.Fprintln(p.out)
		p.lastWidth = 0
		return
	}
	fmt.Fprintln(p.out, line)
}

func (p *progressPrinter) writeLive(line string) {
	width := p.lastWidth
	if len(line) > width {
		width = len(line)
	}
	fmt.Fprintf(p.out, "\r%-*s", width, line)
	p.lastWidth = width
}

// formatTransferProgress builds the periodic line: optional bar, real time
// instantaneous rate, window average, active connections and, when it can be
// estimated, the remaining time.
func formatTransferProgress(event speedtest.Progress) string {
	parts := make([]string, 0, 5)
	if bar := formatProgressBar(event.Budget); bar != "" {
		parts = append(parts, bar)
	}
	if event.InstantMbps > 0 {
		parts = append(parts, "current "+speedtest.FormatMbps(event.InstantMbps))
	}
	parts = append(parts, "average "+speedtest.FormatMbps(event.Mbps))
	if event.ActiveConnections > 0 {
		parts = append(parts, fmt.Sprintf("%d conn", event.ActiveConnections))
	}
	if remaining := formatRemaining(event.Budget); remaining != "" {
		parts = append(parts, "eta "+remaining)
	}
	return fmt.Sprintf("%-8s %s", event.Phase, strings.Join(parts, "  "))
}

func formatProgressBar(budget speedtest.Budget) string {
	if budget.Fraction == nil {
		return ""
	}
	fraction := *budget.Fraction
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(fraction*progressBarWidth + 0.5)
	if filled > progressBarWidth {
		filled = progressBarWidth
	}
	return fmt.Sprintf("[%s%s] %3.0f%%",
		strings.Repeat("#", filled),
		strings.Repeat("-", progressBarWidth-filled),
		fraction*100)
}

func formatRemaining(budget speedtest.Budget) string {
	if budget.Remaining == nil {
		return ""
	}
	remaining := *budget.Remaining
	if remaining < 0 {
		remaining = 0
	}
	if remaining >= time.Second {
		return fmt.Sprintf("%.1fs", remaining.Seconds())
	}
	return fmt.Sprintf("%dms", remaining.Milliseconds())
}

func (a *App) renderResult(result speedtest.Result) {
	fmt.Fprintln(a.Out)
	fmt.Fprintln(a.Out, "====================================")
	fmt.Fprintf(a.Out, "Test ID:     %s\n", valueOrNA(result.TestID))
	fmt.Fprintf(a.Out, "Status:      %s\n", valueOrNA(string(result.Status)))
	if result.ErrorMessage != "" {
		fmt.Fprintf(a.Out, "Error:       %s\n", result.ErrorMessage)
	}
	fmt.Fprintf(a.Out, "Server:      %s (%s)\n", valueOrNA(result.Target.ServerName), valueOrNA(result.Target.ServerID))
	fmt.Fprintf(a.Out, "Address:     %s\n", valueOrNA(result.Target.ServerAddress))
	fmt.Fprintf(a.Out, "Protocol:    %s\n", valueOrNA(string(result.Target.Protocol)))
	fmt.Fprintf(a.Out, "Scope:       %s\n", valueOrNA(string(result.Target.NetworkScope)))
	fmt.Fprintf(a.Out, "Connections: %d requested\n", result.Settings.Connections)
	fmt.Fprintf(a.Out, "Sampling:    %s\n", result.Settings.SampleInterval)
	if result.Target.SelectionMethod != "" {
		fmt.Fprintf(a.Out, "Selection:   %s\n", result.Target.SelectionMethod)
	}
	if result.Target.SelectionReason != "" {
		fmt.Fprintf(a.Out, "Selected by: %s\n", result.Target.SelectionReason)
	}
	if result.Target.HealthStatus != "" {
		health := result.Target.HealthStatus
		if result.Target.HealthLatencyNs > 0 {
			health = fmt.Sprintf("%s (last request %s)", health, speedtest.FormatMillis(result.Target.HealthLatencyNs))
		}
		fmt.Fprintf(a.Out, "Node health: %s\n", health)
	}
	if capabilities := result.Target.Capabilities; capabilities != nil {
		if capabilities.Supported {
			fmt.Fprintf(a.Out, "Capabilities: protocol %d, server %s, %s\n",
				capabilities.ProtocolVersion,
				valueOrNA(capabilities.ServerVersion),
				strings.Join(capabilities.Capabilities, "/"))
		} else if capabilities.Error != "" {
			fmt.Fprintf(a.Out, "Capabilities: not negotiated (%s)\n", capabilities.Error)
		}
	}
	if result.Target.Local {
		fmt.Fprintln(a.Out, "Mode:        Local Loopback Test - does not represent internet bandwidth")
	}
	fmt.Fprintln(a.Out)

	a.renderLatency(result.Latency)
	a.renderTransfer("Download", result.Download)
	a.renderUpload(result.Upload)
	for _, warning := range result.Warnings {
		fmt.Fprintf(a.Out, "Warning: %s\n", warning)
	}
	fmt.Fprintln(a.Out, "====================================")
}

func (a *App) renderLatency(latency *speedtest.LatencyResult) {
	if latency == nil {
		fmt.Fprintln(a.Out, "Latency:  N/A (not measured)")
		fmt.Fprintln(a.Out, "Jitter:   N/A (not measured)")
	} else {
		fmt.Fprintf(a.Out, "Latency:  %s min / %s avg / %s max (%d/%d %s samples)\n",
			speedtest.FormatMillis(latency.MinNs),
			speedtest.FormatMillis(latency.AverageNs),
			speedtest.FormatMillis(latency.MaxNs),
			latency.SuccessfulSamples,
			latency.Attempts,
			latency.Type)
		if latency.JitterNs != nil {
			fmt.Fprintf(a.Out, "Jitter:   %s (mean absolute difference of consecutive HTTP RTT samples)\n",
				speedtest.FormatMillis(*latency.JitterNs))
		} else {
			fmt.Fprintln(a.Out, "Jitter:   N/A (fewer than two samples)")
		}
	}
	fmt.Fprintln(a.Out, "Packet loss: N/A (no packet level test is implemented)")
}

// renderTransfer prints the aggregate and connections detail that make the
// number verifiable. Per connection evidence lives in the JSON result.
func (a *App) renderTransfer(label string, transfer *speedtest.TransferResult) {
	if transfer == nil {
		fmt.Fprintf(a.Out, "%-9s N/A (not measured)\n", label+":")
		return
	}
	fmt.Fprintf(a.Out, "%-9s %s (%.2f MB/s, %s in %.2f s, %d connection(s))\n",
		label+":",
		speedtest.FormatMbps(transfer.Mbps),
		transfer.MBPerSecond,
		humanBytes(transfer.Bytes),
		transfer.DurationNs.Seconds(),
		transfer.Connections)
	fmt.Fprintf(a.Out, "  Active:  %d of %d connections (%d failed)\n",
		transfer.ActiveConnections, transfer.Connections, transfer.FailedConnections)
	fmt.Fprintf(a.Out, "  Window:  %s (%s)\n", transfer.MeasurementWindow, transfer.StopReason)
	fmt.Fprintf(a.Out, "  Samples: %s\n", formatStatistics(transfer.Statistics))
}

func (a *App) renderUpload(upload *speedtest.UploadResult) {
	if upload == nil {
		fmt.Fprintln(a.Out, "Upload:   N/A (not measured)")
		return
	}
	fmt.Fprintf(a.Out, "%-9s %s (%.2f MB/s, %s in %.2f s, %d connection(s))\n",
		"Upload:",
		speedtest.FormatMbps(upload.Mbps),
		upload.MBPerSecond,
		humanBytes(upload.Bytes),
		upload.DurationNs.Seconds(),
		upload.Connections)
	fmt.Fprintf(a.Out, "  Server confirmed %s (client sent %s)\n",
		humanBytes(upload.ServerConfirmedBytes), humanBytes(upload.Bytes))
	fmt.Fprintf(a.Out, "  Active:  %d of %d connections (%d failed)\n",
		upload.ActiveConnections, upload.Connections, upload.FailedConnections)
	fmt.Fprintf(a.Out, "  Window:  %s (%s)\n", upload.MeasurementWindow, upload.StopReason)
	fmt.Fprintf(a.Out, "  Samples: %s\n", formatStatistics(upload.Statistics))
}

// formatStatistics renders the descriptive statistics of the real time
// samples. Statistics that cannot be computed are shown as N/A, not zero.
func formatStatistics(stats speedtest.Statistics) string {
	if stats.Samples == 0 {
		return "N/A (no sample was collected before the phase ended)"
	}
	parts := []string{fmt.Sprintf("%d samples", stats.Samples)}
	if stats.MedianMbps != nil {
		parts = append(parts, "median "+speedtest.FormatMbps(*stats.MedianMbps))
	}
	if stats.StdDevMbps != nil {
		parts = append(parts, fmt.Sprintf("stddev %s (%s)", speedtest.FormatMbps(*stats.StdDevMbps), stats.StdDevKind))
	}
	if stats.CoefficientOfVariationPercent != nil {
		parts = append(parts, fmt.Sprintf("cv %.2f%%", *stats.CoefficientOfVariationPercent))
	}
	return strings.Join(parts, ", ")
}

// renderSummary prints the aggregate of a repeated test: one line per run plus
// the statistics of the completed runs only.
func (a *App) renderSummary(summary speedtest.Summary) {
	fmt.Fprintln(a.Out)
	fmt.Fprintln(a.Out, "====================================")
	fmt.Fprintf(a.Out, "GoSpeed v%s - %d run(s)\n", version.Version, summary.Runs)
	fmt.Fprintf(a.Out, "Server:      %s (%s)\n", valueOrNA(summary.NodeName), valueOrNA(summary.NodeID))
	fmt.Fprintf(a.Out, "Address:     %s\n", valueOrNA(summary.ServerAddress))
	fmt.Fprintf(a.Out, "Protocol:    %s\n", valueOrNA(string(summary.Protocol)))
	fmt.Fprintf(a.Out, "Scope:       %s\n", valueOrNA(summary.NetworkScope))
	fmt.Fprintf(a.Out, "Connections: %d\n", summary.Connections)
	fmt.Fprintf(a.Out, "Completed:   %d, failed: %d, cancelled: %d\n",
		summary.Completed, summary.Failed, summary.Cancelled)
	fmt.Fprintln(a.Out)

	fmt.Fprintf(a.Out, "%-4s %-10s %-18s %-18s %-14s %s\n",
		"RUN", "STATUS", "DOWNLOAD", "UPLOAD", "RTT", "WARNINGS")
	for _, run := range summary.RunDetails {
		fmt.Fprintf(a.Out, "%-4d %-10s %-18s %-18s %-14s %d\n",
			run.Index+1,
			run.Status,
			optionalMbps(run.DownloadMbps),
			optionalMbps(run.UploadMbps),
			optionalMillis(run.LatencyAverageNs),
			len(run.Warnings))
	}
	fmt.Fprintln(a.Out)
	fmt.Fprintf(a.Out, "Download Mbps: %s\n", formatMetricStats(summary.DownloadMbps))
	fmt.Fprintf(a.Out, "Upload Mbps:   %s\n", formatMetricStats(summary.UploadMbps))
	fmt.Fprintf(a.Out, "Latency ms:    %s\n", formatMetricStats(summary.LatencyMs))
	fmt.Fprintf(a.Out, "Jitter ms:     %s\n", formatMetricStats(summary.JitterMs))
	for _, warning := range summary.Warnings {
		fmt.Fprintf(a.Out, "Warning: %s\n", warning)
	}
	fmt.Fprintln(a.Out, "Statistics cover the completed runs of this command only; different nodes,")
	fmt.Fprintln(a.Out, "connection counts or durations are never mixed into one population.")
	fmt.Fprintln(a.Out, "====================================")
}

func formatMetricStats(stats speedtest.MetricStats) string {
	if stats.Samples == 0 {
		return "N/A (no completed run produced this metric)"
	}
	parts := []string{fmt.Sprintf("%d value(s)", stats.Samples)}
	if stats.Mean != nil {
		parts = append(parts, fmt.Sprintf("mean %.2f", *stats.Mean))
	}
	if stats.Median != nil {
		parts = append(parts, fmt.Sprintf("median %.2f", *stats.Median))
	}
	if stats.Min != nil {
		parts = append(parts, fmt.Sprintf("min %.2f", *stats.Min))
	}
	if stats.Max != nil {
		parts = append(parts, fmt.Sprintf("max %.2f", *stats.Max))
	}
	if stats.StdDev != nil {
		parts = append(parts, fmt.Sprintf("stddev %.2f (%s)", *stats.StdDev, stats.StdDevKind))
	}
	if stats.CVPercent != nil {
		parts = append(parts, fmt.Sprintf("cv %.2f%%", *stats.CVPercent))
	}
	return strings.Join(parts, ", ")
}

func optionalMbps(value *float64) string {
	if value == nil {
		return "N/A"
	}
	return speedtest.FormatMbps(*value)
}

func optionalMillis(value *time.Duration) string {
	if value == nil {
		return "N/A"
	}
	return speedtest.FormatMillis(*value)
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
