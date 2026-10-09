package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCapabilitiesEndpoint(t *testing.T) {
	httpServer := newTestServer(t, Config{
		MaxUploadBytes:        2 << 20,
		MaxDownloadBytes:      4 << 20,
		MaxDownloadDuration:   30 * time.Second,
		MaxConcurrentTests:    8,
		MaxConnectionsPerTest: 16,
		WriteTimeout:          60 * time.Second,
		ReadTimeout:           30 * time.Second,
		Version:               "0.3.0",
	})
	response, err := http.Get(httpServer.URL + "/capabilities")
	if err != nil {
		t.Fatalf("GET /capabilities: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var payload capabilitiesResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if payload.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", payload.ProtocolVersion, ProtocolVersion)
	}
	if payload.ServerVersion != "0.3.0" {
		t.Fatalf("server version = %q", payload.ServerVersion)
	}
	for _, capability := range []string{"latency", "download", "upload"} {
		if !strings.Contains(strings.Join(payload.Capabilities, ","), capability) {
			t.Fatalf("capabilities = %v, missing %s", payload.Capabilities, capability)
		}
	}
	if payload.Limits.MaxConnectionsPerTest != 16 || payload.Limits.MaxConcurrentTests != 8 {
		t.Fatalf("connection limits = %+v", payload.Limits)
	}
	if payload.Limits.MaxDurationSeconds != 30 {
		t.Fatalf("max duration = %d, want 30", payload.Limits.MaxDurationSeconds)
	}
	if payload.Limits.MaxDownloadBytes != 4<<20 || payload.Limits.MaxUploadBytes != 2<<20 {
		t.Fatalf("byte limits = %+v", payload.Limits)
	}

	post, err := http.Post(httpServer.URL+"/capabilities", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /capabilities: %v", err)
	}
	defer post.Body.Close()
	if post.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", post.StatusCode)
	}
}

func TestConcurrentTestLimitReturns503(t *testing.T) {
	httpServer := newTestServer(t, Config{
		MaxConcurrentTests:  1,
		MaxDownloadBytes:    1 << 20,
		MaxDownloadDuration: 5 * time.Second,
		ReadTimeout:         10 * time.Second,
		WriteTimeout:        10 * time.Second,
	})

	first, err := http.Get(httpServer.URL + "/download?duration_ms=1000")
	if err != nil {
		t.Fatalf("first download: %v", err)
	}
	defer first.Body.Close()
	// Reading one byte proves the handler owns the only slot and is streaming.
	if _, err := io.ReadFull(first.Body, make([]byte, 1)); err != nil {
		t.Fatalf("first download did not start: %v", err)
	}

	// Control endpoints must stay reachable while the server is busy.
	health, err := http.Get(httpServer.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health while busy: %v", err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health status while busy = %d, want 200", health.StatusCode)
	}

	second, err := http.Get(httpServer.URL + "/download?bytes=1024")
	if err != nil {
		t.Fatalf("second download: %v", err)
	}
	defer second.Body.Close()
	if second.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second status = %d, want 503", second.StatusCode)
	}
	if retryAfter := second.Header.Get("Retry-After"); retryAfter == "" {
		t.Fatal("503 must carry a Retry-After header")
	}

	// Draining the first response returns the slot.
	if _, err := io.Copy(io.Discard, first.Body); err != nil {
		t.Fatalf("drain first download: %v", err)
	}
	third, err := http.Get(httpServer.URL + "/download?bytes=1024")
	if err != nil {
		t.Fatalf("third download: %v", err)
	}
	defer third.Body.Close()
	if third.StatusCode != http.StatusOK {
		t.Fatalf("third status = %d, want 200 after the slot was released", third.StatusCode)
	}
}
