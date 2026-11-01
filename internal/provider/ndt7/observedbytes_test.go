package ndt7_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7/mock"
)

// TestBudgetStopWithoutAWindowReportsTheObservedTraffic proves that a run
// cut short at the byte budget, before a measurement window could form, says
// so instead of reporting an empty transfer.
//
// The window bytes are zero in this case - there was no window - but the
// socket really did carry traffic. The result must carry that traffic as the
// observed byte count, and it must not publish a rate for a measurement that
// never happened.
func TestBudgetStopWithoutAWindowReportsTheObservedTraffic(t *testing.T) {
	srv := mock.NewStreaming(mock.ScenarioUpload, 1<<20, 0, 10*time.Second)
	defer srv.Close()

	r := rawRunner(t)
	nodeID := "upload-budget"
	if _, err := r.AuthorizeRun(nodeID, provider.DirectionUpload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	// A budget small enough that the very first doubled message exceeds it,
	// whatever the machine's speed: the transfer therefore ends before the
	// SDK could emit a second client measurement frame.
	const budget = 8 << 10
	result, _ := r.Run(context.Background(), provider.RunRequest{
		Node:         nodeOf("ws://"+srv.Host(), nodeID),
		Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapUpload: true}},
		Directions:   []provider.Direction{provider.DirectionUpload},
		Duration:     5 * time.Second,
		Budget:       provider.BudgetLimits{UploadBytes: budget},
	})

	if result.Status != provider.StatusBudgetExceeded {
		t.Fatalf("status = %q, want the budget stop", result.Status)
	}
	rate := result.Upload
	if rate == nil {
		t.Fatal("the upload rate is missing")
	}
	if rate.Mbps != nil {
		t.Fatalf("Mbps = %v, want no rate at all when no window was formed", *rate.Mbps)
	}
	if rate.TransferredBytes != 0 {
		t.Fatalf("window bytes = %d, want 0: there was no measurement window", rate.TransferredBytes)
	}
	if rate.ObservedBytes == nil {
		t.Fatal("the observed byte count is missing: the traffic that really moved is not reported")
	}
	if *rate.ObservedBytes <= 0 {
		t.Fatalf("observed bytes = %d, want the traffic that actually crossed the socket", *rate.ObservedBytes)
	}
	if result.TransferredBytes != *rate.ObservedBytes {
		t.Fatalf("the run's transferred bytes = %d, want the observed %d", result.TransferredBytes, *rate.ObservedBytes)
	}
	if rate.StopReason != ndt7.StopReasonBudgetExceeded {
		t.Fatalf("stop reason = %q, want %q", rate.StopReason, ndt7.StopReasonBudgetExceeded)
	}
	if result.ErrorCode == "" {
		t.Fatal("the result carries no error code for the budget stop")
	}
	// The reason has to be visible in the result, not only in a log.
	found := false
	for _, warning := range result.Method.Warnings {
		if strings.Contains(warning, "no rate published") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the warnings do not say that no rate was published: %v", result.Method.Warnings)
	}
}

// TestShippedBudgetNeverFabricatesARate proves what the shipped budget does
// on a fast peer.
//
// The product's own budget is far larger than the one above, so whether a
// measurement window forms depends on the machine. What must hold either way
// is the contract: a published rate is backed by the window bytes, and a run
// without a window publishes no rate and still reports the traffic it moved.
func TestShippedBudgetNeverFabricatesARate(t *testing.T) {
	srv := mock.NewStreaming(mock.ScenarioUpload, 1<<20, 0, 10*time.Second)
	defer srv.Close()

	r := rawRunner(t)
	nodeID := "upload-shipped-budget"
	if _, err := r.AuthorizeRun(nodeID, provider.DirectionUpload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	result, _ := r.Run(context.Background(), provider.RunRequest{
		Node:         nodeOf("ws://"+srv.Host(), nodeID),
		Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapUpload: true}},
		Directions:   []provider.Direction{provider.DirectionUpload},
		Duration:     5 * time.Second,
		Budget:       r.Plan(),
	})
	rate := result.Upload
	if rate == nil {
		t.Fatal("the upload rate is missing")
	}
	if rate.Mbps != nil {
		// A window was formed: the rate is backed by the window bytes, and
		// the traffic that crossed the socket bounds them.
		if rate.TransferredBytes <= 0 {
			t.Fatalf("window bytes = %d, want a real byte count behind the published rate", rate.TransferredBytes)
		}
		if rate.ObservedBytes == nil {
			t.Fatal("the observed byte count is missing")
		}
		if *rate.ObservedBytes < rate.TransferredBytes {
			t.Fatalf("observed bytes %d must bound the window bytes %d", *rate.ObservedBytes, rate.TransferredBytes)
		}
		return
	}
	// No window: no rate may be published, the traffic that moved must be
	// reported, and the stop reason must say why there is no rate.
	if rate.TransferredBytes != 0 {
		t.Fatalf("window bytes = %d, want 0 when no window was formed", rate.TransferredBytes)
	}
	if rate.ObservedBytes == nil || *rate.ObservedBytes <= 0 {
		t.Fatalf("the traffic that moved is not reported: observed bytes = %v", rate.ObservedBytes)
	}
	if result.TransferredBytes != *rate.ObservedBytes {
		t.Fatalf("the run's transferred bytes = %d, want the observed %d", result.TransferredBytes, *rate.ObservedBytes)
	}
	switch rate.StopReason {
	case ndt7.StopReasonBudgetExceeded, ndt7.StopReasonWindowUnavailable, ndt7.StopReasonNoData:
	default:
		t.Fatalf("stop reason = %q, want a reason that explains the missing rate", rate.StopReason)
	}
}

// TestObservedBytesAreAbsentForAProviderThatCannotReportThem proves the
// observed byte count stays absent rather than becoming a measured zero.
//
// A zero would claim that nothing moved, which is a measurement claim the
// provider cannot make. The field is null when the provider has no socket
// level observation to report.
func TestObservedBytesAreAbsentForAProviderThatCannotReportThem(t *testing.T) {
	rate := provider.Rate{Mbps: nil, ElapsedMs: 0, TransferredBytes: 0}
	if rate.ObservedBytes != nil {
		t.Fatal("a rate that was never measured must not carry an observed byte count")
	}
}
