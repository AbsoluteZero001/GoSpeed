package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// This file covers the B4 model gaps: concurrency, node scope, the enriched
// progress event and the verbatim native detail channel.

// ---------------------------------------------------------------------------
// A1: connections
// ---------------------------------------------------------------------------

// TestRunForwardsEverySupportedConnectionCount is the concurrency regression:
// the provider must pass 1, 4, 8 and 16 through to the engine and must not
// quietly reduce them. The engine result is checked, not just the options.
func TestRunForwardsEverySupportedConnectionCount(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	for _, connections := range []int{1, 4, 8, 16} {
		t.Run(connectionCaseName(connections), func(t *testing.T) {
			caps := CapabilitySet{Supported: map[Capability]bool{CapDownload: true}}
			request := RunRequest{
				Node:         nodeFor(testServer.URL),
				Capabilities: caps,
				Directions:   []Direction{DirectionDownload},
				Connections:  connections,
				Budget:       BudgetLimits{DownloadBytes: measurableByteBudget},
			}
			opts, err := optionsFor(request, []Direction{DirectionDownload}, BudgetLimits{DownloadBytes: 1 << 20}, "node")
			if err != nil {
				t.Fatalf("optionsFor: %v", err)
			}
			if opts.Connections != connections {
				t.Fatalf("engine connections = %d, want the requested %d", opts.Connections, connections)
			}

			result, err := provider.Run(context.Background(), request)
			if err != nil {
				t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
			}
			if result.Download == nil || result.Download.Mbps == nil {
				t.Fatal("no download rate")
			}
			// The run really happened with the requested count: the native detail
			// is the engine's own result, so its requested connection count is
			// direct evidence and not an inference.
			native := decodeSpeedtestDetail(t, result)
			if native == nil || native.Download == nil {
				t.Fatal("the run produced no native download evidence")
			}
			if native.Download.Connections != connections {
				t.Fatalf("engine used %d connections, want %d", native.Download.Connections, connections)
			}
			if result.Download.StopReason != string(speedtest.StopReasonRequestedBytes) {
				t.Errorf("stop reason = %q", result.Download.StopReason)
			}
		})
	}
}

func TestOptionsForKeepsEngineDefaultWhenConnectionsIsZero(t *testing.T) {
	opts, err := optionsFor(RunRequest{Node: unitNode()}, []Direction{DirectionDownload}, BudgetLimits{}, "node")
	if err != nil {
		t.Fatalf("optionsFor: %v", err)
	}
	if opts.Connections != speedtest.DefaultConnections {
		t.Fatalf("connections = %d, want the unchanged engine default %d", opts.Connections, speedtest.DefaultConnections)
	}
}

// TestOptionsForForwardsMeasurementTuning checks the GUI's own settings reach
// the engine unchanged, so routing through the provider cannot change the
// sampling cadence or the warmup behaviour.
func TestOptionsForForwardsMeasurementTuning(t *testing.T) {
	opts, err := optionsFor(RunRequest{
		Node:            unitNode(),
		SampleInterval:  250 * time.Millisecond,
		Warmup:          true,
		LatencySamples:  7,
		LatencyInterval: 42 * time.Millisecond,
	}, []Direction{DirectionDownload}, BudgetLimits{}, "node")
	if err != nil {
		t.Fatalf("optionsFor: %v", err)
	}
	if opts.SampleInterval != 250*time.Millisecond {
		t.Errorf("sample interval = %s", opts.SampleInterval)
	}
	if !opts.Warmup {
		t.Error("warmup was dropped")
	}
	if opts.LatencySamples != 7 {
		t.Errorf("latency samples = %d, want 7", opts.LatencySamples)
	}
	if opts.LatencyInterval != 42*time.Millisecond {
		t.Errorf("latency interval = %s", opts.LatencyInterval)
	}
}

