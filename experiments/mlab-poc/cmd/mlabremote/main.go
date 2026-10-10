// Command mlabremote is the EXPERIMENTAL entry point for a real M-Lab
// measurement through Locate discovery. It exists to prove that the P0-K-C
// safety gate works end to end; the experimental switch is DISABLED by
// default and this phase does not enable it for a real measurement.
//
// Flow: privacy consent (P0-J/B) -> Locate discovery, consent-gated and
// validated (P0-K-A/B) -> per-run risk confirmation (P0-K-C) -> one measured
// plan with wire-level budgets and an overall timeout (P0-J). The run never
// retries and never runs automatically in the background.
//
// Without -enable-remote-switch the command refuses every non-loopback
// target. Loopback targets are only reachable through cmd/mlabpoc, which
// stays the localhost-only demo runner.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	v2 "github.com/m-lab/locate/api/v2"

	mlabpoc "github.com/AbsoluteZero001/GoSpeed/experiments/mlab-poc/provider"
)

func main() {
	enableSwitch := flag.Bool("enable-remote-switch", false,
		"turn on the experimental remote switch (required for any non-loopback target)")
	locateURL := flag.String("locate-url", mlabpoc.DefaultLocateBaseURL,
		"Locate v2 base URL (point at a localhost mock for offline verification)")
	locateTimeoutMs := flag.Int64("locate-timeout-ms", int64(mlabpoc.DefaultLocateTimeout/time.Millisecond),
		"Locate HTTP timeout in milliseconds")
	direction := flag.String("direction", "download", "download | upload | both")
	downloadBudget := flag.Int64("download-budget-bytes", mlabpoc.DefaultDownloadBudgetBytes,
		"wire-level download byte budget")
	uploadBudget := flag.Int64("upload-budget-bytes", mlabpoc.DefaultUploadBudgetBytes,
		"wire-level upload byte budget")
	totalBudget := flag.Int64("total-budget-bytes", mlabpoc.DefaultTotalBudgetBytes,
		"wire-level budget across the whole plan")
	timeoutMs := flag.Int64("timeout-ms", int64(mlabpoc.DefaultOverallTimeout/time.Millisecond),
		"overall timeout per direction in milliseconds")
	flag.Parse()

	// The experimental switch exists only in this binary and starts disabled.
	sw := mlabpoc.NewRemoteExperimentSwitch()
	if *enableSwitch {
		fmt.Fprintln(os.Stderr, "warning: -enable-remote-switch turns on the EXPERIMENTAL public measurement path")
		sw.Enable()
	} else {
		fmt.Fprintln(os.Stderr, "remote switch is DISABLED (default); every non-loopback target will be refused")
	}

	// Privacy consent is always interactive and never pre-agreed. It gates
	// the Locate query as much as the measurement itself. Both prompts read
	// from one shared reader; readPromptLine consumes exactly one line so
	// piped answers reach both prompts.
	stdin := bufio.NewReader(os.Stdin)
	record, err := mlabpoc.PromptConsent(stdin, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: consent prompt failed: %v\n", err)
		os.Exit(1)
	}
	if !record.Agreed {
		fmt.Println("未获得同意，已取消。没有执行 Locate 查询，也没有建立任何测速连接。")
		return
	}

	var directions []mlabpoc.Direction
	switch strings.ToLower(*direction) {
	case "download":
		directions = []mlabpoc.Direction{mlabpoc.DirectionDownload}
	case "upload":
		directions = []mlabpoc.Direction{mlabpoc.DirectionUpload}
	case "both":
		directions = []mlabpoc.Direction{mlabpoc.DirectionDownload, mlabpoc.DirectionUpload}
	default:
		fmt.Fprintf(os.Stderr, "error: unsupported direction %q\n", *direction)
		os.Exit(2)
	}

	locateOpts := mlabpoc.LocateOptions{
		BaseURL:       *locateURL,
		Timeout:       time.Duration(*locateTimeoutMs) * time.Millisecond,
		Authorization: os.Getenv("MLAB_LOCATE_AUTHORIZATION"),
	}

	// Step 1: consent-gated discovery with strict production validation.
	fmt.Println("正在查询 M-Lab Locate（已获授权）…")
	targets, err := mlabpoc.DiscoverServers(context.Background(), &record, locateOpts)
	if err != nil {
		var classified *mlabpoc.ClassifiedError
		class := "unknown"
		if errors.As(err, &classified) {
			class = string(classified.Class)
		}
		fmt.Fprintf(os.Stderr, "error: discovery failed (%s): %v\n", class, err)
		os.Exit(1)
	}
	target := targets[0]
	fmt.Printf("发现节点 %s（machine %s），候选 URL：\n", target.Hostname, target.Machine)
	for key, value := range target.URLs {
		fmt.Printf("  %s -> %s\n", key, value)
	}

	// Step 2: derive the gate from the VALIDATED discovery result.
	gate := mlabpoc.NewRemoteGate(sw, targets)

	// Step 3: per-run confirmation with budget/timeout risk disclosure.
	planHost := ""
	if len(directions) > 0 {
		serviceURL, err := mlabpoc.TargetServiceURL(&target, "wss", directions[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		parsed, err := url.Parse(serviceURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		planHost = parsed.Hostname()
	}
	confirmation, err := mlabpoc.ConfirmRemoteRun(stdin, os.Stdout, planHost,
		*downloadBudget, *uploadBudget, *totalBudget,
		time.Duration(*timeoutMs)*time.Millisecond)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: confirmation prompt failed: %v\n", err)
		os.Exit(1)
	}
	if !confirmation.Confirmed {
		fmt.Println("未获得单次确认，已取消。没有建立任何测速连接。")
		return
	}
	gate.Confirm(confirmation)

	// Step 4: one plan, wire budgets, overall timeout, no retries.
	client, err := mlabpoc.NewClient(mlabpoc.Options{
		Scheme:              "wss",
		Consent:             &record,
		DownloadBudgetBytes: *downloadBudget,
		UploadBudgetBytes:   *uploadBudget,
		OverallTimeout:      time.Duration(*timeoutMs) * time.Millisecond,
		Discover: func(ctx context.Context) ([]v2.Target, error) {
			// Reuse the already-validated discovery result; the per-run
			// confirmation applies to exactly this target.
			return targets, nil
		},
		RemoteGate: gate,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	results := client.RunPlan(context.Background(), mlabpoc.Plan{
		Directions:       directions,
		TotalBudgetBytes: *totalBudget,
	})
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	for _, result := range results {
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "error: encode result: %v\n", err)
			os.Exit(1)
		}
	}
}
