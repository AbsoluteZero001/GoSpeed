package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
)

// This file covers the B5 contract between the unified provider verdict and the
// GUI: the finished event must carry it, must keep the legacy shape, and must
// never present a local, LAN or self hosted run as something it is not.

// unifiedJSON decodes the finished event the way the frontend receives it.
type finishedWire struct {
	Status    string          `json:"status"`
	TestID    string          `json:"testId"`
	Error     string          `json:"error"`
	Cancelled bool            `json:"cancelled"`
	Result    json.RawMessage `json:"result"`
	Unified   struct {
		ProviderID string `json:"providerId"`
		ServerID   string `json:"serverId"`
		Status     string `json:"status"`
		Scope      string `json:"scope"`
		Ownership  string `json:"ownership"`
		Discovery  string `json:"discovery"`
		Download   *struct {
			Mbps             *float64 `json:"mbps"`
			Window           string   `json:"window"`
			StopReason       string   `json:"stopReason"`
			TransferredBytes int64    `json:"transferredBytes"`
		} `json:"download"`
		Latency *struct {
			ValueMs *float64 `json:"valueMs"`
			Type    string   `json:"type"`
			Source  string   `json:"source"`
		} `json:"latency"`
		Jitter *struct {
			ValueMs *float64 `json:"valueMs"`
			Type    string   `json:"type"`
		} `json:"jitter"`
		Quality struct {
			Completeness string `json:"completeness"`
			Stability    string `json:"stability"`
			Trust        string `json:"trust"`
		} `json:"quality"`
		Method struct {
			Protocol string   `json:"protocol"`
			Warnings []string `json:"warnings"`
		} `json:"method"`
		Detail *struct {
			Format  string          `json:"format"`
			Payload json.RawMessage `json:"payload"`
		} `json:"detail"`
	} `json:"unified"`
}

func decodeFinished(t *testing.T, event FinishedEvent) finishedWire {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal finished event: %v", err)
	}
	var wire finishedWire
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode finished event: %v", err)
	}
	return wire
}

// TestFinishedEventCarriesTheUnifiedVerdict proves the GUI receives the
// provider independent result on every completed run.
func TestFinishedEventCarriesTheUnifiedVerdict(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))
	t.Cleanup(func() { _ = runner.CloseProviders() })

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      4,
		DurationMs:       1000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 60*time.Second)
	runner.Wait()

	if finished.Unified == nil {
		t.Fatal("the finished event carries no unified result")
	}
	wire := decodeFinished(t, finished)
	if wire.Unified.ProviderID != string(provider.IDGoSpeedHTTP) {
		t.Errorf("providerId = %q", wire.Unified.ProviderID)
	}
	if wire.Unified.Status != string(provider.StatusCompleted) {
		t.Errorf("unified status = %q", wire.Unified.Status)
	}
	// A loopback fixture must be reported as local, never as remote.
	if wire.Unified.Scope != string(provider.ScopeLocal) {
		t.Errorf("scope = %q, want local for a loopback fixture", wire.Unified.Scope)
	}
	if wire.Unified.Ownership != string(provider.OwnershipSelfHosted) {
		t.Errorf("ownership = %q, want self_hosted", wire.Unified.Ownership)
	}
	if wire.Unified.Download == nil || wire.Unified.Download.Mbps == nil {
		t.Fatal("the unified download rate is missing")
	}
	if *wire.Unified.Download.Mbps <= 0 {
		t.Errorf("download mbps = %v, want a real positive rate", *wire.Unified.Download.Mbps)
	}
	if wire.Unified.Download.Window == "" || wire.Unified.Download.StopReason == "" {
		t.Error("the unified rate lost its window or stop reason")
	}
	// On a loopback runner every round trip can finish inside one tick of the
	// platform clock, which makes the samples unresolvable. That is an
	// observation that the quantity was too small to resolve, so the unified
	// metric must be null. Either outcome is correct; a published 0.00 ms is
	// not. This assertion is stated as "positive value or null" on purpose: a
	// stricter version failed the Windows CI runner and broke main.
	if wire.Unified.Latency == nil {
		if wire.Unified.Jitter != nil {
			t.Error("jitter survived an unresolvable latency")
		}
	} else {
		if wire.Unified.Latency.ValueMs == nil {
			t.Fatal("a reported latency must carry a value")
		}
		if *wire.Unified.Latency.ValueMs <= 0 {
			t.Fatalf("latency = %v ms, want a positive measurement or null", *wire.Unified.Latency.ValueMs)
		}
		if wire.Unified.Latency.Type != string(provider.MetricHTTPRTT) {
			t.Errorf("latency type = %q, want http_rtt", wire.Unified.Latency.Type)
		}
		if !strings.Contains(wire.Unified.Latency.Source, "not an icmp ping") {
			t.Errorf("latency source = %q, want it to disclaim ICMP", wire.Unified.Latency.Source)
		}
		if wire.Unified.Jitter == nil || wire.Unified.Jitter.ValueMs == nil {
			t.Error("a measured latency must come with its derived jitter")
		}
	}
	if wire.Unified.Quality.Completeness != string(provider.Complete) {
		t.Errorf("completeness = %q", wire.Unified.Quality.Completeness)
	}
	if wire.Unified.Method.Protocol != "gospeed-http" {
		t.Errorf("protocol = %q", wire.Unified.Method.Protocol)
	}

	// The legacy payload must still be there, byte compatible.
	if len(wire.Result) == 0 {
		t.Fatal("the legacy result disappeared")
	}
	var legacy struct {
		TestID   string `json:"test_id"`
		Status   string `json:"status"`
		Download *struct {
			Bytes int64   `json:"bytes"`
			Mbps  float64 `json:"mbps"`
		} `json:"download"`
	}
	if err := json.Unmarshal(wire.Result, &legacy); err != nil {
		t.Fatalf("decode legacy result: %v", err)
	}
	if legacy.TestID == "" || legacy.Download == nil {
		t.Fatalf("legacy result lost evidence: %+v", legacy)
	}
	if wire.Unified.Detail == nil || wire.Unified.Detail.Format != provider.DetailFormatGoSpeedResult {
		t.Error("the unified result lost its native detail marker")
	}
}

