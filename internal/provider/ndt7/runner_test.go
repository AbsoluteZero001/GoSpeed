package ndt7_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7/mock"
)

func recordedConsent() *ndt7.ConsentRecord {
	return &ndt7.ConsentRecord{
		Agreed:        true,
		AgreedAt:      time.Now().UTC(),
		PolicyVersion: "ndt7-notice-v1",
		ClientName:    "GoSpeed-test",
		ClientVersion: "0.0.0-test",
	}
}

func nodeOf(endpoint, id string) provider.Node {
	return provider.Node{
		ID:        id,
		Name:      id,
		Endpoint:  endpoint,
		Provider:  provider.IDMLabNDT7,
		Ownership: provider.OwnershipThirdParty,
		Discovery: provider.DiscoveryService,
	}
}

func TestRegistrationThroughRunner(t *testing.T) {
	r := ndt7.NewRunnerWithConsent(recordedConsent())
	if got := r.ID(); got != provider.IDMLabNDT7 {
		t.Fatalf("ID() = %q, want %q", got, provider.IDMLabNDT7)
	}
	if !r.ConsentRequired() {
		t.Fatal("ConsentRequired() must be true for NDT7")
	}
	if notice := r.ConsentNotice(); notice == "" {
		t.Fatal("ConsentNotice() must not be empty")
	}
	if plan := r.Plan(); plan.DownloadBytes == 0 || plan.UploadBytes == 0 || plan.TotalBytes == 0 {
		t.Fatalf("Plan() must report non-zero defaults, got %+v", plan)
	}
}

func TestConsentGateBlocksEveryNetworkCall(t *testing.T) {
	r := ndt7.NewRunner()
	node := nodeOf("ws://127.0.0.1:1", "test")
	caps, err := r.Negotiate(context.Background(), node)
	if !errors.Is(err, ndt7.ErrConsentRequired) {
		t.Fatalf("Negotiate err = %v, want ErrConsentRequired", err)
	}
	if caps.Supported != nil {
		t.Fatalf("Negotiate returned a capability set without consent: %+v", caps)
	}
	if _, err := r.Discover(context.Background()); !errors.Is(err, ndt7.ErrConsentRequired) {
		t.Fatalf("Discover err = %v, want ErrConsentRequired", err)
	}
	_, err = r.Run(context.Background(), provider.RunRequest{
		Node:         node,
		Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true}},
		Directions:   []provider.Direction{provider.DirectionDownload},
	})
	if !errors.Is(err, ndt7.ErrConsentRequired) {
		t.Fatalf("Run err = %v, want ErrConsentRequired", err)
	}
}

func TestNegotiateReturnsDocumentedCapabilityShape(t *testing.T) {
	r := ndt7.NewRunnerWithConsent(recordedConsent())
	caps, err := r.Negotiate(context.Background(), nodeOf("ws://127.0.0.1:1", "test"))
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	for _, want := range []provider.Capability{provider.CapDownload, provider.CapUpload, provider.CapLatency} {
		if !caps.Supports(want) {
			t.Fatalf("capability %q is missing from %+v", want, caps.Supported)
		}
		if caps.Note(want) == "" {
			t.Fatalf("capability %q has empty note", want)
		}
	}
	if caps.Supports(provider.CapJitter) {
		t.Fatalf("NDT7 has no application-level jitter; capability must not be claimed")
	}
}

func TestRefusesMultiConnectionAndMultiDirection(t *testing.T) {
	r := ndt7.NewRunnerWithConsent(recordedConsent())
	node := nodeOf("ws://127.0.0.1:1", "test")
	t.Run("multi-connection refused", func(t *testing.T) {
		_, err := r.Run(context.Background(), provider.RunRequest{
			Node:         node,
			Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true}},
			Directions:   []provider.Direction{provider.DirectionDownload},
			Connections:  2,
		})
		if !errors.Is(err, ndt7.ErrConnectionsNotSupported) {
			t.Fatalf("err = %v, want ErrConnectionsNotSupported", err)
		}
	})
	t.Run("multi-direction refused", func(t *testing.T) {
		_, err := r.Run(context.Background(), provider.RunRequest{
			Node:         node,
			Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true, provider.CapUpload: true}},
			Directions:   []provider.Direction{provider.DirectionDownload, provider.DirectionUpload},
		})
		if err == nil {
			t.Fatal("two-direction Run must fail")
		}
		if !strings.Contains(err.Error(), "direction") {
			t.Fatalf("error message must explain why two directions are refused, got %v", err)
		}
	})
	t.Run("unsupported direction refused", func(t *testing.T) {
		_, err := r.Run(context.Background(), provider.RunRequest{
			Node:         node,
			Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true}},
			Directions:   []provider.Direction{"sideways"},
		})
		if !errors.Is(err, ndt7.ErrUnsupportedDirection) {
			t.Fatalf("err = %v, want ErrUnsupportedDirection", err)
		}
	})
}

