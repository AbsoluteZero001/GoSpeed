package speedtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMeasureUploadConfirmsByteCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upload" {
			http.NotFound(w, r)
			return
		}
		start := time.Now()
		received, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"bytes_received": received,
			"duration_ns":    time.Since(start).Nanoseconds(),
		})
	}))
	defer server.Close()

	const payloadSize = 2 << 20
	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesUpload,
		Duration: 5 * time.Second,
		Timeout:  10 * time.Second,
		MaxBytes: payloadSize,
	})
	engine := NewEngine(nil)
	result, err := engine.measureUpload(context.Background(), options)
	if err != nil {
		t.Fatalf("measureUpload returned error: %v", err)
	}
	if result.Bytes != payloadSize {
		t.Fatalf("client bytes = %d, want %d", result.Bytes, payloadSize)
	}
	if result.ServerConfirmedBytes != payloadSize {
		t.Fatalf("server confirmed bytes = %d, want %d", result.ServerConfirmedBytes, payloadSize)
	}
	if result.DurationNs < 0 || result.ServerDurationNs < 0 {
		t.Fatalf("durations must not be negative: client=%s server=%s", result.DurationNs, result.ServerDurationNs)
	}
	if result.Mbps <= 0 {
		t.Fatalf("mbps = %v, want positive (a zero length window must fail instead)", result.Mbps)
	}
	if result.MeasurementWindow != WindowUpload {
		t.Fatalf("measurement window = %q, want %q", result.MeasurementWindow, WindowUpload)
	}
	if result.StopReason != StopReasonRequestedBytes {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, StopReasonRequestedBytes)
	}
}

func TestMeasureUploadDurationLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"bytes_received": received,
			"duration_ns":    1,
		})
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesUpload,
		Duration: 100 * time.Millisecond,
		Timeout:  5 * time.Second,
	})
	engine := NewEngine(nil)
	result, err := engine.measureUpload(context.Background(), options)
	if err != nil {
		t.Fatalf("measureUpload returned error: %v", err)
	}
	if result.Bytes == 0 {
		t.Fatal("duration limited upload transferred no data")
	}
	if result.Bytes != result.ServerConfirmedBytes {
		t.Fatalf("client bytes %d != server bytes %d", result.Bytes, result.ServerConfirmedBytes)
	}
	if result.StopReason != StopReasonDuration {
		t.Fatalf("stop reason = %q, want %q", result.StopReason, StopReasonDuration)
	}
}

func TestMeasureUploadDetectsByteCountMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ := io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":         "ok",
			"bytes_received": received - 1,
			"duration_ns":    1,
		})
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesUpload,
		Duration: 5 * time.Second,
		Timeout:  10 * time.Second,
		MaxBytes: 512 << 10,
	})
	engine := NewEngine(nil)
	if _, err := engine.measureUpload(context.Background(), options); !errors.Is(err, ErrByteCountMismatch) {
		t.Fatalf("error = %v, want ErrByteCountMismatch", err)
	}
}

func TestMeasureUploadRejectsServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"too large"}`, http.StatusRequestEntityTooLarge)
	}))
	defer server.Close()

	options := normalizedOptions(t, Options{
		Target:   Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:   PhasesUpload,
		Duration: 5 * time.Second,
		Timeout:  10 * time.Second,
		MaxBytes: 64 << 10,
	})
	engine := NewEngine(nil)
	_, err := engine.measureUpload(context.Background(), options)
	if !errors.Is(err, ErrUnexpectedStatus) {
		t.Fatalf("error = %v, want ErrUnexpectedStatus", err)
	}
}

func TestUploadReaderStopsAtByteLimit(t *testing.T) {
	block := make([]byte, 1024)
	for index := range block {
		block[index] = byte(index)
	}
	reader := &uploadReader{block: block, budget: newSharedBudget(1500)}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("io.ReadAll returned error: %v", err)
	}
	if len(data) != 1500 {
		t.Fatalf("read %d bytes, want 1500", len(data))
	}
	if !bytes.Equal(data[:1024], block) {
		t.Fatal("first block does not match the payload block")
	}
	if !bytes.Equal(data[1024:], block[:476]) {
		t.Fatal("wrapped block does not continue from the start of the payload")
	}
	if reader.written.Load() != 1500 {
		t.Fatalf("written = %d, want 1500", reader.written.Load())
	}
}
