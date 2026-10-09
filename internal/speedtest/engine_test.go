package speedtest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
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
	const payloadSize = 8 << 20
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
	if result.Upload == nil || result.Upload.Bytes != payloadSize {
		t.Fatalf("unexpected upload result: %+v", result.Upload)
	}
	if result.Upload.ServerConfirmedBytes != result.Upload.Bytes {
		t.Fatalf("upload byte mismatch: client=%d server=%d",
			result.Upload.Bytes, result.Upload.ServerConfirmedBytes)
	}
	if len(result.Settings.Phases) != 3 {
		t.Fatalf("recorded phases = %v, want latency, download and upload", result.Settings.Phases)
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
			name:    "multiple connections",
			options: Options{Target: validTarget, Connections: 2},
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