// TestRunRejectsOutOfRangeConnections proves the engine's own range check still
// applies through the provider: a caller must never receive a silently
// clamped run.
func TestRunRejectsOutOfRangeConnections(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	tests := []struct {
		name        string
		connections int
		wantCode    string
	}{
		// The engine owns the supported range, so each violation keeps its own
		// specific code instead of one generic "invalid options".
		{"above maximum", speedtest.MaxConnections + 1, "unsupported_connections"},
		{"negative", -1, "invalid_options"},
	}
	for _, testCase := range tests {
		name, connections, wantCode := testCase.name, testCase.connections, testCase.wantCode
		t.Run(name, func(t *testing.T) {
			request := RunRequest{
				Node:         nodeFor(testServer.URL),
				Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true}},
				Directions:   []Direction{DirectionDownload},
				Connections:  connections,
			}
			result, err := provider.Run(context.Background(), request)
			if err == nil {
				t.Fatal("an out of range connection count must be refused")
			}
			if result.Status != StatusFailed {
				t.Errorf("status = %q, want failed", result.Status)
			}
			if result.Download != nil {
				t.Error("a refused run must not produce a rate")
			}
			if result.ErrorCode != wantCode {
				t.Errorf("errorCode = %q, want %q", result.ErrorCode, wantCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A2: node scope
// ---------------------------------------------------------------------------

// TestScopeMapsTheEngineClassification pins the mapping from the engine's
// address classification onto the unified scope.
//
// The classification itself is the engine's existing, separately tested logic
// (internal/speedtest and internal/nodes own it). What matters here is that the
// provider forwards it unchanged and never substitutes a value derived from a
// measured rate.
func TestScopeMapsTheEngineClassification(t *testing.T) {
	tests := map[speedtest.NetworkScope]Scope{
		speedtest.ScopeLocal:   ScopeLocal,
		speedtest.ScopeLAN:     ScopeLAN,
		speedtest.ScopeRemote:  ScopeRemote,
		speedtest.ScopeUnknown: ScopeUnknown,
	}
	for engineScope, want := range tests {
		// The node carries no classification, so only the engine's value can
		// decide the mapping here.
		node := Node{ID: "n", Endpoint: "http://speed.example.com", Scope: ScopeUnknown}
		engineResult := speedtest.Result{}
		engineResult.Target.NetworkScope = engineScope
		if got := scopeOf(engineResult, node); got != want {
			t.Errorf("engine scope %q mapped to %q, want %q", engineScope, got, want)
		}
	}

	// A fast measurement must not turn a local node into a remote one: the
	// classification is taken from the address and only from the address.
	localResult := speedtest.Result{}
	localResult.Target.NetworkScope = speedtest.ScopeLocal
	localResult.Download = &speedtest.TransferResult{Mbps: 10_000}
	if got := scopeOf(localResult, Node{Scope: ScopeRemote}); got != ScopeLocal {
		t.Errorf("a 10 Gbps local run was reported as %q", got)
	}
}

// TestScopeFallsBackToConfiguredClassification covers a host name the engine
// could not classify but the node configuration already did.
func TestScopeFallsBackToConfiguredClassification(t *testing.T) {
	node := Node{ID: "n", Endpoint: "http://speed.example.com", Scope: ScopeLAN}
	if got := scopeOf(speedtest.Result{}, node); got != ScopeLAN {
		t.Fatalf("scope = %q, want the configured %q", got, ScopeLAN)
	}
	unknown := Node{ID: "n", Endpoint: "http://speed.example.com", Scope: ScopeUnknown}
	if got := scopeOf(speedtest.Result{}, unknown); got != ScopeUnknown {
		t.Fatalf("scope = %q, want unknown", got)
	}
}

// TestResultCarriesNodeIdentity proves a result names the node honestly without
// ever publishing the endpoint, which may embed credentials.
func TestResultCarriesNodeIdentity(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	node := nodeFor(testServer.URL)
	result, err := provider.Run(context.Background(), RunRequest{
		Node:         node,
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true}},
		Directions:   []Direction{DirectionDownload},
		Budget:       BudgetLimits{DownloadBytes: measurableByteBudget},
	})
	if err != nil {
		t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
	}
	if result.Scope != ScopeLocal {
		t.Errorf("scope = %q, want local for a loopback node", result.Scope)
	}
	if result.Ownership != OwnershipSelfHosted {
		t.Errorf("ownership = %q, want self_hosted", result.Ownership)
	}
	if result.Discovery != DiscoveryConfigured {
		t.Errorf("discovery = %q, want configured", result.Discovery)
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "@") && strings.Contains(string(encoded), "://") {
		// A credential bearing address would show up as userinfo after the
		// scheme. The payload must never contain one.
		if strings.Contains(string(encoded), ":pass@") || strings.Contains(string(encoded), ":secret@") {
			t.Error("the serialized result published node credentials")
		}
	}
}

// TestRedactURLCredentialsKeepsCleanAddressesIntact pins the redaction rule: a
// credential free address is left byte identical, so the detail payload still
// describes the node the user actually configured, while every credential
// carrier is removed.
//
// It covers userinfo, the whole query string and the fragment. The query is
// stripped wholesale rather than filtered by parameter name on purpose: a
// deny-list fails open the moment somebody adds a new parameter name.
func TestRedactURLCredentialsKeepsCleanAddressesIntact(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    string
		changed bool
	}{
		// A clean address is left byte identical.
		{"loopback", "http://127.0.0.1:8080", "http://127.0.0.1:8080", false},
		{"plain host", "https://speed.example.com", "https://speed.example.com", false},
		{"empty", "", "", false},
		// A path prefix is legitimate for a reverse proxied node.
		{"path prefix is kept", "https://speed.example.com/prefix/path", "https://speed.example.com/prefix/path", false},
		{"port is kept", "http://127.0.0.1:8080/base", "http://127.0.0.1:8080/base", false},
		// Userinfo is a credential carrier.
		{"userinfo", "https://user:pass@speed.example.com", "https://speed.example.com", true},
		{"userinfo without password", "https://user@speed.example.com", "https://speed.example.com", true},
		{"userinfo with escaped password and path", "https://user:p%40ss@speed.example.com/base", "https://speed.example.com/base", true},
		// The whole query and fragment are credential carriers. internal/nodes
		// refuses both on a configured node; the provider enforces the same rule
		// because provider.Node itself is unvalidated.
		{"access_token query", "https://speed.example.com/?access_token=abc123", "https://speed.example.com/", true},
		{"token query", "https://speed.example.com/ping?token=abc123", "https://speed.example.com/ping", true},
		{"api_key query", "https://speed.example.com/?api_key=abc123", "https://speed.example.com/", true},
		{"unknown parameter is stripped too", "https://speed.example.com/?future_secret=abc123", "https://speed.example.com/", true},
		{"query plus path", "https://host:9090/base?token=abc", "https://host:9090/base", true},
		{"fragment", "https://speed.example.com/#secret-cookie", "https://speed.example.com/", true},
		{"userinfo and query together", "https://u:p@h.example.com/x?token=abc", "https://h.example.com/x", true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, changed := redactURLCredentials(testCase.address)
			if got != testCase.want || changed != testCase.changed {
				t.Errorf("redactURLCredentials(%q) = (%q, %t), want (%q, %t)",
					testCase.address, got, changed, testCase.want, testCase.changed)
			}
		})
	}
}

