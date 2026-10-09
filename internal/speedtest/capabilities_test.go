package speedtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type capabilityOptions struct {
	noCapabilities     bool
	capabilities       []string
	maxConnections     int
	maxDurationSeconds int
	maxDownloadBytes   int64
	maxUploadBytes     int64
}

// capabilityServer builds a minimal GoSpeed-like server for capability and
// limit tests. It never touches a real network service.
func capabilityServer(t *testing.T, options capabilityOptions) *httptest.Server {
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
		if options.noCapabilities {
			http.NotFound(w, r)
			return
		}
		capabilities := options.capabilities
		if capabilities == nil {
			capabilities = []string{"latency", "download", "upload"}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocol_version": 1,
			"server_version":   "0.3.0",
			"capabilities":     capabilities,
			"limits": map[string]any{
				"max_connections_per_test": options.maxConnections,
				"max_duration_seconds":     options.maxDurationSeconds,
				"max_download_bytes":       options.maxDownloadBytes,
				"max_upload_bytes":         options.maxUploadBytes,
			},
		})
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
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
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		received, err := io.Copy(io.Discard, r.Body)
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
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func transferOptions(server *httptest.Server) Options {
	return Options{
		Target:                Target{ID: "test", Name: "test", BaseURL: server.URL, Protocol: ProtocolHTTP},
		Phases:                PhasesDownload,
		Duration:              time.Second,
		Timeout:               5 * time.Second,
		MaxBytes:              1 << 20,
		Connections:           1,
		Warmup:                true,
		NegotiateCapabilities: true,
	}
}

func TestEngineNegotiatesCapabilities(t *testing.T) {
	server := capabilityServer(t, capabilityOptions{
		maxConnections:     4,
		maxDurationSeconds: 5,
		maxDownloadBytes:   1 << 30,
		maxUploadBytes:     1 << 30,
	})
	options := transferOptions(server)
	options.Connections = 2
	result, err := NewEngine(nil).Run(context.Background(), options)
	if err != nil {
		t.Fatalf("Run returned error: %v (%s)", err, result.ErrorMessage)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q", result.Status)
	}
	capabilities := result.Target.Capabilities
	if capabilities == nil || !capabilities.Supported {
		t.Fatalf("capabilities = %+v, want a supported negotiation", capabilities)
	}
	if capabilities.ServerVersion != "0.3.0" || capabilities.ProtocolVersion != 1 {
		t.Fatalf("capabilities = %+v", capabilities)
	}
	if capabilities.Limits == nil || capabilities.Limits.MaxConnectionsPerTest != 4 {
		t.Fatalf("limits = %+v", capabilities.Limits)
	}
	if result.Target.HealthStatus != "healthy" || result.Target.HealthLatencyNs < 0 {
		t.Fatalf("warmup health record = %q / %s", result.Target.HealthStatus, result.Target.HealthLatencyNs)
	}
}

func TestEngineRejectsAdvertisedLimits(t *testing.T) {
	server := capabilityServer(t, capabilityOptions{
		maxConnections:     2,
		maxDurationSeconds: 3,
		maxDownloadBytes:   1 << 20,
		maxUploadBytes:     1 << 20,
	})
	engine := NewEngine(nil)

	options := transferOptions(server)
	options.Connections = 4
	if _, err := engine.Run(context.Background(), options); !errors.Is(err, ErrServerLimitExceeded) {
		t.Fatalf("connections error = %v, want ErrServerLimitExceeded", err)
	}

	options = transferOptions(server)
	options.Duration = 10 * time.Second
	options.Timeout = 30 * time.Second
	if _, err := engine.Run(context.Background(), options); !errors.Is(err, ErrServerLimitExceeded) {
		t.Fatalf("duration error = %v, want ErrServerLimitExceeded", err)
	}

	options = transferOptions(server)
	options.MaxBytes = 2 << 20
	if _, err := engine.Run(context.Background(), options); !errors.Is(err, ErrServerLimitExceeded) {
		t.Fatalf("byte budget error = %v, want ErrServerLimitExceeded", err)
	}
}

func TestEngineAcceptsLegacyServerWithoutCapabilities(t *testing.T) {
	server := capabilityServer(t, capabilityOptions{noCapabilities: true})
	result, err := NewEngine(nil).Run(context.Background(), transferOptions(server))
	if err != nil {
		t.Fatalf("Run returned error: %v (%s)", err, result.ErrorMessage)
	}
	capabilities := result.Target.Capabilities
	if capabilities == nil || capabilities.Supported || !capabilities.Unsupported {
		t.Fatalf("capabilities = %+v, want an unsupported endpoint recorded", capabilities)
	}
	if result.Download == nil || result.Download.Bytes == 0 {
		t.Fatalf("legacy server must still be measurable: %+v", result.Download)
	}
}

func TestEngineRejectsPhaseTheServerDoesNotAdvertise(t *testing.T) {
	server := capabilityServer(t, capabilityOptions{
		capabilities:       []string{"latency"},
		maxConnections:     4,
		maxDurationSeconds: 5,
	})
	options := transferOptions(server)
	options.Phases = PhasesLatency | PhasesDownload | PhasesUpload
	if _, err := NewEngine(nil).Run(context.Background(), options); !errors.Is(err, ErrServerCapabilityMissing) {
		t.Fatalf("error = %v, want ErrServerCapabilityMissing", err)
	}
}

func TestEngineSkipsNegotiationWhenDisabled(t *testing.T) {
	server := capabilityServer(t, capabilityOptions{
		maxConnections:     1,
		maxDurationSeconds: 1,
		maxDownloadBytes:   1024,
	})
	options := transferOptions(server)
	options.NegotiateCapabilities = false
	options.Connections = 4
	result, err := NewEngine(nil).Run(context.Background(), options)
	if err != nil {
		t.Fatalf("Run returned error: %v (%s)", err, result.ErrorMessage)
	}
	if result.Target.Capabilities != nil {
		t.Fatalf("capabilities must stay nil when negotiation is disabled: %+v", result.Target.Capabilities)
	}
}

func TestTargetNetworkScope(t *testing.T) {
	cases := []struct {
		host  string
		scope NetworkScope
	}{
		{"127.0.0.1", ScopeLocal},
		{"localhost", ScopeLocal},
		{"192.168.1.20", ScopeLAN},
		{"10.0.0.5", ScopeLAN},
		{"8.8.8.8", ScopeRemote},
		{"speed.example.com", ScopeUnknown},
	}
	for _, testCase := range cases {
		target, err := Target{ID: "test", BaseURL: "http://" + testCase.host + ":8080"}.normalized()
		if err != nil {
			t.Fatalf("normalized(%s): %v", testCase.host, err)
		}
		if target.NetworkScope() != testCase.scope {
			t.Fatalf("scope(%s) = %q, want %q", testCase.host, target.NetworkScope(), testCase.scope)
		}
		if testCase.scope == ScopeLocal && !target.Local {
			t.Fatalf("loopback target %s must be marked local", testCase.host)
		}
	}
}
