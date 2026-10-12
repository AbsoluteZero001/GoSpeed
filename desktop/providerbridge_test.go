package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// This file covers the B4 Desktop integration: the registry backed call path,
// the compatibility bridge and the comparison against the pre-B4 direct engine
// path. Every measurement runs against a local GoSpeed server.

// ---------------------------------------------------------------------------
// registry wiring
// ---------------------------------------------------------------------------

// TestDefaultRegistryExposesOnlyTheAuthorisedProvider pins the provider set.
// A build must not gain an M-Lab or Cloudflare backend silently.
func TestDefaultRegistryExposesOnlyTheAuthorisedProvider(t *testing.T) {
	registry, err := newDefaultRegistry()
	if err != nil {
		t.Fatalf("newDefaultRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	ids := registry.IDs()
	if len(ids) != 1 || ids[0] != defaultProviderID {
		t.Fatalf("registered providers = %v, want exactly [%s]", ids, defaultProviderID)
	}
	for _, forbidden := range []provider.ID{provider.IDMLabNDT7, provider.IDCloudflare} {
		if registry.Has(forbidden) {
			t.Errorf("provider %q must not be registered in this build", forbidden)
		}
	}
	runner, err := registry.Lookup(defaultProviderID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if runner.ID() != provider.IDGoSpeedHTTP {
		t.Fatalf("runner id = %q", runner.ID())
	}
}

// TestRunnerResolvesTheRunnerThroughTheRegistry proves the runner asks the
// registry for a measurement backend instead of hardcoding one.
func TestRunnerResolvesTheRunnerThroughTheRegistry(t *testing.T) {
	rec := newRecorder()
	runner := NewRunner(rec.emit)
	t.Cleanup(func() { _ = runner.CloseProviders() })

	// An empty registry has no gospeed-http runner, so a run must fail with a
	// clear wiring error rather than silently measuring something else.
	empty := provider.NewRegistry()
	runner.registryFn = func() (*provider.Registry, error) { return empty, nil }

	_, err := runner.providerRunner()
	if !errors.Is(err, errProviderUnavailable) {
		t.Fatalf("error = %v, want the provider to be reported as unavailable", err)
	}
	// The registry's own cause must stay matchable so a caller can tell a
	// wiring problem from a measurement failure.
	if !errors.Is(err, provider.ErrProviderNotRegistered) {
		t.Fatalf("error = %v, want the registry cause to be preserved", err)
	}
}

// TestRunnerWithoutAnyProviderReportsNoBackend checks the user facing failure
// when a build has no measurement provider at all.
func TestRunnerWithoutAnyProviderReportsNoBackend(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))
	runner.registryFn = func() (*provider.Registry, error) { return provider.NewRegistry(), nil }

	if _, err := runner.Start(StartOptions{
		SelectionMode: SelectionModeManual,
		NodeID:        "test",
		DurationMs:    1000,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 30*time.Second)
	if finished.Status == StateCompleted {
		t.Fatal("a run completed without a registered provider")
	}
	if !strings.Contains(finished.Error, "provider") {
		t.Fatalf("error = %q, want it to name the missing provider", finished.Error)
	}
	runner.Wait()
}

// ---------------------------------------------------------------------------
// full path through the registry
// ---------------------------------------------------------------------------

// TestRegistryBackedRunMatchesTheLegacyEnginePath is the core compatibility
// test: the pre-B4 direct engine call and the B4 registry plus provider call
// must produce equivalent evidence for the same request against the same
// server.
//
// It compares structure and invariants rather than two live throughput numbers,
// because two independent measurements of a loopback link are never bit equal
// and their difference would say nothing about the bridge.
func TestRegistryBackedRunMatchesTheLegacyEnginePath(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	options := StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      4,
		DurationMs:       1000,
		SampleIntervalMs: 100,
	}

	// --- old path: the engine the v0.4.0 client called directly ---
	manager := newManager(t, httpServer.URL)
	node, err := manager.Get("test")
	if err != nil {
		t.Fatalf("manager.Get: %v", err)
	}
	target := nodeTarget(node)
	legacy, legacyErr := runLegacyMeasurement(context.Background(), target, options, nil)

	// --- new path: runner -> registry -> provider ---
	rec := newRecorder()
	runner := newTestRunner(t, rec, manager)
	if _, err := runner.Start(options); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 60*time.Second)
	runner.Wait()

	if legacyErr != nil {
		t.Fatalf("legacy path failed: %v", legacyErr)
	}
	if finished.Status != StateCompleted {
		t.Fatalf("provider path status = %q, error = %q", finished.Status, finished.Error)
	}
	bridged := finished.Result
	if bridged == nil {
		t.Fatal("the provider path published no result")
	}

	// The detail panel reads these fields, so they must be present and real.
	if bridged.Download == nil || bridged.Download.Bytes <= 0 {
		t.Fatalf("provider path has no download evidence: %+v", bridged.Download)
	}
	if bridged.Upload == nil || bridged.Upload.Bytes <= 0 {
		t.Fatalf("provider path has no upload evidence: %+v", bridged.Upload)
	}
	if bridged.Latency == nil || bridged.Latency.SuccessfulSamples == 0 {
		t.Fatalf("provider path has no latency evidence: %+v", bridged.Latency)
	}
	if bridged.Target.ServerID != legacy.Target.ServerID {
		t.Errorf("server id = %q, legacy = %q", bridged.Target.ServerID, legacy.Target.ServerID)
	}
	if bridged.Download.Connections != legacy.Download.Connections {
		t.Errorf("connections = %d, legacy = %d", bridged.Download.Connections, legacy.Download.Connections)
	}
	if bridged.Download.MeasurementWindow != legacy.Download.MeasurementWindow {
		t.Errorf("window = %q, legacy = %q", bridged.Download.MeasurementWindow, legacy.Download.MeasurementWindow)
	}
	if bridged.Settings.DurationNs != legacy.Settings.DurationNs {
		t.Errorf("duration = %s, legacy = %s", bridged.Settings.DurationNs, legacy.Settings.DurationNs)
	}
	if bridged.Settings.Warmup != legacy.Settings.Warmup {
		t.Errorf("warmup = %t, legacy = %t: the bridge changed the request", bridged.Settings.Warmup, legacy.Settings.Warmup)
	}
	if !bridged.Settings.Warmup {
		t.Error("the GUI warmup request was lost on the way to the engine")
	}
	if bridged.Settings.SampleInterval != legacy.Settings.SampleInterval {
		t.Errorf("sample interval = %s, legacy = %s", bridged.Settings.SampleInterval, legacy.Settings.SampleInterval)
	}
	if bridged.Settings.SampleInterval != 100*time.Millisecond {
		t.Errorf("the configured sample interval was not honoured: %s", bridged.Settings.SampleInterval)
	}
	// The unified rate must agree with the native payload it was mapped from.
	if got, want := finishedProviderRate(t, finished), bridged.Download.Mbps; got != want {
		t.Errorf("unified mbps %v disagrees with the native payload %v", got, want)
	}
}

