package provider

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// This file pins the two honesty rules the B5 GUI work uncovered from real
// captured measurements: an unresolvable metric is null rather than a measured
// zero, and one result never states two different scopes.

// TestUnresolvableLatencyIsNullNotZero covers the loopback case where every
// round trip completes inside one tick of the platform clock.
//
// The engine reports a set of zero samples. That is an observation that the
// quantity was too small to resolve; publishing 0.00 ms would claim a
// measurement that was never made.
func TestUnresolvableLatencyIsNullNotZero(t *testing.T) {
	latency, jitter := latencyMetrics(&speedtest.LatencyResult{
		Type:              speedtest.LatencyHTTPRTT,
		Attempts:          5,
		SuccessfulSamples: 5,
		MinNs:             0,
		AverageNs:         0,
		MaxNs:             0,
		JitterNs:          durationPtr(0),
		SamplesNs:         []time.Duration{0, 0, 0, 0, 0},
	})
	if latency != nil {
		t.Fatalf("latency = %+v, want null for an unresolvable measurement", latency)
	}
	if jitter != nil {
		t.Fatalf("jitter = %+v, want null when its source samples are unresolvable", jitter)
	}
}

// TestMeasurableLatencyKeepsItsValue is the other half: a real measurement must
// still be reported, not suppressed by the rule above.
func TestMeasurableLatencyKeepsItsValue(t *testing.T) {
	latency, jitter := latencyMetrics(&speedtest.LatencyResult{
		Type:              speedtest.LatencyHTTPRTT,
		Attempts:          5,
		SuccessfulSamples: 5,
		MinNs:             2 * time.Millisecond,
		AverageNs:         3 * time.Millisecond,
		MaxNs:             9 * time.Millisecond,
		JitterNs:          durationPtr(1500 * time.Microsecond),
		SamplesNs:         []time.Duration{2 * time.Millisecond, 3 * time.Millisecond, 9 * time.Millisecond},
	})
	if latency == nil || latency.ValueMs == nil {
		t.Fatal("a resolvable measurement was dropped")
	}
	if *latency.ValueMs != 3 {
		t.Errorf("latency = %v ms, want 3", *latency.ValueMs)
	}
	if jitter == nil || jitter.ValueMs == nil || *jitter.ValueMs != 1.5 {
		t.Fatalf("jitter = %+v, want 1.5 ms", jitter)
	}
}

// TestSingleResolvableSampleStillReportsLatency covers a mixed sample set: one
// sample above the clock resolution is enough to report a real value.
func TestSingleResolvableSampleStillReportsLatency(t *testing.T) {
	latency, _ := latencyMetrics(&speedtest.LatencyResult{
		Type:              speedtest.LatencyHTTPRTT,
		SuccessfulSamples: 2,
		AverageNs:         time.Microsecond,
		SamplesNs:         []time.Duration{0, time.Microsecond},
	})
	if latency == nil || latency.ValueMs == nil {
		t.Fatal("one resolvable sample must produce a reported latency")
	}
}

func TestBelowClockResolutionDetection(t *testing.T) {
	cases := []struct {
		name    string
		result  *speedtest.LatencyResult
		want    bool
		comment string
	}{
		{"all zero samples", &speedtest.LatencyResult{SamplesNs: []time.Duration{0, 0}}, true, "unresolvable"},
		{"some measurable", &speedtest.LatencyResult{SamplesNs: []time.Duration{0, time.Microsecond}}, false, "resolvable"},
		{"no samples but zero average", &speedtest.LatencyResult{AverageNs: 0}, true, "unresolvable"},
		{"no samples but real average", &speedtest.LatencyResult{AverageNs: time.Microsecond}, false, "resolvable"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := belowClockResolution(testCase.result); got != testCase.want {
				t.Errorf("belowClockResolution = %t, want %t (%s)", got, testCase.want, testCase.comment)
			}
		})
	}
}

// TestResultStatesOneScope pins that a result never contradicts itself: the
// reported scope and the quality note must describe the same reachability.
func TestResultStatesOneScope(t *testing.T) {
	cases := []struct {
		name          string
		engineScope   speedtest.NetworkScope
		nodeScope     Scope
		wantScope     Scope
		wantNoteScope string
	}{
		{"loopback", speedtest.ScopeLocal, ScopeUnknown, ScopeLocal, "local"},
		{"lan", speedtest.ScopeLAN, ScopeUnknown, ScopeLAN, "lan"},
		{"remote", speedtest.ScopeRemote, ScopeUnknown, ScopeRemote, "remote"},
		// The engine derived the scope; a stale caller scope must not override
		// it, and it must not leak into the note either.
		{"engine wins over a stale caller scope", speedtest.ScopeLocal, ScopeRemote, ScopeLocal, "local"},
		// Neither side could classify it: stays unknown in both places.
		{"unknown stays unknown", speedtest.ScopeUnknown, ScopeUnknown, ScopeUnknown, "unknown"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			node := Node{ID: "n", Endpoint: "http://127.0.0.1:8080", Scope: testCase.nodeScope}
			engineResult := speedtest.Result{Status: speedtest.StatusCompleted}
			engineResult.Target.NetworkScope = testCase.engineScope

			final := mapResult(engineResult, nil, runID{node: node, caps: allCapabilities()})
			if final.Scope != testCase.wantScope {
				t.Fatalf("scope = %q, want %q", final.Scope, testCase.wantScope)
			}

			var note string
			for _, candidate := range final.Quality.Notes {
				if strings.HasPrefix(candidate, "node scope: ") {
					note = candidate
				}
			}
			if note != "node scope: "+testCase.wantNoteScope {
				t.Fatalf("quality note = %q, want %q: the result states two scopes",
					note, "node scope: "+testCase.wantNoteScope)
			}
		})
	}
}

// TestLoopbackRunKeepsScopeAndRatesConsistent is the end to end version of the
// rule above, measured against a real loopback server.
func TestLoopbackRunKeepsScopeAndRatesConsistent(t *testing.T) {
	testServer := realGoSpeedServer(t)
	provider := newTestProvider(t)

	// The node deliberately carries a stale remote scope, which is what the
	// desktop bridge can pass when the caller never normalised the target.
	result, err := provider.Run(context.Background(), RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: allCapabilities(),
		Budget:       BudgetLimits{DownloadBytes: measurableByteBudget},
	})
	if err != nil {
		t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
	}
	if result.Scope != ScopeLocal {
		t.Fatalf("scope = %q, want local for a loopback node", result.Scope)
	}
	for _, note := range result.Quality.Notes {
		if strings.Contains(note, "node scope: remote") {
			t.Fatalf("a loopback run reported a remote scope note: %v", result.Quality.Notes)
		}
	}
	// No metric may be a published zero that was never measured.
	if result.Latency != nil && result.Latency.ValueMs != nil && *result.Latency.ValueMs <= 0 {
		t.Errorf("latency = %v ms, want a positive value or null", *result.Latency.ValueMs)
	}
	if result.Download == nil || result.Download.Mbps == nil || *result.Download.Mbps <= 0 {
		t.Error("the download rate must be a real positive measurement")
	}
}

func durationPtr(d time.Duration) *time.Duration { return &d }
