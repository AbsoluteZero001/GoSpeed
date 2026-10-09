package speedtest

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewTestID(t *testing.T) {
	first, err := NewTestID()
	if err != nil {
		t.Fatalf("NewTestID returned error: %v", err)
	}
	if !strings.HasPrefix(first, "gs-") || len(first) != len("gs-")+16 {
		t.Fatalf("unexpected test id format: %q", first)
	}
	second, err := NewTestID()
	if err != nil {
		t.Fatalf("NewTestID returned error: %v", err)
	}
	if first == second {
		t.Fatalf("two generated test ids are identical: %q", first)
	}
}

func TestTargetNormalized(t *testing.T) {
	target, err := Target{ID: "local", BaseURL: "http://127.0.0.1:8080/"}.normalized()
	if err != nil {
		t.Fatalf("normalized returned error: %v", err)
	}
	if target.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("base url = %q, want trailing slash removed", target.BaseURL)
	}
	if target.Protocol != ProtocolHTTP {
		t.Fatalf("protocol = %q, want http", target.Protocol)
	}
	if target.Name != "local" {
		t.Fatalf("name = %q, want the id as fallback", target.Name)
	}

	https, err := Target{ID: "tls", BaseURL: "https://example.com"}.normalized()
	if err != nil {
		t.Fatalf("https normalized returned error: %v", err)
	}
	if https.Protocol != ProtocolHTTPS {
		t.Fatalf("protocol = %q, want https", https.Protocol)
	}
}

func TestTargetNormalizedRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		target Target
		want   error
	}{
		{"missing id", Target{BaseURL: "http://127.0.0.1:8080"}, ErrInvalidTarget},
		{"missing url", Target{ID: "local"}, ErrInvalidTarget},
		{"relative url", Target{ID: "local", BaseURL: "127.0.0.1:8080"}, ErrInvalidTarget},
		{"unsupported scheme", Target{ID: "local", BaseURL: "ftp://127.0.0.1"}, ErrUnsupportedProtocol},
		{"query", Target{ID: "local", BaseURL: "http://127.0.0.1:8080?a=b"}, ErrInvalidTarget},
		{"protocol mismatch", Target{ID: "local", BaseURL: "https://127.0.0.1", Protocol: ProtocolHTTP}, ErrInvalidTarget},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := testCase.target.normalized(); !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestResultJSONShape(t *testing.T) {
	jitter := 500 * time.Microsecond
	result := Result{
		TestID:    "gs-0000000000000001",
		Timestamp: time.Unix(0, 0).UTC(),
		Status:    StatusCompleted,
		Target: TargetInfo{
			ServerID:      "local",
			ServerName:    "Local Test Server",
			ServerAddress: "http://127.0.0.1:8080",
			Protocol:      ProtocolHTTP,
			Local:         true,
		},
		Settings: SettingsSnapshot{
			Phases:          []Phase{PhaseLatency, PhaseDownload},
			DurationNs:      time.Second,
			TimeoutNs:       5 * time.Second,
			Connections:     1,
			LatencySamples:  2,
			LatencyInterval: 10 * time.Millisecond,
		},
		Latency: &LatencyResult{
			Type:              LatencyHTTPRTT,
			Attempts:          2,
			SuccessfulSamples: 2,
			MinNs:             time.Millisecond,
			AverageNs:         2 * time.Millisecond,
			MaxNs:             3 * time.Millisecond,
			JitterNs:          &jitter,
			SamplesNs:         []time.Duration{time.Millisecond, 2 * time.Millisecond},
		},
		Download: &TransferResult{
			Bytes:             1 << 20,
			DurationNs:        time.Second,
			Mbps:              8.388,
			MBPerSecond:       1.048,
			Connections:       1,
			MeasurementWindow: WindowDownload,
			StopReason:        StopReasonDuration,
		},
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	if strings.Contains(string(raw), "\"upload\"") {
		t.Fatalf("unmeasured upload section must be omitted: %s", raw)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	latency, ok := decoded["latency"].(map[string]any)
	if !ok {
		t.Fatalf("latency section missing: %s", raw)
	}
	if value, present := latency["packet_loss_percent"]; !present || value != nil {
		t.Fatalf("packet_loss_percent must be present and null, got %v (present=%t)", value, present)
	}
	if latency["type"] != string(LatencyHTTPRTT) {
		t.Fatalf("latency type = %v, want %q", latency["type"], LatencyHTTPRTT)
	}
	download, ok := decoded["download"].(map[string]any)
	if !ok {
		t.Fatalf("download section missing: %s", raw)
	}
	if download["bytes"] != float64(1<<20) {
		t.Fatalf("download bytes = %v, want %d", download["bytes"], 1<<20)
	}
	if download["stop_reason"] != string(StopReasonDuration) {
		t.Fatalf("stop reason = %v", download["stop_reason"])
	}
	if decoded["status"] != string(StatusCompleted) {
		t.Fatalf("status = %v", decoded["status"])
	}
}