// TestConnectionsConfigurationsReachTheEngine runs the GUI path once per
// supported connection count and checks the engine really used it.
func TestConnectionsConfigurationsReachTheEngine(t *testing.T) {
	for _, connections := range []int{1, 4, 8, 16} {
		t.Run(connectionName(connections), func(t *testing.T) {
			httpServer := newSpeedtestServer(t)
			rec := newRecorder()
			runner := newTestRunner(t, rec, newManager(t, httpServer.URL))

			if _, err := runner.Start(StartOptions{
				SelectionMode:    SelectionModeManual,
				NodeID:           "test",
				Connections:      connections,
				DurationMs:       1000,
				SampleIntervalMs: 100,
			}); err != nil {
				t.Fatalf("Start: %v", err)
			}
			finished := waitFinished(t, rec, 60*time.Second)
			runner.Wait()

			if finished.Status != StateCompleted {
				t.Fatalf("status = %q, error = %q", finished.Status, finished.Error)
			}
			if finished.Result == nil || finished.Result.Download == nil {
				t.Fatal("no download result")
			}
			if got := finished.Result.Download.Connections; got != connections {
				t.Fatalf("engine used %d connections, want %d", got, connections)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// bridge behaviour
// ---------------------------------------------------------------------------

// TestBridgePreservesTheFinishedEventShape pins the compatibility contract: the
// finished event keeps its fields, its JSON names and its notice.
func TestBridgePreservesTheFinishedEventShape(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      2,
		DurationMs:       1000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 60*time.Second)
	runner.Wait()

	if finished.Notice == nil {
		t.Fatal("the finished event lost the target notice")
	}
	if finished.TestID == "" {
		t.Error("the finished event lost the test id")
	}
	if finished.DurationMs <= 0 {
		t.Error("the finished event lost the wall time")
	}
	// The legacy result must still serialise under the same JSON names.
	encoded, err := json.Marshal(finished)
	if err != nil {
		t.Fatalf("marshal finished event: %v", err)
	}
	for _, field := range []string{
		`"status"`, `"testId"`, `"cancelled"`, `"durationMs"`, `"result"`,
		`"connection_reports"`, `"statistics"`, `"measurement_window"`, `"stop_reason"`,
		`"samples_ns"`, `"settings"`, `"target"`,
	} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("finished event JSON lost %s", field)
		}
	}
}

// TestBridgeWithoutNativeDetailKeepsTheUnifiedVerdict proves the bridge
// degrades honestly: a provider without native detail still reports its status
// and error, and simply has no legacy payload.
func TestBridgeWithoutNativeDetailKeepsTheUnifiedVerdict(t *testing.T) {
	final := provider.FinalResult{
		ProviderID:   provider.IDGoSpeedHTTP,
		ServerID:     "node-1",
		Status:       provider.StatusFailed,
		ErrorCode:    "server_limit_exceeded",
		ErrorMessage: "request exceeds the server limit",
	}
	event := FinishedEvent{}
	applyFinished(&event, final, errors.New("request exceeds the server limit"))

	if event.Result != nil {
		t.Error("no native detail must mean no legacy payload, never a reconstructed one")
	}
	if event.Status != StateFailed {
		t.Errorf("status = %q, want failed", event.Status)
	}
	if event.Error == "" {
		t.Error("the failure message was dropped")
	}
	if event.Cancelled {
		t.Error("a failure must not be reported as a cancellation")
	}
}

// TestBridgeKeepsCancellationDistinct proves a cancelled run stays cancelled and
// does not masquerade as a completed or failed measurement.
func TestBridgeKeepsCancellationDistinct(t *testing.T) {
	event := FinishedEvent{}
	applyFinished(&event, provider.FinalResult{Status: provider.StatusCancelled}, context.Canceled)
	if event.Status != StateCancelled || !event.Cancelled {
		t.Fatalf("status = %q cancelled = %t", event.Status, event.Cancelled)
	}
	if event.Result != nil {
		t.Error("a cancelled run has no completed legacy result")
	}
}

// TestBridgeRefusesToReportSuccessWithAnError guards the one direction that
// could hide a failure: a completed verdict combined with a non-nil error must
// not reach the user as a successful run.
func TestBridgeRefusesToReportSuccessWithAnError(t *testing.T) {
	event := FinishedEvent{}
	applyFinished(&event, provider.FinalResult{Status: provider.StatusCompleted}, errors.New("late failure"))
	if event.Status == StateCompleted {
		t.Fatal("a run with an error was reported as completed")
	}
	if event.Error == "" {
		t.Error("the error was swallowed")
	}
}

// TestSessionStopsReceivingEventsAfterItEnds is the stale event guard: once a
// session is no longer the current one, its progress must be dropped instead of
// repainting a finished chart.
func TestSessionStopsReceivingEventsAfterItEnds(t *testing.T) {
	rec := newRecorder()
	runner := NewRunner(rec.emit)
	t.Cleanup(func() { _ = runner.CloseProviders() })

	finishedSession, _, err := runner.begin("test")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	sink := &progressSink{runner: runner, session: finishedSession}

	// While the session is current the sink must forward.
	sink.OnProgress(provider.ProgressEvent{
		Phase: "download", State: "progress", RunState: StateDownloadTesting, Bytes: 10,
	})
	runner.mu.Lock()
	open := len(rec.events[EventProgress])
	runner.mu.Unlock()
	if open == 0 {
		t.Fatal("a current session received no progress")
	}

	// After the session is released the same sink must stay silent.
	runner.finish(finishedSession, StateCompleted, "")
	sink.OnProgress(provider.ProgressEvent{
		Phase: "download", State: "progress", RunState: StateDownloadTesting, Bytes: 999999,
	})
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if got := len(rec.events[EventProgress]); got != open {
		t.Fatalf("a finished session received %d late event(s)", got-open)
	}
}

// TestProgressSinkNeverBlocksTheMeasurement documents that the sink returns
// immediately. A sink that blocked would stall the engine's sampler goroutine.
func TestProgressSinkNeverBlocksTheMeasurement(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))

	start := time.Now()
	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      4,
		DurationMs:       1000,
		SampleIntervalMs: 50,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 60*time.Second)
	elapsed := time.Since(start)
	runner.Wait()

	if finished.Status != StateCompleted {
		t.Fatalf("status = %q, error = %q", finished.Status, finished.Error)
	}
	// A one second window plus teardown must not blow up: a blocking sink would
	// show up here as a run far longer than the requested window.
	if elapsed > 20*time.Second {
		t.Fatalf("the run took %s, which suggests the progress sink blocked the engine", elapsed)
	}
}

