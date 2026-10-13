package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// This file pins the engine to unified result mapping.
//
// The golden strategy is deliberate: the comparison maps the result of ONE real
// engine run, so every field can be asserted for exact equality without ever
// comparing two independent live measurements. A second live run would differ
// by a few microseconds of window timing, which says nothing about whether the
// mapping is correct. Timing dependent behaviour is covered separately, by
// asserting the relations between a live adapter run and the engine's own
// invariants (status, stop reason, byte accounting).

// unitRunID is the request context of the pure mapping tests.
func unitRunID() runID {
	return runID{
		node: unitNode(),
		caps: allCapabilities(),
		budget: BudgetLimits{
			DownloadBytes: 4 << 20,
			UploadBytes:   4 << 20,
			TotalBytes:    8 << 20,
		},
		elapsed: 3*time.Second + 250*time.Millisecond,
	}
}

// TestMapResultMatchesALiveEngineRun is the central golden test: the adapter's
// mapping of a real engine result must reproduce the engine's numbers exactly.
func TestMapResultMatchesALiveEngineRun(t *testing.T) {
	testServer := realGoSpeedServer(t)
	ctx := context.Background()

	// The budget must be large enough to span several clock ticks: a 4 MiB
	// total split into two directions finished inside one tick on the Windows
	// CI runner, and the engine correctly refused to turn that into a rate.
	// measurableByteBudget is the size that always spans ticks on loopback.
	directions := []Direction{DirectionDownload, DirectionUpload}
	budget, err := effectiveBudget(RunRequest{Budget: BudgetLimits{TotalBytes: measurableByteBudget * 2}}, directions)
	if err != nil {
		t.Fatalf("effectiveBudget: %v", err)
	}
	opts, err := optionsFor(RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: allCapabilities(),
		Budget:       BudgetLimits{TotalBytes: measurableByteBudget * 2},
	}, directions, budget, "loopback-1")
	if err != nil {
		t.Fatalf("optionsFor: %v", err)
	}

	engineResult, err := speedtest.NewEngine(nil).Run(ctx, opts)
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if engineResult.Status != speedtest.StatusCompleted {
		t.Fatalf("engine status = %q, want completed", engineResult.Status)
	}

	final := mapResult(engineResult, nil, runID{
		node:    nodeFor(testServer.URL),
		caps:    allCapabilities(),
		budget:  budget,
		elapsed: 2 * time.Second,
	})

	if final.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", final.Status)
	}

	// 1. Download Mbps is reproduced exactly, not recomputed.
	if final.Download == nil || final.Download.Mbps == nil {
		t.Fatal("download rate is missing")
	}
	if *final.Download.Mbps != engineResult.Download.Mbps {
		t.Errorf("download mbps = %v, engine = %v", *final.Download.Mbps, engineResult.Download.Mbps)
	}

	// 2. Upload Mbps is reproduced exactly.
	if final.Upload == nil || final.Upload.Mbps == nil {
		t.Fatal("upload rate is missing")
	}
	if *final.Upload.Mbps != engineResult.Upload.Mbps {
		t.Errorf("upload mbps = %v, engine = %v", *final.Upload.Mbps, engineResult.Upload.Mbps)
	}

	// 6. Byte statistics are carried over, never recomputed or double counted.
	if final.Download.TransferredBytes != engineResult.Download.Bytes {
		t.Errorf("download bytes = %d, engine = %d", final.Download.TransferredBytes, engineResult.Download.Bytes)
	}
	if final.Upload.TransferredBytes != engineResult.Upload.Bytes {
		t.Errorf("upload bytes = %d, engine = %d", final.Upload.TransferredBytes, engineResult.Upload.Bytes)
	}
	if final.TransferredBytes != engineResult.Download.Bytes+engineResult.Upload.Bytes {
		t.Errorf("total bytes = %d, want %d: the sum of both directions counted exactly once",
			final.TransferredBytes, engineResult.Download.Bytes+engineResult.Upload.Bytes)
	}

	// 7. The measurement window semantics are preserved verbatim.
	if final.Download.Window != engineResult.Download.MeasurementWindow {
		t.Errorf("download window = %q, engine = %q", final.Download.Window, engineResult.Download.MeasurementWindow)
	}
	if final.Upload.Window != engineResult.Upload.MeasurementWindow {
		t.Errorf("upload window = %q, engine = %q", final.Upload.Window, engineResult.Upload.MeasurementWindow)
	}
	if final.Download.ElapsedMs != engineResult.Download.DurationNs.Milliseconds() {
		t.Errorf("download window length = %d ms, engine = %d ms",
			final.Download.ElapsedMs, engineResult.Download.DurationNs.Milliseconds())
	}

	// 5. The original stop reason is not lost or renamed.
	for name, pair := range map[string]struct {
		got  string
		want speedtest.StopReason
	}{
		"download": {final.Download.StopReason, engineResult.Download.StopReason},
		"upload":   {final.Upload.StopReason, engineResult.Upload.StopReason},
	} {
		if pair.got != string(pair.want) {
			t.Errorf("%s stopReason = %q, engine = %q", name, pair.got, pair.want)
		}
	}

	// 3. Latency value and its declared quantity.
	//
	// On a loopback link every round trip can finish inside one clock tick, in
	// which case the engine reports unresolvable samples and the mapped metric
	// must be null. Either outcome is correct; a published 0.00 ms never is.
	if belowClockResolution(engineResult.Latency) {
		if final.Latency != nil || final.Jitter != nil {
			t.Fatalf("an unresolvable latency must map to null, got latency=%+v jitter=%+v", final.Latency, final.Jitter)
		}
	} else {
		if final.Latency == nil || final.Latency.ValueMs == nil {
			t.Fatal("a resolvable latency was dropped")
		}
		wantLatency := float64(engineResult.Latency.AverageNs) / float64(time.Millisecond)
		if *final.Latency.ValueMs != wantLatency {
			t.Errorf("latency ms = %v, engine average = %v", *final.Latency.ValueMs, wantLatency)
		}
		if *final.Latency.ValueMs <= 0 {
			t.Errorf("latency ms = %v, want a positive measurement", *final.Latency.ValueMs)
		}
		if final.Latency.Type != MetricHTTPRTT {
			t.Errorf("latency type = %q, want %q", final.Latency.Type, MetricHTTPRTT)
		}

		// 4. Jitter value and its declared derivation.
		if final.Jitter == nil || final.Jitter.ValueMs == nil {
			t.Fatal("jitter is missing although the engine produced a jitter value")
		}
		wantJitter := float64(*engineResult.Latency.JitterNs) / float64(time.Millisecond)
		if *final.Jitter.ValueMs != wantJitter {
			t.Errorf("jitter ms = %v, engine jitter = %v", *final.Jitter.ValueMs, wantJitter)
		}
		if final.Jitter.Type != JitterConsecutiveDiff {
			t.Errorf("jitter type = %q, want %q", final.Jitter.Type, JitterConsecutiveDiff)
		}
	}

	// The method record names the protocol and the applied budget.
	if final.Method.Protocol != ProtocolGoSpeedHTTP {
		t.Errorf("protocol = %q", final.Method.Protocol)
	}
	if final.Method.Budget != budget {
		t.Errorf("recorded budget = %+v, want the applied caps %+v", final.Method.Budget, budget)
	}
	// A completed upload is server confirmed, so the run earns verified trust.
	if final.Quality.Trust != "verified" {
		t.Errorf("trust = %q, want verified for a server confirmed upload", final.Quality.Trust)
	}
}

