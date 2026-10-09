package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// newTestEngine builds the same HTTP client the GUI uses and closes its idle
// connections when the test ends.
func newTestEngine(t *testing.T) *speedtest.Engine {
	t.Helper()
	client := speedtest.NewHTTPClient()
	t.Cleanup(client.CloseIdleConnections)
	return speedtest.NewEngine(client)
}

// TestUploadBeyondServerLimitReturns413 is the regression test for the CI
// failure: the client runs without a byte budget (exactly like the GUI), the
// server enforces a small per-request upload cap, and the run must fail with
// the real 413 the server sent instead of publishing a result.
func TestUploadBeyondServerLimitReturns413(t *testing.T) {
	const uploadCap = 1 << 20 // 1 MiB: reached by any loopback in well under a second
	limits := testServerConfig()
	limits.MaxUploadBytes = uploadCap
	httpServer := newSpeedtestServerWithConfig(t, limits)

	result, err := newTestEngine(t).Run(context.Background(), speedtest.Options{
		Target: speedtest.Target{
			ID:       "limited",
			Name:     "limited upload server",
			BaseURL:  httpServer.URL,
			Protocol: speedtest.ProtocolHTTP,
			Local:    true,
		},
		Phases:                speedtest.PhasesUpload,
		Duration:              5 * time.Second,
		Timeout:               15 * time.Second,
		Connections:           1,
		MaxBytes:              0, // no client-side budget: the server limit decides
		NegotiateCapabilities: true,
		SampleInterval:        200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("upload beyond the server limit unexpectedly succeeded")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("upload timed out before reaching the server limit: %v", err)
	}
	if !strings.Contains(err.Error(), "413") {
		t.Fatalf("error = %q, want the real HTTP 413 from the server", err.Error())
	}
	if !strings.Contains(err.Error(), "upload exceeds the server limit") {
		t.Fatalf("error = %q, want the server's own limit message", err.Error())
	}
	if result.Status == speedtest.StatusCompleted {
		t.Fatalf("refused upload was reported as completed: %+v", result.Status)
	}
	if result.Upload != nil {
		t.Fatalf("a refused upload must not produce an upload result: %+v", result.Upload)
	}
	// The limit came from capability negotiation, not from a hard-coded client
	// value: the failed result records what the server advertised.
	if result.Target.Capabilities == nil || result.Target.Capabilities.Limits == nil ||
		result.Target.Capabilities.Limits.MaxUploadBytes != uploadCap {
		t.Fatalf("negotiated capabilities do not carry the server upload limit: %+v",
			result.Target.Capabilities)
	}
}

// TestByteBudgetBeyondServerLimitIsRefusedBeforeTransfer is the client side of
// the same contract: a byte budget that exceeds the advertised per-request
// limit must be refused before any traffic is sent, instead of letting the
// server cut the stream with a 413 halfway through.
//
// This is deliberately a pure validation check: it never opens a transfer, so
// it cannot depend on the runner's throughput or on the platform clock
// resolution. The within-limit case (a normal run that stays inside the
// advertised limits) is covered by the duration based runs above.
func TestByteBudgetBeyondServerLimitIsRefusedBeforeTransfer(t *testing.T) {
	const uploadCap = 1 << 20
	const downloadCap = 2 << 20

	cases := []struct {
		name     string
		phases   speedtest.PhaseMask
		maxBytes int64
	}{
		{name: "upload budget over the upload limit", phases: speedtest.PhasesUpload, maxBytes: uploadCap + 1},
		{name: "download budget over the download limit", phases: speedtest.PhasesDownload, maxBytes: downloadCap + 1},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			limits := testServerConfig()
			limits.MaxUploadBytes = uploadCap
			limits.MaxDownloadBytes = downloadCap
			httpServer := newSpeedtestServerWithConfig(t, limits)

			result, err := newTestEngine(t).Run(context.Background(), speedtest.Options{
				Target: speedtest.Target{
					ID:       "limited",
					Name:     "limited server",
					BaseURL:  httpServer.URL,
					Protocol: speedtest.ProtocolHTTP,
					Local:    true,
				},
				Phases:                testCase.phases,
				Duration:              5 * time.Second,
				Timeout:               15 * time.Second,
				Connections:           1,
				MaxBytes:              testCase.maxBytes,
				NegotiateCapabilities: true,
				SampleInterval:        200 * time.Millisecond,
			})
			if !errors.Is(err, speedtest.ErrServerLimitExceeded) {
				t.Fatalf("error = %v, want %v", err, speedtest.ErrServerLimitExceeded)
			}
			if result.Status == speedtest.StatusCompleted {
				t.Fatalf("a refused budget was reported as completed: %q", result.Status)
			}
			if result.Download != nil || result.Upload != nil {
				t.Fatalf("refused run must not publish a transfer result: %+v %+v",
					result.Download, result.Upload)
			}
			if result.Target.Capabilities == nil || result.Target.Capabilities.Limits == nil {
				t.Fatal("the refusal did not come from negotiated server limits")
			}
		})
	}
}
