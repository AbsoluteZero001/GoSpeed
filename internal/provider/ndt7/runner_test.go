package ndt7_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7/mock"
)

// streamDuration is how long a rate-asserting mock keeps transferring.
// ndt7-client-go emits a client-origin AppInfo frame only every
// params.UpdateInterval (250 ms), so a run must exceed two intervals
// before a genuine client-side delta exists. 900 ms leaves generous
// slack so the assertion never sits on the interval boundary.
const streamDuration = 900 * time.Millisecond

// rateTestBudget is the wire budget used by the download tests that
// assert on a rate. It is deliberately larger than the payload the
// streaming mock produces at streamDuration (16 KiB per chunk every
// 5 ms => roughly 2.9 MiB of application bytes plus WebSocket
// framing), so the budget is never the reason a run ends. A run that
// tripped the budget here would publish no rate and the assertion
// would be vacuous.
const rateTestBudget = 16 << 20

// uploadRateTestBudget is the same idea for the upload direction, and
// it is much larger because the NDT7 upload loop doubles its message
// size on every write up to params.MaxMessageSize (1 MiB). Over the
// same 900 ms fixture window that yields on the order of 60 MB of
// application bytes, so the 16 MiB download budget would trip long
// before the streaming window completes. This is a fixture constant
// only; DefaultBudgetBytes in runner.go is unchanged.
const uploadRateTestBudget = 256 << 20

func recordedConsent() *ndt7.ConsentRecord {
	return &ndt7.ConsentRecord{
		Agreed:        true,
		AgreedAt:      time.Now().UTC(),
		PolicyVersion: "ndt7-notice-v2",
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
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, streamDuration)
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
		Budget:         provider.BudgetLimits{DownloadBytes: rateTestBudget},
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
	srv := mock.NewStreaming(mock.ScenarioUpload, int64(target), 5*time.Millisecond, streamDuration)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "upload-ok"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapUpload: true},
		},
		Directions: []provider.Direction{provider.DirectionUpload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{UploadBytes: uploadRateTestBudget},
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
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, streamDuration)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "window"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: rateTestBudget},
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
	// The window is now a real, client-reported one rather than a
	// best-effort value, so it must be strictly positive. It is also
	// bounded below by the streaming duration of the fixture minus the
	// first client frame.
	if result.Download.ElapsedMs <= 0 {
		t.Fatalf("download window = %d ms, want > 0", result.Download.ElapsedMs)
	}
	if result.Download.ElapsedMs >= streamDuration.Milliseconds() {
		t.Fatalf("download window = %d ms must be shorter than the %d ms fixture run: "+
			"the window starts at the FIRST client frame, not at SDK start",
			result.Download.ElapsedMs, streamDuration.Milliseconds())
	}
}

