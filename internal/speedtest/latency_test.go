package speedtest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMeasureLatencyCollectsSamples(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:          Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:          PhasesLatency,
		LatencySamples:  4,
		LatencyInterval: time.Millisecond,
		Timeout:         5 * time.Second,
	})
	engine := NewEngine(nil)
	latency, err := engine.measureLatency(context.Background(), options)
	if err != nil {
		t.Fatalf("measureLatency returned error: %v", err)
	}
	if latency.Type != LatencyHTTPRTT {
		t.Fatalf("latency type = %q, want %q", latency.Type, LatencyHTTPRTT)
	}
	if latency.Attempts != 4 || latency.SuccessfulSamples != 4 {
		t.Fatalf("attempts/successful = %d/%d, want 4/4", latency.Attempts, latency.SuccessfulSamples)
	}
	if len(latency.SamplesNs) != 4 {
		t.Fatalf("raw samples = %d, want 4", len(latency.SamplesNs))
	}
	// A loopback sample can be smaller than the platform clock resolution and
	// therefore round to zero; the engine reports the raw value instead of
	// inventing a floor, so the test only checks ordering and non-negativity.
	if latency.MinNs < 0 || latency.AverageNs < 0 || latency.MaxNs < 0 {
		t.Fatalf("latency stats must not be negative: min=%s avg=%s max=%s", latency.MinNs, latency.AverageNs, latency.MaxNs)
	}
	if latency.MinNs > latency.AverageNs || latency.AverageNs > latency.MaxNs {
		t.Fatalf("latency stats out of order: min=%s avg=%s max=%s", latency.MinNs, latency.AverageNs, latency.MaxNs)
	}
	if latency.JitterNs == nil {
		t.Fatal("jitter must be present with more than one sample")
	}
	if latency.PacketLossPercent != nil {
		t.Fatalf("packet loss must stay null without a packet level test, got %v", *latency.PacketLossPercent)
	}
}

func TestMeasureLatencyRecordsPartialFailures(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:          Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:          PhasesLatency,
		LatencySamples:  3,
		LatencyInterval: time.Millisecond,
		Timeout:         5 * time.Second,
	})
	engine := NewEngine(nil)
	latency, err := engine.measureLatency(context.Background(), options)
	if err != nil {
		t.Fatalf("measureLatency returned error: %v", err)
	}
	if latency.FailedSamples != 1 {
		t.Fatalf("failed samples = %d, want 1", latency.FailedSamples)
	}
	if latency.SuccessfulSamples != 2 {
		t.Fatalf("successful samples = %d, want 2", latency.SuccessfulSamples)
	}
	if len(latency.Errors) != 1 {
		t.Fatalf("recorded errors = %d, want 1", len(latency.Errors))
	}
	if !strings.Contains(latency.Errors[0], ErrUnexpectedStatus.Error()) {
		t.Fatalf("recorded error %q should mention the unexpected status", latency.Errors[0])
	}
}

func TestMeasureLatencyFailsWhenEverySampleFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:          Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:          PhasesLatency,
		LatencySamples:  2,
		LatencyInterval: time.Millisecond,
		Timeout:         5 * time.Second,
	})
	engine := NewEngine(nil)
	latency, err := engine.measureLatency(context.Background(), options)
	if !errors.Is(err, ErrAllSamplesFailed) {
		t.Fatalf("error = %v, want ErrAllSamplesFailed", err)
	}
	if latency == nil || latency.SuccessfulSamples != 0 || latency.FailedSamples != 2 {
		t.Fatalf("unexpected latency result: %+v", latency)
	}
}

func TestMeasureLatencyHonoursCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:          Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:          PhasesLatency,
		LatencySamples:  2,
		LatencyInterval: time.Millisecond,
		Timeout:         5 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	engine := NewEngine(nil)
	if _, err := engine.measureLatency(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestLatencyStatistics(t *testing.T) {
	samples := []time.Duration{10 * time.Millisecond, 12 * time.Millisecond, 9 * time.Millisecond}
	min, max, average := latencyStats(samples)
	if min != 9*time.Millisecond || max != 12*time.Millisecond {
		t.Fatalf("min/max = %s/%s", min, max)
	}
	if average != 31*time.Millisecond/3 {
		t.Fatalf("average = %s, want %s", average, 31*time.Millisecond/3)
	}
	jitter, ok := jitterStats(samples)
	if !ok {
		t.Fatal("jitterStats reported no jitter for three samples")
	}
	if jitter != 2500*time.Microsecond {
		t.Fatalf("jitter = %s, want 2.5ms", jitter)
	}
	if _, ok := jitterStats(samples[:1]); ok {
		t.Fatal("jitterStats must report false for a single sample")
	}
}
