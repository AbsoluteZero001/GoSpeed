package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/server"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// This file covers the GoSpeed HTTP provider behaviour: capability negotiation,
// request validation, status classification and the end to end run over a
// localhost GoSpeed server. The engine to FinalResult field mapping is pinned
// separately in mapping_test.go.
//
// Every server in this file listens on loopback. No test contacts a public
// network service.

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// newTestProvider returns a provider that owns its HTTP client, so the test can
// release pooled connections when it ends.
func newTestProvider(t *testing.T) *GoSpeedHTTPProvider {
	t.Helper()
	provider := NewGoSpeedHTTP(nil)
	t.Cleanup(provider.CloseIdleConnections)
	return provider
}

// realGoSpeedServer starts the production GoSpeed test server on loopback. It
// is the faithful fixture: the provider is checked against the same server the
// CLI and the desktop client talk to.
func realGoSpeedServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, err := server.New(server.Config{
		MaxUploadBytes:          32 << 20,
		MaxDownloadBytes:        64 << 20,
		MaxDownloadDuration:     30 * time.Second,
		DefaultDownloadDuration: 5 * time.Second,
		MaxConcurrentTests:      8,
		MaxConnectionsPerTest:   speedtest.MaxConnections,
		ReadTimeout:             60 * time.Second,
		WriteTimeout:            60 * time.Second,
	})
	if err != nil {
		t.Fatalf("build gospeed server: %v", err)
	}
	testServer := httptest.NewServer(srv.Handler())
	t.Cleanup(testServer.Close)
	return testServer
}

// stubOptions configures the capability answering stub server.
type stubOptions struct {
	// omitCapabilities answers 404 on GET /capabilities (legacy server).
	omitCapabilities bool
	// capabilitiesStatus, when non-zero, is returned instead of a payload.
	capabilitiesStatus int
	// capabilities is the advertised list. A non-nil empty slice advertises
	// nothing, which is different from omitting the endpoint.
	capabilities []string
	// limits are the advertised server limits.
	limits map[string]any
}