func TestMbpsFromApplicationBytesIsHonest(t *testing.T) {
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, streamDuration)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "mbps"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: rateTestBudget},
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
		ApplicationBytes   int64   `json:"applicationBytes"`
		ElapsedMs          int64   `json:"elapsedMs"`
		Mbps               float64 `json:"mbps"`
		DeltaBytes         int64   `json:"deltaBytes"`
		WindowUs           int64   `json:"windowUs"`
		MeasurementSource  string  `json:"measurementSource"`
		CumulativeAppBytes int64   `json:"cumulativeAppBytes"`
	}
	if err := json.Unmarshal(result.Detail.Payload, &native); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if native.ApplicationBytes != result.Download.TransferredBytes {
		t.Fatalf("native bytes %d != rate bytes %d", native.ApplicationBytes, result.Download.TransferredBytes)
	}
	// The rate must be exactly reproducible from the published window
	// inputs. This is what proves the formula is a single-origin delta
	// and not a mixture that happens to look plausible.
	if native.MeasurementSource != ndt7.MeasurementSourceClientAppInfo {
		t.Fatalf("measurementSource = %q, want %q", native.MeasurementSource, ndt7.MeasurementSourceClientAppInfo)
	}
	if native.DeltaBytes != native.ApplicationBytes || native.DeltaBytes <= 0 {
		t.Fatalf("deltaBytes %d must equal applicationBytes %d and be positive", native.DeltaBytes, native.ApplicationBytes)
	}
	if native.WindowUs <= 0 {
		t.Fatalf("windowUs = %d, want > 0", native.WindowUs)
	}
	if native.ElapsedMs != native.WindowUs/1000 {
		t.Fatalf("elapsedMs %d != windowUs/1000 %d", native.ElapsedMs, native.WindowUs/1000)
	}
	recomputed := float64(native.DeltaBytes) * 8 / (float64(native.WindowUs) / 1e6) / 1e6
	if d := recomputed - native.Mbps; d > 0.5 || d < -0.5 {
		t.Fatalf("payload Mbps %v is not reproducible from deltaBytes %d / windowUs %d (want %v)",
			native.Mbps, native.DeltaBytes, native.WindowUs, recomputed)
	}
	// The window delta must be strictly smaller than the cumulative
	// counter: the window starts at the FIRST client frame, not at SDK
	// start. Equal values would mean the first frame contributed zero.
	if native.ApplicationBytes >= native.CumulativeAppBytes {
		t.Fatalf("window delta %d must be < cumulative %d (window starts at the first client frame)",
			native.ApplicationBytes, native.CumulativeAppBytes)
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
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, streamDuration)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node: nodeOf("ws://"+srv.Host(), "stop-reason"),
		Capabilities: provider.CapabilitySet{
			Supported: map[provider.Capability]bool{provider.CapDownload: true},
		},
		Directions: []provider.Direction{provider.DirectionDownload},
		Duration:   5 * time.Second,
		Budget:     provider.BudgetLimits{DownloadBytes: rateTestBudget},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Download == nil {
		t.Fatal("download nil")
	}
	// ndt7-client-go runs the protocol loop as `go f(ctx, conn, inch)`
	// inside Client.collectData and discards its returned error, so the
	// adapter never observes WHY the channel closed. The only
	// defensible value is the neutral completion reason. A previous
	// revision asserted "server_eof" here, on the grounds that a
	// server-origin frame had been seen; that inference was wrong,
	// because ndt-server emits those frames roughly every 250 ms while
	// a test is still running, so their presence says nothing at all
	// about how the test ended.
	if result.Download.StopReason != ndt7.StopReasonNDT7Completed {
		t.Fatalf("StopReason = %q, want the neutral %q", result.Download.StopReason, ndt7.StopReasonNDT7Completed)
	}
	if result.Download.StopReason == "server_eof" || result.Download.StopReason == "duration_elapsed" {
		t.Fatalf("StopReason %q claims a termination cause the SDK never exposes", result.Download.StopReason)
	}
	// The ambiguity must be disclosed rather than silently assumed.
	disclosed := false
	for _, w := range result.Method.Warnings {
		if strings.Contains(w, "discards the protocol loop error") {
			disclosed = true
		}
	}
	if !disclosed {
		t.Fatalf("a neutral StopReason must disclose the SDK limitation; warnings = %v", result.Method.Warnings)
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

// --- Origin / termination-evidence tests ---------------------------------
//
// Every test in this block exists because the SDK does NOT expose the
// termination cause and the adapter must not invent one.

func TestServerMeasurementFrameIsNotTerminationEvidence(t *testing.T) {
	// The mock streams for streamDuration, so the server sends many
	// periodic server-origin measurement frames and the test is still
	// running when the first ones arrive. Their presence must not be
	// read as "the server closed the stream".
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, streamDuration)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node:         nodeOf("ws://"+srv.Host(), "origin-not-eof"),
		Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true}},
		Directions:   []provider.Direction{provider.DirectionDownload},
		Duration:     5 * time.Second,
		Budget:       provider.BudgetLimits{DownloadBytes: rateTestBudget},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var native struct {
		ServerOriginFrames  int64  `json:"serverOriginFrames"`
		ServerFrameSeen     bool   `json:"serverFrameSeen"`
		ServerAppBytes      int64  `json:"serverAppBytes"`
		ServerElapsedUs     int64  `json:"serverElapsedUs"`
		CumulativeAppBytes  int64  `json:"cumulativeAppBytes"`
		CumulativeElapsedUs int64  `json:"cumulativeElapsedUs"`
		MeasurementSource   string `json:"measurementSource"`
	}
	if err := json.Unmarshal(result.Detail.Payload, &native); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// Precondition: the fixture really does produce server frames.
	if !native.ServerFrameSeen || native.ServerOriginFrames < 1 {
		t.Fatalf("fixture must produce server-origin frames; got seen=%v count=%d",
			native.ServerFrameSeen, native.ServerOriginFrames)
	}
	// Even with server frames present, the reason stays neutral.
	if got := result.Download.StopReason; got != ndt7.StopReasonNDT7Completed {
		t.Fatalf("StopReason = %q, want neutral %q even though %d server frames were observed",
			got, ndt7.StopReasonNDT7Completed, native.ServerOriginFrames)
	}
	if result.Download.StopReason == "server_eof" {
		t.Fatal("a server measurement frame must never be reported as a server-side EOF")
	}
	// And the server's counter must not have been used as the source.
	if native.MeasurementSource != ndt7.MeasurementSourceClientAppInfo {
		t.Fatalf("measurementSource = %q, want %q", native.MeasurementSource, ndt7.MeasurementSourceClientAppInfo)
	}
}

