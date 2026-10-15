package speedtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/server"
)

func TestEngineRunEndToEnd(t *testing.T) {
	testServer, err := server.New(server.Config{
		MaxUploadBytes:          64 << 20,
		MaxDownloadBytes:        64 << 20,
		MaxDownloadDuration:     5 * time.Second,
		DefaultDownloadDuration: time.Second,
		WriteTimeout:            10 * time.Second,
	})
	if err != nil {
		t.Fatalf("server.New returned error: %v", err)
	}
	httpServer := httptest.NewServer(testServer.Handler())
	defer httpServer.Close()

	var mu sync.Mutex
	seen := map[Phase]bool{}
	const payloadSize = 16 << 20
	options := Options{
		Target: Target{
			ID:       "local",
			Name:     "Local Test Server",
			BaseURL:  httpServer.URL,
			Protocol: ProtocolHTTP,
			Local:    true,
		},
		Duration:        2 * time.Second,
		Timeout:         10 * time.Second,
		MaxBytes:        payloadSize,
		LatencySamples:  3,
		LatencyInterval: 10 * time.Millisecond,
		Warmup:          true,
		Progress: func(event Progress) {
			mu.Lock()
			seen[event.Phase] = true
			mu.Unlock()
		},
	}
	engine := NewEngine(nil)
	result, err := engine.Run(context.Background(), options)
	if err != nil {
		t.Fatalf("Engine.Run returned error: %v (%s)", err, result.ErrorMessage)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want %q", result.Status, StatusCompleted)
	}
	if result.ErrorMessage != "" {
		t.Fatalf("unexpected error message: %q", result.ErrorMessage)
	}
	if !result.Target.Local {
		t.Fatal("local loopback target must be recorded in the result")
	}
	if result.Latency == nil || result.Latency.SuccessfulSamples != 3 {
		t.Fatalf("unexpected latency result: %+v", result.Latency)
	}
	if result.Download == nil || result.Download.Bytes != payloadSize {
		t.Fatalf("unexpected download result: %+v", result.Download)
	}
	if result.Download.ActiveConnections != 1 || result.Download.FailedConnections != 0 {
		t.Fatalf("single connection download active/failed = %d/%d", result.Download.ActiveConnections, result.Download.FailedConnections)
	}
	if result.Download.MeasurementWindow != WindowDownload {
		t.Fatalf("download window = %q, want %q", result.Download.MeasurementWindow, WindowDownload)
	}
	if result.Upload == nil || result.Upload.Bytes != payloadSize {
		t.Fatalf("unexpected upload result: %+v", result.Upload)
	}
	if result.Upload.ServerConfirmedBytes != result.Upload.Bytes {
		t.Fatalf("upload byte mismatch: client=%d server=%d",
			result.Upload.Bytes, result.Upload.ServerConfirmedBytes)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("clean run must not warn: %v", result.Warnings)
	}
	if len(result.Settings.Phases) != 3 {
		t.Fatalf("recorded phases = %v, want latency, download and upload", result.Settings.Phases)
	}
	if result.Settings.SampleInterval != DefaultSampleInterval {
		t.Fatalf("sample interval = %s, want the default %s", result.Settings.SampleInterval, DefaultSampleInterval)
	}
	for _, phase := range []Phase{PhaseLatency, PhaseDownload, PhaseUpload} {
		if !seen[phase] {
			t.Fatalf("phase %q never reported progress", phase)
		}
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	var decoded Result
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	if decoded.TestID != result.TestID || decoded.Download == nil || decoded.Upload == nil {
		t.Fatalf("json round trip lost data: %s", raw)
	}
	if !strings.Contains(string(raw), "statistics") || !strings.Contains(string(raw), "active_connections") {
		t.Fatalf("json result must expose the v0.2.0 fields: %s", raw)
	}
}

func TestEngineRunEndToEndMultiConnection(t *testing.T) {
	// The 64 MiB advertised ceiling is far above the 16 MiB caller budget,
	// so the server ceiling is never what ends either phase. The 1 ms
	// chunkDelay paces the /download stream and the 50 ms ackDelay paces
	// the /upload confirmation. Both delays sit inside the measurement
	// window the client observes (after the first byte has been received
	// for download; after the body has been drained for upload), so the
	// transfer is resolvable on coarse Windows CI clocks without changing
	// the engine semantics the test guards. The byte budget and duration
	// limits the client sends through query parameters are honoured exactly
	// like the production server.
	httpServer := pacedMultiServer(t, 64<<20, 1*time.Millisecond, 50*time.Millisecond)
	defer httpServer.Close()

	const payloadSize = 16 << 20
	options := Options{
		Target: Target{
			ID:       "local",
			Name:     "Local Test Server",
			BaseURL:  httpServer.URL,
			Protocol: ProtocolHTTP,
			Local:    true,
		},
		Duration:        3 * time.Second,
		Timeout:         15 * time.Second,
		MaxBytes:        payloadSize,
		Connections:     4,
		SampleInterval:  50 * time.Millisecond,
		LatencySamples:  2,
		LatencyInterval: 5 * time.Millisecond,
		Warmup:          true,
	}
	result, err := NewEngine(nil).Run(context.Background(), options)
	if err != nil {
		t.Fatalf("Engine.Run returned error: %v (%s)", err, result.ErrorMessage)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want %q", result.Status, StatusCompleted)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", result.Warnings)
	}
	if result.Download == nil || result.Upload == nil {
		t.Fatalf("multi connection run must produce both transfers: %+v", result)
	}
	if result.Download.Bytes != payloadSize || result.Download.ActiveConnections != 4 || result.Download.FailedConnections != 0 {
		t.Fatalf("unexpected download result: %+v", result.Download)
	}
	if result.Download.MeasurementWindow != WindowDownloadMulti {
		t.Fatalf("download window = %q, want %q", result.Download.MeasurementWindow, WindowDownloadMulti)
	}
	if result.Upload.Bytes != payloadSize || result.Upload.ServerConfirmedBytes != payloadSize {
		t.Fatalf("unexpected upload result: %+v", result.Upload)
	}
	if result.Upload.MeasurementWindow != WindowUploadMulti {
		t.Fatalf("upload window = %q, want %q", result.Upload.MeasurementWindow, WindowUploadMulti)
	}
	if len(result.Upload.ConnectionReports) != 4 || len(result.Download.ConnectionReports) != 4 {
		t.Fatalf("every connection must be reported: download=%d upload=%d",
			len(result.Download.ConnectionReports), len(result.Upload.ConnectionReports))
	}
	if result.Settings.Connections != 4 || result.Settings.SampleInterval != 50*time.Millisecond {
		t.Fatalf("settings snapshot = %+v", result.Settings)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	if !strings.Contains(string(raw), "\"active_connections\":4") {
		t.Fatalf("json must record active connections: %s", raw)
	}
}

func TestEngineRunHonoursCancellation(t *testing.T) {
	testServer, err := server.New(server.Config{
		MaxDownloadBytes:        64 << 20,
		MaxDownloadDuration:     30 * time.Second,
		DefaultDownloadDuration: 30 * time.Second,
		WriteTimeout:            40 * time.Second,
	})
	if err != nil {
		t.Fatalf("server.New returned error: %v", err)
	}
	httpServer := httptest.NewServer(testServer.Handler())
	defer httpServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	options := Options{
		Target:   Target{ID: "local", Name: "Local Test Server", BaseURL: httpServer.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesDownload,
		Duration: 25 * time.Second,
		Timeout:  30 * time.Second,
	}
	result, err := NewEngine(nil).Run(ctx, options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if result.Status != StatusCancelled {
		t.Fatalf("status = %q, want %q", result.Status, StatusCancelled)
	}
	if result.Download != nil {
		t.Fatalf("cancelled download must not publish a rate: %+v", result.Download)
	}
}

func TestEngineRunRejectsInvalidOptions(t *testing.T) {
	validTarget := Target{ID: "test", Name: "test", BaseURL: "http://127.0.0.1:8080", Protocol: ProtocolHTTP}
	cases := []struct {
		name    string
		options Options
		want    error
	}{
		{
			name:    "too many connections",
			options: Options{Target: validTarget, Connections: MaxConnections + 1},
			want:    ErrUnsupportedConnections,
		},
		{
			name:    "timeout shorter than duration",
			options: Options{Target: validTarget, Duration: 10 * time.Second, Timeout: 5 * time.Second},
			want:    ErrInvalidOptions,
		},
		{
			name:    "missing base url",
			options: Options{Target: Target{ID: "test"}},
			want:    ErrInvalidTarget,
		},
		{
			name:    "negative max bytes",
			options: Options{Target: validTarget, MaxBytes: -1},
			want:    ErrInvalidOptions,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewEngine(nil).Run(context.Background(), testCase.options); !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

// pacedMultiServer builds a GoSpeed-like test server that paces both
// the download chunks and the upload acknowledgement. The /download
// handler emits each chunk with chunkDelay between writes (and flushes
// after every chunk so the client can observe the gap), and the
// /upload handler sleeps ackDelay between draining the body and sending
// the acknowledgement.
//
// Both delays sit inside the measurement window the client observes:
// the download delay runs after the first byte has been received, so the
// window keeps spanning real chunks; the upload delay runs after the
// client has finished writing the body, so the window spans the
// confirmation round trip plus the sleep. A run that the platform clock
// cannot otherwise resolve on coarse Windows CI hardware therefore
// becomes deterministic.
//
// The byte budget and duration limits the client sends through query
// parameters are honoured exactly like the production server, so byte
// limited and duration limited engine semantics are unchanged.
func pacedMultiServer(t *testing.T, advertisedCeiling int64, chunkDelay, ackDelay time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"gospeed","version":"0.3.0"}`))
	})
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/capabilities", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": 1,
			"server_version":   "0.3.0",
			"capabilities":     []string{"latency", "download", "upload"},
			"limits": map[string]any{
				"max_connections_per_test": 16,
				"max_duration_seconds":     60,
				"max_download_bytes":       advertisedCeiling,
				"max_upload_bytes":         advertisedCeiling,
			},
		})
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		var byteLimit int64
		var durationLimit time.Duration
		if raw := query.Get("bytes"); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				http.Error(w, "bytes must be a positive integer", http.StatusBadRequest)
				return
			}
			byteLimit = value
		}
		if raw := query.Get("duration_ms"); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				http.Error(w, "duration_ms must be a positive integer", http.StatusBadRequest)
				return
			}
			durationLimit = time.Duration(value) * time.Millisecond
		}
		block := make([]byte, 32<<10)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		start := time.Now()
		var sent int64
		for {
			if r.Context().Err() != nil {
				return
			}
			if byteLimit > 0 && sent >= byteLimit {
				return
			}
			if durationLimit > 0 && time.Since(start) >= durationLimit {
				return
			}
			chunk := block
			if byteLimit > 0 {
				if remaining := byteLimit - sent; remaining < int64(len(chunk)) {
					chunk = chunk[:remaining]
				}
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			sent += int64(len(chunk))
			if flusher != nil {
				flusher.Flush()
			}
			if chunkDelay <= 0 {
				continue
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(chunkDelay):
			}
		}
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		received, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// The delay sits between draining the body and sending the
		// acknowledgement, so the client's measurement window spans the
		// full delay plus the network round trip. Real production runs
		// depend on real network latency, not on this artificial delay.
		if ackDelay > 0 {
			time.Sleep(ackDelay)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"bytes_received": received,
			"duration_ns":    int64(1),
		})
	})
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	return httpServer
}