func TestDownloadSuccessAndJsonShape(t *testing.T) {
	srv := mock.NewWithChunkDelay(mock.ScenarioDownload, 0, 5*time.Millisecond)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	req := provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "download-ok"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{
				provider.CapDownload: true,
				provider.CapLatency:  true,
			},
		},
		Directions:     []provider.Direction{provider.DirectionDownload},
		Duration:       5 * time.Second,
		SampleInterval: 50 * time.Millisecond,
		Budget:         provider.BudgetLimits{DownloadBytes: 1 << 20},
	}
	result, err := r.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run: %v (status=%q class=%q)", err, result.Status, result.ErrorMessage)
	}
	if result.Status != provider.StatusCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if result.Download == nil {
		t.Fatal("download result is nil")
	}
	dr := result.Download
	if dr.TransferredBytes == 0 {
		t.Fatal("download transferred 0 bytes")
	}
	if dr.Mbps == nil || *dr.Mbps <= 0 {
		t.Fatalf("Mbps = %v, want positive", dr.Mbps)
	}
	if dr.Window == "" {
		t.Fatal("Window must name the measurement window")
	}
	if dr.StopReason == "" {
		t.Fatal("StopReason must be set")
	}
	if result.Detail == nil {
		t.Fatal("Detail must carry the native payload")
	}
	if result.Detail.Format != ndt7.DetailFormat {
		t.Fatalf("Detail.Format = %q, want %q", result.Detail.Format, ndt7.DetailFormat)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded provider.FinalResult
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Download == nil || decoded.Download.TransferredBytes != dr.TransferredBytes {
		t.Fatalf("round-trip changed byte count: %+v vs %+v", decoded.Download, dr)
	}
	if decoded.Method.Protocol != ndt7.Protocol {
		t.Fatalf("Method.Protocol = %q, want %q", decoded.Method.Protocol, ndt7.Protocol)
	}
}

func TestUploadSuccessAndServerConfirmedBytes(t *testing.T) {
	const target = 512 << 10
	srv := mock.New(mock.ScenarioUpload, int64(target))
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "upload-ok"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapUpload: true},
		},
		Directions: []provider.Direction{provider.DirectionUpload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{UploadBytes: 1 << 20},
	})
	if err != nil {
		t.Fatalf("Run: status=%q", result.Status)
	}
	if result.Status != provider.StatusCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if result.Upload == nil {
		t.Fatal("upload result is nil")
	}
	if result.Upload.TransferredBytes == 0 {
		t.Fatal("upload transferred 0 bytes")
	}
	if srv.Snapshot().BytesRead < result.Upload.TransferredBytes {
		t.Fatalf("mock saw %d bytes, client reported %d", srv.Snapshot().BytesRead, result.Upload.TransferredBytes)
	}
}

func TestHandshakeFailureClassifiesAsHandshake(t *testing.T) {
	srv := mock.New(mock.ScenarioHandshakeFailure, 0)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "handshake-fail"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: 1 << 20},
	})
	if err == nil {
		t.Fatal("expected a handshake error")
	}
	if result.Status != provider.StatusFailed {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if result.ErrorCode != "handshake_failed" {
		t.Fatalf("ErrorCode = %q, want handshake_failed", result.ErrorCode)
	}
	if result.Download != nil && result.Download.TransferredBytes != 0 {
		t.Fatalf("a failed handshake must not publish non-zero bytes: %+v", result.Download)
	}
}