// stubServer is a minimal GoSpeed shaped server used to drive the negotiation
// and failure paths that the production server cannot be configured into.
func stubServer(t *testing.T, options stubOptions) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"gospeed","version":"0.3.0"}`))
	})
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		switch {
		case options.omitCapabilities:
			http.NotFound(w, nil)
		case options.capabilitiesStatus != 0:
			http.Error(w, "capabilities unavailable", options.capabilitiesStatus)
		default:
			capabilities := options.capabilities
			if capabilities == nil {
				capabilities = []string{"latency", "download", "upload"}
			}
			limits := options.limits
			if limits == nil {
				limits = map[string]any{}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"protocol_version": 1,
				"server_version":   "0.3.0",
				"capabilities":     capabilities,
				"limits":           limits,
			})
		}
	})
	testServer := httptest.NewServer(mux)
	t.Cleanup(testServer.Close)
	return testServer
}

// nodeFor builds the provider node of a loopback test server.
func nodeFor(endpoint string) Node {
	return Node{
		ID:        "loopback-1",
		Name:      "Loopback",
		Endpoint:  endpoint,
		Scope:     ScopeLocal,
		Provider:  IDGoSpeedHTTP,
		Ownership: OwnershipSelfHosted,
		Discovery: DiscoveryConfigured,
	}
}

// unitNode is a node with a syntactically valid endpoint for the pure mapping
// tests. optionsFor only builds a target, it never dials, so the address is
// never contacted.
func unitNode() Node {
	return Node{ID: "unit", Name: "Unit", Endpoint: "http://127.0.0.1:1", Scope: ScopeLocal, Provider: IDGoSpeedHTTP}
}

// allCapabilities is the set a fully capable GoSpeed server negotiates.
func allCapabilities() CapabilitySet {
	return CapabilitySet{
		Supported: map[Capability]bool{
			CapDownload: true,
			CapUpload:   true,
			CapLatency:  true,
			CapJitter:   true,
		},
		Notes: map[Capability]string{
			CapDownload: NoteDownloadSource,
			CapUpload:   NoteUploadSource,
			CapLatency:  NoteLatencySource,
			CapJitter:   NoteJitterSource,
		},
	}
}

// recordingSink collects the progress events a run produced.
type recordingSink struct {
	mu     sync.Mutex
	events []ProgressEvent
}

func (s *recordingSink) OnProgress(event ProgressEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *recordingSink) snapshot() []ProgressEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ProgressEvent(nil), s.events...)
}

func (s *recordingSink) phases() []string {
	var phases []string
	seen := map[string]bool{}
	for _, event := range s.snapshot() {
		if event.Phase != "" && !seen[event.Phase] {
			seen[event.Phase] = true
			phases = append(phases, event.Phase)
		}
	}
	return phases
}

// ---------------------------------------------------------------------------
// identity
// ---------------------------------------------------------------------------

func TestProviderReportsItsIdentity(t *testing.T) {
	if got := NewGoSpeedHTTP(nil).ID(); got != IDGoSpeedHTTP {
		t.Fatalf("ID() = %q, want %q", got, IDGoSpeedHTTP)
	}
}

func TestProviderImplementsRunner(t *testing.T) {
	var runner Runner = NewGoSpeedHTTP(nil)
	if runner.ID() != IDGoSpeedHTTP {
		t.Fatalf("Runner.ID() = %q", runner.ID())
	}
}

// TestProviderIsNotAConsentProvider documents that GoSpeed HTTP nodes need no
// consent flow: they come from the user's own configuration and the provider
// does not implement ConsentHandler or Discoverer.
func TestProviderIsNotAConsentProvider(t *testing.T) {
	provider := NewGoSpeedHTTP(nil)
	if _, ok := interface{}(provider).(ConsentHandler); ok {
		t.Fatal("GoSpeed HTTP provider must not implement ConsentHandler")
	}
	if _, ok := interface{}(provider).(Discoverer); ok {
		t.Fatal("GoSpeed HTTP provider must not implement Discoverer")
	}
	if plan := provider.Plan(); plan != (BudgetLimits{}) {
		t.Fatalf("Plan() = %+v, want the zero budget", plan)
	}
}

// ---------------------------------------------------------------------------
// capability negotiation
// ---------------------------------------------------------------------------

func TestNegotiateDeclaresOnlyAdvertisedCapabilities(t *testing.T) {
	testServer := stubServer(t, stubOptions{capabilities: []string{"download", "latency"}})
	provider := newTestProvider(t)

	caps, err := provider.Negotiate(context.Background(), nodeFor(testServer.URL))
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}

	if !caps.Supports(CapDownload) {
		t.Error("download was advertised but not declared")
	}
	if caps.Supports(CapUpload) {
		t.Error("upload was not advertised but was declared")
	}
	if !caps.Supports(CapLatency) {
		t.Error("latency was advertised but not declared")
	}
	if !caps.Supports(CapJitter) {
		t.Error("jitter must be declared with latency, it is derived from the latency samples")
	}
	if caps.Supports(CapPacketLoss) {
		t.Error("packet loss must never be declared: the engine runs no packet level measurement")
	}
	if caps.Supports(CapLoadedLatency) {
		t.Error("loaded latency must never be declared")
	}
	if caps.Note(CapLatency) != NoteLatencySource {
		t.Errorf("latency note = %q, want %q", caps.Note(CapLatency), NoteLatencySource)
	}
	if strings.Contains(caps.Note(CapLatency), "icmp ping") &&
		!strings.Contains(caps.Note(CapLatency), "not an icmp ping") {
		t.Error("the latency note must state that this is not an ICMP ping")
	}
}

func TestNegotiateAcceptsLegacyServerWithoutCapabilities(t *testing.T) {
	testServer := stubServer(t, stubOptions{omitCapabilities: true})
	provider := newTestProvider(t)

	caps, err := provider.Negotiate(context.Background(), nodeFor(testServer.URL))
	if err != nil {
		t.Fatalf("legacy negotiation must keep working, got %v", err)
	}
	if !caps.Supports(CapDownload) || !caps.Supports(CapUpload) || !caps.Supports(CapLatency) {
		t.Fatalf("legacy server lost a capability: %+v", caps.Supported)
	}
	if !strings.Contains(caps.Note(CapDownload), "inferred") {
		t.Errorf("inferred capabilities must be labelled, note = %q", caps.Note(CapDownload))
	}
}

// TestNegotiateNeverGuessesAfterAFailedProbe is the red line of negotiation: a
// probe that did not answer must not turn into an optimistic default set.
func TestNegotiateNeverGuessesAfterAFailedProbe(t *testing.T) {
	for name, status := range map[string]int{
		"server error": http.StatusInternalServerError,
		"unauthorized": http.StatusUnauthorized,
		"not json":     http.StatusOK,
	} {
		t.Run(name, func(t *testing.T) {
			testServer := stubServer(t, stubOptions{capabilitiesStatus: status})
			provider := newTestProvider(t)

			caps, err := provider.Negotiate(context.Background(), nodeFor(testServer.URL))
			if err == nil {
				t.Fatal("expected a negotiation error when the probe did not answer")
			}
			if !errors.Is(err, ErrNegotiationFailed) {
				t.Errorf("error = %v, want ErrNegotiationFailed", err)
			}
			for capability, supported := range caps.Supported {
				if supported {
					t.Errorf("capability %q was declared although the probe failed", capability)
				}
			}
		})
	}
}

func TestNegotiateConfirmsNothingWhenNothingIsAdvertised(t *testing.T) {
	testServer := stubServer(t, stubOptions{capabilities: []string{}})
	provider := newTestProvider(t)

	caps, err := provider.Negotiate(context.Background(), nodeFor(testServer.URL))
	if err == nil {
		t.Fatal("a server that advertises nothing must not yield a usable set")
	}
	if len(caps.Supported) != 0 {
		t.Fatalf("caps = %+v, want none", caps.Supported)
	}
}

func TestNegotiateRejectsNodeWithoutEndpoint(t *testing.T) {
	provider := newTestProvider(t)

	_, err := provider.Negotiate(context.Background(), Node{ID: "no-endpoint"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

func TestNegotiateHonoursContextCancellation(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.Negotiate(ctx, nodeFor(testServer.URL))
	if err == nil {
		t.Fatal("a cancelled negotiation must return an error")
	}
	if !errors.Is(err, ErrNegotiationFailed) {
		t.Errorf("error = %v, want the probe failure to be reported as ErrNegotiationFailed", err)
	}
}

// ---------------------------------------------------------------------------
// request validation
// ---------------------------------------------------------------------------

func TestRunRefusesDirectionOutsideNegotiatedSet(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	// A download only node: asking for upload must fail instead of trimming.
	request := RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true}},
		Directions:   []Direction{DirectionUpload},
	}

	result, err := provider.Run(context.Background(), request)
	if !errors.Is(err, ErrCapabilityNotNegotiated) {
		t.Fatalf("error = %v, want ErrCapabilityNotNegotiated", err)
	}
	if result.Status != StatusFailed {
		t.Errorf("status = %q, want %q", result.Status, StatusFailed)
	}
	if result.ErrorCode != "capability_not_negotiated" {
		t.Errorf("errorCode = %q", result.ErrorCode)
	}
	if result.Download != nil || result.Upload != nil {
		t.Error("a refused request must not produce any rate")
	}
}

func TestRunRefusesRequestWithoutAnyDirection(t *testing.T) {
	provider := newTestProvider(t)

	_, err := provider.Run(context.Background(), RunRequest{Node: Node{ID: "n"}})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

func TestRunRefusesUnknownDirection(t *testing.T) {
	provider := newTestProvider(t)

	_, err := provider.Run(context.Background(), RunRequest{
		Node:         Node{ID: "n"},
		Capabilities: allCapabilities(),
		Directions:   []Direction{Direction("sideways")},
	})
	if !errors.Is(err, ErrCapabilityNotNegotiated) {
		t.Fatalf("error = %v, want ErrCapabilityNotNegotiated", err)
	}
}

func TestSelectedDirectionsDefaultsToTheNegotiatedSet(t *testing.T) {
	got, err := selectedDirections(RunRequest{
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapUpload: true}},
	})
	if err != nil {
		t.Fatalf("selectedDirections: %v", err)
	}
	if len(got) != 1 || got[0] != DirectionUpload {
		t.Fatalf("directions = %v, want [upload]", got)
	}
}

func TestSelectedDirectionsDeduplicates(t *testing.T) {
	got, err := selectedDirections(RunRequest{
		Capabilities: allCapabilities(),
		Directions:   []Direction{DirectionDownload, DirectionDownload, DirectionUpload},
	})
	if err != nil {
		t.Fatalf("selectedDirections: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("directions = %v, want two distinct directions", got)
	}
}

// ---------------------------------------------------------------------------
// budget mapping
// ---------------------------------------------------------------------------

func TestEffectiveBudgetSplitsTotalAcrossDirections(t *testing.T) {
	budget, err := effectiveBudget(RunRequest{Budget: BudgetLimits{TotalBytes: 8 << 20}},
		[]Direction{DirectionDownload, DirectionUpload})
	if err != nil {
		t.Fatalf("effectiveBudget: %v", err)
	}
	if budget.DownloadBytes != 4<<20 || budget.UploadBytes != 4<<20 {
		t.Fatalf("budget = %+v, want 4 MiB per direction", budget)
	}
}

func TestEffectiveBudgetDirectionLimitCanOnlyLower(t *testing.T) {
	budget, err := effectiveBudget(RunRequest{
		Budget: BudgetLimits{TotalBytes: 8 << 20, DownloadBytes: 1 << 20},
	}, []Direction{DirectionDownload, DirectionUpload})
	if err != nil {
		t.Fatalf("effectiveBudget: %v", err)
	}
	if budget.DownloadBytes != 1<<20 {
		t.Errorf("download budget = %d, want the lower direction limit of 1 MiB", budget.DownloadBytes)
	}
	if budget.UploadBytes != 4<<20 {
		t.Errorf("upload budget = %d, want the total share of 4 MiB", budget.UploadBytes)
	}
}

func TestEffectiveBudgetRejectsUnsatisfiableTotal(t *testing.T) {
	if _, err := effectiveBudget(RunRequest{Budget: BudgetLimits{TotalBytes: 1}},
		[]Direction{DirectionDownload, DirectionUpload}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

func TestEffectiveBudgetRejectsNegativeLimit(t *testing.T) {
	if _, err := effectiveBudget(RunRequest{Budget: BudgetLimits{DownloadBytes: -1}},
		[]Direction{DirectionDownload}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

// TestOptionsForDerivesTimeoutAboveDuration pins the engine rule that a phase
// timeout must outlast the transfer window so the transfer can be confirmed.
func TestOptionsForDerivesTimeoutAboveDuration(t *testing.T) {
	opts, err := optionsFor(RunRequest{Duration: 90 * time.Second, Node: unitNode()},
		[]Direction{DirectionDownload}, BudgetLimits{}, "node")
	if err != nil {
		t.Fatalf("optionsFor: %v", err)
	}
	if opts.Timeout <= opts.Duration {
		t.Fatalf("timeout %s must be longer than duration %s", opts.Timeout, opts.Duration)
	}
}

// TestOptionsForUsesTheEngineDefaultConnections documents that the provider
// does not invent a connection count: RunRequest carries none.
func TestOptionsForUsesTheEngineDefaultConnections(t *testing.T) {
	opts, err := optionsFor(RunRequest{Node: unitNode()}, []Direction{DirectionDownload}, BudgetLimits{}, "node")
	if err != nil {
		t.Fatalf("optionsFor: %v", err)
	}
	if opts.Connections != speedtest.DefaultConnections {
		t.Fatalf("connections = %d, want the engine default %d", opts.Connections, speedtest.DefaultConnections)
	}
	if !opts.NegotiateCapabilities {
		t.Error("the engine must keep enforcing the advertised server limits during the run")
	}
}

func TestOptionsForAddsLatencyOnlyWhenNegotiated(t *testing.T) {
	withoutLatency, err := optionsFor(RunRequest{Node: unitNode()},
		[]Direction{DirectionDownload}, BudgetLimits{}, "node")
	if err != nil {
		t.Fatalf("optionsFor: %v", err)
	}
	if withoutLatency.Phases&speedtest.PhasesLatency != 0 {
		t.Error("the latency phase must not run when CapLatency was not negotiated")
	}

	withLatency, err := optionsFor(RunRequest{
		Node:         unitNode(),
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapLatency: true}},
	}, []Direction{DirectionDownload}, BudgetLimits{}, "node")
	if err != nil {
		t.Fatalf("optionsFor: %v", err)
	}
	if withLatency.Phases&speedtest.PhasesLatency == 0 {
		t.Error("the latency phase must run when CapLatency was negotiated")
	}
}

// ---------------------------------------------------------------------------
// status classification
// ---------------------------------------------------------------------------

// TestClassifyKeepsABudgetStopCompleted is the red line of budget handling. The
// engine's MaxBytes is an exact shared cap, so stopping exactly at the
// requested budget is a planned measurement, not a budget abort.
func TestClassifyKeepsABudgetStopCompleted(t *testing.T) {
	engineResult := speedtest.Result{
		Status: speedtest.StatusCompleted,
		Download: &speedtest.TransferResult{
			Bytes:             1 << 20,
			StopReason:        speedtest.StopReasonRequestedBytes,
			DurationNs:        time.Second,
			Mbps:              8,
			MeasurementWindow: speedtest.WindowDownload,
		},
	}

	status, code, message := classify(nil, engineResult, BudgetLimits{DownloadBytes: 1 << 20})
	if status != StatusCompleted {
		t.Fatalf("status = %q, want %q: a transfer that stops at the requested budget is complete", status, StatusCompleted)
	}
	if code != "" || message != "" {
		t.Errorf("a completed run must carry no error, got code=%q message=%q", code, message)
	}
}

func TestClassifySeparatesTerminalStates(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want MeasurementStatus
		code string
	}{
		{"canceled", context.Canceled, StatusCancelled, "cancelled"},
		{"timeout", context.DeadlineExceeded, StatusTimeout, "timeout"},
		{"wrapped cancel", errors.Join(errors.New("download: boom"), context.Canceled), StatusCancelled, "cancelled"},
		{"truncation", speedtest.ErrIncompleteTransfer, StatusFailed, "incomplete_transfer"},
		{"no data", speedtest.ErrNoDataTransferred, StatusFailed, "no_data_transferred"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, code, _ := classify(test.err, speedtest.Result{}, BudgetLimits{})
			if status != test.want {
				t.Errorf("status = %q, want %q", status, test.want)
			}
			if code != test.code {
				t.Errorf("code = %q, want %q", code, test.code)
			}
		})
	}
}

// TestClassifyReportsByteCountMismatchAsBudgetExceeded pins the only reachable
// budget_exceeded case for this provider: a budget that cannot be accounted for.
func TestClassifyReportsByteCountMismatchAsBudgetExceeded(t *testing.T) {
	err := errors.Join(errors.New("upload"), speedtest.ErrByteCountMismatch)

	status, code, _ := classify(err, speedtest.Result{}, BudgetLimits{UploadBytes: 1 << 20})
	if status != StatusBudgetExceeded || code != "budget_exceeded" {
		t.Fatalf("status=%q code=%q, want budget_exceeded", status, code)
	}

	// Without a budget in force the same engine error is a plain failure.
	status, code, _ = classify(err, speedtest.Result{}, BudgetLimits{})
	if status != StatusFailed || code != "byte_count_mismatch" {
		t.Fatalf("status=%q code=%q, want a plain failed run", status, code)
	}
}

func TestErrorCodeForMapsEngineSentinels(t *testing.T) {
	tests := map[error]string{
		speedtest.ErrInvalidOptions:         "invalid_options",
		speedtest.ErrInvalidTarget:          "invalid_target",
		speedtest.ErrUnsupportedProtocol:    "unsupported_protocol",
		speedtest.ErrUnsupportedConnections: "unsupported_connections",
		speedtest.ErrServerLimitExceeded:    "server_limit_exceeded",
		speedtest.ErrAllConnectionsFailed:   "all_connections_failed",
		speedtest.ErrByteCountMismatch:      "byte_count_mismatch",
		speedtest.ErrAllSamplesFailed:       "all_samples_failed",
		errors.New("boom"):                  "measurement_failed",
	}
	for err, want := range tests {
		if got := errorCodeFor(err); got != want {
			t.Errorf("errorCodeFor(%v) = %q, want %q", err, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// end to end over localhost
// ---------------------------------------------------------------------------

func TestRunEndToEndAgainstRealGoSpeedServer(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)
	sink := &recordingSink{}

	result, err := provider.Run(context.Background(), RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: allCapabilities(),
		Budget:       BudgetLimits{TotalBytes: 4 << 20},
		Events:       sink,
	})
	if err != nil {
		t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed; error %q", result.Status, result.ErrorMessage)
	}
	if result.ProviderID != IDGoSpeedHTTP {
		t.Errorf("providerId = %q", result.ProviderID)
	}
	if result.ServerID != "loopback-1" {
		t.Errorf("serverId = %q", result.ServerID)
	}
	if result.Method.Protocol != ProtocolGoSpeedHTTP {
		t.Errorf("protocol = %q, want %q", result.Method.Protocol, ProtocolGoSpeedHTTP)
	}

	if result.Download == nil || result.Download.Mbps == nil {
		t.Fatal("download rate is missing")
	}
	if result.Upload == nil || result.Upload.Mbps == nil {
		t.Fatal("upload rate is missing")
	}
	if result.Latency == nil || result.Latency.ValueMs == nil {
		t.Fatal("latency is missing")
	}
	if result.Latency.Type != MetricHTTPRTT {
		t.Errorf("latency type = %q, want %q: an HTTP RTT is not an ICMP ping", result.Latency.Type, MetricHTTPRTT)
	}
	if result.Jitter == nil || result.Jitter.ValueMs == nil {
		t.Fatal("jitter is missing although the engine collected several latency samples")
	}
	if result.Jitter.Type != JitterConsecutiveDiff {
		t.Errorf("jitter type = %q, want %q", result.Jitter.Type, JitterConsecutiveDiff)
	}

	// The engine's stop reason must survive the mapping untouched.
	for name, rate := range map[string]*Rate{"download": result.Download, "upload": result.Upload} {
		if rate.StopReason != string(speedtest.StopReasonRequestedBytes) {
			t.Errorf("%s stopReason = %q, want %q", name, rate.StopReason, speedtest.StopReasonRequestedBytes)
		}
		if rate.Mbps != nil && *rate.Mbps <= 0 {
			t.Errorf("%s mbps = %v, must be a real positive rate", name, *rate.Mbps)
		}
		if rate.TransferredBytes <= 0 {
			t.Errorf("%s transferredBytes = %d, want a real byte count", name, rate.TransferredBytes)
		}
	}

	// A completed upload is the one direction with an independent server side
	// byte count, so it earns the verified trust level.
	if result.Quality.Trust != "verified" {
		t.Errorf("trust = %q, want verified for a confirmed upload", result.Quality.Trust)
	}
	if result.Quality.Completeness != Complete {
		t.Errorf("completeness = %q, want complete", result.Quality.Completeness)
	}

	// The progress stream must reach the sink without the adapter polling.
	phases := sink.phases()
	for _, want := range []string{"latency", "download", "upload"} {
		if !containsString(phases, want) {
			t.Errorf("progress phases %v are missing %q", phases, want)
		}
	}
}

// TestRunRespectsTheByteBudget is the budget integration test: the engine's
// shared cap is honoured, and stopping at the cap stays a completed run.
func TestRunRespectsTheByteBudget(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	const budget = 1 << 20
	result, err := provider.Run(context.Background(), RunRequest{
		Node: nodeFor(testServer.URL),
		// A download only set: the latency phase must not run at all.
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true}},
		Directions:   []Direction{DirectionDownload},
		Budget:       BudgetLimits{DownloadBytes: budget},
	})
	if err != nil {
		t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if result.Download == nil {
		t.Fatal("download result is missing")
	}
	if result.Download.TransferredBytes > budget {
		t.Errorf("transferred %d bytes, budget was %d", result.Download.TransferredBytes, budget)
	}
	if result.Download.StopReason != string(speedtest.StopReasonRequestedBytes) {
		t.Errorf("stopReason = %q, want requested_bytes_reached", result.Download.StopReason)
	}
	if result.Method.Budget.DownloadBytes != budget {
		t.Errorf("recorded budget = %+v, want the applied cap", result.Method.Budget)
	}
	// The latency phase was not requested, so it must stay null.
	if result.Latency != nil || result.Jitter != nil {
		t.Error("latency and jitter must stay null when CapLatency was not negotiated")
	}
}

func TestRunHonoursCancellation(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	result, err := provider.Run(ctx, RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: allCapabilities(),
		Directions:   []Direction{DirectionDownload},
		Duration:     10 * time.Second,
	})
	if err == nil {
		t.Fatal("a cancelled run must return an error")
	}
	if result.Status != StatusCancelled {
		t.Fatalf("status = %q, want cancelled (error %v)", result.Status, err)
	}
	if result.ErrorCode != "cancelled" {
		t.Errorf("errorCode = %q", result.ErrorCode)
	}
	if result.Quality.Completeness != Incomplete {
		t.Errorf("completeness = %q, want incomplete for a cancelled run", result.Quality.Completeness)
	}
}

func TestRunHonoursTimeout(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	result, err := provider.Run(ctx, RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: allCapabilities(),
		Directions:   []Direction{DirectionDownload},
		Duration:     30 * time.Second,
	})
	if err == nil {
		t.Fatal("a timed out run must return an error")
	}
	if result.Status != StatusTimeout && result.Status != StatusCancelled {
		t.Fatalf("status = %q, want timeout or cancelled", result.Status)
	}
	if result.Status == StatusTimeout && result.ErrorCode != "timeout" {
		t.Errorf("errorCode = %q", result.ErrorCode)
	}
}

// TestRunFailsOnServerTruncation proves the provider keeps reporting a real
// early stream close as a failure, and that the B1 window semantics are not
// weakened by the adapter.
func TestRunFailsOnServerTruncation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": 1, "server_version": "0.3.0",
			"capabilities": []string{"download"}, "limits": map[string]any{},
		})
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, _ *http.Request) {
		// Close after a short burst, far before the client window ends.
		_, _ = w.Write(make([]byte, 32<<10))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(20 * time.Millisecond)
	})
	testServer := httptest.NewServer(mux)
	t.Cleanup(testServer.Close)

	provider := newTestProvider(t)
	result, err := provider.Run(context.Background(), RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true}},
		Directions:   []Direction{DirectionDownload},
		Duration:     3 * time.Second,
	})
	if err == nil {
		t.Fatal("a server that closes the stream early must fail the run")
	}
	if result.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if result.Download != nil && result.Download.Mbps != nil {
		t.Error("a truncated transfer must not publish a rate")
	}
}

// TestRunKeepsServerLimitEnforcement proves the adapter did not bypass the
// engine's refusal to exceed an advertised server limit.
func TestRunKeepsServerLimitEnforcement(t *testing.T) {
	testServer := stubServer(t, stubOptions{
		capabilities: []string{"download"},
		limits:       map[string]any{"max_download_bytes": 4096},
	})
	provider := newTestProvider(t)

	result, err := provider.Run(context.Background(), RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true}},
		Directions:   []Direction{DirectionDownload},
		Budget:       BudgetLimits{DownloadBytes: 1 << 20},
	})
	if err == nil {
		t.Fatal("a run beyond the advertised server limit must be refused")
	}
	if result.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if result.ErrorCode != "server_limit_exceeded" {
		t.Errorf("errorCode = %q, want server_limit_exceeded", result.ErrorCode)
	}
}

func TestRunWithoutEventSinkStillMeasures(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	result, err := provider.Run(context.Background(), RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: allCapabilities(),
		Directions:   []Direction{DirectionDownload},
		Budget:       BudgetLimits{DownloadBytes: 1 << 20},
		Events:       nil,
	})
	if err != nil {
		t.Fatalf("a nil event sink must not fail the run: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Errorf("status = %q, want completed", result.Status)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
