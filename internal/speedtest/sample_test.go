package speedtest

import (
	"math"
	"testing"
	"time"
)

func TestSampleRecorderUsesRealElapsedTime(t *testing.T) {
	windowStart := time.Unix(0, 0)
	recorder := &sampleRecorder{phase: PhaseDownload}

	// 1,000,000 bytes arrived 200ms into the window: 40 Mbps.
	sample, ok := recorder.record(windowStart.Add(200*time.Millisecond), 1_000_000, 1, windowStart)
	if !ok {
		t.Fatal("first sample was rejected")
	}
	if math.Abs(sample.CurrentMbps-40) > 1e-9 {
		t.Fatalf("current = %v, want 40 Mbps", sample.CurrentMbps)
	}
	if math.Abs(sample.AverageMbps-40) > 1e-9 {
		t.Fatalf("average = %v, want 40 Mbps", sample.AverageMbps)
	}
	if sample.ActiveConnections != 1 {
		t.Fatalf("active connections = %d, want 1", sample.ActiveConnections)
	}

	// The next observation is 250ms late (not the nominal 200ms): 2,000,000
	// more bytes over the real 250ms means 64 Mbps, and the average uses the
	// real 450ms window.
	sample, ok = recorder.record(windowStart.Add(450*time.Millisecond), 3_000_000, 2, windowStart)
	if !ok {
		t.Fatal("second sample was rejected")
	}
	if math.Abs(sample.CurrentMbps-64) > 1e-9 {
		t.Fatalf("current = %v, want 64 Mbps (real elapsed time)", sample.CurrentMbps)
	}
	wantAverage := 3_000_000.0 * 8 / 0.45 / 1e6
	if math.Abs(sample.AverageMbps-wantAverage) > 1e-9 {
		t.Fatalf("average = %v, want %v", sample.AverageMbps, wantAverage)
	}
	if sample.ElapsedNs != 450*time.Millisecond {
		t.Fatalf("elapsed = %s, want 450ms", sample.ElapsedNs)
	}
	if len(recorder.samples) != 2 {
		t.Fatalf("recorded samples = %d, want 2", len(recorder.samples))
	}

	// A zero interval or a regressing counter must not fabricate a sample.
	if _, ok := recorder.record(windowStart.Add(450*time.Millisecond), 3_000_000, 2, windowStart); ok {
		t.Fatal("a zero interval produced a sample")
	}
	if _, ok := recorder.record(windowStart.Add(500*time.Millisecond), 2_000_000, 2, windowStart); ok {
		t.Fatal("a regressing byte counter produced a sample")
	}
}

func TestSummarizeStatistics(t *testing.T) {
	stats := Summarize([]Sample{{CurrentMbps: 40}, {CurrentMbps: 64}})
	if stats.Samples != 2 {
		t.Fatalf("samples = %d, want 2", stats.Samples)
	}
	assertStat(t, "mean", stats.MeanMbps, 52)
	assertStat(t, "median", stats.MedianMbps, 52)
	assertStat(t, "min", stats.MinMbps, 40)
	assertStat(t, "max", stats.MaxMbps, 64)
	assertStat(t, "stddev", stats.StdDevMbps, math.Sqrt(288))
	assertStat(t, "cv", stats.CoefficientOfVariationPercent, math.Sqrt(288)/52*100)
	if stats.StdDevKind != StdDevKindSample {
		t.Fatalf("stddev kind = %q, want %q", stats.StdDevKind, StdDevKindSample)
	}

	odd := Summarize([]Sample{{CurrentMbps: 10}, {CurrentMbps: 30}, {CurrentMbps: 20}})
	assertStat(t, "odd mean", odd.MeanMbps, 20)
	assertStat(t, "odd median", odd.MedianMbps, 20)
	assertStat(t, "odd stddev", odd.StdDevMbps, 10)
	assertStat(t, "odd cv", odd.CoefficientOfVariationPercent, 50)
}

func TestSummarizeInsufficientSamples(t *testing.T) {
	empty := Summarize(nil)
	if empty.Samples != 0 {
		t.Fatalf("empty samples = %d, want 0", empty.Samples)
	}
	for name, value := range map[string]*float64{
		"mean":   empty.MeanMbps,
		"median": empty.MedianMbps,
		"min":    empty.MinMbps,
		"max":    empty.MaxMbps,
		"stddev": empty.StdDevMbps,
		"cv":     empty.CoefficientOfVariationPercent,
	} {
		if value != nil {
			t.Fatalf("%s must be N/A without samples, got %v", name, *value)
		}
	}
	if empty.StdDevKind != "" {
		t.Fatalf("stddev kind = %q, want empty", empty.StdDevKind)
	}

	single := Summarize([]Sample{{CurrentMbps: 12}})
	assertStat(t, "single mean", single.MeanMbps, 12)
	assertStat(t, "single median", single.MedianMbps, 12)
	if single.StdDevMbps != nil {
		t.Fatalf("stddev must be N/A with one sample, got %v", *single.StdDevMbps)
	}
	if single.CoefficientOfVariationPercent != nil {
		t.Fatalf("cv must be N/A with one sample, got %v", *single.CoefficientOfVariationPercent)
	}
}

func assertStat(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = N/A, want %v", name, want)
	}
	if math.Abs(*got-want) > 1e-6 {
		t.Fatalf("%s = %v, want %v", name, *got, want)
	}
}
