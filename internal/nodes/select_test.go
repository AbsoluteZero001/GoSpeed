package nodes

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func selectNode(id string) Node {
	return Node{ID: id, Name: strings.ToUpper(id), BaseURL: "http://192.168.1.10:8080", Protocol: ProtocolHTTP, Enabled: true}
}

func TestSelectAutoPrefersHealthyThenLatency(t *testing.T) {
	list := []Node{selectNode("a"), selectNode("b"), selectNode("c")}
	probes := []Probe{
		{NodeID: "a", Status: HealthHealthy, LatencyMedian: 5 * time.Millisecond},
		{NodeID: "b", Status: HealthHealthy, LatencyMedian: 2 * time.Millisecond},
		{NodeID: "c", Status: HealthDegraded, LatencyMedian: time.Millisecond, Error: "needed 2 attempts"},
	}
	selection, err := SelectAuto(list, probes)
	if err != nil {
		t.Fatalf("SelectAuto returned error: %v", err)
	}
	if selection.Node.ID != "b" {
		t.Fatalf("selected %q, want b (healthy beats degraded)", selection.Node.ID)
	}
	if selection.Method != SelectionAuto {
		t.Fatalf("method = %q", selection.Method)
	}
	if !strings.Contains(selection.Reason, "lowest measured median HTTP RTT") {
		t.Fatalf("reason = %q", selection.Reason)
	}
	if len(selection.Candidates) != 3 {
		t.Fatalf("candidates = %d, want 3", len(selection.Candidates))
	}
	if selection.Candidates[0].Rank != 1 || selection.Candidates[0].Node.ID != "b" {
		t.Fatalf("first candidate = %+v", selection.Candidates[0])
	}
	if selection.Candidates[1].Node.ID != "a" || selection.Candidates[2].Node.ID != "c" {
		t.Fatalf("candidate order = %+v", selection.Candidates)
	}
	if !strings.Contains(selection.Note, "does not rank server bandwidth") {
		t.Fatalf("note = %q", selection.Note)
	}
}

func TestSelectAutoTieBreaksByNodeID(t *testing.T) {
	list := []Node{selectNode("b"), selectNode("a")}
	probes := []Probe{
		{NodeID: "b", Status: HealthHealthy, LatencyMedian: 3 * time.Millisecond},
		{NodeID: "a", Status: HealthHealthy, LatencyMedian: 3 * time.Millisecond},
	}
	selection, err := SelectAuto(list, probes)
	if err != nil {
		t.Fatalf("SelectAuto returned error: %v", err)
	}
	if selection.Node.ID != "a" {
		t.Fatalf("selected %q, want the stable ID tie break (a)", selection.Node.ID)
	}
	if !strings.Contains(selection.Reason, "tied on measured median HTTP RTT") {
		t.Fatalf("reason = %q, want the tie break named explicitly", selection.Reason)
	}
}

func TestSelectAutoWithoutLatencySamples(t *testing.T) {
	list := []Node{selectNode("b"), selectNode("a")}
	probes := []Probe{
		{NodeID: "b", Status: HealthHealthy},
		{NodeID: "a", Status: HealthHealthy},
	}
	selection, err := SelectAuto(list, probes)
	if err != nil {
		t.Fatalf("SelectAuto returned error: %v", err)
	}
	if selection.Node.ID != "a" {
		t.Fatalf("selected %q, want ID order", selection.Node.ID)
	}
	if !strings.Contains(selection.Reason, "no latency sample") {
		t.Fatalf("reason = %q, want an honest N/A explanation", selection.Reason)
	}
}

func TestSelectAutoFallsBackToDegradedNode(t *testing.T) {
	list := []Node{selectNode("a"), selectNode("b")}
	probes := []Probe{
		{NodeID: "a", Status: HealthUnavailable, Error: "connection refused"},
		{NodeID: "b", Status: HealthDegraded, Error: "backoff", LatencyMedian: 4 * time.Millisecond},
	}
	selection, err := SelectAuto(list, probes)
	if err != nil {
		t.Fatalf("SelectAuto returned error: %v", err)
	}
	if selection.Node.ID != "b" {
		t.Fatalf("selected %q, want the degraded node", selection.Node.ID)
	}
	if !strings.Contains(selection.Reason, "no healthy node") {
		t.Fatalf("reason = %q", selection.Reason)
	}
}

func TestSelectAutoWithoutCandidates(t *testing.T) {
	list := []Node{selectNode("a")}
	probes := []Probe{{NodeID: "a", Status: HealthUnavailable, Error: "refused"}}
	if _, err := SelectAuto(list, probes); !errors.Is(err, ErrNoNodeAvailable) {
		t.Fatalf("error = %v, want ErrNoNodeAvailable", err)
	}
}

func TestSelectManualNeverBlockedByHealth(t *testing.T) {
	selection := SelectManual(selectNode("a"), Probe{NodeID: "a", Status: HealthUnavailable})
	if selection.Method != SelectionManual {
		t.Fatalf("method = %q", selection.Method)
	}
	if !strings.Contains(selection.Reason, "manually selected") || !strings.Contains(selection.Reason, "unavailable") {
		t.Fatalf("reason = %q", selection.Reason)
	}
}

func TestProbeSelectionLatencyFallsBackToHealthRTT(t *testing.T) {
	if _, ok := (Probe{NodeID: "a"}).SelectionLatency(); ok {
		t.Fatal("an empty probe must not report latency")
	}
	if latency, ok := (Probe{NodeID: "a", HTTPRTT: 2 * time.Millisecond}).SelectionLatency(); !ok || latency != 2*time.Millisecond {
		t.Fatalf("health RTT fallback = %s, %v", latency, ok)
	}
	if latency, ok := (Probe{NodeID: "a", HTTPRTT: 2 * time.Millisecond, LatencyMedian: 3 * time.Millisecond}).SelectionLatency(); !ok || latency != 3*time.Millisecond {
		t.Fatalf("median must win = %s, %v", latency, ok)
	}
}