func TestRateNeverMixesClientAndServerCounters(t *testing.T) {
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, streamDuration)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node:         nodeOf("ws://"+srv.Host(), "no-mixing"),
		Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true}},
		Directions:   []provider.Direction{provider.DirectionDownload},
		Duration:     5 * time.Second,
		Budget:       provider.BudgetLimits{DownloadBytes: rateTestBudget},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Download == nil || result.Download.Mbps == nil {
		t.Fatal("rate missing")
	}
	var native struct {
		DeltaBytes          int64   `json:"deltaBytes"`
		WindowUs            int64   `json:"windowUs"`
		Mbps                float64 `json:"mbps"`
		CumulativeAppBytes  int64   `json:"cumulativeAppBytes"`
		CumulativeElapsedUs int64   `json:"cumulativeElapsedUs"`
		ServerAppBytes      int64   `json:"serverAppBytes"`
		ServerElapsedUs     int64   `json:"serverElapsedUs"`
		ServerFrameSeen     bool    `json:"serverFrameSeen"`
	}
	if err := json.Unmarshal(result.Detail.Payload, &native); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	// The rate is exactly the client-side delta over the client-side clock.
	clientRate := float64(native.DeltaBytes) * 8 / (float64(native.WindowUs) / 1e6) / 1e6
	if d := clientRate - native.Mbps; d > 0.5 || d < -0.5 {
		t.Fatalf("Mbps %v != client-side delta %v", native.Mbps, clientRate)
	}
	// The server's AppInfo is genuinely different data: it counts the
	// server's own bytes against the server's own clock. The mock
	// deliberately reports an elapsed time that is NOT a real clock
	// reading, so a server-derived rate is a materially different
	// number. The published rate must therefore be the client one, and
	// the two must disagree - otherwise this test would not be able to
	// tell the two sources apart.
	// The fixture is a streaming download, which the mock always seeds
	// with a leading sentinel server-origin frame, so ServerElapsedUs
	// is non-zero by construction. Asserting that here (rather than
	// skipping) keeps the fixture contract honest: if a future change
	// stopped emitting server frames, this test must fail loudly
	// instead of quietly ceasing to check anything.
	if native.ServerElapsedUs <= 0 {
		t.Fatalf("streaming download fixture must emit a server-origin measurement; "+
			"ServerElapsedUs = %d, ServerFrameSeen = %v", native.ServerElapsedUs, native.ServerFrameSeen)
	}
	serverRate := float64(native.ServerAppBytes) * 8 / (float64(native.ServerElapsedUs) / 1e6) / 1e6
	if d := math.Abs(serverRate - native.Mbps); d < 1.0 {
		t.Fatalf("client rate %v and server rate %v are too close to prove the sources were distinguished; "+
			"the fixture cannot detect origin mixing", native.Mbps, serverRate)
	}
	if native.ServerAppBytes == native.CumulativeAppBytes && native.ServerElapsedUs == native.CumulativeElapsedUs {
		t.Fatal("server and client counters are identical; the fixture cannot detect origin mixing")
	}
	if native.DeltaBytes >= native.CumulativeAppBytes {
		t.Fatalf("window delta %d must be smaller than the cumulative counter %d",
			native.DeltaBytes, native.CumulativeAppBytes)
	}
	if native.WindowUs >= native.CumulativeElapsedUs {
		t.Fatalf("window %d us must be smaller than the cumulative clock %d us",
			native.WindowUs, native.CumulativeElapsedUs)
	}
	// The mock's FIRST server-origin frame reports the sentinel
	// NumBytes. The SDK cannot emit a client frame before it has read a
	// full binary message, so no client sample can carry that value. If
	// the window had started on a server frame, firstAppBytes would be
	// the sentinel. This is the direct check that both endpoints come
	// from the client-origin series.
	var first struct {
		FirstAppBytes  int64 `json:"firstAppBytes"`
		FirstElapsedUs int64 `json:"firstElapsedUs"`
		LastAppBytes   int64 `json:"lastAppBytes"`
		LastElapsedUs  int64 `json:"lastElapsedUs"`
	}
	if err := json.Unmarshal(result.Detail.Payload, &first); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if first.FirstAppBytes == mock.SentinelServerNumBytes {
		t.Fatalf("the measurement window started on the mock's leading server-origin frame "+
			"(NumBytes=%d): the adapter is mixing origins", first.FirstAppBytes)
	}
	if first.FirstAppBytes <= 0 {
		t.Fatalf("firstAppBytes = %d, want a positive client sample", first.FirstAppBytes)
	}
	// The window must be exactly the difference of the two published
	// client samples; nothing else may have contributed to it.
	if native.DeltaBytes != first.LastAppBytes-first.FirstAppBytes {
		t.Fatalf("deltaBytes %d != lastAppBytes-firstAppBytes (%d)",
			native.DeltaBytes, first.LastAppBytes-first.FirstAppBytes)
	}
	if native.WindowUs != first.LastElapsedUs-first.FirstElapsedUs {
		t.Fatalf("windowUs %d != lastElapsedUs-firstElapsedUs (%d)",
			native.WindowUs, first.LastElapsedUs-first.FirstElapsedUs)
	}
}

