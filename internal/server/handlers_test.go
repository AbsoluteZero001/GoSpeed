package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer
}

func TestHealthEndpoint(t *testing.T) {
	httpServer := newTestServer(t, Config{})
	response, err := http.Get(httpServer.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var health healthResponse
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if health.Status != "ok" || health.Service != ServiceName || health.Version == "" {
		t.Fatalf("unexpected health payload: %+v", health)
	}
}

func TestPingEndpoint(t *testing.T) {
	httpServer := newTestServer(t, Config{})
	response, err := http.Get(httpServer.URL + "/ping")
	if err != nil {
		t.Fatalf("GET /ping: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.StatusCode)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	httpServer := newTestServer(t, Config{})
	response, err := http.Post(httpServer.URL+"/health", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST /health: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.StatusCode)
	}
	if allow := response.Header.Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
}

func TestDownloadByteLimited(t *testing.T) {
	httpServer := newTestServer(t, Config{
		MaxDownloadBytes:    1 << 20,
		MaxDownloadDuration: 5 * time.Second,
		WriteTimeout:        10 * time.Second,
	})
	response, err := http.Get(httpServer.URL + "/download?bytes=65536")
	if err != nil {
		t.Fatalf("GET /download: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Content-Length"); got != "65536" {
		t.Fatalf("Content-Length = %q, want 65536", got)
	}
	if got := response.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) != 65536 {
		t.Fatalf("body length = %d, want 65536", len(body))
	}
}

func TestDownloadDurationLimited(t *testing.T) {
	httpServer := newTestServer(t, Config{
		MaxDownloadBytes:    1 << 30,
		MaxDownloadDuration: 5 * time.Second,
		WriteTimeout:        10 * time.Second,
	})
	response, err := http.Get(httpServer.URL + "/download?duration_ms=100")
	if err != nil {
		t.Fatalf("GET /download: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("duration limited download returned no data")
	}
}

func TestDownloadRejectsInvalidParameters(t *testing.T) {
	httpServer := newTestServer(t, Config{
		MaxDownloadBytes:    1 << 20,
		MaxDownloadDuration: time.Second,
		WriteTimeout:        10 * time.Second,
	})
	cases := []string{
		"/download?bytes=0",
		"/download?bytes=abc",
		"/download?bytes=2000000",
		"/download?duration_ms=0",
		"/download?duration_ms=abc",
		"/download?duration_ms=5000",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			response, err := http.Get(httpServer.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.StatusCode)
			}
		})
	}
}

func TestUploadCountsBytes(t *testing.T) {
	httpServer := newTestServer(t, Config{MaxUploadBytes: 1 << 20})
	payload := bytes.Repeat([]byte("gospeed"), 4096)
	response, err := http.Post(httpServer.URL+"/upload", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST /upload: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var acknowledgement uploadResponse
	if err := json.NewDecoder(response.Body).Decode(&acknowledgement); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if acknowledgement.Status != "ok" {
		t.Fatalf("status field = %q, want ok", acknowledgement.Status)
	}
	if acknowledgement.BytesReceived != int64(len(payload)) {
		t.Fatalf("bytes_received = %d, want %d", acknowledgement.BytesReceived, len(payload))
	}
	// On Windows the monotonic clock can resolve a very small loopback upload
	// to zero nanoseconds; the counter is still the raw measurement.
	if acknowledgement.DurationNs < 0 {
		t.Fatalf("duration_ns = %d, want non-negative", acknowledgement.DurationNs)
	}
}

func TestUploadRejectsOversizedRequests(t *testing.T) {
	httpServer := newTestServer(t, Config{MaxUploadBytes: 1024})
	payload := bytes.Repeat([]byte("a"), 4096)

	t.Run("known length", func(t *testing.T) {
		response, err := http.Post(httpServer.URL+"/upload", "application/octet-stream", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("POST /upload: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", response.StatusCode)
		}
	})

	t.Run("chunked", func(t *testing.T) {
		request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/upload",
			io.LimitReader(bytes.NewReader(payload), int64(len(payload))))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("POST /upload: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", response.StatusCode)
		}
	})
}

func TestUnknownPathReturnsNotFound(t *testing.T) {
	httpServer := newTestServer(t, Config{})
	response, err := http.Get(httpServer.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

func TestNewAppliesDefaultsAndValidates(t *testing.T) {
	srv, err := New(Config{})
	if err != nil {
		t.Fatalf("New(Config{}) returned error: %v", err)
	}
	if srv.Config().Addr != "127.0.0.1:8080" {
		t.Fatalf("addr = %q, want the loopback default", srv.Config().Addr)
	}
	if srv.Config().MaxUploadBytes <= 0 || srv.Config().MaxDownloadBytes <= 0 {
		t.Fatal("defaults must bound upload and download sizes")
	}
	if _, err := New(Config{Addr: "not-an-address"}); err == nil {
		t.Fatal("invalid addr must be rejected")
	}
	if _, err := New(Config{
		MaxDownloadDuration: 30 * time.Second,
		WriteTimeout:        10 * time.Second,
	}); err == nil {
		t.Fatal("write timeout shorter than the maximum download duration must be rejected")
	}
	if _, err := New(Config{MaxUploadBytes: -1}); err == nil {
		t.Fatal("negative upload limit must be rejected")
	}
}