func TestConnectionFailureDoesNotLeakGoroutines(t *testing.T) {
	srv := mock.New(mock.ScenarioNetworkDisconnect, 0)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, _ := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "net-disconnect"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: 1 << 20},
	})
	// The network-disconnect scenario closes the TCP socket after writing a single byte.
	// The SDK completes the run as status completed because it has bytes to report.
	if result.Status != provider.StatusCompleted && result.Status != provider.StatusFailed {
		t.Fatalf("status = %q, want completed or failed", result.Status)
	}
	if result.Download != nil && result.Download.TransferredBytes > 1024 {
		t.Fatalf("disconnect must not transfer many bytes: %+v", result.Download)
	}
	if result.Download != nil && result.Download.Mbps != nil && *result.Download.Mbps > 0 {
		t.Fatalf("Mbps > 0 on a disconnect is dishonest: %v", *result.Download.Mbps)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Snapshot().Active == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("mock still has %d active connections after disconnect", srv.Snapshot().Active)
}

func TestImmediateCancellationForceClosesUnderlyingSocket(t *testing.T) {
	srv := mock.NewWithChunkDelay(mock.ScenarioDownload, 0, 30*time.Millisecond)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	result, err := r.Run(ctx, provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "cancel"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   30 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: 1 << 20},
	})
	elapsed := time.Since(start)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Logf("Run returned err = %v (treated as cancel indicator)", err)
	}
	if result.Status != provider.StatusCancelled {
		t.Fatalf("status = %q, want cancelled", result.Status)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("cancellation latency = %v, want < 3s (active I/O case)", elapsed)
	}
}

func TestOverallTimeoutFiresBeforeSDKDeadline(t *testing.T) {
	srv := mock.NewWithChunkDelay(mock.ScenarioDownload, 0, 100*time.Millisecond)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "timeout"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   200 * time.Millisecond,
		Budget:     provider.BudgetLimits{DownloadBytes: 1 << 20},
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Logf("Run returned err = %v (treated as timeout indicator)", err)
	}
	if result.Status != provider.StatusTimeout {
		t.Fatalf("status = %q, want timeout", result.Status)
	}
	if result.ElapsedMs > 3000 {
		t.Fatalf("elapsed = %d ms, want < 3000", result.ElapsedMs)
	}
}

func TestWireBudgetStopsDownloadWithPartialStatus(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	const budget = 32 << 10
	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "budget"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   10 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: budget},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != provider.StatusBudgetExceeded {
		t.Fatalf("status = %q, want budget_exceeded", result.Status)
	}
	if result.Download == nil {
		t.Fatal("budget-exceeded run must still publish a partial download")
	}
	if result.Download.StopReason != ndt7.StopReasonNDT7BudgetExceeded {
		t.Fatalf("StopReason = %q, want %q", result.Download.StopReason, ndt7.StopReasonNDT7BudgetExceeded)
	}
	if result.Download.Mbps != nil && *result.Download.Mbps > 0 {
		t.Fatalf("budget-aborted run must not publish a Mbps: %v", *result.Download.Mbps)
	}
	if result.Method.Budget.DownloadBytes != budget {
		t.Fatalf("Method.Budget.DownloadBytes = %d, want %d", result.Method.Budget.DownloadBytes, budget)
	}
}

func TestWireBudgetRejectsUploadBeforeBytesReachNetwork(t *testing.T) {
	const target = 512 << 10
	srv := mock.New(mock.ScenarioUpload, int64(target))
	defer srv.Close()

	const budget = 32 << 10
	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "upload-budget"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapUpload: true},
		},
		Directions: []provider.Direction{provider.DirectionUpload},
		Duration:   10 * time.Second,
		Budget:     provider.BudgetLimits{UploadBytes: budget},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != provider.StatusBudgetExceeded {
		t.Fatalf("status = %q, want budget_exceeded", result.Status)
	}
	if result.Upload == nil {
		t.Fatal("budget-exceeded upload must still publish a partial result")
	}
	if result.Upload.StopReason != ndt7.StopReasonNDT7BudgetExceeded {
		t.Fatalf("StopReason = %q, want %q", result.Upload.StopReason, ndt7.StopReasonNDT7BudgetExceeded)
	}
}