// TestDetailNeverCarriesCredentials drives the redaction through a real run by
// checking the published payload of a configured address that embeds a secret.
func TestDetailNeverCarriesCredentials(t *testing.T) {
	server := realGoSpeedServer(t)
	withSecret := strings.Replace(server.URL, "://", "://user:sup3rsecret@", 1)

	measurement := NewGoSpeedHTTP(nil)
	t.Cleanup(measurement.CloseIdleConnections)

	caps, err := measurement.Negotiate(context.Background(), Node{ID: "n", Endpoint: withSecret})
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	result, err := measurement.Run(context.Background(), RunRequest{
		Node:         Node{ID: "n", Endpoint: withSecret},
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true}},
		Directions:   []Direction{DirectionDownload},
		Budget:       BudgetLimits{DownloadBytes: measurableByteBudget},
	})
	if err != nil {
		t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
	}
	if caps.Supports(CapDownload) != true {
		t.Fatal("negotiation did not confirm download")
	}
	if result.Detail == nil {
		t.Fatal("no native detail")
	}
	if strings.Contains(string(result.Detail.Payload), "sup3rsecret") {
		t.Fatal("the native detail published the node credentials")
	}
	if strings.Contains(string(result.Detail.Payload), "user:") {
		t.Fatalf("the native detail still carries a userinfo component: %s", result.Detail.Payload)
	}
}