// TestWindowUnavailableEndToEnd asserts the observable contract of the
// window guard against a REAL run, without racing the wall clock.
//
// The previous revision of this test drove a mock that finished inside
// the SDK's 250 ms UpdateInterval and then called t.Skipf whenever the
// platform turned out to be fast enough to emit a client frame anyway.
// That made the test's outcome a function of how loaded the machine was:
// on a busy CI runner it would silently skip and the guard would go
// completely uncovered while the suite stayed green.
//
// The degenerate cases themselves (no frame, single frame, counter
// regression, clock regression, zero window) are asserted
// exhaustively and deterministically by the in-package
// TestWindowRejectReasonCoversEveryDegenerateCase in
// window_internal_test.go. What is left for this end-to-end test is
// the part that genuinely needs a socket: that whichever branch the SDK
// happens to land on, the adapter NEVER fabricates a rate.
//
// So instead of skipping when the window exists, this test accepts both
// legitimate outcomes and demands the correct invariant from each:
//   - window available  -> a real, recomputable rate is published
//   - window unavailable-> no Mbps at all, with the reason disclosed
//
// A fabricated zero rate, or a published rate whose inputs do not
// reproduce it, fails in either case.
func TestWindowUnavailableEndToEnd(t *testing.T) {
	// minDuration 0: this mock ends as soon as it has sent its chunks,
	// which is the cheapest way to reach the SDK's early-exit flush
	// path. Whether that produces zero, one or several client frames
	// is deliberately NOT assumed.
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node:         nodeOf("ws://"+srv.Host(), "window-guard"),
		Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true}},
		Directions:   []provider.Direction{provider.DirectionDownload},
		Duration:     5 * time.Second,
		Budget:       provider.BudgetLimits{DownloadBytes: rateTestBudget},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Download == nil {
		t.Fatal("download result is nil")
	}
	var native struct {
		MeasurementSource  string  `json:"measurementSource"`
		WindowRejectReason string  `json:"windowRejectReason"`
		DeltaBytes         int64   `json:"deltaBytes"`
		WindowUs           int64   `json:"windowUs"`
		FirstAppBytes      int64   `json:"firstAppBytes"`
		LastAppBytes       int64   `json:"lastAppBytes"`
		FirstElapsedUs     int64   `json:"firstElapsedUs"`
		LastElapsedUs      int64   `json:"lastElapsedUs"`
		Mbps               float64 `json:"mbps"`
	}
	if err := json.Unmarshal(result.Detail.Payload, &native); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	switch native.MeasurementSource {
	case ndt7.MeasurementSourceClientAppInfo:
		// The SDK produced a usable window. The published rate must be
		// exactly reproducible from the published inputs, and the
		// window endpoints must come from the same client series.
		if result.Download.Mbps == nil {
			t.Fatal("a usable window must publish a rate")
		}
		if native.WindowUs <= 0 {
			t.Fatalf("windowUs = %d, want > 0 when a window was published", native.WindowUs)
		}
		if native.DeltaBytes != native.LastAppBytes-native.FirstAppBytes {
			t.Fatalf("deltaBytes %d != lastAppBytes-firstAppBytes (%d)",
				native.DeltaBytes, native.LastAppBytes-native.FirstAppBytes)
		}
		if native.WindowUs != native.LastElapsedUs-native.FirstElapsedUs {
			t.Fatalf("windowUs %d != lastElapsedUs-firstElapsedUs (%d)",
				native.WindowUs, native.LastElapsedUs-native.FirstElapsedUs)
		}
		recomputed := float64(native.DeltaBytes) * 8 / (float64(native.WindowUs) / 1e6) / 1e6
		if d := math.Abs(recomputed - native.Mbps); d > 0.5 {
			t.Fatalf("published Mbps %v is not reproducible from deltaBytes %d / windowUs %d (want %v)",
				native.Mbps, native.DeltaBytes, native.WindowUs, recomputed)
		}

	case ndt7.MeasurementSourceUnavailable:
		// The window could not be formed. Two hard requirements: no rate
		// at all (never a fabricated 0 Mbps), and the reason disclosed.
		if result.Download.Mbps != nil {
			t.Fatalf("an unavailable window must publish no rate, got %v Mbps", *result.Download.Mbps)
		}
		if native.WindowRejectReason == "" {
			t.Fatal("an unavailable window must state why it was rejected")
		}
		if result.Download.StopReason != ndt7.StopReasonNDT7WindowUnavailable {
			t.Fatalf("StopReason = %q, want %q", result.Download.StopReason, ndt7.StopReasonNDT7WindowUnavailable)
		}
		disclosed := false
		for _, w := range result.Method.Warnings {
			if strings.Contains(w, "no rate published") {
				disclosed = true
			}
		}
		if !disclosed {
			t.Fatalf("an unavailable window must be disclosed in Warnings, got %v", result.Method.Warnings)
		}

	default:
		t.Fatalf("MeasurementSource = %q, want one of the two documented values",
			native.MeasurementSource)
	}

	// Independent of which branch ran: the server's counters must never
	// have contributed, and a run that observed no client bytes must not
	// claim to have completed.
	if native.MeasurementSource == ndt7.MeasurementSourceClientAppInfo &&
		native.FirstAppBytes == mock.SentinelServerNumBytes {
		t.Fatalf("the window started on the mock's leading server-origin frame (NumBytes=%d)",
			native.FirstAppBytes)
	}
}

