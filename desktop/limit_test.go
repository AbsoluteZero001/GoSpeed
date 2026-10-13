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

// TestUploadBeyondServerLimitStopsAtTheCeiling covers the case that used to
// fail a whole run: the client runs without a byte budget (exactly like the
// GUI), the server enforces a small per request upload cap, and a fast link
// produces more bytes than the cap within the requested duration.
//
// Before v0.5.0-beta.1 RC-01 this ended as a failure, with the server's real
// "413 Request Entity Too Large" discarding a finished transfer; the previous
// version of this test asserted that failure on purpose, to keep the engine
// from silently clamping. The contract has changed on purpose: the server
// limit is now negotiated and applied as a stop the phase can reach by itself,
// so the run completes and states why it stopped.
//
// What the test still guards is the part that must not regress: the ceiling
// comes from GET /capabilities rather than from a hard-coded client value, the
// transfer never exceeds it, and the shortened window is stated out loud rather
// than passed off as a full duration run.
func TestUploadBeyondServerLimitStopsAtTheCeiling(t *testing.T) {
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
		MaxBytes:              0, // no client-side budget: the negotiated server limit decides
		NegotiateCapabilities: true,
		SampleInterval:        200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("a run bounded by the server ceiling returned an error: %v (status %q)", err, result.Status)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("upload timed out before reaching the server limit: %v", err)
	}
	if result.Status != speedtest.StatusCompleted {
		t.Fatalf("status = %q, want %q: reaching an advertised limit is a planned stop, not a failure",
			result.Status, speedtest.StatusCompleted)
	}
	if result.Upload == nil {
		t.Fatal("the completed run published no upload result")
	}
	if result.Upload.ServerConfirmedBytes != uploadCap {
		t.Fatalf("server confirmed %d bytes, want exactly the advertised ceiling %d",
			result.Upload.ServerConfirmedBytes, uploadCap)
	}
	if result.Upload.StopReason != speedtest.StopReasonServerLimit {
		t.Fatalf("stop reason = %q, want %q", result.Upload.StopReason, speedtest.StopReasonServerLimit)
	}
	if result.Upload.Mbps <= 0 {
		t.Fatalf("upload rate = %v, want a real positive measurement", result.Upload.Mbps)
	}
	// The window is shorter than requested, and the run must say so instead of
	// letting a caller read it as a full duration measurement.
	var explained bool
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "advertised") {
			explained = true
		}
	}
	if !explained {
		t.Fatalf("warnings %q do not explain the server byte ceiling", result.Warnings)
	}
	// The ceiling came from capability negotiation, not from a hard-coded
	// client value: the result records what the server advertised.
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
