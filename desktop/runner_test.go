package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/server"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// recorder captures the events a Runner emits so tests can assert on the same
// data the frontend receives.
type recorder struct {
	mu       sync.Mutex
	events   map[string][]any
	finished chan FinishedEvent
	progress chan ProgressEvent
}

func newRecorder() *recorder {
	return &recorder{
		events:   make(map[string][]any),
		finished: make(chan FinishedEvent, 8),
		progress: make(chan ProgressEvent, 4096),
	}
}

func (r *recorder) emit(name string, payload any) {
	r.mu.Lock()
	r.events[name] = append(r.events[name], payload)
	r.mu.Unlock()
	switch typed := payload.(type) {
	case FinishedEvent:
		r.finished <- typed
	case ProgressEvent:
		select {
		case r.progress <- typed:
		default:
			// Tests only assert on a bounded prefix of the sample stream.
		}
	}
}

func (r *recorder) targets(t *testing.T) []TargetEvent {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	targets := make([]TargetEvent, 0, len(r.events[EventTarget]))
	for _, payload := range r.events[EventTarget] {
		target, ok := payload.(TargetEvent)
		if !ok {
			t.Fatalf("unexpected target payload %T", payload)
		}
		targets = append(targets, target)
	}
	return targets
}

func newSpeedtestServer(t *testing.T) *httptest.Server {
	t.Helper()
	testServer, err := server.New(server.Config{})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	httpServer := httptest.NewServer(testServer.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func newManager(t *testing.T, baseURL string) *nodes.Manager {
	t.Helper()
	manager, err := nodes.NewManager([]nodes.Node{{
		ID:           "test",
		Name:         "Test server",
		BaseURL:      baseURL,
		Protocol:     nodes.ProtocolHTTP,
		Enabled:      true,
		Local:        true,
		Capabilities: []nodes.Capability{nodes.CapabilityLatency, nodes.CapabilityDownload, nodes.CapabilityUpload},
	}})
	if err != nil {
		t.Fatalf("nodes.NewManager: %v", err)
	}
	return manager
}

func newTestRunner(t *testing.T, rec *recorder, manager *nodes.Manager) *Runner {
	t.Helper()
	runner := NewRunner(rec.emit)
	runner.loadNodesFn = func() (*nodes.Manager, string, error) {
		return manager, "test-nodes.json", nil
	}
	return runner
}

func waitFinished(t *testing.T, rec *recorder, timeout time.Duration) FinishedEvent {
	t.Helper()
	select {
	case finished := <-rec.finished:
		return finished
	case <-time.After(timeout):
		t.Fatal("timed out waiting for the finished event")
		return FinishedEvent{}
	}
}

func waitProgress(t *testing.T, rec *recorder, match func(ProgressEvent) bool, timeout time.Duration) ProgressEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case progress := <-rec.progress:
			if match(progress) {
				return progress
			}
		case <-deadline:
			t.Fatal("timed out waiting for a matching progress event")
			return ProgressEvent{}
		}
	}
}

