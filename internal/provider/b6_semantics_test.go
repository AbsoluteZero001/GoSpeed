package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// B6 unified measurement semantics audit.
//
// This file states the ten presentation rules as executable checks. It does not
// recompute any speed figure: the engine owns the measurement, and the audit
// only verifies that what the provider publishes cannot be misread.
//
// Rule numbering matches the B6 task list.

// ---------------------------------------------------------------------------
// 1. No fabricated zero when nothing was measured
// ---------------------------------------------------------------------------

func TestSemantic01NoFabricatedZero(t *testing.T) {
	final := mapResult(speedtest.Result{Status: speedtest.StatusCompleted}, nil, unitRunID())
	if final.Download != nil || final.Upload != nil || final.Latency != nil || final.Jitter != nil {
		t.Fatalf("an unmeasured run published a metric: %+v", final)
	}
	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	for _, field := range []string{"download", "upload", "latency", "jitter"} {
		value, present := decoded[field]
		if !present || value != nil {
			t.Errorf("%q = %v, want null rather than a fabricated zero", field, value)
		}
	}
	// A rate that exists must never be a zero standing in for "unknown".
	if final.Download != nil && final.Download.Mbps != nil && *final.Download.Mbps <= 0 {
		t.Error("a published rate must be a real positive measurement")
	}
}

// ---------------------------------------------------------------------------
// 2. Below clock resolution is N/A
// ---------------------------------------------------------------------------

func TestSemantic02BelowResolutionIsNull(t *testing.T) {
	latency, jitter := latencyMetrics(&speedtest.LatencyResult{
		Type:              speedtest.LatencyHTTPRTT,
		SuccessfulSamples: 5,
		AverageNs:         0,
		SamplesNs:         []time.Duration{0, 0, 0, 0, 0},
		JitterNs:          durationPtr(0),
	})
	if latency != nil || jitter != nil {
		t.Fatalf("an unresolvable measurement was published: latency=%+v jitter=%+v", latency, jitter)
	}
}

// ---------------------------------------------------------------------------
// 3. Scope and the quality note must agree
// ---------------------------------------------------------------------------

