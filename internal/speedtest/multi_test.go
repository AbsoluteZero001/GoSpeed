package speedtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// streamHandler serves a download stream until the client goes away. failFirst
// requests answer with HTTP 500 instead, which lets a test simulate partial
// connection failures deterministically. delay throttles each chunk so short
// tests still produce several samples without moving gigabytes.
func streamHandler(requests *atomic.Int64, failFirst int64, delay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		index := requests.Add(1)
		if index <= failFirst {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		block := make([]byte, 32<<10)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
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
			if delay <= 0 {
				continue
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(delay):
			}
		}
	}
}

// confirmingUploadHandler counts the bytes it received and confirms them,
// optionally mutating the confirmed count to simulate a broken server.
func confirmingUploadHandler(requests *atomic.Int64, mutate func(index int64, received int64) int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		index := requests.Add(1)
		received, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		confirmed := received
		if mutate != nil {
			confirmed = mutate(index, received)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"bytes_received": confirmed,
			"duration_ns":    int64(1),
		})
	}
}

func multiOptions(t *testing.T, url string, connections int, duration, timeout time.Duration, maxBytes int64) Options {
	t.Helper()
	return normalizedOptions(t, Options{
		Target:         Target{ID: "test", Name: "test", BaseURL: url, Protocol: ProtocolHTTP},
		Phases:         PhasesDownload,
		Duration:       duration,
		Timeout:        timeout,
		Connections:    connections,
		MaxBytes:       maxBytes,
		SampleInterval: 50 * time.Millisecond,
	})
}

func TestMeasureDownloadMultiConnectionByteBudget(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(streamHandler(&requests, 0, 0))
	defer server.Close()

	const payloadSize = 4 << 20
	options := multiOptions(t, server.URL, 4, 5*time.Second, 10*time.Second, payloadSize)
	result, err := NewEngine(nil).measureDownload(context.Background(), options)
	if err != nil {
		t.Fatalf("measureDownload returned error: %v", err)
	}
	if result.Bytes != payloadSize {
		t.Fatalf("bytes = %d, want %d", result.Bytes, payloadSize)
	}
	if result.ActiveConnections != 4 || result.FailedConnections != 0 {
		t.Fatalf("active/failed = %d/%d, want 4/0", result.ActiveConnections, result.FailedConnections)
	}
	if result.Connections != 4 {
		t.Fatalf("requested connections = %d, want 4", result.Connections)
	}
	if got := int64(requests.Load()); got != 4 {
		t.Fatalf("server saw %d requests, want one per connection (4)", got)
	}
	if result.MeasurementWindow != WindowDownloadMulti {
		t.Fatalf("window = %q, want %q", result.MeasurementWindow, WindowDownloadMulti)
	}
	if result.StopReason != StopReasonRequestedBytes {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, StopReasonRequestedBytes)
	}
	if result.Mbps <= 0 {
		t.Fatalf("mbps = %v, want positive", result.Mbps)
	}
	if sum := sumReportBytes(result.ConnectionReports, false); sum != result.Bytes {
		t.Fatalf("connection reports sum to %d, want %d (no double counting)", sum, result.Bytes)
	}
	for index, report := range result.ConnectionReports {
		if report.Index != index {
			t.Fatalf("connection report %d has index %d", index, report.Index)
		}
	}
}

func TestMeasureDownloadMultiConnectionDuration(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(streamHandler(&requests, 0, 2*time.Millisecond))
	defer server.Close()

	options := multiOptions(t, server.URL, 4, 300*time.Millisecond, 5*time.Second, 0)
	result, err := NewEngine(nil).measureDownload(context.Background(), options)
	if err != nil {
		t.Fatalf("measureDownload returned error: %v", err)
	}
	if result.StopReason != StopReasonDuration {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, StopReasonDuration)
	}
	if result.Bytes == 0 {
		t.Fatal("duration limited multi connection download transferred no data")
	}
	if result.ActiveConnections != 4 {
		t.Fatalf("active connections = %d, want 4", result.ActiveConnections)
	}
	if len(result.Samples) < 2 {
		t.Fatalf("samples = %d, want at least 2", len(result.Samples))
	}
	if result.Statistics.MeanMbps == nil || result.Statistics.MedianMbps == nil || result.Statistics.StdDevMbps == nil {
		t.Fatalf("statistics must be computed from real samples: %+v", result.Statistics)
	}
	if sum := sumReportBytes(result.ConnectionReports, false); sum != result.Bytes {
		t.Fatalf("connection reports sum to %d, want %d", sum, result.Bytes)
	}
	for _, report := range result.ConnectionReports {
		if report.State != ConnectionCompleted {
			t.Fatalf("connection %d state = %q, want completed", report.Index, report.State)
		}
	}
}

func TestMeasureDownloadPartialConnectionFailure(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(streamHandler(&requests, 2, 0))
	defer server.Close()

	const payloadSize = 4 << 20
	options := multiOptions(t, server.URL, 4, 5*time.Second, 10*time.Second, payloadSize)
	result, err := NewEngine(nil).measureDownload(context.Background(), options)
	if err != nil {
		t.Fatalf("a partial failure must still produce a result: %v", err)
	}
	if result.FailedConnections != 2 || result.ActiveConnections != 2 {
		t.Fatalf("active/failed = %d/%d, want 2/2", result.ActiveConnections, result.FailedConnections)
	}
	if result.Bytes != payloadSize {
		t.Fatalf("bytes = %d, want the full shared budget %d", result.Bytes, payloadSize)
	}
	warnings := transferWarnings(PhaseDownload, result)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one partial failure warning", warnings)
	}
	failedReports := 0
	for _, report := range result.ConnectionReports {
		if report.State == ConnectionFailed {
			failedReports++
			if report.Error == "" {
				t.Fatalf("failed connection %d has no error text", report.Index)
			}
		}
	}
	if failedReports != 2 {
		t.Fatalf("failed reports = %d, want 2", failedReports)
	}
}