func TestManualRunCompletesAgainstLocalServer(t *testing.T) {
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

	download := waitProgress(t, rec, func(progress ProgressEvent) bool {
		return progress.Phase == string(speedtest.PhaseDownload) && progress.Stage == string(speedtest.StageProgress)
	}, 30*time.Second)
	if download.State != StateDownloadTesting {
		t.Fatalf("download progress state = %q, want %q", download.State, StateDownloadTesting)
	}
	if download.Bytes <= 0 {
		t.Fatalf("download progress reported %d bytes", download.Bytes)
	}
	if download.ActiveConnections < 1 {
		t.Fatalf("download progress reported %d active connections", download.ActiveConnections)
	}
	if download.InstantMbps < 0 || download.Mbps < 0 {
		t.Fatalf("negative rate in progress event: %+v", download)
	}

	finished := waitFinished(t, rec, 60*time.Second)
	if finished.Status != StateCompleted {
		t.Fatalf("finished status = %q, error = %q", finished.Status, finished.Error)
	}
	if finished.Error != "" {
		t.Fatalf("completed run reported an error: %q", finished.Error)
	}
	result := finished.Result
	if result == nil {
		t.Fatal("completed run has no result")
	}
	if result.Status != speedtest.StatusCompleted {
		t.Fatalf("result status = %q", result.Status)
	}
	if result.Download == nil || result.Download.Bytes <= 0 {
		t.Fatalf("download result missing bytes: %+v", result.Download)
	}
	if result.Upload == nil {
		t.Fatal("upload result missing")
	}
	if result.Upload.ServerConfirmedBytes != result.Upload.Bytes {
		t.Fatalf("server confirmed %d of %d uploaded bytes",
			result.Upload.ServerConfirmedBytes, result.Upload.Bytes)
	}
	if result.Latency == nil || result.Latency.SuccessfulSamples == 0 {
		t.Fatalf("latency result missing samples: %+v", result.Latency)
	}

	targets := rec.targets(t)
	if len(targets) != 1 {
		t.Fatalf("target events = %d, want 1", len(targets))
	}
	if targets[0].SelectionMethod != string(nodes.SelectionManual) {
		t.Fatalf("selection method = %q", targets[0].SelectionMethod)
	}
	if targets[0].Target.ID != "test" || targets[0].Target.NetworkScope != nodes.ScopeLocal {
		t.Fatalf("unexpected target: %+v", targets[0].Target)
	}

	runner.Wait()
	if status := runner.Status(); status.Busy {
		t.Fatalf("runner still busy: %+v", status)
	}
}

func TestAutoSelectionRunUsesMeasuredProbeData(t *testing.T) {
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
	if finished.Status != StateCompleted {
		t.Fatalf("finished status = %q, error = %q", finished.Status, finished.Error)
	}
	targets := rec.targets(t)
	if len(targets) != 1 {
		t.Fatalf("target events = %d, want 1", len(targets))
	}
	target := targets[0]
	if target.SelectionMethod != string(nodes.SelectionAuto) {
		t.Fatalf("selection method = %q", target.SelectionMethod)
	}
	if target.SelectionReason == "" {
		t.Fatal("automatic selection must explain its decision")
	}
	if target.SelectionNote != nodes.SelectionNote {
		t.Fatalf("selection note = %q, want the latency-not-bandwidth note", target.SelectionNote)
	}
	if len(target.Candidates) != 1 || target.Candidates[0].NodeID != "test" {
		t.Fatalf("unexpected candidates: %+v", target.Candidates)
	}
	if target.Candidates[0].Rank != 1 || target.Candidates[0].Reason == "" {
		t.Fatalf("candidate is not explained: %+v", target.Candidates[0])
	}
}

func TestCancelStopsRunAndReleasesResources(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      1,
		DurationMs:       30000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitProgress(t, rec, func(progress ProgressEvent) bool {
		return progress.Phase == string(speedtest.PhaseDownload) && progress.Stage == string(speedtest.StageProgress)
	}, 30*time.Second)

	if err := runner.CancelTest(); err != nil {
		t.Fatalf("CancelTest: %v", err)
	}
	finished := waitFinished(t, rec, 15*time.Second)
	if !finished.Cancelled || finished.Status != StateCancelled {
		t.Fatalf("cancelled run reported status %q (cancelled=%v, error=%q)",
			finished.Status, finished.Cancelled, finished.Error)
	}
	if !strings.Contains(finished.Error, context.Canceled.Error()) {
		t.Fatalf("cancelled run error = %q, want the engine cancellation error", finished.Error)
	}
	runner.Wait()
	if status := runner.Status(); status.Busy {
		t.Fatalf("runner still busy after cancellation: %+v", status)
	}
	// A cancelled run must not claim a completed result.
	if finished.Result != nil && finished.Result.Status == speedtest.StatusCompleted {
		t.Fatalf("cancelled run produced a completed result: %+v", finished.Result)
	}
}