// TestMapResultReproducesEngineFieldsExactly walks fixed engine results through
// the mapping and asserts the unified result field by field. These inputs are
// constructed, not measured, so the expectations are exact and stable.
func TestMapResultReproducesEngineFieldsExactly(t *testing.T) {
	engineResults := []struct {
		name   string
		result speedtest.Result
		err    error
	}{
		{
			name: "byte limited download and upload",
			result: speedtest.Result{
				Status: speedtest.StatusCompleted,
				Download: &speedtest.TransferResult{
					Bytes: 1 << 20, DurationNs: 500 * time.Millisecond,
					Mbps: 16.777216, MeasurementWindow: speedtest.WindowDownload,
					StopReason: speedtest.StopReasonRequestedBytes,
				},
				Upload: &speedtest.UploadResult{
					TransferResult: speedtest.TransferResult{
						Bytes: 1 << 20, DurationNs: 500 * time.Millisecond,
						Mbps: 16.777216, MeasurementWindow: speedtest.WindowUpload,
						StopReason: speedtest.StopReasonRequestedBytes,
					},
					ServerConfirmedBytes: 1 << 20,
				},
			},
		},
		{
			name: "duration limited download",
			result: speedtest.Result{
				Status: speedtest.StatusCompleted,
				Download: &speedtest.TransferResult{
					Bytes: 4 << 20, DurationNs: 10 * time.Second,
					Mbps: 3.3554432, MeasurementWindow: speedtest.WindowDownloadMulti,
					StopReason: speedtest.StopReasonDuration,
				},
			},
		},
		{
			name: "server closed the stream",
			result: speedtest.Result{
				Status: speedtest.StatusCompleted,
				Download: &speedtest.TransferResult{
					Bytes: 4096, DurationNs: 120 * time.Millisecond,
					Mbps: 0.273066, MeasurementWindow: speedtest.WindowDownload,
					StopReason: speedtest.StopReasonServerEOF,
				},
			},
		},
	}

	for _, test := range engineResults {
		t.Run(test.name, func(t *testing.T) {
			final := mapResult(test.result, test.err, unitRunID())

			if test.result.Download != nil {
				if final.Download == nil {
					t.Fatal("download is missing")
				}
				if *final.Download.Mbps != test.result.Download.Mbps {
					t.Errorf("download mbps = %v, want %v", *final.Download.Mbps, test.result.Download.Mbps)
				}
				if final.Download.ElapsedMs != test.result.Download.DurationNs.Milliseconds() {
					t.Errorf("download elapsed = %d", final.Download.ElapsedMs)
				}
				if final.Download.StopReason != string(test.result.Download.StopReason) {
					t.Errorf("download stopReason = %q, want %q", final.Download.StopReason, test.result.Download.StopReason)
				}
				if final.Download.Window != test.result.Download.MeasurementWindow {
					t.Errorf("download window = %q", final.Download.Window)
				}
			}
			if test.result.Upload != nil {
				if final.Upload == nil {
					t.Fatal("upload is missing")
				}
				if *final.Upload.Mbps != test.result.Upload.Mbps {
					t.Errorf("upload mbps = %v, want %v", *final.Upload.Mbps, test.result.Upload.Mbps)
				}
				if final.Upload.StopReason != string(test.result.Upload.StopReason) {
					t.Errorf("upload stopReason = %q", final.Upload.StopReason)
				}
			}
			// A direction that never ran stays null in both directions.
			if test.result.Download == nil {
				if final.Download != nil {
					t.Error("download must stay null when the engine did not measure it")
				}
			}
			if test.result.Upload == nil {
				if final.Upload != nil {
					t.Error("upload must stay null when the engine did not measure it")
				}
			}

			want := int64(0)
			if test.result.Download != nil {
				want += test.result.Download.Bytes
			}
			if test.result.Upload != nil {
				want += test.result.Upload.Bytes
			}
			if final.TransferredBytes != want {
				t.Errorf("total bytes = %d, want %d", final.TransferredBytes, want)
			}
			if final.Status != StatusCompleted {
				t.Errorf("status = %q, want completed", final.Status)
			}
		})
	}
}