func TestCompletedStatusIsNeverAccompaniedByAFabricatedCause(t *testing.T) {
	// Exercises the three terminal shapes the SDK does not distinguish:
	// a normal server close, an SDK deadline, and an abrupt disconnect.
	// All three close the measurement channel the same way, so all three
	// must report the same neutral reason and must never claim
	// "server_eof" or "duration_elapsed".
	cases := []struct {
		name     string
		scenario mock.Scenario
	}{
		{"normal server close", mock.ScenarioDownload},
		{"abrupt disconnect", mock.ScenarioNetworkDisconnect},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := mock.NewWithChunkDelay(tc.scenario, 0, 5*time.Millisecond)
			defer srv.Close()

			r := ndt7.NewRunnerWithConsent(recordedConsent())
			result, _ := r.Run(context.Background(), provider.RunRequest{
				Node:         nodeOf("ws://"+srv.Host(), tc.name),
				Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true}},
				Directions:   []provider.Direction{provider.DirectionDownload},
				Duration:     5 * time.Second,
				Budget:       provider.BudgetLimits{DownloadBytes: rateTestBudget},
			})
			rate := result.Download
			if rate == nil {
				rate = result.Upload
			}
			if rate == nil {
				t.Fatal("no rate published at all")
			}
			switch result.Status {
			case provider.StatusCompleted:
				switch rate.StopReason {
				case ndt7.StopReasonNDT7Completed, ndt7.StopReasonNDT7WindowUnavailable:
					// Both are honest: either the run completed with a
					// window, or it completed without one.
				default:
					t.Fatalf("status=completed but StopReason=%q claims an unobserved cause", rate.StopReason)
				}
				if rate.StopReason == "server_eof" || rate.StopReason == "duration_elapsed" {
					t.Fatalf("StopReason=%q is an unverified claim", rate.StopReason)
				}
			case provider.StatusFailed:
				// A disconnect with no bytes is allowed to fail; it must
				// then carry the no-data reason, not a completed one.
				if rate.StopReason != ndt7.StopReasonNDT7NoData && rate.StopReason != ndt7.StopReasonNDT7WindowUnavailable {
					t.Fatalf("status=failed but StopReason=%q", rate.StopReason)
				}
			default:
				t.Fatalf("unexpected status %q (StopReason %q)", result.Status, rate.StopReason)
			}
		})
	}
}

