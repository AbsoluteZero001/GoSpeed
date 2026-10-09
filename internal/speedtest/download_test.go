package speedtest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMeasureDownloadByteLimited(t *testing.T) {
	const payloadSize = 1 << 20
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/download" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		block := make([]byte, 32<<10)
		for written := 0; written < payloadSize; {
			chunk := block
			if remaining := payloadSize - written; remaining < len(chunk) {
				chunk = chunk[:remaining]
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			written += len(chunk)
		}
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesDownload,
		Duration: 5 * time.Second,
		Timeout:  10 * time.Second,
		MaxBytes: payloadSize,
	})
	engine := NewEngine(nil)
	result, err := engine.measureDownload(context.Background(), options)
	if err != nil {
		t.Fatalf("measureDownload returned error: %v", err)
	}
	if result.Bytes != payloadSize {
		t.Fatalf("bytes = %d, want %d", result.Bytes, payloadSize)
	}
	if result.StopReason != StopReasonRequestedBytes {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, StopReasonRequestedBytes)
	}
	if result.MeasurementWindow != WindowDownload {
		t.Fatalf("measurement window = %q, want %q", result.MeasurementWindow, WindowDownload)
	}
	if result.DurationNs <= 0 {
		t.Fatalf("duration = %s, want positive", result.DurationNs)
	}
	if result.Mbps <= 0 {
		t.Fatalf("mbps = %v, want positive", result.Mbps)
	}
	if result.Connections != 1 {
		t.Fatalf("connections = %d, want 1", result.Connections)
	}
}

func TestMeasureDownloadDurationLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		block := make([]byte, 32<<10)
		flusher, _ := w.(http.Flusher)
		for {
			if r.Context().Err() != nil {
				return
			}
			if _, err := w.Write(block); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesDownload,
		Duration: 200 * time.Millisecond,
		Timeout:  5 * time.Second,
	})
	engine := NewEngine(nil)
	result, err := engine.measureDownload(context.Background(), options)
	if err != nil {
		t.Fatalf("measureDownload returned error: %v", err)
	}
	if result.StopReason != StopReasonDuration {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, StopReasonDuration)
	}
	if result.Bytes == 0 {
		t.Fatal("duration limited download transferred no data")
	}
	if result.DurationNs < 150*time.Millisecond {
		t.Fatalf("duration = %s, want close to the requested window", result.DurationNs)
	}
}

func TestMeasureDownloadRejectsUnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesDownload,
		Duration: time.Second,
		Timeout:  5 * time.Second,
	})
	engine := NewEngine(nil)
	if _, err := engine.measureDownload(context.Background(), options); !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("error = %v, want ErrUnexpectedStatus", err)
	}
}

func TestMeasureDownloadDetectsTruncatedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "65536")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesDownload,
		Duration: 5 * time.Second,
		Timeout:  10 * time.Second,
		MaxBytes: 65536,
	})
	engine := NewEngine(nil)
	result, err := engine.measureDownload(context.Background(), options)
	if err == nil {
		t.Fatalf("expected an error for a truncated body, got result %+v", result)
	}
	if result != nil {
		t.Fatalf("failed download must not publish a result: %+v", result)
	}
}

func TestMeasureDownloadHonoursCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		block := make([]byte, 32<<10)
		for {
			if r.Context().Err() != nil {
				return
			}
			if _, err := w.Write(block); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesDownload,
		Duration: 25 * time.Second,
		Timeout:  30 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	engine := NewEngine(nil)
	if _, err := engine.measureDownload(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestDownloadEndpointIncludesLimits(t *testing.T) {
	options := Options{
		Target:   Target{ID: "test", BaseURL: "http://127.0.0.1:8080", Protocol: ProtocolHTTP},
		Duration: 3 * time.Second,
		MaxBytes: 4096,
	}
	endpoint, err := downloadEndpoint(options)
	if err != nil {
		t.Fatalf("downloadEndpoint returned error: %v", err)
	}
	if endpoint != "http://127.0.0.1:8080/download?bytes=4096&duration_ms=3000" {
		t.Fatalf("endpoint = %q", endpoint)
	}
}