// TestMapResultKeepsMissingMetricsNull is the red line of the model: a metric
// that was not measured must serialize as JSON null, never as a measured zero.
func TestMapResultKeepsMissingMetricsNull(t *testing.T) {
	final := mapResult(speedtest.Result{Status: speedtest.StatusCompleted}, nil, unitRunID())

	if final.Download != nil || final.Upload != nil || final.Latency != nil || final.Jitter != nil {
		t.Fatalf("expected every metric to stay nil, got %+v", final)
	}

	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"download", "upload", "latency", "jitter"} {
		value, present := decoded[field]
		if !present {
			t.Errorf("%q is missing from the result", field)
			continue
		}
		if value != nil {
			t.Errorf("%q = %v, want null for an unmeasured metric", field, value)
		}
	}
	// The total byte count is a real observation of zero, not a missing metric.
	if decoded["transferredBytes"] != float64(0) {
		t.Errorf("transferredBytes = %v, want a real 0", decoded["transferredBytes"])
	}
}

// TestMapResultJitterStaysNullBelowTwoSamples pins the jitter rule: the whole
// metric is null, not a zero value, when the engine could not derive it.
func TestMapResultJitterStaysNullBelowTwoSamples(t *testing.T) {
	engineResult := speedtest.Result{
		Status: speedtest.StatusCompleted,
		Latency: &speedtest.LatencyResult{
			Type:              speedtest.LatencyHTTPRTT,
			Attempts:          5,
			SuccessfulSamples: 1,
			FailedSamples:     4,
			MinNs:             time.Millisecond,
			AverageNs:         time.Millisecond,
			MaxNs:             time.Millisecond,
			JitterNs:          nil,
		},
	}

	final := mapResult(engineResult, nil, unitRunID())
	if final.Latency == nil || final.Latency.ValueMs == nil {
		t.Fatal("the observed latency must still be reported")
	}
	if final.Jitter != nil {
		t.Fatalf("jitter = %+v, want nil when fewer than two samples were collected", final.Jitter)
	}

	// The missing jitter is explained instead of silently rendered as 0.
	found := false
	for _, warning := range final.Method.Warnings {
		if strings.Contains(warning, "jitter is null") {
			found = true
		}
		if strings.Contains(warning, "4 of 5 http rtt samples failed") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected warnings about the failed samples and the null jitter, got %v", final.Method.Warnings)
	}
}