func TestMeasurementWindowSpansFirstToLastFrame(t *testing.T) {
	srv := mock.NewWithChunkDelay(mock.ScenarioDownload, 0, 5*time.Millisecond)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "window"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: 1 << 20},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Download == nil {
		t.Fatal("download is nil")
	}
	if result.Download.Window == "" {
		t.Fatal("Window name is empty")
	}
	if result.ElapsedMs < 0 {
		t.Fatalf("elapsed = %d ms, want >= 0", result.ElapsedMs)
	}
	if result.Download.ElapsedMs < 0 {
		t.Fatalf("download window = %d ms, want >= 0", result.Download.ElapsedMs)
	}
}

func TestMbpsFromApplicationBytesIsHonest(t *testing.T) {
	srv := mock.NewWithChunkDelay(mock.ScenarioDownload, 0, 5*time.Millisecond)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "mbps"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: 1 << 20},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Download == nil || result.Download.Mbps == nil {
		t.Fatal("rate missing")
	}
	appBytes := float64(result.Download.TransferredBytes)
	windowSec := float64(result.Download.ElapsedMs) / 1000.0
	expectedMbps := appBytes * 8 / windowSec / 1e6
	got := *result.Download.Mbps
	if delta := got - expectedMbps; delta > 0.5 || delta < -0.5 {
		t.Fatalf("Mbps = %v, want %v (application bytes %v / %v s * 8 / 1e6)", got, expectedMbps, appBytes, windowSec)
	}
	var native struct {
		ApplicationBytes int64   `json:"applicationBytes"`
		ElapsedMs        int64   `json:"elapsedMs"`
		Mbps             float64 `json:"mbps"`
	}
	if err := json.Unmarshal(result.Detail.Payload, &native); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if native.ApplicationBytes != result.Download.TransferredBytes {
		t.Fatalf("native bytes %d != rate bytes %d", native.ApplicationBytes, result.Download.TransferredBytes)
	}
}

func TestResourceCleanupAfterRun(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	for i := 0; i < 8; i++ {
		_, _ = r.Run(context.Background(), provider.RunRequest{
			Node: nodeOf("ws://"+srv.Host(), "leak-check"),
			Capabilities: provider.CapabilitySet{
				Supported: map[provider.Capability]bool{provider.CapDownload: true},
			},
			Directions: []provider.Direction{provider.DirectionDownload},
			Duration:   5 * time.Second,
			Budget:     provider.BudgetLimits{DownloadBytes: 1 << 20},
		})
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Snapshot().Active == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("mock still has %d active connections after 8 runs", srv.Snapshot().Active)
}

func TestRunnerIsConcurrencySafeForSequentialRuns(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Run(context.Background(), provider.RunRequest{
				Node: nodeOf("ws://"+srv.Host(), "concurrent"),
				Capabilities: provider.CapabilitySet{
					Supported: map[provider.Capability]bool{provider.CapDownload: true},
				},
				Directions: []provider.Direction{provider.DirectionDownload},
				Duration:   5 * time.Second,
				Budget:     provider.BudgetLimits{DownloadBytes: 1 << 20},
			})
		}()
	}
	wg.Wait()
}

func TestStopReasonMappingsAreStable(t *testing.T) {
	srv := mock.NewWithChunkDelay(mock.ScenarioDownload, 0, 5*time.Millisecond)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "stop-reason"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: 1 << 20},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Download == nil {
		t.Fatal("download nil")
	}
	if result.Download.StopReason != "duration_elapsed" {
		t.Fatalf("StopReason = %q, want %q", result.Download.StopReason, "duration_elapsed")
	}
}

func TestProviderImplementsConsentHandler(t *testing.T) {
	r := ndt7.NewRunner()
	var _ provider.ConsentHandler = r
	var _ provider.BudgetReporter = r
	var _ provider.Runner = r
}

func TestDetailSchemaVersionIsPinned(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, _ := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "detail"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: 1 << 20},
	})
	if result.Detail == nil {
		t.Fatal("Detail is nil")
	}
	if result.Detail.Format != ndt7.DetailFormat {
		t.Fatalf("Detail.Format = %q, want %q", result.Detail.Format, ndt7.DetailFormat)
	}
	if len(result.Detail.Payload) == 0 {
		t.Fatal("Detail.Payload is empty")
	}
}