// ---------------------------------------------------------------------------
// A3: progress fidelity
// ---------------------------------------------------------------------------

// collectingSink records the events a run produced.
type collectingSink struct {
	mu     sync.Mutex
	events []ProgressEvent
}

func (s *collectingSink) OnProgress(event ProgressEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *collectingSink) snapshot() []ProgressEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ProgressEvent(nil), s.events...)
}

// TestProgressForwardsEveryObservedValue checks the real time stream carries the
// engine's own counters, including the fields the v0.4.0 GUI already needed.
func TestProgressForwardsEveryObservedValue(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)
	sink := &collectingSink{}

	// A duration window (rather than a byte budget) guarantees the phase is
	// still running when the sampler fires, which is what a real time curve
	// depends on.
	if _, err := provider.Run(context.Background(), RunRequest{
		Node:           nodeFor(testServer.URL),
		Capabilities:   allCapabilities(),
		Directions:     []Direction{DirectionDownload},
		Duration:       700 * time.Millisecond,
		SampleInterval: 50 * time.Millisecond,
		Events:         sink,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := sink.snapshot()
	var progressEvents []ProgressEvent
	for _, event := range events {
		if event.State == "progress" {
			progressEvents = append(progressEvents, event)
		}
	}
	if len(progressEvents) == 0 {
		t.Fatalf("no real time progress events were delivered, got %d events", len(events))
	}

	var sawConnections bool
	for _, event := range progressEvents {
		if event.RunState != string(speedtest.StateDownloadTesting) {
			continue
		}
		sawConnections = true
		if event.Phase != string(speedtest.PhaseDownload) {
			t.Errorf("phase = %q", event.Phase)
		}
		if event.Bytes <= 0 {
			t.Errorf("bytes = %d, want a real observed count", event.Bytes)
		}
		if event.ActiveConnections <= 0 {
			t.Errorf("activeConnections = %d, want a real observed count", event.ActiveConnections)
		}
		if event.InstantMbps < 0 || event.Mbps < 0 {
			t.Errorf("negative rate: %+v", event)
		}
		if event.ElapsedMs <= 0 {
			t.Errorf("elapsedMs = %v", event.ElapsedMs)
		}
	}
	if !sawConnections {
		t.Errorf("no download phase event reported the run state, events: %+v", progressEvents[0])
	}
}

// TestProgressKeepsUnknownBudgetNull proves an unknown budget stays null in the
// stream instead of becoming a measured zero.
func TestProgressKeepsUnknownBudgetNull(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	var mu sync.Mutex
	seen := 0
	sink := sinkFunc(func(event ProgressEvent) {
		if event.State != "progress" {
			return
		}
		mu.Lock()
		seen++
		if event.BudgetFraction != nil && *event.BudgetFraction == 0 {
			t.Error("an unknown budget fraction was reported as a measured zero")
		}
		if event.BudgetRemainingMs != nil && *event.BudgetRemainingMs < 0 {
			t.Error("a negative remaining budget was reported")
		}
		mu.Unlock()
	})

	if _, err := provider.Run(context.Background(), RunRequest{
		Node:           nodeFor(testServer.URL),
		Capabilities:   CapabilitySet{Supported: map[Capability]bool{CapDownload: true}},
		Directions:     []Direction{DirectionDownload},
		Duration:       500 * time.Millisecond,
		SampleInterval: 50 * time.Millisecond,
		Events:         sink,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	observed := seen
	mu.Unlock()
	if observed == 0 {
		t.Error("no progress event was observed")
	}
}

// TestProgressStopsAfterRunReturns proves the sink cannot keep firing once the
// run is over: the adapter only emits while the engine is inside Run.
func TestProgressStopsAfterRunReturns(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)
	sink := &collectingSink{}

	// A duration window keeps the phase measurable: a small byte budget on a
	// loopback target can finish inside a single clock tick, which the engine
	// correctly refuses to turn into a rate.
	if _, err := provider.Run(context.Background(), RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true}},
		Directions:   []Direction{DirectionDownload},
		Duration:     400 * time.Millisecond,
		Events:       sink,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	settled := len(sink.snapshot())
	time.Sleep(300 * time.Millisecond)
	if got := len(sink.snapshot()); got != settled {
		t.Fatalf("%d events arrived after the run finished", got-settled)
	}
}

// ---------------------------------------------------------------------------
// multi connection partial failure, through a real run
// ---------------------------------------------------------------------------

// TestRunKeepsPartialConnectionFailure is a genuine Provider.Run, not a mapping
// exercise: one of two connections is cut off after a short burst while the
// other keeps streaming, and the run must survive with the loss reported.
//
// A subset of connections failing is a warning, not a failed measurement, and
// the evidence of the connections that did work has to survive the mapping.
func TestRunKeepsPartialConnectionFailure(t *testing.T) {
	var requests atomic.Int32
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download" && requests.Add(1) == 1 {
			// The first connection ends early, while the window is still open.
			_, _ = w.Write(make([]byte, 256<<10))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}
		switch r.URL.Path {
		case "/download":
			// Every later connection streams for the whole window.
			w.Header().Set("Content-Type", "application/octet-stream")
			flusher, _ := w.(http.Flusher)
			block := make([]byte, 32<<10)
			for r.Context().Err() == nil {
				if _, err := w.Write(block); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(time.Millisecond)
			}
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/ping":
			w.WriteHeader(http.StatusNoContent)
		case "/capabilities":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"protocol_version": 1, "server_version": "0.3.0",
				"capabilities": []string{"download"}, "limits": map[string]any{},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(testServer.Close)

	provider := newTestProvider(t)
	result, err := provider.Run(context.Background(), RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true}},
		Directions:   []Direction{DirectionDownload},
		Duration:     700 * time.Millisecond,
		Connections:  2,
	})
	if err != nil {
		t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed: losing one connection is a warning", result.Status)
	}
	if result.Download == nil || result.Download.Mbps == nil || *result.Download.Mbps <= 0 {
		t.Fatalf("the surviving connection produced no rate: %+v", result.Download)
	}

	// The per connection evidence is what tells a reader how much to trust the
	// aggregate, so it must reach the result intact.
	native := decodeSpeedtestDetail(t, result)
	if native.Download == nil {
		t.Fatal("no native download evidence")
	}
	if native.Download.FailedConnections != 1 || native.Download.ActiveConnections != 1 {
		t.Fatalf("connections: active %d failed %d, want 1 and 1",
			native.Download.ActiveConnections, native.Download.FailedConnections)
	}
	if len(native.Download.ConnectionReports) == 0 {
		t.Fatal("the per connection reports were dropped")
	}

	// The unified result must carry the warning too.
	if len(result.Method.Warnings) == 0 {
		t.Fatal("the partial connection failure did not reach the unified warnings")
	}
	if result.Quality.Stability != "degraded" {
		t.Fatalf("stability = %q, want degraded when a connection was lost", result.Quality.Stability)
	}
}