// TestFinishedEventKeepsUnifiedOnCancellation proves the verdict survives a run
// that did not complete, because that is exactly when the UI needs it.
func TestFinishedEventKeepsUnifiedOnCancellation(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))
	t.Cleanup(func() { _ = runner.CloseProviders() })

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      4,
		DurationMs:       10000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitProgress(t, rec, func(event ProgressEvent) bool {
		return event.Phase == "download" && event.Bytes > 0
	}, 30*time.Second)
	if err := runner.CancelTest(); err != nil {
		t.Fatalf("CancelTest: %v", err)
	}
	finished := waitFinished(t, rec, 60*time.Second)
	runner.Wait()

	wire := decodeFinished(t, finished)
	if !wire.Cancelled {
		t.Errorf("cancelled = false for status %q", wire.Status)
	}
	if wire.Unified.Status != string(provider.StatusCancelled) {
		t.Errorf("unified status = %q, want cancelled", wire.Unified.Status)
	}
	if wire.Unified.Quality.Completeness == string(provider.Complete) {
		t.Error("a cancelled run must not claim complete evidence")
	}
	if wire.Unified.Scope != string(provider.ScopeLocal) {
		t.Errorf("scope = %q, a cancelled run still knows where it measured", wire.Unified.Scope)
	}
}

// TestNodeConfigurationRejectsCredentials proves the first layer of the detail
// audit: a node whose address embeds credentials is refused at configuration
// time, so the desktop client can never hold one in the first place.
func TestNodeConfigurationRejectsCredentials(t *testing.T) {
	_, err := nodes.NewManager([]nodes.Node{{
		ID:           "test",
		Name:         "With credentials",
		BaseURL:      "https://user:sup3rsecret@127.0.0.1:9/",
		Protocol:     nodes.ProtocolHTTPS,
		Enabled:      true,
		Capabilities: []nodes.Capability{nodes.CapabilityLatency},
	}})
	if err == nil {
		t.Fatal("a node with embedded credentials must be refused")
	}
	if !strings.Contains(err.Error(), "credential") {
		t.Fatalf("error = %q, want it to name the credential problem", err)
	}
}

// TestFinishedEventNeverPublishesAUserinfoComponent checks the serialized
// finished event of a real run for any userinfo component.
//
// Together with internal/provider's audit tests this pins the defence in depth:
// the node layer refuses credentials outright, the provider strips userinfo
// from the native detail it publishes, and Go's transport errors already
// replace a password with ***.
func TestFinishedEventNeverPublishesAUserinfoComponent(t *testing.T) {
	httpServer := newSpeedtestServer(t)
	rec := newRecorder()
	runner := newTestRunner(t, rec, newManager(t, httpServer.URL))
	t.Cleanup(func() { _ = runner.CloseProviders() })

	if _, err := runner.Start(StartOptions{
		SelectionMode:    SelectionModeManual,
		NodeID:           "test",
		Connections:      2,
		DurationMs:       1000,
		SampleIntervalMs: 100,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	finished := waitFinished(t, rec, 60*time.Second)
	runner.Wait()

	encoded, err := json.Marshal(finished)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "@") && strings.Contains(string(encoded), "://") {
		t.Fatalf("the finished event published a userinfo component: %s", encoded)
	}
	if strings.Contains(string(encoded), "sup3rsecret") {
		t.Fatal("the finished event published a credential")
	}
}

// TestApplyFinishedAlwaysSetsUnified covers the paths that never reach the
// network, where the verdict must still be present so the UI can render it.
func TestApplyFinishedAlwaysSetsUnified(t *testing.T) {
	tests := []struct {
		name   string
		final  provider.FinalResult
		err    error
		status string
	}{
		{
			name:   "completed",
			final:  provider.FinalResult{Status: provider.StatusCompleted},
			status: StateCompleted,
		},
		{
			name:   "cancelled",
			final:  provider.FinalResult{Status: provider.StatusCancelled, ErrorMessage: "context canceled"},
			err:    context.Canceled,
			status: StateCancelled,
		},
		{
			name:   "timeout",
			final:  provider.FinalResult{Status: provider.StatusTimeout, ErrorMessage: "deadline exceeded"},
			status: StateFailed,
		},
		{
			name:   "budget exceeded",
			final:  provider.FinalResult{Status: provider.StatusBudgetExceeded, ErrorMessage: "over budget"},
			status: StateFailed,
		},
		{
			name:   "consent required",
			final:  provider.FinalResult{Status: provider.StatusConsentNeeded, ErrorMessage: "consent missing"},
			status: StateFailed,
		},
		{
			name:   "failed",
			final:  provider.FinalResult{Status: provider.StatusFailed, ErrorMessage: "boom"},
			status: StateFailed,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			event := FinishedEvent{}
			applyFinished(&event, testCase.final, testCase.err)
			if event.Status != testCase.status {
				t.Errorf("status = %q, want %q", event.Status, testCase.status)
			}
			if event.Unified == nil {
				t.Fatal("the unified verdict is missing")
			}
			if event.Unified.Status != testCase.final.Status {
				t.Errorf("unified status = %q, want %q", event.Unified.Status, testCase.final.Status)
			}
		})
	}
}
