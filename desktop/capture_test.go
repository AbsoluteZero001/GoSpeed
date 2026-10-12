package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
)

// TestCaptureRealEventStream records the exact event payloads the Wails frontend
// receives during a real localhost measurement, so the GUI can be reviewed
// against production data instead of hand written fixtures.
//
// It is skipped unless GOSPEED_CAPTURE=1 and GOSPEED_CAPTURE_BASE_URL are set,
// so it never runs in CI and never changes ordinary test behaviour.
//
//	GOSPEED_CAPTURE=1 GOSPEED_CAPTURE_BASE_URL=http://127.0.0.1:8080 \
//	  GOSPEED_CAPTURE_DIR=<dir> go test -run TestCaptureRealEventStream ./...
//
// It writes <dir>/events.json containing the ordered event stream exactly as
// the backend emitted it.
func TestCaptureRealEventStream(t *testing.T) {
	if os.Getenv("GOSPEED_CAPTURE") != "1" {
		t.Skip("set GOSPEED_CAPTURE=1 to record a real event stream")
	}
	baseURL := os.Getenv("GOSPEED_CAPTURE_BASE_URL")
	if baseURL == "" {
		t.Skip("set GOSPEED_CAPTURE_BASE_URL to the node under test")
	}
	dir := os.Getenv("GOSPEED_CAPTURE_DIR")
	if dir == "" {
		dir = t.TempDir()
	}

	manager, err := nodes.NewManager([]nodes.Node{{
		ID:           "capture",
		Name:         "Loopback capture",
		BaseURL:      baseURL,
		Protocol:     nodes.ProtocolHTTP,
		Enabled:      true,
		Local:        true,
		Capabilities: []nodes.Capability{nodes.CapabilityLatency, nodes.CapabilityDownload, nodes.CapabilityUpload},
	}})
	if err != nil {
		t.Fatalf("nodes.NewManager: %v", err)
	}

	var mu sync.Mutex
	type captured struct {
		Name    string `json:"name"`
		Payload any    `json:"payload"`
	}
	var stream []captured

	emit := func(name string, payload any) {
		mu.Lock()
		stream = append(stream, captured{Name: name, Payload: payload})
		mu.Unlock()
	}

	runner := NewRunner(emit)
	runner.loadNodesFn = func() (*nodes.Manager, string, error) {
		return manager, "capture.json", nil
	}
	t.Cleanup(func() { _ = runner.CloseProviders() })

	durationMs := 1000
	if os.Getenv("GOSPEED_CAPTURE_CANCEL") == "1" {
		// A long window that is cancelled mid transfer, so the captured run is
		// a real cancellation rather than a synthetic one.
		durationMs = 20000
	}
	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "capture",
		Connections:      4,
		DurationMs:       durationMs,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if os.Getenv("GOSPEED_CAPTURE_CANCEL") == "1" {
		// Wait for real transfer progress, then cancel through the same path
		// the Cancel button uses.
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			seen := len(stream)
			mu.Unlock()
			if seen > 8 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := runner.CancelTest(); err != nil {
			t.Fatalf("CancelTest: %v", err)
		}
	}
	runner.Wait()

	mu.Lock()
	encoded, err := json.MarshalIndent(stream, "", "  ")
	count := len(stream)
	mu.Unlock()
	if err != nil {
		t.Fatalf("marshal stream: %v", err)
	}
	path := filepath.Join(dir, "events.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write capture: %v", err)
	}

	// The bootstrap calls the app info and node list once on mount, so capture
	// the real answers too instead of hand written fixtures.
	bootstrap := map[string]any{}
	app := NewApp()
	app.runner = runner
	if info, err := app.GetAppInfo(); err == nil {
		bootstrap["appInfo"] = info
	} else {
		t.Fatalf("GetAppInfo: %v", err)
	}
	if list, err := app.ListNodes(); err == nil {
		bootstrap["nodes"] = list
	} else {
		t.Fatalf("ListNodes: %v", err)
	}
	bootstrapEncoded, err := json.MarshalIndent(bootstrap, "", "  ")
	if err != nil {
		t.Fatalf("marshal bootstrap: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bootstrap.json"), bootstrapEncoded, 0o600); err != nil {
		t.Fatalf("write bootstrap: %v", err)
	}
	t.Logf("captured %d events to %s", count, path)
	if count == 0 {
		t.Fatal("no events were captured")
	}
}