func TestMeasureDownloadAllConnectionsFail(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(streamHandler(&requests, 99, 0))
	defer server.Close()

	options := multiOptions(t, server.URL, 4, 5*time.Second, 10*time.Second, 1<<20)
	_, err := NewEngine(nil).measureDownload(context.Background(), options)
	if !errors.Is(err, ErrAllConnectionsFailed) {
		t.Fatalf("error = %v, want ErrAllConnectionsFailed", err)
	}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("error = %v, want the underlying ErrUnexpectedStatus", err)
	}
}

func TestMeasureUploadMultiConnection(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(confirmingUploadHandler(&requests, nil))
	defer server.Close()

	const payloadSize = 2 << 20
	options := multiOptions(t, server.URL, 4, 5*time.Second, 10*time.Second, payloadSize)
	options.Phases = PhasesUpload
	result, err := NewEngine(nil).measureUpload(context.Background(), options)
	if err != nil {
		t.Fatalf("measureUpload returned error: %v", err)
	}
	if result.Bytes != payloadSize || result.ServerConfirmedBytes != payloadSize {
		t.Fatalf("bytes = %d, server confirmed = %d, want %d",
			result.Bytes, result.ServerConfirmedBytes, payloadSize)
	}
	if result.ActiveConnections != 4 || result.FailedConnections != 0 {
		t.Fatalf("active/failed = %d/%d, want 4/0", result.ActiveConnections, result.FailedConnections)
	}
	if got := int64(requests.Load()); got != 4 {
		t.Fatalf("server saw %d uploads, want one per connection (4)", got)
	}
	if result.MeasurementWindow != WindowUploadMulti {
		t.Fatalf("window = %q, want %q", result.MeasurementWindow, WindowUploadMulti)
	}
	if result.StopReason != StopReasonRequestedBytes {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, StopReasonRequestedBytes)
	}
	if sum := sumReportBytes(result.ConnectionReports, true); sum != result.ServerConfirmedBytes {
		t.Fatalf("per connection confirmations sum to %d, want %d", sum, result.ServerConfirmedBytes)
	}
	for index, report := range result.ConnectionReports {
		if report.Index != index {
			t.Fatalf("connection report %d has index %d", index, report.Index)
		}
	}
}

func TestMeasureUploadMultiConnectionDuration(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		received, err := io.Copy(io.Discard, &slowDrain{reader: r.Body, delay: time.Millisecond, chunk: 32 << 10})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"bytes_received": received,
			"duration_ns":    int64(1),
		})
	}))
	defer server.Close()

	options := multiOptions(t, server.URL, 4, 200*time.Millisecond, 5*time.Second, 0)
	options.Phases = PhasesUpload
	result, err := NewEngine(nil).measureUpload(context.Background(), options)
	if err != nil {
		t.Fatalf("measureUpload returned error: %v", err)
	}
	if result.Bytes == 0 || result.Bytes != result.ServerConfirmedBytes {
		t.Fatalf("bytes = %d, server confirmed = %d", result.Bytes, result.ServerConfirmedBytes)
	}
	if result.StopReason != StopReasonDuration {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, StopReasonDuration)
	}
	if result.ActiveConnections != 4 {
		t.Fatalf("active connections = %d, want 4", result.ActiveConnections)
	}
	if len(result.Samples) < 2 {
		t.Fatalf("samples = %d, want at least 2", len(result.Samples))
	}
}

func TestMeasureUploadMultiConnectionMismatch(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(confirmingUploadHandler(&requests, func(index int64, received int64) int64 {
		if index == 2 {
			return received - 1
		}
		return received
	}))
	defer server.Close()

	options := multiOptions(t, server.URL, 4, 5*time.Second, 10*time.Second, 2<<20)
	options.Phases = PhasesUpload
	_, err := NewEngine(nil).measureUpload(context.Background(), options)
	if !errors.Is(err, ErrByteCountMismatch) {
		t.Fatalf("error = %v, want ErrByteCountMismatch", err)
	}
}

func TestMeasureUploadAllConnectionsFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"too large"}`, http.StatusRequestEntityTooLarge)
	}))
	defer server.Close()

	options := multiOptions(t, server.URL, 4, 5*time.Second, 10*time.Second, 1<<20)
	options.Phases = PhasesUpload
	_, err := NewEngine(nil).measureUpload(context.Background(), options)
	if !errors.Is(err, ErrAllConnectionsFailed) {
		t.Fatalf("error = %v, want ErrAllConnectionsFailed", err)
	}
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("error = %v, want the underlying ErrUnexpectedStatus", err)
	}
}

// slowDrain backpressures an upload so a duration limited multi connection test
// stays small enough for CI while still exercising the real data path.
type slowDrain struct {
	reader io.Reader
	delay  time.Duration
	chunk  int
}

func (d *slowDrain) Read(p []byte) (int, error) {
	if d.chunk > 0 && len(p) > d.chunk {
		p = p[:d.chunk]
	}
	n, err := d.reader.Read(p)
	if n > 0 && d.delay > 0 {
		time.Sleep(d.delay)
	}
	return n, err
}

func sumReportBytes(reports []ConnectionReport, useServerBytes bool) int64 {
	var total int64
	for _, report := range reports {
		if useServerBytes && report.ServerConfirmedBytes != nil {
			total += *report.ServerConfirmedBytes
			continue
		}
		total += report.Bytes
	}
	return total
}
