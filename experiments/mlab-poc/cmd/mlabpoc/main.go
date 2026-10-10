// Command mlabpoc is a localhost-only demo CLI for the GoSpeed M-Lab NDT7
// experiment module. It enforces the P0-J constraints:
//
//   - Interactive privacy consent is required before any connection.
//   - Only loopback (localhost mock) targets are accepted; this tool cannot
//     reach M-Lab or any public server.
//   - No automatic retries, no automatic repeats.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	mlabpoc "github.com/AbsoluteZero001/GoSpeed/experiments/mlab-poc/provider"
)

func main() {
	server := flag.String("server", "", "localhost mock server host:port (required)")
	direction := flag.String("direction", "download", "download | upload | both")
	downloadBudget := flag.Int64("download-budget-bytes", 0, "wire-level download byte budget, 0 = disabled")
	uploadBudget := flag.Int64("upload-budget-bytes", 0, "wire-level upload byte budget, 0 = disabled")
	totalBudget := flag.Int64("total-budget-bytes", 0, "wire-level budget across the whole plan, 0 = disabled")
	timeoutMs := flag.Int64("timeout-ms", 0, "overall timeout in milliseconds, 0 = SDK defaults only")
	flag.Parse()

	if *server == "" {
		fmt.Fprintln(os.Stderr, "error: -server is required")
		flag.Usage()
		os.Exit(2)
	}
	host, _, err := net.SplitHostPort(*server)
	if err != nil {
		host = *server
	}
	if !isLoopbackHost(host) {
		fmt.Fprintf(os.Stderr, "error: target %q is not loopback; this PoC tool only talks to localhost mock servers\n", host)
		os.Exit(2)
	}

	// Privacy consent is always interactive and never pre-agreed.
	record, err := mlabpoc.PromptConsent(os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: consent prompt failed: %v\n", err)
		os.Exit(1)
	}
	if !record.Agreed {
		fmt.Println("未获得同意，已取消。没有建立任何测速连接。")
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

	options := mlabpoc.Options{
		Server:              *server,
		Scheme:              "ws",
		Consent:             &record,
		DownloadBudgetBytes: *downloadBudget,
		UploadBudgetBytes:   *uploadBudget,
		OverallTimeout:      time.Duration(*timeoutMs) * time.Millisecond,
	}
	client, err := mlabpoc.NewClient(options)
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

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