// TestMapResultNeverReusesZeroForAnAbsentRate guards the case where a rate is
// absent because the phase failed: no pointer, so no consumer can render 0.
func TestMapResultNeverReusesZeroForAnAbsentRate(t *testing.T) {
	engineResult := speedtest.Result{
		Status:  speedtest.StatusFailed,
		Latency: nil,
	}
	final := mapResult(engineResult, speedtest.ErrIncompleteTransfer, unitRunID())

	if final.Status != StatusFailed {
		t.Fatalf("status = %q, want failed", final.Status)
	}
	if final.Download != nil || final.Upload != nil {
		t.Fatal("a failed phase must not publish a rate")
	}
	if final.ErrorCode != "incomplete_transfer" {
		t.Errorf("errorCode = %q", final.ErrorCode)
	}
	if final.Quality.Completeness != Incomplete {
		t.Errorf("completeness = %q, want incomplete", final.Quality.Completeness)
	}
	if final.Quality.Stability != "unknown" || final.Quality.Trust != "unknown" {
		t.Errorf("quality = %+v, want unknown stability and trust", final.Quality)
	}
}

// TestMapResultKeepsPartialConnectionWarnings proves the engine's own warning
// about a subset of connections failing survives into the unified result.
func TestMapResultKeepsPartialConnectionWarnings(t *testing.T) {
	engineResult := speedtest.Result{
		Status: speedtest.StatusCompleted,
		Warnings: []string{
			"download: 3 of 4 connections failed",
		},
		Download: &speedtest.TransferResult{
			Bytes: 2 << 20, DurationNs: time.Second,
			Mbps: 16.777216, MeasurementWindow: speedtest.WindowDownloadMulti,
			StopReason:        speedtest.StopReasonDuration,
			Connections:       4,
			ActiveConnections: 1,
			FailedConnections: 3,
		},
	}

	final := mapResult(engineResult, nil, unitRunID())

	if len(final.Method.Warnings) == 0 {
		t.Fatal("the partial connection failure warning was dropped")
	}
	if final.Method.Warnings[0] != "download: 3 of 4 connections failed" {
		t.Errorf("warning = %q, want the engine warning verbatim", final.Method.Warnings[0])
	}
	if final.Status != StatusCompleted {
		t.Errorf("status = %q: losing connections is a warning, not a failed status", final.Status)
	}
	if final.Quality.Stability != "degraded" {
		t.Errorf("stability = %q, want degraded when connections were lost", final.Quality.Stability)
	}
}

