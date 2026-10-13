package provider

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/server"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// cappedGoSpeedServer runs the production GoSpeed server with a small upload
// ceiling, so a duration limited run on loopback reaches it quickly.
func cappedGoSpeedServer(t *testing.T, maxUploadBytes int64) *httptest.Server {
	t.Helper()
	srv, err := server.New(server.Config{
		MaxUploadBytes:          maxUploadBytes,
		MaxDownloadBytes:        64 << 20,
		MaxDownloadDuration:     30 * time.Second,
		DefaultDownloadDuration: 10 * time.Second,
		MaxConcurrentTests:      8,
		MaxConnectionsPerTest:   16,
		ReadTimeout:             60 * time.Second,
		WriteTimeout:            60 * time.Second,
	})
	if err != nil {
		t.Fatalf("build gospeed server: %v", err)
	}
	testServer := httptest.NewServer(srv.Handler())
	t.Cleanup(testServer.Close)
	return testServer
}

// TestDurationLimitedUploadMapsToAServerLimitStop is the golden mapping for the
// reported 413. The provider must report a completed measurement that names the
// server ceiling as the reason, never a failed run and never a budget abort.
func TestDurationLimitedUploadMapsToAServerLimitStop(t *testing.T) {
	const ceiling = 4 << 20
	testServer := cappedGoSpeedServer(t, ceiling)

	provider := NewGoSpeedHTTP(nil)
	node := Node{
		ID: "local", Name: "Local", Endpoint: testServer.URL,
		Scope: ScopeLocal, Provider: IDGoSpeedHTTP,
	}
	capabilities, err := provider.Negotiate(context.Background(), node)
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if !capabilities.Supports(CapUpload) {
		t.Fatal("the server advertises upload but the negotiation dropped it")
	}

	// No byte budget at all: this is the pure duration run that used to fail.
	final, err := provider.Run(context.Background(), RunRequest{
		Node:         node,
		Capabilities: capabilities,
		Directions:   []Direction{DirectionUpload},
		Duration:     10 * time.Second,
		Connections:  1,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v (status %q code %q)", err, final.Status, final.ErrorCode)
	}

	if final.Status != StatusCompleted {
		t.Fatalf("status = %q, want %q", final.Status, StatusCompleted)
	}
	if final.ErrorCode != "" {
		t.Errorf("a completed run must carry no error code, got %q", final.ErrorCode)
	}
	if final.Upload == nil {
		t.Fatal("upload metric is missing from the final result")
	}
	if final.Upload.StopReason != string(speedtest.StopReasonServerLimit) {
		t.Fatalf("stopReason = %q, want %q", final.Upload.StopReason, speedtest.StopReasonServerLimit)
	}
	if final.Upload.TransferredBytes != ceiling {
		t.Fatalf("transferredBytes = %d, want the advertised ceiling %d", final.Upload.TransferredBytes, ceiling)
	}
	if final.Upload.Mbps == nil || *final.Upload.Mbps <= 0 {
		t.Fatalf("upload Mbps = %v, want a real positive measurement", final.Upload.Mbps)
	}
	// A real transfer on loopback must never be reported as zero or N/A.
	if final.Upload.Mbps != nil && *final.Upload.Mbps == 0 {
		t.Fatal("upload Mbps must never be a measured zero")
	}
	if final.ProviderID != IDGoSpeedHTTP {
		t.Errorf("providerID = %q, want %q", final.ProviderID, IDGoSpeedHTTP)
	}

	// The quality model must see a real measurement, and the shortened window
	// must be visible rather than silently presented as a full duration run.
	if final.Quality.Completeness != Complete {
		t.Errorf("completeness = %q, want %q", final.Quality.Completeness, Complete)
	}
	if final.Quality.Stability != "degraded" {
		t.Errorf("stability = %q, want %q: the window ended early and must be reported as degraded",
			final.Quality.Stability, "degraded")
	}

	var explained bool
	for _, warning := range final.Method.Warnings {
		if strings.Contains(warning, "advertised") {
			explained = true
		}
	}
	if !explained {
		t.Fatalf("warnings %q do not explain the server byte ceiling", final.Method.Warnings)
	}
}
