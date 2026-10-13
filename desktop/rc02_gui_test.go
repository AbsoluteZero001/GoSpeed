package main

import (
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
)

// This file proves the RC-01 stop reason survives the whole GUI path: engine ->
// provider -> registry -> runner -> finished event -> the JSON the frontend
// parses. It asserts the wire form, because that is what ProviderResultPanel
// actually reads. It does not claim to be a native window interaction.

// TestServerLimitStopReachesTheGUIFromWire is the acceptance for "RC-01 GUI
// 兼容验证". A duration limited upload that reaches the node's advertised byte
// ceiling must arrive at the GUI as a completed run that names the reason,
// keeps the real rate, and carries the warning. It must never arrive as a 413,
// as a failure, or as a silently truncated measurement.
func TestServerLimitStopReachesTheGUIFromWire(t *testing.T) {
	// Large enough that the measured window always spans more than one platform
	// clock tick (a small ceiling can finish inside one tick, which the engine
	// correctly refuses to rate), and small enough that any loopback reaches it well
	// inside the 5 s window, so the run ends at the limit rather than at the window.
	const ceiling = 16 << 20

	limits := testServerConfig()
	limits.MaxUploadBytes = ceiling
	httpServer := newSpeedtestServerWithConfig(t, limits)

	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))
	t.Cleanup(func() { _ = runner.CloseProviders() })

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      1,
		DurationMs:       5000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 60*time.Second)
	runner.Wait()

	// The legacy finished event must still say "completed": a planned stop at
	// an advertised limit is not a failure, and the GUI's headline state must
	// not say otherwise.
	if finished.Status != StateCompleted {
		t.Fatalf("finished status = %q, want %q: a node byte ceiling is a planned stop",
			finished.Status, StateCompleted)
	}
	if finished.Cancelled {
		t.Error("a run that stopped at the node limit must not be reported as cancelled")
	}
	if finished.Error != "" {
		t.Errorf("a completed run must carry no error, got %q", finished.Error)
	}

	if finished.Unified == nil {
		t.Fatal("the finished event carries no unified result")
	}
	wire := decodeFinished(t, finished)

	if wire.Unified.Status != string(provider.StatusCompleted) {
		t.Fatalf("unified status = %q, want %q", wire.Unified.Status, provider.StatusCompleted)
	}
	if wire.Unified.Upload == nil {
		t.Fatal("the unified upload metric is missing")
	}
	if wire.Unified.Upload.StopReason != "server_limit_reached" {
		t.Fatalf("upload stopReason = %q, want %q", wire.Unified.Upload.StopReason, "server_limit_reached")
	}
	if wire.Unified.Upload.Mbps == nil || *wire.Unified.Upload.Mbps <= 0 {
		t.Fatalf("upload mbps = %v, want the real measured rate to survive the GUI mapping", wire.Unified.Upload.Mbps)
	}
	if wire.Unified.Upload.TransferredBytes != ceiling {
		t.Errorf("upload transferredBytes = %d, want the confirmed %d",
			wire.Unified.Upload.TransferredBytes, ceiling)
	}
	// The GUI needs a non-zero elapsedMs to contrast the real window against
	// the planned one; a zero would render as "0 s" and hide the truncation.
	if wire.Unified.Upload.ElapsedMs <= 0 {
		t.Errorf("upload elapsedMs = %d, want the real measured window", wire.Unified.Upload.ElapsedMs)
	}
	if wire.Unified.Upload.ElapsedMs >= 5000 {
		t.Errorf("upload elapsedMs = %d, want a window shorter than the planned 5000 ms", wire.Unified.Upload.ElapsedMs)
	}
	// The warning must reach the GUI: it is what tells the user the window was
	// cut short and how to cover more of it.
	var explained bool
	for _, warning := range wire.Unified.Method.Warnings {
		if containsAny(warning, "advertised", "ceiling") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("warnings %q do not explain the node byte ceiling", wire.Unified.Method.Warnings)
	}
	if wire.Unified.Quality.Completeness != string(provider.Complete) {
		t.Errorf("completeness = %q, want %q", wire.Unified.Quality.Completeness, provider.Complete)
	}
	if wire.Unified.Quality.Stability != "degraded" {
		t.Errorf("stability = %q, want %q so the shortened window is visible",
			wire.Unified.Quality.Stability, "degraded")
	}
	// A 413 must never reappear: the whole point of RC-01 is that the node's
	// own limit no longer rejects the body half way through.
	if containsAny(finished.Error, "413", "Request Entity Too Large") {
		t.Errorf("the GUI error still reports a 413: %q", finished.Error)
	}
}

// TestServerLimitStopNeverFailsADownloadOnlyRun is the guard against the stop
// reason leaking into a direction that never hit a limit.
func TestServerLimitStopNeverFailsADownloadOnlyRun(t *testing.T) {
	limits := testServerConfig()
	limits.MaxUploadBytes = 1 << 30 // unreachable in this window
	httpServer := newSpeedtestServerWithConfig(t, limits)

	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))
	t.Cleanup(func() { _ = runner.CloseProviders() })

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      1,
		DurationMs:       1000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 60*time.Second)
	runner.Wait()

	if finished.Status != StateCompleted {
		t.Fatalf("finished status = %q, want %q", finished.Status, StateCompleted)
	}
	wire := decodeFinished(t, finished)
	if wire.Unified.Upload == nil {
		t.Fatal("the unified upload metric is missing")
	}
	if wire.Unified.Upload.StopReason == "server_limit_reached" {
		t.Error("a run that never reached the node ceiling must not claim it did")
	}
}