// TestMapResultTrustDistinguishesServerConfirmation pins the meaning of the
// trust level: only a server side byte count can make a run verified.
func TestMapResultTrustDistinguishesServerConfirmation(t *testing.T) {
	t.Run("download only stays normal", func(t *testing.T) {
		final := mapResult(speedtest.Result{
			Status: speedtest.StatusCompleted,
			Download: &speedtest.TransferResult{
				Bytes: 1 << 20, DurationNs: time.Second, Mbps: 8,
				MeasurementWindow: speedtest.WindowDownload, StopReason: speedtest.StopReasonDuration,
			},
		}, nil, unitRunID())
		if final.Quality.Trust != "normal" {
			t.Errorf("trust = %q, want normal: a download has no server side byte count", final.Quality.Trust)
		}
	})

	t.Run("confirmed upload is verified", func(t *testing.T) {
		final := mapResult(speedtest.Result{
			Status: speedtest.StatusCompleted,
			Upload: &speedtest.UploadResult{
				TransferResult: speedtest.TransferResult{
					Bytes: 1 << 20, DurationNs: time.Second, Mbps: 8,
					MeasurementWindow: speedtest.WindowUpload, StopReason: speedtest.StopReasonDuration,
				},
				ServerConfirmedBytes: 1 << 20,
			},
		}, nil, unitRunID())
		if final.Quality.Trust != "verified" {
			t.Errorf("trust = %q, want verified when the server confirmed the bytes", final.Quality.Trust)
		}
	})
}

// TestMapResultPartialCompleteness covers a run where one direction finished and
// the other did not.
func TestMapResultPartialCompleteness(t *testing.T) {
	engineResult := speedtest.Result{
		Status: speedtest.StatusFailed,
		Download: &speedtest.TransferResult{
			Bytes: 1 << 20, DurationNs: time.Second, Mbps: 8,
			MeasurementWindow: speedtest.WindowDownload, StopReason: speedtest.StopReasonDuration,
		},
	}

	final := mapResult(engineResult, speedtest.ErrNoDataTransferred, unitRunID())
	if final.Quality.Completeness != Partial {
		t.Errorf("completeness = %q, want partial: one direction produced evidence", final.Quality.Completeness)
	}
	if final.Download == nil {
		t.Error("the finished direction must still be reported")
	}
}

// TestFinalResultSerializesRealMbps checks the wire form: the rate is the real
// megabit per second value and the unmeasured metrics stay null.
func TestFinalResultSerializesRealMbps(t *testing.T) {
	mbps := 1234.5678
	jitter := 0.42
	latency := 12.5
	final := FinalResult{
		ProviderID: IDGoSpeedHTTP,
		ServerID:   "node-1",
		Status:     StatusCompleted,
		Download:   &Rate{Mbps: &mbps, ElapsedMs: 1000, TransferredBytes: 154320, Window: speedtest.WindowDownload, StopReason: string(speedtest.StopReasonDuration)},
		Latency:    &LatencyMetric{ValueMs: &latency, Type: MetricHTTPRTT, Source: NoteLatencySource},
		Jitter:     &JitterMetric{ValueMs: &jitter, Type: JitterConsecutiveDiff, Source: NoteJitterSource},
		Quality:    Quality{Completeness: Complete, Stability: "normal", Trust: "normal"},
		Method:     MethodRecord{Protocol: ProtocolGoSpeedHTTP},
	}

	encoded, err := json.Marshal(final)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Download *struct {
			Mbps *float64 `json:"mbps"`
		} `json:"download"`
		Upload *struct{} `json:"upload"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Download == nil || decoded.Download.Mbps == nil {
		t.Fatal("download mbps did not survive the round trip")
	}
	if *decoded.Download.Mbps != mbps {
		t.Errorf("mbps = %v, want %v: the rate must not be rescaled", *decoded.Download.Mbps, mbps)
	}
	if decoded.Upload != nil {
		t.Error("an unmeasured upload must serialize as null")
	}
	if !strings.Contains(string(encoded), `"status":"completed"`) {
		t.Errorf("unexpected wire form: %s", encoded)
	}
}