func TestSecondTaskIsRejectedWhileRunning(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      1,
		DurationMs:       30000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := runner.Start(StartOptions{SelectionMode: SelectionModeAuto}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Start error = %v, want ErrBusy", err)
	}
	if _, err := runner.CheckNodes(); !errors.Is(err, ErrBusy) {
		t.Fatalf("CheckNodes error = %v, want ErrBusy", err)
	}
	if err := runner.CancelTest(); err != nil {
		t.Fatalf("CancelTest: %v", err)
	}
	runner.Wait()
}

func TestStartValidatesOptions(t *testing.T) {
	runner := NewRunner(nil)
	cases := []struct {
		name    string
		options StartOptions
	}{
		{"unknown mode", StartOptions{SelectionMode: "fastest"}},
		{"manual without id", StartOptions{SelectionMode: SelectionModeManual}},
		{"too many connections", StartOptions{SelectionMode: SelectionModeAuto, Connections: speedtest.MaxConnections + 1}},
		{"duration too short", StartOptions{SelectionMode: SelectionModeAuto, DurationMs: 10}},
		{"duration too long", StartOptions{SelectionMode: SelectionModeAuto, DurationMs: maxDurationMs + 1}},
		{"sample interval too short", StartOptions{SelectionMode: SelectionModeAuto, SampleIntervalMs: 1}},
		{"sample interval too long", StartOptions{SelectionMode: SelectionModeAuto, SampleIntervalMs: maxSampleIntervalMs + 1}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := runner.Start(testCase.options); err == nil {
				t.Fatal("expected a validation error")
			}
			if status := runner.Status(); status.Busy {
				t.Fatalf("invalid request registered a session: %+v", status)
			}
		})
	}
}

func TestAutoSelectionDoesNotFabricateResultWhenNodesFail(t *testing.T) {
	// Port 1 on loopback refuses connections: every probe attempt fails, so no
	// node may be selected and no rate may be reported.
	manager := newManager(t, "http://127.0.0.1:1")
	rec := newRecorder()
	runner := newTestRunner(t, rec, manager)

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeAuto,
		Connections:      1,
		DurationMs:       1000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 60*time.Second)
	if finished.Status != StateFailed {
		t.Fatalf("finished status = %q, want failed", finished.Status)
	}
	if finished.Result != nil {
		t.Fatalf("failed run produced a result: %+v", finished.Result)
	}
	if !strings.Contains(finished.Error, nodes.ErrNoNodeAvailable.Error()) {
		t.Fatalf("error = %q, want it to name the unavailable nodes", finished.Error)
	}
	if len(rec.targets(t)) != 0 {
		t.Fatal("a target was published although no node was available")
	}
}

func TestCheckNodesCancelReturnsCancelledResult(t *testing.T) {
	// The handler never answers, so the check only ends when it is cancelled.
	hanging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer hanging.Close()

	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, hanging.URL))

	type outcome struct {
		result NodeCheckResult
		err    error
	}
	results := make(chan outcome, 1)
	go func() {
		result, err := runner.CheckNodes()
		results <- outcome{result: result, err: err}
	}()

	// Give the probe a moment to start before cancelling it.
	time.Sleep(300 * time.Millisecond)
	if err := runner.CancelCheck(); err != nil {
		t.Fatalf("CancelCheck: %v", err)
	}
	select {
	case got := <-results:
		if got.err != nil {
			t.Fatalf("CheckNodes returned %v, want a clean cancellation", got.err)
		}
		if !got.result.Cancelled {
			t.Fatalf("cancelled check result = %+v", got.result)
		}
		if len(got.result.Nodes) != 1 {
			t.Fatalf("cancelled check returned %d node rows", len(got.result.Nodes))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("CheckNodes did not return after cancellation")
	}
	runner.Wait()
	if status := runner.Status(); status.Busy {
		t.Fatalf("runner still busy after cancellation: %+v", status)
	}
}