func TestSemantic03ScopeAndNoteAgree(t *testing.T) {
	for _, engineScope := range []speedtest.NetworkScope{
		speedtest.ScopeLocal, speedtest.ScopeLAN, speedtest.ScopeRemote, speedtest.ScopeUnknown,
	} {
		// A deliberately wrong caller scope: the engine's derivation must win
		// everywhere, including the note.
		node := Node{ID: "n", Endpoint: "http://127.0.0.1:1", Scope: ScopeRemote}
		engineResult := speedtest.Result{Status: speedtest.StatusCompleted}
		engineResult.Target.NetworkScope = engineScope

		final := mapResult(engineResult, nil, runID{node: node, caps: allCapabilities()})
		var note string
		for _, candidate := range final.Quality.Notes {
			if strings.HasPrefix(candidate, "node scope: ") {
				note = candidate
			}
		}
		want := "node scope: " + string(final.Scope)
		if note != want {
			t.Errorf("engine scope %q: scope field = %q but note = %q", engineScope, final.Scope, note)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. An unnegotiated capability must never read as supported
// ---------------------------------------------------------------------------

func TestSemantic04UnnegotiatedCapabilityIsNotSupported(t *testing.T) {
	// The engine advertised download only.
	info := speedtest.CapabilityInfo{Supported: true, Capabilities: []string{"download"}}
	caps, err := capabilitiesFrom(info)
	if err != nil {
		t.Fatalf("capabilitiesFrom: %v", err)
	}
	if !caps.Supports(CapDownload) {
		t.Error("download was advertised but is not declared")
	}
	for _, absent := range []Capability{CapUpload, CapLatency, CapJitter, CapPacketLoss, CapLoadedLatency} {
		if caps.Supports(absent) {
			t.Errorf("%q was not advertised but is declared as supported", absent)
		}
		// A note must never accompany a capability that is not supported: the
		// GUI would then render "not negotiated" next to a method note.
		if note := caps.Note(absent); note != "" {
			t.Errorf("%q is not supported but carries the note %q", absent, note)
		}
	}
}

// ---------------------------------------------------------------------------
// 5. A cancelled run must not read as completed
// ---------------------------------------------------------------------------

func TestSemantic05CancelIsNotCompleted(t *testing.T) {
	cases := []struct {
		err    error
		status MeasurementStatus
	}{
		{context.Canceled, StatusCancelled},
		{context.DeadlineExceeded, StatusTimeout},
		{speedtest.ErrIncompleteTransfer, StatusFailed},
		{errors.New("server exploded"), StatusFailed},
	}
	for _, testCase := range cases {
		status, _, _ := classify(testCase.err, speedtest.Result{}, BudgetLimits{})
		if status != testCase.status {
			t.Errorf("error %v mapped to %q, want %q", testCase.err, status, testCase.status)
		}
		if status == StatusCompleted {
			t.Errorf("error %v was reported as completed", testCase.err)
		}
	}
}

// ---------------------------------------------------------------------------
// 6. A partial run must be announced
// ---------------------------------------------------------------------------

func TestSemantic06PartialIsAnnounced(t *testing.T) {
	// The download finished; the upload produced nothing.
	engineResult := speedtest.Result{
		Status: speedtest.StatusFailed,
		Download: &speedtest.TransferResult{
			Bytes: 1 << 20, DurationNs: time.Second, Mbps: 8,
			MeasurementWindow: speedtest.WindowDownload, StopReason: speedtest.StopReasonDuration,
		},
	}
	final := mapResult(engineResult, speedtest.ErrNoDataTransferred, unitRunID())
	if final.Quality.Completeness != Partial {
		t.Errorf("completeness = %q, want partial", final.Quality.Completeness)
	}
	if final.Download == nil {
		t.Error("the direction that did produce evidence was dropped")
	}
	if final.Upload != nil {
		t.Error("the direction that produced nothing was published")
	}
}

// ---------------------------------------------------------------------------
// 7. Timeout and failed are different outcomes
// ---------------------------------------------------------------------------

func TestSemantic07TimeoutDiffersFromFailed(t *testing.T) {
	timeoutStatus, timeoutCode, _ := classify(context.DeadlineExceeded, speedtest.Result{}, BudgetLimits{})
	failedStatus, failedCode, _ := classify(speedtest.ErrNoDataTransferred, speedtest.Result{}, BudgetLimits{})
	if timeoutStatus != StatusTimeout || failedStatus != StatusFailed {
		t.Fatalf("statuses: timeout=%q failed=%q", timeoutStatus, failedStatus)
	}
	if timeoutCode == failedCode {
		t.Errorf("timeout and failure share the error code %q", timeoutCode)
	}
	if timeoutCode != "timeout" {
		t.Errorf("timeout error code = %q", timeoutCode)
	}
}

// ---------------------------------------------------------------------------
// 8. A planned stop at the byte budget is completed, never budget_exceeded
// ---------------------------------------------------------------------------

func TestSemantic08BudgetStopIsCompleted(t *testing.T) {
	engineResult := speedtest.Result{
		Status: speedtest.StatusCompleted,
		Download: &speedtest.TransferResult{
			Bytes: 1 << 20, DurationNs: time.Second, Mbps: 8,
			MeasurementWindow: speedtest.WindowDownload, StopReason: speedtest.StopReasonRequestedBytes,
		},
	}
	status, code, _ := classify(nil, engineResult, BudgetLimits{DownloadBytes: 1 << 20})
	if status != StatusCompleted {
		t.Fatalf("status = %q, want completed: stopping at the requested bytes is planned", status)
	}
	if code != "" {
		t.Errorf("a completed run reported error code %q", code)
	}

	// And a byte count disagreement is a failure, not a budget abort.
	mismatch := errors.Join(errors.New("upload"), speedtest.ErrByteCountMismatch)
	status, code, _ = classify(mismatch, speedtest.Result{}, BudgetLimits{UploadBytes: 1 << 20})
	if status != StatusFailed || code != "byte_count_mismatch" {
		t.Errorf("byte count mismatch mapped to %q/%q, want failed/byte_count_mismatch", status, code)
	}
}

// ---------------------------------------------------------------------------
// 9 and 10. RTTVar never becomes jitter; HTTP RTT never becomes an ICMP ping
// ---------------------------------------------------------------------------

func TestSemantic09JitterIsDerivedFromLatencySamplesOnly(t *testing.T) {
	// Jitter exists only alongside latency, and carries its derivation.
	latency, jitter := latencyMetrics(&speedtest.LatencyResult{
		Type:              speedtest.LatencyHTTPRTT,
		SuccessfulSamples: 3,
		AverageNs:         2 * time.Millisecond,
		JitterNs:          durationPtr(400 * time.Microsecond),
		SamplesNs:         []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond},
	})
	if latency == nil || latency.Type != MetricHTTPRTT {
		t.Fatalf("latency = %+v, want http_rtt", latency)
	}
	if strings.Contains(latency.Source, "icmp") && !strings.Contains(latency.Source, "not an icmp") {
		t.Errorf("the latency source must disclaim ICMP, got %q", latency.Source)
	}
	if jitter == nil || jitter.Type != JitterConsecutiveDiff {
		t.Fatalf("jitter = %+v, want consecutive_diff", jitter)
	}
	if jitter.Source == "" {
		t.Error("the jitter must state how it was derived")
	}
}

func TestSemantic10JitterNeverOutlivesAnUnresolvableLatency(t *testing.T) {
	// Jitter derived from samples that were all zero is not a measurement.
	latency, jitter := latencyMetrics(&speedtest.LatencyResult{
		Type:              speedtest.LatencyHTTPRTT,
		SuccessfulSamples: 3,
		AverageNs:         0,
		JitterNs:          durationPtr(0),
		SamplesNs:         []time.Duration{0, 0, 0},
	})
	if latency != nil || jitter != nil {
		t.Fatalf("jitter survived an unresolvable latency: latency=%+v jitter=%+v", latency, jitter)
	}
}

// ---------------------------------------------------------------------------
// metric identity, end to end
// ---------------------------------------------------------------------------

// TestSemanticMetricIdentityFromALiveRun checks the whole published identity on
// a real measurement: provider, ownership, discovery, scope and metric types.
func TestSemanticMetricIdentityFromALiveRun(t *testing.T) {
	testServer := realGoSpeedServer(t)
	measurement := newTestProvider(t)

	node := nodeFor(testServer.URL)
	result, err := measurement.Run(context.Background(), RunRequest{
		Node:         node,
		Capabilities: allCapabilities(),
		Budget:       BudgetLimits{DownloadBytes: measurableByteBudget},
	})
	if err != nil {
		t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
	}

	if result.ProviderID != IDGoSpeedHTTP {
		t.Errorf("providerId = %q", result.ProviderID)
	}
	if result.ServerID != node.ID {
		t.Errorf("serverId = %q, want %q", result.ServerID, node.ID)
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
	if result.Method.Protocol != ProtocolGoSpeedHTTP {
		t.Errorf("protocol = %q", result.Method.Protocol)
	}

	// Rates are real megabits per second and carry their window.
	for name, rate := range map[string]*Rate{"download": result.Download, "upload": result.Upload} {
		if rate == nil || rate.Mbps == nil {
			t.Fatalf("%s rate is missing", name)
		}
		if *rate.Mbps <= 0 {
			t.Errorf("%s mbps = %v, want a positive real value", name, *rate.Mbps)
		}
		if rate.Window == "" {
			t.Errorf("%s has no measurement window definition", name)
		}
		if rate.ElapsedMs <= 0 {
			t.Errorf("%s window length = %d ms", name, rate.ElapsedMs)
		}
	}

	if result.Latency != nil {
		if result.Latency.Type != MetricHTTPRTT {
			t.Errorf("latency type = %q", result.Latency.Type)
		}
		if result.Jitter != nil && result.Jitter.Type != JitterConsecutiveDiff {
			t.Errorf("jitter type = %q", result.Jitter.Type)
		}
	}

	// Packet loss is never claimed by this provider.
	for capability, supported := range result.Method.NegotiatedCaps.Supported {
		if capability == CapPacketLoss && supported {
			t.Error("packet loss must never be claimed")
		}
	}
	if result.Quality.Completeness != Complete {
		t.Errorf("completeness = %q, want complete for a finished run", result.Quality.Completeness)
	}
}
