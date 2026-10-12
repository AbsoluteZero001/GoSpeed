package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// This file is the compatibility bridge between the unified provider model and
// the existing desktop client.
//
// The problem it solves
//
// A consumer needs a Runner by provider ID, so the runner resolves providers
// through provider.Registry instead of hardcoding the GoSpeed engine. But
// provider.Runner.Run returns provider.FinalResult, a cross-provider model that
// deliberately carries only what every provider can express. The desktop result
// panel reads fields FinalResult does not have at all: per-connection evidence,
// the 200 ms sample series, the descriptive statistics and the settings
// snapshot.
//
// The tempting fix is to rebuild a speedtest.Result out of FinalResult. That is
// rejected: it would invent engine fields the measurement never produced.
//
// The bridge used here
//
// The measurement runs exactly once, inside the provider. FinalResult is the
// unified verdict, and it additionally carries FinalResult.Detail: the GoSpeed
// provider's own result, encoded verbatim. The bridge decodes that payload for
// the legacy detail panel. Nothing is reconstructed and nothing is translated,
// so the panel keeps reading exactly the bytes the engine produced, and the
// finished event keeps its original shape. A provider without native detail
// leaves the field nil and the panel falls back to the unified view, which is
// what B5 will render properly.

// defaultProviderID is the only provider this build may use.
//
// Registering M-Lab or Cloudflare here would make the desktop claim an
// integration that does not exist. It stays opt-in and per-run consented.
const defaultProviderID = provider.IDGoSpeedHTTP

// newDefaultRegistry wires the providers the desktop build is allowed to use.
//
// It performs no network I/O: the GoSpeed provider builds an HTTP client and
// nothing more. Registering a provider never probes a node and never negotiates
// capabilities.
func newDefaultRegistry() (*provider.Registry, error) {
	registry := provider.NewRegistry()
	measurement := provider.NewGoSpeedHTTP(nil)
	// The provider owns the client it created itself, so it is the one that
	// releases it, and the closer is the registry's only release path.
	if err := registry.Register(defaultProviderID, measurement, func() error {
		measurement.CloseIdleConnections()
		return nil
	}); err != nil {
		return nil, err
	}
	return registry, nil
}

// providerRunner resolves the runner that serves the build's default provider.
func (r *Runner) providerRunner() (provider.Runner, error) {
	registry, err := r.registry()
	if err != nil {
		return nil, err
	}
	runner, err := registry.Lookup(defaultProviderID)
	if err != nil {
		// The registry never substitutes another provider: a missing
		// gospeed-http runner must surface as a missing measurement backend,
		// not as a silent fallback to an unauthorised public service.
		return nil, fmt.Errorf("%w: %s: %w", errProviderUnavailable, defaultProviderID, err)
	}
	return runner, nil
}

// providerNode turns the resolved engine target into the provider node view.
//
// Only the identity and the address cross the boundary. Scope and ownership
// come from the node configuration and are never inferred from a rate.
func providerNode(target speedtest.Target) provider.Node {
	scope := provider.Scope(target.NetworkScope())
	if scope == "" {
		scope = provider.ScopeUnknown
	}
	// A GoSpeed HTTP node is one the user configured themselves. The registry
	// never hands out an official node, and this mapping must never imply one.
	return provider.Node{
		ID:        target.ID,
		Name:      target.Name,
		Endpoint:  target.BaseURL,
		Scope:     scope,
		Provider:  defaultProviderID,
		Ownership: provider.OwnershipSelfHosted,
		Discovery: provider.DiscoveryConfigured,
	}
}

// providerCapabilities reports what the GUI may ask for. The GUI measures
// latency, download and upload against a node the user configured, so the
// request states those three capabilities.
//
// The runner still probes GET /capabilities for itself during the run and the
// engine still refuses a run the server's limits do not allow; this set only
// says which directions the GUI intends to measure.
func providerCapabilities() provider.CapabilitySet {
	return provider.CapabilitySet{
		Supported: map[provider.Capability]bool{
			provider.CapDownload: true,
			provider.CapUpload:   true,
			provider.CapLatency:  true,
			// Jitter is derived from the latency samples, so it exists exactly
			// when latency sampling does. Declaring its note without declaring
			// the capability would make the GUI report "not negotiated" next to a
			// jitter method note, which is a contradiction.
			provider.CapJitter: true,
		},
		Notes: map[provider.Capability]string{
			provider.CapDownload: provider.NoteDownloadSource,
			provider.CapUpload:   provider.NoteUploadSource,
			provider.CapLatency:  provider.NoteLatencySource,
			provider.CapJitter:   provider.NoteJitterSource,
		},
	}
}

// providerRequest builds the provider request for a GUI run.
//
// The transfer window, the per phase timeout and the byte budget keep exactly
// the semantics the engine previously received, so routing through the provider
// does not change what is measured:
//
//   - MaxBytes stays zero: a GUI run is duration limited, and the provider maps
//     a zero budget to "no byte cap" for exactly that reason.
//   - Warmup, latency sampling and the sample cadence are forwarded unchanged,
//     so the live curves keep the cadence the user configured.
func providerRequest(target speedtest.Target, options StartOptions, sink provider.EventSink) provider.RunRequest {
	duration := time.Duration(options.DurationMs) * time.Millisecond
	return provider.RunRequest{
		Node:         providerNode(target),
		Capabilities: providerCapabilities(),
		Directions: []provider.Direction{
			provider.DirectionDownload,
			provider.DirectionUpload,
		},
		Duration:        duration,
		Connections:     options.Connections,
		Budget:          provider.BudgetLimits{},
		SampleInterval:  time.Duration(options.SampleIntervalMs) * time.Millisecond,
		Warmup:          true,
		LatencySamples:  speedtest.DefaultLatencySamples,
		LatencyInterval: speedtest.DefaultLatencyInterval,
		Events:          sink,
	}
}

