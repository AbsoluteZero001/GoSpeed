package nodes

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrNoNodeAvailable reports that every enabled node failed its health check.
var ErrNoNodeAvailable = errors.New("nodes: no node is available")

// SelectionMethod records why a node was chosen. Automatic selection must
// always be explainable, so the method and the reason are part of the result.
type SelectionMethod string

const (
	// SelectionAuto means the node was ranked from real probe data.
	SelectionAuto SelectionMethod = "auto"
	// SelectionManual means the user named the node.
	SelectionManual SelectionMethod = "manual"
	// SelectionDefault means no node was requested and none was probed.
	SelectionDefault SelectionMethod = "default"
)

// Candidate is one node considered by the automatic selector, in ranked order.
type Candidate struct {
	Node      Node          `json:"node"`
	Probe     Probe         `json:"probe"`
	Rank      int           `json:"rank"`
	LatencyNs time.Duration `json:"latency_ns,omitempty"`
	Reason    string        `json:"reason"`
}

// Selection is the outcome of node selection.
type Selection struct {
	Node       Node            `json:"node"`
	Method     SelectionMethod `json:"method"`
	SelectedAt time.Time       `json:"selected_at"`
	Reason     string          `json:"reason"`
	Probe      Probe           `json:"probe"`
	Candidates []Candidate     `json:"candidates,omitempty"`
	// Note is always present so a UI cannot present the ranking as a bandwidth
	// comparison by accident.
	Note string `json:"note"`
}

// SelectionNote is attached to every selection result.
const SelectionNote = "Latency ranking only picks a candidate node; it does not rank server bandwidth or throughput."

// rankedCandidate pairs a node with the probe that decided its rank.
type rankedCandidate struct {
	node  Node
	probe Probe
}

// SelectAuto ranks nodes using measured data only:
//
//  1. healthy nodes before degraded ones; unavailable/unknown nodes are excluded;
//  2. lower measured median HTTP RTT first;
//  3. stable node ID ordering as the final tie breaker.
//
// The returned reason names the policy that actually decided, never a guess.
func SelectAuto(nodes []Node, probes []Probe) (Selection, error) {
	probesByID := make(map[string]Probe, len(probes))
	for _, probe := range probes {
		probesByID[probe.NodeID] = probe
	}
	candidates := make([]rankedCandidate, 0, len(nodes))
	for _, node := range nodes {
		probe, ok := probesByID[node.ID]
		if !ok {
			probe = Probe{NodeID: node.ID, Status: HealthUnknown}
		}
		if probe.Status != HealthHealthy && probe.Status != HealthDegraded {
			continue
		}
		candidates = append(candidates, rankedCandidate{node: node, probe: probe})
	}
	if len(candidates) == 0 {
		return Selection{}, fmt.Errorf("%w: every enabled node failed its health check", ErrNoNodeAvailable)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if rankOf(left.probe.Status) != rankOf(right.probe.Status) {
			return rankOf(left.probe.Status) < rankOf(right.probe.Status)
		}
		leftLatency, leftOK := left.probe.SelectionLatency()
		rightLatency, rightOK := right.probe.SelectionLatency()
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK && rightOK && leftLatency != rightLatency {
			return leftLatency < rightLatency
		}
		return left.node.ID < right.node.ID
	})

	selection := Selection{
		Node:       candidates[0].node,
		Method:     SelectionAuto,
		SelectedAt: time.Now().UTC(),
		Probe:      candidates[0].probe,
		Note:       SelectionNote,
	}
	for index, candidate := range candidates {
		entry := Candidate{
			Node:   candidate.node,
			Probe:  candidate.probe,
			Rank:   index + 1,
			Reason: candidateReason(candidate.probe),
		}
		if latency, ok := candidate.probe.SelectionLatency(); ok {
			entry.LatencyNs = latency
		}
		selection.Candidates = append(selection.Candidates, entry)
	}
	selection.Reason = selectionReason(candidates, len(candidates))
	return selection, nil
}

// SelectManual returns a user chosen node. It does not probe: the caller may
// pass the latest probe for reporting, but manual selection must never be
// blocked by a failed health check.
func SelectManual(node Node, probe Probe) Selection {
	reason := "manually selected"
	if probe.Status != "" && probe.Status != HealthUnknown {
		reason = fmt.Sprintf("%s; last health check: %s", reason, probe.Status)
	}
	return Selection{
		Node:       node,
		Method:     SelectionManual,
		SelectedAt: time.Now().UTC(),
		Reason:     reason,
		Probe:      probe,
		Note:       SelectionNote,
	}
}

func rankOf(status HealthStatus) int {
	switch status {
	case HealthHealthy:
		return 0
	case HealthDegraded:
		return 1
	default:
		return 2
	}
}

func candidateReason(probe Probe) string {
	parts := []string{string(probe.Status)}
	if latency, ok := probe.SelectionLatency(); ok {
		parts = append(parts, "median HTTP RTT "+FormatDuration(latency))
	} else if len(probe.LatencySamples) > 0 {
		parts = append(parts, "HTTP RTT below the platform clock resolution (N/A)")
	} else {
		parts = append(parts, "no latency sample (N/A)")
	}
	if probe.Attempts > 1 {
		parts = append(parts, fmt.Sprintf("needed %d attempts", probe.Attempts))
	}
	if probe.Capabilities.Supported && probe.Capabilities.ServerVersion != "" {
		parts = append(parts, "server "+probe.Capabilities.ServerVersion)
	}
	return strings.Join(parts, ", ")
}

// selectionReason explains the winning policy in plain language. It names the
// exact tie breaker that decided, so a latency tie is never presented as a
// measurable difference.
func selectionReason(candidates []rankedCandidate, total int) string {
	if len(candidates) == 0 {
		return "no candidate"
	}
	best := candidates[0]
	if best.probe.Status == HealthDegraded {
		detail := best.probe.Error
		if detail == "" {
			detail = "the health check did not confirm ok"
		}
		return fmt.Sprintf("no healthy node was available; selected the best degraded node (%s) among %d candidates: %s",
			best.node.ID, total, detail)
	}
	if bestLatency, ok := best.probe.SelectionLatency(); ok {
		if len(candidates) > 1 {
			if otherLatency, otherOK := candidates[1].probe.SelectionLatency(); otherOK && otherLatency == bestLatency {
				return fmt.Sprintf("healthy; tied on measured median HTTP RTT %s with another candidate; selected the lowest node ID (%s)",
					FormatDuration(bestLatency), best.node.ID)
			}
		}
		return fmt.Sprintf("healthy; lowest measured median HTTP RTT %s among %d candidates",
			FormatDuration(bestLatency), total)
	}
	if len(best.probe.LatencySamples) > 0 {
		return fmt.Sprintf("healthy; HTTP RTT was below the platform clock resolution (N/A), selected by node ID order among %d candidates", total)
	}
	return fmt.Sprintf("healthy; no latency sample was available, selected by node ID order among %d candidates", total)
}

// FormatDuration renders a latency for CLI and reason strings.
func FormatDuration(value time.Duration) string {
	return fmt.Sprintf("%.2f ms", float64(value)/float64(time.Millisecond))
}
