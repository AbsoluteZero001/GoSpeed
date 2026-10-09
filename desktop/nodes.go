package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/nodes"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

func nodeViews(list []nodes.Node) []NodeView {
	views := make([]NodeView, 0, len(list))
	for _, node := range list {
		views = append(views, nodeView(node))
	}
	return views
}

func nodeView(node nodes.Node) NodeView {
	return NodeView{
		ID:           node.ID,
		Name:         node.Name,
		BaseURL:      node.BaseURL,
		Protocol:     node.Protocol,
		Enabled:      node.Enabled,
		Local:        node.Local,
		Provider:     node.Provider,
		Description:  node.Description,
		Location:     locationOf(node),
		NetworkScope: nodes.NetworkScope(node.BaseURL),
	}
}

func probeView(node nodes.Node, probe nodes.Probe) NodeStatusView {
	view := NodeStatusView{
		ID:                     node.ID,
		Name:                   node.Name,
		BaseURL:                node.BaseURL,
		Protocol:               node.Protocol,
		Enabled:                node.Enabled,
		Local:                  node.Local,
		NetworkScope:           nodes.NetworkScope(node.BaseURL),
		Status:                 string(probe.Status),
		HTTPRTTMs:              durationMs(probe.HTTPRTT),
		DNSMs:                  durationMs(probe.DNSDuration),
		TCPMs:                  durationMs(probe.TCPDuration),
		TLSMs:                  durationMs(probe.TLSDuration),
		LatencyMedianMs:        durationMs(probe.LatencyMedian),
		LatencySamples:         len(probe.LatencySamples),
		LatencyBelowResolution: probe.LatencyBelowResolution,
		LatencyFailures:        probe.LatencyFailures,
		Attempts:               probe.Attempts,
		ServerVersion:          probe.Capabilities.ServerVersion,
		Capabilities:           probe.Capabilities.Supported,
		CapabilitiesLegacy:     probe.Capabilities.Unsupported,
		CheckedAt:              formatTime(probe.CheckedAt),
	}
	view.Detail = probeDetail(probe)
	return view
}

// probeDetail keeps the human readable reason of a probe: the error that
// decided the status plus the clock resolution note when it applies.
func probeDetail(probe nodes.Probe) string {
	parts := make([]string, 0, 3)
	if probe.Error != "" {
		parts = append(parts, probe.Error)
	} else if probe.Attempts > 1 && probe.Status == nodes.HealthDegraded {
		parts = append(parts, fmt.Sprintf("needed %d attempts", probe.Attempts))
	}
	if probe.LatencyBelowResolution > 0 {
		parts = append(parts, fmt.Sprintf("%d sample(s) below the platform clock resolution", probe.LatencyBelowResolution))
	}
	return strings.Join(parts, "; ")
}

// targetView describes a resolved measurement target.
func targetView(target speedtest.Target) NodeTargetView {
	view := NodeTargetView{
		ID:           target.ID,
		Name:         target.Name,
		BaseURL:      target.BaseURL,
		Protocol:     string(target.Protocol),
		Local:        target.Local,
		NetworkScope: nodes.NetworkScope(target.BaseURL),
		HealthStatus: target.HealthStatus,
	}
	if target.HealthLatency > 0 {
		view.HealthLatencyMs = durationMs(target.HealthLatency)
	}
	return view
}

func candidateViews(candidates []nodes.Candidate) []CandidateView {
	views := make([]CandidateView, 0, len(candidates))
	for _, candidate := range candidates {
		views = append(views, CandidateView{
			Rank:      candidate.Rank,
			NodeID:    candidate.Node.ID,
			NodeName:  candidate.Node.Name,
			Status:    string(candidate.Probe.Status),
			LatencyMs: durationMs(candidate.LatencyNs),
			Reason:    candidate.Reason,
		})
	}
	return views
}

// enabledNodes keeps the Enabled filter local so the GUI and the CLI agree on
// what "automatic selection" considers.
func enabledNodes(list []nodes.Node) []nodes.Node {
	enabled := make([]nodes.Node, 0, len(list))
	for _, node := range list {
		if node.Enabled {
			enabled = append(enabled, node)
		}
	}
	return enabled
}

// nodeTarget maps a configured node onto the engine's minimal target type.
func nodeTarget(node nodes.Node) speedtest.Target {
	protocol := speedtest.ProtocolHTTP
	if strings.EqualFold(node.Protocol, nodes.ProtocolHTTPS) {
		protocol = speedtest.ProtocolHTTPS
	}
	return speedtest.Target{
		ID:       node.ID,
		Name:     node.Name,
		BaseURL:  node.BaseURL,
		Protocol: protocol,
		Local:    node.Local || nodes.IsLoopbackHost(urlHost(node.BaseURL)),
	}
}

func nodeIDList(list []nodes.Node) string {
	if len(list) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(list))
	for _, node := range list {
		ids = append(ids, node.ID)
	}
	return strings.Join(ids, ", ")
}

func locationOf(node nodes.Node) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{node.Country, node.Region, node.City} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		if node.Local {
			return "local"
		}
		return "N/A"
	}
	return strings.Join(parts, "/")
}

func urlHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// durationMs converts a measured duration into milliseconds. A non-positive
// duration means "not observed" and stays nil so the UI renders N/A.
func durationMs(value time.Duration) *float64 {
	if value <= 0 {
		return nil
	}
	ms := float64(value) / float64(time.Millisecond)
	return &ms
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