// TestConsecutiveRunsReuseTheSameProvider proves repeated measurements work and
// that the provider instance is reused rather than rebuilt per click.
func TestConsecutiveRunsReuseTheSameProvider(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))
	t.Cleanup(func() { _ = runner.CloseProviders() })

	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := runner.Start(StartOptions{
			SelectionMode:    SelectionModeManual,
			NodeID:           "test",
			Connections:      2,
			DurationMs:       1000,
			SampleIntervalMs: 100,
		}); err != nil {
			t.Fatalf("attempt %d Start: %v", attempt, err)
		}
		finished := waitFinished(t, rec, 60*time.Second)
		if finished.Status != StateCompleted {
			t.Fatalf("attempt %d status = %q, error = %q", attempt, finished.Status, finished.Error)
		}
		if finished.Result == nil || finished.Result.Download == nil {
			t.Fatalf("attempt %d produced no download evidence", attempt)
		}
		runner.Wait()

		first, err := runner.registry()
		if err != nil {
			t.Fatalf("registry: %v", err)
		}
		runner.mu.Lock()
		cached := runner.providers
		runner.mu.Unlock()
		if cached != first {
			t.Fatalf("attempt %d rebuilt the registry instead of reusing it", attempt)
		}
	}
}

// TestAutoSelectionStillWorksThroughTheRegistry proves the automatic node
// selection path is untouched by the provider integration.
func TestAutoSelectionStillWorksThroughTheRegistry(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeAuto,
		Connections:      1,
		DurationMs:       1000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 60*time.Second)
	runner.Wait()

	if finished.Status != StateCompleted {
		t.Fatalf("status = %q, error = %q", finished.Status, finished.Error)
	}
	targets := rec.targets(t)
	if len(targets) != 1 {
		t.Fatalf("target events = %d, want 1", len(targets))
	}
	if targets[0].SelectionMethod == "" {
		t.Error("automatic selection lost its reason")
	}
}