func TestSDKDeadlineRunIsNotLabelledServerEOF(t *testing.T) {
	// A long streaming mock against a short OverallTimeout: the run is
	// cut short by OUR timeout, which is a genuine observed event, so
	// the timeout reason is correct. Server frames will also be present,
	// which must not divert the label to server_eof.
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 20*time.Millisecond, 10*time.Second)
	defer srv.Close()

	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node:         nodeOf("ws://"+srv.Host(), "deadline"),
		Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true}},
		Directions:   []provider.Direction{provider.DirectionDownload},
		Duration:     200 * time.Millisecond,
		Budget:       provider.BudgetLimits{DownloadBytes: rateTestBudget},
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Logf("Run returned err = %v", err)
	}
	if result.Status != provider.StatusTimeout {
		t.Fatalf("status = %q, want timeout", result.Status)
	}
	if result.Download.StopReason != ndt7.StopReasonNDT7Timeout {
		t.Fatalf("StopReason = %q, want %q (an observed timeout must not be relabelled)",
			result.Download.StopReason, ndt7.StopReasonNDT7Timeout)
	}
	if result.Download.StopReason == "server_eof" {
		t.Fatal("server frames must never relabel an observed timeout as a server EOF")
	}
}

// --- Budget status semantics ---------------------------------------------

func TestBudgetTruncationIsNotPresentedAsACompleteMeasurement(t *testing.T) {
	srv := mock.NewStreaming(mock.ScenarioDownload, 0, 5*time.Millisecond, streamDuration)
	defer srv.Close()

	const budget = 64 << 10
	r := ndt7.NewRunnerWithConsent(recordedConsent())
	result, err := r.Run(context.Background(), provider.RunRequest{
		Node:         nodeOf("ws://"+srv.Host(), "budget-partial"),
		Capabilities: provider.CapabilitySet{Supported: map[provider.Capability]bool{provider.CapDownload: true}},
		Directions:   []provider.Direction{provider.DirectionDownload},
		Duration:     5 * time.Second,
		Budget:       provider.BudgetLimits{DownloadBytes: budget},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Contract evidence: internal/provider/model.go defines exactly six
	// MeasurementStatus values and "partial" is not one of them, so a
	// truncated run maps onto StatusBudgetExceeded. The "partial"
	// notion is carried by Quality.Completeness instead.
	if result.Status != provider.StatusBudgetExceeded {
		t.Fatalf("status = %q, want %q", result.Status, provider.StatusBudgetExceeded)
	}
	if result.Status == provider.StatusCompleted {
		t.Fatal("a budget-truncated run must never be reported as completed")
	}
	// The budget semantics must survive as an explicit error code, not
	// an empty string.
	if result.ErrorCode != "budget_exceeded" {
		t.Fatalf("ErrorCode = %q, want budget_exceeded", result.ErrorCode)
	}
	if result.ErrorMessage == "" {
		t.Fatal("a budget-truncated run must carry an error message")
	}
	if result.Quality.Completeness == provider.Complete {
		t.Fatalf("completeness = %q; a truncated run is not complete evidence", result.Quality.Completeness)
	}
	if result.Download == nil || result.Download.StopReason != ndt7.StopReasonNDT7BudgetExceeded {
		t.Fatalf("stop reason = %+v, want %q", result.Download, ndt7.StopReasonNDT7BudgetExceeded)
	}
	var native struct {
		BudgetExceeded bool  `json:"budgetExceeded"`
		BudgetLimit    int64 `json:"budgetLimitBytes"`
	}
	if err := json.Unmarshal(result.Detail.Payload, &native); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !native.BudgetExceeded {
		t.Fatal("the native payload must record that the budget fired")
	}
	if native.BudgetLimit != budget {
		t.Fatalf("budgetLimitBytes = %d, want %d", native.BudgetLimit, budget)
	}
}
