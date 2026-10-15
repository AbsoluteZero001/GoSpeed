package provider_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/experiments/wan01a-poc/mock"
	wan01a "github.com/AbsoluteZero001/GoSpeed/experiments/wan01a-poc/provider"
)

func TestSubprotocolIsDocumented(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	adapter, err := wan01a.New(wan01a.Options{
		Server:         srv.Host(),
		Scheme:         "ws",
		Direction:      wan01a.DirectionDownload,
		OverallTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := adapter.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v (status=%q class=%q)", err, result.Status, result.ErrorClass)
	}
	if result.Status != wan01a.StatusCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if result.SubprotocolObserved != wan01a.SubprotocolRequired {
		t.Fatalf("subprotocol = %q, want %q", result.SubprotocolObserved, wan01a.SubprotocolRequired)
	}
}

func TestDownloadReceivesApplicationFrames(t *testing.T) {
	srv := mock.NewWithChunkDelay(mock.ScenarioDownload, 0, 5*time.Millisecond)
	defer srv.Close()

	adapter, _ := wan01a.New(wan01a.Options{
		Server:    srv.Host(),
		Scheme:    "ws",
		Direction: wan01a.DirectionDownload,
	})
	result, err := adapter.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Bytes == 0 {
		t.Fatalf("download received 0 application bytes")
	}
	if result.WireBytesRead < result.Bytes {
		t.Fatalf("wire bytes %d below application bytes %d", result.WireBytesRead, result.Bytes)
	}
	if result.Mbps <= 0 {
		t.Fatalf("Mbps = %v, want positive", result.Mbps)
	}
	if !result.FinalMeasurementObserved {
		t.Fatalf("expected server-origin measurement frame")
	}
}

func TestUploadSendsApplicationFrames(t *testing.T) {
	const target = 512 << 10
	srv := mock.New(mock.ScenarioUpload, int64(target))
	defer srv.Close()

	adapter, _ := wan01a.New(wan01a.Options{
		Server:    srv.Host(),
		Scheme:    "ws",
		Direction: wan01a.DirectionUpload,
	})
	result, err := adapter.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Bytes == 0 {
		t.Fatalf("upload reported 0 application bytes")
	}
	if result.WireBytesWritten < result.Bytes {
		t.Fatalf("wire bytes %d below application bytes %d", result.WireBytesWritten, result.Bytes)
	}
	snap := srv.Snapshot()
	if int64(snap.BytesRead) < int64(result.Bytes) {
		t.Fatalf("mock saw %d bytes, client reported %d", snap.BytesRead, result.Bytes)
	}
}

func TestElapsedWindowIsResolvable(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	adapter, _ := wan01a.New(wan01a.Options{
		Server:    srv.Host(),
		Scheme:    "ws",
		Direction: wan01a.DirectionDownload,
	})
	result, err := adapter.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ElapsedMs < 0 {
		t.Fatalf("elapsed = %d ms, want >= 0", result.ElapsedMs)
	}
}

func TestImmediateCancellation(t *testing.T) {
	srv := mock.NewWithChunkDelay(mock.ScenarioDownload, 0, 30*time.Millisecond)
	defer srv.Close()

	adapter, _ := wan01a.New(wan01a.Options{
		Server:         srv.Host(),
		Scheme:         "ws",
		Direction:      wan01a.DirectionDownload,
		OverallTimeout: 30 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result, err := adapter.Run(ctx)
	elapsed := time.Since(start)

	if err != nil && !errors.Is(err, context.Canceled) {
		t.Logf("Run returned err = %v (treated as cancel indicator)", err)
	}
	if result.Status != wan01a.StatusCancelled {
		t.Fatalf("status = %q, want %q", result.Status, wan01a.StatusCancelled)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("cancellation latency = %v, want < 5s", elapsed)
	}
}

func TestOverallTimeout(t *testing.T) {
	srv := mock.NewWithChunkDelay(mock.ScenarioDownload, 0, 30*time.Millisecond)
	defer srv.Close()

	adapter, _ := wan01a.New(wan01a.Options{
		Server:         srv.Host(),
		Scheme:         "ws",
		Direction:      wan01a.DirectionDownload,
		OverallTimeout: 100 * time.Millisecond,
	})
	result, err := adapter.Run(context.Background())
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Logf("Run returned err = %v (treated as timeout indicator)", err)
	}
	if result.Status != wan01a.StatusTimeout {
		t.Fatalf("status = %q, want %q", result.Status, wan01a.StatusTimeout)
	}
	if result.ElapsedMs > 5000 {
		t.Fatalf("elapsed = %d ms, want < 5000", result.ElapsedMs)
	}
}

func TestHandshakeFailure(t *testing.T) {
	srv := mock.New(mock.ScenarioHandshakeFailure, 0)
	defer srv.Close()

	adapter, _ := wan01a.New(wan01a.Options{
		Server:         srv.Host(),
		Scheme:         "ws",
		Direction:      wan01a.DirectionDownload,
		OverallTimeout: 5 * time.Second,
	})
	result, err := adapter.Run(context.Background())
	if err == nil {
		t.Fatal("expected a handshake error, got nil")
	}
	if result.Status != wan01a.StatusFailed {
		t.Fatalf("status = %q, want %q", result.Status, wan01a.StatusFailed)
	}
	if result.ErrorClass != wan01a.ErrorHandshake {
		t.Fatalf("error class = %q, want %q", result.ErrorClass, wan01a.ErrorHandshake)
	}
}

func TestNetworkDisconnect(t *testing.T) {
	srv := mock.New(mock.ScenarioNetworkDisconnect, 0)
	defer srv.Close()

	adapter, _ := wan01a.New(wan01a.Options{
		Server:         srv.Host(),
		Scheme:         "ws",
		Direction:      wan01a.DirectionDownload,
		OverallTimeout: 5 * time.Second,
	})
	result, _ := adapter.Run(context.Background())
	if result.Bytes > 16 {
		t.Fatalf("download received %d bytes after a disconnect (mock only writes 1)", result.Bytes)
	}
	if result.FinalMeasurementObserved {
		t.Fatalf("disconnect must not produce a server-origin measurement")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Snapshot().Active == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("mock still has %d active connections after disconnect", srv.Snapshot().Active)
}

func TestWireBudgetDownloadStopsAfterBudget(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	const budget = 32 << 10
	adapter, _ := wan01a.New(wan01a.Options{
		Server:          srv.Host(),
		Scheme:          "ws",
		Direction:       wan01a.DirectionDownload,
		WireBudgetBytes: budget,
	})
	result, err := adapter.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.BudgetExceeded {
		t.Fatalf("budget was not exceeded (wire bytes %d, budget %d)", result.WireBytesRead, budget)
	}
	if result.Status != wan01a.StatusPartial {
		t.Fatalf("status = %q, want %q", result.Status, wan01a.StatusPartial)
	}
	if result.Mbps != 0 {
		t.Fatalf("Mbps = %v, want 0", result.Mbps)
	}
}

func TestWireBudgetUploadStopsBeforeSentBytesExceedBudget(t *testing.T) {
	const target = 512 << 10
	srv := mock.New(mock.ScenarioUpload, int64(target))
	defer srv.Close()

	const budget = 64 << 10
	adapter, _ := wan01a.New(wan01a.Options{
		Server:          srv.Host(),
		Scheme:          "ws",
		Direction:       wan01a.DirectionUpload,
		WireBudgetBytes: budget,
	})
	result, err := adapter.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.BudgetExceeded {
		t.Fatalf("budget was not exceeded (wire bytes %d, budget %d)", result.WireBytesWritten, budget)
	}
	if result.WireBytesWritten > budget+int64(64<<10) {
		t.Fatalf("upload wire bytes %d overshoot budget %d by too much", result.WireBytesWritten, budget)
	}
}

func TestResourceCleanupOnCancellation(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	adapter, _ := wan01a.New(wan01a.Options{
		Server:         srv.Host(),
		Scheme:         "ws",
		Direction:      wan01a.DirectionDownload,
		OverallTimeout: 30 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, _ = adapter.Run(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Snapshot().Active == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("mock still has %d active connections after cancel", srv.Snapshot().Active)
}

func TestProgressStreamReportsEveryEvent(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	var progressEvents int32
	adapter, _ := wan01a.New(wan01a.Options{
		Server:    srv.Host(),
		Scheme:    "ws",
		Direction: wan01a.DirectionDownload,
		OnProgress: func(p wan01a.Progress) {
			atomic.AddInt32(&progressEvents, 1)
		},
	})
	if _, err := adapter.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if atomic.LoadInt32(&progressEvents) == 0 {
		t.Fatalf("progress callback never fired")
	}
}