// progressSink forwards provider progress into the existing Wails progress
// event and keeps the runner state current.
//
// Two properties matter here:
//   - It never blocks the measurement. It only copies observed values and hands
//     them to the emit function, which the UI layer already treats as a
//     fire-and-forget call.
//   - It stops emitting once the session is no longer the current one, so a
//     cancelled or finished session can never receive a late sample that would
//     repaint a chart for a run that already ended.
type progressSink struct {
	runner  *Runner
	session *session
}

func (s *progressSink) OnProgress(event provider.ProgressEvent) {
	if !s.runner.sessionActive(s.session) {
		return
	}
	if event.RunState != "" {
		s.runner.recordState(event.RunState)
	}
	s.runner.emit(EventProgress, desktopProgress(event))
}

// desktopProgress converts a provider progress event into the existing GUI
// event. The JSON field names and their meanings are unchanged: a consumer that
// already reads this event keeps reading the same values.
//
// State stays the engine stage and RunState becomes State, matching the event
// the v0.4.0 client already receives.
func desktopProgress(event provider.ProgressEvent) ProgressEvent {
	out := ProgressEvent{
		State:             firstNonEmpty(event.RunState, event.State),
		Phase:             event.Phase,
		Stage:             event.State,
		ElapsedMs:         event.ElapsedMs,
		Bytes:             event.Bytes,
		Mbps:              event.Mbps,
		InstantMbps:       event.InstantMbps,
		ActiveConnections: event.ActiveConnections,
		Samples:           event.Samples,
		Message:           event.Message,
		BudgetFraction:    event.BudgetFraction,
		BudgetRemainingMs: event.BudgetRemainingMs,
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// applyFinished folds the unified result into the existing finished event.
//
// The status comes from the unified verdict, the error text from the unified
// result, and the legacy result payload from the provider's verbatim native
// detail. Nothing is derived from the other two, so no field can disagree with
// the measurement that produced it.
//
// It mutates the event the runner already built instead of replacing it: the
// target notice and the wall time were recorded before the measurement started
// and must survive it.
func applyFinished(event *FinishedEvent, final provider.FinalResult, runErr error) {
	event.Status = ""
	event.Error = ""
	event.Cancelled = false
	event.Result = nil
	event.TestID = ""
	event.Unified = nil

	// The unified verdict is published for every run, including the ones that
	// never reached the network. It is the only place that states the measured
	// scope, the measurement method of each metric and the evidence quality, so
	// a UI can never have to infer those from a rate.
	unified := final
	event.Unified = &unified
	switch final.Status {
	case provider.StatusCompleted:
		event.Status = StateCompleted
	case provider.StatusCancelled:
		event.Status = StateCancelled
		event.Cancelled = true
	case provider.StatusTimeout:
		event.Status = StateFailed
	default:
		event.Status = StateFailed
	}
	if final.ErrorMessage != "" {
		event.Error = final.ErrorMessage
	} else if runErr != nil {
		event.Error = runErr.Error()
	}
	if event.Status == StateCompleted && runErr != nil {
		// A completed unified verdict with an error means the two disagree,
		// which must not be reported as a success.
		event.Status = StateFailed
		event.Error = runErr.Error()
	}

	result, testID := decodeNativeDetail(final)
	event.Result = result
	event.TestID = testID
}

// decodeNativeDetail returns the provider's native result, decoded verbatim.
//
// A nil result means the provider had no native detail: the measurement still
// succeeded and the unified verdict still stands, only the legacy detail panel
// has nothing to show. A payload this build cannot decode is reported the same
// way rather than being partially reconstructed.
func decodeNativeDetail(final provider.FinalResult) (*speedtest.Result, string) {
	if final.Detail == nil || final.Detail.Format != provider.DetailFormatGoSpeedResult {
		return nil, ""
	}
	if len(final.Detail.Payload) == 0 {
		return nil, ""
	}
	var result speedtest.Result
	if err := json.Unmarshal(final.Detail.Payload, &result); err != nil {
		return nil, ""
	}
	return &result, result.TestID
}

// runProviderMeasurement performs one measurement through the registry's runner
// and returns the unified result.
//
// It performs no network request of its own: the runner owns the whole
// measurement, the budget and the cancellation.
func (r *Runner) runProviderMeasurement(ctx context.Context, target speedtest.Target, options StartOptions, session *session) (provider.FinalResult, error) {
	runner, err := r.providerRunner()
	if err != nil {
		return provider.FinalResult{ProviderID: defaultProviderID, ServerID: target.ID, Status: provider.StatusFailed, ErrorCode: "provider_unavailable", ErrorMessage: err.Error()}, err
	}
	request := providerRequest(target, options, &progressSink{runner: r, session: session})
	return runner.Run(ctx, request)
}

// sessionActive reports whether session is still the runner's current session.
// A cancelled or finished session is no longer current, which is what stops
// late progress events from repainting an ended run.
func (r *Runner) sessionActive(session *session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return session != nil && r.session == session
}

// errProviderUnavailable reports that the build has no runner for the provider
// a run needs. It is a wiring problem, not a measurement failure.
var errProviderUnavailable = errors.New("desktop: no measurement provider is registered")