func TestProgressEventKeepsMeasuredValues(t *testing.T) {
	fraction := 0.5
	remaining := 2500 * time.Millisecond
	event := progressEvent(speedtest.Progress{
		State:             speedtest.StateDownloadTesting,
		Phase:             speedtest.PhaseDownload,
		Stage:             speedtest.StageProgress,
		Elapsed:           1500 * time.Millisecond,
		Bytes:             1234567,
		Mbps:              88.5,
		InstantMbps:       91.25,
		ActiveConnections: 4,
		Budget: speedtest.Budget{
			Fraction:  &fraction,
			Remaining: &remaining,
		},
	})
	if event.ElapsedMs != 1500 {
		t.Fatalf("elapsed = %v ms", event.ElapsedMs)
	}
	if event.Bytes != 1234567 || event.Mbps != 88.5 || event.InstantMbps != 91.25 {
		t.Fatalf("counters were altered: %+v", event)
	}
	if event.ActiveConnections != 4 || event.State != StateDownloadTesting {
		t.Fatalf("connection or state fields were altered: %+v", event)
	}
	if event.BudgetFraction == nil || *event.BudgetFraction != 0.5 {
		t.Fatalf("budget fraction = %v", event.BudgetFraction)
	}
	if event.BudgetRemainingMs == nil || *event.BudgetRemainingMs != 2500 {
		t.Fatalf("budget remaining = %v", event.BudgetRemainingMs)
	}
}

func TestStartOptionsNormalizedFillsEngineDefaults(t *testing.T) {
	normalized, err := (StartOptions{SelectionMode: SelectionModeManual, NodeID: " test "}).normalized()
	if err != nil {
		t.Fatalf("normalized: %v", err)
	}
	if normalized.NodeID != "test" {
		t.Fatalf("node id = %q", normalized.NodeID)
	}
	if normalized.Connections != speedtest.DefaultConnections {
		t.Fatalf("connections = %d", normalized.Connections)
	}
	if normalized.DurationMs != defaultDurationMs() {
		t.Fatalf("duration = %d", normalized.DurationMs)
	}
	if normalized.SampleIntervalMs != defaultSampleIntervalMs() {
		t.Fatalf("sample interval = %d", normalized.SampleIntervalMs)
	}
	automatic, err := (StartOptions{SelectionMode: SelectionModeAuto, NodeID: "ignored"}).normalized()
	if err != nil {
		t.Fatalf("normalized auto: %v", err)
	}
	if automatic.NodeID != "" {
		t.Fatalf("automatic selection kept a node id: %q", automatic.NodeID)
	}
}

func TestEngineOptionsDoNotChangeMeasurementWindow(t *testing.T) {
	options := engineOptions(speedtest.Target{ID: "test", BaseURL: "http://127.0.0.1:1"}, StartOptions{
		Connections:      4,
		DurationMs:       15000,
		SampleIntervalMs: 250,
	}, nil)
	if options.Duration != 15*time.Second {
		t.Fatalf("duration = %s", options.Duration)
	}
	if options.Connections != 4 {
		t.Fatalf("connections = %d", options.Connections)
	}
	if options.SampleInterval != 250*time.Millisecond {
		t.Fatalf("sample interval = %s", options.SampleInterval)
	}
	if options.MaxBytes != 0 {
		t.Fatalf("max bytes = %d, want 0 (duration only)", options.MaxBytes)
	}
	if options.Timeout <= options.Duration {
		t.Fatalf("timeout %s must be longer than duration %s", options.Timeout, options.Duration)
	}
	if !options.Warmup || !options.NegotiateCapabilities {
		t.Fatalf("warmup or capability negotiation was disabled: %+v", options)
	}
	if options.LatencySamples != speedtest.DefaultLatencySamples {
		t.Fatalf("latency samples = %d", options.LatencySamples)
	}
}