// TestLiveCurvesKeepUpdatingForBothDirections checks that download and upload
// progress both reach the GUI while the run is in flight.
func TestLiveCurvesKeepUpdatingForBothDirections(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      4,
		DurationMs:       1500,
		SampleIntervalMs: 50,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	download := waitProgress(t, rec, func(event ProgressEvent) bool {
		return event.Phase == string(speedtest.PhaseDownload) && event.Stage == string(speedtest.StageProgress) && event.Bytes > 0
	}, 30*time.Second)
	if download.State != StateDownloadTesting {
		t.Errorf("download state = %q", download.State)
	}
	if download.InstantMbps <= 0 {
		t.Errorf("instant rate = %v, want a real measured value", download.InstantMbps)
	}
	upload := waitProgress(t, rec, func(event ProgressEvent) bool {
		return event.Phase == string(speedtest.PhaseUpload) && event.Stage == string(speedtest.StageProgress)
	}, 60*time.Second)
	if upload.State != StateUploadTesting {
		t.Errorf("upload state = %q", upload.State)
	}
	waitFinished(t, rec, 60*time.Second)
	runner.Wait()
}

// TestCloseProvidersReleasesTheRegistryOnce proves the shutdown path releases
// the provider and is safe to call repeatedly.
func TestCloseProvidersReleasesTheRegistryOnce(t *testing.T) {
	rec := newRecorder()
	runner := NewRunner(rec.emit)

	if err := runner.CloseProviders(); err != nil {
		t.Fatalf("closing before any run: %v", err)
	}
	if _, err := runner.registry(); err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := runner.CloseProviders(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := runner.CloseProviders(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// finishedProviderRate extracts the unified download rate from a finished event
// by re-deriving it from the native payload, which is how a consumer that only
// has the legacy event can cross-check the two.
func finishedProviderRate(t *testing.T, finished FinishedEvent) float64 {
	t.Helper()
	if finished.Result == nil || finished.Result.Download == nil {
		t.Fatal("no download evidence to compare")
	}
	return finished.Result.Download.Mbps
}

func connectionName(value int) string {
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