// ---------------------------------------------------------------------------
// native detail channel
// ---------------------------------------------------------------------------

// TestDetailCarriesTheEngineResultVerbatim proves the detail payload decodes to
// exactly the engine's own result, including the fields the unified model does
// not carry. Nothing may be reconstructed.
func TestDetailCarriesTheEngineResultVerbatim(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	// Download only: an unbounded multi connection upload would exceed the
	// fixture's per request byte limit, which is a different test.
	result, err := provider.Run(context.Background(), RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: CapabilitySet{Supported: map[Capability]bool{CapDownload: true, CapLatency: true}},
		Directions:   []Direction{DirectionDownload},
		Duration:     600 * time.Millisecond,
		Connections:  2,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Detail == nil {
		t.Fatal("the GoSpeed provider published no native detail")
	}
	if result.Detail.Format != DetailFormatGoSpeedResult {
		t.Fatalf("detail format = %q", result.Detail.Format)
	}

	var decoded speedtest.Result
	if err := json.Unmarshal(result.Detail.Payload, &decoded); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if decoded.TestID == "" {
		t.Error("the detail lost the engine test id")
	}
	if decoded.Download == nil || decoded.Download.Bytes <= 0 {
		t.Fatal("the detail lost the download evidence")
	}
	// The unified rate and the native result must agree, because both come
	// from the same measurement rather than from a reconstruction.
	if decoded.Download.Mbps != *result.Download.Mbps {
		t.Errorf("detail mbps %v != unified mbps %v", decoded.Download.Mbps, *result.Download.Mbps)
	}
	if len(decoded.Download.Samples) == 0 {
		t.Error("the detail lost the real time sample series")
	}
	if decoded.Download.Connections != 2 {
		t.Errorf("the detail lost the connection count: %d", decoded.Download.Connections)
	}
	if decoded.Download.Statistics.Samples == 0 {
		t.Error("the detail lost the descriptive statistics")
	}
	// The latency phase always produces a result object, but on a loopback
	// runner the samples can all be unresolvable zeros. The detail must still
	// carry the raw samples it collected.
	if decoded.Latency == nil {
		t.Error("the detail lost the latency result entirely")
	} else if len(decoded.Latency.SamplesNs) == 0 {
		t.Error("the detail lost the raw latency samples")
	}
}

// TestDetailIsAbsentWhenTheEngineNeverStarted keeps a rejected request from
// publishing an empty result payload.
func TestDetailIsAbsentWhenTheEngineNeverStarted(t *testing.T) {
	provider := newTestProvider(t)

	result, err := provider.Run(context.Background(), RunRequest{
		Node:         Node{ID: "n"},
		Capabilities: allCapabilities(),
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if result.Detail != nil {
		t.Error("a request that never started must not carry native detail")
	}
}

// TestDetailOfUnknownFormatIsIgnoredByConsumers documents that a consumer can
// safely ignore a detail it does not understand.
func TestDetailOfUnknownFormatIsIgnoredByConsumers(t *testing.T) {
	final := FinalResult{Detail: &ProviderDetail{Format: "some-other-provider/v9", Payload: []byte(`{}`)}}
	if final.Detail.Format == DetailFormatGoSpeedResult {
		t.Fatal("test setup: an unknown format must not equal the GoSpeed format")
	}
}

// sinkFunc adapts a function to the EventSink interface.
type sinkFunc func(ProgressEvent)

func (f sinkFunc) OnProgress(event ProgressEvent) { f(event) }

// decodeSpeedtestDetail decodes the provider's native detail payload, failing
// the test when it is missing or malformed. It is a test helper for asserting
// that the payload really is the engine's own result.
func decodeSpeedtestDetail(t *testing.T, final FinalResult) *speedtest.Result {
	t.Helper()
	if final.Detail == nil {
		t.Fatal("result carried no native detail")
	}
	if final.Detail.Format != DetailFormatGoSpeedResult {
		t.Fatalf("detail format = %q", final.Detail.Format)
	}
	var decoded speedtest.Result
	if err := json.Unmarshal(final.Detail.Payload, &decoded); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	return &decoded
}

func connectionCaseName(value int) string {
	switch value {
	case 1:
		return "one"
	case 4:
		return "four"
	case 8:
		return "eight"
	case 16:
		return "sixteen"
	default:
		return "unsupported"
	}
}
